package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	domainadvisor "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	domainrevenue "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type dataRecallAdvisorListerStub struct {
	runs     []domainadvisor.AdviceRunRecord
	gotLimit int
}

func (s *dataRecallAdvisorListerStub) ListAdviceRuns(_ context.Context, limit int) ([]domainadvisor.AdviceRunRecord, error) {
	s.gotLimit = limit
	return s.runs, nil
}

type dataRecallSandboxListerStub struct {
	sandboxes []domainsandbox.SandboxRecord
	gotLimit  int
}

func (s *dataRecallSandboxListerStub) ListSandboxes(_ context.Context, limit int) ([]domainsandbox.SandboxRecord, error) {
	s.gotLimit = limit
	return s.sandboxes, nil
}

type dataRecallDCIListerStub struct {
	traces   []domaindci.SearchTrace
	gotLimit int
}

func (s *dataRecallDCIListerStub) ListRecent(limit int) ([]domaindci.SearchTrace, error) {
	s.gotLimit = limit
	return s.traces, nil
}

type dataRecallDCISearchResultFinderStub struct {
	result      domaindci.SearchResult
	found       bool
	err         error
	gotActionID modulecore.ActionID
}

func (s *dataRecallDCISearchResultFinderStub) FindSearchResultByActionID(_ context.Context, actionID modulecore.ActionID) (domaindci.SearchResult, bool, error) {
	s.gotActionID = actionID
	return s.result, s.found, s.err
}

type dataRecallSkillGovernanceListerStub struct {
	manifests []domainskill.SkillManifest
	gotLimit  int
}

func (s *dataRecallSkillGovernanceListerStub) ListSkillManifests(_ context.Context, limit int) ([]domainskill.SkillManifest, error) {
	s.gotLimit = limit
	return s.manifests, nil
}

type dataRecallWorkstreamListerStub struct {
	goals    []domainworkstream.Goal
	gotLimit int
}

func (s *dataRecallWorkstreamListerStub) ListGoals(_ context.Context, limit int) ([]domainworkstream.Goal, error) {
	s.gotLimit = limit
	return s.goals, nil
}

type dataRecallRevenueListerStub struct {
	opportunities []domainrevenue.Opportunity
	gotLimit      int
}

func (s *dataRecallRevenueListerStub) ListOpportunities(_ context.Context, limit int) ([]domainrevenue.Opportunity, error) {
	s.gotLimit = limit
	return s.opportunities, nil
}

func TestRegisterDataRecallAdvisor(t *testing.T) {
	created := time.Date(2026, 8, 13, 1, 2, 3, 0, time.UTC)
	store := &dataRecallAdvisorListerStub{runs: []domainadvisor.AdviceRunRecord{
		{RunID: "run-1", AdvisorID: "codex", Status: domainadvisor.AdviceStatus(domainadvisor.StatusCompleted), Summary: "Needle summary", StartedAt: created, PromptHash: "prompt-secret", OutputHash: "output-secret"},
		{RunID: "run-2", AdvisorID: "other", Status: domainadvisor.AdviceStatus(domainadvisor.StatusFailed), Summary: "Other", StartedAt: created.Add(time.Minute)},
	}}
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallAdvisor(registry, store); err != nil {
		t.Fatalf("registerRuntimeDataRecallAdvisor() error = %v", err)
	}
	result := recallDataRecallAdapter(t, registry, dataRecallInternalContext(t), toolsinfra.DataRecallRequest{Store: "advisor", Operation: "advice_runs", Query: "NEEDLE", Limit: 3})
	assertRecallResult(t, result, "advisor", "advice_runs", 1)
	if store.gotLimit != 3 {
		t.Fatalf("ListAdviceRuns limit = %d, want 3", store.gotLimit)
	}
	assertARecord(t, result.Records[0], map[string]any{"run_id": "run-1", "advisor_id": "codex", "status": "completed", "summary": "Needle summary", "created_at": created}, "advisor_id", "created_at", "run_id", "status", "summary")
	assertARecordDoesNotContain(t, result.Records[0], "prompt_hash", "output_hash", "prompt-secret", "output-secret")
	assertARecallEmpty(t, registry, dataRecallInternalContext(t), "advisor", "advice_runs", "not present")
	assertARecallDenied(t, registry, dataRecallUserContext(t), "advisor", "advice_runs")
}

func TestRegisterDataRecallSandbox(t *testing.T) {
	created := time.Date(2026, 8, 13, 2, 3, 4, 0, time.UTC)
	store := &dataRecallSandboxListerStub{sandboxes: []domainsandbox.SandboxRecord{
		{SandboxID: "sandbox-1", Status: domainsandbox.SandboxStatusActive, BaseRef: "main", Path: "/secret/worktree", CreatedAt: created},
		{SandboxID: "sandbox-2", Status: domainsandbox.SandboxStatusClosed, BaseRef: "release", CreatedAt: created.Add(time.Minute)},
	}}
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallSandbox(registry, store); err != nil {
		t.Fatalf("registerRuntimeDataRecallSandbox() error = %v", err)
	}
	result := recallDataRecallAdapter(t, registry, dataRecallInternalContext(t), toolsinfra.DataRecallRequest{Store: "sandbox", Operation: "sandboxes", Query: "MAIN", Limit: 2})
	assertRecallResult(t, result, "sandbox", "sandboxes", 1)
	if store.gotLimit != 2 {
		t.Fatalf("ListSandboxes limit = %d, want 2", store.gotLimit)
	}
	assertARecord(t, result.Records[0], map[string]any{"sandbox_id": "sandbox-1", "status": "active", "base_branch": "main", "created_at": created}, "base_branch", "created_at", "sandbox_id", "status")
	assertARecordDoesNotContain(t, result.Records[0], "path", "/secret/worktree")
	assertARecallEmpty(t, registry, dataRecallInternalContext(t), "sandbox", "sandboxes", "unknown")
	assertARecallDenied(t, registry, dataRecallUserContext(t), "sandbox", "sandboxes")
}

func TestRegisterDataRecallDCI(t *testing.T) {
	created := time.Date(2026, 8, 13, 3, 4, 5, 0, time.UTC)
	traceID := modulecore.NewTraceID()
	actionID := modulecore.NewActionID()
	otherTraceID := modulecore.NewTraceID()
	otherActionID := modulecore.NewActionID()
	store := &dataRecallDCIListerStub{traces: []domaindci.SearchTrace{
		{TraceID: traceID, ActionID: actionID, UserQuery: "Needle query", CorpusScope: []string{"core"}, Status: "completed", StartedAt: created, EndedAt: created, ActorAttribution: domaindci.ActorAttributionAuthenticated, ActorKind: "agent", ActorID: "shiro", Mode: "dci"},
		{TraceID: otherTraceID, ActionID: otherActionID, UserQuery: "Other", CorpusScope: []string{"docs"}, Status: "failed", StartedAt: created.Add(time.Minute), EndedAt: created.Add(time.Minute), ActorAttribution: domaindci.ActorAttributionAuthenticated, ActorKind: "agent", ActorID: "shiro", Mode: "dci"},
	}}
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallDCI(registry, store); err != nil {
		t.Fatalf("registerRuntimeDataRecallDCI() error = %v", err)
	}
	result := recallDataRecallAdapter(t, registry, dataRecallInternalContext(t), toolsinfra.DataRecallRequest{Store: "dci", Operation: "search_traces", Query: "NEEDLE", Limit: 4})
	assertRecallResult(t, result, "dci", "search_traces", 1)
	if store.gotLimit != 4 {
		t.Fatalf("ListRecent limit = %d, want 4", store.gotLimit)
	}
	assertARecord(t, result.Records[0], map[string]any{
		"action_id": string(actionID), "trace_id": string(traceID), "actor_attribution": "authenticated", "actor_kind": "agent", "actor_id": "shiro",
		"query": "Needle query", "scope": []string{"core"}, "status": "completed", "created_at": created,
	}, "action_id", "actor_attribution", "actor_id", "actor_kind", "created_at", "query", "scope", "status", "trace_id")
	assertARecordDoesNotContain(t, result.Records[0], "secret command", "command_text", "file_path", "snippet", "evidence", "idempotency_key")
	assertARecallEmpty(t, registry, dataRecallInternalContext(t), "dci", "search_traces", "unknown")
	assertARecallDenied(t, registry, dataRecallUserContext(t), "dci", "search_traces")
	if _, err := registry.Recall(dataRecallInternalContext(t), toolsinfra.DataRecallRequest{Store: "dci", Operation: "search_trace", Query: string(actionID), Limit: 1}); !errors.Is(err, errDataRecallRegistryUnknownOperation) {
		t.Fatalf("retired dci/search_trace error = %v, want unavailable", err)
	}
}

func TestRegisterDataRecallDCISearchResult(t *testing.T) {
	created := time.Date(2026, 8, 13, 3, 4, 5, 0, time.UTC)
	traceID := modulecore.NewTraceID()
	actionID := modulecore.NewActionID()
	stepEventID := modulecore.NewEventID()
	evidenceID := modulecore.NewEvidenceID()
	createdByEventID := modulecore.NewEventID()
	stored := validDCIAdapterResult("Needle query", traceID, actionID, "agent", "shiro", "request-secret")
	stored.Pack.CorpusScope = []string{"core"}
	stored.Pack.Intent = "direct corpus evidence lookup"
	stored.Pack.DerivedTerms = []string{"needle"}
	stored.Pack.Confidence = 0.8
	stored.Pack.Limitations = []string{"limited"}
	stored.Pack.Evidence = []domaindci.Evidence{{
		EvidenceID: evidenceID, CreatedByEventID: createdByEventID, FilePath: "/private/source.md",
		LineStart: 2, LineEnd: 3, Snippet: "private snippet", Confidence: 0.8,
	}}
	stored.Trace.CorpusScope = []string{"core"}
	stored.Trace.Steps = []domaindci.SearchStep{{
		StepNo: 1, EventID: stepEventID, EventType: "dci.file.read", Tool: "read_file", FilePath: "/private/source.md",
		ResultCount: 1, Status: "ok", CreatedAt: created,
	}}
	stored.Trace.FinalEvidenceCount = 1
	stored.Trace.StartedAt = created
	stored.Trace.EndedAt = created
	finder := &dataRecallDCISearchResultFinderStub{result: stored, found: true}
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallDCISearchResult(registry, finder); err != nil {
		t.Fatalf("registerRuntimeDataRecallDCISearchResult() error = %v", err)
	}
	result := recallDataRecallAdapter(t, registry, dataRecallInternalContext(t), toolsinfra.DataRecallRequest{Store: "dci", Operation: "search_result", Query: string(actionID), Limit: 1})
	assertRecallResult(t, result, "dci", "search_result", 1)
	if finder.gotActionID != actionID {
		t.Fatalf("FindSearchResultByActionID action=%q, want %q", finder.gotActionID, actionID)
	}
	assertARecord(t, result.Records[0], map[string]any{
		"action_id": string(actionID), "trace_id": string(traceID), "actor_attribution": "authenticated", "actor_kind": "agent", "actor_id": "shiro",
		"mode": "dci", "query": "Needle query", "scope": []string{"core"}, "status": "completed", "error_message": "",
		"started_at": created, "ended_at": created, "steps": []domaindci.SearchStep(stored.Trace.Steps), "evidence": []domaindci.Evidence(stored.Pack.Evidence),
		"intent": "direct corpus evidence lookup", "derived_terms": []string{"needle"}, "confidence": 0.8, "limitations": []string{"limited"}, "evidence_count": 1,
	}, "action_id", "actor_attribution", "actor_id", "actor_kind", "confidence", "derived_terms", "ended_at", "error_message", "evidence", "evidence_count", "intent", "limitations", "mode", "query", "scope", "started_at", "status", "steps", "trace_id")
	assertARecordDoesNotContain(t, result.Records[0], "request-secret", "idempotency_key")
}

func TestRegisterDataRecallDCISearchResultRejectsInvalidAndMismatchedResults(t *testing.T) {
	actionID := modulecore.NewActionID()
	tests := []struct {
		name   string
		query  string
		result domaindci.SearchResult
	}{
		{name: "malformed action id", query: "not-an-action", result: domaindci.SearchResult{}},
		{name: "mismatched action id", query: string(actionID), result: validDCIAdapterResult("query", modulecore.NewTraceID(), modulecore.NewActionID(), "agent", "shiro", "request")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := newRuntimeDataRecallRegistry()
			finder := &dataRecallDCISearchResultFinderStub{result: tt.result, found: true}
			if err := registerRuntimeDataRecallDCISearchResult(registry, finder); err != nil {
				t.Fatal(err)
			}
			if _, err := registry.Recall(dataRecallInternalContext(t), toolsinfra.DataRecallRequest{Store: "dci", Operation: "search_result", Query: tt.query, Limit: 1}); !errors.Is(err, errDataRecallRegistryCallbackFailed) {
				t.Fatalf("Recall() error = %v, want callback failure", err)
			}
		})
	}
}

func TestRegisterDataRecallDCISearchResultReadsLegacyUnattributed(t *testing.T) {
	actionID := modulecore.NewActionID()
	stored := validDCIAdapterResult("legacy query", modulecore.NewTraceID(), actionID, "agent", "shiro", "legacy-request")
	stored.Trace.ActorAttribution = domaindci.ActorAttributionLegacyUnattributed
	stored.Trace.ActorKind = ""
	stored.Trace.ActorID = ""
	finder := &dataRecallDCISearchResultFinderStub{result: stored, found: true}
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallDCISearchResult(registry, finder); err != nil {
		t.Fatal(err)
	}
	result := recallDataRecallAdapter(t, registry, dataRecallInternalContext(t), toolsinfra.DataRecallRequest{Store: "dci", Operation: "search_result", Query: string(actionID), Limit: 1})
	assertRecallResult(t, result, "dci", "search_result", 1)
	if result.Records[0]["actor_attribution"] != "legacy_unattributed" || result.Records[0]["actor_kind"] != "" || result.Records[0]["actor_id"] != "" {
		t.Fatalf("legacy actor projection=%#v", result.Records[0])
	}
	assertARecordDoesNotContain(t, result.Records[0], "legacy-request", "idempotency_key")
}

func TestRegisterDataRecallSkillGovernance(t *testing.T) {
	updated := time.Date(2026, 8, 13, 4, 5, 6, 0, time.UTC)
	store := &dataRecallSkillGovernanceListerStub{manifests: []domainskill.SkillManifest{
		{SkillID: "skill-1", Name: "Needle skill", Version: "1.2.3", Path: "/secret/skill.md", Description: "private body", Enabled: true, UpdatedAt: updated},
		{SkillID: "skill-2", Name: "Other", Version: "2.0.0", Enabled: false, UpdatedAt: updated.Add(time.Minute)},
	}}
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallSkillGovernance(registry, store); err != nil {
		t.Fatalf("registerRuntimeDataRecallSkillGovernance() error = %v", err)
	}
	result := recallDataRecallAdapter(t, registry, dataRecallInternalContext(t), toolsinfra.DataRecallRequest{Store: "skill_governance", Operation: "skill_manifests", Query: "NEEDLE", Limit: 5})
	assertRecallResult(t, result, "skill_governance", "skill_manifests", 1)
	if store.gotLimit != 5 {
		t.Fatalf("ListSkillManifests limit = %d, want 5", store.gotLimit)
	}
	assertARecord(t, result.Records[0], map[string]any{"skill_id": "skill-1", "name": "Needle skill", "version": "1.2.3", "status": "enabled"}, "name", "skill_id", "status", "version")
	assertARecordDoesNotContain(t, result.Records[0], "path", "/secret/skill.md", "private body", "description")
	assertARecallEmpty(t, registry, dataRecallInternalContext(t), "skill_governance", "skill_manifests", "unknown")
	assertARecallDenied(t, registry, dataRecallUserContext(t), "skill_governance", "skill_manifests")
}

func TestRegisterDataRecallWorkstream(t *testing.T) {
	created := time.Date(2026, 8, 13, 5, 6, 7, 0, time.UTC)
	store := &dataRecallWorkstreamListerStub{goals: []domainworkstream.Goal{
		{GoalID: "goal-1", WorkstreamID: "ws-1", Title: "Needle goal", Status: domainworkstream.StatusActive, Description: "secret description", CreatedAt: created},
		{GoalID: "goal-2", WorkstreamID: "ws-2", Title: "Other", Status: domainworkstream.StatusDraft, CreatedAt: created.Add(time.Minute)},
	}}
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallWorkstream(registry, store); err != nil {
		t.Fatalf("registerRuntimeDataRecallWorkstream() error = %v", err)
	}
	result := recallDataRecallAdapter(t, registry, dataRecallUserContext(t), toolsinfra.DataRecallRequest{Store: "workstream", Operation: "goals", Query: "NEEDLE", Limit: 6})
	assertRecallResult(t, result, "workstream", "goals", 1)
	if store.gotLimit != 6 {
		t.Fatalf("ListGoals limit = %d, want 6", store.gotLimit)
	}
	assertARecord(t, result.Records[0], map[string]any{"goal_id": "goal-1", "title": "Needle goal", "status": "active", "workstream_id": "ws-1", "created_at": created}, "created_at", "goal_id", "status", "title", "workstream_id")
	assertARecordDoesNotContain(t, result.Records[0], "description", "secret description", "success_criteria", "verification")
	assertARecallEmpty(t, registry, dataRecallUserContext(t), "workstream", "goals", "unknown")
	assertARecallDenied(t, registry, dataRecallInternalContext(t), "workstream", "goals")
}

func TestRegisterDataRecallRevenue(t *testing.T) {
	updated := time.Date(2026, 8, 13, 6, 7, 8, 0, time.UTC)
	store := &dataRecallRevenueListerStub{opportunities: []domainrevenue.Opportunity{
		{OpportunityID: "opportunity-1", Title: "Needle opportunity", Summary: "safe summary", SourceKind: "secret-source", ExpectedRevenue: 999999, UpdatedAt: updated},
		{OpportunityID: "opportunity-2", Title: "Other", Summary: "other", UpdatedAt: updated.Add(time.Minute)},
	}}
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallRevenue(registry, store); err != nil {
		t.Fatalf("registerRuntimeDataRecallRevenue() error = %v", err)
	}
	result := recallDataRecallAdapter(t, registry, dataRecallInternalContext(t), toolsinfra.DataRecallRequest{Store: "revenue", Operation: "opportunities", Query: "NEEDLE", Limit: 7})
	assertRecallResult(t, result, "revenue", "opportunities", 1)
	if store.gotLimit != 7 {
		t.Fatalf("ListOpportunities limit = %d, want 7", store.gotLimit)
	}
	assertARecord(t, result.Records[0], map[string]any{"opportunity_id": "opportunity-1", "title": "Needle opportunity", "summary": "safe summary", "updated_at": updated}, "opportunity_id", "summary", "title", "updated_at")
	assertARecordDoesNotContain(t, result.Records[0], "source_kind", "secret-source", "expected_revenue", "999999")
	assertARecallEmpty(t, registry, dataRecallInternalContext(t), "revenue", "opportunities", "unknown")
	assertARecallDenied(t, registry, dataRecallUserContext(t), "revenue", "opportunities")
}

func TestRegisterDataRecallAdaptersARejectUnavailableDependencies(t *testing.T) {
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallAdvisor(registry, nil); err == nil {
		t.Fatal("advisor adapter must reject nil store")
	}
	if err := registerRuntimeDataRecallAdvisor(nil, &dataRecallAdvisorListerStub{}); err == nil {
		t.Fatal("advisor adapter must reject nil registry")
	}
}

func assertARecord(t *testing.T, got map[string]any, want map[string]any, keys ...string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("record = %#v, want %#v", got, want)
	}
	if gotKeys := sortedDataRecallRecordKeys(got); !reflect.DeepEqual(gotKeys, append([]string(nil), keys...)) {
		t.Fatalf("record keys = %#v, want %#v", gotKeys, keys)
	}
}

func assertARecordDoesNotContain(t *testing.T, record map[string]any, forbidden ...string) {
	t.Helper()
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("json.Marshal(record) error = %v", err)
	}
	for _, value := range forbidden {
		if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(value)) {
			t.Fatalf("record leaked %q: %s", value, encoded)
		}
	}
}

func assertARecallEmpty(t *testing.T, registry *runtimeDataRecallRegistry, ctx context.Context, store, operation, query string) {
	t.Helper()
	result := recallDataRecallAdapter(t, registry, ctx, toolsinfra.DataRecallRequest{Store: store, Operation: operation, Query: query, Limit: 10})
	if len(result.Records) != 0 || result.Partial {
		t.Fatalf("no-match result = %#v", result)
	}
}

func assertARecallDenied(t *testing.T, registry *runtimeDataRecallRegistry, ctx context.Context, store, operation string) {
	t.Helper()
	if _, err := registry.Recall(ctx, toolsinfra.DataRecallRequest{Store: store, Operation: operation, Query: "record", Limit: 1}); !errors.Is(err, errDataRecallRegistryDenied) {
		t.Fatalf("Recall(%s/%s) error = %v, want denied", store, operation, err)
	}
}
