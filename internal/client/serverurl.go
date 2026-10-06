package client

import (
	"fmt"
	"net/url"
	"strings"
)

// HTTPURLFromWebSocket rewrites a Client or Agent WebSocket URL into an HTTP(S) URL
// for one path on the same origin.
//
// The Server serves the management API, the health endpoints and both WebSocket
// upgrades from a single listener, so the origin a tunnel connects to is also the
// origin that answers /health/ready and /api/v1. Deriving it instead of asking for a
// second configured URL removes a way for the two to disagree, which is why the CLI
// doctor and the tray share this one implementation.
func HTTPURLFromWebSocket(serverURL, path string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil {
		return "", fmt.Errorf("parse server URL: %w", err)
	}
	switch parsed.Scheme {
	case "ws":
		parsed.Scheme = "http"
	case "wss":
		parsed.Scheme = "https"
	default:
		return "", fmt.Errorf("server URL must use ws:// or wss://")
	}
	parsed.Path = path
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

// HealthReadyPath is the readiness endpoint both the CLI doctor and the tray check.
const HealthReadyPath = "/health/ready"

// ClientAgentsPath is the client-scoped Agent listing a client token may call.
const ClientAgentsPath = "/api/v1/client/agents"
