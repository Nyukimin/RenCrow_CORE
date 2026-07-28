package config

import (
	"path/filepath"
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
	if !cfg.STT.Enabled || cfg.STT.GatewayBaseURL != "http://192.168.1.205:8766" {
		t.Fatal("config.yaml.example should use RenCrow_STT Gateway as the production path")
	}
	if cfg.TTS.GatewayBaseURL == "" {
		t.Fatal("config.yaml.example should use RenCrow_TTS Gateway as the production path")
	}
	if !cfg.Vision.Enabled || cfg.Vision.BaseURL == "" {
		t.Fatal("config.yaml.example should use RenCrow_Vision as the production image recognition path")
	}
	if _, ok := cfg.VTuber.Characters["shiro"]; !ok {
		t.Fatal("config.yaml.example should keep vtuber.characters.shiro separate from audio_router.device_map")
	}
	if _, ok := cfg.AudioRouter.DeviceMap["shiro"]; !ok {
		t.Fatal("config.yaml.example should keep audio_router.device_map.shiro for audio routing")
	}
}
