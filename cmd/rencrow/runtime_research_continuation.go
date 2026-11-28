package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

type authorizedResearchContinuation struct {
	Task       domaintask.Task
	PriorRun   domaintask.Run
	Actor      string
	Envelope   c.Envelope
	Additional int
}

var errResearchContinuationUnavailable = errors.New("Research continuation owner proof is unavailable")

// authorizeResearchContinuation binds a read-only Engine status to the exact
// current Task/Run owner state before seed or resume can write. User identity
// comes from immutable Task provenance; the execution Actor comes from the
// persisted Task and Run, never from the HTTP request.
func authorizeResearchContinuation(
	ctx context.Context,
	engineOwner *engine.Engine,
	tasks *taskmanager.Manager,
	ownerID, currentPolicy string,
	source c.Result,
) (authorizedResearchContinuation, error) {
	if ctx == nil || ctx.Err() != nil || engineOwner == nil || engineOwner.Ports.Store == nil || tasks == nil {
		return authorizedResearchContinuation{}, continuationUnavailable("owner dependencies")
	}
	record, err := engineOwner.Ports.Store.Load(ctx, source.RunID)
	if err != nil {
		return authorizedResearchContinuation{}, err
	}
	original := record.Envelope
	bound := original
	if record.Invocation != nil {
		bound = *record.Invocation
	}
	if original.TaskID == "" || original.RunID == "" || original.OwnerID != ownerID || original.DataScope != "user" || original.PolicyRevision != currentPolicy ||
		bound.TaskID != original.TaskID || bound.OwnerID != ownerID || bound.DataScope != "user" || bound.PolicyRevision != currentPolicy || bound.Actor == "" || bound.Actor != original.Actor ||
		currentPolicy == "" || engineOwner.PolicyRevision != "" && engineOwner.PolicyRevision != currentPolicy ||
		source.SchemaVersion != c.ResultSchema || source.RunID != record.Result.RunID || source.TaskID != original.TaskID ||
		source.ExecutionRunID != bound.RunID || record.Result.ExecutionRunID != bound.RunID || source.Status != record.Result.Status {
		return authorizedResearchContinuation{}, continuationUnavailable("frozen envelope/result binding")
	}
	requestBytes, err := json.Marshal(record.Request)
	if err != nil {
		return authorizedResearchContinuation{}, continuationUnavailable("frozen request digest")
	}
	requestDigest := sha256.Sum256(requestBytes)
	if hex.EncodeToString(requestDigest[:]) != record.RequestHash {
		return authorizedResearchContinuation{}, continuationUnavailable("stored source result")
	}
	sourceJSON, err := json.Marshal(source)
	if err != nil {
		return authorizedResearchContinuation{}, continuationUnavailable("Task owner scope or lifecycle")
	}
	storedJSON, err := json.Marshal(record.Result)
	if err != nil || !bytes.Equal(sourceJSON, storedJSON) {
		return authorizedResearchContinuation{}, continuationUnavailable("Task Research receipt")
	}
	taskID := core.TaskID(original.TaskID)
	tasksByID, runsByID, err := tasks.ReadTaskRuns(ctx, []core.TaskID{taskID})
	if err != nil {
		return authorizedResearchContinuation{}, err
	}
	task, found := tasksByID[taskID]
	if !found || domaintask.IsOwnerManagedTask(task) || task.Activity != nil && task.Activity.Passive || task.Status == domaintask.StatusRunning ||
		!domaintask.ResearchResultDataScopeMatches(task, "user", ownerID) || task.ResearchResult == nil {
		return authorizedResearchContinuation{}, continuationUnavailable("Task Run history")
	}
	projection := task.ResearchResult
	if projection.Validate(task) != nil || projection.OwnerID != ownerID || projection.DataScope != "user" ||
		projection.ActorID != task.Assignee || projection.ExecutionRunID != core.RunID(bound.RunID) ||
		projection.ResearchRunID != source.RunID || projection.Status != source.Status {
		return authorizedResearchContinuation{}, errResearchContinuationUnavailable
	}
	runs := runsByID[taskID]
	if len(runs) == 0 {
		return authorizedResearchContinuation{}, errResearchContinuationUnavailable
	}
	latest := runs[0]
	var prior domaintask.Run
	seen := make(map[core.RunID]struct{}, len(runs))
	for _, run := range runs {
		if run.Validate() != nil || run.TaskID != taskID {
			return authorizedResearchContinuation{}, continuationUnavailable("Task Run identity")
		}
		if _, duplicate := seen[run.RunID]; duplicate {
			return authorizedResearchContinuation{}, continuationUnavailable("Task Run duplication")
		}
		seen[run.RunID] = struct{}{}
		if run.StartedAt.Equal(latest.StartedAt) && run.RunID != latest.RunID {
			return authorizedResearchContinuation{}, continuationUnavailable("Task Run ordering")
		}
		if run.StartedAt.After(latest.StartedAt) {
			latest = run
		}
		if run.RunID == core.RunID(bound.RunID) {
			prior = run
		}
	}
	if prior.RunID == "" || prior.RunID != latest.RunID || prior.Assignee != bound.Actor || prior.Status == domaintask.RunStatusRunning ||
		prior.TraceID != "" && prior.TraceID != core.TraceID(bound.TraceID) || !researchContinuationTaskRunStateMatches(task.Status, prior.Status, source.Status) {
		return authorizedResearchContinuation{}, continuationUnavailable("latest Task Run or source status")
	}
	return authorizedResearchContinuation{Task: task, PriorRun: prior, Actor: task.Assignee, Envelope: bound, Additional: len(record.AdditionalSeeds)}, nil
}

func researchContinuationTaskRunStateMatches(taskStatus domaintask.Status, runStatus domaintask.RunStatus, researchStatus string) bool {
	switch researchStatus {
	case "waiting_input":
		return taskStatus == domaintask.StatusWaiting && runStatus == domaintask.RunStatusWaiting
	case "partial", "failed":
		return taskStatus == domaintask.StatusFailed && runStatus == domaintask.RunStatusFailed
	default:
		return false
	}
}

func continuationUnavailable(reason string) error {
	return fmt.Errorf("%w: %s", errResearchContinuationUnavailable, reason)
}

func startResearchContinuation(
	ctx context.Context,
	tasks *taskmanager.Manager,
	authorized authorizedResearchContinuation,
	ownerID string,
) (domaintask.Run, error) {
	return tasks.StartResearchContinuation(ctx, authorized.Task.TaskID, authorized.PriorRun.RunID, ownerID, authorized.Actor)
}

func researchContinuationErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, taskmanager.ErrResearchContinuationConflict) || errors.Is(err, errResearchContinuationUnavailable) {
		return "task_continuation_not_admissible"
	}
	return "task_continuation_unavailable"
}

func researchContinuationHTTPStatus(err error) int {
	if errors.Is(err, taskmanager.ErrResearchContinuationConflict) || errors.Is(err, errResearchContinuationUnavailable) {
		return http.StatusConflict
	}
	return http.StatusServiceUnavailable
}
