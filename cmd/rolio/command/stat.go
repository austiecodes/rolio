package command

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/austiecodes/rolio/internal/store"
)

func NewStatCommand(adapter store.Adapter) *cobra.Command {
	var customFormat string
	var terse bool

	cmd := &cobra.Command{
		Use:   "stat <path>",
		Short: "Print VFS node metadata",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := adapter.Stat(cmd.Context(), store.StatRequest{Path: args[0]})
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			node := resp.Node

			if customFormat != "" {
				fmt.Fprintln(out, expandStatFormat(customFormat, node))
				return nil
			}

			if terse {
				modTime := "-"
				if node.ModTime != "" {
					modTime = node.ModTime
				}
				fmt.Fprintf(out, "%s\t%s\t%d\t%s\n", node.Path, node.Kind, node.Size, modTime)
				return nil
			}

			fmt.Fprintf(out, "  Path: %s\n", node.Path)
			fmt.Fprintf(out, "  Name: %s\n", node.Name)
			fmt.Fprintf(out, "  Kind: %s\n", node.Kind)
			fmt.Fprintf(out, "  Size: %d\n", node.Size)
			modTime := "-"
			if node.ModTime != "" {
				modTime = node.ModTime
			}
			fmt.Fprintf(out, "  ModTime: %s\n", modTime)
			if len(node.Meta) > 0 {
				fmt.Fprintf(out, "  Meta: %s\n", formatMeta(node.Meta))
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&customFormat, "format", "c", "", "custom format string")
	cmd.Flags().BoolVarP(&terse, "terse", "f", false, "terse single-line format")
	return cmd
}

func expandStatFormat(format string, node store.Node) string {
	s := format
	s = strings.ReplaceAll(s, "%%", "\x00")
	s = strings.ReplaceAll(s, "%n", node.Name)
	s = strings.ReplaceAll(s, "%p", node.Path)
	s = strings.ReplaceAll(s, "%k", node.Kind)
	s = strings.ReplaceAll(s, "%s", fmt.Sprintf("%d", node.Size))
	modTime := "-"
	if node.ModTime != "" {
		modTime = node.ModTime
	}
	s = strings.ReplaceAll(s, "%y", modTime)
	meta := formatMeta(node.Meta)
	s = strings.ReplaceAll(s, "%m", meta)
	s = strings.ReplaceAll(s, "\x00", "%")
	return s
}

func formatMeta(meta map[string]string) string {
	if len(meta) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(meta))
	for k, v := range meta {
		pairs = append(pairs, k+"="+v)
	}
	return strings.Join(pairs, ", ")
}
