// Command tunnelmesh-client starts the TunnelMesh client process.
package main

import (
	"context"
	"log"

	"github.com/tunnelmesh/tunnelmesh/internal/cli"
)

func main() {
	if err := cli.Execute(context.Background(), cli.NewClientRoot()); err != nil {
		log.Fatal(err)
	}
}
