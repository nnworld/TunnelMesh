// Package cli contains the thin Cobra command shells for the three binaries.
// Commands deliberately stop at configuration/orchestration boundaries; later
// tasks can attach services without changing their public command surface.
package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tunnelmesh/tunnelmesh/internal/auth"
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
	configFile string
	mode       string
	storage    string
	autoInit   bool
	registry   string
	nodeID     string
	bridge     bool
}

func newRoot(use string, factory func(*rootOptions) []*cobra.Command) *cobra.Command {
	opts := &rootOptions{}
	root := &cobra.Command{
		Use:           use,
		Short:         "TunnelMesh " + strings.TrimPrefix(use, "tunnelmesh-") + " command",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
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
	flags.BoolVar(&opts.bridge, "server.tcp_bridge.enabled", false, "enable TCP-over-WebSocket bridge")
	flags.BoolVar(&opts.bridge, "tcp-bridge", false, "enable TCP-over-WebSocket bridge")
	flags.Bool("server.tcp_bridge_enabled", false, "enable TCP-over-WebSocket bridge (flat spelling)")
	root.AddCommand(factory(opts)...)
	return root
}

func serverCommands(opts *rootOptions) []*cobra.Command {
	return []*cobra.Command{
		configCommand(opts, "run", "start the TunnelMesh server", func(cmd *cobra.Command, cfg config.Config) error {
			db, err := storage.OpenConfig(cmd.Context(), cfg.Storage)
			if err != nil {
				return err
			}
			defer db.Close()
			if _, err := server.NewServerRuntime(db, server.AgentSessionConfig{}); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "server ready in %s mode\n", cfg.Mode)
			return nil
		}),
		configCommand(opts, "check-config", "validate configuration and exit", func(cmd *cobra.Command, _ config.Config) error {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "configuration valid")
			return nil
		}),
		configCommand(opts, "init-db", "initialize the configured database", func(cmd *cobra.Command, cfg config.Config) error {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "database initialization requested for %s\n", cfg.Storage.Driver)
			return nil
		}),
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
	admin.AddCommand(regenerate)
	return admin
}

func agentCommands(opts *rootOptions) []*cobra.Command {
	return []*cobra.Command{
		configCommand(opts, "run", "start the TunnelMesh agent", func(cmd *cobra.Command, cfg config.Config) error {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "agent ready in %s mode\n", cfg.Mode)
			return nil
		}),
		configCommand(opts, "register", "register this agent", func(cmd *cobra.Command, cfg config.Config) error {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "agent registration requested for %s\n", effectiveAgentID(cfg))
			return nil
		}),
		configCommand(opts, "check-config", "validate configuration and exit", func(cmd *cobra.Command, _ config.Config) error {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "configuration valid")
			return nil
		}),
		configCommand(opts, "id", "print the configured agent identity", func(cmd *cobra.Command, cfg config.Config) error {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), effectiveAgentID(cfg))
			return nil
		}),
	}
}

func clientCommands(opts *rootOptions) []*cobra.Command {
	result := []*cobra.Command{
		configCommand(opts, "login", "authenticate the TunnelMesh client", func(cmd *cobra.Command, cfg config.Config) error {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "login requested for %s\n", cfg.Client.ServerURL)
			return nil
		}),
		configCommand(opts, "agent", "inspect an agent", clientShellRun("agent")),
		configCommand(opts, "stop", "stop a local tunnel", clientShellRun("stop")),
		configCommand(opts, "status", "show local tunnel status", clientShellRun("status")),
	}
	forward := &cobra.Command{Use: "forward", Short: "create a local forward"}
	forward.AddCommand(clientForwardCommand(opts, "tcp"), clientForwardCommand(opts, "udp"), clientForwardCommand(opts, "http"))
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
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s forward %s -> %s:%d via %s\n", proto, listen, targetHost, targetPort, agentID)
		if len(cfg.Client.Tunnels) > 0 {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "loaded %d configured tunnel(s)\n", len(cfg.Client.Tunnels))
		}
		return nil
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
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "proxy %s %s:%d via %s\n", proto, targetHost, targetPort, agentID)
		if cfg.Client.ServerURL == "" {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "note: configure client.server_url for a live WebSocket session")
		}
		return nil
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
	return values
}

func effectiveAgentID(cfg config.Config) string {
	if cfg.Agent.ID != "" {
		return cfg.Agent.ID
	}
	return cfg.Node.ID
}

// Execute is a convenience for command binaries that want a context-aware
// entrypoint while keeping main functions tiny.
func Execute(ctx context.Context, root *cobra.Command) error { return root.ExecuteContext(ctx) }
