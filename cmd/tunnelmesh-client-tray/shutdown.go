//go:build tray && (darwin || windows)

package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/tray"
)

// shutdownTray is the one teardown shared by a signal and, through the shell, by the menu.
//
// The order matters: the client stops first so the tunnels close cleanly, the local HTTP
// server goes next so no request can reach a runtime that is already gone, and only then is
// the shell asked to leave its loop. The watchdog exists because "leave your run loop" is a
// request rather than a guarantee, and a shell that answers nothing would otherwise hold the
// process open through a logout.
func shutdownTray(logger *log.Logger, app *tray.App, api *tray.APIServer, stopShell func(), shellDone <-chan struct{}) {
	if err := app.StopRuntime(); err != nil {
		logger.Printf("stopping the client failed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	_ = api.Shutdown(ctx)
	stopShell()
	select {
	case <-shellDone:
	case <-time.After(shutdownWatchdog):
		logger.Printf("the tray shell did not stop within %s, exiting anyway", shutdownWatchdog)
		os.Exit(0)
	}
}
