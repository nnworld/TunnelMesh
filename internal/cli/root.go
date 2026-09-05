// Package cli contains the thin Cobra command shells for the three binaries.
// Commands deliberately stop at configuration/orchestration boundaries; later
// tasks can attach services without changing their public command surface.
package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
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
	}
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
	commands := []string{"login", "agent", "tunnel", "forward", "publish", "proxy", "stop", "status"}
	result := make([]*cobra.Command, 0, len(commands))
	for _, name := range commands {
		commandName := name
		result = append(result, configCommand(opts, commandName, "TunnelMesh client "+commandName, func(cmd *cobra.Command, cfg config.Config) error {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s requested in %s mode\n", commandName, cfg.Mode)
			return nil
		}))
	}
	return result
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
