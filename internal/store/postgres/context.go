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
	"github.com/austiecodes/rolio/internal/summary"
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

// Invalidation participates in the same transaction as the original write.
// Clearing tokens prevents in-flight generations from publishing old content.
func (d *DocAdapter) invalidate(ctx context.Context, tx pgx.Tx, p string, deleted bool) error {
	table, _ := quoteTable(d.cfg.Schema, "rolio_summaries")
	if deleted {
		if _, err := tx.Exec(ctx, "delete from "+table+" where path=$1 or starts_with(path,$1||'/')", p); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, "update "+table+" set status='stale', token='', error='' where path='/' or starts_with($1,path||'/')", p)
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
	if isFile {
		return nil, store.ErrNotDir
	}
	if !exists && p != "/" {
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
	} else if s.Status == "generating" && time.Since(updated) > 2*time.Minute {
		s.Status = "failed"
		s.Error = "generation interrupted; refresh to retry"
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
func (d *DocAdapter) Refresh(ctx context.Context, p string) (*store.Summary, error) {
	if d.cfg.Generator == nil {
		return nil, fmt.Errorf("%w: configure summary.url and summary.model first", store.ErrNotSupported)
	}
	p, err := validPath(p)
	if err != nil {
		return nil, err
	}
	tx, err := d.beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	paths, _ := quoteTable(d.cfg.Schema, "rolio_paths")
	docs, _ := quoteTable(d.cfg.Schema, "rolio_documents")
	table, _ := quoteTable(d.cfg.Schema, "rolio_summaries")
	var isFile bool
	if err = tx.QueryRow(ctx, "select exists(select 1 from "+paths+" where path=$1)", p).Scan(&isFile); err != nil {
		return nil, err
	}
	if isFile {
		return nil, store.ErrNotDir
	}
	rows, err := tx.Query(ctx, "select p.path,d.content,d.content_hash from "+paths+" p join "+docs+" d on d.id=p.doc_id where starts_with(p.path,$1) order by p.path", normalizePrefix(p))
	if err != nil {
		return nil, err
	}
	var sources []summary.Source
	var versions [][2]string
	size := 0
	for rows.Next() {
		var name, content, hash string
		if err = rows.Scan(&name, &content, &hash); err != nil {
			rows.Close()
			return nil, err
		}
		doc, parseErr := parseDocument(name, content)
		if parseErr != nil {
			rows.Close()
			return nil, parseErr
		}
		size += len(name) + len(doc.Body)
		if size > summary.MaxSourceBytes {
			rows.Close()
			return nil, fmt.Errorf("%w: directory exceeds summary input limit (%d bytes); refresh a smaller directory", store.ErrInvalidParam, summary.MaxSourceBytes)
		}
		sources = append(sources, summary.Source{Path: name, Body: doc.Body})
		versions = append(versions, [2]string{name, hash})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("%w: directory has no source files", store.ErrNotFound)
	}
	versionBytes, _ := json.Marshal(versions)
	sourceHash := store.HashContent(string(versionBytes))
	var token string
	err = tx.QueryRow(ctx, "insert into "+table+" as s (path,language,status,token) values($1,$2,'generating',gen_random_uuid()::text) on conflict(path) do update set status='generating',token=gen_random_uuid()::text,error='',updated_at=now() where s.status<>'generating' or s.updated_at<now()-interval '2 minutes' returning token", p, d.cfg.Language).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: generation already in progress", store.ErrConflict)
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	genCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	result, genErr := d.cfg.Generator.Generate(genCtx, d.cfg.Language, p, sources)
	cancel()
	// A cancelled client must still leave a retryable persisted state.
	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer saveCancel()
	if genErr != nil {
		_, saveErr := d.pool.Exec(saveCtx, "update "+table+" set status='failed',error=$3,token='',updated_at=now() where path=$1 and token=$2", p, token, genErr.Error())
		if saveErr != nil {
			return nil, saveErr
		}
		return nil, genErr
	}
	// Serialize publication with writes/deletes, just as snapshot creation does.
	saveTx, err := d.beginWrite(saveCtx)
	if err != nil {
		return nil, err
	}
	defer saveTx.Rollback(saveCtx)
	tag, err := saveTx.Exec(saveCtx, "update "+table+" set abstract=$3,overview=$4,language=$5,source_hash=$6,status='ready',error='',token='',updated_at=now() where path=$1 and token=$2 and status='generating'", p, token, result.Abstract, result.Overview, d.cfg.Language, sourceHash)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, fmt.Errorf("%w: source changed during generation; refresh again", store.ErrConflict)
	}
	if err = saveTx.Commit(saveCtx); err != nil {
		return nil, err
	}
	return d.Summary(saveCtx, p)
}
