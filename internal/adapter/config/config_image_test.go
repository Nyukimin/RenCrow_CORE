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

func validImageVideoBrowserConfig() ImageConfig {
	return ImageConfig{
		Enabled: true,
		VideoBrowser: ImageVideoBrowserConfig{
			Enabled:            true,
			Host:               "llm-rx6800.tailb07d8d.ts.net:8444",
			Origin:             "https://llm-rx6800.tailb07d8d.ts.net:8444",
			ActorID:            "user:yukimi",
			TailscaleUserLogin: "Yukimi+viewer@example.com",
		},
	}
}

func TestImageVideoBrowserValidationRequiresExactServeIdentity(t *testing.T) {
	valid := validImageVideoBrowserConfig()
	if err := validateImageVideoBrowserConfig(valid); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	cases := []struct {
		name string
		edit func(*ImageConfig)
	}{
		{name: "image disabled", edit: func(c *ImageConfig) { c.Enabled = false }},
		{name: "host origin mismatch", edit: func(c *ImageConfig) { c.VideoBrowser.Origin = "https://other.tailb07d8d.ts.net:8444" }},
		{name: "http origin", edit: func(c *ImageConfig) { c.VideoBrowser.Origin = "http://llm-rx6800.tailb07d8d.ts.net:8444" }},
		{name: "non tailscale host", edit: func(c *ImageConfig) { c.VideoBrowser.Host = "example.com:8444" }},
		{name: "invalid actor kind", edit: func(c *ImageConfig) { c.VideoBrowser.ActorID = "tailscale:yukimi" }},
		{name: "empty actor", edit: func(c *ImageConfig) { c.VideoBrowser.ActorID = "" }},
		{name: "missing login", edit: func(c *ImageConfig) { c.VideoBrowser.TailscaleUserLogin = "" }},
		{name: "login with spaces", edit: func(c *ImageConfig) { c.VideoBrowser.TailscaleUserLogin = "user name@example.com" }},
		{name: "login with comma", edit: func(c *ImageConfig) { c.VideoBrowser.TailscaleUserLogin = "user@example.com,other@example.com" }},
		{name: "login with control", edit: func(c *ImageConfig) { c.VideoBrowser.TailscaleUserLogin = "user@example.com\n" }},
		{name: "login too long", edit: func(c *ImageConfig) { c.VideoBrowser.TailscaleUserLogin = strings.Repeat("a", 321) }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			candidate := valid
			tt.edit(&candidate)
			if err := validateImageVideoBrowserConfig(candidate); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func TestLoadConfigImageVideoBrowserReadsExactLoginConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "image-video.yaml")
	content := `
server:
  port: 8080
image:
  enabled: true
  base_url: "http://127.0.0.1:8780"
  timeout_ms: 450000
  video_browser:
    enabled: true
    allow_generation: true
    host: "llm-rx6800.tailb07d8d.ts.net:8444"
    origin: "https://llm-rx6800.tailb07d8d.ts.net:8444"
    actor_id: "user:yukimi"
    tailscale_user_login: "Yukimi+viewer@example.com"
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.Image.VideoBrowser.Enabled || !cfg.Image.VideoBrowser.AllowGeneration ||
		cfg.Image.VideoBrowser.ActorID != "user:yukimi" ||
		cfg.Image.VideoBrowser.TailscaleUserLogin != "Yukimi+viewer@example.com" {
		t.Fatalf("unexpected video browser config: enabled=%t allow_generation=%t actor_id=%q login=%q",
			cfg.Image.VideoBrowser.Enabled, cfg.Image.VideoBrowser.AllowGeneration,
			cfg.Image.VideoBrowser.ActorID, cfg.Image.VideoBrowser.TailscaleUserLogin)
	}
}

func TestLoadConfigImageVideoGenerationPermissionDefaultsClosed(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "image-video-policy.yaml")
	content := `
server:
  port: 8080
image:
  enabled: true
  base_url: "http://127.0.0.1:8780"
  timeout_ms: 450000
  video_browser:
    enabled: true
    host: "llm-rx6800.tailb07d8d.ts.net:8444"
    origin: "https://llm-rx6800.tailb07d8d.ts.net:8444"
    actor_id: "user:yukimi"
    tailscale_user_login: "yukimi@example.com"
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Image.VideoBrowser.AllowGeneration {
		t.Fatal("video generation permission must default closed")
	}
}

func TestLoadConfigImageVideoBrowserRejectsRemovedSessionKeysRegardlessOfEnabled(t *testing.T) {
	content := `
server:
  port: 8080
image:
  enabled: true
  base_url: "http://127.0.0.1:8780"
  timeout_ms: 450000
  video_browser:
    enabled: ENABLED
    host: "llm-rx6800.tailb07d8d.ts.net:8444"
    origin: "https://llm-rx6800.tailb07d8d.ts.net:8444"
    actor_id: "user:yukimi"
    RETIRED_KEY: "value"
`
	cases := []struct {
		name    string
		enabled string
		key     string
	}{
		{name: "enabled credential", enabled: "true", key: "credential_sha256"},
		{name: "enabled cookie key", enabled: "true", key: "cookie_hmac_key_hex"},
		{name: "disabled credential", enabled: "false", key: "credential_sha256"},
		{name: "disabled cookie key", enabled: "false", key: "cookie_hmac_key_hex"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "image-video-retired-auth.yaml")
			configContent := strings.Replace(content, "ENABLED", tt.enabled, 1)
			configContent = strings.Replace(configContent, "RETIRED_KEY", tt.key, 1)
			if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			if _, err := LoadConfig(configPath); err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("LoadConfig error = %v, want retired key %q rejection", err, tt.key)
			}
		})
	}
}

func TestLoadConfigImageVideoBrowserRejectsDuplicateFields(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "image-video-duplicate.yaml")
	content := `
server:
  port: 8080
image:
  enabled: true
  base_url: "http://127.0.0.1:8780"
  timeout_ms: 450000
  video_browser:
    enabled: true
    host: "llm-rx6800.tailb07d8d.ts.net:8444"
    origin: "https://llm-rx6800.tailb07d8d.ts.net:8444"
    actor_id: "user:yukimi"
    tailscale_user_login: "yukimi@example.com"
    tailscale_user_login: "other@example.com"
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := LoadConfig(configPath); err == nil || !strings.Contains(err.Error(), "tailscale_user_login") {
		t.Fatalf("LoadConfig error = %v, want duplicate login rejection", err)
	}
}
