package memory

import (
	"cmp"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/austiecodes/rolio/internal/document"
	"github.com/austiecodes/rolio/internal/store"
	"github.com/austiecodes/rolio/internal/vfs"
)

type Adapter struct {
	tree *vfs.Tree
}

var _ store.Adapter = (*Adapter)(nil)

func New(tree *vfs.Tree) *Adapter {
	return &Adapter{tree: tree}
}

func (a *Adapter) LS(_ context.Context, req store.LSRequest) (*store.LSResponse, error) {
	opts := vfs.LSOptions{
		Sort:      req.Sort,
		Reverse:   req.Reverse,
		Recursive: req.Recursive,
		All:       req.All,
	}
	nodes, err := a.tree.LS(req.Path, opts)
	if err != nil {
		return nil, err
	}
	return &store.LSResponse{Nodes: paginateNodes(nodes, req.Limit, req.Offset), Total: len(nodes)}, nil
}

func (a *Adapter) Tree(_ context.Context, req store.TreeRequest) (*store.TreeResponse, error) {
	root, err := a.tree.Stat(req.Path)
	if err != nil {
		return nil, err
	}
	opts := vfs.TreeOptions{
		All:       req.All,
		DirsOnly:  req.DirsOnly,
		FullPath:  req.FullPath,
		ShowSize:  req.ShowSize,
		Sort:      req.Sort,
		DirsFirst: req.DirsFirst,
	}
	text, err := a.tree.Tree(req.Path, req.Depth, opts)
	if err != nil {
		return nil, err
	}
	return &store.TreeResponse{Root: root, Text: text}, nil
}

func (a *Adapter) Cat(_ context.Context, req store.CatRequest) (*store.CatResponse, error) {
	content, err := a.tree.Cat(req.Path)
	if err != nil {
		return nil, err
	}
	return &store.CatResponse{Path: req.Path, Content: content, Hash: store.HashContent(content)}, nil
}

func (a *Adapter) Grep(_ context.Context, req store.GrepRequest) (*store.GrepResponse, error) {
	opts := vfs.GrepOptions{
		CaseInsensitive: req.CaseInsensitive,
		Invert:          req.Invert,
		WholeWord:       req.WholeWord,
		WholeLine:       req.WholeLine,
		ContextBefore:   req.ContextBefore,
		ContextAfter:    req.ContextAfter,
		All:             req.All,
		Include:         req.Include,
		Exclude:         req.Exclude,
	}
	matches, err := a.tree.Grep(req.Path, req.Pattern, req.Regex, opts)
	if err != nil {
		return nil, err
	}
	return &store.GrepResponse{Matches: matches}, nil
}

func (a *Adapter) Find(_ context.Context, req store.FindRequest) (*store.FindResponse, error) {
	opts := vfs.FindOptions{
		Type:     req.Type,
		MaxDepth: req.MaxDepth,
		MinDepth: req.MinDepth,
		All:      req.All,
		IName:    req.IName,
	}
	nodes, err := a.tree.Find(req.Path, req.Name, opts)
	if err != nil {
		return nil, err
	}
	return &store.FindResponse{Nodes: paginateNodes(nodes, req.Limit, req.Offset), Total: len(nodes)}, nil
}

func (a *Adapter) Stat(_ context.Context, req store.StatRequest) (*store.StatResponse, error) {
	node, err := a.tree.Stat(req.Path)
	if err != nil {
		return nil, err
	}
	return &store.StatResponse{Node: node}, nil
}

func (a *Adapter) Put(_ context.Context, req store.PutRequest) (*store.PutResponse, error) {
	a.tree.Put(req.Path, req.Content)
	node, err := a.tree.Stat(req.Path)
	if err != nil {
		return nil, err
	}
	return &store.PutResponse{Node: node}, nil
}

func (a *Adapter) Delete(_ context.Context, req store.DeleteRequest) (*store.DeleteResponse, error) {
	if err := a.tree.Delete(req.Path); err != nil {
		return nil, err
	}
	return &store.DeleteResponse{}, nil
}

func (a *Adapter) Edit(_ context.Context, req store.EditRequest) (*store.EditResponse, error) {
	replaced, err := a.tree.Edit(req.Path, req.Old, req.New, req.All)
	if err != nil {
		return nil, err
	}
	content, _ := a.tree.Cat(req.Path)
	return &store.EditResponse{Path: req.Path, Replaced: replaced, Content: content}, nil
}

// Search finds the documents that contain one or more of the query terms.
// Documents with more of the terms come first. A term agrees with a text when
// the text contains it: this adapter has no stemmer and no summaries.
func (a *Adapter) Search(_ context.Context, req store.SearchRequest) (*store.SearchResponse, error) {
	terms := document.QueryTerms(req.Query, 16)
	if len(terms) == 0 {
		return nil, store.ErrEmptyQuery
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	// matched returns the number of terms that a text contains.
	matched := func(text string) int {
		text = strings.ToLower(text)
		n := 0
		for _, term := range terms {
			if strings.Contains(text, term) {
				n++
			}
		}
		return n
	}

	nodes, err := a.tree.Find(req.Path, "*", vfs.FindOptions{Type: "file", All: true})
	if err != nil {
		return nil, err
	}
	results := []store.SearchResult{}
	for _, n := range nodes {
		content, err := a.tree.Cat(n.Path)
		if err != nil {
			continue
		}
		count := matched(content)
		if count == 0 {
			continue
		}
		body := content
		if doc, err := document.Parse(content); err == nil {
			body = doc.Body
		}
		var passages []string
		more := 0
		for _, line := range document.Passages(body, 480) {
			switch {
			case matched(line) == 0:
			case len(passages) < 3:
				passages = append(passages, line)
			default:
				more++
			}
		}
		results = append(results, store.SearchResult{
			Path:      n.Path,
			Rank:      float64(count) / float64(len(terms)),
			Passages:  passages,
			MoreLines: more,
			Snippet:   strings.Join(passages, "\n"),
			Size:      n.Size,
			ModTime:   n.ModTime,
		})
	}
	slices.SortStableFunc(results, func(x, y store.SearchResult) int { return cmp.Compare(y.Rank, x.Rank) })
	total := len(results)
	results = results[min(max(req.Offset, 0), total):]
	results = results[:min(limit, len(results))]
	return &store.SearchResponse{Results: results, Total: total}, nil
}

func (a *Adapter) Glob(_ context.Context, req store.GlobRequest) (*store.GlobResponse, error) {
	if req.Pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}

	regex, err := memoryGlobToRegex(req.Pattern)
	if err != nil {
		return nil, err
	}
	re, err := regexp.Compile("^" + regex + "$")
	if err != nil {
		return nil, err
	}

	allNodes, err := a.tree.Find("/", "", vfs.FindOptions{Type: "file", All: true})
	if err != nil {
		return nil, err
	}

	var results []store.GlobResult
	for _, n := range allNodes {
		// Strip leading / from path for matching (consistent with DB paths)
		p := strings.TrimPrefix(n.Path, "/")
		if re.MatchString(p) {
			results = append(results, store.GlobResult{
				Path:    p,
				Size:    n.Size,
				ModTime: n.ModTime,
			})
		}
	}

	total := len(results)
	if results == nil {
		results = []store.GlobResult{}
	}

	// Apply offset/limit
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > len(results) {
		offset = len(results)
	}
	results = results[offset:]
	if req.Limit > 0 && len(results) > req.Limit {
		results = results[:req.Limit]
	}

	return &store.GlobResponse{Results: results, Total: total}, nil
}

// memoryGlobToRegex converts a glob pattern to a regex.
// Supports: * (non-/), ? (single non-/), ** (any depth).
func memoryGlobToRegex(pattern string) (string, error) {
	pattern = strings.TrimPrefix(pattern, "./")
	pattern = strings.Trim(pattern, "/")
	if pattern == "" {
		return ".*", nil
	}

	var b strings.Builder
	i := 0
	for i < len(pattern) {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				if i+2 < len(pattern) && pattern[i+2] == '/' {
					b.WriteString("(.*/)?")
					i += 3
				} else {
					b.WriteString(".*")
					i += 2
				}
			} else {
				b.WriteString("[^/]*")
				i++
			}
		case '?':
			b.WriteString("[^/]")
			i++
		default:
			if strings.ContainsAny(string(pattern[i]), `\.+{}()[]^$|`) {
				b.WriteByte('\\')
			}
			b.WriteByte(pattern[i])
			i++
		}
	}
	return b.String(), nil
}

// paginateNodes applies limit/offset to a node slice.
func paginateNodes(nodes []store.Node, limit, offset int) []store.Node {
	if offset < 0 {
		offset = 0
	}
	if offset > len(nodes) {
		offset = len(nodes)
	}
	nodes = nodes[offset:]
	if limit > 0 && len(nodes) > limit {
		nodes = nodes[:limit]
	}
	return nodes
}
