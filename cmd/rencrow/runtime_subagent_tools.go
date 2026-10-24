package main

import (
	"context"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	healthadapter "github.com/Nyukimin/RenCrow_CORE/internal/adapter/health"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domainhealth "github.com/Nyukimin/RenCrow_CORE/internal/domain/health"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	infrahealth "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/health"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// buildHealthHandler は Health Check HTTP ハンドラを構築
func (d *Dependencies) buildHealthHandler(cfg *config.Config) *healthadapter.Handler {
	return healthadapter.NewHandler(buildHealthService(cfg, d.runtimeHealthChecks()...))
}

// runtimeHealthChecks are the health checks of what this process has open. The CLI
// health commands build the service without them: they open no store.
func (d *Dependencies) runtimeHealthChecks() []domainhealth.Check {
	var checks []domainhealth.Check
	if d == nil {
		return checks
	}
	if d.persistenceQueue != nil {
		checks = append(checks, newRuntimePersistenceQueueCheck(d.persistenceQueue, d.eventRelay))
	}
	if d.actionStoreHealth != nil {
		checks = append(checks, infrahealth.NewActionStoreCheck(d.actionStoreHealth))
	}
	if store, ok := d.canonicalEventStore.(infrahealth.SQLiteCheckpointStatusReporter); ok {
		checks = append(checks, infrahealth.NewSQLiteCheckpointCheck("event_store_checkpoint", store))
	}
	if store, ok := d.personaRuntimeStore.(infrahealth.SQLiteCheckpointStatusReporter); ok {
		checks = append(checks, infrahealth.NewSQLiteCheckpointCheck("persona_architecture_checkpoint", store))
	}
	if store, ok := d.aiWorkflowStateStore.(infrahealth.SQLiteCheckpointStatusReporter); ok {
		checks = append(checks, infrahealth.NewSQLiteCheckpointCheck("ai_workflow_checkpoint", store))
	}
	return checks
}

type runtimePersistenceQueueCheck struct {
	queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]
	relay interface{}
}

func newRuntimePersistenceQueueCheck(queue *persistencequeue.Queue[persistencequeue.PersistenceCommand], relay interface{}) *runtimePersistenceQueueCheck {
	return &runtimePersistenceQueueCheck{queue: queue, relay: relay}
}

func (c *runtimePersistenceQueueCheck) Name() string { return "runtime_persistence_queue" }

func (c *runtimePersistenceQueueCheck) Run(context.Context) domainhealth.CheckResult {
	started := time.Now()
	status := persistencequeue.Status{}
	if c != nil && c.queue != nil {
		status = c.queue.Status()
	}
	oldestAge := int64(0)
	if !status.OldestPendingAt.IsZero() {
		age := started.Sub(status.OldestPendingAt)
		if age > 0 {
			oldestAge = age.Milliseconds()
		}
	}
	durableEventSeq := modulecore.EventSeq(0)
	if reporter, ok := c.relay.(interface{ DurableEventSeq() modulecore.EventSeq }); ok {
		durableEventSeq = reporter.DurableEventSeq()
	}
	resultStatus := domainhealth.StatusOK
	if !status.Accepting || status.Failure != nil {
		resultStatus = domainhealth.StatusDegraded
	}
	failedSequence := uint64(0)
	if status.Failure != nil {
		failedSequence = status.Failure.Sequence
	}
	return domainhealth.CheckResult{
		Name: c.Name(), Status: resultStatus, Duration: time.Since(started),
		Details: map[string]any{
			"accepting":             status.Accepting,
			"pending_commands":      status.Pending,
			"reserved_commands":     status.Reserved,
			"pending_bytes":         status.PendingBytes,
			"reserved_bytes":        status.ReservedBytes,
			"oldest_pending_age_ms": oldestAge,
			"in_flight":             status.InFlight,
			"failed":                status.Failure != nil,
			"failed_sequence":       failedSequence,
			"durable_event_seq":     durableEventSeq,
		},
	}
}

// resolveSubagentProvider はRenCrow_LLM WorkerのToolCallingProviderを使用する。
func resolveSubagentProvider(_ *config.Config, fallback llm.ToolCallingProvider) llm.ToolCallingProvider {
	return fallback
}

// mustGetToolList はツールリストを取得（エラーは無視）
func mustGetToolList(runner tool.RunnerV2) []string {
	metas, _ := runner.ListTools(context.Background())
	list := make([]string, 0, len(metas))
	for _, meta := range metas {
		list = append(list, meta.ToolID)
	}
	return list
}
