package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/austiecodes/rolio/internal/client"
	"github.com/austiecodes/rolio/internal/server"
	"github.com/austiecodes/rolio/internal/store"
	"github.com/austiecodes/rolio/internal/store/postgres"
)

func TestSingleTreeHTTP(t *testing.T) {
	dsn := os.Getenv("ROLIO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set ROLIO_TEST_POSTGRES_DSN to run PostgreSQL integration tests")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("rolio_test_%d", time.Now().UnixNano())
	adapter, err := postgres.Connect(ctx, postgres.Config{DSN: dsn, Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		adapter.Close()
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Error(err)
			return
		}
		defer pool.Close()
		if _, err := pool.Exec(ctx, "drop schema "+schema+" cascade"); err != nil {
			t.Error(err)
		}
	})
	httpServer := httptest.NewServer(server.NewHandler(adapter))
	defer httpServer.Close()
	cli := client.New(httpServer.URL)
	put := func(p, content string) string {
		t.Helper()
		r, err := cli.Put(ctx, store.PutRequest{Path: p, Content: content})
		if err != nil {
			t.Fatal(err)
		}
		return r.Node.Hash
	}
	paths := []string{"/projects/payment/guide.md", "/projects/console/guide.md", "/shared/auth/guide.md", "/projects/pay_1/guide.md", "/projects/payX1/guide.md", "/projects/pay%/guide.md", "/projects/payment-extra/guide.md"}
	for _, p := range paths {
		put(p, "authentication retry reference")
	}
	put("/shared/空 格#?%.md", "")
	t.Run("read and browse", func(t *testing.T) {
		r, err := cli.Cat(ctx, store.CatRequest{Path: "/shared/空 格#?%.md"})
		if err != nil || r.Content != "" {
			t.Fatalf("cat: %+v %v", r, err)
		}
		r, err = cli.Cat(ctx, store.CatRequest{Path: paths[0]})
		if err != nil {
			t.Fatal(err)
		}
		_, err = cli.Cat(ctx, store.CatRequest{Path: paths[0], IfNoneMatch: r.Hash})
		if !errors.Is(err, store.ErrNotModified) {
			t.Fatalf("conditional cat: %v", err)
		}
		ls, err := cli.LS(ctx, store.LSRequest{Path: "/", All: true})
		if err != nil || len(ls.Nodes) != 2 {
			t.Fatalf("ls: %+v %v", ls, err)
		}
		tree, err := cli.Tree(ctx, store.TreeRequest{Path: "/projects/payment", Depth: 2})
		if err != nil || !strings.Contains(tree.Text, "guide.md") {
			t.Fatalf("tree: %+v %v", tree, err)
		}
		find, err := cli.Find(ctx, store.FindRequest{Path: "/shared", Name: "*.md", All: true})
		if err != nil || len(find.Nodes) != 2 {
			t.Fatalf("find: %+v %v", find, err)
		}
		stat, err := cli.Stat(ctx, store.StatRequest{Path: paths[0]})
		if err != nil || stat.Node.Hash == "" {
			t.Fatalf("stat: %+v %v", stat, err)
		}
		glob, err := cli.Glob(ctx, store.GlobRequest{Pattern: "projects/**/guide.md", Limit: 2, Offset: 1})
		if err != nil || glob.Total != 6 || len(glob.Results) != 2 {
			t.Fatalf("glob: %+v %v", glob, err)
		}
	})
	t.Run("literal prefix search", func(t *testing.T) {
		for _, scope := range []string{"/projects/payment", "/projects/pay_1", "/projects/pay%"} {
			r, err := cli.Search(ctx, store.SearchRequest{Query: "authentication", Path: scope})
			if err != nil || r.Total != 1 || r.Results[0].Path != scope+"/guide.md" {
				t.Fatalf("search %s: %+v %v", scope, r, err)
			}
			g, err := cli.Grep(ctx, store.GrepRequest{Path: scope, Pattern: "authentication"})
			if err != nil || len(g.Matches) != 1 {
				t.Fatalf("grep %s: %+v %v", scope, g, err)
			}
		}
		r, err := cli.Search(ctx, store.SearchRequest{Query: "authentication", Path: "/", Limit: 2, Offset: 1})
		if err != nil || r.Total != 7 || len(r.Results) != 2 {
			t.Fatalf("global search: %+v %v", r, err)
		}
	})
	t.Run("conditional mutation", func(t *testing.T) {
		p := "/shared/cas.md"
		hash := put(p, "before")
		_, err := cli.Put(ctx, store.PutRequest{Path: p, Content: "bad", ExpectedHash: "*"})
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("create-only: %v", err)
		}
		_, err = cli.Put(ctx, store.PutRequest{Path: "/missing.md", ExpectedHash: hash})
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("missing expected file: %v", err)
		}
		r, err := cli.Edit(ctx, store.EditRequest{Path: p, Old: "before", New: "after", ExpectedHash: hash})
		if err != nil || r.Content != "after" {
			t.Fatalf("edit: %+v %v", r, err)
		}
		_, err = cli.Delete(ctx, store.DeleteRequest{Path: p, ExpectedHash: hash})
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stale delete: %v", err)
		}
		_, err = cli.Delete(ctx, store.DeleteRequest{Path: p, ExpectedHash: store.HashContent("after")})
		if err != nil {
			t.Fatal(err)
		}
		_, err = cli.Cat(ctx, store.CatRequest{Path: p})
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("deleted file: %v", err)
		}
	})
	t.Run("concurrent conditional writes", func(t *testing.T) {
		p := "/shared/race.md"
		hash := put(p, "original")
		var wg sync.WaitGroup
		out := make(chan error, 2)
		for _, body := range []string{"one", "two"} {
			wg.Add(1)
			go func(body string) {
				defer wg.Done()
				_, err := cli.Put(ctx, store.PutRequest{Path: p, Content: body, ExpectedHash: hash})
				out <- err
			}(body)
		}
		wg.Wait()
		close(out)
		success, conflict := 0, 0
		for err := range out {
			if err == nil {
				success++
			} else if errors.Is(err, store.ErrConflict) {
				conflict++
			} else {
				t.Fatal(err)
			}
		}
		if success != 1 || conflict != 1 {
			t.Fatalf("success=%d conflict=%d", success, conflict)
		}
	})
	t.Run("directory invariants", func(t *testing.T) {
		if _, err := cli.Put(ctx, store.PutRequest{Path: "/shared", Content: "bad"}); err == nil {
			t.Fatal("overwrote directory")
		}
		if _, err := cli.Put(ctx, store.PutRequest{Path: paths[0] + "/child", Content: "bad"}); err == nil {
			t.Fatal("created child of file")
		}
		if _, err := cli.Delete(ctx, store.DeleteRequest{Path: "/"}); err == nil {
			t.Fatal("deleted root")
		}
		if _, err := cli.Delete(ctx, store.DeleteRequest{Path: "/projects/pay%"}); err != nil {
			t.Fatal(err)
		}
		if _, err := cli.Cat(ctx, store.CatRequest{Path: paths[0]}); err != nil {
			t.Fatalf("recursive deletion escaped literal prefix: %v", err)
		}
	})
	t.Run("removed APIs", func(t *testing.T) {
		for _, p := range []string{"/v1/repos", "/v1/repos/cat?repo=test&path=/", "/v1/docsets", "/v1/mount-sources", "/v1/cache", "/v1/usage-events", "/v1/locate", "/v1/hashes"} {
			r, err := http.Get(httpServer.URL + p)
			if err != nil {
				t.Fatal(err)
			}
			r.Body.Close()
			if r.StatusCode != 404 {
				t.Errorf("%s: %d", p, r.StatusCode)
			}
		}
	})
	t.Run("restart persistence", func(t *testing.T) {
		reopened, err := postgres.Connect(ctx, postgres.Config{DSN: dsn, Schema: schema})
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		r, err := reopened.Cat(ctx, store.CatRequest{Path: paths[0]})
		if err != nil || r.Content != "authentication retry reference" {
			t.Fatalf("reopen: %+v %v", r, err)
		}
	})
}
