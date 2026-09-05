// Command tunnelmesh-agent starts the TunnelMesh agent process.
package main

import (
	"context"
	"log"

	"github.com/tunnelmesh/tunnelmesh/internal/cli"
)

func main() {
	if err := cli.Execute(context.Background(), cli.NewAgentRoot()); err != nil {
		log.Fatal(err)
	}
}
