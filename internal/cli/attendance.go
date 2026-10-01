package cli

import (
	"context"
	"errors"
	"io"

	"github.com/spf13/cobra"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/cred"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/mlearning"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/session"
)

// newAttendanceCmd builds `sjtu attendance` and its submit leaf.
func newAttendanceCmd(stdout io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attendance",
		Short: "mlearning roll-call operations",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "submit <url>",
		Short: "Submit a scanned roll-call QR URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAttendanceSubmit(cmd.Context(), args[0], stdout)
		},
	})
	return cmd
}

// runAttendanceSubmit implements `sjtu attendance submit`: ride the forscan
// URL's SSO chain, call the scan endpoint, and pass its response through to
// stdout verbatim. The exit code reports delivery only; the business
// outcome lives in the response body.
func runAttendanceSubmit(ctx context.Context, rawURL string, stdout io.Writer) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	cookie, err := getCredential(store, cred.KeyJAccount)
	if errors.Is(err, cred.ErrNotFound) {
		return fail("auth_required", "attendance needs a jAccount session; run sjtu auth jaccount login", exitAuth)
	}
	if err != nil {
		return err
	}
	sess, err := session.NewCookies()
	if err != nil {
		return err
	}
	sess.SeedCookie("JAAuthCookie", cookie, "jaccount.sjtu.edu.cn", "my.sjtu.edu.cn")
	resp, err := mlearning.New(sess).Submit(ctx, rawURL)
	if errors.Is(err, mlearning.ErrInvalidURL) {
		return fail("usage", err.Error(), exitUsage)
	}
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(resp, '\n'))
	return err
}
