package action

import (
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestActionValidateAndClose(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	action := Action{
		ActionID:  modulecore.NewActionID(),
		TaskID:    modulecore.NewTaskID(),
		RunID:     modulecore.NewRunID(),
		Kind:      KindTool,
		Name:      "browser.click",
		Status:    StatusOpen,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := action.Validate(); err != nil {
		t.Fatalf("validate open action: %v", err)
	}
	closed, err := action.Close(StatusSucceeded, now.Add(time.Second), "ok")
	if err != nil {
		t.Fatalf("close action: %v", err)
	}
	if closed.Status != StatusSucceeded || closed.Summary != "ok" || closed.CompletedAt == nil {
		t.Fatalf("unexpected closed action: %+v", closed)
	}
	if _, err := closed.Close(StatusFailed, now.Add(2*time.Second), "again"); err == nil {
		t.Fatal("expected already-closed error")
	}
}

func TestAttemptRetryKeepsActionIdentityContract(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	actionID := modulecore.NewActionID()
	first := Attempt{
		AttemptID:   modulecore.NewAttemptID(),
		ActionID:    actionID,
		StartReason: AttemptStartReasonFirst,
		Status:      AttemptStatusRunning,
		StartedAt:   now,
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	closed, err := first.Close(AttemptStatusFailed, now.Add(time.Second), "timeout")
	if err != nil {
		t.Fatalf("close first: %v", err)
	}
	retry := Attempt{
		AttemptID:   modulecore.NewAttemptID(),
		ActionID:    actionID,
		StartReason: AttemptStartReasonRetry,
		Status:      AttemptStatusRunning,
		StartedAt:   now.Add(2 * time.Second),
	}
	if err := retry.Validate(); err != nil {
		t.Fatalf("retry attempt: %v", err)
	}
	if retry.ActionID != closed.ActionID {
		t.Fatalf("retry must preserve ActionID: %s vs %s", retry.ActionID, closed.ActionID)
	}
	if retry.AttemptID == closed.AttemptID {
		t.Fatal("retry must issue a new AttemptID")
	}
}
