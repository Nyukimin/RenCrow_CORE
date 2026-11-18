package task

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestNativePlanningInvocationValidatesBoundActorAndReadonlyInheritance(t *testing.T) {
	now := time.Now().UTC()
	sum := sha256.Sum256([]byte("{}"))
	parent, parentRun := modulecore.NewTaskID(), modulecore.NewRunID()
	draft := Task{
		TaskID: modulecore.NewTaskID(), Title: "planning", Route: RoutePLAN,
		ParentTaskID: parent, Assignee: "aka", Status: StatusRunning, Priority: PriorityNormal,
		InterruptPolicy: InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now,
		NativePlanningInvocation: &NativePlanningInvocation{
			ParentTaskID: parent, ParentRunID: parentRun, ParentActorID: "mio", ActorID: "aka", Purpose: "task_center_planning",
			RequestKey: modulecore.NewRequestID(), EnvelopeSHA256: hex.EncodeToString(sum[:]), ParentReadOnly: true, DataScope: "public",
		},
	}
	if err := draft.Validate(); err == nil {
		t.Fatal("mutable planning child relaxed readonly parent")
	}
	draft.ReadOnly = true
	if err := draft.Validate(); err != nil {
		t.Fatalf("inherited readonly planning task invalid: %v", err)
	}
	draft.Assignee = "shiro"
	if err := draft.Validate(); err == nil {
		t.Fatal("planning task accepted an actor different from its immutable invocation")
	}
}

func TestLegacyTaskJSONOmitsNativePlanningInvocation(t *testing.T) {
	legacy := Task{TaskID: "tsk_legacy", Title: "legacy", Route: RouteGeneral}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("native_planning_invocation")) {
		t.Fatalf("legacy JSON unexpectedly gained planning marker: %s", encoded)
	}
}
