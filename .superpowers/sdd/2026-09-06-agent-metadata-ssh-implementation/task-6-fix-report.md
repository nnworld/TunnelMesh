# Task 6 Review Fix Report

## Corrections

- Added an injected `TCPBridgeAuditEvent` hook for `tcp_proxy.open` and `tcp_proxy.close`. Events contain Agent ID, target host/port, and authenticated user context from the request context; SSH payloads, commands, credentials, and private keys are never included.
- Added server bridge assertions for open/close audit actions and context propagation.
- Updated dynamic wildcard SSH examples to the implemented `<agent-id>-<a>-<b>-<c>-<d>-<port>.apps.example.com` format, while documenting explicit route URLs separately.
- Changed the SSH-like E2E response fixture to carry a nonzero exit-status byte and assert that it is preserved.

The E2E bridge test uses the existing `WSConn` seam because the repository intentionally does not select a concrete WebSocket library. This exercises the handler's binary-message bridge path; a network-level WebSocket acceptance test belongs with the deployment's chosen WebSocket adapter.

## Verification

- Focused bridge and E2E tests pass.
- Full Go tests, race tests, vet, diff check, web tests, and web build pass.
- Docker/Compose validation remains unavailable because the `docker` executable is not installed.
