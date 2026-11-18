package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentprofileapp "github.com/Nyukimin/RenCrow_CORE/internal/application/agentprofile"
	moviecatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/moviecatalog"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/taskexecution"
	tool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	persisttask "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

const taskCenterKind = "movie.catalog.lookup"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	runtimeDir := strings.TrimSpace(os.Getenv("RENCROW_E2E_RUNTIME"))
	if runtimeDir == "" || !filepath.IsAbs(runtimeDir) {
		return fmt.Errorf("RENCROW_E2E_RUNTIME must be an absolute path")
	}
	workstreamPath := filepath.Join(runtimeDir, "workspace", "logs", "workstream.db")
	movieCatalogPath := filepath.Join(runtimeDir, "workspace", "logs", "movie_catalog.sqlite")
	policyPath := filepath.Join(runtimeDir, "task-execution-policy.yaml")
	foreignGoalPath := filepath.Join(runtimeDir, "foreign-goal-id.txt")
	userID, err := configuredTaskCenterUser(filepath.Join(runtimeDir, "local-agent-ops.user-id"))
	if err != nil {
		return fmt.Errorf("read registered TaskCenter user from runtime config: %w", err)
	}

	if err := initializeMovieCatalog(movieCatalogPath); err != nil {
		return fmt.Errorf("initialize isolated MovieCatalog fixture: %w", err)
	}
	policyBytes, err := os.ReadFile(policyPath)
	if err != nil {
		return fmt.Errorf("read task execution policy: %w", err)
	}
	var policy taskexecution.Policy
	decoder := yaml.NewDecoder(strings.NewReader(string(policyBytes)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&policy); err != nil {
		return fmt.Errorf("decode task execution policy: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return fmt.Errorf("task execution policy must contain one YAML document")
		}
		return fmt.Errorf("decode trailing task execution policy document: %w", err)
	}

	profiles := agentprofileapp.NewStaticCatalog().List()
	actors := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		actors = append(actors, profile.ID)
	}
	store, err := workstream.NewSQLiteStoreWithVault(workstreamPath, filepath.Join(runtimeDir, "workspace", "vault"))
	if err != nil {
		return fmt.Errorf("open isolated Workstream SQLite store: %w", err)
	}
	defer store.Close()

	service, err := taskcenter.NewService(store, policy, actors, []taskcenter.Kind{
		{
			ID: taskCenterKind, Label: "Movie catalog lookup", InputSchema: moviecatalogapp.LookupInputSchema(),
			RequiresTypedInput: true, ExecutorAvailable: false, UnavailableReason: "fixture_lookup_executor_not_bound",
		},
	}, time.Now)
	if err != nil {
		return fmt.Errorf("create TaskCenter service for fixture: %w", err)
	}
	revision, err := policy.CanonicalHash()
	if err != nil {
		return fmt.Errorf("hash task execution policy: %w", err)
	}

	scope, err := tool.NewToolExecutionScope(
		"task-center-live-e2e-foreign-owner",
		tool.ActorKindUser,
		"foreign_task_center_user",
		"foreign_task_center_user",
		[]string{tool.DataScopePublic, tool.DataScopeUser},
		tool.AuthenticationSourceHTTP,
	)
	if err != nil {
		return fmt.Errorf("create isolated foreign-user scope: %w", err)
	}
	ctx := tool.WithToolExecutionScope(context.Background(), scope)
	result, err := service.Submit(ctx, taskcenter.SubmitInput{
		DataScope:   "user",
		KindID:      taskCenterKind,
		KindInput:   json.RawMessage(`{"kind":"movie","name":"Task Center E2E Film"}`),
		Title:       "FOREIGN_SCOPE_PRIVATE_SENTINEL",
		Description: "Private owner fixture used to verify that another user cannot read this Goal.",
		SuccessCriteria: []string{
			"The owner fixture is stored under the foreign test user scope.",
		},
		Verification: []string{
			"Read the Goal through the authenticated task-center API and confirm it is hidden.",
		},
		Assignee: "unassigned",
		Timing: modulecore.TaskTiming{
			Timezone:       "Asia/Tokyo",
			AllowedWindows: []modulecore.TaskTimeWindow{},
		},
		IdempotencyKey:         "task-center-live-e2e-foreign-private",
		ExpectedPolicyRevision: revision,
	})
	if err != nil {
		return fmt.Errorf("seed private Goal through TaskCenter service: %w", err)
	}
	if result.Item.DataScope != "user" || result.Item.GoalID == "" {
		return fmt.Errorf("foreign Goal fixture returned unexpected scope or empty ID")
	}
	if err := os.WriteFile(foreignGoalPath, []byte(result.Item.GoalID), 0600); err != nil {
		return fmt.Errorf("write private Goal ID fixture: %w", err)
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("close isolated Workstream before native result fixture: %w", err)
	}
	for _, browser := range []string{"chromium", "firefox"} {
		fixturePath := filepath.Join(runtimeDir, "pending-result-goal-id-"+browser+".txt")
		if err := seedPendingResult(ctx, runtimeDir, workstreamPath, movieCatalogPath, userID, browser, policy, revision, fixturePath); err != nil {
			return err
		}
	}
	return nil
}

func configuredTaskCenterUser(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	userID := strings.TrimSpace(string(contents))
	if userID == "" || strings.ContainsAny(userID, "\r\n") || userID != string(contents) {
		return "", fmt.Errorf("runtime/local-agent-ops.user-id must contain one exact non-empty user ID")
	}
	return userID, nil
}

func taskStorePath(runtimeDir string) string {
	return filepath.Join(runtimeDir, "workspace", "tasks")
}

func seedPendingResult(ctx context.Context, runtimeDir, workstreamPath, movieCatalogPath, userID, browser string, policy taskexecution.Policy, policyRevision, fixturePath string) error {
	goalStore, err := workstream.NewSQLiteStoreWithVault(workstreamPath, filepath.Join(runtimeDir, "workspace", "vault"))
	if err != nil {
		return fmt.Errorf("reopen isolated Workstream for native result fixture: %w", err)
	}
	taskStore, err := persisttask.NewJSONLStore(taskStorePath(runtimeDir))
	if err != nil {
		_ = goalStore.Close()
		return fmt.Errorf("open isolated native Task store: %w", err)
	}
	manager := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
	service, err := taskcenter.NewService(goalStore, policy, []string{"mio"}, []taskcenter.Kind{{
		ID: taskCenterKind, Label: "Movie catalog lookup", InputSchema: moviecatalogapp.LookupInputSchema(),
		RequiresTypedInput: true, ExecutorAvailable: true,
	}}, time.Now)
	if err != nil {
		_ = taskStore.Close()
		_ = goalStore.Close()
		return fmt.Errorf("create native TaskCenter fixture service: %w", err)
	}
	lookup := isolatedMovieLookup{path: movieCatalogPath}
	service.WithNativeExecution(manager, goalStore, lookup)

	userScope, err := tool.NewToolExecutionScope(
		"task-center-live-e2e-result-create-"+browser, tool.ActorKindUser, userID, userID,
		[]string{tool.DataScopePublic, tool.DataScopeUser}, tool.AuthenticationSourceHTTP,
	)
	if err != nil {
		_ = taskStore.Close()
		_ = goalStore.Close()
		return fmt.Errorf("create native result creator scope: %w", err)
	}
	created, err := service.Submit(tool.WithToolExecutionScope(ctx, userScope), taskcenter.SubmitInput{
		DataScope: "public", KindID: taskCenterKind,
		KindInput: json.RawMessage(`{"kind":"movie","name":"Task Center E2E Film","information":"overview","limit":1}`),
		Title:     "Task Center E2E catalog result " + browser, Description: "Read the seeded local MovieCatalog record.",
		SuccessCriteria: []string{"Inspect the canonical local catalog result."},
		Verification:    []string{"Confirm that the returned result identifies Task Center E2E Film."},
		Assignee:        "unassigned", Timing: modulecore.TaskTiming{},
		IdempotencyKey: "task-center-live-e2e-pending-result-" + browser, ExpectedPolicyRevision: policyRevision,
	})
	if err != nil {
		_ = taskStore.Close()
		_ = goalStore.Close()
		return fmt.Errorf("submit native result Goal: %w", err)
	}
	agentScope, err := tool.NewToolExecutionScope(
		"task-center-live-e2e-result-execute-"+browser, tool.ActorKindAgent, "mio", "",
		[]string{tool.DataScopePublic}, tool.AuthenticationSourceAgentOrchestrator,
	)
	if err != nil {
		_ = taskStore.Close()
		_ = goalStore.Close()
		return fmt.Errorf("create native result executor scope: %w", err)
	}
	claim, err := service.Claim(tool.WithToolExecutionScope(ctx, agentScope), taskcenter.ClaimInput{
		GoalID: created.Item.GoalID, ExpectedRevision: created.Item.Revision,
		ExpectedPolicyRevision: policyRevision, IdempotencyKey: "task-center-live-e2e-claim-pending-result-" + browser,
	})
	if err != nil {
		_ = taskStore.Close()
		_ = goalStore.Close()
		return fmt.Errorf("claim native result Goal: %w", err)
	}
	callerTask, callerRun, err := manager.CreateAndStartRun(ctx, domaintask.Task{
		Title: "TaskCenter E2E native invocation parent " + browser, Route: domaintask.RouteGeneral, Assignee: "mio",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		_ = taskStore.Close()
		_ = goalStore.Close()
		return fmt.Errorf("create real native invocation parent Task: %w", err)
	}
	executionCtx, err := domainexecution.WithIdentity(tool.WithToolExecutionScope(ctx, agentScope), callerTask.TaskID, callerRun.RunID, callerRun.TraceID)
	if err != nil {
		abortErr := manager.AbortStartedRun(ctx, callerTask.TaskID, callerRun.RunID, "mio", err)
		_ = taskStore.Close()
		_ = goalStore.Close()
		return fmt.Errorf("bind real native invocation parent identity: %w", errors.Join(err, abortErr))
	}
	_, err = service.ExecuteTaskCenterExecution(executionCtx, taskcenter.ExecutionInput{
		GoalID: claim.Item.GoalID, ExpectedRevision: claim.Item.Revision,
		ExpectedAssignmentEpoch: claim.Item.AssignmentEpoch, ExpectedPolicyRevision: policyRevision,
		IdempotencyKey: "task-center-live-e2e-execute-pending-result-" + browser,
		CallerTaskID:   callerTask.TaskID, CallerRunID: callerRun.RunID,
	})
	if err != nil {
		closedErr := manager.CloseStartedRun(ctx, callerTask.TaskID, callerRun.RunID, "mio", taskmanager.RunOutcomeOf(err, ""), err)
		_ = taskStore.Close()
		_ = goalStore.Close()
		return fmt.Errorf("execute native MovieCatalog result: %w", closedErr)
	}
	if err := manager.CloseStartedRun(ctx, callerTask.TaskID, callerRun.RunID, "mio", taskmanager.RunOutcomeOf(nil, "TaskCenter E2E result fixture completed"), nil); err != nil {
		_ = taskStore.Close()
		_ = goalStore.Close()
		return fmt.Errorf("close real native invocation parent Run: %w", err)
	}
	if err := taskStore.Close(); err != nil {
		_ = goalStore.Close()
		return fmt.Errorf("close native Task store before CORE startup: %w", err)
	}
	if err := goalStore.Close(); err != nil {
		return fmt.Errorf("close native Workstream before CORE startup: %w", err)
	}
	// Reopen the real owners before CORE startup: the new Task writer
	// generation must still read the exact historical successful Run/result
	// receipt without a connected MovieCatalog executor.
	reopenedGoalStore, err := workstream.NewSQLiteStoreWithVault(workstreamPath, filepath.Join(runtimeDir, "workspace", "vault"))
	if err != nil {
		return fmt.Errorf("reopen Workstream for historical result proof: %w", err)
	}
	reopenedTaskStore, err := persisttask.NewJSONLStore(taskStorePath(runtimeDir))
	if err != nil {
		_ = reopenedGoalStore.Close()
		return fmt.Errorf("reopen Task store for historical result proof: %w", err)
	}
	reopenedService, err := taskcenter.NewService(reopenedGoalStore, policy, []string{"mio"}, []taskcenter.Kind{{
		ID: taskCenterKind, Label: "Movie catalog lookup", InputSchema: moviecatalogapp.LookupInputSchema(),
		RequiresTypedInput: true, ExecutorAvailable: false, UnavailableReason: "movie_catalog_lookup_owner_unavailable",
	}}, time.Now)
	if err != nil {
		_ = reopenedTaskStore.Close()
		_ = reopenedGoalStore.Close()
		return fmt.Errorf("create historical result reader: %w", err)
	}
	reopenedService.WithNativeExecution(taskmanager.New(reopenedTaskStore, taskmanager.DefaultParallelLimits()), reopenedGoalStore, nil)
	view, err := reopenedService.ReadResult(tool.WithToolExecutionScope(ctx, userScope), claim.Item.GoalID)
	if err != nil || view.ExecutionVerification != "verification_pending" || view.ResultSHA256 == "" {
		_ = reopenedTaskStore.Close()
		_ = reopenedGoalStore.Close()
		return fmt.Errorf("read historical result after TaskStore reopen with executor disabled: %w", err)
	}
	if err := reopenedTaskStore.Close(); err != nil {
		_ = reopenedGoalStore.Close()
		return fmt.Errorf("close reopened Task store: %w", err)
	}
	if err := reopenedGoalStore.Close(); err != nil {
		return fmt.Errorf("close reopened Workstream: %w", err)
	}
	if err := os.WriteFile(fixturePath, []byte(claim.Item.GoalID), 0600); err != nil {
		return fmt.Errorf("write pending result Goal ID fixture: %w", err)
	}
	return nil
}

type isolatedMovieLookup struct{ path string }

func (lookup isolatedMovieLookup) Lookup(_ context.Context, kind, name, information string, limit int) (any, error) {
	db, err := sql.Open("sqlite", lookup.path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return moviecatalogapp.Lookup(db, moviecatalogapp.LookupRequest{Kind: kind, Name: name, Information: information, Limit: limit})
}

func initializeMovieCatalog(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	statements := []string{
		"CREATE TABLE IF NOT EXISTS movies (movie_id TEXT PRIMARY KEY, title TEXT NOT NULL, title_lookup_key TEXT NOT NULL DEFAULT '', url TEXT NOT NULL, synopsis TEXT, fetched_at TEXT DEFAULT CURRENT_TIMESTAMP)",
		"CREATE TABLE IF NOT EXISTS people (person_id TEXT PRIMARY KEY, name TEXT NOT NULL, name_lookup_key TEXT NOT NULL DEFAULT '', url TEXT NOT NULL, profile_json TEXT, biography TEXT, fetched_at TEXT DEFAULT CURRENT_TIMESTAMP)",
		"CREATE TABLE IF NOT EXISTS movie_people (movie_id TEXT NOT NULL, person_id TEXT NOT NULL, role TEXT NOT NULL, source TEXT NOT NULL, movie_title TEXT, person_name TEXT, movie_url TEXT, person_url TEXT, PRIMARY KEY(movie_id, person_id, role, source))",
		"INSERT INTO movies (movie_id, title, title_lookup_key, url, synopsis) VALUES ('task-center-e2e-film', 'Task Center E2E Film', 'task center e2e film', 'https://example.invalid/task-center-e2e-film', 'Isolated local MovieCatalog fixture') ON CONFLICT(movie_id) DO NOTHING",
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}
	return moviecatalogapp.EnsureIndexedLookupSchema(context.Background(), db)
}
