package store

import (
	"context"
	"errors"
)

var (
	ErrNotFound         = errors.New("path not found")
	ErrIsDir            = errors.New("is a directory")
	ErrNotDir           = errors.New("not a directory")
	ErrContentNotReady  = errors.New("content not loaded")
	ErrEmptyOld         = errors.New("old string cannot be empty")
	ErrOldNotFound      = errors.New("old string not found")
	ErrCannotDeleteRoot = errors.New("cannot delete root")
	ErrEmptyQuery       = errors.New("search query cannot be empty")
	ErrInvalidParam     = errors.New("invalid parameter")
	ErrNotModified      = errors.New("not modified")
	ErrConflict         = errors.New("conflict: content hash mismatch")
	ErrNotSupported     = errors.New("operation not supported")
)

type Node struct {
	Path    string            `json:"path"`
	Name    string            `json:"name"`
	Kind    string            `json:"kind"`
	Size    int64             `json:"size,omitempty"`
	ModTime string            `json:"mod_time,omitempty"`
	Hash    string            `json:"hash,omitempty"` // Deferred: not populated by Stat yet; see Phase 3E
	Meta    map[string]string `json:"meta,omitempty"`
}

type Match struct {
	Path   string   `json:"path"`
	Line   int      `json:"line"`
	Text   string   `json:"text"`
	Before []string `json:"before,omitempty"`
	After  []string `json:"after,omitempty"`
}

type LSRequest struct {
	Path      string
	Sort      string // "name" (default), "size", "mtime"
	Reverse   bool
	Recursive bool
	All       bool // show hidden files (names starting with .)
	Limit     int  // max results (0 = unlimited)
	Offset    int  // skip first N results
}

type LSResponse struct {
	Nodes []Node `json:"nodes"`
	Total int    `json:"total,omitempty"` // total matching (pre-limit/offset)
}

type TreeRequest struct {
	Path      string
	Depth     int
	All       bool
	DirsOnly  bool
	FullPath  bool
	ShowSize  bool
	Sort      string // "name" (default), "size", "mtime"
	DirsFirst bool
}

type TreeResponse struct {
	Root Node   `json:"root"`
	Text string `json:"text"`
}

type CatRequest struct {
	Path        string
	IfNoneMatch string // optional: known hash; server returns 304 if content unchanged
}

type CatResponse struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Hash    string `json:"hash,omitempty"`
}

type GrepRequest struct {
	Path            string
	Pattern         string
	Regex           bool
	CaseInsensitive bool
	Invert          bool
	WholeWord       bool
	WholeLine       bool
	ContextBefore   int
	ContextAfter    int
	All             bool
	Include         string
	Exclude         string
}

type GrepResponse struct {
	Matches []Match `json:"matches"`
}

type FindRequest struct {
	Path     string
	Name     string
	Type     string // "file" or "" = files only, "dir" = dirs only
	MaxDepth int
	MinDepth int
	All      bool   // include hidden files
	IName    string // case-insensitive name glob (empty = use Name only)
	Limit    int    // max results (0 = unlimited)
	Offset   int    // skip first N results
}

type FindResponse struct {
	Nodes []Node `json:"nodes"`
	Total int    `json:"total,omitempty"` // total matching (pre-limit/offset)
}

type StatRequest struct {
	Path string
}

type StatResponse struct {
	Node Node `json:"node"`
}

type Lister interface {
	LS(context.Context, LSRequest) (*LSResponse, error)
}

type Treer interface {
	Tree(context.Context, TreeRequest) (*TreeResponse, error)
}

type Catter interface {
	Cat(context.Context, CatRequest) (*CatResponse, error)
}

type Grepper interface {
	Grep(context.Context, GrepRequest) (*GrepResponse, error)
}

type Finder interface {
	Find(context.Context, FindRequest) (*FindResponse, error)
}

type Statter interface {
	Stat(context.Context, StatRequest) (*StatResponse, error)
}

type PutRequest struct {
	Path         string
	Content      string
	ExpectedHash string // CAS: if set, reject update unless current hash matches; "*" means create-only (reject if exists)
}

type PutResponse struct {
	Node Node `json:"node"`
}

type DeleteRequest struct {
	Path         string
	ExpectedHash string // CAS: if set, reject delete unless current hash matches
}

type DeleteResponse struct{}

type EditRequest struct {
	Path         string
	Old          string
	New          string
	All          bool
	ExpectedHash string // CAS: if set, reject edit unless current hash matches
}

type EditResponse struct {
	Path     string `json:"path"`
	Replaced int    `json:"replaced"`
	Content  string `json:"content"`
}

type Adapter interface {
	Lister
	Treer
	Catter
	Grepper
	Finder
	Statter
	Writer
	Editor
	Searcher
	Globber
}

type Writer interface {
	Put(context.Context, PutRequest) (*PutResponse, error)
	Delete(context.Context, DeleteRequest) (*DeleteResponse, error)
}

type Editor interface {
	Edit(context.Context, EditRequest) (*EditResponse, error)
}

type SearchRequest struct {
	Query  string
	Path   string // scope to this path prefix, empty = whole tree
	Limit  int    // max results, default 20
	Offset int    // skip first N results
}

type SearchResult struct {
	Path    string  `json:"path"`
	Rank    float64 `json:"rank"`
	Snippet string  `json:"snippet"`
	Size    int64   `json:"size"`
	ModTime string  `json:"mod_time,omitempty"`
}

type SearchResponse struct {
	Results []SearchResult `json:"results"`
	Total   int            `json:"total"`
}

type Searcher interface {
	Search(ctx context.Context, req SearchRequest) (*SearchResponse, error)
}

// GlobRequest is the input for Glob — path discovery via glob pattern.
type GlobRequest struct {
	Pattern string // glob pattern like "**/*.md"
	Limit   int
	Offset  int
}

// GlobResult is a single path match returned by Glob.
type GlobResult struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time,omitempty"`
}

// GlobResponse is the output for Glob.
type GlobResponse struct {
	Results []GlobResult `json:"results"`
	Total   int          `json:"total"`
}

// Globber discovers file paths by glob pattern.
type Globber interface {
	Glob(ctx context.Context, req GlobRequest) (*GlobResponse, error)
}
