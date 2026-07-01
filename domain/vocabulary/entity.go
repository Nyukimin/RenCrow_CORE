package vocabulary

import (
	"time"
)

// Entry represents a glossary entry for recent topics
type Entry struct {
	Term        string    `json:"term"`
	Description string    `json:"description"`
	Source      string    `json:"source"`
	Timestamp   time.Time `json:"timestamp"`
	Categories  []string  `json:"categories"`
}

// IsRecent returns true if the entry is from the last 7 days
func (e *Entry) IsRecent() bool {
	return time.Since(e.Timestamp) <= 7*24*time.Hour
}
