package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	coreresearch "github.com/Nyukimin/RenCrow_CORE/internal/application/research"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
)

const researchIndexAdmissionKind = "index_build"

type researchDocumentIndexBuilder interface {
	Build(context.Context, c.Envelope, coreresearch.ResearchDocumentIndexRequest) (coreresearch.ResearchDocumentIndexResult, error)
}

func handleResearchIndexOperation(
	w http.ResponseWriter,
	r *http.Request,
	input researchOwnerRequest,
	caller c.Envelope,
	owner string,
	tasks *taskmanager.Manager,
	builder researchDocumentIndexBuilder,
) bool {
	if input.Operation != "index_build" && input.Operation != "index_status" {
		return false
	}
	fail := func(status int, code string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "failed", "error": code})
	}
	if input.OperationKey == "" || input.RunID != "" || input.WatchID != "" || input.URL != "" || input.Origin != "" || len(input.Request) == 0 {
		fail(http.StatusBadRequest, "mixed_operation_fields")
		return true
	}
	request, err := c.DecodeIndexRequest(input.Request)
	if err != nil {
		fail(http.StatusBadRequest, "invalid_index_request")
		return true
	}
	request, _, requestDigest, err := c.CanonicalizeIndexRequest(request)
	if err != nil {
		fail(http.StatusBadRequest, "invalid_index_request")
		return true
	}
	if input.Operation == "index_status" {
		admission, found, err := tasks.LookupResearchAdmission(r.Context(), owner, input.OperationKey, requestDigest, researchIndexAdmissionKind)
		if err != nil {
			fail(http.StatusConflict, "admission_conflict_or_unavailable")
			return true
		}
		response := newIndexOperationResponse(input.OperationKey, requestDigest)
		if !found {
			response.State = "not_found"
			_ = json.NewEncoder(w).Encode(response)
			return true
		}
		response = readResearchIndexOperation(r.Context(), tasks, owner, input.OperationKey, requestDigest, len(request.EventIDs), admission)
		_ = json.NewEncoder(w).Encode(response)
		return true
	}

	admission, found, err := tasks.LookupResearchAdmission(r.Context(), owner, input.OperationKey, requestDigest, researchIndexAdmissionKind)
	if err != nil {
		fail(http.StatusConflict, "admission_conflict_or_unavailable")
		return true
	}
	if found {
		response := readResearchIndexOperation(r.Context(), tasks, owner, input.OperationKey, requestDigest, len(request.EventIDs), admission)
		_ = json.NewEncoder(w).Encode(response)
		return true
	}
	if builder == nil {
		fail(http.StatusServiceUnavailable, "index_builder_unavailable")
		return true
	}
	admission, created, err := tasks.EnsureResearchAdmission(r.Context(), owner, input.OperationKey, requestDigest, caller.PolicyRevision, researchIndexAdmissionKind)
	if err != nil {
		fail(http.StatusConflict, "admission_conflict_or_unavailable")
		return true
	}
	if !created {
		response := readResearchIndexOperation(r.Context(), tasks, owner, input.OperationKey, requestDigest, len(request.EventIDs), admission)
		_ = json.NewEncoder(w).Encode(response)
		return true
	}
	if !researchIndexAdmissionIsRunning(r.Context(), tasks, owner, admission) {
		response := indexPendingResponse(input.OperationKey, requestDigest, admission)
		_ = json.NewEncoder(w).Encode(response)
		return true
	}

	guard := newResearchStartedRunGuard(tasks, admission.TaskID, admission.InitialRunID, owner, r.Context())
	defer guard.DeferFallback()
	indexEnvelope := caller
	indexEnvelope.TaskID, indexEnvelope.RunID = string(admission.TaskID), string(admission.InitialRunID)
	indexEnvelope.TraceID, indexEnvelope.RequestID = string(admission.TraceID), string(admission.RequestID)
	indexEnvelope.PolicyRevision = admission.PolicyRevision
	built, buildErr := builder.Build(r.Context(), indexEnvelope, coreresearch.ResearchDocumentIndexRequest{EventIDs: request.EventIDs})
	terminal := researchIndexTerminalResult(owner, requestDigest, len(request.EventIDs), admission, built, buildErr)
	encoded, err := json.Marshal(terminal)
	if err != nil || terminal.Validate() != nil {
		summary := fmt.Sprintf("index result receipt unavailable; published documents=%d published chunks=%d", built.DocumentsIndexed, built.ChunksIndexed)
		_ = guard.Close(domaintask.StatusFailed, summary, "", errors.New("research index result unavailable"))
		_ = json.NewEncoder(w).Encode(indexPendingResponse(input.OperationKey, requestDigest, admission))
		return true
	}
	status := researchIndexTaskStatus(terminal.Status)
	_ = guard.Close(status, string(encoded), "", buildErr)
	if !guard.closed {
		_ = json.NewEncoder(w).Encode(indexPendingResponse(input.OperationKey, requestDigest, admission))
		return true
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer cancel()
	response := readResearchIndexOperation(readCtx, tasks, owner, input.OperationKey, requestDigest, len(request.EventIDs), admission)
	_ = json.NewEncoder(w).Encode(response)
	return true
}

func newIndexOperationResponse(operationKey, requestDigest string) c.IndexOperationResponse {
	return c.IndexOperationResponse{
		SchemaVersion: c.IndexOperationSchema,
		OperationKey:  operationKey,
		RequestDigest: requestDigest,
	}
}

func indexPendingResponse(operationKey, requestDigest string, admission taskmanager.ResearchAdmissionReceipt) c.IndexOperationResponse {
	response := newIndexOperationResponse(operationKey, requestDigest)
	response.State, response.ErrorCode = "pending", "outcome_unknown"
	response.Admission = indexAdmissionReceipt(admission)
	return response
}

func indexAdmissionReceipt(admission taskmanager.ResearchAdmissionReceipt) *c.IndexAdmissionReceipt {
	return &c.IndexAdmissionReceipt{
		TaskID: string(admission.TaskID), InitialRunID: string(admission.InitialRunID),
		TraceID: string(admission.TraceID), RequestID: string(admission.RequestID),
		PolicyRevision: admission.PolicyRevision, RequestDigest: admission.RequestDigest,
		CreatedAt: admission.CreatedAt,
	}
}

func researchIndexAdmissionIsRunning(ctx context.Context, tasks *taskmanager.Manager, owner string, admission taskmanager.ResearchAdmissionReceipt) bool {
	task, err := tasks.Get(ctx, admission.TaskID)
	if err != nil || !validResearchIndexTask(task, owner, admission) || task.Status != domaintask.StatusRunning {
		return false
	}
	run, err := tasks.GetRun(ctx, admission.InitialRunID)
	return err == nil && validResearchIndexRun(run, owner, admission) && run.Status == domaintask.RunStatusRunning && run.CompletedAt == nil
}

func readResearchIndexOperation(
	ctx context.Context,
	tasks *taskmanager.Manager,
	owner, operationKey, requestDigest string,
	expectedDocuments int,
	admission taskmanager.ResearchAdmissionReceipt,
) c.IndexOperationResponse {
	response := indexPendingResponse(operationKey, requestDigest, admission)
	task, err := tasks.Get(ctx, admission.TaskID)
	if err != nil || !validResearchIndexTask(task, owner, admission) {
		return response
	}
	run, err := tasks.GetRun(ctx, admission.InitialRunID)
	if err != nil || !validResearchIndexRun(run, owner, admission) || run.Status == domaintask.RunStatusRunning || run.CompletedAt == nil {
		return response
	}
	var result c.IndexBuildResult
	decoder := json.NewDecoder(bytes.NewReader([]byte(run.Summary)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return response
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return response
	}
	canonical, err := json.Marshal(result)
	if err != nil || string(canonical) != run.Summary || result.Validate() != nil ||
		result.TaskID != string(admission.TaskID) || result.RunID != string(admission.InitialRunID) ||
		result.OwnerID != owner || result.DataScope != "user" || result.RequestDigest != requestDigest ||
		result.PublishedDocuments > expectedDocuments ||
		result.Status == "succeeded" && result.PublishedDocuments != expectedDocuments ||
		!researchIndexTaskRunStatusMatches(task.Status, run.Status, result.Status) ||
		task.Summary != run.Summary {
		return response
	}
	response.State, response.ErrorCode = "completed", ""
	response.Result = &result
	return response
}

func validResearchIndexTask(task domaintask.Task, owner string, admission taskmanager.ResearchAdmissionReceipt) bool {
	return task.TaskID == admission.TaskID && task.Route == domaintask.RouteResearch &&
		task.OwnerID == owner && task.Assignee == owner && task.PrivateAccess == nil && task.ResearchResult == nil &&
		task.ResearchBinding != nil && task.ResearchBinding.DataScope == "user" &&
		task.ResearchBinding.OwnerID == owner && task.ResearchBinding.OwnerRoute == "core.research.local" &&
		task.ResearchBinding.Validate(task) == nil
}

func validResearchIndexRun(run domaintask.Run, owner string, admission taskmanager.ResearchAdmissionReceipt) bool {
	return run.RunID == admission.InitialRunID && run.TaskID == admission.TaskID && run.Assignee == owner &&
		run.TraceID == admission.TraceID && run.StartReason == domaintask.RunStartReasonFirst && run.Validate() == nil
}

func researchIndexTaskRunStatusMatches(taskStatus domaintask.Status, runStatus domaintask.RunStatus, indexStatus string) bool {
	switch indexStatus {
	case "succeeded":
		return taskStatus == domaintask.StatusSucceeded && runStatus == domaintask.RunStatusSucceeded
	case "partial", "failed":
		return taskStatus == domaintask.StatusFailed && runStatus == domaintask.RunStatusFailed
	case "cancelled":
		return taskStatus == domaintask.StatusCancelled && runStatus == domaintask.RunStatusCancelled
	default:
		return false
	}
}

func researchIndexTerminalResult(
	owner, requestDigest string,
	expectedDocuments int,
	admission taskmanager.ResearchAdmissionReceipt,
	built coreresearch.ResearchDocumentIndexResult,
	buildErr error,
) c.IndexBuildResult {
	result := c.IndexBuildResult{
		SchemaVersion:      c.IndexResultSchema,
		Status:             "failed",
		TaskID:             string(admission.TaskID),
		RunID:              string(admission.InitialRunID),
		OwnerID:            owner,
		DataScope:          "user",
		RequestDigest:      requestDigest,
		PublishedDocuments: built.DocumentsIndexed,
		PublishedChunks:    built.ChunksIndexed,
	}
	if errors.Is(buildErr, context.Canceled) {
		result.Status, result.ErrorCode = "cancelled", "cancelled"
	} else if buildErr != nil || built.DocumentsIndexed != expectedDocuments {
		result.ErrorCode = "index_build_failed"
		if built.DocumentsIndexed > 0 || built.ChunksIndexed > 0 {
			result.Status = "partial"
		}
	} else {
		result.Status, result.ReadbackVerified = "succeeded", true
		result.ModelID, result.AssetRevision = built.ModelID, built.AssetRevision
		result.IndexRevision, result.Dimension = built.IndexRevision, built.Dimension
		if result.Validate() != nil {
			result.ReadbackVerified, result.ErrorCode = false, "index_build_failed"
			result.Status = "failed"
			result.ModelID, result.AssetRevision, result.IndexRevision, result.Dimension = "", "", "", 0
			if built.DocumentsIndexed > 0 || built.ChunksIndexed > 0 {
				result.Status = "partial"
			}
		}
	}
	return result
}

func researchIndexTaskStatus(indexStatus string) domaintask.Status {
	switch indexStatus {
	case "succeeded":
		return domaintask.StatusSucceeded
	case "cancelled":
		return domaintask.StatusCancelled
	default:
		return domaintask.StatusFailed
	}
}
