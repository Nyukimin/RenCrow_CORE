package main

import (
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainvision "github.com/Nyukimin/RenCrow_CORE/internal/domain/vision"
	infravision "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/vision"
)

func buildVisionRuntime(cfg *config.Config) (domainvision.Analyzer, orchestrator.VisionOptions, error) {
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
