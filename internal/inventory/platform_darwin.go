//go:build darwin

package inventory

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// macOS asks the kernel through sysctl, which is the supported interface and needs
// no entitlement. GPU discovery is omitted: the only route is system_profiler, which
// is slow enough to stall a heartbeat.
const sysctlPath = "/usr/sbin/sysctl"

func sysctl(name string) string {
	out, err := exec.Command(sysctlPath, "-n", name).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func osName() string {
	if version := sysctl("kern.osproductversion"); version != "" {
		return "macOS " + version
	}
	return "macOS"
}

func kernelRelease() string { return sysctl("kern.osrelease") }

func cpuModel() (string, int) {
	logical, _ := strconv.Atoi(sysctl("hw.logicalcpu"))
	return sysctl("machdep.cpu.brand_string"), logical
}

func memoryTotal() uint64 {
	total, _ := strconv.ParseUint(sysctl("hw.memsize"), 10, 64)
	return total
}

func disks() []Disk {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil || stat.Blocks == 0 {
		return nil
	}
	fsType := make([]byte, 0, len(stat.Fstypename))
	for _, c := range stat.Fstypename {
		if c == 0 {
			break
		}
		fsType = append(fsType, byte(c))
	}
	bsize := uint64(stat.Bsize)
	return []Disk{{Mount: "/", FS: string(fsType), TotalBytes: stat.Blocks * bsize, FreeBytes: stat.Bavail * bsize}}
}

func gpus(context.Context) []string { return nil }
