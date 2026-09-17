# Security policy

## Supported versions

TunnelMesh provides security fixes for:

- the latest tagged release
- the current `main` branch, for fixes integrated into the next release

## Reporting a vulnerability

Please report security vulnerabilities privately to the repository maintainers through a GitHub
security advisory. If private advisories are unavailable, contact a maintainer directly.

Do not open a public issue for an exploitable vulnerability, exposed credential, or production incident.

We aim to acknowledge reports within two business days. Include reproduction details, affected
component, TunnelMesh version, and relevant logs with secrets removed.

## Scope

The following are in scope:

- `tunnelmesh-server`
- `tunnelmesh-agent`
- `tunnelmesh-client`
- the embedded Vue admin console
- the WebSocket and relay protocols
- documented deployment defaults
- security-relevant documentation

Issues in third-party dependencies should be reported to the dependency maintainer and then reported
to TunnelMesh if the dependency is used in an affected configuration.

## Safe disclosure

Please allow maintainers time to assess and fix a report before public disclosure. Avoid scanning or
testing against systems you do not own or administer.
