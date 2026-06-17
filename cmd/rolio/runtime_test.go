package main

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/austiecodes/rolio/internal/server"
	"github.com/austiecodes/rolio/internal/store"
	"github.com/austiecodes/rolio/internal/store/memory"
	"github.com/austiecodes/rolio/internal/vfs"
)

func TestRuntimeUsesOneTreeWithoutProjectState(t *testing.T) {
	tree, err := vfs.New([]vfs.File{{Path: "/projects/a/guide.md", Content: "help authentication"}, {Path: "/shared/auth.md", Content: "help authentication"}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.NewHandler(memory.New(tree)))
	defer srv.Close()
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("ROLIO_SERVER", srv.URL)
	t.Setenv("ROLIO_CONFIG", filepath.Join(dir, "missing.toml"))
	for _, args := range [][]string{{"cat", "/projects/a/guide.md"}, {"search", "help", "--path", "/shared", "--json"}, {"write", "/shared/new.md", "help"}, {"cat", "/shared/new.md"}} {
		var out, stderr bytes.Buffer
		if code := run(args, &out, &stderr); code != 0 {
			t.Fatalf("%v: %d %s", args, code, stderr.String())
		}
		if args[0] == "search" && strings.Contains(out.String(), "/projects/a") {
			t.Fatal("search escaped explicit path")
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("CLI created local project state: %v %v", entries, err)
	}
}
func TestRemovedCommandsFailWithoutConfiguration(t *testing.T) {
	t.Setenv("ROLIO_CONFIG", "/missing/config")
	for _, name := range []string{"repo", "docset", "mount", "sync", "hook", "init", "config", "locate", "materialize", "dematerialize"} {
		var out, stderr bytes.Buffer
		if code := run([]string{name}, &out, &stderr); code == 0 || !strings.Contains(stderr.String(), "unknown command") {
			t.Errorf("%s: code=%d error=%s", name, code, stderr.String())
		}
	}
}
func TestConditionalWriteFlag(t *testing.T) {
	fake := &fakeClient{}
	executeWithClient(t, fake, "write", "/shared/a.md", "text", "--expected-hash", "*")
	if len(fake.putReqs) != 1 || fake.putReqs[0].ExpectedHash != "*" {
		t.Fatalf("requests: %+v", fake.putReqs)
	}
	executeWithClient(t, fake, "rm", "/shared/a.md", "--expected-hash", store.HashContent("text"))
	if len(fake.deleteReqs) != 1 || fake.deleteReqs[0].ExpectedHash != store.HashContent("text") {
		t.Fatalf("requests: %+v", fake.deleteReqs)
	}
}
func TestGlobalConfigDoesNotNeedRepo(t *testing.T) {
	srv := httptest.NewServer(server.NewHandler(&fakeClient{}))
	defer srv.Close()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROLIO_SERVER", "")
	t.Setenv("ROLIO_CONFIG", "")
	if err := os.Mkdir(filepath.Join(home, ".rolio"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".rolio", "settings.toml"), []byte("[server]\naddr = \""+srv.URL+"\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	var out, stderr bytes.Buffer
	if code := run([]string{"ls", "/"}, &out, &stderr); code != 0 {
		t.Fatalf("code=%d error=%s", code, stderr.String())
	}
}
