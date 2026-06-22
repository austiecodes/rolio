package document

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestFrontmatter(t *testing.T) {
	raw := "---\r\ntitle: 身份认证\r\ntags: [auth, 中文]\r\ncustom: true\r\n---\r\n\r\n正文\r\n"
	d, err := Parse(raw)
	if err != nil || d.Metadata["title"] != "身份认证" || d.Body != "\r\n正文\r\n" {
		t.Fatalf("%+v %v", d, err)
	}
	for _, raw := range []string{"---\ntitle: 123\n---\nx", "---\ntags: wrong\n---\nx", "---\nstatus: ready\n---\nx", "---\ntitle: x", "---\ntitle: x\ntitle: y\n---\n", "---\n- list\n---\n"} {
		if _, err := Parse(raw); err == nil {
			t.Errorf("accepted invalid metadata %q", raw)
		}
	}
	d, err = Parse("# Original\nno frontmatter\n")
	if err != nil || d.Body != "# Original\nno frontmatter\n" {
		t.Fatal(d, err)
	}
}
func TestMixedLanguageTokens(t *testing.T) {
	tokens := strings.Fields(Tokens("支付失败 Retry API；身份认证"))
	set := map[string]bool{}
	for _, s := range tokens {
		set[s] = true
	}
	for _, want := range []string{"支付", "失败", "付", "retry", "api", "认证"} {
		if !set[want] {
			t.Errorf("missing %s in %v", want, tokens)
		}
	}
	if set["败身"] {
		t.Fatal("bigram crosses a word boundary")
	}
}

func TestTokens(t *testing.T) {
	if got, want := Tokens("支付失败 Retry-API_v2；认证a付"), "支 付 失 败 支付 付失 失败 retry api v2 认 证 认证 a 付"; got != want {
		t.Errorf("Tokens = %q, want %q", got, want)
	}
}

func TestQueryTerms(t *testing.T) {
	// The limit is the maximum number of terms.
	if got := QueryTerms("a b a c d", 3); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("QueryTerms with a limit = %q", got)
	}
	// The time for a long query is in proportion to its length.
	var long strings.Builder
	for i := range 200_000 {
		fmt.Fprintf(&long, "w%d 支付%c ", i, rune(0x4e00+i%20000))
	}
	start := time.Now()
	if got := QueryTerms(long.String(), 16); len(got) != 16 {
		t.Errorf("QueryTerms of a long query = %q", got)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("QueryTerms of %d bytes took %v", long.Len(), took)
	}
	for query, want := range map[string][]string{
		"Support groups, SUPPORT!": {"support", "groups"},
		"支付失败":                     {"支付", "付失", "失败"},
		"付":                        {"付"},
		"付 API 认证 付":               {"付", "api", "认证"},
		"a' | b & !c:* <-> (d)":    {"a", "b", "c", "d"},
		" ?! ":                     nil,
	} {
		if got := QueryTerms(query, 16); !slices.Equal(got, want) {
			t.Errorf("QueryTerms(%q) = %q, want %q", query, got, want)
		}
	}
}

func TestPassages(t *testing.T) {
	got := Passages("  first line \r\n\n\t\nsecond\n", 20)
	if want := []string{"first line", "second"}; !slices.Equal(got, want) {
		t.Errorf("Passages = %q, want %q", got, want)
	}
	// A long line is cut at a space, and without a space at a character.
	got = Passages("one two three four five six", 12)
	if want := []string{"one two ...", "... three four ...", "... five six"}; !slices.Equal(got, want) {
		t.Errorf("Passages = %q, want %q", got, want)
	}
	long := strings.Repeat("支付", 25) + " " + strings.Repeat("é", 30)
	got = Passages(long, 20)
	clean := strings.NewReplacer(" ", "", ".", "")
	if clean.Replace(strings.Join(got, "")) != clean.Replace(long) {
		t.Errorf("Passages lost text: %q", got)
	}
	for _, passage := range got {
		passage = strings.TrimSuffix(strings.TrimPrefix(passage, "... "), " ...")
		if n := utf8.RuneCountInString(passage); n == 0 || n > 20 || !utf8.ValidString(passage) {
			t.Errorf("passage %q has %d characters", passage, n)
		}
	}
	// A later part of a line starts with the label of the line.
	got = Passages("[Caroline] (D1:3): one two three four five six\n[user]: ok\n", 12)
	if want := []string{"[Caroline] ...", "[Caroline] (D1:3): ... (D1:3): one ...", "[Caroline] (D1:3): ... two three ...", "[Caroline] (D1:3): ... four five ...", "[Caroline] (D1:3): ... six", "[user]: ok"}; !slices.Equal(got, want) {
		t.Errorf("Passages with a label = %q, want %q", got, want)
	}
	// The label is in the first 40 characters of the line.
	for line, want := range map[string]string{
		"[user]: text":                     "[user]: ",
		"no label here":                    "",
		"time 10:30 is not a label":        "",
		strings.Repeat("x", 38) + ": text": strings.Repeat("x", 38) + ": ",
		strings.Repeat("x", 39) + ": text": "",
		"ends with a colon:":               "",
	} {
		if got := lineLabel([]rune(line)); got != want {
			t.Errorf("lineLabel(%q) = %q, want %q", line, got, want)
		}
	}
	// The time for a long line is in proportion to its length.
	line := strings.Repeat("word ", 100_000) + strings.Repeat("字", 500_000)
	start := time.Now()
	got = Passages(line, 480)
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("Passages of a line of %d bytes took %v", len(line), took)
	}
	if n := len(got); n < 2000 || n > 2200 {
		t.Errorf("Passages of a long line = %d parts", n)
	}
}
