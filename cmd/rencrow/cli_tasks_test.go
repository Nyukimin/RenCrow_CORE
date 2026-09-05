package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
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
