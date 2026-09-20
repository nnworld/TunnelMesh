package scripts

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reversedClaim is one absolute statement that ADR 0002 retires. Matching is
// plain substring search on purpose: the brittleness is the feature, so a
// reintroduced claim fails the build instead of waiting for a reader to notice.
// It cannot catch the same claim in new words, so this test supplements review
// rather than replacing it.
type reversedClaim struct {
	phrase string
	why    string
}

var reversedClaims = []reversedClaim{
	{"never listens for public UDP", "ADR 0002 允许 VPN 网关持有一个公网 UDP 端口"},
	{"does not expose public UDP", "同上"},
	{"Public UDP is not exposed", "同上"},
	{"not as a public Server listener", "UDP 已成为 Server 侧公网监听能力之一"},
	{"不监听公网 UDP", "ADR 0002 允许 VPN 网关持有一个公网 UDP 端口"},
	{"公网 UDP 不作为", "同上"},
	{"不提供公网 UDP", "必须限定到具体功能，不得作为全局主张"},
	{"不支持公网 UDP", "同上"},
	{"不开放公网 UDP", "同上"},
	{"不直接监听 UDP", "同上"},
	{"公网 UDP 不由 Server 监听", "同上"},
	{"HTTP/HTTPS/WSS only", "公网入口不再只有 HTTP/HTTPS/WSS"},
	{"only HTTP, HTTPS, and WebSocket", "同上"},
	{"只使用 HTTP/HTTPS/WSS", "同上"},
	{"只使用 HTTP/HTTPS/WebSocket", "同上"},
	{"只监听 HTTP/HTTPS/WebSocket", "必须限定到代理模块，不得作为全局主张"},
	{"只需要 HTTP/HTTPS/WSS 入口", "公网入口不再只有 HTTP/HTTPS/WSS"},
	{"只允许 HTTP/HTTPS/WebSocket", "必须限定到 tp-* 代理入口"},
	{"公网仍只提供 HTTP/HTTPS/WebSocket", "公网入口不再只有 HTTP/HTTPS/WebSocket"},
	{"公网侧只有 HTTP/HTTPS/WebSocket", "必须限定到 tp-* 代理入口"},
	{"ICMP、TUN/L2 VPN", "ICMP echo 与内存态 TUN 已由 ADR 0002 批准；P2P 与 RCE 禁令保留"},
	{"ICMP, TUN/L2 VPN", "同上"},
	{"Explicitly not planned: ICMP", "ICMP echo 已进入路线图"},
	{"does not implement ICMP", "ICMP echo 已批准实施；改写时必须限定范围"},
}

// liveDocRoots describe current behaviour and must stay truthful. Record
// directories are excluded: plans, specs, PR records and accepted ADRs are
// point-in-time evidence and keep the wording they were written with
// (docs/development/documentation.md, 时点记录不可改写).
var liveDocRoots = []string{
	"../AGENTS.md",
	"../README.md",
	"../README.zh-CN.md",
	"../docs",
	"../deploy",
}

var immutableRecordDirs = []string{
	"docs/superpowers/",
	"docs/pull-requests/",
	"docs/architecture/adr/",
}

var skipDirs = map[string]bool{
	"node_modules": true,
	"web_dist":     true,
	".git":         true,
}

func isImmutableRecord(path string) bool {
	clean := strings.TrimPrefix(filepath.ToSlash(path), "../")
	for _, dir := range immutableRecordDirs {
		if strings.HasPrefix(clean, dir) {
			return true
		}
	}
	return false
}

// scanFile reports one violation per offending line, not per matched phrase: a
// single line often trips both the Chinese and the English pattern, and the
// baseline this guard was written against is 29 lines across 21 files.
func scanFile(t *testing.T, path string) int {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	violations := 0
	for i, line := range strings.Split(string(data), "\n") {
		var matched []string
		for _, claim := range reversedClaims {
			if strings.Contains(line, claim.phrase) {
				matched = append(matched, claim.phrase)
			}
		}
		if len(matched) == 0 {
			continue
		}
		violations++
		t.Errorf("%s:%d still asserts a constraint reversed by ADR 0002 %q\n\tline: %s",
			strings.TrimPrefix(filepath.ToSlash(path), "../"), i+1, matched, strings.TrimSpace(line))
	}
	return violations
}

func TestLiveDocsDoNotAssertReversedConstraints(t *testing.T) {
	total := 0
	for _, root := range liveDocRoots {
		info, err := os.Stat(root)
		if err != nil {
			t.Fatalf("stat %s: %v", root, err)
		}
		if !info.IsDir() {
			total += scanFile(t, root)
			continue
		}
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".md") || isImmutableRecord(path) {
				return nil
			}
			total += scanFile(t, path)
			return nil
		})
		if walkErr != nil {
			t.Fatalf("walk %s: %v", root, walkErr)
		}
	}
	if total > 0 {
		t.Errorf("%d live-document line(s) across the repo still assert a reversed constraint", total)
	}
}

// TestADR0002RecordsTheConstraintReversal keeps the decision carrier honest:
// the reversal is only legitimate while an accepted ADR states it, links the
// design spec and the Task 0 evidence, and keeps the two bans that survive.
func TestADR0002RecordsTheConstraintReversal(t *testing.T) {
	const path = "../docs/architecture/adr/0002-public-ingress-and-embedded-vpn.md"

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ADR 0002 is the decision carrier for the constraint reversal: %v", err)
	}
	body := string(data)
	for _, want := range []string{
		"- Status: Accepted",
		"WireGuard",
		"netstack",
		"CAP_NET_ADMIN",
		"AGENTS.md",
		"P2P NAT traversal",
		"2026-09-19-embedded-vpn-gateway-design.md",
		"vpn-task0/REPORT.md",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("ADR 0002 is missing required content %q", want)
		}
	}
}
