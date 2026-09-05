package main

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	appstore "github.com/Nyukimin/RenCrow_CORE/internal/application/durablestore"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	domainbrowser "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	domaincomplexity "github.com/Nyukimin/RenCrow_CORE/internal/domain/complexity"
	domaindurable "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRegisterDataRecallPersonaArchitecture(t *testing.T) {
	store := &dataRecallPersonaListerStub{
		canonicals: []domainpersona.CanonicalResponseLog{
			{EventID: "evt-1", CharacterID: "mio", ResponseID: "Block-Destructive", MessageID: "secret-message", Used: true, Rewritten: false, CreatedAt: time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)},
			{EventID: "evt-2", CharacterID: "mio", ResponseID: "unrelated", CreatedAt: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)},
		},
	}
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallPersonaArchitecture(registry, store); err != nil {
		t.Fatalf("registerRuntimeDataRecallPersonaArchitecture() error = %v", err)
	}
	result := recallDataRecallAdapter(t, registry, dataRecallUserContext(t), toolsinfra.DataRecallRequest{
		Store: "persona_architecture", Operation: "canonical_responses", Query: "BLOCK-DESTRUCTIVE", Limit: 1,
	})
	assertRecallResult(t, result, "persona_architecture", "canonical_responses", 1)
	if store.gotLimit != 1 {
		t.Fatalf("persona ListCanonicalResponseLogs limit = %d, want 1", store.gotLimit)
	}
	record := result.Records[0]
	if got, want := record["response_id"], "Block-Destructive"; got != want {
		t.Fatalf("response_id = %#v, want %#v", got, want)
	}
	if _, leaked := record["message_id"]; leaked {
		t.Fatal("persona projection must not expose message_id or response body")
	}
	if _, leaked := record["response"]; leaked {
		t.Fatal("persona projection must not expose response body")
	}
	assertRecordKeys(t, record, "created_at", "response_id", "status", "trigger")
}

func TestRegisterDataRecallBrowserTraceToAPI(t *testing.T) {
	created := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	store := &dataRecallBrowserListerStub{
		candidates: []domainbrowser.APICandidate{
			{CandidateID: "candidate-safe", Method: "GET", ObservedURL: "https://user:password@example.com/private?token=secret", Status: "candidate", CreatedAt: created},
			{CandidateID: "candidate-rejected", Method: "POST", ObservedURL: "https://example.com/rejected", Status: "candidate", CreatedAt: created},
		},
		validations: []domainbrowser.APICandidateValidationResult{
			{ValidationID: "validation-safe", CandidateID: "candidate-safe", Passed: true, Status: "validated", CreatedAt: created},
			{ValidationID: "validation-rejected", CandidateID: "candidate-rejected", Passed: false, Status: "rejected", CreatedAt: created},
		},
	}
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallBrowserTraceToAPI(registry, store); err != nil {
		t.Fatalf("registerRuntimeDataRecallBrowserTraceToAPI() error = %v", err)
	}
	result := recallDataRecallAdapter(t, registry, dataRecallInternalContext(t), toolsinfra.DataRecallRequest{
		Store: "browser_trace_to_api", Operation: "validated_candidates", Query: "EXAMPLE.COM", Limit: 4,
	})
	assertRecallResult(t, result, "browser_trace_to_api", "validated_candidates", 1)
	if store.gotCandidateLimit != 4 || store.gotValidationLimit != 4 {
		t.Fatalf("browser limits = candidates:%d validations:%d, want 4/4", store.gotCandidateLimit, store.gotValidationLimit)
	}
	record := result.Records[0]
	if got, want := record["candidate_id"], "candidate-safe"; got != want {
		t.Fatalf("candidate_id = %#v, want %#v", got, want)
	}
	if got, want := record["host"], "example.com"; got != want {
		t.Fatalf("host = %#v, want %#v", got, want)
	}
	if got, want := record["status"], "validated"; got != want {
		t.Fatalf("status = %#v, want %#v", got, want)
	}
	encoded := strings.ToLower(string(mustJSON(record)))
	for _, forbidden := range []string{"https://", "password", "token", "private", "query", "auth", "header", "body"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("browser projection leaked %q: %s", forbidden, encoded)
		}
	}
	assertRecordKeys(t, record, "candidate_id", "host", "method", "status")
}

func TestRegisterDataRecallComplexityHotspot(t *testing.T) {
	store := &dataRecallComplexityListerStub{hotspots: []domaincomplexity.Hotspot{
		{HotspotID: "hot-1", ScanID: "scan-1", FilePath: "internal/worker.go", HotspotType: "Nested_Lookup", RiskLevel: "High", Summary: "source excerpt must not be returned", CreatedAt: time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)},
	}}
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallComplexityHotspot(registry, store); err != nil {
		t.Fatalf("registerRuntimeDataRecallComplexityHotspot() error = %v", err)
	}
	result := recallDataRecallAdapter(t, registry, dataRecallInternalContext(t), toolsinfra.DataRecallRequest{
		Store: "complexity_hotspot", Operation: "hotspots", Query: "NESTED", Limit: 3,
	})
	assertRecallResult(t, result, "complexity_hotspot", "hotspots", 1)
	if store.gotLimit != 3 {
		t.Fatalf("complexity ListHotspots limit = %d, want 3", store.gotLimit)
	}
	record := result.Records[0]
	assertRecordKeys(t, record, "category", "file_path", "hotspot_id", "severity", "status")
	if strings.Contains(string(mustJSON(record)), "source excerpt") {
		t.Fatal("complexity projection must not expose source excerpts")
	}
	if strings.HasPrefix(record["file_path"].(string), "/") || strings.Contains(record["file_path"].(string), "..") {
		t.Fatalf("complexity file path is not relative-safe: %#v", record["file_path"])
	}
}

func TestRegisterDataRecallSuperAgentHarness(t *testing.T) {
	runID, taskID := modulecore.NewRunID(), modulecore.NewTaskID()
	store := &dataRecallSuperAgentListerStub{runs: []domainsuperagent.AgentRun{
		{RunID: runID, TaskID: taskID, AgentType: "Luna", Goal: "secret context", Status: "completed", StartedAt: time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC), CompletedAt: time.Date(2026, 8, 13, 0, 1, 0, 0, time.UTC), Summary: "private context pack"},
	}}
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallSuperAgentHarness(registry, store); err != nil {
		t.Fatalf("registerRuntimeDataRecallSuperAgentHarness() error = %v", err)
	}
	result := recallDataRecallAdapter(t, registry, dataRecallInternalContext(t), toolsinfra.DataRecallRequest{
		Store: "super_agent_harness", Operation: "agent_runs", Query: "luna", Limit: 2,
	})
	assertRecallResult(t, result, "super_agent_harness", "agent_runs", 1)
	if store.gotLimit != 2 {
		t.Fatalf("superagent ListAgentRuns limit = %d, want 2", store.gotLimit)
	}
	record := result.Records[0]
	assertRecordKeys(t, record, "agent", "completed_at", "run_id", "started_at", "status")
	encoded := string(mustJSON(record))
	for _, forbidden := range []string{"secret context", "private context", "goal", "summary", "context_pack"} {
		if strings.Contains(strings.ToLower(encoded), forbidden) {
			t.Fatalf("superagent projection leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestRegisterDataRecallAIWorkflow(t *testing.T) {
	store := &dataRecallAIWorkflowListerStub{commands: []domainai.CommandRegistry{
		{CommandName: "/review-architecture", FilePath: "/secrets/commands/review.md", Description: "Review architecture", UpdatedAt: time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)},
	}}
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallAIWorkflow(registry, store); err != nil {
		t.Fatalf("registerRuntimeDataRecallAIWorkflow() error = %v", err)
	}
	result := recallDataRecallAdapter(t, registry, dataRecallInternalContext(t), toolsinfra.DataRecallRequest{
		Store: "ai_workflow", Operation: "command_registry", Query: "REVIEW", Limit: 5,
	})
	assertRecallResult(t, result, "ai_workflow", "command_registry", 1)
	if store.gotLimit != 5 {
		t.Fatalf("ai workflow ListCommandRegistries limit = %d, want 5", store.gotLimit)
	}
	record := result.Records[0]
	assertRecordKeys(t, record, "command", "description", "name", "status")
	encoded := strings.ToLower(string(mustJSON(record)))
	for _, forbidden := range []string{"/secrets/", "file_path", "executable", "body"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("ai workflow projection leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestRegisterDataRecallDurableStoreWorkflow(t *testing.T) {
	created := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	store := &dataRecallDurableStoreStub{result: &domaindurable.WorkflowResult{
		Status:    domaindurable.StatusCompleted,
		CreatedAt: created,
		Requirement: domaindurable.StorageRequirement{
			RequirementID:    "sr-1",
			DedupeKey:        "dedupe-key-1",
			RequestID:        "request-1",
			UserScope:        "user-1",
			RequestedOutcome: domaindurable.OutcomeAssess,
			FactsToStore:     []string{"secret payload"},
		},
		Classification: domaindurable.Classification{OwnerModule: "RenCrow_CORE"},
	}}
	store.receipt = &domaindurable.RequestReceipt{RequestID: "request-1", UserScope: "user-1", PayloadHash: "hash", RequirementID: "sr-1", CreatedAt: created}
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallDurableStoreWorkflow(registry, store); err != nil {
		t.Fatalf("registerRuntimeDataRecallDurableStoreWorkflow() error = %v", err)
	}
	result := recallDataRecallAdapter(t, registry, dataRecallUserContext(t), toolsinfra.DataRecallRequest{
		Store: "durable_store_workflow", Operation: "exact_request", Query: "request-1", Limit: 50,
	})
	assertRecallResult(t, result, "durable_store_workflow", "exact_request", 1)
	if store.gotKey != "request-1" {
		t.Fatalf("durable lookup key = %q, want exact query", store.gotKey)
	}
	record := result.Records[0]
	assertRecordKeys(t, record, "created_at", "deduplicated", "lifecycle", "owner_module", "reason_code", "requested_outcome", "requirement_id", "status")
	encoded := strings.ToLower(string(mustJSON(record)))
	for _, forbidden := range []string{"secret payload", "facts_to_store", "dedupe_key", "request_id", "payload"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("durable projection leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestRegisterDataRecallDurableStoreWorkflowRequirement(t *testing.T) {
	created := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	updated := created.Add(time.Minute)
	stored := &domaindurable.WorkflowResult{
		Status:    domaindurable.StatusCompleted,
		Lifecycle: domaindurable.LifecycleValidated,
		CreatedAt: created,
		UpdatedAt: updated,
		Reason:    "private reason must not be projected",
		Requirement: domaindurable.StorageRequirement{
			RequirementID:    "sr-1",
			DedupeKey:        "dedupe-key-1",
			RequestID:        "request-1",
			UserScope:        "user-1",
			FactsToStore:     []string{"private message"},
			RequestedOutcome: domaindurable.OutcomeAssess,
		},
	}
	store := &dataRecallDurableStoreStub{result: stored}
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallDurableStoreWorkflow(registry, store); err != nil {
		t.Fatalf("registerRuntimeDataRecallDurableStoreWorkflow() error = %v", err)
	}
	result := recallDataRecallAdapter(t, registry, dataRecallUserContext(t), toolsinfra.DataRecallRequest{
		Store: "durable_store_workflow", Operation: "requirement", Query: "sr-1", Limit: 1,
	})
	assertRecallResult(t, result, "durable_store_workflow", "requirement", 1)
	if store.gotRequirementKey != "sr-1" {
		t.Fatalf("durable requirement lookup key = %q, want exact query", store.gotRequirementKey)
	}
	record := result.Records[0]
	assertRecordKeys(t, record, "created_at", "dedupe_key", "lifecycle", "request_id", "requirement_id", "status", "updated_at")
	if record["request_id"] != "request-1" || record["requirement_id"] != "sr-1" || record["dedupe_key"] != "dedupe-key-1" || record["status"] != "completed" || record["lifecycle"] != "validated" || record["created_at"] != created || record["updated_at"] != updated {
		t.Fatalf("durable requirement projection = %#v", record)
	}
	encoded := strings.ToLower(string(mustJSON(record)))
	for _, forbidden := range []string{"private message", "facts_to_store", "user_scope", "reason", "path", "database", "sql"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("durable requirement projection leaked %q: %s", forbidden, encoded)
		}
	}
	var foundRoute bool
	for _, route := range registry.Snapshot() {
		if route.Store == "durable_store_workflow" && route.Operation == "requirement" && route.Access == dataRecallAccessUser {
			foundRoute = true
		}
	}
	if !foundRoute {
		t.Fatalf("durable requirement route missing from snapshot: %#v", registry.Snapshot())
	}

	otherUser := &dataRecallDurableStoreStub{result: &domaindurable.WorkflowResult{
		Status:    domaindurable.StatusCompleted,
		Lifecycle: domaindurable.LifecycleValidated,
		CreatedAt: created,
		UpdatedAt: updated,
		Requirement: domaindurable.StorageRequirement{
			RequirementID: "sr-1", DedupeKey: "dedupe-key-1", RequestID: "request-1", UserScope: "user-2",
		},
	}}
	otherRegistry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallDurableStoreWorkflow(otherRegistry, otherUser); err != nil {
		t.Fatal(err)
	}
	otherResult := recallDataRecallAdapter(t, otherRegistry, dataRecallUserContext(t), toolsinfra.DataRecallRequest{
		Store: "durable_store_workflow", Operation: "requirement", Query: "sr-1", Limit: 1,
	})
	assertRecallResult(t, otherResult, "durable_store_workflow", "requirement", 0)

	missingRegistry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallDurableStoreWorkflow(missingRegistry, &dataRecallDurableStoreStub{}); err != nil {
		t.Fatal(err)
	}
	missingResult := recallDataRecallAdapter(t, missingRegistry, dataRecallUserContext(t), toolsinfra.DataRecallRequest{
		Store: "durable_store_workflow", Operation: "requirement", Query: "missing", Limit: 1,
	})
	assertRecallResult(t, missingResult, "durable_store_workflow", "requirement", 0)

	malformedRegistry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallDurableStoreWorkflow(malformedRegistry, &dataRecallDurableStoreStub{result: &domaindurable.WorkflowResult{
		Status:    domaindurable.StatusCompleted,
		Lifecycle: domaindurable.LifecycleValidated,
		CreatedAt: created,
		UpdatedAt: updated,
		Requirement: domaindurable.StorageRequirement{
			RequirementID: "sr-1", RequestID: "request-1", UserScope: "user-1",
		},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := malformedRegistry.Recall(dataRecallUserContext(t), toolsinfra.DataRecallRequest{
		Store: "durable_store_workflow", Operation: "requirement", Query: "sr-1", Limit: 1,
	}); err == nil {
		t.Fatal("malformed durable workflow record must fail recall")
	}
}

func TestRegisterDataRecallAdaptersRejectUnavailableDependencies(t *testing.T) {
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallPersonaArchitecture(registry, nil); err == nil {
		t.Fatal("persona adapter must reject nil store")
	}
	if err := registerRuntimeDataRecallPersonaArchitecture(nil, &dataRecallPersonaListerStub{}); err == nil {
		t.Fatal("persona adapter must reject nil registry")
	}
}

func recallDataRecallAdapter(t *testing.T, registry *runtimeDataRecallRegistry, ctx context.Context, request toolsinfra.DataRecallRequest) runtimeDataRecallResult {
	t.Helper()
	value, err := registry.Recall(ctx, request)
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	result, ok := value.(runtimeDataRecallResult)
	if !ok {
		t.Fatalf("Recall() result type = %T, want runtimeDataRecallResult", value)
	}
	return result
}

func assertRecallResult(t *testing.T, result runtimeDataRecallResult, store, operation string, records int) {
	t.Helper()
	if result.Store != store || result.Operation != operation || len(result.Records) != records || result.Partial {
		t.Fatalf("unexpected recall result: %#v", result)
	}
}

func assertRecordKeys(t *testing.T, record map[string]any, want ...string) {
	t.Helper()
	if got := sortedDataRecallRecordKeys(record); !reflect.DeepEqual(got, append([]string(nil), want...)) {
		t.Fatalf("record keys = %#v, want %#v", got, want)
	}
}

func sortedDataRecallRecordKeys(record map[string]any) []string {
	keys := make([]string, 0, len(record))
	for key := range record {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// The adapter tests intentionally use only the existing typed read interfaces.
type dataRecallPersonaListerStub struct {
	canonicals []domainpersona.CanonicalResponseLog
	gotLimit   int
}

func (s *dataRecallPersonaListerStub) ListDiscomfortLogs(context.Context, int) ([]domainpersona.DiscomfortLog, error) {
	return nil, nil
}
func (s *dataRecallPersonaListerStub) ListTriggerLogs(context.Context, int) ([]domainpersona.TriggerLog, error) {
	return nil, nil
}
func (s *dataRecallPersonaListerStub) ListCanonicalResponseLogs(_ context.Context, limit int) ([]domainpersona.CanonicalResponseLog, error) {
	s.gotLimit = limit
	return s.canonicals, nil
}
func (s *dataRecallPersonaListerStub) ListObservationLogs(context.Context, int) ([]domainpersona.ObservationLog, error) {
	return nil, nil
}
func (s *dataRecallPersonaListerStub) ListMetaProfileUpdates(context.Context, int) ([]domainpersona.MetaProfileUpdate, error) {
	return nil, nil
}
func (s *dataRecallPersonaListerStub) ListInterfaceSessions(context.Context, int) ([]domainpersona.InterfaceSession, error) {
	return nil, nil
}

type dataRecallBrowserListerStub struct {
	candidates         []domainbrowser.APICandidate
	validations        []domainbrowser.APICandidateValidationResult
	gotCandidateLimit  int
	gotValidationLimit int
}

func (s *dataRecallBrowserListerStub) ListTraceRuns(context.Context, int) ([]domainbrowser.TraceRun, error) {
	return nil, nil
}
func (s *dataRecallBrowserListerStub) ListAPICandidates(_ context.Context, limit int) ([]domainbrowser.APICandidate, error) {
	s.gotCandidateLimit = limit
	return s.candidates, nil
}
func (s *dataRecallBrowserListerStub) ListAPICandidateSchemas(context.Context, int) ([]domainbrowser.APICandidateSchema, error) {
	return nil, nil
}
func (s *dataRecallBrowserListerStub) ListAPICandidateValidationResults(_ context.Context, limit int) ([]domainbrowser.APICandidateValidationResult, error) {
	s.gotValidationLimit = limit
	return s.validations, nil
}
func (s *dataRecallBrowserListerStub) ListAPICoverageReports(context.Context, int) ([]domainbrowser.APICoverageReport, error) {
	return nil, nil
}
func (s *dataRecallBrowserListerStub) ListAPIArtifacts(context.Context, int) ([]domainbrowser.APIArtifact, error) {
	return nil, nil
}

type dataRecallComplexityListerStub struct {
	hotspots []domaincomplexity.Hotspot
	gotLimit int
}

func (s *dataRecallComplexityListerStub) ListScanEvents(context.Context, int) ([]domaincomplexity.ScanEvent, error) {
	return nil, nil
}
func (s *dataRecallComplexityListerStub) ListHotspots(_ context.Context, limit int) ([]domaincomplexity.Hotspot, error) {
	s.gotLimit = limit
	return s.hotspots, nil
}
func (s *dataRecallComplexityListerStub) ListHotspotEvidence(context.Context, int) ([]domaincomplexity.HotspotEvidence, error) {
	return nil, nil
}
func (s *dataRecallComplexityListerStub) ListReportArtifacts(context.Context, int) ([]domaincomplexity.ReportArtifact, error) {
	return nil, nil
}

type dataRecallSuperAgentListerStub struct {
	runs     []domainsuperagent.AgentRun
	gotLimit int
}

func (s *dataRecallSuperAgentListerStub) ListAgentRuns(_ context.Context, limit int) ([]domainsuperagent.AgentRun, error) {
	s.gotLimit = limit
	return s.runs, nil
}
func (s *dataRecallSuperAgentListerStub) ListSubagentTasks(context.Context, int) ([]domainsuperagent.SubagentTask, error) {
	return nil, nil
}
func (s *dataRecallSuperAgentListerStub) ListContextPacks(context.Context, int) ([]domainsuperagent.ContextPack, error) {
	return nil, nil
}
func (s *dataRecallSuperAgentListerStub) ListMessageChannels(context.Context, int) ([]domainsuperagent.MessageChannel, error) {
	return nil, nil
}
func (s *dataRecallSuperAgentListerStub) ListRunQueueItems(context.Context, int) ([]domainsuperagent.RunQueueItem, error) {
	return nil, nil
}
func (s *dataRecallSuperAgentListerStub) GetByID(context.Context, modulecore.EventID) (modulecore.EventEnvelope, bool, error) {
	return modulecore.EventEnvelope{}, false, nil
}
func (s *dataRecallSuperAgentListerStub) ListByComponent(context.Context, string, int) ([]modulecore.EventEnvelope, error) {
	return nil, nil
}

type dataRecallAIWorkflowListerStub struct {
	commands []domainai.CommandRegistry
	gotLimit int
}

func (s *dataRecallAIWorkflowListerStub) ListProjectMemoryIndexes(context.Context, int) ([]domainai.ProjectMemoryIndex, error) {
	return nil, nil
}
func (s *dataRecallAIWorkflowListerStub) ListWorktreeRegistries(context.Context, int) ([]domainai.WorktreeRegistry, error) {
	return nil, nil
}
func (s *dataRecallAIWorkflowListerStub) ListCommandRegistries(_ context.Context, limit int) ([]domainai.CommandRegistry, error) {
	s.gotLimit = limit
	return s.commands, nil
}
func (s *dataRecallAIWorkflowListerStub) ListContextUsages(context.Context, int) ([]domainai.ContextUsage, error) {
	return nil, nil
}
func (s *dataRecallAIWorkflowListerStub) GetByID(context.Context, modulecore.EventID) (modulecore.EventEnvelope, bool, error) {
	return modulecore.EventEnvelope{}, false, nil
}
func (s *dataRecallAIWorkflowListerStub) ListByComponent(context.Context, string, int) ([]modulecore.EventEnvelope, error) {
	return nil, nil
}

type dataRecallDurableStoreStub struct {
	result            *domaindurable.WorkflowResult
	receipt           *domaindurable.RequestReceipt
	requirementErr    error
	gotKey            string
	gotRequirementKey string
}

var _ appstore.Store = (*dataRecallDurableStoreStub)(nil)

func (s *dataRecallDurableStoreStub) FindByDedupeKey(_ context.Context, key string) (*domaindurable.WorkflowResult, error) {
	s.gotKey = key
	return s.result, nil
}
func (s *dataRecallDurableStoreStub) FindByRequestID(_ context.Context, key string) (*domaindurable.RequestReceipt, error) {
	s.gotKey = key
	return s.receipt, nil
}
func (s *dataRecallDurableStoreStub) FindByRequirementID(_ context.Context, key string) (*domaindurable.WorkflowResult, error) {
	s.gotRequirementKey = key
	if s.requirementErr != nil {
		return nil, s.requirementErr
	}
	if s.result == nil || s.result.Requirement.RequirementID != key {
		return nil, nil
	}
	return s.result, nil
}
func (s *dataRecallDurableStoreStub) SaveWithReceipt(context.Context, *domaindurable.WorkflowResult, domaindurable.RequestReceipt) error {
	return nil
}
func (s *dataRecallDurableStoreStub) Save(context.Context, domaindurable.WorkflowResult) error {
	return nil
}

var _ viewer.PersonaObservationLister = (*dataRecallPersonaListerStub)(nil)
var _ viewer.BrowserTraceAPILister = (*dataRecallBrowserListerStub)(nil)
var _ viewer.ComplexityHotspotLister = (*dataRecallComplexityListerStub)(nil)
var _ viewer.SuperAgentLister = (*dataRecallSuperAgentListerStub)(nil)
var _ viewer.AIWorkflowLister = (*dataRecallAIWorkflowListerStub)(nil)
