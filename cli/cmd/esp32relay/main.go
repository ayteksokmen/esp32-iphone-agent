// esp32relay is the cloud relay alone (docs/cli-protocol.md, "Cloud relay"), for a server or a Raspberry Pi:
// esp32relay [ADDR], default :7400. `esp32agent relay` runs the same.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/relay"
)

func main() {
	addr := ":7400"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := (&relay.Server{}).ListenAndServe(ctx, addr); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}
