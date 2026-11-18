package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const repoSlug = "hongmengchen/kube-token"

type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type releaseInfo struct {
	TagName string         `json:"tag_name"`
	HTMLURL string         `json:"html_url"`
	Assets  []releaseAsset `json:"assets"`
}

// runUpgrade 实现 `kube-token upgrade`
func runUpgrade(args []string) int {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	check := fs.Bool("check", false, "只检查是否有新版本，不下载")
	force := fs.Bool("force", false, "版本相同也重新安装")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	// 管道/脚本/CI 里没有终端，退回纯文本，别往日志里塞转义序列
	if *check || !isInteractive() {
		return runUpgradePlain(*force, *check)
	}
	return runUpgradeTUI(*force)
}

// isInteractive 判断 stdout 是不是真的终端
func isInteractive() bool {
	st, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

// runUpgradePlain 是给脚本用的老路径：一行一步，没有动画
func runUpgradePlain(force, check bool) int {
	fmt.Printf("当前版本: %s\n", version)

	rel, err := fetchLatestRelease()
	if err != nil {
		fmt.Fprintf(os.Stderr, "检查更新失败: %v\n", err)
		fmt.Fprintf(os.Stderr, "可以手动安装: curl -fsSL https://raw.githubusercontent.com/%s/main/install.sh | sh\n", repoSlug)
		return 1
	}

	latest := strings.TrimPrefix(rel.TagName, "v")
	fmt.Printf("最新版本: %s\n", latest)

	if !force && !isNewer(latest, version) {
		fmt.Println("已是最新版本。")
		return 0
	}
	if check {
		fmt.Printf("有新版本可用: %s，运行 kube-token upgrade 更新\n", rel.TagName)
		return 0
	}

	asset := assetName(runtime.GOOS, runtime.GOARCH)
	url := ""
	for _, a := range rel.Assets {
		if a.Name == asset {
			url = a.URL
			break
		}
	}
	if url == "" {
		fmt.Fprintf(os.Stderr, "这个平台没有对应的二进制（%s），请到 %s 手动下载\n", asset, rel.HTMLURL)
		return 1
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "找不到当前可执行文件: %v\n", err)
		return 1
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	fmt.Printf("下载: %s\n", url)
	if err := replaceBinary(exe, url); err != nil {
		fmt.Fprintf(os.Stderr, "更新失败: %v\n", err)
		return 1
	}

	fmt.Printf("已更新到 %s（%s）\n", rel.TagName, exe)
	// 装过 agent skill 的话，让新二进制把它刷成配套版本
	refreshSkillAfterUpgrade(exe)
	if runtime.GOOS == "windows" {
		fmt.Println("新开的终端会使用新版本；当前窗口里的命令需要重开或重新安装。")
	}
	return 0
}

// fetchLatestRelease 先走 releases/latest 的跳转（不吃 API 限流），失败再回落到 API
func fetchLatestRelease() (*releaseInfo, error) {
	if rel, err := fetchLatestViaRedirect(); err == nil {
		return rel, nil
	}
	return fetchLatestViaAPI()
}

func fetchLatestViaRedirect() (*releaseInfo, error) {
	client := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // 只要 Location，不跟过去
		},
	}
	req, err := http.NewRequest(http.MethodGet, "https://github.com/"+repoSlug+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "kube-token/"+version)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusMovedPermanently {
		return nil, fmt.Errorf("releases/latest 返回 %s", resp.Status)
	}
	loc, err := resp.Location()
	if err != nil {
		return nil, err
	}
	tag := strings.TrimSpace(path.Base(strings.TrimRight(loc.Path, "/")))
	if !strings.HasPrefix(tag, "v") {
		return nil, fmt.Errorf("没能从跳转里解析出版本号: %s", loc.String())
	}

	asset := assetName(runtime.GOOS, runtime.GOARCH)
	return &releaseInfo{
		TagName: tag,
		HTMLURL: "https://github.com/" + repoSlug + "/releases/tag/" + tag,
		Assets: []releaseAsset{{
			Name: asset,
			URL:  "https://github.com/" + repoSlug + "/releases/download/" + tag + "/" + asset,
		}},
	}, nil
}

func fetchLatestViaAPI() (*releaseInfo, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/"+repoSlug+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "kube-token/"+version)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden, http.StatusTooManyRequests:
		return nil, errors.New("GitHub API 限流了，稍后再试")
	default:
		return nil, fmt.Errorf("GitHub 返回 %s", resp.Status)
	}

	var rel releaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	if rel.TagName == "" {
		return nil, errors.New("没有解析到版本号")
	}
	return &rel, nil
}

func assetName(goos, goarch string) string {
	name := fmt.Sprintf("kube-token_%s_%s", goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// replaceBinary 下载到同目录的临时文件再原子替换，避免半截文件
func replaceBinary(exe, url string) error {
	tmp, err := downloadBinary(url, filepath.Dir(exe), nil)
	if err != nil {
		return err
	}
	return installBinary(tmp, exe)
}

// downloadBinary 下载到 dir 下的临时文件；onProgress 按写入量回调（可为 nil）
func downloadBinary(url, dir string, onProgress func(done, total int64)) (string, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载失败: %s", resp.Status)
	}

	tmp, err := os.CreateTemp(dir, ".kube-token-update-*")
	if err != nil {
		return "", fmt.Errorf("无法在 %s 写入（试试 sudo kube-token upgrade）: %w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	total := resp.ContentLength
	reader := io.Reader(resp.Body)
	if onProgress != nil {
		reader = &progressReader{r: resp.Body, total: total, onProgress: onProgress}
	}

	n, err := io.Copy(tmp, reader)
	closeErr := tmp.Close()
	if err != nil {
		cleanup()
		return "", fmt.Errorf("下载中断: %w", err)
	}
	if closeErr != nil {
		cleanup()
		return "", closeErr
	}
	if n < 100000 {
		cleanup()
		return "", fmt.Errorf("下载的文件只有 %d 字节，可能不是二进制", n)
	}
	return tmpName, nil
}

// installBinary 把临时文件原子换到正式位置
func installBinary(tmpName, exe string) error {
	cleanup := func() { _ = os.Remove(tmpName) }

	if err := os.Chmod(tmpName, 0o755); err != nil {
		cleanup()
		return err
	}

	if err := os.Rename(tmpName, exe); err != nil {
		// Windows 上正在运行的 exe 不能直接覆盖：先挪开旧的
		old := exe + ".old"
		_ = os.Remove(old)
		if renameErr := os.Rename(exe, old); renameErr == nil {
			if err2 := os.Rename(tmpName, exe); err2 == nil {
				_ = os.Remove(old)
				return nil
			}
			_ = os.Rename(old, exe) // 回滚
		}
		cleanup()
		return fmt.Errorf("无法覆盖 %s（权限不足？可试 sudo kube-token upgrade，或用安装脚本重装）: %w", exe, err)
	}
	return nil
}

// progressReader 在拷贝过程中回调已下载字节数
type progressReader struct {
	r          io.Reader
	total      int64
	done       int64
	onProgress func(done, total int64)
	last       time.Time
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	now := time.Now()
	if err == nil && now.Sub(p.last) > 80*time.Millisecond {
		p.last = now
		p.onProgress(p.done, p.total)
	}
	return n, err
}

// isNewer 比较点分版本号：isNewer("0.2.0", "0.1.9") == true
func isNewer(a, b string) bool {
	pa := strings.Split(strings.TrimPrefix(a, "v"), ".")
	pb := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var na, nb int
		if i < len(pa) {
			na, _ = strconv.Atoi(strings.TrimSpace(pa[i]))
		}
		if i < len(pb) {
			nb, _ = strconv.Atoi(strings.TrimSpace(pb[i]))
		}
		if na != nb {
			return na > nb
		}
	}
	return false
}
