#!/usr/bin/env bash
# 测试驱动：把 key=value 形式的答案灌进 tm_ans_set，再调用角色渲染函数。
# 只被 oneclick_config_test.go 与 run_tests.sh 使用，不进发布归档。
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
source "${TM_ONECLICK_LIB:-$HERE/../tunnelmesh-install-common.sh}"
role="$1"; shift
TM_TUNNEL_SPECS=()
for kv in "$@"; do
  case "$kv" in
    tunnel=*) TM_TUNNEL_SPECS+=("${kv#tunnel=}") ;;
    env=*) IFS='=' read -r _ k v <<<"$kv"; tm_env_set "$k" "$v" ;;
    *) tm_ans_set "${kv%%=*}" "${kv#*=}" ;;
  esac
done
case "$role" in
  server) tm_render_server_yaml ;;
  agent) tm_render_agent_yaml ;;
  client) tm_render_client_yaml ;;
  *) echo "unknown role: $role" >&2; exit 2 ;;
esac
