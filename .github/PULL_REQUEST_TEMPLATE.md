## Summary

## User impact

## API, schema, and configuration impact

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

## Rollback notes

<!-- Do not include tokens, private keys, production DSNs, or unredacted logs. -->
