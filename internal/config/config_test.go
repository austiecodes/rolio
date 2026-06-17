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
	srv, err := LoadServer(configFile(t, "[backend.postgres]\ndsn='${ROLIO_TEST_CONFIG_DSN}'"))
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
