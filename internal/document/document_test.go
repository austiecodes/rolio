package document

import (
	"strings"
	"testing"
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
