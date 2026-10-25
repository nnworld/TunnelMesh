# Client Agent 角色融合与自动注册

- 标题：为 Client 增加可选 Agent 角色并支持独立 Agent ID 回写
- 目标分支：`main`

## 摘要

新增 `client.agent_id`。配置后 Client 复用现有 `client.token`、Server 地址和传输配置，同时承载本机 Agent
会话；没有隧道时也可以以 Agent-only 模式运行。首次连接不存在目标 Agent 时，Server 为 token 所属用户幂等创建
同名 Agent。独立 `tunnelmesh-agent` 在缺少 `agent.id` 时通过 Agent token 查询绑定身份并原子回写。

## 用户影响

- 现有 Client 配置和 CLI 隧道行为保持兼容；新增 Agent 角色需要显式填写 `client.agent_id`。
- macOS/Windows 托盘路由配置页增加 Client Agent ID 及说明，保存仍只回传 token 状态，不泄露 token。
- `client.tunnels` 与 `client.agent_id` 至少配置一项；两者都为空时启动/检测失败。

## API、配置与授权

- 配置新增 `client.agent_id`。
- 新增 `GET /api/v1/agent/self`，仅接受 Agent service token，返回 `{id,name}`。
- Agent WebSocket 接受 client service token 作为嵌入 Agent 角色入口；owner、enabled 和 `scope.agentIds` 由服务端校验。
- 自动注册仅写 Agent ID、名称、owner 和审计事实，不记录 token。

## 测试证据

- `go test ./... -count=1`：通过。
- `go test -race`：本次涉及的 `internal/client`、`internal/config`、`internal/server` 定向用例通过；全量执行在既有 `internal/server/TestVPNPeerAPIRejectsMalformedRequests` 处达到 10 分钟测试超时，堆栈位于 Argon2 密码计算与既有会话 writer，未发现本次新增断言或竞态报告。
- `go vet ./...`、`git diff --check`：通过。
- `cd web-tray && npm test -- --run && npm run build`：通过，101 个前端测试通过并生成托盘资源。

## 发布与回滚

先发布 Server，再发布带 `client.agent_id` 的 Client/托盘。回滚时清空 `client.agent_id` 即可恢复为纯 Client；已
自动创建的 Agent 不由回滚自动删除，应通过管理 API 显式禁用或删除。独立 Agent 若回写失败保留旧配置文件。

## Reviewer 关注点

- client token 不能跨 owner 或越过 `scope.agentIds` 创建/连接 Agent。
- Agent self API 不得回显 token、owner 或 metadata。
- YAML 回写必须保留注释、未知键、文件权限并使用原子替换。

## 集成状态

当前分支实现与常规验证已完成；全量竞态测试仍受既有 Server 压力测试超时影响，提交和创建 MR 前需决定是否单独处理该测试环境问题。
