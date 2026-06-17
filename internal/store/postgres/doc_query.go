package postgres

import "fmt"

// DocListPathsSQL returns a query that selects all file paths under a prefix
// from the document-centric tables.
func DocListPathsSQL(cfg Config) (string, error) {
	pathsTable, err := quoteTable(cfg.Schema, "rolio_paths")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"select path, size, mtime from %s where path like $1 order by path",
		pathsTable,
	), nil
}

// DocCatSQL returns a query that selects content and hash for a single file.
func DocCatSQL(cfg Config) (string, error) {
	pathsTable, err := quoteTable(cfg.Schema, "rolio_paths")
	if err != nil {
		return "", err
	}
	docsTable, err := quoteTable(cfg.Schema, "rolio_documents")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"select d.content, d.content_hash from %s rp join %s d on rp.doc_id = d.id where rp.path = $1",
		pathsTable, docsTable,
	), nil
}

// DocStatSQL returns a query that selects metadata for a single path.
func DocStatSQL(cfg Config) (string, error) {
	pathsTable, err := quoteTable(cfg.Schema, "rolio_paths")
	if err != nil {
		return "", err
	}
	docsTable, err := quoteTable(cfg.Schema, "rolio_documents")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"select rp.path, rp.size, rp.mtime, d.content_hash from %s rp join %s d on rp.doc_id = d.id where rp.path = $1",
		pathsTable, docsTable,
	), nil
}

// DocSearchCountSQL returns a query that counts full-text search results.
func DocSearchCountSQL(cfg Config) (string, error) {
	pathsTable, err := quoteTable(cfg.Schema, "rolio_paths")
	if err != nil {
		return "", err
	}
	docsTable, err := quoteTable(cfg.Schema, "rolio_documents")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"select count(*) from %s rp join %s d on rp.doc_id = d.id, "+
			"plainto_tsquery('english', $1) as query "+
			"where d.content_search @@ query "+
			"and ($2 = '' or rp.path = $2 or starts_with(rp.path, $2 || '/'))",
		pathsTable, docsTable,
	), nil
}

// DocSearchDataSQL returns a query that selects full-text search results with
// rank and snippet.
func DocSearchDataSQL(cfg Config) (string, error) {
	pathsTable, err := quoteTable(cfg.Schema, "rolio_paths")
	if err != nil {
		return "", err
	}
	docsTable, err := quoteTable(cfg.Schema, "rolio_documents")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"select rp.path, ts_rank_cd(d.content_search, query, 32) as rank, "+
			"ts_headline('english', d.content, query, 'StartSel=**,StopSel=**,MaxWords=50,MinWords=10') as snippet, "+
			"rp.size, rp.mtime "+
			"from %s rp join %s d on rp.doc_id = d.id, "+
			"plainto_tsquery('english', $1) as query "+
			"where d.content_search @@ query "+
			"and ($2 = '' or rp.path = $2 or starts_with(rp.path, $2 || '/')) "+
			"order by rank desc, rp.path limit $3 offset $4",
		pathsTable, docsTable,
	), nil
}

// DocStreamGrepSQL returns a query that streams (path, content) for grep.
func DocStreamGrepSQL(cfg Config) (string, error) {
	pathsTable, err := quoteTable(cfg.Schema, "rolio_paths")
	if err != nil {
		return "", err
	}
	docsTable, err := quoteTable(cfg.Schema, "rolio_documents")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"select rp.path, d.content from %s rp join %s d on rp.doc_id = d.id "+
			"where starts_with(rp.path, $1) "+
			"order by rp.path",
		pathsTable, docsTable,
	), nil
}

// --- Write SQL builders ---

// DocInsertSQL inserts a new doc row with content and hash, returning the doc ID.
// content_search is GENERATED and auto-updated.
func DocInsertSQL(cfg Config) (string, error) {
	docsTable, err := quoteTable(cfg.Schema, "rolio_documents")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"insert into %s(title, content, content_hash) values($1, $2, $3) returning id",
		docsTable,
	), nil
}

// DocUpdateByPathSQL updates the doc linked to a specific bound path.
// Used when Put overwrites an existing file — updates in-place to avoid orphans.
// content_search is GENERATED and auto-updated.
func DocUpdateByPathSQL(cfg Config) (string, error) {
	pathsTable, err := quoteTable(cfg.Schema, "rolio_paths")
	if err != nil {
		return "", err
	}
	docsTable, err := quoteTable(cfg.Schema, "rolio_documents")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"update %s d set content = $2, content_hash = $3, "+
			"title = $4, revision = revision + 1, updated_at = now() "+
			"from %s rp where rp.path = $1 and rp.doc_id = d.id",
		docsTable, pathsTable,
	), nil
}

// DocSelectForUpdateSQL locks the doc row for Edit's read-modify-write cycle.
func DocSelectForUpdateSQL(cfg Config) (string, error) {
	pathsTable, err := quoteTable(cfg.Schema, "rolio_paths")
	if err != nil {
		return "", err
	}
	docsTable, err := quoteTable(cfg.Schema, "rolio_documents")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"select d.id, d.content, d.content_hash from %s rp join %s d on rp.doc_id = d.id "+
			"where rp.path = $1 for update of d",
		pathsTable, docsTable,
	), nil
}

// DocUpdateByIDSQL updates a doc by its ID (used after FOR UPDATE).
func DocUpdateByIDSQL(cfg Config) (string, error) {
	docsTable, err := quoteTable(cfg.Schema, "rolio_documents")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"update %s set content = $2, content_hash = $3, revision = revision + 1, updated_at = now() "+
			"where id = $1",
		docsTable,
	), nil
}

// DocUpsertPathSQL inserts or updates a bound path row.
func DocUpsertPathSQL(cfg Config) (string, error) {
	pathsTable, err := quoteTable(cfg.Schema, "rolio_paths")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"insert into %s(path, doc_id, size, mtime) values($1, $2, $3, now()) "+
			"on conflict(path) do update set doc_id = excluded.doc_id, "+
			"size = excluded.size, mtime = excluded.mtime",
		pathsTable,
	), nil
}

// DocGlobCountSQL returns a query that counts paths matching a regex pattern.
func DocGlobCountSQL(cfg Config) (string, error) {
	pathsTable, err := quoteTable(cfg.Schema, "rolio_paths")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"select count(*) from %s where path ~ $1",
		pathsTable,
	), nil
}

// DocGlobDataSQL returns a query that selects paths matching a regex pattern
// with pagination (LIMIT $2 OFFSET $3).
func DocGlobDataSQL(cfg Config) (string, error) {
	pathsTable, err := quoteTable(cfg.Schema, "rolio_paths")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"select path, size, mtime from %s where path ~ $1 order by path limit $2 offset $3",
		pathsTable,
	), nil
}

// DocGlobDataAllSQL returns a query that selects all paths matching a regex pattern
// without LIMIT but with OFFSET support ($2).
func DocGlobDataAllSQL(cfg Config) (string, error) {
	pathsTable, err := quoteTable(cfg.Schema, "rolio_paths")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"select path, size, mtime from %s where path ~ $1 order by path offset $2",
		pathsTable,
	), nil
}
