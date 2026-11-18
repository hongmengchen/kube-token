# kube-token

<p align="center"><img src="docs/logo.svg" width="96" height="96" alt="kube-token 标志"></p>
<p align="center">六边形是集群，中间那颗菱形是换上去的新凭证。</p>

[![CI](https://github.com/hongmengchen/kube-token/actions/workflows/ci.yml/badge.svg)](https://github.com/hongmengchen/kube-token/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/hongmengchen/kube-token)](https://github.com/hongmengchen/kube-token/releases)
[![License](https://img.shields.io/github/license/hongmengchen/kube-token)](LICENSE)

一个终端 TUI：粘贴一个新 token，直接替换 kubeconfig 里对应 user 的旧 token。界面样式来自共享的 [tui](https://github.com/hongmengchen/tui) 主题库。

给 EKS / K8s 这类需要手动更新 `users[].user.token` 的场景用。启动、选 user、粘 token、回车，剩下的备份和验证都由它做。

```
╱───╲  kube-token  v0.8.0  选择要更新的 user
│ K │  2 个 user · 2 个文件 · 已是最新
╲───╱  a 自动更新 [开]    Vault 免密登录，剩 6d
│ ~/.kube/config                                                    当前 context
│ user     arn:aws:eks:us-east-1:123456789012:cluster/example
│ token    eyJhbGci…-llw  ·  剩 14m02s
│ ~/.kube/prod.yaml
│ user     dev-sa
│ token    eyJhbGci…9pg  ·  已过期 3h
  ↑↓ 选择  ·  enter 手动粘贴  ·  v 从 Vault 获取  ·  a 自动更新  ·  c 配置  ·  q 退出
```

## 特性

- **多 kubeconfig 扫描**：自动读取 `$KUBECONFIG`（多个文件用 `:` 或 `;` 分隔）、`~/.kube/config`、`~/.kube/*.{yaml,yml,conf}`，列出所有 user
- **JWT 过期倒计时**：解析 token 里的 `exp`，直接显示「剩 14m02s」或「已过期 3h」
- **剪贴板直读**：`ctrl+y` 从系统剪贴板取 token，不用手动粘贴
- **Vault 直接取 token**：`v` 输入一次密码，自动登录 Vault、调 creds 接口拿服务账号 token 并写入 kubeconfig；地址、挂载点、角色都只存在本机配置里，不随仓库分发
- **免密自动更新**：登录一次后把 Vault 登录（client token，不是密码）存在本机，之后 `v` 和自动更新都不再问密码；按 `a` 打开开关，token 到期前 10 分钟自动换新并验证。也可以交给 cron / launchd 跑 `kube-token refresh`
- **自动备份**：写入前存成 `config.bak.<时间戳>`，权限 `0600`
- **写入后校验**：从文件回读确认 token 真的变了，再请求集群 `/version` 验证认证是否生效
- **优先官方 kubectl**：有 `kubectl` 就用它写；没有时会问你要不要**一键安装官方 kubectl**（从 `dl.k8s.io` 下载并校验 sha256），不想装再用内置方式直接改 YAML——注释、键顺序、其它字段都会保留
- **版本与自升级**：标题栏常显当前版本；启动时检查 GitHub 最新版，有新版时按 `U` 直接下载、校验 SHA-256 并替换自身
- **单二进制**：Go 编译，macOS / Linux / Windows（amd64 / arm64）都能直接跑，不需要 Python、Node、kubectl 之类外部命令
- **非交互模式**：`-set-token` 给脚本用，一条命令搞定
- **自带 agent skill**：`kube-token skill` 把用法说明装进 `~/.claude/skills`（安装时也会问一次），内容和程序版本一致，升级时自动刷新

## 安装

一条命令，自动识别系统和架构（macOS / Linux / Windows Git Bash）：

```bash
curl -fsSL https://raw.githubusercontent.com/hongmengchen/kube-token/main/install.sh | sh
```

Windows PowerShell：

```powershell
irm https://raw.githubusercontent.com/hongmengchen/kube-token/main/install.ps1 | iex
```

支持 macOS、Linux、Windows 的 amd64 / arm64，共 6 个单文件二进制；Windows ARM64 是原生构建。安装脚本会从 release 的 `checksums.txt` 校验 SHA-256。

安装过程有和 `kube-token upgrade` 一样的动画：旋钮 → 下载进度条（百分比、已下载/总大小、速度）→ 校验 SHA-256 → 完成。输出被重定向（CI、日志）时自动退回逐行纯文本，也可以设 `NO_ANIMATION=1` 关掉。

默认装到 `~/.local/bin`（root 下是 `/usr/local/bin`），Windows 是 `%USERPROFILE%\.local\bin`（Git Bash 里就是 `~/.local/bin`）；如果该目录不在 `PATH` 里，脚本会打印需要追加的那一行。指定目录或版本：

```bash
curl -fsSL https://raw.githubusercontent.com/hongmengchen/kube-token/main/install.sh | sh -s -- -b /usr/local/bin -v v0.6.1
```

也可以手动从 [Releases](../../releases) 下载对应平台的二进制放进 `PATH`，或者用 Go 自己编：

```bash
go install github.com/hongmengchen/kube-token@latest
```

## 使用

```bash
kube-token
```

| 按键 | 作用 |
| --- | --- |
| `↑` `↓` / `j` `k` | 选择 user |
| `enter` | 进入输入页 / 下一步 / 应用 |
| `v` | 从 Vault 登录取 token（需先按 `c` 配好；登录过一次后免密） |
| `a` | 开关自动更新：token 到期前从 Vault 自动换新 |
| `c` | 编辑 Vault 配置（只存本机） |
| `ctrl+y` | 从剪贴板读取 token |
| `r` | 重新扫描 kubeconfig |
| `esc` | 返回上一页 |
| `q` / `ctrl+c` | 退出 |

没有 `kubectl` 的时候，确认页回车后会先问你：

```
kube-token  ! 没有找到 kubectl

  用官方 kubectl 写入最稳妥，行为和命令行完全一致；
  也可以不装，由本程序直接改 YAML（照样先备份，注释和其它字段保留）。

  enter 一键安装官方 kubectl  ·  n 不安装，用内置方式  ·  esc 返回
```

一键安装会从 `dl.k8s.io` 下载对应平台的官方二进制，校验 sha256 后放到 kube-token 自己所在的目录（这个目录本来就在 `PATH` 里），不会动系统包管理器。过程用的是和 `kube-token upgrade` 同一套动画：旋钮 + 进度条 + 百分比/大小/速度 → 校验并安装 → 接着写入 token。

非交互（适合脚本、新设备初始化）：

```bash
# 自动挑当前 context 的 user
kube-token -set-token "$NEW_TOKEN"

# 指定 user / 文件
kube-token -set-token "$NEW_TOKEN" -user dev-sa -file ~/.kube/prod.yaml

# 只看看扫到了什么
kube-token -list

# 缺 kubectl 时先装官方 kubectl 再写入（脚本里的“一键安装”）
kube-token -set-token "$NEW_TOKEN" -install-kubectl

# 免密从 Vault 换 token（到期前才换；可放 cron / launchd）
kube-token refresh
kube-token refresh --force   # 还没到期也换

# 忘掉本机保存的 Vault 登录
kube-token logout
```

`-set-token` 的退出码：`0` 成功，`1` 写入失败，`2` 已写入但验证失败（会打印回滚命令）。`refresh` 同理，还没到期时打印剩余时间并以 `0` 退出，适合定时任务。

## 从 Vault 获取 token

如果你的 token 来自 Vault（比如 Kubernetes secrets engine 的 `creds/<role>`），不用再手动 `vault write` 复制粘贴：

```bash
kube-token
# 第一次：按 c 填写配置，ctrl+s 保存；按 v 输入一次 Vault 密码
# 之后：按 v 直接拿 token（免密），或按 a 打开自动更新
```

程序直接走 Vault 的 HTTP API，**不需要安装 vault CLI**，也不需要预先 `vault login` 或设置 `VAULT_ADDR`。

### 配置

按 `c` 打开的配置项：

| 字段 | 说明 | 示例 |
| --- | --- | --- |
| Vault 地址 | Vault 的访问地址 | `https://vault.example.com` |
| 认证方式 | `ldap` / `userpass` / `token` | `ldap` |
| 认证路径 | 认证方法的挂载点，一般同认证方式 | `ldap` |
| 用户名 | 认证用的用户名（`token` 方式留空） | `alice` |
| 密钥路径 | 读取 token 的 API 路径（不含开头的 `/v1/`） | `<mount>/creds/<role>` |
| 请求参数 | 调用密钥接口时带的参数，写成 `k=v,k2=v2` | `kubernetes_namespace=<namespace>` |
| 目标文件 | 写入哪个 kubeconfig，留空用列表里选中的 | 留空 |
| 目标 user | 写入哪个 user，留空用列表里选中的 | 留空 |

保存位置（权限 `0600`，只在本机，不会出现在仓库里）：

- macOS：`~/Library/Application Support/kube-token/config.yaml`
- Linux：`~/.config/kube-token/config.yaml`
- Windows：`%AppData%\kube-token\config.yaml`

内容就是上面这些字段：

```yaml
addr: https://vault.example.com
auth:
    method: ldap
    path: ldap
    username: alice
secret:
    path: <mount>/creds/<role>
    params:
        kubernetes_namespace: <namespace>
```

还有几个不在表单里的可选项，需要时手动编辑这个文件（`auto_update` 也可以在 TUI 里按 `a` 切换）：

```yaml
namespace: <vault-namespace>   # 多租户 Vault 才需要，会变成 X-Vault-Namespace
auto_update: true              # token 到期前自动换新（等同 TUI 里按 a）
tls:
    ca_cert: /path/to/ca.pem   # 公司自签证书
    insecure: true             # 跳过证书校验，仅用于内网测试
```

### 流程与安全

1. 用你的密码调用 `auth/<path>/login/<username>` 换 Vault client token
2. 用这个 client token 调 `POST /v1/<密钥路径>`，取 `data.service_account_token`（也兼容 `token` / KV v2 的 `data.data.token`）
3. 拿到 token 后进确认页，来源显示 `Vault`；enter 才会真正写进 kubeconfig
4. 密码只在登录那一次用，不落盘；换到的 Vault client token（不是密码）存到本机 `session.json`（`0600`），之后 `v` 和自动更新都直接用它，快到期时先 `renew-self` 续期

登录失败会停在提示页显示 Vault 返回的错误（比如 `invalid username or password`），密码输错不会有副作用。token 已经取到但文件写入失败时，回退到列表重来即可。

### 自动更新（免密）

登录一次后，Vault 的 client token 会存在本机：

| 系统 | 位置 |
| --- | --- |
| macOS | `~/Library/Application Support/kube-token/session.json` |
| Linux | `~/.config/kube-token/session.json` |
| Windows | `%AppData%\kube-token\session.json` |

这个文件只包含 Vault 地址、认证方式、用户名和 client token，权限 `0600`；**密码从不落盘**。client token 快到期时，程序会先调 `auth/token/renew-self` 续期，所以正常情况下只需要输一次密码；只有 Vault 那边允许的最长有效期到了、或 token 被回收，才需要重新登录。

- 列表里按 `a` 打开/关闭自动更新，写进 `config.yaml` 的 `auto_update`，下次打开保持
- 打开后：TUI 启动时和开着时每分钟检查一次；目标 user 的 token 过期或 10 分钟内到期，就免密换新、写入、验证，不弹确认页，完成后在列表下面留一行结果
- 目标 user 取配置里的「目标 user / 目标文件」，留空就用当前 context；`exec` 插件自己会续期、不是 JWT 的 token 判断不了到期时间，这两种都不动
- 还没登录过时不会替你输密码：列表提示「按 v 登录一次 Vault」，登录后自动更新就接管
- TUI 关着也能更新，用 `kube-token refresh` 放定时任务：

```bash
# 每 10 分钟看一眼，快过期才换；还没到期时直接退出，不写文件
*/10 * * * * /path/to/kube-token refresh >> ~/.local/state/kube-token-refresh.log 2>&1
```

- 也认 `VAULT_TOKEN` 环境变量：设置了就优先用它，不会写 `session.json`
- `kube-token logout` 随时清掉本机保存的 Vault 登录；之后 `v` 会重新问密码

## 更新

```bash
kube-token upgrade          # 更新到最新版本
kube-token upgrade --check  # 只检查，不下载
kube-token upgrade --force  # 版本相同也重新安装
```

它去 GitHub Releases 查最新 tag，和当前版本比对，然后下载对应平台的二进制，先用 release 的 `checksums.txt` 校验 SHA-256，再在可执行文件同目录写临时文件、原子替换，不会留下半截文件；校验失败不会动原文件。安装目录需要提权时用 `sudo kube-token upgrade`。

终端里运行会有一个升级动画：检查版本 → 下载进度条（大小、百分比、速度）→ 替换文件 → 完成。`--check`、管道、CI 这类非终端场景自动退回逐行纯文本，不会往日志里塞控制字符。

TUI 标题栏始终显示当前版本（如 `kube-token v0.6.1`），启动时会顺手查最新版。有新版本时状态行显示 `最新 0.6.2，按 U 升级`，按 `U` 就在界面里下载、校验并替换自身，完成后提示重开生效。

> 从 `v0.1.0` 升上来是第一次：那个版本还没有 `upgrade` 子命令，需要先用安装脚本重装一次，之后就能用 `kube-token upgrade` 了。

## 它做了什么

1. 扫描 kubeconfig，解析每个 user 的 token 和 JWT 过期时间
2. 选中的条目：备份文件 → 写入新 token
   - 有 `kubectl`：`kubectl config set-credentials <user> --token=<new>`（结果更稳，和命令行行为一致）
   - 没有 `kubectl`：先问要不要一键安装官方 kubectl；选择不装（或装失败）就由程序直接改 YAML 节点
3. 重新解析文件，确认 token 确实写进去了（不一致就报错）
4. 用该 user 的 token 请求集群 `/version`（有 `kubectl` 时走 `kubectl get --raw /version`）：能拿到版本号就是通了；返回 `Forbidden` 也算认证成功（只是身份权限受限）
5. 结果页会标明这次用的是「官方 kubectl」还是「内置方式」，验证失败时保留备份并给出回滚命令

## 安全说明

- token 只在内存和你本机的 kubeconfig 里，不联网、不上报、不写日志
- 界面上只显示掩码（头 8 位 + 尾 4 位）
- 文件写入优先交给 `kubectl`；没有它就原地改 YAML 节点（保留注释和其它字段），原文件完整备份在旁边的 `.bak.<时间戳>`
- 备份文件权限 `0600`
- 发布二进制带 `checksums.txt`，安装脚本、`kube-token upgrade` 和 hmck 都会校验 SHA-256
- Vault 的公司内部地址、挂载点、角色、用户只写在本地配置里（`0600`），不进入程序、不进入仓库；仓库 `.gitignore` 也屏蔽了 `config.yaml`
- 登录过的 Vault client token 存在本机 `session.json`（`0600`，和 kubeconfig 里的 token 同等敏感）；不想要就 `kube-token logout`，或在自动更新里关掉不代表清除登录

如果某个 user 用的是 `exec` 插件（例如 `aws eks get-token`），TUI 会在详情里标注「exec 自动续期」——这种 user 写死 token 可能和插件冲突，一般不需要用它。

## 给 agent 用

仓库里带了一个 Claude Code / 兼容 agent skill：[`.claude/skills/kube-token/SKILL.md`](.claude/skills/kube-token/SKILL.md)。它写清了 agent 该走的非交互路径（`list` → 取 token → `-set-token` → 读退出码）、回滚方式，以及「不回显 token、不提交 kubeconfig、不猜 user」这几条硬约束。CLI 相关的用法和人读的是同一套，TUI 只留给人。

skill 内嵌在二进制里，可以直接装到全局目录，让**所有**项目里的 agent 都能读到：

```bash
kube-token skill           # 写到 ~/.claude/skills/kube-token/SKILL.md（旧版本会覆盖）
kube-token skill -print    # 只把内容打到 stdout，不写文件
kube-token skill -dir /path/to/skills   # 换个根目录
```

- 内容永远和当前程序版本配套（`go:embed`），不需要单独维护或联网拉
- 运行 `kube-token upgrade` 后，如果之前装过 skill，会自动用新版本刷新一次
- 安装脚本装完会问一句「要不要装 skill」，答 `n` 跳过；`NO_PROMPT=1` 可以完全不问

## 开发

```bash
gofmt -l .
go vet ./...
go test ./...
go build -o kube-token .
```

push / PR 会跑 CI；打 tag 会触发 GitHub Actions 跑测试、构建六个平台的二进制、生成 `checksums.txt` 并发布到 Releases。

## 参与

- [CHANGELOG](CHANGELOG.md) · [CONTRIBUTING](CONTRIBUTING.md)
- 安全问题请按 [SECURITY](SECURITY.md) 走私密报告，不要公开贴真实 token、kubeconfig 或公司内部地址

## License

本项目采用 [MIT](LICENSE) 协议，Copyright (c) 2026 chenliang。
