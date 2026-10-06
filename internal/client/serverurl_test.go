package client

import "testing"

func TestHTTPURLFromWebSocket(t *testing.T) {
	for _, test := range []struct {
		name      string
		serverURL string
		path      string
		want      string
	}{
		{"ws becomes http", "ws://server.example/ws/client", "/health/ready", "http://server.example/health/ready"},
		{"wss becomes https", "wss://server.example:8443/ws/client", "/api/v1/client/agents", "https://server.example:8443/api/v1/client/agents"},
		{"query and fragment dropped", "wss://server.example/ws/client?a=b#frag", "/health/ready", "https://server.example/health/ready"},
		{"surrounding space tolerated", "  wss://server.example/ws/client  ", "/health/ready", "https://server.example/health/ready"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := HTTPURLFromWebSocket(test.serverURL, test.path)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("HTTPURLFromWebSocket() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestHTTPURLFromWebSocketRejectsNonWebSocketSchemes keeps a mistyped configuration
// from silently producing an unusable URL.
func TestHTTPURLFromWebSocketRejectsNonWebSocketSchemes(t *testing.T) {
	for _, serverURL := range []string{"", "https://server.example/ws/client", "server.example/ws/client", "://bad"} {
		if _, err := HTTPURLFromWebSocket(serverURL, "/health/ready"); err == nil {
			t.Fatalf("HTTPURLFromWebSocket(%q) accepted a non-WebSocket URL", serverURL)
		}
	}
}
