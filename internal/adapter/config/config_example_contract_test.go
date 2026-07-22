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
	if cfg.LocalLLM.Enabled {
		t.Fatal("config.yaml.example must not expose the legacy physical local_llm route")
	}
	if cfg.STT.StreamURL == "" {
		t.Fatal("config.yaml.example should expose stt.stream_url for Viewer STT contract")
	}
	if _, ok := cfg.VTuber.Characters["shiro"]; !ok {
		t.Fatal("config.yaml.example should keep vtuber.characters.shiro separate from audio_router.device_map")
	}
	if _, ok := cfg.AudioRouter.DeviceMap["shiro"]; !ok {
		t.Fatal("config.yaml.example should keep audio_router.device_map.shiro for audio routing")
	}
}
