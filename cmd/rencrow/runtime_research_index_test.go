package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	coreresearch "github.com/Nyukimin/RenCrow_CORE/internal/application/research"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	taskstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

type researchIndexBuilderSpy struct {
	calls    int
	result   coreresearch.ResearchDocumentIndexResult
	err      error
	envelope c.Envelope
	request  coreresearch.ResearchDocumentIndexRequest
	onBuild  func()
}

func (builder *researchIndexBuilderSpy) Build(_ context.Context, envelope c.Envelope, request coreresearch.ResearchDocumentIndexRequest) (coreresearch.ResearchDocumentIndexResult, error) {
	builder.calls++
	builder.envelope = envelope
	builder.request = request
	if builder.onBuild != nil {
		builder.onBuild()
	}
	return builder.result, builder.err
}

type researchIndexTaskStoreFault struct {
	*taskstore.JSONLStore
	failTerminalWrites            bool
	armReadbackAfterTerminalWrite bool
	failNextReadback              bool
	terminalRunWrites             int
	readbackFailures              int
}

func (store *researchIndexTaskStoreFault) TaskTransaction(ctx context.Context, taskID core.TaskID, callback func(domaintask.Store) error) error {
	return store.JSONLStore.TaskTransaction(ctx, taskID, func(transaction domaintask.Store) error {
		return callback(researchIndexTaskTransactionFault{Store: transaction, owner: store})
	})
}

func (store *researchIndexTaskStoreFault) GetRun(ctx context.Context, runID core.RunID) (domaintask.Run, error) {
	if store.failNextReadback {
		store.readbackFailures++
		store.failNextReadback = false
		return domaintask.Run{}, errors.New("injected terminal Run readback failure")
	}
	return store.JSONLStore.GetRun(ctx, runID)
}

type researchIndexTaskTransactionFault struct {
	domaintask.Store
	owner *researchIndexTaskStoreFault
}

func (transaction researchIndexTaskTransactionFault) SaveTask(ctx context.Context, task domaintask.Task) error {
	if transaction.owner.failTerminalWrites {
		return errors.New("injected terminal Task write failure")
	}
	return transaction.Store.SaveTask(ctx, task)
}

func (transaction researchIndexTaskTransactionFault) SaveRun(ctx context.Context, run domaintask.Run) error {
	if transaction.owner.failTerminalWrites {
		return errors.New("injected terminal Run summary write failure")
	}
	if err := transaction.Store.SaveRun(ctx, run); err != nil {
		return err
	}
	if transaction.owner.armReadbackAfterTerminalWrite && run.Status != domaintask.RunStatusRunning {
		transaction.owner.terminalRunWrites++
		transaction.owner.armReadbackAfterTerminalWrite = false
		transaction.owner.failNextReadback = true
	}
	return nil
}

func researchIndexSuccessfulBuilder() *researchIndexBuilderSpy {
	return &researchIndexBuilderSpy{result: coreresearch.ResearchDocumentIndexResult{
		DocumentsIndexed: 2, ChunksIndexed: 3, ModelID: "local-embedding-v2",
		AssetRevision: "sha256:" + strings.Repeat("a", 64),
		IndexRevision: "sha256:" + strings.Repeat("b", 64), Dimension: 1024,
	}}
}

func researchIndexPost(t *testing.T, handler http.HandlerFunc, input any, remote, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:18790/v1/research/owner", strings.NewReader(string(body)))
	request.RemoteAddr = remote
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response := httptest.NewRecorder()
	handler(response, request)
	return response
}

func researchIndexResponse(t *testing.T, response *httptest.ResponseRecorder) c.IndexOperationResponse {
	t.Helper()
	var decoded c.IndexOperationResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode index response: %v; body=%s", err, response.Body.String())
	}
	return decoded
}

func researchIndexOwnerHandler(t *testing.T, cfg *config.Config, engine *engine.Engine, tasks *taskmanager.Manager, builder researchDocumentIndexBuilder) http.HandlerFunc {
	t.Helper()
	handler, err := newResearchOwnerHandler(cfg, engine, tasks, nil, builder)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func usePhysicalResearchTestTempDir(t *testing.T) {
	t.Helper()
	physical, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatalf("resolve test temp directory: %v", err)
	}
	t.Setenv("TMPDIR", physical)
}

func researchIndexFaultRuntimeFixture(t *testing.T) (*config.Config, *engine.Engine, *taskmanager.Manager, *researchIndexTaskStoreFault) {
	t.Helper()
	root := t.TempDir()
	tokenPath := filepath.Join(root, "owner.token")
	if err := os.WriteFile(tokenPath, []byte(strings.Repeat("test", 16)), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := taskstore.NewJSONLStoreWithOptions(filepath.Join(root, "tasks"), taskstore.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	faultStore := &researchIndexTaskStoreFault{JSONLStore: store}
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
	researchEngine := &engine.Engine{Ports: engine.Ports{Store: artifacts, Acquisition: adapter, Owner: adapter}}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: tokenPath, UserID: "ren"}}
	return cfg, researchEngine, taskmanager.New(faultStore, taskmanager.DefaultParallelLimits()), faultStore
}

func TestResearchOwnerIndexBuildReplayAndStatusDoNotRebuild(t *testing.T) {
	usePhysicalResearchTestTempDir(t)
	cfg, engine, tasks := researchOwnerFixture(t)
	builder := researchIndexSuccessfulBuilder()
	handler := researchIndexOwnerHandler(t, cfg, engine, tasks, builder)
	key := "index-key-01"
	request := c.IndexRequest{SchemaVersion: c.IndexRequestSchema, EventIDs: []string{"event-b", "event-a"}}
	input := map[string]any{"operation": "index_build", "operation_key": key, "request": request}
	firstHTTP := researchIndexPost(t, handler, input, "127.0.0.1:12345", "Bearer "+strings.Repeat("test", 16))
	first := researchIndexResponse(t, firstHTTP)
	if firstHTTP.Code != http.StatusOK || first.Validate(key, first.RequestDigest) != nil || first.State != "completed" || first.Result == nil || first.Result.Status != "succeeded" {
		t.Fatalf("index build failed: status=%d response=%+v body=%s", firstHTTP.Code, first, firstHTTP.Body.String())
	}
	if _, found, err := tasks.LookupResearchAdmission(context.Background(), "other-user", key, first.RequestDigest, researchIndexAdmissionKind); err != nil || found {
		t.Fatalf("other owner obtained the stored index admission: found=%t err=%v", found, err)
	}
	if builder.calls != 1 || builder.envelope.OwnerID != "ren" || builder.envelope.DataScope != "user" || builder.envelope.Actor != "ren" || len(builder.request.EventIDs) != 2 || builder.request.EventIDs[0] != "event-a" || builder.request.EventIDs[1] != "event-b" {
		t.Fatalf("builder received an untrusted or non-canonical request: calls=%d envelope=%+v request=%+v", builder.calls, builder.envelope, builder.request)
	}
	task, err := tasks.Get(context.Background(), core.TaskID(first.Result.TaskID))
	if err != nil || task.ResearchBinding == nil || task.ResearchBinding.DataScope != "user" || task.ResearchBinding.OwnerID != "ren" || task.ResearchResult != nil {
		t.Fatalf("index Task lost its private scope binding: task=%+v err=%v", task, err)
	}
	runs, err := tasks.ListRuns(context.Background(), domaintask.RunFilter{TaskID: core.TaskID(first.Result.TaskID)})
	if err != nil || len(runs) != 1 || string(runs[0].RunID) != first.Result.RunID || runs[0].Status != domaintask.RunStatusSucceeded || runs[0].Summary == "" {
		t.Fatalf("index result was not stored on the admitted Run: runs=%+v err=%v", runs, err)
	}
	if strings.Contains(runs[0].Summary, "event-a") || strings.Contains(runs[0].Summary, "event-b") {
		t.Fatalf("exact source IDs leaked into Task summary: %s", runs[0].Summary)
	}

	input["request"] = c.IndexRequest{SchemaVersion: c.IndexRequestSchema, EventIDs: []string{"event-a", "event-b"}}
	replayHTTP := researchIndexPost(t, handler, input, "127.0.0.1:12345", "Bearer "+strings.Repeat("test", 16))
	replay := researchIndexResponse(t, replayHTTP)
	if replayHTTP.Code != http.StatusOK || replay.State != "completed" || replay.Result == nil || replay.Result.TaskID != first.Result.TaskID || builder.calls != 1 {
		t.Fatalf("same-key replay repeated or changed work: status=%d response=%+v calls=%d", replayHTTP.Code, replay, builder.calls)
	}

	input["operation"] = "index_status"
	statusHTTP := researchIndexPost(t, handler, input, "127.0.0.1:12345", "Bearer "+strings.Repeat("test", 16))
	status := researchIndexResponse(t, statusHTTP)
	if statusHTTP.Code != http.StatusOK || status.State != "completed" || status.Result == nil || status.Result.RunID != first.Result.RunID || builder.calls != 1 {
		t.Fatalf("read-only status changed or lost historical receipt: status=%d response=%+v calls=%d", statusHTTP.Code, status, builder.calls)
	}

	input["operation"] = "index_build"
	input["request"] = c.IndexRequest{SchemaVersion: c.IndexRequestSchema, EventIDs: []string{"event-c"}}
	conflict := researchIndexPost(t, handler, input, "127.0.0.1:12345", "Bearer "+strings.Repeat("test", 16))
	if conflict.Code != http.StatusConflict || builder.calls != 1 {
		t.Fatalf("same-key changed request was not blocked: status=%d body=%s calls=%d", conflict.Code, conflict.Body.String(), builder.calls)
	}
}

func TestResearchOwnerIndexStatusAndBuildReplayAfterManagerRestartDoNotRebuild(t *testing.T) {
	usePhysicalResearchTestTempDir(t)
	root := t.TempDir()
	tokenPath := filepath.Join(root, "owner.token")
	if err := os.WriteFile(tokenPath, []byte(strings.Repeat("test", 16)), 0600); err != nil {
		t.Fatal(err)
	}
	taskRoot := filepath.Join(root, "tasks")
	store, err := taskstore.NewJSONLStoreWithOptions(taskRoot, taskstore.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	storeClosed := false
	t.Cleanup(func() {
		if !storeClosed {
			_ = store.Close()
		}
	})
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
	researchEngine := &engine.Engine{Ports: engine.Ports{Store: artifacts, Acquisition: adapter, Owner: adapter}}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: tokenPath, UserID: "ren"}}
	firstTasks := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	firstBuilder := researchIndexSuccessfulBuilder()
	firstHandler := researchIndexOwnerHandler(t, cfg, researchEngine, firstTasks, firstBuilder)
	request := c.IndexRequest{SchemaVersion: c.IndexRequestSchema, EventIDs: []string{"event-a", "event-b"}}
	input := map[string]any{"operation": "index_build", "operation_key": "restart-index-key", "request": request}
	firstHTTP := researchIndexPost(t, firstHandler, input, "127.0.0.1:12345", "Bearer "+strings.Repeat("test", 16))
	first := researchIndexResponse(t, firstHTTP)
	if firstHTTP.Code != http.StatusOK || first.State != "completed" || first.Result == nil || first.Result.Status != "succeeded" || firstBuilder.calls != 1 {
		t.Fatalf("initial build failed: status=%d response=%+v calls=%d body=%s", firstHTTP.Code, first, firstBuilder.calls, firstHTTP.Body.String())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	storeClosed = true
	reopened, err := taskstore.NewJSONLStoreWithOptions(taskRoot, taskstore.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restartedTasks := taskmanager.New(reopened, taskmanager.DefaultParallelLimits())
	restartedBuilder := researchIndexSuccessfulBuilder()
	restartedHandler := researchIndexOwnerHandler(t, cfg, researchEngine, restartedTasks, restartedBuilder)
	for _, operation := range []string{"index_status", "index_build"} {
		input["operation"] = operation
		responseHTTP := researchIndexPost(t, restartedHandler, input, "127.0.0.1:12345", "Bearer "+strings.Repeat("test", 16))
		response := researchIndexResponse(t, responseHTTP)
		if responseHTTP.Code != http.StatusOK || response.State != "completed" || response.Result == nil || response.Result.TaskID != first.Result.TaskID || response.Result.RunID != first.Result.RunID {
			t.Fatalf("post-restart %s lost historical receipt: status=%d response=%+v body=%s", operation, responseHTTP.Code, response, responseHTTP.Body.String())
		}
		if restartedBuilder.calls != 0 {
			t.Fatalf("post-restart %s invoked index builder %d times", operation, restartedBuilder.calls)
		}
	}
}

func TestResearchOwnerIndexStatusMissingAndInvalidRequestsHaveNoBuilderEffects(t *testing.T) {
	usePhysicalResearchTestTempDir(t)
	cfg, engine, tasks := researchOwnerFixture(t)
	builder := researchIndexSuccessfulBuilder()
	handler := researchIndexOwnerHandler(t, cfg, engine, tasks, builder)
	valid := c.IndexRequest{SchemaVersion: c.IndexRequestSchema, EventIDs: []string{"event-a"}}
	statusHTTP := researchIndexPost(t, handler, map[string]any{"operation": "index_status", "operation_key": "missing-key", "request": valid}, "127.0.0.1:12345", "Bearer "+strings.Repeat("test", 16))
	status := researchIndexResponse(t, statusHTTP)
	if statusHTTP.Code != http.StatusOK || status.State != "not_found" || status.Admission != nil || builder.calls != 0 {
		t.Fatalf("missing status was not read-only: status=%d response=%+v calls=%d", statusHTTP.Code, status, builder.calls)
	}
	tooMany := make([]string, c.IndexMaxDocuments+1)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("event-%02d", index)
	}
	tests := []struct {
		name  string
		input any
	}{
		{name: "scope injection", input: map[string]any{"operation": "index_build", "operation_key": "scope-key", "request": valid, "owner_id": "other-user"}},
		{name: "too many IDs", input: map[string]any{"operation": "index_build", "operation_key": "large-key", "request": c.IndexRequest{SchemaVersion: c.IndexRequestSchema, EventIDs: tooMany}}},
		{name: "unknown request field", input: json.RawMessage(`{"operation":"index_build","operation_key":"unknown-key","request":{"schema_version":"rencrow.research.index.v1","event_ids":["event-a"],"owner_id":"other-user"}}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var input any = test.input
			if raw, ok := test.input.(json.RawMessage); ok {
				input = raw
			}
			response := researchIndexPost(t, handler, input, "127.0.0.1:12345", "Bearer "+strings.Repeat("test", 16))
			if response.Code != http.StatusBadRequest || builder.calls != 0 {
				t.Fatalf("invalid request reached index builder: status=%d body=%s calls=%d", response.Code, response.Body.String(), builder.calls)
			}
		})
	}
}

func TestResearchOwnerIndexAuthorizationPrecedesIndexWork(t *testing.T) {
	usePhysicalResearchTestTempDir(t)
	cfg, engine, tasks := researchOwnerFixture(t)
	builder := researchIndexSuccessfulBuilder()
	handler := researchIndexOwnerHandler(t, cfg, engine, tasks, builder)
	input := map[string]any{"operation": "index_build", "operation_key": "auth-key", "request": c.IndexRequest{SchemaVersion: c.IndexRequestSchema, EventIDs: []string{"event-a"}}}
	if response := researchIndexPost(t, handler, input, "192.0.2.10:12345", "Bearer "+strings.Repeat("test", 16)); response.Code != http.StatusForbidden {
		t.Fatalf("non-loopback caller status=%d body=%s", response.Code, response.Body.String())
	}
	if response := researchIndexPost(t, handler, input, "127.0.0.1:12345", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated caller status=%d body=%s", response.Code, response.Body.String())
	}
	if builder.calls != 0 {
		t.Fatalf("unauthenticated caller invoked builder %d times", builder.calls)
	}
}

func TestResearchOwnerIndexPartialPublicationIsTerminalWithoutReadiness(t *testing.T) {
	usePhysicalResearchTestTempDir(t)
	cfg, engine, tasks := researchOwnerFixture(t)
	builder := &researchIndexBuilderSpy{
		result: coreresearch.ResearchDocumentIndexResult{DocumentsIndexed: 2, ChunksIndexed: 5},
		err:    errors.New("injected exact readback failure"),
	}
	handler := researchIndexOwnerHandler(t, cfg, engine, tasks, builder)
	input := map[string]any{"operation": "index_build", "operation_key": "partial-key", "request": c.IndexRequest{SchemaVersion: c.IndexRequestSchema, EventIDs: []string{"event-a", "event-b"}}}
	responseHTTP := researchIndexPost(t, handler, input, "127.0.0.1:12345", "Bearer "+strings.Repeat("test", 16))
	response := researchIndexResponse(t, responseHTTP)
	if responseHTTP.Code != http.StatusOK || response.State != "completed" || response.Result == nil || response.Result.Status != "partial" || response.Result.ReadbackVerified || response.Result.PublishedDocuments != 2 || response.Result.PublishedChunks != 5 || builder.calls != 1 {
		t.Fatalf("partial publication was mislabeled or counts lost: status=%d response=%+v calls=%d body=%s", responseHTTP.Code, response, builder.calls, responseHTTP.Body.String())
	}
}

func TestResearchOwnerIndexTerminalWriteAndReadbackFailuresNeverClaimSuccess(t *testing.T) {
	for _, test := range []struct {
		name string
		arm  func(*researchIndexTaskStoreFault)
	}{
		{name: "terminal write", arm: func(store *researchIndexTaskStoreFault) { store.failTerminalWrites = true }},
		{name: "terminal readback", arm: func(store *researchIndexTaskStoreFault) { store.armReadbackAfterTerminalWrite = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			usePhysicalResearchTestTempDir(t)
			cfg, engine, tasks, store := researchIndexFaultRuntimeFixture(t)
			builder := researchIndexSuccessfulBuilder()
			builder.onBuild = func() { test.arm(store) }
			handler := researchIndexOwnerHandler(t, cfg, engine, tasks, builder)
			input := map[string]any{"operation": "index_build", "operation_key": "receipt-failure-key", "request": c.IndexRequest{SchemaVersion: c.IndexRequestSchema, EventIDs: []string{"event-a", "event-b"}}}
			firstHTTP := researchIndexPost(t, handler, input, "127.0.0.1:12345", "Bearer "+strings.Repeat("test", 16))
			first := researchIndexResponse(t, firstHTTP)
			if firstHTTP.Code != http.StatusOK || first.State != "pending" || first.Result != nil || first.ErrorCode != "outcome_unknown" || builder.calls != 1 {
				t.Fatalf("uncertain receipt was reported as completed: status=%d response=%+v calls=%d runWrites=%d readbackFailures=%d armed=%t pending=%t body=%s", firstHTTP.Code, first, builder.calls, store.terminalRunWrites, store.readbackFailures, store.armReadbackAfterTerminalWrite, store.failNextReadback, firstHTTP.Body.String())
			}
			if test.name == "terminal readback" && (store.terminalRunWrites != 1 || store.readbackFailures != 1 || store.failNextReadback) {
				t.Fatalf("readback fault was not injected after the terminal Run write: writes=%d failures=%d pending=%t", store.terminalRunWrites, store.readbackFailures, store.failNextReadback)
			}
			input["operation"] = "index_status"
			statusHTTP := researchIndexPost(t, handler, input, "127.0.0.1:12345", "Bearer "+strings.Repeat("test", 16))
			status := researchIndexResponse(t, statusHTTP)
			wantState := "pending"
			if test.name == "terminal readback" {
				wantState = "completed"
			}
			if statusHTTP.Code != http.StatusOK || status.State != wantState || status.State == "completed" && (status.Result == nil || status.Result.Status != "succeeded" || !status.Result.ReadbackVerified) {
				t.Fatalf("status returned an invalid receipt: status=%d response=%+v body=%s", statusHTTP.Code, status, statusHTTP.Body.String())
			}
			input["operation"] = "index_build"
			replayHTTP := researchIndexPost(t, handler, input, "127.0.0.1:12345", "Bearer "+strings.Repeat("test", 16))
			replay := researchIndexResponse(t, replayHTTP)
			if replayHTTP.Code != http.StatusOK || replay.State != wantState || replay.State == "completed" && (replay.Result == nil || replay.Result.RunID != status.Result.RunID) || builder.calls != 1 {
				t.Fatalf("uncertain same-key replay rebuilt or malformed receipt: status=%d response=%+v calls=%d body=%s", replayHTTP.Code, replay, builder.calls, replayHTTP.Body.String())
			}
		})
	}
}

func TestResearchIndexTerminalFallbackPreservesCountsWhenMetadataIsInvalid(t *testing.T) {
	admission := taskmanager.ResearchAdmissionReceipt{
		TaskID: core.NewTaskID(), InitialRunID: core.NewRunID(),
	}
	result := researchIndexTerminalResult("ren", strings.Repeat("a", 64), 2, admission, coreresearch.ResearchDocumentIndexResult{
		DocumentsIndexed: 2, ChunksIndexed: 5, ModelID: " ",
		AssetRevision: "untrusted-revision", IndexRevision: "also-untrusted", Dimension: 9000,
	}, nil)
	if err := result.Validate(); err != nil || result.Status != "partial" || result.ReadbackVerified || result.PublishedDocuments != 2 || result.PublishedChunks != 5 || result.ModelID != "" || result.AssetRevision != "" || result.IndexRevision != "" || result.Dimension != 0 {
		t.Fatalf("invalid metadata hid counts or yielded a false success: result=%+v err=%v", result, err)
	}
	mismatched := researchIndexTerminalResult("ren", strings.Repeat("a", 64), 2, admission, coreresearch.ResearchDocumentIndexResult{
		DocumentsIndexed: 1, ChunksIndexed: 3, ModelID: "local-embedding-v2",
		AssetRevision: "sha256:" + strings.Repeat("a", 64),
		IndexRevision: "sha256:" + strings.Repeat("b", 64), Dimension: 1024,
	}, nil)
	if err := mismatched.Validate(); err != nil || mismatched.Status != "partial" || mismatched.ReadbackVerified || mismatched.PublishedDocuments != 1 || mismatched.PublishedChunks != 3 {
		t.Fatalf("request-count mismatch was promoted to success or lost counts: result=%+v err=%v", mismatched, err)
	}
}

func TestResearchSemanticIndexCompositionSharesAuthenticatedPortWithReadOnlyRecall(t *testing.T) {
	usePhysicalResearchTestTempDir(t)
	const gatewayToken = "configured-research-gateway-token"
	const serviceRef = "research_embedding_local"
	const assetSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.Header.Get("Authorization"); got != "Bearer "+gatewayToken {
			t.Errorf("Gateway authentication = %q", got)
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v1/embedding-services/"+serviceRef+"/capability" {
			t.Errorf("unexpected Gateway request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"format_version":"rencrow.embedding-capability/v1","available":true,"model_id":"local-embedding-v2","model_revision":"sha256:%s","asset_sha256":"%s","dimension":2,"identity_basis":"configured_asset_sha256+loaded_model_probe"}`, assetSHA, assetSHA)
	}))
	defer server.Close()
	t.Setenv("RESEARCH_INDEX_TEST_GATEWAY_TOKEN", gatewayToken)
	cfg := &config.Config{
		Research:   config.ResearchConfig{Enabled: true, SemanticIndex: config.ResearchSemanticIndexConfig{Enabled: true, ServiceRef: serviceRef}},
		LLMGateway: config.LLMGatewayConfig{Enabled: true, BaseURL: server.URL, APIKeyEnv: "RESEARCH_INDEX_TEST_GATEWAY_TOKEN", TimeoutSec: 5},
	}
	embeddings, err := buildResearchSemanticEmbeddingPort(cfg)
	if err != nil || embeddings == nil {
		t.Fatalf("build configured semantic embedding port: port=%v err=%v", embeddings, err)
	}
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l1.Close() })
	policy := configuredResearchSourcePolicy(cfg.Research)
	indexBuilder, err := buildResearchDocumentIndexBuilder(cfg, l1, embeddings, policy)
	if err != nil || indexBuilder == nil {
		t.Fatalf("build single owner index builder: builder=%v err=%v", indexBuilder, err)
	}
	researchEngine := &engine.Engine{}
	if err := connectResearchRecall(researchEngine, toolRuntime{}, l1, cfg, embeddings, policy); err != nil {
		t.Fatal(err)
	}
	result, err := researchEngine.Ports.Recall.Recall(context.Background(), c.Envelope{
		Actor: "ren", OwnerID: "ren", DataScope: "user", PolicyRevision: "policy.v1",
	}, c.RecallRequest{Query: "paraphrase query", Targets: []string{"documents"}})
	if err != nil || requests != 1 || !containsString(result.Missing, "documents") {
		t.Fatalf("semantic recall did not use the configured authenticated adapter or claim configured index availability: result=%+v requests=%d err=%v", result, requests, err)
	}

	// The disabled default retains the old lexical-only constructor and never
	// probes the Gateway.
	cfg.Research.SemanticIndex.Enabled = false
	lexicalEngine := &engine.Engine{}
	if err := connectResearchRecall(lexicalEngine, toolRuntime{}, l1, cfg, nil, nil); err != nil {
		t.Fatal(err)
	}
	_, err = lexicalEngine.Ports.Recall.Recall(context.Background(), c.Envelope{
		Actor: "ren", OwnerID: "ren", DataScope: "user", PolicyRevision: "policy.v1",
	}, c.RecallRequest{Query: "lexical query", Targets: []string{"documents"}})
	if err != nil || requests != 1 {
		t.Fatalf("disabled semantic index changed lexical-only recall: requests=%d err=%v", requests, err)
	}
}
