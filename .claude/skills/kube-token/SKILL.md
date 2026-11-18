---
name: kube-token
description: 更新 kubeconfig 里过期或将被替换的 token（EKS / K8s 服务账号场景）。当用户说 token 过期、kubectl 报 401/403/Unauthorized、要从 Vault（或别的密钥源）换新 token 写回 kubeconfig、要在脚本或 CI 里非交互地替换 token、或要在缺 kubectl 的机器上安装官方 kubectl 时使用。包含 kube-token 的 CLI 用法、退出码、回滚方式与安全约束。
---

# kube-token

`kube-token` 是这台机器上的 TUI 程序（也能当纯 CLI 用）：把 kubeconfig 里某个 user 的
`users[].user.token` 换成新 token，写前备份、写后回读校验并请求集群 `/version` 验证。

**agent 一律走 CLI，不要启动 TUI。** TUI 需要真终端（选 user、输入 token、Vault 密码），
在非 TTY 环境里跑不起来；CLI 的每个动作都有对应参数。

## 铁律

1. **不要回显 token。** 不要把完整 token 写进回复、日志、临时文件或提交。需要展示时只用品脱
   （头 8 位 + 尾 4 位）；`kube-token list` 已经帮你掩码了。
2. **不要提交 kubeconfig 或 Vault 配置。** 本机 Vault 配置里有公司内部地址/挂载点/角色，
   只在本机（`0600`），`.gitignore` 也屏蔽了 `config.yaml`。要引用它的字段时只读不打印全文。
3. **覆盖前先确认目标 user。** 先跑 `kube-token list`，确认要改的是哪个文件/哪个 user；
   多个 user 命中时必须用 `-user` / `-file` 明确指定，不要靠默认的「当前 context」猜。
4. **写坏能回滚。** 每次写入都会在 kubeconfig 旁边留 `.bak.<时间戳>`；验证失败时把回滚命令
   原样报给用户，不要自己 `git checkout` 或手改 YAML。

## 1. 看清现状

```bash
kube-token list
```

输出每个 user 一行文件、一行 `user`、一行掩码 token 加过期状态，`*` 标记当前 context：

```
* ~/.kube/config
  user  arn:aws:eks:...:cluster/example
  token eyJhbGci…-llw  ·  剩 14m02s
```

「已过期 3h」「剩 14m02s」都是解析 JWT 的 `exp` 得来的；非 JWT 会显示「非 JWT，无法判断」。
带 `exec` 插件的 user（如 `aws eks get-token`）会自动续期，通常**不该**写死 token。

## 2. 拿到新 token

token 的来源由用户决定，kube-token 只负责写。三种常见情况：

- **用户直接给**（贴过来、放在环境变量里）：直接进第 3 步，注意别回显。
- **从 Vault 取**：首选 `kube-token refresh`，它会用本机保存的 Vault 登录免密换新 token
  并写入、验证，不需要密码、也不需要 TTY：

  ```bash
  kube-token refresh           # 过期或 10 分钟内到期才换；否则只打印剩余时间
  kube-token refresh --force   # 强制换
  ```

  如果报 `还没有可用的 Vault 登录`，说明用户还没在本机登录过 Vault——请用户自己打开
  `kube-token` 按 `v` 输一次密码（之后免密），**不要向用户索要 Vault 密码，也不要自己模拟登录**。
  登录凭据存在本机 `session.json`（`0600`，别打印、别提交）；`kube-token logout` 可以清除。
  本机配置文件里是地址、认证方式、密钥路径等，agent 可以读来拼请求，但**不要打印文件内容**
  （含内部地址）。配置文件位置：

  | 系统 | 路径 |
  | --- | --- |
  | macOS | `~/Library/Application Support/kube-token/config.yaml` |
  | Linux | `~/.config/kube-token/config.yaml` |
  | Windows | `%AppData%\kube-token\config.yaml` |

  字段：`addr`、`auth.method`/`auth.path`/`auth.username`、`secret.path`、`secret.params`、
  可选 `namespace`（`X-Vault-Namespace`）、`tls.ca_cert` / `tls.insecure`。取 token 等价于：

  ```bash
  # 1) 登录换 client token
  curl -sS -X POST "$VAULT_ADDR/v1/auth/<method-path>/login/<username>" \
    -d '{"password":"<password>"}'
  # 2) 用 client token 调密钥路径（params 放到 body；namespace 放到 X-Vault-Namespace 头）
  curl -sS -X POST "$VAULT_ADDR/v1/<secret.path>" \
    -H "X-Vault-Token: <client-token>" \
    -d '{"<key>":"<value>"}'
  ```

  取 `data.service_account_token`（也兼容 `data.token`、`data.client_token`、KV v2 的
  `data.data.*`）。密码不要落盘；client token 交给 kube-token 自己缓存（`session.json`，`0600`），
  不要手动转存或打印。有 `vault` CLI 时用它更省事。
- **别的密钥源**（云厂商 STS、身份提供商）：自己取到 token 后同样进第 3 步。

## 3. 写入并验证

```bash
kube-token -set-token "$NEW_TOKEN" -user <user> -file <kubeconfig 路径>
```

常用参数：

| 参数 | 作用 |
| --- | --- |
| `-set-token <token>` | 非交互写入（必填；token 里不能有空白字符） |
| `-user <name>` | 指定 user；留空时若只有一个候选、或其中有当前 context 的 user，就自动选它 |
| `-file <path>` | 指定 kubeconfig；文件不在默认扫描范围也会被显式读入 |
| `-no-verify` | 只写不验证（离线、内网不通时用） |
| `-install-kubectl` | 机器上没有 kubectl 时，先下载官方 kubectl（`dl.k8s.io`，校验 sha256）再写入 |

退出码：

| 码 | 含义 | 处理 |
| --- | --- | --- |
| `0` | 写入成功（默认还会打印「验证: 认证通过 · 集群 x.y」） | 报结果 |
| `1` | 参数/备份/写入失败 | 看 stderr，通常是 user 没匹配上或文件不可写 |
| `2` | 已写入但验证失败；stderr 会给出 `cp <备份> <文件>` 回滚命令 | 把回滚命令原样交给用户 |

成功时 stdout 形如：

```
写入方式: 官方 kubectl（~/.local/bin/kubectl）
已更新 ~/.kube/config / arn:aws:eks:...:cluster/example
备份: ~/.kube/config.bak.20261118-143118
验证: 认证通过 · 集群 1.31
```

要点：

- `写入方式` 有两种：**官方 kubectl**（`kubectl config set-credentials <user> --token=...`）
  和**内置 YAML 改写**（保住注释、键顺序和其它字段）。两者都先备份。
- 验证是请求集群 `GET /version`：`Forbidden` 也算认证成功（只是这个身份权限受限），
  401 才是 token 没被接受。
- 想自己再确认一次，用新 token 请求 `/version` 而不是 `get ns`（后者容易因权限报错误判）：
  `kubectl --kubeconfig <file> get --raw /version`。
- 需要换成另一个 context 再试？先问用户，别擅自 `kubectl config use-context`。

## 4. 安装与升级

首次安装（Linux / macOS / Windows Git Bash）：

```bash
curl -fsSL https://raw.githubusercontent.com/hongmengchen/kube-token/main/install.sh | sh
```

指定目录或版本、以及在 CI 里关掉动画：

```bash
curl -fsSL https://raw.githubusercontent.com/hongmengchen/kube-token/main/install.sh | sh -s -- -b ~/.local/bin -v v0.4.1
NO_ANIMATION=1 curl -fsSL https://raw.githubusercontent.com/hongmengchen/kube-token/main/install.sh | sh
```

Windows PowerShell：`irm https://raw.githubusercontent.com/hongmengchen/kube-token/main/install.ps1 | iex`

升级（非 TTY 自动输出纯文本，适合脚本）：

```bash
kube-token upgrade --check   # 只检查
kube-token upgrade           # 装到同一路径，原子替换
kube-token version
```

## 5. 在本仓库里改代码时

- 版本号在 `main.go` 的 `var version`；打 tag（`git tag vX.Y.Z && git push origin vX.Y.Z`）
  会触发 `.github/workflows` 构建五个平台的二进制并发布到 Releases，先 bump 再打 tag。
- 改完跑 `gofmt -l .`、`go vet ./...`、`go test ./...`、`go build -o kube-token .`。
- **不要往仓库里写任何公司内部信息**：Vault 地址、挂载点、角色名、用户名、namespace、集群 ARN
  只能出现在本机配置或测试里的假数据（如 `alice`、`creds/my-role`、`demo`）。提交前可以自查：

  ```bash
  grep -rniE "<公司关键词1>|<公司关键词2>|<内部域名>" --include='*.go' --include='*.md' --include='*.sh' --include='*.ps1' .
  ```

- 用户可见文案是中文；TUI 样式来自共享主题库 `github.com/hongmengchen/tui/theme`
  （`ui.go` 里的 `var styles = theme.Default`），升级动画在 `upgrade_tui.go`，
  一键装 kubectl 的动画在 `ui.go` 的 `viewKcInstall`。
- TUI 列表页启动时会异步查最新版，标题显示当前版本；有新版时按 `U` 走和
  `kube-token upgrade` 相同的下载/替换流程（`applyRelease`）。
- 涉及 YAML 写入的改动，保留原有测试（`kubeconfig_test.go` 里
  `TestApplyTokenNative*`、`TestVerifyClusterNative*` 覆盖无 kubectl 的路径）。

## 6. 常见问题

| 现象 | 原因 / 处理 |
| --- | --- |
| `没有匹配的 user（共扫描到 N 个条目）` | `-user` / `-file` 写错，或该文件不在扫描范围；先 `kube-token list` |
| `匹配到多个 user` | 输出里列了候选，挑一个用 `-user` + `-file` 指定 |
| `找不到 kubectl` | 加 `-install-kubectl`；不加则先用内置 YAML 改写，结果里 `写入方式` 会标出来 |
| `验证失败: ... 401` | token 无效或集群不认；文件已改，按 stderr 的回滚命令恢复 |
| `Forbidden` | 正常，认证已通过，只是这个身份没权限读 `/version` 之外的资源 |
| `kubectl get ...` 看到 `REDACTED` | kubectl 展示时脱敏，文件里是真 token；要探测集群用 `get --raw /version` |
| `还没有可用的 Vault 登录` | 用户本机没登录过（或登录过期/被清）；让用户自己开 TUI 按 `v` 输一次密码，不要索取密码 |
| 想让它自动换 token | `kube-token` 里按 `a` 开关，写进配置 `auto_update`；或定时跑 `kube-token refresh` |
| Vault 登录失效 | `kube-token logout` 后让用户重新按 `v` 登录一次；`VAULT_TOKEN` 环境变量优先于本机缓存 |
| 机器上同时有多个 kubeconfig | 扫描顺序是 `$KUBECONFIG`、`~/.kube/config`、`~/.kube/*.{yaml,yml,conf}`；用 `-file` 精确指定 |
