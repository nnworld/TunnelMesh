package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/build"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/metadata"
	"github.com/tunnelmesh/tunnelmesh/internal/observability"
	"github.com/tunnelmesh/tunnelmesh/internal/protocol"
)

// Forward is one local ingress started by a Runtime.
//
// Err is part of the contract rather than an optional extra: a forwarder that cannot
// say why its listener stopped is indistinguishable from a healthy one once the port
// goes deaf.
type Forward interface {
	Start(context.Context) error
	Close() error
	Err() <-chan error
}

// Tunnel states reported by Runtime.Stats. They describe what an operator can act
// on, not internal transitions: a tunnel is listening, it failed, or the run ended.
const (
	TunnelStateStarting  = "starting"
	TunnelStateListening = "listening"
	TunnelStateFailed    = "failed"
	TunnelStateStopped   = "stopped"
)

// RuntimeOptions configures one client run.
type RuntimeOptions struct {
	// ConfigPath is the configuration file the run was loaded from. It derives the
	// advisory lock path, which is what makes the tray and `client run` mutually
	// exclusive exactly when they share a configuration. Empty disables locking.
	ConfigPath string
	// Stdout receives one line per started tunnel. `client run` passes the command
	// output so its existing transcript is unchanged; the tray passes nil.
	Stdout io.Writer
	// Runner overrides the physical WebSocket runner. Tests inject a fake transport
	// and the CLI injects its own package-level seam.
	Runner SessionPoolRunner
	// InstanceID overrides the persisted instance identity. Empty keeps the
	// documented behaviour of ensuring the identity file.
	InstanceID string
	// Metrics enables in-process instrumentation. It is opt-in so `client run`
	// keeps allocating nothing it does not use; the tray passes a registry it
	// gathers for the statistics tab.
	Metrics *observability.Metrics
	// SkipLock disables the advisory lock. Only one-shot forwards and tests that
	// deliberately run two configurations side by side need it.
	SkipLock bool
}

// Runtime owns one client run: the advisory lock that makes it exclusive, every
// local listener, and the per-Agent session pool behind them.
//
// It exists so the CLI and the tray drive tunnels through one implementation. Two
// separate copies of this assembly would disagree about which protocols are valid,
// which environment variables supply proxy credentials, or when a deaf listener
// should end the run, and the disagreement would only show up in production.
type Runtime struct {
	cfg      config.Config
	opts     RuntimeOptions
	manager  *SessionPoolManager
	forwards []*runtimeForward

	mu        sync.Mutex
	lock      runtimeLock
	runCtx    context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	started   bool
	startedAt time.Time
	stoppedAt time.Time
	runErr    error
	attempts  map[int]int
	watcher   *ForwardServeWatcher
}

// runtimeForward pairs one listener with the configuration it was built from, so
// Stats can describe a tunnel without re-deriving its identity.
type runtimeForward struct {
	forward     Forward
	description string
	tunnel      config.TunnelConfig
	state       string
	lastError   string
}

// RuntimeStats is the runtime snapshot the tray renders. Every field is derived
// from state the Runtime already owns, so reading it cannot perturb the run.
type RuntimeStats struct {
	Running   bool
	StartedAt time.Time
	Uptime    time.Duration
	ServerURL string
	// Connected reports at least one registered WebSocket session. It is not the
	// same as "a slot exists": slots are scheduled before the handshake completes.
	Connected  bool
	Reconnects int
	Agents     []AgentStats
	Tunnels    []TunnelStatus
}

// AgentStats summarizes one Agent's connection pool.
type AgentStats struct {
	AgentID       string
	OpenSlots     int
	ReadySessions int
	ActiveStreams int
}

// TunnelStatus describes one configured local ingress.
type TunnelStatus struct {
	Name       string
	Protocol   string
	ListenAddr string
	AgentID    string
	Target     string
	State      string
	LastError  string
}

// NewRuntimeFromConfig assembles a run from a validated configuration without
// starting anything.
//
// Construction is separated from Start so a malformed tunnel is reported before any
// port is bound, and so the caller can hold a Runtime while it acquires other
// resources. The precondition errors are the ones `client run` has always produced,
// verbatim, because operators and scripts match on them.
func NewRuntimeFromConfig(ctx context.Context, cfg config.Config, opts RuntimeOptions) (*Runtime, error) {
	if strings.TrimSpace(cfg.Client.ServerURL) == "" || strings.TrimSpace(cfg.Client.Token) == "" {
		return nil, errors.New("client run requires client.server_url and client.token")
	}
	if len(cfg.Client.Tunnels) == 0 {
		return nil, errors.New("client run requires at least one client.tunnels entry")
	}
	meta, err := RuntimeMetadata(ctx, cfg, opts.InstanceID)
	if err != nil {
		return nil, err
	}
	runner := opts.Runner
	if runner == nil {
		runner = RunWebSocketWithOptions
	}
	r := &Runtime{cfg: cfg, opts: opts, attempts: make(map[int]int)}
	// The manager is assigned after r exists because the reconnect counter wraps the
	// runner, and a struct literal cannot reference the value it is initializing.
	r.manager = NewSessionPoolManager(SessionPoolManagerOptions{
		ServerURL: cfg.Client.ServerURL,
		Token:     cfg.Client.Token,
		Config: SessionPoolConfig{
			Min:                cfg.Client.Connections.Min,
			Max:                cfg.Client.Connections.Max,
			HighWatermark:      cfg.Client.Connections.HighWatermark,
			LowWatermark:       cfg.Client.Connections.LowWatermark,
			EvaluationInterval: cfg.Client.Connections.EvaluationInterval,
			Cooldown:           cfg.Client.Connections.Cooldown,
		},
		Metadata: meta,
		Runner:   r.countingRunner(runner),
		WebSocket: WebSocketRunOptions{
			InboundBufferBytes: cfg.Client.Stream.InboundBufferBytes,
			Metrics:            opts.Metrics,
		},
	})
	for _, tunnel := range cfg.Client.Tunnels {
		forward, description, err := NewConfiguredForward(r.manager.Opener(tunnel.AgentID), cfg.Client, tunnel)
		if err != nil {
			return nil, err
		}
		r.forwards = append(r.forwards, &runtimeForward{
			forward: forward, description: description, tunnel: tunnel, state: TunnelStateStarting,
		})
	}
	return r, nil
}

// countingRunner wraps a runner so reconnects become observable.
//
// The pool recreates a slot's WebSocket after a drop without telling its caller, so
// "how unstable is this link" is otherwise invisible. Slot numbers are part of the
// run options the pool already supplies, which makes them a stable key without the
// Runtime having to reach into pool internals.
func (r *Runtime) countingRunner(inner SessionPoolRunner) SessionPoolRunner {
	return func(ctx context.Context, serverURL, token string, onReady func(*Session) error, options WebSocketRunOptions) error {
		r.mu.Lock()
		r.attempts[options.ConnectionSlot]++
		r.mu.Unlock()
		return inner(ctx, serverURL, token, onReady, options)
	}
}

// Start takes the advisory lock, binds every listener and launches the session pool
// in the background. It returns once the listeners are up, so a caller can report
// "ready" without waiting for the run to end.
func (r *Runtime) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return errors.New("client: runtime already started")
	}
	r.started = true
	r.mu.Unlock()

	if !r.opts.SkipLock {
		// The lock is taken before any listener binds so the second runtime fails
		// with one clear message instead of a wall of address-in-use errors.
		lock, err := acquireRuntimeLock(LockPathForConfig(r.opts.ConfigPath))
		if err != nil {
			r.mu.Lock()
			r.started = false
			r.mu.Unlock()
			return err
		}
		r.mu.Lock()
		r.lock = lock
		r.mu.Unlock()
	}

	runCtx, cancel := context.WithCancel(ctx)
	watcher := NewForwardServeWatcher(len(r.forwards))
	done := make(chan struct{})
	r.mu.Lock()
	r.runCtx, r.cancel, r.watcher, r.done = runCtx, cancel, watcher, done
	r.startedAt = time.Now().UTC()
	r.mu.Unlock()

	for _, entry := range r.forwards {
		if err := entry.forward.Start(runCtx); err != nil {
			_ = entry.forward.Close()
			r.fail(cancel)
			return err
		}
		r.setTunnelState(entry, TunnelStateListening, "")
		if r.opts.Stdout != nil {
			_, _ = fmt.Fprintln(r.opts.Stdout, entry.description)
		}
		// One failure channel, two consumers: the supervisor needs it to end the run
		// and Stats needs it to explain why a tunnel is no longer listening.
		streams := broadcastErr(runCtx, entry.forward.Err(), 2)
		watcher.Watch(runCtx, entry.description, cancel, streams[0])
		go r.recordFailure(runCtx, entry, streams[1])
	}

	go func() {
		defer close(done)
		err := SuperviseRun(func() error { return r.manager.Run(runCtx) }, watcher)
		r.mu.Lock()
		r.runErr = err
		r.stoppedAt = time.Now().UTC()
		r.mu.Unlock()
		r.markStopped()
	}()
	return nil
}

// Wait blocks until the run finishes and returns the supervised error.
//
// runErr is written before done is closed, so the close is the publication point and
// reading the field after a receive needs no lock.
func (r *Runtime) Wait(ctx context.Context) error {
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done == nil {
		return errors.New("client: runtime was not started")
	}
	select {
	case <-done:
		return r.runErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop ends the run, closes listeners in reverse start order and releases the lock.
// It is idempotent and safe to call after a failed Start.
func (r *Runtime) Stop() error {
	r.mu.Lock()
	cancel, done, lock := r.cancel, r.done, r.lock
	r.lock = nil
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	r.closeForwards()
	if done != nil {
		<-done
	}
	if lock == nil {
		return nil
	}
	return lock.Release()
}

// Stats returns a point-in-time snapshot safe to poll from another goroutine.
func (r *Runtime) Stats() RuntimeStats {
	r.mu.Lock()
	startedAt, stoppedAt, isRunning := r.startedAt, r.stoppedAt, r.started && r.stoppedAt.IsZero()
	attempts := make(map[int]int, len(r.attempts))
	for slot, count := range r.attempts {
		attempts[slot] = count
	}
	r.mu.Unlock()

	stats := RuntimeStats{
		Running:   isRunning,
		StartedAt: startedAt,
		ServerURL: r.cfg.Client.ServerURL,
	}
	if !startedAt.IsZero() {
		end := time.Now().UTC()
		if !stoppedAt.IsZero() {
			end = stoppedAt
		}
		stats.Uptime = end.Sub(startedAt)
	}

	reconnects := 0
	for _, count := range attempts {
		if count > 1 {
			reconnects += count - 1
		}
	}
	stats.Reconnects = reconnects

	for _, agentID := range r.manager.AgentIDs() {
		agent := AgentStats{
			AgentID:       agentID,
			OpenSlots:     r.manager.OpenCount(agentID),
			ReadySessions: r.manager.ReadyCount(agentID),
			ActiveStreams: len(r.manager.ActiveStreams(agentID)),
		}
		stats.Agents = append(stats.Agents, agent)
		if agent.ReadySessions > 0 {
			stats.Connected = true
		}
	}

	r.mu.Lock()
	for _, entry := range r.forwards {
		stats.Tunnels = append(stats.Tunnels, TunnelStatus{
			Name:       TunnelDisplayName(entry.tunnel),
			Protocol:   strings.ToLower(strings.TrimSpace(entry.tunnel.Protocol)),
			ListenAddr: entry.tunnel.ListenAddr,
			AgentID:    strings.TrimSpace(entry.tunnel.AgentID),
			Target:     tunnelTarget(entry.tunnel),
			State:      entry.state,
			LastError:  entry.lastError,
		})
	}
	r.mu.Unlock()
	return stats
}

// fail unwinds a partially started run. Listeners are closed in reverse order so the
// newest ingress is withdrawn before the one an operator is most likely using.
func (r *Runtime) fail(cancel context.CancelFunc) {
	cancel()
	r.closeForwards()
	r.mu.Lock()
	r.started = false
	r.stoppedAt = time.Now().UTC()
	r.mu.Unlock()
}

func (r *Runtime) closeForwards() {
	for i := len(r.forwards) - 1; i >= 0; i-- {
		_ = r.forwards[i].forward.Close()
	}
}

func (r *Runtime) setTunnelState(entry *runtimeForward, state, lastError string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry.state = state
	if lastError != "" {
		entry.lastError = lastError
	}
}

func (r *Runtime) recordFailure(ctx context.Context, entry *runtimeForward, errs <-chan error) {
	select {
	case <-ctx.Done():
	case reported, open := <-errs:
		if !open || reported == nil {
			return
		}
		r.setTunnelState(entry, TunnelStateFailed, reported.Error())
	}
}

// markStopped records the terminal state of every tunnel that was still listening.
//
// A tunnel that failed earlier keeps its own state and reason: overwriting it with
// "stopped" would discard the only explanation of why the run ended.
func (r *Runtime) markStopped() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, entry := range r.forwards {
		if entry.state == TunnelStateListening || entry.state == TunnelStateStarting {
			entry.state = TunnelStateStopped
		}
	}
}

// broadcastErr fans one terminal failure channel out to several receivers.
//
// A second goroutine reading the source directly would steal the value: channel
// delivery goes to exactly one receiver, so the supervisor and the status recorder
// would each observe the failure only half the time. Broadcasting from a single
// reader keeps both consumers deterministic.
func broadcastErr(ctx context.Context, src <-chan error, receivers int) []<-chan error {
	if src == nil {
		return make([]<-chan error, receivers)
	}
	outs := make([]chan error, receivers)
	result := make([]<-chan error, receivers)
	for i := range outs {
		outs[i] = make(chan error, 1)
		result[i] = outs[i]
	}
	go func() {
		defer func() {
			for _, out := range outs {
				close(out)
			}
		}()
		var err error
		select {
		case <-ctx.Done():
			return
		case reported, open := <-src:
			if !open || reported == nil {
				return
			}
			err = reported
		}
		for _, out := range outs {
			select {
			case out <- err:
			default:
			}
		}
	}()
	return result
}

// tunnelTarget renders the internal target for display. socks5 and http-proxy
// tunnels have no fixed target, so they report the Agent they resolve through.
func tunnelTarget(tunnel config.TunnelConfig) string {
	if tunnel.TargetHost == "" || tunnel.TargetPort == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", tunnel.TargetHost, tunnel.TargetPort)
}

// SupportedTunnelProtocols lists the protocols NewConfiguredForward can build.
//
// It is exported so an interface that offers a protocol picker cannot drift from what
// the runtime can actually start. config.validateClientTunnels keeps its own switch
// because it has to reject a protocol by name in an error message.
func SupportedTunnelProtocols() []string {
	return []string{"tcp", "udp", "http", "socks5", "http-proxy"}
}

// TunnelDisplayName returns the operator-facing name of a tunnel.
func TunnelDisplayName(t config.TunnelConfig) string {
	if strings.TrimSpace(t.Name) != "" {
		return strings.TrimSpace(t.Name)
	}
	return "unnamed"
}

// RuntimeMetadata builds the shared observability snapshot for every physical
// WebSocket. The instance identity is persisted before listeners start so reconnects
// and per-Agent pools aggregate under one Client resource.
func RuntimeMetadata(ctx context.Context, cfg config.Config, instanceIDOverride string) (ClientMetadataOptions, error) {
	instanceID := strings.TrimSpace(instanceIDOverride)
	if instanceID == "" {
		instanceID = strings.TrimSpace(cfg.Client.InstanceID)
	}
	if instanceID == "" {
		identityPath := strings.TrimSpace(cfg.Client.InstanceIDPath)
		if identityPath == "" {
			identityPath = DefaultClientInstanceIDPath()
		}
		generated, err := EnsureClientInstanceID(identityPath)
		if err != nil {
			return ClientMetadataOptions{}, fmt.Errorf("ensure client instance identity: %w", err)
		}
		instanceID = generated
	}

	collected, err := metadata.NewCollector(cfg.Client.Metadata).Collect(ctx)
	if err != nil {
		return ClientMetadataOptions{}, fmt.Errorf("collect client metadata: %w", err)
	}
	fields := make([]MetadataField, 0, len(collected.Fields))
	for _, field := range collected.Fields {
		fields = append(fields, MetadataField{Name: field.Name, Source: field.Source, Value: field.Value})
	}

	agentIDs := make([]string, 0, len(cfg.Client.Tunnels))
	seenAgents := make(map[string]struct{}, len(cfg.Client.Tunnels))
	listeners := make([]protocol.ClientListener, 0, len(cfg.Client.Tunnels))
	for _, tunnel := range cfg.Client.Tunnels {
		agentID := strings.TrimSpace(tunnel.AgentID)
		if agentID == "" {
			continue
		}
		if _, exists := seenAgents[agentID]; !exists {
			seenAgents[agentID] = struct{}{}
			agentIDs = append(agentIDs, agentID)
		}
		listeners = append(listeners, protocol.ClientListener{
			Protocol:      strings.ToLower(strings.TrimSpace(tunnel.Protocol)),
			ListenAddress: tunnel.ListenAddr, AgentID: agentID, Enabled: true,
		})
	}
	info := build.Current()
	return ClientMetadataOptions{
		InstanceID: instanceID, AgentIDs: agentIDs, Version: info.Version, Commit: info.Commit,
		Listeners: listeners, Metadata: fields,
	}, nil
}

// NewConfiguredForward maps one configured tunnel onto the forwarder that implements
// its protocol, and returns the human-readable line describing the listener.
func NewConfiguredForward(opener StreamOpener, cfg config.ClientConfig, tunnel config.TunnelConfig) (Forward, string, error) {
	switch strings.ToLower(strings.TrimSpace(tunnel.Protocol)) {
	case "tcp":
		forward, err := NewTCPForward(opener, TCPForwardConfig{
			ListenAddr: tunnel.ListenAddr, AgentID: tunnel.AgentID,
			TargetHost: tunnel.TargetHost, TargetPort: tunnel.TargetPort,
			AllowRemote: tunnel.AllowRemote,
		})
		return forward, fmt.Sprintf("tcp tunnel %s listening on %s", TunnelDisplayName(tunnel), tunnel.ListenAddr), err
	case "udp":
		forward, err := NewUDPForward(opener, UDPForwardConfig{
			ListenAddr: tunnel.ListenAddr, AgentID: tunnel.AgentID,
			TargetHost: tunnel.TargetHost, TargetPort: tunnel.TargetPort,
			AllowRemote: tunnel.AllowRemote,
		})
		return forward, fmt.Sprintf("udp tunnel %s listening on %s", TunnelDisplayName(tunnel), tunnel.ListenAddr), err
	case "http":
		forward, err := NewHTTPForward(opener, HTTPForwardConfig{
			ListenAddr: tunnel.ListenAddr, AgentID: tunnel.AgentID,
			TargetHost: tunnel.TargetHost, TargetPort: tunnel.TargetPort,
			AllowRemote: tunnel.AllowRemote,
		})
		return forward, fmt.Sprintf("http tunnel %s listening on %s", TunnelDisplayName(tunnel), tunnel.ListenAddr), err
	case "socks5":
		forward, err := newConfiguredSOCKS5Forward(opener, cfg, tunnel)
		return forward, fmt.Sprintf("socks5 tunnel %s listening on %s", TunnelDisplayName(tunnel), tunnel.ListenAddr), err
	case "http-proxy":
		forward, err := newConfiguredHTTPProxyForward(opener, cfg, tunnel)
		return forward, fmt.Sprintf("http-proxy tunnel %s listening on %s", TunnelDisplayName(tunnel), tunnel.ListenAddr), err
	default:
		return nil, "", fmt.Errorf("unsupported tunnel protocol %q", tunnel.Protocol)
	}
}

// newConfiguredSOCKS5Forward keeps proxy credentials out of the configuration file.
// They are read from the environment at start, so a client.yaml that is shared or
// committed never carries a password.
func newConfiguredSOCKS5Forward(opener StreamOpener, cfg config.ClientConfig, tunnel config.TunnelConfig) (*SOCKS5Forward, error) {
	authMode := strings.ToLower(strings.TrimSpace(tunnel.AuthMode))
	username := ""
	password := ""
	if authMode == "password" {
		username = os.Getenv("TUNNELMESH_SOCKS5_USERNAME")
		password = os.Getenv("TUNNELMESH_SOCKS5_PASSWORD")
		if username == "" || password == "" {
			return nil, errors.New("socks5 password auth requires TUNNELMESH_SOCKS5_USERNAME and TUNNELMESH_SOCKS5_PASSWORD")
		}
	}
	return NewSOCKS5Forward(opener, SOCKS5ForwardConfig{
		ListenAddr:  tunnel.ListenAddr,
		AgentID:     tunnel.AgentID,
		AllowRemote: tunnel.AllowRemote,
		AuthMode:    SOCKS5AuthMode(authMode),
		Username:    username,
		Password:    password,
		AuthURL:     tunnel.AuthURL,
		RemoteValidation: RemoteValidationCacheConfig{
			Endpoint:    tunnel.AuthURL,
			PositiveTTL: cfg.RemoteValidation.PositiveTTL,
			NegativeTTL: cfg.RemoteValidation.NegativeTTL,
			Timeout:     cfg.RemoteValidation.Timeout,
			MaxEntries:  cfg.RemoteValidation.MaxEntries,
		},
	})
}

// newConfiguredHTTPProxyForward applies the same environment-only credential rule to
// the HTTP CONNECT proxy.
func newConfiguredHTTPProxyForward(opener StreamOpener, cfg config.ClientConfig, tunnel config.TunnelConfig) (*HTTPProxyForward, error) {
	authMode := strings.ToLower(strings.TrimSpace(tunnel.AuthMode))
	username := ""
	password := ""
	if authMode == "basic" {
		username = os.Getenv("TUNNELMESH_HTTP_PROXY_USERNAME")
		password = os.Getenv("TUNNELMESH_HTTP_PROXY_PASSWORD")
		if username == "" || password == "" {
			return nil, errors.New("http-proxy basic auth requires TUNNELMESH_HTTP_PROXY_USERNAME and TUNNELMESH_HTTP_PROXY_PASSWORD")
		}
	}
	return NewHTTPProxyForward(opener, HTTPProxyForwardConfig{
		ListenAddr:  tunnel.ListenAddr,
		AgentID:     tunnel.AgentID,
		AllowRemote: tunnel.AllowRemote,
		AuthMode:    HTTPProxyAuthMode(authMode),
		Username:    username,
		Password:    password,
		AuthURL:     tunnel.AuthURL,
		RemoteValidation: RemoteValidationCacheConfig{
			Endpoint:    tunnel.AuthURL,
			PositiveTTL: cfg.RemoteValidation.PositiveTTL,
			NegativeTTL: cfg.RemoteValidation.NegativeTTL,
			Timeout:     cfg.RemoteValidation.Timeout,
			MaxEntries:  cfg.RemoteValidation.MaxEntries,
		},
	})
}
