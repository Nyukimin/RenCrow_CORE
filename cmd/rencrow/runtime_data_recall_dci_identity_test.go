package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	dcipersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/dci"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type runtimeDCIIdentityVerifierStub struct {
	calls   int
	ctx     context.Context
	action  modulecore.ActionID
	receipt dcipersistence.IdentityEvidence
	err     error
}

func (s *runtimeDCIIdentityVerifierStub) VerifyAction(ctx context.Context, actionID modulecore.ActionID) (dcipersistence.IdentityEvidence, error) {
	s.calls++
	s.ctx = ctx
	s.action = actionID
	return s.receipt, s.err
}

func TestRuntimeDataRecallDCIIdentityEvidenceRegistrationAndWorkerProjection(t *testing.T) {
	actionID := modulecore.NewActionID()
	verifier := &runtimeDCIIdentityVerifierStub{receipt: validRuntimeDCIIdentityEvidence(actionID)}
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallDCIIdentityEvidence(registry, verifier); err != nil {
		t.Fatalf("register identity evidence: %v", err)
	}
	if routes := registry.Snapshot(); !reflect.DeepEqual(routes, []runtimeDataRecallRoute{{
		Store: "dci", Operation: "identity_evidence", Access: dataRecallAccessInternal,
	}}) {
		t.Fatalf("routes = %#v", routes)
	}

	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{
		OperationalDataRecall: registry,
		DisableToolHarness:    true,
	})
	ctx := runtimeDCIIdentityScope(t)
	response, err := worker.ExecuteV2(ctx, "data.recall", map[string]any{
		"store": "dci", "operation": "identity_evidence", "query": "  " + string(actionID) + "  ", "limit": 1,
	})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("worker response = %#v, err = %v", response, err)
	}
	result, ok := response.Result.(runtimeDataRecallResult)
	if !ok {
		t.Fatalf("result type = %T, want runtimeDataRecallResult", response.Result)
	}
	if result.Store != "dci" || result.Operation != "identity_evidence" || len(result.Records) != 1 {
		t.Fatalf("result = %#v, want one dci identity record", result)
	}
	want := identityEvidenceRecord(verifier.receipt)
	if !reflect.DeepEqual(result.Records[0], want) {
		t.Fatalf("record = %#v, want %#v", result.Records[0], want)
	}
	for _, forbidden := range []string{"query", "path", "snippet", "url", "raw", "payload", "meta"} {
		if _, exposed := result.Records[0][forbidden]; exposed {
			t.Fatalf("record exposed forbidden field %q: %#v", forbidden, result.Records[0])
		}
	}
	if verifier.calls != 1 || verifier.action != actionID {
		t.Fatalf("verifier calls/action = %d/%q, want 1/%q", verifier.calls, verifier.action, actionID)
	}
	if got, found := domaintool.ToolExecutionScopeFromContext(verifier.ctx); !found || got.ActorKind != domaintool.ActorKindAgent || got.ActorID != "shiro" || got.AgentRole != "worker" || got.Purpose != "ops" {
		t.Fatalf("verifier scope = %#v, found = %v", got, found)
	}
}

func TestRuntimeDataRecallDCIIdentityEvidenceRequiresExactActionAndLimit(t *testing.T) {
	actionID := modulecore.NewActionID()
	verifier := &runtimeDCIIdentityVerifierStub{receipt: validRuntimeDCIIdentityEvidence(actionID)}
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallDCIIdentityEvidence(registry, verifier); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataRecall: registry, DisableToolHarness: true})
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "invalid action", args: map[string]any{"store": "dci", "operation": "identity_evidence", "query": "not-an-action", "limit": 1}},
		{name: "limit two", args: map[string]any{"store": "dci", "operation": "identity_evidence", "query": string(actionID), "limit": 2}},
		{name: "default limit", args: map[string]any{"store": "dci", "operation": "identity_evidence", "query": string(actionID)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := worker.ExecuteV2(runtimeDCIIdentityScope(t), "data.recall", tc.args)
			if err != nil || response == nil || !response.IsError() {
				t.Fatalf("response = %#v, err = %v, want validation/unavailable error", response, err)
			}
		})
	}
	if verifier.calls != 0 {
		t.Fatalf("verifier calls = %d, want zero for invalid action/limit", verifier.calls)
	}
}

func TestRuntimeDataRecallDCIIdentityEvidenceFailsClosedWithoutVerifierOrScope(t *testing.T) {
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallDCIIdentityEvidence(registry, nil); err == nil {
		t.Fatal("nil verifier registration must fail")
	}

	actionID := modulecore.NewActionID()
	verifier := &runtimeDCIIdentityVerifierStub{receipt: validRuntimeDCIIdentityEvidence(actionID)}
	if err := registerRuntimeDataRecallDCIIdentityEvidence(registry, verifier); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataRecall: registry, DisableToolHarness: true})
	response, err := worker.ExecuteV2(runtimeDCIIdentityPublicScope(t), "data.recall", map[string]any{
		"store": "dci", "operation": "identity_evidence", "query": string(actionID), "limit": 1,
	})
	if err != nil || response == nil || !response.IsError() {
		t.Fatalf("denied response = %#v, err = %v", response, err)
	}
	if verifier.calls != 0 {
		t.Fatalf("verifier calls = %d, want zero on access denial", verifier.calls)
	}
}

func TestRuntimeDataRecallDCIIdentityEvidenceRejectsVerifierErrorsTamperingAndMismatch(t *testing.T) {
	actionID := modulecore.NewActionID()
	cases := []struct {
		name    string
		receipt dcipersistence.IdentityEvidence
		err     error
	}{
		{
			name:    "verifier error",
			receipt: validRuntimeDCIIdentityEvidence(actionID),
			err:     errors.New("raw=/private/secret.txt snippet=do-not-leak url=https://secret.invalid query=private"),
		},
		{
			name: "tampered receipt",
			receipt: func() dcipersistence.IdentityEvidence {
				receipt := validRuntimeDCIIdentityEvidence(actionID)
				receipt.EventGraphSHA256 = strings.Repeat("A", 64)
				return receipt
			}(),
		},
		{
			name:    "action mismatch",
			receipt: validRuntimeDCIIdentityEvidence(modulecore.NewActionID()),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verifier := &runtimeDCIIdentityVerifierStub{receipt: tc.receipt, err: tc.err}
			registry := newRuntimeDataRecallRegistry()
			if err := registerRuntimeDataRecallDCIIdentityEvidence(registry, verifier); err != nil {
				t.Fatal(err)
			}
			worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataRecall: registry, DisableToolHarness: true})
			response, err := worker.ExecuteV2(runtimeDCIIdentityScope(t), "data.recall", map[string]any{
				"store": "dci", "operation": "identity_evidence", "query": string(actionID), "limit": 1,
			})
			if err != nil || response == nil || !response.IsError() {
				t.Fatalf("response = %#v, err = %v, want bounded failure", response, err)
			}
			public := response.String()
			for _, secret := range []string{"/private/secret.txt", "do-not-leak", "https://secret.invalid", "raw=", "snippet=", "query=private"} {
				if strings.Contains(public, secret) {
					t.Fatalf("public response leaked %q: %q", secret, public)
				}
			}
			if verifier.calls != 1 {
				t.Fatalf("verifier calls = %d, want one", verifier.calls)
			}
		})
	}
}

func TestRuntimeDataRecallDCIIdentityEvidenceRejectsPathLikeQueryWithoutLeak(t *testing.T) {
	actionID := modulecore.NewActionID()
	verifier := &runtimeDCIIdentityVerifierStub{receipt: validRuntimeDCIIdentityEvidence(actionID)}
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallDCIIdentityEvidence(registry, verifier); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataRecall: registry, DisableToolHarness: true})
	query := "/private/secret.txt?query=raw-query&snippet=raw-snippet"
	response, err := worker.ExecuteV2(runtimeDCIIdentityScope(t), "data.recall", map[string]any{
		"store": "dci", "operation": "identity_evidence", "query": query, "limit": 1,
	})
	if err != nil || response == nil || !response.IsError() {
		t.Fatalf("response = %#v, err = %v, want failure", response, err)
	}
	if strings.Contains(response.String(), query) || verifier.calls != 0 {
		t.Fatalf("invalid query leaked or verifier was called: response=%q calls=%d", response.String(), verifier.calls)
	}
}

func validRuntimeDCIIdentityEvidence(actionID modulecore.ActionID) dcipersistence.IdentityEvidence {
	return dcipersistence.IdentityEvidence{
		SchemaVersion:          dcipersistence.IdentityEvidenceSchemaVersion,
		Status:                 "passed",
		ActionID:               actionID,
		TraceID:                modulecore.NewTraceID(),
		ActorKind:              "agent",
		ActorID:                "shiro",
		SearchStatus:           "completed",
		EventCount:             6,
		StepCount:              1,
		EvidenceCount:          1,
		CurrentProjectionCount: 1,
		ArchiveProjectionCount: 1,
		EventGraphSHA256:       strings.Repeat("a", 64),
	}
}

func identityEvidenceRecord(e dcipersistence.IdentityEvidence) map[string]any {
	return map[string]any{
		"schema_version":           e.SchemaVersion,
		"status":                   e.Status,
		"action_id":                string(e.ActionID),
		"trace_id":                 string(e.TraceID),
		"actor_kind":               e.ActorKind,
		"actor_id":                 e.ActorID,
		"search_status":            e.SearchStatus,
		"event_count":              e.EventCount,
		"step_count":               e.StepCount,
		"evidence_count":           e.EvidenceCount,
		"current_projection_count": e.CurrentProjectionCount,
		"archive_projection_count": e.ArchiveProjectionCount,
		"event_graph_sha256":       e.EventGraphSHA256,
	}
}

func runtimeDCIIdentityScope(t *testing.T) context.Context {
	t.Helper()
	scope := domaintool.ToolExecutionScope{
		RequestID:            "runtime-dci-identity-evidence",
		ActorKind:            domaintool.ActorKindAgent,
		ActorID:              "shiro",
		AllowedDataScopes:    []string{domaintool.DataScopeInternal},
		AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator,
		AgentRole:            "worker",
		Purpose:              "ops",
	}
	if err := scope.Validate(); err != nil {
		t.Fatalf("scope validation: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func runtimeDCIIdentityPublicScope(t *testing.T) context.Context {
	t.Helper()
	scope := domaintool.ToolExecutionScope{
		RequestID:            "runtime-dci-identity-public",
		ActorKind:            domaintool.ActorKindAgent,
		ActorID:              "shiro",
		AllowedDataScopes:    []string{domaintool.DataScopePublic},
		AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator,
		AgentRole:            "worker",
		Purpose:              "ops",
	}
	if err := scope.Validate(); err != nil {
		t.Fatalf("scope validation: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}
