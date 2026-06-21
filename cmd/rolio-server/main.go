package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/austiecodes/rolio/internal/config"
	"github.com/austiecodes/rolio/internal/server"
	"github.com/austiecodes/rolio/internal/store/postgres"
	"github.com/austiecodes/rolio/internal/summary"
)

// summaryDelay collects the writes of a short period into one summary job.
// summaryRetry is the time before the second attempt of a failed generation.
const (
	summaryDelay = 2 * time.Second
	summaryRetry = 30 * time.Second
)

func main() {
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func newRootCommand() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{Use: "rolio-server", Short: "Serve a shared knowledge tree", Args: cobra.NoArgs, SilenceUsage: true, SilenceErrors: true}
	cmd.Flags().StringVar(&configPath, "config", "rolio-server.toml", "server configuration file")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.LoadServer(configPath)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		var generator summary.Generator
		if cfg.Summary.URL != "" {
			generator = &summary.ChatGenerator{URL: cfg.Summary.URL, Model: cfg.Summary.Model, APIKey: cfg.Summary.APIKey}
		}
		adapter, err := postgres.Connect(ctx, postgres.Config{DSN: cfg.Backend.Postgres.DSN, Schema: cfg.Backend.Postgres.Schema, Language: cfg.Language, Generator: generator, SummaryDelay: summaryDelay, SummaryRetry: summaryRetry})
		if err != nil {
			return err
		}
		defer adapter.Close()
		summaries := make(chan struct{})
		go func() {
			defer close(summaries)
			if generator == nil {
				return
			}
			if err := adapter.RunSummaries(ctx, cfg.Summary.Concurrency); err != nil && ctx.Err() == nil {
				fmt.Fprintln(os.Stderr, "summary queue:", err)
			}
		}()
		// The workers use the adapter. Close it after they stop.
		defer func() { <-summaries }()
		srv := &http.Server{Addr: cfg.Addr, Handler: server.NewHandler(adapter), ReadHeaderTimeout: 10 * time.Second}
		done := make(chan struct{})
		go func() {
			defer close(done)
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdown); err != nil {
				_ = srv.Close()
			}
		}()
		err = srv.ListenAndServe()
		stop()
		<-done
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
	return cmd
}
