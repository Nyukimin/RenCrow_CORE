package orchestrator

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type phase8RecordingIdleNotifier struct {
	activities int
	chatBusy   []bool
	workerBusy []bool
}

func (n *phase8RecordingIdleNotifier) NotifyActivity() {
	n.activities++
}

func (n *phase8RecordingIdleNotifier) SetChatBusy(busy bool) {
	n.chatBusy = append(n.chatBusy, busy)
}

func (n *phase8RecordingIdleNotifier) SetWorkerBusy(busy bool) {
	n.workerBusy = append(n.workerBusy, busy)
}

type phase8RecordingReportStore struct {
	reports []domainexecution.ExecutionReport
}

func (s *phase8RecordingReportStore) Save(_ context.Context, report domainexecution.ExecutionReport) error {
	s.reports = append(s.reports, report)
	return nil
}

func TestPhase8MessageResponseAssemblerContracts(t *testing.T) {
	assembler := messageResponseAssembler{}
	jobID := modulecore.NewTaskID()
	decision := routing.NewDecision(routing.RoutePLAN, 0.82, "plan")

	resp := assembler.Build("計画しました", decision, jobID)
	if resp.Response != "計画しました" {
		t.Fatalf("expected response text to be preserved, got %q", resp.Response)
	}
	if resp.Route != routing.RoutePLAN {
		t.Fatalf("expected route PLAN, got %s", resp.Route)
	}
	if resp.Confidence != 0.82 {
		t.Fatalf("expected confidence 0.82, got %f", resp.Confidence)
	}
	if resp.JobID != jobID.String() {
		t.Fatalf("expected job ID %s, got %s", jobID.String(), resp.JobID)
	}

	commandResp := assembler.BuildChatCommand("停止しました", jobID)
	if commandResp.Response != "停止しました" {
		t.Fatalf("expected chat command response text to be preserved, got %q", commandResp.Response)
	}
	if commandResp.Route != routing.RouteCHAT {
		t.Fatalf("expected chat command route CHAT, got %s", commandResp.Route)
	}
	if commandResp.Confidence != 1.0 {
		t.Fatalf("expected chat command confidence 1.0, got %f", commandResp.Confidence)
	}
	if commandResp.JobID != jobID.String() {
		t.Fatalf("expected chat command response to preserve job ID %s, got %s", jobID.String(), commandResp.JobID)
	}
}

func TestPhase8IdleBusyGuardFactoryContracts(t *testing.T) {
	guard := newIdleBusyGuardFactory(nil)
	guard.BeginChat()()
	guard.BeginWorker(routing.RouteOPS)()

	notifier := &phase8RecordingIdleNotifier{}
	guard.SetNotifier(notifier)

	endChat := guard.BeginChat()
	if notifier.activities != 1 {
		t.Fatalf("expected one activity notification, got %d", notifier.activities)
	}
	if got := notifier.chatBusy; len(got) != 1 || got[0] != true {
		t.Fatalf("expected chat busy to start once, got %#v", got)
	}
	endChat()
	if got := notifier.chatBusy; len(got) != 2 || got[1] != false {
		t.Fatalf("expected chat busy to end once, got %#v", got)
	}

	guard.BeginWorker(routing.RouteCHAT)()
	if len(notifier.workerBusy) != 0 {
		t.Fatalf("expected CHAT route not to set worker busy, got %#v", notifier.workerBusy)
	}

	endWorker := guard.BeginWorker(routing.RouteOPS)
	if got := notifier.workerBusy; len(got) != 1 || got[0] != true {
		t.Fatalf("expected worker busy to start once, got %#v", got)
	}
	endWorker()
	if got := notifier.workerBusy; len(got) != 2 || got[1] != false {
		t.Fatalf("expected worker busy to end once, got %#v", got)
	}
}

func TestPhase8AutonomousExecutionCoordinatorUsesUpdatedReportStore(t *testing.T) {
	var emitted []string
	var executed bool
	reporter := &phase8RecordingReportStore{}
	coordinator := newAutonomousExecutionCoordinator(
		nil,
		func() int { return 0 },
		func(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {
			emitted = append(emitted, eventType+":"+content)
		},
		func(ctx context.Context, gotTask conversation.TurnInput, route routing.Route, jobID modulecore.TaskID, ttsSessionID string) (string, error) {
			executed = true
			if route != routing.RoutePLAN {
				t.Fatalf("expected route PLAN, got %s", route)
			}
			address := gotTask.ChannelAddress()
			if gotTask.SessionID() != "sess-1" || address.ChannelType() != "line" || address.ExternalConversationID() != "U123" || ttsSessionID != "tts-1" {
				t.Fatalf("unexpected route context: session=%s channel=%s chat=%s tts=%s", gotTask.SessionID(), address.ChannelType(), address.ExternalConversationID(), ttsSessionID)
			}
			return "計画しました", nil
		},
	)
	coordinator.SetReportStore(reporter)

	jobID := modulecore.NewTaskID()
	tk := newOrchestratorTestTurnInput(t, "買い物の計画を作ってください", "line", "U123").WithSessionID("sess-1")
	resp, err := coordinator.Execute(context.Background(), tk, routing.RoutePLAN, jobID, "tts-1")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if resp != "計画しました" {
		t.Fatalf("expected response to be returned, got %q", resp)
	}
	if !executed {
		t.Fatal("expected route direct executor to be called")
	}
	if len(emitted) == 0 {
		t.Fatal("expected entry stage events to be emitted")
	}
	if len(reporter.reports) != 1 {
		t.Fatalf("expected one execution report to be saved, got %d", len(reporter.reports))
	}
	if reporter.reports[0].Status != "passed" {
		t.Fatalf("expected passed report, got %s", reporter.reports[0].Status)
	}
}
