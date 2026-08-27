package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigRejectsRetiredDuckDBSettings(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "top level duckdb path",
			content: "server:\n  port: 8080\nduckdb_path: /state/memory.duckdb\n",
			want:    "duckdb_path",
		},
		{
			name:    "legacy database inventory",
			content: "server:\n  port: 8080\nstorage:\n  legacy_databases:\n    memory_duckdb: /state/memory.duckdb\n",
			want:    "storage.legacy_databases",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConfig(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "retired") {
				t.Fatalf("LoadConfig() error=%v, want retired %q rejection", err, tt.want)
			}
		})
	}
}

func TestLoadConfigRejectsRetiredDirectTTSEndpointKeys(t *testing.T) {
	const gatewayURL = "http://127.0.0.1:8787"
	tests := []struct {
		name      string
		key       string
		wantError bool
	}{
		{name: "http_base_url", key: "http_base_url", wantError: true},
		{name: "base_url", key: "base_url", wantError: true},
		{name: "public_base_url", key: "public_base_url", wantError: true},
		{name: "audio_base_url", key: "audio_base_url", wantError: true},
		{name: "canonical gateway_base_url", key: "gateway_base_url"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			content := "server:\n  port: 8080\ntts:\n  enabled: true\n  " + tt.key + ": \"" + gatewayURL + "\"\n"
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}

			cfg, err := LoadConfig(path)
			if tt.wantError {
				if err == nil || !strings.Contains(err.Error(), "tts."+tt.key) || !strings.Contains(err.Error(), "CORE uses tts.gateway_base_url") {
					t.Fatalf("LoadConfig() error=%v, want retired tts.%s rejection", err, tt.key)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadConfig() canonical gateway_base_url: %v", err)
			}
			if cfg.TTS.GatewayURL() != gatewayURL {
				t.Fatalf("canonical gateway_base_url=%q, want %q", cfg.TTS.GatewayURL(), gatewayURL)
			}
		})
	}
}
