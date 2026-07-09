package main

import (
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/l1sqlite"
	"log"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/viewer"
	appverification "github.com/Nyukimin/picoclaw_multiLLM/internal/application/verification"
	domainverification "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/verification"
	verificationpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/verification"
)

type verificationRuntime struct {
	Pipeline *appverification.Pipeline
	Store    *verificationpersistence.JSONLReportStore
}

func buildVerificationRuntime(cfg *config.Config, deps *Dependencies, l1Store *l1sqlite.L1SQLiteStore) verificationRuntime {
	policy := domainverification.VerificationPolicy{
		Enabled: cfg.Verification.Enabled,
		Mode:    cfg.Verification.Mode,
		Default: parseVerificationTriggerLevel(cfg.Verification.DefaultLevel),
	}
	if !policy.Enabled {
		log.Println("Verification pipeline disabled")
		deps.verificationRecent = viewer.HandleVerificationUnavailable()
		deps.verificationDetail = viewer.HandleVerificationUnavailable()
		deps.verificationSummary = viewer.HandleVerificationUnavailable()
		return verificationRuntime{}
	}

	store, err := verificationpersistence.NewJSONLReportStore(cfg.Verification.ReportPath)
	if err != nil {
		log.Printf("Verification report store disabled: %v", err)
	}

	var evidenceReader appverification.EvidenceReader
	if l1Store != nil {
		evidenceReader = verificationpersistence.NewL1EvidenceReader(l1Store)
	}

	runtime := verificationRuntime{
		Store: store,
		Pipeline: appverification.NewPipeline(appverification.Options{
			Policy:         policy,
			EvidenceReader: evidenceReader,
			Repository:     store,
		}),
	}
	if store != nil {
		deps.verificationRecent = viewer.HandleVerificationRecent(store)
		deps.verificationDetail = viewer.HandleVerificationDetail(store)
		deps.verificationSummary = viewer.HandleVerificationSummary(store)
	} else {
		deps.verificationRecent = viewer.HandleVerificationUnavailable()
		deps.verificationDetail = viewer.HandleVerificationUnavailable()
		deps.verificationSummary = viewer.HandleVerificationUnavailable()
	}
	log.Printf("Verification pipeline enabled (mode=%s default_level=%s report_path=%s)", cfg.Verification.Mode, cfg.Verification.DefaultLevel, cfg.Verification.ReportPath)
	return runtime
}

func parseVerificationTriggerLevel(raw string) domainverification.TriggerLevel {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(domainverification.TriggerHigh):
		return domainverification.TriggerHigh
	case string(domainverification.TriggerMedium):
		return domainverification.TriggerMedium
	default:
		return domainverification.TriggerLow
	}
}
