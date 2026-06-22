package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/austiecodes/rolio/internal/client"
	"github.com/austiecodes/rolio/internal/server"
	"github.com/austiecodes/rolio/internal/store"
	"github.com/austiecodes/rolio/internal/store/postgres"
	"github.com/austiecodes/rolio/internal/summary"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fixedModel is a model that gives each path a fixed summary. A path that is
// not in the map gets a summary without the words of the tests.
type fixedModel map[string]string

func (m fixedModel) Generate(_ context.Context, r summary.Request) (summary.Result, error) {
	text := m[r.Path]
	if text == "" {
		text = "zzz"
	}
	return summary.Result{Abstract: text, Overview: "overview: " + text}, nil
}

func (st *summaryTest) search(adapter *postgres.DocAdapter, req store.SearchRequest) *store.SearchResponse {
	st.Helper()
	r, err := adapter.Search(st.ctx, req)
	if err != nil {
		st.Fatalf("search %+v: %v", req, err)
	}
	return r
}

func resultPaths(r *store.SearchResponse) []string {
	var paths []string
	for _, result := range r.Results {
		paths = append(paths, result.Path)
	}
	return paths
}

// Without a model there are no summary rows. Search uses only the text.
func TestSearchRanking(t *testing.T) {
	st := newSummaryTest(t)
	adapter := st.open("en", nil, 0)
	st.put(adapter, "/a/all.md", "alpha beta gamma")
	st.put(adapter, "/a/two.md", "beta alpha")
	// Many occurrences of one term do not go before more terms.
	st.put(adapter, "/a/one.md", strings.Repeat("alpha ", 20))
	st.put(adapter, "/a/none.md", "delta")
	st.put(adapter, "/ab/one.md", "alpha")

	r := st.search(adapter, store.SearchRequest{Query: "alpha beta gamma"})
	want := []string{"/a/all.md", "/a/two.md", "/a/one.md", "/ab/one.md"}
	if !slices.Equal(resultPaths(r), want) || r.Total != 4 || len(r.Directories) != 0 {
		t.Fatalf("results = %v total %d directories %v, want %v", resultPaths(r), r.Total, r.Directories, want)
	}
	for _, result := range r.Results {
		if result.Abstract != "" || len(result.Passages) != 1 || result.Snippet != result.Passages[0] {
			t.Errorf("%s: abstract %q, passages %q, snippet %q", result.Path, result.Abstract, result.Passages, result.Snippet)
		}
	}
	if r.Results[0].Rank <= 0 {
		t.Errorf("rank = %v", r.Results[0].Rank)
	}

	// The scope is a path and not a prefix of a name.
	r = st.search(adapter, store.SearchRequest{Query: "alpha beta gamma", Path: "/a"})
	if !slices.Equal(resultPaths(r), want[:3]) || r.Total != 3 {
		t.Errorf("scope /a = %v total %d", resultPaths(r), r.Total)
	}
	r = st.search(adapter, store.SearchRequest{Query: "alpha", Path: "/a/two.md"})
	if !slices.Equal(resultPaths(r), []string{"/a/two.md"}) || r.Total != 1 {
		t.Errorf("scope of one document = %v total %d", resultPaths(r), r.Total)
	}
	r = st.search(adapter, store.SearchRequest{Query: "alpha beta gamma", Limit: 2, Offset: 1})
	if !slices.Equal(resultPaths(r), want[1:3]) || r.Total != 4 {
		t.Errorf("page = %v total %d", resultPaths(r), r.Total)
	}
	// A page after the last result has no document. The total stays.
	r = st.search(adapter, store.SearchRequest{Query: "alpha beta gamma", Offset: 9})
	if len(r.Results) != 0 || r.Total != 4 {
		t.Errorf("page after the end = %v total %d", resultPaths(r), r.Total)
	}
	r = st.search(adapter, store.SearchRequest{Query: "epsilon"})
	if len(r.Results) != 0 || r.Total != 0 {
		t.Errorf("no candidate = %v total %d", resultPaths(r), r.Total)
	}

	for i := range 105 {
		st.put(adapter, fmt.Sprintf("/many/%03d.md", i), "omega")
	}
	r = st.search(adapter, store.SearchRequest{Query: "omega"})
	if len(r.Results) != 10 || r.Total != 105 {
		t.Errorf("default limit: %d results, total %d", len(r.Results), r.Total)
	}
	r = st.search(adapter, store.SearchRequest{Query: "omega", Limit: 500})
	if len(r.Results) != 100 || r.Total != 105 {
		t.Errorf("maximum limit: %d results, total %d", len(r.Results), r.Total)
	}

	// Two words with the same lexeme are one term. Without that, the first
	// document has two terms and comes first.
	st.put(adapter, "/lex/a.md", "one group")
	st.put(adapter, "/lex/b.md", "zebra zebra zebra")
	r = st.search(adapter, store.SearchRequest{Query: "group groups zebra", Path: "/lex"})
	if !slices.Equal(resultPaths(r), []string{"/lex/b.md", "/lex/a.md"}) {
		t.Errorf("lexemes: %v", resultPaths(r))
	}

	// The text of a query is data. Characters that are operators of a
	// tsquery or of SQL have no effect.
	for _, query := range []string{"alpha'); drop table rolio_paths; --", "alpha & | ! ( ) <-> :* \\ '", "alpha:*", "!alpha", "'alpha' \"beta\"", "alpha\x00beta"} {
		r = st.search(adapter, store.SearchRequest{Query: query, Path: "/a"})
		if r.Total != 3 {
			t.Errorf("query %q: total %d, want 3", query, r.Total)
		}
	}
	// A query with only stop words has no result.
	if r = st.search(adapter, store.SearchRequest{Query: "the and of"}); r.Total != 0 {
		t.Errorf("stop words: total %d", r.Total)
	}
	for _, query := range []string{"", "  ", "!!! ---"} {
		if _, err := adapter.Search(st.ctx, store.SearchRequest{Query: query}); !errors.Is(err, store.ErrEmptyQuery) {
			t.Errorf("query %q: %v", query, err)
		}
	}
}

func TestSearchPassages(t *testing.T) {
	st := newSummaryTest(t)
	adapter := st.open("en", nil, 0)
	long := strings.Repeat("filler words go here. ", 60) + "The zebra crossed the road. " + strings.Repeat("more filler. ", 60)
	st.put(adapter, "/docs/guide.md", "---\ntitle: Walrus handbook\ntags: [walrus]\n---\n\n# Heading\n\nThe support groups meet on Monday.\n\n"+long+"\n\nNothing here.\n")
	st.put(adapter, "/docs/accents.md", strings.Repeat("é", 700)+" zebra "+strings.Repeat("ü", 700)+"\n")
	st.put(adapter, "/docs/many.md", "cat one\n\ncat dog two\n\nbird cat dog three\n\ncat four\n\ncat five\n")

	// The stemmer of the index is also the stemmer of the passages.
	r := st.search(adapter, store.SearchRequest{Query: "group"})
	if len(r.Results) != 1 || !slices.Equal(r.Results[0].Passages, []string{"The support groups meet on Monday."}) || r.Results[0].MoreLines != 0 || r.Results[0].MoreLinesMin {
		t.Fatalf("stemming: %+v", r.Results)
	}
	// The stemmer changes the third character of some words. Each form finds
	// the lines with the other forms.
	st.put(adapter, "/stems/forms.md", "I will try this.\n\nWe are using postgres.\n\nNothing here.\n")
	for query, want := range map[string]string{"tried": "I will try this.", "tries": "I will try this.", "use": "We are using postgres.", "used": "We are using postgres."} {
		r = st.search(adapter, store.SearchRequest{Query: query, Path: "/stems"})
		if r.Total != 1 || len(r.Results) != 1 || !slices.Equal(r.Results[0].Passages, []string{want}) {
			t.Errorf("forms of a word: query %q: %+v", query, r)
		}
	}
	st.put(adapter, "/stems/past.md", "She tried it and used it.\n")
	for _, query := range []string{"try", "using"} {
		r = st.search(adapter, store.SearchRequest{Query: query, Path: "/stems/past.md"})
		if r.Total != 1 || len(r.Results) != 1 || !slices.Equal(r.Results[0].Passages, []string{"She tried it and used it."}) {
			t.Errorf("forms of a word: query %q: %+v", query, r)
		}
	}
	// The frontmatter is in the index and not in the passages.
	r = st.search(adapter, store.SearchRequest{Query: "walrus"})
	if len(r.Results) != 1 || len(r.Results[0].Passages) != 0 || r.Results[0].Snippet != "" {
		t.Fatalf("frontmatter: %+v", r.Results)
	}
	// A long line gives the part that agrees with the query.
	r = st.search(adapter, store.SearchRequest{Query: "zebra"})
	if len(r.Results) != 2 {
		t.Fatalf("long lines: %+v", r.Results)
	}
	for _, result := range r.Results {
		if len(result.Passages) != 1 || !strings.Contains(result.Passages[0], "zebra") || !utf8.ValidString(result.Passages[0]) || utf8.RuneCountInString(result.Passages[0]) > 480+len("...  ...") ||
			!strings.HasPrefix(result.Passages[0], "... ") || !strings.HasSuffix(result.Passages[0], " ...") {
			t.Errorf("%s: passages %q", result.Path, result.Passages)
		}
	}
	// A later part of a long line starts with the label of the line.
	st.put(adapter, "/docs/talk.md", "# Talk\n\n[Caroline] (D1:3): "+strings.Repeat("We talked about many things. ", 40)+"Then the okapi came. "+strings.Repeat("And more. ", 20)+"\n")
	r = st.search(adapter, store.SearchRequest{Query: "okapis"})
	if len(r.Results) != 1 || len(r.Results[0].Passages) != 1 {
		t.Fatalf("label: %+v", r.Results)
	}
	if p := r.Results[0].Passages[0]; !strings.HasPrefix(p, "[Caroline] (D1:3): ... ") || !strings.Contains(p, "Then the okapi came.") || utf8.RuneCountInString(p) > 480+len("[Caroline] (D1:3): ...  ...") {
		t.Errorf("label: passage %q", p)
	}
	// Only a part of the lines of a very long document is ranked.
	st.put(adapter, "/docs/lines.md", strings.Repeat("a cat\n", 2500))
	r = st.search(adapter, store.SearchRequest{Query: "cats", Path: "/docs/lines.md"})
	// The other lines were not examined, thus the number is a lower limit.
	if len(r.Results) != 1 || !slices.Equal(r.Results[0].Passages, []string{"a cat", "a cat", "a cat"}) || r.Results[0].MoreLines != 1997 || !r.Results[0].MoreLinesMin {
		t.Fatalf("many lines: %+v", r.Results)
	}
	// A search ranks a limited number of lines. The last document of this
	// page is after that limit, thus it has no passage.
	for i := range 11 {
		st.put(adapter, fmt.Sprintf("/budget/%02d.md", i), strings.Repeat("a cat\n", 2000))
	}
	r = st.search(adapter, store.SearchRequest{Query: "cat", Path: "/budget", Limit: 11})
	if len(r.Results) != 11 || r.Results[10].Path != "/budget/10.md" {
		t.Fatalf("line limit: %v", resultPaths(r))
	}
	for i, result := range r.Results {
		// All lines of the first documents were examined. No line of the
		// last document was examined.
		if want := min(3, 3*(10-i)); len(result.Passages) != want || result.MoreLines != 2000*want/3-want || result.MoreLinesMin != (i == 10) {
			t.Errorf("line limit: %s has %d passages, %d more lines, lower limit %v", result.Path, len(result.Passages), result.MoreLines, result.MoreLinesMin)
		}
	}
	// A document gives its three best lines, in the order of the document.
	r = st.search(adapter, store.SearchRequest{Query: "cat dog bird", Path: "/docs/many.md"})
	// The two other lines also have a word of the query.
	if len(r.Results) != 1 || !slices.Equal(r.Results[0].Passages, []string{"cat one", "cat dog two", "bird cat dog three"}) || r.Results[0].Snippet != "cat one\ncat dog two\nbird cat dog three" || r.Results[0].MoreLines != 2 || r.Results[0].MoreLinesMin {
		t.Fatalf("best lines: %+v", r.Results)
	}
}

func TestSearchChinese(t *testing.T) {
	st := newSummaryTest(t)
	adapter := st.open("zh", nil, 0)
	st.put(adapter, "/zh/retry.md", "# 重试\n\n支付失败后重试三次。\n\n其他内容。\n")
	st.put(adapter, "/zh/proverb.md", "失败是成功之母。\n")
	// This document has characters of the query and none of its bigrams.
	st.put(adapter, "/zh/method.md", "付款方式与支出。\n")
	st.put(adapter, "/zh/auth.md", "身份认证使用令牌。API token.\n")

	r := st.search(adapter, store.SearchRequest{Query: "支付失败"})
	if !slices.Equal(resultPaths(r), []string{"/zh/retry.md", "/zh/proverb.md"}) || r.Total != 2 {
		t.Fatalf("bigrams: %v total %d", resultPaths(r), r.Total)
	}
	if !slices.Equal(r.Results[0].Passages, []string{"支付失败后重试三次。"}) || !slices.Equal(r.Results[1].Passages, []string{"失败是成功之母。"}) {
		t.Errorf("passages: %q %q", r.Results[0].Passages, r.Results[1].Passages)
	}
	// One character is a term when it is alone.
	r = st.search(adapter, store.SearchRequest{Query: "付"})
	if !slices.Equal(resultPaths(r), []string{"/zh/method.md", "/zh/retry.md"}) {
		t.Errorf("one character: %v", resultPaths(r))
	}
	r = st.search(adapter, store.SearchRequest{Query: "认证 API 重试"})
	if !slices.Equal(resultPaths(r), []string{"/zh/auth.md", "/zh/retry.md"}) || !slices.Equal(r.Results[0].Passages, []string{"身份认证使用令牌。API token."}) {
		t.Errorf("mixed text: %+v", r.Results)
	}
}

func TestSearchSummaries(t *testing.T) {
	st := newSummaryTest(t)
	model := fixedModel{
		"/team/notes.md": "kubernetes rollout procedure",
		"/zeta":          "mineral storage",
		"/zeta/deep":     "mineral samples and more mineral",
		"/yard":          "mineral yard",
		"/xeno":          "mineral",
		"/":              "mineral root",
	}
	adapter := st.open("en", model, 0)
	st.put(adapter, "/team/notes.md", "The deploy needs a token.")
	st.put(adapter, "/team/lunch.md", "The menu of today.")
	st.put(adapter, "/alpha/b.md", "quartz crystal")
	st.put(adapter, "/zeta/a.md", "quartz crystal")
	st.put(adapter, "/zeta/deep/c.md", "feldspar")
	st.put(adapter, "/yard/d.md", "feldspar")
	st.put(adapter, "/xeno/e.md", "feldspar")
	st.settle(adapter)

	// The text of the document does not have the word. Its summary has it.
	r := st.search(adapter, store.SearchRequest{Query: "kubernetes"})
	if len(r.Results) != 1 || r.Total != 1 || r.Results[0].Path != "/team/notes.md" || r.Results[0].Abstract != "kubernetes rollout procedure" || len(r.Results[0].Passages) != 0 || r.Results[0].Rank <= 0 {
		t.Fatalf("summary only: %+v", r)
	}
	// A result has the abstract of its document also when the text agrees.
	r = st.search(adapter, store.SearchRequest{Query: "deploy"})
	if len(r.Results) != 1 || r.Results[0].Abstract != "kubernetes rollout procedure" || !slices.Equal(r.Results[0].Passages, []string{"The deploy needs a token."}) {
		t.Fatalf("abstract and passage: %+v", r)
	}

	// The two documents have the same text. The summary of /zeta agrees with
	// the query, thus its document comes first. Without that, the order is
	// the order of the paths.
	r = st.search(adapter, store.SearchRequest{Query: "quartz"})
	if !slices.Equal(resultPaths(r), []string{"/alpha/b.md", "/zeta/a.md"}) {
		t.Fatalf("without a directory: %v", resultPaths(r))
	}
	r = st.search(adapter, store.SearchRequest{Query: "quartz mineral"})
	if !slices.Equal(resultPaths(r), []string{"/zeta/a.md", "/alpha/b.md"}) || r.Total != 2 || r.Results[0].Rank <= r.Results[1].Rank {
		t.Fatalf("directory lift: %+v", r)
	}
	// The best three directories, without the root. A directory is not a
	// candidate, and a directory does not make its documents candidates.
	dirs := []store.SearchDirectory{{Path: "/zeta/deep", Abstract: "mineral samples and more mineral"}, {Path: "/xeno", Abstract: "mineral"}, {Path: "/yard", Abstract: "mineral yard"}}
	if !slices.Equal(r.Directories, dirs) {
		t.Errorf("directories = %+v, want %+v", r.Directories, dirs)
	}
	// The HTTP API gives the same result.
	srv := httptest.NewServer(server.NewHandler(adapter))
	defer srv.Close()
	if remote, err := client.New(srv.URL).Search(st.ctx, store.SearchRequest{Query: "quartz mineral"}); err != nil || !reflect.DeepEqual(remote, r) {
		t.Errorf("HTTP search = %+v %v, want %+v", remote, err, r)
	}
	r = st.search(adapter, store.SearchRequest{Query: "mineral", Path: "/zeta"})
	if r.Total != 0 || len(r.Results) != 0 || !slices.Equal(r.Directories, []store.SearchDirectory{dirs[0], {Path: "/zeta", Abstract: "mineral storage"}}) {
		t.Errorf("directories in a scope: %+v", r)
	}

	// Without a model, a write makes the summary stale. Its text stays in
	// the search, but it can be about the old text, thus a result does not
	// show it.
	st.stop()
	adapter = st.open("en", nil, 0)
	st.put(adapter, "/team/notes.md", "The deploy needs two tokens.")
	st.put(adapter, "/yard/d.md", "feldspar and more")
	if s := st.wait(adapter, "/team/notes.md", "stale"); s.Abstract == "" {
		t.Fatal("stale summary lost its text")
	}
	if r = st.search(adapter, store.SearchRequest{Query: "kubernetes"}); r.Total != 1 || r.Results[0].Abstract != "" {
		t.Errorf("stale summary: %+v", r)
	}
	// A delete removes the summary from the search.
	if _, err := adapter.Delete(st.ctx, store.DeleteRequest{Path: "/zeta"}); err != nil {
		t.Fatal(err)
	}
	r = st.search(adapter, store.SearchRequest{Query: "quartz mineral"})
	if !slices.Equal(resultPaths(r), []string{"/alpha/b.md"}) || !slices.Equal(r.Directories, []store.SearchDirectory{{Path: "/xeno", Abstract: "mineral"}, {Path: "/yard"}}) {
		t.Errorf("after delete: %+v", r)
	}
	// The summary of a directory stays after its last document is deleted.
	// The directory is not there, thus a result does not list it.
	if _, err := adapter.Delete(st.ctx, store.DeleteRequest{Path: "/xeno/e.md"}); err != nil {
		t.Fatal(err)
	}
	if s, err := adapter.Summary(st.ctx, "/xeno"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("summary of a deleted directory: %+v %v", s, err)
	}
	if r = st.search(adapter, store.SearchRequest{Query: "mineral"}); r.Total != 0 || !slices.Equal(r.Directories, []store.SearchDirectory{{Path: "/yard"}}) {
		t.Errorf("deleted directory: %+v", r)
	}
}

// A database of an earlier version has summaries without a search vector.
func TestSearchSummaryIndex(t *testing.T) {
	st := newSummaryTest(t)
	adapter := st.open("en", fixedModel{"/docs/a.md": "walruses on the 海象岛", "/docs": "arctic animals"}, 0)
	st.put(adapter, "/docs/a.md", "text")
	st.settle(adapter)
	pool, err := pgxpool.New(st.ctx, st.dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	found := func(adapter *postgres.DocAdapter, query string) bool {
		t.Helper()
		r := st.search(adapter, store.SearchRequest{Query: query})
		return r.Total == 1 && len(r.Directories) == 1
	}
	if !found(adapter, "walrus animal") {
		t.Fatal("summaries are not in the search")
	}
	st.stop()

	// A start with all vectors there does not write the summaries.
	versions := func() string {
		t.Helper()
		var v string
		if err := pool.QueryRow(st.ctx, "select string_agg(path||' '||xmin::text, ',' order by path) from "+st.schema+".rolio_summaries").Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := versions()
	adapter = st.open("en", nil, 0)
	if after := versions(); after != before || !found(adapter, "walrus animal") {
		t.Errorf("a start wrote the summaries: %s, before %s", after, before)
	}
	st.stop()

	// The start of the server adds the columns and builds the vectors.
	if _, err := pool.Exec(st.ctx, "alter table "+st.schema+".rolio_summaries drop column search_vector, drop column search_language"); err != nil {
		t.Fatal(err)
	}
	adapter = st.open("en", nil, 0)
	if !found(adapter, "walrus animal") {
		t.Error("the start did not build the vectors")
	}
	// Reindex builds all vectors.
	if _, err := pool.Exec(st.ctx, "update "+st.schema+".rolio_summaries set search_vector=null"); err != nil {
		t.Fatal(err)
	}
	if found(adapter, "walrus animal") {
		t.Fatal("the vectors are there")
	}
	if _, err := adapter.Reindex(st.ctx); err != nil {
		t.Fatal(err)
	}
	if !found(adapter, "walrus animal") {
		t.Error("reindex did not build the vectors")
	}
	st.stop()

	// A different language builds the vectors with its configuration: the
	// simple configuration has no stemmer.
	adapter = st.open("zh", nil, 0)
	if _, err := adapter.Reindex(st.ctx); err != nil {
		t.Fatal(err)
	}
	if found(adapter, "walrus animal") || !found(adapter, "walruses animals") || !found(adapter, "海象 animals") {
		t.Error("the vectors do not have the new configuration")
	}
}
