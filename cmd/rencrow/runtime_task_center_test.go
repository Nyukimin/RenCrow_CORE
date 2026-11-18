package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func TestLoadTaskExecutionPolicyValidatesAndHashes(t *testing.T) {
	path := writeTaskCenterPolicyFixture(t, "schema_version: 1\ntask_kinds:\n  movie.catalog.lookup:\n    allowed_agents:\n      mode: all_registered\n")
	kinds := []string{"movie.catalog.lookup"}
	actors := []string{"mio", "midori"}
	policy, revision, err := loadTaskExecutionPolicy(path, kinds, actors)
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.Validate(kinds, actors); err != nil {
		t.Fatalf("loaded policy is invalid: %v", err)
	}
	want, err := policy.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if revision != want || len(revision) != 64 {
		t.Fatalf("revision=%q want canonical sha256 %q", revision, want)
	}
}

func TestLoadTaskExecutionPolicyRejectsInvalidPathDocumentAndRegistry(t *testing.T) {
	valid := "schema_version: 1\ntask_kinds:\n  movie.catalog.lookup:\n    allowed_agents:\n      mode: all_registered\n"
	unknownField := "schema_version: 1\nunexpected: true\ntask_kinds:\n  movie.catalog.lookup:\n    allowed_agents:\n      mode: all_registered\n"
	secondDocument := valid + "---\nschema_version: 1\ntask_kinds: {}\n"
	unknownActor := "schema_version: 1\ntask_kinds:\n  movie.catalog.lookup:\n    allowed_agents:\n      mode: explicit\n      actor_ids: [missing]\n"
	unknownKind := strings.ReplaceAll(valid, "movie.catalog.lookup", "writing.novel")
	for _, test := range []struct {
		name string
		path string
		body string
	}{
		{name: "relative path", path: "policy.yaml", body: valid},
		{name: "unknown field", body: unknownField},
		{name: "multiple documents", body: secondDocument},
		{name: "unknown actor", body: unknownActor},
		{name: "unknown kind", body: unknownKind},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := test.path
			if path == "" {
				path = writeTaskCenterPolicyFixture(t, test.body)
			}
			if _, _, err := loadTaskExecutionPolicy(path, []string{"movie.catalog.lookup"}, []string{"midori"}); err == nil {
				t.Fatal("expected policy load error")
			}
		})
	}
}

func TestLoadTaskExecutionPolicyRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.yaml")
	if err := os.WriteFile(path, []byte(strings.Repeat("#", maxTaskExecutionPolicyBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadTaskExecutionPolicy(path, []string{"movie.catalog.lookup"}, []string{"midori"}); err == nil {
		t.Fatal("expected oversized policy to fail")
	}
}

func writeTaskCenterPolicyFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type runtimeTaskCenterTestService struct {
	capabilitiesCalls int
	listCalls         int
	getCalls          int
	lastGoalID        string
	lastScope         domaintool.ToolExecutionScope
}

func (s *runtimeTaskCenterTestService) Capabilities(ctx context.Context) (taskcenter.Capabilities, error) {
	s.capabilitiesCalls++
	s.lastScope, _ = domaintool.ToolExecutionScopeFromContext(ctx)
	return taskcenter.Capabilities{Kinds: []taskcenter.KindCapability{{KindID: "movie.catalog.lookup", Label: "Movie catalog lookup"}}, PolicyRevision: strings.Repeat("a", 64)}, nil
}
func (s *runtimeTaskCenterTestService) List(ctx context.Context, _ taskcenter.ListQuery) (taskcenter.ListResult, error) {
	s.listCalls++
	s.lastScope, _ = domaintool.ToolExecutionScopeFromContext(ctx)
	return taskcenter.ListResult{Items: []taskcenter.Item{}}, nil
}
func (s *runtimeTaskCenterTestService) Get(ctx context.Context, goalID string) (taskcenter.Item, error) {
	s.getCalls++
	s.lastGoalID = goalID
	s.lastScope, _ = domaintool.ToolExecutionScopeFromContext(ctx)
	return taskcenter.Item{GoalID: goalID}, nil
}
func (*runtimeTaskCenterTestService) Submit(context.Context, taskcenter.SubmitInput) (taskcenter.MutationResult, error) {
	return taskcenter.MutationResult{}, nil
}
func (*runtimeTaskCenterTestService) Claim(context.Context, taskcenter.ClaimInput) (taskcenter.MutationResult, error) {
	return taskcenter.MutationResult{}, nil
}

func TestTaskCenterLateAttachFlowsThroughAdmittedWorkerAndCapabilitySnapshot(t *testing.T) {
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	deps := &Dependencies{}
	if err := initializeRuntimeTaskOwner(deps, workspace); err != nil {
		t.Fatalf("initialize Task owner: %v", err)
	}
	t.Cleanup(func() {
		if err := deps.taskManager.Close(); err != nil {
			t.Errorf("close Task owner: %v", err)
		}
	})
	task, err := deps.taskManager.Create(context.Background(), domaintask.Task{Title: "Worker TaskCenter fixture", Route: domaintask.RouteGeneral, Assignee: "shiro"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := deps.taskManager.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	base := tools.NewToolRunner(tools.ToolRunnerConfig{DisableToolHarness: true})
	workerRuntime := tools.NewContextBudgetRunner(base, tools.ContextBudgetRunnerConfig{Agent: "shiro"})
	securedWorker := &taskExecutionRunner{owner: deps.taskManager, inner: workerRuntime}
	service := &runtimeTaskCenterTestService{}
	base.WithTaskCenterService(service, true)

	metadata, err := securedWorker.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, item := range metadata {
		seen[item.ToolID] = true
	}
	for _, id := range []string{"tasks.capabilities", "tasks.list_remaining", "tasks.list_claimable", "tasks.get", "tasks.add", "tasks.claim"} {
		if !seen[id] {
			t.Fatalf("late-attached Tool %q missing from secured Worker ListTools", id)
		}
	}
	stableContext := capdomain.RenderStableRuntimeContext(buildRuntimeCapabilitySnapshot(metadata, nil, nil))
	for _, id := range []string{"tasks.capabilities", "tasks.get", "tasks.add"} {
		if !strings.Contains(stableContext, id) {
			t.Fatalf("stable runtime context omitted %q: %s", id, stableContext)
		}
	}

	ctx := newTaskExecutionRunnerContext(t, task, run, domaintool.ActorKindAgent, "shiro", true, true)
	response, err := securedWorker.ExecuteV2(ctx, "tasks.capabilities", map[string]any{})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("admitted Worker capabilities call response=%+v err=%v", response, err)
	}
	if service.capabilitiesCalls != 1 || service.lastScope.ActorKind != domaintool.ActorKindAgent || service.lastScope.ActorID != "shiro" {
		t.Fatalf("service call=%d scope=%+v", service.capabilitiesCalls, service.lastScope)
	}
}

func TestTaskCenterKindsRequireActualOwnerBinding(t *testing.T) {
	kinds := taskCenterOperationKinds(toolRuntime{})
	if len(kinds) != 2 || kinds[0].ID != "movie.catalog.lookup" || kinds[0].ExecutorAvailable || kinds[0].UnavailableReason == "" ||
		kinds[1].ID != "writing.novel" || kinds[1].ExecutorAvailable || len(kinds[1].IntrinsicAllowedAgents) != 1 || kinds[1].IntrinsicAllowedAgents[0] != "midori" {
		t.Fatalf("unbound runtime kind definitions = %+v", kinds)
	}
	kinds = taskCenterOperationKinds(toolRuntime{MovieCatalogLookup: &runtimeMovieCatalogLookup{}})
	if len(kinds) != 2 || kinds[0].ID != "movie.catalog.lookup" || !kinds[0].ExecutorAvailable || kinds[0].UnavailableReason != "" || kinds[1].ID != "writing.novel" {
		t.Fatalf("registered runtime kinds = %+v", kinds)
	}
}

func TestTaskCenterNativeOwnerRejectsTypedNilManager(t *testing.T) {
	var manager *taskmanager.Manager
	if owner := taskCenterNativeOwner(manager); owner != nil {
		t.Fatalf("typed nil TaskManager advertised native execution owner: %T", owner)
	}
}

func TestRegisterTaskCenterRoutesServesPageAndExplicitUnavailableAPI(t *testing.T) {
	mux := http.NewServeMux()
	registerTaskCenterRoutes(mux, &config.Config{}, &Dependencies{})
	pageRequest := httptest.NewRequest(http.MethodGet, "/viewer/task-center", nil)
	pageResponse := httptest.NewRecorder()
	mux.ServeHTTP(pageResponse, pageRequest)
	if pageResponse.Code != http.StatusOK {
		t.Fatalf("task-center page status=%d body=%s", pageResponse.Code, pageResponse.Body.String())
	}
	apiRequest := httptest.NewRequest(http.MethodGet, "/api/v1/task-center/capabilities", nil)
	apiRequest.RemoteAddr = "127.0.0.1:1234"
	apiRequest.Host = "127.0.0.1:8080"
	apiResponse := httptest.NewRecorder()
	mux.ServeHTTP(apiResponse, apiRequest)
	if apiResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured API status=%d body=%s", apiResponse.Code, apiResponse.Body.String())
	}
}

func TestTaskCenterUnavailableWorkstreamStillAuthenticatesConfiguredUser(t *testing.T) {
	tokenPath := filepath.Join(t.TempDir(), "local-agent-ops.token")
	if err := os.WriteFile(tokenPath, []byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	workstreamEnabled := false
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{
		Enabled:       true,
		AuthTokenFile: tokenPath,
		UserID:        "taskcenter-test-user",
	}, Workstream: config.WorkstreamConfig{Enabled: &workstreamEnabled, Storage: "sqlite"}}
	deps := &Dependencies{}
	buildTaskCenterRuntime(cfg, deps, nil, agentRuntime{}, toolRuntime{})
	if deps.taskCenter != nil || deps.taskCenterUnavailableReason != "task_center_workstream_unavailable" {
		t.Fatalf("service=%v unavailable_reason=%q", deps.taskCenter, deps.taskCenterUnavailableReason)
	}
	if deps.taskCenterUserID != cfg.LocalAgentOps.UserID || string(deps.taskCenterUserToken) != strings.Repeat("a", 64) {
		t.Fatalf("TaskCenter user credentials were not loaded: user=%q token_bytes=%d", deps.taskCenterUserID, len(deps.taskCenterUserToken))
	}

	mux := http.NewServeMux()
	registerTaskCenterRoutes(mux, cfg, deps)
	handler := withInteractionProfileGuard(mux)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/task-center/capabilities", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	request.Host = "127.0.0.1:8080"
	request.Header.Set("X-RenCrow-Client", "RenCrow_Viewer")
	request.Header.Set("X-RenCrow-Interaction-Profile", "task-center")
	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, request)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/task-center/capabilities", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	request.Host = "127.0.0.1:8080"
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 64))
	request.Header.Set("X-RenCrow-Client", "RenCrow_Viewer")
	request.Header.Set("X-RenCrow-Interaction-Profile", "task-center")
	authenticated := httptest.NewRecorder()
	handler.ServeHTTP(authenticated, request)
	if authenticated.Code != http.StatusServiceUnavailable || !strings.Contains(authenticated.Body.String(), "task_center_workstream_unavailable") {
		t.Fatalf("authenticated unavailable API status=%d body=%s", authenticated.Code, authenticated.Body.String())
	}
}
