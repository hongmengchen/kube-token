#!/usr/bin/env sh
# kube-token 安装脚本：自动识别系统/架构，从 GitHub Releases 下载对应二进制
#
#   curl -fsSL https://raw.githubusercontent.com/hongmengchen/kube-token/main/install.sh | sh
#
# 选项：
#   -b, --bindir <dir>   安装目录（默认 ~/.local/bin；root 下默认 /usr/local/bin）
#   -v, --version <tag>  指定版本，如 v0.1.0（默认 latest）
#   -h, --help           显示帮助
set -eu

REPO="hongmengchen/kube-token"
BIN="kube-token"
VERSION="latest"
BIN_DIR=""

usage() {
  cat <<'EOF'
用法: install.sh [选项]

  -b, --bindir <dir>   安装目录（默认 ~/.local/bin；root 下默认 /usr/local/bin）
  -v, --version <tag>  指定版本，如 v0.1.0（默认 latest）
  -h, --help           显示帮助

环境变量：
  INSTALL_DIR          同 --bindir
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    -b|--bindir)   [ $# -ge 2 ] || { echo "错误: --bindir 缺少参数" >&2; exit 1; }; BIN_DIR="$2"; shift 2 ;;
    -v|--version)  [ $# -ge 2 ] || { echo "错误: --version 缺少参数" >&2; exit 1; }; VERSION="$2"; shift 2 ;;
    -h|--help)     usage; exit 0 ;;
    -*)            echo "错误: 未知选项 $1" >&2; exit 1 ;;
    *)             VERSION="$1"; shift ;;
  esac
done

red()   { printf '\033[31m%s\033[0m\n' "$*" >&2; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
dim()   { printf '\033[2m%s\033[0m\n' "$*"; }
die()   { red "错误: $*"; exit 1; }

# ---- 系统 / 架构 ----
os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  darwin)               os=darwin ;;
  linux)                os=linux ;;
  mingw*|msys*|cygwin*|windows*) os=windows ;;
  *) die "不支持的系統: $os（可去 https://github.com/$REPO/releases 手动下载）" ;;
esac

machine=$(uname -m)
case "$machine" in
  x86_64|amd64)   arch=amd64 ;;
  arm64|aarch64)  arch=arm64 ;;
  *) die "不支持的架构: $machine（可去 https://github.com/$REPO/releases 手动下载）" ;;
esac

if [ "$os" = "windows" ] && [ "$arch" = "arm64" ]; then
  die "暂未提供 Windows ARM64 构建，可尝试 amd64 版本"
fi

# ---- 下载地址 ----
if [ "$VERSION" = "latest" ]; then
  base="https://github.com/$REPO/releases/latest/download"
else
  base="https://github.com/$REPO/releases/download/$VERSION"
fi
asset="${BIN}_${os}_${arch}"
[ "$os" = "windows" ] && asset="${asset}.exe"
url="${base}/${asset}"

# ---- 安装目录 ----
if [ -z "$BIN_DIR" ]; then
  if [ -n "${INSTALL_DIR:-}" ]; then
    BIN_DIR="$INSTALL_DIR"
  elif [ "$(id -u)" = "0" ]; then
    BIN_DIR="/usr/local/bin"
  else
    BIN_DIR="$HOME/.local/bin"
  fi
fi
mkdir -p "$BIN_DIR" 2>/dev/null || die "无法创建目录 $BIN_DIR"
[ -w "$BIN_DIR" ] || die "目录不可写: $BIN_DIR（加 --bindir 换一个，或用 sudo 重新执行）"

target="$BIN_DIR/$BIN"
[ "$os" = "windows" ] && target="${target}.exe"

# ---- 下载 ----
tmp=$(mktemp "${TMPDIR:-/tmp}/kube-token.XXXXXX") || die "无法创建临时文件"
trap 'rm -f "$tmp"' EXIT INT TERM

dim "系统: ${os}/${arch}  版本: ${VERSION}"
dim "下载: ${url}"
if command -v curl >/dev/null 2>&1; then
  curl -fsSL --retry 3 --connect-timeout 15 "$url" -o "$tmp" || die "下载失败（网络问题？可手动去 releases 页面下载）"
elif command -v wget >/dev/null 2>&1; then
  wget -q -O "$tmp" "$url" || die "下载失败（网络问题？可手动去 releases 页面下载）"
else
  die "需要 curl 或 wget"
fi

size=$(wc -c <"$tmp" | tr -d ' ')
[ "$size" -gt 100000 ] || die "下载的文件只有 ${size} 字节，可能不是二进制（版本 $VERSION 存在吗？）"

# ---- 安装 ----
mv "$tmp" "$target" || die "无法写入 $target"
chmod +x "$target" 2>/dev/null || true
trap - EXIT INT TERM

green "已安装: $target"
"$target" --version 2>/dev/null || true

case ":${PATH}:" in
  *":${BIN_DIR}:"*) ;;
  *)
    echo
    echo "$BIN_DIR 不在 PATH 里，加一行："
    echo "  echo 'export PATH=\"$BIN_DIR:\$PATH\"' >> ~/.zshrc && exec zsh"
    ;;
esac

echo
dim "下一步: 运行 $BIN"
