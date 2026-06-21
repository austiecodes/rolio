package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/austiecodes/rolio/internal/client"
	"github.com/austiecodes/rolio/internal/server"
	"github.com/austiecodes/rolio/internal/store"
	"github.com/austiecodes/rolio/internal/store/postgres"
	"github.com/austiecodes/rolio/internal/summary"
	"github.com/jackc/pgx/v5/pgxpool"
)

// recorder is a model that records its requests. The summary of a document
// contains its text and the abstract of a directory contains its input, thus
// a test can see which version a summary came from, and a change goes up the
// tree.
type recorder struct {
	mu       sync.Mutex
	requests []summary.Request
	fail     map[string]bool
	// block makes the generation of a path wait until the channel is closed.
	// started gets the path when the generation starts.
	block   map[string]chan struct{}
	started chan string
}

func (r *recorder) Generate(ctx context.Context, request summary.Request) (summary.Result, error) {
	r.mu.Lock()
	r.requests = append(r.requests, request)
	fail, block := r.fail[request.Path], r.block[request.Path]
	r.mu.Unlock()
	if block != nil {
		r.started <- request.Path
		select {
		case <-block:
		case <-ctx.Done():
			return summary.Result{}, ctx.Err()
		}
	}
	if fail {
		return summary.Result{}, fmt.Errorf("provider unavailable")
	}
	if request.Directory {
		var bodies []string
		for _, source := range request.Sources {
			bodies = append(bodies, source.Body)
		}
		return summary.Result{Abstract: request.Language + " directory " + request.Path + ": " + strings.Join(bodies, "; "), Overview: "# " + request.Path}, nil
	}
	return summary.Result{Abstract: request.Sources[0].Body, Overview: "overview of " + request.Sources[0].Body}, nil
}

func (r *recorder) set(change func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	change()
}

// paths returns the paths of the requests after the first n, in order.
func (r *recorder) paths(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, request := range r.requests[n:] {
		out = append(out, request.Path)
	}
	return out
}

func (r *recorder) last(p string) summary.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.requests) - 1; i >= 0; i-- {
		if r.requests[i].Path == p {
			return r.requests[i]
		}
	}
	return summary.Request{}
}

type summaryTest struct {
	*testing.T
	ctx    context.Context
	dsn    string
	schema string
	stops  []func()
}

func newSummaryTest(t *testing.T) *summaryTest {
	dsn := os.Getenv("ROLIO_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set ROLIO_TEST_POSTGRES_DSN")
	}
	st := &summaryTest{T: t, ctx: context.Background(), dsn: dsn, schema: fmt.Sprintf("rolio_summary_%d", time.Now().UnixNano())}
	t.Cleanup(func() {
		pool, err := pgxpool.New(st.ctx, dsn)
		if err != nil {
			t.Error(err)
			return
		}
		defer pool.Close()
		if _, err = pool.Exec(st.ctx, "drop schema if exists "+st.schema+" cascade"); err != nil {
			t.Error(err)
		}
	})
	return st
}

// open connects an adapter. With a model, it also starts the summary workers.
// The cleanup stops the workers before it closes the adapter.
func (st *summaryTest) open(language string, model summary.Generator, delay time.Duration) *postgres.DocAdapter {
	st.Helper()
	adapter, err := postgres.Connect(st.ctx, postgres.Config{DSN: st.dsn, Schema: st.schema, Language: language, Generator: model, SummaryDelay: delay, SummaryRetry: 10 * time.Millisecond})
	if err != nil {
		st.Fatal(err)
	}
	ctx, cancel := context.WithCancel(st.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if model == nil {
			return
		}
		if err := adapter.RunSummaries(ctx, 3); err != nil {
			st.Error(err)
		}
	}()
	// Refresh is an error until the workers run.
	for deadline := time.Now().Add(10 * time.Second); model != nil; time.Sleep(10 * time.Millisecond) {
		if _, err := adapter.Refresh(st.ctx, "/no-such-path"); !errors.Is(err, store.ErrNotSupported) {
			break
		}
		if time.Now().After(deadline) {
			st.Fatal("the summary workers did not start")
		}
	}
	st.stops = append(st.stops, sync.OnceFunc(func() {
		cancel()
		<-done
		adapter.Close()
	}))
	st.Cleanup(st.stop)
	return adapter
}

// stop stops the workers and closes the adapters that open made.
func (st *summaryTest) stop() {
	for _, stop := range st.stops {
		stop()
	}
}

func (st *summaryTest) put(adapter *postgres.DocAdapter, p, content string) {
	st.Helper()
	if _, err := adapter.Put(st.ctx, store.PutRequest{Path: p, Content: content}); err != nil {
		st.Fatal(err)
	}
}

// settle waits until the summary of the root is ready. The workers go from
// the bottom to the top, thus all other summaries are complete at that time.
func (st *summaryTest) settle(adapter *postgres.DocAdapter) {
	st.Helper()
	st.wait(adapter, "/", "ready")
}

func (st *summaryTest) wait(adapter *postgres.DocAdapter, p, status string) *store.Summary {
	st.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s, err := adapter.Summary(st.ctx, p)
		if err != nil {
			st.Fatal(p, err)
		}
		if s.Status == status {
			return s
		}
		if time.Now().After(deadline) {
			st.Fatalf("%s: status %s, want %s (%s)", p, s.Status, status, s.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func sorted(paths []string) []string {
	out := slices.Clone(paths)
	slices.Sort(out)
	return out
}

func TestSummaryQueue(t *testing.T) {
	st := newSummaryTest(t)
	model := &recorder{}
	adapter := st.open("en", model, 0)
	st.put(adapter, "/docs/a.md", "---\ntitle: A\n---\n\nalpha one")
	st.put(adapter, "/docs/sub/b.md", "beta one")
	st.put(adapter, "/notes/c.md", "gamma one")
	st.settle(adapter)

	// Each document and each directory gets one summary.
	first := model.paths(0)
	want := []string{"/", "/docs", "/docs/a.md", "/docs/sub", "/docs/sub/b.md", "/notes", "/notes/c.md"}
	if !slices.Equal(sorted(first), want) {
		t.Fatalf("requests = %v, want %v", first, want)
	}
	// A directory comes after its children.
	for _, pair := range [][2]string{{"/docs/sub/b.md", "/docs/sub"}, {"/docs/sub", "/docs"}, {"/docs/a.md", "/docs"}, {"/docs", "/"}, {"/notes", "/"}} {
		if slices.Index(first, pair[0]) > slices.Index(first, pair[1]) {
			t.Errorf("%s came after %s: %v", pair[0], pair[1], first)
		}
	}
	// A document gives its text without the frontmatter. A directory gets
	// the overview of a document and the abstract of a subdirectory.
	if got := model.last("/docs/a.md"); got.Directory || len(got.Sources) != 1 || got.Sources[0].Body != "alpha one" {
		t.Errorf("document request = %+v", got)
	}
	docs := model.last("/docs")
	wantSources := []summary.Source{{Path: "/docs/a.md", Body: "overview of alpha one"}, {Path: "/docs/sub/", Body: "en directory /docs/sub: overview of beta one"}}
	if !docs.Directory || !slices.Equal(docs.Sources, wantSources) {
		t.Errorf("directory request = %+v, want sources %+v", docs, wantSources)
	}
	if s, err := adapter.Summary(st.ctx, "/docs/a.md"); err != nil || s.Status != "ready" || s.Abstract != "alpha one" {
		t.Errorf("document summary = %+v, %v", s, err)
	}
	sidecar, err := adapter.Cat(st.ctx, store.CatRequest{Path: "/docs/.abstract.md"})
	if err != nil || !strings.HasPrefix(sidecar.Body, "en directory /docs: ") || !strings.Contains(sidecar.Content, "source_hash:") {
		t.Errorf("sidecar = %+v, %v", sidecar, err)
	}

	// The HTTP interface gives the same summaries.
	srv := httptest.NewServer(server.NewHandler(adapter))
	defer srv.Close()
	cli := client.New(srv.URL)
	if _, err := cli.Cat(st.ctx, store.CatRequest{Path: "/docs/.abstract.md", IfNoneMatch: sidecar.Hash}); !errors.Is(err, store.ErrNotModified) {
		t.Errorf("sidecar ETag: %v", err)
	}
	if s, err := cli.Summary(st.ctx, "/docs/a.md"); err != nil || s.Status != "ready" || s.Abstract != "alpha one" {
		t.Errorf("HTTP summary = %+v, %v", s, err)
	}
	n := len(model.paths(0))
	if s, err := cli.Refresh(st.ctx, "/docs/sub"); err != nil || s.Status != "ready" {
		t.Errorf("HTTP refresh = %+v, %v", s, err)
	}
	if got := model.paths(n); !slices.Equal(got, []string{"/docs/sub"}) {
		t.Errorf("requests of HTTP refresh = %v", got)
	}

	// A change makes new summaries only for the document and its ancestors.
	n = len(model.paths(0))
	st.put(adapter, "/docs/sub/b.md", "beta two")
	st.settle(adapter)
	if got, want := sorted(model.paths(n)), []string{"/", "/docs", "/docs/sub", "/docs/sub/b.md"}; !slices.Equal(got, want) {
		t.Errorf("requests after a change = %v, want %v", got, want)
	}
	if s, _ := adapter.Summary(st.ctx, "/docs/sub/b.md"); s.Abstract != "beta two" {
		t.Errorf("summary of the old text: %+v", s)
	}

	// A write of the same text changes nothing.
	n = len(model.paths(0))
	st.put(adapter, "/docs/sub/b.md", "beta two")
	st.settle(adapter)
	if got := model.paths(n); len(got) != 0 {
		t.Errorf("requests after an identical write = %v", got)
	}

	// A change of the frontmatter only does not change the model input.
	st.put(adapter, "/docs/a.md", "---\ntitle: New title\n---\n\nalpha one")
	st.settle(adapter)
	if got := model.paths(n); len(got) != 0 {
		t.Errorf("requests after a change of frontmatter = %v", got)
	}

	// The summaries of a deleted document and of an empty directory go away.
	if _, err := adapter.Delete(st.ctx, store.DeleteRequest{Path: "/notes/c.md"}); err != nil {
		t.Fatal(err)
	}
	st.settle(adapter)
	if _, err := adapter.Summary(st.ctx, "/notes"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("summary of an empty directory: %v", err)
	}
	if got := model.paths(n); !slices.Equal(got, []string{"/"}) {
		t.Errorf("requests after a delete = %v, want only the root", got)
	}
	if root := model.last("/"); len(root.Sources) != 1 || root.Sources[0].Path != "/docs/" {
		t.Errorf("root request after a delete = %+v", root)
	}
	var rows int
	pool, err := pgxpool.New(st.ctx, st.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := pool.QueryRow(st.ctx, "select count(*) from "+st.schema+".rolio_summaries where path like '/notes%'").Scan(&rows); err != nil || rows != 0 {
		t.Errorf("%d rows for the deleted directory, %v", rows, err)
	}
}

func (r *recorder) count(p string) int {
	n := 0
	for _, got := range r.paths(0) {
		if got == p {
			n++
		}
	}
	return n
}

func TestSummaryFailureAndRefresh(t *testing.T) {
	st := newSummaryTest(t)
	model := &recorder{fail: map[string]bool{}}
	adapter := st.open("en", model, 0)
	st.put(adapter, "/a/x/y.md", "one")
	st.settle(adapter)

	// A generation that fails is tried three times. Then the old summary
	// stays, and the directories do not wait for the document.
	model.set(func() { model.fail["/a/x/y.md"] = true })
	n := model.count("/a/x/y.md")
	st.put(adapter, "/a/x/y.md", "two")
	s := st.wait(adapter, "/a/x/y.md", "failed")
	if s.Error == "" || s.Abstract != "one" || model.count("/a/x/y.md") != n+3 {
		t.Errorf("failed summary = %+v after %d attempts", s, model.count("/a/x/y.md")-n)
	}
	st.settle(adapter)
	if _, err := adapter.Refresh(st.ctx, "/a/x/y.md"); err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Errorf("refresh with a failing model: %v", err)
	}

	// Refresh of a directory does the failed document below it, and each
	// directory between them gets the new summary of its child.
	model.set(func() { model.fail["/a/x/y.md"] = false })
	n = len(model.paths(0))
	if s, err := adapter.Refresh(st.ctx, "/a"); err != nil || s.Status != "ready" {
		t.Fatalf("refresh = %+v, %v", s, err)
	}
	// The new summary of /a then goes up to the root.
	st.settle(adapter)
	if got, want := model.paths(n), []string{"/a/x/y.md", "/a/x", "/a", "/"}; !slices.Equal(got, want) {
		t.Errorf("requests of refresh = %v, want %v", got, want)
	}
	if s, _ := adapter.Summary(st.ctx, "/a/x/y.md"); s.Status != "ready" || s.Abstract != "two" {
		t.Errorf("summary after refresh = %+v", s)
	}

	// Refresh calls the model also when the input did not change. The
	// summary is the same, thus the directories above stay as they are.
	st.settle(adapter)
	n = len(model.paths(0))
	if _, err := adapter.Refresh(st.ctx, "/a/x/y.md"); err != nil {
		t.Fatal(err)
	}
	st.settle(adapter)
	if got := model.paths(n); !slices.Equal(got, []string{"/a/x/y.md"}) {
		t.Errorf("requests of a forced refresh = %v", got)
	}
	if _, err := adapter.Refresh(st.ctx, "/missing"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("refresh of a missing path: %v", err)
	}
}

// A document that gets a summary after a failure changes the input of its
// directory, thus the directory gets a new summary without a write.
func TestSummaryAfterFailure(t *testing.T) {
	st := newSummaryTest(t)
	model := &recorder{fail: map[string]bool{"/a/c.md": true}}
	adapter := st.open("en", model, 0)
	st.put(adapter, "/a/b.md", "one")
	st.put(adapter, "/a/c.md", "two")
	st.wait(adapter, "/a/c.md", "failed")
	st.settle(adapter)
	// A child without a summary gives no input.
	if got := model.last("/a"); len(got.Sources) != 1 || got.Sources[0].Path != "/a/b.md" {
		t.Errorf("directory request with a failed child = %+v", got)
	}
	// A directory below which all generations failed has no summary, and
	// Refresh says so. Its last attempt leaves the directory as failed too.
	st.put(adapter, "/f/only.md", "three")
	model.set(func() { model.fail["/f/only.md"], model.fail["/f"] = true, true })
	st.wait(adapter, "/f/only.md", "failed")
	st.settle(adapter)
	if s, err := adapter.Refresh(st.ctx, "/f"); !errors.Is(err, store.ErrContentNotReady) {
		t.Errorf("refresh of a directory without summaries = %+v, %v", s, err)
	}
	// The directory /a fails three times. Then its child gets a summary: the
	// directory goes into the queue with all its attempts.
	model.set(func() { model.fail = map[string]bool{"/a/c.md": true, "/a": true} })
	if _, err := adapter.Refresh(st.ctx, "/a"); err == nil {
		t.Fatal("refresh of a failing directory gave no error")
	}
	n := model.count("/a")
	model.set(func() { model.fail = map[string]bool{"/a": true} })
	if _, err := adapter.Refresh(st.ctx, "/a/c.md"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); model.count("/a") < n+3; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%d attempts for the directory after its child changed, want 3", model.count("/a")-n)
		}
	}
	st.wait(adapter, "/a", "failed")
	st.settle(adapter)
	if got := model.count("/a") - n; got != 3 {
		t.Errorf("%d attempts for the directory after its child changed, want 3", got)
	}
	model.set(func() { model.fail = nil })
	if _, err := adapter.Refresh(st.ctx, "/a"); err != nil {
		t.Fatal(err)
	}
	if got := model.last("/a"); len(got.Sources) != 2 {
		t.Errorf("directory request = %+v", got)
	}
	st.settle(adapter)
}

// A summary of old text must not replace the state of a newer write.
func TestSummarySuperseded(t *testing.T) {
	st := newSummaryTest(t)
	release := make(chan struct{})
	model := &recorder{block: map[string]chan struct{}{"/docs/a.md": release}, started: make(chan string, 8)}
	adapter := st.open("en", model, 0)
	st.put(adapter, "/docs/a.md", "alpha one")
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("generation did not start")
	}
	if s := st.wait(adapter, "/docs/a.md", "generating"); s.Abstract != "" {
		t.Errorf("summary during the first generation = %+v", s)
	}
	st.put(adapter, "/docs/a.md", "alpha two")
	model.set(func() { model.block = nil })
	close(release)
	st.settle(adapter)
	if s, _ := adapter.Summary(st.ctx, "/docs/a.md"); s.Abstract != "alpha two" {
		t.Errorf("summary = %+v, want that of the second write", s)
	}
	if got := model.last("/docs"); len(got.Sources) != 1 {
		t.Errorf("directory request = %+v", got)
	}
}

// The delay collects many writes to one directory into one job.
func TestSummaryDelay(t *testing.T) {
	st := newSummaryTest(t)
	model := &recorder{}
	adapter := st.open("en", model, 1500*time.Millisecond)
	for i := range 5 {
		st.put(adapter, fmt.Sprintf("/docs/%d.md", i), fmt.Sprintf("text %d", i))
	}
	st.settle(adapter)
	count := 0
	for _, p := range model.paths(0) {
		if p == "/docs" {
			count++
		}
	}
	if count != 1 || len(model.paths(0)) != 7 {
		t.Errorf("requests = %v, want one for each document and each directory", model.paths(0))
	}
}

// Documents that were written without a model get summaries when a model is
// configured. A change of language makes all summaries again.
func TestSummaryBackfillAndLanguage(t *testing.T) {
	st := newSummaryTest(t)
	plain := st.open("zh", nil, 0)
	st.put(plain, "/docs/a.md", "alpha one")
	if s, err := plain.Summary(st.ctx, "/docs"); err != nil || s.Status != "missing" {
		t.Fatalf("summary without a model = %+v, %v", s, err)
	}
	if _, err := plain.Refresh(st.ctx, "/docs"); !errors.Is(err, store.ErrNotSupported) {
		t.Fatalf("refresh without a model: %v", err)
	}

	model := &recorder{}
	chinese := st.open("zh", model, 0)
	st.settle(chinese)
	if got, want := sorted(model.paths(0)), []string{"/", "/docs", "/docs/a.md"}; !slices.Equal(got, want) {
		t.Errorf("requests of the first run = %v, want %v", got, want)
	}
	if s, _ := chinese.Summary(st.ctx, "/docs"); s.Language != "zh" || !strings.HasPrefix(s.Abstract, "zh directory /docs: ") {
		t.Errorf("summary = %+v", s)
	}

	// Two servers with different languages must not use one database.
	st.stop()
	n := len(model.paths(0))
	english := st.open("en", model, 0)
	deadline := time.Now().Add(10 * time.Second)
	for s := st.wait(english, "/", "ready"); s.Language != "en"; s = st.wait(english, "/", "ready") {
		if time.Now().After(deadline) {
			t.Fatalf("the root summary is not in the new language: %+v", s)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got, want := sorted(model.paths(n)), []string{"/", "/docs", "/docs/a.md"}; !slices.Equal(got, want) {
		t.Errorf("requests after a change of language = %v, want %v", got, want)
	}
	if s, _ := english.Summary(st.ctx, "/docs"); s.Language != "en" || !strings.HasPrefix(s.Abstract, "en directory /docs: ") {
		t.Errorf("summary = %+v", s)
	}

	// A server without a model makes the summaries stale. A server with a
	// model then makes them again, and also the summary of a new document.
	st.stop()
	n = len(model.paths(0))
	plain = st.open("en", nil, 0)
	st.put(plain, "/docs/a.md", "alpha two")
	st.put(plain, "/docs/new.md", "new")
	for _, p := range []string{"/docs/a.md", "/docs", "/"} {
		if s, err := plain.Summary(st.ctx, p); err != nil || s.Status != "stale" || s.Abstract == "" {
			t.Errorf("%s after a write without a model = %+v, %v", p, s, err)
		}
	}
	st.stop()
	english = st.open("en", model, 0)
	st.settle(english)
	if got, want := sorted(model.paths(n)), []string{"/", "/docs", "/docs/a.md", "/docs/new.md"}; !slices.Equal(got, want) {
		t.Errorf("requests after writes without a model = %v, want %v", got, want)
	}
}

// Two servers on one database do each job one time.
func TestSummaryTwoServers(t *testing.T) {
	st := newSummaryTest(t)
	model := &recorder{}
	one := st.open("en", model, 0)
	two := st.open("en", model, 0)
	for i := range 12 {
		adapter := one
		if i%2 == 1 {
			adapter = two
		}
		st.put(adapter, fmt.Sprintf("/d%d/doc.md", i%4), fmt.Sprintf("text %d", i))
		st.put(adapter, fmt.Sprintf("/d%d/%d.md", i%4, i), fmt.Sprintf("text %d", i))
	}
	st.settle(one)
	seen := map[string]bool{}
	for _, p := range model.paths(0) {
		// A directory can get a second summary when a write came after its
		// first one. A document was written one time or was complete before
		// its next write.
		if seen[p] && strings.HasSuffix(p, ".md") && !strings.HasSuffix(p, "/doc.md") {
			t.Errorf("two requests for %s: %v", p, model.paths(0))
		}
		seen[p] = true
	}
	if len(seen) != 12+4+4+1 {
		t.Errorf("%d paths got a summary, want 21: %v", len(seen), model.paths(0))
	}
}

// A server that stops gives its job back to the queue.
func TestSummaryShutdown(t *testing.T) {
	st := newSummaryTest(t)
	model := &recorder{block: map[string]chan struct{}{"/docs/a.md": make(chan struct{})}, started: make(chan string, 8)}
	adapter := st.open("en", model, 0)
	st.put(adapter, "/docs/a.md", "alpha one")
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("generation did not start")
	}
	st.stop()
	plain := st.open("en", nil, 0)
	if s, err := plain.Summary(st.ctx, "/docs/a.md"); err != nil || s.Status != "stale" {
		t.Fatalf("summary after the server stopped = %+v, %v", s, err)
	}
	st.stop()
	model.set(func() { model.block = nil })
	again := st.open("en", model, 0)
	st.settle(again)
	if s, _ := again.Summary(st.ctx, "/docs/a.md"); s.Abstract != "alpha one" {
		t.Errorf("summary = %+v", s)
	}
}

// A database of the version before the queue has summary rows without the
// queue columns. Its directory summaries stay, and the documents get theirs.
func TestSummaryMigration(t *testing.T) {
	st := newSummaryTest(t)
	plain := st.open("en", nil, 0)
	st.put(plain, "/docs/sub/a.md", "alpha one")
	st.stop()
	pool, err := pgxpool.New(st.ctx, st.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	table := st.schema + ".rolio_summaries"
	for _, statement := range []string{
		"drop table " + table,
		"create table " + table + ` (path text primary key, language text not null, source_hash text not null default '',
 abstract text not null default '', overview text not null default '', status text not null,
 error text not null default '', token text not null default '', updated_at timestamptz not null default now())`,
		"insert into " + table + "(path,language,source_hash,abstract,overview,status) values('/docs/sub','en','old','old abstract','old overview','ready'),('/','en','old','old root','old root','ready')",
	} {
		if _, err := pool.Exec(st.ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	model := &recorder{}
	adapter := st.open("en", model, 0)
	var parent string
	if err := pool.QueryRow(st.ctx, "select parent from "+table+" where path='/docs/sub'").Scan(&parent); err != nil || parent != "/docs" {
		t.Errorf("parent of the old row = %q, %v", parent, err)
	}
	st.settle(adapter)
	if got, want := sorted(model.paths(0)), []string{"/", "/docs", "/docs/sub", "/docs/sub/a.md"}; !slices.Equal(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
	if s, _ := adapter.Summary(st.ctx, "/docs/sub"); !strings.HasPrefix(s.Abstract, "en directory /docs/sub: ") {
		t.Errorf("summary = %+v", s)
	}
}
