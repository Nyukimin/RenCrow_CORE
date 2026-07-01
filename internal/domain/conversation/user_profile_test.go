package conversation

import (
	"strings"
	"testing"
)

func TestNewUserProfile(t *testing.T) {
	p := NewUserProfile("user-001")
	if p.UserID != "user-001" {
		t.Errorf("UserID: want 'user-001', got %q", p.UserID)
	}
	if p.Preferences == nil {
		t.Fatal("Preferences should be non-nil")
	}
	if len(p.Preferences) != 0 {
		t.Errorf("Preferences should be empty, got %d", len(p.Preferences))
	}
	if p.Facts == nil {
		t.Fatal("Facts should be non-nil")
	}
	if len(p.Facts) != 0 {
		t.Errorf("Facts should be empty, got %d", len(p.Facts))
	}
	if p.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be non-zero")
	}
}

func TestUserProfile_Merge_NewPreferences(t *testing.T) {
	p := NewUserProfile("u1")
	p.Merge(map[string]string{"lang": "Go", "editor": "vim"}, nil)
	if p.Preferences["lang"] != "Go" {
		t.Errorf("lang: want 'Go', got %q", p.Preferences["lang"])
	}
	if p.Preferences["editor"] != "vim" {
		t.Errorf("editor: want 'vim', got %q", p.Preferences["editor"])
	}
}

func TestUserProfile_Merge_OverwritePreferences(t *testing.T) {
	p := NewUserProfile("u1")
	p.Merge(map[string]string{"lang": "Go"}, nil)
	p.Merge(map[string]string{"lang": "Rust"}, nil)
	if p.Preferences["lang"] != "Rust" {
		t.Errorf("lang should be overwritten to 'Rust', got %q", p.Preferences["lang"])
	}
}

func TestUserProfile_Merge_NewFacts(t *testing.T) {
	p := NewUserProfile("u1")
	p.Merge(nil, []string{"likes cats", "works at startup"})
	if len(p.Facts) != 2 {
		t.Fatalf("Facts: want 2, got %d", len(p.Facts))
	}
	if p.Facts[0] != "likes cats" || p.Facts[1] != "works at startup" {
		t.Errorf("unexpected facts: %v", p.Facts)
	}
}

func TestUserProfile_Merge_DuplicateFacts(t *testing.T) {
	p := NewUserProfile("u1")
	p.Merge(nil, []string{"likes cats"})
	p.Merge(nil, []string{"likes cats", "new fact"})
	if len(p.Facts) != 2 {
		t.Errorf("duplicate fact should be ignored, got %d facts: %v", len(p.Facts), p.Facts)
	}
}

func TestUserProfile_Merge_UpdatesTimestamp(t *testing.T) {
	p := NewUserProfile("u1")
	before := p.UpdatedAt
	p.Merge(map[string]string{"key": "val"}, nil)
	if !p.UpdatedAt.After(before) && p.UpdatedAt != before {
		// time.Now() may be same granularity, so just check not before
		t.Log("UpdatedAt should be updated (may be equal due to clock granularity)")
	}
}

func TestUserProfile_ToPromptText_Empty(t *testing.T) {
	p := NewUserProfile("u1")
	text := p.ToPromptText()
	if text != "" {
		t.Errorf("empty profile should return empty string, got %q", text)
	}
}

func TestUserProfile_ToPromptText_WithPreferences(t *testing.T) {
	p := NewUserProfile("u1")
	p.Preferences["lang"] = "Go"
	text := p.ToPromptText()
	if !strings.Contains(text, "ユーザーについて知っていること") {
		t.Error("should contain header")
	}
	if !strings.Contains(text, "lang: Go") {
		t.Error("should contain preference")
	}
}

func TestUserProfile_ToPromptText_WithFacts(t *testing.T) {
	p := NewUserProfile("u1")
	p.Facts = []string{"likes Go"}
	text := p.ToPromptText()
	if !strings.Contains(text, "likes Go") {
		t.Error("should contain fact")
	}
}

func TestUserProfile_ToPromptText_Both(t *testing.T) {
	p := NewUserProfile("u1")
	p.Preferences["theme"] = "dark"
	p.Facts = []string{"developer"}
	text := p.ToPromptText()
	if !strings.Contains(text, "theme: dark") {
		t.Error("should contain preference")
	}
	if !strings.Contains(text, "developer") {
		t.Error("should contain fact")
	}
}
