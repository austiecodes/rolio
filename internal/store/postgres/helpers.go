package postgres

import (
	"path"
	"regexp"
	"strings"

	"github.com/austiecodes/rolio/internal/store"
)

func grepLineMatch(line, pattern string, re *regexp.Regexp, caseInsensitive, wholeWord, wholeLine bool) bool {
	switch {
	case wholeLine:
		return wholeLineMatch(line, pattern, re, caseInsensitive)
	case wholeWord:
		return wholeWordMatch(line, pattern, re, caseInsensitive)
	default:
		return grepBasicMatch(line, pattern, re, caseInsensitive)
	}
}

func grepBasicMatch(line, pattern string, re *regexp.Regexp, caseInsensitive bool) bool {
	if re != nil {
		return re.MatchString(line)
	}
	if caseInsensitive {
		return strings.Contains(strings.ToLower(line), strings.ToLower(pattern))
	}
	return strings.Contains(line, pattern)
}

func wholeLineMatch(line, pattern string, re *regexp.Regexp, caseInsensitive bool) bool {
	if re != nil {
		return re.MatchString(line)
	}
	if caseInsensitive {
		return strings.EqualFold(line, pattern)
	}
	return line == pattern
}

func wholeWordMatch(line, pattern string, re *regexp.Regexp, caseInsensitive bool) bool {
	if re != nil {
		return re.MatchString(line)
	}
	if caseInsensitive {
		return hasWholeWordSubstring(strings.ToLower(line), strings.ToLower(pattern))
	}
	return hasWholeWordSubstring(line, pattern)
}

// hasWholeWordSubstring checks if pattern exists in line with word boundaries.
// Ported from vfs.hasWholeWordSubstring to keep semantics identical.
func hasWholeWordSubstring(line, pattern string) bool {
	idx := strings.Index(line, pattern)
	for idx != -1 {
		if isWordBoundary(line, idx) && isWordBoundary(line, idx+len(pattern)) {
			return true
		}
		next := strings.Index(line[idx+1:], pattern)
		if next == -1 {
			return false
		}
		idx = idx + 1 + next
	}
	return false
}

func isWordBoundary(s string, pos int) bool {
	if pos <= 0 || pos >= len(s) {
		return true
	}
	return !isWordChar(s[pos-1]) || !isWordChar(s[pos])
}

func isWordChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// globMatch matches a file path against include/exclude glob patterns.
// Uses path.Base to extract the filename (same as VFS filterByGlob).
// Returns false if include doesn't match or exclude matches.
func globMatch(filePath, include, exclude string) bool {
	name := path.Base(filePath)
	if include != "" {
		if ok, _ := path.Match(include, name); !ok {
			return false
		}
	}
	if exclude != "" {
		if ok, _ := path.Match(exclude, name); ok {
			return false
		}
	}
	return true
}

// pathHasHidden checks if a file path has a hidden component (starting with .).
func pathHasHidden(filePath, root string) bool {
	rel := strings.TrimPrefix(filePath, root)
	parts := strings.Split(rel, "/")
	for _, p := range parts {
		if strings.HasPrefix(p, ".") && p != "." && p != ".." {
			return true
		}
	}
	return false
}

func copyLines(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	cp := make([]string, len(lines))
	copy(cp, lines)
	return cp
}

// paginateNodes applies limit/offset to a node slice.
// Limit <= 0 means unlimited; Offset is clamped to [0, len(nodes)].
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

// Glob is not supported by the legacy postgres adapter.
