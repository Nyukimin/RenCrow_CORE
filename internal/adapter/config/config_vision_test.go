package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigVisionSettings(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "vision.yaml")
	content := `
server:
  port: 8080
ollama:
  base_url: "http://127.0.0.1:11434"
  model: "test"
session:
  storage_dir: "./sessions"
vision:
  enabled: true
  base_url: "http://127.0.0.1:8770"
  timeout_ms: 45000
  max_image_bytes: 20971520
  max_video_bytes: 104857600
  max_frames: 12
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.Vision.Enabled || cfg.Vision.BaseURL != "http://127.0.0.1:8770" {
		t.Fatalf("unexpected vision config: %+v", cfg.Vision)
	}
	if cfg.Vision.TimeoutMS != 45000 || cfg.Vision.MaxFrames != 12 {
		t.Fatalf("unexpected vision runtime limits: %+v", cfg.Vision)
	}
	if cfg.Vision.MaxImageBytes != 20<<20 || cfg.Vision.MaxVideoBytes != 100<<20 {
		t.Fatalf("unexpected vision byte limits: %+v", cfg.Vision)
	}
}

func TestVisionDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.setDefaults()

	if cfg.Vision.BaseURL != "http://127.0.0.1:8770" {
		t.Fatalf("vision.base_url = %q", cfg.Vision.BaseURL)
	}
	if cfg.Vision.TimeoutMS != 120000 || cfg.Vision.MaxFrames != 8 {
		t.Fatalf("unexpected vision defaults: %+v", cfg.Vision)
	}
	if cfg.Vision.MaxImageBytes != 20<<20 || cfg.Vision.MaxVideoBytes != 100<<20 {
		t.Fatalf("unexpected vision byte defaults: %+v", cfg.Vision)
	}
}

func TestVisionValidationRejectsNonAbsoluteURL(t *testing.T) {
	cfg := &Config{
		Server:  ServerConfig{Port: 8080},
		Ollama:  OllamaConfig{BaseURL: "http://127.0.0.1:11434", Model: "test"},
		Session: SessionConfig{StorageDir: "./sessions"},
		Vision: VisionConfig{
			Enabled:       true,
			BaseURL:       "127.0.0.1:8770",
			TimeoutMS:     120000,
			MaxImageBytes: 20 << 20,
			MaxVideoBytes: 100 << 20,
			MaxFrames:     8,
		},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "vision.base_url") {
		t.Fatalf("Validate error = %v, want vision.base_url", err)
	}
}

func TestVisionValidationRejectsInvalidLimits(t *testing.T) {
	cfg := &Config{
		Server:  ServerConfig{Port: 8080},
		Ollama:  OllamaConfig{BaseURL: "http://127.0.0.1:11434", Model: "test"},
		Session: SessionConfig{StorageDir: "./sessions"},
		Vision: VisionConfig{
			Enabled:       true,
			BaseURL:       "http://127.0.0.1:8770",
			TimeoutMS:     120000,
			MaxImageBytes: 20 << 20,
			MaxVideoBytes: 100 << 20,
			MaxFrames:     0,
		},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "vision.max_frames") {
		t.Fatalf("Validate error = %v, want vision.max_frames", err)
	}
}
