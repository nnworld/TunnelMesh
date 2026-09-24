# Client WINDOW_UPDATE 泄漏修复

## Title

`fix(server): stop client window update leak`

## Target branch

`main`

## Summary

Server 不再把 Client 返回的 `WINDOW_UPDATE` 镜像给 Agent。该额度只补充 Server→Client 发送窗口；Agent→Server 额度继续由 Server 消费 Agent relay 字节后独立回补。

## User impact

本地 TCP 转发加载大体积 JavaScript、下载和其它固定长度响应时，不再因为重复 credit 导致 Agent 冲破 Server 接收缓冲并触发 `RESET`。该修复与 Client “应用消费后再归还窗口”修复共同解决 `ERR_CONTENT_LENGTH_MISMATCH`。

## API、Schema 与配置影响

无。不修改 API、数据库 Schema、协议 frame、窗口大小、阈值或配置。

## 安全与授权影响

无。授权、目标策略、审计和传输加密逻辑不变。

## 测试证据

- 红灯：`go test ./internal/server -run '^TestServeClientSessionRelayHonorsClientWindow$' -count=1` 失败，输出 `Client WINDOW_UPDATE leaked to Agent relay`。
- 绿灯：同一命令通过。
- 红灯：本地 TCP 转发 1382571 字节固定长度响应的 E2E 用例失败，出现短包和 `protocol: stream is reset`。
- 绿灯：`go test ./internal/e2e -run '^TestSOCKS5WebPageLatency$/^local_tcp_forward_delivers_large_fixed-length_response$' -count=10` 通过。
- `go test ./... -count=1` 通过。
- `go test -race ./...` 通过。
- `go vet ./...` 通过。
- `git diff --check` 通过。

## 发布步骤

1. 合并后构建并部署新版 `tunnelmesh-server`。
2. 保留或部署包含“应用消费后再归还窗口”修复的 `tunnelmesh-client`。
3. 重启本地转发会话；Agent 无需重启。

## 回滚步骤

回退本 Server 合并提交并重新部署旧 Server。无需回滚数据库或配置；回滚后本地 Client 大响应会重新偶发截断。

## Reviewer 关注点

- Client `WINDOW_UPDATE` 是否只更新 `clientRelayStream.sendState`。
- Agent→Server 额度是否仍由 `agentRelayStream.Read` 消费后回补。
- E2E 是否校验响应头 `Content-Length` 与响应体逐字节一致。

## 集成状态

实现与聚焦验证完成；本记录与相关 Client 流控修复同属一个事故修复 MR。
