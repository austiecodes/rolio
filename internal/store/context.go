package store

import "context"

// Summaries are derived directory views. They never own the original documents.
type Summary struct {
	Path       string `json:"path"`
	Language   string `json:"language"`
	SourceHash string `json:"source_hash,omitempty"`
	Abstract   string `json:"abstract"`
	Overview   string `json:"overview"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}
type IndexStatus struct {
	Total int `json:"total"`
	Stale int `json:"stale"`
}
type Settings struct {
	Language       string      `json:"language"`
	SummaryEnabled bool        `json:"summary_enabled"`
	Index          IndexStatus `json:"index"`
}
type ContextStore interface {
	Summary(context.Context, string) (*Summary, error)
	Refresh(context.Context, string) (*Summary, error)
	Reindex(context.Context) (*IndexStatus, error)
	Settings(context.Context) (*Settings, error)
}
