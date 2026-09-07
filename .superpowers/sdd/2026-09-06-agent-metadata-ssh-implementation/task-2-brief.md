### Task 2: Metadata WebSocket protocol and session reporting

**Files:** Modify `internal/protocol/frame.go`, `internal/protocol/codec.go`, `internal/protocol/protocol_test.go`, `internal/server/session_manager.go`, `internal/server/ws_agent.go`, and `internal/agent/session.go`; create `internal/server/metadata_protocol_test.go`.

**Interfaces:** Produce `FrameAgentHello`, `FrameAgentMetadataUpdate`, `FrameAgentMetadataAck`, bounded payload encoding, and server metadata callbacks.

- [ ] Write failing tests for valid frames, unknown control types, oversized payloads, mismatched Agent ID/epoch, duplicate revisions, lower revisions, and field-level ACK errors.
- [ ] Run `go test ./internal/protocol ./internal/server -run Metadata -count=1` and verify RED.
- [ ] Add control frame types without changing existing numeric values; validate size before JSON decoding.
- [ ] Send a full snapshot after authenticated registration and after reconnect; send updates on revision changes; keep metadata errors separate from data streams.
- [ ] Fence by authenticated Agent ID and epoch, accept equal revision replay idempotently, and return structured ACK errors.
- [ ] Run `go test ./internal/protocol ./internal/server ./internal/agent -count=1` and the matching race tests.

