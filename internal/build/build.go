// Package build exposes the names of the binaries shipped by TunnelMesh.
package build

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
