package command

import (
	"encoding/json"
	"fmt"
	"github.com/austiecodes/rolio/internal/store"
	"github.com/spf13/cobra"
)

func NewContextCommands(adapter store.Adapter) []*cobra.Command {
	var commands []*cobra.Command
	for _, name := range []string{"abstract", "overview", "summary", "refresh", "reindex"} {
		cmd := &cobra.Command{Use: name + " <directory>", Args: cobra.ExactArgs(1)}
		switch name {
		case "abstract":
			cmd.Short = "Read a directory's L0 abstract"
		case "overview":
			cmd.Short = "Read a directory's L1 overview"
		case "summary":
			cmd.Short = "Inspect directory summary status and metadata"
		case "refresh":
			cmd.Short = "Generate L0/L1 for a directory using the configured model"
		case "reindex":
			cmd.Use = name
			cmd.Args = cobra.NoArgs
			cmd.Short = "Rebuild the search index for the configured language"
		}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			contextStore, ok := adapter.(store.ContextStore)
			if !ok {
				return store.ErrNotSupported
			}
			if name == "reindex" {
				r, err := contextStore.Reindex(cmd.Context())
				if err != nil {
					return err
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(r)
			}
			var r *store.Summary
			var err error
			if name == "refresh" {
				r, err = contextStore.Refresh(cmd.Context(), args[0])
			} else {
				r, err = contextStore.Summary(cmd.Context(), args[0])
			}
			if err != nil {
				return err
			}
			if name == "abstract" || name == "overview" {
				body := r.Abstract
				if name == "overview" {
					body = r.Overview
				}
				if body == "" {
					return fmt.Errorf("%w: summary is %s; run rolio refresh %s", store.ErrContentNotReady, r.Status, r.Path)
				}
				if r.Status != "ready" {
					fmt.Fprintf(cmd.ErrOrStderr(), "summary status: %s (language: %s)\n", r.Status, r.Language)
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), body)
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(r)
		}
		commands = append(commands, cmd)
	}
	return commands
}
