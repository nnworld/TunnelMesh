package tray

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/client"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

// Check statuses. A warning never invalidates a report: it marks a configuration that
// is correct but will not carry traffic until something outside the tray changes, such
// as an Agent coming back online.
const (
	StatusPassed  = "passed"
	StatusFailed  = "failed"
	StatusWarning = "warning"
	StatusSkipped = "skipped"
)

// Check identifiers are stable machine keys.
//
// The interface localizes the label from the identifier and renders Message as detail.
// Keeping English text out of the report is what lets one Go binary serve both locales
// without a translation table on the Go side.
const (
	CheckServerURLFormat   = "serverUrl.format"
	CheckServerReachable   = "serverUrl.reachable"
	CheckTokenPresent      = "token.present"
	CheckTokenValid        = "token.valid"
	CheckAgentsAvailable   = "agents.available"
	CheckTunnelsPresent    = "tunnels.present"
	CheckTunnelProtocol    = "tunnel.protocol"
	CheckTunnelListen      = "tunnel.listen"
	CheckTunnelAgent       = "tunnel.agent"
	CheckTunnelTarget      = "tunnel.target"
	CheckTunnelCredentials = "tunnel.credentials"
	CheckTunnelAuthURL     = "tunnel.authUrl"
	CheckConfigValid       = "config.valid"
)

// CheckResult is one line of a validation report.
type CheckResult struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	// Tunnel and Index attribute a check to one configured tunnel. Index is 1-based to
	// match how the interface numbers the cards, and zero means the check is global.
	Tunnel string `json:"tunnel,omitempty"`
	Index  int    `json:"index,omitempty"`
}

// ValidationReport is the result of one 检测 run.
type ValidationReport struct {
	Valid     bool          `json:"valid"`
	CheckedAt time.Time     `json:"checkedAt"`
	Checks    []CheckResult `json:"checks"`
	// Agents carries the picker data the run already fetched, so validating refreshes
	// the Agent dropdown instead of forcing a second round trip.
	Agents []AgentRef `json:"agents,omitempty"`
}

// OK reports whether no check failed.
func (r ValidationReport) OK() bool {
	for _, check := range r.Checks {
		if check.Status == StatusFailed {
			return false
		}
	}
	return true
}

// find returns the check with the given identifier.
func (r ValidationReport) find(id string) (CheckResult, bool) {
	for _, check := range r.Checks {
		if check.ID == id {
			return check, true
		}
	}
	return CheckResult{}, false
}

// Validator answers "is this configuration usable" for the routing tab.
type Validator struct {
	server *ServerClient
	store  *ConfigStore
	// bindProbes decides whether listen addresses are test-bound. It has to be off
	// while the tray hosts the client, because the tray itself holds every listener and
	// a probe would report the operator's own tunnels as unavailable.
	bindProbes bool
}

// NewValidator builds a validator.
func NewValidator(server *ServerClient, store *ConfigStore) *Validator {
	return &Validator{server: server, store: store}
}

// SetBindProbes enables or disables listen-address probing.
func (v *Validator) SetBindProbes(enabled bool) { v.bindProbes = enabled }

// Validate runs every check and returns the full report.
//
// Checks that depend on an earlier one are reported as skipped rather than omitted, so
// the interface can explain why it has nothing to say instead of looking broken.
func (v *Validator) Validate(ctx context.Context, settings ClientSettings) ValidationReport {
	report := ValidationReport{CheckedAt: time.Now().UTC()}
	add := func(id, status, message string) {
		report.Checks = append(report.Checks, CheckResult{ID: id, Status: status, Message: message})
	}
	addTunnel := func(id string, index int, name, status, message string) {
		report.Checks = append(report.Checks, CheckResult{ID: id, Status: status, Message: message, Tunnel: name, Index: index})
	}

	serverURL := strings.TrimSpace(settings.ServerURL)
	urlOK := false
	switch {
	case serverURL == "":
		add(CheckServerURLFormat, StatusFailed, "client.server_url is required")
	default:
		if _, err := client.HTTPURLFromWebSocket(serverURL, client.HealthReadyPath); err != nil {
			add(CheckServerURLFormat, StatusFailed, err.Error())
		} else {
			urlOK = true
			add(CheckServerURLFormat, StatusPassed, "")
		}
	}

	reachable := false
	if !urlOK {
		add(CheckServerReachable, StatusSkipped, "the server URL is not usable")
	} else if err := v.server.CheckHealth(ctx, serverURL); err != nil {
		add(CheckServerReachable, StatusFailed, err.Error())
	} else {
		reachable = true
		add(CheckServerReachable, StatusPassed, "")
	}

	token := strings.TrimSpace(settings.Token)
	tokenPresent := token != ""
	if !tokenPresent {
		add(CheckTokenPresent, StatusFailed, "client.token is required")
	} else {
		add(CheckTokenPresent, StatusPassed, "")
	}

	var agents []AgentRef
	agentsKnown := false
	if !reachable || !tokenPresent {
		add(CheckTokenValid, StatusSkipped, "the server is not reachable with a token supplied")
		add(CheckAgentsAvailable, StatusSkipped, "the server is not reachable with a token supplied")
	} else {
		list, err := v.server.ListAgents(ctx, serverURL, token)
		switch {
		case err == nil:
			agents, agentsKnown = list, true
			report.Agents = list
			add(CheckTokenValid, StatusPassed, "")
			add(CheckAgentsAvailable, StatusPassed, fmt.Sprintf("%d agent(s) in scope", len(list)))
		case errors.Is(err, ErrInvalidToken):
			// The token was judged and rejected, so the Agent list was never reached.
			add(CheckTokenValid, StatusFailed, err.Error())
			add(CheckAgentsAvailable, StatusSkipped, "the token was rejected")
		case errors.Is(err, ErrServerTooOld):
			// The Server cannot answer the client-scoped endpoint, so it never judged
			// the token. Reporting the token as invalid here would send the operator to
			// rotate a credential that may be perfectly fine.
			add(CheckTokenValid, StatusSkipped, "the server cannot validate a client token through this endpoint")
			add(CheckAgentsAvailable, StatusFailed, err.Error())
		default:
			add(CheckTokenValid, StatusFailed, err.Error())
			add(CheckAgentsAvailable, StatusFailed, err.Error())
		}
	}

	if len(settings.Tunnels) == 0 {
		add(CheckTunnelsPresent, StatusFailed, "at least one client.tunnels entry is required")
	} else {
		add(CheckTunnelsPresent, StatusPassed, fmt.Sprintf("%d tunnel(s) configured", len(settings.Tunnels)))
	}

	byID := make(map[string]AgentRef, len(agents))
	for _, agent := range agents {
		byID[agent.ID] = agent
	}
	for index, tunnel := range settings.Tunnels {
		number := index + 1
		name := client.TunnelDisplayName(tunnel)
		v.checkTunnel(ctx, addTunnel, number, name, tunnel, byID, agentsKnown)
	}

	// The authoritative structural check. Running the real loader is what guarantees a
	// configuration the tray accepts is one `client run` accepts, including the
	// mode/storage coupling and the non-loopback allow_remote guard.
	if _, err := v.store.BuildConfig(ctx, settings); err != nil {
		add(CheckConfigValid, StatusFailed, err.Error())
	} else {
		add(CheckConfigValid, StatusPassed, "")
	}

	report.Valid = report.OK()
	return report
}

// checkTunnel runs the checks that need server data, the live host or the process
// environment - the three things config.Validate cannot see.
func (v *Validator) checkTunnel(
	_ context.Context,
	addTunnel func(id string, index int, name, status, message string),
	index int, name string, tunnel config.TunnelConfig,
	agents map[string]AgentRef, agentsKnown bool,
) {
	protocol := strings.ToLower(strings.TrimSpace(tunnel.Protocol))
	if supportedProtocol(protocol) {
		addTunnel(CheckTunnelProtocol, index, name, StatusPassed, "")
	} else {
		addTunnel(CheckTunnelProtocol, index, name, StatusFailed,
			fmt.Sprintf("unsupported tunnel protocol %q, want one of %s", tunnel.Protocol, strings.Join(client.SupportedTunnelProtocols(), ", ")))
	}

	switch {
	case strings.TrimSpace(tunnel.ListenAddr) == "":
		addTunnel(CheckTunnelListen, index, name, StatusFailed, "listen address is required")
	case !v.bindProbes:
		if _, _, err := net.SplitHostPort(strings.TrimSpace(tunnel.ListenAddr)); err != nil {
			addTunnel(CheckTunnelListen, index, name, StatusFailed, fmt.Sprintf("listen must be a host:port address: %v", err))
		} else {
			addTunnel(CheckTunnelListen, index, name, StatusSkipped, "the running client already holds the listeners")
		}
	default:
		if err := probeListen(protocol, tunnel.ListenAddr); err != nil {
			addTunnel(CheckTunnelListen, index, name, StatusFailed, err.Error())
		} else {
			addTunnel(CheckTunnelListen, index, name, StatusPassed, "")
		}
	}

	agentID := strings.TrimSpace(tunnel.AgentID)
	switch {
	case agentID == "":
		addTunnel(CheckTunnelAgent, index, name, StatusFailed, "agent_id is required")
	case !agentsKnown:
		addTunnel(CheckTunnelAgent, index, name, StatusSkipped, "the agent list could not be fetched")
	default:
		agent, known := agents[agentID]
		switch {
		case !known:
			addTunnel(CheckTunnelAgent, index, name, StatusFailed,
				fmt.Sprintf("agent %q is not available to this token", agentID))
		case !agent.Online:
			// Offline is not a configuration mistake: the tunnel is correct and will
			// carry traffic as soon as the Agent reconnects.
			addTunnel(CheckTunnelAgent, index, name, StatusWarning,
				fmt.Sprintf("agent %q is currently offline", agent.DisplayName()))
		default:
			addTunnel(CheckTunnelAgent, index, name, StatusPassed, agent.DisplayName())
		}
	}

	switch protocol {
	case "tcp", "udp", "http":
		// Only the raw forwarders have a fixed internal target; socks5 and http-proxy
		// resolve the target per request, so asking for one here would be wrong.
		switch {
		case strings.TrimSpace(tunnel.TargetHost) == "":
			addTunnel(CheckTunnelTarget, index, name, StatusFailed, "target_host is required")
		case tunnel.TargetPort < 1 || tunnel.TargetPort > 65535:
			addTunnel(CheckTunnelTarget, index, name, StatusFailed,
				fmt.Sprintf("target_port %d must be between 1 and 65535", tunnel.TargetPort))
		default:
			addTunnel(CheckTunnelTarget, index, name, StatusPassed, "")
		}
		if strings.TrimSpace(tunnel.AuthURL) != "" {
			// Nothing reads it on these protocols, so the operator has to be told rather
			// than left believing requests are being authorised.
			addTunnel(CheckTunnelAuthURL, index, name, StatusWarning,
				"auth_url is only consulted by the socks5 and http-proxy forwarders")
		}
	case "socks5":
		if strings.EqualFold(strings.TrimSpace(tunnel.AuthMode), "password") {
			checkProxyCredentials(addTunnel, index, name, "TUNNELMESH_SOCKS5_USERNAME", "TUNNELMESH_SOCKS5_PASSWORD")
		}
		checkAuthURL(addTunnel, index, name, tunnel.AuthURL)
	case "http-proxy":
		if strings.EqualFold(strings.TrimSpace(tunnel.AuthMode), "basic") {
			checkProxyCredentials(addTunnel, index, name, "TUNNELMESH_HTTP_PROXY_USERNAME", "TUNNELMESH_HTTP_PROXY_PASSWORD")
		}
		checkAuthURL(addTunnel, index, name, tunnel.AuthURL)
	}
}

// checkAuthURL validates the remote validation endpoint a proxy tunnel asks on every request.
//
// The forwarder builds the call with http.NewRequestWithContext, so anything that is not an
// absolute http(s) URL makes the tunnel reject every connection - a failure the runtime only
// discovers once someone actually uses the proxy, which is exactly the class of problem 检测
// exists to catch first. An empty value means the feature is off, the common case, so nothing
// is reported for it.
func checkAuthURL(addTunnel func(id string, index int, name, status, message string), index int, name, raw string) {
	endpoint := strings.TrimSpace(raw)
	if endpoint == "" {
		return
	}
	parsed, err := url.Parse(endpoint)
	switch {
	case err != nil:
		addTunnel(CheckTunnelAuthURL, index, name, StatusFailed, err.Error())
	case !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https"):
		addTunnel(CheckTunnelAuthURL, index, name, StatusFailed,
			fmt.Sprintf("%q must be an absolute http(s) URL", endpoint))
	default:
		addTunnel(CheckTunnelAuthURL, index, name, StatusPassed, "")
	}
}

// checkProxyCredentials reports missing proxy credentials.
//
// The runtime reads them from the environment rather than from client.yaml so a shared
// configuration file never carries a password. That is the right storage decision and
// the wrong discovery experience, so validation names the exact variables to set.
func checkProxyCredentials(addTunnel func(id string, index int, name, status, message string), index int, name, userVar, passwordVar string) {
	var missing []string
	if strings.TrimSpace(os.Getenv(userVar)) == "" {
		missing = append(missing, userVar)
	}
	if strings.TrimSpace(os.Getenv(passwordVar)) == "" {
		missing = append(missing, passwordVar)
	}
	if len(missing) > 0 {
		addTunnel(CheckTunnelCredentials, index, name, StatusFailed,
			fmt.Sprintf("missing environment variables: %s", strings.Join(missing, ", ")))
		return
	}
	addTunnel(CheckTunnelCredentials, index, name, StatusPassed, "")
}

// probeListen test-binds a listen address and releases it immediately.
func probeListen(protocol, addr string) error {
	addr = strings.TrimSpace(addr)
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("listen must be a host:port address: %w", err)
	}
	if strings.EqualFold(protocol, "udp") {
		conn, err := net.ListenPacket("udp", addr)
		if err != nil {
			return fmt.Errorf("listen address %s cannot be bound: %w", addr, err)
		}
		_ = conn.Close()
		return nil
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen address %s cannot be bound: %w", addr, err)
	}
	_ = listener.Close()
	return nil
}

// supportedProtocol reports whether the runtime can build a protocol.
func supportedProtocol(protocol string) bool {
	for _, candidate := range client.SupportedTunnelProtocols() {
		if candidate == protocol {
			return true
		}
	}
	return false
}
