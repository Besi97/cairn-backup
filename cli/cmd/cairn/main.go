package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/Besi97/cairn-backup/cli/internal/cancellation"
	"github.com/Besi97/cairn-backup/cli/internal/cli"
)

// version is overridden with -ldflags for versioned builds.
var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signalContext()
	defer stop()
	return cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, version)
}

func signalContext() (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(context.Background())
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	finished := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case sig := <-signals:
			cancel(cancellation.Signal(sig.(syscall.Signal)))
		case <-finished:
		}
	}()
	return ctx, func() {
		signal.Stop(signals)
		close(finished)
		cancel(nil)
		<-done
	}
}
