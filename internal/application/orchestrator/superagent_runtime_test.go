package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestLeadAgentRunInputFromRequestCopiesOnlyTheRecordedFields(t *testing.T) {
	checkpointID := modulecore.NewCheckpointID()
	req := ProcessMessageRequest{
		TraceID:                  string(modulecore.NewTraceID()),
		SessionID:                string(modulecore.NewSessionID()),
		Channel:                  "chat",
		ChatID:                   "room-1",
		UserMessage:              "hello",
		ResumeCheckpointRevision: 3,
		ResumeCheckpointSummary:  "summary",
		ResumeNextAction:         "next",
		ResumeCheckpointID:       checkpointID,
	}
	got := leadAgentRunInputFromRequest(req)
	want := LeadAgentRunInput{
		TraceID: req.TraceID, SessionID: req.SessionID, Channel: "chat", ChatID: "room-1", Goal: "hello",
		ResumeCheckpointRevision: 3, ResumeCheckpointSummary: "summary", ResumeNextAction: "next", ResumeCheckpointID: checkpointID,
	}
	if got != want {
		t.Fatalf("input=%+v want %+v", got, want)
	}
}

func TestRecordLeadAgentRunRecordsGoalAndCorrelatedTerminalEvent(t *testing.T) {
	recorder := &mockSuperAgentRuntimeRecorder{}
	input := LeadAgentRunInput{
		TraceID:   string(modulecore.NewTraceID()),
		SessionID: string(modulecore.NewSessionID()),
		Channel:   "agent_ops",
		ChatID:    "agent-ops",
		Goal:      "fixed goal",
	}
	taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()

	started, err := RecordLeadAgentRunStarted(context.Background(), recorder, input, taskID, runID, "shiro", routing.RouteOPS)
	if err != nil {
		t.Fatalf("RecordLeadAgentRunStarted() error=%v", err)
	}
	if started.TraceID != modulecore.TraceID(input.TraceID) || started.StartedEventID == "" || started.StartedAt.IsZero() {
		t.Fatalf("started record=%+v", started)
	}
	if err := RecordLeadAgentRunFinished(context.Background(), recorder, input, taskID, runID, "shiro", routing.RouteOPS, started, "completed", "done"); err != nil {
		t.Fatalf("RecordLeadAgentRunFinished() error=%v", err)
	}

	if len(recorder.runs) != 2 || recorder.runs[0].Status != "running" || recorder.runs[1].Status != "completed" {
		t.Fatalf("runs=%+v", recorder.runs)
	}
	for _, run := range recorder.runs {
		if run.Goal != "fixed goal" || run.WorkstreamID != input.SessionID || run.ActorID != "shiro" || run.TaskID != taskID || run.RunID != runID {
			t.Fatalf("run=%+v", run)
		}
	}
	if len(recorder.contextPacks) != 1 {
		t.Fatalf("context packs=%d", len(recorder.contextPacks))
	}
	// The pack summary layout is persisted and hashed; it must not change.
	wantSummary := "route=OPS channel=agent_ops chat_id=agent-ops user_message=fixed goal"
	if recorder.contextPacks[0].Summary != wantSummary {
		t.Fatalf("context pack summary=%q want %q", recorder.contextPacks[0].Summary, wantSummary)
	}
	if len(recorder.traces) != 2 || recorder.traces[0].EventType != "lead_agent.started" || recorder.traces[1].EventType != "lead_agent.completed" {
		t.Fatalf("traces=%+v", recorder.traces)
	}
	if recorder.traces[1].CausationEventID != started.StartedEventID || recorder.traces[1].TraceID != started.TraceID {
		t.Fatalf("terminal event is not correlated to the start event: %+v", recorder.traces[1])
	}
}

func TestRecordLeadAgentRunHonorsCompleteResumeCheckpointOnly(t *testing.T) {
	checkpointID := modulecore.NewCheckpointID()
	base := LeadAgentRunInput{
		TraceID:   string(modulecore.NewTraceID()),
		SessionID: string(modulecore.NewSessionID()),
		Goal:      "goal",
	}
	resumed := base
	resumed.ResumeCheckpointRevision = 4
	resumed.ResumeCheckpointSummary = " resumed summary "
	resumed.ResumeNextAction = " resumed action "
	resumed.ResumeCheckpointID = checkpointID
	incomplete := base
	incomplete.ResumeCheckpointRevision = 4
	incomplete.ResumeCheckpointSummary = "summary without next action"
	incomplete.ResumeCheckpointID = checkpointID

	for name, tc := range map[string]struct {
		input        LeadAgentRunInput
		wantRevision int
		wantSummary  string
	}{
		"complete resume":    {resumed, 4, "resumed summary"},
		"incomplete resume":  {incomplete, 1, "request accepted; route=OPS"},
		"no resume metadata": {base, 1, "request accepted; route=OPS"},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := &mockSuperAgentRuntimeRecorder{}
			if _, err := RecordLeadAgentRunStarted(context.Background(), recorder, tc.input, modulecore.NewTaskID(), modulecore.NewRunID(), "shiro", routing.RouteOPS); err != nil {
				t.Fatalf("RecordLeadAgentRunStarted() error=%v", err)
			}
			run := recorder.runs[0]
			if run.CheckpointRevision != tc.wantRevision || run.CheckpointSummary != tc.wantSummary {
				t.Fatalf("checkpoint revision=%d summary=%q", run.CheckpointRevision, run.CheckpointSummary)
			}
			if name == "complete resume" && (run.CheckpointID != checkpointID || run.NextAction != "resumed action") {
				t.Fatalf("resume checkpoint id/next action = %q/%q", run.CheckpointID, run.NextAction)
			}
		})
	}
}

func TestRecordLeadAgentRunStartedWithoutRecorderOnlyReturnsStartTime(t *testing.T) {
	started, err := RecordLeadAgentRunStarted(context.Background(), nil, LeadAgentRunInput{}, modulecore.NewTaskID(), modulecore.NewRunID(), "shiro", routing.RouteOPS)
	if err != nil || started.StartedAt.IsZero() || started.TraceID != "" || started.StartedEventID != "" {
		t.Fatalf("started=%+v err=%v", started, err)
	}
	if err := RecordLeadAgentRunFinished(context.Background(), nil, LeadAgentRunInput{}, modulecore.NewTaskID(), modulecore.NewRunID(), "shiro", routing.RouteOPS, started, "completed", "done"); err != nil {
		t.Fatalf("RecordLeadAgentRunFinished() without recorder error=%v", err)
	}
}

func TestRecordLeadAgentRunStartedRejectsInvalidIdentity(t *testing.T) {
	recorder := &mockSuperAgentRuntimeRecorder{}
	valid := LeadAgentRunInput{TraceID: string(modulecore.NewTraceID()), SessionID: string(modulecore.NewSessionID()), Goal: "goal"}
	for name, tc := range map[string]struct {
		input LeadAgentRunInput
		actor string
		want  string
	}{
		"invalid trace": {LeadAgentRunInput{TraceID: "not-a-trace", Goal: "goal"}, "shiro", "trace identity"},
		"unknown actor": {valid, "worker", "actor identity"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := RecordLeadAgentRunStarted(context.Background(), recorder, tc.input, modulecore.NewTaskID(), modulecore.NewRunID(), tc.actor, routing.RouteOPS)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v want substring %q", err, tc.want)
			}
		})
	}
	if len(recorder.runs) != 0 || len(recorder.traces) != 0 {
		t.Fatalf("invalid identity must not reach the ledger: runs=%d traces=%d", len(recorder.runs), len(recorder.traces))
	}
}
