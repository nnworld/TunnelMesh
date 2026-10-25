package selfupdate

import (
	"strings"
	"testing"
)

const goodSum = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

func TestParseSHA256SUMS(t *testing.T) {
	input := goodSum + "  tunnelmesh-v1.3.5-linux-amd64.tar.gz\n" +
		strings.ToUpper(goodSum) + " *tunnelmesh-v1.3.5-windows-amd64.zip\n" +
		"\n# a comment\n" +
		goodSum + "  ./tunnelmesh-v1.3.5-darwin-arm64.tar.gz\n"
	sums, err := ParseSHA256SUMS(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tunnelmesh-v1.3.5-linux-amd64.tar.gz", "tunnelmesh-v1.3.5-windows-amd64.zip", "tunnelmesh-v1.3.5-darwin-arm64.tar.gz"} {
		if sums[name] != goodSum {
			t.Errorf("sums[%q] = %q, want %q (lower-cased, without the ./ or * markers)", name, sums[name], goodSum)
		}
	}
}

func TestParseSHA256SUMSRejectsMalformedLines(t *testing.T) {
	for name, line := range map[string]string{
		"short hash":  "abc123  file.tar.gz",
		"not hex":     strings.Repeat("z", 64) + "  file.tar.gz",
		"no filename": goodSum,
		"path escape": goodSum + "  ../file.tar.gz",
	} {
		if _, err := ParseSHA256SUMS(strings.NewReader(line + "\n")); err == nil {
			t.Errorf("%s: ParseSHA256SUMS accepted %q", name, line)
		}
	}
}

func TestParseSHA256SUMSRejectsConflictingEntriesForOneName(t *testing.T) {
	other := strings.Repeat("a", 64)
	sums, err := ParseSHA256SUMS(strings.NewReader(goodSum + "  f.tar.gz\n" + other + "  f.tar.gz\n"))
	if err == nil {
		t.Fatalf("sums = %v, want a conflict error: two different hashes for one file are not trustworthy", sums)
	}
}
