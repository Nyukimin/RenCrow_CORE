package superagent

import (
	"strings"
	"testing"
	"time"
)

func TestValidateSubagentTaskRequiresScopeAndTermination(t *testing.T) {
	err := ValidateSubagentTask(SubagentTask{
		SubagentID:  "sub_1",
		ParentRunID: "run_1",
		AgentType:   "ResearchAgent",
		Task:        "調査",
		Status:      "pending",
	})
	if err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("expected scope error, got %v", err)
	}
}

func TestValidateSuperAgentAcceptsCompleteRecords(t *testing.T) {
	now := time.Date(2026, 5, 20, 7, 0, 0, 0, time.UTC)
	if err := ValidateAgentRun(AgentRun{
		RunID:       "run_1",
		AgentType:   "LeadAgent",
		Status:      "completed",
		StartedAt:   now,
		CompletedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("agent run should validate: %v", err)
	}
	if err := ValidateSubagentTask(SubagentTask{
		SubagentID:           "sub_1",
		ParentRunID:          "run_1",
		AgentType:            "ResearchAgent",
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
		RunID:         "run_1",
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
	if err := ValidateTraceEvent(TraceEvent{
		EventID:   "evt_1",
		EventType: "lead_agent_started",
		Status:    "completed",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("trace event should validate: %v", err)
	}
	if err := ValidateRunQueueItem(RunQueueItem{
		QueueID:     "queue_1",
		Goal:        "resume run",
		Action:      "resume",
		Status:      "completed",
		CreatedAt:   now,
		CompletedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("run queue item should validate: %v", err)
	}
}

func TestValidateContextPackRespectsMaxTokens(t *testing.T) {
	err := ValidateContextPack(ContextPack{
		ContextPackID: "ctx_1",
		RunID:         "run_1",
		Summary:       "summary",
		TokenEstimate: 4000,
	}, 3000)
	if err == nil || !strings.Contains(err.Error(), "max_context_pack_tokens") {
		t.Fatalf("expected token limit error, got %v", err)
	}
}

func TestValidateTraceEventRequiresEventType(t *testing.T) {
	err := ValidateTraceEvent(TraceEvent{EventID: "evt_1", Status: "completed"})
	if err == nil || !strings.Contains(err.Error(), "event_type") {
		t.Fatalf("expected event_type error, got %v", err)
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
				return ValidateAgentRun(AgentRun{RunID: "run_1", AgentType: "LeadAgent", Status: "running"})
			},
		},
		{
			name: "subagent task created_at",
			err:  "created_at",
			run: func() error {
				return ValidateSubagentTask(SubagentTask{
					SubagentID:           "sub_1",
					ParentRunID:          "run_1",
					AgentType:            "ResearchAgent",
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
				return ValidateContextPack(ContextPack{ContextPackID: "ctx_1", RunID: "run_1", Summary: "summary", TokenEstimate: 1200}, 3000)
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
			name: "trace event created_at",
			err:  "created_at",
			run: func() error {
				return ValidateTraceEvent(TraceEvent{EventID: "evt_1", EventType: "lead_agent_started", Status: "completed"})
			},
		},
		{
			name: "run queue created_at",
			err:  "created_at",
			run: func() error {
				return ValidateRunQueueItem(RunQueueItem{QueueID: "queue_1", Goal: "resume run", Action: "resume", Status: "queued"})
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
	cases := []struct {
		name string
		run  func() error
	}{
		{
			name: "agent run",
			run: func() error {
				return ValidateAgentRun(AgentRun{RunID: "run_1", AgentType: "LeadAgent", Status: "failed", StartedAt: now, Summary: "failed"})
			},
		},
		{
			name: "run queue",
			run: func() error {
				return ValidateRunQueueItem(RunQueueItem{QueueID: "queue_1", Goal: "resume run", Action: "resume", Status: "completed", CreatedAt: now})
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
		{name: "agent run id", err: ValidateAgentRun(AgentRun{AgentType: "LeadAgent", Status: "running", StartedAt: now}), want: "run_id"},
		{name: "agent type", err: ValidateAgentRun(AgentRun{RunID: "run_1", Status: "running", StartedAt: now}), want: "agent_type"},
		{name: "agent status", err: ValidateAgentRun(AgentRun{RunID: "run_1", AgentType: "LeadAgent", StartedAt: now}), want: "status"},
		{name: "subagent id", err: ValidateSubagentTask(SubagentTask{ParentRunID: "run_1", AgentType: "ResearchAgent", Task: "調査", Scope: []string{"docs/"}, TerminationCondition: "report", Status: "pending", CreatedAt: now}), want: "subagent_id"},
		{name: "subagent parent", err: ValidateSubagentTask(SubagentTask{SubagentID: "sub_1", AgentType: "ResearchAgent", Task: "調査", Scope: []string{"docs/"}, TerminationCondition: "report", Status: "pending", CreatedAt: now}), want: "parent_run_id"},
		{name: "subagent agent type", err: ValidateSubagentTask(SubagentTask{SubagentID: "sub_1", ParentRunID: "run_1", Task: "調査", Scope: []string{"docs/"}, TerminationCondition: "report", Status: "pending", CreatedAt: now}), want: "agent_type"},
		{name: "subagent task", err: ValidateSubagentTask(SubagentTask{SubagentID: "sub_1", ParentRunID: "run_1", AgentType: "ResearchAgent", Scope: []string{"docs/"}, TerminationCondition: "report", Status: "pending", CreatedAt: now}), want: "task"},
		{name: "subagent termination", err: ValidateSubagentTask(SubagentTask{SubagentID: "sub_1", ParentRunID: "run_1", AgentType: "ResearchAgent", Task: "調査", Scope: []string{"docs/"}, Status: "pending", CreatedAt: now}), want: "termination_condition"},
		{name: "subagent status", err: ValidateSubagentTask(SubagentTask{SubagentID: "sub_1", ParentRunID: "run_1", AgentType: "ResearchAgent", Task: "調査", Scope: []string{"docs/"}, TerminationCondition: "report", CreatedAt: now}), want: "status"},
		{name: "context id", err: ValidateContextPack(ContextPack{RunID: "run_1", Summary: "summary", CreatedAt: now}, 0), want: "context_pack_id"},
		{name: "context run", err: ValidateContextPack(ContextPack{ContextPackID: "ctx_1", Summary: "summary", CreatedAt: now}, 0), want: "run_id"},
		{name: "context summary", err: ValidateContextPack(ContextPack{ContextPackID: "ctx_1", RunID: "run_1", CreatedAt: now}, 0), want: "summary"},
		{name: "context negative tokens", err: ValidateContextPack(ContextPack{ContextPackID: "ctx_1", RunID: "run_1", Summary: "summary", TokenEstimate: -1, CreatedAt: now}, 0), want: "token_estimate"},
		{name: "channel id", err: ValidateMessageChannel(MessageChannel{ChannelType: "superagent", Status: "active", CreatedAt: now}), want: "channel_id"},
		{name: "channel type", err: ValidateMessageChannel(MessageChannel{ChannelID: "chan_1", Status: "active", CreatedAt: now}), want: "channel_type"},
		{name: "channel status", err: ValidateMessageChannel(MessageChannel{ChannelID: "chan_1", ChannelType: "superagent", CreatedAt: now}), want: "status"},
		{name: "trace id", err: ValidateTraceEvent(TraceEvent{EventType: "lead_agent_started", Status: "completed", CreatedAt: now}), want: "event_id"},
		{name: "trace status", err: ValidateTraceEvent(TraceEvent{EventID: "evt_1", EventType: "lead_agent_started", CreatedAt: now}), want: "status"},
		{name: "queue id", err: ValidateRunQueueItem(RunQueueItem{Goal: "resume run", Action: "resume", Status: "queued", CreatedAt: now}), want: "queue_id"},
		{name: "queue goal", err: ValidateRunQueueItem(RunQueueItem{QueueID: "queue_1", Action: "resume", Status: "queued", CreatedAt: now}), want: "goal"},
		{name: "queue action", err: ValidateRunQueueItem(RunQueueItem{QueueID: "queue_1", Goal: "resume run", Status: "queued", CreatedAt: now}), want: "action"},
		{name: "queue status", err: ValidateRunQueueItem(RunQueueItem{QueueID: "queue_1", Goal: "resume run", Action: "resume", CreatedAt: now}), want: "status"},
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
			err := ValidateAgentRun(AgentRun{RunID: "run_1", AgentType: "LeadAgent", Status: status, StartedAt: now})
			if err == nil || !strings.Contains(err.Error(), "completed_at") {
				t.Fatalf("err=%v, want completed_at", err)
			}
		})
	}
	for _, status := range []string{"completed", "failed", "cancelled"} {
		t.Run("queue "+status, func(t *testing.T) {
			err := ValidateRunQueueItem(RunQueueItem{QueueID: "queue_1", Goal: "resume run", Action: "resume", Status: status, CreatedAt: now})
			if err == nil || !strings.Contains(err.Error(), "completed_at") {
				t.Fatalf("err=%v, want completed_at", err)
			}
		})
	}
}
