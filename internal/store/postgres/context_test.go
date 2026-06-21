package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/austiecodes/rolio/internal/client"
	"github.com/austiecodes/rolio/internal/server"
	"github.com/austiecodes/rolio/internal/store"
	"github.com/austiecodes/rolio/internal/store/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestContextLifecycle(t *testing.T) {
	dsn := os.Getenv("ROLIO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set ROLIO_TEST_POSTGRES_DSN")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("rolio_context_%d", time.Now().UnixNano())
	adapter, err := postgres.Connect(ctx, postgres.Config{DSN: dsn, Schema: schema, Language: "zh"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		adapter.Close()
		pool, e := pgxpool.New(ctx, dsn)
		if e != nil {
			t.Error(e)
			return
		}
		defer pool.Close()
		if _, e = pool.Exec(ctx, "drop schema "+schema+" cascade"); e != nil {
			t.Error(e)
		}
	})
	srv := httptest.NewServer(server.NewHandler(adapter))
	defer srv.Close()
	cli := client.New(srv.URL)
	raw := "---\ntitle: 支付参考\ntags: [认证, API]\nsource: https://example.com\n---\n\n身份认证与支付失败重试。Authentication retries are useful.\n"
	put := func(content string) {
		t.Helper()
		if _, e := cli.Put(ctx, store.PutRequest{Path: "/docs/guide.md", Content: content}); e != nil {
			t.Fatal(e)
		}
	}
	put(raw)
	r, err := cli.Cat(ctx, store.CatRequest{Path: "/docs/guide.md"})
	if err != nil || r.Content != raw || r.Metadata["title"] != "支付参考" || strings.Contains(r.Body, "tags:") {
		t.Fatalf("cat: %+v %v", r, err)
	}
	for _, query := range []string{"认证", "支付失败", "付", "authentication", "API"} {
		r, e := cli.Search(ctx, store.SearchRequest{Query: query})
		if e != nil || r.Total != 1 {
			t.Fatalf("search %s: %+v %v", query, r, e)
		}
	}
	for _, bad := range []string{"---\ntags: nope\n---\nbody", "---\nrevision: 123\n---\nbody"} {
		if _, e := cli.Put(ctx, store.PutRequest{Path: "/docs/guide.md", Content: bad}); e == nil {
			t.Fatal("accepted invalid metadata")
		}
	}
	if _, e := cli.Edit(ctx, store.EditRequest{Path: "/docs/guide.md", Old: "title: 支付参考", New: "title: [invalid]"}); e == nil {
		t.Fatal("accepted invalid metadata edit")
	}
	r, err = cli.Cat(ctx, store.CatRequest{Path: "/docs/guide.md"})
	if err != nil || r.Content != raw {
		t.Fatal("failed write changed source", r, err)
	}
	if _, e := cli.Put(ctx, store.PutRequest{Path: "/docs/.abstract.md", Content: "spoof"}); e == nil {
		t.Fatal("accepted system summary write")
	}
	s, err := cli.Summary(ctx, "/docs")
	if err != nil || s.Status != "missing" {
		t.Fatal(s, err)
	}
	// The summary queue has its tests in summarize_test.go.
	if _, err = cli.Refresh(ctx, "/docs"); !errors.Is(err, store.ErrNotSupported) {
		t.Fatal("refresh without a model", err)
	}
	put(raw + "更新内容。\n")
	// A different configured language must not silently reuse the old index.
	english, err := postgres.Connect(ctx, postgres.Config{DSN: dsn, Schema: schema, Language: "en"})
	if err != nil {
		t.Fatal(err)
	}
	defer english.Close()
	settings, err := english.Settings(ctx)
	if err != nil || settings.Index.Stale != 1 {
		t.Fatal(settings, err)
	}
	if _, err = english.Search(ctx, store.SearchRequest{Query: "authentication"}); !errors.Is(err, store.ErrConflict) {
		t.Fatal("accepted old index", err)
	}
	if _, err = english.Reindex(ctx); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"retry", "认证"} {
		r, e := english.Search(ctx, store.SearchRequest{Query: q})
		if e != nil || r.Total != 1 {
			t.Fatal(q, r, e)
		}
	}
	// Source changes preserve raw content and sidecar freshness across restarts.
	r, err = english.Cat(ctx, store.CatRequest{Path: "/docs/guide.md"})
	if err != nil || r.Content != raw+"更新内容。\n" {
		t.Fatal(r, err)
	}
	if _, err = english.Delete(ctx, store.DeleteRequest{Path: "/docs"}); err != nil {
		t.Fatal(err)
	}
	if _, err = english.Summary(ctx, "/docs"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("orphaned directory summary", err)
	}
	if _, err = english.Put(ctx, store.PutRequest{Path: "/docs/new.md", Content: "new"}); err != nil {
		t.Fatal(err)
	}
	s, err = english.Summary(ctx, "/docs")
	if err != nil || s.Status != "missing" {
		t.Fatal("resurrected old summary", s, err)
	}
}

func TestLegacyIndexMigration(t *testing.T) {
	dsn := os.Getenv("ROLIO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set ROLIO_TEST_POSTGRES_DSN")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("rolio_legacy_%d", time.Now().UnixNano())
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err = pool.Exec(ctx, "create schema "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, e := pool.Exec(ctx, "drop schema "+schema+" cascade"); e != nil {
			t.Error(e)
		}
	}()
	// Construct the actual previous single-tree schema, including its generated index.
	_, err = pool.Exec(ctx, fmt.Sprintf(`
 create table %s.rolio_documents (
 id uuid primary key default gen_random_uuid(), title text not null, content text not null,
 content_hash text not null, revision bigint not null default 1, updated_at timestamptz not null default now(),
 content_search tsvector generated always as (to_tsvector('english',content)) stored);
 create table %s.rolio_paths (
 path text primary key, doc_id uuid not null unique references %s.rolio_documents(id),
 size bigint not null, mtime timestamptz not null default now());
 `, schema, schema, schema))
	if err != nil {
		t.Fatal(err)
	}
	raw := "---\ntitle: legacy\ntags: invalid-legacy-value\n---\n原始内容。"
	var id string
	err = pool.QueryRow(ctx, "insert into "+schema+".rolio_documents(title,content,content_hash,revision) values('legacy',$1,$2,7) returning id", raw, store.HashContent(raw)).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "insert into "+schema+".rolio_paths(path,doc_id,size) values('/legacy.md',$1,$2)", id, len(raw)); err != nil {
		t.Fatal(err)
	}
	adapter, err := postgres.Connect(ctx, postgres.Config{DSN: dsn, Schema: schema, Language: "zh"})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	status, err := adapter.Settings(ctx)
	if err != nil || status.Index.Stale != 1 {
		t.Fatal(status, err)
	}
	doc, err := adapter.Cat(ctx, store.CatRequest{Path: "/legacy.md"})
	if err != nil || doc.Content != raw || doc.MetadataError == "" || doc.Hash != store.HashContent(raw) {
		t.Fatal(doc, err)
	}
	if _, err = adapter.Reindex(ctx); err == nil {
		t.Fatal("reindex accepted malformed metadata")
	}
	var rev int
	if err = pool.QueryRow(ctx, "select revision from "+schema+".rolio_documents where id=$1", id).Scan(&rev); err != nil || rev != 7 {
		t.Fatal("migration modified source revision", rev, err)
	}
	fixed := strings.Replace(raw, "tags: invalid-legacy-value", "tags: [历史]", 1)
	if _, err = adapter.Put(ctx, store.PutRequest{Path: "/legacy.md", Content: fixed, ExpectedHash: doc.Hash}); err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.Reindex(ctx); err != nil {
		t.Fatal(err)
	}
	r, err := adapter.Search(ctx, store.SearchRequest{Query: "原始"})
	if err != nil || r.Total != 1 {
		t.Fatal(r, err)
	}
}
