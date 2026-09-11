// Package build exposes the names of the binaries shipped by TunnelMesh.
package build

// Linker flags override these values in release builds. The defaults keep
// local binaries identifiable without requiring a special build target.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// Info is the immutable build identity exposed by commands and APIs.
type Info struct {
	Version   string
	Commit    string
	BuildTime string
}

// Current returns the build identity injected into this binary.
func Current() Info {
	return Info{Version: Version, Commit: Commit, BuildTime: BuildTime}
}

// String renders the identity in a stable single line for CLI output.
func String() string {
	info := Current()
	return info.Version + " commit=" + info.Commit + " built=" + info.BuildTime
}

// BinaryNames is the stable set of command names produced by the repository.
// Keeping this metadata in one package lets build tooling and tests agree on
// the public command surface without parsing the Makefile.
func BinaryNames() []string {
	return []string{
		"tunnelmesh-server",
		"tunnelmesh-agent",
		"tunnelmesh-client",
	}
}
