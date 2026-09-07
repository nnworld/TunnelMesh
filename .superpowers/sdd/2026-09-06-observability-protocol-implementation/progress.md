# SDD ledger — plan: docs/superpowers/plans/2026-09-06-observability-protocol-implementation.md

## Execution rulings

- Preserve all existing uncommitted security, metadata, SSH, Docker, and documentation changes.
- No commit/push/merge without explicit authorization.
- Grafana is one dashboard file with five Rows, not five dashboard files.
- Nginx guidance is a first-class deployment document at `docs/deployment/nginx.md`.
- Real MySQL contract remains deferred when `TUNNELMESH_TEST_MYSQL_DSN` is absent.
- Ruling: Task 7 implementer dispatch was attempted with the available model identifiers but the collaboration backend rejected each identifier as unknown; proceed with local implementation after preserving the same task brief and review requirements.

## Task ledger

- [x] Task 1: Observability primitives and redacted structured events
- [x] Task 2: Health, readiness, and Prometheus HTTP endpoints
- [x] Task 3: Runtime instrumentation
- [x] Task 4: Runtime stats and probe persistence
- [ ] [ ] Task 5: Capability-gated protocol extensions
- [ ] Task 6: Relay protobuf contract
- [x] Task 7: Unified Grafana dashboard and Prometheus rules
- [x] Task 8: Documentation and completeness audit
- [ ] Task 9: Integration and failure injection
Task 1: fix round 1/5 (2 addressed, 0 open — JSON tags restored and active gauges bounded; scoped review found metadata-value redaction and concurrency publication issues).
Task 1: fix round 2/5 (2 addressed, 0 open — all metadata values are redacted and Gauge.Set is inside the active-state lock; scoped re-review PASS).
Task 1: complete (no commits authorized).
Task 2: complete (no commits authorized; independent review PASS).
Task 3: fix round 1/5 (1 addressed, 0 open — peer-controlled protocol labels normalized to a finite allowlist; scoped re-review PASS).
Task 3: complete (no commits authorized; full Go/race/vet/diff-check reported PASS by implementer).
Task 4: fix round 1/5 complete (MySQL-indexable timestamp columns, probe epoch fencing, bounded values, deadline and cursor scope findings addressed; focused tests PASS; live MySQL contract deferred because TUNNELMESH_TEST_MYSQL_DSN is absent).
Task 7: complete locally after implementer dispatch was rejected by the collaboration backend; RED schema test failed before dashboard creation, then GREEN focused test passed. Independent review findings were addressed for missing observability metric families and empty-vector alerts. promtool remains deferred because it is not installed.
Task 8: complete locally with observability, SLO, capacity, completeness, architecture, cluster and protocol documentation; Nginx guidance is in docs/deployment/nginx.md. No commit/push authorized.
