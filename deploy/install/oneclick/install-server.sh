#!/usr/bin/env bash
# TunnelMesh Server 一键安装。设计与参数见 docs/deployment/oneclick-install.md。
# 本文件刻意保持薄：下载、校验、交互、渲染、服务注册全部在共享库里。
set -euo pipefail

TM_ROLE="server"

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
  tm_ask_choice mode "运行模式" "local" local cluster
  tm_ask http_addr "HTTP 监听地址" "127.0.0.1:8080"
  tm_ask dynamic_suffix "动态托管域名后缀" "apps.example.com"
  if [[ "$(tm_ans_get mode)" == "cluster" ]]; then
    tm_ans_set storage_driver "mysql"
    tm_secret_value mysql_dsn "MySQL DSN（含账号密码）" TUNNELMESH_STORAGE_MYSQL_DSN
    tm_ask_bool mysql_tls "MySQL 启用 TLS" "no"
    tm_ask_choice registry_type "注册发现" "database" database etcd
    [[ "$(tm_ans_get registry_type)" == "etcd" ]] && tm_ask registry_endpoints "etcd endpoints（逗号分隔）" "127.0.0.1:2379"
    tm_ask_bool relay_enabled "启用 Server 节点间 relay" "no"
    if [[ "$(tm_ans_get relay_enabled)" == "yes" ]]; then
      tm_ask relay_listen "relay 监听地址" "0.0.0.0:9443"
      tm_ask relay_endpoint "relay 对外 endpoint（留空自动推导）" ""
      tm_ask relay_ca "relay CA 证书路径（留空为明文 relay）" ""
      tm_ask relay_cert "relay 证书路径" ""
      tm_ask relay_key "relay 私钥路径" ""
      tm_ask relay_server_name "relay TLS ServerName" ""
      tm_secret_value relay_node_token "server-node relay token" TUNNELMESH_SERVER_RELAY_NODE_TOKEN
    fi
  else
    tm_ans_set storage_driver "sqlite"
    tm_ask sqlite_path "SQLite 数据库路径" "$(tm_default_state_dir)/tunnelmesh.db"
    tm_ans_set registry_type "database"
    tm_ans_set relay_enabled "no"
  fi
  tm_ask_bool auto_init "自动建表/执行增量迁移" "yes"
  tm_ask_bool webssh_enabled "启用浏览器 WebSSH/SFTP" "yes"
  tm_ask_bool proxy_entry_enabled "启用 tp-* HTTP 代理入口" "no"
  if [[ "$(tm_ans_get proxy_entry_enabled)" == "yes" ]]; then
    tm_ask proxy_entry_listen "代理入口内部监听地址" "127.0.0.1:8089"
    tm_ask proxy_entry_domain_suffix "代理入口域名后缀" ""
  fi
  tm_ask_bool tls_enabled "由本进程终止 TLS（否则交给 Nginx）" "no"
  tm_ask allowed_hosts "Host 白名单（逗号分隔，留空不限制）" ""
  tm_ask allowed_origins "Origin 白名单（逗号分隔）" ""
  tm_ask_bool gen_identity_key "自动生成身份主密钥（SSO/MFA 与 token reveal 必需）" "yes"
  if [[ "$(tm_ans_get gen_identity_key)" == "yes" ]]; then
    tm_ans_set identity_key "$(tm_generate_secret_key)"
    [[ "$(tm_ans_get mode)" == "cluster" ]] && tm_ans_set trace_signing_key "$(tm_generate_secret_key)"
  fi
  tm_ask_bool bootstrap_admin "安装后立即创建首个管理员" "yes"
}

# 「哪个秘密走哪个环境变量」由共享库的 tm_render_server_yaml 统一定义，入口不再重复一份。
tm_role_render_config() { tm_render_server_yaml; }

tm_role_post_install() {
  printf '  管理后台:  http://%s/\n' "$(tm_ans_get http_addr)"
  if [[ "$(tm_ans_get bootstrap_admin)" == "yes" ]]; then
    tm_info "创建首个管理员（凭据只输出到本终端，请立即修改）"
    "$(tm_binary_path server)" --config "$(tm_config_path server)" admin bootstrap || \
      tm_warn "admin bootstrap 失败，可稍后手工执行：tunnelmesh-server --config <path> admin regenerate-credentials --confirm"
  fi
  printf '  健康检查:  %s doctor\n' "$(tm_binary_path server)"
}

tm_main "$@"
