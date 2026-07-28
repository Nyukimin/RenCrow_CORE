package main

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func TestInferTTSDebugHealthPathFromConfigUsesGatewayHealthProbes(t *testing.T) {
	cfg := &config.Config{}
	cfg.TTS.GatewayBaseURL = "http://192.168.1.207:7870"

	if got := inferTTSDebugHealthPathFromConfig(cfg); got != "" {
		t.Fatalf("health path = %q, want empty path so /health/live and /health/ready are probed", got)
	}
}

func TestInferTTSDebugBaseURLFromConfigUsesGatewayOnly(t *testing.T) {
	cfg := &config.Config{TTS: config.TTSConfig{
		GatewayBaseURL: "http://gateway.example:7870",
	}}
	if got := inferTTSDebugBaseURLFromConfig(cfg); got != "http://gateway.example:7870" {
		t.Fatalf("base URL = %q", got)
	}
}
