package postgres

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/austiecodes/rolio/internal/document"
	"github.com/austiecodes/rolio/internal/store"
	"github.com/austiecodes/rolio/internal/summary"
	"github.com/austiecodes/rolio/internal/vfs"
)

type Config struct {
	DSN       string
	Schema    string
	Language  string
	Generator summary.Generator
	// SummaryDelay is the time between a write and the generation of the
	// summaries that it made stale.
	SummaryDelay time.Duration
	// SummaryRetry is the time before the second attempt of a generation
	// that failed. Each subsequent attempt waits longer.
	SummaryRetry time.Duration
}
type DocAdapter struct {
	pool *pgxpool.Pool
	cfg  Config
	// summarizing is true while the workers of RunSummaries run.
	summarizing atomic.Bool
}

var _ store.Adapter = (*DocAdapter)(nil)

func NewDocAdapter(pool *pgxpool.Pool, cfg Config) *DocAdapter {
	return &DocAdapter{pool: pool, cfg: cfg}
}
func Connect(ctx context.Context, cfg Config) (*DocAdapter, error) {
	if cfg.Language != "zh" && cfg.Language != "en" {
		return nil, fmt.Errorf("language must be zh or en")
	}
	pool, err := pgxpool.New(ctx, cfg.DSN)
	if err != nil {
		return nil, err
	}
	statements, err := SchemaSQL(cfg)
	if err == nil {
		for _, s := range statements {
			if _, err = pool.Exec(ctx, s); err != nil {
				break
			}
		}
	}
	if err != nil {
		pool.Close()
		return nil, err
	}
	return NewDocAdapter(pool, cfg), nil
}
func (d *DocAdapter) Close() { d.pool.Close() }

// cleanDocPath normalizes a path to match vfs.Tree.cleanPath behavior.
func cleanDocPath(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return path.Clean(p)
}

// buildTree derives directories from the stored file paths.
func (d *DocAdapter) buildTree(ctx context.Context) (*vfs.Tree, error) {
	query, err := DocListPathsSQL(d.cfg)
	if err != nil {
		return nil, err
	}

	rows, err := d.pool.Query(ctx, query, "/%")
	if err != nil {
		return nil, fmt.Errorf("doc build tree query: %w", err)
	}
	defer rows.Close()

	var files []vfs.File
	for rows.Next() {
		var filePath string
		var size int64
		var mtime time.Time
		if err := rows.Scan(&filePath, &size, &mtime); err != nil {
			return nil, fmt.Errorf("doc build tree scan: %w", err)
		}
		files = append(files, vfs.File{
			Path:    filePath,
			Size:    size,
			ModTime: mtime.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("doc build tree rows: %w", err)
	}

	tree, err := vfs.New(files)
	if err != nil {
		return nil, fmt.Errorf("doc build vfs tree: %w", err)
	}
	return tree, nil
}

// --- Read methods ---

func (d *DocAdapter) LS(ctx context.Context, req store.LSRequest) (*store.LSResponse, error) {
	req.Path = cleanDocPath(req.Path)

	tree, err := d.buildTree(ctx)
	if err != nil {
		return nil, err
	}

	nodes, err := tree.LS(req.Path, vfs.LSOptions{
		Sort:      req.Sort,
		Reverse:   req.Reverse,
		Recursive: req.Recursive,
		All:       req.All,
	})
	if err != nil {
		return nil, err
	}

	total := len(nodes)
	nodes = paginateNodes(nodes, req.Limit, req.Offset)

	return &store.LSResponse{Nodes: nodes, Total: total}, nil
}

func (d *DocAdapter) Cat(ctx context.Context, req store.CatRequest) (*store.CatResponse, error) {
	req.Path = cleanDocPath(req.Path)
	if name := path.Base(req.Path); name == ".abstract.md" || name == ".overview.md" {
		return d.sidecar(ctx, req.Path)
	}

	query, err := DocCatSQL(d.cfg)
	if err != nil {
		return nil, err
	}

	var content, hash string
	if err := d.pool.QueryRow(ctx, query, req.Path).Scan(&content, &hash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("doc cat %s: %w", req.Path, store.ErrNotFound)
		}
		return nil, fmt.Errorf("doc cat %s: %w", req.Path, err)
	}

	doc, err := parseDocument(req.Path, content)
	if err != nil {
		// Legacy documents predate metadata validation. Always make their raw
		// source available so they can be exported or repaired through the UI.
		return &store.CatResponse{Path: req.Path, Content: content, Hash: hash, Body: content, MetadataError: err.Error()}, nil
	}
	return &store.CatResponse{Path: req.Path, Content: content, Hash: hash, Body: doc.Body, Metadata: doc.Metadata}, nil
}

func (d *DocAdapter) Stat(ctx context.Context, req store.StatRequest) (*store.StatResponse, error) {
	req.Path = cleanDocPath(req.Path)

	tree, err := d.buildTree(ctx)
	if err != nil {
		return nil, err
	}

	node, err := tree.Stat(req.Path)
	if err != nil {
		return nil, err
	}

	// For file nodes, enrich with hash from doc tables.
	if node.Kind == "file" {
		fileQuery, err := DocStatSQL(d.cfg)
		if err != nil {
			return nil, err
		}
		var filePath string
		var size int64
		var mtime time.Time
		var hash string
		if err := d.pool.QueryRow(ctx, fileQuery, req.Path).Scan(&filePath, &size, &mtime, &hash); err == nil {
			node.Hash = hash
		}
	}

	return &store.StatResponse{Node: node}, nil
}

func (d *DocAdapter) Find(ctx context.Context, req store.FindRequest) (*store.FindResponse, error) {
	req.Path = cleanDocPath(req.Path)

	tree, err := d.buildTree(ctx)
	if err != nil {
		return nil, err
	}

	nodes, err := tree.Find(req.Path, req.Name, vfs.FindOptions{
		Type:     req.Type,
		MaxDepth: req.MaxDepth,
		MinDepth: req.MinDepth,
		All:      req.All,
		IName:    req.IName,
	})
	if err != nil {
		return nil, err
	}

	total := len(nodes)
	nodes = paginateNodes(nodes, req.Limit, req.Offset)

	return &store.FindResponse{Nodes: nodes, Total: total}, nil
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
	req.Query = document.Tokens(req.Query)
	if req.Query == "" {
		return nil, store.ErrEmptyQuery
	}

	pathFilter := cleanDocPath(req.Path)
	if pathFilter == "/" {
		pathFilter = ""
	}

	countQuery, err := DocSearchCountSQL(d.cfg)
	if err != nil {
		return nil, err
	}

	var total int
	if err := d.pool.QueryRow(ctx, countQuery, req.Query, pathFilter).Scan(&total); err != nil {
		return nil, fmt.Errorf("doc search count: %w", err)
	}
	if total == 0 {
		return &store.SearchResponse{Total: 0}, nil
	}

	dataQuery, err := DocSearchDataSQL(d.cfg)
	if err != nil {
		return nil, err
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}

	rows, err := d.pool.Query(ctx, dataQuery, req.Query, pathFilter, limit, req.Offset)
	if err != nil {
		return nil, fmt.Errorf("doc search data: %w", err)
	}
	defer rows.Close()

	var results []store.SearchResult
	for rows.Next() {
		var filePath string
		var rank float64
		var snippet string
		var size int64
		var mtime time.Time
		if err := rows.Scan(&filePath, &rank, &snippet, &size, &mtime); err != nil {
			return nil, fmt.Errorf("doc search scan: %w", err)
		}
		results = append(results, store.SearchResult{
			Path:    filePath,
			Rank:    rank,
			Snippet: snippet,
			Size:    size,
			ModTime: mtime.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("doc search rows: %w", err)
	}

	return &store.SearchResponse{Results: results, Total: total}, nil
}

func (d *DocAdapter) Glob(ctx context.Context, req store.GlobRequest) (*store.GlobResponse, error) {
	if req.Pattern == "" {
		return nil, fmt.Errorf("%w: pattern is required", store.ErrInvalidParam)
	}

	regex, err := globToRegex(req.Pattern)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", store.ErrInvalidParam, err)
	}

	// DB paths have leading /; globToRegex assumes no leading /.
	// Add /? anchor so regex matches both /docs/x.md and docs/x.md.
	anchored := "^/?" + regex + "$"

	// Count total matches.
	countSQL, err := DocGlobCountSQL(d.cfg)
	if err != nil {
		return nil, err
	}
	var total int
	if err := d.pool.QueryRow(ctx, countSQL, anchored).Scan(&total); err != nil {
		return nil, fmt.Errorf("doc glob count: %w", err)
	}

	if total == 0 {
		return &store.GlobResponse{Total: 0}, nil
	}

	// Fetch results.
	var rows pgx.Rows
	if req.Limit > 0 {
		dataSQL, err := DocGlobDataSQL(d.cfg)
		if err != nil {
			return nil, err
		}
		rows, err = d.pool.Query(ctx, dataSQL, anchored, req.Limit, req.Offset)
		if err != nil {
			return nil, fmt.Errorf("doc glob query: %w", err)
		}
	} else {
		allSQL, err := DocGlobDataAllSQL(d.cfg)
		if err != nil {
			return nil, err
		}
		rows, err = d.pool.Query(ctx, allSQL, anchored, req.Offset)
		if err != nil {
			return nil, fmt.Errorf("doc glob query: %w", err)
		}
	}
	defer rows.Close()

	var results []store.GlobResult
	for rows.Next() {
		var filePath string
		var size int64
		var mtime time.Time
		if err := rows.Scan(&filePath, &size, &mtime); err != nil {
			return nil, fmt.Errorf("doc glob scan: %w", err)
		}
		// Normalize: strip leading / so output matches memory adapter.
		results = append(results, store.GlobResult{
			Path:    strings.TrimPrefix(filePath, "/"),
			Size:    size,
			ModTime: mtime.UTC().Format(time.RFC3339),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("doc glob rows: %w", err)
	}

	return &store.GlobResponse{Results: results, Total: total}, nil
}

func (d *DocAdapter) Grep(ctx context.Context, req store.GrepRequest) (*store.GrepResponse, error) {
	req.Path = cleanDocPath(req.Path)

	tree, err := d.buildTree(ctx)
	if err != nil {
		return nil, err
	}

	// Verify root exists.
	node, err := tree.Stat(req.Path)
	if err != nil {
		return nil, err
	}
	if node.Kind != "dir" {
		return nil, store.ErrNotDir
	}

	prefix := normalizePrefix(req.Path)
	query, err := DocStreamGrepSQL(d.cfg)
	if err != nil {
		return nil, err
	}

	rows, err := d.pool.Query(ctx, query, prefix)
	if err != nil {
		return nil, fmt.Errorf("doc grep query: %w", err)
	}
	defer rows.Close()

	// Compile regex if needed.
	var re *regexp.Regexp
	if req.Regex {
		pattern := req.Pattern
		if req.CaseInsensitive {
			pattern = "(?i)" + pattern
		}
		if req.WholeWord {
			pattern = `\b` + pattern + `\b`
		}
		if req.WholeLine {
			pattern = "^" + pattern + "$"
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid regex: %v", store.ErrInvalidParam, err)
		}
		re = compiled
	}

	var matches []store.Match
	for rows.Next() {
		var filePath, content string
		if err := rows.Scan(&filePath, &content); err != nil {
			return nil, fmt.Errorf("doc grep scan: %w", err)
		}

		// Hidden filter.
		if !req.All && pathHasHidden(filePath, prefix) {
			continue
		}

		// Include/exclude glob.
		if !globMatch(filePath, req.Include, req.Exclude) {
			continue
		}

		// Line-level matching.
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			if grepLineMatch(line, req.Pattern, re, req.CaseInsensitive, req.WholeWord, req.WholeLine) == req.Invert {
				continue
			}

			m := store.Match{
				Path: filePath,
				Line: i + 1,
				Text: line,
			}
			if req.ContextBefore > 0 {
				start := i - req.ContextBefore
				if start < 0 {
					start = 0
				}
				m.Before = copyLines(lines[start:i])
			}
			if req.ContextAfter > 0 {
				end := i + 1 + req.ContextAfter
				if end > len(lines) {
					end = len(lines)
				}
				m.After = copyLines(lines[i+1 : end])
			}
			matches = append(matches, m)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("doc grep rows: %w", err)
	}

	return &store.GrepResponse{Matches: matches}, nil
}

func (d *DocAdapter) Tree(ctx context.Context, req store.TreeRequest) (*store.TreeResponse, error) {
	req.Path = cleanDocPath(req.Path)

	tree, err := d.buildTree(ctx)
	if err != nil {
		return nil, err
	}

	text, err := tree.Tree(req.Path, req.Depth, vfs.TreeOptions{
		All:       req.All,
		DirsOnly:  req.DirsOnly,
		FullPath:  req.FullPath,
		ShowSize:  req.ShowSize,
		Sort:      req.Sort,
		DirsFirst: req.DirsFirst,
	})
	if err != nil {
		return nil, err
	}

	// Use Stat for Root node to match old adapter behavior exactly.
	root, err := tree.Stat(req.Path)
	if err != nil {
		return nil, err
	}

	return &store.TreeResponse{
		Root: root,
		Text: text,
	}, nil
}

func normalizePrefix(p string) string { return strings.TrimRight(p, "/") + "/" }
