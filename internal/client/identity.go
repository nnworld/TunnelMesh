package client

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultClientInstanceIDPath returns the packaged service's identity file.
// A writable override avoids permission problems on locked-down workstations.
func DefaultClientInstanceIDPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("ProgramData"), "TunnelMesh", "client-instance-id")
	}
	if runtime.GOOS == "darwin" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, "Library", "Application Support", "TunnelMesh", "client-instance-id")
		}
	}
	return "/var/lib/tunnelmesh-client/client-instance-id"
}

// EnsureClientInstanceID returns an existing identity or atomically creates
// one. The file is the source of truth, so retries never replace the value.
func EnsureClientInstanceID(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("client instance identity path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", err
	}
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", fmt.Errorf("generate client instance identity: %w", err)
	}
	id := "client-" + hex.EncodeToString(entropy[:])
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := file.WriteString(id + "\n"); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return id, nil
}
