# 变更记录

更早的版本见 [GitHub Releases](https://github.com/hongmengchen/kube-token/releases)。

## [0.8.0] - 2026-11-18

### 新增

- **Vault 登录缓存**：第一次按 `v` 输入密码换到 client token 后存本机（`session.json`，`0600`），之后取 token **不用再输密码**；密码本身从不落盘
- **自动更新（可开关）**：TUI 里按 `a` 打开/关闭，写进配置 `auto_update`。打开后启动时和每分钟检查一次，token 过期或 10 分钟内到期就免密换新并写回、验证
- `kube-token refresh [--force]`：非交互免密换 token 并写入，适合 cron / launchd 定时跑
- `kube-token logout`：清掉本机保存的 Vault 登录
- 有 `VAULT_TOKEN` 环境变量时优先用它，不读本机缓存
- 快过期（24 小时内）的 Vault 登录会先调 `token/renew-self` 续期再使用
- 列表页第三行显示 `a 自动更新 [开/关]` 和当前登录状态（剩多久 / 按 v 登录一次 Vault）

### 变更

- 配置改了地址、认证方式、路径或用户名后，旧的 Vault 登录自动作废，需要重新按 `v` 登录
- Vault 登录被拒（403）时清掉本地缓存，并提示重新输入一次密码

### 安全

- `session.json` 在配置目录下（macOS `~/Library/Application Support/kube-token/`，Linux `~/.config/kube-token/`），权限 `0600`，已加入 `.gitignore`

## [0.7.0] - 2026-11-18

### 新增

- kube-token 有了自己的标志：`docs/logo.svg`（六边形 + 菱形）
- TUI 左上角用纯文字画出同系列的招牌（六边形 + K），标题、统计和更新状态排在它右边；不依赖图片、字体或终端图形支持

## [0.6.2] - 2026-11-18

### 变更

- Windows 安装脚本默认装到 `%USERPROFILE%\.local\bin`，和 hmck 管理的其他工具一致
- 安装时清理旧的 `%LOCALAPPDATA%\kube-token` 二进制和 PATH 项

## [0.6.1] - 2026-11-18

### 安全

- release 附带 `checksums.txt`，安装脚本、`kube-token upgrade` 和 hmck 安装/升级都会校验 SHA-256
- 自更新先下载到同目录临时文件，校验通过后才原子替换；校验失败不会动原文件

### 新增

- 提供 Windows ARM64 原生构建（共 6 个平台）
- 增加 push / PR CI，发布前跑 gofmt、vet、test

### 变更

- 安装脚本的默认目录统一为 `~/.local/bin`（root 为 `/usr/local/bin`），与 npm-token / hmck 一致
- 安装脚本的强调色改成中性灰蓝，不再使用紫色

## [0.6.0] - 2026-11-18

### 新增

- TUI 标题栏显示当前版本
- 启动时检查 GitHub 最新版；有新版时状态行提示，按 `U` 在界面内升级自身
