# Schema Upgrade Guide

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
