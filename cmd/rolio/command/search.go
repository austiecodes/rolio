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
		Long:  "Search documents by keyword using full-text search.\n\nA document is a result when its text or its summary contains one or more of\nthe words. Documents with more of the words come first. Each result shows the\nabstract of the document, when it has one, and the lines that agree best with\nthe query. These lines are excerpts. When more lines of the document agree\nwith the query, the result gives their number: read the document with\nrolio cat. The directories whose summaries agree with the query come last:\nread one with rolio overview <directory>.",
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
	cmd.Flags().IntVar(&limit, "limit", 10, "max results; the server uses a maximum of 100")
	cmd.Flags().IntVar(&searchOffset, "offset", 0, "skip first N results")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	return cmd
}

// oneLine puts a text on one line.
func oneLine(text string) string { return strings.Join(strings.Fields(text), " ") }

// printSearchHuman prints one block for each document: its line, then its
// abstract, its passages, and the number of other lines that agree with the
// query, with an indent, then an empty line. Without a
// document it prints one line that says so. The directories come last.
// test/bench/trace.go reads this format.
func printSearchHuman(cmd *cobra.Command, resp *store.SearchResponse, offset int) error {
	out := cmd.OutOrStdout()
	if len(resp.Results) == 0 {
		fmt.Fprintln(out, "no results found")
	}
	for _, r := range resp.Results {
		fmt.Fprintf(out, "%s    [rank: %.2f, %s]\n", r.Path, r.Rank, formatSize(r.Size))
		if abstract := oneLine(r.Abstract); abstract != "" {
			fmt.Fprintf(out, "  abstract: %s\n", abstract)
		}
		for _, passage := range r.Passages {
			if passage = oneLine(passage); passage != "" {
				fmt.Fprintf(out, "  > %s\n", passage)
			}
		}
		// The passages are a part of the document. Tell the reader when
		// the document has more. With a lower limit the line shows also
		// when the number is 0, because other lines can agree.
		if r.MoreLines > 0 || r.MoreLinesMin {
			bound := ""
			if r.MoreLinesMin {
				bound = "+"
			}
			fmt.Fprintf(out, "  (%d%s more lines match; rolio cat %s shows the document)\n", r.MoreLines, bound, r.Path)
		}
		fmt.Fprintln(out)
	}
	if len(resp.Directories) > 0 {
		fmt.Fprintln(out, "directories:")
		for _, d := range resp.Directories {
			fmt.Fprintln(out, strings.TrimRight(fmt.Sprintf("  %s/    %s", strings.TrimSuffix(d.Path, "/"), oneLine(d.Abstract)), " "))
		}
	}
	printPaginationSummary(out, offset, len(resp.Results), resp.Total)
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
