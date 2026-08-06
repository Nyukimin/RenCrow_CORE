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
	if !strings.Contains(p.SystemPrompt, "Mio（澪）") {
		t.Error("default persona should contain 'Mio（澪）'")
	}
	if !strings.Contains(p.SystemPrompt, "Chat Agent") {
		t.Error("default persona should identify Mio as a Chat Agent")
	}
	if !strings.Contains(p.SystemPrompt, "patch適用") {
		t.Error("default persona should preserve Mio's execution boundary")
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
