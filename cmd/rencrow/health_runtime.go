package main

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	healthapp "github.com/Nyukimin/RenCrow_CORE/internal/application/health"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domainhealth "github.com/Nyukimin/RenCrow_CORE/internal/domain/health"
	infrahealth "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/health"
	executionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/execution"
)

type healthChecker interface {
	RunChecks(ctx context.Context) domainhealth.HealthReport
}

func loadExecutionStats(cfg *config.Config) (map[domainexecution.Status]int, error) {
	if !cfg.Security.Audit.Enabled {
		return map[domainexecution.Status]int{}, nil
	}
	repo, err := executionpersistence.NewJSONLRepository(cfg.Security.Audit.Path)
	if err != nil {
		return nil, err
	}
	return repo.CountByStatus(context.Background())
}

func loadEvidenceSummary(cfg *config.Config) (map[string]map[string]int, error) {
	if !cfg.Security.Audit.Enabled {
		return map[string]map[string]int{
			"status": {
				"passed": 0,
				"failed": 0,
				"other":  0,
			},
			"error_kind": {
				"apply":  0,
				"verify": 0,
				"repair": 0,
				"none":   0,
				"other":  0,
			},
		}, nil
	}
	store, err := executionpersistence.NewJSONLReportStore(cfg.Security.Audit.Path)
	if err != nil {
		return nil, err
	}
	return store.Summary(context.Background())
}

func buildHealthService(cfg *config.Config) *healthapp.HealthService {
	var checks []domainhealth.Check
	apiKey := ""
	if cfg.LLMGateway.APIKeyEnv != "" {
		apiKey = strings.TrimSpace(os.Getenv(cfg.LLMGateway.APIKeyEnv))
	}
	timeout := time.Duration(cfg.LLMGateway.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.LLMGateway.BaseURL), "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8090"
	}
	for _, agentID := range []string{"mio", "worker", "shiro", "kuro", "midori"} {
		checks = append(checks, infrahealth.NewGatewayAliasCheck("gateway_"+agentID, baseURL, agentID, apiKey, timeout))
	}
	if cfg.Vision.Enabled {
		analyzer, _, err := buildVisionRuntime(cfg)
		if err != nil {
			checks = append(checks, infrahealth.NewVisionCheck(nil))
		} else {
			checks = append(checks, infrahealth.NewVisionCheck(analyzer))
		}
	}

	return healthapp.NewHealthService(checks...)
}

func inferTTSDebugBaseURLFromConfig(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.TTS.GatewayURL())
}

func inferTTSDebugHealthPathFromConfig(cfg *config.Config) string {
	return ""
}
