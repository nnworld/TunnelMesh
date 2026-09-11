# Schema Upgrade Guide

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
