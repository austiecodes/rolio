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

// parentSQL is the SQL expression for the parent directory of a path. The
// root has no parent.
func parentSQL(column string) string {
	return fmt.Sprintf("case when %[1]s='/' then '' else coalesce(nullif(regexp_replace(%[1]s,'/[^/]*$',''),''),'/') end", column)
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
 updated_at timestamptz not null default now()
 )`, docs), fmt.Sprintf(`create table if not exists %s (
 path text primary key,
 doc_id uuid not null unique references %s(id),
 size bigint not null,
 mtime timestamptz not null default now(),
 check (left(path,1)='/' and path <> '/')
 )`, paths, docs))

	summaries, err := quoteTable(cfg.Schema, "rolio_summaries")
	if err != nil {
		return nil, err
	}
	statements = append(statements,
		fmt.Sprintf("alter table %s add column if not exists metadata jsonb not null default '{}'", docs),
		fmt.Sprintf("alter table %s add column if not exists search_vector tsvector", docs),
		fmt.Sprintf("alter table %s add column if not exists search_language text not null default ''", docs),
		fmt.Sprintf("create index if not exists rolio_documents_context_search on %s using gin(search_vector)", docs),
		fmt.Sprintf(`create table if not exists %s (
   path text primary key,
   language text not null,
   source_hash text not null default '',
   abstract text not null default '',
   overview text not null default '',
   status text not null,
   error text not null default '',
   token text not null default '',
   updated_at timestamptz not null default now()
  )`, summaries),
		// A summary row is also a job of the summary queue: see summarize.go.
		fmt.Sprintf("alter table %s add column if not exists parent text not null default ''", summaries),
		fmt.Sprintf("alter table %s add column if not exists due timestamptz not null default now()", summaries),
		fmt.Sprintf("alter table %s add column if not exists attempts int not null default 0", summaries),
		fmt.Sprintf("update %s set parent=%s where parent='' and path<>'/'", summaries, parentSQL("path")),
		fmt.Sprintf("create index if not exists rolio_summaries_parent on %s(parent)", summaries),
		fmt.Sprintf("create index if not exists rolio_summaries_queue on %s(due) where status in ('stale','generating')", summaries),
		// The search vector of the abstract and the overview: see search.go.
		fmt.Sprintf("alter table %s add column if not exists search_vector tsvector", summaries),
		fmt.Sprintf("alter table %s add column if not exists search_language text not null default ''", summaries),
		fmt.Sprintf("create index if not exists rolio_summaries_search on %s using gin(search_vector)", summaries))
	return statements, nil
}
