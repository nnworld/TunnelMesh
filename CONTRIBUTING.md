# Contributing to TunnelMesh

Thank you for improving TunnelMesh.

## Development prerequisites

- Go 1.23 or newer
- Node.js 22 or newer
- Docker Engine and Compose plugin for container validation

## Workflow

1. Create a feature branch from the latest `main`.
2. Write a failing test for the behavior you are changing.
3. Make the smallest implementation that passes the test.
4. Update user, deployment, operations, or API documentation when behavior changes.
5. Run the required validation commands.
6. Open a pull request using the repository template.

## Validation

Run all of the following for Go changes:

```sh
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
```

Run all of the following for frontend changes:

```sh
cd web
npm test -- --run
npm run build
cd ..
./scripts/verify-web-embed.sh
git diff --check
```

For deployment or container changes, also run the applicable Compose, Docker, systemd, launchd, or
Windows validation documented in `docs/deployment/`.

## Commit format

Use:

```text
<type>(<scope>): <subject>
```

Valid types include `feat`, `fix`, `refactor`, `perf`, `docs`, `test`, `chore`, `ci`, `style`, and
`revert`. The subject is imperative, no longer than 50 characters, and has no trailing period.

## Security

Do not commit tokens, private keys, passwords, production DSNs, `.env` files, or unredacted logs.
Report security issues privately as described in [SECURITY.md](SECURITY.md).
