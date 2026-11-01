//go:build linux

package inventory

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Linux reads kernel interfaces directly. Only GPU discovery runs a program, and
// only the stock `lspci` if it is installed; without it the GPU field is omitted.
const gpuTimeout = 5 * time.Second

func osName() string {
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		if name := parseOSRelease(string(data)); name != "" {
			return name
		}
	}
	return "Linux"
}

func kernelRelease() string {
	data, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	return strings.TrimSpace(string(data))
}

func cpuModel() (string, int) {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "", 0
	}
	return parseCPUInfo(string(data))
}

func memoryTotal() uint64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	return parseMemInfo(string(data))
}

// disks reports one entry per backing device. Bind mounts and btrfs subvolumes
// repeat a device, and listing them would count the same space several times.
func disks() []Disk {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil
	}
	var out []Disk
	seen := map[string]bool{}
	for _, entry := range parseMounts(string(data)) {
		if seen[entry.Device] {
			continue
		}
		var stat syscall.Statfs_t
		if err := syscall.Statfs(entry.Mount, &stat); err != nil || stat.Blocks == 0 {
			continue
		}
		seen[entry.Device] = true
		bsize := uint64(stat.Bsize)
		out = append(out, Disk{Mount: entry.Mount, FS: entry.FS, TotalBytes: stat.Blocks * bsize, FreeBytes: stat.Bavail * bsize})
		if len(out) == maxDisks {
			break
		}
	}
	return out
}

func gpus(ctx context.Context) []string {
	path, err := exec.LookPath("lspci")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, gpuTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-mm").Output()
	if err != nil {
		return nil
	}
	return parseLspci(string(out))
}
