#!/usr/bin/env bash
# 函数级测试套件：由 oneclick_scripts_test.go 调用，也可手工执行。
# 只测纯函数与可注入依赖的函数，不联网、不注册服务。
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# ROOT 指向仓库根，用于定位 deploy/systemd-user/ 等模板（testdata 在 deploy/install/oneclick 下）。
ROOT="$(cd "$HERE/../../../.." && pwd)"
LIB="${TM_ONECLICK_LIB:-$HERE/../tunnelmesh-install-common.sh}"
# shellcheck source=/dev/null
source "$LIB"

FAILURES=0
assert_eq() { # assert_eq <name> <expected> <actual>
  if [[ "$2" == "$3" ]]; then
    printf 'ok   %s\n' "$1"
  else
    printf 'FAIL %s\n  expected: %s\n  actual:   %s\n' "$1" "$2" "$3"
    FAILURES=$((FAILURES + 1))
  fi
}
assert_contains() { # assert_contains <name> <haystack> <needle>
  case "$2" in
    *"$3"*) printf 'ok   %s\n' "$1" ;;
    *) printf 'FAIL %s\n  missing: %s\n  in:      %s\n' "$1" "$3" "$2"; FAILURES=$((FAILURES + 1)) ;;
  esac
}
assert_exit_sh() { # assert_exit_sh <expected-code> <name> <shell-snippet>
  # tm_die 直接 exit，被测片段必须跑在子 shell 里才能断言退出码而不终止套件。
  local want="$1" name="$2" snippet="$3" got=0
  TM_ONECLICK_LIB="$LIB" bash -c "source \"\$TM_ONECLICK_LIB\"; $snippet" >/dev/null 2>&1 || got=$?
  assert_eq "$name" "$want" "$got"
}

# --- Task 1: 平台探测与掩码 ---
assert_eq "arch/x86_64" "amd64" "$(tm_detect_arch x86_64)"
assert_eq "arch/aarch64" "arm64" "$(tm_detect_arch aarch64)"
assert_eq "arch/arm64" "arm64" "$(tm_detect_arch arm64)"
assert_eq "arch/unknown-empty" "" "$(tm_detect_arch riscv64 || true)"
assert_eq "mask/long" "abcd****" "$(tm_mask abcdefgh)"
assert_eq "mask/short" "****" "$(tm_mask abc)"
assert_eq "mask/empty" "(empty)" "$(tm_mask '')"
assert_eq "exit-codes" "0 2 3 4 5 6 7 8" \
  "$TM_EXIT_OK $TM_EXIT_USAGE $TM_EXIT_PREFLIGHT $TM_EXIT_DOWNLOAD $TM_EXIT_CHECKSUM $TM_EXIT_CONFIG $TM_EXIT_SERVICE $TM_EXIT_UNINSTALL"
assert_eq "default-mode" "user" "$TM_DEFAULT_INSTALL_MODE"
tm_tty_init
assert_contains "tty/value" "/dev/tty " "${TM_TTY} "
tm_entry_init "bash" "main"
assert_eq "entry/bash-c-empty" "" "$TM_ONECLICK_ENTRY"
tm_entry_init "$HERE/fixtures/fake-entry.sh" "$HERE/fixtures/fake-entry.sh"
assert_eq "entry/real-path" "$HERE/fixtures/fake-entry.sh" "$TM_ONECLICK_ENTRY"
tm_detect_platform
assert_contains "platform/family" "linux darwin" "$TM_OS_FAMILY"
assert_eq "platform/ext" "tar.gz" "$TM_ARCHIVE_EXT"

# --- Task 2: 下载、校验、缓存 ---
# fixture 归档按本机架构现场生成，仓库里不提交二进制。
TM_TMP="$(mktemp -d)"
FIXDIR="$TM_TMP/fixtures"
mkdir -p "$FIXDIR" "$TM_TMP/cache" "$TM_TMP/bin"
cp "$HERE/fixtures/releases__latest" "$FIXDIR/releases__latest"
TM_GOOS="$(uname -s | tr 'A-Z' 'a-z')"
TM_GOARCH="$(tm_detect_arch "$(uname -m)")"
TM_ARCHIVE_EXT="tar.gz"
archive_name="$(tm_archive_name v9.9.9)"
stage="$TM_TMP/stage"; mkdir -p "$stage"
for role in server agent client; do
  printf '#!/bin/sh\necho stub-%s\n' "$role" >"$stage/tunnelmesh-$role"
  chmod +x "$stage/tunnelmesh-$role"
done
tar -czf "$FIXDIR/v9.9.9__${archive_name}" -C "$stage" tunnelmesh-server tunnelmesh-agent tunnelmesh-client
sum="$(tm_checksum_file "$FIXDIR/v9.9.9__${archive_name}")"
printf '%s  %s\n' "$sum" "$archive_name" >"$FIXDIR/v9.9.9__SHA256SUMS"
printf '%s  %s\n' "$(printf '0%.0s' 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30 31 32 33 34 35 36 37 38 39 40 41 42 43 44 45 46 47 48 49 50 51 52 53 54 55 56 57 58 59 60 61 62 63 64)" "$archive_name" >"$TM_TMP/SHA256SUMS.bad"
export PATH="$HERE/bin:$PATH"
export CURL_STUB_FIXTURES="$FIXDIR" TM_CACHE_DIR="$TM_TMP/cache"

assert_eq "archive-name" "tunnelmesh-v9.9.9-${TM_GOOS}-${TM_GOARCH}.tar.gz" "$archive_name"
assert_exit_sh 0 "checksum/ok" "TM_GOOS=$TM_GOOS TM_GOARCH=$TM_GOARCH TM_ARCHIVE_EXT=tar.gz tm_verify_checksum '$FIXDIR/v9.9.9__${archive_name}' '$FIXDIR/v9.9.9__SHA256SUMS' '$archive_name'"
assert_exit_sh 5 "checksum/mismatch" "tm_verify_checksum '$FIXDIR/v9.9.9__${archive_name}' '$TM_TMP/SHA256SUMS.bad' '$archive_name'"
assert_exit_sh 5 "checksum/missing-entry" "tm_verify_checksum '$FIXDIR/v9.9.9__${archive_name}' '$FIXDIR/v9.9.9__SHA256SUMS' 'not-in-sums.tar.gz'"
assert_exit_sh 2 "version/invalid" "tm_validate_version 'v1.2'"
assert_exit_sh 2 "version/no-prefix" "tm_validate_version '1.2.3'"

# latest 解析：API 优先
export CURL_STUB_LOG="$TM_TMP/log-api"
assert_eq "version/api" "v9.9.9" "$(TM_VERSION=latest tm_resolve_version_stdout)"
assert_contains "version/api-url" "$(cat "$CURL_STUB_LOG")" "api.github.com/repos/nnworld/TunnelMesh/releases/latest"

# latest 解析：API 404 时回退到 /releases/latest 的 302 Location。
# stub 变量必须 export 后才对子进程可见：`VAR=x resolved=$(...)` 这种纯赋值语句
# 的前缀赋值只作用于当前 shell，不会进入 curl stub 的环境。
resolved="$(
  export CURL_STUB_FIXTURES="$HERE/fixtures/noapi"
  export CURL_STUB_LOG="$TM_TMP/log-redir"
  export CURL_STUB_REDIRECT="https://github.com/nnworld/TunnelMesh/releases/tag/v9.9.9"
  TM_VERSION=latest tm_resolve_version_stdout
)"
export CURL_STUB_FIXTURES="$FIXDIR"
assert_eq "version/redirect-fallback" "v9.9.9" "$resolved"
assert_contains "version/redirect-url" "$(cat "$TM_TMP/log-redir")" "/releases/latest"

# 缓存：首次下载 → 命中不再下载 → 损坏自动重下
export CURL_STUB_LOG="$TM_TMP/log-dl1"
TM_NO_CACHE=0 tm_download_release v9.9.9
assert_eq "cache/file-created" "yes" "$([[ -s "$TM_TMP/cache/$archive_name" ]] && echo yes)"
CURL_STUB_LOG="$TM_TMP/log-dl2" TM_NO_CACHE=0 TM_WORKDIR="" tm_download_release v9.9.9
assert_eq "cache/hit-no-archive-download" "0" "$(grep -c "download/v9.9.9/${archive_name}\$" "$TM_TMP/log-dl2" || true)"
printf 'corrupted' >"$TM_TMP/cache/$archive_name"
CURL_STUB_LOG="$TM_TMP/log-dl3" TM_NO_CACHE=0 tm_download_release v9.9.9
assert_eq "cache/corrupt-recovered" "yes" "$(tm_verify_checksum "$TM_TMP/cache/$archive_name" "$FIXDIR/v9.9.9__SHA256SUMS" "$archive_name" >/dev/null 2>&1 && echo yes)"
assert_contains "cache/corrupt-redownloaded" "$(cat "$TM_TMP/log-dl3")" "$archive_name"
CURL_STUB_LOG="$TM_TMP/log-dl4" TM_NO_CACHE=1 tm_download_release v9.9.9
assert_contains "cache/bypassed" "$(cat "$TM_TMP/log-dl4")" "download/v9.9.9/${archive_name}"

# 解压
extract="$TM_TMP/extract"
tm_extract_archive "$TM_TMP/cache/$archive_name" "$extract"
assert_eq "extract/agent-binary" "yes" "$([[ -x "$extract/tunnelmesh-agent" ]] && echo yes)"
rm -rf "$TM_TMP"
# --- Task 3: 参数解析与交互原语 ---
export TM_ONECLICK_ALLOW_STDIN=1

tm_ans_set "server.url" "wss://a/ws/agent"
assert_eq "ans/roundtrip-dotted-key" "wss://a/ws/agent" "$(tm_ans_get server.url)"
tm_ans_set "agent-id" "agent-1"
assert_eq "ans/roundtrip-dash-key" "agent-1" "$(tm_ans_get agent-id)"
assert_eq "ans/missing-empty" "" "$(tm_ans_get nope)"
assert_exit_sh 2 "ans/illegal-key" "tm_ans_set 'bad key' v"

# --yes：全部取默认值，不读 stdin
assert_eq "ask/yes-default" "8080" "$(TM_YES=1 tm_ask_http_port)"
# flag/env 已给值时不覆盖
assert_eq "ask/preset-wins" "preset" "$(TM_YES=1 bash -c 'source "$TM_ONECLICK_LIB"; tm_ans_set demo preset; tm_ask demo "prompt" "default"; tm_ans_get demo')"
# 非 --yes：从 stdin 读；空行取默认
assert_eq "ask/stdin-value" "typed" "$(printf 'typed\n' | TM_YES=0 TM_TTY= tm_ask_stdio demo "prompt" "default")"
assert_eq "ask/stdin-empty-uses-default" "default" "$(printf '\n' | TM_YES=0 TM_TTY= tm_ask_stdio demo "prompt" "default")"
assert_eq "ask/bool-normalize-Y" "yes" "$(printf 'Y\n' | TM_YES=0 TM_TTY= tm_ask_bool_stdio demo "prompt" "no")"
assert_eq "ask/bool-normalize-empty" "no" "$(printf '\n' | TM_YES=0 TM_TTY= tm_ask_bool_stdio demo "prompt" "no")"
assert_eq "ask/choice-invalid-then-valid" "udp" "$(printf 'quic\nudp\n' | TM_YES=0 TM_TTY= tm_ask_choice_stdio demo "prompt" "tcp" tcp udp http socks5)"

# 没有输入通道时必须失败，不能静默取默认值
assert_exit_sh 3 "ask/no-channel-dies" "TM_YES=0 TM_TTY= TM_ONECLICK_ALLOW_STDIN= </dev/null tm_require_input_channel"

# URL 与隧道规格校验
assert_exit_sh 0 "url/ok-agent" "tm_validate_ws_url wss://tunnel.example.com/ws/agent /ws/agent"
assert_exit_sh 2 "url/bad-scheme" "tm_validate_ws_url https://tunnel.example.com/ws/agent /ws/agent"
assert_exit_sh 2 "url/bad-suffix" "tm_validate_ws_url wss://tunnel.example.com/ws/client /ws/agent"
assert_eq "tunnel/tcp" "pg|tcp|127.0.0.1:15432|agent-db|db.internal|5432" \
  "$(tm_parse_tunnel_spec 'pg:tcp:127.0.0.1:15432:agent-db:db.internal:5432')"
assert_eq "tunnel/socks5-no-target" "s|socks5|127.0.0.1:10866|agent-a||" \
  "$(tm_parse_tunnel_spec 's:socks5:127.0.0.1:10866:agent-a::')"
assert_exit_sh 2 "tunnel/bad-protocol" "tm_parse_tunnel_spec 'pg:quic:127.0.0.1:1:agent-db:host:5432'"
assert_exit_sh 2 "tunnel/bad-port" "tm_parse_tunnel_spec 'pg:tcp:127.0.0.1:99999:agent-db:host:5432'"
assert_exit_sh 2 "tunnel/missing-fields" "tm_parse_tunnel_spec 'pg:tcp'"

# 参数解析
assert_eq "args/version" "v1.2.3" "$(tm_parse_common_args_stdout --print version --version v1.2.3)"
assert_eq "args/yes-flag" "1" "$(tm_parse_common_args_stdout --print yes --yes)"
assert_eq "args/yes-env" "1" "$(TM_ONECLICK_YES=1 tm_parse_common_args_stdout --print yes)"
assert_eq "args/mode" "system" "$(tm_parse_common_args_stdout --print mode --mode system)"
assert_eq "args/base-url" "https://mirror.example/releases" "$(tm_parse_common_args_stdout --print base-url --base-url https://mirror.example/releases)"
assert_exit_sh 2 "args/unknown" "tm_parse_common_args_stdout --print version --bogus"
assert_exit_sh 2 "args/bad-mode" "tm_parse_common_args_stdout --print mode --mode root"
assert_exit_sh 2 "args/missing-value" "tm_parse_common_args_stdout --print version --version"

# secret env file 权限（TM_TMP3 必须先于 secretfile 赋值，否则 set -u 下引用未定义变量）
TM_TMP3="$(mktemp -d)"; secretfile="$TM_TMP3/agent.env"
printf "TUNNELMESH_AGENT_TOKEN='abc'\n" >"$secretfile"; chmod 0644 "$secretfile"
assert_exit_sh 3 "secret-file/insecure-perm" "tm_load_secret_env_file '$secretfile'"
chmod 0600 "$secretfile"
assert_eq "secret-file/value" "abc" "$(tm_load_secret_env_file "$secretfile"; tm_secret_loaded_get TUNNELMESH_AGENT_TOKEN)"
rm -rf "$TM_TMP3"
# --- Task 4: 渲染原语 ---
assert_eq "yaml/plain" "'abc'" "$(tm_yaml_quote abc)"
assert_eq "yaml/inner-quote" "'a''b'" "$(tm_yaml_quote "a'b")"
assert_eq "yaml/empty" "''" "$(tm_yaml_quote '')"
assert_eq "yaml/dsn" "'user:p@ss w/db?parseTime=true'" "$(tm_yaml_quote 'user:p@ss w/db?parseTime=true')"

tm_env_clear
tm_env_set TUNNELMESH_AGENT_TOKEN "tok'en"
tm_env_set TUNNELMESH_REGION "shanghai"
assert_eq "env/get" "shanghai" "$(tm_env_get TUNNELMESH_REGION)"
rendered_env="$(tm_render_env_file)"
assert_contains "env/header" "$rendered_env" "包含敏感值"
assert_contains "env/quoted-value" "$rendered_env" 'TUNNELMESH_AGENT_TOKEN="tok'"'"'en"'
assert_contains "env/plain-value" "$rendered_env" 'TUNNELMESH_REGION="shanghai"'
tm_env_clear
tm_env_set TUNNELMESH_DSN 'a\b"c'
assert_contains "env/escape-backslash-and-quote" "$(tm_render_env_file)" 'TUNNELMESH_DSN="a\\b\"c"'
tm_env_clear

assert_eq "xml/escape" "a&amp;b&lt;c&gt;d&quot;e" "$(tm_xml_escape 'a&b<c>d"e')"
assert_eq "plist/empty-when-no-env" "" "$(tm_env_clear; tm_render_plist_environment)"
tm_env_set TUNNELMESH_AGENT_TOKEN "t0k"
plist_env="$(tm_render_plist_environment)"
assert_contains "plist/dict-open" "$plist_env" "<key>EnvironmentVariables</key>"
assert_contains "plist/key" "$plist_env" "<key>TUNNELMESH_AGENT_TOKEN</key>"
assert_contains "plist/value" "$plist_env" "<string>t0k</string>"
assert_contains "winsw/env" "$(tm_render_winsw_env_block)" '<env name="TUNNELMESH_AGENT_TOKEN" value="t0k" />'
tm_env_clear

unit="$(tm_render_systemd_user_unit agent "$ROOT/deploy/systemd-user/tunnelmesh-agent.service" \
  "$HOME/.local/bin/tunnelmesh-agent" "$HOME/.config/tunnelmesh/agent.yaml" \
  "$HOME/.config/tunnelmesh/agent.env" "$HOME/.local/share/tunnelmesh")"
assert_contains "unit/exec" "$unit" "ExecStart=$HOME/.local/bin/tunnelmesh-agent --config $HOME/.config/tunnelmesh/agent.yaml run"
assert_contains "unit/envfile" "$unit" "EnvironmentFile=-$HOME/.config/tunnelmesh/agent.env"
assert_contains "unit/state" "$unit" "ReadWritePaths=$HOME/.local/share/tunnelmesh"
assert_eq "unit/no-placeholder-left" "" "$(printf '%s' "$unit" | grep -o '__[A-Z_]*__' || true)"

dropin="$(tm_render_systemd_dropin server /opt/tm/tunnelmesh-server /etc/tunnelmesh/server.yaml /etc/tunnelmesh/server.env /var/lib/tunnelmesh tmuser tmgroup)"
assert_contains "dropin/user" "$dropin" "User=tmuser"
assert_contains "dropin/group" "$dropin" "Group=tmgroup"
assert_contains "dropin/clear-execstart" "$dropin" "ExecStart="
assert_contains "dropin/clear-execstartpre" "$dropin" "ExecStartPre="
assert_contains "dropin/init-node-id-root" "$dropin" "ExecStartPre=+/opt/tm/tunnelmesh-server --config /etc/tunnelmesh/server.yaml init-node-id"
assert_contains "dropin/exec" "$dropin" "ExecStart=/opt/tm/tunnelmesh-server --config /etc/tunnelmesh/server.yaml run"

# tm_run_validate 必须把 node identity 指到用户可写的状态目录：init-node-id 的默认
# 路径 /var/lib/tunnelmesh 在 user 模式（非 root）下不可写，会让 server 一键安装在
# 校验阶段直接以退出码 6 失败。用桩二进制记录实际参数来断言。
TM_TMPV="$(mktemp -d)"
cat >"$TM_TMPV/tunnelmesh-server" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$TM_ARG_LOG"
exit 0
STUB
chmod +x "$TM_TMPV/tunnelmesh-server"
: >"$TM_TMPV/server.yaml"
TM_ONECLICK_LIB="$LIB" TM_ARG_LOG="$TM_TMPV/args.log" bash -c '
  source "$TM_ONECLICK_LIB"
  TM_MODE=user TM_OS_FAMILY=linux
  TM_BIN_DIR="'"$TM_TMPV"'" TM_CONFIG_DIR="'"$TM_TMPV"'" TM_STATE_DIR="'"$TM_TMPV"'/state"
  tm_run_validate server' >/dev/null 2>&1
assert_eq "validate/exit-ok" "0" "$?"
assert_contains "validate/node-id-path" "$(cat "$TM_TMPV/args.log")" \
  "--node-id-path $TM_TMPV/state/node-id init-node-id"
assert_contains "validate/check-config" "$(cat "$TM_TMPV/args.log")" "--config $TM_TMPV/server.yaml check-config"
rm -rf "$TM_TMPV"

# 覆盖前备份，只保留最近 3 份（TM_TMP4 必须先创建再拼路径，否则 set -u 下引用未定义变量）
TM_TMP4="$(mktemp -d)"; bk="$TM_TMP4"
target="$TM_TMP4/agent.yaml"
for i in 1 2 3 4 5; do printf 'v%s\n' "$i" >"$TM_TMP4/content"; TM_BACKUP_TIMESTAMP="2026010${i}T000000Z" tm_backup_and_overwrite "$target" 0600 "$TM_TMP4/content"; done
assert_eq "backup/content" "v5" "$(cat "$target")"
assert_eq "backup/kept-3" "3" "$(ls "$target".bak-* | wc -l | tr -d ' ')"
assert_eq "backup/newest" "v4" "$(cat "$target.bak-20260105T000000Z" 2>/dev/null || cat "$target".bak-2026010*T000000Z | tail -1)"
assert_eq "backup/mode" "600" "$(tm_file_perms "$target")"
rm -rf "$TM_TMP4"
# --- Task 6: 模式判定与服务生命周期 ---
TM_TMP6="$(mktemp -d)"; SVCLOG="$TM_TMP6/svc.log"
export SVC_STUB_LOG="$SVCLOG"
export PATH="$HERE/bin:$PATH"

# 模式判定规则只对 Linux 有分支差异（非 Linux 一律 user），
# 因此固定成 linux 来测规则本身，测完再还原，保证 macOS 与 Linux 上结果一致。
TM_OS_FAMILY_DETECTED="$TM_OS_FAMILY"
TM_OS_FAMILY="linux"

# 规则 1：非 root → user
tm_euid() { printf '1000\n'; }
TM_MODE="" TM_TARGET_USER="$(id -un)" SUDO_USER=""
out="$(tm_resolve_install_mode_stdout)"
assert_eq "mode/non-root" "user" "$out"

# 规则 2：root + SUDO_USER → user，目标用户取 SUDO_USER
tm_euid() { printf '0\n'; }
out="$(SUDO_USER=alice tm_resolve_install_mode_stdout)"
assert_eq "mode/sudo-user" "user" "$out"
# TM_TARGET_USER 非空代表用户显式给了 --user，优先级高于 SUDO_USER，因此这里必须清空。
assert_eq "mode/sudo-target" "alice" "$(TM_TARGET_USER= SUDO_USER=alice tm_resolve_target_user)"

# 规则 3：root 无 SUDO_USER → system
out="$(SUDO_USER= tm_resolve_install_mode_stdout)"
assert_eq "mode/real-root" "system" "$out"

# 规则 4：显式 --mode system 但非 root → 退出码 3
tm_euid() { printf '1000\n'; }
assert_exit_sh 3 "mode/system-needs-root" "tm_euid() { printf '1000\n'; }; TM_MODE=system tm_resolve_install_mode"

# 显式 --mode user 时即使 root 也保持 user
tm_euid() { printf '0\n'; }
assert_eq "mode/explicit-user" "user" "$(SUDO_USER= TM_MODE=user tm_resolve_install_mode_stdout)"

# 路径推导
TM_MODE="user" TM_OS_FAMILY="linux" TM_TARGET_USER="$(id -un)" TM_BIN_DIR="" TM_CONFIG_DIR="" TM_STATE_DIR=""
assert_eq "path/user-binary" "$HOME/.local/bin/tunnelmesh-agent" "$(tm_binary_path agent)"
assert_eq "path/user-config" "$HOME/.config/tunnelmesh/agent.yaml" "$(tm_config_path agent)"
assert_eq "path/user-env" "$HOME/.config/tunnelmesh/agent.env" "$(tm_env_path agent)"
assert_eq "path/user-unit" "$HOME/.config/systemd/user/tunnelmesh-agent.service" "$(tm_unit_path agent)"
assert_eq "path/user-state" "$HOME/.local/share/tunnelmesh" "$(tm_default_state_dir)"
TM_MODE="system"
assert_eq "path/system-binary" "/usr/local/bin/tunnelmesh-agent" "$(TM_BIN_DIR= tm_binary_path agent)"
assert_eq "path/system-config" "/etc/tunnelmesh/agent.yaml" "$(TM_CONFIG_DIR= tm_config_path agent)"
assert_eq "path/system-state" "/var/lib/tunnelmesh-agent" "$(TM_STATE_DIR= TM_ROLE=agent tm_default_state_dir)"
TM_MODE="user" TM_OS_FAMILY="darwin"
assert_eq "path/macos-unit" "$HOME/Library/LaunchAgents/com.tunnelmesh.agent.plist" "$(tm_unit_path agent)"
TM_MODE="user" TM_OS_FAMILY="linux"
TM_ROLE="agent"
TM_OS_FAMILY="$TM_OS_FAMILY_DETECTED"

# 服务生命周期：systemd user 调用序列
: >"$SVCLOG"
TM_SERVICE_MANAGER="systemd-user" tm_service_install agent
TM_SERVICE_MANAGER="systemd-user" tm_service_enable agent
TM_SERVICE_MANAGER="systemd-user" tm_service_start agent
assert_contains "svc/user-daemon-reload" "$(cat "$SVCLOG")" "systemctl --user daemon-reload"
assert_contains "svc/user-enable" "$(cat "$SVCLOG")" "systemctl --user enable tunnelmesh-agent.service"
assert_contains "svc/user-start" "$(cat "$SVCLOG")" "systemctl --user restart tunnelmesh-agent.service"

# 服务生命周期：systemd system 调用序列
: >"$SVCLOG"
TM_SERVICE_MANAGER="systemd-system" tm_service_install server
TM_SERVICE_MANAGER="systemd-system" tm_service_enable server
assert_contains "svc/system-daemon-reload" "$(cat "$SVCLOG")" "systemctl daemon-reload"
assert_contains "svc/system-enable" "$(cat "$SVCLOG")" "systemctl enable tunnelmesh-server.service"

# 服务生命周期：launchd 调用序列
: >"$SVCLOG"
TM_SERVICE_MANAGER="launchd" tm_service_install agent
TM_SERVICE_MANAGER="launchd" tm_service_start agent
assert_contains "svc/launchd-bootout" "$(cat "$SVCLOG")" "launchctl bootout gui/$(id -u)"
assert_contains "svc/launchd-bootstrap" "$(cat "$SVCLOG")" "launchctl bootstrap gui/$(id -u)"
assert_contains "svc/launchd-kickstart" "$(cat "$SVCLOG")" "launchctl kickstart -k gui/$(id -u)/com.tunnelmesh.agent"

# 无服务管理器：不失败，只标记未注册
: >"$SVCLOG"
TM_SERVICE_MANAGER="none" tm_service_install agent
assert_eq "svc/none-noop" "0" "$TM_SERVICE_REGISTERED"
assert_eq "svc/none-no-calls" "0" "$(wc -l <"$SVCLOG" | tr -d ' ')"
rm -rf "$TM_TMP6"
# --- Task 7: 归档版本推导与 metadata 名称校验 ---
assert_eq "archive-version" "v9.9.9" "$(tm_version_from_archive /tmp/tunnelmesh-v9.9.9-linux-amd64.tar.gz)"
assert_eq "archive-version-zip" "v1.2.3" "$(tm_version_from_archive /tmp/tunnelmesh-v1.2.3-windows-amd64.zip)"
assert_exit_sh 2 "archive-version-unknown" "tm_version_from_archive /tmp/random.tar.gz"
assert_exit_sh 0 "metadata/name-ok" "tm_validate_metadata_name device_id"
assert_exit_sh 2 "metadata/name-sensitive" "tm_validate_metadata_name db_password"
assert_exit_sh 2 "metadata/name-charset" "tm_validate_metadata_name 'bad name'"

# 非 loopback 监听：默认拒绝；显式同意后写入 auth_mode/allow_remote
assert_exit_sh 2 "listen/remote-refused" "TM_YES=1 tm_validate_listen 0.0.0.0:18080"
TM_ONECLICK_ALLOW_REMOTE=1 tm_validate_listen "0.0.0.0:18080"
assert_eq "listen/remote-auth-mode" "password" "$TM_LISTEN_AUTH_MODE"
assert_eq "listen/remote-allow" "yes" "$TM_LISTEN_ALLOW_REMOTE"
TM_ONECLICK_ALLOW_REMOTE=0 tm_validate_listen "127.0.0.1:18080"
assert_eq "listen/loopback-auth-empty" "" "$TM_LISTEN_AUTH_MODE"

# 客户端 YAML：认证设置按转发逐条渲染
TM_TUNNEL_SPECS=("s1:socks5:0.0.0.0:10866:agent-a::" "s2:socks5:127.0.0.1:10867:agent-a::")
TM_TUNNEL_AUTH=("password|yes" "")
tm_ans_set server_url "wss://t.example.com/ws/client"
client_yaml="$(tm_render_client_yaml)"
assert_contains "client-yaml/auth-mode" "$client_yaml" "auth_mode: 'password'"
assert_contains "client-yaml/allow-remote" "$client_yaml" "allow_remote: true"
assert_eq "client-yaml/auth-only-once" "1" "$(printf '%s\n' "$client_yaml" | grep -c 'auth_mode:')"
TM_TUNNEL_SPECS=(); TM_TUNNEL_AUTH=()

# --- Task 8: 服务管理器注入接缝 ---
# 容器与 CI 里 /run/systemd/system 常常不存在，端到端测试需要显式指定管理器，
# 否则只能覆盖 none 分支。
assert_eq "svc/inject-user" "systemd-user" "$(TM_ONECLICK_SERVICE_MANAGER=systemd-user tm_detect_service_manager_stdout)"
assert_eq "svc/inject-none" "none" "$(TM_ONECLICK_SERVICE_MANAGER=none tm_detect_service_manager_stdout)"
assert_eq "svc/inject-no-service" "none" "$(TM_ONECLICK_SERVICE_MANAGER= TM_NO_SERVICE=1 tm_detect_service_manager_stdout)"

printf '\n%s: %d failure(s)\n' "$0" "$FAILURES"
[[ "$FAILURES" -eq 0 ]]
