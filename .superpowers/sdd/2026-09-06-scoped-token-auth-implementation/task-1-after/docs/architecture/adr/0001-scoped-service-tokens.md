# ADR 0001: Separate scoped service credentials from management sessions

- Status: Accepted
- Date: 2026-09-06

## Context

TunnelMesh authenticates both interactive management users and long-lived service principals. Management sessions belong to users, while Agent, Client, and Server-node credentials have different ownership, scope, rotation, and revocation requirements. Treating these credentials as one namespace would blur authorization boundaries and make lifecycle policy harder to audit.

Service credentials must remain non-recoverable after issuance. Their active, expired, revoked, and recently used states also need portable semantics across SQLite and MySQL without driver-specific generated columns or duplicated state flags.

## Decision

Management sessions remain in `api_tokens`. Agent, Client, and Server-node credentials are stored in the separate `service_tokens` table with an explicit `token_type`, optional principal bindings, a non-secret prefix for lookup and display, a one-way Token hash, and an authorization scope.

Credential state is derived from timestamps rather than persisted as another mutable status field:

- `revoked_at` records explicit revocation.
- `expires_at` records optional expiry.
- `last_used_at` records the latest successful use.

A service Token is usable only when it is not revoked and has not expired. This keeps the database as the single source of truth and avoids contradictory status and timestamp combinations.

Server-node authentication requires both transport identity and application credentials: mutually authenticated TLS establishes the node-to-node channel, and a scoped Server-node Token authorizes the requested TunnelMesh operation. Neither factor replaces the other.

Token plaintext is returned only at successful issuance. Idempotency records and replay responses omit the Token secret; a replay may return non-secret resource metadata but never reproduces credential material.

## Consequences

- Management and service credential policies can evolve independently.
- A leaked database does not directly reveal usable Token plaintext.
- Callers must retain newly issued Token plaintext because it cannot be recovered later.
- Authorization must validate Token type, principal binding, scope, revocation, and expiry for each service request.
- Schema v3 introduces `service_tokens`; automatic initialization upgrades schema v2 by applying the authoritative portable DDL before advancing `schema_meta`.

## Rejected alternatives

### One shared Token namespace

Keeping management sessions and service credentials in `api_tokens` was rejected because their principals, scopes, issuance paths, and revocation policies differ. A shared namespace would encourage ambiguous authorization branches and broaden the effect of future schema changes.

### Custom encryption

Implementing application-specific reversible encryption was rejected because it creates key-management, rotation, nonce, and misuse risks without a requirement to recover service credentials.

### Recoverable Token ciphertext

Storing encrypted Token plaintext for later display or idempotency replay was rejected because compromise of the ciphertext and decryption key would expose every credential. One-way hashes keep credential verification possible while limiting recovery risk.
