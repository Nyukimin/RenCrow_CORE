package conversation

import "testing"

func TestDocumentIsValid(t *testing.T) {
	valid := &Document{
		ID:        "doc-1",
		Domain:    "tech",
		Content:   "content",
		Embedding: []float32{0.1},
	}
	if !valid.IsValid() {
		t.Fatal("expected complete document to be valid")
	}

	tests := []struct {
		name   string
		mutate func(*Document)
	}{
		{
			name: "missing ID",
			mutate: func(d *Document) {
				d.ID = ""
			},
		},
		{
			name: "missing domain",
			mutate: func(d *Document) {
				d.Domain = ""
			},
		},
		{
			name: "missing content",
			mutate: func(d *Document) {
				d.Content = ""
			},
		},
		{
			name: "missing embedding",
			mutate: func(d *Document) {
				d.Embedding = nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := *valid
			tt.mutate(&doc)
			if doc.IsValid() {
				t.Fatalf("expected %s to be invalid", tt.name)
			}
		})
	}
}

func TestNewSessionConversationInitializesFields(t *testing.T) {
	session := NewSessionConversation("session-1", "user-1")

	if session.ID != "session-1" {
		t.Fatalf("unexpected session ID: %s", session.ID)
	}
	if session.UserID != "user-1" {
		t.Fatalf("unexpected user ID: %s", session.UserID)
	}
	if session.History == nil {
		t.Fatal("expected history to be initialized")
	}
	if len(session.History) != 0 {
		t.Fatalf("expected empty history, got %d entries", len(session.History))
	}
	if session.LastThreadID != 0 {
		t.Fatalf("expected zero last thread ID, got %d", session.LastThreadID)
	}
	if session.CreatedAt.IsZero() || session.UpdatedAt.IsZero() {
		t.Fatal("expected timestamps to be initialized")
	}
	if !session.CreatedAt.Equal(session.UpdatedAt) {
		t.Fatalf("expected matching timestamps, got %s and %s", session.CreatedAt, session.UpdatedAt)
	}
}
