package main

import (
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
)

func TestInferTTSDebugHealthPathFromConfigUsesStandardHealthProbesForIrodori(t *testing.T) {
	cfg := &config.Config{}
	cfg.TTS.Irodori.Enabled = true
	cfg.TTS.Irodori.BaseURL = "http://192.168.1.207:7870"

	if got := inferTTSDebugHealthPathFromConfig(cfg); got != "" {
		t.Fatalf("health path = %q, want empty path so /health/live and /health/ready are probed", got)
	}
}
