package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// vaultConfig 只存在本地（不随仓库分发），里面是公司内部的地址/路径/角色
type vaultConfig struct {
	Addr      string      `yaml:"addr"`
	Namespace string      `yaml:"namespace,omitempty"`
	Auth      vaultAuth   `yaml:"auth"`
	Secret    vaultSecret `yaml:"secret"`
	Target    vaultTarget `yaml:"target,omitempty"`
	TLS       vaultTLS    `yaml:"tls,omitempty"`
	// AutoUpdate 打开后，token 快过期时自动从 Vault 换新（TUI 里按 a 切换）
	AutoUpdate bool `yaml:"auto_update,omitempty"`
}

type vaultAuth struct {
	Method   string `yaml:"method"` // ldap / userpass / token
	Path     string `yaml:"path"`   // 认证挂载点，默认 ldap
	Username string `yaml:"username"`
}

type vaultSecret struct {
	Path   string            `yaml:"path"`
	Params map[string]string `yaml:"params,omitempty"`
}

type vaultTarget struct {
	File string `yaml:"file,omitempty"`
	User string `yaml:"user,omitempty"`
}

type vaultTLS struct {
	CACert   string `yaml:"ca_cert,omitempty"`
	Insecure bool   `yaml:"insecure,omitempty"`
}

// userConfigDir 留成变量，测试里可以指向临时目录
var userConfigDir = os.UserConfigDir

func vaultConfigPath() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "kube-token", "config.yaml"), nil
}

// loadVaultConfig 没有配置文件时返回 (nil, nil)
func loadVaultConfig() (*vaultConfig, error) {
	path, err := vaultConfigPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var cfg vaultConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}
	normalizeVaultConfig(&cfg)
	return &cfg, nil
}

func saveVaultConfig(cfg *vaultConfig) (string, error) {
	path, err := vaultConfigPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	normalizeVaultConfig(cfg)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	_ = os.Chmod(path, 0o600)
	return path, nil
}

func normalizeVaultConfig(cfg *vaultConfig) {
	cfg.Addr = strings.TrimRight(strings.TrimSpace(cfg.Addr), "/")
	if cfg.Auth.Method == "" {
		cfg.Auth.Method = "ldap"
	}
	if cfg.Auth.Path == "" {
		cfg.Auth.Path = cfg.Auth.Method
	}
	cfg.Secret.Path = strings.Trim(strings.TrimSpace(cfg.Secret.Path), "/")
	if cfg.Secret.Params == nil {
		cfg.Secret.Params = map[string]string{}
	}
}

func (c *vaultConfig) validate() error {
	if c.Addr == "" {
		return errors.New("Vault 地址不能为空")
	}
	if _, err := url.Parse(c.Addr); err != nil {
		return fmt.Errorf("Vault 地址不合法: %w", err)
	}
	if c.Auth.Username == "" && c.Auth.Method != "token" {
		return errors.New("用户名为空")
	}
	if c.Secret.Path == "" {
		return errors.New("密钥路径不能为空")
	}
	return nil
}

func newVaultHTTPClient(cfg *vaultConfig) (*http.Client, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if cfg.TLS.Insecure {
		tlsCfg.InsecureSkipVerify = true // 本地配置显式开启，用于自签证书
	}
	if cfg.TLS.CACert != "" {
		pem, err := os.ReadFile(expandHome(cfg.TLS.CACert))
		if err != nil {
			return nil, fmt.Errorf("读取 CA 证书失败: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("CA 证书解析失败")
		}
		tlsCfg.RootCAs = pool
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: tlsCfg,
			Proxy:           http.ProxyFromEnvironment,
		},
	}, nil
}

func (c *vaultConfig) request(method, path, token string, body any) (map[string]any, error) {
	client, err := newVaultHTTPClient(c)
	if err != nil {
		return nil, err
	}

	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(buf)
	}

	req, err := http.NewRequest(method, c.Addr+"/v1/"+strings.TrimLeft(path, "/"), reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	if c.Namespace != "" {
		req.Header.Set("X-Vault-Namespace", c.Namespace)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("连不上 Vault（%s）: %w", c.Addr, err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(data))
		var e struct {
			Errors []string `json:"errors"`
		}
		if json.Unmarshal(data, &e) == nil && len(e.Errors) > 0 {
			msg = strings.Join(e.Errors, "; ")
		}
		return nil, fmt.Errorf("Vault 返回 %d: %s", resp.StatusCode, msg)
	}

	var out map[string]any
	if len(data) > 0 {
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, fmt.Errorf("解析响应失败: %w", err)
		}
	}
	return out, nil
}

// vaultLoginSession 用 LDAP/username-password 换 Vault client token；
// method=token 时直接把密码当 token
func vaultLoginSession(cfg *vaultConfig, password string) (vaultSession, error) {
	if cfg.Auth.Method == "token" {
		if strings.TrimSpace(password) == "" {
			return vaultSession{}, errors.New("Vault token 为空")
		}
		return vaultSession{Token: strings.TrimSpace(password)}, nil
	}

	path := fmt.Sprintf("auth/%s/login/%s", cfg.Auth.Path, url.PathEscape(cfg.Auth.Username))
	out, err := cfg.request(http.MethodPost, path, "", map[string]string{"password": password})
	if err != nil {
		return vaultSession{}, err
	}
	auth, _ := out["auth"].(map[string]any)
	s := sessionFromAuth(auth)
	if s.Token == "" {
		return vaultSession{}, errors.New("登录成功但没拿到 client_token")
	}
	return s, nil
}

// vaultFetchTokenWithSession 用已有的 client token 读 creds 接口，取服务账号 token
func vaultFetchTokenWithSession(cfg *vaultConfig, s vaultSession) (string, error) {
	out, err := cfg.request(http.MethodPost, cfg.Secret.Path, s.Token, cfg.Secret.Params)
	if err != nil {
		return "", err
	}
	return tokenFromVaultResponse(out)
}

// vaultFetchToken 用密码登录，成功后把 Vault 登录存到本机，之后就能免密换 token。
// method=token 时用户给的本身就是 token，不落盘，免密请用 VAULT_TOKEN 环境变量。
func vaultFetchToken(cfg *vaultConfig, password string) (string, error) {
	if err := cfg.validate(); err != nil {
		return "", err
	}
	s, err := vaultLoginSession(cfg, password)
	if err != nil {
		return "", err
	}
	if cfg.Auth.Method != "token" {
		_, _ = saveVaultSession(cfg, s)
	}
	return vaultFetchTokenWithSession(cfg, s)
}

func tokenFromVaultResponse(out map[string]any) (string, error) {
	data, _ := out["data"].(map[string]any)
	if data == nil {
		return "", errors.New("响应里没有 data")
	}
	// kubernetes secrets engine: service_account_token；KV: token / client_token
	keys := []string{"service_account_token", "token", "client_token"}
	if token, ok := firstString(data, keys); ok {
		return token, nil
	}
	if nested, ok := data["data"].(map[string]any); ok {
		if token, ok := firstString(nested, keys); ok {
			return token, nil
		}
	}
	var available []string
	for k := range data {
		available = append(available, k)
	}
	return "", fmt.Errorf("响应里没找到 token 字段（可用字段: %s）", strings.Join(available, ", "))
}

func firstString(m map[string]any, keys []string) (string, bool) {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}
