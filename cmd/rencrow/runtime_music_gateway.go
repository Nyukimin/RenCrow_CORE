package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

// configureRuntimeMusicTools probes the authenticated, configured Music
// gateway and adds project Tools to the Worker Runner only after the exact
// project-storage capability and actor binding have been observed.
func configureRuntimeMusicTools(ctx context.Context, cfg config.MusicConfig, workerConfig *tools.ToolRunnerConfig) error {
	if workerConfig == nil {
		return fmt.Errorf("Worker ToolRunner config is required")
	}
	if !cfg.Enabled {
		return nil
	}
	token := os.Getenv(cfg.TokenEnv)
	if token == "" {
		log.Printf("RenCrow_Music project Tools unavailable: configured bearer environment variable is empty")
		return nil
	}
	gateway, err := tools.NewMusicProjectGatewayClient(tools.MusicGatewayClientConfig{
		Endpoint: cfg.Endpoint, ActorID: cfg.ActorID, BearerToken: token,
	})
	if err != nil {
		return fmt.Errorf("configure RenCrow_Music project gateway: %w", err)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	capabilities, err := gateway.ProbeCapabilities(probeCtx)
	cancel()
	if err != nil {
		log.Printf("RenCrow_Music project Tools unavailable: authenticated capability probe did not confirm the configured actor")
		return nil
	}
	projectStorage, found := capabilities.Capabilities["project_storage"]
	if !found || projectStorage.SupportState != "implemented" || projectStorage.Availability != "ready" {
		log.Printf("RenCrow_Music project Tools unavailable: project_storage is not confirmed implemented and ready")
		return nil
	}
	workerConfig.MusicProjects = gateway
	log.Printf("RenCrow_Music project Tools ready for the configured actor")
	return nil
}
