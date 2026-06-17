package command

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/austiecodes/rolio/internal/store"
)

func NewGrepCommand(adapter store.Adapter) *cobra.Command {
	var regex, countOnly, filesOnly, onlyMatch bool
	var caseInsensitive, invert, wholeWord, wholeLine bool
	var contextAfter, contextBefore, context int
	var allFiles bool
	var includePattern, excludePattern string

	cmd := &cobra.Command{
		Use:   "grep <pattern> [path]",
		Short: "Search VFS files",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			pattern := args[0]

			before := contextBefore
			after := contextAfter
			if context > 0 {
				if !cmd.Flags().Changed("before-context") {
					before = context
				}
				if !cmd.Flags().Changed("after-context") {
					after = context
				}
			}

			resp, err := adapter.Grep(cmd.Context(), store.GrepRequest{
				Pattern:         pattern,
				Path:            argPath(args[1:], "/"),
				Regex:           regex,
				CaseInsensitive: caseInsensitive,
				Invert:          invert,
				WholeWord:       wholeWord,
				WholeLine:       wholeLine,
				ContextBefore:   before,
				ContextAfter:    after,
				All:             allFiles,
				Include:         includePattern,
				Exclude:         excludePattern,
			})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			if filesOnly {
				seen := map[string]bool{}
				for _, match := range resp.Matches {
					if !seen[match.Path] {
						fmt.Fprintln(out, match.Path)
						seen[match.Path] = true
					}
				}
				return nil
			}

			if countOnly {
				counts := map[string]int{}
				for _, match := range resp.Matches {
					counts[match.Path]++
				}
				for _, match := range resp.Matches {
					if counts[match.Path] >= 0 {
						fmt.Fprintf(out, "%s:%d\n", match.Path, counts[match.Path])
						counts[match.Path] = -1
					}
				}
				return nil
			}

			if onlyMatch && !regex {
				for _, match := range resp.Matches {
					remaining := match.Text
					for {
						idx := strings.Index(remaining, pattern)
						if idx < 0 {
							break
						}
						fmt.Fprintf(out, "%s:%d:%s\n", match.Path, match.Line, pattern)
						remaining = remaining[idx+len(pattern):]
					}
				}
				return nil
			}

			for _, match := range resp.Matches {
				for _, line := range match.Before {
					fmt.Fprintf(out, "%s-%s\n", match.Path, line)
				}
				fmt.Fprintf(out, "%s:%d:%s\n", match.Path, match.Line, match.Text)
				for _, line := range match.After {
					fmt.Fprintf(out, "%s-%s\n", match.Path, line)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&regex, "regex", "E", false, "treat pattern as a regular expression")
	cmd.Flags().BoolVarP(&countOnly, "count", "c", false, "print only a count of matching lines per file")
	cmd.Flags().BoolVarP(&filesOnly, "files-with-matches", "l", false, "print only names of files with matches")
	cmd.Flags().BoolVarP(&onlyMatch, "only-matching", "o", false, "show only the matching part of the line")
	cmd.Flags().BoolVarP(&caseInsensitive, "ignore-case", "i", false, "case-insensitive search")
	cmd.Flags().BoolVarP(&invert, "invert-match", "v", false, "show non-matching lines")
	cmd.Flags().BoolVarP(&wholeWord, "word-regexp", "w", false, "match whole words only")
	cmd.Flags().BoolVarP(&wholeLine, "line-regexp", "x", false, "match whole lines only")
	cmd.Flags().IntVarP(&contextAfter, "after-context", "A", 0, "lines of trailing context")
	cmd.Flags().IntVarP(&contextBefore, "before-context", "B", 0, "lines of leading context")
	cmd.Flags().IntVarP(&context, "context", "C", 0, "lines of context (before and after)")
	cmd.Flags().BoolVarP(&allFiles, "all", "a", false, "search hidden files")
	cmd.Flags().StringVar(&includePattern, "include", "", "only search files matching glob")
	cmd.Flags().StringVar(&excludePattern, "exclude", "", "skip files matching glob")
	return cmd
}
