package main

import (
	"context"
	"log"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	capdomain "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/capability"
	capinfra "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/capability"
	toolregistry "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/toolregistry"
)

func buildRuntimeToolRegistry(cfg *config.Config) capdomain.ToolRegistry {
	if cfg.Capability.ToolRegistryDB == "" {
		return nil
	}
	tr, err := toolregistry.NewDuckDBToolRegistryStore(cfg.Capability.ToolRegistryDB)
	if err != nil {
		log.Printf("WARN: ToolRegistry init failed (%s): %v", cfg.Capability.ToolRegistryDB, err)
		return nil
	}
	log.Printf("ToolRegistry initialized: %s", cfg.Capability.ToolRegistryDB)
	return tr
}

func buildCapabilityRuntime(cfg *config.Config, runtimeToolRegistry capdomain.ToolRegistry) capdomain.NodeCapabilities {
	var nodeCaps capdomain.NodeCapabilities
	if !cfg.Capability.ProbeLLMs {
		return nodeCaps
	}
	detector := capinfra.NewCapabilityDetector(cfg)
	if runtimeToolRegistry != nil {
		detector = detector.WithToolRegistry(runtimeToolRegistry)
	}
	caps, err := detector.Detect(context.Background())
	if err != nil {
		log.Printf("WARN: capability detection failed: %v", err)
		return nodeCaps
	}
	nodeCaps = caps
	profile := capdomain.DetermineProfile(caps)
	log.Printf("Node capabilities: profile=%s llms=%d tools=%d memory=%dMB/%dMB os=%s/%s",
		profile, len(caps.LLMs), len(caps.Tools),
		caps.Memory.AvailableMB, caps.Memory.TotalMB,
		caps.Platform.OS, caps.Platform.Arch)
	for _, l := range caps.LLMs {
		log.Printf("  LLM: provider=%s model=%s available=%v quality=%d",
			l.ProviderName, l.ModelName, l.Available, l.Quality)
	}
	return nodeCaps
}
