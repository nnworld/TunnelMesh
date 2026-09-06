# Task 4 Report: Metadata API, OpenAPI, and Audit Behavior

## Scope

Implemented the read-only runtime metadata API for agents:

- `GET /api/v1/agents/{agentId}/metadata`
- optional `includeStale=true` query parameter
- unified `{code,msg,data}` responses
- owner/admin authorization with metadata existence protected behind agent authorization
- bounded public serialization that excludes stored JSON, expiry, and last-seen internals
- sensitive metadata values remain redacted
- successful reads emit `agent.metadata.read` audit events
- metadata write methods return `405 Method Not Allowed`

## Stale semantics

Expired or explicitly stale snapshots return `404` by default. A caller with access to the agent may opt in with `includeStale=true`; the response then returns the last snapshot with `stale: true`.

## Verification

- Added API coverage for unauthenticated, owner/admin, unrelated user, missing metadata, stale opt-in, redaction, bounded serialization, audit behavior, and write-method rejection.
- Ran the focused test red phase before implementation and confirmed it failed because the route returned the public agent response.
- Ran the focused green test after implementation: `go test ./internal/server -run MetadataAPI -count=1`.
