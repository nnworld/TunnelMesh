# Security hardening

This guide summarizes TunnelMesh's transport, token, identity, policy, RBAC, and logging controls. The Chinese documentation remains the authoritative deep reference.

## TLS and allowed hosts/origins

- Use HTTPS and WSS for all public traffic.
- Terminate TLS at a reverse proxy or directly on the Server.
- Allow only TLS 1.2 or newer.
- Restrict `security.allowed_hosts` to the production hostname.
- When WebSSH or SFTP is enabled, restrict `security.allowed_origins` to the exact admin-console origin.
- Public ingress is HTTP/HTTPS/WSS. The approved VPN gateway ([ADR 0002](../../architecture/adr/0002-public-ingress-and-embedded-vpn.md), in progress) will add exactly one public UDP port that bypasses the reverse proxy; until it ships, the Server exposes no public UDP. Allow that port separately in security groups and host firewalls when it lands.

## Scoped Agent and Client tokens

Service tokens are typed as `agent`, `client`, or `server_node`.

- Bind an Agent token to a specific Agent.
- Bind a Client token to one or more explicitly allowed Agents when practical.
- Limit token scope by protocol, target CIDR, and target port.
- Set an expiration time for short-lived or operator-specific tokens.
- Rotate tokens instead of reusing long-lived shared credentials.

Token plaintext is shown once at creation or rotation. Store it in a secret manager, not in a repository or ticket.

## Agent CIDR and port policy

Agent policy is enforced on the Server and revalidated by the Agent before dialing a target.

- Permit only required protocols.
- Allow only necessary CIDRs.
- Restrict target ports to the minimum set.
- Keep private, loopback, and link-local ranges blocked unless a specific route needs them.
- Review policies when an internal network changes.

This defense in depth prevents a compromised token from becoming unrestricted access to every internal service.

## Admin RBAC and audit logs

- Assign the `admin` role only to operators who need global control.
- Regular users should see only their own Agents, routes, and tokens.
- Use structured audit logs for create, update, delete, reveal, and authorization-denied events.
- Review audit logs during incident response and periodic access reviews.

The admin console and API enforce authorization server-side. Do not trust client-supplied ownership or role fields.

## Console identity: SSO, MFA, and trusted devices

The management console supports OIDC single sign-on, TOTP multi-factor authentication with one-time
recovery codes, and revocable trusted devices. The operating guide is
[Single sign-on and multi-factor authentication](../user-guide/sso-and-mfa.md); this section states the
security model.

**Secret storage fails closed.** OIDC client secrets, TOTP shared secrets, and every `auth_challenges`
payload (OIDC state, nonce, PKCE verifier, and the post-callback login ticket) are sealed with AES-GCM
under `TUNNELMESH_TOKEN_ENCRYPTION_KEY` before they are written. When the key is absent the Server still
starts and password login keeps working, but MFA enrollment, OIDC provider creation with a non-empty
client secret, and every OIDC login return `503` with `data.error=secret_storage_unavailable`. There is
no plaintext fallback path, and a provider whose row already holds a sealed secret cannot be resolved
at all when the key is missing or the key id no longer matches.
Every node in a cluster must carry the same key and key id, or a challenge issued on one node cannot be
opened on another.

**Bypass credentials are stored one-way.** Recovery codes and trusted-device tokens are persisted only as
SHA-256 digests (`user_recovery_codes.code_hash`, `user_devices.token_hash`), both under a uniqueness
constraint. A database leak cannot be replayed, and no API — including the administrator ones — can read
a code or token back. Login throttle buckets are keyed by `SHA-256(lower(username) + "|" + clientIP)`, so
`auth_login_attempts` stores neither usernames nor IP addresses. This is deliberately different from the
service-token and TOTP-secret path, which must remain recoverable and is therefore encrypted rather than
hashed.

**`alg=none` and every `HS*` are rejected unconditionally.** id_token signature verification accepts only
`RS256/384/512`, `PS256/384/512`, `ES256/384/512`, and `EdDSA`. The `none` algorithm and all HMAC
algorithms are refused twice: when an operator saves a provider allowlist, and again while verifying a
token. No configuration value can enable them. The reason is that an HMAC key is the client secret, so
anyone able to read the `oidc_providers` row already holds it — accepting `HS*` would turn readable
configuration into a token-forging capability.

**The relying party does not trust the IdP's URL space.** The issuer must be an absolute `https` URL and
must match the id_token `iss` claim, with only a trailing slash normalized on either side. Discovery,
JWKS, token, and userinfo endpoints must be
absolute `http(s)` URLs, which stops a relative endpoint from being resolved against an
attacker-influenced base. Every relying-party response body is size-capped by
`security.auth.oidc.max_discovery_body_bytes` — despite the name it covers discovery, JWKS,
userinfo, and the token endpoint alike, and an oversized body fails the step instead of being
truncated and parsed. Every outbound call is bounded by `security.auth.oidc.http_timeout`.

Note the deliberate asymmetry with tunnel targets: the SSRF policy that blocks loopback, private, and
link-local addresses applies to Agent dial targets, **not** to operator-configured identity providers. An
on-premises IdP legitimately lives on an internal address, and only an administrator can register one.
The compensating controls are that registration is administrator-only, the issuer must be `https`, and
the callback must fall inside the deployment's own allowed base. Treat the ability to create an OIDC
provider as the ability to make the Server issue outbound requests to an administrator-chosen host, and
restrict egress accordingly.

**The callback cannot become an open redirect.** `redirectUri` must be `https` and must fall inside a
base derived from `security.allowed_origins` and `security.allowed_hosts`; with both empty, every value
is rejected. After the callback the browser is redirected to exactly one fixed relative path (`/login`)
with a URL-escaped value, so neither the provider row nor the IdP can influence the destination. An IdP
that returns an `error` parameter produces only the stable `oidc_provider_error` code — the
IdP-controlled `error_description` is never echoed, so it cannot become reflected content on this origin.

**The authorization code flow is hardened by construction.** PKCE is always used with
`code_challenge_method=S256`; `plain` is not offered and `grant_type` is fixed to `authorization_code`.
State, nonce, and verifier live in the database rather than in process memory, so any cluster node can
complete the redirect. The state is single-use (`max_attempts=1`) and is consumed on every terminal
callback path, success and failure alike, so a captured state can never be presented a second time.
The post-callback hand-off uses a single-use login ticket exchanged by **POST**, capped at 300
seconds, so this bearer credential never lands in browser history, a `Referer` header, or a proxy access
log.

**A verified IdP assertion is one factor, not a session.** Account policy is re-evaluated after the
callback, so `mfaMode: required` and per-account `users.mfa_required` still demand a second factor from
an SSO-authenticated user.

**Role authority is explicit and bounded.** With `authoritativeRoles: true` the IdP is the single source
of truth for role, and every login rewrites it from the ordered claim mappings. Demoting the last active
administrator fails with `last_admin_protected` instead of locking the deployment out. Only `admin` and
`user` are assignable; an invalid mapping role fails the request rather than being skipped, so an
operator can never believe a rule is in force when it is not.

**Trusted devices are revocable, bounded, and immediately killable.** The credential is delivered once in
an `HttpOnly` cookie, and the cookie `Max-Age` is the smaller of the configured TTL and the remaining
lifetime of the stored row, so a cookie can never outlive its authorization. Device count per account is
capped by `auth_settings.max_trusted_devices`; at the cap the least recently seen active device is
evicted, ordered by `COALESCE(last_seen_at, trusted_at)`. Disabling
`device_trust_enabled` invalidates every bypass on the next login without waiting for cookies to expire,
because policy is re-read from the database on each decision. Disabling MFA also revokes every trusted
device on that account, since each was trusted on the strength of the factor being removed.

**Account enumeration and throttle probing are both closed off.** Every credential failure collapses into
a single `invalid_credentials`. A missing device or identity link returns `404` rather than `403`, so an
identifier cannot be used to discover which accounts have an external login or a trusted browser. The
request that trips the login throttle still returns an ordinary `401`; only the next request receives
`429`, so the block boundary cannot be probed. `X-Forwarded-For` is believed only when the direct peer is
listed in `server.trusted_proxies`, which is empty by default — otherwise any client could pick its own
throttle bucket.

**Secrets never reach logs, metrics, or audit rows.** Identity metric labels are a closed enumeration
(`method`, `result`, `step`) that excludes usernames, client IPs, provider ids, challenge ids, and device
tokens; anything outside the set is normalized to `unknown`. `/metrics` is readable without a management
session, so putting account names in a label would be both a disclosure and an attacker-controlled
cardinality problem. Audit rows record outcomes and counts, never challenge ids, tickets, device tokens,
TOTP secrets, recovery-code plaintext, client secrets, or IdP `error_description`.

**Disable without deploying.** Because policy lives in the database, an incident can be contained by
setting `auth_settings.mfa_mode='disabled'` and disabling every OIDC provider — no restart, no schema
change, no binary rollback. Both actions are audited. See
[Schema Upgrade Guide](../../operations/schema-upgrades.md#v13-to-v14) for the procedure.

## Token encryption and reveal risk

With `TUNNELMESH_TOKEN_ENCRYPTION_KEY` configured, newly created or rotated service tokens are stored as AES-256-GCM ciphertext, not plaintext. Token validation still uses a hash.

Reveal is a privileged, audited operation:

1. Only administrators may call the reveal API.
2. The request must include the explicit confirmation header and a unique idempotency key.
3. The response is marked `Cache-Control: no-store`.
4. The action is written to the audit log.

Legacy tokens created before encryption cannot be recovered; rotate them first.

The same key and key id also seal the console identity secrets described above: the OIDC
`client_secret`, the TOTP shared secret, and every authentication challenge payload. Rotating the key
without a re-seal plan orphans all of them at once — service-token reveal, browser SSH/SFTP
auto-authentication, MFA verification, and OIDC login all stop working. Rotate deliberately, keep every
node on the same key id, and treat the key as a tier-one secret with the same custody as the database
backup.

## Safe logging and secret handling

Do not log or expose:

- Bearer tokens or complete `Authorization` headers.
- Passwords, private keys, or database DSNs.
- WebSSH terminal bytes or SFTP file contents.
- Unredacted Agent metadata values.
- Full target addresses when a summarized or redacted form is enough.
- TOTP shared secrets, recovery codes, trusted-device tokens, OIDC client secrets, authorization codes, PKCE verifiers, login tickets, or id_token values.

Restrict access to Server logs, audit logs, metrics, and database backups. Use short log retention where allowed, and forward security-relevant events to a controlled audit system.

