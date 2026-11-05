// Command tunnelmesh-server starts the TunnelMesh server process.
package main

import (
	"os"

	"github.com/tunnelmesh/tunnelmesh/internal/cli"
)

func main() {
	os.Exit(cli.Main(cli.NewServerRoot()))
}
