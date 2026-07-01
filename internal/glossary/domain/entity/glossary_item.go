package entity

import (
	"fmt"
	"time"
)

type GlossaryItem struct {
	ID          string    `json:"id"`
	Term        string    `json:"term"`
	Explanation string    `json:"explanation"`
	Source      string    `json:"source"`
	Category    string    `json:"category"` // e.g., "new_word", "organization", "location"
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func NewGlossaryItem(term, explanation, source, category string) *GlossaryItem {
	now := time.Now()
	return &GlossaryItem{
		ID:          generateID(),
		Term:        term,
		Explanation: explanation,
		Source:      source,
		Category:    category,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func generateID() string {
	return fmt.Sprintf("gloss_%d", time.Now().UnixNano())
}
