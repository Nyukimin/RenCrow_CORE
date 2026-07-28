package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigExampleLoadsForPhase25E2E(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join("..", "..", "..", "config", "config.yaml.example"))
	if err != nil {
		t.Fatalf("config.yaml.example must be parseable for repo-default E2E: %v", err)
	}
	if !cfg.LLMGateway.Enabled || cfg.LLMGateway.BaseURL == "" {
		t.Fatal("config.yaml.example should use RenCrow_LLM Gateway as the production path")
	}
	if cfg.LocalLLM.Enabled {
		t.Fatal("config.yaml.example must not expose the legacy physical local_llm route")
	}
	if !cfg.STT.Enabled || cfg.STT.GatewayBaseURL != "http://127.0.0.1:8766" {
		t.Fatal("config.yaml.example should use RenCrow_STT Gateway as the production path")
	}
	if cfg.TTS.GatewayBaseURL == "" {
		t.Fatal("config.yaml.example should use RenCrow_TTS Gateway as the production path")
	}
	if !cfg.Vision.Enabled || cfg.Vision.BaseURL == "" {
		t.Fatal("config.yaml.example should use RenCrow_Vision as the production image recognition path")
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "config.yaml.example"))
	if err != nil {
		t.Fatalf("read config.yaml.example: %v", err)
	}
	text := string(raw)
	for _, forbidden := range []string{
		"provider_priority:", "provider_params:", "irodori:", "sbv2:", "azure:", "eleven:",
		"STT_PROVIDER_URL", "stream_url:", "external_http:", "192.168.1.31",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("config.yaml.example must not expose CORE-owned direct backend setting %q", forbidden)
		}
	}
	if _, ok := cfg.VTuber.Characters["shiro"]; !ok {
		t.Fatal("config.yaml.example should keep vtuber.characters.shiro separate from audio_router.device_map")
	}
	if _, ok := cfg.AudioRouter.DeviceMap["shiro"]; !ok {
		t.Fatal("config.yaml.example should keep audio_router.device_map.shiro for audio routing")
	}
}
