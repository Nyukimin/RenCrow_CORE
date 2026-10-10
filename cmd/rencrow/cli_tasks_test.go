package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestParseTasksCreateArgsKeepsCanonicalRelationshipsAndOrigin(t *testing.T) {
	parent := modulecore.NewTaskID()
	dependencyOne := modulecore.NewTaskID()
	dependencyTwo := modulecore.NewTaskID()
	sessionID := modulecore.NewSessionID()
	threadID := modulecore.NewThreadID()
	turnID := modulecore.NewTurnID()
	messageID := modulecore.NewMessageID()
	workstreamID := modulecore.NewWorkstreamID()
	goalID := modulecore.NewGoalID()
	superseded := modulecore.NewTaskID()

	draft, _, _, err := parseTasksCreateArgs([]string{
		"--title", "canonical Task",
		"--parent-task-id", string(parent),
		"--dependency-task-id", string(dependencyOne),
		"--dependency-task-id", string(dependencyTwo),
		"--origin-session-id", string(sessionID),
		"--origin-thread-id", string(threadID),
		"--origin-turn-id", string(turnID),
		"--origin-message-id", string(messageID),
		"--workstream-id", string(workstreamID),
		"--goal-id", string(goalID),
		"--supersedes-task-id", string(superseded),
	})
	if err != nil {
		t.Fatal(err)
	}
	if draft.ParentTaskID != parent || len(draft.DependencyTaskIDs) != 2 || draft.DependencyTaskIDs[0] != dependencyOne || draft.DependencyTaskIDs[1] != dependencyTwo {
		t.Fatalf("relationships = %#v", draft)
	}
	if draft.OriginSessionID != sessionID || draft.OriginThreadID != threadID || draft.OriginTurnID != turnID || draft.OriginMessageID != messageID || draft.WorkstreamID != workstreamID || draft.GoalID != goalID || draft.SupersedesTaskID != superseded {
		t.Fatalf("origin and ownership IDs = %#v", draft)
	}
}

func TestParseTasksCreateArgsRejectsMalformedCanonicalOption(t *testing.T) {
	if _, _, _, err := parseTasksCreateArgs([]string{"--title", "bad", "--parent-task-id", "job_old"}); err == nil {
		t.Fatal("legacy parent identity was accepted")
	}
	if _, _, _, err := parseTasksCreateArgs([]string{"--title", "bad", "--dependency-task-id"}); err == nil {
		t.Fatal("missing dependency value was accepted")
	}
}

func TestRunTasksCommandAcceptsCompactForCreate(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close task store: %v", err)
		}
	})
	manager := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	var stdout, stderr bytes.Buffer
	code := runTasksCommand([]string{"create", "--title", "compact Task", "--json", "--compact"}, manager, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if strings.Count(stdout.String(), "\n") != 1 || !strings.Contains(stdout.String(), `"task_id":"tsk_`) {
		t.Fatalf("compact output=%q", stdout.String())
	}
}

func TestLoadTaskManagerKeepsReadCommandsAvailableDuringWriterLease(t *testing.T) {
	root := t.TempDir()
	writer, err := taskpersistence.NewJSONLStore(defaultTaskStorePath(root))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	data, err := json.Marshal(map[string]any{"workspace_dir": root, "server": map[string]any{"port": 8080}})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"LIST"}, {"--compact", "show"}, {"notifications"}} {
		reader, err := loadTaskManager(configPath, args)
		if err != nil {
			t.Fatalf("read command %v blocked by writer: %v", args, err)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := loadTaskManager(configPath, []string{"create"})
	if manager != nil {
		_ = manager.Close()
	}
	if !errors.Is(err, taskpersistence.ErrTaskWriterBusy) {
		t.Fatalf("write command bypassed writer lease: %v", err)
	}
}

func TestLoadTaskManagerFreezesEnabledNativeCriteriaPinAtTaskCreation(t *testing.T) {
	root := t.TempDir()
	pinned := strings.Repeat("a", 64)
	configPath := writeTaskManagerHarnessConfigAtRoot(t, root, true, pinned)
	manager, err := loadTaskManager(configPath, []string{"create"})
	if err != nil {
		t.Fatalf("load enabled native task manager: %v", err)
	}
	defer manager.Close()

	task, err := manager.Create(t.Context(), domaintask.Task{Title: "CLI native-capable root", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	if task.ExpectedCriteriaRevision != pinned {
		t.Fatalf("CLI-created Task pin = %q, want trusted profile pin", task.ExpectedCriteriaRevision)
	}
}

func TestLoadTaskManagerDoesNotBackfillLegacyTaskPin(t *testing.T) {
	root := t.TempDir()
	pinned := strings.Repeat("a", 64)
	legacyConfig := writeTaskManagerHarnessConfigAtRoot(t, root, false, pinned)
	legacyManager, err := loadTaskManager(legacyConfig, []string{"create"})
	if err != nil {
		t.Fatalf("load disabled-profile task manager: %v", err)
	}
	legacy, err := legacyManager.Create(t.Context(), domaintask.Task{Title: "legacy CLI root", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		_ = legacyManager.Close()
		t.Fatalf("create legacy Task: %v", err)
	}
	if err := legacyManager.Close(); err != nil {
		t.Fatal(err)
	}
	if legacy.ExpectedCriteriaRevision != "" {
		t.Fatalf("disabled profile seeded a Task pin: %q", legacy.ExpectedCriteriaRevision)
	}

	enabledConfig := writeTaskManagerHarnessConfigAtRoot(t, root, true, pinned)
	manager, err := loadTaskManager(enabledConfig, []string{"start"})
	if err != nil {
		t.Fatalf("load enabled native task manager for legacy Task: %v", err)
	}
	defer manager.Close()
	if _, err := manager.Start(t.Context(), legacy.TaskID); err != nil {
		t.Fatalf("start legacy Task: %v", err)
	}
	stored, err := manager.Get(t.Context(), legacy.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ExpectedCriteriaRevision != "" {
		t.Fatalf("loading/updating a legacy Task backfilled pin %q", stored.ExpectedCriteriaRevision)
	}
}

func writeTaskManagerHarnessConfigAtRoot(t *testing.T, root string, enabled bool, criteriaRevision string) string {
	t.Helper()
	workspace := filepath.Join(root, "workspace")
	private := filepath.Join(root, "private")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(private, 0o700); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(private, "origin.key")
	if err := os.WriteFile(keyPath, []byte(strings.Repeat("0", 64)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	harnessConfigPath := filepath.Join(private, "harness.json")
	harnessConfig := map[string]any{
		"config_version": "rencrow-harness-config/v1",
		"caller": map[string]any{
			"principal": "core:local", "default_origin": "automation",
			"readable_session_owners": []string{"core:local"}, "controllable_session_owners": []string{"core:local"},
			"relay_issuers": []any{map[string]any{"issuer": "core:test-issuer", "key_id": "test-key-1", "audience": "core:local", "key_file": "/not/read/by/core"}},
		},
	}
	data, err := json.Marshal(harnessConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(harnessConfigPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(private, "rencrow-harness")
	if os.PathSeparator == '\\' {
		binaryPath += ".exe"
	}
	if err := os.WriteFile(binaryPath, []byte("test fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(binaryPath, 0o700); err != nil {
		t.Fatal(err)
	}

	profile := map[string]any{
		"enabled": enabled, "harness_binary": binaryPath, "expected_build_revision": "0123456789abcdef0123456789abcdef01234567",
		"expected_criteria_revision": criteriaRevision,
		"workspace_ref":              map[string]any{"path": workspace, "policy_ref": "workspace-write", "execution_mode": "trusted_host"},
		"binding_ref":                map[string]any{"kind": "alias", "selector": "shiro-worker-exec", "profile_revision": "rev-1", "execution_role": "worker"},
		"limits":                     map[string]any{"max_model_steps": 10, "max_tool_calls_per_step": 8, "deadline_seconds": 1800, "max_capture_bytes": 67108864, "max_generation_attempts": 32},
	}
	rootConfig := map[string]any{
		"workspace_dir": workspace,
		"server":        map[string]any{"port": 18790},
		"native_harness": map[string]any{
			"harness_config": harnessConfigPath,
			"issuer":         map[string]any{"issuer": "core:test-issuer", "key_id": "test-key-1", "audience": "core:local", "key_file": keyPath, "ttl_seconds": 300},
			"profile":        profile,
		},
	}
	data, err = json.Marshal(rootConfig)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadConfig(configPath); err != nil {
		t.Fatalf("test native Harness config is invalid: %v", err)
	}
	return configPath
}
