# SDD ledger — plan: docs/superpowers/plans/2026-09-06-agent-metadata-ssh-implementation.md

## Pre-flight scan

### Shared files and interfaces

| Tasks | Shared surface | Finding / ruling |
|---|---|---|
| 1 → 2 | `MetadataSnapshot`, Agent config | Task 1 owns source collection and limits; Task 2 consumes a deterministic snapshot. No conflict. |
| 2 → 3 | Metadata frame callback and epoch/revision | Task 2 emits validated snapshots; Task 3 owns persistence fencing. No protocol/storage coupling beyond the Service input. |
| 3 → 4 | `AgentMetadataService` | Task 3 produces read/write Service methods; Task 4 consumes read methods and does not access Repository directly. |
| 4 → 5 | Metadata API response | Task 4 defines the response/OpenAPI contract; Task 5 uses it for the Agent detail view. |
| 2 → 6 | Existing TCP proxy/session | Task 6 reuses current TCP proxy and does not alter metadata frame semantics. |
| 5 → 6 | Client/Agent documentation | Task 5 adds backend docs; Task 6 extends client/agent/SSH docs. No file ownership conflict. |

### Per-task self-consistency

| Task | Finding / ruling |
|---|---|
| 1 | Tests cover every source and limit rule named; files and produced interfaces agree. |
| 2 | Control frames preserve existing values and tests cover fencing/idempotency. |
| 3 | DDL, model, repository and Service responsibilities align; SQLite is required and MySQL is optional when configured. |
| 4 | API tests cover authentication, ownership, stale state and read-only behavior; OpenAPI endpoint matches the handler. |
| 5 | Frontend route/view and server-admin documentation are independent deliverables with a frontend test/build gate. |
| 6 | SSH uses existing TCP proxy; integration tests and docs do not introduce a second command protocol. |

### Rulings

- Ruling: Keep metadata in a dedicated runtime table rather than adding columns to `agents` — runtime freshness and fencing differ from managed Agent configuration; cost is one additional repository and migration surface.
- Ruling: Use explicit file/env allowlists and reject command sources — this prevents metadata configuration from becoming remote code execution; cost is that arbitrary host discovery is deferred.
- Ruling: Treat SSH public-key proxy as the first-class command execution path — host sshd remains the authorization boundary; cost is requiring SSH service and key setup on the host.

## Task status

- Task 1: complete (commits dad6f65..3ff15f1, fix baf2b54; review clean)
- Task 2: complete (commits baf2b54..fad1114, fix 870912b; review clean)
- Task 3: complete (commits 870912b..4c344a6, fix 71b5d6c; review clean)
- Task 4: complete (commits 71b5d6c..7e34c1f, fix 9ea2368; review clean)
- Task 5: complete (commits 9ea2368..db575b4, fix 91c9d17; review clean)
- Task 6: complete (commits 91c9d17..6aa8cab, fix bd6e289; review clean)
- Runtime integration residuals: complete (commits 512bf2e, dc49e0f, bearer-token validation and TLS deployment documentation)
- Agent runtime client residual: complete (commit 26b239c; bearer-configured ws/wss dial, binary protocol transport, CLI run wiring, and end-to-end persistence coverage)
