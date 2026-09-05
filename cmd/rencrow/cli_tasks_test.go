package main

import (
	"testing"

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
