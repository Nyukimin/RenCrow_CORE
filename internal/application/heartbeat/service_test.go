package heartbeat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	skillbootstrap "github.com/Nyukimin/picoclaw_multiLLM/internal/application/skillgovernance"
	domainbacklog "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/backlog"
	domainrevenue "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/revenue"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
	domainskill "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/skillgovernance"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
	domainworkstream "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/workstream"
)

// mockWorkerAgent はテスト用のHeartbeat workerモック。
type mockWorkerAgent struct {
	response   string
	err        error
	called     bool
	chatCalled bool
	executed   bool
	lastMsg    string
	lastTask   task.Task
}

func (m *mockWorkerAgent) Chat(ctx context.Context, t task.Task) (string, error) {
	m.chatCalled = true
	m.recordCall(t)
	return m.response, m.err
}

func (m *mockWorkerAgent) Execute(ctx context.Context, t task.Task) (string, error) {
	m.executed = true
	m.recordCall(t)
	return m.response, m.err
}

func (m *mockWorkerAgent) recordCall(t task.Task) {
	m.called = true
	m.lastMsg = t.UserMessage()
	m.lastTask = t
}

// mockSender はテスト用のNotificationSenderモック
type mockSender struct {
	messages []string
	err      error
}

func (m *mockSender) SendNotification(ctx context.Context, message string) error {
	m.messages = append(m.messages, message)
	return m.err
}

type recordingEventListener struct {
	events []orchestrator.OrchestratorEvent
}

func (r *recordingEventListener) OnEvent(ev orchestrator.OrchestratorEvent) {
	r.events = append(r.events, ev)
}

type fakeIdleChatSequenceMonitor struct {
	report heartbeatSequenceMonitorReport
	called bool
}

type heartbeatSequenceMonitorReport struct {
	Status     string
	Active     bool
	Recovered  bool
	Stage      string
	Detail     string
	SessionID  string
	Generation uint64
	AgeSeconds int64
	Action     string
}

func (m *fakeIdleChatSequenceMonitor) CheckIdleChatSequence(_ context.Context, now time.Time) IdleChatSequenceCheck {
	m.called = true
	return IdleChatSequenceCheck{
		Status:     m.report.Status,
		Active:     m.report.Active,
		Recovered:  m.report.Recovered,
		Stage:      m.report.Stage,
		Detail:     m.report.Detail,
		SessionID:  m.report.SessionID,
		Generation: m.report.Generation,
		AgeSeconds: m.report.AgeSeconds,
		Action:     m.report.Action,
		CheckedAt:  now.UTC(),
	}
}

type memoryWorkstreamHeartbeatStore struct {
	workstreams   []domainworkstream.Workstream
	goals         []domainworkstream.Goal
	artifacts     []domainworkstream.Artifact
	schedules     []domainworkstream.HeartbeatSchedule
	saved         []domainworkstream.HeartbeatSchedule
	steering      []domainworkstream.SteeringItem
	savedSteering []domainworkstream.SteeringItem
	vaultUpdates  []domainworkstream.VaultUpdateLog
}

type memoryBacklogStore struct {
	items []domainbacklog.Item
	saved []domainbacklog.Item
}

type heartbeatSkillStore struct {
	manifests []domainskill.SkillManifest
	logs      []domainskill.SkillTriggerLog
}

type memoryRevenueDailyRoutineStore struct {
	market    []domainrevenue.MarketResearchItem
	posts     []domainrevenue.SNSPostMetric
	products  []domainrevenue.Product
	voices    []domainrevenue.CustomerVoice
	events    []domainrevenue.RevenueEvent
	decisions []domainrevenue.HumanDecisionGateRecord
	reports   []domainrevenue.DailyRoutineReport
}

func (s *heartbeatSkillStore) ListSkillManifests(_ context.Context, _ int) ([]domainskill.SkillManifest, error) {
	return append([]domainskill.SkillManifest(nil), s.manifests...), nil
}

func (s *heartbeatSkillStore) SaveSkillTriggerLog(_ context.Context, log domainskill.SkillTriggerLog) error {
	s.logs = append(s.logs, log)
	return nil
}

func (s *memoryRevenueDailyRoutineStore) ListMarketResearchItems(_ context.Context, _ int) ([]domainrevenue.MarketResearchItem, error) {
	return append([]domainrevenue.MarketResearchItem(nil), s.market...), nil
}

func (s *memoryRevenueDailyRoutineStore) ListSNSPostMetrics(_ context.Context, _ int) ([]domainrevenue.SNSPostMetric, error) {
	return append([]domainrevenue.SNSPostMetric(nil), s.posts...), nil
}

func (s *memoryRevenueDailyRoutineStore) ListProducts(_ context.Context, _ int) ([]domainrevenue.Product, error) {
	return append([]domainrevenue.Product(nil), s.products...), nil
}

func (s *memoryRevenueDailyRoutineStore) ListCustomerVoices(_ context.Context, _ int) ([]domainrevenue.CustomerVoice, error) {
	return append([]domainrevenue.CustomerVoice(nil), s.voices...), nil
}

func (s *memoryRevenueDailyRoutineStore) ListRevenueEvents(_ context.Context, _ int) ([]domainrevenue.RevenueEvent, error) {
	return append([]domainrevenue.RevenueEvent(nil), s.events...), nil
}

func (s *memoryRevenueDailyRoutineStore) ListHumanDecisionGateRecords(_ context.Context, _ int) ([]domainrevenue.HumanDecisionGateRecord, error) {
	return append([]domainrevenue.HumanDecisionGateRecord(nil), s.decisions...), nil
}

func (s *memoryRevenueDailyRoutineStore) SaveDailyRoutineReport(_ context.Context, item domainrevenue.DailyRoutineReport) error {
	if err := domainrevenue.ValidateDailyRoutineReport(item); err != nil {
		return err
	}
	s.reports = append(s.reports, item)
	return nil
}

func (m *memoryWorkstreamHeartbeatStore) SaveWorkstream(_ context.Context, item domainworkstream.Workstream) error {
	if err := domainworkstream.ValidateWorkstream(item); err != nil {
		return err
	}
	m.workstreams = append([]domainworkstream.Workstream{item}, m.workstreams...)
	return nil
}

func (m *memoryWorkstreamHeartbeatStore) SaveGoal(_ context.Context, item domainworkstream.Goal) error {
	if err := domainworkstream.ValidateGoal(item); err != nil {
		return err
	}
	m.goals = append([]domainworkstream.Goal{item}, m.goals...)
	return nil
}

func (m *memoryWorkstreamHeartbeatStore) SaveArtifact(_ context.Context, item domainworkstream.Artifact) error {
	if err := domainworkstream.ValidateArtifact(item); err != nil {
		return err
	}
	m.artifacts = append([]domainworkstream.Artifact{item}, m.artifacts...)
	return nil
}

func (m *memoryWorkstreamHeartbeatStore) ListHeartbeatSchedules(_ context.Context, _ int) ([]domainworkstream.HeartbeatSchedule, error) {
	return append([]domainworkstream.HeartbeatSchedule(nil), m.schedules...), nil
}

func (m *memoryWorkstreamHeartbeatStore) SaveHeartbeatSchedule(_ context.Context, item domainworkstream.HeartbeatSchedule) error {
	m.saved = append(m.saved, item)
	m.schedules = append([]domainworkstream.HeartbeatSchedule{item}, m.schedules...)
	return nil
}

func (m *memoryWorkstreamHeartbeatStore) ListSteeringItems(_ context.Context, _ int) ([]domainworkstream.SteeringItem, error) {
	return append([]domainworkstream.SteeringItem(nil), m.steering...), nil
}

func (m *memoryWorkstreamHeartbeatStore) SaveSteeringItem(_ context.Context, item domainworkstream.SteeringItem) error {
	m.savedSteering = append(m.savedSteering, item)
	m.steering = append([]domainworkstream.SteeringItem{item}, m.steering...)
	return nil
}

func (m *memoryWorkstreamHeartbeatStore) SaveVaultUpdateLog(_ context.Context, item domainworkstream.VaultUpdateLog) error {
	m.vaultUpdates = append(m.vaultUpdates, item)
	return nil
}

func (m *memoryBacklogStore) List(_ context.Context, _ int) ([]domainbacklog.Item, error) {
	return append([]domainbacklog.Item(nil), m.items...), nil
}

func (m *memoryBacklogStore) Save(_ context.Context, item domainbacklog.Item) error {
	m.saved = append(m.saved, item)
	next := append([]domainbacklog.Item(nil), m.items...)
	for idx, existing := range next {
		if existing.ItemID == item.ItemID {
			next[idx] = item
			m.items = next
			return nil
		}
	}
	m.items = append([]domainbacklog.Item{item}, next...)
	return nil
}

func TestRunIdleChatSequenceCheckEmitsRecoveredEvent(t *testing.T) {
	dir := t.TempDir()
	listener := &recordingEventListener{}
	monitor := &fakeIdleChatSequenceMonitor{
		report: heartbeatSequenceMonitorReport{
			Status:     "recovered",
			Active:     true,
			Recovered:  true,
			Stage:      "tts_wait",
			Detail:     "Ren->Mio turn=2",
			SessionID:  "idle-1-topic-00",
			Generation: 7,
			AgeSeconds: 180,
			Action:     "interrupt_idlechat_and_clear_active_state_and_reset_tts_queue",
		},
	}
	svc := NewHeartbeatService(&mockWorkerAgent{response: "HEARTBEAT_OK"}, &mockSender{}, dir, 30).
		WithEventListener(listener).
		WithIdleChatSequenceMonitor(monitor)

	report := svc.runIdleChatSequenceCheck(context.Background(), time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	if !monitor.called {
		t.Fatal("expected idlechat monitor to be called")
	}
	if report.Status != "recovered" || !report.Recovered {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(listener.events) != 1 {
		t.Fatalf("events len = %d, want 1", len(listener.events))
	}
	if listener.events[0].Type != "heartbeat.idlechat_sequence.recovered" {
		t.Fatalf("event type = %q", listener.events[0].Type)
	}
	if !strings.Contains(listener.events[0].Content, "stage=tts_wait") {
		t.Fatalf("event content missing stage: %q", listener.events[0].Content)
	}
}

func TestNewHeartbeatService(t *testing.T) {
	t.Run("minimum interval is 5 minutes", func(t *testing.T) {
		svc := NewHeartbeatService(&mockWorkerAgent{}, &mockSender{}, "/tmp", 1)
		if svc.interval != 5*time.Minute {
			t.Errorf("expected 5m, got %v", svc.interval)
		}
	})

	t.Run("normal interval", func(t *testing.T) {
		svc := NewHeartbeatService(&mockWorkerAgent{}, &mockSender{}, "/tmp", 30)
		if svc.interval != 30*time.Minute {
			t.Errorf("expected 30m, got %v", svc.interval)
		}
	})
}

func TestTick_HeartbeatOKEmitsViewerEvent(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("Check system status"), 0644)

	listener := &recordingEventListener{}
	agent := &mockWorkerAgent{response: "HEARTBEAT_OK"}
	svc := NewHeartbeatService(agent, &mockSender{}, dir, 30).WithEventListener(listener)

	if err := svc.tick(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(listener.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(listener.events))
	}
	ev := listener.events[0]
	if ev.Type != "heartbeat.ok" || ev.From != "heartbeat" || ev.Channel != "heartbeat" || ev.Route != "HEARTBEAT" {
		t.Fatalf("unexpected event: %+v", ev)
	}
	if ev.Content != "silent" {
		t.Fatalf("unexpected event content: %q", ev.Content)
	}
}

func TestTick_NotificationEmitsViewerEvent(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("Check alerts"), 0644)

	listener := &recordingEventListener{}
	agent := &mockWorkerAgent{response: "Disk usage is 95%"}
	svc := NewHeartbeatService(agent, &mockSender{}, dir, 30).WithEventListener(listener)

	if err := svc.tick(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(listener.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(listener.events))
	}
	ev := listener.events[0]
	if ev.Type != "heartbeat.notify" {
		t.Fatalf("expected heartbeat.notify, got %+v", ev)
	}
	if ev.Content != "Disk usage is 95%" {
		t.Fatalf("unexpected event content: %q", ev.Content)
	}
}

func TestTick_MissingFileEmitsViewerSkipEvent(t *testing.T) {
	dir := t.TempDir()
	listener := &recordingEventListener{}
	svc := NewHeartbeatService(&mockWorkerAgent{response: "HEARTBEAT_OK"}, &mockSender{}, dir, 30).WithEventListener(listener)

	if err := svc.tick(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(listener.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(listener.events))
	}
	if listener.events[0].Type != "heartbeat.skip" {
		t.Fatalf("expected heartbeat.skip, got %+v", listener.events[0])
	}
}

func TestRunBacklogIntakePromotesOpenItemToWorkstream(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	backlogStore := &memoryBacklogStore{items: []domainbacklog.Item{
		{
			ItemID:    "normal-old",
			Kind:      "unimplemented",
			Title:     "通常項目",
			Body:      "通常優先度の実装",
			Source:    "user",
			Status:    "open",
			Priority:  "normal",
			CreatedAt: "2026-06-20T00:00:00Z",
			UpdatedAt: "2026-06-20T00:00:00Z",
		},
		{
			ItemID:    "urgent-new",
			Kind:      "unimplemented",
			Title:     "緊急項目",
			Body:      "緊急優先度の実装",
			Source:    "mio",
			Status:    "open",
			Priority:  "urgent",
			CreatedAt: "2026-06-21T00:00:00Z",
			UpdatedAt: "2026-06-21T00:00:00Z",
		},
		{
			ItemID:    "done",
			Kind:      "unimplemented",
			Title:     "完了済み",
			Source:    "coder",
			Status:    "ok",
			Priority:  "urgent",
			CheckOK:   true,
			CreatedAt: "2026-06-19T00:00:00Z",
			UpdatedAt: "2026-06-19T00:00:00Z",
		},
	}}
	workstreamStore := &memoryWorkstreamHeartbeatStore{}
	listener := &recordingEventListener{}
	svc := NewHeartbeatService(&mockWorkerAgent{}, &mockSender{}, t.TempDir(), 30).
		WithBacklogStore(backlogStore).
		WithWorkstreamStore(workstreamStore).
		WithEventListener(listener)

	report, err := svc.RunBacklogIntake(context.Background(), now)
	if err != nil {
		t.Fatalf("RunBacklogIntake: %v", err)
	}
	if report.Promoted != 1 || report.ItemID != "urgent-new" {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(workstreamStore.workstreams) != 1 || workstreamStore.workstreams[0].WorkstreamID != "ws_backlog_urgent-new" {
		t.Fatalf("workstream not created: %+v", workstreamStore.workstreams)
	}
	if len(workstreamStore.goals) != 1 || workstreamStore.goals[0].WorkstreamID != "ws_backlog_urgent-new" {
		t.Fatalf("goal not created: %+v", workstreamStore.goals)
	}
	if len(workstreamStore.artifacts) != 1 || workstreamStore.artifacts[0].WorkstreamID != "ws_backlog_urgent-new" {
		t.Fatalf("artifact not created: %+v", workstreamStore.artifacts)
	}
	if len(backlogStore.saved) != 1 || backlogStore.saved[0].Status != "implementing" || backlogStore.saved[0].Implementer != "coder" {
		t.Fatalf("backlog not updated: %+v", backlogStore.saved)
	}
	if !strings.Contains(backlogStore.saved[0].Implementation, "ws_backlog_urgent-new") {
		t.Fatalf("implementation note missing workstream id: %q", backlogStore.saved[0].Implementation)
	}
	if len(listener.events) != 1 || listener.events[0].Type != "backlog.intake.promoted" {
		t.Fatalf("event not emitted: %+v", listener.events)
	}
}

func TestRunBacklogIntakeSkipsWithoutRunnableItems(t *testing.T) {
	backlogStore := &memoryBacklogStore{items: []domainbacklog.Item{
		{ItemID: "active", Title: "実装中", Status: "implementing", Priority: "urgent"},
		{ItemID: "done", Title: "完了", Status: "ok", Priority: "urgent", CheckOK: true},
	}}
	workstreamStore := &memoryWorkstreamHeartbeatStore{}
	svc := NewHeartbeatService(&mockWorkerAgent{}, &mockSender{}, t.TempDir(), 30).
		WithBacklogStore(backlogStore).
		WithWorkstreamStore(workstreamStore)

	report, err := svc.RunBacklogIntake(context.Background(), time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("RunBacklogIntake: %v", err)
	}
	if report.Promoted != 0 || report.Skipped != 2 || report.Active != 1 || report.ItemID != "active" {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(workstreamStore.workstreams) != 0 || len(backlogStore.saved) != 0 {
		t.Fatalf("unexpected writes: workstreams=%+v backlog=%+v", workstreamStore.workstreams, backlogStore.saved)
	}
}

func TestRunBacklogIntakeDoesNotPromoteNextItemWhileActiveItemExists(t *testing.T) {
	backlogStore := &memoryBacklogStore{items: []domainbacklog.Item{
		{
			ItemID:    "active",
			Title:     "実装中",
			Status:    "implementing",
			Priority:  "normal",
			UpdatedAt: "2026-06-21T00:00:00Z",
		},
		{
			ItemID:    "next-high",
			Title:     "次の高優先",
			Status:    "open",
			Priority:  "high",
			UpdatedAt: "2026-06-20T00:00:00Z",
		},
	}}
	workstreamStore := &memoryWorkstreamHeartbeatStore{}
	listener := &recordingEventListener{}
	svc := NewHeartbeatService(&mockWorkerAgent{}, &mockSender{}, t.TempDir(), 30).
		WithBacklogStore(backlogStore).
		WithWorkstreamStore(workstreamStore).
		WithEventListener(listener)

	report, err := svc.RunBacklogIntake(context.Background(), time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("RunBacklogIntake: %v", err)
	}
	if report.Promoted != 0 || report.Active != 1 || report.ItemID != "active" {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(workstreamStore.workstreams) != 0 || len(backlogStore.saved) != 0 {
		t.Fatalf("active item should block new promotion: workstreams=%+v backlog=%+v", workstreamStore.workstreams, backlogStore.saved)
	}
	if len(listener.events) != 1 || listener.events[0].Type != "backlog.runner.waiting_active" {
		t.Fatalf("waiting event not emitted: %+v", listener.events)
	}
}

func TestRunBacklogRunnerStartsActiveItemOnce(t *testing.T) {
	backlogStore := &memoryBacklogStore{items: []domainbacklog.Item{
		{
			ItemID:   "active",
			Title:    "P01 terminal outcome",
			Body:     "visible terminal outcome を実装する",
			Status:   "implementing",
			Priority: "high",
		},
		{
			ItemID:   "next",
			Title:    "次の項目",
			Status:   "open",
			Priority: "high",
		},
	}}
	agent := &mockWorkerAgent{response: "accepted"}
	listener := &recordingEventListener{}
	svc := NewHeartbeatService(agent, &mockSender{}, t.TempDir(), 30).
		WithBacklogStore(backlogStore).
		WithEventListener(listener)

	report, err := svc.RunBacklogRunner(context.Background(), time.Date(2026, 6, 22, 5, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("RunBacklogRunner: %v", err)
	}
	if report.Started != 1 || report.ItemID != "active" {
		t.Fatalf("unexpected report: %+v", report)
	}
	if !agent.called || !strings.Contains(agent.lastMsg, "/code2 Backlog item active") || !strings.Contains(agent.lastMsg, "status=ok") {
		t.Fatalf("runner did not send code2 backlog prompt: %q", agent.lastMsg)
	}
	if len(backlogStore.saved) != 1 || !strings.Contains(backlogStore.saved[0].Implementation, backlogRunnerStartedMarker) {
		t.Fatalf("runner start not persisted: %+v", backlogStore.saved)
	}
	if len(listener.events) != 1 || listener.events[0].Type != "backlog.runner.started" {
		t.Fatalf("runner event not emitted: %+v", listener.events)
	}

	agent.called = false
	second, err := svc.RunBacklogRunner(context.Background(), time.Date(2026, 6, 22, 5, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("RunBacklogRunner second: %v", err)
	}
	if second.Started != 0 || agent.called {
		t.Fatalf("runner should not start same active item twice: report=%+v called=%t", second, agent.called)
	}
}

func TestBacklogActiveItemsKeepsStartedRunnerFirst(t *testing.T) {
	items := []domainbacklog.Item{
		{
			ItemID:         "older-active",
			Title:          "older",
			Status:         "implementing",
			Priority:       "high",
			Implementation: "promoted earlier",
			UpdatedAt:      "2026-06-20T00:00:00Z",
		},
		{
			ItemID:         "started-active",
			Title:          "started",
			Status:         "implementing",
			Priority:       "high",
			Implementation: backlogRunnerStartedMarker + " item_id=started-active at 2026-06-22T06:03:03Z.",
			UpdatedAt:      "2026-06-22T06:03:03Z",
		},
	}

	active := backlogActiveItems(items)
	if len(active) != 2 || active[0].ItemID != "started-active" {
		t.Fatalf("started runner item should stay first while active: %+v", active)
	}
}

func TestRunBacklogRunnerBlocksItemWhenWorkerStartFails(t *testing.T) {
	backlogStore := &memoryBacklogStore{items: []domainbacklog.Item{
		{ItemID: "active", Title: "実装中", Status: "implementing", Priority: "high"},
	}}
	agent := &mockWorkerAgent{err: context.Canceled}
	svc := NewHeartbeatService(agent, &mockSender{}, t.TempDir(), 30).WithBacklogStore(backlogStore)

	report, err := svc.RunBacklogRunner(context.Background(), time.Date(2026, 6, 22, 5, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("expected worker start error")
	}
	if report.Failed != 1 || report.ItemID != "active" {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(backlogStore.saved) < 2 {
		t.Fatalf("expected start and blocked saves, got %+v", backlogStore.saved)
	}
	last := backlogStore.saved[len(backlogStore.saved)-1]
	if last.Status != "blocked" || !strings.Contains(last.TestResult, "Backlog Runner failed to start") {
		t.Fatalf("expected blocked item with reason, got %+v", last)
	}
}

func TestTick_HeartbeatOK(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("Check system status"), 0644)

	agent := &mockWorkerAgent{response: "HEARTBEAT_OK"}
	sender := &mockSender{}
	svc := NewHeartbeatService(agent, sender, dir, 30)

	err := svc.tick(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !agent.called {
		t.Error("expected heartbeat worker to be called")
	}
	if len(sender.messages) != 0 {
		t.Errorf("expected no notification, got %d", len(sender.messages))
	}

	logData, _ := os.ReadFile(filepath.Join(dir, "heartbeat.log"))
	if !strings.Contains(string(logData), "[OK]") {
		t.Error("expected [OK] in heartbeat.log")
	}
}

func TestTick_HeartbeatUsesShiroWorkerRoute(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "persona"), 0755)
	os.WriteFile(filepath.Join(dir, "persona", "mio.md"), []byte("Mio persona"), 0644)
	os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("Check system status"), 0644)

	agent := &mockWorkerAgent{response: "HEARTBEAT_OK"}
	svc := NewHeartbeatService(agent, &mockSender{}, dir, 30)

	if err := svc.tick(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !agent.executed {
		t.Fatal("expected Heartbeat to execute through Shiro/worker")
	}
	if agent.chatCalled {
		t.Fatal("Heartbeat must not use the Mio/chat path")
	}
	if agent.lastTask.Route() != routing.RouteOPS || agent.lastTask.ForcedRoute() != routing.RouteOPS {
		t.Fatalf("expected OPS route task, got route=%q forced=%q", agent.lastTask.Route(), agent.lastTask.ForcedRoute())
	}
	if strings.Contains(agent.lastMsg, "Mio persona") {
		t.Fatalf("Heartbeat OPS prompt must not include Mio chat persona: %q", agent.lastMsg)
	}
}

func TestTick_Notification(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("Check alerts"), 0644)

	agent := &mockWorkerAgent{response: "Disk usage is 95%"}
	sender := &mockSender{}
	svc := NewHeartbeatService(agent, sender, dir, 30)

	err := svc.tick(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sender.messages) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(sender.messages))
	}
	if sender.messages[0] != "Disk usage is 95%" {
		t.Errorf("expected 'Disk usage is 95%%', got %q", sender.messages[0])
	}
}

func TestTick_NoFile(t *testing.T) {
	dir := t.TempDir()

	agent := &mockWorkerAgent{response: "HEARTBEAT_OK"}
	sender := &mockSender{}
	svc := NewHeartbeatService(agent, sender, dir, 30)

	err := svc.tick(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if agent.called {
		t.Error("expected heartbeat worker NOT to be called when file is missing")
	}
}

func TestTick_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("   \n  "), 0644)

	agent := &mockWorkerAgent{response: "HEARTBEAT_OK"}
	sender := &mockSender{}
	svc := NewHeartbeatService(agent, sender, dir, 30)

	err := svc.tick(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if agent.called {
		t.Error("expected heartbeat worker NOT to be called for empty file")
	}
}

func TestTick_WorkerError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("Check"), 0644)

	agent := &mockWorkerAgent{err: context.DeadlineExceeded}
	sender := &mockSender{}
	svc := NewHeartbeatService(agent, sender, dir, 30)

	err := svc.tick(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "worker failed") {
		t.Errorf("expected 'worker failed' error, got: %v", err)
	}
}

func TestRunDueWorkstreamHeartbeatsCreatesDraftReportAndPendingVaultUpdate(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &memoryWorkstreamHeartbeatStore{
		schedules: []domainworkstream.HeartbeatSchedule{{
			HeartbeatID:  "hb_revenue_daily",
			WorkstreamID: "ws_revenue",
			ScheduleText: "daily 08:00",
			Task:         "昨日の投稿反応を確認する",
			Status:       domainworkstream.StatusActive,
			NextRunAt:    now.Add(-time.Minute),
			CreatedAt:    now.Add(-24 * time.Hour),
		}},
	}
	listener := &recordingEventListener{}
	agent := &mockWorkerAgent{response: "draft report body"}
	sender := &mockSender{}
	svc := NewHeartbeatService(agent, sender, dir, 30).
		WithWorkstreamStore(store).
		WithEventListener(listener)

	report, err := svc.RunDueWorkstreamHeartbeats(context.Background(), now)
	if err != nil {
		t.Fatalf("RunDueWorkstreamHeartbeats failed: %v", err)
	}
	if report.Checked != 1 || report.Run != 1 || report.Failed != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(sender.messages) != 0 {
		t.Fatalf("workstream heartbeat must be draft-only, sent notifications: %#v", sender.messages)
	}
	if len(store.vaultUpdates) != 1 {
		t.Fatalf("expected pending vault update, got %#v", store.vaultUpdates)
	}
	if store.vaultUpdates[0].ReviewStatus != "pending" || store.vaultUpdates[0].UpdateType != "heartbeat_draft_report" {
		t.Fatalf("unexpected vault update: %#v", store.vaultUpdates[0])
	}
	body, err := os.ReadFile(store.vaultUpdates[0].FilePath)
	if err != nil {
		t.Fatalf("read draft report: %v", err)
	}
	if !strings.Contains(string(body), "draft report body") || !strings.Contains(string(body), "昨日の投稿反応を確認する") {
		t.Fatalf("unexpected draft report body: %s", string(body))
	}
	if len(store.saved) != 1 || store.saved[0].LastRunAt.IsZero() || !store.saved[0].NextRunAt.After(now) {
		t.Fatalf("expected updated schedule with next run, got %#v", store.saved)
	}
	if !agent.executed || !strings.Contains(agent.lastMsg, "draft report only") {
		t.Fatalf("expected draft-only task sent to worker agent, got executed=%v msg=%q", agent.executed, agent.lastMsg)
	}
	if agent.chatCalled {
		t.Fatal("workstream heartbeat must not use the Mio/chat path")
	}
	if agent.lastTask.Route() != routing.RouteOPS || agent.lastTask.ForcedRoute() != routing.RouteOPS {
		t.Fatalf("expected OPS route task, got route=%q forced=%q", agent.lastTask.Route(), agent.lastTask.ForcedRoute())
	}
}

func TestRunDueWorkstreamHeartbeatsCreatesRevenueDailyRoutineDraftReport(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	workstreamStore := &memoryWorkstreamHeartbeatStore{
		schedules: []domainworkstream.HeartbeatSchedule{{
			HeartbeatID:  "hb_revenue_daily",
			WorkstreamID: "ws_revenue",
			ScheduleText: "daily 08:00",
			Task:         "収益化の日次ルーチンとして市場調査と顧客の声を確認する",
			Status:       domainworkstream.StatusActive,
			NextRunAt:    now.Add(-time.Minute),
			CreatedAt:    now.Add(-24 * time.Hour),
		}},
	}
	revenueStore := &memoryRevenueDailyRoutineStore{
		market: []domainrevenue.MarketResearchItem{{ItemID: "mkt_1", SourcePlatform: "note"}},
		posts:  []domainrevenue.SNSPostMetric{{PostID: "post_1", Platform: "x"}},
		products: []domainrevenue.Product{{
			ProductID:   "prod_1",
			ProductName: "商品設計シート",
			Status:      "draft",
		}},
		voices:    []domainrevenue.CustomerVoice{{VoiceID: "voice_1", RawText: "ここがわからない", PermissionStatus: "unknown"}},
		events:    []domainrevenue.RevenueEvent{{EventID: "rev_1", EventType: "purchase", Amount: 980, CustomerID: "cust_1"}},
		decisions: []domainrevenue.HumanDecisionGateRecord{{DecisionID: "dec_1", DecisionType: "external_publish", ApprovalStatus: "pending", GateStatus: "needs_review"}},
	}
	sender := &mockSender{}
	svc := NewHeartbeatService(&mockWorkerAgent{response: "revenue draft"}, sender, dir, 30).
		WithWorkstreamStore(workstreamStore).
		WithRevenueDailyRoutineStore(revenueStore)

	report, err := svc.RunDueWorkstreamHeartbeats(context.Background(), now)
	if err != nil {
		t.Fatalf("RunDueWorkstreamHeartbeats failed: %v", err)
	}
	if report.Run != 1 {
		t.Fatalf("unexpected run report: %+v", report)
	}
	if len(sender.messages) != 0 {
		t.Fatalf("revenue heartbeat must not send external notifications: %#v", sender.messages)
	}
	if len(revenueStore.reports) != 1 {
		t.Fatalf("expected revenue daily routine report, got %#v", revenueStore.reports)
	}
	daily := revenueStore.reports[0]
	if daily.Status != "draft_report" || daily.ExternalSendApplied {
		t.Fatalf("expected draft-only revenue report: %#v", daily)
	}
	if daily.WorkstreamID != "ws_revenue" || daily.MarketResearch != 1 || daily.SNSPosts != 1 || daily.Products != 1 || daily.CustomerVoices != 1 || daily.RevenueEvents != 1 || daily.PaidCustomers != 1 || daily.PendingDecisions != 1 {
		t.Fatalf("unexpected revenue daily routine report: %#v", daily)
	}
}

func TestRunDueWorkstreamHeartbeatsRecordsSkillBootstrap(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	workstreamStore := &memoryWorkstreamHeartbeatStore{
		schedules: []domainworkstream.HeartbeatSchedule{{
			HeartbeatID:  "hb_1",
			WorkstreamID: "ws_1",
			ScheduleText: "daily 08:00",
			Task:         "作業ログを確認する",
			Status:       domainworkstream.StatusActive,
			NextRunAt:    now.Add(-time.Minute),
			CreatedAt:    now,
		}},
	}
	skillStore := &heartbeatSkillStore{
		manifests: []domainskill.SkillManifest{{
			SkillID:        "core.workstream-heartbeat",
			Enabled:        true,
			IntentTriggers: []string{"workstream_heartbeat"},
		}},
	}
	skills := skillbootstrap.NewBootstrapService(skillStore).WithNow(func() time.Time { return now })
	svc := NewHeartbeatService(&mockWorkerAgent{response: "draft"}, &mockSender{}, dir, 30).
		WithWorkstreamStore(workstreamStore).
		WithSkillBootstrap(skills)

	if _, err := svc.RunDueWorkstreamHeartbeats(context.Background(), now); err != nil {
		t.Fatalf("RunDueWorkstreamHeartbeats failed: %v", err)
	}
	if len(skillStore.logs) != 1 {
		t.Fatalf("expected skill bootstrap log, got %#v", skillStore.logs)
	}
	if skillStore.logs[0].SkillID != "core.workstream-heartbeat" || skillStore.logs[0].Status != domainskill.TriggerStatusTriggered {
		t.Fatalf("unexpected skill log: %#v", skillStore.logs[0])
	}
	if skillStore.logs[0].WorkstreamID != "ws_1" {
		t.Fatalf("expected workstream id in skill log, got %#v", skillStore.logs[0])
	}
}

func TestRunDueWorkstreamHeartbeatsAppliesPendingSteeringAtSafeCheckpoint(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &memoryWorkstreamHeartbeatStore{
		schedules: []domainworkstream.HeartbeatSchedule{{
			HeartbeatID:  "hb_1",
			WorkstreamID: "ws_1",
			ScheduleText: "daily 08:00",
			Task:         "作業ログを確認する",
			Status:       domainworkstream.StatusActive,
			NextRunAt:    now.Add(-time.Minute),
			CreatedAt:    now,
		}},
		steering: []domainworkstream.SteeringItem{
			{
				SteeringID:       "stq_1",
				WorkstreamID:     "ws_1",
				TargetArtifactID: "art_1",
				Instruction:      "見出しを具体化する",
				Status:           "pending",
				CreatedAt:        now.Add(-time.Hour),
			},
			{
				SteeringID:   "stq_other",
				WorkstreamID: "ws_other",
				Instruction:  "別workstream",
				Status:       "pending",
				CreatedAt:    now.Add(-time.Hour),
			},
		},
	}
	agent := &mockWorkerAgent{response: "draft"}
	svc := NewHeartbeatService(agent, &mockSender{}, dir, 30).WithWorkstreamStore(store)

	if _, err := svc.RunDueWorkstreamHeartbeats(context.Background(), now); err != nil {
		t.Fatalf("RunDueWorkstreamHeartbeats failed: %v", err)
	}
	if !strings.Contains(agent.lastMsg, "stq_1 [art_1]: 見出しを具体化する") {
		t.Fatalf("expected pending steering in prompt, got %q", agent.lastMsg)
	}
	if strings.Contains(agent.lastMsg, "stq_other") {
		t.Fatalf("other workstream steering leaked into prompt: %q", agent.lastMsg)
	}
	if len(store.savedSteering) != 1 {
		t.Fatalf("expected one applied steering, got %#v", store.savedSteering)
	}
	applied := store.savedSteering[0]
	if applied.SteeringID != "stq_1" || applied.Status != "applied" || applied.AppliedAt.IsZero() {
		t.Fatalf("unexpected applied steering: %#v", applied)
	}
}

func TestRunDueWorkstreamHeartbeatsSkipsInactiveOrFutureSchedules(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &memoryWorkstreamHeartbeatStore{
		schedules: []domainworkstream.HeartbeatSchedule{
			{
				HeartbeatID:  "hb_paused",
				WorkstreamID: "ws_1",
				ScheduleText: "daily 08:00",
				Task:         "paused",
				Status:       domainworkstream.StatusPaused,
				NextRunAt:    now.Add(-time.Minute),
				CreatedAt:    now,
			},
			{
				HeartbeatID:  "hb_future",
				WorkstreamID: "ws_1",
				ScheduleText: "daily 08:00",
				Task:         "future",
				Status:       domainworkstream.StatusActive,
				NextRunAt:    now.Add(time.Hour),
				CreatedAt:    now,
			},
		},
	}
	agent := &mockWorkerAgent{response: "should not run"}
	svc := NewHeartbeatService(agent, &mockSender{}, t.TempDir(), 30).WithWorkstreamStore(store)

	report, err := svc.RunDueWorkstreamHeartbeats(context.Background(), now)
	if err != nil {
		t.Fatalf("RunDueWorkstreamHeartbeats failed: %v", err)
	}
	if report.Checked != 2 || report.Run != 0 || report.Skipped != 2 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if agent.called {
		t.Fatal("expected no worker call for skipped schedules")
	}
}

func TestTick_NilSender(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("Check"), 0644)

	agent := &mockWorkerAgent{response: "Alert: something is wrong"}
	svc := NewHeartbeatService(agent, nil, dir, 30)

	err := svc.tick(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStartStop(t *testing.T) {
	agent := &mockWorkerAgent{response: "HEARTBEAT_OK"}
	sender := &mockSender{}
	svc := NewHeartbeatService(agent, sender, t.TempDir(), 5)

	svc.Start()
	svc.Start() // 二重起動しないこと

	time.Sleep(50 * time.Millisecond)
	svc.Stop()
	svc.Stop() // 二重停止しないこと
}

func TestContextBuilder_WithWorkspaceFiles(t *testing.T) {
	dir := t.TempDir()

	// workspace ファイル群を作成
	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("Agent rules here"), 0644)
	os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte("Soul values here"), 0644)
	os.WriteFile(filepath.Join(dir, "IDENTITY.md"), []byte("Identity info"), 0644)
	os.WriteFile(filepath.Join(dir, "USER.md"), []byte("User prefs"), 0644)
	os.MkdirAll(filepath.Join(dir, "persona"), 0755)
	os.WriteFile(filepath.Join(dir, "persona", "mio.md"), []byte("Mio persona"), 0644)

	// skills
	os.MkdirAll(filepath.Join(dir, "skills", "weather"), 0755)
	os.WriteFile(filepath.Join(dir, "skills", "weather", "SKILL.md"), []byte("# Weather lookup"), 0644)

	svc := NewHeartbeatService(&mockWorkerAgent{}, &mockSender{}, dir, 30)

	// tick 経由で ContextBuilder が使われることを確認
	os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("Check system status"), 0644)
	agent := svc.workerAgent.(*mockWorkerAgent)
	agent.response = "HEARTBEAT_OK"

	err := svc.tick(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msg := agent.lastMsg

	// workspace コンテキストが含まれること（Heartbeat は OPS ルートなので ChatOnly は含まない）
	if !strings.Contains(msg, "# AGENT\nAgent rules here") {
		t.Error("expected AGENT.md content")
	}
	if strings.Contains(msg, "Mio persona") || strings.Contains(msg, "# SOUL\nSoul values here") {
		t.Fatal("expected Heartbeat OPS context to exclude Mio chat persona/SOUL")
	}
	if !strings.Contains(msg, "# IDENTITY\nIdentity info") {
		t.Error("expected IDENTITY.md content")
	}
	if !strings.Contains(msg, "weather: Weather lookup") {
		t.Error("expected skills summary")
	}

	// HEARTBEAT タスクが末尾にあること
	if !strings.Contains(msg, "# HEARTBEAT TASKS\nCheck system status") {
		t.Error("expected HEARTBEAT TASKS section")
	}

	// コンテキストとタスクの区切り
	if !strings.Contains(msg, "===") {
		t.Error("expected separator between context and tasks")
	}
}

func TestContextBuilder_NoWorkspaceFiles(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("Check system status"), 0644)

	agent := &mockWorkerAgent{response: "HEARTBEAT_OK"}
	svc := NewHeartbeatService(agent, &mockSender{}, dir, 30)

	err := svc.tick(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// workspace ファイルがなければ HEARTBEAT タスクのみ
	if agent.lastMsg != "Check system status" {
		t.Errorf("expected plain heartbeat content, got: %q", agent.lastMsg)
	}
}

func TestTick_WithWorkspaceContext(t *testing.T) {
	dir := t.TempDir()

	// workspace + HEARTBEAT.md
	os.WriteFile(filepath.Join(dir, "AGENT.md"), []byte("Be concise"), 0644)
	os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("Check alerts"), 0644)

	agent := &mockWorkerAgent{response: "HEARTBEAT_OK"}
	svc := NewHeartbeatService(agent, &mockSender{}, dir, 30)

	err := svc.tick(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Shiro workerに送信されたメッセージにworkspaceコンテキストが含まれること
	if !strings.Contains(agent.lastMsg, "# AGENT\nBe concise") {
		t.Error("expected workspace context in message sent to agent")
	}
	if !strings.Contains(agent.lastMsg, "# HEARTBEAT TASKS\nCheck alerts") {
		t.Error("expected heartbeat tasks in message sent to agent")
	}
}
