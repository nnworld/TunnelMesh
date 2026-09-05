# Task 6 report — Registry implementations and cluster ownership

## Delivered

- Added `NodeRegistry` with `Register`, `KeepAlive`, `ResolveAgent`, `Watch`, and `Revoke` operations plus epoch/fencing-aware `NodeOwner` metadata.
- Added `DatabaseRegistry`, backed by the existing `server_nodes` and `agent_runtime_leases` repositories. Lease acquisition, renewal, takeover and release inherit the storage layer's conditional epoch fencing for SQLite and MySQL.
- Added in-process database watch events for register, keep-alive and revoke transitions.
- Added `EtcdRegistry` using `/tunnelmesh/nodes/<node-id>` and `/tunnelmesh/agents/<agent-id>` keys. Agent ownership uses an etcd lease; registration uses a transaction CAS and a persistent per-agent epoch marker so takeover on a different node still increments fencing epochs; persistent node records retain the last owner metadata; watches use the etcd watch stream with previous values for revoke events.
- Added endpoint constructor `NewEtcdRegistryFromEndpoints` and integration tests gated by `TUNNELMESH_TEST_ETCD_ENDPOINTS`; MySQL integration is gated by `TUNNELMESH_TEST_MYSQL_DSN`.
- Added etcd client dependency (`go.etcd.io/etcd/client/v3`).

## TDD evidence

The registry contract test was authored against the unimplemented interface first, then adapters were implemented until acquisition, renewal, expiration, takeover, stale fencing, watch, and revoke assertions passed.

## Verification

```text
go test ./internal/registry -v -count=1       PASS (MySQL/etcd integration skipped when env vars are absent)
go test -race ./internal/registry -count=1    PASS
go vet ./...                                  PASS
go test ./... -count=1                        PASS
git diff --check                              PASS
```

## Commit

Implementation commit is recorded in the task branch after verification.
