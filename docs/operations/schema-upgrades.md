# Schema Upgrade Guide

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
