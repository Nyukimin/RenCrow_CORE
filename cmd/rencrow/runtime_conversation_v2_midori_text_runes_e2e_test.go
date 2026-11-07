package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/providers/rencrowllm"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMidoriTextRuneRangeThroughRealGateway(t *testing.T) {
	if os.Getenv("RENCROW_RUN_MIDORI_TEXT_E2E") != "1" {
		t.Skip("set RENCROW_RUN_MIDORI_TEXT_E2E=1 to run the opt-in local Midori Gateway acceptance test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	root := t.TempDir()
	eventStore, err := eventstore.NewSQLiteStore(filepath.Join(root, "events.sqlite"))
	if err != nil {
		t.Fatalf("open temporary EventStore: %v", err)
	}
	defer eventStore.Close()
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(root, "tasks"))
	if err != nil {
		t.Fatalf("open temporary Task store: %v", err)
	}
	defer taskStore.Close()
	tasks := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())

	shell := &midoriTextRuneE2EConversationProvider{}
	readback := responseFirstReadbackWiring{Assignees: map[string]agent.ReadbackAssigneeConfig{
		"midori": {Provider: shell, SystemPrompt: "Trusted Midori readback prompt"},
	}}
	publisher := newResponseFirstTestPublisher()
	runtime, err := newResponseFirstConversationRuntime(
		eventStore, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), shell,
		publisher, responseFirstTestOwnerID, readback,
	)
	if err != nil {
		t.Fatalf("build response-first runtime: %v", err)
	}
	allowResponseFirstTestReservations(runtime)
	timing, err := turntiming.NewBatchRecorderWithConfig(turntiming.RecorderConfig{BatchCapacity: 1, StampsPerTurn: 32, MemoryLimitBytes: 1 << 20})
	if err != nil {
		t.Fatalf("create timing batch: %v", err)
	}
	runtime.processing = &responseFirstV2Processing{timing: timing}

	gateway := rencrowllm.NewGatewayProviderWithOptions("", "midori", "http://127.0.0.1:8090", 70*time.Second).
		WithRenCrowExecution("midori", "wild", "midori")
	wild := agent.NewWildAgent(gateway, "Midori. Write the requested Japanese story as text only.")
	if err := runtime.configureTaskReport(tasks, []string{"midori"}, nil, wild); err != nil {
		t.Fatalf("configure real Midori Task/report execution: %v", err)
	}

	requestText := "「風鈴の町」の短編物語を書いてください。本文はUnicodeコードポイント数で60..400文字（両端を含む）にしてください。"
	accepted, err := runtime.Accept(ctx, responseFirstRequest(modulecore.NewSessionID(), "midori-text-runes-e2e", requestText))
	if err != nil {
		t.Fatalf("accept synthetic Japanese story request: %v", err)
	}
	if err := runtime.Process(ctx, accepted); err != nil {
		t.Fatalf("process Mio handoff fixture: %v", err)
	}
	origin, found, err := eventStore.LoadCommittedOutcome(ctx, accepted.TurnID)
	if err != nil || !found || origin.ResultState != "awaiting_readback" {
		t.Fatalf("origin result state=%q found=%t err=%v, want handoff awaiting readback", origin.ResultState, found, err)
	}
	priorityProgressed, err := runtime.drainOneResponseFirstPriorityWork(ctx)
	if err != nil || !priorityProgressed {
		t.Fatalf("priority drain progressed=%t err=%v, want accepted readback and Task admission", priorityProgressed, err)
	}
	created, err := tasks.List(ctx, domaintask.Filter{Limit: 10})
	if err != nil || len(created) != 1 {
		t.Fatalf("admitted Task count=%d err=%v, want one Midori Task", len(created), err)
	}
	task := created[0]
	if task.Assignee != "midori" || task.Production == nil || task.Production.SchemaVersion != domaintask.CreativeTaskV02SchemaVersion ||
		task.Production.TextRunes == nil || task.Production.TextRunes.Min != 60 || task.Production.TextRunes.Max != 400 {
		t.Fatalf("admitted Task id=%s assignee=%s production=%+v, want Midori creative-task.v0.2 range 60..400", task.TaskID, task.Assignee, task.Production)
	}
	if task.Status != domaintask.StatusRunning {
		t.Fatalf("admitted Task status=%s, want running before the execution drain", task.Status)
	}

	executionProgressed, reportPending, err := runtime.drainOneResponseFirstExecutionWork(ctx)
	if err != nil || !executionProgressed || !reportPending {
		t.Fatalf("execution drain progressed=%t reportPending=%t err=%v, want a real Midori report", executionProgressed, reportPending, err)
	}
	pending, err := tasks.ListPendingReports(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending report count=%d err=%v, want one durable Midori report", len(pending), err)
	}
	report := pending[0]
	actualRunes := utf8.RuneCountInString(strings.TrimSpace(report.Envelope.Result))
	if report.ReportID == "" || report.TaskID != task.TaskID || report.RunID == "" || report.Envelope.WorkTaskID != task.TaskID ||
		report.Envelope.WorkRunID != report.RunID || report.Envelope.Status != conversation.ReportStatusSucceeded || report.TaskStatus != domaintask.StatusSucceeded {
		t.Fatalf("durable report identities/status report_id=%s task_id=%s run_id=%s status=%s task_status=%s, want a succeeded source-bound report", report.ReportID, report.TaskID, report.RunID, report.Envelope.Status, report.TaskStatus)
	}
	wantCountEvidence := fmt.Sprintf("Text length verified: %d Unicode code points; required 60..400 inclusive.", actualRunes)
	if actualRunes < 60 || actualRunes > 400 || !strings.Contains(report.Envelope.Summary, wantCountEvidence) {
		t.Fatalf("real worker report measured_runes=%d summary_has_evidence=%t, want verified 60..400 code points", actualRunes, strings.Contains(report.Envelope.Summary, "Text length verified:"))
	}
	t.Logf("verified Midori report task_id=%s run_id=%s report_id=%s rune_count=%d", task.TaskID, report.RunID, report.ReportID, actualRunes)
	completedTask, err := tasks.Get(ctx, task.TaskID)
	if err != nil || completedTask.Status != domaintask.StatusSucceeded {
		t.Fatalf("completed Task id=%s status=%s err=%v, want succeeded Task", task.TaskID, completedTask.Status, err)
	}
	completedRun, err := tasks.GetRun(ctx, report.RunID)
	if err != nil || completedRun.TaskID != task.TaskID || completedRun.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("completed Run id=%s task_id=%s status=%s err=%v, want succeeded source-bound Run", report.RunID, completedRun.TaskID, completedRun.Status, err)
	}

	reportProgressed, retryRecovery, err := runtime.drainOneResponseFirstReportWork(ctx)
	if err != nil || !reportProgressed || retryRecovery {
		t.Fatalf("report drain progressed=%t retryRecovery=%t err=%v, want Mio owner report delivery", reportProgressed, retryRecovery, err)
	}
	if after, err := tasks.ListPendingReports(ctx); err != nil || len(after) != 0 {
		t.Fatalf("pending reports after owner delivery=%d err=%v, want none", len(after), err)
	}
	if shell.delegateCalls != 1 || shell.readbackCalls != 1 || shell.reportReplyCalls != 1 {
		t.Fatalf("Mio fixture calls=(%d,%d,%d), want one handoff, readback, and owner reply", shell.delegateCalls, shell.readbackCalls, shell.reportReplyCalls)
	}
	var reportInput modulecore.EventEnvelope
	for len(publisher.accepted) != 0 {
		event := <-publisher.accepted
		if event.EventType == "conversation.agent_report" {
			reportInput = event
		}
	}
	if reportInput.EventType != "conversation.agent_report" || reportInput.Payload["trusted_sender"] != "midori" ||
		reportInput.Payload["work_task_id"] != string(task.TaskID) {
		t.Fatalf("accepted owner report event=%s task_id=%v trusted_sender=%v, want Midori report bound to Task %s", reportInput.EventType, reportInput.Payload["work_task_id"], reportInput.Payload["trusted_sender"], task.TaskID)
	}
	committedReply, found, err := eventStore.LoadCommittedOutcome(ctx, reportInput.TurnID)
	if err != nil || !found || committedReply.ResultState != "completed" || !hasMioReportReply(committedReply.Events) {
		t.Fatalf("Mio owner report reply outcome state=%q found=%t err=%v, want committed final reply", committedReply.ResultState, found, err)
	}
}

type midoriTextRuneE2EConversationProvider struct {
	delegateCalls    int
	readbackCalls    int
	reportReplyCalls int
}

func (p *midoriTextRuneE2EConversationProvider) Name() string { return "midori-text-rune-e2e-shell" }

func (p *midoriTextRuneE2EConversationProvider) GenerateConversation(_ context.Context, scope conversation.ConversationScope, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
	var content string
	switch scope.Purpose {
	case conversation.PurposeMioTurn:
		p.delegateCalls++
		sourceIDs := []modulecore.MessageID{scope.InputID}
		encoded, err := json.Marshal(conversation.AgentEmission{
			SchemaVersion: "mio.turn.v1", Kind: conversation.EmissionKindDelegate,
			UserMessage: conversation.AgentUserMessage{To: "user", Text: "Midoriに短編物語の草稿を依頼します。"},
			Delegation: &conversation.AgentDelegation{
				To: "midori", Goal: "「風鈴の町」の短編物語の草稿",
				Instruction: "日本語で、情景と小さな出来事を含む短編物語を一つ書いてください。",
				Constraints: []conversation.AgentConstraint{
					{Key: domaintask.ProductionStageConstraintKey, Text: "author", SourceMessageIDs: sourceIDs},
					{Key: domaintask.ProductionTextRunesConstraintKey, Text: "60..400", SourceMessageIDs: sourceIDs},
				},
				ExpectedOutput:   "日本語の短編物語本文（Unicodeコードポイント数60..400、両端を含む）",
				SourceMessageIDs: sourceIDs,
			},
		})
		if err != nil {
			return llm.GenerateResponse{}, err
		}
		content = string(encoded)
	case conversation.PurposeReadback:
		p.readbackCalls++
		content = `{"schema_version":"agent.readback.v1","status":"accepted","understood_goal":"「風鈴の町」の短編物語の草稿","acknowledged_constraint_keys":["rencrow_production_stage_v1","rencrow_production_text_runes_v2"],"expected_output":"日本語の短編物語本文（Unicodeコードポイント数60..400、両端を含む）","text":"執筆と文字数条件を確認し、草稿を作成します。"}`
	case conversation.PurposeReportReply:
		p.reportReplyCalls++
		content = `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"user","text":"Midoriの物語草稿と文字数条件の確認結果を受け取りました。"}}`
	default:
		return llm.GenerateResponse{}, fmt.Errorf("unexpected Midori text length fixture purpose %q", scope.Purpose)
	}
	return llm.GenerateResponse{Content: content, RequestID: modulecore.NewRequestID(), ResponseID: modulecore.NewResponseID(), TokensUsed: 8}, nil
}

var _ conversationruntime.DelegatedPrivateWorker = (*agent.WildAgent)(nil)
