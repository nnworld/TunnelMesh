# Schema Upgrade Guide

## v14 to v15

Schema v15 widens the connection fencing token from a 32-bit `INTEGER` to a
`BIGINT` on both lease tables. It changes no table, no column name, no index,
and no data semantics — only the storage width of one column per table.

| Table | Column | Before | After |
| --- | --- | --- | --- |
| `client_connection_leases` | `connection_epoch` | `INTEGER NOT NULL` | `BIGINT NOT NULL` |
| `agent_connection_leases` | `connection_epoch` | `INTEGER NOT NULL` in `migrations/ddl.sql`, `BIGINT NOT NULL` in `v0006_to_v0007` | `BIGINT NOT NULL` |

Why this is not a cosmetic change: `connection_epoch` is the fencing token that
rejects stale connection generations. `internal/server/ws_client.go` derives the
Client token from eight random bytes, so it routinely exceeds the signed 32-bit
range. On MySQL a 32-bit `INTEGER` column **clamps** such a value to
`2147483647` at insert time. Every later write filters on
`WHERE connection_id=? AND connection_epoch=?` using the true in-memory token,
so it matched zero rows and returned `sql.ErrNoRows`:

- `Renew` never extended `expires_at`, so the lease lapsed after
  `DefaultClientConnectionLeaseTTL` (90s) while the WebSocket stayed open.
- `ClientObservabilityService.Heartbeat` returned early on that error, so
  `TouchInstance` never ran; `client_instance_metadata.expires_at` lapsed after
  `DefaultClientMetadataTTL` (5m) and `ClientMetadataSweeper` set `stale=1`.
- `newClientView` therefore reported `status=stale`, `activeConnections=0`,
  `activeStreams=0`, and no server node, and the console summary cards — which
  derive from those rows — showed zeros for a client that was actively serving
  traffic.

The detail drawer still listed the lease rows, so the page contradicted itself:
"0 active connections" beside four connections, all showing epoch `2147483647`.
That identical epoch across unrelated connections is the diagnostic signature of
this defect.

SQLite was never affected: its `INTEGER` is already a signed 64-bit value. The
defect survived because the MySQL-backed contract tests are gated behind
`TUNNELMESH_TEST_MYSQL_DSN` and did not run in CI. That gap is closed by the
`mysql56` job, which runs the same contract functions against a real MySQL 5.6
service container and is a required status check on `main`; see
[测试与验证](../development/testing.md). `agent_connection_leases` is
widened in the same step to remove the drift between `migrations/ddl.sql` and
`v0006_to_v0007/mysql.sql`; agent epochs are small counters today, but a fresh
MySQL install and an upgraded one must agree on the authoritative schema.

Before upgrading:

1. **Back up the database and verify the backup can be restored.**
2. Confirm `schema_meta.version=14`.
3. Confirm `migrations/incremental/v0014_to_v0015/mysql.sql` and `sqlite.sql` are
   present, along with every intermediate version from the current one.
4. Confirm the database account can `ALTER TABLE` both lease tables and update
   `schema_meta`.
5. No secret or environment change is required for this version.

Start the new Server with `storage.auto_init: true`. The migration applies the
driver-specific statements and advances `schema_meta.version` to `15` only after
they succeed. On SQLite the step is structurally inert and executes a single
idempotent `UPDATE schema_meta SET version=version WHERE id=1` no-op, because
SQLite cannot alter a column type in place and does not need to.

Expected lock impact: `client_connection_leases` and `agent_connection_leases`
hold one row per live physical WebSocket, so both are small (typically tens to
hundreds of rows). Widening `INT` to `BIGINT` requires a table rebuild — MySQL
8.0 can use `ALGORITHM=INPLACE`, MySQL 5.6/5.7 rebuild with concurrent DML
allowed. Budget seconds, not minutes, and run it in a normal maintenance window.

Verify:

```sql
SELECT version FROM schema_meta WHERE id=1;
-- expected: 15

-- MySQL
SHOW COLUMNS FROM client_connection_leases LIKE 'connection_epoch';
SHOW COLUMNS FROM agent_connection_leases LIKE 'connection_epoch';
-- expected Type: bigint(20)

-- SQLite (declared type is unchanged; INTEGER already stores 64 bits)
PRAGMA table_info(client_connection_leases);
```

Both MySQL columns must report `bigint`. A fresh deployment seeded from
`migrations/ddl.sql` produces the same types at version `15`.

### Self-healing after the upgrade

Rows written before the upgrade still hold the clamped `2147483647` value; the
migration widens the column but deliberately does not rewrite those rows, since
they are already-expired leases and there is no reliable way to reconstruct the
original token. Live connections repair themselves: `ClientConnectionLeaseController.Heartbeat`
treats a missed `Renew` as "the stored token is not authoritative" and
re-registers the lease with the true in-memory epoch. Connected clients
therefore recover on their next heartbeat (default 30s) **without reconnecting**,
and the console numbers return with them.

Because of that self-heal, upgrading the Server is sufficient. **Agent and
Client binaries do not need to be upgraded or restarted** — no protocol frame,
capability, or API contract changed in this release.

### Compatibility and rollback

Widening a column is backward compatible for readers: a v14 binary reading a v15
`BIGINT` column still receives an `int64`. The version gate is still strict, so
a v14 Server refuses to open a v15 database with `schema version mismatch:
database has version 15, application requires version 14`.

Roll back by **reverting the binary and keeping the v15 schema**. Do not narrow
the column back to `INTEGER`: that reintroduces the clamping and would silently
truncate any token already stored above the 32-bit range. If the exact v14
structure is genuinely required, restore the pre-upgrade backup instead of
writing reverse DDL, and never decrement `schema_meta.version` by hand.

Five-minute containment: if a new Server fails to start after this migration,
redeploy the previous binary. The v15 schema is readable by v14, so no database
rollback is needed to restore service.

## v13 to v14

Schema v14 is the enterprise identity foundation: OIDC single sign-on, TOTP MFA
with recovery codes, revocable trusted devices, and the operator-editable auth
policy. It adds two columns to `users` and eight new tables. Every statement is
additive — no existing column is modified, dropped, or rewritten — so the change
is expand-only.

New columns on `users`:

| Column | Type | Default | Purpose |
| --- | --- | --- | --- |
| `auth_source` | `VARCHAR(32) NOT NULL` | `'local'` | Where the primary credential comes from: `local`, `oidc`, or `mixed` |
| `mfa_required` | `INTEGER NOT NULL` | `0` | Per-account second-factor requirement; it is a floor that overrides a globally `disabled` policy |

New tables:

| Table | Primary key | Notable columns and constraints |
| --- | --- | --- |
| `auth_settings` | `id INTEGER` (single row, `id=1`) | `mfa_mode VARCHAR(16) DEFAULT 'disabled'`, `device_trust_enabled INTEGER DEFAULT 1`, `device_trust_ttl_seconds INTEGER DEFAULT 2592000`, `allow_trusted_device_bypass INTEGER DEFAULT 1`, `max_trusted_devices INTEGER DEFAULT 10`, `session_token_ttl_seconds INTEGER DEFAULT 0`, `updated_at VARCHAR(32) NOT NULL` |
| `oidc_providers` | `id VARBINARY(255)` | `name VARCHAR(191) NOT NULL UNIQUE`, `issuer VARCHAR(512)`, `client_id VARCHAR(255)`, `client_secret_ciphertext/nonce/key_id/version` (nullable, sealed), `scopes TEXT`, `redirect_uri VARCHAR(512)`, optional endpoint overrides, `id_token_algs VARCHAR(255) DEFAULT 'RS256'`, `username_claim VARCHAR(64) DEFAULT 'preferred_username'`, `role_mappings TEXT`, `default_role VARCHAR(32) DEFAULT 'user'`, `authoritative_roles`, `auto_create_users`, `fetch_userinfo`, `public_listed`, `enabled`, timestamps |
| `user_identities` | `id VARBINARY(255)` | `UNIQUE(provider_id, subject)`, `user_id`, `email`, `display_name`, `created_at`, `last_login_at` |
| `user_mfa` | `user_id VARBINARY(255)` | `secret_ciphertext/nonce/key_id/version NOT NULL` (sealed TOTP secret), `status VARCHAR(16) DEFAULT 'pending'`, `enrolled_at`, `enabled_at`, `last_used_at`, `last_used_step INTEGER DEFAULT -1` (TOTP replay guard) |
| `user_recovery_codes` | `id VARBINARY(255)` | `code_hash VARBINARY(64) NOT NULL UNIQUE` (SHA-256, never plaintext), `used_at`, `created_at` |
| `user_devices` | `id VARBINARY(255)` | `token_hash VARBINARY(64) NOT NULL UNIQUE` (SHA-256, never plaintext), `name`, `user_agent`, `ip`, `trusted_at`, `expires_at`, `last_seen_at`, `revoked_at` |
| `auth_challenges` | `id VARBINARY(255)` | `kind VARCHAR(32)` (`login_mfa`, `oidc_state`, `login_ticket`), `payload_ciphertext/nonce/key_id/version NOT NULL` (sealed), `attempts`, `max_attempts`, `consumed_at`, `expires_at` |
| `auth_login_attempts` | `bucket_key VARBINARY(255)` | `attempts INTEGER DEFAULT 0`, `window_start`, `blocked_until`, `updated_at`; `bucket_key` is the hex SHA-256 digest of the lowercased username joined with the trimmed client IP by a single separator character, so the table stores no username or IP |

New indexes:

- `idx_oidc_providers_enabled` on `oidc_providers(enabled, public_listed, id)`
- `idx_user_identities_user` on `user_identities(user_id, id)`
- `idx_user_recovery_codes_user` on `user_recovery_codes(user_id, id)`
- `idx_user_devices_user` on `user_devices(user_id, revoked_at, expires_at)`
- `idx_user_devices_expires` on `user_devices(expires_at)`
- `idx_auth_challenges_expires` on `auth_challenges(expires_at)`
- `idx_auth_challenges_kind_user` on `auth_challenges(kind, user_id, id)`
- `idx_auth_login_attempts_blocked` on `auth_login_attempts(blocked_until)`

As in v12, indexed timestamp columns are `VARCHAR(32)` rather than `TEXT` so
MySQL can build the index without an explicit key length (`Error 1170`).

Before upgrading:

1. **Back up the database and verify the backup can be restored.** This upgrade
   is not reversible by application rollback; see "Roll back" below.
2. Confirm `schema_meta.version=13`.
3. Confirm both `migrations/incremental/v0013_to_v0014/mysql.sql` and
   `migrations/incremental/v0013_to_v0014/sqlite.sql` are present, and that every
   intermediate version from the current one is present too. Cross-version
   upgrades must run one adjacent step at a time.
4. Confirm the database account can `ALTER TABLE users`, create tables and
   indexes, and update `schema_meta`.
5. Inject `TUNNELMESH_TOKEN_ENCRYPTION_KEY` (base64 or hex, 16/24/32 bytes) and,
   when rotating keys, `TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID` on **every** Server
   node before starting v14. Without it the Server still starts and password
   login still works, but MFA enrollment, OIDC provider creation, and every
   OIDC login fail closed with `503` and `data.error=secret_storage_unavailable`.
6. Plan for a full-fleet restart rather than a rolling upgrade. See the
   compatibility note below.

Start the new Server with schema initialization enabled (`storage.auto_init: true`).
The migration applies the driver-specific statements and advances
`schema_meta.version` to `14` only after they succeed. MySQL DDL commits
implicitly, so an interrupted upgrade must be inspected before retrying; the
runner tolerates duplicate objects from v6 onward, which makes a partially
applied v14 migration retry-safe. Never advance or decrement
`schema_meta.version` by hand.

Expected lock impact: the two `ALTER TABLE users ADD COLUMN` statements add
trailing `NOT NULL DEFAULT` columns, which MySQL 8.0 applies with the `INSTANT`
algorithm and MySQL 5.6/5.7 with `INPLACE` (table rebuild, concurrent DML
allowed), so `users` stays usable. All eight new tables are empty on creation
and their indexes are built inline, which costs no locking on existing data.
SQLite `ADD COLUMN` rewrites only the stored schema, not the rows, and the
`CREATE TABLE`/`CREATE INDEX IF NOT EXISTS` statements run inside the migration
transaction; the pause is negligible. On a large `users` table under MySQL 5.7
budget for the `INPLACE` rebuild in the same maintenance window used for
adjacent releases.

Verify:

```sql
SELECT version FROM schema_meta WHERE id=1;
-- expected: 14

-- MySQL
SHOW COLUMNS FROM users LIKE 'auth_source';
SHOW COLUMNS FROM users LIKE 'mfa_required';
SHOW TABLES LIKE 'auth_%';
SHOW TABLES LIKE 'user_%';
SHOW TABLES LIKE 'oidc_providers';
SHOW INDEX FROM user_devices;

-- SQLite
PRAGMA table_info(users);
SELECT name FROM sqlite_master WHERE type='table' AND name IN
  ('auth_settings','oidc_providers','user_identities','user_mfa',
   'user_recovery_codes','user_devices','auth_challenges','auth_login_attempts');
SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='user_devices';
```

All eight tables must be present, `users` must carry both new columns, and
`SELECT COUNT(*) FROM auth_settings` must be `0` until the policy is first read,
after which exactly one row with `id=1` exists. A fresh deployment seeded from
`migrations/ddl.sql` produces the same objects at version `14`.

### Compatibility: a v13 binary cannot open a v14 database

The migration statements are additive, but the version gate is strict. A v13
Server refuses a v14 database before serving any traffic: with
`storage.auto_init: false` the schema check requires an exact version match and
fails with `schema version mismatch: database has version 14, application
requires version 13`; with `auto_init: true` the loader rejects any version above
its own `SchemaVersion` with the same error. The gate is symmetric: a v14 Server
against a v13 database fails with `database has version 13, application requires
version 14` when `auto_init` is off, and with `auto_init` on it runs
`v0013_to_v0014` — or, if that adjacent script is missing from the embedded
migrations, reports `missing adjacent migration v0013_to_v0014` and refuses to
start. Independent of the version number, startup also verifies that all eight
new tables exist and fails with `schema is missing required table <name>` if any
is absent.

Consequences for the release:

- There is **no rolling upgrade window** for this release. Take the fleet down,
  migrate, and start every node on v14. Do not leave a v13 node pointed at the
  migrated database.
- Application-level downgrade therefore requires **restoring the pre-upgrade
  backup**, not reverse DDL. Do not hand-write `DROP TABLE` statements or
  decrement `schema_meta.version`: a downgrade that keeps v14 rows would leave a
  v13 Server issuing tokens for accounts whose `mfa_required` and `auth_source`
  columns it cannot read.
- Keep `TUNNELMESH_TOKEN_ENCRYPTION_KEY` stable across the whole window.
  Restoring a backup taken before the key existed leaves sealed secrets that no
  node can open.

### Feature-level rollback without a deploy

If SSO or MFA misbehaves in production, disable the feature instead of the
release. Both switches live in the database and take effect on the next login,
with no restart, no schema change, and no binary rollback:

1. Turn off second-factor enforcement globally:

   ```sql
   UPDATE auth_settings SET mfa_mode='disabled' WHERE id=1;
   ```

   Or call `PUT /api/v1/auth/policy` with `{"mfaMode": "disabled", ...}` as an
   administrator. Per-account `users.mfa_required=1` overrides remain in force by
   design — they are a floor, not a suggestion. Clear them with
   `PATCH /api/v1/users/{id}` (`{"mfaRequired": false}`) if an account must be
   exempted too.

2. Disable every OIDC provider so the login page stops offering SSO and the
   callback stops resolving:

   ```sql
   UPDATE oidc_providers SET enabled=0;
   ```

   Or `PATCH /api/v1/sso/providers/{id}` with `{"enabled": false}` per provider.
   A disabled provider answers `404 oidc_provider_not_found` on the authorize and
   callback paths. Accounts whose only credential is an OIDC identity cannot log
   in while their provider is disabled; re-enable the provider rather than
   deleting it.

3. Revoke trusted devices if the bypass itself is suspect:

   ```sql
   UPDATE auth_settings SET allow_trusted_device_bypass=0, device_trust_enabled=0 WHERE id=1;
   ```

   Disabling device trust invalidates every bypass immediately, without waiting
   for cookies to expire.

Both `PUT /api/v1/auth/policy` and the provider update write an audit row, so
the rollback is attributable. Restore by reversing the same two settings; no
data is lost because disabling never deletes enrollment, recovery-code, or
identity rows.

## v12 to v13

Schema v13 adds four nullable columns to `credentials` so a credential can
carry an encrypted authentication secret (SSH password, or SSH private key plus
passphrase) for browser-side auto-authentication:
`secret_ciphertext TEXT`, `secret_nonce TEXT`, `secret_key_id VARCHAR(64)`,
`secret_version INTEGER`. No existing table, column, or index is modified and no
data is rewritten, so the change is expand-only and safe for a rolling upgrade.

Before upgrading:

1. Back up the database and verify the backup can be restored.
2. Confirm `schema_meta.version=12`.
3. Confirm both `migrations/incremental/v0012_to_v0013/mysql.sql` and
   `migrations/incremental/v0012_to_v0013/sqlite.sql` are present.
4. Confirm the database account can `ALTER TABLE credentials` and update
   `schema_meta`.
5. If auto-authentication must work immediately after the upgrade, inject
   `TUNNELMESH_TOKEN_ENCRYPTION_KEY` (base64 or hex, 16/24/32 bytes) and
   optionally `TUNNELMESH_TOKEN_ENCRYPTION_KEY_ID`. Without the key the Server
   still starts; creating a credential with a secret then fails fast with `503`
   instead of storing plaintext.

Start the new Server with schema initialization enabled. The migration adds the
columns and advances `schema_meta.version` to `13` only after the
driver-specific statements succeed. MySQL DDL commits implicitly, so an
interrupted upgrade must be inspected before retrying; the migration is
idempotent-safe only through the runner's version check, never by manually
advancing or decrementing `schema_meta.version`.

Expected lock impact: MySQL 8.0 adds trailing nullable columns with the
`INSTANT` algorithm and MySQL 5.6/5.7 with `INPLACE` (table rebuild but
concurrent DML allowed), so `credentials` stays usable during the upgrade.
SQLite `ALTER TABLE ... ADD COLUMN` only rewrites the stored schema, not the
rows, and runs inside the migration transaction; the pause is negligible.

Verify:

```sql
SELECT version FROM schema_meta WHERE id=1;
-- MySQL
SHOW COLUMNS FROM credentials LIKE 'secret_%';
-- SQLite
PRAGMA table_info(credentials);
```

The expected version is `13` and all four `secret_*` columns must be present and
nullable. Existing credential rows keep `NULL` secrets, which the UI reports as
"secret not stored" and treats as manual authentication.

Roll back the application to v12 while retaining the v13 columns. A v12 binary
ignores the new columns, so no reverse DDL is required; stored secrets simply
become invisible and browser SSH/SFTP falls back to the manual password dialog.
Rolling back loses the ability to decrypt secrets only if the encryption key is
also removed, so keep `TUNNELMESH_TOKEN_ENCRYPTION_KEY` stable across the
rollback window. If exact schema restoration is mandatory, restore the
pre-upgrade backup instead of issuing reverse DDL.

## v11 to v12

Schema v12 adds the `credentials`, `remote_servers`, and `webssh_sessions`
tables used by the admin remote-server and browser SSH/SFTP features. The
change is expand-only: existing tables and columns are not modified.

Before upgrading:

1. Back up the database and verify the backup can be restored.
2. Confirm `schema_meta.version=11`.
3. Confirm both `migrations/incremental/v0011_to_v0012/mysql.sql` and
   `migrations/incremental/v0011_to_v0012/sqlite.sql` are present.
4. Confirm the database account can create tables and indexes and update
   `schema_meta`.
5. Confirm `server.webssh.enabled` remains disabled until the new Server is
   ready; the management tables can be created without enabling the broker.

Start the new Server with schema initialization enabled. The migration creates
the new objects and advances `schema_meta.version` to `12` only after the
driver-specific statements succeed. MySQL DDL can commit implicitly; if an
upgrade is interrupted, inspect the listed objects and version before retrying.
Do not manually advance or decrement the version.

The indexed timestamp columns in these tables use `VARCHAR(32)` rather than
`TEXT`. This is intentional: MySQL rejects an index on a `TEXT` column without
a key length and reports `Error 1170`. The full DDL and both incremental
migration scripts must keep these definitions identical.

For MySQL, the incremental migration also normalizes these columns with
`ALTER TABLE ... MODIFY COLUMN` after each `CREATE TABLE IF NOT EXISTS`.
If a previous attempt created one of the new tables with `TEXT` columns before
failing, a retry with the fixed Server repairs the column types and continues;
already-created duplicate indexes are tolerated by the migration runner. Do not
manually drop the partially created tables or advance `schema_meta.version`.

Verify:

```sql
SELECT version FROM schema_meta WHERE id=1;
SELECT COUNT(*) FROM credentials;
SELECT COUNT(*) FROM remote_servers;
SELECT COUNT(*) FROM webssh_sessions;
-- SQLite
SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN
  ('credentials', 'remote_servers', 'webssh_sessions');
-- MySQL
SHOW TABLES LIKE 'credentials';
SHOW TABLES LIKE 'remote_servers';
SHOW TABLES LIKE 'webssh_sessions';
```

The expected version is `12`; the row counts may be zero on a fresh deployment.

Roll back the application to v11 while retaining the v12 objects. v11 ignores
the new tables, so no reverse DDL is required. Disable `server.webssh.enabled`
before rollback if any browser session is active. If exact schema restoration
is mandatory, restore the pre-upgrade backup instead of issuing reverse DDL.

## v9 to v10

Schema v10 adds the nullable `agent_policies.deleted_at` column. `NULL` means
the policy is active; a non-null timestamp means it is logically deleted and
must not participate in stream authorization.

Before upgrading:

1. Back up the database and verify the backup can be restored.
2. Confirm `schema_meta.version=9`.
3. Confirm both `migrations/incremental/v0009_to_v0010/mysql.sql` and
   `migrations/incremental/v0009_to_v0010/sqlite.sql` are present.
4. Confirm the database account can execute `ALTER TABLE` and update
   `schema_meta`.

Start the new Server with schema initialization enabled. The migration adds
the column and advances `schema_meta.version` to `10`. MySQL 5.6 supports this
nullable `TEXT` column without changing server parameters. `ALTER TABLE` may
briefly lock `agent_policies`; expected duration depends on table size, so run
the upgrade in the maintenance window used for adjacent releases.

Verify:

```sql
SELECT version FROM schema_meta WHERE id=1;
SELECT COUNT(*) FROM pragma_table_info('agent_policies') WHERE name='deleted_at'; -- SQLite
SHOW COLUMNS FROM agent_policies LIKE 'deleted_at'; -- MySQL
```

If MySQL reports that the column already exists after an interrupted upgrade,
the retry path tolerates that duplicate-column result and continues. If another
SQL error occurs, inspect the statement and database state before retrying;
the version is not advanced before success.

Roll back the application to v9 while retaining the v10 column. Do not drop
the column or change `schema_meta.version` manually. Because v9 does not
understand logical deletion, any policy deleted under v10 would be treated as
active after application rollback; restore required policies in the v10 UI
first or restore the pre-upgrade backup.

## v8 to v9

Schema v9 adds the forward-compatible `authorization_revision` table. The table
has one row with `id=1` and is used by Server nodes to invalidate stream
authorization caches after token, user, Agent, or policy changes.

Before upgrading:

1. Back up the database and verify the backup can be restored.
2. Confirm `schema_meta.version=8`.
3. Confirm both `migrations/incremental/v0008_to_v0009/mysql.sql` and
   `migrations/incremental/v0008_to_v0009/sqlite.sql` are present.
4. Confirm the database account can create a table and update `schema_meta`.

Apply the upgrade by starting the new Server with schema initialization
enabled. The migration creates `authorization_revision`, inserts the fixed
initial row, and advances `schema_meta.version` to 9 only after the statements
succeed. MySQL DDL may commit implicitly; if execution is interrupted, retry
after checking that no partial object is inconsistent.

Verify:

```sql
SELECT version FROM schema_meta WHERE id=1;
SELECT revision FROM authorization_revision WHERE id=1;
```

The expected version is `9` and the initial revision is `1`.

Roll back the application to v8 while retaining the v9 table. v8 ignores the
table and does not require its removal. If exact schema restoration is
mandatory, restore the pre-upgrade backup instead of issuing reverse DDL.

## General Rules

- `migrations/ddl.sql` describes the current full schema for empty databases.
- `migrations/incremental/` is the immutable version-to-version upgrade path.
- Upgrade multi-version databases one adjacent version at a time.
- PATCH releases do not change schema; MINOR releases only add compatible
  structures unless an approved MAJOR migration is in progress.
