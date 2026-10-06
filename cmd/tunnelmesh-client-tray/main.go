//go:build tray && darwin

// Command tunnelmesh-client-tray is the macOS menu-bar client.
//
// It hosts the same tunnels as `tunnelmesh-client run` in this process, shares the same
// ~/.config/tunnelmesh/client.yaml, and is mutually exclusive with it through the
// advisory lock the runtime derives from that path. Everything the operator can change
// lives in a WKWebView served from a loopback port by the Go process; the native shell
// only owns the status item, the window and the login item.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/tunnelmesh/tunnelmesh/internal/build"
	"github.com/tunnelmesh/tunnelmesh/internal/client"
	"github.com/tunnelmesh/tunnelmesh/internal/tray"
	"github.com/tunnelmesh/tunnelmesh/internal/tray/native"
	"github.com/tunnelmesh/tunnelmesh/internal/tray/webdist"
)

// shutdownTimeout bounds the teardown after the run loop ends. A logout delivers SIGTERM
// and expects the process to go away promptly; the tunnels close immediately, so this only
// has to cover the HTTP server and the lock release.
const shutdownTimeout = 5 * time.Second

// init pins the main goroutine to the process's main thread.
//
// NSApplication is main-thread only. Without this the Go scheduler is free to migrate the
// main goroutine onto another OS thread, and the first AppKit call then either deadlocks or
// silently misbehaves - the classic failure mode of a Cocoa app written in Go.
func init() { runtime.LockOSThread() }

func main() {
	var configDir string
	var showVersion bool
	flag.StringVar(&configDir, "config-dir", "", "configuration directory holding client.yaml (default ~/.config/tunnelmesh)")
	flag.BoolVar(&showVersion, "version", false, "print the build identity and exit")
	flag.Parse()

	if showVersion {
		fmt.Println(build.String())
		return
	}

	logger, closeLog := setupLogger(configDir)
	defer closeLog()
	if err := run(configDir, logger); err != nil {
		logger.Printf("tray exited: %v", err)
		os.Exit(1)
	}
}

// setupLogger mirrors diagnostics to stderr and to tray.log in the preferences directory.
//
// A login item has no terminal, so stderr disappears; without a file there is no way to
// find out why the client did not come up. The log holds no configuration content, only
// errors, and is written owner-only.
func setupLogger(configDir string) (*log.Logger, func()) {
	writers := []io.Writer{os.Stderr}
	closer := func() {}
	if dir, err := resolveLogDir(configDir); err == nil {
		if err := os.MkdirAll(dir, 0o750); err == nil {
			path := filepath.Join(dir, "tray.log")
			if file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
				writers = append(writers, file)
				closer = func() { _ = file.Close() }
			}
		}
	}
	return log.New(io.MultiWriter(writers...), "tunnelmesh-tray ", log.LstdFlags), closer
}

func resolveLogDir(configDir string) (string, error) {
	if override := strings.TrimSpace(configDir); override != "" {
		paths, err := tray.ResolvePaths(override)
		if err != nil {
			return "", err
		}
		return paths.PrefsDir, nil
	}
	paths, err := tray.DefaultPaths()
	if err != nil {
		return "", err
	}
	return paths.PrefsDir, nil
}

func run(configDir string, logger *log.Logger) error {
	app, err := tray.NewApp(tray.AppOptions{
		ConfigDir:   configDir,
		Autostart:   native.NewAutostart(),
		OpenURL:     native.OpenURL,
		SystemProbe: systemProbe,
		OnPreferencesChanged: func(prefs tray.Preferences) {
			// The shell asks Go what a window close means, but pushing the value too keeps
			// the native side from calling back for something it already knows.
			native.SetMinimizeToTray(prefs.MinimizeToTray)
		},
	})
	if err != nil {
		return err
	}
	defer func() { _ = app.Close() }()

	assets, err := webdist.FS()
	if err != nil {
		return fmt.Errorf("load the settings interface: %w", err)
	}
	if !webdist.Index() {
		logger.Printf("the settings interface is not bundled; the window will show build instructions")
	}
	api, err := tray.NewAPIServer(tray.APIOptions{App: app, Assets: assets})
	if err != nil {
		return err
	}
	if err := api.Start(); err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = api.Shutdown(ctx)
	}()
	logger.Printf("local interface listening on %s", api.Addr())

	prefs, err := app.Preferences()
	if err != nil {
		return err
	}
	native.SetMinimizeToTray(prefs.MinimizeToTray)
	if err := app.SyncAutostart(); err != nil {
		// Not fatal: an unsigned or moved bundle is refused by SMAppService, and refusing
		// to run the tunnels over a login item would be the wrong trade.
		logger.Printf("launch at login could not be applied: %v", err)
	}

	if err := app.StartRuntime(context.Background()); err != nil {
		logStartFailure(logger, err)
	}

	native.SetHandlers(shellHandlers(app, logger))
	installShutdownSignals(logger, app, api)

	shell := native.DefaultConfig(api.URL())
	applyMenuLabels(&shell, prefs.Language)
	shell.MinimizeToTray = prefs.MinimizeToTray
	logger.Printf("starting the menu bar shell")
	if err := native.Run(shell); err != nil {
		return err
	}
	logger.Printf("menu bar shell stopped")
	return nil
}

// shellHandlers wires the menu and the window close into the application layer.
//
// MinimizeToTray is a callback rather than a value captured at start-up: the operator can
// change it in the general tab while the window is open, and the very next close has to
// honour the new choice.
func shellHandlers(app *tray.App, logger *log.Logger) native.Handlers {
	return native.Handlers{
		OpenMain: func() { logger.Printf("settings window opened from the menu") },
		OpenWebsite: func() error {
			if err := app.OpenWebsite(); err != nil {
				logger.Printf("opening the website failed: %v", err)
				return err
			}
			return nil
		},
		Quit: func() {
			logger.Printf("quit requested")
			app.RequestQuit()
			if err := app.StopRuntime(); err != nil {
				logger.Printf("stopping the client on quit failed: %v", err)
			}
			native.Stop()
		},
		MinimizeToTray: func() bool {
			prefs, err := app.Preferences()
			if err != nil {
				// Falling back to "hide" keeps the tunnels up: quitting because a
				// preferences file is unreadable would be the more damaging mistake.
				logger.Printf("reading the close behaviour failed, keeping the client running: %v", err)
				return true
			}
			return prefs.MinimizeToTray
		},
		WindowVisibilityChanged: func(visible bool) {
			if !visible {
				logger.Printf("settings window hidden")
			}
		},
	}
}

// logStartFailure explains why the client did not come up without dumping a stack.
func logStartFailure(logger *log.Logger, err error) {
	switch {
	case errors.Is(err, client.ErrRuntimeAlreadyRunning):
		logger.Printf("client not started: %v", err)
	case errors.Is(err, tray.ErrConfigUnusable):
		logger.Printf("client not started, the configuration needs attention: %v", err)
	default:
		logger.Printf("client not started: %v", err)
	}
}

// installShutdownSignals turns a logout or an explicit kill into a clean teardown.
func installShutdownSignals(logger *log.Logger, app *tray.App, api *tray.APIServer) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		received := <-signals
		logger.Printf("received %s, shutting down", received)
		if err := app.StopRuntime(); err != nil {
			logger.Printf("stopping the client failed: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = api.Shutdown(ctx)
		// Ends the Cocoa run loop so Run returns and main can exit.
		native.Stop()
	}()
}

// systemProbe reports the host for the about tab.
func systemProbe() tray.SystemInfo {
	info := native.System()
	return tray.SystemInfo{GOOS: runtime.GOOS, Arch: info.Arch, OSVersion: info.OSVersion}
}

// applyMenuLabels localizes the status-item menu.
//
// The menu is built before the webview reports its resolved locale, so the shell's own
// system language is the only signal available at that moment.
func applyMenuLabels(shell *native.Config, preference string) {
	chinese := strings.HasPrefix(strings.ToLower(preference), "zh")
	if preference == tray.LanguageSystem {
		language := strings.ToLower(native.PreferredLanguage())
		chinese = strings.HasPrefix(language, "zh")
	}
	if chinese {
		shell.OpenMainLabel = "打开主界面"
		shell.OpenWebsiteLabel = "打开官网首页"
		shell.QuitLabel = "退出"
		return
	}
	shell.OpenMainLabel = "Open Dashboard"
	shell.OpenWebsiteLabel = "Open Website"
	shell.QuitLabel = "Quit"
}
