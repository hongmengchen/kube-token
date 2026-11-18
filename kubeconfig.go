package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// kubeConfig 只取我们关心的字段，其余原样忽略（写入用 kubectl 或原生 YAML 节点，避免破坏格式）
type kubeConfig struct {
	CurrentContext string `yaml:"current-context"`
	Clusters       []struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server                   string `yaml:"server"`
			CertificateAuthority     string `yaml:"certificate-authority"`
			CertificateAuthorityData string `yaml:"certificate-authority-data"`
			InsecureSkipTLSVerify    bool   `yaml:"insecure-skip-tls-verify"`
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

// applyToken 备份后写入新 token，并从文件回读校验。
// 有 kubectl 就用它（兼容各种边角写法），没有就原生改 YAML，Windows 上不用额外装东西。
func applyToken(e Entry, token string) (backup string, err error) {
	backup = fmt.Sprintf("%s.bak.%s", e.File, time.Now().Format("20060102-150405"))
	if err := copyFile(e.File, backup); err != nil {
		return "", fmt.Errorf("备份失败: %w", err)
	}

	if _, lookErr := exec.LookPath("kubectl"); lookErr == nil {
		cmd := exec.Command("kubectl", "--kubeconfig", e.File, "config", "set-credentials", e.User, "--token="+token)
		if out, cmdErr := cmd.CombinedOutput(); cmdErr != nil {
			_ = os.Remove(backup)
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = cmdErr.Error()
			}
			return "", fmt.Errorf("kubectl 写入失败: %s", msg)
		}
	} else if err := applyTokenNative(e.File, e.User, token); err != nil {
		_ = os.Remove(backup)
		return "", err
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

// applyTokenNative 直接改 YAML 节点，保留里面的注释、键顺序和其它字段
func applyTokenNative(path, user, token string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("解析 YAML 失败: %w", err)
	}

	root := rootMapping(&doc)
	if root == nil {
		return errors.New("kubeconfig 结构不认识（根部不是 mapping）")
	}
	users := mapValue(root, "users")
	if users == nil || users.Kind != yaml.SequenceNode {
		return errors.New("文件里没有 users 列表")
	}

	var target *yaml.Node
	for _, item := range users.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		if name := mapValue(item, "name"); name != nil && name.Value == user {
			target = item
			break
		}
	}
	if target == nil {
		return fmt.Errorf("文件里没有 user %q", user)
	}

	userNode := mapValue(target, "user")
	if userNode == nil {
		userNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		target.Content = append(target.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "user"}, userNode)
	}
	if userNode.Kind != yaml.MappingNode {
		return fmt.Errorf("user %q 的配置不是 mapping", user)
	}
	setScalar(userNode, "token", token)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("生成 YAML 失败: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("生成 YAML 失败: %w", err)
	}
	if err := writeFileAtomic(path, buf.Bytes()); err != nil {
		return fmt.Errorf("写入失败: %w", err)
	}
	return nil
}

func rootMapping(doc *yaml.Node) *yaml.Node {
	n := doc
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	return n
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func setScalar(m *yaml.Node, key, val string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			v := m.Content[i+1]
			v.Kind = yaml.ScalarNode
			v.Tag = "!!str"
			v.Value = val
			v.Style = 0
			v.Content = nil
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: val})
}

// writeFileAtomic 同目录写临时文件再改名，保留原文件权限
func writeFileAtomic(path string, data []byte) error {
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".kube-token-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// verifyCluster 用 /version 探测认证是否生效（任何已认证身份都放行）
func verifyCluster(file string) (string, error) {
	if _, err := exec.LookPath("kubectl"); err != nil {
		return verifyClusterNative(file)
	}
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

// verifyClusterNative 不依赖 kubectl：用 current-context 的 token 直接请求 /version
func verifyClusterNative(file string) (string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	var kc kubeConfig
	if err := yaml.Unmarshal(data, &kc); err != nil {
		return "", fmt.Errorf("解析 YAML 失败: %w", err)
	}

	userName, clusterName := "", ""
	for _, c := range kc.Contexts {
		if c.Name == kc.CurrentContext {
			userName, clusterName = c.Context.User, c.Context.Cluster
			break
		}
	}
	if userName == "" && len(kc.Users) > 0 {
		userName = kc.Users[0].Name
	}
	if clusterName == "" && len(kc.Clusters) > 0 {
		clusterName = kc.Clusters[0].Name
	}

	token := ""
	for _, u := range kc.Users {
		if u.Name == userName {
			token = u.User.Token
			break
		}
	}
	if token == "" {
		return "", errors.New("文件里没有可用 token")
	}

	var server, caFile, caData string
	insecure := false
	for _, c := range kc.Clusters {
		if c.Name == clusterName {
			server = c.Cluster.Server
			caFile = c.Cluster.CertificateAuthority
			caData = c.Cluster.CertificateAuthorityData
			insecure = c.Cluster.InsecureSkipTLSVerify
			break
		}
	}
	if server == "" {
		return "", errors.New("kubeconfig 里没有集群地址")
	}

	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	switch {
	case insecure:
		tlsCfg.InsecureSkipVerify = true
	case caData != "":
		pool, err := certPool([]byte(caData), true)
		if err != nil {
			return "", err
		}
		tlsCfg.RootCAs = pool
	case caFile != "":
		p := expandHome(caFile)
		if !filepath.IsAbs(p) {
			p = filepath.Join(filepath.Dir(file), p)
		}
		pem, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("读取 CA 证书失败: %w", err)
		}
		pool, err := certPool(pem, false)
		if err != nil {
			return "", err
		}
		tlsCfg.RootCAs = pool
	}

	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsCfg,
			Proxy:           http.ProxyFromEnvironment,
		},
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(server, "/")+"/version", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New(firstLine(err.Error()))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch {
	case resp.StatusCode == http.StatusOK:
		var v struct {
			Major string `json:"major"`
			Minor string `json:"minor"`
		}
		if json.Unmarshal(body, &v) == nil && v.Major != "" {
			return fmt.Sprintf("认证通过 · 集群 %s.%s", v.Major, strings.TrimRight(v.Minor, "+")), nil
		}
		return "认证通过", nil
	case resp.StatusCode == http.StatusForbidden:
		return "认证通过（身份权限受限，属正常）", nil
	case resp.StatusCode == http.StatusUnauthorized:
		return "", fmt.Errorf("token 未被集群接受（401）：%s", statusMessage(body))
	default:
		return "", fmt.Errorf("集群返回 %d：%s", resp.StatusCode, statusMessage(body))
	}
}

func certPool(pem []byte, base64Encoded bool) (*x509.CertPool, error) {
	if base64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(pem)))
		if err != nil {
			return nil, fmt.Errorf("CA 证书解码失败: %w", err)
		}
		pem = decoded
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("CA 证书解析失败")
	}
	return pool, nil
}

// statusMessage 取 k8s Status 里的 message，取不到就截一段原文
func statusMessage(body []byte) string {
	var st struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &st) == nil && st.Message != "" {
		return st.Message
	}
	msg := firstLine(string(body))
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	if msg == "" {
		return "(空响应)"
	}
	return msg
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
