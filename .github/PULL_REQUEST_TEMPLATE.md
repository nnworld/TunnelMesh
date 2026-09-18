## Summary

## Plan reference

Link `docs/superpowers/plans/...` for non-trivial changes, or state why a plan is not required.

## User impact

## API, schema, and configuration impact

## Documentation impact

State README, OpenAPI, deployment, operations, or user-guide updates, or why no documentation change is needed.

## Security impact

## Tests run

```sh
go test ./... -count=1
go test -race ./...
go vet ./...
cd web && npm test -- --run
cd web && npm run build
./scripts/verify-web-embed.sh
git diff --check
```

Mark the commands you actually ran and add any deployment-specific validation.

## Reviewer focus

Highlight compatibility, authorization, migration, concurrency, or rollout risks that need a second review.

## Rollback notes

<!-- Do not include tokens, private keys, production DSNs, or unredacted logs. -->
