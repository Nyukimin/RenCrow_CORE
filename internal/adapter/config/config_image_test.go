package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigImageGatewaySettings(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "image.yaml")
	content := `
server:
  port: 8080
image:
  enabled: true
  base_url: "http://127.0.0.1:8780"
  timeout_ms: 450000
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.Image.Enabled || cfg.Image.BaseURL != "http://127.0.0.1:8780" {
		t.Fatalf("unexpected image config: %+v", cfg.Image)
	}
	if cfg.Image.TimeoutMS != 450000 {
		t.Fatalf("unexpected image timeout: %+v", cfg.Image)
	}
}

func TestImageGatewayDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.setDefaults()

	if cfg.Image.BaseURL != "http://127.0.0.1:8780" || cfg.Image.TimeoutMS != 600000 {
		t.Fatalf("unexpected image defaults: %+v", cfg.Image)
	}
}

func TestImageGatewayValidationRejectsNonAbsoluteURL(t *testing.T) {
	cfg := &Config{
		Server:  ServerConfig{Port: 8080},
		Session: SessionConfig{StorageDir: "./sessions"},
		Image: ImageConfig{
			Enabled:   true,
			BaseURL:   "127.0.0.1:8780",
			TimeoutMS: 600000,
		},
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "image.base_url") {
		t.Fatalf("Validate error = %v, want image.base_url", err)
	}
}
