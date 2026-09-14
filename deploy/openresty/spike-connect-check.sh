#!/usr/bin/env bash
# Diagnostic for the tp-* HTTP proxy entry.
#
# 写于 Task 0 的可行性 spike，之后保留为部署前置检查：它回答“这台机器的
# OpenResty 到底能不能把 CONNECT 交给 server 级 access_by_lua”。
# 只读取 nginx 构建信息并在临时目录起一个最小 server，不修改任何生产配置。
#
# 判读方式：
#   (b)(c)(d) 段里出现 "spike connect_host=example.com connect_port=443"
#   且 nc 侧收到 "HTTP/1.1 200 Connection Established" -> 内核已打 proxy_connect
#   补丁，A3（OpenResty Lua 中继 CONNECT）方案可用。
#   若 nc 侧收到 "405 Not Allowed" 或 error.log 里没有 spike 行 -> 内核没有补丁，
#   access_by_lua 永远看不到 CONNECT，必须先换内核（deploy/openresty/Dockerfile.proxy-connect）。
set -euo pipefail
PORT="${1:-18443}"
WORKDIR="$(mktemp -d)"

# macOS 没有 timeout(1)（只有 coreutils 的 gtimeout）；这个脚本会作为部署前置检查
# 在管理员的本机上跑，因此自带一个可移植的兜底，缺 timeout 时退化为后台 kill。
run_with_timeout() {
  local secs="$1"; shift
  if command -v timeout >/dev/null 2>&1; then
    timeout "$secs" "$@" || true
  elif command -v gtimeout >/dev/null 2>&1; then
    gtimeout "$secs" "$@" || true
  else
    "$@" &
    local pid=$!
    ( sleep "$secs"; kill "$pid" 2>/dev/null || true ) &
    local watcher=$!
    wait "$pid" 2>/dev/null || true
    kill "$watcher" 2>/dev/null || true
  fi
}

echo "== (a) build flags =="
nginx -V 2>&1 | tr ' ' '\n' | grep -i "proxy_connect\|add-module\|with-http_ssl" || true

cat > "$WORKDIR/lua.lua" <<'LUA'
if ngx.req.get_method() ~= "CONNECT" then return end
ngx.log(ngx.WARN, "spike connect_host=", tostring(ngx.var.connect_host),
        " connect_port=", tostring(ngx.var.connect_port),
        " sni=", tostring(ngx.var.ssl_server_name))
local sock = ngx.req.socket(true)
if not sock then ngx.log(ngx.ERR, "spike: no raw socket") return ngx.exit(500) end
sock:send("HTTP/1.1 200 Connection Established\r\n\r\n")
sock:send("spike-ok\r\n")
return ngx.exit(444)
LUA

cat > "$WORKDIR/nginx.conf" <<CONF
events {}
http {
  error_log $WORKDIR/error.log warn;
  server {
    listen 127.0.0.1:$PORT;
    lua_check_client_abort on;
    access_by_lua_file $WORKDIR/lua.lua;
    location / { return 403 "non-connect"; }
  }
}
CONF

echo "== (b)(c)(d) plain-text CONNECT through access_by_lua =="
nginx -p "$WORKDIR" -c "$WORKDIR/nginx.conf"
sleep 1
printf 'CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n' \
  | run_with_timeout 5 nc 127.0.0.1 "$PORT"
sleep 1
echo "== error.log =="
cat "$WORKDIR/error.log" || true
nginx -p "$WORKDIR" -c "$WORKDIR/nginx.conf" -s quit || true
echo "workdir kept for inspection: $WORKDIR"
