//go:build !linux && !darwin

package inventory

import (
	"context"
	"runtime"
)

// Other platforms report what the standard library knows portably: the operating
// system family and logical processor count. Model, memory and disk need platform
// APIs that are not implemented yet, so those facts are omitted rather than guessed.
func osName() string { return runtime.GOOS }

func kernelRelease() string { return "" }

func cpuModel() (string, int) { return "", runtime.NumCPU() }

func memoryTotal() uint64 { return 0 }

func disks() []Disk { return nil }

func gpus(context.Context) []string { return nil }
