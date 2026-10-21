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
func TestImageVideoBrowserValidationRequiresExactServeIdentity(t *testing.T) {
	valid := ImageConfig{
		Enabled: true,
		VideoBrowser: ImageVideoBrowserConfig{
			Enabled:          true,
			Host:             "llm-rx6800.tailb07d8d.ts.net:8444",
			Origin:           "https://llm-rx6800.tailb07d8d.ts.net:8444",
			ActorID:          "user:yukimi",
			CredentialSHA256: strings.Repeat("a", 64),
			CookieHMACKeyHex: strings.Repeat("b", 64),
		},
	}
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
		{name: "invalid credential verifier", edit: func(c *ImageConfig) { c.VideoBrowser.CredentialSHA256 = "not-a-sha256" }},
		{name: "missing cookie key", edit: func(c *ImageConfig) { c.VideoBrowser.CookieHMACKeyHex = "" }},
		{name: "short cookie key", edit: func(c *ImageConfig) { c.VideoBrowser.CookieHMACKeyHex = strings.Repeat("b", 62) }},
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

func TestLoadConfigImageVideoBrowserReadsCredentialSessionConfig(t *testing.T) {
	credentialHash := strings.Repeat("a", 64)
	cookieKey := strings.Repeat("b", 64)
	t.Setenv("RENCROW_VIDEO_BROWSER_CREDENTIAL_SHA256", credentialHash)
	t.Setenv("RENCROW_VIDEO_BROWSER_COOKIE_HMAC_KEY_HEX", cookieKey)
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
    credential_sha256: "${RENCROW_VIDEO_BROWSER_CREDENTIAL_SHA256}"
    cookie_hmac_key_hex: "${RENCROW_VIDEO_BROWSER_COOKIE_HMAC_KEY_HEX}"
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.Image.VideoBrowser.Enabled || !cfg.Image.VideoBrowser.AllowGeneration || cfg.Image.VideoBrowser.ActorID != "user:yukimi" ||
		cfg.Image.VideoBrowser.CredentialSHA256 != credentialHash || cfg.Image.VideoBrowser.CookieHMACKeyHex != cookieKey {
		t.Fatal("video browser credential session config was not loaded")
	}
}

func TestLoadConfigImageVideoGenerationPermissionDefaultsClosed(t *testing.T) {
	t.Setenv("RENCROW_VIDEO_BROWSER_CREDENTIAL_SHA256", strings.Repeat("a", 64))
	t.Setenv("RENCROW_VIDEO_BROWSER_COOKIE_HMAC_KEY_HEX", strings.Repeat("b", 64))
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
    credential_sha256: "${RENCROW_VIDEO_BROWSER_CREDENTIAL_SHA256}"
    cookie_hmac_key_hex: "${RENCROW_VIDEO_BROWSER_COOKIE_HMAC_KEY_HEX}"
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
