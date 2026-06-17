package store

import (
	"crypto/sha256"
	"fmt"
)

// HashContent returns the SHA-256 content hash in "sha256:<hex>" format.
// This is the canonical hash function used across the project for content
// conditional writes and ETag generation.
func HashContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return fmt.Sprintf("sha256:%x", sum)
}
