# SSO, MFA, and device trust design (Phase A)

Status: proposed, awaiting user confirmation before any implementation.

Roadmap context: `docs/superpowers/specs/2026-09-18-enterprise-capability-roadmap-design.md`.

## 1. Goals

- Let an operator authenticate console users through an external OIDC identity
  provider without giving up local password login.
- Let an operator require a second factor (TOTP) with recovery codes, per user or
  globally.
- Let a user mark a browser as trusted so the second factor is not requested on
  every login, with visibility and revocation.
- Keep every secret encrypted at rest, every administrative action audited, and
  every decision observable.

### Non-goals

- SAML, LDAP, CAS, and SCIM provisioning.
- WebAuthn / passkeys / FIDO2 (the `user_mfa.status` and challenge design leave
  room for a later `webauthn_credentials` table, but nothing is implemented).
- Per-application SSO (TunnelMesh is the relying party only; it does not become an
  identity provider).
- Conditional access rules beyond "MFA required" and "trusted device bypass".
- General rate limiting; only the auth-specific attempt counters are in scope.
  Phase B generalizes them.

## 2. Architecture

```
Login.vue ──POST /auth/login──▶ authLoginHandler ──▶ auth.LoginService
   ▲                                                       │
   │  {token,user} | {mfaRequired,challengeId}             │
   └───────────────────────────────────────────────────────┘
                                                            │
      ┌─────────────────────────────────────────────────────┤
      ▼                                                     ▼
auth.MFAService                                    auth.DeviceService
(TOTP + recovery codes)                            (trusted device tokens)
      │                                                     │
      └──────────────┬──────────────────────────────────────┘
                     ▼
             storage repositories (SQLite / MySQL)

Browser ──GET /auth/oidc/{id}/authorize──▶ auth/oidc.RelyingParty
        ◀─302 to IdP with state + PKCE
IdP ──GET /auth/oidc/{id}/callback──▶ RelyingParty ─▶ identity provisioning
        ◀─302 to /login?ticket=…                     └▶ LoginService issue
```

Layering:

- `internal/auth/oidc` is a self-contained relying party. It knows HTTP, JWKS, and
  claims. It does not know about `net/http` handlers or the database.
- `internal/auth` services (`LoginService`, `MFAService`, `DeviceService`,
  `OIDCProviderService`) orchestrate and write audit rows.
- `internal/server/auth_*_api.go` handlers only decode, authorize, and render.
- `internal/storage/identity_repository.go` holds all SQL.

## 3. Data model (Schema v13 → v14)

All statements are additive and backward compatible, so v14 is a MINOR release.

### 3.1 `users` extensions

| Column | Type | Notes |
| --- | --- | --- |
| `auth_source` | `VARCHAR(32) NOT NULL DEFAULT 'local'` | `local`, `oidc`, `mixed`. `mixed` means a local password exists and at least one external identity is linked |
| `mfa_required` | `INTEGER NOT NULL DEFAULT 0` | Per-user override; wins over the global policy when `1` |

OIDC-only users keep `password_hash = '*'`. `'*'` is not a valid Argon2id PHC
string, so `auth.verifyPassword` returns false for it. This avoids a
cross-database `NULL` migration on SQLite, which cannot alter column nullability
without a table rebuild.

### 3.2 New tables

`auth_settings` — singleton runtime policy, `id = 1`.

| Column | Type |
| --- | --- |
| `mfa_mode` | `VARCHAR(16) NOT NULL` (`disabled`, `optional`, `required`) |
| `device_trust_enabled` | `INTEGER NOT NULL DEFAULT 1` |
| `device_trust_ttl_seconds` | `INTEGER NOT NULL DEFAULT 2592000` (30d) |
| `allow_trusted_device_bypass` | `INTEGER NOT NULL DEFAULT 1` |
| `max_trusted_devices` | `INTEGER NOT NULL DEFAULT 10` |
| `session_token_ttl_seconds` | `INTEGER NOT NULL DEFAULT 0` (0 = no expiry, preserves today's behaviour) |
| `updated_at` | `TEXT NOT NULL` |

`oidc_providers`

| Column | Type | Notes |
| --- | --- | --- |
| `id` | `VARBINARY(255) PK` | |
| `name` | `VARCHAR(191) NOT NULL UNIQUE` | URL-safe slug used in the authorize/callback path |
| `display_name` | `VARCHAR(191) NOT NULL` | Button label on the login page |
| `issuer` | `VARCHAR(512) NOT NULL` | Must be an absolute `https` URL |
| `client_id` | `VARCHAR(255) NOT NULL` | |
| `client_secret_ciphertext` | `TEXT` | AES-GCM, base64 |
| `client_secret_nonce` | `TEXT` | |
| `client_secret_key_id` | `VARCHAR(128)` | |
| `client_secret_version` | `INTEGER` | |
| `scopes` | `TEXT NOT NULL` | Comma separated; `openid` always enforced |
| `redirect_uri` | `VARCHAR(512) NOT NULL` | Must be registered at the IdP |
| `authorization_endpoint` | `VARCHAR(512)` | Optional manual override |
| `token_endpoint` | `VARCHAR(512)` | Optional manual override |
| `userinfo_endpoint` | `VARCHAR(512)` | Optional; unused unless `fetch_userinfo=1` |
| `jwks_uri` | `VARCHAR(512)` | Optional manual override |
| `id_token_algs` | `VARCHAR(255) NOT NULL DEFAULT 'RS256'` | Comma separated allowlist |
| `username_claim` | `VARCHAR(64) NOT NULL DEFAULT 'preferred_username'` | Falls back to `email` when empty in the token |
| `role_mappings` | `TEXT NOT NULL` | JSON array, see 5.4 |
| `default_role` | `VARCHAR(32) NOT NULL DEFAULT 'user'` | |
| `auto_create_users` | `INTEGER NOT NULL DEFAULT 1` | |
| `fetch_userinfo` | `INTEGER NOT NULL DEFAULT 0` | |
| `public_listed` | `INTEGER NOT NULL DEFAULT 1` | Whether the unauthenticated provider list exposes it |
| `enabled` | `INTEGER NOT NULL DEFAULT 1` | |
| `created_at`, `updated_at` | `TEXT NOT NULL` | |

`user_identities`

| Column | Type |
| --- | --- |
| `id` | `VARBINARY(255) PK` |
| `user_id` | `VARBINARY(255) NOT NULL` |
| `provider_id` | `VARBINARY(255) NOT NULL` |
| `subject` | `VARBINARY(255) NOT NULL` |
| `email` | `VARCHAR(255)` |
| `display_name` | `VARCHAR(255)` |
| `created_at` | `TEXT NOT NULL` |
| `last_login_at` | `TEXT` |

Indexes: `UNIQUE(provider_id, subject)`, `idx_user_identities_user(user_id, id)`.

`user_mfa`

| Column | Type | Notes |
| --- | --- | --- |
| `user_id` | `VARBINARY(255) PK` | |
| `secret_ciphertext` | `TEXT NOT NULL` | AES-GCM, base64 |
| `secret_nonce` | `TEXT NOT NULL` | |
| `secret_key_id` | `VARCHAR(128) NOT NULL` | |
| `secret_version` | `INTEGER NOT NULL` | |
| `status` | `VARCHAR(16) NOT NULL` | `pending` (enrolled, not confirmed) or `enabled` |
| `enrolled_at` | `TEXT NOT NULL` | |
| `enabled_at` | `TEXT` | |
| `last_used_at` | `TEXT` | |
| `last_used_step` | `INTEGER NOT NULL DEFAULT -1` | TOTP replay guard |
| `updated_at` | `TEXT NOT NULL` | |

`user_recovery_codes`

| Column | Type | Notes |
| --- | --- | --- |
| `id` | `VARBINARY(255) PK` | |
| `user_id` | `VARBINARY(255) NOT NULL` | |
| `code_hash` | `VARBINARY(64) NOT NULL UNIQUE` | SHA-256 of a 128-bit random code |
| `used_at` | `TEXT` | |
| `created_at` | `TEXT NOT NULL` | |

Recovery codes are 128-bit random (`tmrc-` prefix plus 20 base32 characters), so a
fast hash is appropriate; Argon2id would add 64 MB of work per verification for no
entropy gain. Comparison is constant time.

`user_devices`

| Column | Type | Notes |
| --- | --- | --- |
| `id` | `VARBINARY(255) PK` | |
| `user_id` | `VARBINARY(255) NOT NULL` | |
| `token_hash` | `VARBINARY(64) NOT NULL UNIQUE` | SHA-256 of a 256-bit random token |
| `name` | `VARCHAR(191)` | User editable label |
| `user_agent` | `VARCHAR(512)` | Truncated |
| `ip` | `VARCHAR(64)` | Source address at trust time |
| `trusted_at` | `TEXT NOT NULL` | |
| `expires_at` | `TEXT NOT NULL` | |
| `last_seen_at` | `TEXT` | |
| `revoked_at` | `TEXT` | |

`auth_challenges` — short-lived state shared by all cluster nodes.

| Column | Type | Notes |
| --- | --- | --- |
| `id` | `VARBINARY(255) PK` | Random, unguessable |
| `kind` | `VARCHAR(32) NOT NULL` | `login_mfa`, `oidc_state`, `login_ticket`, `mfa_enroll` |
| `user_id` | `VARBINARY(255)` | Empty for `oidc_state` before provisioning |
| `payload_ciphertext` | `TEXT NOT NULL` | AES-GCM JSON |
| `payload_nonce` | `TEXT NOT NULL` | |
| `payload_key_id` | `VARCHAR(128) NOT NULL` | |
| `payload_version` | `INTEGER NOT NULL` | |
| `attempts` | `INTEGER NOT NULL DEFAULT 0` | |
| `max_attempts` | `INTEGER NOT NULL` | |
| `consumed_at` | `TEXT` | Single use |
| `expires_at` | `TEXT NOT NULL` | |
| `created_at` | `TEXT NOT NULL` | |

Indexes: `idx_auth_challenges_expires(expires_at)`,
`idx_auth_challenges_kind_user(kind, user_id, id)`.

`auth_login_attempts` — cluster-safe brute-force counter.

| Column | Type |
| --- | --- |
| `bucket_key` | `VARBINARY(255) PK` |
| `attempts` | `INTEGER NOT NULL DEFAULT 0` |
| `window_start` | `TEXT NOT NULL` |
| `blocked_until` | `TEXT` |
| `updated_at` | `TEXT NOT NULL` |

`bucket_key` is `sha256(lower(username) + "|" + clientIP)` truncated to hex. The
raw username and IP are not stored.

## 4. Configuration

New `security.auth` block in `internal/config`. Precedence stays
CLI > env > file > default, so `TUNNELMESH_SECURITY_AUTH_MFA_ISSUER` works.

```yaml
security:
  auth:
    session_token_ttl: 0s          # 0 = keep today's non-expiring console token
    login_throttle:
      max_attempts: 10             # per username+IP bucket
      window: 5m
      block: 10m
    mfa:
      issuer: TunnelMesh           # otpauth:// label
      digits: 6                    # 6 or 8
      period: 30s
      skew: 1                      # accepted steps before/after now
      challenge_ttl: 5m
      max_attempts: 5
      recovery_codes: 10
    device_trust:
      enabled: true
      cookie_name: tm_device
      cookie_secure: true
      cookie_same_site: lax
      bypass_mfa: true
    oidc:
      public_providers: true       # unauthenticated GET /auth/oidc/providers
      http_timeout: 10s
      state_ttl: 10m
      login_ticket_ttl: 60s
      jwks_cache_ttl: 1h
      max_discovery_body_bytes: 1048576
```

Config only supplies defaults and process-level knobs. `auth_settings` supplies
runtime policy and is seeded once from these defaults when the row does not exist.
After seeding, the database row is authoritative and config edits do not silently
override an operator decision. This is documented in
`docs/operations/configuration.md`.

Validation additions in `config.Validate`:

- `mfa.digits` is 6 or 8; `mfa.period` between 15s and 120s; `mfa.skew` 0..2.
- `mfa.issuer` is non-empty, ≤ 64 chars, and contains no `:` (it is embedded in an
  `otpauth://` URL label).
- `oidc.http_timeout`, `state_ttl`, `login_ticket_ttl`, `jwks_cache_ttl` > 0.
- `login_ticket_ttl` ≤ 300s.
- `device_trust.cookie_same_site` is `lax`, `strict`, or `none`; `none` requires
  `cookie_secure`.
- `session_token_ttl` ≥ 0.

## 5. Flows

### 5.1 Password login

1. `POST /api/v1/auth/login {username,password,trustDevice?}`.
2. Throttle check on `sha256(username|ip)`; a blocked bucket returns `429` with
   `Retry-After` and `data.error = "login_throttled"`.
3. Verify Argon2id hash. On failure: increment the bucket, write
   `auth.login.failure` audit (no password, no hash), return
   `401 {"error":"invalid_credentials"}`. The same response is returned for
   unknown usernames, disabled accounts, deleted accounts, and bad passwords.
4. Resolve the effective MFA requirement: `users.mfa_required == 1`, or
   `auth_settings.mfa_mode == 'required'`, or (`mfa_mode == 'optional'` and the
   user has `user_mfa.status == 'enabled'`).
5. If MFA is not required: issue the console token, optionally trust the device,
   return `{token,user,deviceTrusted}`. Shape unchanged from today.
6. If MFA is required and a valid trusted device cookie is present and
   `allow_trusted_device_bypass == 1`: issue the token, refresh
   `user_devices.last_seen_at`, return `{token,user,deviceTrusted:true}`.
7. Otherwise create a `login_mfa` challenge (TTL `mfa.challenge_ttl`,
   `max_attempts` from config), clear the password from memory, and return
   `200 {mfaRequired:true, challengeId, methods:["totp"] or ["totp","recovery"], expiresAt}`.

### 5.2 MFA verification

`POST /api/v1/auth/mfa/verify {challengeId, code, trustDevice?}`

- Unknown, consumed, or expired challenge: `401 {"error":"mfa_challenge_invalid"}`.
  The response does not distinguish the three cases.
- Attempt accounting uses a conditional update
  (`UPDATE auth_challenges SET attempts = attempts + 1 WHERE id = ? AND consumed_at IS NULL AND expires_at > ?`)
  so concurrent guesses cannot each get a full budget.
- When `attempts >= max_attempts`, the challenge is consumed and the response is
  `401 {"error":"mfa_attempts_exceeded"}`; the user must log in again.
- A 6- or 8-digit code is matched against TOTP with `skew` steps. The accepted
  step must be strictly greater than `user_mfa.last_used_step` to prevent replay
  inside the skew window; `last_used_step` is updated in the same transaction.
- A non-numeric code with the `tmrc-` prefix is matched against unused
  `user_recovery_codes`. A consumed code marks `used_at`, and when the remaining
  count reaches zero the response includes `recoveryCodesExhausted: true` so the
  console can prompt a reset.
- On success: consume the challenge, issue the console token, optionally trust the
  device, write `auth.login.success` audit with `method=mfa`, and return
  `{token,user,deviceTrusted}`.

### 5.3 OIDC login

1. `GET /api/v1/auth/oidc/providers` (unauthenticated, only when
   `oidc.public_providers` is true) returns
   `[{id,name,displayName}]` for providers with `enabled=1 AND public_listed=1`.
2. `GET /api/v1/auth/oidc/{name}/authorize` builds the authorization URL with
   `response_type=code`, `scope` including `openid`, `state`, `nonce`,
   `code_challenge` (S256), and `redirect_uri`. The `state`, `nonce`, and
   `code_verifier` are stored encrypted in one `oidc_state` challenge row
   (TTL `oidc.state_ttl`, `max_attempts=1`). The response is a `302` with
   `Cache-Control: no-store`.
3. `GET /api/v1/auth/oidc/{name}/callback?code=&state=` loads and consumes the
   challenge. `error`/`error_description` from the IdP maps to
   `400 {"error":"oidc_provider_error"}` without echoing the description into
   audit details.
4. Token exchange is a form-encoded POST with a `http.Client` bounded by
   `oidc.http_timeout` and a response body limit. `client_secret_basic` is used
   when a secret exists, otherwise the provider is treated as public and PKCE is
   the only proof.
5. `id_token` validation: header `alg` must be in the provider allowlist and must
   never be `none` or an `HS*` algorithm; signature verified against JWKS (cached
   `oidc.jwks_cache_ttl`, refreshed once on unknown `kid`); `iss` equals the
   configured issuer; `aud` contains `client_id`; `exp`/`nbf` checked with 60s
   clock skew; `nonce` equals the challenge nonce; `at_hash` checked when present
   for RS/ES/PS/EdDSA. Any failure is
   `401 {"error":"oidc_id_token_invalid"}`.
6. Claim resolution: `sub` is required. Username comes from `username_claim`,
   falling back to `email`, then to `<provider>-<sub[:8]>`. Role comes from the
   first matching entry in `role_mappings`, otherwise `default_role`.
7. Provisioning:
   - Existing `user_identities(provider_id, sub)` → update `last_login_at`,
     refresh `email`/`display_name`.
   - No identity, but a local user with the same username exists and
     `link_existing_users` behaviour applies → link and set
     `users.auth_source='mixed'`. Linking is only performed when the IdP verified
     the email (`email_verified == true`) or the username claim matched exactly.
   - No identity and no local user: create one when `auto_create_users=1` with
     `password_hash='*'`, `auth_source='oidc'`; otherwise return
     `403 {"error":"oidc_user_not_provisioned"}`.
   - A disabled or soft-deleted user always fails with
     `403 {"error":"account_disabled"}`.
8. Session issuance reuses the MFA decision from 5.1 steps 4-7. If MFA is
   required, the callback stores the resolved `user_id` in a new `login_mfa`
   challenge and redirects to `/login?mfa=<challengeId>`. Otherwise it creates a
   `login_ticket` challenge (TTL `oidc.login_ticket_ttl`, single use) and
   redirects to `/login?ticket=<ticket>`.
9. The console calls `POST /api/v1/auth/oidc/exchange {ticket}` and receives
   `{token,user,deviceTrusted}`. The ticket never appears in a response body, in
   audit details, or in a log line.

Redirect target is always the fixed relative path `/login`, so no open redirect is
possible. `redirect_uri` itself is validated as an absolute `https` URL under the
server's own configured base URL at provider save time.

### 5.4 Role mapping

`role_mappings` is a JSON array evaluated in order; the first match wins:

```json
[{"claim":"groups","value":"tunnelmesh-admins","role":"admin"},
 {"claim":"roles","value":"tm-viewer","role":"user"}]
```

`claim` must be a top-level string or string-array claim. `role` must be `admin`
or `user`. Role downgrade is applied on every login when the provider is marked
`authoritative_roles` (default true), so removing a user from the IdP group
removes admin rights in TunnelMesh. The bootstrap admin is never downgraded, and
an account whose role would drop to `user` while it is the last enabled admin is
rejected with `409 {"error":"last_admin_protected"}`.

### 5.5 Device trust

- On successful MFA (or successful password login when MFA is not required and
  `trustDevice` is requested), the server generates a 256-bit token, stores its
  SHA-256 hash, and sets
  `Set-Cookie: tm_device=<token>; Path=/; HttpOnly; Secure; SameSite=Lax; Max-Age=<ttl>`.
- The token is returned in the JSON body only for non-browser API clients,
  guarded by an explicit `trustDevice: true` request field. The console never
  stores it in `localStorage`.
- On the next login the cookie is looked up by hash. Valid means
  `revoked_at IS NULL AND expires_at > now`. `last_seen_at` is refreshed
  asynchronously with a 5-minute write throttle to avoid a write per request.
- Enrollment is capped by `max_trusted_devices`; the oldest `last_seen_at` row is
  revoked when the cap is reached.
- `GET /api/v1/auth/devices` lists the caller's devices with `current: true` on
  the one matching the request cookie. `DELETE /api/v1/auth/devices/{id}` revokes.
  Admins get `GET/DELETE /api/v1/users/{userId}/devices`.
- Revoking all devices for a user is part of `POST /api/v1/users/{userId}/mfa/reset`.

### 5.6 MFA lifecycle

| Endpoint | Auth | Behaviour |
| --- | --- | --- |
| `GET /api/v1/auth/mfa` | self | `{status, enabledAt, lastUsedAt, remainingRecoveryCodes, policy:{mode, required}}` |
| `POST /api/v1/auth/mfa/enroll` | self | Creates or replaces a `pending` enrollment, returns `{secret, otpauthUrl, recoveryCodes[]}`. Recovery codes are returned exactly once |
| `POST /api/v1/auth/mfa/enable` | self | Body `{code}`; verifies one TOTP code, sets `status='enabled'`, writes audit |
| `DELETE /api/v1/auth/mfa` | self | Body `{code}` with a current TOTP or recovery code; deletes secret and codes, revokes all trusted devices, writes audit |
| `POST /api/v1/users/{userId}/mfa/reset` | admin | Idempotency-Key required; clears MFA and devices so the user re-enrolls; writes audit |
| `PATCH /api/v1/users/{userId}` | admin | Adds `mfaRequired` to the existing account update body |

Enrollment requires an authenticated session and, when the user has a local
password, the current password in the enroll body to resist session hijacking.
`pending` enrollments expire after 15 minutes and are replaced on re-enroll; a
`pending` enrollment never satisfies an MFA requirement.

Disabling MFA when `mfa_mode='required'` or `users.mfa_required=1` is rejected
with `409 {"error":"mfa_required_by_policy"}`.

## 6. API summary

Unauthenticated: `POST /auth/login`, `POST /auth/mfa/verify`,
`GET /auth/oidc/providers`, `GET /auth/oidc/{name}/authorize`,
`GET /auth/oidc/{name}/callback`, `POST /auth/oidc/exchange`.

Self-service: `GET/POST/DELETE /auth/mfa*`, `GET/DELETE /auth/devices*`,
`GET/DELETE /auth/identities*`.

Admin: `GET/POST /sso/providers`, `GET/PATCH/DELETE /sso/providers/{id}`,
`POST /sso/providers/{id}/test`, `GET/PUT /auth/policy`,
`GET/DELETE /users/{userId}/devices`, `POST /users/{userId}/mfa/reset`,
`GET/DELETE /users/{userId}/identities*`.

`POST /sso/providers/{id}/test` performs discovery and JWKS retrieval and reports
`{discoveryOk, jwksOk, algorithms[], endpoints{}, error}`. It never returns the
client secret and never performs a token exchange.

All admin mutations require `Idempotency-Key` and write audit rows:
`auth.oidc.provider.create|update|delete|test`, `auth.policy.update`,
`auth.mfa.reset`, `auth.device.revoke`, `auth.identity.unlink`.

`GET /sso/providers` returns `hasSecret: bool` and never the ciphertext or the
plaintext secret. Editing with an empty secret field keeps the existing secret,
matching the credentials UI convention.

## 7. Security analysis

| Threat | Control |
| --- | --- |
| Password brute force | `auth_login_attempts` bucket, Argon2id cost, uniform error, audit |
| TOTP brute force | Per-challenge attempt cap with conditional update, 5-minute challenge TTL |
| TOTP replay | `last_used_step` monotonic guard inside the skew window |
| Recovery code guessing | 128-bit random codes, SHA-256, constant-time compare, single use |
| Session hijack via stolen cookie | Device token is `HttpOnly`, `Secure`, `SameSite=Lax`, revocable, capped, expiring |
| CSRF on login | `SameSite=Lax` plus JSON body plus no state-changing GET |
| OIDC state/CSRF | Encrypted single-use `state` row, `nonce` bound to the same row |
| PKCE downgrade | S256 always; plain never accepted |
| Algorithm confusion | Explicit per-provider `alg` allowlist, `none` and `HS*` rejected unconditionally |
| Token substitution | `aud` must contain `client_id`; `at_hash` verified when present |
| Open redirect | Callback redirects only to the fixed relative path `/login` |
| Secret leakage | Secrets AES-GCM at rest; never returned, logged, audited, or exported |
| Last-admin lockout | Local password login always available; `last_admin_protected`; bootstrap recovery unchanged |
| Unauthenticated IdP enumeration | `public_listed` per provider and `oidc.public_providers` global switch |
| Challenge table growth | Sweeper deletes expired rows every 5 minutes |

## 8. Observability

Metrics (all in `internal/observability/metrics.go`):

- `tunnelmesh_auth_login_total{method="password|oidc",result="success|failure|mfa_required|throttled"}`
- `tunnelmesh_auth_mfa_verify_total{method="totp|recovery",result="success|invalid|expired|attempts_exceeded"}`
- `tunnelmesh_auth_oidc_step_total{step="discovery|jwks|token|id_token|provision",result="ok|error"}`
- `tunnelmesh_auth_trusted_devices` (gauge)
- `tunnelmesh_auth_pending_challenges` (gauge)
- `tunnelmesh_auth_login_blocked_buckets` (gauge)

Structured events use the existing `observability` helpers and carry
`trace_id`, `user_id`, `provider`, and `result` only.

## 9. Testing strategy

- `internal/auth/totp_test.go` uses the RFC 6238 SHA-1 test vectors and asserts
  skew and replay behaviour.
- `internal/auth/mfa_service_test.go` covers enroll/enable/verify/disable/reset,
  recovery exhaustion, missing encryption key, and policy denial.
- `internal/auth/device_service_test.go` covers issue, validate, revoke, cap
  eviction, and expiry.
- `internal/auth/login_service_test.go` covers the seven decision branches in 5.1
  plus throttling and audit content assertions (no secrets in `details`).
- `internal/auth/oidc/*_test.go` uses an `httptest` IdP that serves discovery,
  JWKS (RSA and EC), and a token endpoint; negative cases cover `alg=none`,
  `HS256` with the client secret, wrong `iss`, wrong `aud`, expired token, bad
  nonce, bad `at_hash`, and unknown `kid`.
- `internal/storage/identity_repository_test.go` runs against SQLite and, through
  the existing MySQL contract harness, against MySQL.
- `internal/storage/db_test.go` gains a v13 → v14 upgrade assertion.
- `internal/server/auth_api_test.go` and `internal/server/sso_api_test.go` cover
  routing, authorization, idempotency replay, pagination, and error envelopes.
- `web/src/tests/login-mfa.spec.ts`, `web/src/tests/account-security-mfa.spec.ts`,
  `web/src/tests/sso-providers.spec.ts`, and an updated `web/src/tests/i18n.spec.ts`
  key-parity assertion.

## 10. Rollback

- v14 is additive. Rolling back the binary to v13 leaves the extra tables and
  columns unused; `SchemaVersion` mismatch detection means a downgrade requires
  restoring a backup or setting the deployment's expected version. The upgrade
  document states this explicitly.
- Feature-level rollback without a deploy: set `auth_settings.mfa_mode='disabled'`
  and `enabled=0` on all `oidc_providers`. Local password login then behaves
  exactly as before.
- Console tokens issued before the change remain valid because `api_tokens` is
  untouched.
