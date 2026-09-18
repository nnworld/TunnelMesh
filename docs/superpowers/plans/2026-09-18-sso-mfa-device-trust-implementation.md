# SSO, MFA, and device trust implementation plan (Phase A)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship enterprise identity for the management console: OIDC single sign-on, TOTP multi-factor authentication with recovery codes, and revocable trusted devices, all audited and observable.

**Architecture:** Add a database-authoritative identity layer beside the existing local-password login. `internal/auth/oidc` is a self-contained relying party with no database or handler knowledge. `internal/auth` services (`LoginService`, `MFAService`, `DeviceService`, `OIDCProviderService`, `AuthPolicyService`) orchestrate repositories and emit audit rows. `internal/server` gains thin handlers only. Short-lived cross-node state (login MFA challenges, OIDC state, login tickets) lives in an encrypted `auth_challenges` table so any cluster node can complete a flow started on another node.

**Tech Stack:** Go 1.26 standard library (`crypto/hmac`, `crypto/sha1`, `crypto/rsa`, `crypto/ecdsa`, `crypto/ed25519`, `encoding/base32`, `encoding/jose`-free JWK parsing), existing `golang.org/x/crypto/argon2`, existing SQLite/MySQL storage layer, existing `internal/auth.SecretStore` AES-256-GCM, Vue 3 + TypeScript + Element Plus console, Vitest.

**Spec:** `docs/superpowers/specs/2026-09-18-sso-mfa-device-trust-design.md`
**Roadmap:** `docs/superpowers/specs/2026-09-18-enterprise-capability-roadmap-design.md`

## Global Constraints

- No new third-party Go dependency. OIDC, JWKS, and TOTP are implemented with the standard library because signature and claim validation must be explicit, testable, and free of transitive supply chain growth.
- `alg=none` and every `HS*` algorithm are rejected unconditionally in `id_token` verification. There is no configuration that enables them.
- No plaintext secret is persisted, logged, audited, exported, or returned by an API. OIDC `client_secret`, TOTP shared secrets, and PKCE verifiers are AES-GCM ciphertext produced by `internal/auth.SecretStore`. When `TUNNELMESH_TOKEN_ENCRYPTION_KEY` is absent, enrollment and provider creation fail closed with `503` and `data.error = "secret_storage_unavailable"`.
- Recovery codes, device tokens, and login attempt bucket keys are stored only as one-way hashes.
- `POST /api/v1/auth/login` keeps returning `{token, user}` when MFA is not triggered. `auth_settings.mfa_mode` defaults to `disabled`, so an upgraded deployment behaves exactly as before until an operator opts in.
- Schema v14 is additive only. `migrations/ddl.sql`, `migrations/incremental/v0013_to_v0014/{mysql,sqlite}.sql`, `storage.SchemaVersion`, migration tests, and `docs/operations/schema-upgrades.md` all change in the same task.
- Released incremental scripts are immutable.
- Audit `details` never contain passwords, hashes, TOTP codes, recovery codes, device tokens, tickets, `code_verifier`, `client_secret`, or full `Authorization` headers.
- All authorization is evaluated from the bearer principal. `userId`, `role`, and `providerId` values from a request body are never trusted as identity.
- Do not add `go test`, `go test -race`, or `go vet` to CI; the user explicitly rejected that change.
- Do not commit, push, merge, or open a remote PR without explicit user authorization.
- Do not include `Co-Authored-By`, `Generated-by`, or any AI attribution trailer in commit messages.
- `web/src/i18n/messages/zh-CN.ts` defines `MessageSchema`; add keys there first, then mirror them in `en-US.ts` or the frontend test suite fails.
- Run the full local validation set even though CI stays lightweight.

---

### Task 1: Schema v14 migration and identity models

**Files:**

- Modify: `migrations/ddl.sql`
- Create: `migrations/incremental/v0013_to_v0014/mysql.sql`
- Create: `migrations/incremental/v0013_to_v0014/sqlite.sql`
- Modify: `internal/storage/db.go`
- Modify: `internal/storage/models.go`
- Modify: `internal/storage/db_test.go`
- Modify: `docs/operations/schema-upgrades.md`

**Interfaces:**

- Produces: `storage.SchemaVersion == 14`.
- Produces: model structs `AuthSettings`, `OIDCProvider`, `UserIdentity`, `UserMFA`, `UserRecoveryCode`, `UserDevice`, `AuthChallenge`, `AuthLoginAttempt` in `internal/storage/models.go`.
- Produces: `User.AuthSource string` and `User.MFARequired bool` fields.
- Produces: constants `AuthSourceLocal`, `AuthSourceOIDC`, `AuthSourceMixed`, `MFAModeDisabled`, `MFAModeOptional`, `MFAModeRequired`, `MFAStatusPending`, `MFAStatusEnabled`, `ChallengeKindLoginMFA`, `ChallengeKindOIDCState`, `ChallengeKindLoginTicket`, `PasswordHashNone = "*"`.
- Consumes: nothing; this task is the foundation for Tasks 2-10.

**Steps:**

- [ ] Write the failing test first in `internal/storage/db_test.go`:
  - `TestSchemaVersionIsFourteen` asserts `SchemaVersion == 14`.
  - `TestMigrateV13ToV14AddsIdentityTables` opens a SQLite file, forces `schema_meta.version = 13` with the v13 table set, runs the migration path, and asserts every new table exists and `users` has `auth_source` and `mfa_required` columns with the documented defaults.
  - `TestMigrateV13ToV14IsIdempotent` runs the migration twice and asserts the second run reports no change and no error.
  - `TestFreshDatabaseMatchesIncrementalChain` builds one database from `migrations/ddl.sql` and another by replaying v0005→v0014 incremental scripts, then asserts both expose the same table set and, for the identity tables, the same column names.
- [ ] Run `go test ./internal/storage -run 'SchemaVersion|V13ToV14|IncrementalChain' -count=1` and confirm it fails with `SchemaVersion == 13` and missing-table errors. Record the failing output.
- [ ] Add the eight DDL blocks from spec section 3 to `migrations/ddl.sql`, keeping the existing file's `CREATE TABLE IF NOT EXISTS` style, `VARBINARY(255)` identifier convention, and index naming (`idx_<table>_<cols>`). Add the two `users` columns to the `users` block in `ddl.sql` as `auth_source VARCHAR(32) NOT NULL DEFAULT 'local'` and `mfa_required INTEGER NOT NULL DEFAULT 0`.
- [ ] Write `migrations/incremental/v0013_to_v0014/sqlite.sql` with `ALTER TABLE users ADD COLUMN ...` statements and the eight `CREATE TABLE IF NOT EXISTS` blocks plus indexes.
- [ ] Write `migrations/incremental/v0013_to_v0014/mysql.sql` with the same intent using MySQL types (`VARBINARY(255)`, `VARCHAR(n)`, `INTEGER`, `TEXT`, `ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`). `UNIQUE(provider_id, subject)` on `user_identities` and `UNIQUE(code_hash)` on `user_recovery_codes` must be declared inline so both drivers agree.
- [ ] Bump `SchemaVersion` to `14` in `internal/storage/db.go`.
- [ ] Add the model structs and constants to `internal/storage/models.go`. Every struct carries only the columns it owns; no secret field is added to a struct that is rendered by an API handler.
- [ ] Add the v13 → v14 upgrade and rollback section to `docs/operations/schema-upgrades.md`, stating that the change is additive, that a binary downgrade requires a backup restore because `SchemaVersion` validation rejects a newer database, and that feature-level rollback is `mfa_mode='disabled'` plus disabling all providers.
- [ ] Run `go test ./internal/storage -count=1` and confirm green.

**Verification:**

```bash
go test ./internal/storage -count=1
go vet ./internal/storage ./migrations
git diff --check
```

Expected: all tests pass; `git diff --check` prints nothing.

**Rollback:** Delete the new incremental directory, revert `ddl.sql`, `models.go`, and `SchemaVersion`. A database already migrated to v14 must be restored from backup before running a v13 binary.

---

### Task 2: Identity repositories

**Files:**

- Create: `internal/storage/identity_repository.go`
- Create: `internal/storage/identity_repository_test.go`
- Modify: `internal/storage/db.go`
- Modify: `internal/storage/storage_contract_test.go`

**Interfaces:**

- Produces: `AuthSettingsRepository { Get(ctx) (AuthSettings, error); Update(ctx, AuthSettings) error; SeedIfMissing(ctx, AuthSettings) (AuthSettings, error) }`.
- Produces: `OIDCProviderRepository { Create; Get(ctx,id); GetByName(ctx,name); List(ctx,cursor,limit) (Page[OIDCProvider], error); Update; Delete; ListEnabledPublic(ctx) ([]OIDCProvider, error); CountEnabled(ctx) (int, error) }`.
- Produces: `UserIdentityRepository { Create; GetByProviderSubject(ctx,providerID,subject); ListByUser(ctx,userID); UpdateLastLogin(ctx,id,time.Time); Delete(ctx,id); DeleteByUser(ctx,userID) }`.
- Produces: `UserMFARepository { Upsert(ctx,UserMFA); Get(ctx,userID); SetStatus(ctx,userID,status,time.Time); MarkUsed(ctx,userID string, step int64, when time.Time) error; Delete(ctx,userID) }`.
- Produces: `UserRecoveryCodeRepository { ReplaceAll(ctx,userID string, hashes [][]byte) error; ListUnused(ctx,userID) ([][]byte, error); Consume(ctx,userID string, hash []byte) (bool, error) }`.
- Produces: `UserDeviceRepository { Create; GetByTokenHash(ctx,hash) (UserDevice, error); ListByUser(ctx,userID); Touch(ctx,id,time.Time); Revoke(ctx,id); RevokeAllForUser(ctx,userID); CountActive(ctx,userID) (int, error); OldestActive(ctx,userID) (UserDevice, error); DeleteExpired(ctx, time.Time) (int64, error) }`.
- Produces: `AuthChallengeRepository { Create; Get(ctx,id) (AuthChallenge, error); Consume(ctx,id) (bool, error); IncrementAttempts(ctx,id) (attempts int, consumed bool, err error); DeleteExpired(ctx, time.Time) (int64, error); CountPending(ctx) (int, error) }`.
- Produces: `AuthLoginAttemptRepository { RegisterFailure(ctx,bucketKey string, window time.Duration, maxAttempts int, block time.Duration) (blocked bool, retryAfter time.Duration, err error); IsBlocked(ctx,bucketKey string) (bool, time.Duration, error); RegisterSuccess(ctx,bucketKey string) error; CountBlocked(ctx) (int, error) }`.
- Produces: `DB` accessors `AuthSettings()`, `OIDCProviders()`, `UserIdentities()`, `UserMFA()`, `UserRecoveryCodes()`, `UserDevices()`, `AuthChallenges()`, `AuthLoginAttempts()`, plus `IdentityTransaction(ctx, func(IdentityRepositories) error) error` where `IdentityRepositories` bundles users, MFA, recovery codes, devices, challenges, and audits for atomic login and enrollment writes.
- Consumes: Task 1 models.

**Steps:**

- [ ] Write `internal/storage/identity_repository_test.go` first with a SQLite temp database created through the existing test helper used by `credential_repository_test.go`. Cover, per repository:
  - round-trip create/get/update/delete,
  - cursor pagination ordering and `hasMore` for `OIDCProviderRepository.List`,
  - the `UNIQUE(provider_id, subject)` constraint returning a typed `ErrConflict`-style error,
  - `Consume` returning `false` on the second call,
  - `IncrementAttempts` returning monotonically increasing counts and `consumed=true` once the row is consumed,
  - `RegisterFailure` returning `blocked=true` and a positive `retryAfter` only after `maxAttempts` inside the window, and `false` after the window rolls over,
  - `MarkUsed` refusing to move `last_used_step` backwards,
  - `DeleteExpired` removing only rows past the cutoff,
  - `IdentityTransaction` rolling back every write when the callback returns an error.
- [ ] Add the same repositories to `internal/storage/storage_contract_test.go` so the existing MySQL contract harness exercises them when `TUNNELMESH_MYSQL_TEST_DSN` is set.
- [ ] Run `go test ./internal/storage -run Identity -count=1` and confirm it fails to compile against the missing types. Record the output.
- [ ] Implement `internal/storage/identity_repository.go` following the existing `credential_repository.go` conventions: constructor functions returning interfaces, `sql.ErrNoRows` mapped to the package's not-found sentinel, timestamps encoded with the same helper the rest of the file uses, and no `SELECT *`.
- [ ] Wire the repositories and accessors into the `DB` struct in `internal/storage/db.go`, constructing them in the same place the existing repositories are constructed so both drivers get identical wiring.
- [ ] Run `go test ./internal/storage -count=1` and confirm green.

**Verification:**

```bash
go test ./internal/storage -count=1
go test -race ./internal/storage
go vet ./internal/storage
git diff --check
```

Expected: all pass.

**Rollback:** Delete `identity_repository.go` and its test, revert the `DB` wiring. The v14 tables remain harmless.

---

### Task 3: TOTP and recovery code primitives

**Files:**

- Create: `internal/auth/totp.go`
- Create: `internal/auth/totp_test.go`
- Create: `internal/auth/recovery_code.go`
- Create: `internal/auth/recovery_code_test.go`

**Interfaces:**

- Produces: `type TOTPParams struct { Digits int; Period time.Duration; Skew int }`.
- Produces: `GenerateTOTPSecret() (string, error)` returning a base32 160-bit secret.
- Produces: `TOTPCode(secret string, at time.Time, p TOTPParams) (string, error)`.
- Produces: `VerifyTOTP(secret, code string, at time.Time, p TOTPParams) (step int64, ok bool, err error)` where `step` is the accepted counter value used for the replay guard.
- Produces: `OTPAuthURL(issuer, account, secret string, p TOTPParams) (string, error)`.
- Produces: `GenerateRecoveryCodes(count int) (codes []string, hashes [][]byte, err error)`.
- Produces: `HashRecoveryCode(code string) []byte` and `RecoveryCodeMatches(hash, candidate []byte) bool`.
- Produces: `IsRecoveryCode(candidate string) bool` detecting the `tmrc-` prefix.
- Consumes: nothing.

**Steps:**

- [ ] Write `internal/auth/totp_test.go` first:
  - RFC 6238 Appendix B SHA-1 vectors for T = 59, 1111111109, 1111111111, 1234567890, 2000000000, 20000000000 with the standard ASCII secret `12345678901234567890`, asserting 6-digit and 8-digit outputs.
  - `Skew=1` accepts the previous and next step and rejects two steps away.
  - `VerifyTOTP` returns the accepted step so callers can enforce monotonicity.
  - Non-numeric, short, long, and empty codes return `ok=false` with no error.
  - A secret that is not valid base32 returns an error, not a panic.
  - `OTPAuthURL` produces `otpauth://totp/<issuer>:<account>?secret=…&issuer=…&algorithm=SHA1&digits=…&period=…` with percent-encoded label and issuer.
- [ ] Write `internal/auth/recovery_code_test.go` first:
  - generated codes are unique, match `^tmrc-[A-Z2-7]{20}$`, and hashes are 32 bytes,
  - `RecoveryCodeMatches` is case-insensitive on the base32 body and ignores surrounding whitespace,
  - a wrong code never matches,
  - `IsRecoveryCode` accepts the prefix form and rejects a 6-digit TOTP code.
- [ ] Run `go test ./internal/auth -run 'TOTP|Recovery' -count=1` and confirm a compile failure. Record the output.
- [ ] Implement `internal/auth/totp.go` with `crypto/hmac`, `crypto/sha1`, `encoding/base32`, and `math/big`-free big-endian counter encoding. Dynamic truncation follows RFC 4226 section 5.3. Comparison uses `hmac.Equal` on the digit strings encoded as bytes.
- [ ] Implement `internal/auth/recovery_code.go` with `crypto/rand` and `crypto/sha256`, and a constant-time comparison via `hmac.Equal`.
- [ ] Add a doc comment on each exported function stating the RFC it implements and why SHA-1 is retained (authenticator app compatibility, not a security downgrade of the shared secret).
- [ ] Run `go test ./internal/auth -count=1` and confirm green.

**Verification:**

```bash
go test ./internal/auth -run 'TOTP|Recovery' -count=1
go test ./internal/auth -count=1
go vet ./internal/auth
```

Expected: RFC 6238 vectors match exactly; all tests pass.

**Rollback:** Delete the four files. Nothing else depends on them until Task 4.

---

### Task 4: MFA service

**Files:**

- Create: `internal/auth/mfa_service.go`
- Create: `internal/auth/mfa_service_test.go`
- Modify: `internal/auth/service.go`
- Create: `internal/auth/errors.go`
- Modify: `internal/auth/errors_test.go` (create if absent)

**Interfaces:**

- Produces: `type MFAService struct` constructed by `NewMFAService(repos MFARepositories, secrets SecretProvider, cfg MFAConfig, audits AuditWriter) *MFAService`.
- Produces: `MFARepositories` interface bundling `storage.UserMFARepository`, `storage.UserRecoveryCodeRepository`, `storage.UserDeviceRepository`, and `storage.UserRepository`.
- Produces: `SecretProvider interface { Encrypt(string) (ciphertext, nonce []byte, keyID string, version int, err error); Decrypt(ciphertext, nonce []byte, keyID string, version int) (string, error); Available() bool }` with an adapter over `*SecretStore`. This is the seam that keeps the service testable without a real key and lets `internal/server` reuse it for credentials.
- Produces: `MFAStatus struct { Status string; EnabledAt *time.Time; LastUsedAt *time.Time; RemainingRecoveryCodes int; Policy AuthPolicy }`.
- Produces: `Enrollment struct { Secret string; OTPAuthURL string; RecoveryCodes []string; ExpiresAt time.Time }`.
- Produces: methods `Status(ctx,userID)`, `Enroll(ctx,userID,currentPassword string)`, `Enable(ctx,userID,code string)`, `Disable(ctx,userID,code string)`, `AdminReset(ctx,actorID,userID string)`, `VerifyCode(ctx,userID,code string, at time.Time) (VerifyResult, error)`.
- Produces: `VerifyResult struct { Method string; RecoveryCodesExhausted bool }`.
- Produces: sentinel errors `ErrSecretStorageUnavailable`, `ErrMFANotEnrolled`, `ErrMFAPendingNotConfirmed`, `ErrMFACodeInvalid`, `ErrMFAReplayDetected`, `ErrMFARequiredByPolicy`, `ErrCurrentPasswordRequired`, `ErrLastAdminProtected` in `internal/auth/errors.go`.
- Consumes: Task 2 repositories, Task 3 primitives.

**Steps:**

- [ ] Write `internal/auth/mfa_service_test.go` first with in-memory fakes for the four repositories and a fake `SecretProvider`:
  - `Enroll` returns a decodable base32 secret, a well-formed `otpauth://` URL, and exactly `cfg.RecoveryCodes` codes; the stored row has `status=pending`, ciphertext non-empty, and no plaintext secret anywhere in the fake store.
  - `Enroll` twice replaces the pending row and invalidates the first secret.
  - `Enable` with a code generated from the returned secret flips `status` to `enabled` and writes an `auth.mfa.enable` audit row whose `details` contain no code and no secret.
  - `Enable` with a wrong code returns `ErrMFACodeInvalid` and leaves `status=pending`.
  - `VerifyCode` with a TOTP code returns `Method="totp"`; a second call with the same code in the same step returns `ErrMFAReplayDetected`.
  - `VerifyCode` with a recovery code returns `Method="recovery"`, marks it used, and decrements the remaining count; when the last code is consumed, `RecoveryCodesExhausted` is true.
  - `VerifyCode` when `status=pending` returns `ErrMFAPendingNotConfirmed`.
  - Every method returns `ErrSecretStorageUnavailable` when `SecretProvider.Available()` is false, and `Enroll` persists nothing in that case.
  - `Disable` with `Policy.Mode=required` returns `ErrMFARequiredByPolicy`; with `Mode=optional` it deletes the secret, deletes all recovery codes, revokes all trusted devices, and writes `auth.mfa.disable`.
  - `Enroll` for a user with `auth_source=local` requires `currentPassword`; a wrong password returns `ErrCurrentPasswordRequired`. A user with `auth_source=oidc` and `password_hash="*"` skips the password check.
  - `AdminReset` clears MFA, clears recovery codes, revokes devices, and writes `auth.mfa.reset` with the actor id.
  - `AdminReset` and `Disable` refuse to act on the last enabled administrator and return `ErrLastAdminProtected`.
- [ ] Run `go test ./internal/auth -run MFA -count=1` and confirm a compile failure. Record the output.
- [ ] Create `internal/auth/errors.go` collecting the sentinels above plus the existing `ErrInvalidCredentials`, `ErrUnauthenticated`, `ErrForbidden` re-exports so handlers have one import site. Keep `service.go` declarations as the definitions and have `errors.go` only add new ones, to avoid a duplicate-declaration break.
- [ ] Implement `internal/auth/mfa_service.go`. Encryption is the first operation in `Enroll`; if it fails, nothing is written. `VerifyCode` reads the row, decrypts, verifies, and only then calls `MarkUsed` with the accepted step so a failed verification never advances the replay guard.
- [ ] Add `AuthPolicy` resolution as a pure function `ResolvePolicy(settings storage.AuthSettings, user storage.User) AuthPolicy` in `internal/auth/service.go`, returning `{Mode string; Required bool; DeviceTrustEnabled bool; AllowBypass bool; SessionTokenTTL time.Duration}`. `Required` is true when `user.MFARequired` is true, or `Mode == required`, or (`Mode == optional` and the user has enabled MFA).
- [ ] Run `go test ./internal/auth -count=1` and confirm green.

**Verification:**

```bash
go test ./internal/auth -count=1
go test -race ./internal/auth
go vet ./internal/auth
```

Expected: all pass, including the replay and fail-closed cases.

**Rollback:** Delete `mfa_service.go`, `errors.go`, and their tests; revert the `ResolvePolicy` addition. No schema or handler change exists yet.

---

### Task 5: Device trust service

**Files:**

- Create: `internal/auth/device_service.go`
- Create: `internal/auth/device_service_test.go`
- Modify: `internal/auth/service.go`

**Interfaces:**

- Produces: `NewDeviceService(devices storage.UserDeviceRepository, cfg DeviceTrustConfig, audits AuditWriter) *DeviceService`.
- Produces: `DeviceTrustConfig struct { Enabled bool; TTL time.Duration; MaxPerUser int; CookieName string; CookieSecure bool; CookieSameSite string }`.
- Produces: `Issue(ctx, userID, userAgent, ip string) (token string, device storage.UserDevice, err error)`.
- Produces: `Validate(ctx, token string) (storage.UserDevice, bool, error)` where the boolean reports trust and the device row is returned for `last_seen_at` refresh.
- Produces: `List(ctx, userID string, currentToken string) ([]DeviceView, error)` where `DeviceView` exposes `id`, `name`, `userAgent`, `ip`, `trustedAt`, `expiresAt`, `lastSeenAt`, `current`, and never the hash.
- Produces: `Revoke(ctx, actorID, userID, deviceID string) error` and `RevokeAll(ctx, actorID, userID string) error`.
- Produces: `Cookie(token string, cfg DeviceTrustConfig) *http.Cookie` and `TokenFromRequest(r *http.Request, cfg DeviceTrustConfig) string`.
- Consumes: Task 2 repositories.

**Steps:**

- [ ] Write `internal/auth/device_service_test.go` first:
  - `Issue` returns a 43-character base64url token; the stored row holds only the 32-byte SHA-256 hash and no substring of the token.
  - `Validate` succeeds for a fresh token, fails for an expired token, fails for a revoked token, and fails for an unknown token without hitting the database more than once.
  - `Issue` beyond `MaxPerUser` revokes the row with the oldest `last_seen_at` and keeps the count at the cap.
  - `List` marks exactly one entry `current` when `currentToken` matches, and returns entries ordered by `lastSeenAt` descending.
  - `Revoke` by a non-owner returns `ErrForbidden`; an admin actor succeeds for any user.
  - `Cookie` sets `HttpOnly`, `Path=/`, `SameSite` per config, and `Secure` when configured; `CookieSameSite=none` without `Secure` returns a configuration error rather than building an insecure cookie.
  - `Issue` with `Enabled=false` returns a typed `ErrDeviceTrustDisabled` and writes nothing.
  - Audit rows are written for issue and revoke, and their `details` contain no token or hash.
- [ ] Run `go test ./internal/auth -run Device -count=1` and confirm a compile failure. Record the output.
- [ ] Implement `internal/auth/device_service.go`. Cap eviction happens inside `storage.IdentityTransaction` so a concurrent issue cannot exceed the cap.
- [ ] Run `go test ./internal/auth -count=1` and confirm green.

**Verification:**

```bash
go test ./internal/auth -run Device -count=1
go test ./internal/auth -count=1
go vet ./internal/auth
```

Expected: all pass.

**Rollback:** Delete the two files and revert the `service.go` import if any.

---

### Task 6: Login service, challenge store, and throttle

**Files:**

- Create: `internal/auth/login_service.go`
- Create: `internal/auth/login_service_test.go`
- Create: `internal/auth/challenge.go`
- Create: `internal/auth/challenge_test.go`
- Modify: `internal/auth/service.go`
- Modify: `internal/storage/models.go`

**Interfaces:**

- Produces: `ChallengeStore` wrapping `storage.AuthChallengeRepository` and `SecretProvider` with `Create(ctx, kind, userID string, payload any, ttl time.Duration, maxAttempts int) (id string, err error)`, `Load(ctx, id, kind string) (Challenge, error)`, `Consume(ctx, id string) (bool, error)`, `Fail(ctx, id string) (attempts int, exhausted bool, err error)`. `Challenge.Payload` is a `map[string]string` decoded from decrypted JSON.
- Produces: `LoginService` constructed by `NewLoginService(deps LoginDependencies, cfg LoginConfig, audits AuditWriter) *LoginService` where `LoginDependencies` bundles users, tokens, challenges, attempts, MFA, and devices.
- Produces: `LoginRequest struct { Username, Password, ClientIP, UserAgent string; TrustDevice bool; DeviceToken string }`.
- Produces: `LoginOutcome struct { State string; Token string; User storage.User; ChallengeID string; Methods []string; ExpiresAt time.Time; DeviceTrusted bool; DeviceToken string; RetryAfter time.Duration }` with `State` one of `authenticated`, `mfa_required`, `throttled`, `invalid`.
- Produces: `Login(ctx, LoginRequest) (LoginOutcome, error)`.
- Produces: `VerifyMFA(ctx, challengeID, code, clientIP, userAgent string, trustDevice bool) (LoginOutcome, error)`.
- Produces: `IssueForAuthenticatedUser(ctx, user storage.User, method, clientIP, userAgent string, trustDevice bool, deviceToken string) (LoginOutcome, error)` used by the OIDC callback.
- Produces: sentinel errors `ErrLoginThrottled`, `ErrChallengeInvalid`, `ErrChallengeExhausted`.
- Consumes: Tasks 2-5.

**Steps:**

- [ ] Write `internal/auth/challenge_test.go` first:
  - `Create` then `Load` round-trips the payload through encryption; the stored ciphertext does not contain any plaintext payload value.
  - `Load` with the wrong `kind` returns `ErrChallengeInvalid`.
  - `Load` after expiry returns `ErrChallengeInvalid` and does not leak the expiry reason.
  - `Consume` returns true once and false afterwards.
  - `Fail` increments and reports `exhausted=true` when `attempts` reaches `max_attempts`, and marks the challenge consumed at that point.
  - `Create` with an unavailable `SecretProvider` returns `ErrSecretStorageUnavailable` and writes nothing.
- [ ] Write `internal/auth/login_service_test.go` first, covering each branch of spec section 5.1 and 5.2:
  - correct password, MFA disabled → `State=authenticated`, a non-empty `Token`, and an `api_tokens` row whose hash validates through the existing `AuthService.ValidateToken`.
  - unknown username, disabled user, deleted user, and wrong password all → `State=invalid` with the same error and the same audit action.
  - throttle: after `MaxAttempts` failures in the window, `State=throttled` with a positive `RetryAfter`; a success resets the bucket.
  - `users.mfa_required=1` with `mfa_mode=disabled` → `State=mfa_required` and no token issued.
  - `mfa_mode=required` for a user with no enrollment → `State=mfa_required` with `Methods=["totp"]` and the challenge present; verifying then returns `ErrMFANotEnrolled` surfaced as `State=invalid` so the UI can point at enrollment.
  - `mfa_mode=optional` with `status=enabled` → `State=mfa_required` with `Methods=["totp","recovery"]`.
  - trusted device cookie present and `AllowBypass=true` → `State=authenticated`, `DeviceTrusted=true`.
  - trusted device cookie present and `AllowBypass=false` → `State=mfa_required`.
  - `TrustDevice=true` on a successful MFA verification issues a device token, sets `DeviceTrusted=true`, and returns the token exactly once.
  - `VerifyMFA` with a wrong code increments attempts and returns `ErrMFACodeInvalid`; after `MaxAttempts` it returns `ErrChallengeExhausted` and the challenge is unusable.
  - `VerifyMFA` with a consumed challenge returns `ErrChallengeInvalid`.
  - `session_token_ttl > 0` produces an `api_tokens` row with `expires_at` set; `0` leaves it nil, matching today's behaviour.
  - No audit `details` map contains a key whose value equals the password, the code, the token, or the device token; assert this by scanning the recorded details for those substrings.
- [ ] Run `go test ./internal/auth -run 'Challenge|Login' -count=1` and confirm a compile failure. Record the output.
- [ ] Implement `internal/auth/challenge.go` and `internal/auth/login_service.go`. `Login` must not hold the plaintext password beyond the verification call, and must build the challenge payload without the password.
- [ ] Keep `AuthService.Login` intact as the compatibility path and have `LoginService` be the new orchestrator; `AuthService` gains no new responsibility.
- [ ] Run `go test ./internal/auth -count=1` and confirm green.

**Verification:**

```bash
go test ./internal/auth -count=1
go test -race ./internal/auth
go vet ./internal/auth
```

Expected: all pass, including the audit-leak assertions.

**Rollback:** Delete the four files and revert `Login`. `POST /auth/login` still uses `AuthService.Login` until Task 9 flips the handler.

---

### Task 7: OIDC relying party

**Files:**

- Create: `internal/auth/oidc/provider.go`
- Create: `internal/auth/oidc/discovery.go`
- Create: `internal/auth/oidc/jwks.go`
- Create: `internal/auth/oidc/token.go`
- Create: `internal/auth/oidc/idtoken.go`
- Create: `internal/auth/oidc/claims.go`
- Create: `internal/auth/oidc/oidc_test.go`
- Create: `internal/auth/oidc/discovery_test.go`
- Create: `internal/auth/oidc/jwks_test.go`
- Create: `internal/auth/oidc/idtoken_test.go`
- Create: `internal/auth/oidc/claims_test.go`

**Interfaces:**

- Produces: `type Provider struct` holding the non-secret provider configuration plus `Secret func(ctx) (string, error)` so the secret is fetched lazily and never copied into logs.
- Produces: `type RelyingParty struct` constructed by `NewRelyingParty(httpClient *http.Client, cfg Config, secrets SecretProvider) *RelyingParty`.
- Produces: `Config struct { HTTPTimeout time.Duration; StateTTL time.Duration; LoginTicketTTL time.Duration; JWKSCacheTTL time.Duration; MaxBodyBytes int64; ClockSkew time.Duration }`.
- Produces: `Discover(ctx, p Provider) (Endpoints, error)` with `Endpoints{Authorization, Token, Userinfo, JWKS, Issuer}`; explicit provider overrides win over discovery.
- Produces: `AuthRequest struct { State, Nonce, CodeChallenge, RedirectURI, Scope []string }` and `BuildAuthURL(ctx, p Provider, req AuthRequest) (string, error)`.
- Produces: `NewPKCE() (verifier, challenge string, err error)` using S256.
- Produces: `Exchange(ctx, p Provider, code, verifier, redirectURI string) (TokenResponse, error)` with `TokenResponse{IDToken, AccessToken, TokenType, ExpiresIn}`.
- Produces: `VerifyIDToken(ctx, p Provider, rawIDToken, expectedNonce, accessToken string) (Claims, error)`.
- Produces: `Claims struct { Subject, Username, Email string; EmailVerified bool; Raw map[string]any }` and `ResolveIdentity(p Provider, c Claims) (username, role string, err error)`.
- Produces: sentinel errors `ErrDiscoveryFailed`, `ErrJWKSFetchFailed`, `ErrTokenExchangeFailed`, `ErrIDTokenInvalid`, `ErrUnsupportedAlgorithm`, `ErrNoMatchingKey`, `ErrClaimMissing`, `ErrRoleMappingInvalid`.
- Consumes: nothing outside the standard library.

**Steps:**

- [ ] Write `internal/auth/oidc/idtoken_test.go` first using a locally generated RSA and ECDSA key served by an `httptest` JWKS endpoint:
  - a valid RS256 token with correct `iss`, `aud`, `exp`, `nbf`, `nonce`, and `at_hash` verifies and returns claims.
  - ES256, ES384, ES512, PS256, and EdDSA verify when listed in `id_token_algs`.
  - `alg=none` returns `ErrUnsupportedAlgorithm` even when `none` is in the allowlist, because the allowlist parser rejects it at configuration time and verification rejects it again.
  - `HS256` signed with the client secret returns `ErrUnsupportedAlgorithm`.
  - an `alg` not in the provider allowlist returns `ErrUnsupportedAlgorithm`.
  - wrong `iss`, wrong `aud`, missing `aud`, expired `exp`, future `nbf` beyond the 60s skew, wrong `nonce`, and wrong `at_hash` each return `ErrIDTokenInvalid`.
  - a signature from a different key with a matching `kid` returns `ErrIDTokenInvalid`.
  - an unknown `kid` triggers exactly one JWKS refresh and then fails with `ErrNoMatchingKey` if still absent.
  - a malformed header, malformed payload, wrong segment count, or non-base64url segment returns `ErrIDTokenInvalid` without panicking.
- [ ] Write `internal/auth/oidc/discovery_test.go` and `internal/auth/oidc/jwks_test.go` first:
  - discovery parses `authorization_endpoint`, `token_endpoint`, `jwks_uri`, `userinfo_endpoint`, `issuer`; a missing `jwks_uri` when no override is configured returns `ErrDiscoveryFailed`.
  - a non-200 status, a body larger than `MaxBodyBytes`, invalid JSON, and a client timeout each return `ErrDiscoveryFailed`.
  - an `http` (non-TLS) issuer URL returns `ErrDiscoveryFailed`.
  - JWKS caching serves the second request from cache within `JWKSCacheTTL`, and `httptest` request counting asserts exactly one upstream fetch.
  - JWKS parsing accepts `RSA` (`n`, `e`), `EC` (`crv`, `x`, `y`), and `OKP` (`x`) keys, and skips unknown `kty` without error.
- [ ] Write `internal/auth/oidc/claims_test.go` first:
  - `username_claim=preferred_username` wins; an empty value falls back to `email`; both empty falls back to `<provider>-<sub prefix>` and is a valid username under the existing 3-64 character rule.
  - role mappings are evaluated in order and the first match wins; a mapping whose claim is a string array matches on membership.
  - an unknown claim name in a mapping is skipped, not fatal.
  - a mapping with `role` other than `admin`/`user` returns `ErrRoleMappingInvalid`.
  - no mapping matched yields `default_role`.
- [ ] Write `internal/auth/oidc/oidc_test.go` first for the end-to-end happy path and the token exchange failures:
  - `BuildAuthURL` includes `response_type=code`, `scope` with `openid` first, `state`, `nonce`, `code_challenge`, `code_challenge_method=S256`, and `redirect_uri`; `code_challenge` is the base64url SHA-256 of the verifier.
  - `Exchange` posts `application/x-www-form-urlencoded` with `grant_type=authorization_code`, and sends basic auth only when a secret exists.
  - a `4xx`/`5xx` token response returns `ErrTokenExchangeFailed` and the error string does not contain the client secret or the code.
  - an OAuth `error` response body is parsed and surfaced as `ErrTokenExchangeFailed`.
- [ ] Run `go test ./internal/auth/oidc -count=1` and confirm a compile failure. Record the output.
- [ ] Implement the six source files. Every outbound HTTP call uses the injected `*http.Client` with `Timeout` set, and every response body is read through `io.LimitReader` bounded by `MaxBodyBytes`.
- [ ] Implement JWK verification with `crypto/rsa.VerifyPKCS1v15` / `rsa.VerifyPSS`, `ecdsa.VerifyASN1`-compatible raw `r||s` handling for JWS, and `ed25519.Verify`. Base64url decoding uses `encoding/base64.RawURLEncoding`.
- [ ] Add a package doc comment stating the accepted algorithm set and the unconditional rejection of `none` and `HS*`.
- [ ] Run `go test ./internal/auth/oidc -count=1` and confirm green.

**Verification:**

```bash
go test ./internal/auth/oidc -count=1
go test -race ./internal/auth/oidc
go vet ./internal/auth/oidc
```

Expected: all pass. No new entry appears in `go.mod`.

**Rollback:** Delete the `internal/auth/oidc` directory. Nothing else imports it until Task 8.

---

### Task 8: OIDC provider service and SSO admin API

**Files:**

- Create: `internal/auth/oidc_provider_service.go`
- Create: `internal/auth/oidc_provider_service_test.go`
- Create: `internal/auth/auth_policy_service.go`
- Create: `internal/auth/auth_policy_service_test.go`
- Create: `internal/server/sso_api.go`
- Create: `internal/server/sso_api_test.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/credential_service.go`

**Interfaces:**

- Produces: `OIDCProviderService` with `Create(ctx, actorID string, in ProviderInput, idempotencyKey string)`, `Update`, `Delete`, `Get`, `List(ctx, cursor string, limit int)`, `ListForLogin(ctx)`, `Test(ctx, actorID, id string) (TestReport, error)`, `ResolveRelyingParty(ctx, name string) (oidc.Provider, error)`.
- Produces: `ProviderView` with `id`, `name`, `displayName`, `issuer`, `clientId`, `scopes[]`, `redirectUri`, `idTokenAlgs[]`, `usernameClaim`, `roleMappings[]`, `defaultRole`, `autoCreateUsers`, `publicListed`, `enabled`, `hasSecret`, `createdAt`, `updatedAt`. It never carries a secret field.
- Produces: `TestReport struct { DiscoveryOK, JWKSOK bool; Algorithms []string; Endpoints map[string]string; Error string }`.
- Produces: `AuthPolicyService` with `Get(ctx)`, `Update(ctx, actorID string, in AuthPolicyInput, idempotencyKey string)`, seeded from `config.SecurityConfig.Auth` on first read.
- Produces: HTTP routes under `/api/v1/sso/providers*` and `/api/v1/auth/policy`.
- Consumes: Tasks 2, 3, 7.

**Steps:**

- [ ] Write `internal/auth/oidc_provider_service_test.go` first:
  - `Create` validates `name` as a URL-safe slug (`^[a-z0-9][a-z0-9-]{1,62}$`), `issuer` as an absolute `https` URL, `redirectUri` as an absolute URL on an allowed base, `scopes` containing `openid`, `idTokenAlgs` drawn from the accepted set, `defaultRole` in `{admin,user}`, and each `roleMappings` entry through `oidc.ResolveIdentity` validation. Each invalid case returns a distinct validation error and persists nothing.
  - `Create` with a secret stores ciphertext and reports `hasSecret=true`; `ProviderView` never contains the secret or the ciphertext.
  - `Create` without `TUNNELMESH_TOKEN_ENCRYPTION_KEY` returns `ErrSecretStorageUnavailable`.
  - `Update` with an empty secret keeps the existing ciphertext; with a new secret it re-encrypts and updates `client_secret_key_id`.
  - `Update` changing `name` is rejected, because the name is part of the public callback URL.
  - `Delete` refuses to delete the last enabled provider when any user has `auth_source=oidc` and no local password, returning `ErrLastAdminProtected`-style `ErrProviderInUse`, so SSO-only accounts are not orphaned.
  - `Test` performs discovery and JWKS retrieval against an `httptest` IdP and returns the report; a failing IdP returns `DiscoveryOK=false` with a generic `Error` that contains no secret.
  - `Create`, `Update`, `Delete`, and `Test` each write one audit row; `details` contain the provider id and changed field names only, never values for `client_secret`.
  - `Create` and `Update` replay the same response for a repeated `Idempotency-Key`.
- [ ] Write `internal/auth/auth_policy_service_test.go` first:
  - `Get` on an empty table seeds from config defaults and returns them.
  - `Update` validates `mfa_mode` in `{disabled,optional,required}`, `device_trust_ttl_seconds` in `[3600, 7776000]`, `max_trusted_devices` in `[1,100]`, and `session_token_ttl_seconds >= 0`.
  - `Update` writes an `auth.policy.update` audit row containing the changed keys and their new non-secret values.
  - Setting `mfa_mode=disabled` while at least one user has `mfa_required=1` succeeds and the per-user override still applies; assert this through `ResolvePolicy`.
- [ ] Write `internal/server/sso_api_test.go` first:
  - a non-admin principal receives `403` on every `/sso/providers` route,
  - unauthenticated requests receive `401`,
  - `GET /sso/providers` returns the envelope with `items`, `nextCursor`, `hasMore` and honours `limit`,
  - `POST` without `Idempotency-Key` returns `400 {"error":"idempotency_key_required"}`,
  - a replayed key returns the original response body and status,
  - `PATCH` with an unknown id returns `404`,
  - `GET /auth/policy` is admin-only and `PUT /auth/policy` requires `Idempotency-Key`,
  - error responses use the documented stable `data.error` strings.
- [ ] Run `go test ./internal/auth ./internal/server -run 'OIDCProvider|AuthPolicy|SSO' -count=1` and confirm compile failures. Record the output.
- [ ] Implement `internal/auth/oidc_provider_service.go` and `internal/auth/auth_policy_service.go`.
- [ ] Refactor the duplicated secret-store bootstrap in `internal/server/credential_service.go` and `internal/server/token_service.go` into one exported helper `server.NewSecretProvider() (auth.SecretProvider, error)` and use it from the new services. This removes the third copy of the same environment lookup and keeps behaviour identical.
- [ ] Implement `internal/server/sso_api.go` handlers and register `case "sso":` plus the `auth/policy` sub-route in `API.ServeHTTP`. Handlers decode, authorize with `isAdmin`, delegate, and map sentinel errors to status codes through a new `writeAuthError` helper.
- [ ] Run `go test ./internal/auth ./internal/server -count=1` and confirm green.

**Verification:**

```bash
go test ./internal/auth ./internal/server -count=1
go test -race ./internal/auth ./internal/server
go vet ./internal/auth ./internal/server
```

Expected: all pass.

**Rollback:** Delete the new files, revert the `ServeHTTP` case additions and the `NewSecretProvider` refactor. Providers already stored stay in the database but become unreachable.

---

### Task 9: Authentication HTTP endpoints

**Files:**

- Create: `internal/server/auth_api.go`
- Create: `internal/server/auth_oidc_api.go`
- Create: `internal/server/auth_api_test.go`
- Create: `internal/server/auth_oidc_api_test.go`
- Modify: `internal/server/api.go`
- Modify: `internal/server/middleware.go`
- Modify: `internal/auth/service.go`

**Interfaces:**

- Produces: handlers for `POST /auth/login`, `POST /auth/mfa/verify`, `GET /auth/mfa`, `POST /auth/mfa/enroll`, `POST /auth/mfa/enable`, `DELETE /auth/mfa`, `GET /auth/devices`, `DELETE /auth/devices/{id}`, `GET /auth/identities`, `DELETE /auth/identities/{id}`.
- Produces: handlers for `GET /auth/oidc/providers`, `GET /auth/oidc/{name}/authorize`, `GET /auth/oidc/{name}/callback`, `POST /auth/oidc/exchange`.
- Produces: `clientIP(r *http.Request) string` in `internal/server/middleware.go` honouring `X-Forwarded-For` only when the direct peer is a configured trusted proxy; otherwise it uses `RemoteAddr`. The trusted-proxy list is a new `server.trusted_proxies` config field.
- Produces: `writeAuthError(w, err)` mapping every `internal/auth` sentinel to a status code and a stable `data.error` string.
- Consumes: Tasks 4, 5, 6, 7, 8.

**Steps:**

- [ ] Write `internal/server/auth_api_test.go` first:
  - `POST /auth/login` with MFA disabled returns the exact legacy shape `{token,user}` plus `deviceTrusted`, so an existing client keeps working.
  - with `mfa_mode=required` it returns `200` and `{mfaRequired:true, challengeId, methods, expiresAt}` and no token.
  - a throttled bucket returns `429` with a `Retry-After` header and `data.error="login_throttled"`.
  - invalid credentials return `401 {"error":"invalid_credentials"}` identically for unknown user, disabled user, and bad password.
  - `POST /auth/mfa/verify` with a valid code returns `{token,user,deviceTrusted}` and sets the `tm_device` cookie when `trustDevice` is true.
  - `POST /auth/mfa/verify` with an unknown challenge returns `401 {"error":"mfa_challenge_invalid"}`; exhausted attempts return `401 {"error":"mfa_attempts_exceeded"}`.
  - `GET /auth/mfa` requires authentication and reports `{status,remainingRecoveryCodes,policy}`.
  - `POST /auth/mfa/enroll` returns `{secret,otpauthUrl,recoveryCodes}` and the codes appear exactly once across two calls with different sessions.
  - `DELETE /auth/mfa` under `mfa_mode=required` returns `409 {"error":"mfa_required_by_policy"}`.
  - `GET /auth/devices` marks the current cookie device and never returns a hash or token.
  - `DELETE /auth/devices/{id}` for another user's device returns `403`.
  - `GET/DELETE /auth/identities` list and unlink the caller's external identities; unlinking the only identity of an `auth_source=oidc` user with no password returns `409 {"error":"identity_required_for_login"}`.
  - every response body is the `{code,msg,data}` envelope.
- [ ] Write `internal/server/auth_oidc_api_test.go` first with an `httptest` IdP:
  - `GET /auth/oidc/providers` unauthenticated returns only `id`, `name`, `displayName` for providers with `enabled=1 AND public_listed=1`, and returns `404` when `oidc.public_providers=false`.
  - `GET /auth/oidc/{name}/authorize` returns `302` to the IdP with `state`, `nonce`, `code_challenge`, `code_challenge_method=S256`, `redirect_uri`, and `Cache-Control: no-store`; an unknown or disabled provider returns `404`.
  - `GET /auth/oidc/{name}/callback` with a valid code and state issues a `login_ticket` and returns `302` to the relative path `/login?ticket=…`; the ticket value never appears in the audit row.
  - the same callback with MFA required returns `302` to `/login?mfa=<challengeId>`.
  - a callback with an unknown, expired, or already consumed state returns `400 {"error":"oidc_state_invalid"}` and no redirect.
  - a callback where the IdP returns `error=access_denied` returns `400 {"error":"oidc_provider_error"}` and the `error_description` is not echoed.
  - a callback for a user the provider will not provision returns `403 {"error":"oidc_user_not_provisioned"}`.
  - `POST /auth/oidc/exchange` with a valid single-use ticket returns `{token,user}`; a second exchange returns `401 {"error":"login_ticket_invalid"}`.
  - the redirect `Location` header is always exactly `/login?...` and never an absolute external URL, asserted for a provider whose `redirect_uri` points elsewhere.
- [ ] Run `go test ./internal/server -run 'AuthAPI|AuthOIDC' -count=1` and confirm compile failures. Record the output.
- [ ] Implement `internal/server/auth_api.go`. Replace the `path == "/api/v1/auth/login"` special case in `API.ServeHTTP` with a dispatch that keeps `/auth/login` unauthenticated and routes the remaining `auth/*` sub-paths through the new handlers. `GET /auth/me` and `PUT /auth/password` keep their current behaviour and tests.
- [ ] Implement `internal/server/auth_oidc_api.go` including the provisioning transaction: identity lookup, optional link to an existing local user, optional JIT creation, role mapping, last-admin protection, then `LoginService.IssueForAuthenticatedUser`.
- [ ] Add `server.trusted_proxies []string` to `internal/config` with validation that each entry parses as an IP or CIDR, and implement `clientIP`.
- [ ] Add `storage.UserRepository` support for creating a user with `password_hash='*'` and `auth_source='oidc'` without weakening the existing `CreateUser` validation; put that in `internal/auth/login_service.go` as `ProvisionExternalUser`.
- [ ] Run `go test ./internal/server ./internal/auth ./internal/config -count=1` and confirm green.

**Verification:**

```bash
go test ./internal/server ./internal/auth ./internal/config -count=1
go test -race ./internal/server
go vet ./...
```

Expected: all pass, including the legacy login-shape assertion.

**Rollback:** Revert the `ServeHTTP` dispatch change to the original login special case and delete the two handler files. Console login returns to password-only.

---

### Task 10: Runtime wiring, sweeper, and metrics

**Files:**

- Modify: `internal/server/runtime.go`
- Create: `internal/server/auth_maintenance_sweeper.go`
- Create: `internal/server/auth_maintenance_sweeper_test.go`
- Modify: `internal/observability/metrics.go`
- Modify: `internal/observability/metrics_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/tunnelmesh-server/main.go`

**Interfaces:**

- Produces: `NewAuthMaintenanceSweeper(db *storage.DB, interval time.Duration, retention time.Duration) *AuthMaintenanceSweeper` with `Start(ctx)`, `Stop()`, and `SweepOnce(ctx) (challenges int64, devices int64, err error)`.
- Produces: `Metrics` counters and gauges listed in spec section 8, exposed through new methods `AuthLogin(method, result string)`, `AuthMFAVerify(method, result string)`, `AuthOIDCStep(step, result string)`, `SetAuthTrustedDevices(float64)`, `SetAuthPendingChallenges(float64)`, `SetAuthBlockedBuckets(float64)`.
- Produces: `config.SecurityConfig.Auth AuthConfig` with the sub-blocks from spec section 4 and `config.ServerConfig.TrustedProxies []string`.
- Consumes: Tasks 1-9.

**Steps:**

- [ ] Write `internal/server/auth_maintenance_sweeper_test.go` first: expired challenges older than the cutoff are deleted and live ones survive; devices past `expires_at` or revoked more than `retention` ago are deleted and active ones survive; `SweepOnce` is safe to call concurrently from two goroutines; `Start` stops promptly when the context is cancelled.
- [ ] Write `internal/config/config_test.go` additions first: defaults match spec section 4; `TUNNELMESH_SECURITY_AUTH_MFA_ISSUER` overrides the file value; `mfa.digits=7` is rejected; `oidc.login_ticket_ttl=1h` is rejected; `device_trust.cookie_same_site=none` without `cookie_secure` is rejected; `trusted_proxies` with a malformed CIDR is rejected.
- [ ] Write `internal/observability/metrics_test.go` additions first asserting the six new series are registered and labelled as documented.
- [ ] Run `go test ./internal/server ./internal/config ./internal/observability -run 'Sweeper|Auth' -count=1` and confirm failures. Record the output.
- [ ] Implement the sweeper following the existing `client_metadata_sweeper.go` lifecycle pattern. Sweeping is an idempotent `DELETE`, so every node may run it; no distributed lock is needed and none is added.
- [ ] Implement the metrics methods and the config block.
- [ ] Wire `LoginService`, `MFAService`, `DeviceService`, `OIDCProviderService`, `AuthPolicyService`, and the sweeper into `NewServerRuntime` and pass them to `NewAPI` through a new `SetIdentityServices` setter so existing `NewAPI(db, auth)` callers and tests keep compiling.
- [ ] Start and stop the sweeper in `cmd/tunnelmesh-server/main.go` alongside the existing background loops.
- [ ] Run `go test ./... -count=1` and confirm green.

**Verification:**

```bash
go test ./internal/server ./internal/config ./internal/observability -count=1
go test ./... -count=1
go vet ./...
```

Expected: all pass.

**Rollback:** Remove the `SetIdentityServices` call and the sweeper start. Handlers return `503 {"error":"identity_services_unavailable"}` when the services are nil, which is the same fail-closed posture used by credentials today.

---

### Task 11: Console

**Files:**

- Modify: `web/src/api/client.ts`
- Create: `web/src/api/auth.ts`
- Modify: `web/src/stores/auth.ts`
- Modify: `web/src/views/Login.vue`
- Modify: `web/src/views/AccountSecurity.vue`
- Modify: `web/src/views/Users.vue`
- Create: `web/src/views/SSOProviders.vue`
- Modify: `web/src/router.ts`
- Modify: `web/src/layouts/AppShell.vue`
- Modify: `web/src/layouts/breadcrumbs.ts`
- Modify: `web/src/i18n/messages/zh-CN.ts`
- Modify: `web/src/i18n/messages/en-US.ts`
- Create: `web/src/tests/login-mfa.spec.ts`
- Create: `web/src/tests/account-security-mfa.spec.ts`
- Create: `web/src/tests/sso-providers.spec.ts`
- Modify: `web/src/tests/accounts.spec.ts`
- Modify: `web/src/tests/i18n.spec.ts`

**Interfaces:**

- Produces: `LoginResult` discriminated union `{state:'authenticated', token, user, deviceTrusted} | {state:'mfa_required', challengeId, methods, expiresAt} | {state:'throttled', retryAfter}`.
- Produces: `useAuthStore` actions `login`, `verifyMfa`, `exchangeTicket`, `load`, `logout`; `login` resolves to `LoginResult` instead of throwing on MFA.
- Produces: `web/src/api/auth.ts` with `getMFAStatus`, `enrollMFA`, `enableMFA`, `disableMFA`, `listDevices`, `revokeDevice`, `listIdentities`, `unlinkIdentity`, `listOIDCProviders`, `exchangeOIDCTicket`, `getSSOProviders`, `createSSOProvider`, `updateSSOProvider`, `deleteSSOProvider`, `testSSOProvider`, `getAuthPolicy`, `updateAuthPolicy`, `resetUserMFA`, `listUserDevices`, `revokeUserDevice`.
- Produces: route `/sso-providers` guarded by `meta:{auth:true, admin:true}`.
- Consumes: Task 9 endpoints.

**Steps:**

- [ ] Write the three new spec files first, asserting:
  - `Login.vue` renders SSO buttons from `listOIDCProviders`, and a second step with a 6-digit input and a "trust this device" checkbox after `mfa_required`.
  - on mount with `?ticket=` in the query, `Login.vue` calls `exchangeOIDCTicket` once, stores the token, replaces the URL with `router.replace('/')`, and never writes the ticket to `localStorage`.
  - on mount with `?mfa=` it renders the MFA step pre-bound to that challenge id.
  - `AccountSecurity.vue` shows enrollment with the `otpauth://` URL, a copy button, the recovery codes exactly once behind an acknowledgement checkbox, and a trusted-device list with revoke actions.
  - `SSOProviders.vue` lists providers with `hasSecret` instead of a secret value, edits with an empty-secret placeholder meaning "keep", and exposes a Test action that renders `TestReport`.
  - `Users.vue` exposes an `mfaRequired` switch and a "Reset MFA" action requiring confirmation.
  - a throttled login renders `retryAfter` and disables the submit button until it elapses.
- [ ] Run `cd web && npm test -- --run` and confirm the three new specs fail. Record the output.
- [ ] Add the Chinese keys to `web/src/i18n/messages/zh-CN.ts` first, then mirror them exactly in `en-US.ts`. Extend `web/src/tests/i18n.spec.ts` with an assertion that the two message trees have identical key paths so future drift fails the build.
- [ ] Implement `web/src/api/auth.ts`, the store changes, and the four views. Keep Element Plus components and the existing compact single-line template style.
- [ ] Add the `/sso-providers` route, the navigation entry, and the breadcrumb label.
- [ ] Run `cd web && npm test -- --run && npm run build` and confirm green, then run `./scripts/verify-web-embed.sh` after copying the build output into the embed directory used by `internal/server/web.go`.

**Verification:**

```bash
cd web && npm test -- --run && npm run build
./scripts/verify-web-embed.sh
```

Expected: all frontend tests pass, the Vite build succeeds, and the embed verification reports no missing asset.

**Rollback:** Revert the console commit. The API remains usable through `curl`, and the legacy login shape is unchanged for existing clients.

---

### Task 12: Documentation and OpenAPI

**Files:**

- Modify: `docs/api/openapi.yaml`
- Modify: `docs/operations/configuration.md`
- Modify: `docs/operations/config-examples.md`
- Modify: `docs/operations/schema-upgrades.md`
- Modify: `docs/operations/observability.md`
- Modify: `docs/user-guide/server-admin.md`
- Create: `docs/user-guide/sso-and-mfa.md`
- Modify: `docs/en/operations/security.md`
- Modify: `docs/en/user-guide/server-admin.md`
- Create: `docs/en/user-guide/sso-and-mfa.md`
- Modify: `docs/README.md`
- Modify: `docs/index.md`
- Modify: `docs/community/roadmap.md`
- Modify: `README.md`
- Modify: `README.zh-CN.md`
- Modify: `deploy/README.md`
- Modify: `docs/superpowers/plans/README.md` (regenerated)
- Modify: `docs/superpowers/specs/README.md` (regenerated)
- Create: `docs/pull-requests/2026-09-18-sso-mfa-device-trust.md`

**Interfaces:**

- Produces: OpenAPI paths and schemas for every endpoint added in Tasks 8 and 9.
- Produces: a user-facing SSO/MFA guide with an IdP configuration walkthrough for a generic OIDC provider, listing the exact redirect URI format
  `https://<server>/api/v1/auth/oidc/<provider>/callback`.
- Consumes: the final implemented behaviour.

**Steps:**

- [ ] Add every new path, request body, response schema, and `data.error` enumeration value to `docs/api/openapi.yaml`, keeping the existing component style and the `{code,msg,data}` envelope schema.
- [ ] Document the `security.auth` block, `server.trusted_proxies`, the `TUNNELMESH_TOKEN_ENCRYPTION_KEY` requirement for SSO and MFA, and the "config seeds defaults, database is authoritative afterwards" rule in `docs/operations/configuration.md`.
- [ ] Add a cluster example with SSO and MFA enabled to `docs/operations/config-examples.md`.
- [ ] Extend `docs/operations/schema-upgrades.md` with the v13 → v14 upgrade and rollback procedure, expected lock impact (additive `ALTER TABLE` plus new tables), the pre-upgrade backup requirement, and the feature-level rollback that needs no deploy.
- [ ] Add the six new metric families to `docs/operations/observability.md` with suggested alert thresholds.
- [ ] Write `docs/user-guide/sso-and-mfa.md` and its English mirror: enrolling MFA, storing recovery codes, trusting and revoking devices, administrator provider setup, role mapping, troubleshooting table keyed by the stable `data.error` values, and the lockout recovery path through the existing administrator bootstrap flow.
- [ ] Link the new guide from `docs/user-guide/server-admin.md`, `docs/README.md`, `docs/index.md`, `docs/en/user-guide/server-admin.md`, and both root READMEs.
- [ ] Update `docs/community/roadmap.md` to mark SSO/OIDC and MFA/device trust as shipped and the remaining seven capabilities as planned, without inventing dates.
- [ ] Update the capability matrix in `README.md` and `README.zh-CN.md` identically.
- [ ] Create `docs/pull-requests/2026-09-18-sso-mfa-device-trust.md` with title, target branch `main`, summary, user impact, API/schema/config impact, security and authorization impact, test evidence, release steps, rollback steps, reviewer focus areas, and integration status. Include no secrets and no unredacted logs.
- [ ] Run `python3 scripts/gen_doc_index.py` and confirm the plans, specs, and PR indexes pick up the three new records with correct cross-links.
- [ ] Confirm no orphan document: every new file is reachable from `docs/README.md` or `docs/index.md`.

**Verification:**

```bash
python3 scripts/gen_doc_index.py
git diff --check
grep -rn "TBD\|TODO\|待补充" docs/superpowers/plans/2026-09-18-sso-mfa-device-trust-implementation.md docs/superpowers/specs/2026-09-18-sso-mfa-device-trust-design.md
```

Expected: the index regenerates with no diff churn on unrelated files; `git diff --check` prints nothing; the grep finds no placeholder.

**Rollback:** Revert the documentation commit. Docs have no runtime effect.

---

### Task 13: Full validation

**Files:**

- Modify: `docs/pull-requests/2026-09-18-sso-mfa-device-trust.md`

**Steps:**

- [ ] Run the complete Go validation set and record the real output in the PR record:

```bash
go build ./...
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
```

- [ ] Run the complete frontend validation set and record the real output:

```bash
cd web && npm test -- --run && npm run build
cd .. && ./scripts/verify-web-embed.sh
```

- [ ] Run a manual smoke pass against a local SQLite server with `auto-init` enabled: bootstrap admin login, enroll MFA, verify with a code from `otpauth-url`, trust a device, log in again with the cookie, revoke the device, create an OIDC provider pointing at a local test IdP, complete the redirect flow, and confirm each step produced the expected audit row and metric sample. Record the observed audit actions in the PR record with all identifiers redacted.
- [ ] Confirm `go.mod` and `go.sum` are unchanged, proving no new dependency was added.
- [ ] Confirm no secret appears in the diff:

```bash
git diff --cached | grep -Ein "BEGIN [A-Z ]*PRIVATE KEY|client_secret\"?\s*[:=]\s*\"[A-Za-z0-9]|tmrc-[A-Z2-7]{20}" || echo "no secret pattern found"
```

- [ ] Do not commit, push, merge, or open a PR until the user explicitly authorizes it.

**Verification:** the five Go commands, the two frontend commands, and the secret scan all produce the recorded output.

**Rollback:** the whole phase is one feature branch; revert the branch. Databases already at v14 keep working with a v14 binary and require a backup restore to run a v13 binary, as documented in `docs/operations/schema-upgrades.md`.
