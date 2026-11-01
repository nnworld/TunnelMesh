package inventory

import (
	"net"
	"strings"
	"testing"
)

const cpuInfo = `processor	: 0
vendor_id	: GenuineIntel
model name	: Intel(R) Xeon(R) Platinum 8375C CPU @ 2.90GHz
cpu MHz		: 2900.000

processor	: 1
vendor_id	: GenuineIntel
model name	: Intel(R) Xeon(R) Platinum 8375C CPU @ 2.90GHz
`

func TestParseCPUInfoCountsLogicalCPUsAndKeepsOneModel(t *testing.T) {
	model, logical := parseCPUInfo(cpuInfo)
	if model != "Intel(R) Xeon(R) Platinum 8375C CPU @ 2.90GHz" {
		t.Fatalf("model = %q", model)
	}
	if logical != 2 {
		t.Fatalf("logical = %d, want 2", logical)
	}
}

func TestParseMemInfoReturnsTotalBytes(t *testing.T) {
	got := parseMemInfo("MemTotal:       16384000 kB\nMemFree:        1024 kB\n")
	if got != 16384000*1024 {
		t.Fatalf("total = %d", got)
	}
	if parseMemInfo("MemFree: 1 kB\n") != 0 {
		t.Fatal("missing MemTotal must report zero, not guess")
	}
}

func TestParseOSReleasePrettyName(t *testing.T) {
	text := "NAME=\"Ubuntu\"\nPRETTY_NAME=\"Ubuntu 24.04 LTS\"\nID=ubuntu\n"
	if got := parseOSRelease(text); got != "Ubuntu 24.04 LTS" {
		t.Fatalf("name = %q", got)
	}
}

func TestParseMountsKeepsOnlyRealFilesystems(t *testing.T) {
	text := "/dev/sda1 / ext4 rw 0 0\nproc /proc proc rw 0 0\ntmpfs /run tmpfs rw 0 0\n/dev/sdb1 /data xfs rw 0 0\n"
	mounts := parseMounts(text)
	if len(mounts) != 2 || mounts[0].Mount != "/" || mounts[1].Mount != "/data" {
		t.Fatalf("mounts = %+v", mounts)
	}
}

func TestParseLspciKeepsDisplayControllers(t *testing.T) {
	text := `00:02.0 "VGA compatible controller" "Intel Corporation" "UHD Graphics 630" -r02
00:1f.0 "ISA bridge" "Intel Corporation" "Z370 Chipset" -r00
01:00.0 "3D controller" "NVIDIA Corporation" "GP107GLM" -ra1
`
	got := parseLspci(text)
	if len(got) != 2 || got[0] != "Intel Corporation UHD Graphics 630" || got[1] != "NVIDIA Corporation GP107GLM" {
		t.Fatalf("gpus = %v", got)
	}
}

func TestBuildNICsSkipsLoopbackAndDownInterfaces(t *testing.T) {
	mac, _ := net.ParseMAC("aa:bb:cc:dd:ee:ff")
	ifaces := []netInterface{
		{Name: "lo", Flags: net.FlagUp | net.FlagLoopback, Addrs: []string{"127.0.0.1/8"}},
		{Name: "eth0", Flags: net.FlagUp, HardwareAddr: mac, Addrs: []string{"10.0.0.5/24", "fe80::1/64"}},
		{Name: "eth1", Flags: 0, HardwareAddr: mac},
	}
	nics := buildNICs(ifaces)
	if len(nics) != 1 || nics[0].Name != "eth0" || nics[0].MAC != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("nics = %+v", nics)
	}
	if strings.Join(nics[0].Addresses, ",") != "10.0.0.5,fe80::1" {
		t.Fatalf("addresses = %v", nics[0].Addresses)
	}
}
