# TunnelMesh Grafana 观测

本目录只提供一份 Dashboard：`dashboards/tunnelmesh.json`。导入后，所有观测图表都在同一个 Dashboard 内，使用五个 Row 组织：Overview、Agent、Network、Cluster、Security；不会拆成多个 Dashboard。

## 自动配置

将本目录挂载到 Grafana 容器：

```yaml
volumes:
  - ./deploy/grafana/dashboards:/var/lib/grafana/dashboards/tunnelmesh:ro
  - ./deploy/grafana/provisioning:/etc/grafana/provisioning:ro
```

Prometheus 使用 `deploy/prometheus/prometheus.yml.example` 作为起点，并加载 recording/alert rules。Grafana datasource 的变量名是 `${DS_PROMETHEUS}`，导入时选择 Prometheus 数据源即可。

## 指标约束

Dashboard 查询只使用低基数标签（component、mode、protocol、stage、result、error_class、probe_kind）。禁止将 token、连接 ID、stream ID、目标地址或 metadata 放入 Prometheus label；这些信息只能进入脱敏日志或 trace。

## 验证

```bash
go test ./deploy/grafana -count=1
promtool check rules deploy/prometheus/recording-rules.yaml deploy/prometheus/alert-rules.yaml
promtool check config deploy/prometheus/prometheus.yml.example
```

实际部署前请在 Grafana UI 中确认 Prometheus datasource 可达，并根据环境调整告警联系人和通知策略。
