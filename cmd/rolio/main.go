package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/austiecodes/rolio/cmd/rolio/command"
	"github.com/austiecodes/rolio/internal/client"
	"github.com/austiecodes/rolio/internal/config"
	"github.com/austiecodes/rolio/internal/store"
)

func newRootCommand(adapter store.Adapter) *cobra.Command {
	cmd := &cobra.Command{Use: "rolio", Short: "Read, write and search a shared knowledge tree", Args: cobra.NoArgs, SilenceUsage: true, SilenceErrors: true}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error { return cmd.Help() }
	cmd.CompletionOptions.DisableDefaultCmd = true
	cmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		switch cmd.Name() {
		case "cat", "stat", "ls", "tree", "rm", "write", "edit", "find", "glob":
			if len(args) > 0 && strings.Contains(args[0], "://") {
				return fmt.Errorf("use a knowledge path such as /projects/example/docs/guide.md")
			}
		}
		return nil
	}
	cmd.AddCommand(
		command.NewLSCommand(adapter),
		command.NewTreeCommand(adapter),
		command.NewCatCommand(adapter),
		command.NewGrepCommand(adapter),
		command.NewFindCommand(adapter),
		command.NewStatCommand(adapter),
		command.NewWriteCommand(adapter),
		command.NewEditCommand(adapter),
		command.NewDeleteCommand(adapter),
		command.NewSearchCommand(adapter),
		command.NewGlobCommand(adapter),
	)
	cmd.AddCommand(command.NewContextCommands(adapter)...)
	return cmd
}
func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
func run(args []string, stdout, stderr io.Writer) int {
	connection := client.New("")
	cmd := newRootCommand(connection)
	validate := cmd.PersistentPreRunE
	cmd.PersistentPreRunE = func(selected *cobra.Command, args []string) error {
		if err := validate(selected, args); err != nil {
			return err
		}
		if selected == cmd {
			return nil
		}
		addr := os.Getenv("ROLIO_SERVER")
		if addr == "" {
			configPath := os.Getenv("ROLIO_CONFIG")
			if configPath == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				configPath = filepath.Join(home, ".rolio", "settings.toml")
			}
			cfg, err := config.LoadCLI(configPath)
			if err != nil {
				return err
			}
			addr = cfg.Server.Addr
		}
		*connection = *client.New(addr)
		return nil
	}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
