package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/austiecodes/rolio/internal/store"
	"github.com/austiecodes/rolio/internal/summary"
)

// The summary queue.
//
// Each document and each directory has one row in rolio_summaries. A row with
// the status stale is a job. A write puts the document and its ancestors in
// the queue in the same transaction, thus many writes to one directory make
// one job for that directory.
//
// The workers go from the bottom of the tree to the top. A row is not taken
// while one of its children is in the queue. A write puts all ancestors of
// a row in the queue, and a refresh puts there the ancestors to the path of
// the refresh, thus a job in a lower level shows as a job of a child.
//
// A document gets its summary from its text. A directory gets its summary
// from the summaries of its children, thus its input does not grow with the
// size of the documents below it. When a new summary is different from the
// old one, the parent goes into the queue again.
//
// The source hash of a row is the hash of the model input. When the input
// did not change, the model is not called.

const (
	// A stale generation is one that the server did not complete. Other
	// workers take it again after this time.
	abandonedAfter = 2 * time.Minute
	idlePoll       = time.Second
	// A generation that fails is tried again, to this number of attempts.
	summaryAttempts = 3
	// Refresh waits for the summary to this limit.
	refreshLimit = 5 * time.Minute
)

// parentPath returns the parent directory of a path. The root has no parent.
func parentPath(p string) string {
	if p == "/" {
		return ""
	}
	return path.Dir(p)
}

// summaryPaths returns p and its ancestors, and the parent of each.
func summaryPaths(p string, self bool) (paths, parents []string) {
	if !self {
		p = parentPath(p)
	}
	for ; p != ""; p = parentPath(p) {
		paths, parents = append(paths, p), append(parents, parentPath(p))
	}
	return paths, parents
}

// inTree reports if p is root or is below root.
func inTree(p, root string) bool {
	return root == "/" || p == root || strings.HasPrefix(p, root+"/")
}

// RunSummaries generates the summaries in the queue with the given number of
// workers. It returns when ctx is done.
func (d *DocAdapter) RunSummaries(ctx context.Context, workers int) error {
	if d.cfg.Generator == nil {
		return fmt.Errorf("%w: configure summary.url and summary.model first", store.ErrNotSupported)
	}
	// Without this step, documents that were written without a model never
	// get a summary. Thus try until it is done.
	for {
		err := d.enqueue(ctx, "/", false)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return nil
		}
		log.Printf("summary queue: start: %v", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(idlePoll):
		}
	}
	d.summarizing.Store(true)
	defer d.summarizing.Store(false)
	var wg sync.WaitGroup
	for range max(workers, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				worked, err := d.summarizeNext(ctx)
				if err != nil && ctx.Err() == nil {
					log.Printf("summary queue: %v", err)
				}
				if !worked || err != nil {
					select {
					case <-ctx.Done():
					case <-time.After(idlePoll):
					}
				}
			}
		}()
	}
	wg.Wait()
	return nil
}

// enqueue puts in the queue the paths in root that need a summary: those
// without a row, those with a failed summary, and those with a summary in a
// different language. With force, root itself is also put in the queue and
// the model is called for it also when its input did not change. The
// ancestors in root of each such path go into the queue too, which keeps the
// order from the bottom to the top.
func (d *DocAdapter) enqueue(ctx context.Context, root string, force bool) error {
	table, _ := quoteTable(d.cfg.Schema, "rolio_summaries")
	paths, _ := quoteTable(d.cfg.Schema, "rolio_paths")
	// The lock gives this transaction the same order of row locks as a write.
	tx, err := d.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// The directories of a document are the prefixes of its path.
	rows, err := tx.Query(ctx, fmt.Sprintf(`with files as (
  select path, string_to_array(path,'/') as parts from %[2]s where $1='/' or path=$1 or starts_with(path,$1||'/')
 ), nodes as (
  select path from files
  union select '/' from files
  union select array_to_string(parts[1:n],'/') from files, generate_series(2, cardinality(parts)-1) as n
 ), added as (
  insert into %[1]s(path,parent,language,status)
  select path, %[3]s, $2, 'stale' from nodes where $1='/' or path=$1 or starts_with(path,$1||'/')
  on conflict(path) do nothing returning path
 )
 select path from added
 union select path from %[1]s where ($1='/' or path=$1 or starts_with(path,$1||'/')) and (status='failed' or (status='ready' and language<>$2))`,
		table, paths, parentSQL("path")), root, d.cfg.Language)
	if err != nil {
		return err
	}
	pending := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		for ; p != "" && inTree(p, root) && !pending[p]; p = parentPath(p) {
			pending[p] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if force {
		pending[root] = true
	}
	list := make([]string, 0, len(pending))
	for p := range pending {
		list = append(list, p)
	}
	// A worker locks a row and then its parent. Use the same order.
	_, err = tx.Exec(ctx, "update "+table+" set status='stale', token='', error='', attempts=0, due=now(),"+
		" source_hash=case when $2 and path=$3 then '' else source_hash end"+
		" where path in (select path from "+table+" where path=any($1) order by path desc for update)", list, force, root)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type summaryJob struct {
	path, token, sourceHash, language, abstract, overview string
	attempts                                              int
}

// claim takes one job from the queue. It returns nil when no job is ready.
func (d *DocAdapter) claim(ctx context.Context) (*summaryJob, error) {
	table, _ := quoteTable(d.cfg.Schema, "rolio_summaries")
	query := fmt.Sprintf(`update %[1]s set status='generating', token=gen_random_uuid()::text, due=now()
 where path=(
  select s.path from %[1]s s
  where ((s.status='stale' and s.due<=now()) or (s.status='generating' and s.due<now()-$1::interval))
   and not exists (select 1 from %[1]s c where c.parent=s.path and c.status in ('stale','generating'))
  order by s.due limit 1 for update of s skip locked)
 returning path, token, source_hash, language, abstract, overview, attempts`, table)
	var job summaryJob
	err := d.pool.QueryRow(ctx, query, abandonedAfter).Scan(&job.path, &job.token, &job.sourceHash, &job.language, &job.abstract, &job.overview, &job.attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &job, err
}

// summarizeNext does one job. It reports if there was a job.
func (d *DocAdapter) summarizeNext(ctx context.Context) (bool, error) {
	job, err := d.claim(ctx)
	if job == nil || err != nil {
		return false, err
	}
	table, _ := quoteTable(d.cfg.Schema, "rolio_summaries")
	// A write gives the row a new token. Each update of this job has an
	// effect only while its generation is the current one.
	current := " where path=$1 and token=$2 and status='generating'"
	// release puts the job back when the cause is not the job itself: the
	// database, or a server that stops. It must work after ctx is done.
	release := func(cause error) (bool, error) {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, err := d.pool.Exec(releaseCtx, "update "+table+" set status='stale', token='', due=now()"+current, job.path, job.token)
		return true, errors.Join(cause, err)
	}
	// finish gives the result of the last update of the job. If the update
	// failed, the job goes back, and does not wait until it is abandoned.
	finish := func(err error) (bool, error) {
		if err != nil {
			return release(err)
		}
		return true, nil
	}
	request, err := d.summaryRequest(ctx, job.path)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// The document or the last document of the directory was deleted.
		_, err = d.pool.Exec(ctx, "delete from "+table+current, job.path, job.token)
		return finish(err)
	case errors.Is(err, store.ErrInvalidParam):
		// The document has frontmatter that is not correct. A new attempt
		// gives the same result.
		return finish(d.failSummary(ctx, job, err, summaryAttempts))
	case err != nil:
		return release(err)
	}
	sources, _ := json.Marshal(request.Sources)
	sourceHash := store.HashContent(string(sources))
	result := summary.Result{Abstract: job.abstract, Overview: job.overview}
	switch {
	case len(request.Sources) == 0:
		// An empty document, or a directory of which no child has a summary.
		result = summary.Result{}
	case job.sourceHash == sourceHash && job.language == d.cfg.Language && (job.abstract != "" || job.overview != ""):
		// The model input did not change. Keep the summary.
	default:
		result, err = d.cfg.Generator.Generate(ctx, request)
		if ctx.Err() != nil {
			return release(nil)
		}
		if err != nil {
			return finish(d.failSummary(ctx, job, err, job.attempts+1))
		}
	}
	// A different summary changes the input of the parent, thus the parent
	// goes into the queue, with all its attempts. The two updates are one
	// statement.
	changed := result.Abstract != job.abstract || result.Overview != job.overview
	_, err = d.pool.Exec(ctx, fmt.Sprintf(`with done as (
  update %[1]s set abstract=$3, overview=$4, language=$5, source_hash=$6, status='ready', error='', attempts=0, token='',
   search_vector=to_tsvector($8::regconfig,$9), search_language=$5,
   updated_at=case when $7 then now() else updated_at end %[2]s returning parent
 )
 update %[1]s set status='stale', token='', attempts=0, due=case when status='stale' then due else now() end
 where $7 and path=(select parent from done)`, table, current),
		job.path, job.token, result.Abstract, result.Overview, d.cfg.Language, sourceHash, changed,
		searchConfig(d.cfg.Language), summaryTokens(result.Abstract, result.Overview))
	return finish(err)
}

// failSummary records a generation that failed. Before the last attempt, the
// job goes back into the queue with a delay, and its parent waits for it.
func (d *DocAdapter) failSummary(ctx context.Context, job *summaryJob, cause error, attempts int) error {
	log.Printf("summary of %s: attempt %d failed: %v", job.path, attempts, cause)
	table, _ := quoteTable(d.cfg.Schema, "rolio_summaries")
	status := "stale"
	if attempts >= summaryAttempts {
		status = "failed"
	}
	_, err := d.pool.Exec(ctx, "update "+table+" set status=$3, error=$4, attempts=$5, token='', due=now()+$6::interval, updated_at=now() where path=$1 and token=$2 and status='generating'",
		job.path, job.token, status, cause.Error(), attempts, time.Duration(attempts)*d.cfg.SummaryRetry)
	return err
}

// cutText returns the first bytes of text, to a maximum of n, and does not
// divide a character.
func cutText(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n]
}

type summaryChild struct {
	path, abstract, overview string
	file                     bool
}

// childSources returns the model input for the children of a directory, to
// a maximum of limit bytes. A child without a summary gives no input.
func childSources(children []summaryChild, limit int) []summary.Source {
	// A directory gives its abstract. A document gives its overview, which
	// has more of the facts.
	var known []summaryChild
	long := 0
	for _, c := range children {
		if !c.file {
			c.path, c.overview = c.path+"/", c.abstract
		} else if c.overview == "" {
			c.overview = c.abstract
		}
		if c.overview == "" {
			continue
		}
		known = append(known, c)
		long += len(c.path) + len(c.overview)
	}
	var sources []summary.Source
	size := 0
	for i, c := range known {
		body := c.overview
		// With many children, use the short summary of each.
		if long > limit && c.abstract != "" {
			body = c.abstract
		}
		if size += len(c.path) + len(body); size > limit {
			return append(sources, summary.Source{Body: fmt.Sprintf("%d more children are not in this list.", len(known)-i)})
		}
		sources = append(sources, summary.Source{Path: c.path, Body: body})
	}
	return sources
}

// summaryRequest returns the model input for a path. It returns ErrNotFound
// when the path is not a document and has no children.
func (d *DocAdapter) summaryRequest(ctx context.Context, p string) (summary.Request, error) {
	request := summary.Request{Language: d.cfg.Language, Path: p}
	paths, _ := quoteTable(d.cfg.Schema, "rolio_paths")
	docs, _ := quoteTable(d.cfg.Schema, "rolio_documents")
	table, _ := quoteTable(d.cfg.Schema, "rolio_summaries")
	var content string
	err := d.pool.QueryRow(ctx, "select d.content from "+paths+" p join "+docs+" d on d.id=p.doc_id where p.path=$1", p).Scan(&content)
	if err == nil {
		doc, err := parseDocument(p, content)
		if err != nil {
			return request, err
		}
		if body := strings.TrimSpace(doc.Body); body != "" {
			request.Sources = []summary.Source{{Path: p, Body: cutText(body, summary.MaxSourceBytes)}}
		}
		return request, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return request, err
	}
	request.Directory = true
	// The row of an empty directory is deleted before its parent is taken,
	// thus each row here is a document or a directory with documents.
	rows, err := d.pool.Query(ctx, fmt.Sprintf(`select s.path, s.abstract, s.overview, exists(select 1 from %[2]s f where f.path=s.path)
 from %[1]s s where s.parent=$1 order by s.path`, table, paths), p)
	if err != nil {
		return request, err
	}
	defer rows.Close()
	var children []summaryChild
	for rows.Next() {
		var c summaryChild
		if err := rows.Scan(&c.path, &c.abstract, &c.overview, &c.file); err != nil {
			return request, err
		}
		children = append(children, c)
	}
	if err := rows.Err(); err != nil {
		return request, err
	}
	if len(children) == 0 {
		return request, store.ErrNotFound
	}
	request.Sources = childSources(children, summary.MaxSourceBytes)
	return request, nil
}
