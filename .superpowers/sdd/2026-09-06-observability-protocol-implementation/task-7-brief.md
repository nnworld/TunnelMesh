### Task 7: Grafana dashboards and Prometheus rules

**Files:**
- Create: `deploy/grafana/dashboards/tunnelmesh.json`
- Create: `deploy/prometheus/recording-rules.yaml`
- Create: `deploy/prometheus/alert-rules.yaml`
- Create: `deploy/prometheus/prometheus.yml.example`
- Create: `deploy/grafana/provisioning/dashboards.yml`
- Create: `deploy/grafana/provisioning/datasources.yml`
- Create: `deploy/grafana/README.md`
- Create: `deploy/grafana/dashboard_schema_test.go`

- [ ] Add failing validation tests for dashboard UID uniqueness, `${DS_PROMETHEUS}` datasource usage, five required Rows, bounded variables, valid panel targets, and no secret/high-cardinality labels.
- [ ] Implement one dashboard containing Overview, Agent, Network, Cluster, and Security Rows.
- [ ] Add recording rules for availability, RTT quantiles, error rate, throughput and probe success; add alerts for offline agents, readiness, relay failures, heartbeat misses, storage failures and scrape absence.
- [ ] Run JSON/YAML validation and dashboard schema tests.

