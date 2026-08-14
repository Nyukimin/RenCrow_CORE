package main

import (
	"context"
	"testing"
	"time"

	domainbrowser "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	browsertracepersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/browsertrace"
	personapersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/persona"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func TestRuntimeDataWritePersonaArchitectureOwnerE2EThroughWorkerAndRecall(t *testing.T) {
	store := personapersistence.NewJSONLStore(t.TempDir())
	writeRegistry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWritePersonaArchitecture(writeRegistry, store); err != nil {
		t.Fatalf("register persona write: %v", err)
	}
	recallRegistry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallPersonaArchitectureObservations(recallRegistry, store); err != nil {
		t.Fatalf("register persona observation recall: %v", err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{
		OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true,
	})
	ctx := runtimeDataWriteOwnerContext(t, "persona-owner-1", true)
	payload := map[string]any{
		"observation_type": " daily ",
		"summary":          "  stable preference  ",
		"evidence_refs":    []any{" ref-1 ", "ref-2"},
		"sensitivity":      " normal ",
	}
	first := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "persona_architecture", "record_observation", payload)
	if first.IdempotentReplay || first.SchemaVersion != "persona-observation/v1" || first.MigrationState != "embedded_current" || first.ValidationState != "owner_validated" || first.PolicyRevision != runtimeDataWritePolicyRevision || first.AuditRef == "" || first.IdempotencyKey != "persona-owner-1" {
		t.Fatalf("first persona receipt = %#v", first)
	}
	second := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "persona_architecture", "record_observation", payload)
	if !second.IdempotentReplay || second.AuditRef != first.AuditRef {
		t.Fatalf("persona replay receipt = %#v, first=%#v", second, first)
	}
	changed := runtimeDataWriteOwnerClonePayload(payload)
	changed["summary"] = "different"
	if response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "persona_architecture", "operation": "record_observation", "payload": changed}); err != nil || response == nil || !response.IsError() {
		t.Fatalf("persona conflict response=%#v err=%v", response, err)
	}
	observations, err := store.ListObservationLogs(ctx, 10)
	if err != nil || len(observations) != 1 {
		t.Fatalf("persona observations=%#v err=%v", observations, err)
	}
	if got := observations[0]; got.EventID != first.AuditRef || got.ObserverID != "shiro" || got.TargetID != "user-1" || got.ObservationType != "daily" || got.Sensitivity != "normal" || got.ReviewStatus != "adopted" || len(got.EvidenceRefs) != 2 {
		t.Fatalf("persona observation=%#v", got)
	}
	recalled := runtimeDataWriteOwnerExecuteRecall(t, worker, ctx, "persona_architecture", "observations", first.AuditRef)
	if recalled.Evidence.RequestID != "persona-owner-1" || recalled.Evidence.ActorID != "shiro" || recalled.Evidence.DataScope != string(dataRecallAccessUser) || recalled.Evidence.OwnerRoute != "persona_architecture/observations" || len(recalled.Records) != 1 {
		t.Fatalf("persona recall=%#v", recalled)
	}
	if got := recalled.Records[0]; got["event_id"] != first.AuditRef || got["observation_type"] != "daily" || got["review_status"] != "adopted" {
		t.Fatalf("persona recalled record=%#v", got)
	}
	if _, leaked := recalled.Records[0]["observer_id"]; leaked {
		t.Fatal("persona observation projection leaked observer_id")
	}
	otherUser := runtimeDataWriteContext(t, domaintool.ActorKindAgent, "shiro", "user-2", []string{domaintool.DataScopeUser}, "worker", "ops")
	otherRecall := runtimeDataWriteOwnerExecuteRecall(t, worker, otherUser, "persona_architecture", "observations", first.AuditRef)
	if len(otherRecall.Records) != 0 {
		t.Fatalf("persona recall leaked another user's observation: %#v", otherRecall.Records)
	}

	sensitiveCtx := runtimeDataWriteOwnerContext(t, "persona-owner-sensitive", true)
	sensitivePayload := runtimeDataWriteOwnerClonePayload(payload)
	sensitivePayload["sensitivity"] = "sensitive"
	sensitive := runtimeDataWriteOwnerExecuteWrite(t, worker, sensitiveCtx, "persona_architecture", "record_observation", sensitivePayload)
	if sensitive.IdempotentReplay || sensitive.AuditRef == first.AuditRef {
		t.Fatalf("fresh sensitive persona receipt=%#v", sensitive)
	}
	observations, err = store.ListObservationLogs(ctx, 10)
	if err != nil || len(observations) != 2 || observations[0].ReviewStatus != "rejected" {
		t.Fatalf("sensitive persona observations=%#v err=%v", observations, err)
	}
}

func TestRuntimeDataWritePersonaArchitectureRejectsModelOwnedFields(t *testing.T) {
	store := personapersistence.NewJSONLStore(t.TempDir())
	registry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWritePersonaArchitecture(registry, store); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
	ctx := runtimeDataWriteOwnerContext(t, "persona-owner-invalid", true)
	for _, extra := range []string{"event_id", "observer_id", "target_id", "review_status", "created_at", "request_id", "unknown"} {
		payload := map[string]any{
			"observation_type": "daily", "sensitivity": "normal", extra: "model-owned",
		}
		response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "persona_architecture", "operation": "record_observation", "payload": payload})
		if err != nil || response == nil || !response.IsError() {
			t.Fatalf("extra field %q response=%#v err=%v", extra, response, err)
		}
	}
	if observations, err := store.ListObservationLogs(context.Background(), 10); err != nil || len(observations) != 0 {
		t.Fatalf("invalid persona payload mutated=%#v err=%v", observations, err)
	}
}

func TestRuntimeDataWriteBrowserTraceValidationOwnerE2EThroughWorkerAndRecall(t *testing.T) {
	store := browsertracepersistence.NewJSONLStore(t.TempDir())
	candidate := domainbrowser.APICandidate{
		CandidateID:          "candidate-owner-1",
		TraceRunID:           "trace-owner-1",
		Method:               "GET",
		ObservedURL:          "https://example.com/api/items",
		ContainsPersonalData: "none",
		RiskLevel:            "low",
		Status:               "candidate",
		CreatedAt:            time.Date(2026, 8, 14, 4, 0, 0, 0, time.UTC),
	}
	if err := store.SaveAPICandidate(context.Background(), candidate); err != nil {
		t.Fatalf("seed browser candidate: %v", err)
	}
	writeRegistry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteBrowserTraceToAPI(writeRegistry, store); err != nil {
		t.Fatalf("register browser write: %v", err)
	}
	recallRegistry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallBrowserTraceValidationReviews(recallRegistry, store); err != nil {
		t.Fatalf("register browser validation recall: %v", err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{
		OperationalDataWrite: writeRegistry, OperationalDataRecall: recallRegistry, DisableToolHarness: true,
	})
	ctx := runtimeDataWriteBContext(t, "browser-owner-1")
	payload := map[string]any{
		"candidate_id":          " candidate-owner-1 ",
		"review_note":           " all checks recorded ",
		"terms_reviewed":        true,
		"official_api_reviewed": true,
		"pii_reviewed":          true,
		"schema_reviewed":       true,
		"risk_reviewed":         true,
	}
	first := runtimeDataWriteBExecuteWrite(t, worker, ctx, "browser_trace_to_api", "review_candidate", payload)
	if first.IdempotentReplay || first.SchemaVersion != "browser-validation/v1" || first.MigrationState != "embedded_current" || first.ValidationState != "owner_validated" || first.PolicyRevision != runtimeDataWritePolicyRevision || first.AuditRef == "" || first.IdempotencyKey != "browser-owner-1" {
		t.Fatalf("first browser receipt=%#v", first)
	}
	second := runtimeDataWriteBExecuteWrite(t, worker, ctx, "browser_trace_to_api", "review_candidate", payload)
	if !second.IdempotentReplay || second.AuditRef != first.AuditRef {
		t.Fatalf("browser replay receipt=%#v first=%#v", second, first)
	}
	changed := runtimeDataWriteBClonePayload(payload)
	changed["risk_reviewed"] = false
	if response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "browser_trace_to_api", "operation": "review_candidate", "payload": changed}); err != nil || response == nil || !response.IsError() {
		t.Fatalf("browser conflict response=%#v err=%v", response, err)
	}
	validations, err := store.ListAPICandidateValidationResults(ctx, 10)
	if err != nil || len(validations) != 1 || !validations[0].Passed || validations[0].Status != "validated" || validations[0].Reviewer != "shiro" {
		t.Fatalf("browser validations=%#v err=%v", validations, err)
	}
	recalled := runtimeDataWriteBExecuteRecall(t, worker, ctx, "browser_trace_to_api", "validation_reviews", first.AuditRef)
	if recalled.Evidence.RequestID != "browser-owner-1" || recalled.Evidence.ActorID != "shiro" || recalled.Evidence.DataScope != string(dataRecallAccessInternal) || recalled.Evidence.OwnerRoute != "browser_trace_to_api/validation_reviews" || len(recalled.Records) != 1 {
		t.Fatalf("browser recall=%#v", recalled)
	}
	if got := recalled.Records[0]; got["validation_id"] != first.AuditRef || got["candidate_id"] != candidate.CandidateID || got["status"] != "validated" || got["passed"] != true {
		t.Fatalf("browser recalled record=%#v", got)
	}

	blockedCtx := runtimeDataWriteBContext(t, "browser-owner-rejected")
	blockedPayload := runtimeDataWriteBClonePayload(payload)
	blockedPayload["review_note"] = " incomplete review "
	blockedPayload["risk_reviewed"] = false
	blocked := runtimeDataWriteBExecuteWrite(t, worker, blockedCtx, "browser_trace_to_api", "review_candidate", blockedPayload)
	if blocked.IdempotentReplay || blocked.AuditRef == first.AuditRef {
		t.Fatalf("fresh rejected browser receipt=%#v", blocked)
	}
	validations, err = store.ListAPICandidateValidationResults(ctx, 10)
	if err != nil || len(validations) != 2 || validations[0].Status != "rejected" || validations[0].Passed || len(validations[0].Issues) == 0 {
		t.Fatalf("rejected browser validations=%#v err=%v", validations, err)
	}

	missingPayload := runtimeDataWriteBClonePayload(payload)
	missingPayload["candidate_id"] = "missing-candidate"
	missingResponse, missingErr := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "browser_trace_to_api", "operation": "review_candidate", "payload": missingPayload})
	if missingErr != nil || missingResponse == nil || !missingResponse.IsError() {
		t.Fatalf("missing candidate response=%#v err=%v", missingResponse, missingErr)
	}
	validations, err = store.ListAPICandidateValidationResults(ctx, 10)
	if err != nil || len(validations) != 2 {
		t.Fatalf("missing candidate mutated validations=%#v err=%v", validations, err)
	}
}

func TestRuntimeDataWriteBrowserTraceValidationRejectsModelOwnedFields(t *testing.T) {
	store := browsertracepersistence.NewJSONLStore(t.TempDir())
	if err := store.SaveAPICandidate(context.Background(), domainbrowser.APICandidate{
		CandidateID:          "candidate-owner-invalid",
		TraceRunID:           "trace-owner-invalid",
		Method:               "GET",
		ObservedURL:          "https://example.com/items",
		ContainsPersonalData: "none",
		RiskLevel:            "low",
		Status:               "candidate",
		CreatedAt:            time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	registry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteBrowserTraceToAPI(registry, store); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
	ctx := runtimeDataWriteBContext(t, "browser-owner-invalid")
	for _, extra := range []string{"validation_id", "trace_run_id", "reviewer", "status", "passed", "created_at", "request_id", "unknown"} {
		payload := map[string]any{
			"candidate_id": "candidate-owner-invalid", "terms_reviewed": true, "official_api_reviewed": true,
			"pii_reviewed": true, "schema_reviewed": true, "risk_reviewed": true, extra: "model-owned",
		}
		response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "browser_trace_to_api", "operation": "review_candidate", "payload": payload})
		if err != nil || response == nil || !response.IsError() {
			t.Fatalf("extra field %q response=%#v err=%v", extra, response, err)
		}
	}
	if validations, err := store.ListAPICandidateValidationResults(context.Background(), 10); err != nil || len(validations) != 0 {
		t.Fatalf("invalid browser payload mutated=%#v err=%v", validations, err)
	}
}
