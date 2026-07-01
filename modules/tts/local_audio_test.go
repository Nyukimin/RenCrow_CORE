package tts

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildLocalAudioURL(t *testing.T) {
	outputDir := t.TempDir()
	audioPath := filepath.Join(outputDir, "sample.wav")
	got := BuildLocalAudioURL(outputDir, audioPath)
	want := "/viewer/tts/audio?path=sample.wav"
	if got != want {
		t.Fatalf("BuildLocalAudioURL() = %q, want %q", got, want)
	}
}

func TestBuildLocalAudioURLRejectsOutsideOutputDir(t *testing.T) {
	outputDir := t.TempDir()
	outsidePath := filepath.Join(t.TempDir(), "sample.wav")
	if got := BuildLocalAudioURL(outputDir, outsidePath); got != "" {
		t.Fatalf("BuildLocalAudioURL() = %q, want empty", got)
	}
}

func TestLocalAudioRelPathHandlesRelativeInput(t *testing.T) {
	outputDir := t.TempDir()
	got, ok := LocalAudioRelPath(outputDir, "nested/chunk.wav")
	if !ok || got != "nested/chunk.wav" {
		t.Fatalf("LocalAudioRelPath() = %q,%t", got, ok)
	}
}

func TestLocalAudioRelPathCollapsesRelativeOutputDirPath(t *testing.T) {
	cwd := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	outputDir := filepath.Join("workspace", "tts")
	audioPath := filepath.Join("workspace", "tts", "sample.wav")
	got, ok := LocalAudioRelPath(outputDir, audioPath)
	if !ok || got != "sample.wav" {
		t.Fatalf("LocalAudioRelPath() = %q,%t, want sample.wav,true", got, ok)
	}
}

func TestResolveLocalAudioPathRejectsTraversal(t *testing.T) {
	base, ok := NormalizeLocalAudioBase(t.TempDir())
	if !ok {
		t.Fatal("NormalizeLocalAudioBase() failed")
	}
	if got, ok := ResolveLocalAudioPath(base, "../secret.wav"); ok || got != "" {
		t.Fatalf("ResolveLocalAudioPath() = %q,%t, want reject", got, ok)
	}
}
