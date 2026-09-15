package main

import (
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/transportmanager"
	domainvision "github.com/Nyukimin/RenCrow_CORE/internal/domain/vision"
	infravision "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/vision"
)

func buildVisionRuntime(cfg *config.Config, tasks *taskmanager.Manager, actions *actionmanager.Manager, transport *transportmanager.Manager) (domainvision.Analyzer, orchestrator.VisionOptions, error) {
	client, options, err := buildVisionClient(cfg)
	if err != nil || client == nil {
		return client, options, err
	}
	owned, err := infravision.NewActionAnalyzer(client, actions, tasks, transport, "mio")
	if err != nil {
		return nil, options, err
	}
	return owned, options, nil
}

func buildVisionClient(cfg *config.Config) (domainvision.Analyzer, orchestrator.VisionOptions, error) {
	options := orchestrator.VisionOptions{
		MaxImageBytes: cfg.Vision.MaxImageBytes,
		MaxVideoBytes: cfg.Vision.MaxVideoBytes,
		MaxFrames:     cfg.Vision.MaxFrames,
		Language:      "ja",
	}
	if !cfg.Vision.Enabled {
		return nil, options, nil
	}
	client, err := infravision.NewClient(
		cfg.Vision.BaseURL,
		time.Duration(cfg.Vision.TimeoutMS)*time.Millisecond,
	)
	if err != nil {
		return nil, options, err
	}
	return client, options, nil
}
