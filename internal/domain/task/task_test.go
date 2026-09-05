package task

import (
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestTaskDefaultsAndCanonicalIdentity(t *testing.T) {
	now := time.Date(2026, 9, 5, 1, 2, 3, 0, time.UTC)
	value := Task{Title: "write spec"}
	value.ApplyDefaults(now)
	if err := value.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if value.TaskID.Validate() != nil || value.Status != StatusQueued || value.Priority != PriorityNormal || value.Route != RouteGeneral || !value.UpdatedAt.Equal(now) {
		t.Fatalf("defaults = %#v", value)
	}
}

func TestTaskWaitingRequiresMachineReasonAndHasNoHumanStatus(t *testing.T) {
	value := validTask()
	value.Status = StatusWaiting
	if err := value.Validate(); err == nil {
		t.Fatal("waiting without reason was accepted")
	}
	value.WaitingReason = "dependency task is still running"
	if err := value.Validate(); err != nil {
		t.Fatalf("waiting with reason: %v", err)
	}
	if ValidStatus(Status("waiting_user")) {
		t.Fatal("waiting_user remains a valid Task status")
	}
}

func TestTaskIdentityAndRelationshipValidation(t *testing.T) {
	value := validTask()
	value.ParentTaskID = value.TaskID
	if err := value.Validate(); err == nil {
		t.Fatal("self parent was accepted")
	}
	value = validTask()
	value.DependencyTaskIDs = []modulecore.TaskID{value.TaskID}
	if err := value.Validate(); err == nil {
		t.Fatal("self dependency was accepted")
	}
	value = validTask()
	value.DependencyTaskIDs = []modulecore.TaskID{modulecore.NewTaskID(), modulecore.NewTaskID()}
	if err := value.Validate(); err != nil {
		t.Fatalf("valid dependencies: %v", err)
	}
}

func TestTaskRejectsUnknownEnumsAndRegressingTimestamp(t *testing.T) {
	for name, mutate := range map[string]func(*Task){
		"route":            func(value *Task) { value.Route = Route("DIRECT") },
		"interrupt policy": func(value *Task) { value.InterruptPolicy = InterruptPolicy("ask_user") },
		"updated at":       func(value *Task) { value.UpdatedAt = value.CreatedAt.Add(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			value := validTask()
			mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("invalid Task was accepted")
			}
		})
	}
}

func TestTaskTransitionsAndNotification(t *testing.T) {
	if !CanTransition(StatusQueued, StatusRunning) || !CanTransition(StatusRunning, StatusWaiting) || !CanTransition(StatusWaiting, StatusQueued) {
		t.Fatal("expected lifecycle transitions are unavailable")
	}
	if CanTransition(StatusSucceeded, StatusRunning) || CanTransition(StatusQueued, StatusSucceeded) {
		t.Fatal("invalid lifecycle transition accepted")
	}
	value := validTask()
	value.Status = StatusSucceeded
	notification := NewNotification(value, value.UpdatedAt)
	if notification.Type != "task.notification" || notification.TaskID != value.TaskID || notification.Level != NotificationDone || !notification.Interrupt {
		t.Fatalf("notification = %#v", notification)
	}
	value.InterruptPolicy = InterruptSilent
	if ShouldNotify(value) {
		t.Fatal("silent Task should not notify")
	}
}

func validTask() Task {
	now := time.Date(2026, 9, 5, 1, 2, 3, 0, time.UTC)
	value := Task{TaskID: modulecore.NewTaskID(), Title: "task", Status: StatusQueued, Priority: PriorityNormal, Route: RouteCode, InterruptPolicy: InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
	return value
}
