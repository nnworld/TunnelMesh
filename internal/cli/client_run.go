package cli

import (
	"context"
	"path/filepath"
	"strings"
	"time"

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
//
// report is called with the Server version whenever the client is connected, which a
// supervisor reads as "this version works".
func runClientTunnels(ctx context.Context, cmd *cobra.Command, cfg config.Config, configPath string, report func(string)) error {
	runtime, err := client.NewRuntimeFromConfig(ctx, cfg, client.RuntimeOptions{
		ConfigPath: configPath,
		Stdout:     cmd.OutOrStdout(),
		// The package-level seam stays the injection point so the existing tests keep
		// substituting a fake transport without knowing about the runtime.
		Runner: runClientSessionPool,
	})
	if err != nil {
		// Everything NewRuntimeFromConfig rejects is a configuration problem: a
		// missing server URL or token, no tunnels or agent_id, an unusable transport.
		return configError(err)
	}
	if err := runtime.Start(ctx); err != nil {
		_ = runtime.Stop()
		return classifyStartError(err)
	}
	defer func() { _ = runtime.Stop() }()
	go reportReadiness(ctx, runtime, report)
	return runtime.Wait(ctx)
}

// readinessPoll is how often the runtime is asked whether it is connected. A
// connection and the Server's ack that follows it arrive within moments of each
// other, so a short poll is both cheap and precise enough.
const readinessPoll = 250 * time.Millisecond

// reportReadiness tells report each time the client is connected, until ctx ends.
// report is idempotent for an unchanged version, so repeating it is harmless, and
// repeating it is what carries a Server version that only arrives after the
// connection is up.
func reportReadiness(ctx context.Context, runtime *client.Runtime, report func(string)) {
	ticker := time.NewTicker(readinessPoll)
	defer ticker.Stop()
	for {
		if ready := runtime.Readiness(); ready.Ready {
			report(ready.ServerVersion)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// clientStateDir is where a client keeps files that must survive a restart: the
// directory of its instance id, which the service unit already makes writable.
func clientStateDir(cfg config.Config, configPath string) string {
	identity := strings.TrimSpace(cfg.Client.InstanceIDPath)
	if identity == "" {
		identity = client.DefaultClientInstanceIDPathForConfig(configPath)
	}
	return filepath.Dir(identity)
}
