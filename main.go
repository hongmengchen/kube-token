package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

var version = "0.8.0"

func usage() {
	fmt.Fprintf(os.Stderr, `kube-token %s — 粘贴新 token 直接替换 kubeconfig 里的旧值

用法:
  kube-token                        打开 TUI
  kube-token list                   列出扫描到的 user（同 -list）
  kube-token refresh [--force]      免密从 Vault 换 token（到期前；可放 cron/launchd）
  kube-token logout                 忘掉本机保存的 Vault 登录
  kube-token skill                  把 agent skill 装到 ~/.claude/skills（旧版本会覆盖）
  kube-token upgrade [--check]      更新到最新版本
  kube-token version                打印版本

选项:
  -list                 列出扫描到的 user 后退出
  -set-token <token>    非交互模式：直接替换成这个 token
  -user <name>          配合 -set-token：指定 user
  -file <path>          配合 -set-token：指定 kubeconfig 文件
  -no-verify            替换后不验证集群连通性
  -install-kubectl      没有 kubectl 时先一键安装官方 kubectl

`, version)
}

func main() {
	// 子命令
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "upgrade", "update":
			os.Exit(runUpgrade(os.Args[2:]))
		case "version":
			fmt.Printf("kube-token %s\n", version)
			return
		case "list":
			entries, errs := scanEntries()
			printList(entries, errs)
			return
		case "refresh":
			os.Exit(runRefresh(os.Args[2:]))
		case "logout":
			os.Exit(runLogout())
		case "skill":
			os.Exit(runSkill(os.Args[2:]))
		case "help":
			usage()
			return
		}
	}

	var (
		listFlag = flag.Bool("list", false, "列出扫描到的 kubeconfig 条目后退出")
		setToken = flag.String("set-token", "", "非交互模式：直接替换成这个 token")
		userFlag = flag.String("user", "", "配合 -set-token：指定 user（默认取当前 context 的 user）")
		fileFlag = flag.String("file", "", "配合 -set-token：指定 kubeconfig 文件")
		noVerify = flag.Bool("no-verify", false, "替换后不验证集群连通性")
		installK = flag.Bool("install-kubectl", false, "没有 kubectl 时先安装官方 kubectl 再写入")
		verFlag  = flag.Bool("version", false, "打印版本后退出")
	)
	flag.Usage = usage
	flag.Parse()

	if *verFlag {
		fmt.Printf("kube-token %s\n", version)
		return
	}

	entries, scanErrs := scanEntries()

	// -file 指定的文件不一定在默认扫描范围里（比如临时文件、别的目录），显式读进来
	if *fileFlag != "" && *setToken != "" {
		path := expandHome(*fileFlag)
		es, err := loadEntries(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "错误: 读取 %s 失败: %v\n", path, err)
			os.Exit(1)
		}
		var rest []Entry
		for _, e := range entries {
			if !sameFile(e.File, path) {
				rest = append(rest, e)
			}
		}
		entries = append(rest, es...)
	}

	if *setToken != "" {
		os.Exit(runSet(*setToken, *userFlag, *fileFlag, entries, *noVerify, *installK))
	}

	if *listFlag {
		printList(entries, scanErrs)
		return
	}

	p := tea.NewProgram(newModel(entries, scanErrs), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "启动 TUI 失败: %v\n", err)
		os.Exit(1)
	}
}

// runSet 是给脚本/多设备场景用的非交互入口
func runSet(token, user, file string, entries []Entry, noVerify, wantKubectl bool) int {
	if strings.ContainsAny(token, " \t\r\n") {
		fmt.Fprintln(os.Stderr, "错误: token 里含有空白字符")
		return 1
	}

	match, err := pickEntry(entries, user, file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误: "+err.Error())
		return 1
	}

	kubectlPath, ok := findKubectl()
	if !ok {
		if wantKubectl {
			fmt.Println("没有找到 kubectl，正在安装官方 kubectl…")
			path, installErr := installKubectl("")
			if installErr != nil {
				fmt.Fprintf(os.Stderr, "安装 kubectl 失败: %v\n改用内置方式写入\n", installErr)
			} else {
				kubectlPath, ok = path, true
				fmt.Println("kubectl 已安装: " + shortenHome(path))
			}
		} else {
			fmt.Fprintln(os.Stderr, "提示: 没有找到 kubectl，改用内置方式改写 YAML（要装官方 kubectl 可加 -install-kubectl）")
		}
	}
	if ok {
		fmt.Println("写入方式: 官方 kubectl（" + shortenHome(kubectlPath) + "）")
	} else {
		fmt.Println("写入方式: 内置 YAML 改写")
	}

	backup, err := applyTokenWith(match, token, kubectlPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误: "+err.Error())
		return 1
	}
	fmt.Printf("已更新 %s / %s\n备份: %s\n", shortenHome(match.File), match.User, shortenHome(backup))

	if noVerify {
		return 0
	}
	detail, err := verifyClusterWith(match.File, kubectlPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "验证失败: %v\n如需回滚: cp %s %s\n", err, backup, match.File)
		return 2
	}
	fmt.Println("验证: " + detail)
	return 0
}

func pickEntry(entries []Entry, user, file string) (Entry, error) {
	var candidates []Entry
	for _, e := range entries {
		if user != "" && e.User != user {
			continue
		}
		if file != "" && !sameFile(e.File, file) {
			continue
		}
		candidates = append(candidates, e)
	}

	switch len(candidates) {
	case 0:
		return Entry{}, fmt.Errorf("没有匹配的 user（共扫描到 %d 个条目）", len(entries))
	case 1:
		return candidates[0], nil
	}

	for _, e := range candidates {
		if e.IsCurrent {
			return e, nil
		}
	}

	var b strings.Builder
	b.WriteString("匹配到多个 user，请用 -user 或 -file 指定：\n")
	for _, e := range candidates {
		fmt.Fprintf(&b, "  %s  %s\n", shortenHome(e.File), e.User)
	}
	return Entry{}, fmt.Errorf("%s", strings.TrimRight(b.String(), "\n"))
}

// autoTargetEntry 自动更新挑哪个 user：配置里的 target 优先，否则当前 context，再否则第一个。
// 没配置 target 时不会去更新一堆 user，只碰看起来正在用的那个。
func autoTargetEntry(entries []Entry, cfg *vaultConfig) (Entry, bool) {
	if len(entries) == 0 || cfg == nil {
		return Entry{}, false
	}
	if cfg.Target.User != "" || cfg.Target.File != "" {
		e, err := pickEntry(entries, cfg.Target.User, cfg.Target.File)
		if err != nil {
			return Entry{}, false
		}
		return e, true
	}
	for _, e := range entries {
		if e.IsCurrent {
			return e, true
		}
	}
	return entries[0], true
}

// autoRefreshWindow 提前换新的余量：快过期就换，不等真的过期
const autoRefreshWindow = 10 * time.Minute

// autoDue 判断这个条目该不该自动换 token。
// exec 插件自己会续期；不是 JWT 的判断不了到期时间，都不动。
func autoDue(e Entry, now time.Time) bool {
	switch {
	case e.HasExec:
		return false
	case !e.HasToken():
		return true
	case !e.HasExp:
		return false
	default:
		return !e.Expiry.After(now.Add(autoRefreshWindow))
	}
}

// runRefresh 是给 cron / launchd 用的免密入口：用本机保存的 Vault 登录换 token
func runRefresh(args []string) int {
	fs := flag.NewFlagSet("refresh", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var force bool
	fs.BoolVar(&force, "force", false, "还没到期也换新")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := loadVaultConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误: "+err.Error())
		return 1
	}
	if cfg == nil {
		fmt.Fprintln(os.Stderr, "还没有 Vault 配置：先运行 kube-token 打开 TUI，按 c 填写")
		return 1
	}

	entries, _ := scanEntries()
	e, ok := autoTargetEntry(entries, cfg)
	if !ok {
		fmt.Fprintln(os.Stderr, "没有扫描到可以更新的 user")
		return 1
	}
	if !force && !autoDue(e, time.Now()) {
		label, _, _ := expiryLabel(e)
		fmt.Printf("%s / %s 还不用更新（%s）\n", shortenHome(e.File), e.User, label)
		return 0
	}

	tok, err := vaultFetchTokenAuto(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误: "+err.Error())
		if errors.Is(err, errNoVaultSession) {
			fmt.Fprintln(os.Stderr, "提示: 先在 TUI 里按 v 输入一次 Vault 密码，之后免密；或设置 VAULT_TOKEN")
		}
		return 1
	}

	kubectlPath, _ := findKubectl()
	backup, err := applyTokenWith(e, tok, kubectlPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误: "+err.Error())
		return 1
	}
	fmt.Printf("已更新 %s / %s\n备份: %s\n", shortenHome(e.File), e.User, shortenHome(backup))

	verify, verr := verifyClusterWith(e.File, kubectlPath)
	if verr != nil {
		fmt.Fprintf(os.Stderr, "验证失败: %v\n如需回滚: cp %s %s\n", verr, backup, e.File)
		return 2
	}
	fmt.Println("验证: " + verify)
	return 0
}

func runLogout() int {
	path, err := vaultSessionPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误: "+err.Error())
		return 1
	}
	if err := clearVaultSession(); err != nil {
		fmt.Fprintln(os.Stderr, "错误: "+err.Error())
		return 1
	}
	fmt.Printf("已忘掉本机保存的 Vault 登录（%s）\n", shortenHome(path))
	fmt.Println("VAULT_TOKEN 环境变量如果还在，仍然会用它")
	return 0
}

func printList(entries []Entry, scanErrs []error) {
	if len(entries) == 0 {
		fmt.Println("没有发现可用的 kubeconfig")
	}
	for _, e := range entries {
		expLabel, _, expired := expiryLabel(e)
		mark := " "
		if e.IsCurrent {
			mark = "*"
		}
		exp := expLabel
		if expired {
			exp = styles.Bad.Render(expLabel)
		}
		fmt.Printf("%s %s\n  user  %s\n  token %s  ·  %s\n\n",
			mark, shortenHome(e.File), e.User, maskToken(e.Token), exp)
	}
	for _, err := range scanErrs {
		fmt.Fprintf(os.Stderr, "警告: %v\n", err)
	}
}

func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	absA, errA := filepath.Abs(expandHome(a))
	absB, errB := filepath.Abs(expandHome(b))
	return errA == nil && errB == nil && absA == absB
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if home, err := homeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

func homeDir() (string, error) { return os.UserHomeDir() }
