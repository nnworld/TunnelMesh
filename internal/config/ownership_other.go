//go:build !unix

package config

import "io/fs"

func preserveOwnership(string, fs.FileInfo) error { return nil }
