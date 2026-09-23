// Command lt is the LibreTimes command-line client.
//
// It is a pure client of the public REST API: it holds no internal secret,
// sets no X-Profile-Id, and reaches no domain service. If it were compromised
// it could do exactly what the presented token allows and nothing more.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/The-LibreTimes/libretimes-cli/internal/cli"
)

func main() {
	// Ctrl-C cancels the in-flight request rather than leaving a half-written
	// publish and an orphaned loopback listener behind. stop() restores the
	// default handler, so a second Ctrl-C during shutdown still kills it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(cli.Execute(ctx))
}
