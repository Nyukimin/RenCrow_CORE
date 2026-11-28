package research

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
	"strings"
)

// ResultRecorder projects only verified Engine facts. It does not issue Task
// authority, terminalize a Run, or copy private source text into the Task.
type ResultRecorder struct {
	Store        engine.Store
	Tasks        *taskmanager.Manager
	PrivateRead  func(context.Context, c.Envelope) domaintask.TaskReadAuthorizationCheck
	PrivateWrite func(context.Context, c.Envelope) domaintask.TaskWriteAuthorizationCheck
}

func (r *ResultRecorder) Record(ctx context.Context, env c.Envelope, result c.Result) error {
	if r == nil || r.Store == nil || r.Tasks == nil || ctx == nil {
		return errors.New("unavailable: Research result projection owner")
	}
	// Recording observed outcomes is bounded cleanup, including after cancel.
	ctx, stop := context.WithTimeout(context.WithoutCancel(ctx), taskmanager.StartedRunTerminalWriteTimeout)
	defer stop()
	if result.SchemaVersion != c.ResultSchema || result.TaskID != env.TaskID || result.ExecutionRunID != env.RunID || result.RunID == "" {
		return errors.New("invalid_response: Research execution/result binding")
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	digest := digestBytes(data)
	ref, err := r.Store.Put(ctx, data)
	if err != nil {
		return err
	}
	if ref.Hash != digest || ref.Ref == "" {
		return errors.New("invalid_response: Research result CAS receipt")
	}
	projection := researchProjection(env, result, digest, ref.Ref)
	verify := func(checkCtx context.Context, proposed domaintask.ResearchResultProjection) error {
		if proposed.ResultDigest != digest || proposed.ArtifactRef != ref.Ref {
			return errors.New("invalid_response: Research projection digest/ref")
		}
		actual, err := r.Store.Get(checkCtx, ref)
		if err != nil || !bytes.Equal(actual, data) || digestBytes(actual) != digest {
			return errors.New("invalid_response: Research result exact readback")
		}
		record, err := r.Store.Load(checkCtx, result.RunID)
		if err != nil {
			return err
		}
		requestBytes, err := json.Marshal(record.Request)
		if err != nil || digestBytes(requestBytes) != record.RequestHash {
			return errors.New("invalid_response: frozen Research request digest")
		}
		bound := record.Envelope
		if record.Invocation != nil {
			bound = *record.Invocation
		}
		stored, err := json.Marshal(record.Result)
		if err != nil || bound != env || !bytes.Equal(stored, data) {
			return errors.New("invalid_response: Research journal/result binding")
		}
		expected := researchProjection(env, record.Result, digest, ref.Ref)
		expected.RequiredReceiptsComplete = requiredResearchReceiptsComplete(record)
		proposedJSON, _ := json.Marshal(proposed)
		expectedJSON, _ := json.Marshal(expected)
		if !bytes.Equal(proposedJSON, expectedJSON) {
			return errors.New("invalid_response: Research facts differ from stored result")
		}
		return nil
	}
	record, err := r.Store.Load(ctx, result.RunID)
	if err != nil {
		return err
	}
	projection.RequiredReceiptsComplete = requiredResearchReceiptsComplete(record)
	var read domaintask.TaskReadAuthorizationCheck
	var write domaintask.TaskWriteAuthorizationCheck
	if r.PrivateRead != nil {
		read = r.PrivateRead(ctx, env)
	}
	if r.PrivateWrite != nil {
		write = r.PrivateWrite(ctx, env)
	}
	return r.Tasks.RecordResearchResult(ctx, core.TaskID(env.TaskID), core.RunID(env.RunID), env.Actor, projection, verify, read, write)
}

// Preflight rejects execution identities that this recorder cannot durably
// project before the Research Engine performs external source work.
func (r *ResultRecorder) Preflight(ctx context.Context, env c.Envelope) error {
	if r == nil || r.Tasks == nil || ctx == nil || ctx.Err() != nil {
		return errors.New("unavailable: Research result projection owner")
	}
	taskID := core.TaskID(env.TaskID)
	runID := core.RunID(env.RunID)
	if err := taskID.Validate(); err != nil {
		return err
	}
	if err := runID.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(env.Actor) == "" || strings.TrimSpace(env.Actor) != env.Actor {
		return errors.New("invalid Research actor")
	}
	task, err := r.Tasks.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if task.PrivateAccess != nil || task.Status != domaintask.StatusRunning || task.Assignee != env.Actor {
		return errors.New("Research Task projection is unavailable")
	}
	if err := r.Tasks.ValidateRunExecution(ctx, taskID, runID, env.Actor); err != nil {
		return err
	}
	switch env.DataScope {
	case "public":
		if !domaintask.ResearchResultDataScopeMatches(task, env.DataScope, env.OwnerID) {
			return errors.New("Research public provenance is unavailable")
		}
	case "user":
		if !domaintask.ResearchResultDataScopeMatches(task, env.DataScope, env.OwnerID) {
			return errors.New("Research user provenance is unavailable")
		}
	default:
		return errors.New("invalid Research data scope")
	}
	return nil
}

// Reconcile confirms that a readonly Engine result is the exact result projected
// to its current Task and that the Task's latest Run reached the matching state.
// It never writes owner state or starts a replacement invocation.
func (r *ResultRecorder) Reconcile(ctx context.Context, caller c.Envelope, source c.Result) (c.Result, error) {
	if r == nil || r.Store == nil || r.Tasks == nil || ctx == nil {
		return c.Result{}, errors.New("unavailable: Research result status owner")
	}
	if err := ctx.Err(); err != nil {
		return c.Result{}, err
	}
	if source.SchemaVersion != c.ResultSchema || source.RunID == "" {
		return c.Result{}, errors.New("invalid_response: Research status result")
	}
	record, err := r.Store.Load(ctx, source.RunID)
	if err != nil {
		return c.Result{}, err
	}
	bound := record.Envelope
	if record.Invocation != nil {
		bound = *record.Invocation
	}
	if bound.DataScope != caller.DataScope || record.Envelope.DataScope != caller.DataScope ||
		source.TaskID != bound.TaskID || source.ExecutionRunID != bound.RunID || source.RunID != record.Envelope.RunID {
		return c.Result{}, errors.New("blocked_by_policy: Research status owner or invocation differs")
	}
	owner, actor := bound.OwnerID, bound.Actor
	if bound.DataScope == "user" {
		if owner == "" || owner != caller.OwnerID || record.Envelope.OwnerID != caller.OwnerID {
			return c.Result{}, errors.New("blocked_by_policy: Research status owner differs")
		}
	} else if bound.DataScope == "public" {
		if actor == "" || actor != caller.Actor {
			return c.Result{}, errors.New("blocked_by_policy: Research status actor differs")
		}
	} else {
		return c.Result{}, errors.New("blocked_by_policy: Research status scope is unsupported")
	}
	sourceJSON, err := json.Marshal(source)
	if err != nil {
		return c.Result{}, err
	}
	storedJSON, err := json.Marshal(record.Result)
	if err != nil || !bytes.Equal(sourceJSON, storedJSON) {
		return unavailableResearchTaskReceipt(source), nil
	}
	taskID := core.TaskID(bound.TaskID)
	taskSnapshot, runSnapshot, err := r.Tasks.ReadTaskRuns(ctx, []core.TaskID{taskID})
	if err != nil {
		return c.Result{}, err
	}
	task, found := taskSnapshot[taskID]
	if !found || task.PrivateAccess != nil || task.Assignee != actor {
		return unavailableResearchTaskReceipt(source), nil
	}
	if bound.DataScope == "user" {
		researchOwner, ok := domaintask.ResearchResultUserOwner(task)
		if !ok || researchOwner != owner {
			return unavailableResearchTaskReceipt(source), nil
		}
	} else if task.ResearchBinding != nil || task.PrivateAccess != nil ||
		(task.OriginPublication != nil && (task.OriginPublication.Validate(task.OriginSessionID) != nil || task.OriginPublication.Audience == conversation.SessionAudiencePrivate)) {
		return unavailableResearchTaskReceipt(source), nil
	}
	runs := runSnapshot[taskID]
	if len(runs) == 0 || runs[len(runs)-1].RunID != core.RunID(bound.RunID) {
		return unavailableResearchTaskReceipt(source), nil
	}
	var run domaintask.Run
	for _, candidate := range runs {
		if candidate.RunID == core.RunID(bound.RunID) {
			run = candidate
			break
		}
	}
	if run.RunID == "" || run.TaskID != task.TaskID || run.TraceID != "" && run.TraceID != core.TraceID(bound.TraceID) || run.Assignee != actor {
		return unavailableResearchTaskReceipt(source), nil
	}
	if task.Status == domaintask.StatusRunning && run.Status == domaintask.RunStatusRunning {
		return pendingResearchTaskReceipt(source, "running"), nil
	}
	projection := task.ResearchResult
	if projection == nil || projection.Validate(task) != nil {
		return unavailableResearchTaskReceipt(source), nil
	}
	digest := sha256.Sum256(sourceJSON)
	digestText := hex.EncodeToString(digest[:])
	if projection.ResultDigest != digestText || projection.ArtifactRef != "sha256:"+digestText {
		return unavailableResearchTaskReceipt(source), nil
	}
	artifact, err := r.Store.Get(ctx, c.ArtifactRef{Kind: "sha256", Hash: digestText, Ref: projection.ArtifactRef})
	if err != nil || !bytes.Equal(artifact, sourceJSON) {
		return unavailableResearchTaskReceipt(source), nil
	}
	expected := researchProjection(bound, source, digestText, projection.ArtifactRef)
	expected.RequiredReceiptsComplete = requiredResearchReceiptsComplete(record)
	expectedJSON, err := json.Marshal(expected)
	if err != nil {
		return c.Result{}, err
	}
	actualJSON, err := json.Marshal(projection)
	if err != nil {
		return c.Result{}, err
	}
	if !bytes.Equal(actualJSON, expectedJSON) || !researchTaskRunStateMatches(task.Status, run.Status, source.Status) {
		return unavailableResearchTaskReceipt(source), nil
	}
	return source, nil
}

func pendingResearchTaskReceipt(source c.Result, status string) c.Result {
	source.Status = status
	source.Missing = appendMissing(source.Missing, "task_receipt")
	return source
}

func unavailableResearchTaskReceipt(source c.Result) c.Result {
	if source.Counts.DocumentsFetched > 0 || source.Counts.RecordsSaved > 0 || source.Counts.BoardPostsCreated > 0 {
		source.Status = "partial"
	} else {
		source.Status = "failed"
	}
	source.Missing = appendMissing(source.Missing, "task_receipt")
	return source
}

func appendMissing(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func researchTaskRunStateMatches(taskStatus domaintask.Status, runStatus domaintask.RunStatus, resultStatus string) bool {
	switch resultStatus {
	case "succeeded":
		return taskStatus == domaintask.StatusSucceeded && runStatus == domaintask.RunStatusSucceeded
	case "waiting_input":
		return taskStatus == domaintask.StatusWaiting && runStatus == domaintask.RunStatusWaiting
	case "cancelled":
		return taskStatus == domaintask.StatusCancelled && runStatus == domaintask.RunStatusCancelled
	case "partial", "failed":
		return taskStatus == domaintask.StatusFailed && runStatus == domaintask.RunStatusFailed
	default:
		return false
	}
}

func researchProjection(env c.Envelope, result c.Result, digest, ref string) domaintask.ResearchResultProjection {
	counts := result.Counts
	p := domaintask.ResearchResultProjection{SchemaVersion: domaintask.ResearchResultSchema, TaskID: core.TaskID(env.TaskID), ExecutionRunID: core.RunID(env.RunID), ResearchRunID: result.RunID, ActorID: env.Actor, DataScope: env.DataScope, OwnerID: env.OwnerID, ResultDigest: digest, ArtifactRef: ref, Status: result.Status, Missing: append([]string(nil), result.Missing...), Counts: domaintask.ResearchResultCounts{SearchCalls: counts.SearchCalls, Candidates: counts.Candidates, FetchAttempts: counts.FetchAttempts, DocumentsFetched: counts.DocumentsFetched, LocalDocumentsReused: counts.LocalDocumentsReused, DocumentsAnalyzed: counts.DocumentsAnalyzed, RecordsSaved: counts.RecordsSaved, BoardPostsCreated: counts.BoardPostsCreated, AnalysisCalls: counts.AnalysisCalls, PaidExternalCalls: counts.PaidExternalCalls}}
	receipts := append([]c.Receipt(nil), result.Receipts...)
	if result.RefreshReceipt != nil && result.RefreshReceipt.CursorCommitted {
		receipts = append(receipts, result.RefreshReceipt.Receipt)
	}
	p.ReceiptCount = len(receipts)
	for _, receipt := range receipts {
		if receipt.State != "committed" || receipt.Scope != env.DataScope || receipt.VerifiedAt.IsZero() {
			continue
		}
		p.VerifiedReceiptRefs = append(p.VerifiedReceiptRefs, domaintask.ResearchVerifiedReceipt{Owner: receipt.Owner, Route: receipt.OwnerRoute, CanonicalID: receipt.CanonicalID, Digest: receipt.Digest, VerifiedAt: receipt.VerifiedAt})
	}
	switch {
	case p.ReceiptCount == 0:
		p.ReceiptState = "none"
	case len(p.VerifiedReceiptRefs) == 0:
		p.ReceiptState = "unavailable"
	case len(p.VerifiedReceiptRefs) < p.ReceiptCount:
		p.ReceiptState = "partially_verified"
	default:
		p.ReceiptState = "verified"
	}
	return p
}

func requiredResearchReceiptsComplete(record c.RunRecord) bool {
	request := record.Request
	if record.RefreshPlan != nil {
		request = record.RefreshPlan.Request
	}
	for _, missing := range record.Result.Missing {
		if strings.HasPrefix(missing, "save:") || strings.Contains(missing, "receipt") || strings.Contains(missing, "cursor") {
			return false
		}
	}
	if request.Mode == "refresh" {
		if record.Result.RefreshReceipt == nil || !record.Result.RefreshReceipt.CursorCommitted {
			return false
		}
	}
	for _, target := range request.Outputs.Save {
		if !target.Required {
			continue
		}
		if target.Target == "artifact" {
			found := false
			for _, ref := range record.Result.Artifacts {
				if ref.Kind == "collection" {
					found = true
				}
			}
			if !found {
				return false
			}
			continue
		}
		if record.Collection == nil {
			return false
		}
		if len(record.Collection.Documents) == 0 && request.Mode != "refresh" {
			return false
		}
		digests := []string{}
		if target.Target == "knowledge_staging" {
			for _, doc := range record.Collection.Documents {
				digests = append(digests, doc.ExtractedHash)
			}
		} else {
			if record.Result.Report == "" {
				return false
			}
			digests = append(digests, digestBytes([]byte(record.Result.Report)))
		}
		for _, digest := range digests {
			found := false
			for _, receipt := range record.Result.Receipts {
				if receipt.OwnerRoute == target.Target && receipt.Digest == digest && receipt.Scope == record.Envelope.DataScope && receipt.State == "committed" && !receipt.VerifiedAt.IsZero() {
					found = true
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}
