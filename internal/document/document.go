// Package document handles Markdown metadata without rewriting the source.
package document

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

type Document struct {
	Body     string
	Metadata map[string]any
}

func Parse(raw string) (Document, error) {
	d := Document{Body: raw}
	lines := strings.Split(raw, "\n")
	if strings.TrimSuffix(lines[0], "\r") != "---" {
		return d, nil
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSuffix(lines[i], "\r") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return d, fmt.Errorf("frontmatter requires a closing --- line")
	}
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &d.Metadata); err != nil {
		return d, fmt.Errorf("invalid YAML frontmatter: %w", err)
	}
	for _, key := range []string{"title", "source"} {
		if value, ok := d.Metadata[key]; ok {
			if _, ok := value.(string); !ok {
				return d, fmt.Errorf("frontmatter %s must be a string", key)
			}
		}
	}
	if value, ok := d.Metadata["tags"]; ok {
		tags, ok := value.([]any)
		if !ok {
			return d, fmt.Errorf("frontmatter tags must be a list of strings")
		}
		for _, tag := range tags {
			if _, ok := tag.(string); !ok {
				return d, fmt.Errorf("frontmatter tags must be a list of strings")
			}
		}
	}
	for _, key := range []string{"language", "source_hash", "generated_by", "status", "revision"} {
		if _, ok := d.Metadata[key]; ok {
			return d, fmt.Errorf("frontmatter %s is reserved for server metadata", key)
		}
	}
	if _, err := json.Marshal(d.Metadata); err != nil {
		return d, fmt.Errorf("frontmatter must be JSON-compatible: %w", err)
	}
	d.Body = strings.Join(lines[end+1:], "\n")
	return d, nil
}

// Tokens splits Han runs into unigrams and overlapping bigrams, retaining
// Latin words for PostgreSQL's stemmer.
// No dictionary, external extension or runtime model is required.
func Tokens(text string) string {
	var tokens []string
	var run []rune
	flush := func() {
		for _, r := range run {
			tokens = append(tokens, string(r))
		}
		for i := 0; i+1 < len(run); i++ {
			tokens = append(tokens, string(run[i:i+2]))
		}
		run = nil
	}
	var word []rune
	flushWord := func() {
		if len(word) > 0 {
			tokens = append(tokens, string(word))
			word = nil
		}
	}
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			flushWord()
			run = append(run, r)
		} else {
			flush()
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				word = append(word, unicode.ToLower(r))
			} else {
				flushWord()
			}
		}
	}
	flush()
	flushWord()
	return strings.Join(tokens, " ")
}
