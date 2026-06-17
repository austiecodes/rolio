package command

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/austiecodes/rolio/internal/store"
)

func NewWriteCommand(adapter store.Adapter) *cobra.Command {
	var expected string
	cmd := &cobra.Command{Use: "write <path> [content]", Short: "Write a knowledge file; read stdin when content is omitted", Args: cobra.RangeArgs(1, 2)}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		var content string
		if len(args) == 2 {
			content = args[1]
		} else {
			data, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return err
			}
			content = string(data)
		}
		resp, err := adapter.Put(cmd.Context(), store.PutRequest{Path: args[0], Content: content, ExpectedHash: expected})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (%d bytes)\n", resp.Node.Path, resp.Node.Size)
		return nil
	}
	cmd.Flags().StringVar(&expected, "expected-hash", "", "require this content hash; * means create only")
	return cmd
}
func NewDeleteCommand(adapter store.Adapter) *cobra.Command {
	var expected string
	cmd := &cobra.Command{Use: "rm <path>", Short: "Delete a knowledge file or directory", Args: cobra.ExactArgs(1)}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		_, err := adapter.Delete(cmd.Context(), store.DeleteRequest{Path: args[0], ExpectedHash: expected})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "deleted %s\n", args[0])
		return nil
	}
	cmd.Flags().StringVar(&expected, "expected-hash", "", "require this file content hash")
	return cmd
}
func NewEditCommand(adapter store.Adapter) *cobra.Command {
	var old, replacement, expected string
	var all bool
	cmd := &cobra.Command{Use: "edit <path> --old <text> --new <text>", Short: "Replace text in a knowledge file", Args: cobra.ExactArgs(1)}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		resp, err := adapter.Edit(cmd.Context(), store.EditRequest{Path: args[0], Old: old, New: replacement, All: all, ExpectedHash: expected})
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "edited %s (%d replacements)\n", resp.Path, resp.Replaced)
		return nil
	}
	cmd.Flags().StringVar(&old, "old", "", "text to replace")
	cmd.Flags().StringVar(&replacement, "new", "", "replacement text")
	cmd.Flags().StringVar(&expected, "expected-hash", "", "require this file content hash")
	cmd.Flags().BoolVar(&all, "all", false, "replace all occurrences")
	_ = cmd.MarkFlagRequired("old")
	_ = cmd.MarkFlagRequired("new")
	return cmd
}
func printPaginationSummary(out io.Writer, offset, shown, total int) {
	if total <= shown || shown == 0 {
		return
	}
	fmt.Fprintf(out, "\nshowing %d-%d of %d\n", offset+1, offset+shown, total)
}
func validateNonNeg(names []string, vals ...int) error {
	for i, v := range vals {
		if v < 0 {
			return fmt.Errorf("--%s must be non-negative", names[i])
		}
	}
	return nil
}
