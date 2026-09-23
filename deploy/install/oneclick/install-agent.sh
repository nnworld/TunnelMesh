#!/usr/bin/env bash
# TunnelMesh Agent 一键安装。设计与参数见 docs/deployment/oneclick-install.md。
# 本文件刻意保持薄：下载、校验、交互、渲染、服务注册全部在共享库里。
set -euo pipefail

TM_ROLE="agent"

# --- bootstrap-begin ---
# 定位共享库：TM_ONECLICK_LIB → 同目录 → raw 下载。
# `bash -c "$(curl ...)"` 与 `curl | bash` 下 $0/BASH_SOURCE 都不是真实路径，必须回退到下载。
# 三个入口的这段引导必须逐字节一致（TestEntryBootstrapIsIdentical 守护）。
_tm_self="${BASH_SOURCE[0]:-$0}"
_tm_dir=""
[[ -f "$_tm_self" ]] && _tm_dir="$(cd "$(dirname "$_tm_self")" && pwd)"
_tm_raw="${TUNNELMESH_RAW_BASE_URL:-${TM_RAW_BASE_URL:-https://raw.githubusercontent.com/nnworld/TunnelMesh}}"
_tm_ref="${TM_ONECLICK_REF:-main}"
if [[ -n "${TM_ONECLICK_LIB:-}" && -f "${TM_ONECLICK_LIB}" ]]; then
  _tm_lib="$TM_ONECLICK_LIB"
elif [[ -n "$_tm_dir" && -f "${_tm_dir}/tunnelmesh-install-common.sh" ]]; then
  _tm_lib="${_tm_dir}/tunnelmesh-install-common.sh"
else
  _tm_lib="$(mktemp -d)/tunnelmesh-install-common.sh"
  curl --fail --silent --show-error --location --max-time 60 \
    "${_tm_raw}/${_tm_ref}/deploy/install/oneclick/tunnelmesh-install-common.sh" --output "$_tm_lib" || {
    echo "ERROR: cannot download tunnelmesh-install-common.sh from ${_tm_raw}/${_tm_ref}" >&2; exit 4; }
  [[ -s "$_tm_lib" ]] || { echo "ERROR: downloaded installer library is empty" >&2; exit 4; }
  [[ "$(head -n 1 "$_tm_lib")" == '#!/usr/bin/env bash' ]] || {
    echo "ERROR: downloaded installer library is not a bash script" >&2; exit 4; }
fi
# shellcheck source=/dev/null
source "$_tm_lib"
tm_load_lib() { printf '%s\n' "$_tm_lib"; }   # 供共享库内部与测试查询实际加载路径
tm_entry_init "$_tm_self" "$_tm_self"
# --- bootstrap-end ---

tm_role_parse_args() { :; }

tm_role_prompts() {
  tm_ask server_url "Server WebSocket URL" ""
  [[ -n "$(tm_ans_get server_url)" ]] || tm_die "$TM_EXIT_PREFLIGHT" "缺少 Server URL：交互输入，或 --server-url / TM_ONECLICK_YES=1 + --server-url"
  tm_validate_ws_url "$(tm_ans_get server_url)" "/ws/agent"
  tm_ask agent_id "Agent ID" "agent-$(hostname | tr 'A-Z' 'a-z' | tr -cs 'a-z0-9-' '-' | cut -c1-40)"
  tm_secret_value agent_token "Agent service token" TUNNELMESH_AGENT_TOKEN
  tm_ask conn_min "连接池最小连接数" "1"
  tm_ask conn_max "连接池最大连接数" "1"
  tm_ask instance_id "instance_id（留空自动生成并持久化）" ""
  tm_ask_bool report_metadata "上报宿主机 metadata（来自 allowlist 的文件/环境变量）" "no"
  if [[ "$(tm_ans_get report_metadata)" == "yes" ]]; then
    tm_ask metadata_name "metadata 名称" "device_id"
    tm_ask_choice metadata_source "metadata 来源" "file" file env
    tm_ask metadata_path "文件路径（source=file 时）" "/etc/machine-id"
    tm_ask metadata_key "环境变量名（source=env 时）" ""
    tm_validate_metadata_name "$(tm_ans_get metadata_name)"
  fi
}

tm_role_render_config() { tm_render_agent_yaml; }

tm_role_post_install() {
  printf '  Agent ID:  %s\n' "$(tm_ans_get agent_id)"
  printf '  下一步:    在管理后台为该 Agent 绑定 service token，然后执行 %s id\n' "$(tm_binary_path agent)"
}

tm_main "$@"
