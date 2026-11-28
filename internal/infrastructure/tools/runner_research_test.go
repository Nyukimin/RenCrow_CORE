package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coreresearch "github.com/Nyukimin/RenCrow_CORE/internal/application/research"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	contract "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	engine "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type researchRunnerStore struct{ records map[string]contract.RunRecord }

type researchRunnerRecall struct{ result contract.RecallResult }

func (r researchRunnerRecall) Recall(context.Context, contract.Envelope, contract.RecallRequest) (contract.RecallResult, error) {
	return r.result, nil
}

func (s *researchRunnerStore) Put(_ context.Context, data []byte) (contract.ArtifactRef, error) {
	return contract.ArtifactRef{Kind: "artifact", Ref: "artifact-ref", Hash: "hash"}, nil
}
func (s *researchRunnerStore) Get(context.Context, contract.ArtifactRef) ([]byte, error) {
	return nil, fs.ErrNotExist
}
func (s *researchRunnerStore) Load(_ context.Context, runID string) (contract.RunRecord, error) {
	record, ok := s.records[runID]
	if !ok {
		return contract.RunRecord{}, fs.ErrNotExist
	}
	return record, nil
}
func (s *researchRunnerStore) Save(_ context.Context, record contract.RunRecord) error {
	s.records[record.Envelope.RunID] = record
	return nil
}

func researchRunnerContext(t *testing.T) context.Context {
	t.Helper()
	ctx, err := domainexecution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	scope, err := tool.NewToolExecutionScope("research-request-1", tool.ActorKindAgent, "agent-a", "", []string{tool.DataScopePublic}, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	return tool.WithToolExecutionScope(ctx, scope)
}

func TestToolRunnerResearchRegistrationAndTrustedEnvelope(t *testing.T) {
	store := &researchRunnerStore{records: map[string]contract.RunRecord{}}
	eng := &engine.Engine{Ports: engine.Ports{Store: store}}
	recorder := &researchRecorderFixture{}
	runner := NewToolRunner(ToolRunnerConfig{}).WithResearchEngine(eng).WithResearchResultRecorder(recorder)

	metas, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"research.run": false, "research.status": false, "research.recall": false, "research.watch": false}
	for _, metadata := range metas {
		if _, ok := want[metadata.ToolID]; ok {
			want[metadata.ToolID] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("metadata for %s not registered", name)
		}
	}

	ctx := researchRunnerContext(t)
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	response, err := runner.ExecuteV2(ctx, researchRunToolName, map[string]any{"request": map[string]any{
		"schema_version": contract.RequestSchema, "mode": "search_only", "objective": "test research", "seed_urls": []any{"https://example.com"}, "cost_profile": "zero_incremental_cost",
	}})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("research.run failed: response=%+v err=%v", response, err)
	}
	if len(store.records) != 1 {
		t.Fatalf("stored records = %d, want 1", len(store.records))
	}
	if recorder.calls != 1 || recorder.result.Status != "succeeded" {
		t.Fatal("Research Task facts were not recorded")
	}
	for _, record := range store.records {
		if record.Envelope.TaskID != string(identity.TaskID) || record.Envelope.RunID != string(identity.RunID) || record.Envelope.TraceID != string(identity.TraceID) {
			t.Fatalf("identity was not copied from trusted context: %+v", record.Envelope)
		}
		if record.Envelope.RequestID != "research-request-1" || record.Envelope.Actor != "agent-a" || record.Envelope.DataScope != tool.DataScopePublic || record.Envelope.OwnerID != "" || record.Envelope.PolicyRevision != ResearchPolicyRevision {
			t.Fatalf("unexpected trusted envelope: %+v", record.Envelope)
		}
	}

	response, err = runner.ExecuteV2(ctx, researchStatusToolName, map[string]any{"run_id": string(identity.RunID)})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("research.status failed: response=%+v err=%v", response, err)
	}
	response, err = runner.ExecuteV2(ctx, researchRecallToolName, map[string]any{"request": map[string]any{"query": "test"}})
	if err != nil || response == nil || response.Error == nil || response.Error.Code != tool.ErrNotFound {
		t.Fatalf("unconnected recall target must not look like an empty successful query: response=%+v err=%v", response, err)
	}
	missing, ok := response.Error.Details["recall_result"].(contract.RecallResult)
	if !ok || len(missing.Missing) == 0 {
		t.Fatalf("unavailable recall result was not preserved: %#v", response.Error.Details)
	}
	emptyRunner := NewToolRunner(ToolRunnerConfig{}).WithResearchEngine(&engine.Engine{Ports: engine.Ports{Recall: researchRunnerRecall{result: contract.RecallResult{Hits: []contract.Hit{}, Missing: []string{}}}}})
	response, err = emptyRunner.ExecuteV2(ctx, researchRecallToolName, map[string]any{"request": map[string]any{"query": "test"}})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("connected recall returning no matches must remain a successful empty query: response=%+v err=%v", response, err)
	}
	response, err = runner.ExecuteV2(ctx, researchWatchToolName, map[string]any{"request": map[string]any{
		"objective": "monitor source", "source_refs": []any{"source-1"}, "source_kinds": []any{"web"}, "cadence": "daily", "output_contract": map[string]any{}, "initial_run": false,
	}})
	if err != nil || response == nil || response.Error == nil {
		t.Fatalf("unconnected research.watch should return an unavailable error: response=%+v err=%v", response, err)
	}
}

func TestResearchStatusReadsWatchUnderTrustedPublicActorScope(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "research-watch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if _, err := store.SaveSourceRegistryEntry(ctx, l1sqlite.L1SourceRegistryEntry{SourceID: "source:agent", URL: "https://example.org/source", Kind: l1sqlite.L1SourceKindWebGather, Enabled: true, TrustScore: 1, FetchInterval: time.Minute, LicenseNote: "fixture policy"}); err != nil {
		t.Fatal(err)
	}
	watch, err := store.RegisterResearchWatch(ctx, "public", "agent-a", `{"objective":"watch source"}`, []string{"source:agent"}, time.Hour, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	engineOwner := &engine.Engine{Ports: engine.Ports{Watch: coreresearch.NewWatchAdapter(store, nil)}}
	runner := NewToolRunner(ToolRunnerConfig{}).WithResearchEngine(engineOwner)
	response, err := runner.ExecuteV2(researchRunnerContext(t), researchStatusToolName, map[string]any{"watch_id": watch.WatchID})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("watch status failed: response=%+v err=%v", response, err)
	}
	item, ok := response.Result.(coreresearch.WatchManagementItem)
	if !ok || item.WatchID != watch.WatchID || item.OwnerID != "agent-a" || item.Scope != "public" || item.Receipt.CanonicalID != watch.WatchID {
		t.Fatalf("watch status response=%+v item=%+v err=%v", response, item, err)
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "payload_json") || strings.Contains(string(encoded), "cursor_json") || strings.Contains(string(encoded), "lease_token") {
		t.Fatalf("watch status exposed private owner data: %s", encoded)
	}
	for _, args := range []map[string]any{{}, {"run_id": "run_1", "watch_id": watch.WatchID}, {"watch_id": watch.WatchID, "owner_id": "agent-a"}} {
		invalid, err := runner.ExecuteV2(researchRunnerContext(t), researchStatusToolName, args)
		if err != nil || invalid == nil || invalid.Error == nil || invalid.Error.Code != tool.ErrValidationFailed {
			t.Errorf("invalid status args accepted: args=%v response=%+v err=%v", args, invalid, err)
		}
	}
}

type researchRecorderFixture struct {
	calls        int
	preflights   int
	result       contract.Result
	fail         bool
	preflightErr error
}

func (r *researchRecorderFixture) Preflight(context.Context, contract.Envelope) error {
	r.preflights++
	return r.preflightErr
}

func (r *researchRecorderFixture) Record(_ context.Context, _ contract.Envelope, result contract.Result) error {
	r.calls++
	r.result = result
	if r.fail {
		return context.DeadlineExceeded
	}
	return nil
}

func (r *researchRecorderFixture) Reconcile(_ context.Context, _ contract.Envelope, result contract.Result) (contract.Result, error) {
	return result, nil
}

func TestToolRunnerResearchRequiresResultProjectionOwner(t *testing.T) {
	store := &researchRunnerStore{records: map[string]contract.RunRecord{}}
	runner := NewToolRunner(ToolRunnerConfig{}).WithResearchEngine(&engine.Engine{Ports: engine.Ports{Store: store}})
	args := map[string]any{"request": map[string]any{"schema_version": contract.RequestSchema, "mode": "search_only", "objective": "test", "seed_urls": []any{"https://example.org/source"}}}
	response, err := runner.ExecuteV2(researchRunnerContext(t), researchRunToolName, args)
	if err != nil || response.Error == nil || len(store.records) != 0 {
		t.Fatal("unconfigured Task projection owner silently omitted")
	}
	recorder := &researchRecorderFixture{fail: true}
	runner.WithResearchResultRecorder(recorder)
	response, err = runner.ExecuteV2(researchRunnerContext(t), researchRunToolName, args)
	if err != nil || response.Error == nil || recorder.calls != 1 {
		t.Fatal("failed Task projection reported as success")
	}
	result, ok := response.Error.Details["research_result"].(contract.Result)
	if !ok || result.Status != "succeeded" {
		t.Fatal("valid source result lost when Task projection failed")
	}
}

func TestToolRunnerResearchPreflightsProjectionBeforeEngine(t *testing.T) {
	store := &researchRunnerStore{records: map[string]contract.RunRecord{}}
	recorder := &researchRecorderFixture{preflightErr: errors.New("typed Task owner unavailable")}
	runner := NewToolRunner(ToolRunnerConfig{}).
		WithResearchEngine(&engine.Engine{Ports: engine.Ports{Store: store}}).
		WithResearchResultRecorder(recorder)
	response, err := runner.ExecuteV2(researchRunnerContext(t), researchRunToolName, map[string]any{"request": map[string]any{
		"schema_version": contract.RequestSchema, "mode": "search_only", "objective": "test research", "cost_profile": "zero_incremental_cost",
	}})
	if err != nil || response == nil || !response.IsError() {
		t.Fatalf("preflight failure response=%+v err=%v", response, err)
	}
	if recorder.preflights != 1 || recorder.calls != 0 || len(store.records) != 0 {
		t.Fatalf("preflight did not stop before Engine: preflights=%d records=%d journal=%d", recorder.preflights, recorder.calls, len(store.records))
	}
}

func TestToolRunnerResearchRejectsAuthorityFieldsAndUnsupportedSaves(t *testing.T) {
	store := &researchRunnerStore{records: map[string]contract.RunRecord{}}
	runner := NewToolRunner(ToolRunnerConfig{}).WithResearchEngine(&engine.Engine{Ports: engine.Ports{Store: store}})
	ctx := researchRunnerContext(t)

	response, err := runner.ExecuteV2(ctx, researchRunToolName, map[string]any{"request": map[string]any{
		"schema_version": contract.RequestSchema, "mode": "search_only", "objective": "test research", "cost_profile": "zero_incremental_cost", "owner_id": "forged",
	}})
	if err != nil || response == nil || response.Error == nil || response.Error.Code != tool.ErrValidationFailed {
		t.Fatalf("authority field was not rejected: response=%+v err=%v", response, err)
	}

	response, err = runner.ExecuteV2(ctx, researchRunToolName, map[string]any{"request": map[string]any{
		"schema_version": contract.RequestSchema, "mode": "collect", "objective": "test research", "cost_profile": "zero_incremental_cost",
		"outputs": map[string]any{"save": []any{map[string]any{"target": "board", "required": true, "destination_ref": "registered"}}},
	}})
	if err != nil || response == nil || response.Error == nil || response.Error.Code != tool.ErrPermissionDenied {
		t.Fatalf("unsupported save target was not blocked: response=%+v err=%v", response, err)
	}
	if len(store.records) != 0 {
		t.Fatalf("unsupported save target reached the engine: records=%d", len(store.records))
	}
}

func TestToolRunnerResearchRejectsMissingTraceAndPreservesFailedResult(t *testing.T) {
	ctx, err := domainexecution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), "")
	if err != nil {
		t.Fatal(err)
	}
	scope, err := tool.NewToolExecutionScope("research-request-1", tool.ActorKindAgent, "agent-a", "", []string{tool.DataScopePublic}, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	ctx = tool.WithToolExecutionScope(ctx, scope)
	runner := NewToolRunner(ToolRunnerConfig{}).WithResearchEngine(&engine.Engine{})
	response, err := runner.ExecuteV2(ctx, researchRecallToolName, map[string]any{"request": map[string]any{"query": "x"}})
	if err != nil || response == nil || response.Error == nil || response.Error.Code != tool.ErrToolExecutionScopeMissing {
		t.Fatalf("missing trace must fail closed: response=%+v err=%v", response, err)
	}

	failed := contract.Result{SchemaVersion: contract.ResultSchema, RunID: "run_x", Status: "partial"}
	response = researchResultResponse(failed)
	if response.Error == nil || response.Error.Code != tool.ErrInternalError {
		t.Fatalf("partial result was treated as success: %+v", response)
	}
	encoded, err := json.Marshal(response.Error.Details["research_result"])
	if err != nil {
		t.Fatal(err)
	}
	var got contract.Result
	if err := json.Unmarshal(encoded, &got); err != nil || got.Status != "partial" || got.RunID != failed.RunID {
		t.Fatalf("typed result was not preserved: got=%+v err=%v", got, err)
	}
}

func TestDecodeResearchToolArgsRejectsTrailingAndUnknownFields(t *testing.T) {
	var got struct {
		RunID string `json:"run_id"`
	}
	if err := decodeResearchToolArgs(map[string]any{"run_id": "run_1", "owner_id": "user"}, &got); err == nil {
		t.Fatal("unknown authority field accepted")
	}
	if err := decodeResearchRequest([]byte(`{"run_id":"run_1"} {}`), &got); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	if err := decodeResearchRequest([]byte(`{"run_id":"run_1"}`), &got); err != nil {
		t.Fatalf("valid strict request rejected: %v", err)
	}
}

func TestResearchEngineErrorDoesNotExposeRawError(t *testing.T) {
	response := researchEngineError(errors.New("unavailable: bearer=secret-token"), contract.Result{})
	if response.Error == nil || response.Error.Message == "" || response.Error.Message == "unavailable: bearer=secret-token" {
		t.Fatalf("raw engine error leaked: %+v", response)
	}
	if strings.Contains(response.Error.Message, "secret-token") {
		t.Fatalf("credential leaked: %+v", response.Error)
	}
}

func TestResearchRunSchemaAcceptsNormalizedEngineRequests(t *testing.T) {
	schema := compileResearchRunSchema(t)
	cases := []contract.Request{
		{SchemaVersion: contract.RequestSchema, Mode: "search_only", Objective: "Find sources"},
		{SchemaVersion: contract.RequestSchema, Mode: "collect", Objective: "Collect registered sources"},
		{SchemaVersion: contract.RequestSchema, Mode: "investigate", Objective: "Investigate the topic"},
		{SchemaVersion: contract.RequestSchema, Mode: "collect", Objective: "Read a historical code target", RetrievalTargets: []string{"code"}},
		{SchemaVersion: contract.RequestSchema, Mode: "collect", Objective: "Read a pinned code target", RetrievalTargets: []string{"code"}, RepoRef: "repo-core", Revision: "0123456789abcdef0123456789abcdef01234567"},
		{SchemaVersion: contract.RequestSchema, Mode: "collect", Objective: "Compare a retained PDF and implementation", RetrievalTargets: []string{"documents", "code"}, DocumentRefs: []string{"pdf-ref@pdf-rev"}, RepoRef: "repo-core", Revision: "0123456789abcdef0123456789abcdef01234567"},
		{SchemaVersion: contract.RequestSchema, Mode: "refresh", WatchID: "rw_12345"},
	}
	for _, request := range cases {
		t.Run(request.Mode, func(t *testing.T) {
			raw, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			normalized, err := engine.DecodeRequest(raw)
			if err != nil {
				t.Fatalf("Engine rejected fixture request: %v", err)
			}
			normalizedJSON, err := json.Marshal(normalized)
			if err != nil {
				t.Fatal(err)
			}
			var instance map[string]any
			if err := json.Unmarshal(normalizedJSON, &instance); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(map[string]any{"request": instance}); err != nil {
				t.Fatalf("public schema rejected Engine-normalized request %s: %v\n%s", request.Mode, err, normalizedJSON)
			}
		})
	}
}

func TestResearchCoverageSourceKindsAreDeclaredByRunAndWatchSchemas(t *testing.T) {
	runSchema := compileResearchRunSchema(t)
	runArgs := map[string]any{"request": map[string]any{
		"schema_version": contract.RequestSchema, "mode": "collect", "objective": "Collect web and GitHub sources",
		"source_kinds": []any{"web", "github"}, "cost_profile": "zero_incremental_cost",
		"outputs": map[string]any{"evidence": map[string]any{"required_source_kinds": []any{"github"}}},
	}}
	if err := runSchema.Validate(runArgs); err != nil {
		t.Fatalf("run schema rejected required source coverage: %v", err)
	}
	runArgs["request"].(map[string]any)["outputs"].(map[string]any)["evidence"].(map[string]any)["required_source_kinds"] = []any{1}
	if err := runSchema.Validate(runArgs); err == nil {
		t.Fatal("run schema accepted a non-string required source kind")
	}

	watchSchema := compileResearchWatchSchema(t)
	watchArgs := map[string]any{"request": map[string]any{
		"objective": "Watch web sources", "source_refs": []any{"source:1"}, "source_kinds": []any{"web"},
		"cadence": "daily", "initial_run": false,
		"output_contract": map[string]any{"evidence": map[string]any{"required_source_kinds": []any{"web"}}},
	}}
	if err := watchSchema.Validate(watchArgs); err != nil {
		t.Fatalf("watch schema rejected required source coverage: %v", err)
	}
	watchArgs["request"].(map[string]any)["output_contract"].(map[string]any)["evidence"].(map[string]any)["required_source_kinds"] = []any{false}
	if err := watchSchema.Validate(watchArgs); err == nil {
		t.Fatal("watch schema accepted a non-string required source kind")
	}
}

func TestResearchStatusSchemaRequiresExactlyOneRunOrWatchID(t *testing.T) {
	schema := compileResearchStatusSchema(t)
	for _, tc := range []struct {
		name string
		args map[string]any
		ok   bool
	}{
		{name: "run", args: map[string]any{"run_id": "run_123"}, ok: true},
		{name: "watch", args: map[string]any{"watch_id": "rw_12345"}, ok: true},
		{name: "neither", args: map[string]any{}, ok: false},
		{name: "both", args: map[string]any{"run_id": "run_123", "watch_id": "rw_12345"}, ok: false},
		{name: "unknown", args: map[string]any{"watch_id": "rw_12345", "actor": "other"}, ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := schema.Validate(tc.args)
			if (err == nil) != tc.ok {
				t.Fatalf("schema validation err=%v want success=%t", err, tc.ok)
			}
		})
	}
}

func TestResearchRunSchemaStillRejectsInvalidRequests(t *testing.T) {
	schema := compileResearchRunSchema(t)
	validDocumentRefs := map[string]any{"schema_version": contract.RequestSchema, "mode": "collect", "objective": "topic", "cost_profile": "zero_incremental_cost", "retrieval_targets": []any{"documents"}, "document_refs": []any{"canonical-id"}}
	if err := schema.Validate(map[string]any{"request": validDocumentRefs}); err != nil {
		t.Fatalf("valid bounded document_refs rejected: %v", err)
	}
	invalid := []map[string]any{
		{"schema_version": contract.RequestSchema, "mode": "collect", "objective": "", "cost_profile": "zero_incremental_cost"},
		{"schema_version": contract.RequestSchema, "mode": "collect", "objective": "topic", "cost_profile": "paid"},
		{"schema_version": contract.RequestSchema, "mode": "refresh", "watch_id": "not-a-watch"},
		{"schema_version": contract.RequestSchema, "mode": "collect", "objective": "topic", "cost_profile": "zero_incremental_cost", "owner_id": "forged"},
		{"schema_version": contract.RequestSchema, "mode": "collect", "objective": "topic", "cost_profile": "zero_incremental_cost", "retrieval_targets": []any{"documents"}, "document_refs": []any{""}},
		{"schema_version": contract.RequestSchema, "mode": "collect", "objective": "topic", "cost_profile": "zero_incremental_cost", "retrieval_targets": []any{"documents"}, "document_refs": []any{}},
		{"schema_version": contract.RequestSchema, "mode": "collect", "objective": "topic", "cost_profile": "zero_incremental_cost", "retrieval_targets": []any{"documents"}, "document_refs": []any{strings.Repeat("x", 513)}},
		{"schema_version": contract.RequestSchema, "mode": "collect", "objective": "topic", "cost_profile": "zero_incremental_cost", "retrieval_targets": []any{"documents"}, "document_refs": makeResearchStringArray(21)},
		{"schema_version": contract.RequestSchema, "mode": "collect", "objective": "topic", "cost_profile": "zero_incremental_cost", "document_refs": []any{"canonical-id"}},
		{"schema_version": contract.RequestSchema, "mode": "search_only", "objective": "topic", "cost_profile": "zero_incremental_cost", "retrieval_targets": []any{"documents"}, "document_refs": []any{"canonical-id"}},
		{"schema_version": contract.RequestSchema, "mode": "refresh", "watch_id": "rw_valid-watch-id", "document_refs": []any{"canonical-id"}},
		{"schema_version": contract.RequestSchema, "mode": "collect", "objective": "topic", "cost_profile": "zero_incremental_cost", "repo_ref": "repo-core", "retrieval_targets": []any{"code"}},
		{"schema_version": contract.RequestSchema, "mode": "collect", "objective": "topic", "cost_profile": "zero_incremental_cost", "revision": "0123456789abcdef0123456789abcdef01234567", "retrieval_targets": []any{"code"}},
		{"schema_version": contract.RequestSchema, "mode": "collect", "objective": "topic", "cost_profile": "zero_incremental_cost", "repo_ref": "repo-core", "revision": "0123456789abcdef0123456789abcdef01234567", "retrieval_targets": []any{"documents"}},
		{"schema_version": contract.RequestSchema, "mode": "collect", "objective": "topic", "cost_profile": "zero_incremental_cost", "repo_ref": "/tmp/repo", "revision": "0123456789abcdef0123456789abcdef01234567", "retrieval_targets": []any{"code"}},
		{"schema_version": contract.RequestSchema, "mode": "collect", "objective": "topic", "cost_profile": "zero_incremental_cost", "repo_ref": "repo-core", "revision": "main", "retrieval_targets": []any{"code"}},
		{"schema_version": contract.RequestSchema, "mode": "refresh", "watch_id": "rw_valid-watch-id", "repo_ref": "repo-core", "revision": "0123456789abcdef0123456789abcdef01234567"},
		{"schema_version": contract.RequestSchema, "mode": "collect", "objective": "topic", "cost_profile": "zero_incremental_cost", "repo_path": "/tmp/repo", "retrieval_targets": []any{"code"}},
	}
	for i, instance := range invalid {
		if err := schema.Validate(map[string]any{"request": instance}); err == nil {
			t.Errorf("invalid public Research request %d was accepted: %#v", i, instance)
		}
	}
}

func makeResearchStringArray(length int) []any {
	values := make([]any, length)
	for index := range values {
		values[index] = "doc"
	}
	return values
}

func compileResearchRunSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	const resource = "https://rencrow.invalid/research-run-schema"
	if err := compiler.AddResource(resource, researchRunSchema()); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(resource)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func compileResearchWatchSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	const resource = "https://rencrow.invalid/research-watch-schema"
	if err := compiler.AddResource(resource, researchWatchSchema()); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(resource)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func compileResearchStatusSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	const resource = "https://rencrow.invalid/research-status-schema"
	if err := compiler.AddResource(resource, researchStatusSchema()); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(resource)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}
