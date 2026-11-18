# kube-token

一个终端 TUI：粘贴一个新 token，直接替换 kubeconfig 里对应 user 的旧 token。

给 EKS / K8s 这类需要手动更新 `users[].user.token` 的场景用。启动、选 user、粘 token、回车，剩下的备份和验证都由它做。

```
kube-token  选择要更新的 user
  2 个 user · 2 个文件
│ ~/.kube/config                                                    当前 context
│ user     arn:aws-cn:eks:cn-northwest-1:660922615903:cluster/Sought
│ token    eyJhbGci…-llw  ·  剩 14m02s
│ ~/.kube/prod.yaml
│ user     dev-sa
│ token    eyJhbGci…9pg  ·  已过期 3h
  ↑↓ 选择  ·  enter 更新 token  ·  r 重新扫描  ·  q 退出
```

## 特性

- **多 kubeconfig 扫描**：自动读取 `$KUBECONFIG`（多个文件用 `:` 或 `;` 分隔）、`~/.kube/config`、`~/.kube/*.{yaml,yml,conf}`，列出所有 user
- **JWT 过期倒计时**：解析 token 里的 `exp`，直接显示「剩 14m02s」或「已过期 3h」
- **剪贴板直读**：`ctrl+y` 从系统剪贴板取 token，不用手动粘贴
- **自动备份**：写入前存成 `config.bak.<时间戳>`，权限 `0600`
- **写入后校验**：从文件回读确认 token 真的变了，再用 `kubectl get --raw /version` 验证认证是否生效
- **单二进制**：Go 编译，macOS / Linux / Windows 都能直接跑，不需要 Python、Node 之类运行时
- **非交互模式**：`-set-token` 给脚本用，一条命令搞定

## 安装

一条命令，自动识别系统和架构（macOS / Linux / Windows Git Bash）：

```bash
curl -fsSL https://raw.githubusercontent.com/hongmengchen/kube-token/main/install.sh | sh
```

Windows PowerShell：

```powershell
irm https://raw.githubusercontent.com/hongmengchen/kube-token/main/install.ps1 | iex
```

默认装到 `~/.local/bin`（root 下是 `/usr/local/bin`），如果该目录不在 `PATH` 里，脚本会打印需要追加的那一行。指定目录或版本：

```bash
curl -fsSL https://raw.githubusercontent.com/hongmengchen/kube-token/main/install.sh | sh -s -- -b /usr/local/bin -v v0.1.0
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
| `ctrl+y` | 从剪贴板读取 token |
| `r` | 重新扫描 kubeconfig |
| `esc` | 返回上一页 |
| `q` / `ctrl+c` | 退出 |

非交互（适合脚本、新设备初始化）：

```bash
# 自动挑当前 context 的 user
kube-token -set-token "$NEW_TOKEN"

# 指定 user / 文件
kube-token -set-token "$NEW_TOKEN" -user dev-sa -file ~/.kube/prod.yaml

# 只看看扫到了什么
kube-token -list
```

`-set-token` 的退出码：`0` 成功，`1` 写入失败，`2` 已写入但验证失败（会打印回滚命令）。

## 更新

```bash
kube-token upgrade          # 更新到最新版本
kube-token upgrade --check  # 只检查，不下载
kube-token upgrade --force  # 版本相同也重新安装
```

它去 GitHub Releases 查最新 tag，和当前版本比对，然后下载对应平台的二进制，在可执行文件同目录写临时文件再原子替换，不会留下半截文件。安装目录需要提权时用 `sudo kube-token upgrade`。

> 从 `v0.1.0` 升上来是第一次：那个版本还没有 `upgrade` 子命令，需要先用安装脚本重装一次，之后就能用 `kube-token upgrade` 了。

## 它做了什么

1. 扫描 kubeconfig，解析每个 user 的 token 和 JWT 过期时间
2. 选中的条目：备份文件 → `kubectl config set-credentials <user> --token=<new>`
3. 重新解析文件，确认 token 确实写进去了（不一致就报错）
4. `kubectl --kubeconfig <file> get --raw /version` 验证认证：能拿到版本号就是通了；返回 `Forbidden` 也算认证成功（只是身份权限受限）
5. 验证失败时保留备份，并在结果页给出回滚命令

## 安全说明

- token 只在内存和你本机的 kubeconfig 里，不联网、不上报、不写日志
- 界面上只显示掩码（头 8 位 + 尾 4 位）
- 文件写入交给 `kubectl`，它会把配置重写成标准格式；原文件完整备份在旁边的 `.bak.<时间戳>`
- 备份文件权限 `0600`

如果某个 user 用的是 `exec` 插件（例如 `aws eks get-token`），TUI 会在详情里标注「exec 自动续期」——这种 user 写死 token 可能和插件冲突，一般不需要用它。

## 开发

```bash
go test ./...
go build -o kube-token .
```

打 tag 会触发 GitHub Actions 构建五个平台的二进制并发布到 Releases。

## License

MIT
