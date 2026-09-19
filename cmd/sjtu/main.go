// Command sjtu is the CLI entry point for SJTU online services.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/cli"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/session"
)

// main installs the redacting logger, wires Ctrl-C cancellation into the
// command context, and runs the command tree.
func main() {
	slog.SetDefault(slog.New(session.NewRedactingHandler(
		slog.NewTextHandler(os.Stderr, nil))))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	os.Exit(cli.Execute(ctx, os.Args[1:], os.Stderr))
}
