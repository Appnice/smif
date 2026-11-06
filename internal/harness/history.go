package harness

import (
	"context"
	"time"
)

// HistoryRow is one past session of a provider (ADR-0021).
type HistoryRow struct {
	SessionID string
	Dir       string    // the folder the session started in
	Name      string    // its title, else its first prompt, on one line
	At        time.Time // its last user or assistant record
}

// SearchHit is one record whose text holds the words searched for.
type SearchHit struct {
	HistoryRow
	Role string // RoleHuman or RoleAgent
	Text string // the words with some text around them, on one line
	// At of the embedded row is the record's time.
}

// Historian is a provider that can list and search its past sessions.
type Historian interface {
	// History returns the past sessions, newest first.
	History() []HistoryRow
	// Search looks through the text of user and assistant records, newest
	// file first. It stops at limit hits and when ctx ends.
	Search(ctx context.Context, query string, limit int) []SearchHit
}
