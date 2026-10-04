package storagehost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	browsertraceapp "github.com/Nyukimin/RenCrow_CORE/internal/application/browsertrace"
	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	persistbrowsertrace "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/browsertrace"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const browserTraceToAPIGroupTestToken = "browser-trace-to-api-group-test-token"

func TestBrowserTraceToAPIGroupRegistersExactOperationsAndRoundTripsTypedViewerState(t *testing.T) {
	fixture := openBrowserTraceToAPIHost(t)
	defer fixture.close(t)
	ctx := context.Background()
	contract, err := fixture.rpc.Contract(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		browserTraceToAPIListTraceRuns: false, browserTraceToAPIListCandidates: false,
		browserTraceToAPIListSchemas: false, browserTraceToAPIListValidations: false,
		browserTraceToAPIListCoverage: false, browserTraceToAPIListArtifacts: false,
		browserTraceToAPISaveTraceRun: true, browserTraceToAPISaveCandidate: true,
		browserTraceToAPISaveSchema: true, browserTraceToAPISaveValidation: true,
		browserTraceToAPISaveCoverage: true, browserTraceToAPISaveArtifact: true,
		browserTraceToAPIFindCandidateByID: false, browserTraceToAPIFindValidationByID: false,
		browserTraceToAPICreateArtifactWithIntent: true, browserTraceToAPISupersedeArtifactWithIntent: true,
		browserTraceToAPIListPublicationIntents: false, browserTraceToAPIListSupersessionFacts: false,
	}
	got := map[string]bool{}
	for _, operation := range contract.Operations {
		if operation.Group == GroupBrowserTraceToAPI {
			got[operation.Op] = operation.Mutating
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("browser trace operations=%v want=%v", got, want)
	}
	if len(got) != 18 {
		t.Fatalf("browser trace operation count=%d want exactly 18", len(got))
	}

	stamp := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	taskID := modulecore.TaskID("tsk_00000000-0000-5000-8000-000000000001")
	runID := modulecore.RunID("run_00000000-0000-5000-8000-000000000002")
	workstreamID := modulecore.NewWorkstreamID()
	traceRun := domaintrace.TraceRun{TaskID: taskID, RunID: runID, ActorID: "mio", TracePath: "captures/session.trace", CreatedAt: stamp}
	candidate := domaintrace.APICandidate{
		CandidateID: "candidate-review-1", TaskID: taskID, RunID: runID, ActorID: "mio", Method: "GET",
		ObservedURL: "https://example.test/api/items", ContainsPersonalData: "no", RiskLevel: "low", Status: "candidate", CreatedAt: stamp,
	}
	schema := domaintrace.APICandidateSchema{SchemaID: "schema-review-1", CandidateID: candidate.CandidateID, SchemaType: "response", SchemaJSON: `{"type":"object"}`, SampleCount: 1, CreatedAt: stamp}
	validation := domaintrace.APICandidateValidationResult{
		ValidationID: "validation-review-1", CandidateID: candidate.CandidateID, TaskID: taskID, RunID: runID, ActorID: "mio",
		Passed: true, Status: "validated", Reviewer: "mio", CreatedAt: stamp,
	}
	coverage := domaintrace.APICoverageReport{
		ArtifactID: modulecore.NewArtifactID(), Kind: modulecore.ArtifactKindReport, TaskID: taskID, RunID: runID, ActorID: "mio",
		ObservedFlows: []string{"list-items"}, CreatedAt: stamp,
	}
	coverage.ContentHash = domaintrace.ComputeAPICoverageReportContentHash(coverage)
	plainArtifact := browserTraceToAPIArtifact(t, taskID, runID, workstreamID, "Plain artifact")
	createdArtifact := browserTraceToAPIArtifact(t, taskID, runID, workstreamID, "Created artifact")
	intent, err := browsertraceapp.NewAPIArtifactCreationIntent(createdArtifact, modulecore.NewTraceID(), stamp.Add(time.Second))
	if err != nil {
		t.Fatalf("NewAPIArtifactCreationIntent() = %v", err)
	}

	for _, save := range []struct {
		name string
		fn   func() error
	}{
		{"trace run", func() error { return fixture.store.SaveTraceRun(ctx, traceRun) }},
		{"candidate", func() error { return fixture.store.SaveAPICandidate(ctx, candidate) }},
		{"candidate schema", func() error { return fixture.store.SaveAPICandidateSchema(ctx, schema) }},
		{"candidate validation", func() error { return fixture.store.SaveAPICandidateValidationResult(ctx, validation) }},
		{"coverage report", func() error { return fixture.store.SaveAPICoverageReport(ctx, coverage) }},
		{"plain artifact", func() error { return fixture.store.SaveAPIArtifact(ctx, plainArtifact) }},
	} {
		if err := save.fn(); err != nil {
			t.Fatalf("save %s over storagehost: %v", save.name, err)
		}
	}
	if err := fixture.store.CreateAPIArtifactWithPublicationIntent(ctx, createdArtifact, intent); err != nil {
		t.Fatalf("CreateAPIArtifactWithPublicationIntent over storagehost: %v", err)
	}

	checkList := []struct {
		name string
		fn   func() error
	}{
		{"trace runs", func() error {
			items, err := fixture.store.ListTraceRuns(ctx, 1)
			if err == nil && (len(items) != 1 || items[0] != traceRun) {
				return errors.New("trace run round trip mismatch")
			}
			return err
		}},
		{"candidates", func() error {
			items, err := fixture.store.ListAPICandidates(ctx, 1)
			if err == nil && (len(items) != 1 || items[0].CandidateID != candidate.CandidateID) {
				return errors.New("candidate round trip mismatch")
			}
			return err
		}},
		{"schemas", func() error {
			items, err := fixture.store.ListAPICandidateSchemas(ctx, 1)
			if err == nil && (len(items) != 1 || items[0].SchemaID != schema.SchemaID) {
				return errors.New("schema round trip mismatch")
			}
			return err
		}},
		{"validations", func() error {
			items, err := fixture.store.ListAPICandidateValidationResults(ctx, 1)
			if err == nil && (len(items) != 1 || items[0].ValidationID != validation.ValidationID || items[0].TaskID != candidate.TaskID || items[0].RunID != candidate.RunID || items[0].ActorID != candidate.ActorID) {
				return errors.New("validation identity round trip mismatch")
			}
			return err
		}},
		{"coverage", func() error {
			items, err := fixture.store.ListAPICoverageReports(ctx, 1)
			if err == nil && (len(items) != 1 || items[0].ArtifactID != coverage.ArtifactID) {
				return errors.New("coverage round trip mismatch")
			}
			return err
		}},
		{"artifacts", func() error {
			items, err := fixture.store.ListAPIArtifacts(ctx, 10)
			if err == nil && len(items) != 2 {
				return errors.New("artifact list did not return both rows")
			}
			return err
		}},
	}
	for _, check := range checkList {
		if err := check.fn(); err != nil {
			t.Fatalf("list %s over storagehost: %v", check.name, err)
		}
	}
	gotCandidate, found, err := fixture.store.FindAPICandidateByID(ctx, candidate.CandidateID)
	if err != nil || !found || gotCandidate.CandidateID != candidate.CandidateID {
		t.Fatalf("FindAPICandidateByID()=%+v found=%v err=%v", gotCandidate, found, err)
	}
	gotValidation, found, err := fixture.store.FindAPICandidateValidationResultByID(ctx, validation.ValidationID)
	if err != nil || !found || gotValidation.TaskID != candidate.TaskID || gotValidation.RunID != candidate.RunID || gotValidation.ActorID != candidate.ActorID {
		t.Fatalf("FindAPICandidateValidationResultByID()=%+v found=%v err=%v", gotValidation, found, err)
	}
	if err := fixture.store.SaveAPICandidateValidationResult(ctx, domaintrace.APICandidateValidationResult{
		ValidationID: "validation-wrong-scope", CandidateID: candidate.CandidateID,
		TaskID: modulecore.NewTaskID(), RunID: runID, ActorID: "mio", Passed: true, Status: "validated", CreatedAt: stamp,
	}); err == nil {
		t.Fatal("validation save with a different task identity was accepted")
	}
	if _, found, err := fixture.store.FindAPICandidateValidationResultByID(ctx, "validation-wrong-scope"); err != nil || found {
		t.Fatalf("mismatched validation persisted: found=%v err=%v", found, err)
	}

	intents, err := fixture.store.ListAPIArtifactPublicationIntents(ctx, "", 1)
	if err != nil || len(intents) != 1 || intents[0].EventID != intent.EventID {
		t.Fatalf("ListAPIArtifactPublicationIntents()=%+v err=%v", intents, err)
	}
	moreIntents, err := fixture.store.ListAPIArtifactPublicationIntents(ctx, intents[0].ArtifactID, 1)
	if err != nil || len(moreIntents) != 0 {
		t.Fatalf("publication intent page after cursor=%+v err=%v; want empty", moreIntents, err)
	}

	predecessor := browserTraceToAPIArtifact(t, taskID, runID, workstreamID, "Predecessor")
	successor := browserTraceToAPIArtifact(t, taskID, runID, workstreamID, "Successor")
	if err := fixture.store.SaveAPIArtifact(ctx, predecessor); err != nil {
		t.Fatalf("save supersession predecessor: %v", err)
	}
	if err := fixture.store.SaveAPIArtifact(ctx, successor); err != nil {
		t.Fatalf("save supersession successor: %v", err)
	}
	fact, err := browsertraceapp.NewAPIArtifactSupersessionFact(predecessor, successor, modulecore.NewTraceID(), stamp.Add(2*time.Second))
	if err != nil {
		t.Fatalf("NewAPIArtifactSupersessionFact() = %v", err)
	}
	if err := fixture.store.SupersedeAPIArtifactWithPublicationIntent(ctx, predecessor.ArtifactID, successor.ArtifactID, fact); err != nil {
		t.Fatalf("SupersedeAPIArtifactWithPublicationIntent over storagehost: %v", err)
	}
	facts, err := fixture.store.ListAPIArtifactSupersessionFacts(ctx, "", 1)
	if err != nil || len(facts) != 1 || facts[0].EventID != fact.EventID {
		t.Fatalf("ListAPIArtifactSupersessionFacts()=%+v err=%v", facts, err)
	}
	moreFacts, err := fixture.store.ListAPIArtifactSupersessionFacts(ctx, facts[0].ArtifactID, 1)
	if err != nil || len(moreFacts) != 0 {
		t.Fatalf("supersession fact page after cursor=%+v err=%v; want empty", moreFacts, err)
	}

	if err := fixture.rpc.Call(ctx, GroupBrowserTraceToAPI, browserTraceToAPIListTraceRuns, map[string]any{"limit": 1, "query": "SELECT *"}, nil); errorCode(err) != ErrorCodeSchemaRejected {
		t.Fatalf("unknown-field list request error=%v, want schema rejection", err)
	}
	if _, err := fixture.store.ListTraceRuns(ctx, browserTraceToAPIMaxList+1); errorCode(err) != ErrorCodeSchemaRejected {
		t.Fatalf("unbounded list request error=%v, want schema rejection", err)
	}
}

func TestBrowserTraceToAPIPayloadRejectsTrailingAndUnknownFields(t *testing.T) {
	for _, payload := range []string{`{"limit":1,"query":"SELECT *"}`, `{"limit":1} {}`} {
		var decoded browserTraceToAPIListPayload
		if err := decodeBrowserTraceToAPIPayload([]byte(payload), &decoded); err == nil {
			t.Errorf("decodeBrowserTraceToAPIPayload(%q) succeeded, want rejection", payload)
		}
	}
}

func TestBrowserTraceToAPIListRejectsOwnerResponseBeyondEffectiveBound(t *testing.T) {
	items := make([]domaintrace.TraceRun, browserTraceToAPIDefaultList+1)
	for index := range items {
		items[index] = domaintrace.TraceRun{
			TaskID:  modulecore.TaskID("tsk_00000000-0000-5000-8000-000000000001"),
			RunID:   modulecore.RunID("run_00000000-0000-5000-8000-000000000002"),
			ActorID: "mio", TracePath: "bounded.trace", CreatedAt: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
		}
	}
	operation := browserTraceToAPIList(func(context.Context, int) ([]domaintrace.TraceRun, error) { return items, nil }, domaintrace.ValidateTraceRun)
	for _, requested := range []int{0, 1} {
		raw, err := json.Marshal(browserTraceToAPIListPayload{Limit: requested})
		if err != nil {
			t.Fatal(err)
		}
		_, err = operation(context.Background(), raw)
		var wireErr *Error
		if !errors.As(err, &wireErr) || wireErr.Code != ErrorCodeStoreUnavailable {
			t.Errorf("list response with requested limit %d error=%v, want bounded owner-response rejection", requested, err)
		}
	}
}

type browserTraceToAPIHostFixture struct {
	store   *BrowserTraceToAPIClient
	owner   *persistbrowsertrace.SQLiteStore
	handler *Handler
	server  *httptest.Server
	rpc     *Client
}

func openBrowserTraceToAPIHost(t *testing.T) *browserTraceToAPIHostFixture {
	t.Helper()
	root := t.TempDir()
	owner, err := persistbrowsertrace.NewSQLiteStore(filepath.Join(root, "browser-trace.sqlite"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() = %v", err)
	}
	handler, err := NewHandler(HandlerConfig{Token: browserTraceToAPIGroupTestToken, JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		_ = owner.Close()
		t.Fatalf("NewHandler() = %v", err)
	}
	if err := RegisterBrowserTraceToAPIGroup(handler, owner); err != nil {
		_ = owner.Close()
		_ = handler.Close()
		t.Fatalf("RegisterBrowserTraceToAPIGroup() = %v", err)
	}
	server := httptest.NewServer(handler)
	rpc, err := NewClient(ClientConfig{Endpoint: server.URL, Token: browserTraceToAPIGroupTestToken, HTTPClient: server.Client()})
	if err != nil {
		server.Close()
		_ = owner.Close()
		_ = handler.Close()
		t.Fatalf("NewClient() = %v", err)
	}
	if err := rpc.Handshake(context.Background()); err != nil {
		server.Close()
		_ = owner.Close()
		_ = handler.Close()
		t.Fatalf("Handshake() = %v", err)
	}
	return &browserTraceToAPIHostFixture{store: NewBrowserTraceToAPIClient(rpc), owner: owner, handler: handler, server: server, rpc: rpc}
}

func (fixture *browserTraceToAPIHostFixture) close(t *testing.T) {
	t.Helper()
	fixture.server.Close()
	if err := fixture.handler.Close(); err != nil {
		t.Errorf("close handler: %v", err)
	}
	if err := fixture.owner.Close(); err != nil {
		t.Errorf("close owner: %v", err)
	}
}

func browserTraceToAPIArtifact(t *testing.T, taskID modulecore.TaskID, runID modulecore.RunID, workstreamID modulecore.WorkstreamID, title string) domaintrace.APIArtifact {
	t.Helper()
	content := "openapi: 3.1.0\ninfo:\n  title: " + title + "\n"
	item := domaintrace.APIArtifact{
		ArtifactID: modulecore.NewArtifactID(), Kind: modulecore.ArtifactKindSpecification, TaskID: taskID, RunID: runID,
		ActorID: "mio", WorkstreamID: string(workstreamID), Type: domaintrace.APIArtifactTypeObservedOpenAPI,
		Title: title, Status: "generated", Content: content, ContentHash: modulecore.ContentHashOf([]byte(content)),
		CreatedAt: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
	}
	if err := domaintrace.ValidateAPIArtifact(item); err != nil {
		t.Fatalf("browser trace artifact fixture is invalid: %v", err)
	}
	return item
}
