package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	advisorapp "github.com/Nyukimin/RenCrow_CORE/internal/application/advisor"
	agentprofileapp "github.com/Nyukimin/RenCrow_CORE/internal/application/agentprofile"
	domainadvisor "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainagentprofile "github.com/Nyukimin/RenCrow_CORE/internal/domain/agentprofile"
	advisorpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/advisor"
)

type advisorRuntime struct {
	Service       agent.AdvisorService
	Store         advisorRuntimeStore
	Policy        *agentprofileapp.PolicyService
	Profiles      []domainadvisor.Profile
	AgentProfiles []domainagentprofile.Profile
	Closer        interface{ Close() error }
}

type advisorRuntimeStore interface {
	advisorapp.Store
	agentprofileapp.PolicyDecisionStore
}

func buildAdvisorRuntime(cfg *config.Config, workerToolRunner agent.ToolRunner) (advisorRuntime, error) {
	if cfg == nil {
		return advisorRuntime{}, fmt.Errorf("config is required")
	}
	runtime := advisorRuntime{}
	switch strings.TrimSpace(cfg.Advisor.Storage) {
	case "", "jsonl":
		runtime.Store = advisorpersistence.NewJSONLStore(cfg.Advisor.LogPath)
	case "sqlite":
		store, err := advisorpersistence.NewSQLiteStore(cfg.Advisor.SQLitePath)
		if err != nil {
			return advisorRuntime{}, fmt.Errorf("initialize advisor sqlite store: %w", err)
		}
		runtime.Store = store
		runtime.Closer = store
	default:
		return advisorRuntime{}, fmt.Errorf("unsupported advisor storage %q", cfg.Advisor.Storage)
	}
	catalog := agentprofileapp.NewStaticCatalog()
	runtime.AgentProfiles = catalog.List()
	runtime.Policy = agentprofileapp.NewPolicyService(catalog).WithStore(runtime.Store)
	if !cfg.Codex.Enabled || workerToolRunner == nil {
		return runtime, nil
	}
	registry := advisorapp.NewService(advisorapp.NewCodexToolAdvisor(workerToolRunner))
	runtime.Profiles = registry.Profiles()
	runtime.Service = advisorapp.NewRecordingService(registry, runtime.Store, nil)
	return runtime, nil
}

type advisorScoreRunner interface {
	Run(context.Context) (int, error)
}

func startAdvisorScoreJob(
	store advisorapp.Store,
	profiles []domainadvisor.Profile,
	reporter backgroundJobFailureReporter,
) context.CancelFunc {
	advisorIDs := make([]domainadvisor.AdvisorID, 0, len(profiles))
	for _, profile := range profiles {
		if !profile.Disabled {
			advisorIDs = append(advisorIDs, profile.ID)
		}
	}
	service := advisorapp.NewDailyScoreService(store, advisorIDs, nil)
	return startAdvisorScoreJobRunner(service, 24*time.Hour, reporter)
}

func startAdvisorScoreJobRunner(
	runner advisorScoreRunner,
	interval time.Duration,
	reporter backgroundJobFailureReporter,
) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	if runner == nil {
		return cancel
	}
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	go func() {
		run := func() {
			saved, err := runner.Run(ctx)
			if err != nil {
				if ctx.Err() == nil {
					reporter.Failed("advisor_daily_score", err, "persist previous UTC day score snapshots")
				}
				return
			}
			if saved > 0 {
				log.Printf("Advisor daily score job saved %d snapshot(s)", saved)
			}
		}
		run()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
	return cancel
}
