package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	coreresearch "github.com/Nyukimin/RenCrow_CORE/internal/application/research"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

type researchOwnerRequest struct {
	Operation    string          `json:"operation"`
	OperationKey string          `json:"operation_key,omitempty"`
	Request      json.RawMessage `json:"request,omitempty"`
	RunID        string          `json:"run_id,omitempty"`
	WatchID      string          `json:"watch_id,omitempty"`
	URL          string          `json:"url,omitempty"`
	Origin       string          `json:"origin,omitempty"`
}

type researchWatchManagementResponse struct {
	SchemaVersion string                           `json:"schema_version"`
	Item          coreresearch.WatchManagementItem `json:"item"`
}

type researchDueWatch struct {
	WatchID      string    `json:"watch_id"`
	NextDueAt    time.Time `json:"next_due_at"`
	OperationKey string    `json:"operation_key"`
}

type researchDueResponse struct {
	SchemaVersion  string             `json:"schema_version"`
	PolicyRevision string             `json:"policy_revision"`
	Items          []researchDueWatch `json:"items"`
}

type researchAdmissionResponse struct {
	SchemaVersion string                               `json:"schema_version"`
	State         string                               `json:"state"` // completed, pending, or not_found
	OperationKey  string                               `json:"operation_key"`
	RequestDigest string                               `json:"request_digest"`
	Mode          string                               `json:"mode"`
	Admission     taskmanager.ResearchAdmissionReceipt `json:"admission,omitempty"`
	Result        *c.Result                            `json:"result,omitempty"`
}

// This is a local authenticated business-task owner, not a public URL-fetch
// proxy. It accepts only versioned Research requests and returns no raw paths.
func newResearchOwnerHandler(cfg *config.Config, e *engine.Engine, tasks *taskmanager.Manager, resultRecorder tools.ResearchResultRecorder, indexBuilder researchDocumentIndexBuilder, structuredOwners ...*researchStructuredIndexOwner) (http.HandlerFunc, error) {
	if e == nil {
		return nil, nil
	}
	if !cfg.LocalAgentOps.Enabled || tasks == nil {
		return nil, fmt.Errorf("research requires the existing authenticated local principal and Task owner")
	}
	token, err := readAgentOpsToken(cfg.LocalAgentOps.AuthTokenFile)
	if err != nil {
		return nil, err
	}
	owner := cfg.LocalAgentOps.UserID
	var structuredOwner *researchStructuredIndexOwner
	if len(structuredOwners) > 0 {
		structuredOwner = structuredOwners[0]
	}
	var recorder tools.ResearchResultRecorder = &coreresearch.ResultRecorder{Store: e.Ports.Store, Tasks: tasks}
	if resultRecorder != nil {
		recorder = resultRecorder
	}
	var activeMu sync.Mutex
	active := map[string]bool{}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fail := func(status int, code string) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "failed", "error": code})
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			fail(http.StatusForbidden, "local_principal_required")
			return
		}
		if r.Method != http.MethodPost {
			fail(http.StatusMethodNotAllowed, "post_required")
			return
		}
		if !authorizedAgentOpsBearer(r.Header, token) {
			fail(http.StatusUnauthorized, "unauthorized")
			return
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, researchOwnerMaxRequestBytes))
		decoder.DisallowUnknownFields()
		var input researchOwnerRequest
		if err := decoder.Decode(&input); err != nil {
			fail(http.StatusBadRequest, "invalid_request")
			return
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			fail(http.StatusBadRequest, "trailing_json")
			return
		}
		caller := c.Envelope{OwnerID: owner, Actor: owner, DataScope: "user", PolicyRevision: tools.ResearchPolicyRevision}
		if e.PolicyRevision != "" {
			caller.PolicyRevision = e.PolicyRevision
		}
		if input.Operation != "source_register" && len(input.Request) > 64*1024 {
			fail(http.StatusRequestEntityTooLarge, "request_too_large")
			return
		}
		if structuredOwner != nil && structuredOwner.handleOperation(w, r, input, caller, owner) {
			return
		}
		if handleResearchIndexOperation(w, r, input, caller, owner, tasks, indexBuilder) {
			return
		}
		if input.Operation == "doctor" {
			if input.OperationKey != "" || input.RunID != "" || input.WatchID != "" || len(input.Request) != 0 || input.URL != "" || input.Origin != "" {
				fail(http.StatusBadRequest, "mixed_operation_fields")
				return
			}
			_ = json.NewEncoder(w).Encode(e.Doctor())
			return
		}
		if input.Operation == "refresh_due" || input.Operation == "watch_status" || input.Operation == "watch_disable" {
			if input.OperationKey != "" || input.RunID != "" || len(input.Request) != 0 || input.URL != "" || input.Origin != "" || input.Operation == "refresh_due" && input.WatchID != "" || input.Operation != "refresh_due" && input.WatchID == "" {
				fail(http.StatusBadRequest, "mixed_operation_fields")
				return
			}
			management, ok := e.Ports.Watch.(coreresearch.WatchManagementPort)
			if !ok {
				fail(http.StatusServiceUnavailable, "watch_management_unavailable")
				return
			}
			if input.Operation == "refresh_due" {
				items, listErr := management.ListDue(r.Context(), caller, 20)
				if listErr != nil {
					fail(http.StatusServiceUnavailable, "due_watch_list_unavailable")
					return
				}
				response := researchDueResponse{SchemaVersion: "rencrow.research.due.v1", PolicyRevision: caller.PolicyRevision, Items: make([]researchDueWatch, 0, len(items))}
				for _, item := range items {
					if err := item.ValidateFor(owner, caller.DataScope); err != nil || !item.Enabled || item.NextDueAt.After(time.Now().UTC()) {
						fail(http.StatusServiceUnavailable, "due_watch_receipt_invalid")
						return
					}
					response.Items = append(response.Items, researchDueWatch{WatchID: item.WatchID, NextDueAt: item.NextDueAt, OperationKey: researchRefreshOperationKey(item.WatchID, item.NextDueAt, caller.PolicyRevision)})
				}
				_ = json.NewEncoder(w).Encode(response)
				return
			}
			var item coreresearch.WatchManagementItem
			if input.Operation == "watch_status" {
				item, err = management.Read(r.Context(), caller, input.WatchID)
			} else {
				item, err = management.Disable(r.Context(), caller, input.WatchID)
			}
			if err != nil || item.ValidateFor(owner, caller.DataScope) != nil {
				fail(http.StatusNotFound, "watch_unavailable")
				return
			}
			_ = json.NewEncoder(w).Encode(researchWatchManagementResponse{SchemaVersion: "rencrow.research.watch-management.v1", Item: item})
			return
		}
		if input.Operation == "status" {
			if input.OperationKey != "" || input.WatchID != "" || len(input.Request) != 0 || input.URL != "" || input.Origin != "" || core.RunID(input.RunID).Validate() != nil {
				fail(http.StatusBadRequest, "mixed_operation_fields")
				return
			}
			result, err := e.Status(r.Context(), caller, input.RunID)
			if err != nil {
				fail(http.StatusNotFound, "run_unavailable")
				return
			}
			result, err = reconcileResearchOwnerStatus(r.Context(), recorder, caller, result)
			if err != nil {
				fail(http.StatusServiceUnavailable, "run_status_unavailable")
				return
			}
			_ = json.NewEncoder(w).Encode(result)
			return
		}
		if input.Operation == "seed" || input.Operation == "resume" {
			if input.OperationKey != "" || input.WatchID != "" || len(input.Request) != 0 || core.RunID(input.RunID).Validate() != nil || input.Operation == "resume" && (input.URL != "" || input.Origin != "") {
				fail(http.StatusBadRequest, "mixed_operation_fields")
				return
			}
			activeMu.Lock()
			if active[input.RunID] {
				activeMu.Unlock()
				fail(http.StatusConflict, "run_active")
				return
			}
			active[input.RunID] = true
			activeMu.Unlock()
			defer func() { activeMu.Lock(); delete(active, input.RunID); activeMu.Unlock() }()
			prior, err := e.Status(r.Context(), caller, input.RunID)
			if err != nil {
				fail(http.StatusNotFound, "run_unavailable")
				return
			}
			sourceResult := prior
			sourceStatus := prior.Status
			prior, err = reconcileResearchOwnerStatus(r.Context(), recorder, caller, prior)
			if err != nil {
				fail(http.StatusServiceUnavailable, "run_status_unavailable")
				return
			}
			if input.Operation == "seed" {
				if sourceStatus != "waiting_input" {
					fail(http.StatusConflict, "seed_not_admissible")
					return
				}
				authorized, authErr := authorizeResearchContinuation(r.Context(), e, tasks, owner, caller.PolicyRevision, sourceResult)
				if authErr != nil {
					fail(researchContinuationHTTPStatus(authErr), researchContinuationErrorCode(authErr))
					return
				}
				result, err := e.AddSeed(r.Context(), authorized.Envelope, input.RunID, input.URL, input.Origin)
				if err != nil {
					fail(http.StatusBadRequest, "seed_not_accepted")
					return
				}
				_ = json.NewEncoder(w).Encode(result)
				return
			}
			if sourceStatus == "succeeded" || sourceStatus == "cancelled" {
				_ = json.NewEncoder(w).Encode(prior)
				return
			}
			if sourceStatus != "waiting_input" && sourceStatus != "partial" && sourceStatus != "failed" {
				fail(http.StatusConflict, "task_continuation_not_admissible")
				return
			}
			authorized, authErr := authorizeResearchContinuation(r.Context(), e, tasks, owner, caller.PolicyRevision, sourceResult)
			if authErr != nil {
				fail(researchContinuationHTTPStatus(authErr), researchContinuationErrorCode(authErr))
				return
			}
			if sourceStatus == "waiting_input" && authorized.Additional == 0 {
				_ = json.NewEncoder(w).Encode(prior)
				return
			}
			run, err := startResearchContinuation(r.Context(), tasks, authorized, owner)
			if err != nil {
				fail(researchContinuationHTTPStatus(err), researchContinuationErrorCode(err))
				return
			}
			tid := authorized.Task.TaskID
			guard := newResearchStartedRunGuard(tasks, tid, run.RunID, authorized.Actor, r.Context())
			defer guard.DeferFallback()
			caller = authorized.Envelope
			caller.TaskID = string(tid)
			caller.RunID = string(run.RunID)
			caller.TraceID = string(core.NewTraceID())
			caller.RequestID = string(core.NewRequestID())
			result, runErr := e.Resume(r.Context(), caller, input.RunID)
			if runErr == nil {
				if err := recorder.Record(r.Context(), caller, result); err != nil {
					runErr = fmt.Errorf("Research Task projection unavailable")
				}
			}
			result = finishResearchOwnerTask(tid, input.RunID, guard, result, runErr)
			_ = json.NewEncoder(w).Encode(result)
			return
		}
		if input.Operation == "recall" || input.Operation == "watch" {
			if input.OperationKey != "" || input.RunID != "" || input.WatchID != "" || input.URL != "" || input.Origin != "" {
				fail(http.StatusBadRequest, "mixed_operation_fields")
				return
			}
			var recall c.RecallRequest
			var watch c.WatchRequest
			var err error
			if input.Operation == "recall" {
				err = decodeResearchOperation(input.Request, &recall)
			} else {
				err = decodeResearchOperation(input.Request, &watch)
			}
			if err != nil {
				fail(http.StatusBadRequest, "invalid_request")
				return
			}
			tid := core.NewTaskID()
			task, run, err := tasks.CreateAndStartRun(r.Context(), domaintask.Task{TaskID: tid, Title: "Research " + input.Operation, OwnerID: owner, Assignee: owner, ResearchBinding: researchLocalBinding(owner), Route: domaintask.RouteResearch, ReadOnly: input.Operation == "recall", Priority: domaintask.PriorityHigh}, domaintask.SharedRoleContext{TaskID: tid, UserIntent: "Authenticated Research operation"})
			if err != nil {
				fail(http.StatusServiceUnavailable, "task_admission_unavailable")
				return
			}
			guard := newResearchStartedRunGuard(tasks, task.TaskID, run.RunID, owner, r.Context())
			defer guard.DeferFallback()
			caller.TaskID = string(task.TaskID)
			caller.RunID = string(run.RunID)
			caller.TraceID = string(core.NewTraceID())
			caller.RequestID = string(core.NewRequestID())
			var payload any
			var status string
			if input.Operation == "recall" {
				result, callErr := e.Recall(r.Context(), caller, recall)
				payload = result
				status = result.Status
				err = callErr
			} else {
				result, callErr := e.Watch(r.Context(), caller, watch)
				payload = result
				status = result.Status
				err = callErr
			}
			outcome := researchTaskOutcome(status, err)
			encoded, _ := json.Marshal(map[string]string{"schema_version": "rencrow.research.task_summary.v1", "operation": input.Operation, "status": status})
			_ = guard.Close(outcome.status, string(encoded), outcome.waitingReason, err)
			if !guard.closed {
				fail(http.StatusServiceUnavailable, "operation_or_task_receipt_unavailable")
				return
			}
			_ = json.NewEncoder(w).Encode(payload)
			return
		}
		if input.Operation != "run" && input.Operation != "lookup" {
			fail(http.StatusServiceUnavailable, "operation_owner_not_connected")
			return
		}
		if input.OperationKey == "" || input.RunID != "" || input.WatchID != "" || input.URL != "" || input.Origin != "" {
			fail(http.StatusBadRequest, "mixed_operation_fields")
			return
		}
		request, err := engine.DecodeRequest(input.Request)
		if err != nil {
			fail(http.StatusBadRequest, "invalid_research_request")
			return
		}
		if input.Operation == "run" && request.Mode == "refresh" {
			management, ok := e.Ports.Watch.(coreresearch.WatchManagementPort)
			if !ok || request.WatchID == "" {
				fail(http.StatusConflict, "refresh_admission_unavailable")
				return
			}
			item, readErr := management.Read(r.Context(), caller, request.WatchID)
			if readErr != nil || item.ValidateFor(owner, caller.DataScope) != nil || !item.Enabled || item.NextDueAt.After(time.Now().UTC()) || input.OperationKey != researchRefreshOperationKey(item.WatchID, item.NextDueAt, caller.PolicyRevision) {
				fail(http.StatusConflict, "refresh_due_or_policy_changed")
				return
			}
		}
		canonicalRequest, err := json.Marshal(request)
		if err != nil {
			fail(http.StatusBadRequest, "invalid_research_request")
			return
		}
		digest := sha256.Sum256(canonicalRequest)
		requestDigest := hex.EncodeToString(digest[:])
		var admission taskmanager.ResearchAdmissionReceipt
		var created bool
		if input.Operation == "lookup" {
			var found bool
			admission, found, err = tasks.LookupResearchAdmission(r.Context(), owner, input.OperationKey, requestDigest, request.Mode)
			if err == nil && !found {
				response := researchAdmissionResponse{SchemaVersion: "rencrow.research.admission.v1", State: "not_found", OperationKey: input.OperationKey, RequestDigest: requestDigest, Mode: request.Mode}
				_ = json.NewEncoder(w).Encode(response)
				return
			}
			// A found lookup is still read-only; only EnsureResearchAdmission's
			// created result may enter the Engine execution branch below.
			created = false
		} else {
			admission, created, err = tasks.EnsureResearchAdmission(r.Context(), owner, input.OperationKey, requestDigest, caller.PolicyRevision, request.Mode)
		}
		if err != nil {
			fail(http.StatusConflict, "admission_conflict_or_unavailable")
			return
		}
		response := researchAdmissionResponse{SchemaVersion: "rencrow.research.admission.v1", OperationKey: input.OperationKey, RequestDigest: requestDigest, Mode: request.Mode}
		if !created {
			response.Admission = admission
			priorCaller := caller
			priorCaller.TaskID, priorCaller.RunID = string(admission.TaskID), string(admission.InitialRunID)
			priorCaller.TraceID, priorCaller.RequestID = string(admission.TraceID), string(admission.RequestID)
			priorCaller.PolicyRevision = admission.PolicyRevision
			prior, statusErr := e.Status(r.Context(), priorCaller, string(admission.InitialRunID))
			if statusErr != nil {
				response.State = "pending"
				_ = json.NewEncoder(w).Encode(response)
				return
			}
			prior, statusErr = reconcileResearchOwnerStatus(r.Context(), recorder, priorCaller, prior)
			if statusErr != nil {
				response.State = "pending"
				_ = json.NewEncoder(w).Encode(response)
				return
			}
			if prior.Status == "accepted" || prior.Status == "running" {
				response.State = "pending"
				_ = json.NewEncoder(w).Encode(response)
				return
			}
			response.State, response.Result = "completed", &prior
			_ = json.NewEncoder(w).Encode(response)
			return
		}
		tid := admission.TaskID
		runID := admission.InitialRunID
		run, err := tasks.GetRun(r.Context(), runID)
		if err != nil {
			response.State, response.Admission = "pending", admission
			_ = json.NewEncoder(w).Encode(response)
			return
		}
		guard := newResearchStartedRunGuard(tasks, tid, run.RunID, owner, r.Context())
		defer guard.DeferFallback()
		caller.TaskID = string(admission.TaskID)
		caller.RunID = string(admission.InitialRunID)
		caller.TraceID = string(admission.TraceID)
		caller.RequestID = string(admission.RequestID)
		caller.PolicyRevision = admission.PolicyRevision
		result, runErr := e.Run(r.Context(), caller, request)
		if runErr == nil {
			if err := recorder.Record(r.Context(), caller, result); err != nil {
				runErr = fmt.Errorf("Research Task projection unavailable")
			}
		}
		result = finishResearchOwnerTask(tid, result.RunID, guard, result, runErr)
		response.State, response.Admission, response.Result = "completed", admission, &result
		_ = json.NewEncoder(w).Encode(response)
	}, nil
}

func researchRefreshOperationKey(watchID string, nextDueAt time.Time, policyRevision string) string {
	identity := "rencrow.research.refresh-operation.v1\x00" + watchID + "\x00" + nextDueAt.UTC().Format(time.RFC3339Nano) + "\x00" + policyRevision
	digest := sha256.Sum256([]byte(identity))
	return "refresh_" + hex.EncodeToString(digest[:])
}

func researchLocalBinding(owner string) *domaintask.ResearchExecutionBinding {
	return &domaintask.ResearchExecutionBinding{SchemaVersion: domaintask.ResearchExecutionBindingSchema, DataScope: "user", OwnerID: owner, OwnerRoute: "core.research.local"}
}

func reconcileResearchOwnerStatus(ctx context.Context, recorder tools.ResearchResultRecorder, caller c.Envelope, result c.Result) (c.Result, error) {
	reconciler, ok := recorder.(tools.ResearchResultStatusReconciler)
	if !ok {
		return c.Result{}, fmt.Errorf("unavailable: Research Task status reconciliation owner")
	}
	return reconciler.Reconcile(ctx, caller, result)
}

func decodeResearchOperation(raw []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func finishResearchOwnerTask(tid core.TaskID, researchRunID string, guard *researchStartedRunGuard, result c.Result, runErr error) c.Result {
	if result.RunID == "" {
		result = c.Result{SchemaVersion: c.ResultSchema, RunID: researchRunID, TaskID: string(tid), Status: "failed", Missing: []string{"execution"}, Stages: []c.Stage{}, Errors: []c.Fault{}, Artifacts: []c.ArtifactRef{}, Receipts: []c.Receipt{}}
	}
	result.TaskID = string(tid)
	result.ExecutionRunID = string(guard.runID)
	if runErr != nil {
		result.Status = "failed"
		if result.Counts.DocumentsFetched > 0 {
			result.Status = "partial"
		}
		result.Missing = append(result.Missing, "execution_state")
	}
	summary, _ := json.Marshal(struct {
		SchemaVersion string   `json:"schema_version"`
		RunID         string   `json:"run_id"`
		Status        string   `json:"status"`
		Counts        c.Counts `json:"counts"`
		Missing       []string `json:"missing"`
	}{"rencrow.research.task_summary.v1", result.RunID, result.Status, result.Counts, result.Missing})
	status, waitingReason := domaintask.StatusFailed, ""
	if result.Status == "succeeded" {
		status = domaintask.StatusSucceeded
	} else if result.Status == "waiting_input" {
		status = domaintask.StatusWaiting
		waitingReason = "research manual seed required"
	} else if result.Status == "cancelled" {
		status = domaintask.StatusCancelled
	}
	closeErr := guard.Close(status, string(summary), waitingReason, runErr)
	if !guard.closed {
		result = researchClosureFailure(result, closeErr)
	}
	return result
}
