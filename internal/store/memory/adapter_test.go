package memory

import (
	"context"
	"testing"

	"github.com/austiecodes/rolio/internal/store"
	"github.com/austiecodes/rolio/internal/vfs"
)

var _ store.Adapter = (*Adapter)(nil)

func newAdapter(t *testing.T) *Adapter {
	t.Helper()

	tree, err := vfs.New([]vfs.File{
		{Path: "/docs/readme.md", Content: "# Readme\nhello rolio\n"},
		{Path: "/docs/main.go", Content: "package docs\n\ntype Adapter interface{}\n"},
	})
	if err != nil {
		t.Fatalf("vfs.New() error = %v", err)
	}
	return New(tree)
}

func TestAdapterDelegatesLS(t *testing.T) {
	adapter := newAdapter(t)

	resp, err := adapter.LS(context.Background(), store.LSRequest{Path: "/docs"})
	if err != nil {
		t.Fatalf("LS() error = %v", err)
	}
	if len(resp.Nodes) != 2 || resp.Nodes[0].Name != "main.go" || resp.Nodes[1].Name != "readme.md" {
		t.Fatalf("LS() = %+v, want sorted docs files", resp.Nodes)
	}
}

func TestAdapterDelegatesCatGrepFindTreeAndStat(t *testing.T) {
	adapter := newAdapter(t)

	cat, err := adapter.Cat(context.Background(), store.CatRequest{Path: "/docs/readme.md"})
	if err != nil {
		t.Fatalf("Cat() error = %v", err)
	}
	if cat.Path != "/docs/readme.md" || cat.Content != "# Readme\nhello rolio\n" {
		t.Fatalf("Cat() = %+v, want readme content", cat)
	}

	grep, err := adapter.Grep(context.Background(), store.GrepRequest{Path: "/", Pattern: "Adapter"})
	if err != nil {
		t.Fatalf("Grep() error = %v", err)
	}
	if len(grep.Matches) != 1 || grep.Matches[0].Path != "/docs/main.go" {
		t.Fatalf("Grep() = %+v, want Adapter match in main.go", grep.Matches)
	}

	find, err := adapter.Find(context.Background(), store.FindRequest{Path: "/", Name: "*.go"})
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if len(find.Nodes) != 1 || find.Nodes[0].Path != "/docs/main.go" {
		t.Fatalf("Find() = %+v, want main.go", find.Nodes)
	}

	tree, err := adapter.Tree(context.Background(), store.TreeRequest{Path: "/", Depth: 1})
	if err != nil {
		t.Fatalf("Tree() error = %v", err)
	}
	if tree.Root.Path != "/" || tree.Text != "/\n  docs/\n" {
		t.Fatalf("Tree() = %+v, want root tree depth 1", tree)
	}

	stat, err := adapter.Stat(context.Background(), store.StatRequest{Path: "/docs"})
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if stat.Node.Path != "/docs" || stat.Node.Kind != vfs.KindDir {
		t.Fatalf("Stat() = %+v, want /docs dir", stat.Node)
	}
}

func TestAdapterLSLimitOffset(t *testing.T) {
	tree, err := vfs.New([]vfs.File{
		{Path: "/docs/a.md", Content: "a"},
		{Path: "/docs/b.md", Content: "b"},
		{Path: "/docs/c.md", Content: "c"},
		{Path: "/docs/d.md", Content: "d"},
		{Path: "/docs/e.md", Content: "e"},
	})
	if err != nil {
		t.Fatalf("vfs.New() error = %v", err)
	}
	adapter := New(tree)

	// No limit/offset - returns all
	resp, err := adapter.LS(context.Background(), store.LSRequest{Path: "/docs"})
	if err != nil {
		t.Fatalf("LS() error = %v", err)
	}
	if len(resp.Nodes) != 5 {
		t.Fatalf("LS() got %d nodes, want 5", len(resp.Nodes))
	}
	if resp.Total != 5 {
		t.Fatalf("Total = %d, want 5", resp.Total)
	}

	// Limit 2
	resp, err = adapter.LS(context.Background(), store.LSRequest{Path: "/docs", Limit: 2})
	if err != nil {
		t.Fatalf("LS(limit=2) error = %v", err)
	}
	if len(resp.Nodes) != 2 {
		t.Fatalf("LS(limit=2) got %d nodes, want 2", len(resp.Nodes))
	}
	if resp.Total != 5 {
		t.Fatalf("Total = %d, want 5", resp.Total)
	}
	if resp.Nodes[0].Name != "a.md" || resp.Nodes[1].Name != "b.md" {
		t.Fatalf("LS(limit=2) nodes = %v, want first 2", resp.Nodes)
	}

	// Offset 3
	resp, err = adapter.LS(context.Background(), store.LSRequest{Path: "/docs", Offset: 3})
	if err != nil {
		t.Fatalf("LS(offset=3) error = %v", err)
	}
	if len(resp.Nodes) != 2 {
		t.Fatalf("LS(offset=3) got %d nodes, want 2", len(resp.Nodes))
	}
	if resp.Nodes[0].Name != "d.md" || resp.Nodes[1].Name != "e.md" {
		t.Fatalf("LS(offset=3) nodes = %v, want last 2", resp.Nodes)
	}

	// Limit 2, Offset 1
	resp, err = adapter.LS(context.Background(), store.LSRequest{Path: "/docs", Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("LS(limit=2,offset=1) error = %v", err)
	}
	if len(resp.Nodes) != 2 {
		t.Fatalf("LS(limit=2,offset=1) got %d nodes, want 2", len(resp.Nodes))
	}
	if resp.Nodes[0].Name != "b.md" || resp.Nodes[1].Name != "c.md" {
		t.Fatalf("LS(limit=2,offset=1) nodes = %v, want b,c", resp.Nodes)
	}

	// Offset beyond range
	resp, err = adapter.LS(context.Background(), store.LSRequest{Path: "/docs", Offset: 100})
	if err != nil {
		t.Fatalf("LS(offset=100) error = %v", err)
	}
	if len(resp.Nodes) != 0 {
		t.Fatalf("LS(offset=100) got %d nodes, want 0", len(resp.Nodes))
	}
}

func TestAdapterFindLimitOffset(t *testing.T) {
	tree, err := vfs.New([]vfs.File{
		{Path: "/src/a.go", Content: "a"},
		{Path: "/src/b.go", Content: "b"},
		{Path: "/src/c.go", Content: "c"},
		{Path: "/src/d.go", Content: "d"},
	})
	if err != nil {
		t.Fatalf("vfs.New() error = %v", err)
	}
	adapter := New(tree)

	// Find all .go files
	resp, err := adapter.Find(context.Background(), store.FindRequest{Path: "/src", Name: "*.go"})
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if len(resp.Nodes) != 4 {
		t.Fatalf("Find() got %d nodes, want 4", len(resp.Nodes))
	}
	if resp.Total != 4 {
		t.Fatalf("Total = %d, want 4", resp.Total)
	}

	// Find with limit
	resp, err = adapter.Find(context.Background(), store.FindRequest{Path: "/src", Name: "*.go", Limit: 2})
	if err != nil {
		t.Fatalf("Find(limit=2) error = %v", err)
	}
	if len(resp.Nodes) != 2 {
		t.Fatalf("Find(limit=2) got %d nodes, want 2", len(resp.Nodes))
	}
	if resp.Total != 4 {
		t.Fatalf("Total = %d, want 4", resp.Total)
	}

	// Find with offset
	resp, err = adapter.Find(context.Background(), store.FindRequest{Path: "/src", Name: "*.go", Offset: 2})
	if err != nil {
		t.Fatalf("Find(offset=2) error = %v", err)
	}
	if len(resp.Nodes) != 2 {
		t.Fatalf("Find(offset=2) got %d nodes, want 2", len(resp.Nodes))
	}
	if resp.Nodes[0].Name != "c.go" || resp.Nodes[1].Name != "d.go" {
		t.Fatalf("Find(offset=2) nodes = %v, want c,d", resp.Nodes)
	}
}
