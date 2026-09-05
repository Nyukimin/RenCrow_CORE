package superagent

import (
	"context"
	"fmt"
	"strings"
	"time"

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
)

type InterruptedRunRecoveryStore interface {
	ListAgentRuns(context.Context, int) ([]domainsuperagent.AgentRun, error)
	SaveAgentRun(context.Context, domainsuperagent.AgentRun) error
	ListRunQueueItems(context.Context, int) ([]domainsuperagent.RunQueueItem, error)
	SaveRunQueueItem(context.Context, domainsuperagent.RunQueueItem) error
}

// RecoverInterruptedAgentRuns rebuilds only a deterministic resume intent from
// a durable checkpoint. It never infers progress from a running status alone.
func RecoverInterruptedAgentRuns(ctx context.Context, store InterruptedRunRecoveryStore, now time.Time) (queued int, blocked int, err error) {
	if store == nil {
		return 0, 0, fmt.Errorf("interrupted run recovery store is unavailable")
	}
	runs, err := store.ListAgentRuns(ctx, 500)
	if err != nil {
		return 0, 0, err
	}
	queue, err := store.ListRunQueueItems(ctx, 500)
	if err != nil {
		return 0, 0, err
	}
	queueByID := make(map[string]domainsuperagent.RunQueueItem, len(queue))
	for _, item := range queue {
		queueByID[item.QueueID] = item
	}
	seenRuns := make(map[string]struct{}, len(runs))
	for _, run := range runs {
		if _, seen := seenRuns[string(run.RunID)]; seen {
			continue
		}
		seenRuns[string(run.RunID)] = struct{}{}
		if run.ResumePolicy == "checkpoint" && run.CheckpointRevision > 0 {
			queueID := fmt.Sprintf("resume:%s:%d", run.RunID, run.CheckpointRevision)
			if existing, ok := queueByID[queueID]; ok {
				if existing.Status == "completed" || existing.Status == "failed" || existing.Status == "cancelled" {
					if run.Status != "completed" && run.Status != "failed" && run.Status != "cancelled" {
						run.Status, run.Summary, run.CompletedAt = existing.Status, existing.Reason, existing.CompletedAt
						if err := store.SaveAgentRun(ctx, run); err != nil {
							return queued, blocked, err
						}
					}
				} else if run.Status == "completed" || run.Status == "failed" || run.Status == "cancelled" {
					existing.Status, existing.Reason, existing.CompletedAt = run.Status, run.Summary, run.CompletedAt
					existing.LeaseToken, existing.LeaseUntil = "", time.Time{}
					if err := store.SaveRunQueueItem(ctx, existing); err != nil {
						return queued, blocked, err
					}
					queueByID[queueID] = existing
				}
			}
		}
		if run.Status != "running" {
			continue
		}
		if run.ResumePolicy != "checkpoint" || run.CheckpointRevision <= 0 || strings.TrimSpace(run.CheckpointSummary) == "" || strings.TrimSpace(run.NextAction) == "" || run.LastCheckpointAt.IsZero() {
			run.Status = "blocked"
			run.Summary = "restart resume blocked: durable checkpoint is unavailable"
			if err := store.SaveAgentRun(ctx, run); err != nil {
				return queued, blocked, err
			}
			blocked++
			continue
		}
		queueID := fmt.Sprintf("resume:%s:%d", run.RunID, run.CheckpointRevision)
		if existing, ok := queueByID[queueID]; ok {
			if existing.Status == "completed" {
				run.Status, run.Summary, run.CompletedAt = "completed", existing.Reason, existing.CompletedAt
				if err := store.SaveAgentRun(ctx, run); err != nil {
					return queued, blocked, err
				}
			}
			continue
		}
		item := domainsuperagent.RunQueueItem{
			QueueID: queueID, RunID: string(run.RunID), WorkstreamID: run.WorkstreamID,
			Goal: run.Goal, Action: "resume", Status: "queued",
			CheckpointRevision: run.CheckpointRevision, CheckpointSummary: run.CheckpointSummary,
			NextAction: run.NextAction, IdempotencyKey: queueID, CreatedAt: now.UTC(),
		}
		if err := store.SaveRunQueueItem(ctx, item); err != nil {
			return queued, blocked, err
		}
		queueByID[queueID] = item
		queued++
	}
	return queued, blocked, nil
}
