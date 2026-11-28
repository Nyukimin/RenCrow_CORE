package main

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func configureRuntimeMovieTools(ctx context.Context, cfg config.MovieConfig, workerConfig *tools.ToolRunnerConfig) error {
	if workerConfig == nil {
		return errors.New("Worker ToolRunner config is required")
	}
	if !cfg.Enabled {
		return nil
	}
	token := os.Getenv(cfg.TokenEnv)
	if token == "" {
		log.Printf("RenCrow_Movie Tools unavailable: configured bearer environment variable is empty")
		return nil
	}
	gateway, err := tools.NewMovieGatewayClient(tools.MovieGatewayClientConfig{
		Endpoint: cfg.Endpoint, ActorID: cfg.ActorID, BearerToken: token,
	})
	if err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	capabilities, err := gateway.ProbeCapabilities(probeCtx)
	cancel()
	if err != nil {
		log.Printf("RenCrow_Movie Tools unavailable: authenticated capability probe did not confirm the configured actor")
		return nil
	}
	workerConfig.Movie = gateway
	workerConfig.MovieAssetStorageReady = capabilities.Capabilities.AssetStorage == "available"
	workerConfig.MovieRenderReady = capabilities.Capabilities.Rendering == "available" && capabilities.Capabilities.RenderQueue == "available"
	workerConfig.MovieTimingReady = capabilities.Capabilities.MusicTiming == "available"
	log.Printf("RenCrow_Movie capability snapshot actor_bound=true asset_storage=%t rendering=%t render_queue=%t music_timing=%t",
		workerConfig.MovieAssetStorageReady,
		capabilities.Capabilities.Rendering == "available",
		capabilities.Capabilities.RenderQueue == "available",
		workerConfig.MovieTimingReady,
	)
	return nil
}

func moviePlaybackLinkReady(cfg *config.Config, workerConfig *tools.ToolRunnerConfig) bool {
	if cfg == nil || workerConfig == nil || workerConfig.Movie == nil || !cfg.LocalAgentOps.Enabled || strings.TrimSpace(cfg.Movie.ActorID) == "" {
		return false
	}
	ownerID, token := viewerEventsCMDOwnerCredentials(cfg)
	return ownerID == cfg.Movie.ActorID && len(token) > 0
}
