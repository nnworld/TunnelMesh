# Single sign-on and multi-factor authentication

This guide covers the enterprise identity features of the TunnelMesh admin console: OIDC single
sign-on, TOTP multi-factor authentication with one-time recovery codes, revocable trusted devices,
and the operator-editable authentication policy. It is written for two audiences — administrators who
configure identity providers and policy, and end users who enroll a second factor and manage their own
devices.

The Chinese documentation remains the authoritative deep reference:
[单点登录与两步验证](../../user-guide/sso-and-mfa.md). Configuration keys, defaults, and valid ranges
are documented in [配置说明](../../operations/configuration.md); the database upgrade in
[Schema Upgrade Guide](../../operations/schema-upgrades.md#v13-to-v14).

## Prerequisites

Three things must be true before SSO or MFA can be turned on. Each one fails closed rather than
degrading silently:

1. **`TUNNELMESH_TOKEN_ENCRYPTION_KEY` is injected.** The OIDC `client_secret`, the TOTP shared
   secret, and every `auth_challenges` payload (OIDC state, nonce, PKCE verifier, and the
   post-callback login ticket) are sealed with this AES-GCM key before they are written. Without it
   the Server still starts and password login and existing MFA verification keep working, but MFA
   enrollment, OIDC provider creation with a non-empty client secret, and **every OIDC login** (the
   authorize step has to create an encrypted state challenge) answer `503` with
   `data.error=secret_storage_unavailable`. Every Server node in a cluster must carry the identical key
   and key id, or a challenge issued on one node cannot be opened on another.
2. **`security.allowed_origins` or `security.allowed_hosts` is set.** The OIDC callback allowlist is
   derived from them: origins are used as-is and hosts are expanded to `https://<host>`. When both are
   empty, every `redirectUri` is rejected as "not inside an allowed base" and no provider can be
   registered.
3. **The database is at schema v14.** The eight new tables (`auth_settings`, `oidc_providers`,
   `user_identities`, `user_mfa`, `user_recovery_codes`, `user_devices`, `auth_challenges`,
   `auth_login_attempts`) must exist. A wrong version or a missing table fails at startup with
   `schema version mismatch` or `schema is missing required table <name>`; the Server never comes up
   with half an identity layer. `503 identity_services_unavailable` is reserved for an embedding
   deployment that uses the API as a library and did not install the identity container.

## The database is authoritative, not the config file

The policy fields under `security.auth` are a **seed for first boot only**. Once the `auth_settings`
row exists, the database wins:

- `session_token_ttl`, `device_trust.enabled`, and `device_trust.bypass_mfa` are written into the seed
  row exactly once. `mfa_mode` always starts at `disabled`, so an upgraded deployment behaves exactly
  as it did before this feature existed until an administrator opts in.
- After an administrator changes policy, editing the config file or the environment has no effect, and
  redeploying does not revert the decision.
- Device lifetime and the per-account device cap have **no config key at all**; they live only in
  `auth_settings`.
- Per-provider issuer, client id, client secret, and role mappings live in `oidc_providers`. There is
  no per-provider configuration key.
- The TOTP `issuer`, `digits`, and `period` are baked into the otpauth URL at enrollment time. Changing
  them affects new enrollments only.

One operational consequence is worth stating plainly: **turning a feature off never requires a
release.** Setting `auth_settings.mfa_mode='disabled'` and disabling every OIDC provider takes SSO and
MFA offline with no restart, no schema change, and no binary rollback. The procedure is in
[Schema Upgrade Guide](../../operations/schema-upgrades.md#v13-to-v14).

## Administrator: configuring OIDC single sign-on

The console entry point is **Single sign-on** in the left sidebar (route `/sso-providers`,
administrator-only). Everything the page does is also available under `/api/v1/sso/providers`; every
write requires an `Idempotency-Key`.

### Step 1 — Register the application at your IdP with the exact callback URL

The callback path is fixed, and `<provider>` is the provider's `name`:

```text
https://<your-public-hostname>/api/v1/auth/oidc/<provider>/callback
```

For a provider named `okta` served at `tunnel.example.com`, register exactly:

```text
https://tunnel.example.com/api/v1/auth/oidc/okta/callback
```

It must use `https`, it must fall inside the base derived from `security.allowed_origins` /
`security.allowed_hosts`, and it must match the redirect URI recorded at the IdP **character for
character** — a trailing slash, a case difference, or an internal address is rejected either by the IdP
or by this platform. Behind a reverse proxy, enter the public hostname, not `127.0.0.1:8080`.

`name` must match `^[a-z0-9][a-z0-9-]{1,62}$` (lowercase alphanumeric start, then lowercase letters,
digits, and hyphens; 2–63 characters). It is part of the callback path and **cannot be changed after
creation**; renaming means creating a new provider.

The login page uses a separate read-only surface:

```text
GET  /api/v1/auth/oidc/providers              # lists providers that are publicListed and enabled
GET  /api/v1/auth/oidc/<provider>/authorize    # 302 to the IdP
GET  /api/v1/auth/oidc/<provider>/callback     # IdP redirect target
POST /api/v1/auth/oidc/exchange                # trades a single-use ticket for a session
```

With `security.auth.oidc.public_providers: false` the first endpoint returns `404` — deliberately not
`403`, so a disabled feature is indistinguishable from a route that never existed. The login page then
renders no SSO buttons and users must know the `authorize` URL to start the flow.

### Step 2 — Create the provider

| Field | Required | Notes |
| --- | --- | --- |
| `name` | yes | URL-safe slug; see the rule above. Immutable after creation |
| `displayName` | no | Label shown on the login-page button |
| `issuer` | yes | Absolute `https` URL that must match the id_token `iss` claim (only a trailing slash is normalized). Private addresses are allowed so an on-premises IdP works |
| `clientId` | yes | Client id issued by the IdP |
| `clientSecret` | no | Empty means a public client using PKCE alone. When set, it is stored as AES-GCM ciphertext; the API only ever returns `hasSecret`. Omitting it on PATCH keeps the stored secret |
| `scopes` | yes | Must include `openid`. Stored de-duplicated with `openid` first |
| `redirectUri` | yes | Exact callback URL; see above |
| `authorizationEndpoint`, `tokenEndpoint`, `userinfoEndpoint`, `jwksUri` | no | Endpoint overrides that win over discovery, for an IdP whose discovery document is unreachable from this network. Each must be an absolute `http(s)` URL |
| `idTokenAlgs` | no | Signature allowlist, default `["RS256"]`. Accepts only `RS256/384/512`, `PS256/384/512`, `ES256/384/512`, and `EdDSA` |
| `usernameClaim` | no | Claim the account name comes from, default `preferred_username`, at most 64 characters |
| `roleMappings` | no | Ordered claim → role rules; see the next section |
| `defaultRole` | no | `admin` or `user`, default `user`. Used when no mapping matches |
| `authoritativeRoles` | no | Default `true`: every login rewrites the local role from IdP claims |
| `autoCreateUsers` | no | Default `true`: a first-time subject is provisioned just in time |
| `fetchUserinfo` | no | Default `false`: also call the userinfo endpoint after token exchange to complete the claim set |
| `publicListed` | no | Default `true`: whether the provider appears in the login-page button list |
| `enabled` | no | Default `true`. A disabled provider answers `404 oidc_provider_not_found` on authorize and callback |

The id_token allowlist **can never contain `none` or any `HS*`**. Both are rejected when the provider is
saved and rejected again while a token is verified, and no configuration value can enable them. The
reason is that an HMAC key is the client secret: anyone able to read the `oidc_providers` row already
holds it, so accepting `HS*` would turn readable configuration into a token-forging capability.

Test connectivity immediately after creating the provider:

```bash
curl -sS -X POST "https://tunnel.example.com/api/v1/sso/providers/${PROVIDER_ID}/test" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  -H 'Idempotency-Key: sso-provider-okta-test-001'
```

It performs one live discovery fetch and one JWKS retrieval and reports the result without changing any
configuration. It is treated as a mutation — idempotency key and audit row required — because it makes an
outbound request an operator must be able to account for. The test invalidates the cached discovery
document for that issuer first, so a retry always shows a fresh result.

### Step 3 — Map groups to roles

`roleMappings` is an **ordered** list; the first matching rule wins, and `role` must be `admin` or
`user`.

```json
{
  "roleMappings": [
    {"claim": "groups", "value": "tunnelmesh-admins", "role": "admin"},
    {"claim": "groups", "value": "tunnelmesh-users",  "role": "user"}
  ],
  "defaultRole": "user"
}
```

Mapping semantics:

- A claim value may be a string or an array of strings. Multi-valued claims such as `groups` match on
  membership.
- A claim that is absent from the id_token **skips the rule** instead of failing. IdPs differ in which
  group claim they release, and a missing claim simply means "no match".
- A rule whose `role` is not `admin` or `user` fails the whole request with `oidc_role_mapping_invalid`,
  even if a later rule would have matched. Silently ignoring it would let an operator believe a rule is
  in force.
- `defaultRole` applies when no rule matches.
- To receive `groups`, add it to `scopes` or configure the IdP to release it into the id_token;
  otherwise the claim is not there to match.
- With `fetchUserinfo: true`, claims from the userinfo endpoint also take part in mapping, for IdPs that
  release groups only there.

With `authoritativeRoles: true` (the default) every login rewrites the local role from the mapping
result, so the IdP is the single source of truth. With `false`, the role is assigned at first
provisioning only and is maintained locally afterwards. One hard protection applies: when a rewrite
would demote the **last active administrator**, the login fails with `last_admin_protected` rather than
locking everybody out.

### Step 4 — Understand provisioning

With `autoCreateUsers: true`, the first login of a `(provider, sub)` pair creates an account with
`auth_source='oidc'` and no local password hash; it can only be reached through that IdP. The username
is resolved in order from the claim named by `usernameClaim`, then the `email` claim, then a fallback
derived from the provider and `sub`, and is sanitized to the platform rule (`[A-Za-z0-9._-]`, 3–64
characters). An `@` in an email becomes `_`. The mapping is deterministic, so the same address always
resolves to the same account. When no legal username can be derived, login fails with
`oidc_claim_missing`.

An existing local-password account that signs in through SSO with the same username becomes
`auth_source='mixed'` and can then log in by either path. Unlinking every external identity returns it
to `local`.

With `autoCreateUsers: false`, an unknown subject receives `403 oidc_user_not_provisioned`. Use this
when accounts must be opened explicitly by an administrator.

### Provider management endpoints

| Method | Path | Notes |
| --- | --- | --- |
| `GET` | `/api/v1/sso/providers` | Cursor-paginated list. Returns `hasSecret`, never the secret |
| `POST` | `/api/v1/sso/providers` | Create; requires `Idempotency-Key` |
| `GET` | `/api/v1/sso/providers/{id}` | Detail |
| `PATCH` / `PUT` | `/api/v1/sso/providers/{id}` | Update; requires `Idempotency-Key`. Booleans use pointer semantics, so an omitted field keeps its stored value instead of flipping to `false` |
| `DELETE` | `/api/v1/sso/providers/{id}` | Delete; requires `Idempotency-Key`. Fails with `409 oidc_provider_in_use` while accounts are still linked |
| `POST` | `/api/v1/sso/providers/{id}/test` | Discovery and JWKS connectivity check; requires `Idempotency-Key` |

## Administrator: authentication policy

The console exposes the policy on the **Single sign-on** page. The API is `GET /api/v1/auth/policy` and
`PUT /api/v1/auth/policy` (administrator-only; PUT requires an `Idempotency-Key`). Policy is a single
`auth_settings` row, and the audit record names exactly which fields changed.

| Field | Valid range | Seed default | Meaning |
| --- | --- | --- | --- |
| `mfaMode` | `disabled` / `optional` / `required` | `disabled` | Global second-factor policy; see below |
| `deviceTrustEnabled` | bool | `true` | Whether trusted devices may be issued at all |
| `deviceTrustTtlSeconds` | 3600–7776000 (1 hour–90 days) | `2592000` (30 days) | Trusted-device lifetime |
| `allowTrustedDeviceBypass` | bool | `true` | Whether a valid trusted device skips the second factor |
| `maxTrustedDevices` | 1–100 | `10` | Maximum simultaneously trusted devices per account |
| `sessionTokenTtlSeconds` | ≥ 0 | `0` | Console token lifetime; `0` keeps the historical non-expiring behaviour |

What the three `mfaMode` values mean:

- `disabled` — no global second factor. A per-account `users.mfa_required=1` **still applies**: the
  per-account flag is a floor, not a suggestion.
- `optional` — enrolled accounts must present a second factor; accounts that never enrolled may sign in
  with a password alone. This is the recommended transition setting.
- `required` — every account must present a second factor. An account that has not enrolled is routed to
  enrollment and receives no session until it completes.

One validation rule is worth calling out: `allowTrustedDeviceBypass: true` requires
`deviceTrustEnabled: true`, otherwise the PUT fails with `400 auth_policy_invalid`. A bypass stored
alongside disabled device trust is inert today, but it would silently grant a bypass the moment an
operator re-enabled trust without re-reading the row, so it is rejected on write.

### Forcing MFA on one account

```bash
curl -sS -X PATCH "https://tunnel.example.com/api/v1/users/${USER_ID}" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"mfaRequired": true}'
```

The change is audited as `account.mfa_required` or `account.mfa_optional`. It cannot be applied to an
account with the `admin` role (`403 admin_account_protected`) — cover administrators with the global
`mfaMode: required` instead. At least one of `disabled` and `mfaRequired` must be present, or the
request fails with `400`.

A forced account that has not enrolled yet receives `mfaRequired: true` with `methods: ["totp"]` — no
`recovery`, because it has no recovery codes yet — which is how the console routes it to setup rather
than to a dead end.

### Administrator identity endpoints

| Method | Path | Notes |
| --- | --- | --- |
| `GET` | `/api/v1/users/{id}/mfa` | MFA status and remaining recovery-code count |
| `POST` | `/api/v1/users/{id}/mfa/reset` | **Clears** the second factor and revokes every trusted device; requires `Idempotency-Key`. This is the standard recovery action for a lost authenticator |
| `GET` | `/api/v1/users/{id}/devices` | List that account's trusted devices (none is marked current) |
| `DELETE` | `/api/v1/users/{id}/devices/{deviceId}` | Revoke one device; requires `Idempotency-Key`. The incident-response action for a lost laptop |
| `GET` | `/api/v1/users/{id}/identities` | List external identity links |
| `DELETE` | `/api/v1/users/{id}/identities/{identityId}` | Unlink one; requires `Idempotency-Key`. The last link of a passwordless account cannot be removed (`409 identity_required_for_login`) |

The user id always comes from the path, never from a request body, so an administrator cannot be tricked
into acting on an account the URL does not name.

## User: enrolling a second factor

The console entry point is **Security** in the avatar menu (route `/account/security`).

1. Choose "Enable two-factor authentication". A local-password account must first re-enter its
   **current password**; an SSO-only account has no password and may leave it empty. This step stops a
   hijacked session from making an attacker's authenticator durable.
2. The response carries one-time enrollment material: `secret` (Base32), `otpauthUrl`, `recoveryCodes`,
   and `expiresAt`. Scan the QR code for `otpauthUrl` in an authenticator app, or type `secret` by hand.
   The URL looks like:

   ```text
   otpauth://totp/<issuer>:<username>?algorithm=SHA1&digits=6&issuer=<issuer>&period=30&secret=<BASE32>
   ```

   The issuer and account are percent-encoded inside the label, which is why
   `security.auth.mfa.issuer` may not contain a colon. The secret is upper-cased, `algorithm` is always
   `SHA1`, and `digits`/`period` come from `security.auth.mfa`.
3. Enrollment material stays valid for **15 minutes**. After that, generate it again.
4. Enter the current 6-digit code and confirm. Status moves from `pending` to `enabled`.
5. **Store the recovery codes offline immediately.** They start with `tmrc-` and carry 20 Base32
   characters; the count comes from `security.auth.mfa.recovery_codes` (10 by default). They are
   returned exactly once, at enrollment and at regeneration, with `Cache-Control: no-store`. The
   database keeps only a SHA-256 digest, so **nobody can read them back — not even an administrator**.
   Put them in a password manager or print them into a safe. Do not screenshot them into a photo
   library, and do not paste them into chat or a ticket.

Each recovery code works **once** and is then spent. A login that used one reports
`remainingRecoveryCodes`; spending the last one also sets `recoveryCodesExhausted: true`. That flag is
the only moment the user is told their bypass is gone, so regenerate promptly.

Regenerating recovery codes requires one currently valid TOTP code as proof of possession, and
**invalidates every existing code immediately**.

Disabling MFA also requires one valid TOTP or recovery code, and it **revokes every trusted device on
the account** at the same time. A device was trusted on the strength of the factor being removed, so
that trust has to go with it. When policy is `mfaMode: required`, or the account is individually forced,
disabling fails with `409 mfa_required_by_policy`.

## User: trusted devices

Tick "Trust this device" at login and the Server issues a long-lived credential in an `HttpOnly` cookie
(`tm_device` by default, `Secure` and `SameSite=Lax`). While it is valid, that browser can skip the
second factor — provided `allowTrustedDeviceBypass` is still on.

The Security page lists each device's name, login IP, browser user agent, when it was trusted, when it
was last seen, when it expires, and which one is current. Devices can be renamed and revoked.

Facts worth knowing about this credential:

- Only the SHA-256 digest of the token is stored. The plaintext is delivered once, through the cookie,
  and no API ever echoes it.
- The cookie is `HttpOnly`, so a script-injection bug in the console cannot read it.
- The cookie `Max-Age` is the smaller of the configured TTL and the remaining lifetime of the stored
  device row, so a cookie can never outlive the row that authorizes it.
- Revocation is immediate, and revoking the device making the request also clears the browser cookie.
- At the `maxTrustedDevices` cap the least recently seen active device is revoked before the new one is
  issued (ordered by `last_seen_at`, falling back to `trusted_at` for a device never re-presented); the
  login itself does not fail.
- Turning off `deviceTrustEnabled` in policy invalidates **every** bypass at once, without waiting for
  cookies to expire and without touching tokens or sessions, because each login re-reads the policy from
  the database.
- Expired devices are neither listed nor accepted; the background sweeper deletes their rows after the
  retention window.

Self-service endpoints:

| Method | Path | Notes |
| --- | --- | --- |
| `GET` | `/api/v1/auth/devices` | List your trusted devices, marking `current` |
| `PATCH` | `/api/v1/auth/devices/{id}` | Rename |
| `DELETE` | `/api/v1/auth/devices/{id}` | Revoke |
| `GET` | `/api/v1/auth/identities` | List your external identity links |
| `DELETE` | `/api/v1/auth/identities/{id}` | Unlink one; the last link of a passwordless account cannot be removed |

## User: linking and unlinking single sign-on

The Security page shows linked identities with provider, subject, account, link time, and last login.

- An account that already has a local password is linked automatically the first time it signs in
  through SSO; `auth_source` becomes `mixed` and both paths work afterwards.
- An SSO-provisioned account (`auth_source='oidc'`, no local password) **cannot unlink its last
  identity**, because that would leave it with no usable credential. The attempt returns
  `409 identity_required_for_login`.

## Login flows

### Password plus second factor

The diagram below shows only the contents of `data`; the envelope is described at the end of this section.

```text
POST /api/v1/auth/login   {"username","password","trustDevice"}
  ├─ 200 data={"token","user","deviceTrusted"}                       # policy needs no second factor
  ├─ 200 data={"mfaRequired":true,"challengeId","methods","expiresAt"}  # second factor required
  ├─ 401 data.error="invalid_credentials"                            # bad credentials
  └─ 429 data.error="login_throttled" + Retry-After header           # this username+IP bucket is blocked

POST /api/v1/auth/mfa/verify   {"challengeId","code","trustDevice"}
  └─ 200 data={"token","user","deviceTrusted"[,"recoveryCodesExhausted":true]}
```

The `code` field accepts either a 6-digit TOTP code or a `tmrc-` recovery code; the Server tells them
apart by prefix. When MFA is not triggered, the `data` object of the login response still carries the
pre-upgrade `token` and `user` fields and only adds `deviceTrusted` (plus `recoveryCodesExhausted` when
a recovery login spent the last code), so scripts and clients that ignore unknown fields keep working.

One **behaviour change** is worth noting. The envelope is always
`{ "code": <HTTP status>, "msg": <HTTP status text>, "data": ... }`, and the machine-readable code lives
in `data.error`. On a credential failure `data.error` moved from the prose `invalid credentials` to the
stable code `invalid_credentials`; a client that matched the old string exactly needs to switch to
matching `data.error`.

Throttling buckets by `SHA-256(lower(username) + "|" + clientIP)`. After `max_attempts` failures inside
`security.auth.login_throttle.window`, the bucket is blocked for `block`. **The request that triggers
the block still returns an ordinary `401 invalid_credentials`**; only the next request gets `429`, so
the response cannot be used to probe the block boundary. Every credential failure collapses into the
same `invalid_credentials`, which also makes account enumeration impossible.

### Single sign-on

```text
browser → GET /api/v1/auth/oidc/<provider>/authorize
           the Server generates state, nonce, and a PKCE (S256) verifier, seals them into one
           auth_challenges row, uses the challenge id itself as the state (already 192 bits of
           randomness), and 302s to the IdP
        ← IdP authenticates → GET /api/v1/auth/oidc/<provider>/callback?code&state
           exchange the code → verify the id_token → provision or update the account → consume state
           ├─ second factor required: 302 → /login?mfa=<challengeId>
           └─ otherwise:              302 → /login?ticket=<single-use ticket>
browser → POST /api/v1/auth/oidc/exchange {"ticket","trustDevice"} → 200 data={"token","user",...}
```

Several deliberate constraints are worth knowing operationally:

- **State, nonce, and PKCE verifier live in the database, not in process memory**, so any node in the
  cluster can complete the redirect. Logins do not need session affinity.
- The state's `max_attempts` is 1: it is consumed by the callback, so presenting the same state twice
  always fails.
- State is consumed only after provisioning succeeds, which keeps a transient database error retryable
  inside the state TTL.
- **The ticket is exchanged with POST rather than GET**, so this bearer credential never lands in
  browser history, a `Referer` header, or a proxy access log. Its lifetime is hard-capped at 300
  seconds (60 by default).
- The callback redirects to exactly one fixed relative path, `/login`, with a URL-escaped value. Neither
  the provider configuration nor the IdP can influence the destination, which is what makes an open
  redirect through the callback impossible.
- When the IdP returns an `error` parameter, the platform reports only the stable `oidc_provider_error`
  and never echoes `error_description`. That text is IdP-controlled and must not become reflected
  content on this origin.
- PKCE `code_challenge_method` is always `S256` and `grant_type` is always `authorization_code`;
  neither is configurable.

A verified IdP assertion is **one** factor. Account policy is applied again after the callback, so an
account under `mfaMode: required` or an individual override still owes a second factor even though SSO
succeeded.

## Endpoint reference

No session required:

| Method | Path |
| --- | --- |
| `POST` | `/api/v1/auth/login` |
| `POST` | `/api/v1/auth/mfa/verify` |
| `POST` | `/api/v1/auth/oidc/exchange` |
| `GET` | `/api/v1/auth/oidc/providers` |
| `GET` | `/api/v1/auth/oidc/{provider}/authorize` |
| `GET` | `/api/v1/auth/oidc/{provider}/callback` |

Session required (self):

| Method | Path |
| --- | --- |
| `GET` | `/api/v1/auth/me` |
| `PUT` | `/api/v1/auth/password` |
| `GET` / `DELETE` | `/api/v1/auth/mfa` |
| `POST` | `/api/v1/auth/mfa/enroll`, `/api/v1/auth/mfa/enable`, `/api/v1/auth/mfa/recovery-codes` |
| `GET` | `/api/v1/auth/devices` |
| `PATCH` / `DELETE` | `/api/v1/auth/devices/{id}` |
| `GET` | `/api/v1/auth/identities` |
| `DELETE` | `/api/v1/auth/identities/{id}` |

Administrator role required: `/api/v1/auth/policy`, `/api/v1/sso/providers*`,
`/api/v1/users/{id}/mfa*`, `/api/v1/users/{id}/devices*`, `/api/v1/users/{id}/identities*`.

Every response uses the uniform `{ "code": <HTTP status>, "msg": <HTTP status text>, "data": ... }`
envelope. The stable machine-readable code is in `data.error`; `msg` is only the HTTP status text, so
never branch on it. Lists are cursor-paginated. Trusted-device and identity lists are bounded by policy (at
most `maxTrustedDevices` devices, at most one link per provider), so a single page is always complete
and `nextCursor` is empty with `hasMore: false`.

## Troubleshooting

`data.error` is a stable code. Branch on it, never on the prose message.

| HTTP | `data.error` | Meaning and action |
| --- | --- | --- |
| `400` | `idempotency_key_required` | An administrative write is missing `Idempotency-Key`, or the header exceeds 255 characters |
| `400` | `auth_policy_invalid` | Policy field out of range: `deviceTrustTtlSeconds` must be 3600–7776000, `maxTrustedDevices` must be 1–100, `sessionTokenTtlSeconds` must not be negative, or `allowTrustedDeviceBypass` was sent with `deviceTrustEnabled=false` |
| `400` | `oidc_provider_invalid` | Provider field invalid: `name` does not match `^[a-z0-9][a-z0-9-]{1,62}$`, `issuer`/`redirectUri` is not an absolute `https` URL, `redirectUri` is outside the allowed base, `scopes` lacks `openid`, `defaultRole` is not `admin`/`user`, or the request tried to change the immutable `name` |
| `400` | `oidc_role_mapping_invalid` | `roleMappings[].role` is not `admin` or `user` |
| `400` | `current_password_required` | A local-password account tried to enroll MFA without supplying its current password |
| `400` | `oidc_state_invalid` | State missing, expired, already consumed, or bound to a different provider. Restart SSO from the login page |
| `400` | `oidc_provider_error` | The IdP returned `error` on the callback (user cancelled, application not authorized, and similar). Check IdP-side logs |
| `401` | `invalid_credentials` | Wrong username or password, disabled or deleted account, or an account with no local password. **Every credential failure collapses to this one code**, so it cannot reveal whether an account exists |
| `401` | `login_ticket_invalid` | The single-use ticket expired, was already exchanged, or its account became unusable. Restart SSO |
| `401` | `mfa_challenge_invalid` | `challengeId` missing, expired, or consumed. Start the login again |
| `401` | `mfa_attempts_exceeded` | The challenge's attempt budget is spent (5 by default). Start the login again |
| `401` | `mfa_code_invalid` | Wrong TOTP or recovery code. Check the authenticator clock and that the code is from the current step |
| `401` | `mfa_code_reused` | The same TOTP step was presented twice (replay guard, backed by `user_mfa.last_used_step`). Wait for the next step |
| `401` | `mfa_not_enrolled` | The account has no usable MFA enrollment. Enroll from the Security page first |
| `401` | `oidc_id_token_invalid` / `oidc_unsupported_algorithm` / `oidc_no_matching_key` / `oidc_claim_missing` | id_token verification failed: bad signature, `iss`/`aud`/`exp`/`nonce` mismatch, an algorithm outside the allowlist (or the IdP used `none`/`HS*`), no matching `kid` in the JWKS, a missing `sub`, or no legal username could be derived |
| `403` | `forbidden` | A non-administrator called an administrative endpoint, or a user acted on a resource that is not theirs |
| `403` | `current_password_invalid` | The current password supplied for MFA enrollment or a password change is wrong |
| `403` | `admin_account_protected` | An attempt to modify an `admin`-role account through the child-account API, including setting `mfaRequired` |
| `403` | `last_admin_protected` | The operation would remove or demote the last active administrator |
| `403` | `oidc_user_not_provisioned` | The provider has `autoCreateUsers: false` and the subject has no account. An administrator must create it first |
| `404` | `oidc_provider_not_found` / `oidc_provider_disabled` | The provider name does not exist or is disabled. Note that `GET /api/v1/auth/oidc/providers` also returns `404` when `public_providers` is off |
| `404` | `device_not_found` / `identity_not_found` / `account_not_found` | The target does not exist. `404` rather than `403` on purpose, so an identifier cannot probe which accounts have an external login or a trusted browser |
| `409` | `mfa_required_by_policy` | Policy requires MFA (global `required`, or a per-account override), so it cannot be disabled |
| `409` | `mfa_not_confirmed` | The enrollment is still `pending`; activate it with one TOTP code first |
| `409` | `mfa_already_enabled` | Already activated; it cannot be activated twice |
| `409` | `device_trust_disabled` | Policy has device trust off, so no trusted device can be issued |
| `409` | `identity_required_for_login` | An attempt to unlink the last external identity of a passwordless account |
| `409` | `oidc_provider_in_use` | Accounts are still linked to the provider, so it cannot be deleted. Disable it (`enabled=false`) or unlink the accounts first |
| `409` | `conflict` | The `(provider, subject)` unique constraint was hit, usually by concurrent logins provisioning the same subject. Retry |
| `429` | `login_throttled` | The username+IP bucket is blocked; the response carries `Retry-After` in seconds |
| `502` | `oidc_discovery_failed` / `oidc_jwks_fetch_failed` / `oidc_token_exchange_failed` | The IdP is unreachable or misbehaving. Check egress, `oidc.http_timeout`, the issuer spelling, and reproduce with the provider test endpoint |
| `503` | `secret_storage_unavailable` | `TUNNELMESH_TOKEN_ENCRYPTION_KEY` is absent or unusable. Inject the key and retry; the platform never falls back to plaintext |
| `503` | `identity_services_unavailable` | Identity services are not wired, or the schema is not at v14. Check `schema_meta.version` |
| `500` | `internal_error` | Unclassified failure. Take the trace id to the Server logs |

A useful triage order: group `tunnelmesh_auth_oidc_step_total{result="error"}` by `step` in `/metrics`
to see which stage is failing → reproduce discovery and JWKS with the provider test endpoint → read the
audit log (`auth.login.*`, `auth.mfa.*`, `auth.device.*`, `auth.oidc.*`, `auth.policy.update`) to see
what happened on the account.

## Lockout and recovery

Three cases, depending on who is locked out.

**A user lost their authenticator but still has recovery codes.** Enter any unused `tmrc-` code in the
second-factor field on the login page. After signing in, regenerate recovery codes and re-enroll an
authenticator from the Security page.

**A user lost both the authenticator and the recovery codes.** An administrator calls
`POST /api/v1/users/{id}/mfa/reset` (with an `Idempotency-Key`). It clears the second factor **and
revokes every trusted device on the account**. That destructiveness is intentional: the account must
re-enroll, and re-enrolling is the only way to be confident the person doing it owns the account. The
user then repeats the enrollment steps above.

**Every administrator is locked out** — all administrator authenticators are lost, or `mfaMode: required`
was enabled and nobody can get in. Do not try to recover over HTTP; there is no anonymous recovery
endpoint. Use the existing administrator bootstrap/recovery flow on the Server host, against the same
config file and database:

```bash
tunnelmesh-server --config /etc/tunnelmesh/server.yaml admin regenerate-credentials --confirm
```

The command holds a process-level recovery mutex and runs its work inside a database transaction —
the combination the code calls the cross-process lock path — then revokes every outstanding token on
that administrator account, console sessions and service tokens alike, generates a fresh high-entropy
credential, writes an audit row, and prints the username and password **only to the command's stdout**,
never to logs and never through an HTTP response. Sign in with the new credential,
then run `mfa/reset` for the other administrators, or set `auth_settings.mfa_mode` back to `disabled`
temporarily to buy handling time. Do not reach for `admin bootstrap` when an administrator already
exists; it refuses to run, and repeated attempts only mask a real problem such as pointing at the wrong
database.

The full command semantics, output handling, and cautions are documented in
[Troubleshooting](../../operations/troubleshooting.md) and
[Server administration](server-admin.md); they are not repeated here.

## Audit actions

The identity flows write these audit actions, filterable on the **Audit logs** page:

| Action | Trigger |
| --- | --- |
| `auth.login.success` | Successful login; details carry the method, client IP, and whether a trusted device was used |
| `auth.login.failure` | Wrong password, wrong second factor, or attempt budget exhausted; details carry method, IP, and reason |
| `auth.login.throttled` | A blocked throttle bucket was hit; the resource is the bucket |
| `auth.login.mfa_required` | Password or SSO succeeded but a second factor is still owed |
| `auth.mfa.enroll` / `auth.mfa.enable` / `auth.mfa.disable` | Enrollment material generated / activated / disabled (details include how many devices were revoked) |
| `auth.mfa.verify` | One second-factor verification succeeded |
| `auth.mfa.recovery_regenerate` | Recovery codes regenerated (details carry only the count) |
| `auth.mfa.reset` | An administrator cleared an account's second factor |
| `auth.device.trust` / `auth.device.revoke` / `auth.device.revoke_all` | A trusted device was issued / one revoked / all revoked |
| `auth.identity.unlink` | An external identity link was removed |
| `auth.oidc.provision` | An SSO login provisioned or updated an account |
| `auth.oidc.provider.create` / `.update` / `.delete` / `.test` | Provider create, update, delete, and connectivity test |
| `auth.policy.update` | Global authentication policy changed; details carry the changed field names and their new values |
| `account.mfa_required` / `account.mfa_optional` | An administrator forced or unforced MFA on one account |

No audit row carries a secret: no challenge id, login ticket, device token, TOTP secret, recovery-code
plaintext, client secret, or IdP `error_description`. A provider update records which field names
changed, never the secret value.

## Explicitly out of scope

These are deliberately not implemented, so do not design a workflow around them: P2P NAT traversal and
arbitrary remote command execution. ICMP echo and an embedded WireGuard VPN gateway are a separate
approved feature ([ADR 0002](../../architecture/adr/0002-public-ingress-and-embedded-vpn.md), built only with `-tags vpn`); they are also out of scope for this release and do not
interact with identity. SSH support stays limited to the existing
stdio/WebSocket proxy path and is not extended into a general command-execution API. WebAuthn/passkeys,
SMS and email OTP, SCIM user sync, and SAML are also not implemented; OIDC is the only federation
protocol, and TOTP plus one-time recovery codes is the only second factor.
