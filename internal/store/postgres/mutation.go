package postgres

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/austiecodes/rolio/internal/store"
)

// Serialize tree mutations across processes. This protects parent/file
// invariants as well as create-only and read/modify/write operations.
func (d *DocAdapter) beginWrite(ctx context.Context) (pgx.Tx, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	table, err := quoteTable(d.cfg.Schema, "rolio_paths")
	if err == nil {
		_, err = tx.Exec(ctx, "lock table "+table+" in share row exclusive mode")
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
func (d *DocAdapter) file(ctx context.Context, tx pgx.Tx, p string) (id, content, hash string, err error) {
	query, err := DocSelectForUpdateSQL(d.cfg)
	if err != nil {
		return "", "", "", err
	}
	err = tx.QueryRow(ctx, query, p).Scan(&id, &content, &hash)
	return
}
func checkHash(expected, actual string, exists bool) error {
	if expected == "" {
		return nil
	}
	if expected == "*" {
		if exists {
			return store.ErrConflict
		}
		return nil
	}
	if !exists || actual != expected {
		return store.ErrConflict
	}
	return nil
}
func validPath(p string) (string, error) {
	if strings.Contains(p, "://") || strings.ContainsRune(p, 0) {
		return "", store.ErrInvalidParam
	}
	return cleanDocPath(p), nil
}
func writablePath(p string) error {
	for _, part := range strings.Split(p, "/") {
		if part == ".abstract.md" || part == ".overview.md" {
			return fmt.Errorf("%w: summary sidecars are server-managed; use refresh", store.ErrInvalidParam)
		}
	}
	return nil
}
func (d *DocAdapter) checkFilePath(ctx context.Context, tx pgx.Tx, p string) error {
	table, err := quoteTable(d.cfg.Schema, "rolio_paths")
	if err != nil {
		return err
	}
	var directory bool
	if err := tx.QueryRow(ctx, "select exists(select 1 from "+table+" where starts_with(path,$1))", p+"/").Scan(&directory); err != nil {
		return err
	}
	if directory {
		return store.ErrIsDir
	}
	for parent := path.Dir(p); parent != "/"; parent = path.Dir(parent) {
		var exists bool
		if err := tx.QueryRow(ctx, "select exists(select 1 from "+table+" where path=$1)", parent).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return store.ErrNotDir
		}
	}
	return nil
}
func (d *DocAdapter) Put(ctx context.Context, req store.PutRequest) (*store.PutResponse, error) {
	p, err := validPath(req.Path)
	if err != nil {
		return nil, err
	}
	if p == "/" {
		return nil, store.ErrIsDir
	}
	if err := writablePath(p); err != nil {
		return nil, err
	}
	tx, err := d.beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := d.checkFilePath(ctx, tx, p); err != nil {
		return nil, err
	}
	id, _, oldHash, err := d.file(ctx, tx, p)
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err := checkHash(req.ExpectedHash, oldHash, exists); err != nil {
		return nil, err
	}
	hash := store.HashContent(req.Content)
	if exists {
		query, err := DocUpdateByPathSQL(d.cfg)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, query, p, req.Content, hash, path.Base(p)); err != nil {
			return nil, err
		}
	} else {
		query, err := DocInsertSQL(d.cfg)
		if err != nil {
			return nil, err
		}
		if err := tx.QueryRow(ctx, query, path.Base(p), req.Content, hash).Scan(&id); err != nil {
			return nil, err
		}
	}
	query, err := DocUpsertPathSQL(d.cfg)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, query, p, id, int64(len(req.Content))); err != nil {
		return nil, err
	}
	if err := d.indexDocument(ctx, tx, id, p, req.Content); err != nil {
		return nil, err
	}
	if oldHash != hash {
		if err := d.invalidate(ctx, tx, p, false); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &store.PutResponse{Node: store.Node{Path: p, Name: path.Base(p), Kind: "file", Size: int64(len(req.Content)), Hash: hash, ModTime: time.Now().UTC().Format(time.RFC3339)}}, nil
}
func (d *DocAdapter) Delete(ctx context.Context, req store.DeleteRequest) (*store.DeleteResponse, error) {
	p, err := validPath(req.Path)
	if err != nil {
		return nil, err
	}
	if p == "/" {
		return nil, store.ErrCannotDeleteRoot
	}
	if err := writablePath(p); err != nil {
		return nil, err
	}
	tx, err := d.beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if req.ExpectedHash != "" {
		_, _, hash, err := d.file(ctx, tx, p)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		if req.ExpectedHash != hash {
			return nil, store.ErrConflict
		}
	}
	paths, err := quoteTable(d.cfg.Schema, "rolio_paths")
	if err != nil {
		return nil, err
	}
	docs, err := quoteTable(d.cfg.Schema, "rolio_documents")
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf("with removed as (delete from %s where path=$1 or starts_with(path,$1||'/') returning doc_id) delete from %s where id in (select doc_id from removed)", paths, docs)
	tag, err := tx.Exec(ctx, query, p)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, store.ErrNotFound
	}
	if err := d.invalidate(ctx, tx, p, true); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &store.DeleteResponse{}, nil
}
func (d *DocAdapter) Edit(ctx context.Context, req store.EditRequest) (*store.EditResponse, error) {
	p, err := validPath(req.Path)
	if err != nil {
		return nil, err
	}
	if p == "/" {
		return nil, store.ErrIsDir
	}
	if err := writablePath(p); err != nil {
		return nil, err
	}
	if req.Old == "" {
		return nil, store.ErrEmptyOld
	}
	tx, err := d.beginWrite(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	id, content, hash, err := d.file(ctx, tx, p)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := checkHash(req.ExpectedHash, hash, true); err != nil {
		return nil, err
	}
	count := strings.Count(content, req.Old)
	if count == 0 {
		return nil, store.ErrOldNotFound
	}
	if !req.All {
		count = 1
	}
	content = strings.Replace(content, req.Old, req.New, count)
	if err := d.indexDocument(ctx, tx, id, p, content); err != nil {
		return nil, err
	}
	if hash != store.HashContent(content) {
		if err := d.invalidate(ctx, tx, p, false); err != nil {
			return nil, err
		}
	}
	query, err := DocUpdateByIDSQL(d.cfg)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, query, id, content, store.HashContent(content)); err != nil {
		return nil, err
	}
	query, err = DocUpsertPathSQL(d.cfg)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, query, p, id, int64(len(content))); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &store.EditResponse{Path: p, Content: content, Replaced: count}, nil
}
