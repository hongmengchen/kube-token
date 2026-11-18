# 变更记录

更早的版本见 [GitHub Releases](https://github.com/hongmengchen/kube-token/releases)。

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
