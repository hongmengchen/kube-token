package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func fakeVault(t *testing.T, wantPassword, wantNamespace string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/ldap/login/alice":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["password"] != wantPassword {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"errors":["invalid username or password"]}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"auth": map[string]any{"client_token": "hvs.test-token"},
			})

		case "/v1/creds/my-role":
			if r.Header.Get("X-Vault-Token") != "hvs.test-token" {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"errors":["permission denied"]}`)
				return
			}
			if wantNamespace != "" && r.Header.Get("X-Vault-Namespace") != wantNamespace {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"errors":["bad namespace header"]}`)
				return
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["kubernetes_namespace"] != "demo" {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"errors":["kubernetes_namespace required"]}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"service_account_token": "eyJhbGciOiJSUzI1NiJ9.eyJleHAiOjE5MDAwMDAwMDB9.sig"},
			})

		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":["no handler for path"]}`)
		}
	}))
}

func testVaultConfig(addr string) *vaultConfig {
	cfg := &vaultConfig{
		Addr: addr,
		Auth: vaultAuth{Method: "ldap", Path: "ldap", Username: "alice"},
		Secret: vaultSecret{
			Path:   "creds/my-role",
			Params: map[string]string{"kubernetes_namespace": "demo"},
		},
	}
	normalizeVaultConfig(cfg)
	return cfg
}

func TestVaultFetchToken(t *testing.T) {
	srv := fakeVault(t, "secret", "")
	defer srv.Close()

	tok, err := vaultFetchToken(testVaultConfig(srv.URL), "secret")
	if err != nil {
		t.Fatalf("vaultFetchToken: %v", err)
	}
	if tok != "eyJhbGciOiJSUzI1NiJ9.eyJleHAiOjE5MDAwMDAwMDB9.sig" {
		t.Fatalf("token = %q", tok)
	}
}

func TestVaultLoginRejected(t *testing.T) {
	srv := fakeVault(t, "right-password", "")
	defer srv.Close()

	_, err := vaultFetchToken(testVaultConfig(srv.URL), "wrong-password")
	if err == nil {
		t.Fatal("密码错误时应该返回错误")
	}
	if want := "invalid username or password"; !contains(err.Error(), want) {
		t.Fatalf("错误信息里应包含 %q，实际: %v", want, err)
	}
}

func TestVaultNamespaceHeader(t *testing.T) {
	srv := fakeVault(t, "secret", "team-a")
	defer srv.Close()

	cfg := testVaultConfig(srv.URL)
	cfg.Namespace = "team-a"

	if _, err := vaultFetchToken(cfg, "secret"); err != nil {
		t.Fatalf("带 namespace 的请求失败: %v", err)
	}
}

func TestVaultTokenMethod(t *testing.T) {
	srv := fakeVault(t, "", "")
	defer srv.Close()

	cfg := testVaultConfig(srv.URL)
	cfg.Auth.Method = "token"
	cfg.Auth.Path = "token"
	cfg.Auth.Username = ""

	// method=token：密码字段直接当作 client token
	if tok, err := vaultFetchToken(cfg, "hvs.test-token"); err != nil || tok == "" {
		t.Fatalf("token 模式失败: %v", err)
	}
}

func TestTokenFromVaultResponse(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want string
	}{
		{"kubernetes engine", map[string]any{"data": map[string]any{"service_account_token": "a"}}, "a"},
		{"kv v1", map[string]any{"data": map[string]any{"token": "b"}}, "b"},
		{"kv v2", map[string]any{"data": map[string]any{"data": map[string]any{"token": "c"}}}, "c"},
	}
	for _, c := range cases {
		got, err := tokenFromVaultResponse(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}

	if _, err := tokenFromVaultResponse(map[string]any{"data": map[string]any{"other": "x"}}); err == nil {
		t.Fatal("没有 token 字段时应该报错")
	}
}

func TestVaultConfigRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	cfg := &vaultConfig{
		Addr: "https://vault.example.com/",
		Auth: vaultAuth{Method: "ldap", Username: "alice"},
		Secret: vaultSecret{
			Path:   "/creds/role/",
			Params: map[string]string{"kubernetes_namespace": "ns1"},
		},
	}

	path, err := saveVaultConfig(cfg)
	if err != nil {
		t.Fatalf("saveVaultConfig: %v", err)
	}
	if filepath.Base(path) != "config.yaml" {
		t.Fatalf("配置文件名不对: %s", path)
	}

	loaded, err := loadVaultConfig()
	if err != nil || loaded == nil {
		t.Fatalf("loadVaultConfig: %v", err)
	}
	if loaded.Addr != "https://vault.example.com" {
		t.Fatalf("addr 归一化失败: %q", loaded.Addr)
	}
	if loaded.Auth.Path != "ldap" {
		t.Fatalf("auth.path 默认值应为 ldap，实际 %q", loaded.Auth.Path)
	}
	if loaded.Secret.Path != "creds/role" {
		t.Fatalf("secret.path 归一化失败: %q", loaded.Secret.Path)
	}
	if loaded.Secret.Params["kubernetes_namespace"] != "ns1" {
		t.Fatalf("params 丢了: %+v", loaded.Secret.Params)
	}
}

func TestParamsEncoding(t *testing.T) {
	in := "kubernetes_namespace=demo, ttl=1h"
	params := parseParams(in)
	if params["kubernetes_namespace"] != "demo" || params["ttl"] != "1h" {
		t.Fatalf("parseParams: %+v", params)
	}
	if got := encodeParams(params); got != "kubernetes_namespace=demo,ttl=1h" {
		t.Fatalf("encodeParams: %q", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
