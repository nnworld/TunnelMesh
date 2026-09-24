# Migration comment splitter hotfix

## Title

`fix(storage): parse SQL comments safely`

## Target branch

`main`

## Summary

Production failed while applying `v0014_to_v0015` with MySQL `Error 1064`. The migration comment contains `insert; every later write...`; `applySchemaStatements` used `strings.Split(script, ";")`, so the semicolon inside the comment split the script and the next fragment began with comment prose. MySQL rejected that fragment as SQL before reaching the first `ALTER TABLE`, leaving `schema_meta` at 14 and the Server unable to start.

The published migration is intentionally unchanged. This is an executor defect, not a schema defect.

## User impact

- Restores the v14 → v15 upgrade path.
- The failed production database can retry the migration with the fixed Server binary.
- Future migration comments may contain semicolons without breaking startup.

## API, schema, and configuration impact

None. No API, schema version, migration script, or configuration changes.

## Security impact

None. The parser only changes statement boundary detection; it does not alter authorization, data access, or logging.

## Changes

- `internal/storage/db.go`: replace naive semicolon splitting with `splitSQLStatements`, which tracks line comments, block comments, strings, and quoted identifiers.
- `internal/storage/db_test.go`: regression tests for comment semicolons, the published v14 → v15 script, and quoted semicolons.
- `docs/operations/troubleshooting.md`: incident symptoms, root cause, stop-loss, and retry guidance.

## Test evidence

```
go test ./internal/storage/ -run 'TestApplySchemaStatements|TestSplitSQLStatements' -count=1
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
```

## Release steps

1. Build and deploy the fixed Server binary.
2. Confirm `schema_meta.version` is still 14.
3. Start the Server with `storage.auto_init: true`; the migration retries safely.
4. Confirm the version is 15 and both `connection_epoch` columns are `BIGINT`.

## Rollback steps

If startup still fails, stop the fixed Server and redeploy the previous binary. The database remains at version 14 until the migration completes.

## Reviewer focus

- Confirm `splitSQLStatements` handles doubled quotes and escaped quote characters correctly.
- Confirm the v14 → v15 migration now parses to exactly the two intended `ALTER TABLE` statements.
- Confirm no published migration file was modified.
