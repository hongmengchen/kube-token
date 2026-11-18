package main

import (
	"encoding/base64"
	"encoding/json"
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
