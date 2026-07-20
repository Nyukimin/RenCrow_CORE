package tts

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func testCommand(script string) CommandSpec {
	if runtime.GOOS == "windows" {
		return CommandSpec{Name: "cmd.exe", Args: []string{"/d", "/s", "/c", script}}
	}
	return CommandSpec{Name: "sh", Args: []string{"-c", script}}
}

func TestCommandPlayer_PlaySuccess(t *testing.T) {
	p := NewCommandPlayer([]CommandSpec{
		testCommand("exit 0"),
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
		testCommand("exit 7"),
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
	checkFile := `test -f "{audio}"`
	if runtime.GOOS == "windows" {
		checkFile = `if exist {audio} (exit 0) else (exit 1)`
	}
	p := NewCommandPlayer([]CommandSpec{testCommand(checkFile)})
	r, err := p.Play(context.Background(), audio)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if r.ExitCode != 0 {
		t.Fatalf("expected 0, got %d", r.ExitCode)
	}
}
