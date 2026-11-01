// Package inventory collects the host facts an operator expects on a node's detail
// page: operating system, processor, memory, disks, graphics adapters and network
// interfaces. Collection is built in and always on; it reads local system state and
// never runs an operator-supplied command.
package inventory

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// Keys under which each fact is reported. They share the `system.` prefix so they
// cannot collide with operator-declared metadata names, which are checked separately.
const (
	KeyOS     = "system.os"
	KeyCPU    = "system.cpu"
	KeyGPU    = "system.gpu"
	KeyMemory = "system.memory"
	KeyDisks  = "system.disks"
	KeyNICs   = "system.nics"
)

// Caps keep every value well inside the metadata per-field limit. A host with more
// interfaces or disks than this reports the first ones in a stable order.
const (
	maxDisks     = 16
	maxGPUs      = 8
	maxNICs      = 8
	maxAddresses = 4
)

// OSInfo is the operating system block.
type OSInfo struct {
	Name     string `json:"name"`
	Kernel   string `json:"kernel,omitempty"`
	Arch     string `json:"arch"`
	Hostname string `json:"hostname,omitempty"`
}

// CPUInfo is the processor block.
type CPUInfo struct {
	Model       string `json:"model"`
	LogicalCPUs int    `json:"logical_cpus"`
}

// Disk is one mounted real filesystem. Only capacity is reported: free space changes
// with every write, and this block is part of a snapshot that is revision-checked, so
// volatile usage would turn each heartbeat into a metadata update.
type Disk struct {
	Mount      string `json:"mount"`
	FS         string `json:"fs"`
	TotalBytes uint64 `json:"total_bytes"`
}

// NIC is one active network interface. MAC is always reported: the operator asked
// for hardware and network inventory by default.
type NIC struct {
	Name      string   `json:"name"`
	MAC       string   `json:"mac,omitempty"`
	Addresses []string `json:"addresses"`
}

// netInterface is the subset of net.Interface the builder needs, so the selection
// rules can be tested without a live network stack.
type netInterface struct {
	Name         string
	Flags        net.Flags
	HardwareAddr net.HardwareAddr
	Addrs        []string
}

// Collect gathers every fact the platform can supply. A fact that cannot be read is
// omitted rather than reported as zero, so the UI never shows a made-up value.
func Collect(ctx context.Context) map[string]string {
	values := map[string]string{}
	if ctx.Err() != nil {
		return values
	}
	hostname, _ := os.Hostname()
	put(values, KeyOS, OSInfo{Name: osName(), Kernel: kernelRelease(), Arch: runtime.GOARCH, Hostname: hostname})
	if model, logical := cpuModel(); model != "" || logical > 0 {
		put(values, KeyCPU, CPUInfo{Model: model, LogicalCPUs: logical})
	}
	if total := memoryTotal(); total > 0 {
		put(values, KeyMemory, map[string]uint64{"total_bytes": total})
	}
	if disks := disks(); len(disks) > 0 {
		put(values, KeyDisks, disks)
	}
	if gpus := gpus(ctx); len(gpus) > 0 {
		put(values, KeyGPU, gpus)
	}
	if nics := buildNICs(interfaces()); len(nics) > 0 {
		put(values, KeyNICs, nics)
	}
	return values
}

func put(values map[string]string, key string, value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return
	}
	values[key] = string(encoded)
}

// buildNICs keeps up, non-loopback interfaces in the order the kernel lists them.
func buildNICs(ifaces []netInterface) []NIC {
	nics := make([]NIC, 0, len(ifaces))
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		nic := NIC{Name: iface.Name, Addresses: []string{}}
		if len(iface.HardwareAddr) > 0 {
			nic.MAC = iface.HardwareAddr.String()
		}
		for _, cidr := range iface.Addrs {
			if len(nic.Addresses) == maxAddresses {
				break
			}
			if ip, _, err := net.ParseCIDR(cidr); err == nil {
				nic.Addresses = append(nic.Addresses, ip.String())
			}
		}
		nics = append(nics, nic)
		if len(nics) == maxNICs {
			break
		}
	}
	return nics
}

// interfaces reads the live interface table into the builder's shape.
func interfaces() []netInterface {
	raw, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := make([]netInterface, 0, len(raw))
	for _, iface := range raw {
		item := netInterface{Name: iface.Name, Flags: iface.Flags, HardwareAddr: iface.HardwareAddr}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			item.Addrs = append(item.Addrs, addr.String())
		}
		out = append(out, item)
	}
	return out
}

var spaces = regexp.MustCompile(`\s+`)

// parseCPUInfo reads the first model name and counts logical processors.
func parseCPUInfo(text string) (string, int) {
	model := ""
	logical := 0
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "model name":
			if model == "" {
				model = strings.TrimSpace(value)
			}
		case "processor":
			logical++
		}
	}
	return model, logical
}

// parseMemInfo returns MemTotal in bytes, or zero when the line is absent.
func parseMemInfo(text string) uint64 {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kib, err := strconv.ParseUint(fields[1], 10, 64)
			if err == nil {
				return kib * 1024
			}
		}
	}
	return 0
}

// parseOSRelease returns PRETTY_NAME with its quotes removed.
func parseOSRelease(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if value, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(value, `"`)
		}
	}
	return ""
}

type mountEntry struct {
	Device string
	Mount  string
	FS     string
}

// realFilesystems is the allowlist of on-disk filesystem types. Virtual and
// memory-backed mounts are noise on a hardware inventory.
var realFilesystems = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true,
	"f2fs": true, "jfs": true, "reiserfs": true, "vfat": true, "exfat": true,
	"ntfs": true, "ntfs3": true, "apfs": true, "hfs": true, "hfsplus": true,
}

// parseMounts keeps the real-filesystem lines of a mount table in order.
func parseMounts(text string) []mountEntry {
	var out []mountEntry
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || !realFilesystems[fields[2]] {
			continue
		}
		out = append(out, mountEntry{Device: fields[0], Mount: fields[1], FS: fields[2]})
	}
	return out
}

var quoted = regexp.MustCompile(`"([^"]*)"`)

// parseLspci returns "vendor device" for display and 3D controllers, from
// `lspci -mm` output where each field is quoted.
func parseLspci(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		// Quoted fields are class, vendor, device; the slot before them is unquoted.
		matches := quoted.FindAllStringSubmatch(line, -1)
		if len(matches) < 3 {
			continue
		}
		class := matches[0][1]
		if !strings.Contains(class, "VGA") && !strings.Contains(class, "3D") && !strings.Contains(class, "Display") {
			continue
		}
		out = append(out, spaces.ReplaceAllString(matches[1][1]+" "+matches[2][1], " "))
		if len(out) == maxGPUs {
			break
		}
	}
	return out
}
