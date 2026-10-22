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
			Enabled:           true,
			Host:              "192.168.1.206:18790",
			Origin:            "http://192.168.1.206:18790",
			AllowedClientIPv4: "192.168.1.204",
			ActorID:           "user:yukimi",
		},
	}
}

func TestImageVideoBrowserValidationRequiresExactPrivateLANTarget(t *testing.T) {
	valid := validImageVideoBrowserConfig()
	if err := validateImageVideoBrowserConfig(valid); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	cases := []struct {
		name string
		edit func(*ImageConfig)
	}{
		{name: "image disabled", edit: func(c *ImageConfig) { c.Enabled = false }},
		{name: "host origin mismatch", edit: func(c *ImageConfig) { c.VideoBrowser.Origin = "http://192.168.1.205:18790" }},
		{name: "https origin", edit: func(c *ImageConfig) { c.VideoBrowser.Origin = "https://192.168.1.206:18790" }},
		{name: "non IPv4 host", edit: func(c *ImageConfig) { c.VideoBrowser.Host = "viewer.example.com:18790" }},
		{name: "public host", edit: func(c *ImageConfig) {
			c.VideoBrowser.Host = "8.8.8.8:18790"
			c.VideoBrowser.Origin = "http://8.8.8.8:18790"
		}},
		{name: "invalid host port", edit: func(c *ImageConfig) {
			c.VideoBrowser.Host = "192.168.1.206:abc"
			c.VideoBrowser.Origin = "http://192.168.1.206:abc"
		}},
		{name: "invalid actor kind", edit: func(c *ImageConfig) { c.VideoBrowser.ActorID = "lan:yukimi" }},
		{name: "empty actor", edit: func(c *ImageConfig) { c.VideoBrowser.ActorID = "" }},
		{name: "missing allowed client", edit: func(c *ImageConfig) { c.VideoBrowser.AllowedClientIPv4 = "" }},
		{name: "public allowed client", edit: func(c *ImageConfig) { c.VideoBrowser.AllowedClientIPv4 = "8.8.8.8" }},
		{name: "IPv6 allowed client", edit: func(c *ImageConfig) { c.VideoBrowser.AllowedClientIPv4 = "fd00::1" }},
		{name: "noncanonical allowed client", edit: func(c *ImageConfig) { c.VideoBrowser.AllowedClientIPv4 = "192.168.001.204" }},
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

func TestLoadConfigImageVideoBrowserReadsLANConfig(t *testing.T) {
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
    host: "192.168.1.206:18790"
    origin: "http://192.168.1.206:18790"
    allowed_client_ipv4: "192.168.1.204"
    actor_id: "user:yukimi"
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
		cfg.Image.VideoBrowser.AllowedClientIPv4 != "192.168.1.204" ||
		cfg.Image.VideoBrowser.Host != "192.168.1.206:18790" ||
		cfg.Image.VideoBrowser.Origin != "http://192.168.1.206:18790" {
		t.Fatalf("unexpected video browser config: enabled=%t allow_generation=%t host=%q client=%q actor_id=%q",
			cfg.Image.VideoBrowser.Enabled, cfg.Image.VideoBrowser.AllowGeneration,
			cfg.Image.VideoBrowser.Host, cfg.Image.VideoBrowser.AllowedClientIPv4, cfg.Image.VideoBrowser.ActorID)
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
    host: "192.168.1.206:18790"
    origin: "http://192.168.1.206:18790"
    allowed_client_ipv4: "192.168.1.204"
    actor_id: "user:yukimi"
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

func TestLoadConfigImageVideoBrowserDefaultsDisabled(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "image-video-default.yaml")
	content := `
server:
  port: 8080
session:
  storage_dir: "./sessions"
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Image.VideoBrowser.Enabled || cfg.Image.VideoBrowser.AllowGeneration {
		t.Fatalf("Movie access defaults must be disabled: enabled=%t allow_generation=%t",
			cfg.Image.VideoBrowser.Enabled, cfg.Image.VideoBrowser.AllowGeneration)
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
		{name: "enabled Tailscale login", enabled: "true", key: "tailscale_user_login"},
		{name: "disabled credential", enabled: "false", key: "credential_sha256"},
		{name: "disabled cookie key", enabled: "false", key: "cookie_hmac_key_hex"},
		{name: "disabled Tailscale login", enabled: "false", key: "tailscale_user_login"},
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
    host: "192.168.1.206:18790"
    origin: "http://192.168.1.206:18790"
    allowed_client_ipv4: "192.168.1.204"
    actor_id: "user:yukimi"
    allowed_client_ipv4: "192.168.1.205"
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := LoadConfig(configPath); err == nil || !strings.Contains(err.Error(), "allowed_client_ipv4") {
		t.Fatalf("LoadConfig error = %v, want duplicate allowed client rejection", err)
	}
}
