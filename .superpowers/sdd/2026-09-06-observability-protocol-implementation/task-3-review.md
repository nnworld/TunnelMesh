# Task 3 review

## Result: FAIL

### Findings

#### [P1] User-controlled protocol is used as a Prometheus label

`internal/agent/session.go:105`, `:127`, and `:143` pass `p.Protocol`/`entry.protocol`
directly to `ObserveStream` and `ObserveBytes`. `StreamOpenPayload.Protocol` is
decoded from the peer payload and is not restricted to a finite allowlist before
these calls. The metrics helper only trims to 64 bytes; it does not canonicalize
unknown values. An authenticated or compromised Agent peer can therefore create
unbounded distinct `protocol` label values (and corresponding time series),
causing Prometheus memory/cardinality exhaustion. Map protocols to a fixed
allowlist (`tcp`, `udp`, `http`, `websocket`, or `unknown`) before observing, and
use `unknown` for anything else.

### Verification

```text
go test ./internal/observability ./internal/agent ./internal/storage -count=1   PASS
go test -race ./internal/observability ./internal/agent ./internal/storage -count=1   PASS
go vet ./internal/observability ./internal/agent ./internal/storage   PASS
git diff --check   PASS
```

The focused tests and static checks pass, but the cardinality issue blocks
approval until labels are constrained to a finite set.

