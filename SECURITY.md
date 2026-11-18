# 安全策略

## 支持范围

只为最新一个 release 提供安全修复。报告前请先运行 `kube-token upgrade` 确认问题仍然存在。

## 报告问题

请通过 GitHub 的私密渠道报告，不要开公开 issue：

https://github.com/hongmengchen/kube-token/security/advisories/new

报告里请写清版本（`kube-token version`）、系统/终端、复现步骤和影响。**不要粘贴真实 token、kubeconfig 内容、Vault 地址/挂载点/角色、集群地址或用户名**；用 `alice`、`https://vault.example.com`、`arn:aws:eks:...:cluster/example` 这类假值代替。

这是个人项目，会在力所能及的范围内尽快回复，通常不超过 7 天。

## 程序的安全边界

- token 只在内存和本机 kubeconfig 里；不向第三方上报，不写日志
- 写入前备份，备份和配置文件权限 `0600`；界面只显示掩码
- Vault 地址、认证路径、角色、用户名只存在本机 `0600` 配置里，不进入仓库和二进制
- 从 `v0.6.1` 起，release 附带 `checksums.txt`；安装脚本、`kube-token upgrade` 和 hmck 安装/升级都会校验 SHA-256
- 更新检查只访问 GitHub Releases；没有遥测
