package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const k8sStableURL = "https://dl.k8s.io/release/stable.txt"

// findKubectl 找 PATH 里的官方 kubectl
func findKubectl() (string, bool) {
	p, err := exec.LookPath("kubectl")
	if err != nil || p == "" {
		return "", false
	}
	return p, true
}

func kubectlBinaryName() string {
	if runtime.GOOS == "windows" {
		return "kubectl.exe"
	}
	return "kubectl"
}

// kubectlInstallDir 默认装在 kube-token 自己所在的目录，这个目录本来就在 PATH 里
func kubectlInstallDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe), nil
}

// installKubectl 下载官方 kubectl（dl.k8s.io，校验 sha256）到 dir；dir 为空时用 kube-token 所在目录
func installKubectl(dir string) (string, error) {
	return installKubectlWith(dir, nil, nil)
}

// installKubectlWith 和 installKubectl 一样，但把版本号和下载进度回调出来（TUI 用）
func installKubectlWith(dir string, onVersion func(string), onProgress func(done, total int64)) (string, error) {
	if dir == "" {
		d, err := kubectlInstallDir()
		if err != nil {
			return "", fmt.Errorf("找不到安装目录: %w", err)
		}
		dir = d
	}

	f, err := os.CreateTemp(dir, ".kube-token-probe-*")
	if err != nil {
		return "", fmt.Errorf("没有写入权限（%s）: %w", dir, err)
	}
	probe := f.Name()
	f.Close()
	_ = os.Remove(probe)

	stable, err := fetchText(k8sStableURL)
	if err != nil {
		return "", fmt.Errorf("查询 kubectl 最新版本失败: %w", err)
	}
	if !strings.HasPrefix(stable, "v") {
		return "", fmt.Errorf("版本号看起来不对: %q", stable)
	}
	if onVersion != nil {
		onVersion(stable)
	}

	arch, err := kubectlArch(runtime.GOARCH)
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf("https://dl.k8s.io/release/%s/bin/%s/%s/%s",
		stable, runtime.GOOS, arch, kubectlBinaryName())

	wantSum, err := fetchText(url + ".sha256")
	if err != nil {
		return "", fmt.Errorf("下载校验和失败: %w", err)
	}

	tmp, err := downloadBinary(url, dir, onProgress)
	if err != nil {
		return "", err
	}

	gotSum, err := fileSHA256(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if !strings.EqualFold(gotSum, strings.TrimSpace(wantSum)) {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("校验和不一致（期望 %s，实际 %s）", strings.TrimSpace(wantSum), gotSum)
	}

	target := filepath.Join(dir, kubectlBinaryName())
	if err := installBinary(tmp, target); err != nil {
		return "", err
	}

	// 确认真的能跑（Windows 上首次执行会慢一点）
	cmd := exec.Command(target, "version", "--client")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("装好了但执行失败: %v（%s）", err, firstLine(string(out)))
	}
	return target, nil
}

func kubectlArch(goarch string) (string, error) {
	switch goarch {
	case "amd64", "arm64", "386":
		return goarch, nil
	case "arm":
		return "arm", nil
	}
	return "", fmt.Errorf("dl.k8s.io 没有 %s 架构的 kubectl", goarch)
}

func fetchText(url string) (string, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s 返回 %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
