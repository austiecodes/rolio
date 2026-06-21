package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/austiecodes/rolio/internal/document"
	"github.com/austiecodes/rolio/internal/store"
	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"
)

func searchConfig(language string) string {
	if language == "zh" {
		return "simple"
	}
	return "english"
}
func parseDocument(p, content string) (document.Document, error) {
	if !strings.EqualFold(path.Ext(p), ".md") {
		return document.Document{Body: content}, nil
	}
	doc, err := document.Parse(content)
	if err != nil {
		return doc, fmt.Errorf("%w: %s: %v", store.ErrInvalidParam, p, err)
	}
	return doc, nil
}
func (d *DocAdapter) indexDocument(ctx context.Context, tx pgx.Tx, id, p, content string) error {
	doc, err := parseDocument(p, content)
	if err != nil {
		return err
	}
	metadata := []byte("{}")
	if doc.Metadata != nil {
		metadata, err = json.Marshal(doc.Metadata)
		if err != nil {
			return fmt.Errorf("%w: metadata must be JSON-compatible", store.ErrInvalidParam)
		}
	}
	docs, _ := quoteTable(d.cfg.Schema, "rolio_documents")
	_, err = tx.Exec(ctx, "update "+docs+" set metadata=$2, search_vector=to_tsvector($3::regconfig,$4), search_language=$5 where id=$1", id, string(metadata), searchConfig(d.cfg.Language), document.Tokens(p+"\n"+string(metadata)+"\n"+doc.Body), d.cfg.Language)
	return err
}
func (d *DocAdapter) indexStatus(ctx context.Context) (*store.IndexStatus, error) {
	table, _ := quoteTable(d.cfg.Schema, "rolio_documents")
	var s store.IndexStatus
	err := d.pool.QueryRow(ctx, "select count(*), count(*) filter(where search_language<>$1 or search_vector is null) from "+table, d.cfg.Language).Scan(&s.Total, &s.Stale)
	return &s, err
}
func (d *DocAdapter) Settings(ctx context.Context) (*store.Settings, error) {
	s, err := d.indexStatus(ctx)
	if err != nil {
		return nil, err
	}
	return &store.Settings{Language: d.cfg.Language, SummaryEnabled: d.cfg.Generator != nil, Index: *s}, nil
}
func (d *DocAdapter) Reindex(ctx context.Context) (*store.IndexStatus, error) {
	tx, err := d.beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	docs, _ := quoteTable(d.cfg.Schema, "rolio_documents")
	paths, _ := quoteTable(d.cfg.Schema, "rolio_paths")
	rows, err := tx.Query(ctx, "select d.id,p.path,d.content from "+docs+" d join "+paths+" p on p.doc_id=d.id order by p.path")
	if err != nil {
		return nil, err
	}
	type entry struct{ id, p, content string }
	var entries []entry
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.id, &e.p, &e.content); err != nil {
			rows.Close()
			return nil, err
		}
		entries = append(entries, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if err = d.indexDocument(ctx, tx, e.id, e.p, e.content); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return d.indexStatus(ctx)
}

// invalidate makes the summaries of a changed path and of its ancestors
// stale, in the transaction of the write. With a model, it also puts them in
// the summary queue. It removes the token, thus a generation in progress
// cannot publish a summary of the old content.
func (d *DocAdapter) invalidate(ctx context.Context, tx pgx.Tx, p string, deleted bool) error {
	table, _ := quoteTable(d.cfg.Schema, "rolio_summaries")
	if deleted {
		if _, err := tx.Exec(ctx, "delete from "+table+" where path=$1 or starts_with(path,$1||'/')", p); err != nil {
			return err
		}
	}
	paths, parents := summaryPaths(p, !deleted)
	if d.cfg.Generator == nil {
		_, err := tx.Exec(ctx, "update "+table+" set status='stale', token='', error='' where path=any($1)", paths)
		return err
	}
	// The delay collects the writes of a short period into one job.
	_, err := tx.Exec(ctx, "insert into "+table+"(path,parent,language,status,due) select path, parent, $3, 'stale', now()+$4::interval from unnest($1::text[],$2::text[]) as t(path,parent)"+
		" on conflict(path) do update set status='stale', token='', error='', attempts=0, due=excluded.due", paths, parents, d.cfg.Language, d.cfg.SummaryDelay)
	return err
}
func (d *DocAdapter) Summary(ctx context.Context, p string) (*store.Summary, error) {
	p, err := validPath(p)
	if err != nil {
		return nil, err
	}
	paths, _ := quoteTable(d.cfg.Schema, "rolio_paths")
	var isFile, exists bool
	err = d.pool.QueryRow(ctx, "select exists(select 1 from "+paths+" where path=$1), exists(select 1 from "+paths+" where starts_with(path,$2))", p, normalizePrefix(p)).Scan(&isFile, &exists)
	if err != nil {
		return nil, err
	}
	if !isFile && !exists && p != "/" {
		return nil, store.ErrNotFound
	}
	s := &store.Summary{Path: p, Language: d.cfg.Language, Status: "missing"}
	table, _ := quoteTable(d.cfg.Schema, "rolio_summaries")
	var updated time.Time
	err = d.pool.QueryRow(ctx, "select language,source_hash,abstract,overview,status,error,updated_at from "+table+" where path=$1", p).Scan(&s.Language, &s.SourceHash, &s.Abstract, &s.Overview, &s.Status, &s.Error, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	s.UpdatedAt = updated.UTC().Format(time.RFC3339)
	if s.Language != d.cfg.Language && s.Status == "ready" {
		s.Status = "stale"
		s.Error = "language changed; refresh this summary"
	}
	return s, nil
}
func (d *DocAdapter) sidecar(ctx context.Context, p string) (*store.CatResponse, error) {
	s, err := d.Summary(ctx, path.Dir(p))
	if err != nil {
		return nil, err
	}
	body := s.Abstract
	if path.Base(p) == ".overview.md" {
		body = s.Overview
	}
	if body == "" {
		return nil, store.ErrContentNotReady
	}
	meta := map[string]any{"directory": s.Path, "language": s.Language, "source_hash": s.SourceHash, "status": s.Status, "generated_by": "rolio"}
	header, err := yaml.Marshal(meta)
	if err != nil {
		return nil, err
	}
	raw := "---\n" + string(header) + "---\n\n" + body + "\n"
	return &store.CatResponse{Path: p, Content: raw, Body: body, Metadata: meta, Hash: store.HashContent(raw)}, nil
}

// Refresh puts a path in the summary queue and waits for its summary. The
// model is called for the path also when its input did not change. The paths
// below it that have no summary or a failed one go into the queue too. The
// workers of RunSummaries do the generation. After refreshLimit, Refresh
// returns the summary with the status that it has at that time.
func (d *DocAdapter) Refresh(ctx context.Context, p string) (*store.Summary, error) {
	if d.cfg.Generator == nil {
		return nil, fmt.Errorf("%w: configure summary.url and summary.model first", store.ErrNotSupported)
	}
	if !d.summarizing.Load() {
		return nil, fmt.Errorf("%w: the summary workers do not run", store.ErrNotSupported)
	}
	s, err := d.Summary(ctx, p)
	if err != nil {
		return nil, err
	}
	if err := d.enqueue(ctx, s.Path, true); err != nil {
		return nil, err
	}
	limit := time.After(refreshLimit)
	for {
		s, err := d.Summary(ctx, p)
		if err != nil {
			return nil, err
		}
		switch s.Status {
		case "ready":
			// A directory has no summary when no child has one, for example
			// because all generations below it failed.
			if s.Abstract == "" && s.Overview == "" {
				return nil, fmt.Errorf("%w: %s has no text to summarize, or the summaries below it failed", store.ErrContentNotReady, s.Path)
			}
			return s, nil
		case "failed":
			return nil, fmt.Errorf("summary generation failed: %s", s.Error)
		case "missing":
			return nil, fmt.Errorf("%w: no source files", store.ErrNotFound)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-limit:
			return s, nil
		case <-time.After(200 * time.Millisecond):
		}
	}
}
