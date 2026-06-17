package command

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/austiecodes/rolio/internal/store"
)

func NewLSCommand(adapter store.Adapter) *cobra.Command {
	var longFmt, classify, slashDir bool
	var sortByTime, sortBySize, reverse bool
	var recursive, allFiles, dirOnly bool
	var lsLimit, lsOffset int

	cmd := &cobra.Command{
		Use:   "ls [path]",
		Short: "List VFS directory contents",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateNonNeg([]string{"limit", "offset"}, lsLimit, lsOffset); err != nil {
				return err
			}
			p := argPath(args, "/")

			sortField := "name"
			sortReverse := false
			if sortByTime {
				sortField = "mtime"
				sortReverse = true
			} else if sortBySize {
				sortField = "size"
				sortReverse = true
			}
			if reverse {
				sortReverse = !sortReverse
			}

			if dirOnly {
				resp, err := adapter.Stat(cmd.Context(), store.StatRequest{Path: p})
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), FormatLSLine(resp.Node, longFmt, classify, slashDir))
				return nil
			}

			resp, err := adapter.LS(cmd.Context(), store.LSRequest{
				Path:      p,
				Sort:      sortField,
				Reverse:   sortReverse,
				Recursive: recursive,
				All:       allFiles,
				Limit:     lsLimit,
				Offset:    lsOffset,
			})
			if err != nil {
				return err
			}
			for _, node := range resp.Nodes {
				fmt.Fprintln(cmd.OutOrStdout(), FormatLSLine(node, longFmt, classify, slashDir))
			}
			printPaginationSummary(cmd.OutOrStdout(), lsOffset, len(resp.Nodes), resp.Total)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&longFmt, "long", "l", false, "use a long listing format")
	cmd.Flags().BoolVarP(&classify, "classify", "F", false, "append indicator (one of */>@)")
	cmd.Flags().BoolVarP(&slashDir, "slash", "p", false, "append / to directories")
	cmd.Flags().BoolVarP(&sortByTime, "sort-time", "t", false, "sort by modification time (newest first)")
	cmd.Flags().BoolVarP(&sortBySize, "sort-size", "S", false, "sort by file size (largest first)")
	cmd.Flags().BoolVarP(&reverse, "reverse", "r", false, "reverse the sort order")
	cmd.Flags().BoolVarP(&recursive, "recursive", "R", false, "list subdirectories recursively")
	cmd.Flags().BoolVarP(&allFiles, "all", "a", false, "show hidden files")
	cmd.Flags().BoolVarP(&dirOnly, "directory", "d", false, "list directory entries instead of contents")
	cmd.Flags().IntVar(&lsLimit, "limit", 0, "max results (0 = unlimited)")
	cmd.Flags().IntVar(&lsOffset, "offset", 0, "skip first N results")
	return cmd
}

func FormatLSLine(node store.Node, long, classify, slashDir bool) string {
	isDir := node.Kind == "dir"

	if !long {
		name := node.Name
		if isDir {
			name += "/"
		}
		return name
	}

	kindIndicator := "-rw-"
	if isDir {
		kindIndicator = "drwx"
	}

	size := "       -"
	if !isDir {
		size = fmt.Sprintf("%8d", node.Size)
	}

	modTime := "-"
	if node.ModTime != "" {
		modTime = node.ModTime
	}

	suffix := ""
	if isDir && (classify || slashDir) {
		suffix = "/"
	}

	return fmt.Sprintf("%s  %s  %s  %s%s", kindIndicator, size, modTime, node.Name, suffix)
}
