# Agent 脚本执行协议

本文描述管理后台运行脚本时，Server 与 Agent 之间使用的 `exec` 流。它复用现有的流打开、数据帧与窗口流控，没有新增帧类型。能力协商与安全边界见 [ADR 0005](../architecture/adr/0005-agent-script-execution.md)。

## 能力

Agent 在握手中宣告 `agent_exec.v1`，且仅当 `agent.exec.enabled` 为真时宣告。Server 只会向宣告了该能力的会话发送 `exec` 流；未宣告则返回不支持。

## 请求

`OpenStream` 的协议名为 `exec`，没有目标主机和端口。流的第一段字节是请求头，随后是脚本正文：

```
length  u32 大端        请求头 JSON 的字节数，1..1024
header  JSON            {"timeout_s": <秒>, "script_len": <字节数>}
script  script_len 字节 脚本正文，原样传递，不经过命令行参数
```

- 请求头可以分多次到达，接收方在字节足够时才解析。
- 脚本全部到达后才开始运行；之后再写入任何字节都会被拒绝。
- 脚本长度超过 Agent 的 `max_script_bytes` 时，在开始前拒绝。

## 响应

输出以记录序列返回，每条记录为：

```
kind    u8      1=stdout  2=stderr  3=exit  4=error
length  u32 大端  payload 的字节数，最大 65536
payload ...
```

- `exit` 的 payload 为 JSON：`exit_code`、`signaled`、`timed_out`、`truncated`、`duration_ms`。它是最后一条正常记录。
- `error` 的 payload 为可读文本，说明 Agent 拒绝或无法运行，例如忙碌或已禁用。
- 输出超过 `max_output_bytes` 后，多余部分被丢弃并在 `exit` 中标记为 `truncated`。

## 浏览器侧

管理后台通过 `/ws/agent-exec/{sessionId}?ticket=` 连接 Server。浏览器先发送一条 JSON：

```json
{"type":"run","script":"echo hi","timeoutSec":60}
```

Server 把记录转换为 JSON 文本帧：`{"type":"stdout"|"stderr","data":"<base64>"}`、`{"type":"exit",...}`、`{"type":"error","message":"..."}`。关闭浏览器连接会结束流，Agent 随之终止脚本的进程组。
