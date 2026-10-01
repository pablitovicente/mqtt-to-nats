package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/pablitovicente/mqtt-to-nats/v2/internal/cli"
)

func main() {
	ctx, stopCatchingSignals := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopCatchingSignals()

	// The first Ctrl-C cancels ctx and the bridge shuts down cleanly, which can take a while
	// as queued messages are sent to NATS. After that, stop catching signals, so a second
	// Ctrl-C kills the process right away (Go's default behaviour).
	go func() {
		<-ctx.Done()
		stopCatchingSignals()
	}()

	rootCommand := cli.NewRootCommand()
	if err := rootCommand.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
