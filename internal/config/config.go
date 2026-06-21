package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type CLIConfig struct {
	Server ServerRef `toml:"server"`
}
type ServerRef struct {
	Addr string `toml:"addr"`
}
type ServerConfig struct {
	Language string        `toml:"language"`
	Summary  SummaryConfig `toml:"summary"`
	Addr     string        `toml:"addr"`
	Backend  BackendConfig `toml:"backend"`
}
type SummaryConfig struct {
	URL    string `toml:"url"`
	Model  string `toml:"model"`
	APIKey string `toml:"api_key"`
	// Concurrency is the number of summaries that the server generates at
	// the same time.
	Concurrency int `toml:"concurrency"`
}
type BackendConfig struct {
	Type     string         `toml:"type"`
	Postgres PostgresConfig `toml:"postgres"`
}
type PostgresConfig struct {
	DSN    string `toml:"dsn"`
	Schema string `toml:"schema"`
}

func load(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	if err := toml.NewDecoder(strings.NewReader(os.ExpandEnv(string(data)))).DisallowUnknownFields().Decode(out); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	return nil
}
func LoadCLI(path string) (CLIConfig, error) {
	var cfg CLIConfig
	if err := load(path, &cfg); err != nil {
		return cfg, err
	}
	if cfg.Server.Addr == "" {
		return cfg, fmt.Errorf("server.addr is required")
	}
	return cfg, nil
}
func LoadServer(path string) (ServerConfig, error) {
	var cfg ServerConfig
	if err := load(path, &cfg); err != nil {
		return cfg, err
	}
	if cfg.Language != "zh" && cfg.Language != "en" {
		return cfg, fmt.Errorf("language is required and must be zh or en; example: language = \"zh\"")
	}
	if cfg.Summary.Concurrency < 0 || cfg.Summary.Concurrency > 64 {
		return cfg, fmt.Errorf("summary.concurrency must be between 1 and 64; the default is 4")
	}
	if cfg.Summary.Concurrency == 0 {
		cfg.Summary.Concurrency = 4
	}
	if cfg.Summary.URL != "" || cfg.Summary.Model != "" || cfg.Summary.APIKey != "" {
		u, err := url.Parse(cfg.Summary.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || cfg.Summary.Model == "" {
			return cfg, fmt.Errorf("summary.url must be an http(s) chat completions endpoint and summary.model is required")
		}
	}
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:4837"
	}
	if cfg.Backend.Type == "" {
		cfg.Backend.Type = "postgres"
	}
	if cfg.Backend.Type != "postgres" {
		return cfg, fmt.Errorf("unsupported backend.type %q", cfg.Backend.Type)
	}
	if cfg.Backend.Postgres.DSN == "" {
		return cfg, fmt.Errorf("backend.postgres.dsn is required")
	}
	return cfg, nil
}
