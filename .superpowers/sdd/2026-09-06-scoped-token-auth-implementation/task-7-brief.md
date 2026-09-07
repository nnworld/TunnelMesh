### Task 7: Protect Server-node relay with Token plus mTLS

**Files:**
- Modify: `internal/relay/transport.go`
- Modify: `internal/relay/relay_test.go`
- Modify: `internal/server/runtime.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Produces: gRPC client/server interceptors that validate `TokenTypeServerNode` and bind `node_id` to the mTLS peer identity and Token binding.

- [ ] **Step 1: Write failing relay authentication tests**

Use test certificates and gRPC metadata to cover valid mTLS+Token, missing client certificate, wrong CA, wrong Token type, mismatched node ID, revoked Token, and stale epoch.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/relay -run 'MTLS|ServerNodeToken' -count=1
```

- [ ] **Step 3: Implement interceptors and configuration**

Require TLS client cert verification and `authorization: Bearer` metadata. Validate the certificate SAN against the registered node ID, then validate the `server_node` Token binding before `OpenStream` reads target fields.

- [ ] **Step 4: Verify GREEN**

```bash
go test ./internal/relay ./internal/server -count=1
go test -race ./internal/relay
```

