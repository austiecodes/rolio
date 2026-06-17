package postgres

import (
	"fmt"
	"regexp"
)

var identPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func quoteIdent(s string) (string, error) {
	if !identPattern.MatchString(s) {
		return "", fmt.Errorf("unsafe identifier %q", s)
	}
	return `"` + s + `"`, nil
}
func quoteTable(schema, table string) (string, error) {
	t, err := quoteIdent(table)
	if err != nil {
		return "", err
	}
	if schema == "" {
		return t, nil
	}
	s, err := quoteIdent(schema)
	if err != nil {
		return "", err
	}
	return s + "." + t, nil
}

// SchemaSQL creates the single-tree store without modifying legacy tables.
func SchemaSQL(cfg Config) ([]string, error) {
	docs, err := quoteTable(cfg.Schema, "rolio_documents")
	if err != nil {
		return nil, err
	}
	paths, err := quoteTable(cfg.Schema, "rolio_paths")
	if err != nil {
		return nil, err
	}
	var statements []string
	if cfg.Schema != "" {
		schema, _ := quoteIdent(cfg.Schema)
		statements = append(statements, "create schema if not exists "+schema)
	}
	statements = append(statements, fmt.Sprintf(`create table if not exists %s (
 id uuid primary key default gen_random_uuid(),
 title text not null,
 content text not null,
 content_hash text not null,
 revision bigint not null default 1,
 updated_at timestamptz not null default now(),
 content_search tsvector generated always as (to_tsvector('english',content)) stored
 )`, docs), fmt.Sprintf(`create table if not exists %s (
 path text primary key,
 doc_id uuid not null unique references %s(id),
 size bigint not null,
 mtime timestamptz not null default now(),
 check (left(path,1)='/' and path <> '/')
 )`, paths, docs), fmt.Sprintf("create index if not exists rolio_documents_search on %s using gin(content_search)", docs))
	return statements, nil
}
