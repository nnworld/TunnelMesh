package selfupdate

import (
	"bufio"
	"fmt"
	"io"
	"path"
	"strings"
)

// ParseSHA256SUMS reads the `sha256sum` output the release publishes: a 64-digit
// hex digest, whitespace (with an optional `*` marking binary mode), then the file
// name. Digests come back lower-cased and names without any leading "./".
//
// A malformed line is an error rather than being skipped: a checksum file that is
// partly unreadable cannot vouch for anything, and silently dropping the line for
// the archive we need would turn into "no checksum, no check".
func ParseSHA256SUMS(r io.Reader) (map[string]string, error) {
	sums := make(map[string]string)
	scanner := bufio.NewScanner(r)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		digest, rest, ok := strings.Cut(line, " ")
		rest = strings.TrimLeft(rest, " ")
		rest = strings.TrimPrefix(rest, "*")
		name := strings.TrimPrefix(rest, "./")
		if !ok || len(digest) != 64 || !isHex(digest) || name == "" {
			return nil, fmt.Errorf("selfupdate: malformed SHA256SUMS line %d", lineNo)
		}
		// Names are looked up verbatim and later joined to nothing, but a name with a
		// directory part has no business in a flat release.
		if path.Base(name) != name || name == ".." {
			return nil, fmt.Errorf("selfupdate: SHA256SUMS line %d names a path, not a file", lineNo)
		}
		digest = strings.ToLower(digest)
		if previous, dup := sums[name]; dup && previous != digest {
			return nil, fmt.Errorf("selfupdate: SHA256SUMS lists two different digests for %s", name)
		}
		sums[name] = digest
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return sums, nil
}

func isHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}
