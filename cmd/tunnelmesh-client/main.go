// Command tunnelmesh-client starts the TunnelMesh client process.
package main

import (
	"os"

	"github.com/tunnelmesh/tunnelmesh/internal/cli"
)

func main() {
	os.Exit(cli.Main(cli.NewClientRoot()))
}
