package command

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/austiecodes/rolio/internal/store"
)

func NewGlobCommand(adapter store.Adapter) *cobra.Command {
	var limit, offset int
	var long bool
	cmd := &cobra.Command{Use: "glob <pattern>", Short: "Find knowledge paths using *, ? and **", Args: cobra.ExactArgs(1)}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := validateNonNeg([]string{"limit", "offset"}, limit, offset); err != nil {
			return err
		}
		resp, err := adapter.Glob(cmd.Context(), store.GlobRequest{Pattern: args[0], Limit: limit, Offset: offset})
		if err != nil {
			return err
		}
		for _, r := range resp.Results {
			p := "/" + strings.TrimPrefix(r.Path, "/")
			if long {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t(%s)\t%s\n", p, formatSize(r.Size), r.ModTime)
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), p)
			}
		}
		printPaginationSummary(cmd.OutOrStdout(), offset, len(resp.Results), resp.Total)
		return nil
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "max results (0 = unlimited)")
	cmd.Flags().IntVar(&offset, "offset", 0, "skip first N results")
	cmd.Flags().BoolVarP(&long, "long", "l", false, "show metadata")
	return cmd
}
