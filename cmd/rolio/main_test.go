package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/austiecodes/rolio/cmd/rolio/command"
	"github.com/austiecodes/rolio/internal/store"
	"github.com/austiecodes/rolio/internal/vfs"
)

type fakeClient struct {
	grepReq     store.GrepRequest
	grepMatches []store.Match
	lsNodes     []store.Node
	statNode    *store.Node
	statReq     store.StatRequest
	statErr     error
	catContent  string
	lsReq       store.LSRequest
	findReq     store.FindRequest
	findNodes   []store.Node
	treeReq     store.TreeRequest
	treeText    string
	catReqs     []store.CatRequest
	catMu       sync.Mutex
	catContents map[string]string
	putReqs     []store.PutRequest
	deleteReqs  []store.DeleteRequest
	searchReq   store.SearchRequest
	searchResp  *store.SearchResponse
}

func defaultLSNodes() []store.Node {
	return []store.Node{
		{Path: "/docs", Name: "docs", Kind: "dir"},
		{Path: "/readme.md", Name: "readme.md", Kind: "file"},
	}
}

func detailedLSNodes() []store.Node {
	return []store.Node{
		{Path: "/docs", Name: "docs", Kind: "dir", ModTime: "2025-01-01"},
		{Path: "/readme.md", Name: "readme.md", Kind: "file", Size: 4096, ModTime: "2025-01-02"},
	}
}

func sortLSNodes() []store.Node {
	return []store.Node{
		{Path: "/alpha.txt", Name: "alpha.txt", Kind: "file", Size: 100, ModTime: "2025-01-03"},
		{Path: "/beta.txt", Name: "beta.txt", Kind: "file", Size: 300, ModTime: "2025-01-01"},
		{Path: "/gamma.txt", Name: "gamma.txt", Kind: "file", Size: 200, ModTime: "2025-01-02"},
	}
}

func hiddenLSNodes() []store.Node {
	return []store.Node{
		{Path: "/docs", Name: "docs", Kind: "dir"},
		{Path: "/.hidden", Name: ".hidden", Kind: "file"},
		{Path: "/readme.md", Name: "readme.md", Kind: "file"},
	}
}

func (f *fakeClient) LS(_ context.Context, req store.LSRequest) (*store.LSResponse, error) {
	f.lsReq = req
	nodes := f.lsNodes
	if nodes == nil {
		nodes = defaultLSNodes()
	}
	nodes = vfs.SortNodesCopy(nodes, req.Sort, req.Reverse)
	return &store.LSResponse{Nodes: nodes, Total: len(nodes)}, nil
}

func (f *fakeClient) Tree(_ context.Context, req store.TreeRequest) (*store.TreeResponse, error) {
	f.treeReq = req
	text := f.treeText
	if text == "" {
		text = "/\n  docs/\n"
	}
	return &store.TreeResponse{Text: text}, nil
}

func (f *fakeClient) Cat(_ context.Context, req store.CatRequest) (*store.CatResponse, error) {
	f.catMu.Lock()
	f.catReqs = append(f.catReqs, req)
	f.catMu.Unlock()
	if f.catContents != nil {
		return &store.CatResponse{Path: req.Path, Content: f.catContents[req.Path]}, nil
	}
	content := f.catContent
	if content == "" {
		content = "# Readme\n"
	}
	return &store.CatResponse{Path: req.Path, Content: content}, nil
}

func (f *fakeClient) Grep(_ context.Context, req store.GrepRequest) (*store.GrepResponse, error) {
	f.grepReq = req
	matches := f.grepMatches
	if matches == nil {
		matches = []store.Match{
			{Path: "/go/store.go", Line: 12, Text: "type Adapter interface {"},
		}
	}
	return &store.GrepResponse{Matches: matches}, nil
}

func (f *fakeClient) Find(_ context.Context, req store.FindRequest) (*store.FindResponse, error) {
	f.findReq = req
	nodes := f.findNodes
	if nodes == nil {
		nodes = []store.Node{{Path: "/go/store.go", Name: "store.go", Kind: "file"}}
	}
	return &store.FindResponse{Nodes: nodes, Total: len(nodes)}, nil
}

func (f *fakeClient) Stat(_ context.Context, req store.StatRequest) (*store.StatResponse, error) {
	f.statReq = req
	if f.statErr != nil {
		return nil, f.statErr
	}
	node := store.Node{Path: "/docs", Name: "docs", Kind: "dir"}
	if f.statNode != nil {
		node = *f.statNode
	}
	return &store.StatResponse{Node: node}, nil
}

func (f *fakeClient) Put(_ context.Context, req store.PutRequest) (*store.PutResponse, error) {
	f.putReqs = append(f.putReqs, req)
	return &store.PutResponse{Node: store.Node{Path: req.Path, Name: filepath.Base(req.Path), Kind: "file", Size: int64(len(req.Content))}}, nil
}

func (f *fakeClient) Delete(_ context.Context, req store.DeleteRequest) (*store.DeleteResponse, error) {
	f.deleteReqs = append(f.deleteReqs, req)
	return &store.DeleteResponse{}, nil
}

func (f *fakeClient) Edit(_ context.Context, req store.EditRequest) (*store.EditResponse, error) {
	return &store.EditResponse{Path: req.Path, Replaced: 1}, nil
}

func (f *fakeClient) Search(_ context.Context, req store.SearchRequest) (*store.SearchResponse, error) {
	f.searchReq = req
	if f.searchResp != nil {
		return f.searchResp, nil
	}
	return &store.SearchResponse{
		Results: []store.SearchResult{
			{Path: "/docs/guide.md", Rank: 0.9, Snippet: "**test** result", Size: 100, ModTime: "2026-01-01"},
		},
		Total: 1,
	}, nil
}

func (f *fakeClient) Glob(_ context.Context, req store.GlobRequest) (*store.GlobResponse, error) {
	return &store.GlobResponse{Results: []store.GlobResult{
		{Path: "docs/readme.md", Size: 100, ModTime: "2026-01-01"},
		{Path: "docs/api.md", Size: 200, ModTime: "2026-01-02"},
	}, Total: 2}, nil
}

func execute(t *testing.T, args ...string) (string, *fakeClient) {
	t.Helper()

	client := &fakeClient{}
	cmd := newRootCommand(client)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute(%v) error = %v", args, err)
	}
	return out.String(), client
}

func executeWithNodes(t *testing.T, nodes []store.Node, args ...string) string {
	t.Helper()

	client := &fakeClient{lsNodes: nodes}
	cmd := newRootCommand(client)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute(%v) error = %v", args, err)
	}
	return out.String()
}

func executeWithClient(t *testing.T, client *fakeClient, args ...string) string {
	t.Helper()

	cmd := newRootCommand(client)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute(%v) error = %v", args, err)
	}
	return out.String()
}

func executeWithClientErr(client *fakeClient, args ...string) (string, error) {
	cmd := newRootCommand(client)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestLSOutputMarksDirectories(t *testing.T) {
	got, _ := execute(t, "ls", "/")
	want := "docs/\nreadme.md\n"
	if got != want {
		t.Fatalf("ls output = %q, want %q", got, want)
	}
}

func TestLSLongFormat(t *testing.T) {
	got := executeWithNodes(t, detailedLSNodes(), "ls", "-l", "/")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("ls -l line count = %d, want 2; output = %q", len(lines), got)
	}
	want0 := "drwx         -  2025-01-01  docs"
	if lines[0] != want0 {
		t.Fatalf("ls -l dir line = %q, want %q", lines[0], want0)
	}
	want1 := "-rw-      4096  2025-01-02  readme.md"
	if lines[1] != want1 {
		t.Fatalf("ls -l file line = %q, want %q", lines[1], want1)
	}
}

func TestLSClassify(t *testing.T) {
	got, _ := execute(t, "ls", "-F", "/")
	want := "docs/\nreadme.md\n"
	if got != want {
		t.Fatalf("ls -F output = %q, want %q", got, want)
	}
}

func TestLSSlashDir(t *testing.T) {
	got, _ := execute(t, "ls", "-p", "/")
	want := "docs/\nreadme.md\n"
	if got != want {
		t.Fatalf("ls -p output = %q, want %q", got, want)
	}
}

func TestLSLongClassify(t *testing.T) {
	got := executeWithNodes(t, detailedLSNodes(), "ls", "-l", "-F", "/")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("ls -lF line count = %d, want 2; output = %q", len(lines), got)
	}
	want0 := "drwx         -  2025-01-01  docs/"
	if lines[0] != want0 {
		t.Fatalf("ls -lF dir line = %q, want %q", lines[0], want0)
	}
	want1 := "-rw-      4096  2025-01-02  readme.md"
	if lines[1] != want1 {
		t.Fatalf("ls -lF file line = %q, want %q", lines[1], want1)
	}
}

func TestLSLongSlashDir(t *testing.T) {
	got := executeWithNodes(t, detailedLSNodes(), "ls", "-l", "-p", "/")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("ls -lp line count = %d, want 2; output = %q", len(lines), got)
	}
	want0 := "drwx         -  2025-01-01  docs/"
	if lines[0] != want0 {
		t.Fatalf("ls -lp dir line = %q, want %q", lines[0], want0)
	}
	want1 := "-rw-      4096  2025-01-02  readme.md"
	if lines[1] != want1 {
		t.Fatalf("ls -lp file line = %q, want %q", lines[1], want1)
	}
}

func TestFormatLSLine(t *testing.T) {
	tests := []struct {
		name     string
		node     store.Node
		long     bool
		classify bool
		slashDir bool
		want     string
	}{
		{
			name: "compact dir",
			node: store.Node{Name: "docs", Kind: "dir"},
			want: "docs/",
		},
		{
			name: "compact file",
			node: store.Node{Name: "readme.md", Kind: "file"},
			want: "readme.md",
		},
		{
			name: "long dir no flags",
			node: store.Node{Name: "docs", Kind: "dir", ModTime: "2025-01-01"},
			long: true,
			want: "drwx         -  2025-01-01  docs",
		},
		{
			name: "long file no flags",
			node: store.Node{Name: "readme.md", Kind: "file", Size: 4096, ModTime: "2025-01-02"},
			long: true,
			want: "-rw-      4096  2025-01-02  readme.md",
		},
		{
			name:     "long dir with classify",
			node:     store.Node{Name: "docs", Kind: "dir", ModTime: "2025-06-15"},
			long:     true,
			classify: true,
			want:     "drwx         -  2025-06-15  docs/",
		},
		{
			name:     "long dir with slashDir",
			node:     store.Node{Name: "docs", Kind: "dir", ModTime: "2025-06-15"},
			long:     true,
			slashDir: true,
			want:     "drwx         -  2025-06-15  docs/",
		},
		{
			name:     "long file with classify",
			node:     store.Node{Name: "main.go", Kind: "file", Size: 128, ModTime: "2025-03-10"},
			long:     true,
			classify: true,
			want:     "-rw-       128  2025-03-10  main.go",
		},
		{
			name: "long file no modtime",
			node: store.Node{Name: "notes.txt", Kind: "file", Size: 55},
			long: true,
			want: "-rw-        55  -  notes.txt",
		},
		{
			name: "long dir no modtime",
			node: store.Node{Name: "tmp", Kind: "dir"},
			long: true,
			want: "drwx         -  -  tmp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := command.FormatLSLine(tt.node, tt.long, tt.classify, tt.slashDir)
			if got != tt.want {
				t.Fatalf("command.FormatLSLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCatOutputsContent(t *testing.T) {
	got, _ := execute(t, "cat", "/readme.md")
	if got != "# Readme\n" {
		t.Fatalf("cat output = %q, want readme content", got)
	}
}

func TestGrepOutputAndRegexFlag(t *testing.T) {
	got, client := execute(t, "grep", "-E", "type Adapter", "/go")
	want := "/go/store.go:12:type Adapter interface {\n"
	if got != want {
		t.Fatalf("grep output = %q, want %q", got, want)
	}
	if !client.grepReq.Regex || client.grepReq.Pattern != "type Adapter" || client.grepReq.Path != "/go" {
		t.Fatalf("grep request = %+v, want regex request", client.grepReq)
	}
}

func TestRunHelpDoesNotRequireConfig(t *testing.T) {
	t.Setenv("ROLIO_CONFIG", "/path/that/does/not/exist")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run([]string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run(--help) code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "shared knowledge tree") {
		t.Fatalf("help output = %q, want ROLIO help", stdout.String())
	}
}

func TestLSSortByTime(t *testing.T) {
	c := &fakeClient{lsNodes: sortLSNodes()}
	got := executeWithClient(t, c, "ls", "-t", "/")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("ls -t line count = %d, want 3; output = %q", len(lines), got)
	}
	if lines[0] != "alpha.txt" {
		t.Fatalf("ls -t first = %q, want alpha.txt (newest)", lines[0])
	}
	if lines[1] != "gamma.txt" {
		t.Fatalf("ls -t second = %q, want gamma.txt", lines[1])
	}
	if lines[2] != "beta.txt" {
		t.Fatalf("ls -t third = %q, want beta.txt (oldest)", lines[2])
	}
	if c.lsReq.Sort != "mtime" || !c.lsReq.Reverse {
		t.Fatalf("ls -t request = %+v, want Sort=mtime Reverse=true", c.lsReq)
	}
}

func TestLSSortBySize(t *testing.T) {
	c := &fakeClient{lsNodes: sortLSNodes()}
	got := executeWithClient(t, c, "ls", "-S", "/")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("ls -S line count = %d, want 3; output = %q", len(lines), got)
	}
	if lines[0] != "beta.txt" {
		t.Fatalf("ls -S first = %q, want beta.txt (largest)", lines[0])
	}
	if lines[1] != "gamma.txt" {
		t.Fatalf("ls -S second = %q, want gamma.txt", lines[1])
	}
	if lines[2] != "alpha.txt" {
		t.Fatalf("ls -S third = %q, want alpha.txt (smallest)", lines[2])
	}
	if c.lsReq.Sort != "size" || !c.lsReq.Reverse {
		t.Fatalf("ls -S request = %+v, want Sort=size Reverse=true", c.lsReq)
	}
}

func TestLSReverseName(t *testing.T) {
	c := &fakeClient{lsNodes: sortLSNodes()}
	got := executeWithClient(t, c, "ls", "-r", "/")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("ls -r line count = %d, want 3; output = %q", len(lines), got)
	}
	if lines[0] != "gamma.txt" {
		t.Fatalf("ls -r first = %q, want gamma.txt (reversed alpha)", lines[0])
	}
	if lines[2] != "alpha.txt" {
		t.Fatalf("ls -r last = %q, want alpha.txt (reversed gamma)", lines[2])
	}
	if c.lsReq.Sort != "name" || !c.lsReq.Reverse {
		t.Fatalf("ls -r request = %+v, want Sort=name Reverse=true", c.lsReq)
	}
}

func TestLSSortByTimeReverse(t *testing.T) {
	c := &fakeClient{lsNodes: sortLSNodes()}
	got := executeWithClient(t, c, "ls", "-t", "-r", "/")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("ls -tr line count = %d, want 3; output = %q", len(lines), got)
	}
	if lines[0] != "beta.txt" {
		t.Fatalf("ls -tr first = %q, want beta.txt (oldest first)", lines[0])
	}
	if lines[2] != "alpha.txt" {
		t.Fatalf("ls -tr last = %q, want alpha.txt (newest last)", lines[2])
	}
	if c.lsReq.Sort != "mtime" || c.lsReq.Reverse {
		t.Fatalf("ls -tr request = %+v, want Sort=mtime Reverse=false", c.lsReq)
	}
}

func TestLSSortBySizeReverse(t *testing.T) {
	c := &fakeClient{lsNodes: sortLSNodes()}
	got := executeWithClient(t, c, "ls", "-S", "-r", "/")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("ls -Sr line count = %d, want 3; output = %q", len(lines), got)
	}
	if lines[0] != "alpha.txt" {
		t.Fatalf("ls -Sr first = %q, want alpha.txt (smallest first)", lines[0])
	}
	if lines[2] != "beta.txt" {
		t.Fatalf("ls -Sr last = %q, want beta.txt (largest last)", lines[2])
	}
}

func TestLSRecursiveFlag(t *testing.T) {
	c := &fakeClient{lsNodes: defaultLSNodes()}
	executeWithClient(t, c, "ls", "-R", "/")
	if !c.lsReq.Recursive {
		t.Fatalf("ls -R request = %+v, want Recursive=true", c.lsReq)
	}
}

func TestLSRecursiveLongFormat(t *testing.T) {
	c := &fakeClient{lsNodes: detailedLSNodes()}
	got := executeWithClient(t, c, "ls", "-l", "-R", "/")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("ls -lR line count = %d, want 2; output = %q", len(lines), got)
	}
	if !c.lsReq.Recursive {
		t.Fatalf("ls -lR request = %+v, want Recursive=true", c.lsReq)
	}
	if !strings.Contains(lines[0], "drwx") {
		t.Fatalf("ls -lR dir line = %q, want long format", lines[0])
	}
}

func TestLSAllFlag(t *testing.T) {
	c := &fakeClient{lsNodes: hiddenLSNodes()}
	got := executeWithClient(t, c, "ls", "-a", "/")
	if !c.lsReq.All {
		t.Fatalf("ls -a request = %+v, want All=true", c.lsReq)
	}
	if !strings.Contains(got, ".hidden") {
		t.Fatalf("ls -a output = %q, want .hidden included", got)
	}
	if !strings.Contains(got, "docs") {
		t.Fatalf("ls -a output = %q, want docs included", got)
	}
	if !strings.Contains(got, "readme.md") {
		t.Fatalf("ls -a output = %q, want readme.md included", got)
	}
}

func TestLSAllLongFormat(t *testing.T) {
	c := &fakeClient{lsNodes: hiddenLSNodes()}
	got := executeWithClient(t, c, "ls", "-l", "-a", "/")
	if !c.lsReq.All {
		t.Fatalf("ls -la request = %+v, want All=true", c.lsReq)
	}
	if !strings.Contains(got, ".hidden") {
		t.Fatalf("ls -la output = %q, want .hidden included", got)
	}
}

func TestLSDirectoryFlag(t *testing.T) {
	c := &fakeClient{statNode: &store.Node{Path: "/docs", Name: "docs", Kind: "dir"}}
	got := executeWithClient(t, c, "ls", "-d", "/docs")
	if got != "docs/\n" {
		t.Fatalf("ls -d output = %q, want docs/", got)
	}
}

func TestLSDirectoryLongFormat(t *testing.T) {
	c := &fakeClient{statNode: &store.Node{Path: "/docs", Name: "docs", Kind: "dir", ModTime: "2025-01-01"}}
	got := executeWithClient(t, c, "ls", "-d", "-l", "/docs")
	want := "drwx         -  2025-01-01  docs\n"
	if got != want {
		t.Fatalf("ls -dl output = %q, want %q", got, want)
	}
}

func TestLSDirectoryClassify(t *testing.T) {
	c := &fakeClient{statNode: &store.Node{Path: "/docs", Name: "docs", Kind: "dir"}}
	got := executeWithClient(t, c, "ls", "-d", "-F", "/docs")
	want := "docs/\n"
	if got != want {
		t.Fatalf("ls -dF output = %q, want %q", got, want)
	}
}

func TestLSDirectorySlashDir(t *testing.T) {
	c := &fakeClient{statNode: &store.Node{Path: "/docs", Name: "docs", Kind: "dir"}}
	got := executeWithClient(t, c, "ls", "-d", "-p", "/docs")
	want := "docs/\n"
	if got != want {
		t.Fatalf("ls -dp output = %q, want %q", got, want)
	}
}

func TestLSLongSortByTime(t *testing.T) {
	c := &fakeClient{lsNodes: sortLSNodes()}
	got := executeWithClient(t, c, "ls", "-l", "-t", "/")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("ls -lt line count = %d, want 3; output = %q", len(lines), got)
	}
	if !strings.Contains(lines[0], "alpha.txt") {
		t.Fatalf("ls -lt first = %q, want alpha.txt (newest)", lines[0])
	}
	if !strings.Contains(lines[0], "2025-01-03") {
		t.Fatalf("ls -lt first = %q, want mtime column", lines[0])
	}
}

func TestGrepDefaultOutput(t *testing.T) {
	c := &fakeClient{grepMatches: multiFileMatches()}
	got := executeWithClient(t, c, "grep", "ROLIO")
	want := "/docs/readme.md:1:Welcome to ROLIO, the ROLIO project\n" +
		"/docs/readme.md:3:ROLIO is a virtual filesystem\n" +
		"/docs/readme.md:5:Learn more about ROLIO today\n" +
		"/src/main.go:10:// ROLIO entry point\n"
	if got != want {
		t.Fatalf("grep output = %q, want %q", got, want)
	}
}

func TestGrepCount(t *testing.T) {
	c := &fakeClient{grepMatches: multiFileMatches()}
	got := executeWithClient(t, c, "grep", "-c", "ROLIO")
	want := "/docs/readme.md:3\n/src/main.go:1\n"
	if got != want {
		t.Fatalf("grep -c output = %q, want %q", got, want)
	}
}

func TestGrepFilesOnly(t *testing.T) {
	c := &fakeClient{grepMatches: multiFileMatches()}
	got := executeWithClient(t, c, "grep", "-l", "ROLIO")
	want := "/docs/readme.md\n/src/main.go\n"
	if got != want {
		t.Fatalf("grep -l output = %q, want %q", got, want)
	}
}

func TestGrepOnlyMatching(t *testing.T) {
	c := &fakeClient{grepMatches: []store.Match{
		{Path: "/docs/readme.md", Line: 1, Text: "Welcome to ROLIO, the ROLIO project"},
		{Path: "/src/main.go", Line: 10, Text: "// ROLIO entry point"},
	}}
	got := executeWithClient(t, c, "grep", "-o", "ROLIO")
	want := "/docs/readme.md:1:ROLIO\n" +
		"/docs/readme.md:1:ROLIO\n" +
		"/src/main.go:10:ROLIO\n"
	if got != want {
		t.Fatalf("grep -o output = %q, want %q", got, want)
	}
}

func TestGrepOnlyMatchingRegexFallsBackToFullLine(t *testing.T) {
	c := &fakeClient{grepMatches: []store.Match{
		{Path: "/src/main.go", Line: 10, Text: "// ROLIO entry point"},
	}}
	got := executeWithClient(t, c, "grep", "-o", "-E", "ROLIO")
	want := "/src/main.go:10:// ROLIO entry point\n"
	if got != want {
		t.Fatalf("grep -oE output = %q, want %q", got, want)
	}
}

func TestGrepFilesOverridesCount(t *testing.T) {
	c := &fakeClient{grepMatches: multiFileMatches()}
	got := executeWithClient(t, c, "grep", "-l", "-c", "ROLIO")
	want := "/docs/readme.md\n/src/main.go\n"
	if got != want {
		t.Fatalf("grep -lc output = %q, want %q (files-only takes priority)", got, want)
	}
}

func TestGrepCaseInsensitive(t *testing.T) {
	c := &fakeClient{grepMatches: multiFileMatches()}
	executeWithClient(t, c, "grep", "-i", "rolio")
	if !c.grepReq.CaseInsensitive {
		t.Fatalf("grep -i request = %+v, want CaseInsensitive=true", c.grepReq)
	}
}

func TestGrepInvertMatch(t *testing.T) {
	c := &fakeClient{grepMatches: multiFileMatches()}
	executeWithClient(t, c, "grep", "-v", "ROLIO")
	if !c.grepReq.Invert {
		t.Fatalf("grep -v request = %+v, want Invert=true", c.grepReq)
	}
}

func TestGrepWholeWord(t *testing.T) {
	c := &fakeClient{grepMatches: multiFileMatches()}
	executeWithClient(t, c, "grep", "-w", "ROLIO")
	if !c.grepReq.WholeWord {
		t.Fatalf("grep -w request = %+v, want WholeWord=true", c.grepReq)
	}
}

func TestGrepWholeLine(t *testing.T) {
	c := &fakeClient{grepMatches: multiFileMatches()}
	executeWithClient(t, c, "grep", "-x", "ROLIO is a virtual filesystem")
	if !c.grepReq.WholeLine {
		t.Fatalf("grep -x request = %+v, want WholeLine=true", c.grepReq)
	}
}

func TestGrepAfterContext(t *testing.T) {
	c := &fakeClient{grepMatches: []store.Match{
		{Path: "/a.txt", Line: 5, Text: "match", After: []string{"after1", "after2"}},
	}}
	got := executeWithClient(t, c, "grep", "-A", "2", "match")
	want := "/a.txt:5:match\n/a.txt-after1\n/a.txt-after2\n"
	if got != want {
		t.Fatalf("grep -A 2 output = %q, want %q", got, want)
	}
	if c.grepReq.ContextAfter != 2 {
		t.Fatalf("grep -A 2 request = %+v, want ContextAfter=2", c.grepReq)
	}
}

func TestGrepBeforeContext(t *testing.T) {
	c := &fakeClient{grepMatches: []store.Match{
		{Path: "/a.txt", Line: 5, Text: "match", Before: []string{"before1"}},
	}}
	got := executeWithClient(t, c, "grep", "-B", "1", "match")
	want := "/a.txt-before1\n/a.txt:5:match\n"
	if got != want {
		t.Fatalf("grep -B 1 output = %q, want %q", got, want)
	}
	if c.grepReq.ContextBefore != 1 {
		t.Fatalf("grep -B 1 request = %+v, want ContextBefore=1", c.grepReq)
	}
}

func TestGrepContextBoth(t *testing.T) {
	c := &fakeClient{grepMatches: []store.Match{
		{Path: "/a.txt", Line: 5, Text: "match", Before: []string{"b1", "b2"}, After: []string{"a1", "a2"}},
	}}
	got := executeWithClient(t, c, "grep", "-C", "2", "match")
	want := "/a.txt-b1\n/a.txt-b2\n/a.txt:5:match\n/a.txt-a1\n/a.txt-a2\n"
	if got != want {
		t.Fatalf("grep -C 2 output = %q, want %q", got, want)
	}
	if c.grepReq.ContextBefore != 2 {
		t.Fatalf("grep -C 2 request = %+v, want ContextBefore=2", c.grepReq)
	}
	if c.grepReq.ContextAfter != 2 {
		t.Fatalf("grep -C 2 request = %+v, want ContextAfter=2", c.grepReq)
	}
}

func TestGrepContextDoesNotOverrideExplicit(t *testing.T) {
	c := &fakeClient{grepMatches: []store.Match{}}
	executeWithClient(t, c, "grep", "-C", "2", "-B", "1", "match")
	if c.grepReq.ContextBefore != 1 {
		t.Fatalf("grep -C2 -B1 request = %+v, want ContextBefore=1 (explicit B wins)", c.grepReq)
	}
	if c.grepReq.ContextAfter != 2 {
		t.Fatalf("grep -C2 -B1 request = %+v, want ContextAfter=2 (from C)", c.grepReq)
	}
}

func TestGrepContextDoesNotOverrideExplicitZero(t *testing.T) {
	c := &fakeClient{grepMatches: []store.Match{}}
	executeWithClient(t, c, "grep", "-C", "2", "-A", "0", "match")
	if c.grepReq.ContextBefore != 2 {
		t.Fatalf("grep -C2 -A0 request = %+v, want ContextBefore=2", c.grepReq)
	}
	if c.grepReq.ContextAfter != 0 {
		t.Fatalf("grep -C2 -A0 request = %+v, want ContextAfter=0 (explicit A wins)", c.grepReq)
	}
}

func TestGrepAllFiles(t *testing.T) {
	c := &fakeClient{grepMatches: multiFileMatches()}
	executeWithClient(t, c, "grep", "-a", "ROLIO")
	if !c.grepReq.All {
		t.Fatalf("grep -a request = %+v, want All=true", c.grepReq)
	}
}

func TestGrepInclude(t *testing.T) {
	c := &fakeClient{grepMatches: multiFileMatches()}
	executeWithClient(t, c, "grep", "--include", "*.md", "ROLIO")
	if c.grepReq.Include != "*.md" {
		t.Fatalf("grep --include request = %+v, want Include=*.md", c.grepReq)
	}
}

func TestGrepExclude(t *testing.T) {
	c := &fakeClient{grepMatches: multiFileMatches()}
	executeWithClient(t, c, "grep", "--exclude", "*.log", "ROLIO")
	if c.grepReq.Exclude != "*.log" {
		t.Fatalf("grep --exclude request = %+v, want Exclude=*.log", c.grepReq)
	}
}

func TestGrepCaseInsensitiveWholeWord(t *testing.T) {
	c := &fakeClient{grepMatches: multiFileMatches()}
	executeWithClient(t, c, "grep", "-i", "-w", "ROLIO")
	if !c.grepReq.CaseInsensitive {
		t.Fatalf("grep -iw request = %+v, want CaseInsensitive=true", c.grepReq)
	}
	if !c.grepReq.WholeWord {
		t.Fatalf("grep -iw request = %+v, want WholeWord=true", c.grepReq)
	}
}

func TestGrepRegexCaseInsensitive(t *testing.T) {
	c := &fakeClient{grepMatches: multiFileMatches()}
	executeWithClient(t, c, "grep", "-E", "-i", "rolio.*virtual")
	if !c.grepReq.Regex {
		t.Fatalf("grep -Ei request = %+v, want Regex=true", c.grepReq)
	}
	if !c.grepReq.CaseInsensitive {
		t.Fatalf("grep -Ei request = %+v, want CaseInsensitive=true", c.grepReq)
	}
}

func TestGrepInvertCount(t *testing.T) {
	c := &fakeClient{grepMatches: multiFileMatches()}
	got := executeWithClient(t, c, "grep", "-v", "-c", "ROLIO")
	want := "/docs/readme.md:3\n/src/main.go:1\n"
	if got != want {
		t.Fatalf("grep -vc output = %q, want %q", got, want)
	}
	if !c.grepReq.Invert {
		t.Fatalf("grep -vc request = %+v, want Invert=true", c.grepReq)
	}
}

func TestGrepFilesOnlyAllFiles(t *testing.T) {
	c := &fakeClient{grepMatches: multiFileMatches()}
	got := executeWithClient(t, c, "grep", "-l", "-a", "ROLIO")
	if !c.grepReq.All {
		t.Fatalf("grep -la request = %+v, want All=true", c.grepReq)
	}
	// Verify files-only output still works when combined with -a
	want := "/docs/readme.md\n/src/main.go\n"
	if got != want {
		t.Fatalf("grep -la output = %q, want %q", got, want)
	}
}

func TestGrepContextOutputFormat(t *testing.T) {
	c := &fakeClient{grepMatches: []store.Match{
		{Path: "/readme.md", Line: 10, Text: "main match", Before: []string{"line 8", "line 9"}, After: []string{"line 11"}},
		{Path: "/readme.md", Line: 20, Text: "another match"},
	}}
	got := executeWithClient(t, c, "grep", "-C", "2", "match")
	want := "/readme.md-line 8\n/readme.md-line 9\n/readme.md:10:main match\n/readme.md-line 11\n" +
		"/readme.md:20:another match\n"
	if got != want {
		t.Fatalf("grep -C 2 context output = %q, want %q", got, want)
	}
}

func TestCatNumberAll(t *testing.T) {
	c := &fakeClient{catContent: "line one\nline two\n\nline four\n"}
	got := executeWithClient(t, c, "cat", "-n", "/readme.md")
	want := "     1  line one\n     2  line two\n     3  \n     4  line four\n"
	if got != want {
		t.Fatalf("cat -n output = %q, want %q", got, want)
	}
}

func TestCatNumberNonBlank(t *testing.T) {
	c := &fakeClient{catContent: "line one\nline two\n\nline four\n"}
	got := executeWithClient(t, c, "cat", "-b", "/readme.md")
	want := "     1  line one\n     2  line two\n\n     3  line four\n"
	if got != want {
		t.Fatalf("cat -b output = %q, want %q", got, want)
	}
}

func TestCatSqueezeBlank(t *testing.T) {
	c := &fakeClient{catContent: "line one\n\n\n\nline two\n"}
	got := executeWithClient(t, c, "cat", "-s", "/readme.md")
	want := "line one\n\nline two\n"
	if got != want {
		t.Fatalf("cat -s output = %q, want %q", got, want)
	}
}

func TestCatNumberAllSqueeze(t *testing.T) {
	c := &fakeClient{catContent: "line one\n\n\n\nline two\n"}
	got := executeWithClient(t, c, "cat", "-n", "-s", "/readme.md")
	want := "     1  line one\n     2  \n     3  line two\n"
	if got != want {
		t.Fatalf("cat -n -s output = %q, want %q", got, want)
	}
}

func TestCatNumberNonBlankSqueeze(t *testing.T) {
	c := &fakeClient{catContent: "line one\n\n\n\nline two\n"}
	got := executeWithClient(t, c, "cat", "-b", "-s", "/readme.md")
	want := "     1  line one\n\n     2  line two\n"
	if got != want {
		t.Fatalf("cat -b -s output = %q, want %q", got, want)
	}
}

func TestCatNumberAllNonBlankBwins(t *testing.T) {
	c := &fakeClient{catContent: "line one\nline two\n\nline four\n"}
	got := executeWithClient(t, c, "cat", "-n", "-b", "/readme.md")
	want := "     1  line one\n     2  line two\n\n     3  line four\n"
	if got != want {
		t.Fatalf("cat -n -b output = %q, want %q (non-blank only)", got, want)
	}
}

func TestStatDefaultShowsAllFields(t *testing.T) {
	c := &fakeClient{statNode: &store.Node{
		Path: "/docs/readme.md", Name: "readme.md", Kind: "file",
		Size: 33, ModTime: "2026-05-02T14:00:00Z",
		Meta: map[string]string{"key1": "value1", "key2": "value2"},
	}}
	got := executeWithClient(t, c, "stat", "/docs/readme.md")
	if !strings.Contains(got, "Path: /docs/readme.md") {
		t.Fatalf("stat output missing Path: %q", got)
	}
	if !strings.Contains(got, "Name: readme.md") {
		t.Fatalf("stat output missing Name: %q", got)
	}
	if !strings.Contains(got, "Kind: file") {
		t.Fatalf("stat output missing Kind: %q", got)
	}
	if !strings.Contains(got, "Size: 33") {
		t.Fatalf("stat output missing Size: %q", got)
	}
	if !strings.Contains(got, "ModTime: 2026-05-02T14:00:00Z") {
		t.Fatalf("stat output missing ModTime: %q", got)
	}
	if !strings.Contains(got, "Meta:") {
		t.Fatalf("stat output missing Meta: %q", got)
	}
}

func TestStatEmptyModTimeAndMeta(t *testing.T) {
	c := &fakeClient{statNode: &store.Node{
		Path: "/docs/readme.md", Name: "readme.md", Kind: "file", Size: 10,
	}}
	got := executeWithClient(t, c, "stat", "/docs/readme.md")
	if !strings.Contains(got, "ModTime: -") {
		t.Fatalf("stat output with empty ModTime = %q, want '-'", got)
	}
	if strings.Contains(got, "Meta:") {
		t.Fatalf("stat output with empty Meta should not contain Meta: %q", got)
	}
}

func TestStatCustomFormat(t *testing.T) {
	c := &fakeClient{statNode: &store.Node{
		Path: "/docs/readme.md", Name: "readme.md", Kind: "file",
		Size: 33, ModTime: "2026-05-02T14:00:00Z",
	}}
	got := executeWithClient(t, c, "stat", "-c", "%p %s %y", "/docs/readme.md")
	want := "/docs/readme.md 33 2026-05-02T14:00:00Z\n"
	if got != want {
		t.Fatalf("stat -c output = %q, want %q", got, want)
	}
}

func TestStatCustomFormatLiteralPercent(t *testing.T) {
	c := &fakeClient{statNode: &store.Node{
		Path: "/docs/readme.md", Name: "readme.md", Kind: "file", Size: 10,
	}}
	got := executeWithClient(t, c, "stat", "-c", "%%p", "/docs/readme.md")
	want := "%p\n"
	if got != want {
		t.Fatalf("stat -c %%p output = %q, want %q", got, want)
	}
}

func TestStatTerseFormat(t *testing.T) {
	c := &fakeClient{statNode: &store.Node{
		Path: "/docs/readme.md", Name: "readme.md", Kind: "file",
		Size: 33, ModTime: "2026-05-02T14:00:00Z",
	}}
	got := executeWithClient(t, c, "stat", "-f", "/docs/readme.md")
	want := "/docs/readme.md\tfile\t33\t2026-05-02T14:00:00Z\n"
	if got != want {
		t.Fatalf("stat -f output = %q, want %q", got, want)
	}
}

func TestStatTerseEmptyModTime(t *testing.T) {
	c := &fakeClient{statNode: &store.Node{
		Path: "/docs/readme.md", Name: "readme.md", Kind: "file", Size: 10,
	}}
	got := executeWithClient(t, c, "stat", "-f", "/docs/readme.md")
	want := "/docs/readme.md\tfile\t10\t-\n"
	if got != want {
		t.Fatalf("stat -f with empty ModTime = %q, want %q", got, want)
	}
}

func TestFindBasicName(t *testing.T) {
	c := &fakeClient{findNodes: findMixedNodes()}
	got := executeWithClient(t, c, "find", "--name", "*.go")
	if c.findReq.Name != "*.go" {
		t.Fatalf("find --name request = %+v, want Name=*.go", c.findReq)
	}
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("find --name output = %q, want 5 lines", got)
	}
}

func TestFindTypeFile(t *testing.T) {
	c := &fakeClient{findNodes: findMixedNodes()}
	executeWithClient(t, c, "find", "--name", "*", "-t", "f")
	if c.findReq.Type != "file" {
		t.Fatalf("find -t f request = %+v, want Type=file", c.findReq)
	}
}

func TestFindTypeDir(t *testing.T) {
	c := &fakeClient{findNodes: findMixedNodes()}
	executeWithClient(t, c, "find", "--name", "*", "-t", "d")
	if c.findReq.Type != "dir" {
		t.Fatalf("find -t d request = %+v, want Type=dir", c.findReq)
	}
}

func TestFindMaxDepth(t *testing.T) {
	c := &fakeClient{findNodes: findMixedNodes()}
	executeWithClient(t, c, "find", "--name", "*.go", "--maxdepth", "2")
	if c.findReq.MaxDepth != 2 {
		t.Fatalf("find --maxdepth 2 request = %+v, want MaxDepth=2", c.findReq)
	}
}

func TestFindMinDepth(t *testing.T) {
	c := &fakeClient{findNodes: findMixedNodes()}
	executeWithClient(t, c, "find", "--name", "*.go", "--mindepth", "2")
	if c.findReq.MinDepth != 2 {
		t.Fatalf("find --mindepth 2 request = %+v, want MinDepth=2", c.findReq)
	}
}

func TestFindAll(t *testing.T) {
	c := &fakeClient{findNodes: findMixedNodes()}
	executeWithClient(t, c, "find", "--name", "*.go", "-a")
	if !c.findReq.All {
		t.Fatalf("find -a request = %+v, want All=true", c.findReq)
	}
}

func TestFindIName(t *testing.T) {
	c := &fakeClient{findNodes: findMixedNodes()}
	executeWithClient(t, c, "find", "--iname", "*.MD")
	if c.findReq.IName != "*.MD" {
		t.Fatalf("find --iname request = %+v, want IName=*.MD", c.findReq)
	}
}

func TestFindINameAloneIsValid(t *testing.T) {
	c := &fakeClient{findNodes: findMixedNodes()}
	got := executeWithClient(t, c, "find", "--iname", "*.MD")
	if c.findReq.IName != "*.MD" {
		t.Fatalf("find --iname request = %+v, want IName=*.MD", c.findReq)
	}
	if c.findReq.Name != "" {
		t.Fatalf("find --iname should leave Name empty, got %q", c.findReq.Name)
	}
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatalf("find --iname output empty, want results")
	}
}

func TestFindRequiresNameOrIName(t *testing.T) {
	c := &fakeClient{findNodes: findMixedNodes()}
	cmd := newRootCommand(c)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"find", "/src"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("find without -name or -iname should error")
	}
	if err.Error() != "-name or -iname is required" {
		t.Fatalf("find error = %q, want '-name or -iname is required'", err.Error())
	}
}

func TestFindCombinedTypeAllMaxDepth(t *testing.T) {
	c := &fakeClient{findNodes: findMixedNodes()}
	executeWithClient(t, c, "find", "--name", "*.go", "-t", "f", "-a", "--maxdepth", "3")
	if c.findReq.Type != "file" {
		t.Fatalf("find combined request = %+v, want Type=file", c.findReq)
	}
	if !c.findReq.All {
		t.Fatalf("find combined request = %+v, want All=true", c.findReq)
	}
	if c.findReq.MaxDepth != 3 {
		t.Fatalf("find combined request = %+v, want MaxDepth=3", c.findReq)
	}
}

func TestTreeBasic(t *testing.T) {
	c := &fakeClient{treeText: "/\n  docs/\n    readme.md\n  main.go\n"}
	got := executeWithClient(t, c, "tree")
	want := "/\n  docs/\n    readme.md\n  main.go\n"
	if got != want {
		t.Fatalf("tree output = %q, want %q", got, want)
	}
}

func TestTreeAllFlag(t *testing.T) {
	c := &fakeClient{treeText: "/\n  .hidden\n  docs/\n  main.go\n"}
	executeWithClient(t, c, "tree", "-a")
	if !c.treeReq.All {
		t.Fatalf("tree -a request = %+v, want All=true", c.treeReq)
	}
}

func TestTreeDirsOnly(t *testing.T) {
	c := &fakeClient{treeText: "/\n  docs/\n"}
	executeWithClient(t, c, "tree", "-d")
	if !c.treeReq.DirsOnly {
		t.Fatalf("tree -d request = %+v, want DirsOnly=true", c.treeReq)
	}
}

func TestTreeFullPath(t *testing.T) {
	c := &fakeClient{treeText: "/docs\n/docs/readme.md\n"}
	executeWithClient(t, c, "tree", "-f")
	if !c.treeReq.FullPath {
		t.Fatalf("tree -f request = %+v, want FullPath=true", c.treeReq)
	}
}

func TestTreeShowSize(t *testing.T) {
	c := &fakeClient{treeText: "/\n  main.go [100B]\n"}
	executeWithClient(t, c, "tree", "-s")
	if !c.treeReq.ShowSize {
		t.Fatalf("tree -s request = %+v, want ShowSize=true", c.treeReq)
	}
}

func TestTreeSortByTime(t *testing.T) {
	c := &fakeClient{treeText: "/\n  new.go\n  old.go\n"}
	executeWithClient(t, c, "tree", "-t")
	if c.treeReq.Sort != "mtime" {
		t.Fatalf("tree -t request = %+v, want Sort=mtime", c.treeReq)
	}
}

func TestTreeDirsFirst(t *testing.T) {
	c := &fakeClient{treeText: "/\n  docs/\n  main.go\n"}
	executeWithClient(t, c, "tree", "--dirsfirst")
	if !c.treeReq.DirsFirst {
		t.Fatalf("tree --dirsfirst request = %+v, want DirsFirst=true", c.treeReq)
	}
}

func TestTreeCombinedAllDirsOnlyFullPath(t *testing.T) {
	c := &fakeClient{treeText: "/docs\n/docs/sub\n"}
	executeWithClient(t, c, "tree", "-a", "-d", "-f")
	if !c.treeReq.All {
		t.Fatalf("tree -adf request = %+v, want All=true", c.treeReq)
	}
	if !c.treeReq.DirsOnly {
		t.Fatalf("tree -adf request = %+v, want DirsOnly=true", c.treeReq)
	}
	if !c.treeReq.FullPath {
		t.Fatalf("tree -adf request = %+v, want FullPath=true", c.treeReq)
	}
}

func TestTreeSortByTimeDirsFirstSize(t *testing.T) {
	c := &fakeClient{treeText: "/\n  docs/\n  main.go [50B]\n"}
	executeWithClient(t, c, "tree", "-t", "--dirsfirst", "-s")
	if c.treeReq.Sort != "mtime" {
		t.Fatalf("tree -t --dirsfirst -s request = %+v, want Sort=mtime", c.treeReq)
	}
	if !c.treeReq.DirsFirst {
		t.Fatalf("tree -t --dirsfirst -s request = %+v, want DirsFirst=true", c.treeReq)
	}
	if !c.treeReq.ShowSize {
		t.Fatalf("tree -t --dirsfirst -s request = %+v, want ShowSize=true", c.treeReq)
	}
}

func TestFindInvalidType(t *testing.T) {
	err := executeErr(t, "find", "--name", "*.go", "-t", "x")
	if err == nil {
		t.Fatal("find -t x should error")
	}
	want := `invalid type "x": use f or d`
	if err.Error() != want {
		t.Fatalf("find -t x error = %q, want %q", err.Error(), want)
	}
}

func TestFindBackCompatNameFlag(t *testing.T) {
	c := &fakeClient{findNodes: findMixedNodes()}
	got := executeWithClient(t, c, "find", "--name", "*.go")
	if c.findReq.Name != "*.go" {
		t.Fatalf("backcompat find --name request = %+v, want Name=*.go", c.findReq)
	}
	_ = got
}

func TestTreeBackCompatLevelFlag(t *testing.T) {
	c := &fakeClient{treeText: "/\n  docs/\n"}
	executeWithClient(t, c, "tree", "-L", "1")
	if c.treeReq.Depth != 1 {
		t.Fatalf("backcompat tree -L 1 request = %+v, want Depth=1", c.treeReq)
	}
}

func TestFindNameAndINameBothSet(t *testing.T) {
	c := &fakeClient{findNodes: findMixedNodes()}
	executeWithClient(t, c, "find", "--name", "*.go", "--iname", "*.MD")
	if c.findReq.Name != "*.go" {
		t.Fatalf("find --name --iname request = %+v, want Name=*.go", c.findReq)
	}
	if c.findReq.IName != "*.MD" {
		t.Fatalf("find --name --iname request = %+v, want IName=*.MD", c.findReq)
	}
}

func TestSearchHumanOutput(t *testing.T) {
	client := &fakeClient{}
	got := executeWithClient(t, client, "search", "test query")
	if !strings.Contains(got, "/docs/guide.md") {
		t.Fatalf("search output missing path: %q", got)
	}
	if !strings.Contains(got, "rank: 0.90") {
		t.Fatalf("search output missing rank: %q", got)
	}
	if !strings.Contains(got, "test result") {
		t.Fatalf("search output missing snippet: %q", got)
	}
	if client.searchReq.Query != "test query" {
		t.Fatalf("search query = %q, want 'test query'", client.searchReq.Query)
	}
}

func TestSearchJSONOutput(t *testing.T) {
	client := &fakeClient{}
	got := executeWithClient(t, client, "search", "test", "--json")
	var resp store.SearchResponse
	if err := json.Unmarshal([]byte(got), &resp); err != nil {
		t.Fatalf("unmarshal JSON: %v, body = %q", err, got)
	}
	if resp.Total != 1 || len(resp.Results) != 1 {
		t.Fatalf("search JSON = %+v, want 1 result", resp)
	}
	if resp.Results[0].Path != "/docs/guide.md" {
		t.Fatalf("result path = %q, want /docs/guide.md", resp.Results[0].Path)
	}
}

func TestSearchPathAndLimitFlags(t *testing.T) {
	client := &fakeClient{}
	_ = executeWithClient(t, client, "search", "test", "--path", "/docs", "--limit", "5")
	if client.searchReq.Path != "/docs" {
		t.Fatalf("search path = %q, want /docs", client.searchReq.Path)
	}
	if client.searchReq.Limit != 5 {
		t.Fatalf("search limit = %d, want 5", client.searchReq.Limit)
	}
}

func TestSearchEmptyResults(t *testing.T) {
	client := &fakeClient{
		searchResp: &store.SearchResponse{Results: []store.SearchResult{}, Total: 0},
	}
	got := executeWithClient(t, client, "search", "nonexistent")
	if !strings.Contains(got, "no results found") {
		t.Fatalf("empty search output = %q, want 'no results found'", got)
	}
}

func TestCLILSLimitOffset(t *testing.T) {
	nodes := manyNodes(10)
	client := &fakeClient{lsNodes: nodes}
	cmd := newRootCommand(client)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"ls", "--limit", "3", "--offset", "2"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	// Verify limit/offset passed to LSRequest
	if client.lsReq.Limit != 3 {
		t.Fatalf("lsReq.Limit = %d, want 3", client.lsReq.Limit)
	}
	if client.lsReq.Offset != 2 {
		t.Fatalf("lsReq.Offset = %d, want 2", client.lsReq.Offset)
	}
}

func TestCLILSNegativeLimit(t *testing.T) {
	err := executeErr(t, "ls", "--limit", "-1")
	if err == nil {
		t.Fatal("expected error for negative limit")
	}
	if !strings.Contains(err.Error(), "non-negative") {
		t.Fatalf("error = %q, want non-negative message", err.Error())
	}
}

func TestCLILSNegativeOffset(t *testing.T) {
	err := executeErr(t, "ls", "--offset", "-5")
	if err == nil {
		t.Fatal("expected error for negative offset")
	}
	if !strings.Contains(err.Error(), "non-negative") {
		t.Fatalf("error = %q, want non-negative message", err.Error())
	}
}

func TestCLIFindLimitOffset(t *testing.T) {
	nodes := manyNodes(10)
	client := &fakeClient{findNodes: nodes}
	cmd := newRootCommand(client)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"find", "--name", "*.txt", "--limit", "3", "--offset", "2"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	if client.findReq.Limit != 3 {
		t.Fatalf("findReq.Limit = %d, want 3", client.findReq.Limit)
	}
	if client.findReq.Offset != 2 {
		t.Fatalf("findReq.Offset = %d, want 2", client.findReq.Offset)
	}
}

func TestCLIFindNegativeLimit(t *testing.T) {
	err := executeErr(t, "find", "--name", "*.txt", "--limit", "-1")
	if err == nil {
		t.Fatal("expected error for negative limit")
	}
	if !strings.Contains(err.Error(), "non-negative") {
		t.Fatalf("error = %q, want non-negative message", err.Error())
	}
}

func TestCLISearchOffset(t *testing.T) {
	client := &fakeClient{
		searchResp: &store.SearchResponse{
			Results: []store.SearchResult{
				{Path: "/a.md", Rank: 0.9, Snippet: "test", Size: 10},
				{Path: "/b.md", Rank: 0.8, Snippet: "test", Size: 20},
			},
			Total: 10,
		},
	}
	cmd := newRootCommand(client)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"search", "--offset", "3", "test"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	if client.searchReq.Offset != 3 {
		t.Fatalf("searchReq.Offset = %d, want 3", client.searchReq.Offset)
	}
	if !strings.Contains(out.String(), "showing 4-5 of 10") {
		t.Fatalf("output = %q, want showing summary", out.String())
	}
}

func TestCLISearchNegativeOffset(t *testing.T) {
	err := executeErr(t, "search", "--offset", "-1", "test")
	if err == nil {
		t.Fatal("expected error for negative offset")
	}
	if !strings.Contains(err.Error(), "non-negative") {
		t.Fatalf("error = %q, want non-negative message", err.Error())
	}
}

func TestGlobOutput(t *testing.T) {
	out, client := execute(t, "glob", "**/*.md")
	if client == nil {
		t.Fatal("client is nil")
	}
	if !strings.Contains(out, "docs/readme.md") {
		t.Errorf("glob output missing docs/readme.md: %q", out)
	}
	if !strings.Contains(out, "docs/api.md") {
		t.Errorf("glob output missing docs/api.md: %q", out)
	}
}

func multiFileMatches() []store.Match {
	return []store.Match{
		{Path: "/docs/readme.md", Line: 1, Text: "Welcome to ROLIO, the ROLIO project"},
		{Path: "/docs/readme.md", Line: 3, Text: "ROLIO is a virtual filesystem"},
		{Path: "/docs/readme.md", Line: 5, Text: "Learn more about ROLIO today"},
		{Path: "/src/main.go", Line: 10, Text: "// ROLIO entry point"},
	}
}

func findMixedNodes() []store.Node {
	return []store.Node{
		{Path: "/src/main.go", Name: "main.go", Kind: "file"},
		{Path: "/src/util.go", Name: "util.go", Kind: "file"},
		{Path: "/src/pkg", Name: "pkg", Kind: "dir"},
		{Path: "/src/.hidden.go", Name: ".hidden.go", Kind: "file"},
		{Path: "/README.MD", Name: "README.MD", Kind: "file"},
	}
}

func executeErr(t *testing.T, args ...string) error {
	t.Helper()
	client := &fakeClient{}
	cmd := newRootCommand(client)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	return cmd.Execute()
}

func manyNodes(n int) []store.Node {
	nodes := make([]store.Node, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("file_%02d.txt", i)
		nodes[i] = store.Node{Path: "/" + name, Name: name, Kind: "file"}
	}
	return nodes
}
