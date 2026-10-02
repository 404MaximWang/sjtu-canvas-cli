package cli

import (
	_ "embed"
	"io"
	"sort"

	"github.com/spf13/cobra"
)

// The interface contracts ride inside the binary: an agent holding any
// sjtu build can read the exact contract that build implements, offline.
//
//go:embed docs/cli.md
var cliDoc string

//go:embed docs/daemon.md
var daemonDoc string

// docsTopics maps topic names to their embedded documents.
var docsTopics = map[string]string{
	"cli":    cliDoc,
	"daemon": daemonDoc,
}

// newDocsCmd builds `sjtu docs [topic]`: no topic lists the available
// topics as JSON; a topic prints its document verbatim to stdout.
func newDocsCmd(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "docs [topic]",
		Short: "Print the interface contract documentation",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				topics := make([]string, 0, len(docsTopics))
				for topic := range docsTopics {
					topics = append(topics, topic)
				}
				sort.Strings(topics)
				return writeJSON(stdout, topics)
			}
			doc, ok := docsTopics[args[0]]
			if !ok {
				return fail("not_found", "unknown docs topic; run 'sjtu docs' to list topics", exitError)
			}
			_, err := io.WriteString(stdout, doc)
			return err
		},
	}
}
