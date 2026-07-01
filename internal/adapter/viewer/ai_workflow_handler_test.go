package viewer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	aiworkflowapp "github.com/Nyukimin/picoclaw_multiLLM/internal/application/aiworkflow"
	domainai "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/aiworkflow"
	domainskill "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/skillgovernance"
)

type stubAIWorkflowStore struct {
	events    []domainai.WorkflowEvent
	memories  []domainai.ProjectMemoryIndex
	worktrees []domainai.WorktreeRegistry
	commands  []domainai.CommandRegistry
	contexts  []domainai.ContextUsage
}

func (s *stubAIWorkflowStore) SaveWorkflowEvent(_ context.Context, item domainai.WorkflowEvent) error {
	if err := domainai.ValidateWorkflowEvent(item); err != nil {
		return err
	}
	s.events = append(s.events, item)
	return nil
}
func (s *stubAIWorkflowStore) ListWorkflowEvents(_ context.Context, _ int) ([]domainai.WorkflowEvent, error) {
	return s.events, nil
}
func (s *stubAIWorkflowStore) SaveProjectMemoryIndex(_ context.Context, item domainai.ProjectMemoryIndex) error {
	if err := domainai.ValidateProjectMemoryIndex(item); err != nil {
		return err
	}
	s.memories = append(s.memories, item)
	return nil
}
func (s *stubAIWorkflowStore) ListProjectMemoryIndexes(_ context.Context, _ int) ([]domainai.ProjectMemoryIndex, error) {
	return s.memories, nil
}
func (s *stubAIWorkflowStore) SaveWorktreeRegistry(_ context.Context, item domainai.WorktreeRegistry) error {
	if err := domainai.ValidateWorktreeRegistry(item); err != nil {
		return err
	}
	s.worktrees = append(s.worktrees, item)
	return nil
}
func (s *stubAIWorkflowStore) ListWorktreeRegistries(_ context.Context, _ int) ([]domainai.WorktreeRegistry, error) {
	return s.worktrees, nil
}
func (s *stubAIWorkflowStore) SaveCommandRegistry(_ context.Context, item domainai.CommandRegistry) error {
	if err := domainai.ValidateCommandRegistry(item); err != nil {
		return err
	}
	s.commands = append(s.commands, item)
	return nil
}
func (s *stubAIWorkflowStore) ListCommandRegistries(_ context.Context, _ int) ([]domainai.CommandRegistry, error) {
	return s.commands, nil
}
func (s *stubAIWorkflowStore) SaveContextUsage(_ context.Context, item domainai.ContextUsage) error {
	if err := domainai.ValidateContextUsage(item); err != nil {
		return err
	}
	s.contexts = append(s.contexts, item)
	return nil
}
func (s *stubAIWorkflowStore) ListContextUsages(_ context.Context, _ int) ([]domainai.ContextUsage, error) {
	return s.contexts, nil
}

type stubAIWorkflowSkillBootstrap struct {
	task domainskill.TaskContext
	used []string
	logs []domainskill.SkillTriggerLog
}

func (s *stubAIWorkflowSkillBootstrap) Record(_ context.Context, task domainskill.TaskContext, usedSkillIDs []string) ([]domainskill.SkillTriggerLog, error) {
	s.task = task
	s.used = append([]string(nil), usedSkillIDs...)
	if s.logs == nil {
		s.logs = []domainskill.SkillTriggerLog{{EventID: "skill_1", SkillID: "core.review", Status: domainskill.TriggerStatusTriggered, CreatedAt: time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)}}
	}
	return s.logs, nil
}

func TestHandleAIWorkflowStatus(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubAIWorkflowStore{
		events:    []domainai.WorkflowEvent{{EventID: "evt_1", EventType: "project_init_started", Status: "completed", CreatedAt: now}},
		memories:  []domainai.ProjectMemoryIndex{{ID: "mem_1", Repo: "repo", FilePath: ".ai/PROJECT_MEMORY.md", MemoryType: "project", UpdatedAt: now}},
		worktrees: []domainai.WorktreeRegistry{{WorktreeID: "wt_1", Repo: "repo", Path: "../worktrees/repo-feature", Branch: "feature/a", Status: "active", CreatedAt: now}},
		commands:  []domainai.CommandRegistry{{CommandName: "/review-architecture", FilePath: "commands/review-architecture.md", UpdatedAt: now}},
		contexts:  []domainai.ContextUsage{{EventID: "ctx_1", JobID: "job_1", WorkstreamID: "ws_1", Agent: "Coder", ContextTokens: 120, CreatedAt: now}},
	}
	rec := httptest.NewRecorder()
	HandleAIWorkflowStatus(store).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/ai-workflow", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"workflow_events", "project_memory_indexes", "worktree_registries", "command_registries", "context_usages", "usage_continuity", "context_budget_policy", "job_1", "ws_1"} {
		if !strings.Contains(body, want) {
			t.Fatalf("response missing %s: %s", want, body)
		}
	}
}

func TestHandleAIWorkflowStatusWithPolicyShowsEffectiveContextBudget(t *testing.T) {
	store := &stubAIWorkflowStore{}
	rec := httptest.NewRecorder()
	HandleAIWorkflowStatusWithPolicy(store, domainai.ContextBudgetPolicy{
		MaxContextTokens: 1000,
		WarnAtRatio:      0.8,
		StopAtRatio:      0.95,
	}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/ai-workflow", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"context_budget_policy"`, `"max_context_tokens":1000`, `"warn_at_ratio":0.8`, `"stop_at_ratio":0.95`} {
		if !strings.Contains(body, want) {
			t.Fatalf("response missing %s: %s", want, body)
		}
	}
}

func TestHandleAIWorkflowEventCreate(t *testing.T) {
	store := &stubAIWorkflowStore{}
	rec := httptest.NewRecorder()
	body := `{"event_id":"evt_1","event_type":"project_init_started","status":"completed","created_at":"2026-05-18T12:00:00Z"}`
	HandleAIWorkflowEventCreate(store).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/ai-workflow/events", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.events) != 1 || store.events[0].EventID != "evt_1" {
		t.Fatalf("events=%#v", store.events)
	}
}

func TestHandleAIWorkflowExternalControlCheckRequiresApproval(t *testing.T) {
	store := &stubAIWorkflowStore{}
	rec := httptest.NewRecorder()
	body := `{"actor":"Worker","channel_id":"viewer","action":"promotion_apply","human_approved":false}`
	HandleAIWorkflowExternalControlCheck(store, domainai.ExternalControlPolicy{
		AllowedActors:    []string{"Worker"},
		AllowedChannels:  []string{"viewer"},
		AllowedActions:   []string{"promotion_apply"},
		ApprovalRequired: []string{"promotion_apply"},
	}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/ai-workflow/external-control/check", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"needs_approval"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
	if len(store.events) != 1 || store.events[0].EventType != "external_control_policy_checked" || store.events[0].Status != domainai.ExternalControlStatusNeedsApproval {
		t.Fatalf("events=%#v", store.events)
	}
}

func TestHandleAIWorkflowExternalControlCheckBlocksUnknownChannel(t *testing.T) {
	rec := httptest.NewRecorder()
	body := `{"actor":"Worker","channel_id":"public-web","action":"status_read","human_approved":true}`
	HandleAIWorkflowExternalControlCheck(nil, domainai.ExternalControlPolicy{
		AllowedActors:   []string{"Worker"},
		AllowedChannels: []string{"viewer"},
		AllowedActions:  []string{"status_read"},
	}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/ai-workflow/external-control/check", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"blocked"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestHandleAIWorkflowCommandRunSavesEventAndRunsSkillBootstrap(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubAIWorkflowStore{
		commands: []domainai.CommandRegistry{{
			CommandName:   "/review-architecture",
			FilePath:      "commands/review-architecture.md",
			Description:   "review architecture",
			DefaultAgent:  "Coder",
			RequiredSkill: "core.review",
			UpdatedAt:     now,
		}},
	}
	skills := &stubAIWorkflowSkillBootstrap{}
	rec := httptest.NewRecorder()
	body := `{"command_name":"/review-architecture","text":"境界を確認して","run_id":"run_1","workstream_id":"ws_1"}`

	HandleAIWorkflowCommandRun(store, skills).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/ai-workflow/commands/run", strings.NewReader(body)))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.events) != 1 || store.events[0].EventType != "command_invoked" || store.events[0].CommandName != "/review-architecture" {
		t.Fatalf("events=%#v", store.events)
	}
	if store.events[0].RunID != "run_1" || store.events[0].WorkstreamID != "ws_1" {
		t.Fatalf("event run/workstream=%#v", store.events[0])
	}
	if skills.task.Intent != "review-architecture" || skills.task.Agent != "Coder" || skills.task.WorkstreamID != "ws_1" {
		t.Fatalf("task=%#v", skills.task)
	}
	if len(skills.used) != 1 || skills.used[0] != "core.review" {
		t.Fatalf("used skills=%#v", skills.used)
	}
	if !strings.Contains(rec.Body.String(), `"skills"`) {
		t.Fatalf("missing skill logs: %s", rec.Body.String())
	}
}

func TestHandleAIWorkflowContextUsageRejectsInvalidPayload(t *testing.T) {
	store := &stubAIWorkflowStore{}
	rec := httptest.NewRecorder()
	body := `{"event_id":"ctx_1","agent":"Coder","input_tokens":-1}`
	HandleAIWorkflowContextUsageCreate(store).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/ai-workflow/context-usages", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleAIWorkflowContextBudgetCheckWarnsAndSavesEvent(t *testing.T) {
	store := &stubAIWorkflowStore{}
	rec := httptest.NewRecorder()
	body := `{"event_id":"ctx_warn","agent":"Coder","context_tokens":850,"created_at":"2026-05-18T12:00:00Z"}`

	HandleAIWorkflowContextBudgetCheck(store, domainai.ContextBudgetPolicy{
		MaxContextTokens: 1000,
		WarnAtRatio:      0.8,
		StopAtRatio:      0.95,
	}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/ai-workflow/context-budget/check", strings.NewReader(body)))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.contexts) != 1 || store.contexts[0].EventID != "ctx_warn" {
		t.Fatalf("contexts=%#v", store.contexts)
	}
	if len(store.events) != 1 || store.events[0].EventType != "context_budget_warning" || store.events[0].Status != domainai.ContextBudgetStatusWarn {
		t.Fatalf("events=%#v", store.events)
	}
	if !strings.Contains(rec.Body.String(), `"status":"warn"`) {
		t.Fatalf("missing warn decision: %s", rec.Body.String())
	}
}

func TestHandleAIWorkflowContextBudgetCheckStopsAndSavesEvent(t *testing.T) {
	store := &stubAIWorkflowStore{}
	rec := httptest.NewRecorder()
	body := `{"event_id":"ctx_stop","agent":"Worker","context_tokens":950,"created_at":"2026-05-18T12:00:00Z"}`

	HandleAIWorkflowContextBudgetCheck(store, domainai.ContextBudgetPolicy{
		MaxContextTokens: 1000,
		WarnAtRatio:      0.8,
		StopAtRatio:      0.95,
	}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/ai-workflow/context-budget/check", strings.NewReader(body)))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.events) != 1 || store.events[0].EventType != "context_budget_exceeded" || store.events[0].Status != domainai.ContextBudgetStatusStop {
		t.Fatalf("events=%#v", store.events)
	}
}

func TestHandleAIWorkflowCommandAndContextBudgetAreVisibleInStatus(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubAIWorkflowStore{
		commands: []domainai.CommandRegistry{{
			CommandName:   "/review-architecture",
			FilePath:      "commands/review-architecture.md",
			Description:   "review architecture",
			DefaultAgent:  "Coder",
			RequiredSkill: "core.review",
			UpdatedAt:     now,
		}},
	}
	skills := &stubAIWorkflowSkillBootstrap{}

	commandRec := httptest.NewRecorder()
	HandleAIWorkflowCommandRun(store, skills).ServeHTTP(commandRec, httptest.NewRequest(http.MethodPost, "/viewer/ai-workflow/commands/run", strings.NewReader(
		`{"command_name":"/review-architecture","text":"境界を確認して","run_id":"run_1","workstream_id":"ws_1"}`,
	)))
	if commandRec.Code != http.StatusCreated {
		t.Fatalf("command status=%d body=%s", commandRec.Code, commandRec.Body.String())
	}

	contextRec := httptest.NewRecorder()
	HandleAIWorkflowContextBudgetCheck(store, domainai.ContextBudgetPolicy{
		MaxContextTokens: 1000,
		WarnAtRatio:      0.8,
		StopAtRatio:      0.95,
	}).ServeHTTP(contextRec, httptest.NewRequest(http.MethodPost, "/viewer/ai-workflow/context-budget/check", strings.NewReader(
		`{"event_id":"ctx_ws_1","session_id":"ws_1","agent":"Coder","context_tokens":850,"created_at":"2026-05-18T12:00:00Z"}`,
	)))
	if contextRec.Code != http.StatusCreated {
		t.Fatalf("context status=%d body=%s", contextRec.Code, contextRec.Body.String())
	}

	statusRec := httptest.NewRecorder()
	HandleAIWorkflowStatus(store).ServeHTTP(statusRec, httptest.NewRequest(http.MethodGet, "/viewer/ai-workflow", nil))
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", statusRec.Code, statusRec.Body.String())
	}
	body := statusRec.Body.String()
	for _, want := range []string{"command_invoked", "context_budget_warning", "ctx_ws_1", "run_1", "ws_1", "/review-architecture"} {
		if !strings.Contains(body, want) {
			t.Fatalf("status response missing %s: %s", want, body)
		}
	}
}

func TestHandleAIWorkflowStatusShowsUsageContinuityAcrossJobAndWorkstream(t *testing.T) {
	now := time.Date(2026, 6, 22, 7, 0, 0, 0, time.UTC)
	store := &stubAIWorkflowStore{
		events: []domainai.WorkflowEvent{{
			EventID:      "evt_ws_done",
			RunID:        "run_1",
			WorkstreamID: "ws_1",
			EventType:    "backlog_runner",
			Status:       "completed",
			CreatedAt:    now.Add(2 * time.Minute),
		}},
		contexts: []domainai.ContextUsage{
			{EventID: "ctx_before", JobID: "job_1", RunID: "run_1", WorkstreamID: "ws_1", SessionID: "session_1", Agent: "Coder", ContextTokens: 100, CreatedAt: now},
			{EventID: "ctx_after", JobID: "job_1", RunID: "run_1", WorkstreamID: "ws_1", SessionID: "session_1", CompactionID: "compact_1", Agent: "Coder", Model: "Worker", ContextTokens: 80, InputTokens: 12, OutputTokens: 5, CreatedAt: now.Add(time.Minute)},
		},
	}
	rec := httptest.NewRecorder()

	HandleAIWorkflowStatus(store).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/ai-workflow", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`"usage_continuity"`,
		`"scope":"job"`,
		`"scope_id":"job_1"`,
		`"scope":"workstream"`,
		`"scope_id":"ws_1"`,
		`"latest_event_id":"ctx_after"`,
		`"compaction_id":"compact_1"`,
		`"latest_run_state":"completed"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("status response missing %s: %s", want, body)
		}
	}
}

func TestHandleAIWorkflowContextBudgetCheckDisabledDoesNotSaveEvent(t *testing.T) {
	store := &stubAIWorkflowStore{}
	rec := httptest.NewRecorder()
	body := `{"event_id":"ctx_ok","agent":"Coder","context_tokens":5000,"created_at":"2026-05-18T12:00:00Z"}`

	HandleAIWorkflowContextBudgetCheck(store, domainai.ContextBudgetPolicy{}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/ai-workflow/context-budget/check", strings.NewReader(body)))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.contexts) != 1 {
		t.Fatalf("contexts=%#v", store.contexts)
	}
	if len(store.events) != 0 {
		t.Fatalf("events=%#v", store.events)
	}
	if !strings.Contains(rec.Body.String(), "context budget disabled") {
		t.Fatalf("missing disabled decision: %s", rec.Body.String())
	}
}

func TestHandleAIWorkflowContextBudgetCheckRejectsInvalidPayload(t *testing.T) {
	store := &stubAIWorkflowStore{}
	rec := httptest.NewRecorder()
	body := `{"event_id":"ctx_bad","agent":"Coder","context_tokens":-1}`

	HandleAIWorkflowContextBudgetCheck(store, domainai.ContextBudgetPolicy{MaxContextTokens: 1000}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/ai-workflow/context-budget/check", strings.NewReader(body)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.contexts) != 0 || len(store.events) != 0 {
		t.Fatalf("unexpected writes contexts=%#v events=%#v", store.contexts, store.events)
	}
}

func TestHandleAIWorkflowHeavyWorkerEvaluateSavesRequestedEvent(t *testing.T) {
	store := &stubAIWorkflowStore{}
	rec := httptest.NewRecorder()
	body := `{"event_id":"run_1","agent":"Coder","target_file_count":21,"reason":"large refactor"}`

	HandleAIWorkflowHeavyWorkerEvaluate(store, domainai.HeavyWorkerPolicy{
		Enabled:       true,
		RequireReason: true,
	}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/ai-workflow/heavy-worker/evaluate", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.events) != 1 || store.events[0].EventType != "heavy_worker_requested" || store.events[0].ParentEventID != "run_1" {
		t.Fatalf("events=%#v", store.events)
	}
	if !strings.Contains(rec.Body.String(), `"status":"requested"`) {
		t.Fatalf("missing requested decision: %s", rec.Body.String())
	}
}

func TestHandleAIWorkflowHeavyWorkerEvaluateBlockedDoesNotSaveEvent(t *testing.T) {
	store := &stubAIWorkflowStore{}
	rec := httptest.NewRecorder()
	body := `{"event_id":"run_1","agent":"Coder","user_requested_deep_dive":true}`

	HandleAIWorkflowHeavyWorkerEvaluate(store, domainai.HeavyWorkerPolicy{
		Enabled:       true,
		RequireReason: true,
	}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/ai-workflow/heavy-worker/evaluate", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.events) != 0 {
		t.Fatalf("events=%#v", store.events)
	}
	if !strings.Contains(rec.Body.String(), `"status":"blocked"`) {
		t.Fatalf("missing blocked decision: %s", rec.Body.String())
	}
}

func TestHandleAIWorkflowHeavyWorkerRuntimeDiagnosticsIncludesLiveHeavyState(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/status" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Fatalf("authorization=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"roles":{"Heavy":{"health_ok":true,"halted":false}},"memory":{"llm_by_role":{"Heavy":{"role":"Heavy","model":"/models/qwen-heavy","port":8083,"pid":46923,"rss_mib":49971.38}}}}`))
	}))
	defer upstream.Close()

	rec := httptest.NewRecorder()
	HandleAIWorkflowHeavyWorkerRuntimeDiagnostics(HeavyWorkerRuntimeDiagnosticsOptions{
		LocalLLMEnabled:  true,
		Provider:         "local_openai",
		EffectiveBaseURL: "http://127.0.0.1:8083/",
		EffectiveModel:   "Heavy",
		TimeoutSec:       120,
		LLMOpsConfigured: true,
		LLMOpsEnabled:    true,
		LLMOpsBaseURL:    upstream.URL,
		LLMOps:           LLMOpsProxyOptions{BaseURL: upstream.URL, Token: "tok"},
	}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/ai-workflow/heavy-worker/runtime-diagnostics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"role":"Heavy"`, `"route":"ANALYZE"`, `"route_prefix":"/analyze"`, `"live_available":true`, `"/models/qwen-heavy"`, `"pid":46923`, `"failure_is_error":true`} {
		if !strings.Contains(body, want) {
			t.Fatalf("response missing %s: %s", want, body)
		}
	}
}

func TestHandleAIWorkflowHeavyWorkerRuntimeDiagnosticsSurfacesTokenMissingWithoutFailing(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleAIWorkflowHeavyWorkerRuntimeDiagnostics(HeavyWorkerRuntimeDiagnosticsOptions{
		LocalLLMEnabled:  true,
		Provider:         "local_openai",
		EffectiveBaseURL: "http://127.0.0.1:8082/",
		EffectiveModel:   "Worker",
		LLMOpsConfigured: true,
		LLMOpsEnabled:    false,
		LLMOpsBaseURL:    "http://127.0.0.1:8079/",
	}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/ai-workflow/heavy-worker/runtime-diagnostics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"configured":true`, `"live_available":false`, `"error":"LLM_OPS_TOKEN missing"`, `"base_url":"http://127.0.0.1:8082"`, `"model":"Worker"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("response missing %s: %s", want, body)
		}
	}
}

func TestHandleAIWorkflowProjectInit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	store := &stubAIWorkflowStore{}
	scanner := aiworkflowapp.NewProjectScanner(store)
	rec := httptest.NewRecorder()
	body := `{"repo_root":"` + root + `","repo_name":"example"}`

	HandleAIWorkflowProjectInit(scanner, ".ai").ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/ai-workflow/project-init", strings.NewReader(body)))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.events) != 1 || store.events[0].EventType != "project_init_completed" {
		t.Fatalf("events=%#v", store.events)
	}
	if len(store.memories) != 6 {
		t.Fatalf("project memory indexes=%#v", store.memories)
	}
	if _, err := os.Stat(filepath.Join(root, ".ai", "project_profile.md")); err != nil {
		t.Fatalf("project profile was not generated: %v", err)
	}
}

func TestHandleAIWorkflowWorktreeCreateRequiresHumanApproval(t *testing.T) {
	store := &stubAIWorkflowStore{}
	manager := aiworkflowapp.NewWorktreeManager(store)
	rec := httptest.NewRecorder()
	body := `{"repo_root":".","branch":"feature/no-approval"}`

	HandleAIWorkflowWorktreeCreateRuntime(manager, "../worktrees").ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/ai-workflow/worktrees/create", strings.NewReader(body)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "human_approved=true") {
		t.Fatalf("expected human approval error, got %s", rec.Body.String())
	}
}

func TestHandleAIWorkflowWorktreeCloseRequiresHumanApproval(t *testing.T) {
	store := &stubAIWorkflowStore{}
	manager := aiworkflowapp.NewWorktreeManager(store)
	rec := httptest.NewRecorder()
	body := `{"repo_root":".","worktree_path":"../worktrees/example","branch":"feature/no-approval"}`

	HandleAIWorkflowWorktreeCloseRuntime(manager, "../worktrees").ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/ai-workflow/worktrees/close", strings.NewReader(body)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "human_approved=true") {
		t.Fatalf("expected human approval error, got %s", rec.Body.String())
	}
}
