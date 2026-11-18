package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// kubeConfig 只取我们关心的字段，其余原样忽略（写入仍交给 kubectl，避免破坏格式）
type kubeConfig struct {
	CurrentContext string `yaml:"current-context"`
	Clusters       []struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server string `yaml:"server"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
	Contexts []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster   string `yaml:"cluster"`
			User      string `yaml:"user"`
			Namespace string `yaml:"namespace"`
		} `yaml:"context"`
	} `yaml:"contexts"`
	Users []struct {
		Name string `yaml:"name"`
		User struct {
			Token     string         `yaml:"token"`
			TokenFile string         `yaml:"tokenFile"`
			Exec      map[string]any `yaml:"exec"`
		} `yaml:"user"`
	} `yaml:"users"`
}

// Entry 是 TUI 里的一行：某个 kubeconfig 文件里的一个 user
type Entry struct {
	File      string
	User      string
	Contexts  []string
	Cluster   string
	Server    string
	Namespace string
	Token     string
	HasExec   bool
	IsCurrent bool

	Expiry  time.Time
	HasExp  bool
	Expired bool
}

func (e Entry) HasToken() bool { return e.Token != "" }

// kubeconfigPaths 收集候选文件：$KUBECONFIG + ~/.kube/config + ~/.kube/*.{yaml,yml,conf}
func kubeconfigPaths() []string {
	var paths []string
	if env := os.Getenv("KUBECONFIG"); strings.TrimSpace(env) != "" {
		paths = append(paths, filepath.SplitList(env)...)
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".kube", "config"))
		for _, pat := range []string{"*.yaml", "*.yml", "*.conf"} {
			if m, err := filepath.Glob(filepath.Join(home, ".kube", pat)); err == nil {
				paths = append(paths, m...)
			}
		}
	}

	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		if st, err := os.Stat(abs); err == nil && !st.IsDir() {
			out = append(out, abs)
		}
	}

	// ~/.kube/config 排最前面，其余按字母序
	home, _ := os.UserHomeDir()
	defaultCfg := filepath.Join(home, ".kube", "config")
	sort.SliceStable(out, func(i, j int) bool {
		if out[i] == defaultCfg {
			return true
		}
		if out[j] == defaultCfg {
			return false
		}
		return out[i] < out[j]
	})
	return out
}

// loadEntries 解析单个 kubeconfig，产出其所有 user
func loadEntries(path string) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var kc kubeConfig
	if err := yaml.Unmarshal(data, &kc); err != nil {
		return nil, fmt.Errorf("解析 YAML 失败: %w", err)
	}

	servers := map[string]string{}
	for _, c := range kc.Clusters {
		servers[c.Name] = c.Cluster.Server
	}

	var out []Entry
	for _, u := range kc.Users {
		e := Entry{
			File:    path,
			User:    u.Name,
			Token:   u.User.Token,
			HasExec: len(u.User.Exec) > 0,
		}
		for _, c := range kc.Contexts {
			if c.Context.User != u.Name {
				continue
			}
			e.Contexts = append(e.Contexts, c.Name)
			if c.Name == kc.CurrentContext {
				e.IsCurrent = true
				e.Cluster = c.Context.Cluster
				e.Namespace = c.Context.Namespace
			}
			if e.Cluster == "" {
				e.Cluster = c.Context.Cluster
				e.Namespace = c.Context.Namespace
			}
		}
		e.Server = servers[e.Cluster]
		if e.Token != "" {
			if exp, ok := tokenExpiry(e.Token); ok {
				e.Expiry = exp
				e.HasExp = true
				e.Expired = time.Now().After(exp)
			}
		}
		out = append(out, e)
	}
	return out, nil
}

// scanEntries 扫描全部 kubeconfig，返回条目和解析失败的提示
func scanEntries() ([]Entry, []error) {
	var entries []Entry
	var errs []error
	for _, p := range kubeconfigPaths() {
		es, err := loadEntries(p)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
			continue
		}
		entries = append(entries, es...)
	}
	return entries, errs
}

// applyToken 备份后写入新 token，并从文件回读校验
func applyToken(e Entry, token string) (backup string, err error) {
	if _, err := exec.LookPath("kubectl"); err != nil {
		return "", errors.New("找不到 kubectl，请先安装并加入 PATH")
	}

	backup = fmt.Sprintf("%s.bak.%s", e.File, time.Now().Format("20060102-150405"))
	if err := copyFile(e.File, backup); err != nil {
		return "", fmt.Errorf("备份失败: %w", err)
	}

	cmd := exec.Command("kubectl", "--kubeconfig", e.File, "config", "set-credentials", e.User, "--token="+token)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(backup)
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("kubectl 写入失败: %s", msg)
	}

	entries, err := loadEntries(e.File)
	if err != nil {
		return backup, fmt.Errorf("写入后回读失败: %w", err)
	}
	for _, x := range entries {
		if x.User == e.User {
			if x.Token != token {
				return backup, errors.New("回读校验不一致，文件可能没写对")
			}
			return backup, nil
		}
	}
	return backup, fmt.Errorf("写入后找不到 user %q", e.User)
}

// verifyCluster 用 /version 探测认证是否生效（任何已认证身份都放行）
func verifyCluster(file string) (string, error) {
	cmd := exec.Command("kubectl", "--kubeconfig", file, "get", "--raw", "/version", "--request-timeout=15s")
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if strings.Contains(text, "Forbidden") {
			return "认证通过（身份权限受限，属正常）", nil
		}
		msg := firstLine(text)
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	var v struct {
		Major string `json:"major"`
		Minor string `json:"minor"`
	}
	if json.Unmarshal(out, &v) == nil && v.Major != "" {
		return fmt.Sprintf("认证通过 · 集群 %s.%s", v.Major, strings.TrimRight(v.Minor, "+")), nil
	}
	return "认证通过", nil
}

// tokenExpiry 解析 JWT 的 exp；不是 JWT 就返回 false
func tokenExpiry(tok string) (time.Time, bool) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(dst, 0o600)
}

// firstLine 取第一条有信息量的输出，跳过 klog 的 E1118 11:52:01 这类噪声行
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || isKlogLine(line) {
			continue
		}
		return line
	}
	return strings.TrimSpace(s)
}

func isKlogLine(s string) bool {
	if len(s) < 6 || s[0] != 'E' {
		return false
	}
	for _, c := range s[1:5] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// maskToken 只露头尾，避免整串 token 出现在屏幕/日志里
func maskToken(t string) string {
	if t == "" {
		return "(无)"
	}
	if len(t) <= 16 {
		return t[:min(4, len(t))] + "****"
	}
	return t[:8] + "…" + t[len(t)-4:]
}

func shortDur(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

// expiryLabel 返回 (文案, 是否有效, 是否已过期)
func expiryLabel(e Entry) (string, bool, bool) {
	if !e.HasExp {
		return "非 JWT，无法判断", true, false
	}
	d := time.Until(e.Expiry)
	if d <= 0 {
		return "已过期 " + shortDur(-d), false, true
	}
	return "剩 " + shortDur(d), true, false
}
