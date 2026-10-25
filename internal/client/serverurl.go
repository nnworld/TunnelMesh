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

// AgentWebSocketURLFromClient rewrites the configured client WebSocket endpoint
// to the sibling Agent endpoint used by an embedded Client Agent role.
func AgentWebSocketURLFromClient(serverURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil || (parsed.Scheme != "ws" && parsed.Scheme != "wss") || parsed.Host == "" {
		return "", fmt.Errorf("client server URL must be an absolute ws:// or wss:// URL")
	}
	if strings.HasSuffix(parsed.Path, "/ws/client") {
		parsed.Path = strings.TrimSuffix(parsed.Path, "/ws/client") + "/ws/agent"
	} else {
		parsed.Path = "/ws/agent"
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

// HealthReadyPath is the readiness endpoint both the CLI doctor and the tray check.
const HealthReadyPath = "/health/ready"

// ClientAgentsPath is the client-scoped Agent listing a client token may call.
const ClientAgentsPath = "/api/v1/client/agents"

// AgentSelfPath returns the identity bound to an Agent service token.
const AgentSelfPath = "/api/v1/agent/self"
