// Command tunnelmesh-agent starts the TunnelMesh agent process.
package main

import (
	"os"

	"github.com/tunnelmesh/tunnelmesh/internal/cli"
)

func main() {
	os.Exit(cli.Main(cli.NewAgentRoot()))
}
