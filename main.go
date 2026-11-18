package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var version = "0.3.0"

func usage() {
	fmt.Fprintf(os.Stderr, `kube-token %s — 粘贴新 token 直接替换 kubeconfig 里的旧值

用法:
  kube-token                        打开 TUI
  kube-token list                   列出扫描到的 user（同 -list）
  kube-token upgrade [--check]      更新到最新版本
  kube-token version                打印版本

选项:
  -list                 列出扫描到的 user 后退出
  -set-token <token>    非交互模式：直接替换成这个 token
  -user <name>          配合 -set-token：指定 user
  -file <path>          配合 -set-token：指定 kubeconfig 文件
  -no-verify            替换后不验证集群连通性

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
		verFlag  = flag.Bool("version", false, "打印版本后退出")
	)
	flag.Usage = usage
	flag.Parse()

	if *verFlag {
		fmt.Printf("kube-token %s\n", version)
		return
	}

	entries, scanErrs := scanEntries()

	if *setToken != "" {
		os.Exit(runSet(*setToken, *userFlag, *fileFlag, entries, *noVerify))
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
func runSet(token, user, file string, entries []Entry, noVerify bool) int {
	if strings.ContainsAny(token, " \t\r\n") {
		fmt.Fprintln(os.Stderr, "错误: token 里含有空白字符")
		return 1
	}

	match, err := pickEntry(entries, user, file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误: "+err.Error())
		return 1
	}

	backup, err := applyToken(match, token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误: "+err.Error())
		return 1
	}
	fmt.Printf("已更新 %s / %s\n备份: %s\n", shortenHome(match.File), match.User, shortenHome(backup))

	if noVerify {
		return 0
	}
	detail, err := verifyCluster(match.File)
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
			exp = lipgloss.NewStyle().Foreground(cBad).Render(expLabel)
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
