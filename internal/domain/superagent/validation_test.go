package superagent

import (
	"strings"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func validTaskID() modulecore.TaskID { return modulecore.NewTaskID() }

func validRunID() modulecore.RunID { return modulecore.NewRunID() }

func TestValidateSubagentTaskRequiresScopeAndTermination(t *testing.T) {
	err := ValidateSubagentTask(SubagentTask{
		TaskID:  validTaskID(),
		RunID:   validRunID(),
		ActorID: "shiro",
		Task:    "調査",
		Status:  "pending",
	})
	if err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("expected scope error, got %v", err)
	}
}

func TestValidateSuperAgentAcceptsCompleteRecords(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 0, 0, 0, time.UTC)
	taskID, runID := validTaskID(), validRunID()
	if err := ValidateAgentRun(AgentRun{
		RunID:       runID,
		TaskID:      taskID,
		ActorID:     "mio",
		Status:      "completed",
		StartedAt:   now,
		CompletedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("agent run should validate: %v", err)
	}
	if err := ValidateSubagentTask(SubagentTask{
		TaskID:               taskID,
		RunID:                runID,
		ActorID:              "shiro",
		Task:                 "調査",
		Scope:                []string{"docs/"},
		TerminationCondition: "report",
		Status:               "pending",
		CreatedAt:            now,
	}); err != nil {
		t.Fatalf("subagent task should validate: %v", err)
	}
	if err := ValidateContextPack(ContextPack{
		ContextPackID: "ctx_1",
		TaskID:        taskID,
		RunID:         runID,
		Summary:       "summary",
		TokenEstimate: 3000,
		CreatedAt:     now,
	}, 3000); err != nil {
		t.Fatalf("context pack should validate: %v", err)
	}
	if err := ValidateMessageChannel(MessageChannel{
		ChannelID:   "chan_1",
		ChannelType: "superagent",
		Status:      "active",
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("message channel should validate: %v", err)
	}
	if err := ValidateRunQueueItem(RunQueueItem{
		QueueID:        "queue_1",
		TaskID:         taskID,
		RunID:          runID,
		RunStartReason: domaintask.RunStartReasonCheckpointResume,
		Goal:           "resume run",
		Action:         "resume",
		Status:         "completed",
		CreatedAt:      now,
		CompletedAt:    now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("run queue item should validate: %v", err)
	}
}

func TestValidateAgentRunRejectsLegacyProjectionRunID(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 0, 0, 0, time.UTC)
	err := ValidateAgentRun(AgentRun{
		RunID:     "run_legacy",
		TaskID:    validTaskID(),
		ActorID:   "mio",
		Status:    "running",
		StartedAt: now,
	})
	if err == nil || !strings.Contains(err.Error(), "run_id") {
		t.Fatalf("expected malformed canonical run_id rejection, got %v", err)
	}
}

func TestValidateContextPackRejectsLegacyProjectionRunID(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 0, 0, 0, time.UTC)
	err := ValidateContextPack(ContextPack{
		ContextPackID: "ctx_legacy",
		TaskID:        validTaskID(),
		RunID:         "run_legacy",
		Summary:       "summary",
		CreatedAt:     now,
	}, 0)
	if err == nil || !strings.Contains(err.Error(), "run_id") {
		t.Fatalf("expected malformed canonical run_id rejection, got %v", err)
	}
}

func TestValidateSubagentTaskRejectsLegacyOrMalformedCanonicalIDs(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		taskID modulecore.TaskID
		runID  modulecore.RunID
		want   string
	}{
		{name: "legacy subagent id", taskID: "sub_legacy", runID: validRunID(), want: "task_id"},
		{name: "malformed task id", taskID: "task_legacy", runID: validRunID(), want: "task_id"},
		{name: "legacy parent run id", taskID: validTaskID(), runID: "run_legacy", want: "run_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSubagentTask(SubagentTask{
				TaskID:               tc.taskID,
				RunID:                tc.runID,
				ActorID:              "shiro",
				Task:                 "調査",
				Scope:                []string{"docs/"},
				TerminationCondition: "report",
				Status:               "pending",
				CreatedAt:            now,
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %s rejection, got %v", tc.want, err)
			}
		})
	}
}

func TestValidateSubagentTaskRejectsMechanismActors(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 0, 0, 0, time.UTC)
	for _, actorID := range []string{"worker", "coder", "subagent"} {
		t.Run(actorID, func(t *testing.T) {
			err := ValidateSubagentTask(SubagentTask{
				TaskID:               validTaskID(),
				RunID:                validRunID(),
				ActorID:              actorID,
				Task:                 "調査",
				Scope:                []string{"docs/"},
				TerminationCondition: "report",
				Status:               "pending",
				CreatedAt:            now,
			})
			if err == nil || !strings.Contains(err.Error(), "actor_id") {
				t.Fatalf("expected actor_id rejection for %q, got %v", actorID, err)
			}
		})
	}
}

func TestValidateAgentRunRequiresExactActorID(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 0, 0, 0, time.UTC)
	for _, actorID := range []string{"", " mio", "mio ", "MIO", "LeadAgent", "worker", "provider", "model"} {
		t.Run(actorID, func(t *testing.T) {
			err := ValidateAgentRun(AgentRun{
				RunID: validRunID(), TaskID: validTaskID(), ActorID: actorID, Status: "running", StartedAt: now,
			})
			if err == nil || !strings.Contains(err.Error(), "actor_id") {
				t.Fatalf("expected exact actor_id rejection for %q, got %v", actorID, err)
			}
		})
	}
}

func TestValidateAgentRunUsesClosedStatusAndCompletionTimestampContract(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 0, 0, 0, time.UTC)
	valid := func(status string) AgentRun {
		run := AgentRun{RunID: validRunID(), TaskID: validTaskID(), ActorID: "mio", Status: status, StartedAt: now}
		if status != "running" {
			run.CompletedAt = now.Add(time.Minute)
		}
		return run
	}
	for _, status := range []string{"running", "paused", "completed", "failed", "cancelled", "blocked", "interrupted", "reassigned", "superseded"} {
		t.Run("accept "+status, func(t *testing.T) {
			if err := ValidateAgentRun(valid(status)); err != nil {
				t.Fatalf("status %q should validate: %v", status, err)
			}
		})
	}
	for _, status := range []string{"queued", "done", "unknown", " RUNNING"} {
		t.Run("reject "+status, func(t *testing.T) {
			if err := ValidateAgentRun(valid(status)); err == nil || !strings.Contains(err.Error(), "status") {
				t.Fatalf("status %q should be rejected, got %v", status, err)
			}
		})
	}
	runningWithCompletion := valid("running")
	runningWithCompletion.CompletedAt = now.Add(time.Minute)
	if err := ValidateAgentRun(runningWithCompletion); err == nil || !strings.Contains(err.Error(), "completed_at") {
		t.Fatalf("running run with completed_at should be rejected, got %v", err)
	}
	for _, status := range []string{"paused", "completed", "failed", "cancelled", "blocked", "interrupted", "reassigned", "superseded"} {
		terminalWithoutCompletion := valid(status)
		terminalWithoutCompletion.CompletedAt = time.Time{}
		if err := ValidateAgentRun(terminalWithoutCompletion); err == nil || !strings.Contains(err.Error(), "completed_at") {
			t.Fatalf("terminal status %q without completed_at should be rejected, got %v", status, err)
		}
	}
}

func TestValidateSubagentTaskRequiresCompletedAtForTerminalStatuses(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 0, 0, 0, time.UTC)
	for _, status := range []string{"completed", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			err := ValidateSubagentTask(SubagentTask{
				TaskID:               validTaskID(),
				RunID:                validRunID(),
				ActorID:              "shiro",
				Task:                 "調査",
				Scope:                []string{"docs/"},
				TerminationCondition: "report",
				Status:               status,
				CreatedAt:            now,
			})
			if err == nil || !strings.Contains(err.Error(), "completed_at") {
				t.Fatalf("expected completed_at rejection for %q, got %v", status, err)
			}
		})
	}

	if err := ValidateSubagentTask(SubagentTask{
		TaskID:               validTaskID(),
		RunID:                validRunID(),
		ActorID:              "shiro",
		Task:                 "調査",
		Scope:                []string{"docs/"},
		TerminationCondition: "report",
		Status:               "completed",
		CreatedAt:            now,
		CompletedAt:          now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("terminal subagent task with completed_at should validate: %v", err)
	}
}

func TestValidateProjectionsRejectInvalidTaskID(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "agent run",
			err: ValidateAgentRun(AgentRun{
				RunID: validRunID(), TaskID: "task_legacy", ActorID: "mio", Status: "running", StartedAt: now,
			}),
		},
		{
			name: "context pack",
			err: ValidateContextPack(ContextPack{
				ContextPackID: "ctx_legacy", TaskID: "task_legacy", RunID: validRunID(), Summary: "summary", CreatedAt: now,
			}, 0),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil || !strings.Contains(tc.err.Error(), "task_id") {
				t.Fatalf("expected malformed canonical task_id rejection, got %v", tc.err)
			}
		})
	}
}

func TestValidateContextPackRespectsMaxTokens(t *testing.T) {
	err := ValidateContextPack(ContextPack{
		ContextPackID: "ctx_1",
		TaskID:        validTaskID(),
		RunID:         validRunID(),
		Summary:       "summary",
		TokenEstimate: 4000,
	}, 3000)
	if err == nil || !strings.Contains(err.Error(), "max_context_pack_tokens") {
		t.Fatalf("expected token limit error, got %v", err)
	}
}

func TestValidateSuperAgentRejectsMissingTimestamp(t *testing.T) {
	cases := []struct {
		name string
		err  string
		run  func() error
	}{
		{
			name: "agent run started_at",
			err:  "started_at",
			run: func() error {
				return ValidateAgentRun(AgentRun{RunID: validRunID(), TaskID: validTaskID(), ActorID: "mio", Status: "running"})
			},
		},
		{
			name: "subagent task created_at",
			err:  "created_at",
			run: func() error {
				return ValidateSubagentTask(SubagentTask{
					TaskID:               validTaskID(),
					RunID:                validRunID(),
					ActorID:              "shiro",
					Task:                 "調査",
					Scope:                []string{"docs/"},
					TerminationCondition: "report",
					Status:               "pending",
				})
			},
		},
		{
			name: "context pack created_at",
			err:  "created_at",
			run: func() error {
				return ValidateContextPack(ContextPack{ContextPackID: "ctx_1", TaskID: validTaskID(), RunID: validRunID(), Summary: "summary", TokenEstimate: 1200}, 3000)
			},
		},
		{
			name: "message channel created_at",
			err:  "created_at",
			run: func() error {
				return ValidateMessageChannel(MessageChannel{ChannelID: "chan_1", ChannelType: "superagent", Status: "active"})
			},
		},
		{
			name: "run queue created_at",
			err:  "created_at",
			run: func() error {
				return ValidateRunQueueItem(RunQueueItem{QueueID: "queue_1", TaskID: validTaskID(), RunStartReason: domaintask.RunStartReasonCheckpointResume, Goal: "resume run", Action: "resume", Status: "queued"})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatalf("expected %s error", tc.err)
			}
			if !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("expected error to contain %q, got %v", tc.err, err)
			}
		})
	}
}

func TestValidateSuperAgentRejectsTerminalWithoutCompletedAt(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 0, 0, 0, time.UTC)
	taskID, runID := validTaskID(), validRunID()
	cases := []struct {
		name string
		run  func() error
	}{
		{
			name: "agent run",
			run: func() error {
				return ValidateAgentRun(AgentRun{RunID: validRunID(), TaskID: validTaskID(), ActorID: "mio", Status: "failed", StartedAt: now, Summary: "failed"})
			},
		},
		{
			name: "run queue",
			run: func() error {
				return ValidateRunQueueItem(RunQueueItem{QueueID: "queue_1", TaskID: taskID, RunID: runID, RunStartReason: domaintask.RunStartReasonCheckpointResume, Goal: "resume run", Action: "resume", Status: "completed", CreatedAt: now})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("expected completed_at error")
			}
			if !strings.Contains(err.Error(), "completed_at") {
				t.Fatalf("expected completed_at error, got %v", err)
			}
		})
	}
}

func TestValidateSuperAgentRequiredFields(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "agent run id", err: ValidateAgentRun(AgentRun{ActorID: "mio", Status: "running", StartedAt: now}), want: "run_id"},
		{name: "agent actor", err: ValidateAgentRun(AgentRun{RunID: validRunID(), TaskID: validTaskID(), Status: "running", StartedAt: now}), want: "actor_id"},
		{name: "agent status", err: ValidateAgentRun(AgentRun{RunID: validRunID(), TaskID: validTaskID(), ActorID: "mio", StartedAt: now}), want: "status"},
		{name: "subagent task id", err: ValidateSubagentTask(SubagentTask{RunID: validRunID(), ActorID: "shiro", Task: "調査", Scope: []string{"docs/"}, TerminationCondition: "report", Status: "pending", CreatedAt: now}), want: "task_id"},
		{name: "subagent run id", err: ValidateSubagentTask(SubagentTask{TaskID: validTaskID(), ActorID: "shiro", Task: "調査", Scope: []string{"docs/"}, TerminationCondition: "report", Status: "pending", CreatedAt: now}), want: "run_id"},
		{name: "subagent actor id", err: ValidateSubagentTask(SubagentTask{TaskID: validTaskID(), RunID: validRunID(), Task: "調査", Scope: []string{"docs/"}, TerminationCondition: "report", Status: "pending", CreatedAt: now}), want: "actor_id"},
		{name: "subagent task", err: ValidateSubagentTask(SubagentTask{TaskID: validTaskID(), RunID: validRunID(), ActorID: "shiro", Scope: []string{"docs/"}, TerminationCondition: "report", Status: "pending", CreatedAt: now}), want: "task"},
		{name: "subagent termination", err: ValidateSubagentTask(SubagentTask{TaskID: validTaskID(), RunID: validRunID(), ActorID: "shiro", Task: "調査", Scope: []string{"docs/"}, Status: "pending", CreatedAt: now}), want: "termination_condition"},
		{name: "subagent status", err: ValidateSubagentTask(SubagentTask{TaskID: validTaskID(), RunID: validRunID(), ActorID: "shiro", Task: "調査", Scope: []string{"docs/"}, TerminationCondition: "report", CreatedAt: now}), want: "status"},
		{name: "context id", err: ValidateContextPack(ContextPack{TaskID: validTaskID(), RunID: validRunID(), Summary: "summary", CreatedAt: now}, 0), want: "context_pack_id"},
		{name: "context run", err: ValidateContextPack(ContextPack{ContextPackID: "ctx_1", TaskID: validTaskID(), Summary: "summary", CreatedAt: now}, 0), want: "run_id"},
		{name: "context summary", err: ValidateContextPack(ContextPack{ContextPackID: "ctx_1", TaskID: validTaskID(), RunID: validRunID(), CreatedAt: now}, 0), want: "summary"},
		{name: "context negative tokens", err: ValidateContextPack(ContextPack{ContextPackID: "ctx_1", TaskID: validTaskID(), RunID: validRunID(), Summary: "summary", TokenEstimate: -1, CreatedAt: now}, 0), want: "token_estimate"},
		{name: "channel id", err: ValidateMessageChannel(MessageChannel{ChannelType: "superagent", Status: "active", CreatedAt: now}), want: "channel_id"},
		{name: "channel type", err: ValidateMessageChannel(MessageChannel{ChannelID: "chan_1", Status: "active", CreatedAt: now}), want: "channel_type"},
		{name: "channel status", err: ValidateMessageChannel(MessageChannel{ChannelID: "chan_1", ChannelType: "superagent", CreatedAt: now}), want: "status"},
		{name: "queue id", err: ValidateRunQueueItem(RunQueueItem{Goal: "resume run", Action: "resume", Status: "queued", CreatedAt: now}), want: "queue_id"},
		{name: "queue task id", err: ValidateRunQueueItem(RunQueueItem{QueueID: "queue_1", RunStartReason: domaintask.RunStartReasonCheckpointResume, Goal: "resume run", Action: "resume", Status: "queued", CreatedAt: now}), want: "task_id"},
		{name: "queue goal", err: ValidateRunQueueItem(RunQueueItem{QueueID: "queue_1", TaskID: validTaskID(), RunStartReason: domaintask.RunStartReasonCheckpointResume, Action: "resume", Status: "queued", CreatedAt: now}), want: "goal"},
		{name: "queue action", err: ValidateRunQueueItem(RunQueueItem{QueueID: "queue_1", TaskID: validTaskID(), RunStartReason: domaintask.RunStartReasonCheckpointResume, Goal: "resume run", Status: "queued", CreatedAt: now}), want: "action"},
		{name: "queue status", err: ValidateRunQueueItem(RunQueueItem{QueueID: "queue_1", TaskID: validTaskID(), RunStartReason: domaintask.RunStartReasonCheckpointResume, Goal: "resume run", Action: "resume", CreatedAt: now}), want: "status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err == nil || !strings.Contains(tt.err.Error(), tt.want) {
				t.Fatalf("err=%v, want %s", tt.err, tt.want)
			}
		})
	}
}

func TestValidateSuperAgentTerminalStatusVariants(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 0, 0, 0, time.UTC)
	for _, status := range []string{"completed", "failed", "cancelled", "paused"} {
		t.Run("agent "+status, func(t *testing.T) {
			err := ValidateAgentRun(AgentRun{RunID: validRunID(), TaskID: validTaskID(), ActorID: "mio", Status: status, StartedAt: now})
			if err == nil || !strings.Contains(err.Error(), "completed_at") {
				t.Fatalf("err=%v, want completed_at", err)
			}
		})
	}
	for _, status := range []string{"completed", "failed", "cancelled"} {
		t.Run("queue "+status, func(t *testing.T) {
			err := ValidateRunQueueItem(RunQueueItem{QueueID: "queue_1", TaskID: validTaskID(), RunID: validRunID(), RunStartReason: domaintask.RunStartReasonCheckpointResume, Goal: "resume run", Action: "resume", Status: status, CreatedAt: now})
			if err == nil || !strings.Contains(err.Error(), "completed_at") {
				t.Fatalf("err=%v, want completed_at", err)
			}
		})
	}
}

func TestValidateRunQueueCanonicalTaskAndRunLifecycle(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 0, 0, 0, time.UTC)
	lease := func(status string, runID modulecore.RunID) RunQueueItem {
		return RunQueueItem{
			QueueID:        "queue_1",
			TaskID:         validTaskID(),
			RunID:          runID,
			RunStartReason: domaintask.RunStartReasonCheckpointResume,
			Goal:           "resume run",
			Action:         "resume",
			Status:         status,
			ClaimedAt:      now,
			LeaseToken:     "lease-1",
			LeaseUntil:     now.Add(time.Minute),
			CreatedAt:      now,
		}
	}

	t.Run("queued task with no run is valid", func(t *testing.T) {
		item := lease("queued", "")
		item.ClaimedAt = time.Time{}
		item.LeaseToken = ""
		item.LeaseUntil = time.Time{}
		if err := ValidateRunQueueItem(item); err != nil {
			t.Fatalf("queued item should validate before Task owner issues a Run: %v", err)
		}
	})
	t.Run("missing task is rejected", func(t *testing.T) {
		item := lease("queued", "")
		item.TaskID = ""
		item.ClaimedAt = time.Time{}
		item.LeaseToken = ""
		item.LeaseUntil = time.Time{}
		if err := ValidateRunQueueItem(item); err == nil || !strings.Contains(err.Error(), "task_id") {
			t.Fatalf("expected task_id error, got %v", err)
		}
	})
	t.Run("legacy task is rejected", func(t *testing.T) {
		item := lease("queued", "")
		item.TaskID = "task_legacy"
		item.ClaimedAt = time.Time{}
		item.LeaseToken = ""
		item.LeaseUntil = time.Time{}
		if err := ValidateRunQueueItem(item); err == nil || !strings.Contains(err.Error(), "task_id") {
			t.Fatalf("expected canonical task_id error, got %v", err)
		}
	})
	t.Run("queued item cannot retain a prior run", func(t *testing.T) {
		item := lease("queued", validRunID())
		item.ClaimedAt = time.Time{}
		item.LeaseToken = ""
		item.LeaseUntil = time.Time{}
		if err := ValidateRunQueueItem(item); err == nil || !strings.Contains(err.Error(), "run_id") {
			t.Fatalf("expected queued run_id rejection, got %v", err)
		}
	})
	t.Run("reserved permits the run to be issued after lease reservation", func(t *testing.T) {
		if err := ValidateRunQueueItem(lease("reserved", "")); err != nil {
			t.Fatalf("reserved item should validate without a RunID: %v", err)
		}
	})
	t.Run("reserved cannot retain a prior run", func(t *testing.T) {
		if err := ValidateRunQueueItem(lease("reserved", validRunID())); err == nil || !strings.Contains(err.Error(), "run_id") {
			t.Fatalf("expected reserved run_id rejection, got %v", err)
		}
	})
	t.Run("claimed requires a canonical run", func(t *testing.T) {
		if err := ValidateRunQueueItem(lease("claimed", "")); err == nil || !strings.Contains(err.Error(), "run_id") {
			t.Fatalf("expected claimed run_id error, got %v", err)
		}
		item := lease("claimed", "run_legacy")
		if err := ValidateRunQueueItem(item); err == nil || !strings.Contains(err.Error(), "run_id") {
			t.Fatalf("expected legacy claimed run_id error, got %v", err)
		}
		if err := ValidateRunQueueItem(lease("claimed", validRunID())); err != nil {
			t.Fatalf("claimed item with canonical RunID should validate: %v", err)
		}
	})
	t.Run("terminal requires a canonical run and completion time", func(t *testing.T) {
		item := lease("completed", "")
		item.CompletedAt = now.Add(time.Minute)
		if err := ValidateRunQueueItem(item); err == nil || !strings.Contains(err.Error(), "run_id") {
			t.Fatalf("expected terminal run_id error, got %v", err)
		}
		item.RunID = validRunID()
		item.CompletedAt = time.Time{}
		if err := ValidateRunQueueItem(item); err == nil || !strings.Contains(err.Error(), "completed_at") {
			t.Fatalf("expected terminal completed_at error, got %v", err)
		}
		item.CompletedAt = now.Add(time.Minute)
		if err := ValidateRunQueueItem(item); err != nil {
			t.Fatalf("terminal item should validate: %v", err)
		}
	})
	t.Run("unknown status is rejected", func(t *testing.T) {
		item := lease("done", validRunID())
		if err := ValidateRunQueueItem(item); err == nil || !strings.Contains(err.Error(), "status") {
			t.Fatalf("expected status error, got %v", err)
		}
	})
	t.Run("missing start reason is rejected", func(t *testing.T) {
		item := lease("queued", "")
		item.ClaimedAt = time.Time{}
		item.LeaseToken = ""
		item.LeaseUntil = time.Time{}
		item.RunStartReason = ""
		if err := ValidateRunQueueItem(item); err == nil || !strings.Contains(err.Error(), "run_start_reason") {
			t.Fatalf("expected run_start_reason error, got %v", err)
		}
	})
	t.Run("invalid start reason is rejected", func(t *testing.T) {
		item := lease("queued", "")
		item.ClaimedAt = time.Time{}
		item.LeaseToken = ""
		item.LeaseUntil = time.Time{}
		item.RunStartReason = domaintask.RunStartReason("legacy_resume")
		if err := ValidateRunQueueItem(item); err == nil || !strings.Contains(err.Error(), "run_start_reason") {
			t.Fatalf("expected run_start_reason error, got %v", err)
		}
	})
}

func TestValidateRunQueueBlockedBeforeRun(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 0, 0, 0, time.UTC)
	blocked := func() RunQueueItem {
		return RunQueueItem{
			QueueID:        "queue_1",
			TaskID:         validTaskID(),
			RunStartReason: domaintask.RunStartReasonCheckpointResume,
			Goal:           "resume run",
			Action:         "resume",
			Status:         "blocked",
			Reason:         "Task owner unavailable",
			CompletedAt:    now.Add(time.Minute),
			CreatedAt:      now,
		}
	}

	t.Run("blocked without a Run is valid", func(t *testing.T) {
		if err := ValidateRunQueueItem(blocked()); err != nil {
			t.Fatalf("blocked reservation failure should validate without a RunID: %v", err)
		}
	})
	t.Run("blocked requires a reason", func(t *testing.T) {
		item := blocked()
		item.Reason = ""
		if err := ValidateRunQueueItem(item); err == nil || !strings.Contains(err.Error(), "reason") {
			t.Fatalf("expected blocked reason error, got %v", err)
		}
	})
	t.Run("blocked requires completed_at", func(t *testing.T) {
		item := blocked()
		item.CompletedAt = time.Time{}
		if err := ValidateRunQueueItem(item); err == nil || !strings.Contains(err.Error(), "completed_at") {
			t.Fatalf("expected blocked completed_at error, got %v", err)
		}
	})
	t.Run("blocked rejects a RunID", func(t *testing.T) {
		item := blocked()
		item.RunID = validRunID()
		if err := ValidateRunQueueItem(item); err == nil || !strings.Contains(err.Error(), "run_id") {
			t.Fatalf("expected blocked run_id error, got %v", err)
		}
	})
}
