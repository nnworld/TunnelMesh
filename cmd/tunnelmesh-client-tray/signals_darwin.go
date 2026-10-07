//go:build tray && darwin

package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/tunnelmesh/tunnelmesh/internal/tray"
	"github.com/tunnelmesh/tunnelmesh/internal/tray/native"
)

// installShutdownSignals turns a logout or an explicit kill into a clean teardown.
//
// macOS delivers SIGTERM when the session ends, and the Cocoa run loop has to be stopped
// rather than abandoned: the loop returning is what releases the runtime lock and closes the
// tunnels instead of leaving them held by a process the system is about to remove.
func installShutdownSignals(logger *log.Logger, app *tray.App, api *tray.APIServer, shellDone <-chan struct{}) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		received := <-signals
		logger.Printf("received %s, shutting down", received)
		shutdownTray(logger, app, api, native.Stop, shellDone)
	}()
}
