package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func makeJWT(t *testing.T, exp int64) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload, err := json.Marshal(map[string]any{"exp": exp, "iss": "test"})
	if err != nil {
		t.Fatal(err)
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return header + "." + body + ".signature"
}

func TestTokenExpiry(t *testing.T) {
	exp := time.Now().Add(10 * time.Minute).Unix()
	tok := makeJWT(t, exp)

	got, ok := tokenExpiry(tok)
	if !ok {
		t.Fatal("期望解析出 exp，实际失败")
	}
	if got.Unix() != exp {
		t.Fatalf("exp = %d, 期望 %d", got.Unix(), exp)
	}

	if _, ok := tokenExpiry("not-a-jwt"); ok {
		t.Fatal("非 JWT 不应解析出过期时间")
	}
}

func TestMaskToken(t *testing.T) {
	if got := maskToken(""); got != "(无)" {
		t.Fatalf("空 token 掩码 = %q", got)
	}
	if got := maskToken("short"); got != "shor****" {
		t.Fatalf("短 token 掩码 = %q", got)
	}
	long := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.payload.signature"
	got := maskToken(long)
	if strings.Contains(got, "payload") {
		t.Fatalf("掩码泄露了中段: %q", got)
	}
	if !strings.HasPrefix(got, long[:8]) || !strings.HasSuffix(got, long[len(long)-4:]) {
		t.Fatalf("掩码应保留头尾: %q", got)
	}
}

const sampleConfig = `apiVersion: v1
kind: Config
current-context: ctx-a
clusters:
- name: cluster-a
  cluster:
    server: https://example.test
contexts:
- name: ctx-a
  context:
    cluster: cluster-a
    user: user-a
- name: ctx-b
  context:
    cluster: cluster-a
    user: user-b
users:
- name: user-a
  user:
    token: old-token-aaaaaaaaaaaaaaaa
- name: user-b
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: aws
`

func TestLoadEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(sampleConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := loadEntries(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("期望 2 个 user，实际 %d", len(entries))
	}

	a, b := entries[0], entries[1]
	if !a.IsCurrent || a.Cluster != "cluster-a" || a.Server != "https://example.test" {
		t.Fatalf("user-a 解析不对: %+v", a)
	}
	if a.Token != "old-token-aaaaaaaaaaaaaaaa" {
		t.Fatalf("user-a token = %q", a.Token)
	}
	if !b.HasExec || b.HasToken() {
		t.Fatalf("user-b 应识别为 exec 凭证: %+v", b)
	}
	if len(a.Contexts) != 1 || a.Contexts[0] != "ctx-a" {
		t.Fatalf("user-a contexts = %v", a.Contexts)
	}
}

func TestApplyToken(t *testing.T) {
	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skip("没有 kubectl，跳过")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(sampleConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := loadEntries(path)
	if err != nil {
		t.Fatal(err)
	}

	const newToken = "new-token-bbbbbbbbbbbbbbbb"
	backup, err := applyToken(entries[0], newToken)
	if err != nil {
		t.Fatalf("applyToken 失败: %v", err)
	}

	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("备份文件不存在: %v", err)
	}

	after, err := loadEntries(path)
	if err != nil {
		t.Fatal(err)
	}
	if after[0].Token != newToken {
		t.Fatalf("写入后 token = %q", after[0].Token)
	}
	if after[1].HasExec != true {
		t.Fatal("写入不应影响其它 user")
	}
}

// 模拟 Windows 上没有 kubectl：PATH 里找不到它，写入走原生 YAML
func TestApplyTokenWithoutKubectl(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	withComment := "# 我的集群，手写的注释要保住\n" + sampleConfig
	if err := os.WriteFile(path, []byte(withComment), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := loadEntries(path)
	if err != nil {
		t.Fatal(err)
	}

	const newToken = "new-native-token-cccccccc"
	backup, err := applyToken(entries[0], newToken)
	if err != nil {
		t.Fatalf("没有 kubectl 时写入失败: %v", err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("备份文件不存在: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "# 我的集群，手写的注释要保住") {
		t.Fatalf("注释丢了:\n%s", text)
	}
	if strings.Contains(text, "old-token-aaaaaaaaaaaaaaaa") {
		t.Fatalf("旧 token 还在:\n%s", text)
	}
	if !strings.Contains(text, newToken) {
		t.Fatalf("新 token 没写进去:\n%s", text)
	}

	after, err := loadEntries(path)
	if err != nil {
		t.Fatalf("改完后解析失败: %v", err)
	}
	if after[0].Token != newToken {
		t.Fatalf("回读 token = %q", after[0].Token)
	}
	if !after[1].HasExec {
		t.Fatal("exec user 被改坏了")
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("文件权限变了: %v", st.Mode().Perm())
	}
}

// exec user 本来没有 token 字段，原生写入要能加进去且不动 exec 配置
func TestApplyTokenNativeAddsToken(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(sampleConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := loadEntries(path)
	if err != nil {
		t.Fatal(err)
	}

	const newToken = "added-token-dddddddddd"
	if _, err := applyToken(entries[1], newToken); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	after, err := loadEntries(path)
	if err != nil {
		t.Fatal(err)
	}
	if after[1].Token != newToken {
		t.Fatalf("token = %q", after[1].Token)
	}
	if !after[1].HasExec {
		t.Fatal("exec 配置被删掉了")
	}
	if after[0].Token != "old-token-aaaaaaaaaaaaaaaa" {
		t.Fatal("改 user-b 时动了 user-a")
	}
}

func TestVerifyClusterNative(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-token" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"kind":"Status","message":"Unauthorized"}`))
			return
		}
		w.Write([]byte(`{"major":"1","minor":"29+"}`))
	}))
	defer srv.Close()

	write := func(token string) string {
		path := filepath.Join(t.TempDir(), "config")
		cfg := fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: ctx
clusters:
- name: cl
  cluster:
    server: %s
    insecure-skip-tls-verify: true
contexts:
- name: ctx
  context:
    cluster: cl
    user: u
users:
- name: u
  user:
    token: %s
`, srv.URL, token)
		if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	got, err := verifyCluster(write("good-token"))
	if err != nil {
		t.Fatalf("正常 token 验证失败: %v", err)
	}
	if !strings.Contains(got, "1.29") {
		t.Fatalf("验证结果 = %q", got)
	}

	if _, err := verifyCluster(write("bad-token")); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("错误 token 应该报 401，实际: %v", err)
	}
}

func TestVerifyClusterNativeForbidden(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"kind":"Status","message":"forbidden"}`))
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "config")
	cfg := fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: ctx
clusters:
- name: cl
  cluster:
    server: %s
    insecure-skip-tls-verify: true
contexts:
- name: ctx
  context:
    cluster: cl
    user: u
users:
- name: u
  user:
    token: tok
`, srv.URL)
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := verifyCluster(path)
	if err != nil {
		t.Fatalf("403 应视为认证通过: %v", err)
	}
	if !strings.Contains(got, "权限受限") {
		t.Fatalf("结果 = %q", got)
	}
}

func TestIsNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.2.0", "0.1.9", true},
		{"0.1.0", "0.1.0", false},
		{"v0.1.0", "0.0.9", true},
		{"0.1.10", "0.1.9", true},
		{"1.0.0", "0.9.9", true},
		{"0.9.9", "1.0.0", false},
	}
	for _, c := range cases {
		if got := isNewer(c.a, c.b); got != c.want {
			t.Errorf("isNewer(%q, %q) = %v, 期望 %v", c.a, c.b, got, c.want)
		}
	}
}
