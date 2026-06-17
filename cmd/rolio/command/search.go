package command

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/austiecodes/rolio/internal/store"
)

func NewSearchCommand(adapter store.Adapter) *cobra.Command {
	var searchPath string
	var limit, searchOffset int
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search documents by keyword",
		Long:  "Search documents by keyword using full-text search.\nReturns ranked results with relevance scores and matching snippets.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateNonNeg([]string{"limit", "offset"}, limit, searchOffset); err != nil {
				return err
			}
			resp, err := adapter.Search(cmd.Context(), store.SearchRequest{
				Query:  args[0],
				Path:   searchPath,
				Limit:  limit,
				Offset: searchOffset,
			})
			if err != nil {
				return err
			}
			if jsonOutput {
				return printSearchJSON(cmd, resp)
			}
			return printSearchHuman(cmd, resp, searchOffset)
		},
	}
	cmd.Flags().StringVar(&searchPath, "path", "", "scope search to path prefix")
	cmd.Flags().IntVar(&limit, "limit", 20, "max results")
	cmd.Flags().IntVar(&searchOffset, "offset", 0, "skip first N results")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

func printSearchHuman(cmd *cobra.Command, resp *store.SearchResponse, offset int) error {
	if len(resp.Results) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no results found")
		return nil
	}
	for _, r := range resp.Results {
		sizeStr := formatSize(r.Size)
		fmt.Fprintf(cmd.OutOrStdout(), "%s    [rank: %.2f, %s]\n", r.Path, r.Rank, sizeStr)
		if r.Snippet != "" {
			snippet := strings.ReplaceAll(r.Snippet, "**", "")
			for _, line := range strings.Split(snippet, "\n") {
				if line != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", line)
				}
			}
		}
		fmt.Fprintln(cmd.OutOrStdout())
	}
	printPaginationSummary(cmd.OutOrStdout(), offset, len(resp.Results), resp.Total)
	return nil
}

func printSearchJSON(cmd *cobra.Command, resp *store.SearchResponse) error {
	data, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return nil
}

func formatSize(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%dB", size)
	}
	if size < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(size)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(size)/(1024*1024))
}
