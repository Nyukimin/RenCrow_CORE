package main

import (
	"context"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
)

func TestBuildViewerRuntimeHandlersUsesConfiguredGameObserverURL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/games/status":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/games/launch":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true,"game_id":"nethack","session_id":"session-1","status":"launching"}`))
		default:
			t.Fatalf("upstream path=%q", r.URL.Path)
		}
	}))
	t.Cleanup(upstream.Close)
	legacyURL := "http://127.0.0.1:1"
	if err := os.Setenv("RENCROW_GAMES_OBSERVER_URL", legacyURL); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("RENCROW_GAMES_OBSERVER_URL") })

	deps := &Dependencies{}
	cfg := &config.Config{WorkspaceDir: t.TempDir()}
	cfg.Games.ObserverURL = upstream.URL
	if err := initializeRuntimeTaskOwner(deps, cfg.WorkspaceDir); err != nil {
		t.Fatalf("initialize task owner: %v", err)
	}
	ownerStore := deps.taskStore
	ownerManager := deps.taskManager
	t.Cleanup(func() {
		if err := ownerManager.Close(); err != nil {
			t.Errorf("close task owner: %v", err)
		}
	})
	created, err := ownerManager.Create(context.Background(), domaintask.Task{Title: "shared viewer task"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create shared task: %v", err)
	}
	buildViewerRuntimeHandlers(cfg, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
	if deps.taskStore != ownerStore || deps.taskManager != ownerManager {
		t.Fatal("Viewer construction replaced the canonical Task owner")
	}
	if deps.taskManager == nil || deps.tasks == nil || deps.taskDetail == nil || deps.taskNotifications == nil {
		t.Fatal("canonical Task store was not shared with the Orchestrator lifecycle and Viewer handlers")
	}
	tasksRec := httptest.NewRecorder()
	deps.tasks.ServeHTTP(tasksRec, httptest.NewRequest(http.MethodGet, "/viewer/tasks", nil))
	if tasksRec.Code != http.StatusOK || !strings.Contains(tasksRec.Body.String(), string(created.TaskID)) {
		t.Fatalf("Viewer did not read the initialized owner store: status=%d body=%s", tasksRec.Code, tasksRec.Body.String())
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/games/observer-api/games/status", nil)
	deps.viewerGamesObserverProxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Fatalf("observer proxy status=%d body=%q", rec.Code, rec.Body.String())
	}
	launchRec := httptest.NewRecorder()
	launchReq := httptest.NewRequest(http.MethodPost, "/viewer/games/launch", strings.NewReader(`{"game_id":"nethack"}`))
	deps.viewerGamesLaunch.ServeHTTP(launchRec, launchReq)
	if launchRec.Code != http.StatusOK || !strings.Contains(launchRec.Body.String(), `"session_id":"session-1"`) {
		t.Fatalf("game launch status=%d body=%q", launchRec.Code, launchRec.Body.String())
	}
}

func TestInitializeRuntimeTaskOwnerRejectsDuplicateWithoutReplacingUsableOwner(t *testing.T) {
	deps := &Dependencies{}
	if err := initializeRuntimeTaskOwner(deps, t.TempDir()); err != nil {
		t.Fatalf("initialize task owner: %v", err)
	}
	ownerStore := deps.taskStore
	ownerManager := deps.taskManager
	t.Cleanup(func() {
		if err := ownerManager.Close(); err != nil {
			t.Errorf("close task owner: %v", err)
		}
	})
	if err := initializeRuntimeTaskOwner(deps, t.TempDir()); err == nil {
		t.Fatal("duplicate Task owner initialization was accepted")
	}
	if deps.taskStore != ownerStore || deps.taskManager != ownerManager {
		t.Fatal("duplicate initialization replaced the canonical owner")
	}
	if _, err := ownerManager.Create(context.Background(), domaintask.Task{Title: "owner remains usable"}, domaintask.SharedRoleContext{}); err != nil {
		t.Fatalf("original Task owner became unusable: %v", err)
	}
}

func TestInitializeRuntimeTaskOwnerRejectsBusyWriterWithoutPublishingOwner(t *testing.T) {
	root := t.TempDir()
	busy, err := taskpersistence.NewJSONLStore(defaultTaskStorePath(root))
	if err != nil {
		t.Fatalf("open busy task writer: %v", err)
	}
	t.Cleanup(func() {
		if err := busy.Close(); err != nil {
			t.Errorf("close busy task writer: %v", err)
		}
	})
	deps := &Dependencies{}
	if err := initializeRuntimeTaskOwner(deps, root); err == nil {
		t.Fatal("busy Task writer was accepted")
	}
	if deps.taskStore != nil || deps.taskManager != nil {
		t.Fatal("failed Task owner initialization published dependencies")
	}
}

func TestInitializeRuntimeTaskOwnerRejectsMissingDependenciesAndWorkspace(t *testing.T) {
	if err := initializeRuntimeTaskOwner(nil, t.TempDir()); err == nil {
		t.Fatal("nil dependencies were accepted")
	}
	if err := initializeRuntimeTaskOwner(&Dependencies{}, " "); err == nil {
		t.Fatal("empty workspace was accepted")
	}
}

func TestBuildViewerRuntimeHandlersRegistersSourceRegistryUnavailableHandler(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
	if deps.viewerSourceRegistry == nil {
		t.Fatal("viewerSourceRegistry handler is nil")
	}

	req := httptest.NewRequest(http.MethodGet, "/viewer/source-registry", nil)
	rec := httptest.NewRecorder()
	deps.viewerSourceRegistry.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "source registry unavailable") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestBuildViewerRuntimeHandlersRegistersMemoryLayersUnavailableHandler(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
	if deps.viewerMemoryLayers == nil {
		t.Fatal("viewerMemoryLayers handler is nil")
	}

	req := httptest.NewRequest(http.MethodGet, "/viewer/memory/layers", nil)
	rec := httptest.NewRecorder()
	deps.viewerMemoryLayers.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "memory layers unavailable") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestBuildViewerRuntimeHandlersRegistersRecallTraceUnavailableHandler(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
	if deps.viewerRecallTraces == nil {
		t.Fatal("viewerRecallTraces handler is nil")
	}

	req := httptest.NewRequest(http.MethodGet, "/viewer/recall/traces?limit=5", nil)
	rec := httptest.NewRecorder()
	deps.viewerRecallTraces.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"unavailable"`) {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestBuildViewerRuntimeHandlersRegistersDomainGraphUnavailableHandler(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
	if deps.viewerDomainGraphAssertions == nil {
		t.Fatal("viewerDomainGraphAssertions handler is nil")
	}

	req := httptest.NewRequest(http.MethodGet, "/viewer/domain-graph/assertions", nil)
	rec := httptest.NewRecorder()
	deps.viewerDomainGraphAssertions.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "domain graph unavailable") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestBuildViewerRuntimeHandlersRegistersMovieDomainGraphSyncUnavailableHandler(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
	if deps.viewerMovieDomainGraphSync == nil {
		t.Fatal("viewerMovieDomainGraphSync handler is nil")
	}

	req := httptest.NewRequest(http.MethodPost, "/viewer/movie-catalog/domain-graph-sync", nil)
	rec := httptest.NewRecorder()
	deps.viewerMovieDomainGraphSync.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "movie domain graph sync unavailable") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestBuildViewerRuntimeHandlersRegistersHobbyDomainGraphSyncUnavailableHandler(t *testing.T) {
	deps := &Dependencies{}
	buildViewerRuntimeHandlers(&config.Config{}, deps, nil, nil, filepath.Join(t.TempDir(), "reports.jsonl"), nil, nil)
	if deps.viewerHobbyDomainGraphSync == nil {
		t.Fatal("viewerHobbyDomainGraphSync handler is nil")
	}

	req := httptest.NewRequest(http.MethodPost, "/viewer/hobby-graph/domain-graph-sync", nil)
	rec := httptest.NewRecorder()
	deps.viewerHobbyDomainGraphSync.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if !strings.Contains(rec.Body.String(), "hobby domain graph sync unavailable") {
		t.Fatalf("body=%q", rec.Body.String())
	}
}

func TestRuntimeToolAdmissionRemainsMandatoryWithoutSecurity(t *testing.T) {
	disabled := false
	cfg := &config.Config{WorkspaceDir: t.TempDir(), ToolHarness: config.ToolHarnessConfig{Enabled: &disabled, RecordEvents: &disabled}}
	deps := &Dependencies{}
	if err := initializeRuntimeTaskOwner(deps, cfg.WorkspaceDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = deps.taskManager.Close() })
	task, err := deps.taskManager.Create(context.Background(), domaintask.Task{Title: "runtime admission", Assignee: "shiro"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := deps.taskManager.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := domainexecution.WithIdentity(context.Background(), task.TaskID, run.RunID, modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	scope, err := domaintool.NewToolExecutionScope("runtime-admission-test", domaintool.ActorKindAgent, "shiro", "", []string{domaintool.DataScopePublic}, domaintool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	ctx = domaintool.WithToolExecutionScope(ctx, scope)
	runtime := buildToolRuntimeWithCapabilities(deps.taskManager, cfg, nil, nil, nil, nil, nil, testCanonicalMediationStore(t))
	path := filepath.Join(cfg.WorkspaceDir, "admission.txt")
	if err := os.WriteFile(path, []byte("owner-visible-content"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, runner := range []domaintool.RunnerV2{runtime.ChatRuntimeRunnerV2, runtime.WorkerRuntimeRunnerV2} {
		if _, err := runner.ExecuteV2(context.Background(), "file_read", map[string]any{"path": path}); err == nil {
			t.Fatal("missing identity accepted with security disabled")
		}
		response, err := runner.ExecuteV2(ctx, "file_read", map[string]any{"path": path})
		if err != nil || response == nil || response.IsError() || !strings.Contains(response.String(), "owner-visible-content") {
			t.Fatalf("valid runtime execution: response=%v error=%v", response, err)
		}
	}
	if _, err := deps.taskManager.Succeed(ctx, task.TaskID, "done"); err != nil {
		t.Fatal(err)
	}
	for _, runner := range []domaintool.RunnerV2{runtime.ChatRuntimeRunnerV2, runtime.WorkerRuntimeRunnerV2} {
		if _, err := runner.ExecuteV2(ctx, "file_read", map[string]any{"path": path}); err == nil {
			t.Fatal("terminal Run admitted")
		}
	}
}

// runtimeToolOwnerFixture supplies real persisted owner state for runtime runner tests.
func runtimeToolOwnerFixture(t *testing.T, workspace, actor string) (*taskmanager.Manager, context.Context) {
	t.Helper()
	if workspace == "" {
		workspace = t.TempDir()
	}
	deps := &Dependencies{}
	if err := initializeRuntimeTaskOwner(deps, workspace); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := deps.taskManager.Close(); err != nil {
			t.Error(err)
		}
	})
	task, err := deps.taskManager.Create(context.Background(), domaintask.Task{Title: "runtime tool fixture", Assignee: actor}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := deps.taskManager.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := domainexecution.WithIdentity(context.Background(), task.TaskID, run.RunID, modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	scope, err := domaintool.NewToolExecutionScope("runtime-tool-fixture", domaintool.ActorKindAgent, actor, "", []string{domaintool.DataScopePublic}, domaintool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	return deps.taskManager, domaintool.WithToolExecutionScope(ctx, scope)
}
