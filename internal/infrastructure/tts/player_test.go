package tts

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCommandPlayer_PlaySuccess(t *testing.T) {
	p := NewCommandPlayer([]CommandSpec{
		{Name: "sh", Args: []string{"-c", "exit 0"}},
	})
	r, err := p.Play(context.Background(), "/tmp/a.wav")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if r.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", r.ExitCode)
	}
}

func TestCommandPlayer_PlayFailure(t *testing.T) {
	p := NewCommandPlayer([]CommandSpec{
		{Name: "sh", Args: []string{"-c", "exit 7"}},
	})
	r, err := p.Play(context.Background(), "/tmp/a.wav")
	if err == nil {
		t.Fatal("expected error")
	}
	if r.ExitCode == 0 {
		t.Fatalf("expected non-zero exit code, got %d", r.ExitCode)
	}
}

func TestCommandPlayer_ReplacesAudioToken(t *testing.T) {
	td := t.TempDir()
	audio := filepath.Join(td, "sample.wav")
	if err := os.WriteFile(audio, []byte("x"), 0644); err != nil {
		t.Fatalf("write audio failed: %v", err)
	}
	p := NewCommandPlayer([]CommandSpec{
		{Name: "sh", Args: []string{"-c", "test -f {audio}"}},
	})
	r, err := p.Play(context.Background(), audio)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if r.ExitCode != 0 {
		t.Fatalf("expected 0, got %d", r.ExitCode)
	}
}

