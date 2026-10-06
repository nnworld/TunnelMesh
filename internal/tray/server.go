package tray

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/client"
)

// Errors the settings interface has to distinguish, because each needs different
// advice: a wrong token is something the operator can fix in the form, while a Server
// that predates the client-scoped endpoint is not.
var (
	// ErrMissingToken means no token was supplied, so no request was attempted.
	ErrMissingToken = errors.New("tray: client token is required")
	// ErrInvalidToken means the Server rejected the supplied token.
	ErrInvalidToken = errors.New("tray: the server rejected the client token")
	// ErrServerTooOld means the Server cannot answer the client-scoped request, either
	// because it predates GET /api/v1/client/agents or because it was built without the
	// client token validator wired.
	ErrServerTooOld = errors.New("tray: the server does not provide the client agent endpoint")
)

// clientTokenValidatorUnavailable is the stable message the Server returns when the
// endpoint exists but its validator was never installed.
const clientTokenValidatorUnavailable = "client_token_validator_unavailable"

// maxAgentPages bounds the pagination walk. A Server that keeps handing back a fresh
// cursor must not be able to pin the settings window in an unbounded loop.
const maxAgentPages = 64

// defaultServerTimeout bounds every management API call. Without it a Server that
// accepts the connection and never answers would hang the picker indefinitely.
const defaultServerTimeout = 10 * time.Second

// AgentRef is one Agent a client token may point a tunnel at.
type AgentRef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Online bool   `json:"online"`
}

// DisplayName is what the picker shows: the name when the Server has one, otherwise
// the identity, so a row is never blank.
func (a AgentRef) DisplayName() string {
	if name := strings.TrimSpace(a.Name); name != "" {
		return name
	}
	return a.ID
}

// ServerClient calls the management API with a client service token.
//
// It exists so the settings window never holds the token in JavaScript: the webview
// asks the tray, and the tray talks to the Server. That also keeps one place to add
// the timeout and the error classification the interface needs.
type ServerClient struct{ httpClient *http.Client }

// NewServerClient builds a client. A non-positive timeout selects the default.
func NewServerClient(timeout time.Duration) *ServerClient {
	if timeout <= 0 {
		timeout = defaultServerTimeout
	}
	return &ServerClient{httpClient: &http.Client{Timeout: timeout}}
}

// CheckHealth verifies the configured Server answers its readiness endpoint.
func (c *ServerClient) CheckHealth(ctx context.Context, serverURL string) error {
	endpoint, err := client.HTTPURLFromWebSocket(serverURL, client.HealthReadyPath)
	if err != nil {
		return err
	}
	status, _, err := c.do(ctx, http.MethodGet, endpoint, "")
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("server health check: unexpected status %d", status)
	}
	return nil
}

// ListAgents returns every Agent the supplied client token may use, following the
// cursor to the end.
//
// Results are de-duplicated by identity. The Server pages by a composite cursor, and
// a repeated or overlapping cursor would otherwise surface the same Agent twice in a
// picker, or - if the cursor never advanced - loop forever.
func (c *ServerClient) ListAgents(ctx context.Context, serverURL, token string) ([]AgentRef, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrMissingToken
	}
	endpoint, err := client.HTTPURLFromWebSocket(serverURL, client.ClientAgentsPath)
	if err != nil {
		return nil, err
	}
	agents := make([]AgentRef, 0, 8)
	seenAgents := make(map[string]struct{}, 8)
	seenCursors := make(map[string]struct{}, maxAgentPages)
	cursor := ""
	for page := 0; page < maxAgentPages; page++ {
		target := endpoint
		if cursor != "" {
			target = endpoint + "?cursor=" + url.QueryEscape(cursor) + "&limit=500"
		} else {
			target = endpoint + "?limit=500"
		}
		status, body, err := c.do(ctx, http.MethodGet, target, token)
		if err != nil {
			return nil, err
		}
		if err := classifyAgentStatus(status, body); err != nil {
			return nil, err
		}
		var payload struct {
			Data struct {
				Items      []AgentRef `json:"items"`
				NextCursor string     `json:"nextCursor"`
				HasMore    bool       `json:"hasMore"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("decode agent list: %w", err)
		}
		for _, agent := range payload.Data.Items {
			id := strings.TrimSpace(agent.ID)
			if id == "" {
				continue
			}
			if _, exists := seenAgents[id]; exists {
				continue
			}
			seenAgents[id] = struct{}{}
			agent.ID = id
			agents = append(agents, agent)
		}
		if !payload.Data.HasMore || payload.Data.NextCursor == "" {
			return agents, nil
		}
		if _, repeated := seenCursors[payload.Data.NextCursor]; repeated {
			return agents, nil
		}
		seenCursors[payload.Data.NextCursor] = struct{}{}
		cursor = payload.Data.NextCursor
	}
	return agents, nil
}

// classifyAgentStatus maps the transport outcome onto the errors the interface can
// act on. A 503 is only treated as "Server too old" when it carries the validator
// message; any other 503 is an ordinary unavailable Server and should be retried.
func classifyAgentStatus(status int, body []byte) error {
	switch {
	case status >= 200 && status <= 299:
		return nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ErrInvalidToken
	case status == http.StatusNotFound:
		return ErrServerTooOld
	case status == http.StatusServiceUnavailable && strings.Contains(string(body), clientTokenValidatorUnavailable):
		return ErrServerTooOld
	default:
		return fmt.Errorf("list agents: unexpected status %d", status)
	}
}

// do performs one request and returns the status and a size-bounded body.
//
// The body is capped because it is only ever decoded as a small JSON envelope: reading
// an unbounded response from a misconfigured or hostile origin into memory would be a
// way to exhaust the tray process from outside.
func (c *ServerClient) do(ctx context.Context, method, endpoint, token string) (int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("build request: %w", err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("contact server: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return response.StatusCode, nil, fmt.Errorf("read response: %w", err)
	}
	return response.StatusCode, body, nil
}
