// Package cli contains the thin Cobra command shells for the three binaries.
// Commands deliberately stop at configuration/orchestration boundaries; later
// tasks can attach services without changing their public command surface.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tunnelmesh/tunnelmesh/internal/agent"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
	"github.com/tunnelmesh/tunnelmesh/internal/build"
	"github.com/tunnelmesh/tunnelmesh/internal/client"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
	"github.com/tunnelmesh/tunnelmesh/internal/server"
	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

// NewServerRoot constructs the server command tree.
func NewServerRoot() *cobra.Command { return newRoot("tunnelmesh-server", serverCommands) }

// NewAgentRoot constructs the agent command tree.
func NewAgentRoot() *cobra.Command { return newRoot("tunnelmesh-agent", agentCommands) }

// NewClientRoot constructs the client command tree.
func NewClientRoot() *cobra.Command { return newRoot("tunnelmesh-client", clientCommands) }

// Aliases make the root constructors convenient to embed in main packages and
// preserve a simple naming convention for downstream callers.
func ServerRoot() *cobra.Command { return NewServerRoot() }
func AgentRoot() *cobra.Command  { return NewAgentRoot() }
func ClientRoot() *cobra.Command { return NewClientRoot() }

func NewServerCommand() *cobra.Command { return NewServerRoot() }
func NewAgentCommand() *cobra.Command  { return NewAgentRoot() }
func NewClientCommand() *cobra.Command { return NewClientRoot() }

type rootOptions struct {
	binaryName                  string
	configFile                  string
	nodeIDPath                  string
	mode                        string
	storage                     string
	autoInit                    bool
	registry                    string
	nodeID                      string
	bridge                      bool
	dynamicSuffix               string
	allowedHosts                []string
	allowedOrigins              []string
	allowLegacyConnectionTokens bool
	tlsEnabled                  bool
	tlsCertFile                 string
	tlsKeyFile                  string
	tlsMinVersion               string
	relayEnabled                bool
	relayListen                 string
	relayEndpoint               string
	relayCA                     string
	relayCert                   string
	relayKey                    string
	relayServerName             string
	relayNodeToken              string
	webSSHEnabled               bool
	webSSHTicketTTL             time.Duration
	webSSHSessionTTL            time.Duration
	webSSHMaxActiveSessionsUser int
	webSSHOpenTimeout           time.Duration
	webSSHIdleTimeout           time.Duration
	webSSHMaxMessageBytes       int
	proxyEntryEnabled           bool
	proxyEntryListen            string
	proxyEntryDomainSuffix      string
	proxyEntryConnectTimeout    time.Duration
	proxyEntryIdleTimeout       time.Duration
	proxyEntryMaxTunnels        int
	clientServerURL             string
	clientToken                 string
}

func newRoot(use string, factory func(*rootOptions) []*cobra.Command) *cobra.Command {
	opts := &rootOptions{binaryName: use, nodeIDPath: config.DefaultNodeIDPath}
	root := &cobra.Command{
		Use:           use,
		Short:         "TunnelMesh " + strings.TrimPrefix(use, "tunnelmesh-") + " command",
		Version:       build.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("{{.Name}} {{.Version}}\n")
	flags := root.PersistentFlags()
	flags.StringVar(&opts.configFile, "config", "", "path to a YAML, JSON, or TOML configuration file")
	flags.StringVar(&opts.mode, "mode", "", "operation mode (local or cluster)")
	flags.StringVar(&opts.storage, "storage.driver", "", "storage driver (sqlite or mysql)")
	flags.StringVar(&opts.storage, "storage-driver", "", "storage driver (sqlite or mysql)")
	flags.BoolVar(&opts.autoInit, "storage.auto_init", false, "automatically initialize the database schema")
	flags.BoolVar(&opts.autoInit, "storage-auto-init", false, "automatically initialize the database schema")
	flags.StringVar(&opts.registry, "registry.type", "", "registry type (database or etcd)")
	flags.StringVar(&opts.registry, "registry", "", "registry type (database or etcd)")
	flags.StringVar(&opts.nodeID, "node.id", "", "cluster node identity")
	flags.StringVar(&opts.nodeID, "node-id", "", "cluster node identity")
	// node identity 的持久化路径必须可覆盖：默认值 /var/lib/tunnelmesh/node-id 只有
	// root 可写，用户级安装（systemd user 单元、macOS LaunchAgent、一键脚本 user 模式）
	// 需要把它指到自己的状态目录，否则 init-node-id 与 cluster 模式的 run 都会失败。
	flags.StringVar(&opts.nodeIDPath, "node-id-path", config.DefaultNodeIDPath, "file used to persist the generated cluster node identity")
	flags.BoolVar(&opts.bridge, "server.tcp_bridge.enabled", false, "enable TCP-over-WebSocket bridge")
	flags.BoolVar(&opts.bridge, "tcp-bridge", false, "enable TCP-over-WebSocket bridge")
	flags.Bool("server.tcp_bridge_enabled", false, "enable TCP-over-WebSocket bridge (flat spelling)")
	flags.StringVar(&opts.dynamicSuffix, "server.dynamic_suffix", "", "DNS suffix for dynamic managed routes")
	flags.StringSliceVar(&opts.allowedHosts, "security.allowed_hosts", nil, "exact Host values accepted by the Agent WebSocket endpoint")
	flags.StringSliceVar(&opts.allowedOrigins, "security.allowed_origins", nil, "exact http/https Origin values accepted by Agent WebSocket")
	flags.BoolVar(&opts.allowLegacyConnectionTokens, "security.allow_legacy_connection_tokens", false, "temporarily accept deprecated management tokens for Agent connections")
	flags.BoolVar(&opts.tlsEnabled, "tls.enabled", false, "enable native TLS termination")
	flags.StringVar(&opts.tlsCertFile, "tls.cert_file", "", "native TLS certificate file")
	flags.StringVar(&opts.tlsKeyFile, "tls.key_file", "", "native TLS private key file")
	flags.StringVar(&opts.tlsMinVersion, "tls.min_version", "", "native TLS minimum version (1.2 or 1.3)")
	flags.BoolVar(&opts.relayEnabled, "server.relay.enabled", false, "enable authenticated server-node relay")
	flags.StringVar(&opts.relayListen, "server.relay.listen", "", "server-node relay listen address")
	flags.StringVar(&opts.relayEndpoint, "server.relay.endpoint", "", "server-node relay endpoint")
	flags.StringVar(&opts.relayCA, "server.relay.ca", "", "server-node relay client CA file")
	flags.StringVar(&opts.relayCert, "server.relay.cert", "", "server-node relay certificate file")
	flags.StringVar(&opts.relayKey, "server.relay.key", "", "server-node relay private key file")
	flags.StringVar(&opts.relayServerName, "server.relay.server_name", "", "server-node relay TLS server name")
	flags.StringVar(&opts.relayNodeToken, "server.relay.node_token", "", "server-node relay token")
	flags.BoolVar(&opts.webSSHEnabled, "server.webssh.enabled", false, "enable browser WebSSH/SFTP sessions")
	flags.DurationVar(&opts.webSSHTicketTTL, "server.webssh.ticket_ttl", 0, "WebSSH one-time ticket lifetime")
	flags.DurationVar(&opts.webSSHSessionTTL, "server.webssh.session_ttl", 0, "maximum WebSSH session lifetime")
	flags.IntVar(&opts.webSSHMaxActiveSessionsUser, "server.webssh.max_active_sessions_per_user", 0, "maximum active WebSSH sessions per user")
	flags.DurationVar(&opts.webSSHOpenTimeout, "server.webssh.open_timeout", 0, "WebSSH Agent relay open timeout")
	flags.DurationVar(&opts.webSSHIdleTimeout, "server.webssh.idle_timeout", 0, "WebSSH browser idle timeout")
	flags.IntVar(&opts.webSSHMaxMessageBytes, "server.webssh.max_message_bytes", 0, "maximum WebSSH WebSocket message size")
	flags.BoolVar(&opts.proxyEntryEnabled, "server.proxy_entry.enabled", false, "enable the tp-* managed HTTP proxy entry")
	flags.StringVar(&opts.proxyEntryListen, "server.proxy_entry.listen", "", "internal plaintext listener for the proxy entry")
	flags.StringVar(&opts.proxyEntryDomainSuffix, "server.proxy_entry.domain_suffix", "", "domain suffix for tp-* proxy routes")
	flags.DurationVar(&opts.proxyEntryConnectTimeout, "server.proxy_entry.connect_timeout", 0, "proxy entry stream open timeout")
	flags.DurationVar(&opts.proxyEntryIdleTimeout, "server.proxy_entry.idle_timeout", 0, "proxy entry tunnel idle timeout")
	flags.IntVar(&opts.proxyEntryMaxTunnels, "server.proxy_entry.max_concurrent_tunnels", 0, "maximum concurrent proxy tunnels, 0 means unlimited")
	flags.StringVar(&opts.clientServerURL, "client.server_url", "", "Client WebSocket URL")
	flags.StringVar(&opts.clientToken, "client.token", "", "Client bearer token")
	root.AddCommand(factory(opts)...)
	return root
}

func serverCommands(opts *rootOptions) []*cobra.Command {
	return []*cobra.Command{
		configCommand(opts, "run", "start the TunnelMesh server", func(cmd *cobra.Command, cfg config.Config) error {
			// Certificate material is validated before storage is opened: reading a
			// local key pair is cheap, while OpenConfig can run a full schema
			// migration. Checking TLS first keeps `run` failing fast and stops a
			// missing certificate from being reported as a storage timeout.
			if err := server.PreflightNativeTLS(cfg.TLS); err != nil {
				return err
			}
			db, err := storage.OpenConfig(cmd.Context(), cfg.Storage)
			if err != nil {
				return err
			}
			defer db.Close()
			runtime, err := server.NewServerRuntime(db, server.AgentSessionConfig{}, server.RuntimeConfig{Security: cfg.Security, TLS: cfg.TLS, Relay: cfg.Server.Relay, NodeID: cfg.Node.ID, DynamicSuffix: cfg.Server.DynamicSuffix, Stream: cfg.Server.Stream, AuthorizationCache: cfg.Server.AuthorizationCache, Downloads: cfg.Downloads, WebSSH: cfg.Server.WebSSH, VPN: cfg.Server.VPN, ProxyEntry: cfg.Server.ProxyEntry, TrustedProxies: cfg.Server.TrustedProxies})
			if err != nil {
				return err
			}
			defer runtime.Close()
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "server starting on %s in %s mode\n", cfg.Server.HTTPAddr, cfg.Mode)
			// The proxy entry listens on a separate internal port, so operators
			// need to see where OpenResty must relay to. Nothing is printed when
			// it is disabled, matching how the other optional listeners behave.
			if runtime.ProxyEntryEnabled {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "proxy entry listening on %s for *.%s\n", cfg.Server.ProxyEntry.Listen, cfg.Server.ProxyEntry.DomainSuffix)
			}
			return runtime.Serve(cmd.Context(), cfg.Server.HTTPAddr)
		}),
		configCommand(opts, "check-config", "validate configuration and exit", func(cmd *cobra.Command, _ config.Config) error {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "configuration valid")
			return nil
		}),
		doctorServerCommand(opts),
		configCommand(opts, "init-db", "initialize the configured database", func(cmd *cobra.Command, cfg config.Config) error {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "database initialization requested for %s\n", cfg.Storage.Driver)
			return nil
		}),
		{
			Use:   "init-node-id",
			Short: "generate and persist the cluster node identity",
			RunE: func(cmd *cobra.Command, _ []string) error {
				if strings.TrimSpace(opts.configFile) == "" {
					return errors.New("--config is required")
				}
				id, err := config.InitializeNodeID(opts.configFile, opts.nodeIDPath)
				if err != nil {
					return err
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "node identity: %s\n", id)
				return nil
			},
		},
		configCommand(opts, "print-config", "print the effective redacted configuration", func(cmd *cobra.Command, cfg config.Config) error {
			data, err := cfg.RedactedJSON()
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return nil
		}),
		adminCommand(opts),
	}
}

func adminCommand(opts *rootOptions) *cobra.Command {
	admin := &cobra.Command{Use: "admin", Short: "administrator operations"}
	bootstrap := &cobra.Command{
		Use:   "bootstrap",
		Short: "create the first administrator account",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cmd.Context(), config.ConfigOptions{ConfigFile: opts.configFile, CLI: changedFlags(cmd, opts)})
			if err != nil {
				return err
			}
			db, err := storage.OpenConfig(cmd.Context(), cfg.Storage)
			if err != nil {
				return err
			}
			defer db.Close()
			creds, err := auth.NewBootstrapService(db).BootstrapAdmin(cmd.Context())
			if err != nil {
				return err
			}
			// Credentials are deliberately emitted only to the command's output
			// stream, never logs or HTTP responses.
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "admin username: %s\nadmin password: %s\n", creds.Username, creds.Password)
			return nil
		},
	}
	regenerate := &cobra.Command{
		Use:   "regenerate-credentials",
		Short: "rotate administrator credentials and revoke old sessions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			confirm, err := cmd.Flags().GetBool("confirm")
			if err != nil {
				return err
			}
			cfg, err := config.Load(cmd.Context(), config.ConfigOptions{ConfigFile: opts.configFile, CLI: changedFlags(cmd, opts)})
			if err != nil {
				return err
			}
			db, err := storage.OpenConfig(cmd.Context(), cfg.Storage)
			if err != nil {
				return err
			}
			defer db.Close()
			creds, err := auth.NewBootstrapService(db).RegenerateCredentials(cmd.Context(), confirm)
			if err != nil {
				return err
			}
			// Credentials are deliberately emitted only to the command's output
			// stream, never logs or HTTP responses.
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "admin username: %s\nadmin password: %s\n", creds.Username, creds.Password)
			return nil
		},
	}
	regenerate.Flags().Bool("confirm", false, "confirm credential rotation")
	admin.AddCommand(bootstrap, regenerate)
	return admin
}

func doctorServerCommand(opts *rootOptions) *cobra.Command {
	return configCommand(opts, "doctor", "validate configuration, storage, and readiness", func(cmd *cobra.Command, cfg config.Config) error {
		// Diagnostics must not create or migrate a database as a side effect.
		storageConfig := cfg.Storage
		storageConfig.AutoInit = false
		db, err := storage.OpenConfig(cmd.Context(), storageConfig)
		if err != nil {
			return fmt.Errorf("storage check: %w", err)
		}
		defer db.Close()
		if err := db.Ping(cmd.Context()); err != nil {
			return fmt.Errorf("storage ping: %w", err)
		}

		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "configuration: ok")
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "storage: ok (%s)\n", cfg.Storage.Driver)
		return nil
	})
}

func doctorEndpointCommand(opts *rootOptions, component string) *cobra.Command {
	return configCommand(opts, "doctor", "validate configuration and Server health", func(cmd *cobra.Command, cfg config.Config) error {
		serverURL := cfg.Agent.ServerURL
		if component == "client" {
			serverURL = cfg.Client.ServerURL
		}
		healthURL, err := serverHealthURL(serverURL)
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, healthURL, nil)
		if err != nil {
			return fmt.Errorf("build health request: %w", err)
		}
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
		if err != nil {
			return fmt.Errorf("server health check: %w", err)
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode > 299 {
			return fmt.Errorf("server health check: unexpected status %d", response.StatusCode)
		}

		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "configuration: ok")
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "server health: ok")
		return nil
	})
}

func serverHealthURL(serverURL string) (string, error) {
	parsed, err := url.Parse(serverURL)
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
	parsed.Path = "/health/ready"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func agentCommands(opts *rootOptions) []*cobra.Command {
	return []*cobra.Command{
		configCommand(opts, "run", "start the TunnelMesh agent", func(cmd *cobra.Command, cfg config.Config) error {
			if strings.TrimSpace(cfg.Agent.ServerURL) == "" || strings.TrimSpace(cfg.Agent.Token) == "" || strings.TrimSpace(cfg.Agent.ID) == "" {
				return fmt.Errorf("agent run requires agent.server_url, agent.token, and agent.id")
			}
			nodeID := cfg.Node.ID
			if strings.TrimSpace(nodeID) == "" {
				nodeID = cfg.Agent.ID
			}
			echoer, echoErr := startAgentEchoer(cfg.Agent.Streams)
			if echoErr != nil {
				// Non-fatal on purpose. The agent still serves every tunnel it
				// already had, and the missing capability makes the server refuse
				// to sign ICMP peers, which is where an operator looks next.
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "agent icmp echo is unavailable and will not be advertised: %v\n", echoErr)
			}
			if echoer != nil {
				defer func() { _ = echoer.Close() }()
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "agent connecting to %s in %s mode\n", cfg.Agent.ServerURL, cfg.Mode)
			return agent.RunConnectionPool(cmd.Context(), agent.WebSocketPoolOptions{
				ServerURL: cfg.Agent.ServerURL, Token: cfg.Agent.Token, AgentID: cfg.Agent.ID,
				NodeID: nodeID, InstanceID: cfg.Agent.InstanceID, Epoch: 1,
				Collector: agent.NewMetadataCollector(cfg.Agent.Metadata),
				Factory:   NewAgentDispatcherFactory(cfg.Agent.Streams, echoer),
				Min:       cfg.Agent.Connections.Min, Max: cfg.Agent.Connections.Max,
				HighWatermark: cfg.Agent.Connections.HighWatermark, LowWatermark: cfg.Agent.Connections.LowWatermark,
				EvaluationInterval: cfg.Agent.Connections.EvaluationInterval,
				Cooldown:           cfg.Agent.Connections.Cooldown,
			})
		}),
		configCommand(opts, "register", "register this agent", func(cmd *cobra.Command, cfg config.Config) error {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "agent registration requested for %s\n", effectiveAgentID(cfg))
			return nil
		}),
		configCommand(opts, "check-config", "validate configuration and exit", func(cmd *cobra.Command, _ config.Config) error {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "configuration valid")
			return nil
		}),
		doctorEndpointCommand(opts, "agent"),
		configCommand(opts, "id", "print the configured agent identity", func(cmd *cobra.Command, cfg config.Config) error {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), effectiveAgentID(cfg))
			return nil
		}),
	}
}

// agentEchoOpener is a variable so a test can reproduce the permission failure a
// real host produces when net.ipv4.ping_group_range excludes the process gid.
var agentEchoOpener = agent.OpenEchoer

// startAgentEchoer opens the unprivileged ping socket the echo capability needs.
// It returns a nil engine and a nil error when the configuration leaves ICMP off,
// so a host without the sysctl is never penalised for a feature it does not use.
// When the operator did ask for it and the host refused, the error is returned
// for the caller to report and the engine stays nil, which is what keeps the
// capability out of the advertisement.
func startAgentEchoer(streams config.AgentStreamConfig) (*agent.Echoer, error) {
	if !streams.ICMPEnabled {
		return nil, nil
	}
	echoer, err := agentEchoOpener(agent.EchoerConfig{
		BindAddress:   streams.ICMPBindAddress,
		Timeout:       streams.ICMPTimeout,
		MaxConcurrent: streams.ICMPMaxConcurrent,
	})
	if err != nil {
		return nil, err
	}
	return echoer, nil
}

func NewAgentDispatcherFactory(streams config.AgentStreamConfig, echoer *agent.Echoer) agent.ConnectionSessionFactory {
	return func(session *agent.Session, _ string) agent.SessionFrameHandler {
		// Readiness is the engine, not the configuration: a host that refused the
		// ping socket must not advertise a stream it cannot serve.
		session.SetCapabilities(agent.AgentStreamCapabilities(streams, echoer != nil))
		return agent.NewStreamDispatcherWithConfig(agent.Dialer{ICMPEcho: echoer}, nil, session.Send, agent.DialExecutorConfig{
			MaxConcurrent:  streams.MaxConcurrentDials,
			MaxPending:     streams.MaxPendingDials,
			ConnectTimeout: streams.ConnectTimeout,
			OpenTimeout:    streams.OpenTimeout,
		}, nil)
	}
}

func clientCommands(opts *rootOptions) []*cobra.Command {
	result := []*cobra.Command{
		configCommand(opts, "login", "authenticate the TunnelMesh client", func(cmd *cobra.Command, cfg config.Config) error {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "login requested for %s\n", cfg.Client.ServerURL)
			return nil
		}),
		configCommand(opts, "run", "start the configured client tunnels", runClientTunnels),
		configCommand(opts, "check-config", "validate configuration and exit", func(cmd *cobra.Command, _ config.Config) error {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "configuration valid")
			return nil
		}),
		doctorEndpointCommand(opts, "client"),
		configCommand(opts, "agent", "inspect an agent", clientShellRun("agent")),
		configCommand(opts, "stop", "stop a local tunnel", clientShellRun("stop")),
		configCommand(opts, "status", "show local tunnel status", clientShellRun("status")),
	}
	forward := &cobra.Command{Use: "forward", Short: "create a local forward"}
	forward.AddCommand(clientForwardCommand(opts, "tcp"), clientForwardCommand(opts, "udp"), clientForwardCommand(opts, "http"), clientSOCKS5ForwardCommand(opts), clientHTTPProxyForwardCommand(opts))
	forward.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg, err := loadClientConfig(cmd, opts)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "forward requested in %s mode\n", cfg.Mode)
		return nil
	}
	publish := &cobra.Command{Use: "publish", Short: "publish a managed route"}
	publish.AddCommand(clientForwardCommand(opts, "http"))
	publish.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg, err := loadClientConfig(cmd, opts)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "publish requested in %s mode\n", cfg.Mode)
		return nil
	}
	proxy := &cobra.Command{Use: "proxy", Short: "proxy raw bytes over a tunnel"}
	proxy.AddCommand(clientProxyCommand(opts, "tcp"))
	proxy.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg, err := loadClientConfig(cmd, opts)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "proxy requested in %s mode\n", cfg.Mode)
		return nil
	}
	tunnel := &cobra.Command{Use: "tunnel", Short: "manage configured tunnels"}
	tunnel.AddCommand(configCommand(opts, "stop", "stop a configured tunnel", clientShellRun("tunnel stop")), configCommand(opts, "status", "show configured tunnel status", clientShellRun("tunnel status")))
	result = append(result, tunnel, forward, publish, proxy)
	return result
}

func clientShellRun(name string) func(*cobra.Command, config.Config) error {
	return func(cmd *cobra.Command, cfg config.Config) error {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s requested in %s mode\n", name, cfg.Mode)
		return nil
	}
}

func loadClientConfig(cmd *cobra.Command, opts *rootOptions) (config.Config, error) {
	return config.Load(cmd.Context(), config.ConfigOptions{ConfigFile: opts.configFile, CLI: changedFlags(cmd, opts)})
}

func clientForwardCommand(opts *rootOptions, proto string) *cobra.Command {
	cmd := &cobra.Command{Use: proto, Short: "local " + proto + " forward"}
	var listen, targetHost, agentID string
	var targetPort int
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:0", "local listen address")
	cmd.Flags().StringVar(&targetHost, "target-host", "", "target host on the Agent network")
	cmd.Flags().IntVar(&targetPort, "target-port", 0, "target port on the Agent network")
	cmd.Flags().StringVar(&agentID, "agent", "", "Agent identity")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg, err := loadClientConfig(cmd, opts)
		if err != nil {
			return err
		}
		if targetHost == "" || targetPort < 1 || targetPort > 65535 {
			return fmt.Errorf("%s forward requires --target-host and --target-port", proto)
		}
		if strings.TrimSpace(cfg.Client.ServerURL) == "" || strings.TrimSpace(cfg.Client.Token) == "" {
			return fmt.Errorf("%s forward requires client.server_url and client.token", proto)
		}
		if len(cfg.Client.Tunnels) > 0 {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "loaded %d configured tunnel(s)\n", len(cfg.Client.Tunnels))
		}
		var active io.Closer
		defer func() {
			if active != nil {
				_ = active.Close()
			}
		}()
		return runClientWebSocket(cmd.Context(), cfg.Client.ServerURL, cfg.Client.Token, func(session *client.Session) error {
			if active != nil {
				_ = active.Close()
				active = nil
			}
			opener := client.NewSessionOpener(session)
			switch proto {
			case "tcp":
				forward, err := client.NewTCPForward(opener, client.TCPForwardConfig{ListenAddr: listen, AgentID: agentID, TargetHost: targetHost, TargetPort: targetPort})
				if err != nil {
					return err
				}
				if err := forward.Start(cmd.Context()); err != nil {
					return err
				}
				active = forward
			case "udp":
				forward, err := client.NewUDPForward(opener, client.UDPForwardConfig{ListenAddr: listen, AgentID: agentID, TargetHost: targetHost, TargetPort: targetPort})
				if err != nil {
					return err
				}
				if err := forward.Start(cmd.Context()); err != nil {
					return err
				}
				active = forward
			case "http":
				forward, err := client.NewHTTPForward(opener, client.HTTPForwardConfig{ListenAddr: listen, AgentID: agentID, TargetHost: targetHost, TargetPort: targetPort})
				if err != nil {
					return err
				}
				if err := forward.Start(cmd.Context()); err != nil {
					return err
				}
				active = forward
			default:
				return fmt.Errorf("unsupported forward protocol %q", proto)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s forward %s -> %s:%d via %s\n", proto, listen, targetHost, targetPort, agentID)
			return nil
		})
	}
	return cmd
}

func clientSOCKS5ForwardCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "socks5", Short: "local SOCKS5 CONNECT forward"}
	var listen, agentID, authMode string
	var allowRemote bool
	var authURL string
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:0", "local listen address")
	cmd.Flags().StringVar(&agentID, "agent", "", "Agent identity")
	cmd.Flags().StringVar(&authMode, "auth", "none", "SOCKS5 auth mode (none or password)")
	cmd.Flags().BoolVar(&allowRemote, "allow-remote", false, "allow non-loopback listening")
	cmd.Flags().StringVar(&authURL, "auth-url", "", "remote validation URL")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg, err := loadClientConfig(cmd, opts)
		if err != nil {
			return err
		}
		if strings.TrimSpace(cfg.Client.ServerURL) == "" || strings.TrimSpace(cfg.Client.Token) == "" {
			return fmt.Errorf("socks5 forward requires client.server_url and client.token")
		}
		if strings.TrimSpace(agentID) == "" {
			return fmt.Errorf("socks5 forward requires --agent")
		}
		if authMode != "none" && authMode != "password" {
			return fmt.Errorf("socks5 forward requires --auth none or --auth password")
		}
		username := ""
		password := ""
		if authMode == "password" {
			username = os.Getenv("TUNNELMESH_SOCKS5_USERNAME")
			password = os.Getenv("TUNNELMESH_SOCKS5_PASSWORD")
			if username == "" || password == "" {
				return fmt.Errorf("socks5 password auth requires TUNNELMESH_SOCKS5_USERNAME and TUNNELMESH_SOCKS5_PASSWORD")
			}
		}
		var active io.Closer
		defer func() {
			if active != nil {
				_ = active.Close()
			}
		}()
		return runClientWebSocket(cmd.Context(), cfg.Client.ServerURL, cfg.Client.Token, func(session *client.Session) error {
			if active != nil {
				_ = active.Close()
				active = nil
			}
			forward, err := client.NewSOCKS5Forward(client.NewSessionOpener(session), client.SOCKS5ForwardConfig{
				ListenAddr:  listen,
				AgentID:     agentID,
				AllowRemote: allowRemote,
				AuthMode:    client.SOCKS5AuthMode(authMode),
				Username:    username,
				Password:    password,
				AuthURL:     authURL,
				RemoteValidation: client.RemoteValidationCacheConfig{
					Endpoint:    authURL,
					PositiveTTL: cfg.Client.RemoteValidation.PositiveTTL,
					NegativeTTL: cfg.Client.RemoteValidation.NegativeTTL,
					Timeout:     cfg.Client.RemoteValidation.Timeout,
					MaxEntries:  cfg.Client.RemoteValidation.MaxEntries,
				},
			})
			if err != nil {
				return err
			}
			if err := forward.Start(cmd.Context()); err != nil {
				return err
			}
			active = forward
			addr := listen
			if forward.Addr() != nil {
				addr = forward.Addr().String()
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "socks5 forward %s -> agent %s auth %s\n", addr, agentID, authMode)
			return nil
		})
	}
	return cmd
}

func clientHTTPProxyForwardCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "http-proxy", Short: "local standard HTTP proxy forward"}
	var listen, agentID, authMode string
	var allowRemote bool
	var authURL string
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:8080", "local listen address")
	cmd.Flags().StringVar(&agentID, "agent", "", "Agent identity")
	cmd.Flags().StringVar(&authMode, "auth", "none", "HTTP proxy auth mode (none or basic)")
	cmd.Flags().BoolVar(&allowRemote, "allow-remote", false, "allow non-loopback listening")
	cmd.Flags().StringVar(&authURL, "auth-url", "", "remote validation URL")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg, err := loadClientConfig(cmd, opts)
		if err != nil {
			return err
		}
		if strings.TrimSpace(cfg.Client.ServerURL) == "" || strings.TrimSpace(cfg.Client.Token) == "" {
			return fmt.Errorf("http-proxy forward requires client.server_url and client.token")
		}
		if strings.TrimSpace(agentID) == "" {
			return fmt.Errorf("http-proxy forward requires --agent")
		}
		if authMode != "none" && authMode != "basic" {
			return fmt.Errorf("http-proxy forward requires --auth none or --auth basic")
		}
		username := ""
		password := ""
		if authMode == "basic" {
			username = os.Getenv("TUNNELMESH_HTTP_PROXY_USERNAME")
			password = os.Getenv("TUNNELMESH_HTTP_PROXY_PASSWORD")
			if username == "" || password == "" {
				return fmt.Errorf("http-proxy basic auth requires TUNNELMESH_HTTP_PROXY_USERNAME and TUNNELMESH_HTTP_PROXY_PASSWORD")
			}
		}
		var active io.Closer
		defer func() {
			if active != nil {
				_ = active.Close()
			}
		}()
		return runClientWebSocket(cmd.Context(), cfg.Client.ServerURL, cfg.Client.Token, func(session *client.Session) error {
			if active != nil {
				_ = active.Close()
				active = nil
			}
			forward, err := client.NewHTTPProxyForward(client.NewSessionOpener(session), client.HTTPProxyForwardConfig{
				ListenAddr:  listen,
				AgentID:     agentID,
				AllowRemote: allowRemote,
				AuthMode:    client.HTTPProxyAuthMode(authMode),
				Username:    username,
				Password:    password,
				AuthURL:     authURL,
				RemoteValidation: client.RemoteValidationCacheConfig{
					Endpoint:    authURL,
					PositiveTTL: cfg.Client.RemoteValidation.PositiveTTL,
					NegativeTTL: cfg.Client.RemoteValidation.NegativeTTL,
					Timeout:     cfg.Client.RemoteValidation.Timeout,
					MaxEntries:  cfg.Client.RemoteValidation.MaxEntries,
				},
			})
			if err != nil {
				return err
			}
			if err := forward.Start(cmd.Context()); err != nil {
				return err
			}
			active = forward
			addr := listen
			if forward.Addr() != nil {
				addr = forward.Addr().String()
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "http-proxy forward %s -> agent %s auth %s\n", addr, agentID, authMode)
			return nil
		})
	}
	return cmd
}

func clientProxyCommand(opts *rootOptions, proto string) *cobra.Command {
	cmd := &cobra.Command{Use: proto, Short: "raw " + proto + " stdio proxy"}
	var targetHost, agentID string
	var targetPort int
	cmd.Flags().StringVar(&targetHost, "target-host", "", "target host on the Agent network")
	cmd.Flags().IntVar(&targetPort, "target-port", 0, "target port on the Agent network")
	cmd.Flags().StringVar(&agentID, "agent", "", "Agent identity")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg, err := loadClientConfig(cmd, opts)
		if err != nil {
			return err
		}
		if targetHost == "" || targetPort < 1 || targetPort > 65535 {
			return fmt.Errorf("proxy %s requires --target-host and --target-port", proto)
		}
		if strings.TrimSpace(cfg.Client.ServerURL) == "" || strings.TrimSpace(cfg.Client.Token) == "" {
			return fmt.Errorf("proxy %s requires client.server_url and client.token", proto)
		}
		err = runClientWebSocket(cmd.Context(), cfg.Client.ServerURL, cfg.Client.Token, func(session *client.Session) error {
			stream, openErr := client.NewSessionOpener(session).OpenStream(cmd.Context(), client.StreamRequest{AgentID: agentID, Protocol: proto, TargetHost: targetHost, TargetPort: targetPort})
			if openErr != nil {
				return openErr
			}
			if proxyErr := client.ProxyStdio(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), stream); proxyErr != nil {
				return proxyErr
			}
			return errClientProxyComplete
		})
		if errors.Is(err, errClientProxyComplete) {
			return nil
		}
		return err
	}
	return cmd
}

func configCommand(opts *rootOptions, name, short string, run func(*cobra.Command, config.Config) error) *cobra.Command {
	return &cobra.Command{
		Use:   name,
		Short: short,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cmd.Context(), config.ConfigOptions{
				ConfigFile: opts.configFile,
				CLI:        changedFlags(cmd, opts),
				NodeIDPath: func() string {
					if name == "run" {
						return opts.nodeIDPath
					}
					return ""
				}(),
				PersistGeneratedNodeIDToConfig: name == "run" && opts.binaryName == "tunnelmesh-server",
				AgentInstanceIDPath: func() string {
					if name == "run" && opts.binaryName == "tunnelmesh-agent" {
						return config.DefaultAgentInstanceIDPath
					}
					return ""
				}(),
			})
			if err != nil {
				return err
			}
			return run(cmd, cfg)
		},
	}
}

func changedFlags(cmd *cobra.Command, opts *rootOptions) map[string]any {
	values := make(map[string]any)
	flags := cmd.Flags()
	if flags.Changed("mode") {
		values["mode"] = opts.mode
	}
	if flags.Changed("storage.driver") || flags.Changed("storage-driver") {
		values["storage.driver"] = opts.storage
	}
	if flags.Changed("storage.auto_init") || flags.Changed("storage-auto-init") {
		values["storage.auto_init"] = opts.autoInit
	}
	if flags.Changed("registry.type") || flags.Changed("registry") {
		values["registry.type"] = opts.registry
	}
	if flags.Changed("node.id") || flags.Changed("node-id") {
		values["node.id"] = opts.nodeID
	}
	if flags.Changed("server.tcp_bridge.enabled") || flags.Changed("tcp-bridge") {
		values["server.tcp_bridge.enabled"] = opts.bridge
	}
	if flags.Changed("server.tcp_bridge_enabled") {
		value, err := flags.GetBool("server.tcp_bridge_enabled")
		if err == nil {
			values["server.tcp_bridge_enabled"] = value
		}
	}
	if flags.Changed("security.allowed_hosts") {
		values["security.allowed_hosts"] = append([]string(nil), opts.allowedHosts...)
	}
	if flags.Changed("security.allowed_origins") {
		values["security.allowed_origins"] = append([]string(nil), opts.allowedOrigins...)
	}
	if flags.Changed("security.allow_legacy_connection_tokens") {
		values["security.allow_legacy_connection_tokens"] = opts.allowLegacyConnectionTokens
	}
	if flags.Changed("tls.enabled") {
		values["tls.enabled"] = opts.tlsEnabled
	}
	if flags.Changed("tls.cert_file") {
		values["tls.cert_file"] = opts.tlsCertFile
	}
	if flags.Changed("tls.key_file") {
		values["tls.key_file"] = opts.tlsKeyFile
	}
	if flags.Changed("tls.min_version") {
		values["tls.min_version"] = opts.tlsMinVersion
	}
	if flags.Changed("server.relay.enabled") {
		values["server.relay.enabled"] = opts.relayEnabled
	}
	if flags.Changed("server.relay.listen") {
		values["server.relay.listen"] = opts.relayListen
	}
	if flags.Changed("server.relay.endpoint") {
		values["server.relay.endpoint"] = opts.relayEndpoint
	}
	if flags.Changed("server.relay.ca") {
		values["server.relay.ca"] = opts.relayCA
	}
	if flags.Changed("server.relay.cert") {
		values["server.relay.cert"] = opts.relayCert
	}
	if flags.Changed("server.relay.key") {
		values["server.relay.key"] = opts.relayKey
	}
	if flags.Changed("server.relay.server_name") {
		values["server.relay.server_name"] = opts.relayServerName
	}
	if flags.Changed("server.dynamic_suffix") {
		values["server.dynamic_suffix"] = opts.dynamicSuffix
	}
	if flags.Changed("server.relay.node_token") {
		values["server.relay.node_token"] = opts.relayNodeToken
	}
	if flags.Changed("server.webssh.enabled") {
		values["server.webssh.enabled"] = opts.webSSHEnabled
	}
	if flags.Changed("server.webssh.ticket_ttl") {
		values["server.webssh.ticket_ttl"] = opts.webSSHTicketTTL
	}
	if flags.Changed("server.webssh.session_ttl") {
		values["server.webssh.session_ttl"] = opts.webSSHSessionTTL
	}
	if flags.Changed("server.webssh.max_active_sessions_per_user") {
		values["server.webssh.max_active_sessions_per_user"] = opts.webSSHMaxActiveSessionsUser
	}
	if flags.Changed("server.webssh.open_timeout") {
		values["server.webssh.open_timeout"] = opts.webSSHOpenTimeout
	}
	if flags.Changed("server.webssh.idle_timeout") {
		values["server.webssh.idle_timeout"] = opts.webSSHIdleTimeout
	}
	if flags.Changed("server.webssh.max_message_bytes") {
		values["server.webssh.max_message_bytes"] = opts.webSSHMaxMessageBytes
	}
	if flags.Changed("server.proxy_entry.enabled") {
		values["server.proxy_entry.enabled"] = opts.proxyEntryEnabled
	}
	if flags.Changed("server.proxy_entry.listen") {
		values["server.proxy_entry.listen"] = opts.proxyEntryListen
	}
	if flags.Changed("server.proxy_entry.domain_suffix") {
		values["server.proxy_entry.domain_suffix"] = opts.proxyEntryDomainSuffix
	}
	if flags.Changed("server.proxy_entry.connect_timeout") {
		values["server.proxy_entry.connect_timeout"] = opts.proxyEntryConnectTimeout
	}
	if flags.Changed("server.proxy_entry.idle_timeout") {
		values["server.proxy_entry.idle_timeout"] = opts.proxyEntryIdleTimeout
	}
	if flags.Changed("server.proxy_entry.max_concurrent_tunnels") {
		values["server.proxy_entry.max_concurrent_tunnels"] = opts.proxyEntryMaxTunnels
	}
	if flags.Changed("client.server_url") {
		values["client.server_url"] = opts.clientServerURL
	}
	if flags.Changed("client.token") {
		values["client.token"] = opts.clientToken
	}
	return values
}

var (
	runClientWebSocket   = client.RunWebSocket
	runClientSessionPool = func(ctx context.Context, serverURL, token string, onReady func(*client.Session) error, options client.WebSocketRunOptions) error {
		return client.RunWebSocketWithOptions(ctx, serverURL, token, onReady, options)
	}
	errClientProxyComplete = errors.New("client proxy complete")
)

func effectiveAgentID(cfg config.Config) string {
	if cfg.Agent.ID != "" {
		return cfg.Agent.ID
	}
	return cfg.Node.ID
}

// Execute is a convenience for command binaries that want a context-aware
// entrypoint while keeping main functions tiny.
func Execute(ctx context.Context, root *cobra.Command) error { return root.ExecuteContext(ctx) }
