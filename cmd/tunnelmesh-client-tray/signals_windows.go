//go:build tray && windows

package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/tunnelmesh/tunnelmesh/internal/tray"
	"github.com/tunnelmesh/tunnelmesh/internal/tray/native"
)

// installShutdownSignals turns Ctrl+C in a console into the same teardown as the menu.
//
// Windows has no SIGTERM, and closing a console window delivers a Ctrl+C event rather than a
// signal the Go runtime exposes for it. A tray started from a shortcut or the Run key simply
// exits, which is why the notification-area icon and the window close carry the shutdown
// paths that actually matter on this platform.
func installShutdownSignals(logger *log.Logger, app *tray.App, api *tray.APIServer, shellDone <-chan struct{}) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT)
	go func() {
		received := <-signals
		logger.Printf("received %s, shutting down", received)
		shutdownTray(logger, app, api, native.Stop, shellDone)
	}()
}
