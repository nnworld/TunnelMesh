#!/usr/bin/env bash
# TunnelMesh Client 一键安装。设计与参数见 docs/deployment/oneclick-install.md。
# 本文件刻意保持薄：下载、校验、交互、渲染、服务注册全部在共享库里。
set -euo pipefail

TM_ROLE="client"

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
  [[ -n "$(tm_ans_get server_url)" ]] || tm_die "$TM_EXIT_PREFLIGHT" "缺少 Server URL：交互输入，或 --server-url"
  tm_validate_ws_url "$(tm_ans_get server_url)" "/ws/client"
  tm_secret_value client_token "Client service token" TUNNELMESH_CLIENT_TOKEN
  tm_ask instance_id_path "instance_id 持久化路径" "$(tm_default_instance_id_path)"
  # 已经用 --tunnel 传过转发就不再问答，避免覆盖命令行意图。
  [[ ${#TM_TUNNEL_SPECS[@]} -gt 0 ]] && return 0
  tm_ask_bool add_tunnel "现在添加本地转发（也可安装后用 --tunnel 或直接编辑配置）" "no"
  while [[ "$(tm_ans_get add_tunnel)" == "yes" ]]; do
    # tm_ask* 对已有答案会直接返回，因此每轮循环都要先清空上一条转发用过的键；
    # 否则第二轮拿到的是旧答案，add_tunnel 也永远停在 yes，会变成死循环。
    tm_ans_set t_name ""; tm_ans_set t_protocol ""; tm_ans_set t_listen ""
    tm_ans_set t_agent ""; tm_ans_set t_host ""; tm_ans_set t_port ""
    tm_ask t_name "转发名称" "tunnel-$(( ${#TM_TUNNEL_SPECS[@]} + 1 ))"
    tm_ask_choice t_protocol "协议" "tcp" tcp udp http socks5
    tm_ask t_listen "本地监听地址（host:port）" "127.0.0.1:$(( 15432 + ${#TM_TUNNEL_SPECS[@]} ))"
    tm_ask t_agent "目标 Agent ID" ""
    [[ -n "$(tm_ans_get t_agent)" ]] || tm_die "$TM_EXIT_USAGE" "目标 Agent ID 不能为空"
    if [[ "$(tm_ans_get t_protocol)" != "socks5" ]]; then
      tm_ask t_host "目标 host" ""
      tm_ask t_port "目标 port" ""
      [[ -n "$(tm_ans_get t_host)" && -n "$(tm_ans_get t_port)" ]] \
        || tm_die "$TM_EXIT_USAGE" "tcp/udp/http 转发必须给出目标 host 与 port（socks5 才可以留空）"
    else
      tm_ans_set t_host ""; tm_ans_set t_port ""
    fi
    tm_validate_listen "$(tm_ans_get t_listen)"
    TM_TUNNEL_SPECS+=("$(tm_ans_get t_name):$(tm_ans_get t_protocol):$(tm_ans_get t_listen):$(tm_ans_get t_agent):$(tm_ans_get t_host):$(tm_ans_get t_port)")
    # 认证设置与转发一一对应；loopback 转发留空，渲染时走配置模型默认值。
    if [[ -n "$TM_LISTEN_AUTH_MODE" ]]; then
      TM_TUNNEL_AUTH+=("${TM_LISTEN_AUTH_MODE}|${TM_LISTEN_ALLOW_REMOTE}")
    else
      TM_TUNNEL_AUTH+=("")
    fi
    tm_ans_set add_tunnel ""
    tm_ask_bool add_tunnel "继续添加下一条转发" "no"
  done
}

tm_role_render_config() { tm_render_client_yaml; }

tm_role_post_install() {
  printf '  本地转发: %s 条\n' "${#TM_TUNNEL_SPECS[@]}"
  printf '  查看状态: %s status\n' "$(tm_binary_path client)"
  printf '  下一步:   浏览器/应用把代理或端口指向上面的本地监听地址\n'
}

tm_main "$@"
