# Task 3 review fix report

- Enforced the 4 KiB per-item metadata value limit before JSON persistence.
- Added expiry-aware stale computation to service `List`, matching `Get`.
- Added regression tests for oversized individual fields and expired list rows.

Verification: focused metadata tests, full `go test`, full race tests, `go vet`,
and `git diff --check` pass.
