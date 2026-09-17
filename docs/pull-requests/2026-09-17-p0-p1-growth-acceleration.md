# P0/P1 growth acceleration

## Title

docs: accelerate repository discovery

## Target branch

`main`

## Summary

This change repairs clean-checkout CI, adds English UI visual proof, creates concise English guides, records GitHub metadata, and adds a technical article and distribution plan.

## User impact

- New visitors can see the real English admin dashboard, Agent/token views, WebSSH terminal, and SFTP browser in the README.
- English-speaking operators have concise entry-point guides for production deployment, security, Agent, Client, and Server administration.
- Clean-checkout CI now builds the embedded web bundle before compiling Go and validates Compose with non-secret placeholder tokens.
- The repository has an auditable metadata plan and a focused three-day distribution plan.

## API, schema, and configuration impact

- API contract: unchanged.
- Database schema: unchanged.
- Runtime configuration: unchanged.
- CI configuration: changed to build `web` before Go tests and inject Compose placeholder tokens.

## Security impact

- No real token, password, private key, production DSN, private host, or sensitive command output is committed.
- Token management shows metadata only; secrets are not displayed.
- WebSSH and SFTP screenshots use the repository's throwaway E2E SSH host.
- The social preview is composed from the English dashboard screenshot and contains no credentials.

## Test evidence

- `go test ./... -count=1`: PASS.
- `go test -race ./...`: PASS (`internal/server` took 356.216s).
- `go vet ./...`: PASS.
- `cd web && npm test -- --run`: PASS, 30 files and 244 tests.
- `cd web && npm run build`: PASS.
- `./scripts/verify-web-embed.sh`: PASS.
- `node test/e2e/webssh/run.mjs`: PASS; ZMODEM transfer checks were skipped because `lrzsz` is not installed locally.
- English asset capture against the real E2E stack: PASS.
- `ruby -e 'require "yaml"; YAML.load_file(".github/workflows/ci.yml")'`: PASS.
- `python3 scripts/gen_doc_index.py`: PASS.
- `git diff --check`: PASS.
- Docker is unavailable locally, so Compose `config` validation is deferred to the `packaging` CI job.

## Release steps

1. Merge the branch into `main`.
2. Confirm CI passes, including the `packaging` Compose job.
3. Publish the repository metadata from `docs/community/github-metadata.md` after explicit owner confirmation.
4. No application release tag is required because the runtime binaries and schema are unchanged.

## Rollback steps

1. Revert the presentation, documentation, asset, and CI commit together.
2. No database or runtime rollback is required.
3. Restore GitHub About, topics, website, and social preview manually from the previous repository settings.

## Reviewer focus

- Confirm all screenshots use the English UI and contain no secrets.
- Confirm README image paths and English documentation links are relative and valid.
- Confirm CI builds the web bundle before Go compilation.
- Confirm the English guides defer to the authoritative Chinese documentation when they summarize behavior.

## Integration status

Local validation is complete except for Docker Compose, which is unavailable locally and delegated to CI. GitHub metadata application remains pending explicit owner confirmation.

