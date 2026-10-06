package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/HarnageaGabriel/trasloco/internal/version"
)

func newVersionCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(*cobra.Command, []string) error {
			info := version.Get()
			switch a.output {
			case OutputJSON, OutputJSONL:
				enc := json.NewEncoder(a.io.Out)
				if a.output == OutputJSON {
					enc.SetIndent("", "  ")
				}
				return enc.Encode(info)
			default:
				_, err := fmt.Fprintf(a.io.Out, "trasloco %s (commit %s, built %s)\n", info.Version, info.Commit, info.Date)
				return err
			}
		},
	}
}
