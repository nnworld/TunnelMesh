# 管理后台运行脚本（Agent 原生执行）

> 状态：已实施，未合并。决策与限制见 [ADR 0005](../../architecture/adr/0005-agent-script-execution.md)，协议见 [Agent 脚本执行协议](../../protocol/agent-script-exec.md)。

## 目标

管理员在 `/agents` 列表对在线 Agent 运行一段 shell 脚本，并在管理后台看到输出与退出码。

## 已确认的决策

- Agent 原生执行（`os/exec`），仅管理员可用。
- 按钮只对实际宣告 `agent_exec.v1` 的在线 Agent 显示。
- Agent 默认开启，`agent.exec.enabled` 可关闭；服务端另有 `server.exec.enabled`。

## 实施结果

- **Agent**：`internal/agent/exec_run.go` 为执行器，固定环境变量、独立工作目录、stdin 传脚本、超时与输出上限、进程组终止（Unix）、并发上限；`exec_stream.go` 把请求与输出记录适配为 `io.ReadWriteCloser`，接入分发器；`dialer.go` 与 `dial_executor.go` 完成分发与能力宣告。
- **协议**：`internal/protocol/exec.go`，请求头、输出记录与 `agent_exec.v1` 能力。
- **Server**：`agent_exec.go` 为票据（仅存哈希、单次使用、30 秒）、WebSocket 桥接与审计；`agent_exec_api.go` 为管理员接口；`api.go` 在列表中输出 `execCapable`；`agent_relay_transport.go` 与 `connection_selector.go` 允许无目标的 `exec` 流，并按能力选择会话。
- **前端**：`ScriptRunDialog.vue`、`api/agentExec.ts`、`utils/scriptRun.ts`，列表按钮仅在管理员、在线、`execCapable` 时显示。

## 与原计划的偏差

- 对话框输出使用样式化的 `<pre>`，没有使用 xterm。原因是输出只是文本，不需要终端仿真，而且 xterm 在测试环境里挂载不稳定。
- 服务端票据为内存实现，只在签发节点有效（见 ADR 0005 的已知限制）。
- Windows 不终止进程树（见 ADR 0005）。

## 测试

- Agent：环境清洗、超时杀整个进程组、输出截断、并发 busy、脚本大小上限、关闭即取消、请求头分片到达、开始后拒绝写入。
- 协议：请求头与记录的编解码、长度边界。
- Server：票据单次使用与过期、只存哈希、桥接与审计不含正文、脚本与超时限制、管理员与开关检查、真实 Agent 通过能力协商后执行脚本并回传输出与退出码。
- 前端：按钮显示条件、多字节字符分片解码、请求与帧处理、对话框输出与状态。

## 验证

```
go test ./... -count=1
go test ./internal/agent -run Exec -race
go test ./internal/server -run 'AgentExec|RealAgentRunsScript|NeedsNoTarget' -race
cd web && npm test -- --run && npm run build && cd .. && ./scripts/verify-web-embed.sh
go test ./scripts -count=1
```
