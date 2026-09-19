// Package cli wires the sjtu command tree. It translates between humans and
// the internal packages; all protocol and storage knowledge stays below it.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// Execute runs the root command and reports failures to stderr. It returns
// the process exit code so main stays trivially small.
func Execute(ctx context.Context, args []string, stderr io.Writer) int {
	root := newRootCmd(stderr)
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// newRootCmd assembles the sjtu command tree.
func newRootCmd(stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "sjtu",
		Short:         "CLI for SJTU online services (Canvas, jAccount, and more)",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newAuthCmd(stderr))
	return root
}

// failf formats a command failure.
func failf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}

// ensureStderr guards helpers against a nil writer in tests.
func ensureStderr(w io.Writer) io.Writer {
	if w == nil {
		return os.Stderr
	}
	return w
}
