# Task 7 independent review

Scope reviewed: `deploy/grafana` and `deploy/prometheus` artifacts from
`task-7-brief.md`. The dashboard JSON parses, the repository's Grafana schema
test passes, and the files contain one dashboard with exactly five required
rows. The findings below are against the metrics currently defined in
`internal/observability/metrics.go` and the requested missing/zero-series
alert behavior.

## Findings

### [P1] Alerts do not fire when their primary series is absent

- `deploy/prometheus/alert-rules.yaml:5`: `min(tunnelmesh_ready{component="server"}) < 1` evaluates to an empty vector when the readiness series is missing, so `TunnelMeshServerNotReady` stays inactive instead of reporting an unhealthy/missing server.
- `deploy/prometheus/alert-rules.yaml:10`: `sum(tunnelmesh_connections_active{component="agent"}) == 0` likewise produces no result when the active-connection series is absent; `TunnelMeshAgentOffline` cannot detect a missing agent metric.
- `deploy/prometheus/alert-rules.yaml:15,20,25`: heartbeat, relay, and storage failure rules use `sum(rate(...)) > 0`; missing counters produce an empty vector, not a zero/positive result, so these alerts silently disappear when the corresponding instrumentation is absent.
- `deploy/prometheus/alert-rules.yaml:30`: `tunnelmesh:probe:success_ratio5m < 0.95` is empty when the probe recording rule has no input series, so probe instrumentation loss is not actionable. `absent(...)` (or an explicit `or`) is needed where missing data is intended to be unhealthy; the scrape-absence rule at line 35 does not cover partial metric loss.

### [P1] Dashboard and alert queries reference metrics that do not exist in the current implementation

The only collectors currently registered are the connection, stage, heartbeat,
bytes, stream, probe, and ready families in `internal/observability/metrics.go`.
The following references therefore render empty panels and make alerts inert:

- `deploy/grafana/dashboards/tunnelmesh.json:44-47`: `tunnelmesh_registry_lease_total`, `tunnelmesh_relay_total`, `tunnelmesh_storage_operation_duration_seconds`, and `tunnelmesh_storage_errors_total` are not defined.
- `deploy/grafana/dashboards/tunnelmesh.json:52`: `tunnelmesh_config_reload_total` is not defined.
- `deploy/prometheus/alert-rules.yaml:20,25`: `tunnelmesh_relay_total` and `tunnelmesh_storage_errors_total` are not defined, so the relay/storage alerts never fire.

If these metric families are intentionally deferred to a later task, these
panels/rules should be gated or added together with the instrumentation; they
should not ship as apparently active observability.

### [P2] Grafana variables are misleading and unused

- `deploy/grafana/dashboards/tunnelmesh.json:15-16`: both `node_id` and `agent_id` query `label_values(tunnelmesh_connections_active, component)`. The metric has `component` and `mode` labels, not `node_id` or `agent_id`; these variables therefore show component values under incorrect labels.
- None of the panel expressions interpolates `${cluster}`, `${node_id}`, or `${agent_id}`. The variables cannot filter any panel, and the custom `cluster` variable at line 14 has no metric-backed values. This makes the dashboard controls non-functional and can mislead operators about selected scope.

## Checks performed

- `go test ./deploy/grafana -count=1` — PASS (`TestTunnelMeshDashboardSchema`).

## Re-review

The P1 findings were addressed in `task-7-fix1-review.md`: the referenced
metric families are now registered, and the affected alerts use `absent(...)`
fallbacks. The schema test now recursively checks nested row panels and
requires all three variables to be used in PromQL. `promtool` was unavailable
in the environment, so native Prometheus parser validation remains deferred.
- Python JSON decode and recursive panel/target inspection — PASS for JSON syntax, one dashboard, five rows, and `${DS_PROMETHEUS}` on leaf panels.
- YAML files were inspected for rule/provisioning structure. `promtool` is not installed in the review environment, so native Prometheus rule/config validation could not be run.
