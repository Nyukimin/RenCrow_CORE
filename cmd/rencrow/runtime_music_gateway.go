package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

// configureRuntimeMusicTools probes the authenticated, configured Music
// gateway and adds each Worker Tool family only after its exact storage
// capability and actor binding have been observed.
func configureRuntimeMusicTools(ctx context.Context, cfg config.MusicConfig, workerConfig *tools.ToolRunnerConfig) error {
	if workerConfig == nil {
		return fmt.Errorf("Worker ToolRunner config is required")
	}
	if !cfg.Enabled {
		return nil
	}
	token := os.Getenv(cfg.TokenEnv)
	if token == "" {
		log.Printf("RenCrow_Music Tools unavailable: configured bearer environment variable is empty")
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
		log.Printf("RenCrow_Music Tools unavailable: authenticated capability probe did not confirm the configured actor")
		return nil
	}
	projectStorage, found := capabilities.Capabilities["project_storage"]
	if found && projectStorage.SupportState == "implemented" && projectStorage.Availability == "ready" {
		workerConfig.MusicProjects = gateway
		log.Printf("RenCrow_Music project Tools ready for the configured actor")
	} else {
		log.Printf("RenCrow_Music project Tools unavailable: project_storage is not confirmed implemented and ready")
	}
	productionControl, found := capabilities.Capabilities["production_control"]
	if found && productionControl.SupportState == "implemented" && productionControl.Availability == "ready" {
		workerConfig.MusicControl = gateway
		log.Printf("RenCrow_Music production-control Tools ready for the configured actor")
	} else {
		log.Printf("RenCrow_Music production-control Tools unavailable: production_control is not confirmed implemented and ready")
	}
	planStorage, found := capabilities.Capabilities["production_plan_storage"]
	if found && planStorage.SupportState == "implemented" && planStorage.Availability == "ready" {
		workerConfig.MusicPlans = gateway
		log.Printf("RenCrow_Music production-plan Tools ready for the configured actor")
	} else {
		log.Printf("RenCrow_Music production-plan Tools unavailable: production_plan_storage is not confirmed implemented and ready")
	}
	generation, found := capabilities.Capabilities["music_generation"]
	if found && generation.SupportState == "implemented" && generation.Availability == "ready" {
		workerConfig.MusicGeneration = gateway
		log.Printf("RenCrow_Music generation Tools ready for the configured actor")
	} else {
		log.Printf("RenCrow_Music generation Tools unavailable: music_generation is not confirmed implemented and ready")
	}
	playback, found := capabilities.Capabilities["artifact_playback"]
	if found && playback.SupportState == "implemented" && playback.Availability == "ready" {
		workerConfig.MusicArtifactPlayback = gateway
		log.Printf("RenCrow_Music artifact playback ready for the configured actor")
	} else {
		log.Printf("RenCrow_Music artifact playback unavailable: artifact_playback is not confirmed implemented and ready")
	}
	remix, found := capabilities.Capabilities["music_remix"]
	if found && remix.SupportState == "implemented" && remix.Availability == "ready" {
		workerConfig.MusicRemix = gateway
		log.Printf("RenCrow_Music bounded remix Tool ready for the configured actor")
	} else {
		log.Printf("RenCrow_Music bounded remix Tool unavailable: music_remix is not confirmed implemented and ready")
	}
	return nil
}

func musicPlaybackLinkReady(cfg *config.Config, workerConfig *tools.ToolRunnerConfig) bool {
	if cfg == nil || workerConfig == nil || workerConfig.MusicArtifactPlayback == nil ||
		!cfg.LocalAgentOps.Enabled || strings.TrimSpace(cfg.Music.ActorID) == "" {
		return false
	}
	ownerID, token := viewerEventsCMDOwnerCredentials(cfg)
	return ownerID == cfg.Music.ActorID && len(token) > 0
}
