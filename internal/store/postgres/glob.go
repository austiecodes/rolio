package postgres

import (
	"fmt"
	"regexp"
	"strings"
)

// globToRegex converts a glob pattern to a PostgreSQL-compatible regex pattern
// that can be used with the ~ operator on the path column.
//
// Supported syntax:
//   - **  = match any path segments (including zero)
//   - *   = match any characters except /
//   - ?   = match any single character except /
//   - All other characters are escaped literally.
//
// Paths in rolio_paths are stored with leading / (e.g. "/docs/readme.md").
// Callers add a /? anchor prefix so the regex handles the leading / correctly.
func globToRegex(pattern string) (string, error) {
	if pattern == "" {
		return "", fmt.Errorf("glob pattern cannot be empty")
	}

	// Clean the pattern: strip leading ./ or /
	pattern = strings.TrimPrefix(pattern, "./")
	pattern = strings.Trim(pattern, "/")
	if pattern == "" {
		return ".*", nil // match everything
	}

	var b strings.Builder
	i := 0
	for i < len(pattern) {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				// **
				if i+2 < len(pattern) && pattern[i+2] == '/' {
					// **/ → match any directory depth (including zero)
					b.WriteString("(.*/)?")
					i += 3
				} else {
					// ** at end or before non-/ → match everything
					b.WriteString(".*")
					i += 2
				}
			} else {
				// * → match any non-/ characters
				b.WriteString("[^/]*")
				i++
			}
		case '?':
			b.WriteString("[^/]")
			i++
		default:
			// Escape regex special characters
			if isRegexSpecial(pattern[i]) {
				b.WriteByte('\\')
			}
			b.WriteByte(pattern[i])
			i++
		}
	}

	result := b.String()

	// Validate the regex compiles
	if _, err := regexp.Compile(result); err != nil {
		return "", fmt.Errorf("invalid glob pattern %q: %w", pattern, err)
	}

	return result, nil
}

func isRegexSpecial(c byte) bool {
	return strings.ContainsAny(string(c), `\.+{}()[]^$|`)
}
