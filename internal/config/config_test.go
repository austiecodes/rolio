package config

import (
	"os"
	"path/filepath"
	"testing"
)

func configFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestMinimalConfiguration(t *testing.T) {
	cfg, err := LoadCLI(configFile(t, "[server]\naddr='http://localhost:4837'"))
	if err != nil || cfg.Server.Addr != "http://localhost:4837" {
		t.Fatalf("CLI: %+v %v", cfg, err)
	}
	t.Setenv("ROLIO_TEST_CONFIG_DSN", "postgres://example/test")
	srv, err := LoadServer(configFile(t, "language='en'\n[backend.postgres]\ndsn='${ROLIO_TEST_CONFIG_DSN}'"))
	if err != nil || srv.Backend.Postgres.DSN != "postgres://example/test" || srv.Backend.Type != "postgres" {
		t.Fatalf("server: %+v %v", srv, err)
	}
}
func TestRemovedConfigurationIsRejected(t *testing.T) {
	for _, old := range []string{"repo='a'\n", "[mount]\ninclude=['/']\n", "[cache]\nmetadata_ttl='5m'\n"} {
		if _, err := LoadCLI(configFile(t, old+"\n[server]\naddr='http://localhost:4837'")); err == nil {
			t.Errorf("accepted %s", old)
		}
	}
	if _, err := LoadServer(configFile(t, "[backend]\ntype='doc_postgres'\n[backend.postgres]\ndsn='postgres://example/test'")); err == nil {
		t.Fatal("accepted removed backend")
	}
	if _, err := LoadServer(configFile(t, "[backend.postgres]\nschema='public'")); err == nil {
		t.Fatal("accepted missing DSN")
	}
}

func TestLanguageAndSummaryConfiguration(t *testing.T) {
	for _, language := range []string{"", "auto", "ZH", "fr"} {
		if _, err := LoadServer(configFile(t, "language='"+language+"'\n[backend.postgres]\ndsn='postgres://test'")); err == nil {
			t.Errorf("accepted language %q", language)
		}
	}
	for _, language := range []string{"zh", "en"} {
		cfg, err := LoadServer(configFile(t, "language='"+language+"'\n[backend.postgres]\ndsn='postgres://test'"))
		if err != nil || cfg.Language != language {
			t.Fatal(cfg, err)
		}
	}
	for _, url := range []string{"", "file:///tmp/model", "http://user:secret@example.com/v1/chat/completions"} {
		_, err := LoadServer(configFile(t, "language='zh'\n[backend.postgres]\ndsn='postgres://test'\n[summary]\nmodel='test'\nurl='"+url+"'"))
		if err == nil {
			t.Errorf("accepted URL %s", url)
		}
	}
	base := "language='zh'\n[backend.postgres]\ndsn='postgres://test'\n[summary]\nmodel='test'\nurl='https://example.com/v1/chat/completions'\n"
	if cfg, err := LoadServer(configFile(t, base)); err != nil || cfg.Summary.Concurrency != 4 {
		t.Errorf("default concurrency: %+v, %v", cfg.Summary, err)
	}
	if cfg, err := LoadServer(configFile(t, base+"concurrency=8")); err != nil || cfg.Summary.Concurrency != 8 {
		t.Errorf("concurrency: %+v, %v", cfg.Summary, err)
	}
	for _, n := range []string{"-1", "65"} {
		if _, err := LoadServer(configFile(t, base+"concurrency="+n)); err == nil {
			t.Errorf("accepted concurrency %s", n)
		}
	}
}
