// Package cli wires the sjtu command tree. It translates between humans and
// the internal packages; all protocol and storage knowledge stays below it.
//
// Output contract: command results go to stdout as compact JSON; failures go
// to stderr as {"error": "<code>", "hint": "<guidance>"}. Exit codes: 0
// success, 1 general error, 2 usage error, 3 authentication failure.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/cred"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/mlearning"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/session"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/video"
)

// Exit codes, per the output contract.
const (
	exitOK    = 0 // success
	exitError = 1 // general error
	exitUsage = 2 // usage error
	exitAuth  = 3 // authentication failure
)

// apiError is a command failure rendered as structured JSON on stderr.
type apiError struct {
	code string // snake_case machine code, e.g. "not_found"
	hint string // human-readable guidance
	exit int    // process exit code
}

// Error formats the failure for logs and plain-text contexts.
func (e *apiError) Error() string { return e.code + ": " + e.hint }

// fail builds a structured command failure.
func fail(code, hint string, exit int) *apiError { return &apiError{code, hint, exit} }

// Execute runs the root command and reports failures to stderr as structured
// JSON. It returns the process exit code so main stays trivially small.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	root := newRootCmd(stdout, stderr)
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		return reportError(stderr, err)
	}
	return exitOK
}

// reportError renders err per the output contract and returns the exit code.
func reportError(stderr io.Writer, err error) int {
	ae := classify(err)
	out, _ := json.Marshal(map[string]string{"error": ae.code, "hint": ae.hint})
	fmt.Fprintf(stderr, "%s\n", out)
	return ae.exit
}

// classify maps any command failure to a structured error. *apiError values
// pass through untouched; everything else is classified by cause.
func classify(err error) *apiError {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}
	var httpErr *session.HTTPError
	if errors.As(err, &httpErr) && strings.HasPrefix(httpErr.Status, "401") {
		return fail("auth_failed", "Canvas credential invalid or expired; run sjtu auth canvas login to log in again", exitAuth)
	}
	if errors.Is(err, video.ErrAuth) || errors.Is(err, mlearning.ErrAuth) {
		return fail("auth_required", err.Error(), exitAuth)
	}
	if errors.Is(err, cred.ErrNotFound) {
		return fail("auth_required", "not logged in; run sjtu auth canvas login first", exitAuth)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return fail("not_found", err.Error(), exitError)
	}
	// cobra reports an unrecognized subcommand verbatim.
	if strings.HasPrefix(err.Error(), "unknown command") {
		return fail("usage", err.Error(), exitUsage)
	}
	return fail("error", err.Error(), exitError)
}

// newRootCmd assembles the sjtu command tree. Bare invocation enters the TUI
// when attached to a terminal, and prints help otherwise — agent harnesses
// drive stdin through pipes, humans through terminals.
func newRootCmd(stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "sjtu",
		Short:         "CLI for SJTU online services (Canvas, jAccount, and more)",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
				return runTUI(cmd.Context(), stderr)
			}
			return cmd.Help()
		},
	}
	// Flag parse failures are usage errors, exit code 2.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return fail("usage", err.Error(), exitUsage)
	})
	root.AddCommand(newAuthCmd(stderr))
	root.AddCommand(newLsCmd(stdout), newStatCmd(stdout), newCatCmd(stdout), newDownloadCmd(stdout, stderr))
	root.AddCommand(newAttendanceCmd(stdout))
	root.AddCommand(newUpdateCmd(stdout, stderr))
	root.AddCommand(&cobra.Command{
		Use:   "tui",
		Short: "Enter the interactive shell",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(cmd.Context(), stderr)
		},
	})
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
