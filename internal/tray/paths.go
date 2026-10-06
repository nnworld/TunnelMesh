// Package tray implements the macOS system-tray client: the preferences and
// configuration it persists, the loopback API the settings window talks to, and the
// supervision of the client runtime it hosts.
//
// Nothing in this package imports cgo or Cocoa. The native shell lives in
// internal/tray/native behind the "tray" build tag and depends on this package, not
// the other way round, so all of the tray's logic stays testable on every platform
// and in a CGO_ENABLED=0 build.
package tray

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/tunnelmesh/tunnelmesh/internal/client"
)

// ConfigDirName is the directory name under the user's configuration home.
const ConfigDirName = "tunnelmesh"

const (
	// ClientConfigFileName is the configuration the tray and `client run` share.
	ClientConfigFileName = "client.yaml"
	// PrefsFileName holds tray-only interface preferences. They are deliberately not
	// part of client.yaml: that file is validated by config.Validate, and unknown
	// interface keys there would either be rejected or silently ignored.
	PrefsFileName = "tray.json"
	// LockFileName mirrors the runtime's advisory lock name so the tray can show an
	// operator which file is contested. It is not a second source of truth: Paths
	// derives the lock through client.LockPathForConfig.
	LockFileName = client.LockFileName
)

// Paths are the resolved filesystem locations the tray reads and writes.
type Paths struct {
	// PrefsDir is always the default configuration directory.
	//
	// Preferences record which configuration directory is active, so they have to be
	// readable before that directory is known. Storing them inside it would make the
	// setting unreadable the moment it pointed somewhere else.
	PrefsDir string
	Prefs    string
	// ConfigDir is the directory client.yaml lives in, selected by the preferences.
	ConfigDir  string
	ClientYAML string
	// Lock is derived from ClientYAML by the same function the runtime uses, so the
	// tray and `client run` can never disagree about which lock guards a configuration.
	Lock string
}

// DefaultConfigDir returns the documented default, ~/.config/tunnelmesh.
//
// XDG_CONFIG_HOME is honoured when set because that is the convention the path
// follows, but os.UserConfigDir is deliberately not used: on macOS it returns
// ~/Library/Application Support, which would silently move the configuration away
// from the documented location and from every example in the docs.
func DefaultConfigDir() (string, error) {
	if base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); base != "" {
		return filepath.Join(base, ConfigDirName), nil
	}
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", ConfigDirName), nil
}

// DefaultPaths resolves every location against the default configuration directory.
func DefaultPaths() (Paths, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return Paths{}, err
	}
	return buildPaths(dir)
}

// ResolvePaths resolves every location against an explicit configuration directory.
// A leading ~ is expanded so the value an operator types into the interface works.
func ResolvePaths(configDir string) (Paths, error) {
	expanded, err := ExpandHome(strings.TrimSpace(configDir))
	if err != nil {
		return Paths{}, err
	}
	if expanded == "" {
		return Paths{}, errors.New("tray: configuration directory is required")
	}
	return buildPaths(expanded)
}

func buildPaths(configDir string) (Paths, error) {
	prefsDir, err := DefaultConfigDir()
	if err != nil {
		return Paths{}, err
	}
	clientYAML := filepath.Join(configDir, ClientConfigFileName)
	return Paths{
		PrefsDir:   prefsDir,
		Prefs:      filepath.Join(prefsDir, PrefsFileName),
		ConfigDir:  configDir,
		ClientYAML: clientYAML,
		Lock:       client.LockPathForConfig(clientYAML),
	}, nil
}

// ExpandHome resolves a leading ~ against the user's home directory.
func ExpandHome(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
}

func homeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", errors.New("tray: cannot resolve the user home directory")
	}
	return home, nil
}
