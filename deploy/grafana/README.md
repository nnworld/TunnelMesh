# deploy/grafana

本目录只放产物：`dashboards/tunnelmesh.json`（唯一 Dashboard）与 `provisioning/`（自动装载
Dashboard 和数据源）。导入步骤、Row 结构、指标与标签约束、告警责任和校验命令统一写在
[可观测性文档](../../docs/operations/observability.md)，此处不重复。

修改 `dashboards/tunnelmesh.json` 后执行 `go test ./deploy/grafana -count=1`。
