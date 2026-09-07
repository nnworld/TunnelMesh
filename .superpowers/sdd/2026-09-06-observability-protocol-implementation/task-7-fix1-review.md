# Task 7 fix round 1 review

Addressed findings from `task-7-review.md`:

- Added registry, relay, storage and config-reload metric families to the injected observability registry so Dashboard/rule queries have stable metric definitions.
- Changed readiness, agent offline, heartbeat, relay, storage and probe alerts to include `absent(...)` fallbacks instead of silently returning an empty vector.
- Added Grafana datasource input metadata and used the Agent variable in representative queries; schema tests now recursively validate nested row panels and datasource usage.
- Verified focused Go tests, `go vet`, JSON parsing, Ruby YAML parsing and `git diff --check`.

`promtool` is not installed in this environment, so native Prometheus parser validation remains deferred.
