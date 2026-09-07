# WebSocket 协议说明

Agent 和 Client 使用二进制 WebSocket 帧，认证通过 `Authorization: Bearer <token>`，禁止把 Token 放在 URL query。连接建立后执行 hello、心跳和 stream 生命周期；Server 通过能力与权限决定是否接受 TCP、UDP、HTTP 或 WebSocket 转发。

当前稳定入口为 `/ws/agent` 和 `/ws/client`，TCP-over-WebSocket bridge 为 `/ws/tcp`。心跳周期与 Nginx `proxy_read_timeout` 必须配套，推荐超时至少 180 秒。

完整 capability-gated `hello.v2`、structured PING/PONG、flow control、UDP association 和正式 relay protobuf 仍属于后续协议增强项；旧客户端必须继续获得稳定错误码并保持连接安全关闭。
