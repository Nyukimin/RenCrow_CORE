package conversation

import (
	"strings"
	"testing"
)

func TestNewMioPersona(t *testing.T) {
	p := NewMioPersona("custom prompt")
	if p.Name != "ミオ" {
		t.Errorf("Name: want 'ミオ', got %q", p.Name)
	}
	if p.SystemPrompt != "custom prompt" {
		t.Errorf("SystemPrompt: want 'custom prompt', got %q", p.SystemPrompt)
	}
	if p.Tone != "friendly" {
		t.Errorf("Tone: want 'friendly', got %q", p.Tone)
	}
	if p.Mood != "neutral" {
		t.Errorf("Mood: want 'neutral', got %q", p.Mood)
	}
}

func TestDefaultMioPersona(t *testing.T) {
	p := DefaultMioPersona()
	if p.Name != "ミオ" {
		t.Errorf("Name: want 'ミオ', got %q", p.Name)
	}
	if !strings.Contains(p.SystemPrompt, "ミオ（澪）") {
		t.Error("default persona should contain 'ミオ（澪）'")
	}
	if !strings.Contains(p.SystemPrompt, "超ギャル") {
		t.Error("default persona should mention '超ギャル'")
	}
	if !strings.Contains(p.SystemPrompt, "全モード") {
		t.Error("default persona should mention '全モード'")
	}
}

func TestPersonaState_ZeroValue(t *testing.T) {
	var p PersonaState
	if p.Name != "" {
		t.Errorf("zero value Name should be empty, got %q", p.Name)
	}
	if p.SystemPrompt != "" {
		t.Errorf("zero value SystemPrompt should be empty, got %q", p.SystemPrompt)
	}
	if p.Tone != "" {
		t.Errorf("zero value Tone should be empty, got %q", p.Tone)
	}
	if p.Mood != "" {
		t.Errorf("zero value Mood should be empty, got %q", p.Mood)
	}
}
