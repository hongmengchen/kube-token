#!/usr/bin/env bash
#
# kube-token.sh — 启动后只需粘贴新的 token，直接替换 kubeconfig 里对应 user 的 token
#
# 用法:
#   ./kube-token.sh                      交互输入（不回显），替换当前 context 对应 user 的 token
#   ./kube-token.sh <token>              直接传参（会留在 shell 历史里）
#   echo <token> | ./kube-token.sh       从标准输入读取
#   ./kube-token.sh -f /path/to/config   指定 kubeconfig（默认 $KUBECONFIG 或 ~/.kube/config）
#   ./kube-token.sh -u <user>            指定要修改的 user（默认 current-context 对应的 user）
#
set -euo pipefail

PROG=$(basename "$0")

die()  { printf '错误: %s\n' "$*" >&2; exit 1; }
warn() { printf '警告: %s\n' "$*" >&2; }
info() { printf '%s\n' "$*"; }

usage() {
  cat <<'EOF'
用法:
  kube-token.sh                    交互输入新 token，替换当前 context 对应 user 的 token
  kube-token.sh <token>            直接把 token 作为参数传入
  echo <token> | kube-token.sh     从标准输入读取 token

选项:
  -f, --file <path>   指定 kubeconfig 文件（默认 $KUBECONFIG 或 ~/.kube/config）
  -u, --user <name>   指定要修改的 user（默认 current-context 对应的 user）
  -y, --yes           跳过替换前的确认
      --no-verify     替换后不验证集群连通性
  -h, --help          显示帮助
EOF
}

# ---------- 参数 ----------
KCFG_ARG=""
USER_ARG=""
ASSUME_YES=0
VERIFY=1
ARG_TOKEN=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    -f|--file)   [[ $# -ge 2 ]] || die "--file 缺少参数"; KCFG_ARG="$2"; shift 2 ;;
    -u|--user)   [[ $# -ge 2 ]] || die "--user 缺少参数"; USER_ARG="$2"; shift 2 ;;
    -y|--yes)    ASSUME_YES=1; shift ;;
    --no-verify) VERIFY=0; shift ;;
    -h|--help)   usage; exit 0 ;;
    --)          shift; break ;;
    -*)          die "未知选项: $1（-h 查看帮助）" ;;
    *)           ARG_TOKEN="$1"; shift ;;
  esac
done

command -v kubectl >/dev/null 2>&1 || die "找不到 kubectl，请先安装并加入 PATH"

# ---------- kubeconfig 路径 ----------
if [[ -n "$KCFG_ARG" ]]; then
  KCFG="$KCFG_ARG"
elif [[ -n "${KUBECONFIG:-}" ]]; then
  case "$KUBECONFIG" in
    [A-Za-z]:[\\/]*)  KCFG="$KUBECONFIG" ;;   # Git Bash 下的 Windows 盘符路径
    *:*|*';'*)
      KCFG="${KUBECONFIG%%[:;]*}"
      warn "KUBECONFIG 里配了多个文件，只修改第一个: $KCFG"
      ;;
    *) KCFG="$KUBECONFIG" ;;
  esac
else
  KCFG="$HOME/.kube/config"
fi

[[ -f "$KCFG" ]] || die "找不到 kubeconfig: $KCFG（用 -f 指定路径）"
[[ -r "$KCFG" && -w "$KCFG" ]] || die "没有读写权限: $KCFG"

# go-template 里按 name 精确定位 user（name 里常带 / 和 :，用 jsonpath 容易踩坑）
# $1 = user name, $2 = 要取的 go-template 片段
tmpl_for() {
  local name="$1" frag="$2" esc
  esc=$(printf '%s' "$name" | sed 's/\\/\\\\/g; s/"/\\"/g')
  printf '%s' "{{range .users}}{{if eq .name \"$esc\"}}${frag}{{end}}{{end}}"
}

get_user_token() {
  local tpl
  tpl=$(tmpl_for "$1" '{{.user.token}}')
  # --raw: 不加的话 kubectl 会把 token 输出成 REDACTED
  kubectl --kubeconfig "$KCFG" config view --raw -o go-template="$tpl" 2>/dev/null || true
}

get_user_exec() {
  local tpl
  tpl=$(tmpl_for "$1" '{{if .user.exec}}yes{{end}}')
  kubectl --kubeconfig "$KCFG" config view --raw -o go-template="$tpl" 2>/dev/null || true
}

# ---------- 确定目标 user ----------
USER_NAME="$USER_ARG"

if [[ -z "$USER_NAME" ]]; then
  USER_NAME=$(kubectl --kubeconfig "$KCFG" config view --minify -o jsonpath='{.users[0].name}' 2>/dev/null || true)
fi

if [[ -z "$USER_NAME" ]]; then
  USERS=()
  while IFS= read -r u; do
    [[ -n "$u" ]] && USERS+=("$u")
  done < <(kubectl --kubeconfig "$KCFG" config view -o jsonpath='{range .users[*]}{.name}{"\n"}{end}' 2>/dev/null || true)

  [[ ${#USERS[@]} -gt 0 ]] || die "kubeconfig 里没有任何 user: $KCFG"

  if [[ ${#USERS[@]} -eq 1 ]]; then
    USER_NAME="${USERS[0]}"
  elif [[ -t 0 ]]; then
    info "kubeconfig 里有多个 user，请选择要更新的那个："
    select_ok=0
    PS3="编号: "
    select pick in "${USERS[@]}"; do
      if [[ -n "$pick" ]]; then USER_NAME="$pick"; select_ok=1; break; fi
      info "无效选择，请重试"
    done
    [[ $select_ok -eq 1 ]] || die "未选择 user"
  else
    printf '可用的 user:\n' >&2
    printf '  %s\n' "${USERS[@]}" >&2
    die "存在多个 user，请用 -u 指定（或先设置 current-context）"
  fi
fi

CUR_CTX=$(kubectl --kubeconfig "$KCFG" config current-context 2>/dev/null || true)
OLD_TOKEN=$(get_user_token "$USER_NAME")
HAS_EXEC=$(get_user_exec "$USER_NAME")

# 确认 user 存在：名字能匹配上，或者本来就有 token / exec
if [[ -z "$OLD_TOKEN" && -z "$HAS_EXEC" ]]; then
  if ! kubectl --kubeconfig "$KCFG" config view -o jsonpath='{range .users[*]}{.name}{"\n"}{end}' 2>/dev/null | grep -Fxq "$USER_NAME"; then
    die "kubeconfig 里没有名为 '$USER_NAME' 的 user"
  fi
fi

if [[ -n "$HAS_EXEC" ]]; then
  warn "该 user 使用 exec 插件动态获取凭证（如 aws eks get-token），token 每次都会自动刷新。"
  warn "写死 token 后 exec 仍留在配置里，可能导致 kubectl 报冲突。"
fi

# ---------- 读取并清洗 token ----------
TOKEN="$ARG_TOKEN"
if [[ -z "$TOKEN" ]]; then
  if [[ ! -t 0 ]]; then
    IFS= read -r TOKEN || true          # 从管道读取
  else
    printf '粘贴新的 token（不回显），回车确认: ' >&2
    IFS= read -r -s TOKEN || true
    printf '\n' >&2
  fi
fi

# 去掉 CRLF、首尾空白和引号、Bearer 前缀
TOKEN=$(printf '%s' "$TOKEN" | tr -d '\r\n')
TOKEN="${TOKEN#"${TOKEN%%[![:space:]]*}"}"   # trim 左侧
TOKEN="${TOKEN%"${TOKEN##*[![:space:]]}"}"   # trim 右侧
TOKEN="${TOKEN#Bearer }"; TOKEN="${TOKEN#bearer }"
case "$TOKEN" in
  \"*\") TOKEN="${TOKEN:1:${#TOKEN}-2}" ;;
  \'*\') TOKEN="${TOKEN:1:${#TOKEN}-2}" ;;
esac

[[ -n "$TOKEN" ]] || die "token 为空"
case "$TOKEN" in
  *[[:space:]]*) die "token 里含有空白字符，请检查是否粘贴时混入了换行或空格" ;;
esac
[[ ${#TOKEN} -ge 16 ]] || warn "token 只有 ${#TOKEN} 个字符，长度偏短，请确认没粘贴错"

mask() {
  local t="$1"
  if [[ -z "$t" ]]; then printf '(空)'
  elif [[ ${#t} -le 16 ]]; then printf '%s****' "${t:0:4}"
  else printf '%s…%s' "${t:0:8}" "${t: -4}"
  fi
}

# ---------- 确认 ----------
info ""
info "kubeconfig : $KCFG"
info "user       : $USER_NAME"
[[ -n "$CUR_CTX" ]] && info "context    : $CUR_CTX"
info "旧 token   : $(mask "$OLD_TOKEN")"
info "新 token   : $(mask "$TOKEN")"
info ""

if [[ $ASSUME_YES -eq 0 ]]; then
  if [[ -t 0 ]]; then
    printf '确认替换？[Y/n] ' >&2
    read -r ans || ans=""
    case "$ans" in
      n|N|no|NO) info "已取消"; exit 0 ;;
    esac
  fi
fi

# ---------- 备份 + 替换 ----------
BACKUP="${KCFG}.bak.$(date +%Y%m%d-%H%M%S)"
cp -p "$KCFG" "$BACKUP"
chmod 600 "$BACKUP" 2>/dev/null || true
info "已备份旧配置: $BACKUP"

kubectl --kubeconfig "$KCFG" config set-credentials "$USER_NAME" --token="$TOKEN" >/dev/null

NEW_TOKEN=$(get_user_token "$USER_NAME")
if [[ "$NEW_TOKEN" != "$TOKEN" ]]; then
  warn "写入后的值和新 token 不一致，已回滚"
  cp -p "$BACKUP" "$KCFG"
  die "替换失败（备份已还原: $BACKUP）"
fi

info "✓ token 已替换（$(mask "$NEW_TOKEN")）"

# ---------- 验证 ----------
if [[ $VERIFY -eq 1 ]]; then
  printf '验证集群连通性 ... ' >&2
  # /version 对任何已认证身份都放行，比 get ns 更通用（SA token 往往没有列 namespace 的权限）
  if err=$(kubectl --kubeconfig "$KCFG" get --raw /version --request-timeout=15s 2>&1 >/dev/null); then
    info "通过"
  else
    case "$err" in
      *Forbidden*)
        info "认证通过（当前身份没有列 namespace 的权限，属正常）"
        ;;
      *)
        info "失败"
        printf '%s\n' "$err" | grep -v '^E[0-9]' | tail -5 >&2
        info ""
        warn "集群拒绝了新 token。如需回滚: cp \"$BACKUP\" \"$KCFG\""
        exit 2
        ;;
    esac
  fi
fi

info ""
info "完成。提示：EKS 这类 token 通常 15 分钟就过期，过期后重跑本脚本即可；"
info "如果想自动续期，可以用: aws eks update-kubeconfig --name <cluster> --region cn-northwest-1"
