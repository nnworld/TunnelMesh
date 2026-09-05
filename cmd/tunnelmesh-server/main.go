// Command tunnelmesh-server starts the TunnelMesh server process.
package main

import (
	"context"
	"log"

	"github.com/tunnelmesh/tunnelmesh/internal/cli"
)

func main() {
	if err := cli.Execute(context.Background(), cli.NewServerRoot()); err != nil {
		log.Fatal(err)
	}
}
