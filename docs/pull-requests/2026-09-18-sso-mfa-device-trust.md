# Phase A: enterprise identity foundation (SSO, MFA, device trust)

## Title

Add OIDC single sign-on, TOTP multi-factor authentication, recovery codes, trusted devices, and an authoritative authentication policy

## Target branch

`main`

## Summary

Phase A of the enterprise capability roadmap. It introduces an identity layer next to the existing local-password login: OIDC providers with authorization-code + PKCE, JIT provisioning and claim-to-role mapping; TOTP enrollment with single-use challenges and rate limiting; one-time recovery codes; trusted-device cookies with per-account caps and eviction; an administrator-owned authentication policy that is stored in the database rather than in config; and six Prometheus metric families covering login, MFA, OIDC steps and background retention.

The console gains an SSO provider administration view, a second-factor panel in account security, a trusted-device list, and an MFA reset and identity-unlink action per managed account. The login page renders public SSO buttons and an MFA step without changing the existing password-login contract.

Schema moves from v13 to v14 through `migrations/incremental/v0013_to_v0014/`.

## User impact

- Administrators can require a second factor globally (`mfa_mode` = `optional`, `required`, `disabled`) or per account, and can turn trusted-device bypass on or off without a redeploy.
- Users can enroll an authenticator app, print recovery codes, name and revoke their trusted browsers, and link or unlink an external identity.
- Existing local-password logins keep their exact request and response shape. A deployment that never configures a provider or a second factor behaves as before.
- An operator who enables SSO or MFA without injecting `TUNNELMESH_TOKEN_ENCRYPTION_KEY` gets `503 secret_storage_unavailable` on the affected writes instead of a silent downgrade to plaintext or hash-only storage. Both Compose files and the server systemd unit now carry an injection channel for that key; see `deploy/README.md`.

## API, schema, and configuration impact

**API.** 23 new or changed paths under `/api/v1/auth/*`, `/api/v1/sso/*` and `/api/v1/users/{userId}/*`, all documented in `docs/api/openapi.yaml`. The response envelope stays `{code, msg, data}` and list endpoints stay cursor-paginated. Administrative mutations require `Idempotency-Key`; a replay by the same caller returns the stored response, a key owned by another caller answers `409 idempotency_key_conflict`, and an in-flight duplicate answers `409 idempotency_in_progress`. The `data.error` vocabulary is a closed enum of 49 stable snake_case codes.

Public OIDC routes are `GET /api/v1/auth/oidc/providers` (login-page buttons), `GET /api/v1/auth/oidc/{providerName}/authorize` and `.../callback`, and `POST /api/v1/auth/oidc/exchange`. The callback answers a 302 to a server-constant login path with either `?ticket=` or `?mfa=`; the redirect target is never derived from request data or from the provider row.

**Schema.** v13 to v14, additive only. `users` gains `auth_source` and `mfa_required`. Eight new tables: `auth_settings`, `oidc_providers`, `user_identities`, `user_mfa`, `user_recovery_codes`, `user_devices`, `auth_challenges`, `auth_login_attempts`, plus eight supporting indexes. All eight are added to the startup `requireSchemaTables` list, so `auto-init: false` still fails fast on a partially migrated database. There is no rolling-upgrade window for this step: `internal/storage/db.go` refuses to open a database whose `schema_meta.version` exceeds the binary's `SchemaVersion`, so the header comment in both migration scripts states that rollback means restoring the pre-upgrade backup.

**Configuration.** A new `security.auth` block (22 keys) and `server.trusted_proxies`. Configuration seeds the `auth_settings` row on first start; afterwards the database row is authoritative and `PUT /api/v1/auth/policy` is the only way to change it. `TUNNELMESH_TOKEN_ENCRYPTION_KEY`, `TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID` and `TUNNELMESH_TRACE_SIGNING_KEY` remain environment-only.

## Security impact

- OIDC `client_secret`, TOTP shared secrets, recovery-code hashes and challenge payloads (state, nonce, PKCE verifier, login ticket) are sealed with AES-256-GCM. The master key comes only from the environment; nothing is written to disk, to the audit log or to a response body.
- `alg=none` and every HMAC algorithm are rejected when verifying an id_token, so an attacker who learns a public key cannot forge a signature. Only RS/PS/ES/EdDSA families in the provider's declared `idTokenAlgs` are accepted.
- The `iss` claim must match the stored issuer (a trailing slash is normalized), and `redirectUri` must be an absolute https URL inside a configured allowed base, which bounds where an assertion code can be sent even by a compromised administrator.
- The provider name `providers` is reserved: the public router matches that literal segment first, so a provider stored under it would be permanently unreachable. Creation and update refuse it with `400 oidc_provider_invalid`.
- Login throttling keys on account and client address, and a wrong password answers the same `401 invalid_credentials` as an unknown username, so the endpoint cannot enumerate accounts. A disabled or soft-deleted account reached through OIDC provisioning answers `403 account_disabled`, which is an operator decision rather than a server defect.
- Trusted-device cookies are `HttpOnly`, `Secure` and `SameSite=lax`. An unrecognized or tampered cookie value is discarded and the second factor is demanded again. Revoking or renaming a device removes the bypass immediately, and the policy `allowTrustedDeviceBypass` requires `deviceTrustEnabled`, so a stored bypass cannot come back to life when trust is re-enabled.
- Challenges are single-use and attempt-capped; a replayed TOTP code is detected and refused. Recovery-code login decrements the stored counter.
- Audit rows record field names and identifiers only, never values that could be secrets.
- Out of scope and still not implemented: ICMP, TUN/L2 VPN, P2P NAT traversal, and arbitrary remote command execution.

## Tests run

Backend:

- `go build ./...` — clean
- `go vet ./...` — clean
- `go test ./... -count=1` — all packages pass
- `go test -race ./...` — all packages pass
- `git diff --check` — clean

Frontend:

- `npm test -- --run` — 33 files, 288 tests pass
- `npm run build` — succeeds and mirrors `web/dist` to the Go embed directory
- `./scripts/verify-web-embed.sh` — `web/dist and internal/server/web_dist match`

Documentation and configuration:

- `python3 scripts/gen_doc_index.py` — plan, spec, pull-request and ADR indexes regenerated
- YAML parse of `docs/api/openapi.yaml`, `docker-compose.cluster.yml` and `docker-compose.local.yml`
- Secret scan over the staged diff for private-key blocks, literal `client_secret` values and `tmrc-` recovery codes — no matches

Live smoke pass against a real `tunnelmesh-server` binary on SQLite with `auto-init`: legacy login shape preserved; MFA enroll, enable and verify; challenge single-use enforcement; wrong code, unknown challenge and replayed challenge all refused; device cookie attributes and bypass; garbage cookie rejected; rename and revoke remove the bypass; recovery-code login decrements the counter 10 to 9; `PUT /api/v1/auth/policy` refuses a request without `Idempotency-Key`. Credentials and identifiers from that run were throwaway and are deliberately not recorded here.

## Release steps

1. Back up the database. This release contains a schema migration and the migration has no in-place downgrade path.
2. Inject `TUNNELMESH_TOKEN_ENCRYPTION_KEY` (and `TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID` in a cluster) on every Server node before starting the new binary. For systemd, write `/etc/tunnelmesh/server.env`; for Compose, export the variable in `.env` or the orchestration layer.
3. Deploy the new binary with `auto-init` enabled, or run the v13 to v14 migration explicitly first.
4. Verify `schema_meta.version` is 14, the eight new tables exist, `/healthz` is green and `tunnelmesh_auth_login_total` is being scraped.
5. Existing logins are unaffected. Enable SSO and MFA afterwards through the console: register a provider, run its connectivity test, then set `mfa_mode` to `optional` before moving to `required`.
6. Watch the login success and throttle rates for one release interval before requiring a second factor for everybody.

## Rollback steps

Feature rollback needs no deploy: set `mfa_mode` to `disabled`, disable the OIDC providers, and turn `deviceTrustEnabled` off through `PUT /api/v1/auth/policy`. Every one of those takes effect immediately on all nodes.

Binary rollback to v13 requires restoring the pre-upgrade backup, because a v13 process refuses to open a v14 database. Do not attempt to drop the new tables in place.

## Reviewer focus

- `internal/auth/policy.go` and `internal/auth/auth_policy_service.go`: the config-seeds-database-is-authoritative rule, and the interaction between the global `mfa_mode` and the per-account `mfaRequired` floor.
- `internal/auth/oidc/`: id_token verification, algorithm allowlisting, issuer normalization, JWKS caching and the body-size cap.
- `internal/server/auth_oidc_api.go`: state consumption on every terminal callback path, and the constant redirect target.
- `internal/server/identity_services.go`: `authErrorStatus` is the single place that turns an identity sentinel into a status and a stable code.
- `internal/storage/identity_repository.go`: device eviction orders by least-recently-seen, and the per-account device cap is enforced in storage rather than in the service.
- `migrations/incremental/v0013_to_v0014/`: additive-only statements, retry safety, and SQLite/MySQL parity.
- `web/src/i18n/errors.ts`: every stable code the server can return on these surfaces has a translation in both locales.

## Integration status

Implementation, documentation and local validation are complete on `codex/phase-a-sso-mfa-device-trust`. Pull-request review and merge are pending.
