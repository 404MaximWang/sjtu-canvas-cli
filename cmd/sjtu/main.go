// Command sjtu is the CLI entry point for SJTU online services.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/cli"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/logging"
)

// main installs the file-backed redacting logger, wires Ctrl-C cancellation
// into the command context, and runs the command tree. Logs land exclusively
// in the state directory; stdout carries only command results.
func main() {
	closeLog, err := logging.Setup()
	if err != nil {
		fmt.Fprintf(os.Stderr, `{"error":"logging_unavailable","hint":%q}`+"\n", err.Error())
		os.Exit(1)
	}
	defer closeLog()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	os.Exit(cli.Execute(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
