//go:build unix

package config

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

// preserveOwnership keeps an atomic replacement readable by the same service
// account as the original configuration file.
func preserveOwnership(path string, info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if err := os.Chown(path, int(stat.Uid), int(stat.Gid)); err != nil && !errors.Is(err, syscall.EPERM) {
		return err
	}
	return nil
}
