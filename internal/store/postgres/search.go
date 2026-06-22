package postgres

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/austiecodes/rolio/internal/document"
	"github.com/austiecodes/rolio/internal/store"
)

// Search.
//
// A query is a set of terms. A document is a candidate when its text or its
// own summary contains one or more of the terms. The candidates with more of
// the terms come first, thus those with all terms are at the top. Candidates
// with the same number of terms are in the order of their score:
//
//	score = rank of the text
//	      + ownSummaryWeight * rank of the summary of the document
//	      + directoryWeight * best rank of the summaries of its directories
//
// Each rank is ts_rank_cd with normalization 32, thus it is between 0 and 1.
// The text is the primary source. A summary is derived from the text, thus
// it has a smaller weight. The summary of a directory is about many
// documents, thus it has the smallest weight: it only moves the documents of
// a directory that agrees with the query above equivalent documents of
// other directories.
//
// For each document of the result page, a second statement ranks the lines
// of the document body with the same query and the same search configuration,
// thus a passage agrees with the index. The lines that go to that statement
// are limited: see passages for the cases in which a line is not ranked.
const (
	searchLimit      = 10
	maxSearchLimit   = 100
	ownSummaryWeight = 0.5
	directoryWeight  = 0.25
	// More terms than this are not used. The work of a search is in
	// proportion to the number of candidates times the number of terms.
	maxQueryTerms = 16
	// The number of directories in a result, and the number of directories
	// that can change the score of a document. The second limit keeps the
	// work for each candidate constant.
	resultDirectories = 3
	scoreDirectories  = 20
	// The number of passages for each document, and the maximum number of
	// characters of a passage, without the label and the marks of a line
	// that is cut.
	documentPassages = 3
	passageLength    = 480
	// The maximum number of lines that PostgreSQL ranks, for one document
	// and for one search. The second limit keeps the time of the statement
	// near one second with long lines.
	maxDocumentLines = 2000
	maxSearchLines   = 20000
)

// queriesSQL makes the query that finds one or more of the terms, and one
// query for each different lexeme of the terms: "group groups" is one term
// in English. A term has only letters and digits, thus it cannot contain an
// operator of a tsquery. A term without a lexeme, for example a stop word,
// gives an empty query and is not used.
const queriesSQL = `q as materialized (
  select to_tsquery($2::regconfig, array_to_string($1::text[], ' | ')) as query
 ), terms as materialized (
  select distinct term from unnest($1::text[]) as word, to_tsquery($2::regconfig, word) as term where numnode(term) > 0
 )`

func inScopeSQL(column string) string {
	return fmt.Sprintf("($3 = '' or %[1]s = $3 or starts_with(%[1]s, $3 || '/'))", column)
}

// summaryTokens returns the text of a summary for its search vector.
func summaryTokens(abstract, overview string) string {
	return document.Tokens(abstract + "\n" + overview)
}

// indexSummaries builds the search vectors of the summaries that do not have
// one for the configured language.
func (d *DocAdapter) indexSummaries(ctx context.Context) error {
	table, _ := quoteTable(d.cfg.Schema, "rolio_summaries")
	// Usually there is no such summary. Then do not take the write lock.
	var stale bool
	if err := d.pool.QueryRow(ctx, "select exists(select 1 from "+table+" where search_language<>$1)", d.cfg.Language).Scan(&stale); err != nil || !stale {
		return err
	}
	tx, err := d.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := d.indexSummariesTx(ctx, tx, false); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// indexSummariesTx does the work of indexSummaries in a write transaction.
// With all, it builds the search vectors of all summaries.
func (d *DocAdapter) indexSummariesTx(ctx context.Context, tx pgx.Tx, all bool) error {
	table, _ := quoteTable(d.cfg.Schema, "rolio_summaries")
	// A worker locks a row and then its parent. Use the same order.
	rows, err := tx.Query(ctx, "select path, abstract, overview from "+table+" where $1 or search_language<>$2 order by path desc for update", all, d.cfg.Language)
	if err != nil {
		return err
	}
	var paths, tokens []string
	for rows.Next() {
		var p, abstract, overview string
		if err := rows.Scan(&p, &abstract, &overview); err != nil {
			rows.Close()
			return err
		}
		paths, tokens = append(paths, p), append(tokens, summaryTokens(abstract, overview))
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(paths) == 0 {
		return err
	}
	_, err = tx.Exec(ctx, "update "+table+" s set search_vector=to_tsvector($3::regconfig,t.tokens), search_language=$4"+
		" from unnest($1::text[],$2::text[]) as t(path,tokens) where s.path=t.path", paths, tokens, searchConfig(d.cfg.Language), d.cfg.Language)
	return err
}

func (d *DocAdapter) Search(ctx context.Context, req store.SearchRequest) (*store.SearchResponse, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, store.ErrEmptyQuery
	}
	if req.Offset < 0 || req.Limit < 0 {
		return nil, store.ErrInvalidParam
	}
	status, err := d.indexStatus(ctx)
	if err != nil {
		return nil, err
	}
	if status.Stale > 0 {
		return nil, fmt.Errorf("%w: search index language changed or index is missing; run rolio reindex", store.ErrConflict)
	}
	terms := document.QueryTerms(req.Query, maxQueryTerms)
	if len(terms) == 0 {
		return nil, store.ErrEmptyQuery
	}
	scope := cleanDocPath(req.Path)
	if scope == "/" {
		scope = ""
	}
	limit := min(req.Limit, maxSearchLimit)
	if limit == 0 {
		limit = searchLimit
	}
	config := searchConfig(d.cfg.Language)

	paths, _ := quoteTable(d.cfg.Schema, "rolio_paths")
	docs, _ := quoteTable(d.cfg.Schema, "rolio_documents")
	summaries, _ := quoteTable(d.cfg.Schema, "rolio_summaries")
	// The statement gives one row for each document of the page, or one row
	// without a document when the page is empty. Each row has the number of
	// candidates and the directories. A summary that is not ready can be
	// about an older text: it is used to find documents, and its abstract is
	// not shown. Without a model, the summary of a directory stays after its
	// last document is deleted, thus top lists only directories with
	// documents. That filter cannot use an index, thus it comes after the
	// limit of dirs.
	query := fmt.Sprintf(`with %[4]s, dirs as materialized (
  select s.path, case when s.status = 'ready' then s.abstract else '' end as abstract, ts_rank_cd(s.search_vector, q.query, 32) as rank,
   (select count(*) from terms where s.search_vector @@ term) as matched
  from %[3]s s, q
  where s.search_vector @@ q.query and s.path <> '/' and %[5]s
   and not exists (select 1 from %[1]s f where f.path = s.path)
  order by matched desc, rank desc, s.path limit %[7]d
 ), hits as (
  select f.path from %[1]s f join %[2]s d on d.id = f.doc_id, q where d.search_vector @@ q.query and %[6]s
  union
  select f.path from %[1]s f join %[3]s s on s.path = f.path, q where s.search_vector @@ q.query and %[6]s
 ), found as materialized (
  select f.path, f.doc_id, f.size, f.mtime, case when s.status = 'ready' then s.abstract else '' end as abstract,
   (select count(*) from terms where d.search_vector @@ term or s.search_vector @@ term) as matched,
   coalesce(ts_rank_cd(d.search_vector, q.query, 32), 0)
    + %[9]g * coalesce(ts_rank_cd(s.search_vector, q.query, 32), 0)
    + %[10]g * coalesce((select max(dirs.rank) from dirs where starts_with(f.path, dirs.path || '/')), 0) as score
  from hits h join %[1]s f on f.path = h.path join %[2]s d on d.id = f.doc_id left join %[3]s s on s.path = f.path, q
 ), page as (
  select * from found order by matched desc, score desc, path limit $4 offset $5
 ), top as (
  select path, abstract, row_number() over (order by matched desc, rank desc, path) as n from dirs
  where exists (select 1 from %[1]s f where starts_with(f.path, dirs.path || '/')) order by n limit %[8]d
 )
 select (select count(*) from found), array(select path from top order by n), array(select abstract from top order by n),
  p.path, p.score, p.size, p.mtime, p.abstract, d.content
 from (select 1) one left join page p on true left join %[2]s d on d.id = p.doc_id
 order by p.matched desc, p.score desc, p.path`,
		paths, docs, summaries, queriesSQL, inScopeSQL("s.path"), inScopeSQL("f.path"), scoreDirectories, resultDirectories, ownSummaryWeight, directoryWeight)
	rows, err := d.pool.Query(ctx, query, terms, config, scope, limit, req.Offset)
	if err != nil {
		return nil, fmt.Errorf("doc search: %w", err)
	}
	defer rows.Close()
	resp := &store.SearchResponse{}
	var contents []string
	for rows.Next() {
		var dirPaths, dirAbstracts []string
		var p, abstract, content *string
		var score *float64
		var size *int64
		var mtime *time.Time
		if err := rows.Scan(&resp.Total, &dirPaths, &dirAbstracts, &p, &score, &size, &mtime, &abstract, &content); err != nil {
			return nil, fmt.Errorf("doc search scan: %w", err)
		}
		if resp.Directories == nil {
			for i, dir := range dirPaths {
				resp.Directories = append(resp.Directories, store.SearchDirectory{Path: dir, Abstract: dirAbstracts[i]})
			}
		}
		if p == nil {
			continue
		}
		resp.Results = append(resp.Results, store.SearchResult{Path: *p, Rank: *score, Abstract: *abstract, Size: *size, ModTime: mtime.UTC().Format(time.RFC3339)})
		contents = append(contents, *content)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("doc search rows: %w", err)
	}
	if err := d.passages(ctx, resp.Results, contents, terms, config); err != nil {
		return nil, err
	}
	return resp, nil
}

// passages gives each result the lines of its document body that agree best
// with the query. PostgreSQL ranks the lines, thus a line agrees with the
// query by the same rules as a document, for example with the same stemmer.
// Not all lines are ranked: a filter selects the lines, and the number of
// lines has a limit for each document and for the search. Thus a document
// can be a result without a passage. A result also has the number of the
// other lines that agree with the query, thus the reader knows that the
// passages are only a part of the document.
func (d *DocAdapter) passages(ctx context.Context, results []store.SearchResult, contents, terms []string, config string) error {
	// Only the lines that contain the first 2 characters of a term go to
	// PostgreSQL. A line that agrees with the query has a word with the
	// lexeme of a term, and the English stemmer does not change the first 2
	// characters of a word: it can change the third ("try" and "tried",
	// "use" and "using"). The known exceptions are the words of "die",
	// "lie", and "tie" with their forms "dying", "lying", and "tying": a
	// query with one form does not get the lines with the other form as
	// passages. The document stays a result.
	var starts []string
	for _, term := range terms {
		runes := []rune(term)
		starts = append(starts, string(runes[:min(len(runes), 2)]))
	}
	candidate := func(line string) bool {
		line = strings.ToLower(line)
		return slices.ContainsFunc(starts, func(start string) bool { return strings.Contains(line, start) })
	}
	// The lines of all documents are one list. A line is known by its
	// position in that list. When the list is full, the subsequent documents
	// of the page get fewer passages or none.
	var owners []int32
	var lines, tokens []string
	for i, content := range contents {
		body := content
		// A legacy document can have frontmatter that is not correct. Then
		// all its text is the body, as in Cat.
		if doc, err := parseDocument(results[i].Path, content); err == nil {
			body = doc.Body
		}
		count := 0
		for _, line := range document.Passages(body, passageLength) {
			if !candidate(line) {
				continue
			}
			// The lines after a limit are not ranked, thus the number of
			// lines that agree with the query is a lower limit. The exact
			// number needs the rank of all lines.
			if count == maxDocumentLines || len(lines) == maxSearchLines {
				results[i].MoreLinesMin = true
				break
			}
			owners, lines, tokens = append(owners, int32(i)), append(lines, line), append(tokens, document.Tokens(line))
			count++
		}
	}
	if len(lines) == 0 {
		return nil
	}
	// Each row is a passage, with the number of lines of its document that
	// agree with the query.
	rows, err := d.pool.Query(ctx, `with `+queriesSQL+`
 select doc, n, found from (
  select doc, n, row_number() over (partition by doc order by matched desc, rank desc, n) as place, count(*) over (partition by doc) as found
  from (
   select l.doc, l.n, ts_rank_cd(v, q.query) as rank, (select count(*) from terms where v @@ term) as matched
   from unnest($3::int[], $4::text[]) with ordinality as l(doc, tokens, n), to_tsvector($2::regconfig, l.tokens) as v, q
   where v @@ q.query
  ) matches
 ) ranked where place <= $5 order by n`, terms, config, owners, tokens, documentPassages)
	if err != nil {
		return fmt.Errorf("doc search passages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var doc, found int
		var n int64
		if err := rows.Scan(&doc, &n, &found); err != nil {
			return fmt.Errorf("doc search passages scan: %w", err)
		}
		results[doc].Passages = append(results[doc].Passages, lines[n-1])
		results[doc].MoreLines = found - len(results[doc].Passages)
	}
	for i := range results {
		results[i].Snippet = strings.Join(results[i].Passages, "\n")
	}
	return rows.Err()
}
