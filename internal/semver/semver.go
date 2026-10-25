// Package semver parses and orders the release versions TunnelMesh publishes
// (vMAJOR.MINOR.PATCH with an optional -pre suffix).
//
// It is deliberately narrower than semver.org: the release pipeline only produces
// that shape (scripts/build-release.sh), and refusing anything else is what keeps
// a "dev" or hand-built binary from ever being treated as an upgradable release.
package semver

import (
	"errors"
	"strconv"
	"strings"
)

// ErrNotRelease is returned for anything that is not a published release version,
// including the "dev" a plain `go build` reports.
var ErrNotRelease = errors.New("semver: not a release version")

// Version is a parsed release version.
type Version struct {
	Major, Minor, Patch int
	// Pre is the pre-release label without the leading dash, empty for a release.
	Pre string
}

// Parse accepts "v1.2.3", "1.2.3" and "v1.2.3-pre".
func Parse(s string) (Version, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return Version{}, ErrNotRelease
	}
	core, pre, hasPre := strings.Cut(s, "-")
	if hasPre && pre == "" {
		return Version{}, ErrNotRelease
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, ErrNotRelease
	}
	var numbers [3]int
	for i, part := range parts {
		n, ok := parseNumber(part)
		if !ok {
			return Version{}, ErrNotRelease
		}
		numbers[i] = n
	}
	if hasPre && !validPre(pre) {
		return Version{}, ErrNotRelease
	}
	return Version{Major: numbers[0], Minor: numbers[1], Patch: numbers[2], Pre: pre}, nil
}

// parseNumber rejects signs, spaces and empty strings that strconv.Atoi would
// otherwise accept in some forms.
func parseNumber(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

func validPre(pre string) bool {
	for _, r := range pre {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.':
		default:
			return false
		}
	}
	return true
}

// Compare returns -1, 0 or 1. A pre-release is below the release of the same
// major.minor.patch.
func (v Version) Compare(o Version) int {
	for _, pair := range [3][2]int{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case v.Pre == o.Pre:
		return 0
	case v.Pre == "":
		return 1
	case o.Pre == "":
		return -1
	default:
		return strings.Compare(v.Pre, o.Pre)
	}
}

// IsPre reports whether v is a pre-release.
func (v Version) IsPre() bool { return v.Pre != "" }

// String always includes the "v" prefix, matching release tags and asset names.
func (v Version) String() string {
	s := "v" + strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Min returns the lower of a and b.
func Min(a, b Version) Version {
	if a.Compare(b) <= 0 {
		return a
	}
	return b
}
