# PR: Improve GitHub growth presentation

## Title

`docs: improve github growth presentation`

## Target Branch

`main`

## Summary

This change improves the repository's first impression and contributor workflow without changing runtime code:

- Rewrites the English and Chinese README first screens around outcomes, trust signals, and quick navigation.
- Adds CI and license badges, a factual capability comparison, and a Mermaid architecture diagram.
- Adds an English five-minute quick start.
- Adds pull-request CI with isolated Go, frontend, and packaging jobs.
- Adds security policy, contribution guide, issue templates, and a pull-request template.
- Adds Agent and Client token environment mappings to the local Compose file.
- Fixes Python 3.9 compatibility in the documentation index generator.

## User Impact

Visitors can understand TunnelMesh, its architecture, and its deployment model within the first screen. New users can start a local Server, bootstrap the administrator, create scoped tokens, start an Agent, and test a local forward from one English guide. Contributors can find prerequisites, validation commands, and safe disclosure guidance without reading the full Chinese documentation.

## API / Schema / Configuration Impact

- No public API change.
- No database schema change.
- No application configuration model change.
- Local Compose now places the Agent service behind the `agent` profile and requires `TUNNELMESH_AGENT_TOKEN`.
- Local Compose now requires `TUNNELMESH_CLIENT_TOKEN` for the existing `client` profile.

## Security and Authorization Impact

- Security documentation defines supported versions, reporting expectations, and safe disclosure.
- Contribution and issue templates explicitly reject tokens, private keys, production DSNs, and unredacted logs.
- Local Compose passes service tokens from the host environment instead of baking them into images or files.
- The CI workflow has only `contents: read` permission.
- No secret values are introduced by this change.

## Test Evidence

Fresh verification on the final working tree:

```text
10 changed Markdown files checked; all local links exist

Ruby YAML parse of .github/workflows/ci.yml
PASS

python3 -m py_compile scripts/gen_doc_index.py
PASS

Ruby YAML parse of docker-compose.local.yml
PASS

go test ./... -count=1
all packages with tests passed

go test -race ./...
all packages with tests passed; internal/server completed in 352.366s

go vet ./...
PASS

cd web && npm test -- --run
30 files / 244 tests passed

cd web && npm run build
PASS

./scripts/verify-web-embed.sh
web/dist and internal/server/web_dist match

git diff --check
PASS

docker compose -f docker-compose.local.yml config
SKIPPED locally because Docker is not installed; the CI packaging job runs it

TUNNELMESH_AGENT_TOKEN=ci-placeholder TUNNELMESH_CLIENT_TOKEN=ci-placeholder
docker compose -f docker-compose.local.yml --profile agent --profile client config
SKIPPED locally because Docker is not installed; the CI packaging job runs it
```

## Release Steps

1. Review and merge this documentation, workflow, and Compose change to `main`.
2. Confirm the `ci` workflow passes on the pull request and the subsequent `main` push.
3. In the GitHub UI, set the repository description, topics, website, and social preview; these values are outside repository content.
4. Share the English README in relevant self-hosting, networking, and Go communities with context about the problem TunnelMesh solves.

## Rollback Steps

1. Revert the documentation, workflow, template, and Compose changes.
2. No database migration, binary rollback, or secret rotation is required.
3. Verify the README and documentation index render after the revert.

## Reviewer Focus

- English and Chinese README content remains semantically aligned.
- The comparison table describes common deployment patterns without disparaging other projects.
- The quick start starts only the Server before an Agent token exists and then activates the Agent profile.
- Compose requires Agent and Client service tokens but does not log or persist their values.
- CI runs the required Go, frontend, embed, and Compose validation commands.
- No generated web bundle, secret, token, private key, or production DSN is committed.

## Integration Status

- Documentation, governance files, workflow, and local Compose configuration are implemented.
- Local Go, frontend, embed, YAML, link, syntax, and whitespace validation passed.
- Docker Compose static validation and GitHub Actions execution are pending because Docker is unavailable locally and the workflow has not yet been pushed.
