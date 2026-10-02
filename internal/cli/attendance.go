package cli

import (
	"io"

	"github.com/spf13/cobra"
)

// newAttendanceCmd builds `sjtu attendance` and its submit leaf.
func newAttendanceCmd(rt *Runtime, stdout io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attendance",
		Short: "mlearning roll-call operations",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "submit <url>",
		Short: "Submit a scanned roll-call QR URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return rt.submitAttendance(cmd.Context(), args[0], stdout)
		},
	})
	return cmd
}
