# Task 1 fix round 1 review package

## Full fix-only diff

```diff
--- .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-after/docs/architecture/adr/0001-scoped-service-tokens.md	2026-09-06 14:15:01
+++ .superpowers/sdd/2026-09-06-scoped-token-auth-implementation/task-1-fix1-after/docs/architecture/adr/0001-scoped-service-tokens.md	2026-09-06 14:24:24
@@ -13,13 +13,13 @@
 
 Management sessions remain in `api_tokens`. Agent, Client, and Server-node credentials are stored in the separate `service_tokens` table with an explicit `token_type`, optional principal bindings, a non-secret prefix for lookup and display, a one-way Token hash, and an authorization scope.
 
-Credential state is derived from timestamps rather than persisted as another mutable status field:
+Credential lifecycle facts are stored as timestamps rather than persisted as another mutable status field. Effective availability is derived at authorization time from those timestamps together with the current availability of every bound principal and resource:
 
 - `revoked_at` records explicit revocation.
 - `expires_at` records optional expiry.
 - `last_used_at` records the latest successful use.
 
-A service Token is usable only when it is not revoked and has not expired. This keeps the database as the single source of truth and avoids contradictory status and timestamp combinations.
+A service Token is usable only when it is not revoked, has not expired, and every binding referenced by the Token remains valid and available. If its owner user, Agent, Server node, or another scoped resource is disabled, deleted, or otherwise unavailable, the Token is unavailable immediately. This derived decision is not stored in a separate `status` field, which keeps the authoritative resource state and credential timestamps as the single sources of truth and avoids contradictory persisted state.
 
 Server-node authentication requires both transport identity and application credentials: mutually authenticated TLS establishes the node-to-node channel, and a scoped Server-node Token authorizes the requested TunnelMesh operation. Neither factor replaces the other.
 
@@ -30,7 +30,7 @@
 - Management and service credential policies can evolve independently.
 - A leaked database does not directly reveal usable Token plaintext.
 - Callers must retain newly issued Token plaintext because it cannot be recovered later.
-- Authorization must validate Token type, principal binding, scope, revocation, and expiry for each service request.
+- Authorization must validate Token type, principal binding and bound-resource availability, scope, revocation, and expiry for each service request.
 - Schema v3 introduces `service_tokens`; automatic initialization upgrades schema v2 by applying the authoritative portable DDL before advancing `schema_meta`.
 
 ## Rejected alternatives
```
