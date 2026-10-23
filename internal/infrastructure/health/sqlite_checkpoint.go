package health

import (
	"context"
	"strings"
	"time"

	domainhealth "github.com/Nyukimin/RenCrow_CORE/internal/domain/health"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence"
)

type SQLiteCheckpointStatusReporter interface {
	CheckpointStatus() persistence.SQLiteWALCheckpointStatus
}

type SQLiteCheckpointCheck struct {
	name  string
	store SQLiteCheckpointStatusReporter
}

func NewSQLiteCheckpointCheck(name string, store SQLiteCheckpointStatusReporter) *SQLiteCheckpointCheck {
	return &SQLiteCheckpointCheck{name: name, store: store}
}

func (c *SQLiteCheckpointCheck) Name() string { return c.name }

func (c *SQLiteCheckpointCheck) ReadinessOptional() bool { return true }

func (c *SQLiteCheckpointCheck) Run(context.Context) domainhealth.CheckResult {
	started := time.Now()
	var status persistence.SQLiteWALCheckpointStatus
	if c != nil && c.store != nil {
		status = c.store.CheckpointStatus()
	}

	result := "pending"
	if status.CheckpointAttempted {
		switch status.LastErrorClass {
		case persistence.SQLiteWALCheckpointErrorNone:
			if !status.LastSuccessAt.IsZero() {
				result = "complete"
			} else {
				result = "error"
			}
		case persistence.SQLiteWALCheckpointErrorPartial, persistence.SQLiteWALCheckpointErrorBusy:
			result = "partial"
		default:
			result = "error"
		}
	}

	interval := persistence.SQLiteWALCheckpointInterval
	degraded := result == "partial" || result == "error"
	if !status.LastSuccessAt.IsZero() {
		if elapsedSince(status.LastSuccessAt, started) > interval {
			degraded = true
		}
	} else if status.OpenedAt.IsZero() || elapsedSince(status.OpenedAt, started) > interval {
		degraded = true
	}

	var successAge any
	var lastSuccessAt any
	if !status.LastSuccessAt.IsZero() {
		successAge = elapsedSince(status.LastSuccessAt, started).Seconds()
		lastSuccessAt = status.LastSuccessAt.UTC().Format(time.RFC3339Nano)
	}
	checkStatus := domainhealth.StatusOK
	if degraded {
		checkStatus = domainhealth.StatusDegraded
	}
	journalMode := "unknown"
	if strings.EqualFold(status.JournalMode, "wal") {
		journalMode = "wal"
	}

	return domainhealth.CheckResult{
		Name: c.Name(), Status: checkStatus, Message: "checkpoint=" + result,
		Duration: time.Since(started),
		Details: map[string]any{
			"last_success_age_sec":   successAge,
			"target_interval_sec":    int64(interval.Seconds()),
			"last_checkpoint_result": result,
			"journal_mode":           journalMode,
			"synchronous":            status.Synchronous,
			"wal_autocheckpoint":     status.WALAutoCheckpoint,
			"busy":                   status.Busy,
			"log_frames":             status.LogFrames,
			"checkpointed_frames":    status.CheckpointedFrames,
			"checkpoint_duration_ms": status.Duration.Milliseconds(),
			"last_success_at":        lastSuccessAt,
			"error_class":            boundedCheckpointErrorClass(status.LastErrorClass),
		},
	}
}

func boundedCheckpointErrorClass(class persistence.SQLiteWALCheckpointErrorClass) string {
	switch class {
	case persistence.SQLiteWALCheckpointErrorNone,
		persistence.SQLiteWALCheckpointErrorSQLite,
		persistence.SQLiteWALCheckpointErrorContext,
		persistence.SQLiteWALCheckpointErrorBusy,
		persistence.SQLiteWALCheckpointErrorPartial:
		return string(class)
	default:
		return "unknown"
	}
}

func elapsedSince(then, now time.Time) time.Duration {
	elapsed := now.Sub(then)
	if elapsed < 0 {
		return 0
	}
	return elapsed
}
