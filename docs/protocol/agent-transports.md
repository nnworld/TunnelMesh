# Agent 裸传输协议：preface 与帧流

本文说明 Agent 通过 `tls://`、`tcp://` 与 `quic://` 连接 Server 时，在帧协议之前发生的认证步骤。帧格式本身与 WebSocket 路径完全相同，见 [websocket.md](websocket.md)。

## 为什么需要 preface

WebSocket 在 HTTP 升级请求的 `Authorization: Bearer` 头里携带令牌，因此认证发生在任何帧之前。裸 TCP 与 QUIC 没有 HTTP 升级，所以在第一个帧之前插入一个固定格式的 **preface**。

preface 由一个只读取固定长度字段的小解析器处理，在令牌被接受之前，不会把任何字节交给帧解码器，因此未认证的字节不会触达 1 MiB 的帧负载上限。

## 布局

```
magic    "TMAG"        4 字节
version  u8 = 1        1 字节，preface 版本，与帧版本独立
flags    u8 = 0        1 字节，保留；非零即拒绝
tokLen   u16 (1..2048) 2 字节，大端
token    tokLen 字节   与 WebSocket Bearer 相同的令牌
```

任何 magic、version、flags 或长度不符合上述约束的 preface 都会被拒绝。长度在读取令牌主体之前校验，服务端最多为一个未认证连接缓冲 2048 字节。

## 服务端应答

认证结果以两个字节返回，之后才开始帧交换：

```
status u8    0 ok | 1 unauthorized | 2 bad_preface | 3 unsupported_version | 4 busy
0      u8    保留，必须为 0
```

- `0`：令牌有效。Agent 随后发送 `AgentHello`，并按原有流程完成能力协商与元数据确认。
- `1`、`3`：Agent 把它视为拒绝（rejection），退避时间按拒绝处理，与 WebSocket 的 401 相同。
- `2`、`4`：普通失败，按普通重连退避处理。
- 失败后服务端立即关闭连接。应答和错误信息都不包含令牌字节。

超时（默认 3 秒）未完成 preface，服务端返回 `bad_preface` 并关闭。

## 认证之后

令牌通过后，服务端把同一个 `agentConnectionAuthentication` 交给与 WebSocket 共用的核心逻辑（`serveAgentConnection`）。从此处开始，`AgentHello`、`AgentMetadataAck`、心跳、流控与帧语义与 WebSocket 路径完全一致。

心跳与空闲：Agent 每 30 秒发送一次 `Ping`，连续三个周期无响应即断开。服务端在收到 `AgentHello` 之前使用 1 秒的读取预算，之后使用 90 秒的每帧空闲预算。

## QUIC 的流模型

QUIC 连接上只使用**一个双向流**。preface 与整个帧协议都在这个流上进行，因此：

- 帧格式、流复用、窗口与流控不需要任何改动。
- 服务端的 `MaxIncomingStreams` 设为 1。额外的流会被 QUIC 层拒绝，因此一个对端无法通过多开流放大自己的未认证预算。
- 关闭该流即关闭整个连接，因为协议不使用其他流。
- ALPN 必须是 `tunnelmesh-agent/1`。使用其他 ALPN 的客户端在 TLS 握手阶段就会失败，永远到不了 preface。

## 安全边界

- 令牌只能在 TLS 或 QUIC 保护之下发送。`tcp://` 是明文，仅允许回环地址或显式的 `allow_insecure_transport`。
- 服务端在认证之前执行三项限制：未认证连接总数、每地址的未认证连接数、以及每地址的认证失败次数。
- 令牌在服务端使用后会被清零，日志与错误信息不会输出 preface 内容。
