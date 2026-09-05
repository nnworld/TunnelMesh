# Task 4 Report: Authentication and Administrator Bootstrap Recovery

## Status

Implemented and locally verified in the task worktree. Authentication now uses
Argon2id password hashes and opaque, SHA-256-hashed API tokens. First-start
administrator bootstrap and credential recovery are guarded by a database
transaction writer lock (with a process mutex for local fast-path contention)
and persist through the storage repository interfaces only.

## Delivered

- Added `internal/auth` services for user creation, login, token validation,
  role authorization, one-time administrator bootstrap credentials, and
  confirmed administrator credential regeneration.
- Added bounded Argon2id PHC encoding/verification (`$argon2id$v=19`) with
  cryptographically random salts; clear-text passwords are never sent to
  storage repositories.
- Added opaque random API token generation. Only the SHA-256 token digest is
  persisted; revoked and expired tokens are rejected during validation.
- Added concurrent-safe bootstrap and recovery locking. Database-backed calls
  execute through `storage.DB.AuthTransaction`, which takes a portable schema
  writer lock and commits user, token, and audit changes atomically. Bootstrap
  creates one admin and returns printable credentials only on the creating call.
  Recovery requires explicit confirmation, rotates the password, revokes all
  existing admin sessions with cursor pagination, writes an audit record, and
  returns the new credentials once.
- Added `tunnelmesh-server admin regenerate-credentials --config PATH
  --confirm`, with output restricted to the command's console stream.
- Added focused authentication/bootstrap tests, race coverage, and CLI command
  surface coverage.

## Verification

- `go test ./internal/auth -v -count=1` — pass.
- `go test -race ./internal/auth -count=1` — pass.
- `go test ./... -count=1` — pass.
- `go vet ./...` — pass.
- `git diff --check` — pass.

## Concerns / follow-ups

- Repository-only constructors (used by isolated unit tests) cannot provide a
  cross-process transaction lock; production constructors receive `*storage.DB`
  and use the database transaction path.
- MySQL execution was not available in this environment; authentication uses
  the shared repository interfaces and receives the same driver behavior as
  other services.
