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
  NO_ANIMATION=1       不播放安装动画（管道、CI 里会自动关闭）
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

in_path() {
  case ":${PATH}:" in
    *":$1:"*) return 0 ;;
    *) return 1 ;;
  esac
}

red()   { printf '\033[31m%s\033[0m\n' "$*" >&2; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
dim()   { printf '\033[2m%s\033[0m\n' "$*"; }
die()   { red "错误: $*"; exit 1; }

# ---- 动画：和 `kube-token upgrade` 同一套观感（spinner + 进度条 + 大小/速度） ----
# 只在终端里播放；管道、CI、TERM=dumb、NO_ANIMATION=1 时退回逐行纯文本。
ANIM=0
if [ -t 1 ] && [ -z "${NO_ANIMATION:-}" ] && [ -n "${TERM:-}" ] && [ "$TERM" != "dumb" ]; then
  ANIM=1
fi
ESC=$(printf '\033')
CUR_HIDE="${ESC}[?25l"
CUR_SHOW="${ESC}[?25h"
CLR_LINE="${ESC}[K"
PURPLE="${ESC}[38;5;99m"
DIMC="${ESC}[2m"
GREEN="${ESC}[32m"
NC="${ESC}[0m"

# spin_frame <序号>：输出一个 braille 旋钮帧
spin_frame() {
  _n=$1
  set -- ⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏
  shift $((_n % 10))
  printf '%s' "$1"
}

fmt_size() { # 字节 -> "7.4 MB" / "820 KB"
  awk -v n="$1" 'BEGIN{
    if (n >= 1048576) printf "%.1f MB", n/1048576;
    else if (n >= 1024) printf "%.0f KB", n/1024;
    else printf "%d B", n
  }'
}

fmt_stats() { # done total speed -> " 45.3%  3.4/7.4 MB  1.2 MB/s"
  awk -v d="$1" -v t="$2" -v s="$3" 'BEGIN{
    mb = 1048576;
    line = "";
    if (t > 0) line = line sprintf("%5.1f%%  %.1f/%.1f MB", 100*d/t, d/mb, t/mb);
    else       line = line sprintf("%.1f MB", d/mb);
    if (s > 0) line = line sprintf("  %.1f MB/s", s/mb);
    printf "%s", line
  }'
}

draw_bar() { # done total width -> █████░░░░░
  _filled=$(awk -v d="$1" -v t="$2" -v w="$3" 'BEGIN{
    if (t <= 0) { print 0; exit }
    n = int(d * w / t);
    if (n > w) n = w;
    print n
  }')
  _out=""
  _i=0
  while [ "$_i" -lt "$3" ]; do
    if [ "$_i" -lt "$_filled" ]; then _out="${_out}█"; else _out="${_out}░"; fi
    _i=$((_i + 1))
  done
  printf '%s' "$_out"
}

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
    # 优先选一个「可写且已在 PATH 里」的目录，这样装完当前终端就能直接用
    for d in "$HOME/.local/bin" "$HOME/bin" /usr/local/bin; do
      if [ -d "$d" ] && [ -w "$d" ] && in_path "$d"; then
        BIN_DIR="$d"
        break
      fi
    done
    [ -n "$BIN_DIR" ] || BIN_DIR="$HOME/.local/bin"
  fi
fi
mkdir -p "$BIN_DIR" 2>/dev/null || die "无法创建目录 $BIN_DIR"
[ -w "$BIN_DIR" ] || die "目录不可写: $BIN_DIR（加 --bindir 换一个，或用 sudo 重新执行）"

target="$BIN_DIR/$BIN"
[ "$os" = "windows" ] && target="${target}.exe"

# ---- 下载 ----
if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
  die "需要 curl 或 wget"
fi

tmp=$(mktemp "${TMPDIR:-/tmp}/kube-token.XXXXXX") || die "无法创建临时文件"
rcfile="${tmp}.rc"
trap 'rm -f "$tmp" "$rcfile"' EXIT INT TERM

dim "系统: ${os}/${arch}  版本: ${VERSION}"
dim "下载: ${url}"

# 先问一下大小和标签，进度条和「正在下载 vX」才有数据（拿不到也不影响安装）
total=0
tag="$VERSION"
if command -v curl >/dev/null 2>&1; then
  hdrs=$(curl -fsSIL --connect-timeout 15 "$url" 2>/dev/null || true)
  if [ -n "$hdrs" ]; then
    total=$(printf '%s\n' "$hdrs" | tr -d '\r' | awk 'tolower($1)=="content-length:"{n=$2} END{print (n==""?0:n)}')
    loc=$(printf '%s\n' "$hdrs" | tr -d '\r' | awk 'tolower($1)=="location:"{n=$2} END{print n}')
    case "$loc" in
      */download/*)
        tag=${loc#*/download/}
        tag=${tag%%/*}
        ;;
    esac
  fi
fi
[ -n "$total" ] || total=0
[ -n "$tag" ] || tag="$VERSION"

# 后台下载，结束时把退出码写进 $rcfile（用它判断完成，比 kill -0 可靠）
download_bg() {
  (
    set +e
    if command -v curl >/dev/null 2>&1; then
      curl -fsSL --retry 3 --connect-timeout 15 "$url" -o "$tmp"
    else
      wget -q -O "$tmp" "$url"
    fi
    printf '%s' "$?" >"$rcfile"
  ) &
  dlpid=$!
}

if [ "$ANIM" = 1 ]; then
  trap 'printf "%s" "$CUR_SHOW"; rm -f "$tmp" "$rcfile"' EXIT INT TERM
  printf '%s' "$CUR_HIDE"

  download_bg
  tprev=$(date +%s)
  bprev=0
  speed=0
  i=0
  while [ ! -f "$rcfile" ]; do
    size=$(wc -c <"$tmp" 2>/dev/null | tr -d ' ')
    [ -n "$size" ] || size=0
    now=$(date +%s)
    if [ "$now" -gt "$tprev" ]; then
      speed=$(( (size - bprev) / (now - tprev) ))
      tprev=$now
      bprev=$size
    fi
    stats=$(fmt_stats "$size" "$total" "$speed")
    if [ "$total" -gt 0 ]; then
      printf '\r%s  %s%s%s 正在下载 %s  %s%s%s  %s%s%s' \
        "$CLR_LINE" "$PURPLE" "$(spin_frame "$i")" "$NC" "$tag" \
        "$PURPLE" "$(draw_bar "$size" "$total" 22)" "$NC" "$DIMC" "$stats" "$NC"
    else
      printf '\r%s  %s%s%s 正在下载 %s  %s%s%s' \
        "$CLR_LINE" "$PURPLE" "$(spin_frame "$i")" "$NC" "$tag" "$DIMC" "$stats" "$NC"
    fi
    sleep 0.2
    i=$((i + 1))
  done

  wait "$dlpid" 2>/dev/null || true
  if [ "$(cat "$rcfile" 2>/dev/null || echo 1)" != "0" ]; then
    printf '\r%s' "$CLR_LINE"
    die "下载失败（网络问题？可手动去 releases 页面下载）"
  fi

  size=$(wc -c <"$tmp" | tr -d ' ')
  if [ "$size" -le 100000 ]; then
    printf '\r%s' "$CLR_LINE"
    die "下载的文件只有 ${size} 字节，可能不是二进制（版本 $VERSION 存在吗？）"
  fi
  printf '\r%s  %s✓%s 已下载 %s  %s%s%s\n' \
    "$CLR_LINE" "$GREEN" "$NC" "$(fmt_size "$size")" "$DIMC" "$tag" "$NC"
else
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 --connect-timeout 15 "$url" -o "$tmp" || die "下载失败（网络问题？可手动去 releases 页面下载）"
  else
    wget -q -O "$tmp" "$url" || die "下载失败（网络问题？可手动去 releases 页面下载）"
  fi

  size=$(wc -c <"$tmp" | tr -d ' ')
  [ "$size" -gt 100000 ] || die "下载的文件只有 ${size} 字节，可能不是二进制（版本 $VERSION 存在吗？）"
fi

# ---- 安装 ----
if [ "$ANIM" = 1 ]; then
  printf '  %s%s%s 正在安装到 %s%s%s' "$PURPLE" "$(spin_frame 0)" "$NC" "$DIMC" "$BIN_DIR" "$NC"
fi
if ! mv "$tmp" "$target" 2>/dev/null; then
  printf '\r%s' "$CLR_LINE"
  die "无法写入 $target"
fi
chmod +x "$target" 2>/dev/null || true

if [ "$ANIM" = 1 ]; then
  printf '\r%s  %s✓%s 已安装: %s\n' "$CLR_LINE" "$GREEN" "$NC" "$target"
  printf '%s' "$CUR_SHOW"
else
  green "已安装: $target"
fi
trap - EXIT INT TERM
"$target" --version 2>/dev/null || true

if in_path "$BIN_DIR"; then
  echo
  green "$BIN 现在就能用（当前终端直接运行 $BIN）"
else
  rc="$HOME/.profile"
  case "${SHELL:-}" in
    */zsh)  rc="$HOME/.zshrc" ;;
    */bash) rc="$HOME/.bashrc" ;;
  esac
  echo
  echo "$BIN_DIR 不在 PATH 里。"
  echo "  当前终端立刻生效："
  echo "    export PATH=\"$BIN_DIR:\$PATH\""
  echo "  以后每个终端都生效："
  echo "    echo 'export PATH=\"$BIN_DIR:\$PATH\"' >> $rc"
  echo "  然后重开终端，或执行 source $rc"
fi

echo
dim "下一步: 运行 $BIN"
