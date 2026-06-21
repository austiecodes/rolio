package postgres

import (
	"slices"
	"strings"
	"testing"

	"github.com/austiecodes/rolio/internal/summary"
)

func TestSummaryPaths(t *testing.T) {
	paths, parents := summaryPaths("/a/b/c.md", true)
	if !slices.Equal(paths, []string{"/a/b/c.md", "/a/b", "/a", "/"}) || !slices.Equal(parents, []string{"/a/b", "/a", "/", ""}) {
		t.Errorf("paths = %v, parents = %v", paths, parents)
	}
	if paths, _ := summaryPaths("/a/b/c.md", false); !slices.Equal(paths, []string{"/a/b", "/a", "/"}) {
		t.Errorf("ancestors = %v", paths)
	}
}

func TestChildSources(t *testing.T) {
	long := strings.Repeat("x", 40)
	children := func() []summaryChild {
		return []summaryChild{
			{path: "/d/a.md", abstract: "short a", overview: long, file: true},
			{path: "/d/b.md", abstract: "short b", file: true},
			{path: "/d/sub", abstract: "short sub", overview: long},
		}
	}
	// With space, a document gives its overview, or its abstract when it has
	// no overview. A directory gives its abstract.
	got := childSources(children(), 1000)
	want := []summary.Source{{Path: "/d/a.md", Body: long}, {Path: "/d/b.md", Body: "short b"}, {Path: "/d/sub/", Body: "short sub"}}
	if !slices.Equal(got, want) {
		t.Errorf("sources = %+v, want %+v", got, want)
	}
	// A child without a summary gives no input.
	got = childSources(append(children(), summaryChild{path: "/d/z.md", file: true}, summaryChild{path: "/d/zz"}), 1000)
	if !slices.Equal(got, want) {
		t.Errorf("sources = %+v, want %+v", got, want)
	}
	if got := childSources([]summaryChild{{path: "/d/z.md", file: true}}, 1000); len(got) != 0 {
		t.Errorf("sources = %+v, want none", got)
	}
	// Without space for the overviews, each child gives its abstract.
	got = childSources(children(), 60)
	want = []summary.Source{{Path: "/d/a.md", Body: "short a"}, {Path: "/d/b.md", Body: "short b"}, {Path: "/d/sub/", Body: "short sub"}}
	if !slices.Equal(got, want) {
		t.Errorf("sources = %+v, want %+v", got, want)
	}
	// Without space for all children, the list says how many are not in it.
	got = childSources(children(), 30)
	want = []summary.Source{{Path: "/d/a.md", Body: "short a"}, {Path: "/d/b.md", Body: "short b"}, {Body: "1 more children are not in this list."}}
	if !slices.Equal(got, want) {
		t.Errorf("sources = %+v, want %+v", got, want)
	}
}

func TestInTree(t *testing.T) {
	for _, test := range []struct {
		p, root string
		want    bool
	}{{"/a/b", "/", true}, {"/a/b", "/a", true}, {"/a", "/a", true}, {"/ab", "/a", false}, {"/", "/a", false}} {
		if got := inTree(test.p, test.root); got != test.want {
			t.Errorf("inTree(%q, %q) = %v", test.p, test.root, got)
		}
	}
}

func TestCutText(t *testing.T) {
	if got := cutText("aé", 2); got != "a" {
		t.Errorf("cutText = %q, want %q", got, "a")
	}
}
