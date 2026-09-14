-- TunnelMesh tp-* HTTP 代理入口的 CONNECT 搬运层。
--
-- 本文件只搬字节，不做任何策略判断。路由身份、源 IP ACL、Basic 认证、目标
-- 校验、并发限额、审计与指标全部由 tunnelmesh-server 的内部入口
-- （server.proxy_entry.listen，默认 127.0.0.1:8089）执行。把策略写进 Lua 会形成
-- 第二份权威，并与 Go 侧校验静默漂移。
--
-- 为什么必须是 server 级 access_by_lua_file：proxy_connect 补丁在
-- ngx_http_core_find_config_phase 里对 CONNECT 直接执行
-- ngx_http_update_location_config(r); r->phase_handler++; 跳过 location 匹配，
-- 所以 location 级的 content_by_lua_block 永远收不到 CONNECT。
--
-- CONFIG 表的默认值必须与 Server 配置默认值一致，由
-- deploy/openresty/openresty_artifacts_test.go 守护。部署时若内部入口地址或
-- idle_timeout 与默认值不同，只改这张表；行尾标记供部署脚本与 E2E 定位替换，
-- 不要删除标记本身。

local CONFIG = {
    host = "127.0.0.1", -- __TM_INTERNAL_HOST__
    port = 8089, -- __TM_INTERNAL_PORT__
    connect_timeout_ms = 10000,
    send_timeout_ms = 30000,
    -- Server 的 idle_timeout + 30s：让 Server 先判定空闲并关闭，Lua 只兜底。
    read_timeout_ms = 330000, -- __TM_READ_TIMEOUT_MS__
    route_header = "X-TunnelMesh-Route",
    client_ip_header = "X-TunnelMesh-Client-IP",
    client_port_header = "X-TunnelMesh-Client-Port",
    chunk_size = 65536,
    max_error_body = 8192,
}

-- 非 CONNECT 请求直接放行，由 server 块的 location / 走 proxy_pass。
if ngx.req.get_method() ~= "CONNECT" then
    return
end

local started = ngx.now()
local host = ngx.var.connect_host
local port = ngx.var.connect_port
local route = ngx.var.ssl_server_name
local client_ip = ngx.var.remote_addr
local client_port = ngx.var.remote_port

if not host or host == "" or not port or port == "" then
    -- 变量为空只有一种可能：内核没有打 proxy_connect 补丁（未打补丁时 nginx 在
    -- 解析阶段就对 CONNECT 回 405，根本走不到这里）。
    ngx.log(ngx.ERR, "tunnelmesh: CONNECT target missing; nginx lacks the proxy_connect patch")
    return ngx.exit(403)
end
if not route or route == "" then
    -- 路由身份只来自 SNI。没有 SNI 时不回退到 Host 头，否则客户端可以用一个
    -- Host 头冒充另一条路由。
    ngx.log(ngx.WARN, "tunnelmesh: CONNECT without SNI target=", host, ":", port)
    return ngx.exit(403)
end

local upstream = ngx.socket.tcp()
upstream:settimeouts(CONFIG.connect_timeout_ms, CONFIG.send_timeout_ms, CONFIG.read_timeout_ms)

-- 客户端断开时立刻释放上游 socket 与两条 pump 线程。lua_check_client_abort 必须
-- 为 on，否则要等到读超时（默认 330s）才回收，半开隧道会堆满 worker_connections。
local abort_ok, abort_err = pcall(ngx.on_abort, function()
    ngx.log(ngx.INFO, "tunnelmesh: client aborted route=", route, " target=", host, ":", port,
            " client=", client_ip, ":", client_port)
    pcall(function() upstream:close() end)
end)
if not abort_ok then
    ngx.log(ngx.WARN, "tunnelmesh: cannot register abort handler: ", tostring(abort_err))
end

local ok, err = upstream:connect(CONFIG.host, CONFIG.port)
if not ok then
    ngx.log(ngx.ERR, "tunnelmesh: dial internal entry failed: ", err)
    return ngx.exit(502)
end

-- 只写白名单头。绝不整体透传客户端请求头：客户端可以自带 X-TunnelMesh-Route
-- 冒充别的路由，也可以塞任意 hop-by-hop 头干扰 Server 解析。
local lines = {
    "CONNECT " .. host .. ":" .. port .. " HTTP/1.1",
    "Host: " .. host .. ":" .. port,
    CONFIG.route_header .. ": " .. route,
    CONFIG.client_ip_header .. ": " .. client_ip,
    CONFIG.client_port_header .. ": " .. client_port,
}
local proxy_auth = ngx.var.http_proxy_authorization
if proxy_auth and proxy_auth ~= "" then
    -- 原样透传，不解码不校验；认证结果由 Server 决定。日志里永远不打印它。
    table.insert(lines, 3, "Proxy-Authorization: " .. proxy_auth)
end

ok, err = upstream:send(table.concat(lines, "\r\n") .. "\r\n\r\n")
if not ok then
    ngx.log(ngx.ERR, "tunnelmesh: forward CONNECT failed: ", err)
    upstream:close()
    return ngx.exit(502)
end

local status_line
status_line, err = upstream:receive()
if not status_line then
    ngx.log(ngx.ERR, "tunnelmesh: read internal entry status failed: ", err)
    upstream:close()
    return ngx.exit(502)
end

local status = tonumber(status_line:match("^HTTP/1%.%d (%d%d%d)"))
if not status then
    ngx.log(ngx.ERR, "tunnelmesh: malformed internal entry status line")
    upstream:close()
    return ngx.exit(502)
end

-- 收集上游响应头。成功分支只需要状态码；失败分支要原样交还客户端，让 curl 与
-- 浏览器看到 Server 给的稳定错误码、Proxy-Authenticate 与 Retry-After。
local headers, content_length = {}, 0
while true do
    local line
    line, err = upstream:receive()
    if not line or line == "" then
        break
    end
    headers[#headers + 1] = line
    local name, value = line:match("^([^:]+):%s*(.*)$")
    if name and name:lower() == "content-length" then
        content_length = tonumber(value) or 0
    end
end

local downstream, derr = ngx.req.socket(true)
if not downstream then
    ngx.log(ngx.ERR, "tunnelmesh: cannot take raw downstream socket: ", derr)
    upstream:close()
    return ngx.exit(502)
end
downstream:settimeouts(CONFIG.connect_timeout_ms, CONFIG.send_timeout_ms, CONFIG.read_timeout_ms)

if status ~= 200 then
    local out = { status_line }
    for i = 1, #headers do
        out[#out + 1] = headers[i]
    end
    out[#out + 1] = ""
    out[#out + 1] = ""
    if content_length > 0 then
        local body
        body, err = upstream:receive(math.min(content_length, CONFIG.max_error_body))
        if body then
            out[#out + 1] = body
        end
    end
    local _, werr = downstream:send(table.concat(out, "\r\n"))
    if werr then
        ngx.log(ngx.WARN, "tunnelmesh: relay rejection to client failed: ", werr)
    end
    ngx.log(ngx.WARN, "tunnelmesh: internal entry rejected CONNECT route=", route,
            " target=", host, ":", port, " status=", status, " client=", client_ip)
    upstream:close()
    -- 444：nginx 立即关闭连接且不再补发响应，避免在已写出的原始响应之后又追加
    -- 一个 nginx 错误页。
    return ngx.exit(444)
end

local _, serr = downstream:send("HTTP/1.1 200 Connection Established\r\n\r\n")
if serr then
    ngx.log(ngx.ERR, "tunnelmesh: announce tunnel to client failed: ", serr)
    upstream:close()
    return ngx.exit(444)
end

local function pump(from, to, label, stat)
    while true do
        local data, rerr, partial = from:receiveany(CONFIG.chunk_size)
        if data then
            stat.bytes = stat.bytes + #data
            local _, werr = to:send(data)
            if werr then
                stat.reason = "send:" .. werr
                return
            end
        else
            -- 超时或关闭时 partial 里可能还有数据，先冲刷再结束。读超时已经比
            -- Server 的 idle_timeout 长 30s，走到这里说明对端不再发数据。
            if partial and #partial > 0 then
                stat.bytes = stat.bytes + #partial
                local _, werr = to:send(partial)
                if werr then
                    stat.reason = "send:" .. werr
                    return
                end
            end
            stat.reason = label .. ":" .. tostring(rerr or "eof")
            return
        end
    end
end

local to_upstream = { bytes = 0 }   -- 客户端 -> 内部入口 -> agent -> 目标
local to_client = { bytes = 0 }     -- 目标 -> agent -> 内部入口 -> 客户端

local push = ngx.thread.spawn(pump, downstream, upstream, "downstream", to_upstream)
local pull = ngx.thread.spawn(pump, upstream, downstream, "upstream", to_client)

-- 任一方向结束就收尾。ngx.thread.kill 让另一条线程的 cosocket 操作立刻失败，
-- 否则半开隧道会一直占着 worker connection 直到读超时。
local thread_ok, thread_err = ngx.thread.wait(push, pull)
pcall(ngx.thread.kill, push)
pcall(ngx.thread.kill, pull)
upstream:close()

-- 只输出 route/target/client/字节数/时长/结束原因。Proxy-Authorization 是凭据，
-- 任何情况下都不得进入 error.log。
ngx.log(ngx.INFO, "tunnelmesh: tunnel closed route=", route, " target=", host, ":", port,
        " client=", client_ip, ":", client_port,
        " upstream_bytes=", to_upstream.bytes, " downstream_bytes=", to_client.bytes,
        " duration=", string.format("%.3f", ngx.now() - started),
        " reason=", thread_ok and tostring(to_upstream.reason or to_client.reason)
                               or ("thread:" .. tostring(thread_err)))

return ngx.exit(444)
