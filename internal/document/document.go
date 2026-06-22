// Package document handles Markdown metadata without rewriting the source.
package document

import (
	"encoding/json"
	"fmt"
	"slices"
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

// scan calls han for each run of Han characters and word for each run of
// other letters and digits, in lower case, in the order of the text.
func scan(text string, han func([]rune), word func(string)) {
	var run, letters []rune
	flush := func() {
		if len(run) > 0 {
			han(run)
			run = nil
		}
		if len(letters) > 0 {
			word(string(letters))
			letters = nil
		}
	}
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			if len(letters) > 0 {
				flush()
			}
			run = append(run, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if len(run) > 0 {
				flush()
			}
			letters = append(letters, unicode.ToLower(r))
		default:
			flush()
		}
	}
	flush()
}

// Tokens splits Han runs into unigrams and overlapping bigrams, retaining
// Latin words for PostgreSQL's stemmer.
// No dictionary, external extension or runtime model is required.
func Tokens(text string) string {
	var tokens []string
	scan(text, func(run []rune) {
		for _, r := range run {
			tokens = append(tokens, string(r))
		}
		for i := 0; i+1 < len(run); i++ {
			tokens = append(tokens, string(run[i:i+2]))
		}
	}, func(word string) { tokens = append(tokens, word) })
	return strings.Join(tokens, " ")
}

// QueryTerms returns the different terms of a search query, to a maximum of
// limit terms. Each term is one token of Tokens, thus it contains only
// letters and digits. A search finds the documents that contain one or more
// of the terms. Nearly all Chinese documents contain each of the frequent
// characters, thus a Han run of two or more characters gives its bigrams and
// not its characters.
func QueryTerms(query string, limit int) []string {
	var terms []string
	add := func(term string) {
		if len(terms) < limit && !slices.Contains(terms, term) {
			terms = append(terms, term)
		}
	}
	scan(query, func(run []rune) {
		if len(run) == 1 {
			add(string(run))
		}
		for i := 0; i+1 < len(run) && len(terms) < limit; i++ {
			add(string(run[i : i+2]))
		}
	}, add)
	return terms
}

// labelLength is the maximum number of characters of the label of a line.
const labelLength = 40

// lineLabel returns the label of a line: its text to the first ": ", when
// that is in the first characters of the line. In a conversation this is the
// speaker, for example "[user]: ".
func lineLabel(line []rune) string {
	for i := 0; i+1 < len(line) && i+2 <= labelLength; i++ {
		if line[i] == ':' && line[i+1] == ' ' {
			return string(line[:i+2])
		}
	}
	return ""
}

// Passages divides a text into its lines without the empty lines, and a line
// that is longer than limit characters into parts of that length or less.
// A part ends at a space when that is possible. The mark "..." shows the
// side of a part where the line continues. A part that is not the start of
// its line starts with the label of the line, thus it shows where it is from.
// The label and the marks are not in the limit.
func Passages(text string, limit int) []string {
	var passages []string
	for line := range strings.SplitSeq(text, "\n") {
		runes := []rune(line)
		// The part of the line that is not divided yet is runes[start:end].
		start, end := 0, len(runes)
		for start < end && unicode.IsSpace(runes[start]) {
			start++
		}
		for end > start && unicode.IsSpace(runes[end-1]) {
			end--
		}
		before := ""
		for end-start > limit {
			cut := start + limit
			// A space in the first half makes a part that is too short.
			for i := cut; i > start+limit/2; i-- {
				if unicode.IsSpace(runes[i]) {
					cut = i
					break
				}
			}
			partEnd := cut
			for unicode.IsSpace(runes[partEnd-1]) {
				partEnd--
			}
			passages = append(passages, before+string(runes[start:partEnd])+" ...")
			if before == "" {
				before = lineLabel(runes[start:end]) + "... "
			}
			for start = cut; start < end && unicode.IsSpace(runes[start]); start++ {
			}
		}
		if start < end {
			passages = append(passages, before+string(runes[start:end]))
		}
	}
	return passages
}
