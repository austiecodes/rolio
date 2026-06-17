package command

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/austiecodes/rolio/internal/store"
)

func NewTreeCommand(adapter store.Adapter) *cobra.Command {
	var depth int
	var treeAll, treeDirsOnly, treeFullPath, treeShowSize, treeSortByTime, treeDirsFirst bool
	cmd := &cobra.Command{
		Use:   "tree [path]",
		Short: "Print a VFS tree",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sortField := ""
			if treeSortByTime {
				sortField = "mtime"
			}
			resp, err := adapter.Tree(cmd.Context(), store.TreeRequest{
				Path:      argPath(args, "/"),
				Depth:     depth,
				All:       treeAll,
				DirsOnly:  treeDirsOnly,
				FullPath:  treeFullPath,
				ShowSize:  treeShowSize,
				Sort:      sortField,
				DirsFirst: treeDirsFirst,
			})
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), resp.Text)
			return nil
		},
	}
	cmd.Flags().IntVarP(&depth, "level", "L", 2, "maximum tree depth")
	cmd.Flags().BoolVarP(&treeAll, "all", "a", false, "show hidden files")
	cmd.Flags().BoolVarP(&treeDirsOnly, "dirs-only", "d", false, "list directories only")
	cmd.Flags().BoolVarP(&treeFullPath, "full-path", "f", false, "print full path prefix")
	cmd.Flags().BoolVarP(&treeShowSize, "size", "s", false, "show file sizes")
	cmd.Flags().BoolVarP(&treeSortByTime, "sort-time", "t", false, "sort by modification time")
	cmd.Flags().BoolVar(&treeDirsFirst, "dirsfirst", false, "list directories before files")
	return cmd
}
