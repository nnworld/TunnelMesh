# 全链路网络探针

## 当前可用能力

当前版本已经具备：

- `tunnelmesh_probe_total`：探针结果计数；
- `tunnelmesh_probe_duration_seconds`：探针耗时；
- 统一 Grafana Dashboard 的 Network Row；
- 探针结果摘要持久化、错误分类 allowlist、租约 epoch fencing；
- `/health/live` 和 `/health/ready`，用于 Server 进程/依赖健康检查。

查看指标：

```bash
curl -fsS http://127.0.0.1:8080/metrics | grep '^tunnelmesh_probe_'
curl -fsS http://127.0.0.1:8080/health/live
curl -fsS http://127.0.0.1:8080/health/ready
```

生产环境通过 Nginx 暴露时，只允许 Prometheus 所在内网访问 `/metrics`，不要把它公开到公网。

## 端到端诊断接口状态

`ProbeService` 已实现授权、输入边界、摘要存储和默认超时，但当前还没有接入 HTTP 路由，也没有 Agent 侧 TCP/HTTP/UDP echo executor。因此现在不能声称已经支持下面的命令：

```http
POST /api/v1/agents/{agentId}/diagnose
GET  /api/v1/agents/{agentId}/probes
```

后续接通协议后，推荐请求模型为：

```json
{
  "kind": "tcp",
  "host": "10.0.0.8",
  "port": 22,
  "timeout": "3s"
}
```

探针响应只返回 `success|failure|timeout`、耗时和稳定 `errorClass`，不返回目标响应体，不持久化任意错误文本。探针必须经过 Agent owner 或管理员授权，并复用 Agent 当前 lease 的 node/epoch。

## 当前排障流程

1. 用 `/health/live` 区分进程是否存活。
2. 用 `/health/ready` 判断数据库和必要依赖是否就绪。
3. 在 `/metrics` 检查 heartbeat、connection、stream、bytes 和 probe 指标。
4. 在 Grafana 的 Network Row 查看吞吐、P95 探针延迟、错误率和最近失败分类。
5. 进入 Agent 主机，从 Agent 网络命名空间执行 `nc -vz host port`、`curl --connect-timeout 3` 或 UDP 专用测试，确认问题是在 Server→Agent 通道还是 Agent→目标服务。

公网 UDP 不由 Server 监听；UDP 探针从 Agent 所在网络发起，并受 capability、Agent Policy、目标地址校验和超时限制。当前默认 executor 未配置时会返回 `unsupported`，不会伪造成功结果。
