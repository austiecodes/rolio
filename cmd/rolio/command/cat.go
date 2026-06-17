package command

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/austiecodes/rolio/internal/store"
)

func NewCatCommand(adapter store.Adapter) *cobra.Command {
	var numberAll, numberNonBlank, squeezeBlanks bool

	cmd := &cobra.Command{
		Use:   "cat <path>",
		Short: "Print VFS file content",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]

			resp, err := adapter.Cat(cmd.Context(), store.CatRequest{Path: path})
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if !numberAll && !numberNonBlank && !squeezeBlanks {
				fmt.Fprint(out, resp.Content)
				return nil
			}

			lines := strings.Split(resp.Content, "\n")
			if len(lines) > 0 && lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1]
			}
			return printLines(out, lines, numberAll, numberNonBlank, squeezeBlanks)
		},
	}
	cmd.Flags().BoolVarP(&numberAll, "number", "n", false, "number all output lines")
	cmd.Flags().BoolVarP(&numberNonBlank, "number-nonblank", "b", false, "number non-blank output lines")
	cmd.Flags().BoolVarP(&squeezeBlanks, "squeeze-blank", "s", false, "squeeze multiple blank lines into one")
	return cmd
}

func printLines(out io.Writer, lines []string, numberAll, numberNonBlank, squeezeBlanks bool) error {
	if squeezeBlanks {
		lines = squeezeBlankLines(lines)
	}

	if numberNonBlank {
		n := 0
		for _, line := range lines {
			if strings.TrimSpace(line) == "" {
				fmt.Fprintln(out, line)
			} else {
				n++
				fmt.Fprintf(out, "%6d  %s\n", n, line)
			}
		}
	} else if numberAll {
		for i, line := range lines {
			fmt.Fprintf(out, "%6d  %s\n", i+1, line)
		}
	} else {
		for _, line := range lines {
			fmt.Fprintln(out, line)
		}
	}
	return nil
}

func squeezeBlankLines(lines []string) []string {
	var result []string
	prevBlank := false
	for _, line := range lines {
		blank := strings.TrimSpace(line) == ""
		if blank && prevBlank {
			continue
		}
		result = append(result, line)
		prevBlank = blank
	}
	return result
}
