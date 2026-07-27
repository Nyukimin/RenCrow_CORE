package main

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func TestBuildVisionRuntimeUsesOnlyRenCrowVisionEndpoint(t *testing.T) {
	cfg := &config.Config{
		Vision: config.VisionConfig{
			Enabled:       true,
			BaseURL:       "http://127.0.0.1:8770",
			TimeoutMS:     120000,
			MaxImageBytes: 20 << 20,
			MaxVideoBytes: 100 << 20,
			MaxFrames:     8,
		},
	}
	analyzer, options, err := buildVisionRuntime(cfg)
	if err != nil {
		t.Fatalf("buildVisionRuntime: %v", err)
	}
	if analyzer == nil {
		t.Fatal("enabled Vision must create an analyzer")
	}
	if options.MaxFrames != 8 || options.MaxVideoBytes != 100<<20 {
		t.Fatalf("unexpected options: %+v", options)
	}
}

func TestBuildVisionRuntimeKeepsDisabledVisionUnavailable(t *testing.T) {
	cfg := &config.Config{Vision: config.VisionConfig{
		Enabled:       false,
		MaxImageBytes: 20 << 20,
		MaxVideoBytes: 100 << 20,
		MaxFrames:     8,
	}}
	analyzer, _, err := buildVisionRuntime(cfg)
	if err != nil {
		t.Fatalf("buildVisionRuntime: %v", err)
	}
	if analyzer != nil {
		t.Fatal("disabled Vision must not create a fallback analyzer")
	}
}
