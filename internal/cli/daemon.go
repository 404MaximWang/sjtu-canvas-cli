package cli

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/daemon"
)

// newDaemonCmd builds `sjtu daemon`: the long-running local service
// executing the same command tree over a unix socket.
func newDaemonCmd(rt *Runtime, stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the local daemon serving commands over a unix socket",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			hooks, err := rt.daemonHooks()
			if err != nil {
				return err
			}
			return daemon.Run(cmd.Context(), hooks)
		},
	}
	cmd.AddCommand(newDaemonInstallCmd(rt, stdout, stderr))
	cmd.AddCommand(newDaemonUninstallCmd(stdout, stderr))
	return cmd
}
