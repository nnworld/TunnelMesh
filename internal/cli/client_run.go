package cli

import (
	"github.com/spf13/cobra"
	"github.com/tunnelmesh/tunnelmesh/internal/client"
	"github.com/tunnelmesh/tunnelmesh/internal/config"
)

// runClientTunnels starts every configured local ingress in one process. The
// listeners are intentionally independent from WebSocket reconnects; only the
// per-Agent session pool behind their openers is re-established.
//
// The assembly itself lives in internal/client so the system tray drives tunnels
// through the same code path. Two copies would eventually disagree about which
// protocols are supported, which environment variables supply proxy credentials, or
// when a deaf listener should end the run, and the disagreement would only surface
// in production. configPath is threaded through because it derives the advisory lock
// that keeps this command and the tray from serving one configuration twice.
func runClientTunnels(cmd *cobra.Command, cfg config.Config, configPath string) error {
	runtime, err := client.NewRuntimeFromConfig(cmd.Context(), cfg, client.RuntimeOptions{
		ConfigPath: configPath,
		Stdout:     cmd.OutOrStdout(),
		// The package-level seam stays the injection point so the existing tests keep
		// substituting a fake transport without knowing about the runtime.
		Runner: runClientSessionPool,
	})
	if err != nil {
		return err
	}
	if err := runtime.Start(cmd.Context()); err != nil {
		_ = runtime.Stop()
		return err
	}
	defer func() { _ = runtime.Stop() }()
	return runtime.Wait(cmd.Context())
}
