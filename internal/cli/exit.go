package cli

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/tunnelmesh/tunnelmesh/internal/client"
	"github.com/tunnelmesh/tunnelmesh/internal/workerproto"
)

// Main runs root under a context that SIGINT and SIGTERM cancel, and returns the
// process exit code.
//
// Before this, `run` executed under context.Background(): a signal killed the
// process outright, so deferred cleanup (releasing the client lock, closing
// listeners) never ran, and every failure exited 1. A supervisor needs the
// opposite on both counts: a clean stop it can tell apart from a crash, and a
// code that says whether restarting can help.
func Main(root *cobra.Command) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := Execute(ctx, root)
	code := ExitCodeFor(ctx, err)
	if code != workerproto.ExitOK {
		log.Print(err)
	}
	return code
}

// ExitCodeFor maps the error Execute returned to an exit code. A cancellation is
// a failure only when nobody asked us to stop: SIGTERM from systemd or from the
// supervisor must end the process with 0, or it would be restarted.
func ExitCodeFor(ctx context.Context, err error) int {
	if err == nil {
		return workerproto.ExitOK
	}
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return workerproto.ExitOK
	}
	return workerproto.ExitCodeOf(err)
}

// configError marks err as a configuration problem (exit 78).
func configError(err error) error {
	if err == nil {
		return nil
	}
	return &workerproto.ExitError{Code: workerproto.ExitConfig, Err: err}
}

// classifyStartError gives a failed Runtime.Start the exit code that matches its
// cause. A held lock is retryable because it is normal while the previous worker
// is still shutting down; everything else stays a plain failure.
func classifyStartError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, client.ErrRuntimeAlreadyRunning) {
		return &workerproto.ExitError{Code: workerproto.ExitLockHeld, Err: err}
	}
	return err
}
