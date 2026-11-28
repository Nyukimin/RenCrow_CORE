package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	coreresearch "github.com/Nyukimin/RenCrow_CORE/internal/application/research"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	taskstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
	wg "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

type researchTestFetcher struct{}

func (researchTestFetcher) FetchURL(_ context.Context, r wg.FetchRequest) (wg.FetchResponse, error) {
	text := "A public source body for the isolated integration test."
	return wg.FetchResponse{URL: r.URL, FinalURL: r.URL, Status: "ok", HTTPStatus: 200, ExtractedText: text, ExtractedChars: len([]rune(text)), ContentType: "text/plain"}, nil
}

type failingResearchResultRecorder struct {
	calls    int
	delegate *coreresearch.ResultRecorder
}

func (r *failingResearchResultRecorder) Record(context.Context, c.Envelope, c.Result) error {
	r.calls++
	return fmt.Errorf("injected Task projection failure")
}

func (r *failingResearchResultRecorder) Reconcile(ctx context.Context, env c.Envelope, result c.Result) (c.Result, error) {
	return r.delegate.Reconcile(ctx, env, result)
}

func researchOwnerFixture(t *testing.T) (*config.Config, *engine.Engine, *taskmanager.Manager) {
	t.Helper()
	root := t.TempDir()
	token := filepath.Join(root, "owner.token")
	if err := os.WriteFile(token, []byte(strings.Repeat("test", 16)), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := taskstore.NewJSONLStoreWithOptions(filepath.Join(root, "tasks"), taskstore.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(root, "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l1.Close() })
	artifacts, err := engine.NewFileStore(filepath.Join(root, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = artifacts.Close() })
	adapter := coreresearch.NewAdapters(nil, researchTestFetcher{}, l1).WithSourcePolicy(func(context.Context, c.Candidate) (bool, bool) { return true, true })
	e := &engine.Engine{Ports: engine.Ports{
		Store: artifacts, Acquisition: adapter, Owner: adapter,
		Capabilities: []c.Capability{{ProviderID: "direct", SourceKinds: []string{"web"}, FetchText: true, Enabled: true, Ready: true, FreeAllowed: true}},
	}}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: token, UserID: "ren"}}
	return cfg, e, taskmanager.New(store, taskmanager.DefaultParallelLimits())
}
func TestResearchOwnerUsesUserTaskAndExactStagingReadback(t *testing.T) {
	cfg, e, tasks := researchOwnerFixture(t)
	handler, err := newResearchOwnerHandler(cfg, e, tasks, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"operation": "run", "operation_key": "first-run-key", "request": c.Request{SchemaVersion: c.RequestSchema, Mode: "collect", Objective: "Collect the authorized source", SeedURLs: []string{"https://example.org/document"}}}
	data, _ := json.Marshal(input)
	req := httptest.NewRequest("POST", "http://127.0.0.1:18790/v1/research/owner", bytes.NewReader(data))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("test", 16))
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != 200 {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
	}
	var response researchAdmissionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.State != "completed" || response.Admission.TaskID == "" || response.Admission.InitialRunID == "" || response.Result == nil {
		t.Fatalf("invalid admission response: %+v", response)
	}
	result := *response.Result
	if result.Status != "succeeded" || result.Counts.RecordsSaved != 1 || result.Counts.AnalysisCalls != 0 || len(result.Receipts) != 1 || result.Receipts[0].Scope != "user" {
		t.Fatalf("false owner success: %+v", result)
	}
	record, err := e.Ports.Store.Load(context.Background(), result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Envelope.Actor != "ren" || record.Envelope.OwnerID != "ren" || record.Envelope.DataScope != "user" {
		t.Fatal("CLI impersonated an Agent or lost owner scope")
	}
	runs, err := tasks.ListRuns(context.Background(), domaintask.RunFilter{TaskID: core.TaskID(result.TaskID)})
	if err != nil || len(runs) != 1 || string(runs[0].RunID) != result.ExecutionRunID || runs[0].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("exact admitted invocation was not closed: runs=%+v err=%v result=%+v", runs, err, result)
	}
	e.PolicyRevision = "core.research.policy.v2"
	// An exact replay returns the existing receipt and result without creating
	// another Task or execution Run.
	replay := httptest.NewRequest("POST", "http://127.0.0.1:18790/v1/research/owner", bytes.NewReader(data))
	replay.RemoteAddr = "127.0.0.1:12345"
	replay.Header.Set("Authorization", "Bearer "+strings.Repeat("test", 16))
	replayW := httptest.NewRecorder()
	handler(replayW, replay)
	var replayResponse researchAdmissionResponse
	if replayW.Code != 200 || json.Unmarshal(replayW.Body.Bytes(), &replayResponse) != nil || replayResponse.State != "completed" || replayResponse.Admission.TaskID != response.Admission.TaskID || replayResponse.Admission.InitialRunID != response.Admission.InitialRunID {
		t.Fatalf("replay changed admission: status=%d response=%+v body=%s", replayW.Code, replayResponse, replayW.Body.String())
	}
	if replayResponse.Admission.PolicyRevision != response.Admission.PolicyRevision {
		t.Fatalf("replay changed the original policy pin: first=%q replay=%q", response.Admission.PolicyRevision, replayResponse.Admission.PolicyRevision)
	}
	runs, err = tasks.ListRuns(context.Background(), domaintask.RunFilter{TaskID: core.TaskID(result.TaskID)})
	if err != nil || len(runs) != 1 {
		t.Fatalf("replay issued another Run: %+v %v", runs, err)
	}
	changedInput := map[string]any{"operation": "run", "operation_key": "first-run-key", "request": c.Request{SchemaVersion: c.RequestSchema, Mode: "collect", Objective: "different request", SeedURLs: []string{"https://example.org/document"}}}
	changedData, _ := json.Marshal(changedInput)
	changed := httptest.NewRequest("POST", "http://127.0.0.1:18790/v1/research/owner", bytes.NewReader(changedData))
	changed.RemoteAddr = "127.0.0.1:12345"
	changed.Header.Set("Authorization", "Bearer "+strings.Repeat("test", 16))
	changedW := httptest.NewRecorder()
	handler(changedW, changed)
	if changedW.Code != 409 {
		t.Fatalf("same key with changed request was not rejected: %d %s", changedW.Code, changedW.Body.String())
	}
	lookupInput := map[string]any{"operation": "lookup", "operation_key": "first-run-key", "request": c.Request{SchemaVersion: c.RequestSchema, Mode: "collect", Objective: "Collect the authorized source", SeedURLs: []string{"https://example.org/document"}}}
	lookupData, _ := json.Marshal(lookupInput)
	lookup := httptest.NewRequest("POST", "http://127.0.0.1:18790/v1/research/owner", bytes.NewReader(lookupData))
	lookup.RemoteAddr = "127.0.0.1:12345"
	lookup.Header.Set("Authorization", "Bearer "+strings.Repeat("test", 16))
	lookupW := httptest.NewRecorder()
	handler(lookupW, lookup)
	var lookupResponse researchAdmissionResponse
	if lookupW.Code != 200 || json.Unmarshal(lookupW.Body.Bytes(), &lookupResponse) != nil || lookupResponse.State != "completed" || lookupResponse.Admission.TaskID != response.Admission.TaskID {
		t.Fatalf("readonly lookup failed: status=%d response=%+v body=%s", lookupW.Code, lookupResponse, lookupW.Body.String())
	}
	lookupInput["operation_key"] = "never-admitted-key"
	missingData, _ := json.Marshal(lookupInput)
	missing := httptest.NewRequest("POST", "http://127.0.0.1:18790/v1/research/owner", bytes.NewReader(missingData))
	missing.RemoteAddr = "127.0.0.1:12345"
	missing.Header.Set("Authorization", "Bearer "+strings.Repeat("test", 16))
	missingW := httptest.NewRecorder()
	handler(missingW, missing)
	var missingResponse researchAdmissionResponse
	if missingW.Code != 200 || json.Unmarshal(missingW.Body.Bytes(), &missingResponse) != nil || missingResponse.State != "not_found" {
		t.Fatalf("readonly missing lookup failed: status=%d response=%+v body=%s", missingW.Code, missingResponse, missingW.Body.String())
	}
}

func TestResearchOwnerNeverPromotesSourceSuccessWithoutTaskReceipt(t *testing.T) {
	cfg, e, tasks := researchOwnerFixture(t)
	storeCounter := &countingResearchEngineStore{Store: e.Ports.Store}
	e.Ports.Store = storeCounter
	recorder := &failingResearchResultRecorder{delegate: &coreresearch.ResultRecorder{Store: e.Ports.Store, Tasks: tasks}}
	handler, err := newResearchOwnerHandler(cfg, e, tasks, recorder, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := c.Request{SchemaVersion: c.RequestSchema, Mode: "collect", Objective: "authorized source", SeedURLs: []string{"https://example.org/document"}}
	post := func(input any) *httptest.ResponseRecorder {
		t.Helper()
		data, _ := json.Marshal(input)
		r := httptest.NewRequest("POST", "http://127.0.0.1:18790/v1/research/owner", bytes.NewReader(data))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("test", 16))
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	first := post(map[string]any{"operation": "run", "operation_key": "projection-failure-key", "request": request})
	var firstResponse researchAdmissionResponse
	if first.Code != 200 || json.Unmarshal(first.Body.Bytes(), &firstResponse) != nil || firstResponse.Result == nil || firstResponse.Result.Status == "succeeded" {
		t.Fatalf("source success escaped failed Task receipt: status=%d response=%+v body=%s", first.Code, firstResponse, first.Body.String())
	}
	if recorder.calls != 1 {
		t.Fatalf("initial recorder calls=%d, want 1", recorder.calls)
	}
	runID := firstResponse.Admission.InitialRunID
	beforeTask, err := tasks.Get(context.Background(), firstResponse.Admission.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRun, err := tasks.GetRun(context.Background(), firstResponse.Admission.InitialRunID)
	if err != nil {
		t.Fatal(err)
	}
	beforeTaskJSON, _ := json.Marshal(beforeTask)
	beforeRunJSON, _ := json.Marshal(beforeRun)
	beforeSaves := storeCounter.saves
	status := post(map[string]any{"operation": "status", "run_id": runID})
	var statusResult c.Result
	if status.Code != 200 || json.Unmarshal(status.Body.Bytes(), &statusResult) != nil || statusResult.Status == "succeeded" || !containsString(statusResult.Missing, "task_receipt") {
		t.Fatalf("status falsely promoted Engine result: %d %+v %s", status.Code, statusResult, status.Body.String())
	}
	resume := post(map[string]any{"operation": "resume", "run_id": runID})
	var resumed c.Result
	if resume.Code != 200 || json.Unmarshal(resume.Body.Bytes(), &resumed) != nil || resumed.Status == "succeeded" || !containsString(resumed.Missing, "task_receipt") {
		t.Fatalf("resume falsely promoted Engine result: %d %+v %s", resume.Code, resumed, resume.Body.String())
	}
	lookup := post(map[string]any{"operation": "lookup", "operation_key": "projection-failure-key", "request": request})
	var lookupResponse researchAdmissionResponse
	if lookup.Code != 200 || json.Unmarshal(lookup.Body.Bytes(), &lookupResponse) != nil || lookupResponse.State != "completed" || lookupResponse.Result == nil || lookupResponse.Result.Status == "succeeded" || !containsString(lookupResponse.Result.Missing, "task_receipt") {
		t.Fatalf("lookup falsely promoted Engine result: %d %+v %s", lookup.Code, lookupResponse, lookup.Body.String())
	}
	afterTask, err := tasks.Get(context.Background(), firstResponse.Admission.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	afterRun, err := tasks.GetRun(context.Background(), firstResponse.Admission.InitialRunID)
	if err != nil {
		t.Fatal(err)
	}
	afterTaskJSON, _ := json.Marshal(afterTask)
	afterRunJSON, _ := json.Marshal(afterRun)
	if recorder.calls != 1 || storeCounter.saves != beforeSaves || !bytes.Equal(beforeTaskJSON, afterTaskJSON) || !bytes.Equal(beforeRunJSON, afterRunJSON) {
		t.Fatalf("readonly status/resume/lookup mutated state: recorder=%d/%d engineSaves=%d/%d taskSame=%t runSame=%t", recorder.calls, 1, storeCounter.saves, beforeSaves, bytes.Equal(beforeTaskJSON, afterTaskJSON), bytes.Equal(beforeRunJSON, afterRunJSON))
	}
}

type countingResearchEngineStore struct {
	engine.Store
	saves int
}

func (s *countingResearchEngineStore) Save(ctx context.Context, record c.RunRecord) error {
	s.saves++
	return s.Store.Save(ctx, record)
}
func TestResearchOwnerRejectsTransportAndSchemaViolations(t *testing.T) {
	cfg, e, tasks := researchOwnerFixture(t)
	handler, err := newResearchOwnerHandler(cfg, e, tasks, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body, address, token string
		want                       int
	}{
		{"remote", `{"operation":"status","run_id":"x"}`, "192.0.2.1:42", strings.Repeat("test", 16), 403},
		{"credential", `{"operation":"status","run_id":"x"}`, "127.0.0.1:42", "wrong", 401},
		{"authority", `{"operation":"run","actor":"mio"}`, "127.0.0.1:42", strings.Repeat("test", 16), 400},
		{"trailing malformed", `{"operation":"status","run_id":"x"}{`, "127.0.0.1:42", strings.Repeat("test", 16), 400},
		{"mixed branch", `{"operation":"status","run_id":"x","request":{}}`, "127.0.0.1:42", strings.Repeat("test", 16), 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://127.0.0.1:18790/v1/research/owner", strings.NewReader(tc.body))
			r.RemoteAddr = tc.address
			r.Header.Set("Authorization", "Bearer "+tc.token)
			w := httptest.NewRecorder()
			handler(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d, want %d", w.Code, tc.want)
			}
		})
	}
}

func TestResearchWatchManagementIsReadonlyAndRefreshKeyPinsPolicy(t *testing.T) {
	cfg, e, tasks := researchOwnerFixture(t)
	e.PolicyRevision = "core.research.policy.v1"
	watchStore, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "watch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watchStore.Close() })
	ctx := context.Background()
	if _, err := watchStore.SaveSourceRegistryEntry(ctx, l1sqlite.L1SourceRegistryEntry{SourceID: "official:source", URL: "https://example.org/source", Kind: l1sqlite.L1SourceKindWebGather, Enabled: true, TrustScore: 1, FetchInterval: time.Minute, LicenseNote: "fixture retention authorization"}); err != nil {
		t.Fatal(err)
	}
	payload := `{"cadence":"1h","objective":"track source","source_kinds":["web"],"source_refs":["official:source"]}`
	watch, err := watchStore.RegisterResearchWatch(ctx, "user", "ren", payload, []string{"official:source"}, time.Hour, time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	policy := coreresearch.SourcePolicy(func(context.Context, c.Candidate) (bool, bool) { return true, true })
	e.Ports.Watch = coreresearch.NewWatchAdapter(watchStore, policy)
	handler, err := newResearchOwnerHandler(cfg, e, tasks, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	post := func(value any) *httptest.ResponseRecorder {
		t.Helper()
		data, _ := json.Marshal(value)
		r := httptest.NewRequest("POST", "http://127.0.0.1:18790/v1/research/owner", bytes.NewReader(data))
		r.RemoteAddr = "127.0.0.1:23456"
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("test", 16))
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	due := post(map[string]any{"operation": "refresh_due"})
	var listed researchDueResponse
	if due.Code != 200 || json.Unmarshal(due.Body.Bytes(), &listed) != nil || listed.PolicyRevision != e.PolicyRevision || len(listed.Items) != 1 || listed.Items[0].WatchID != watch.WatchID || listed.Items[0].OperationKey == "" {
		t.Fatalf("due response=%+v code=%d body=%s", listed, due.Code, due.Body.String())
	}
	if strings.Contains(due.Body.String(), "cursor") || strings.Contains(due.Body.String(), "lease") || strings.Contains(due.Body.String(), "payload_json") {
		t.Fatalf("due response exposed owner-private watch state: %s", due.Body.String())
	}
	storedAfterList, found, err := watchStore.GetResearchWatch(ctx, "user", "ren", watch.WatchID)
	if err != nil || !found || storedAfterList.LeaseToken != "" || !storedAfterList.LeaseUntil.IsZero() {
		t.Fatalf("read-only due listing claimed a lease: %+v found=%t err=%v", storedAfterList, found, err)
	}
	status := post(map[string]any{"operation": "watch_status", "watch_id": watch.WatchID})
	var state researchWatchManagementResponse
	if status.Code != 200 || json.Unmarshal(status.Body.Bytes(), &state) != nil || !state.Item.Enabled || state.Item.Receipt.CanonicalID != watch.WatchID {
		t.Fatalf("watch status=%+v code=%d body=%s", state, status.Code, status.Body.String())
	}
	refresh := c.Request{SchemaVersion: c.RequestSchema, Mode: "refresh", WatchID: watch.WatchID}
	// A due key issued under the old trusted Engine policy cannot admit a run
	// after that policy changes, even though the original opaque key is known.
	e.PolicyRevision = "core.research.policy.v2"
	stale := post(map[string]any{"operation": "run", "operation_key": listed.Items[0].OperationKey, "request": refresh})
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "refresh_due_or_policy_changed") {
		t.Fatalf("stale refresh key accepted: %d %s", stale.Code, stale.Body.String())
	}
	disabled := post(map[string]any{"operation": "watch_disable", "watch_id": watch.WatchID})
	if disabled.Code != 200 || json.Unmarshal(disabled.Body.Bytes(), &state) != nil || state.Item.Enabled {
		t.Fatalf("watch disable not verified: %d %+v %s", disabled.Code, state, disabled.Body.String())
	}
	afterDisable := post(map[string]any{"operation": "refresh_due"})
	if afterDisable.Code != 200 || !strings.Contains(afterDisable.Body.String(), `"items":[]`) {
		t.Fatalf("disabled watch remained due: %d %s", afterDisable.Code, afterDisable.Body.String())
	}
	badMixed := post(map[string]any{"operation": "watch_status", "watch_id": watch.WatchID, "request": refresh})
	if badMixed.Code != http.StatusBadRequest {
		t.Fatalf("mixed watch operation accepted: %d %s", badMixed.Code, badMixed.Body.String())
	}
}

func TestResearchRefreshOperationKeyPinsExactDueAndPolicy(t *testing.T) {
	due := time.Date(2026, time.November, 28, 4, 0, 0, 0, time.UTC)
	key := researchRefreshOperationKey("rw_watch0001", due, "core.policy.v1")
	if key == "" || key != researchRefreshOperationKey("rw_watch0001", due, "core.policy.v1") {
		t.Fatal("same due and policy did not yield a stable owner operation key")
	}
	if key == researchRefreshOperationKey("rw_watch0002", due, "core.policy.v1") || key == researchRefreshOperationKey("rw_watch0001", due.Add(time.Nanosecond), "core.policy.v1") || key == researchRefreshOperationKey("rw_watch0001", due, "core.policy.v2") {
		t.Fatal("operation key was not bound to watch, due time, and policy")
	}
}

func TestResearchRuntimeDisabledAndExplicitRoot(t *testing.T) {
	cfg := &config.Config{}
	e, store, err := buildResearchEngine(cfg, nil, nil, nil)
	if err != nil || e != nil || store != nil {
		t.Fatal("disabled Research created an execution surface")
	}
	cfg.Research.Enabled = true
	cfg.Research.ArtifactRoot = "relative"
	if _, _, err := buildResearchEngine(cfg, nil, nil, nil); err == nil {
		t.Fatal("implicit artifact root accepted")
	}
}

func TestResearchOwnerSeedResumeUsesSameResearchRunWithNewTaskInvocation(t *testing.T) {
	cfg, e, tasks := researchOwnerFixture(t)
	e.Ports.Capabilities = append(e.Ports.Capabilities, c.Capability{ProviderID: "google_browser_manual", SourceKinds: []string{"web"}, Enabled: true, Ready: true, FreeAllowed: true, Search: true, RequiresInput: true})
	handler, err := newResearchOwnerHandler(cfg, e, tasks, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(input any) c.Result {
		t.Helper()
		data, _ := json.Marshal(input)
		r := httptest.NewRequest("POST", "http://127.0.0.1/v1/research/owner", bytes.NewReader(data))
		r.RemoteAddr = "127.0.0.1:10000"
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("test", 16))
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != 200 {
			t.Fatalf("http %d %s", w.Code, w.Body.String())
		}
		var operation struct {
			Operation string `json:"operation"`
		}
		_ = json.Unmarshal(data, &operation)
		if operation.Operation == "run" {
			var response researchAdmissionResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Result == nil || response.State != "completed" {
				t.Fatalf("invalid run admission: %+v err=%v body=%s", response, err, w.Body.String())
			}
			return *response.Result
		}
		var result c.Result
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	initial := call(map[string]any{"operation": "run", "operation_key": "manual-seed-run", "request": c.Request{SchemaVersion: c.RequestSchema, Mode: "collect", Objective: "Collect the manually selected source"}})
	if initial.Status != "waiting_input" {
		t.Fatalf("%+v", initial)
	}
	call(map[string]any{"operation": "seed", "run_id": initial.RunID, "url": "https://example.org/document", "origin": "google_browser_manual"})
	resumed := call(map[string]any{"operation": "resume", "run_id": initial.RunID})
	if resumed.Status != "succeeded" || resumed.RunID != initial.RunID || resumed.ExecutionRunID == initial.ExecutionRunID || resumed.TaskID != initial.TaskID || resumed.Counts.RecordsSaved != 1 {
		t.Fatalf("invalid continuation: initial=%+v resumed=%+v", initial, resumed)
	}
	runs, err := tasks.ListRuns(context.Background(), domaintask.RunFilter{TaskID: core.TaskID(initial.TaskID)})
	if err != nil || len(runs) != 2 || string(runs[1].RunID) != resumed.ExecutionRunID || runs[1].Status != domaintask.RunStatusSucceeded || runs[0].Status != domaintask.RunStatusWaiting {
		t.Fatalf("continuation closed a different invocation: runs=%+v err=%v resumed=%+v", runs, err, resumed)
	}
	task, err := tasks.Get(context.Background(), core.TaskID(initial.TaskID))
	if err != nil || task.Status != domaintask.StatusSucceeded {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	again := call(map[string]any{"operation": "resume", "run_id": initial.RunID})
	if again.ExecutionRunID != resumed.ExecutionRunID || again.Counts.RecordsSaved != 1 {
		t.Fatal("terminal resume issued another invocation")
	}
}

func privateOriginWaitingResearchFixture(t *testing.T) (*config.Config, *engine.Engine, *taskmanager.Manager, c.Result) {
	return privateOriginWaitingResearchFixtureFor(t, "ren")
}

func privateOriginWaitingResearchFixtureFor(t *testing.T, dataOwner string) (*config.Config, *engine.Engine, *taskmanager.Manager, c.Result) {
	t.Helper()
	cfg, e, tasks := researchOwnerFixture(t)
	e.PolicyRevision = "fixture.research.policy.v1"
	e.Ports.Capabilities = append(e.Ports.Capabilities, c.Capability{ProviderID: "google_browser_manual", SourceKinds: []string{"web"}, Enabled: true, Ready: true, FreeAllowed: true, Search: true, RequiresInput: true})
	ctx := context.Background()
	taskID := core.NewTaskID()
	draft := domaintask.Task{TaskID: taskID, Title: "Mio private Research", Route: domaintask.RouteCHAT, OwnerID: "mio", Assignee: "mio"}
	draft.OriginSessionID = core.NewSessionID()
	address, err := conversation.NewChannelAddress("viewer", "cmd:ren")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := conversation.NewSessionBindingWithPublicationIncarnation(
		draft.OriginSessionID, address, conversation.SessionAudiencePrivate, dataOwner, 1, "0123456789abcdef0123456789abcdef",
	)
	if err != nil {
		t.Fatal(err)
	}
	task, run, err := tasks.CreateAndStartRun(conversation.WithSessionBinding(ctx, binding), draft, domaintask.SharedRoleContext{TaskID: taskID, UserIntent: "private Research"})
	if err != nil {
		t.Fatal(err)
	}
	env := c.Envelope{TaskID: string(task.TaskID), RunID: string(run.RunID), TraceID: string(core.NewTraceID()), RequestID: string(core.NewRequestID()), Actor: "mio", OwnerID: dataOwner, DataScope: "user", PolicyRevision: e.PolicyRevision}
	result, err := e.Run(ctx, env, c.Request{SchemaVersion: c.RequestSchema, Mode: "collect", Objective: "Collect a manually selected source"})
	if err != nil || result.Status != "waiting_input" {
		t.Fatalf("initial Engine invocation = %+v, %v", result, err)
	}
	recorder := &coreresearch.ResultRecorder{Store: e.Ports.Store, Tasks: tasks}
	if err := recorder.Record(ctx, env, result); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.CompleteRun(ctx, task.TaskID, run.RunID, "mio", domaintask.StatusWaiting, "manual seed required", "manual seed required"); err != nil {
		t.Fatal(err)
	}
	return cfg, e, tasks, result
}

func researchOwnerPost(cfg *config.Config, e *engine.Engine, tasks *taskmanager.Manager, input any) *httptest.ResponseRecorder {
	data, _ := json.Marshal(input)
	handler, err := newResearchOwnerHandler(cfg, e, tasks, nil, nil)
	if err != nil {
		panic(err)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1/v1/research/owner", bytes.NewReader(data))
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("test", 16))
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func TestResearchOwnerResumesPrivateOriginTaskAsPersistedMioActor(t *testing.T) {
	cfg, e, tasks, initial := privateOriginWaitingResearchFixture(t)
	if _, err := authorizeResearchContinuation(context.Background(), e, tasks, "ren", e.PolicyRevision, initial); err != nil {
		t.Fatalf("unexpected continuation authorization failure: %v", err)
	}
	seed := researchOwnerPost(cfg, e, tasks, map[string]any{"operation": "seed", "run_id": initial.RunID, "url": "https://example.org/document", "origin": "google_browser_manual"})
	if seed.Code != http.StatusOK {
		t.Fatalf("seed returned %d: %s", seed.Code, seed.Body.String())
	}
	resume := researchOwnerPost(cfg, e, tasks, map[string]any{"operation": "resume", "run_id": initial.RunID})
	var result c.Result
	if resume.Code != http.StatusOK || json.Unmarshal(resume.Body.Bytes(), &result) != nil || result.Status != "succeeded" || result.ExecutionRunID == initial.ExecutionRunID {
		t.Fatalf("private-origin continuation failed: status=%d result=%+v body=%s", resume.Code, result, resume.Body.String())
	}
	record, err := e.Ports.Store.Load(context.Background(), initial.RunID)
	if err != nil || record.Invocation == nil || record.Invocation.Actor != "mio" || record.Invocation.OwnerID != "ren" || record.Invocation.DataScope != "user" || record.Invocation.PolicyRevision != e.PolicyRevision {
		t.Fatalf("continuation changed authority instead of carrying persisted Actor: invocation=%+v err=%v", record.Invocation, err)
	}
	task, err := tasks.Get(context.Background(), core.TaskID(initial.TaskID))
	if err != nil || task.OwnerID != "mio" || task.Assignee != "mio" || task.ResearchResult == nil || task.ResearchResult.OwnerID != "ren" || task.ResearchResult.ActorID != "mio" {
		t.Fatalf("Research continuation rewrote Task/data owner: task=%+v err=%v", task, err)
	}
	runs, err := tasks.ListRuns(context.Background(), domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil || len(runs) != 2 || runs[1].Assignee != "mio" || runs[1].RunID != core.RunID(result.ExecutionRunID) || runs[1].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("continuation Run actor/state mismatch: runs=%+v err=%v", runs, err)
	}
}

func TestResearchOwnerContinuationRejectsStalePolicyAndChangedActorBeforeWrites(t *testing.T) {
	t.Run("stale policy", func(t *testing.T) {
		cfg, e, tasks, initial := privateOriginWaitingResearchFixture(t)
		counter := &countingResearchEngineStore{Store: e.Ports.Store}
		e.Ports.Store = counter
		beforeRuns, err := tasks.ListRuns(context.Background(), domaintask.RunFilter{TaskID: core.TaskID(initial.TaskID)})
		if err != nil {
			t.Fatal(err)
		}
		e.PolicyRevision = "fixture.research.policy.v2"
		response := researchOwnerPost(cfg, e, tasks, map[string]any{"operation": "seed", "run_id": initial.RunID, "url": "https://example.org/document", "origin": "google_browser_manual"})
		if response.Code != http.StatusConflict {
			t.Fatalf("stale policy continuation returned %d: %s", response.Code, response.Body.String())
		}
		record, err := counter.Load(context.Background(), initial.RunID)
		afterRuns, runsErr := tasks.ListRuns(context.Background(), domaintask.RunFilter{TaskID: core.TaskID(initial.TaskID)})
		if err != nil || runsErr != nil || len(record.AdditionalSeeds) != 0 || counter.saves != 0 || len(beforeRuns) != len(afterRuns) {
			t.Fatalf("stale policy changed source or Task state: record=%+v saves=%d runs=%d/%d err=%v/%v", record, counter.saves, len(beforeRuns), len(afterRuns), err, runsErr)
		}
	})
	t.Run("changed Task actor", func(t *testing.T) {
		cfg, e, tasks, initial := privateOriginWaitingResearchFixture(t)
		counter := &countingResearchEngineStore{Store: e.Ports.Store}
		e.Ports.Store = counter
		if _, err := tasks.RecordAssignment(context.Background(), core.TaskID(initial.TaskID), "shiro", core.NewEventID()); err != nil {
			t.Fatal(err)
		}
		beforeRuns, err := tasks.ListRuns(context.Background(), domaintask.RunFilter{TaskID: core.TaskID(initial.TaskID)})
		if err != nil {
			t.Fatal(err)
		}
		response := researchOwnerPost(cfg, e, tasks, map[string]any{"operation": "resume", "run_id": initial.RunID})
		if response.Code != http.StatusConflict {
			t.Fatalf("changed actor continuation returned %d: %s", response.Code, response.Body.String())
		}
		afterRuns, runsErr := tasks.ListRuns(context.Background(), domaintask.RunFilter{TaskID: core.TaskID(initial.TaskID)})
		record, loadErr := counter.Load(context.Background(), initial.RunID)
		if runsErr != nil || loadErr != nil || counter.saves != 0 || len(beforeRuns) != len(afterRuns) || record.Invocation != nil {
			t.Fatalf("changed Actor produced a continuation side effect: saves=%d runs=%d/%d invocation=%+v errs=%v/%v", counter.saves, len(beforeRuns), len(afterRuns), record.Invocation, runsErr, loadErr)
		}
	})
}

func TestResearchOwnerContinuationRejectsOtherPrivateOriginOwner(t *testing.T) {
	cfg, e, tasks, initial := privateOriginWaitingResearchFixtureFor(t, "bob")
	counter := &countingResearchEngineStore{Store: e.Ports.Store}
	e.Ports.Store = counter
	beforeRuns, err := tasks.ListRuns(context.Background(), domaintask.RunFilter{TaskID: core.TaskID(initial.TaskID)})
	if err != nil {
		t.Fatal(err)
	}
	response := researchOwnerPost(cfg, e, tasks, map[string]any{"operation": "resume", "run_id": initial.RunID})
	if response.Code != http.StatusNotFound {
		t.Fatalf("different private-origin owner returned %d: %s", response.Code, response.Body.String())
	}
	record, err := counter.Load(context.Background(), initial.RunID)
	afterRuns, runsErr := tasks.ListRuns(context.Background(), domaintask.RunFilter{TaskID: core.TaskID(initial.TaskID)})
	if err != nil || runsErr != nil || counter.saves != 0 || len(beforeRuns) != len(afterRuns) || len(record.AdditionalSeeds) != 0 {
		t.Fatalf("owner mismatch caused continuation side effects: saves=%d seeds=%d runs=%d/%d errs=%v/%v", counter.saves, len(record.AdditionalSeeds), len(beforeRuns), len(afterRuns), err, runsErr)
	}
}

type failResearchStoreSaves struct {
	engine.Store
	failRemaining int
	writes        int
}

func (s *failResearchStoreSaves) Save(ctx context.Context, record c.RunRecord) error {
	s.writes++
	if s.failRemaining > 0 {
		s.failRemaining--
		return fmt.Errorf("injected Research journal write failure")
	}
	return s.Store.Save(ctx, record)
}

func TestResearchOwnerResumeWriteFailureDoesNotIssueBlindReplacementRun(t *testing.T) {
	cfg, e, tasks, initial := privateOriginWaitingResearchFixture(t)
	store := &failResearchStoreSaves{Store: e.Ports.Store}
	e.Ports.Store = store
	seed := researchOwnerPost(cfg, e, tasks, map[string]any{"operation": "seed", "run_id": initial.RunID, "url": "https://example.org/document", "origin": "google_browser_manual"})
	if seed.Code != http.StatusOK {
		t.Fatalf("seed returned %d: %s", seed.Code, seed.Body.String())
	}
	store.failRemaining = 2 // fail both the pre-acquisition write and bounded completion write
	first := researchOwnerPost(cfg, e, tasks, map[string]any{"operation": "resume", "run_id": initial.RunID})
	if first.Code != http.StatusOK {
		t.Fatalf("first resume returned %d: %s", first.Code, first.Body.String())
	}
	failedTask, err := tasks.Get(context.Background(), core.TaskID(initial.TaskID))
	if err != nil || failedTask.Status != domaintask.StatusFailed || failedTask.ResearchResult == nil || failedTask.ResearchResult.ExecutionRunID != core.RunID(initial.ExecutionRunID) {
		t.Fatalf("failed resume Task did not retain its prior receipt: task=%+v err=%v", failedTask, err)
	}
	beforeRuns, err := tasks.ListRuns(context.Background(), domaintask.RunFilter{TaskID: failedTask.TaskID})
	if err != nil || len(beforeRuns) != 2 || beforeRuns[1].Status != domaintask.RunStatusFailed {
		t.Fatalf("failed resume Run state=%+v err=%v", beforeRuns, err)
	}
	beforeWrites := store.writes
	second := researchOwnerPost(cfg, e, tasks, map[string]any{"operation": "resume", "run_id": initial.RunID})
	if second.Code != http.StatusConflict {
		t.Fatalf("unreconciled continuation was retried: status=%d body=%s", second.Code, second.Body.String())
	}
	afterRuns, err := tasks.ListRuns(context.Background(), domaintask.RunFilter{TaskID: failedTask.TaskID})
	if err != nil || len(afterRuns) != len(beforeRuns) || store.writes != beforeWrites {
		t.Fatalf("unsafe recovery wrote or issued another Run: runs=%d/%d store writes=%d/%d err=%v", len(beforeRuns), len(afterRuns), beforeWrites, store.writes, err)
	}
	record, err := store.Load(context.Background(), initial.RunID)
	if err != nil || record.Invocation != nil || record.Result.ExecutionRunID != initial.ExecutionRunID {
		t.Fatalf("failed journal write was misreported as committed: invocation=%+v result=%+v err=%v", record.Invocation, record.Result, err)
	}
}
