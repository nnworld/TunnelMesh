#!/usr/bin/env bash
# TunnelMesh 一键安装共享库（Linux / macOS）。
# 三个角色入口 install-{server,agent,client}.sh 只做参数透传与角色钩子，
# 下载、校验、交互、渲染、服务注册全部集中在这里，避免三份实现漂移。
# 本文件不得包含任何真实 token、密码、DSN 或私钥。
set -euo pipefail

# --- 退出码契约（docs/deployment/oneclick-install.md 的排障表以此为准）---
TM_EXIT_OK=0
TM_EXIT_USAGE=2
TM_EXIT_PREFLIGHT=3
TM_EXIT_DOWNLOAD=4
TM_EXIT_CHECKSUM=5
TM_EXIT_CONFIG=6
TM_EXIT_SERVICE=7
TM_EXIT_UNINSTALL=8

TM_REPOSITORY="nnworld/TunnelMesh"
TM_DEFAULT_BASE_URL="https://github.com/${TM_REPOSITORY}/releases"
TM_DEFAULT_RAW_BASE_URL="https://raw.githubusercontent.com/${TM_REPOSITORY}"
TM_LATEST_API_URL="https://api.github.com/repos/${TM_REPOSITORY}/releases/latest"
TM_LIB_RELATIVE_PATH="deploy/install/oneclick/tunnelmesh-install-common.sh"
# Linux 默认装到当前用户（systemd user unit）；--mode system 才走 root + /etc/tunnelmesh。
TM_DEFAULT_INSTALL_MODE="user"
TM_VERSION_PATTERN='^v[0-9]+\.[0-9]+\.[0-9]+$'

# --- 日志原语：正常输出走 stdout，错误与警告走 stderr ---
tm_info() { printf '==> %s\n' "$*"; }
tm_warn() { printf 'WARNING: %s\n' "$*" >&2; }
tm_die() {
  local code="$1"; shift
  printf 'ERROR: %s\n' "$*" >&2
  exit "$code"
}

tm_have() { command -v "$1" >/dev/null 2>&1; }

tm_require_cmds() {
  local missing=()
  local cmd
  for cmd in "$@"; do
    tm_have "$cmd" || missing+=("$cmd")
  done
  if [[ ${#missing[@]} -gt 0 ]]; then
    tm_die "$TM_EXIT_PREFLIGHT" "missing required command(s): ${missing[*]}"
  fi
}

# tm_detect_arch <uname -m 输出>：纯函数，便于测试；未知架构输出空并非零退出。
tm_detect_arch() {
  case "$1" in
    x86_64) printf 'amd64\n' ;;
    aarch64 | arm64) printf 'arm64\n' ;;
    *) return 1 ;;
  esac
}

# tm_detect_platform：设置 TM_OS_FAMILY / TM_GOOS / TM_GOARCH / TM_ARCHIVE_EXT。
# 公网入口只有 HTTP/HTTPS/WebSocket，因此 Windows 归档是 zip，其余是 tar.gz。
tm_detect_platform() {
  local kernel machine arch
  kernel="$(uname -s)"
  machine="$(uname -m)"
  case "$kernel" in
    Linux) TM_OS_FAMILY="linux"; TM_GOOS="linux"; TM_ARCHIVE_EXT="tar.gz" ;;
    Darwin) TM_OS_FAMILY="darwin"; TM_GOOS="darwin"; TM_ARCHIVE_EXT="tar.gz" ;;
    *) tm_die "$TM_EXIT_USAGE" "unsupported operating system: ${kernel} (Windows 请使用 install-<role>.ps1)" ;;
  esac
  arch="$(tm_detect_arch "$machine")" || tm_die "$TM_EXIT_USAGE" "unsupported architecture: ${machine}"
  TM_GOARCH="$arch"
}

# tm_tty_init：交互提示统一从 /dev/tty 读，使 `curl | bash`（stdin 是管道）也能问答。
# 无 tty 时 TM_TTY 为空串，调用方回退 stdin；若同时没有 --yes，tm_ask 会以退出码 3 失败，
# 绝不静默地把所有答案取成默认值。
tm_tty_init() {
  # 重定向错误由 shell 自己打印，`cmd >/dev/tty 2>/dev/null` 压不住（bash 先执行
  # >/dev/tty 并在失败时立即报错），必须把整组命令包起来再重定向 stderr。
  if { : >/dev/tty; } 2>/dev/null; then
    TM_TTY="/dev/tty"
  else
    TM_TTY=""
  fi
}

# tm_entry_init <argv0> <BASH_SOURCE[0]>：解析入口脚本真实路径。
# `bash -c "$(curl ...)"` 下 $0 是 bash、BASH_SOURCE 是 main，两者都不是路径，
# 因此这里只接受「存在的普通文件」，否则留空让共享库走 raw 下载回退。
tm_entry_init() {
  local candidate
  TM_ONECLICK_ENTRY="" TM_ONECLICK_DIR=""
  for candidate in "$2" "$1"; do
    if [[ -n "$candidate" && -f "$candidate" ]]; then
      TM_ONECLICK_ENTRY="$(cd "$(dirname "$candidate")" && pwd)/$(basename "$candidate")"
      TM_ONECLICK_DIR="$(dirname "$TM_ONECLICK_ENTRY")"
      return 0
    fi
  done
  return 0
}

TM_ONECLICK_YES="${TM_ONECLICK_YES:-0}"
TM_ONECLICK_REF="${TM_ONECLICK_REF:-main}"
# 角色由入口脚本设置（server/agent/client）；库自身给空默认值，避免 set -u 下引用未定义变量。
TM_ROLE="${TM_ROLE:-}"

# tm_mask <secret>：摘要与日志里只显示前 4 位。
tm_mask() {
  local value="$1"
  if [[ -z "$value" ]]; then
    printf '(empty)\n'
  elif [[ ${#value} -le 4 ]]; then
    printf '****\n'
  else
    printf '%s****\n' "${value:0:4}"
  fi
}
# --- 下载、校验、缓存 ---
TM_HTTP_TIMEOUT="${TM_HTTP_TIMEOUT:-120}"
TM_BASE_URL="${TUNNELMESH_RELEASE_BASE_URL:-$TM_DEFAULT_BASE_URL}"
TM_RAW_BASE_URL="${TUNNELMESH_RAW_BASE_URL:-$TM_DEFAULT_RAW_BASE_URL}"
TM_GITHUB_TOKEN="${TUNNELMESH_GITHUB_TOKEN:-${TM_GITHUB_TOKEN:-}}"
TM_NO_CACHE="${TM_NO_CACHE:-0}"
TM_CACHE_DIR="${TM_CACHE_DIR:-}"
TM_WORKDIR="" TM_ARCHIVE_PATH="" TM_SUMS_PATH="" TM_EXTRACT_DIR=""

# tm_fetch 是唯一的 curl 内容出口，测试用 PATH 前置的 stub 覆盖它。
tm_fetch() {
  local url="$1" out="$2"
  local args=(--fail --silent --show-error --location --max-time "$TM_HTTP_TIMEOUT")
  if [[ -n "$TM_GITHUB_TOKEN" && "$url" == https://api.github.com/* ]]; then
    args+=(--header "Authorization: Bearer ${TM_GITHUB_TOKEN}")
  fi
  if [[ "$out" == "-" ]]; then
    curl "${args[@]}" "$url" || tm_die "$TM_EXIT_DOWNLOAD" "download failed: ${url}"
  else
    curl "${args[@]}" "$url" --output "$out" || tm_die "$TM_EXIT_DOWNLOAD" "download failed: ${url}"
  fi
}

tm_fetch_head() {
  curl --silent --show-error --head --max-time 30 "$1"
}

tm_checksum_file() {
  # LC_ALL=C：shasum 是 perl 脚本，遇到 C.UTF-8 这类未安装 locale 会往 stderr 刷一屏警告；
  # 十六进制摘要与 locale 无关，固定成 C 既安静又确定。
  if tm_have sha256sum; then LC_ALL=C sha256sum "$1" | awk '{print $1}'; else LC_ALL=C shasum -a 256 "$1" | awk '{print $1}'; fi
}

tm_verify_checksum() { # <file> <sums-file> <archive-name>
  local file="$1" sums="$2" name="$3" line expected actual
  line="$(grep "  ${name}\$" "$sums" || true)"
  [[ -n "$line" ]] || tm_die "$TM_EXIT_CHECKSUM" "checksum entry not found for ${name}"
  expected="${line%%[[:space:]]*}"
  actual="$(tm_checksum_file "$file")"
  if [[ "$expected" != "$actual" ]]; then
    tm_die "$TM_EXIT_CHECKSUM" "checksum mismatch for ${name} (expected ${expected}, actual ${actual})"
  fi
}

tm_archive_name() { printf 'tunnelmesh-%s-%s-%s.%s\n' "$1" "$TM_GOOS" "$TM_GOARCH" "$TM_ARCHIVE_EXT"; }

tm_cache_dir() {
  if [[ -n "$TM_CACHE_DIR" ]]; then printf '%s\n' "$TM_CACHE_DIR"
  else printf '%s/tunnelmesh/releases\n' "${XDG_CACHE_HOME:-$HOME/.cache}"; fi
}

tm_validate_version() {
  [[ "$1" =~ $TM_VERSION_PATTERN ]] \
    || tm_die "$TM_EXIT_USAGE" "invalid version: expected vMAJOR.MINOR.PATCH, got '$1'"
}

tm_resolve_version_stdout() {
  local tag=""
  tag="$(tm_fetch "$TM_LATEST_API_URL" - 2>/dev/null | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1 || true)"
  if [[ -z "$tag" ]]; then
    # API 配额耗尽或不可达时回退：/releases/latest 的 302 Location 里带 tag，不消耗 API 配额。
    tag="$(tm_fetch_head "${TM_BASE_URL}/latest" 2>/dev/null | sed -n 's#^[Ll]ocation:.*\/tag\/\(v[0-9][^[:space:]]*\).*#\1#p' | head -n 1 || true)"
  fi
  [[ -n "$tag" ]] || tm_die "$TM_EXIT_DOWNLOAD" "cannot resolve latest version; pass --version vX.Y.Z explicitly"
  printf '%s\n' "$tag"
}

tm_resolve_version() {
  if [[ "$TM_VERSION" == "latest" ]]; then
    TM_VERSION="$(tm_resolve_version_stdout)"
    tm_info "resolved latest version: ${TM_VERSION}"
  fi
  tm_validate_version "$TM_VERSION"
}

# tm_cache_lookup 依赖「命令替换子 shell 里 tm_die 的 exit 只结束子 shell」这一隔离，
# 因此调用方必须写成 if cached="$(tm_cache_lookup ...)"; then ...，不要改成直接赋值。
tm_cache_lookup() { # <archive-name> <sums-file>
  local name="$1" sums="$2" path
  path="$(tm_cache_dir)/${name}"
  [[ -f "$path" ]] || return 1
  if tm_verify_checksum "$path" "$sums" "$name" 2>/dev/null; then
    printf '%s\n' "$path"; return 0
  fi
  rm -f -- "$path"
  return 1
}

tm_download_release() { # <version>
  local version="$1" name cached
  name="$(tm_archive_name "$version")"
  TM_WORKDIR="$(mktemp -d)"
  TM_SUMS_PATH="${TM_WORKDIR}/SHA256SUMS"
  tm_fetch "${TM_BASE_URL}/download/${version}/SHA256SUMS" "$TM_SUMS_PATH"
  if [[ "$TM_NO_CACHE" != "1" ]] && cached="$(tm_cache_lookup "$name" "$TM_SUMS_PATH")"; then
    tm_info "using cached archive ${cached}"
    TM_ARCHIVE_PATH="$cached"
    return 0
  fi
  TM_ARCHIVE_PATH="${TM_WORKDIR}/${name}"
  tm_info "downloading ${name}"
  tm_fetch "${TM_BASE_URL}/download/${version}/${name}" "$TM_ARCHIVE_PATH"
  tm_verify_checksum "$TM_ARCHIVE_PATH" "$TM_SUMS_PATH" "$name"
  if [[ "$TM_NO_CACHE" != "1" ]]; then
    mkdir -p "$(tm_cache_dir)"
    cp -f -- "$TM_ARCHIVE_PATH" "$(tm_cache_dir)/${name}"
  fi
}

tm_extract_archive() { # <archive> <dest-dir>
  mkdir -p "$2"
  case "$1" in
    *.tar.gz) LC_ALL=C tar -xzf "$1" -C "$2" ;;
    *.zip) unzip -q "$1" -d "$2" ;;
    *) tm_die "$TM_EXIT_USAGE" "unsupported archive type: $1" ;;
  esac
}

# tm_load_lib：三级回退定位共享库（见设计规格 §5）。入口脚本在 source 之前调用它，
# 因此这里不能依赖库内函数——只用 POSIX 内建与 curl。
tm_load_lib() { # <entry-dir> <ref>
  local dir="$1" ref="${2:-main}" candidate out
  if [[ -n "${TM_ONECLICK_LIB:-}" && -f "$TM_ONECLICK_LIB" ]]; then
    printf '%s\n' "$TM_ONECLICK_LIB"; return 0
  fi
  candidate="${dir}/tunnelmesh-install-common.sh"
  if [[ -n "$dir" && -f "$candidate" ]]; then printf '%s\n' "$candidate"; return 0; fi
  out="$(mktemp -d)/tunnelmesh-install-common.sh"
  curl --fail --silent --show-error --location --max-time 60 \
    "${TM_RAW_BASE_URL:-https://raw.githubusercontent.com/nnworld/TunnelMesh}/${ref}/deploy/install/oneclick/tunnelmesh-install-common.sh" \
    --output "$out" || return 4
  [[ -s "$out" ]] || return 4
  [[ "$(head -n 1 "$out")" == '#!/usr/bin/env bash' ]] || return 4
  printf '%s\n' "$out"
}

# --- 答案存储（bash 3.2 无关联数组，用变量名映射）---
tm_ans_key() {
  case "$1" in
    *[!A-Za-z0-9._-]* | "") tm_die "$TM_EXIT_USAGE" "invalid answer key: '$1'" ;;
  esac
  local mapped
  mapped="$(printf '%s' "$1" | tr 'a-z.-' 'A-Z__')"
  printf 'TM_ANS_%s\n' "$mapped"
}
tm_ans_set() { local k; k="$(tm_ans_key "$1")"; printf -v "$k" '%s' "$2"; }
tm_ans_get() { local k; k="$(tm_ans_key "$1")"; eval "printf '%s' \"\${$k-}\""; }

# --- 输入通道 ---
tm_require_input_channel() {
  [[ "${TM_YES:-0}" == "1" ]] && return 0
  [[ -n "${TM_TTY:-}" ]] && return 0
  [[ -t 0 ]] && return 0
  [[ "${TM_ONECLICK_ALLOW_STDIN:-0}" == "1" ]] && return 0
  tm_die "$TM_EXIT_PREFLIGHT" "no interactive input channel: run with --yes (or TM_ONECLICK_YES=1) plus explicit flags, or execute the script from a terminal"
}

tm_read_line() { # <prompt> <default>
  local prompt="$1" default="$2" reply=""
  tm_require_input_channel
  # 提示必须走 stderr：tm_read_line 的 stdout 就是「读到的值」，
  # 调用方普遍写成 value="$(tm_read_line ...)"，提示混进 stdout 会污染取值。
  printf '==> %s [%s]: ' "$prompt" "$default" >&2
  if [[ -n "$TM_TTY" ]]; then IFS= read -r reply <"$TM_TTY" || reply=""
  else IFS= read -r reply || reply=""; fi
  printf '%s' "${reply:-$default}"
}

# tm_read_secret：优先 /dev/tty 并用 read -s 关闭回显；回退 stdin 时无法关回显，
# 因此显式警告一次，避免 token 被 CI 日志录下来。
tm_read_secret() { # <prompt>
  local prompt="$1" reply=""
  tm_require_input_channel
  if [[ -n "$TM_TTY" ]]; then
    printf '==> %s（输入不回显）: ' "$prompt" >&2
    IFS= read -rs reply <"$TM_TTY" || reply=""
    printf '\n' >&2
  else
    tm_warn "no tty available; secret input will be echoed by the piped stdin"
    IFS= read -r reply || reply=""
  fi
  printf '%s' "$reply"
}

# --- 交互原语（纯函数 *_stdio 版本便于测试；对外版本叠加「已有答案/默认值」语义）---
tm_ask_stdio() { local key="$1" prompt="$2" default="$3"; tm_read_line "$prompt" "$default"; }
# tm_ask_http_port：server 角色用的语义化包装，等价于 tm_ask http_port "HTTP 监听端口" "8080"。
tm_ask_http_port() { tm_ask http_port "HTTP 监听端口" "8080"; tm_ans_get http_port; }
tm_ask() { # <key> <prompt> <default>
  local current; current="$(tm_ans_get "$1")"
  if [[ -n "$current" ]]; then return 0; fi
  if [[ "${TM_YES:-0}" == "1" ]]; then tm_ans_set "$1" "$3"; return 0; fi
  tm_ans_set "$1" "$(tm_ask_stdio "$1" "$2" "$3")"
}

tm_normalize_bool() {
  case "$(printf '%s' "$1" | tr 'A-Z' 'a-z')" in
    y | yes | true | 1 | on) printf 'yes\n' ;;
    n | no | false | 0 | off | "") printf 'no\n' ;;
    *) return 1 ;;
  esac
}
tm_ask_bool_stdio() { # <key> <prompt> <yes|no>
  local reply attempt
  for attempt in 1 2 3; do
    reply="$(tm_read_line "$2 (y/n)" "$3")"
    if tm_normalize_bool "$reply" >/dev/null; then tm_normalize_bool "$reply"; return 0; fi
    tm_warn "请输入 y 或 n"
  done
  tm_die "$TM_EXIT_USAGE" "invalid yes/no answer after 3 attempts"
}
tm_ask_bool() {
  local current; current="$(tm_ans_get "$1")"
  [[ -n "$current" ]] && return 0
  if [[ "${TM_YES:-0}" == "1" ]]; then tm_ans_set "$1" "$3"; return 0; fi
  tm_ans_set "$1" "$(tm_ask_bool_stdio "$1" "$2" "$3")"
}

tm_ask_choice_stdio() { # <key> <prompt> <default> <choice...>
  local key="$1" prompt="$2" default="$3"; shift 3
  local choices=("$@") reply attempt ok c
  for attempt in 1 2 3; do
    reply="$(tm_read_line "$prompt (${choices[*]})" "$default")"
    ok=0
    for c in "${choices[@]}"; do [[ "$reply" == "$c" ]] && ok=1 && break; done
    if [[ "$ok" == "1" ]]; then printf '%s\n' "$reply"; return 0; fi
    tm_warn "不在可选值内：${choices[*]}"
  done
  tm_die "$TM_EXIT_USAGE" "invalid choice after 3 attempts"
}
tm_ask_choice() {
  local current; current="$(tm_ans_get "$1")"
  [[ -n "$current" ]] && return 0
  if [[ "${TM_YES:-0}" == "1" ]]; then tm_ans_set "$1" "$3"; return 0; fi
  tm_ans_set "$1" "$(tm_ask_choice_stdio "$1" "$2" "$3" "${@:4}")"
}

tm_ask_secret() { # <key> <prompt>
  local first second attempt
  for attempt in 1 2 3; do
    first="$(tm_read_secret "$2")"
    second="$(tm_read_secret "$2（再次输入确认）")"
    if [[ "$first" == "$second" && -n "$first" ]]; then tm_ans_set "$1" "$first"; return 0; fi
    tm_warn "两次输入不一致或为空，请重试"
  done
  tm_die "$TM_EXIT_USAGE" "secret confirmation failed after 3 attempts"
}

# --- 敏感值：文件 > 环境变量 > 交互；--yes 下缺值直接失败 ---
TM_SECRET_ENV_FILE="${TM_SECRET_ENV_FILE:-}"
TM_TOKEN_FILE="${TM_TOKEN_FILE:-}"
tm_secret_loaded_get() { local k; k="$(tm_ans_key "secret.$1")"; eval "printf '%s' \"\${$k-}\""; }
tm_secret_loaded_set() { local k; k="$(tm_ans_key "secret.$1")"; printf -v "$k" '%s' "$2"; }
tm_load_secret_env_file() { # <path>
  local path="$1" perms line key value
  [[ -f "$path" ]] || tm_die "$TM_EXIT_USAGE" "secret env file not found: $path"
  perms="$(tm_file_perms "$path")"
  case "$perms" in
    600 | 400 | 640) ;;
    *) tm_die "$TM_EXIT_PREFLIGHT" "secret env file must be 0600/0400/0640, got 0${perms}: $path" ;;
  esac
  while IFS= read -r line || [[ -n "$line" ]]; do
    case "$line" in '' | \#*) continue ;; esac
    key="${line%%=*}"; value="${line#*=}"
    # 兼容单引号与双引号两种包裹（tm_env_quote 生成的是双引号）。
    value="${value%\'}"; value="${value#\'}"
    value="${value%\"}"; value="${value#\"}"
    tm_secret_loaded_set "$key" "$value"
  done <"$path"
}
tm_secret_value() { # <key> <prompt> <env-var-name>
  local key="$1" prompt="$2" envname="$3" value=""
  value="$(tm_secret_loaded_get "$envname")"
  if [[ -z "$value" && -n "$TM_TOKEN_FILE" && "$key" == *token* ]]; then
    [[ -f "$TM_TOKEN_FILE" ]] || tm_die "$TM_EXIT_USAGE" "token file not found: $TM_TOKEN_FILE"
    value="$(head -n 1 "$TM_TOKEN_FILE")"
  fi
  [[ -n "$value" ]] || value="${!envname:-}"
  if [[ -z "$value" ]]; then
    if [[ "${TM_YES:-0}" == "1" ]]; then
      tm_die "$TM_EXIT_PREFLIGHT" "missing required secret for '${key}': set ${envname}, pass --token-file/--secret-env-file, or drop --yes to type it"
    fi
    tm_ask_secret "$key" "$prompt"
    value="$(tm_ans_get "$key")"
  fi
  tm_ans_set "$key" "$value"
}

TM_YES="${TM_ONECLICK_YES:-0}"
TM_UNINSTALL=0 TM_NO_SERVICE=0 TM_NO_START=0 TM_NO_ENABLE=0 TM_NO_LINGER=0
TM_KEEP_CONFIG=0 TM_RECONFIGURE=0 TM_CREATE_RUN_USER=0
TM_MODE="" TM_TARGET_USER="" TM_RUN_USER="" TM_BIN_DIR="" TM_CONFIG_DIR="" TM_STATE_DIR=""
TM_VERSION="latest" TM_ARCHIVE=""

tm_validate_ws_url() { # <url> <required-suffix>
  local url="$1" suffix="$2"
  [[ "$url" =~ ^wss?://[^[:space:]]+$ ]] || tm_die "$TM_EXIT_USAGE" "server URL must start with ws:// or wss://: '$url'"
  [[ "$url" == *"$suffix" ]] || tm_die "$TM_EXIT_USAGE" "server URL must end with ${suffix}: '$url'"
}

# tm_parse_tunnel_spec <name:protocol:listen_host:listen_port:agent_id[:target_host:target_port]>
# listen 自身含冒号（host:port），因此不能用 `read -r a b c` 按冒号直读六个变量——
# 那样会把 listen 拆成两段并使后续字段整体错位。这里先切成数组再按位组装。
tm_parse_tunnel_spec() {
  local spec="$1" name protocol listen agent host port count
  local parts=()
  IFS=':' read -r -a parts <<<"$spec"
  count="${#parts[@]}"
  # 末尾空字段会被 read 丢弃，因此 socks5 的 `...:agent-a::` 只得到 6 段，取最小值 5。
  [[ "$count" -ge 5 ]] \
    || tm_die "$TM_EXIT_USAGE" "tunnel spec needs name:protocol:listen_host:listen_port:agent_id[:target_host:target_port]: '$spec'"
  name="${parts[0]}" protocol="${parts[1]}" listen="${parts[2]}:${parts[3]}" agent="${parts[4]}"
  host="${parts[5]-}" port="${parts[6]-}"
  [[ -n "$name" && -n "$protocol" && -n "$agent" ]] \
    || tm_die "$TM_EXIT_USAGE" "tunnel spec needs non-empty name, protocol and agent_id: '$spec'"
  case "$protocol" in tcp | udp | http | socks5) ;; *) tm_die "$TM_EXIT_USAGE" "unknown protocol: '$protocol' (tcp|udp|http|socks5)" ;; esac
  [[ -n "${parts[2]}" ]] || tm_die "$TM_EXIT_USAGE" "listen host must not be empty: '$spec'"
  tm_validate_port "${parts[3]}" "listen port"
  if [[ "$protocol" != "socks5" ]]; then
    [[ -n "$host" && -n "$port" ]] || tm_die "$TM_EXIT_USAGE" "protocol ${protocol} requires target_host and target_port"
    tm_validate_port "$port" "target port"
  else
    host=""; port=""
  fi
  printf '%s|%s|%s|%s|%s|%s\n' "$name" "$protocol" "$listen" "$agent" "$host" "$port"
}

tm_validate_port() { # <port> <label>
  [[ "$1" =~ ^[0-9]+$ ]] || tm_die "$TM_EXIT_USAGE" "$2 must be numeric: '$1'"
  if [[ "$1" -lt 1 || "$1" -gt 65535 ]]; then tm_die "$TM_EXIT_USAGE" "$2 out of range 1-65535: '$1'"; fi
}

# tm_parse_common_args：解析通用 flag。角色入口先调用它，剩余的 "$@" 交给 tm_role_parse_args。
tm_parse_common_args() {
  # TM_ONECLICK_YES 可能在库加载之后才由调用方注入（例如 `TM_ONECLICK_YES=1 bash -c ...`），
  # 因此每次解析都重新同步一次，保证环境变量与 --yes 等价。
  TM_YES="${TM_ONECLICK_YES:-0}"
  TM_REMAINING_ARGS=()
  TM_TUNNEL_SPECS=()
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --version) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --version"; TM_VERSION="$2"; tm_ans_set version "$2"; shift 2 ;;
      --base-url) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --base-url"; TM_BASE_URL="$2"; shift 2 ;;
      --raw-base-url) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --raw-base-url"; TM_RAW_BASE_URL="$2"; shift 2 ;;
      --github-token) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --github-token"; TM_GITHUB_TOKEN="$2"; shift 2 ;;
      --archive) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --archive"; TM_ARCHIVE="$2"; shift 2 ;;
      --mode) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --mode"
        case "$2" in user | system) TM_MODE="$2" ;; *) tm_die "$TM_EXIT_USAGE" "invalid --mode: '$2' (user|system)" ;; esac; shift 2 ;;
      --user) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --user"; TM_TARGET_USER="$2"; shift 2 ;;
      --run-user) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --run-user"; TM_RUN_USER="$2"; shift 2 ;;
      --run-group) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --run-group"; TM_RUN_GROUP="$2"; shift 2 ;;
      --create-run-user) TM_CREATE_RUN_USER=1; shift ;;
      --bin-dir) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --bin-dir"; TM_BIN_DIR="$2"; shift 2 ;;
      --config-dir) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --config-dir"; TM_CONFIG_DIR="$2"; shift 2 ;;
      --state-dir) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --state-dir"; TM_STATE_DIR="$2"; shift 2 ;;
      --token-file) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --token-file"; TM_TOKEN_FILE="$2"; shift 2 ;;
      --secret-env-file) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --secret-env-file"; TM_SECRET_ENV_FILE="$2"; shift 2 ;;
      --server-url) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --server-url"; tm_ans_set server_url "$2"; shift 2 ;;
      --agent-id) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --agent-id"; tm_ans_set agent_id "$2"; shift 2 ;;
      --tunnel) [[ $# -ge 2 ]] || tm_die "$TM_EXIT_USAGE" "missing value for --tunnel"; TM_TUNNEL_SPECS+=("$2"); shift 2 ;;
      --yes) TM_YES=1; shift ;;
      --uninstall) TM_UNINSTALL=1; shift ;;
      --no-service) TM_NO_SERVICE=1; shift ;;
      --no-start) TM_NO_START=1; shift ;;
      --no-enable) TM_NO_ENABLE=1; shift ;;
      --no-linger) TM_NO_LINGER=1; shift ;;
      --no-cache) TM_NO_CACHE=1; shift ;;
      --keep-config) TM_KEEP_CONFIG=1; shift ;;
      --reconfigure) TM_RECONFIGURE=1; shift ;;
      -h | --help) tm_usage; exit "$TM_EXIT_OK" ;;
      --) shift; break ;;
      *) tm_die "$TM_EXIT_USAGE" "unknown argument: $1（--help 查看用法）" ;;
    esac
  done
  TM_REMAINING_ARGS=("$@")
}
# tm_parse_common_args_stdout [--print <field>] <args...>：解析后打印指定字段，供测试断言，
# 避免测试直接依赖全局变量。
tm_parse_common_args_stdout() {
  local field="version"
  if [[ "${1:-}" == "--print" ]]; then field="$2"; shift 2; fi
  tm_parse_common_args "$@"
  case "$field" in
    version) printf '%s\n' "$TM_VERSION" ;;
    yes) printf '%s\n' "$TM_YES" ;;
    mode) printf '%s\n' "$TM_MODE" ;;
    base-url) printf '%s\n' "$TM_BASE_URL" ;;
    *) tm_die "$TM_EXIT_USAGE" "unknown --print field: $field" ;;
  esac
}

# tm_file_perms <path> → 三位八进制。GNU stat 用 -c，BSD/macOS stat 用 -f。
tm_file_perms() {
  if stat -c '%a' "$1" >/dev/null 2>&1; then stat -c '%a' "$1"; else stat -f '%Lp' "$1"; fi
}

# --- YAML / env / XML 渲染 ---
tm_yaml_quote() {
  local v="$1" q="'"
  # bash 3.2 下 ${v//\'/\'\'} 会把反斜杠当字面量留下（得到 a\'\'b），
  # 因此先把单引号放进变量再做替换。
  printf "'%s'\n" "${v//$q/$q$q}"
}

TM_ENV_KEYS=() TM_ENV_VALUES=()
tm_env_clear() { TM_ENV_KEYS=(); TM_ENV_VALUES=(); }
tm_env_set() {
  local i
  for i in "${!TM_ENV_KEYS[@]}"; do
    if [[ "${TM_ENV_KEYS[$i]}" == "$1" ]]; then TM_ENV_VALUES[$i]="$2"; return 0; fi
  done
  TM_ENV_KEYS+=("$1"); TM_ENV_VALUES+=("$2")
}
tm_env_get() {
  local i
  for i in "${!TM_ENV_KEYS[@]}"; do
    [[ "${TM_ENV_KEYS[$i]}" == "$1" ]] && { printf '%s' "${TM_ENV_VALUES[$i]}"; return 0; }
  done
  return 0
}
tm_render_env_file() {
  local i
  printf '# 本文件由 TunnelMesh 一键安装脚本生成，包含敏感值，请勿提交版本库。\n'
  printf '# 权限应保持在 0600（user 模式）或 0640 root:<运行组>（system 模式）。\n'
  for i in "${!TM_ENV_KEYS[@]}"; do
    printf '%s=%s\n' "${TM_ENV_KEYS[$i]}" "$(tm_env_quote "${TM_ENV_VALUES[$i]}")"
  done
}

# tm_env_quote：EnvironmentFile 的值统一用双引号包裹并转义 \ 与 "。
# 不能沿用 YAML 的单引号翻倍：systemd 的单引号串里没有转义机制，
# 值里出现 ' 时会原样留下两个引号，得到错误的 token。
# 双引号下 systemd 按 C 风格转义解析；launchd/WinSW 走 XML 渲染，互不影响。
tm_env_quote() {
  local v="$1"
  # 反斜杠必须写成字面量 `\\` 作为模式：bash 会把展开后的单个 \ 当作未完成的转义而不匹配。
  # 顺序不可颠倒——先转义反斜杠，再转义双引号，否则新加的反斜杠会被二次转义。
  v="${v//\\/\\\\}"
  v="${v//\"/\\\"}"
  printf '"%s"' "$v"
}

tm_xml_escape() {
  local v="$1"
  v="${v//&/&amp;}"; v="${v//</&lt;}"; v="${v//>/&gt;}"; v="${v//\"/&quot;}"
  printf '%s' "$v"
}
tm_render_plist_environment() {
  [[ ${#TM_ENV_KEYS[@]} -eq 0 ]] && return 0
  local i
  printf '  <key>EnvironmentVariables</key>\n  <dict>\n'
  for i in "${!TM_ENV_KEYS[@]}"; do
    printf '    <key>%s</key>\n    <string>%s</string>\n' \
      "$(tm_xml_escape "${TM_ENV_KEYS[$i]}")" "$(tm_xml_escape "${TM_ENV_VALUES[$i]}")"
  done
  printf '  </dict>\n'
}
tm_render_winsw_env_block() {
  [[ ${#TM_ENV_KEYS[@]} -eq 0 ]] && return 0
  local i
  for i in "${!TM_ENV_KEYS[@]}"; do
    printf '  <env name="%s" value="%s" />\n' \
      "$(tm_xml_escape "${TM_ENV_KEYS[$i]}")" "$(tm_xml_escape "${TM_ENV_VALUES[$i]}")"
  done
}

# tm_render_template 用逐字符字面替换而不是 sed：Windows 路径里的反斜杠、
# XML 里的 & 都会被 sed 当转义/反向引用吃掉（windows-install.ps1 里踩过同一个坑）。
tm_render_template() {
  local template="$1"; shift
  local content; content="$(cat "$template")"
  while [[ $# -gt 0 ]]; do
    local ph="$1" val="$2"; shift 2
    content="${content//$ph/$val}"
  done
  printf '%s\n' "$content"
}

tm_render_systemd_user_unit() { # <role> <template> <binary> <config> <env-file> <state-dir>
  local role="$1" template="$2"
  tm_render_template "$template" \
    "__BINARY__" "$3" "__CONFIG__" "$4" "__ENV_FILE__" "$5" "__STATE_DIR__" "$6"
}

# tm_render_systemd_dropin：系统模式下只在用户选择了非默认值时生成，
# 用 ExecStart=/ExecStartPre= 清空再重写的标准做法覆盖路径与账户，
# 不修改 deploy/systemd/ 里的原始单元（单一模板来源约定）。
tm_render_systemd_dropin() { # <role> <binary> <config> <env-file> <state-dir> <run-user> <run-group>
  local role="$1" binary="$2" config="$3" envfile="$4" state="$5" user="$6" group="$7"
  printf '# 由一键安装脚本生成：覆盖 %s 单元中与默认值不同的部分。\n' "tunnelmesh-${role}.service"
  printf '# 手工修改请编辑本文件，不要改 /etc/systemd/system/tunnelmesh-%s.service。\n' "$role"
  printf '[Service]\n'
  printf 'User=%s\nGroup=%s\n' "$user" "$group"
  printf 'WorkingDirectory=%s\nReadWritePaths=%s\n' "$state" "$state"
  printf 'EnvironmentFile=-%s\n' "$envfile"
  printf 'ExecStartPre=\nExecStart=\n'
  if [[ "$role" == "server" ]]; then
    printf 'ExecStartPre=+%s --config %s init-node-id\n' "$binary" "$config"
  fi
  printf 'ExecStartPre=%s --config %s check-config\n' "$binary" "$config"
  printf 'ExecStart=%s --config %s run\n' "$binary" "$config"
}

# --- 落盘：先备份再覆盖，最多保留最近 3 份 ---
TM_BACKUP_TIMESTAMP=""
tm_backup_and_overwrite() { # <dest> <mode> <content-file>
  local dest="$1" mode="$2" content="$3" ts oldest
  mkdir -p "$(dirname "$dest")"
  if [[ -e "$dest" ]]; then
    ts="${TM_BACKUP_TIMESTAMP:-$(date -u +%Y%m%dT%H%M%SZ)}"
    cp -p -- "$dest" "${dest}.bak-${ts}"
    # shellcheck disable=SC2012  # ls -1t 用于按时间排序，find -printf 在 macOS 不可用
    oldest="$(ls -1t "${dest}".bak-* 2>/dev/null | tail -n +4)"
    if [[ -n "$oldest" ]]; then printf '%s\n' "$oldest" | while IFS= read -r f; do rm -f -- "$f"; done; fi
  fi
  install -m "$mode" "$content" "$dest"
}
tm_write_file() { # <dest> <mode> <content-file>
  mkdir -p "$(dirname "$1")"
  install -m "$2" "$3" "$1"
}
tm_file_owner_set() { # <path> <user> <group>
  if [[ -n "$2" || -n "$3" ]]; then chown "${2:-}:${3:-}" "$1" 2>/dev/null || tm_warn "cannot chown $1 to ${2:-}:${3:-}"; fi
}

# --- 角色 YAML 渲染：只输出用户显式回答过的键，空值一律不写，避免覆盖配置模型默认值 ---
tm_yaml_line() { # <indent> <key> <value>
  [[ -z "$3" ]] && return 0
  printf '%s%s: %s\n' "$1" "$2" "$(tm_yaml_quote "$3")"
}
tm_yaml_bool() { # <indent> <key> <yes|no>
  case "$3" in yes) printf '%s%s: true\n' "$1" "$2" ;; no) printf '%s%s: false\n' "$1" "$2" ;; esac
}
tm_yaml_list() { # <indent> <key> <comma-separated-values>
  local indent="$1" key="$2" csv="$3" item
  local items=()
  [[ -z "$csv" ]] && return 0
  printf '%s%s:\n' "$indent" "$key"
  IFS=',' read -r -a items <<<"$csv"
  for item in "${items[@]}"; do
    item="$(printf '%s' "$item" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
    [[ -n "$item" ]] && printf '%s  - %s\n' "$indent" "$(tm_yaml_quote "$item")"
  done
}

tm_render_server_yaml() {
  local a=tm_ans_get
  printf '# 由 TunnelMesh 一键安装脚本生成。敏感值不在此文件，见同目录 env / plist / 服务 XML。\n'
  printf 'mode: %s\n\n' "$($a mode)"
  printf 'storage:\n'
  tm_yaml_line "  " "driver" "$($a storage_driver)"
  tm_yaml_bool "  " "auto_init" "$($a auto_init)"
  if [[ "$($a storage_driver)" == "sqlite" ]]; then
    printf '  sqlite:\n'; tm_yaml_line "    " "path" "$($a sqlite_path)"
  else
    printf '  mysql:\n'; tm_yaml_bool "    " "tls" "$($a mysql_tls)"
    printf '    # dsn 由 TUNNELMESH_STORAGE_MYSQL_DSN 注入，不要写进本文件。\n'
  fi
  printf '\nregistry:\n'; tm_yaml_line "  " "type" "$($a registry_type)"
  tm_yaml_list "  " "endpoints" "$($a registry_endpoints)"
  printf '\nserver:\n'
  tm_yaml_line "  " "http_addr" "$($a http_addr)"
  tm_yaml_line "  " "dynamic_suffix" "$($a dynamic_suffix)"
  printf '  tcp_bridge:\n    enabled: true\n'
  printf '  webssh:\n'; tm_yaml_bool "    " "enabled" "$($a webssh_enabled)"
  if [[ "$($a proxy_entry_enabled)" == "yes" ]]; then
    printf '  proxy_entry:\n    enabled: true\n'
    tm_yaml_line "    " "listen" "$($a proxy_entry_listen)"
    tm_yaml_line "    " "domain_suffix" "$($a proxy_entry_domain_suffix)"
  fi
  if [[ "$($a relay_enabled)" == "yes" ]]; then
    printf '  relay:\n    enabled: true\n'
    tm_yaml_line "    " "listen" "$($a relay_listen)"
    tm_yaml_line "    " "endpoint" "$($a relay_endpoint)"
    tm_yaml_line "    " "ca" "$($a relay_ca)"
    tm_yaml_line "    " "cert" "$($a relay_cert)"
    tm_yaml_line "    " "key" "$($a relay_key)"
    tm_yaml_line "    " "server_name" "$($a relay_server_name)"
    printf '    # node_token 由 TUNNELMESH_SERVER_RELAY_NODE_TOKEN 注入。\n'
  fi
  printf '\nsecurity:\n'
  tm_yaml_list "  " "allowed_hosts" "$($a allowed_hosts)"
  tm_yaml_list "  " "allowed_origins" "$($a allowed_origins)"
  printf '\ntls:\n'; tm_yaml_bool "  " "enabled" "$($a tls_enabled)"
  # node.id 正常由 `tunnelmesh-server init-node-id` 在服务启动前生成并回写；
  # 只有在用户显式给定（例如集群里要固定节点名）时才渲染，否则留空交给 init-node-id。
  if [[ -n "$($a node_id)" ]]; then
    printf '\nnode:\n'; tm_yaml_line "  " "id" "$($a node_id)"
  fi
  printf '\ndownloads:\n'; tm_yaml_line "  " "github_repository" "$TM_REPOSITORY"
  # 敏感值只进 env 载体，绝不进 YAML。「哪个秘密走哪个环境变量」集中在渲染函数里定义，
  # 入口脚本不再各写一份，避免三份实现漂移。
  [[ -n "$($a mysql_dsn)" ]] && tm_env_set TUNNELMESH_STORAGE_MYSQL_DSN "$($a mysql_dsn)"
  [[ -n "$($a relay_node_token)" ]] && tm_env_set TUNNELMESH_SERVER_RELAY_NODE_TOKEN "$($a relay_node_token)"
  [[ -n "$($a identity_key)" ]] && tm_env_set TUNNELMESH_TOKEN_ENCRYPTION_KEY "$($a identity_key)"
  if [[ -n "$($a trace_signing_key)" ]]; then
    tm_env_set TUNNELMESH_TRACE_SIGNING_KEY "$($a trace_signing_key)"
    tm_env_set TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID "default"
  fi
  return 0
}

tm_render_agent_yaml() {
  local a=tm_ans_get
  printf '# 由 TunnelMesh 一键安装脚本生成。agent.token 的 YAML 键被标记为不可序列化，\n'
  printf '# 只能通过 TUNNELMESH_AGENT_TOKEN 注入（见同目录 agent.env / plist / 服务 XML）。\n'
  printf 'mode: local\n\nagent:\n'
  tm_yaml_line "  " "server_url" "$($a server_url)"
  tm_yaml_line "  " "id" "$($a agent_id)"
  tm_yaml_line "  " "instance_id" "$($a instance_id)"
  # 两个值都为空时整块省略：`min:`（空标量）会被解析成 null，既难读也可能与默认值冲突。
  if [[ -n "$($a conn_min)$($a conn_max)" ]]; then
    printf '  connections:\n'
    tm_yaml_line "    " "min" "$($a conn_min)"
    tm_yaml_line "    " "max" "$($a conn_max)"
  fi
  if [[ -n "$($a metadata_name)" ]]; then
    printf '  metadata:\n    - name: %s\n      source: %s\n' \
      "$(tm_yaml_quote "$($a metadata_name)")" "$(tm_yaml_quote "$($a metadata_source)")"
    tm_yaml_line "      " "path" "$($a metadata_path)"
    tm_yaml_line "      " "key" "$($a metadata_key)"
  fi
  # agent.token 的 YAML 键是 `yaml:"-"`，只能由环境变量注入，因此必须落进 env 载体。
  [[ -n "$($a agent_token)" ]] && tm_env_set TUNNELMESH_AGENT_TOKEN "$($a agent_token)"
  return 0
}

tm_render_client_yaml() {
  local a=tm_ans_get spec name protocol listen agent host port auth idx=0
  printf '# 由 TunnelMesh 一键安装脚本生成。client.token 只能通过 TUNNELMESH_CLIENT_TOKEN 注入。\n'
  printf 'mode: local\n\nclient:\n'
  tm_yaml_line "  " "server_url" "$($a server_url)"
  tm_yaml_line "  " "instance_id_path" "$($a instance_id_path)"
  if [[ ${#TM_TUNNEL_SPECS[@]} -gt 0 ]]; then
    printf '  tunnels:\n'
    for spec in "${TM_TUNNEL_SPECS[@]}"; do
      IFS='|' read -r name protocol listen agent host port <<<"$(tm_parse_tunnel_spec "$spec")"
      printf '    - name: %s\n      protocol: %s\n      listen: %s\n      agent_id: %s\n' \
        "$(tm_yaml_quote "$name")" "$(tm_yaml_quote "$protocol")" "$(tm_yaml_quote "$listen")" "$(tm_yaml_quote "$agent")"
      if [[ -n "$host" ]]; then
        printf '      target_host: %s\n      target_port: %s\n' "$(tm_yaml_quote "$host")" "$port"
      fi
      # 认证设置与转发一一对应，存在平行数组里：loopback 转发留空即走配置默认值，
      # 只有非 loopback 监听才会写入 auth_mode/allow_remote（config 校验强制要求）。
      auth=""
      if [[ ${#TM_TUNNEL_AUTH[@]} -gt $idx ]]; then auth="${TM_TUNNEL_AUTH[$idx]}"; fi
      if [[ -n "$auth" ]]; then
        printf '      auth_mode: %s\n' "$(tm_yaml_quote "${auth%%|*}")"
        if [[ "${auth##*|}" == "yes" ]]; then printf '      allow_remote: true\n'; else printf '      allow_remote: false\n'; fi
      fi
      idx=$((idx + 1))
    done
  fi
  [[ -n "$($a client_token)" ]] && tm_env_set TUNNELMESH_CLIENT_TOKEN "$($a client_token)"
  return 0
}

# --- 身份与模式 ---
tm_euid() { id -u; }
tm_is_root() { [[ "$(tm_euid)" == "0" ]]; }

tm_resolve_target_user() {
  if [[ -n "$TM_TARGET_USER" ]]; then printf '%s\n' "$TM_TARGET_USER"; return 0; fi
  if tm_is_root && [[ -n "${SUDO_USER:-}" && "${SUDO_USER}" != "root" ]]; then
    printf '%s\n' "$SUDO_USER"; return 0
  fi
  id -un
}

tm_resolve_install_mode_stdout() {
  local mode="$TM_MODE"
  if [[ -z "$mode" ]]; then
    if [[ "$TM_OS_FAMILY" != "linux" ]]; then mode="user"
    elif ! tm_is_root; then mode="$TM_DEFAULT_INSTALL_MODE"
    elif [[ -n "${SUDO_USER:-}" && "${SUDO_USER}" != "root" ]]; then mode="user"
    else mode="system"; fi
  fi
  printf '%s\n' "$mode"
}

tm_resolve_install_mode() {
  TM_MODE="$(tm_resolve_install_mode_stdout)"
  # 顺序很重要：显式要求 system 却不是 root，是可操作的用户错误，必须先报出来；
  # 否则在非 Linux 平台会被静默降级成 user，用户看不到「需要 sudo」这个真正的阻塞原因。
  if [[ "$TM_MODE" == "system" ]] && ! tm_is_root; then
    tm_die "$TM_EXIT_PREFLIGHT" "--mode system requires root; re-run with sudo, or use the default user mode"
  fi
  if [[ "$TM_OS_FAMILY" != "linux" && "$TM_MODE" == "system" ]]; then
    tm_warn "system 模式只在 Linux 生效；$(uname -s) 上使用用户级服务托管"
    TM_MODE="user"
  fi
  TM_TARGET_USER="$(tm_resolve_target_user)"
  if [[ -z "$TM_RUN_USER" ]]; then TM_RUN_USER="tunnelmesh"; fi
  if [[ -z "$TM_RUN_GROUP" ]]; then TM_RUN_GROUP="$TM_RUN_USER"; fi
  # 配置文件属主：system 模式下 env 文件里是 token/DSN，必须 root:<运行组> 0640，
  # 让服务账户读得到而其它账户读不到；user 模式下一律属于当前用户。
  if [[ "$TM_MODE" == "system" && "$TM_OS_FAMILY" == "linux" ]]; then
    TM_CONFIG_OWNER="root"; TM_CONFIG_GROUP="$TM_RUN_GROUP"
  else
    TM_CONFIG_OWNER="$(id -un)"; TM_CONFIG_GROUP="$(id -gn)"
  fi
  if tm_is_root && [[ -n "${SUDO_USER:-}" && "${SUDO_USER}" != "root" && "$TM_MODE" == "user" ]]; then
    tm_info "检测到 sudo：将以 ${TM_TARGET_USER} 身份做用户级安装；需要系统级请加 --mode system"
  fi
}

tm_reexec_as_user() { # <user> <args...>
  local user="$1"; shift
  [[ "${TM_ONECLICK_REEXEC:-0}" == "1" ]] && tm_die "$TM_EXIT_PREFLIGHT" "refusing to re-exec twice (TM_ONECLICK_REEXEC already set)"
  [[ -n "$TM_ONECLICK_ENTRY" ]] || tm_die "$TM_EXIT_PREFLIGHT" "cannot re-exec: entry script path unknown (bash -c/管道调用请改用目标用户直接执行)"
  if tm_have runuser; then
    TM_ONECLICK_REEXEC=1 runuser -u "$user" -- "$TM_ONECLICK_ENTRY" "$@"
  elif tm_have sudo; then
    TM_ONECLICK_REEXEC=1 sudo -u "$user" -H "$TM_ONECLICK_ENTRY" "$@"
  else
    tm_die "$TM_EXIT_PREFLIGHT" "need runuser or sudo to install for another user"
  fi
}

# --- 路径推导 ---
tm_default_bin_dir() {
  if [[ -n "$TM_BIN_DIR" ]]; then printf '%s\n' "$TM_BIN_DIR"
  elif [[ "$TM_OS_FAMILY" == "linux" && "$TM_MODE" == "system" ]]; then printf '/usr/local/bin\n'
  else printf '%s/.local/bin\n' "$HOME"; fi
}
tm_default_config_dir() {
  if [[ -n "$TM_CONFIG_DIR" ]]; then printf '%s\n' "$TM_CONFIG_DIR"
  elif [[ "$TM_OS_FAMILY" == "linux" && "$TM_MODE" == "system" ]]; then printf '/etc/tunnelmesh\n'
  else printf '%s/.config/tunnelmesh\n' "$HOME"; fi
}
tm_default_state_dir() {
  if [[ -n "$TM_STATE_DIR" ]]; then printf '%s\n' "$TM_STATE_DIR"
  elif [[ "$TM_OS_FAMILY" == "linux" && "$TM_MODE" == "system" ]]; then
    case "$TM_ROLE" in server) printf '/var/lib/tunnelmesh\n' ;; *) printf '/var/lib/tunnelmesh-%s\n' "$TM_ROLE" ;; esac
  else printf '%s/.local/share/tunnelmesh\n' "$HOME"; fi
}
tm_binary_path() { printf '%s/tunnelmesh-%s\n' "$(tm_default_bin_dir)" "$1"; }
tm_config_path() { printf '%s/%s.yaml\n' "$(tm_default_config_dir)" "$1"; }
tm_env_path() { printf '%s/%s.env\n' "$(tm_default_config_dir)" "$1"; }
tm_unit_path() {
  # 按「操作系统 + 安装模式」推导，而不是按 TM_SERVICE_MANAGER：
  # 单元路径在生成配置阶段就要用到，那时服务管理器可能还没探测（或被 --no-service 关成 none）。
  case "$TM_OS_FAMILY" in
    darwin) printf '%s/Library/LaunchAgents/com.tunnelmesh.%s.plist\n' "$HOME" "$1" ;;
    linux)
      if [[ "$TM_MODE" == "system" ]]; then
        printf '/etc/systemd/system/tunnelmesh-%s.service\n' "$1"
      else
        printf '%s/.config/systemd/user/tunnelmesh-%s.service\n' "$HOME" "$1"
      fi ;;
    *) printf '\n' ;;
  esac
}

# --- 服务管理器探测 ---
tm_has_systemd() { tm_have systemctl && [[ -d /run/systemd/system ]]; }
tm_has_systemd_user() { tm_has_systemd && systemctl --user show-environment >/dev/null 2>&1; }
# tm_detect_service_manager_stdout：纯解析，便于测试。
# TM_ONECLICK_SERVICE_MANAGER 是显式注入接缝：容器与 CI 里 /run/systemd/system 常常不存在，
# 没有它端到端测试就只能覆盖 none 分支。
tm_detect_service_manager_stdout() {
  if [[ -n "${TM_ONECLICK_SERVICE_MANAGER:-}" ]]; then printf '%s\n' "$TM_ONECLICK_SERVICE_MANAGER"; return 0; fi
  if [[ "$TM_NO_SERVICE" == "1" ]]; then printf 'none\n'; return 0; fi
  case "$TM_OS_FAMILY" in
    darwin) tm_have launchctl && printf 'launchd\n' || printf 'none\n' ;;
    linux)
      if [[ "$TM_MODE" == "system" ]] && tm_has_systemd; then printf 'systemd-system\n'
      elif tm_has_systemd_user; then printf 'systemd-user\n'
      else printf 'none\n'; fi ;;
    *) printf 'none\n' ;;
  esac
}

tm_detect_service_manager() {
  TM_SERVICE_MANAGER="$(tm_detect_service_manager_stdout)"
  if [[ "$TM_SERVICE_MANAGER" == "none" && "$TM_NO_SERVICE" != "1" ]]; then
    tm_warn "未检测到可用的服务管理器，将只安装二进制与配置；前台启动命令见安装结束后的摘要"
  fi
}

# --- 服务生命周期：所有调用都经过 tm_svc，测试用 PATH 前置的 stub 覆盖 ---
tm_svc() { "$@"; }
TM_SERVICE_REGISTERED="${TM_SERVICE_REGISTERED:-0}"
TM_SERVICE_MANAGER="${TM_SERVICE_MANAGER:-none}"
tm_service_install() { # <role>
  local role="$1" unit
  TM_SERVICE_REGISTERED=1
  case "$TM_SERVICE_MANAGER" in
    systemd-user) tm_svc systemctl --user daemon-reload ;;
    systemd-system) tm_svc systemctl daemon-reload ;;
    launchd)
      unit="$(tm_unit_path "$role")"
      tm_svc plutil -lint "$unit" || tm_die "$TM_EXIT_CONFIG" "plutil -lint failed for ${unit}"
      tm_svc launchctl bootout "gui/$(id -u)" "$unit" >/dev/null 2>&1 || true
      tm_svc launchctl bootstrap "gui/$(id -u)" "$(dirname "$unit")" \
        || tm_die "$TM_EXIT_SERVICE" "launchctl bootstrap failed for ${unit}" ;;
    none) TM_SERVICE_REGISTERED=0 ;;
    *) tm_die "$TM_EXIT_SERVICE" "unsupported service manager: $TM_SERVICE_MANAGER" ;;
  esac
}
tm_service_enable() { # <role>
  [[ "$TM_SERVICE_REGISTERED" == "1" ]] || return 0
  case "$TM_SERVICE_MANAGER" in
    systemd-user)
      tm_svc systemctl --user enable "tunnelmesh-$1.service" || tm_die "$TM_EXIT_SERVICE" "systemctl --user enable failed"
      if [[ "$TM_NO_LINGER" != "1" ]] && tm_have loginctl; then
        tm_svc loginctl enable-linger "$TM_TARGET_USER" \
          || tm_warn "loginctl enable-linger 失败：注销后服务会被停止，可手工执行 loginctl enable-linger ${TM_TARGET_USER}"
      fi ;;
    systemd-system) tm_svc systemctl enable "tunnelmesh-$1.service" || tm_die "$TM_EXIT_SERVICE" "systemctl enable failed" ;;
    launchd) : ;;  # RunAtLoad + KeepAlive 已在 plist 内
  esac
}
tm_service_start() { # <role>
  [[ "$TM_SERVICE_REGISTERED" == "1" ]] || return 0
  case "$TM_SERVICE_MANAGER" in
    systemd-user) tm_svc systemctl --user restart "tunnelmesh-$1.service" || tm_die "$TM_EXIT_SERVICE" "start failed" ;;
    systemd-system) tm_svc systemctl restart "tunnelmesh-$1.service" || tm_die "$TM_EXIT_SERVICE" "start failed" ;;
    launchd) tm_svc launchctl kickstart -k "gui/$(id -u)/com.tunnelmesh.$1" || tm_die "$TM_EXIT_SERVICE" "kickstart failed" ;;
  esac
}
tm_service_stop() { # <role>
  case "$TM_SERVICE_MANAGER" in
    systemd-user) tm_svc systemctl --user stop "tunnelmesh-$1.service" >/dev/null 2>&1 || true ;;
    systemd-system) tm_svc systemctl stop "tunnelmesh-$1.service" >/dev/null 2>&1 || true ;;
    launchd) tm_svc launchctl bootout "gui/$(id -u)/com.tunnelmesh.$1" >/dev/null 2>&1 || true ;;
  esac
}
tm_service_remove() { # <role>
  local unit; unit="$(tm_unit_path "$1")"
  tm_service_stop "$1"
  case "$TM_SERVICE_MANAGER" in
    systemd-user) tm_svc systemctl --user disable "tunnelmesh-$1.service" >/dev/null 2>&1 || true; tm_svc systemctl --user daemon-reload || true ;;
    systemd-system)
      tm_svc systemctl disable "tunnelmesh-$1.service" >/dev/null 2>&1 || true
      rm -f "/etc/systemd/system/tunnelmesh-$1.service.d/oneclick.conf"
      rmdir "/etc/systemd/system/tunnelmesh-$1.service.d" 2>/dev/null || true
      rm -f "/etc/systemd/system/tunnelmesh-$1.service"
      tm_svc systemctl daemon-reload || true ;;
    launchd) rm -f "$unit" ;;
  esac
}
tm_service_status() { # <role>
  case "$TM_SERVICE_MANAGER" in
    systemd-user) tm_svc systemctl --user --no-pager --lines=10 status "tunnelmesh-$1.service" || true ;;
    systemd-system) tm_svc systemctl --no-pager --lines=10 status "tunnelmesh-$1.service" || true ;;
    launchd) tm_svc launchctl print "gui/$(id -u)/com.tunnelmesh.$1" || true ;;
    none) tm_warn "未注册服务" ;;
  esac
}
tm_service_logs_hint() {
  case "$TM_SERVICE_MANAGER" in
    systemd-user) printf 'journalctl --user -u tunnelmesh-%s.service -f\n' "$TM_ROLE" ;;
    systemd-system) printf 'journalctl -u tunnelmesh-%s.service -f\n' "$TM_ROLE" ;;
    launchd) printf 'tail -f %s/Library/Logs/tunnelmesh-%s.log\n' "$HOME" "$TM_ROLE" ;;
    none) printf '%s --config %s run\n' "$(tm_binary_path "$TM_ROLE")" "$(tm_config_path "$TM_ROLE")" ;;
  esac
}

tm_cleanup() {
  [[ -n "${TM_WORKDIR:-}" && -d "$TM_WORKDIR" ]] && rm -rf -- "$TM_WORKDIR"
  [[ -n "${TM_RENDER_TMP:-}" && -d "$TM_RENDER_TMP" ]] && rm -rf -- "$TM_RENDER_TMP"
  return 0
}

tm_preflight() {
  tm_detect_platform
  tm_tty_init
  tm_entry_init "${0:-}" "${BASH_SOURCE[0]:-}"
  tm_resolve_install_mode
  tm_detect_service_manager
  tm_require_cmds curl tar awk sed grep head install mktemp date
  TM_RENDER_TMP="$(mktemp -d)"
}

# tm_install_binary：install 到同目录临时名再 mv，避免替换期间进程读到半个文件。
tm_install_binary() { # <role>
  local role="$1" src dest tmp ts
  src="${TM_EXTRACT_DIR}/tunnelmesh-${role}"
  [[ -x "$src" ]] || tm_die "$TM_EXIT_DOWNLOAD" "release archive is missing tunnelmesh-${role}"
  dest="$(tm_binary_path "$role")"
  mkdir -p "$(dirname "$dest")"
  if [[ -e "$dest" ]]; then
    ts="$(date -u +%Y%m%dT%H%M%SZ)"
    cp -p -- "$dest" "${dest}.bak-${ts}"
    ls -1t "${dest}".bak-* 2>/dev/null | tail -n +2 | while IFS= read -r old; do rm -f -- "$old"; done
    TM_BINARY_BACKUP="${dest}.bak-${ts}"
  else
    TM_BINARY_BACKUP=""
  fi
  tmp="${dest}.tmp.$$"
  install -m 0755 "$src" "$tmp"
  mv -f -- "$tmp" "$dest"
  tm_info "installed ${dest}"
}

tm_rollback_binary() { # <role>
  local role="$1" dest; dest="$(tm_binary_path "$role")"
  [[ -n "${TM_BINARY_BACKUP:-}" && -f "$TM_BINARY_BACKUP" ]] || return 0
  tm_warn "回滚到 ${TM_BINARY_BACKUP}"
  tm_service_stop "$role"
  install -m 0755 "$TM_BINARY_BACKUP" "$dest"
  tm_service_start "$role" || true
}

tm_run_validate() { # <role>
  local role="$1" binary config
  binary="$(tm_binary_path "$role")"; config="$(tm_config_path "$role")"
  if [[ "$role" == "server" ]]; then
    # init-node-id 默认写 /var/lib/tunnelmesh/node-id，user 模式（非 root）下不可写，
    # 因此显式指到本角色的状态目录。node.id 会被回写进 YAML，之后 run 阶段直接复用，
    # 不会再碰默认路径——这也是 macOS LaunchAgent 下能跑 cluster 模式的原因。
    "$binary" --config "$config" --node-id-path "$(tm_default_state_dir)/node-id" init-node-id \
      || tm_die "$TM_EXIT_CONFIG" "init-node-id failed"
  fi
  if ! "$binary" --config "$config" check-config; then
    tm_die "$TM_EXIT_CONFIG" "check-config failed；请检查 ${config}（升级后 schema 不匹配见 docs/operations/schema-upgrades.md）"
  fi
}

tm_write_role_config() { # <role>
  local role="$1" config envfile yaml_tmp env_tmp mode_yaml mode_env
  config="$(tm_config_path "$role")"; envfile="$(tm_env_path "$role")"
  yaml_tmp="${TM_RENDER_TMP}/${role}.yaml"; env_tmp="${TM_RENDER_TMP}/${role}.env"
  tm_env_clear
  tm_role_render_config >"$yaml_tmp"
  if [[ "$TM_KEEP_CONFIG" == "1" && -f "$config" ]]; then
    tm_info "--keep-config：保留现有 ${config}"
  else
    if [[ "$TM_MODE" == "system" && "$TM_OS_FAMILY" == "linux" ]]; then mode_yaml=0640; else mode_yaml=0600; fi
    tm_backup_and_overwrite "$config" "$mode_yaml" "$yaml_tmp"
    tm_file_owner_set "$config" "$TM_CONFIG_OWNER" "$TM_CONFIG_GROUP"
  fi
  if [[ ${#TM_ENV_KEYS[@]} -gt 0 ]]; then
    tm_render_env_file >"$env_tmp"
    if [[ "$TM_MODE" == "system" && "$TM_OS_FAMILY" == "linux" ]]; then mode_env=0640; else mode_env=0600; fi
    tm_backup_and_overwrite "$envfile" "$mode_env" "$env_tmp"
    tm_file_owner_set "$envfile" "$TM_CONFIG_OWNER" "$TM_CONFIG_GROUP"
    tm_info "wrote secrets to ${envfile} (mode 0${mode_env})"
  fi
}

tm_register_service() { # <role>
  local role="$1" unit template
  unit="$(tm_unit_path "$role")"
  case "$TM_SERVICE_MANAGER" in
    systemd-user)
      template="${TM_EXTRACT_DIR}/deploy/systemd-user/tunnelmesh-${role}.service"
      [[ -f "$template" ]] || tm_die "$TM_EXIT_SERVICE" "release archive is missing ${template}"
      mkdir -p "$(dirname "$unit")"
      tm_render_systemd_user_unit "$role" "$template" "$(tm_binary_path "$role")" \
        "$(tm_config_path "$role")" "$(tm_env_path "$role")" "$(tm_default_state_dir)" >"${TM_RENDER_TMP}/unit"
      tm_write_file "$unit" 0644 "${TM_RENDER_TMP}/unit" ;;
    systemd-system)
      template="${TM_EXTRACT_DIR}/deploy/systemd/tunnelmesh-${role}.service"
      [[ -f "$template" ]] || tm_die "$TM_EXIT_SERVICE" "release archive is missing ${template}"
      tm_write_file "$unit" 0644 "$template"
      if [[ "$TM_RUN_USER" != "tunnelmesh" || "$TM_BIN_DIR" != "" || "$TM_CONFIG_DIR" != "" || "$TM_STATE_DIR" != "" ]]; then
        mkdir -p "/etc/systemd/system/tunnelmesh-${role}.service.d"
        tm_render_systemd_dropin "$role" "$(tm_binary_path "$role")" "$(tm_config_path "$role")" \
          "$(tm_env_path "$role")" "$(tm_default_state_dir)" "$TM_RUN_USER" "$TM_RUN_GROUP" \
          >"${TM_RENDER_TMP}/dropin"
        tm_write_file "/etc/systemd/system/tunnelmesh-${role}.service.d/oneclick.conf" 0644 "${TM_RENDER_TMP}/dropin"
      fi ;;
    launchd)
      template="${TM_EXTRACT_DIR}/deploy/macos/tunnelmesh.plist"
      [[ -f "$template" ]] || tm_die "$TM_EXIT_SERVICE" "release archive is missing ${template}"
      mkdir -p "$(dirname "$unit")" "$HOME/Library/Logs"
      tm_render_template "$template" \
        "__ROLE__" "$role" "__HOME__" "$HOME" \
        "__BINARY__" "$(tm_binary_path "$role")" "__CONFIG__" "$(tm_config_path "$role")" \
        >"${TM_RENDER_TMP}/plist"
      # __ENVIRONMENT__ 单独替换：它本身是多行 XML，不能走 sed。
      TM_ENV_BLOCK="$(tm_render_plist_environment)"
      tm_render_template "${TM_RENDER_TMP}/plist" "__ENVIRONMENT__" "$TM_ENV_BLOCK" >"${TM_RENDER_TMP}/plist.final"
      tm_write_file "$unit" 0600 "${TM_RENDER_TMP}/plist.final" ;;
    none) return 0 ;;
  esac
}

tm_uninstall() { # <role>
  local role="$1"
  tm_service_remove "$role" || tm_die "$TM_EXIT_UNINSTALL" "failed to remove service for ${role}"
  rm -f -- "$(tm_binary_path "$role")" "$(tm_binary_path "$role")".bak-*
  tm_info "已卸载 tunnelmesh-${role} 的二进制与服务定义；配置与数据保留："
  printf '  %s\n  %s\n  %s\n' "$(tm_config_path "$role")" "$(tm_env_path "$role")" "$(tm_default_state_dir)"
}

tm_summary() { # <role> <version>
  local role="$1" version="$2"
  printf '\n==> TunnelMesh %s %s 安装完成\n' "$role" "$version"
  printf '  二进制:   %s\n' "$(tm_binary_path "$role")"
  printf '  配置:     %s\n' "$(tm_config_path "$role")"
  [[ ${#TM_ENV_KEYS[@]} -gt 0 ]] && printf '  敏感值:   %s（%s）\n' "$(tm_env_path "$role")" "$(tm_env_masked_summary)"
  printf '  服务:     %s\n' "${TM_SERVICE_MANAGER}"
  printf '  查看日志: %s\n' "$(tm_service_logs_hint)"
  printf '  卸载:     重新运行本脚本并加 --uninstall\n'
  tm_role_post_install
}

tm_env_masked_summary() {
  local i out=""
  for i in "${!TM_ENV_KEYS[@]}"; do
    out="${out}${TM_ENV_KEYS[$i]}=$(tm_mask "${TM_ENV_VALUES[$i]}") "
  done
  printf '%s' "${out% }"
}

tm_main() {
  TM_REMAINING_ARGS=(); TM_TUNNEL_SPECS=()
  tm_parse_common_args "$@"
  # bash 3.2 + set -u 下展开空数组会报 unbound variable，必须用 ${arr[@]+...} 守卫。
  tm_role_parse_args ${TM_REMAINING_ARGS[@]+"${TM_REMAINING_ARGS[@]}"}
  trap tm_cleanup EXIT INT TERM
  tm_preflight
  tm_ensure_run_user
  if [[ "$TM_UNINSTALL" == "1" ]]; then
    tm_uninstall "$TM_ROLE"; exit "$TM_EXIT_OK"
  fi
  # 卸载分支必须在问答之前：卸载只需要角色与路径，问 Server URL / token 既多余又会
  # 在 --yes 下因为拿不到必填项而以退出码 3 失败，导致「装得上、卸不掉」。
  tm_role_prompts
  if [[ -n "$TM_ARCHIVE" ]]; then
    # tm_validate_version / tm_verify_checksum 内部是 tm_die（直接 exit），
    # 放在 `cmd || fallback` 里会把整个脚本带走，因此这里用显式 if 判断而不是 ||。
    if [[ "$TM_VERSION" =~ $TM_VERSION_PATTERN ]]; then :; else TM_VERSION="$(tm_version_from_archive "$TM_ARCHIVE")"; fi
    TM_WORKDIR="$(mktemp -d)"; TM_EXTRACT_DIR="${TM_WORKDIR}/extract"
    local_sums="$(dirname "$TM_ARCHIVE")/SHA256SUMS"
    if [[ -f "$local_sums" ]]; then
      tm_verify_checksum "$TM_ARCHIVE" "$local_sums" "$(basename "$TM_ARCHIVE")"
    else
      tm_warn "本地归档同目录没有 SHA256SUMS，跳过校验（--archive 模式）"
    fi
    tm_extract_archive "$TM_ARCHIVE" "$TM_EXTRACT_DIR"
  else
    tm_resolve_version
    tm_download_release "$TM_VERSION"
    TM_EXTRACT_DIR="${TM_WORKDIR}/extract"
    tm_extract_archive "$TM_ARCHIVE_PATH" "$TM_EXTRACT_DIR"
  fi
  local installed_version=""
  if [[ -x "$(tm_binary_path "$TM_ROLE")" ]]; then
    # set -o pipefail 下二进制非零退出会让整条流水线失败并触发 set -e，
    # 而「读不到旧版本号」本身不是错误，只是走全新安装分支。
    installed_version="$("$(tm_binary_path "$TM_ROLE")" --version 2>/dev/null | awk '{print $2}' || true)"
    tm_info "已安装 ${installed_version}，目标 ${TM_VERSION}：按升级流程处理（保留配置）"
  fi
  mkdir -p "$(tm_default_state_dir)" "$(tm_default_config_dir)"
  tm_chown_state
  tm_install_binary "$TM_ROLE"
  if [[ "$installed_version" == "" || "$TM_RECONFIGURE" == "1" ]]; then
    tm_write_role_config "$TM_ROLE"
  else
    tm_info "升级：保留现有配置（需要重新生成请加 --reconfigure）"
  fi
  tm_run_validate "$TM_ROLE" || { tm_rollback_binary "$TM_ROLE"; exit "$TM_EXIT_CONFIG"; }
  if [[ "$TM_NO_SERVICE" != "1" ]]; then
    tm_register_service "$TM_ROLE"
    tm_service_install "$TM_ROLE"
    [[ "$TM_NO_ENABLE" == "1" ]] || tm_service_enable "$TM_ROLE"
    if [[ "$TM_NO_START" == "1" ]]; then tm_info "--no-start：已注册但未启动"
    else tm_service_start "$TM_ROLE" || { tm_rollback_binary "$TM_ROLE"; exit "$TM_EXIT_SERVICE"; }; fi
  fi
  tm_service_status "$TM_ROLE"
  tm_summary "$TM_ROLE" "$TM_VERSION"
}

# tm_generate_secret_key：openssl 优先，回退 /dev/urandom；输出 base64 的 32 字节。
tm_generate_secret_key() {
  if tm_have openssl; then openssl rand -base64 32
  else head -c 32 /dev/urandom | base64 | tr -d '\n'; fi
}
# tm_default_instance_id_path：与配置模型的三平台默认值一致。
tm_default_instance_id_path() {
  case "$TM_OS_FAMILY" in
    darwin) printf '%s/Library/Application Support/TunnelMesh/client-instance-id\n' "$HOME" ;;
    linux)
      if [[ "$TM_MODE" == "system" ]]; then printf '/var/lib/tunnelmesh-client/client-instance-id\n'
      else printf '%s/.local/share/tunnelmesh/client-instance-id\n' "$HOME"; fi ;;
  esac
}
# tm_validate_listen <host:port>：校验监听地址，非 loopback 时要求显式同意并配认证。
# 结果写进 TM_LISTEN_ALLOW_REMOTE / TM_LISTEN_AUTH_MODE，由调用方（client 的转发问答循环）
# 存进与 TM_TUNNEL_SPECS 平行的数组——每条转发的认证设置彼此独立，不能共用全局答案。
tm_validate_listen() {
  local listen="$1" host="${1%:*}"
  TM_LISTEN_ALLOW_REMOTE="no"; TM_LISTEN_AUTH_MODE=""
  [[ "$listen" =~ ^[^:]+:[0-9]+$ ]] || tm_die "$TM_EXIT_USAGE" "listen 必须是 host:port，收到 '$listen'"
  tm_validate_port "${listen##*:}" "listen port"
  case "$host" in
    127.* | ::1 | localhost | "") return 0 ;;
  esac
  tm_warn "监听地址 ${listen} 不是 loopback：必须同时开启 allow_remote 并配置认证"
  # tm_ask* 对已有答案会直接返回，不清空就会把上一条转发的选择带到下一条。
  tm_ans_set auth_mode ""
  if [[ "${TM_ONECLICK_ALLOW_REMOTE:-0}" == "1" ]]; then
    # 非交互场景下无法「显式同意」，因此要求用环境变量把同意这件事写死在命令里。
    tm_ans_set allow_remote "yes"
  else
    tm_ans_set allow_remote ""
    tm_ask_bool allow_remote "允许非 loopback 监听" "no"
  fi
  [[ "$(tm_ans_get allow_remote)" == "yes" ]] || tm_die "$TM_EXIT_USAGE" "已取消：非 loopback 监听需要显式同意"
  tm_ask_choice auth_mode "认证方式（socks5 只支持 none|password）" "password" password none
  [[ "$(tm_ans_get auth_mode)" != "none" ]] || tm_die "$TM_EXIT_USAGE" "非 loopback 监听不允许 auth_mode=none"
  TM_LISTEN_ALLOW_REMOTE="yes"
  TM_LISTEN_AUTH_MODE="$(tm_ans_get auth_mode)"
}

# --- 编排所需的其余全局默认值 ---
# set -u 下任何未初始化的变量都会让脚本直接退出，因此所有跨函数共享的状态都在这里给默认值。
TM_CONFIG_OWNER="${TM_CONFIG_OWNER:-}"
TM_CONFIG_GROUP="${TM_CONFIG_GROUP:-}"
TM_RUN_GROUP="${TM_RUN_GROUP:-}"
TM_RENDER_TMP="${TM_RENDER_TMP:-}"
TM_BINARY_BACKUP="${TM_BINARY_BACKUP:-}"
TM_ENV_BLOCK="${TM_ENV_BLOCK:-}"
TM_LISTEN_ALLOW_REMOTE="${TM_LISTEN_ALLOW_REMOTE:-no}"
TM_LISTEN_AUTH_MODE="${TM_LISTEN_AUTH_MODE:-}"
TM_ONECLICK_REEXEC="${TM_ONECLICK_REEXEC:-0}"
# 与 TM_TUNNEL_SPECS 平行的认证设置数组，元素格式 "<auth_mode>|<yes|no>"。
TM_TUNNEL_AUTH=()

# tm_version_from_archive <archive-path>：从 tunnelmesh-<version>-<goos>-<goarch>.<ext>
# 里切出 <version>，用于 --archive 且未显式给 --version 的场景（离线安装、E2E 测试）。
tm_version_from_archive() {
  local base name
  base="$(basename "$1")"
  name="${base#tunnelmesh-}"
  case "$name" in
    v[0-9]*-*) printf '%s\n' "${name%%-*}" ;;
    *) tm_die "$TM_EXIT_USAGE" "cannot infer version from archive name: ${base}（请显式传 --version）" ;;
  esac
}

# tm_validate_metadata_name：与 internal/metadata 的 allowlist 规则保持一致。
# 名称命中敏感词时服务端会清空值并标记 redacted，因此在安装阶段就直接拒绝，
# 避免用户装完才发现 metadata 一直是空的。
tm_validate_metadata_name() {
  local name="$1"
  [[ "$name" =~ ^[A-Za-z0-9._-]+$ ]] \
    || tm_die "$TM_EXIT_USAGE" "metadata 名称只允许字母、数字、点、下划线和短横线：'$name'"
  if printf '%s' "$name" | grep -Eiq 'passw|passphrase|token|secret|private.?key|api.?key|credential|authorization|cookie|dsn'; then
    tm_die "$TM_EXIT_USAGE" "metadata 名称命中敏感词，服务端会拒收并标记 redacted：'$name'"
  fi
}

# tm_ensure_run_user：system 模式下保证运行账户存在。
# 不自动创建账户是默认行为——创建系统账户属于有副作用的变更，必须显式 --create-run-user。
tm_ensure_run_user() {
  [[ "$TM_MODE" == "system" && "$TM_OS_FAMILY" == "linux" ]] || return 0
  if id "$TM_RUN_USER" >/dev/null 2>&1; then return 0; fi
  if [[ "$TM_CREATE_RUN_USER" != "1" ]]; then
    tm_die "$TM_EXIT_PREFLIGHT" "运行账户 ${TM_RUN_USER} 不存在：加 --create-run-user 自动创建，或用 --run-user 指定已有账户"
  fi
  tm_info "创建系统账户 ${TM_RUN_USER}"
  if tm_have useradd; then
    useradd --system --home-dir /var/lib/tunnelmesh --shell /usr/sbin/nologin "$TM_RUN_USER" \
      || tm_die "$TM_EXIT_PREFLIGHT" "useradd 创建 ${TM_RUN_USER} 失败"
  elif tm_have adduser; then
    adduser --system --home /var/lib/tunnelmesh --shell /usr/sbin/nologin "$TM_RUN_USER" \
      || tm_die "$TM_EXIT_PREFLIGHT" "adduser 创建 ${TM_RUN_USER} 失败"
  else
    tm_die "$TM_EXIT_PREFLIGHT" "需要 useradd 或 adduser 才能创建 ${TM_RUN_USER}"
  fi
}

# tm_chown_state：system 模式下把状态目录交给运行账户，否则服务启动即因写不进去而失败。
tm_chown_state() {
  [[ "$TM_MODE" == "system" && "$TM_OS_FAMILY" == "linux" ]] || return 0
  tm_file_owner_set "$(tm_default_state_dir)" "$TM_RUN_USER" "$TM_RUN_GROUP"
}

# tm_usage：--help 输出。五种调用形态必须都列出来，其中 Homebrew 风格的
# `/bin/bash -c "$(curl ...)"` 与 `curl ... | bash -s --` 是用户最常照抄的两种，
# 各自的坑（$0 占位、curl 失败静默返回 0、stdin 不是终端）也要一并写清楚。
tm_usage() {
  printf 'TunnelMesh %s 一键安装脚本\n\n' "${TM_ROLE:-<role>}"
  cat <<'TMUSAGE'
用法
  install-<role>.sh [选项]

一行式安装（Linux / macOS）
  # 推荐：先落盘、审阅，再执行；curl 失败会立刻可见
  curl -fsSL https://raw.githubusercontent.com/nnworld/TunnelMesh/main/deploy/install/oneclick/install-<role>.sh -o /tmp/tm-install.sh
  less /tmp/tm-install.sh
  /bin/bash /tmp/tm-install.sh

  # Homebrew 风格命令替换（交互式安装最省事；交互提示从 /dev/tty 读）
  /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/nnworld/TunnelMesh/main/deploy/install/oneclick/install-<role>.sh)"
  # 传参注意：bash -c '<script>' <name> [args...] 的第一个参数会被 bash 当作 $0 吃掉，
  # 因此要么补一个占位名，要么改用环境变量（TM_ONECLICK_YES=1 等）：
  /bin/bash -c "$(curl -fsSL <url>)" install-<role> --yes --server-url wss://tunnel.example.com/ws/agent

  # 管道执行；参数写在 -s -- 之后
  curl -fsSL <url> | bash -s -- --version v1.1.1

  # 已知失效模式：curl 失败时命令替换得到空串，bash 什么都不执行却返回 0，
  # 看起来像装好了。脚本化场景请用「先落盘再执行」，或显式检查 curl 退出码。

Windows（PowerShell）
  irm https://raw.githubusercontent.com/nnworld/TunnelMesh/main/deploy/install/oneclick/install-<role>.ps1 -OutFile install-<role>.ps1
  .\install-<role>.ps1

通用选项
  --version vX.Y.Z         指定版本；默认解析最新 Release
  --archive <path>         用本地归档离线安装（跳过下载；同目录存在校验文件时仍会校验）
  --base-url <url>         Release 下载基址（镜像源）
  --raw-base-url <url>     共享库 raw 基址（镜像源）
  --github-token <token>   提高 api.github.com 配额；建议改用环境变量 TUNNELMESH_GITHUB_TOKEN
  --no-cache               不读也不写本地下载缓存
  --mode user|system       Linux 安装模式；默认 user（systemd user unit），system 需 root
  --user <name>            user 模式下安装到指定用户（需 root，脚本会自动 re-exec）
  --run-user <name>        system 模式下的服务运行账户（默认 tunnelmesh）
  --run-group <name>       system 模式下的服务运行组（默认与运行账户同名）
  --create-run-user        运行账户不存在时创建（useradd/adduser）
  --bin-dir <dir>          二进制目录
  --config-dir <dir>       配置目录
  --state-dir <dir>        状态目录
  --server-url <url>       Server WebSocket 地址（agent/client）
  --agent-id <id>          Agent ID（agent）
  --tunnel <spec>          client 本地转发，可重复；
                           spec = name:protocol:listen_host:listen_port:agent_id[:target_host:target_port]
                           protocol 取 tcp、udp、http、socks5
  --token-file <path>      从文件首行读取 service token
  --secret-env-file <path> 从 KEY=VALUE 文件读取敏感值；权限必须是 0600/0400/0640
  --no-service             只装二进制与配置，不注册服务
  --no-start               注册服务但不启动
  --no-enable              不设置开机自启
  --no-linger              user 模式下不执行 loginctl enable-linger
  --keep-config            已存在配置时不覆盖
  --reconfigure            升级时重新生成配置（默认保留现有配置）
  --uninstall              卸载二进制与服务定义；配置与数据保留
  --yes                    非交互：全部取默认值，必填项缺失直接失败
  -h, --help               显示本帮助

安全约定
  token、DSN、主密钥一律不接受明文命令行参数（会进 shell 历史与 ps 输出）。
  取值优先级：--secret-env-file > --token-file（仅 token） > 环境变量 > 交互隐藏输入。
  相关环境变量：TUNNELMESH_AGENT_TOKEN、TUNNELMESH_CLIENT_TOKEN、
  TUNNELMESH_STORAGE_MYSQL_DSN、TUNNELMESH_SERVER_RELAY_NODE_TOKEN、
  TUNNELMESH_TOKEN_ENCRYPTION_KEY、TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID、
  TUNNELMESH_TRACE_SIGNING_KEY。

脚本行为相关环境变量
  TM_ONECLICK_YES=1          等价于 --yes
  TM_ONECLICK_REF=<git-ref>  从 raw 基址取共享库时用的分支或标签，默认 main
  TM_ONECLICK_LIB=<path>     直接指定共享库路径（离线或本地开发）
  TM_ONECLICK_ALLOW_REMOTE=1 非交互场景下同意非 loopback 监听
  TM_ONECLICK_ALLOW_STDIN=1  允许从管道 stdin 读取交互答案（测试与 CI 用）
  TUNNELMESH_GITHUB_TOKEN    提高 api.github.com 配额
  TM_HTTP_TIMEOUT=<sec>      单次请求超时秒数，默认 120

退出码
  0 成功 | 2 参数错误 | 3 preflight 或没有输入通道 | 4 下载失败
  5 校验失败 | 6 配置校验失败 | 7 服务注册失败 | 8 卸载失败
TMUSAGE
  if tm_have tm_role_usage_extra; then tm_role_usage_extra; fi
}
