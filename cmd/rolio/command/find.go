package command

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/austiecodes/rolio/internal/store"
)

func NewFindCommand(adapter store.Adapter) *cobra.Command {
	var name, findType, iname string
	var maxDepth, minDepth, findLimit, findOffset int
	var allFiles bool
	cmd := &cobra.Command{
		Use:   "find [path] -name <glob>",
		Short: "Find VFS files by name",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateNonNeg([]string{"limit", "offset"}, findLimit, findOffset); err != nil {
				return err
			}
			if name == "" && iname == "" {
				return fmt.Errorf("-name or -iname is required")
			}
			switch findType {
			case "f":
				findType = "file"
			case "d":
				findType = "dir"
			case "", "file", "dir":
				// valid as-is
			default:
				return fmt.Errorf("invalid type %q: use f or d", findType)
			}
			resp, err := adapter.Find(cmd.Context(), store.FindRequest{
				Path:     argPath(args, "/"),
				Name:     name,
				Type:     findType,
				MaxDepth: maxDepth,
				MinDepth: minDepth,
				All:      allFiles,
				IName:    iname,
				Limit:    findLimit,
				Offset:   findOffset,
			})
			if err != nil {
				return err
			}
			for _, node := range resp.Nodes {
				fmt.Fprintln(cmd.OutOrStdout(), node.Path)
			}
			printPaginationSummary(cmd.OutOrStdout(), findOffset, len(resp.Nodes), resp.Total)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "filename glob")
	cmd.Flags().StringVarP(&findType, "type", "t", "", "filter by type (f=file, d=directory)")
	cmd.Flags().IntVar(&maxDepth, "maxdepth", 0, "maximum descent depth (0 = unlimited)")
	cmd.Flags().IntVar(&minDepth, "mindepth", 0, "minimum descent depth")
	cmd.Flags().BoolVarP(&allFiles, "all", "a", false, "search hidden files")
	cmd.Flags().StringVar(&iname, "iname", "", "case-insensitive name glob")
	cmd.Flags().IntVar(&findLimit, "limit", 0, "max results (0 = unlimited)")
	cmd.Flags().IntVar(&findOffset, "offset", 0, "skip first N results")
	return cmd
}
