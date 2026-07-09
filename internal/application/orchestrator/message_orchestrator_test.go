package orchestrator

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	appverification "github.com/Nyukimin/RenCrow_CORE/internal/application/verification"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainverification "github.com/Nyukimin/RenCrow_CORE/internal/domain/verification"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

// mockSessionRepository はテスト用のSessionRepository（エラー注入対応）
type mockSessionRepository struct {
	sessions map[string]*session.Session
	loadErr  error // non-nil ならLoad時にこのエラーを返す
	saveErr  error // non-nil ならSave時にこのエラーを返す
}

type mockSkillBootstrapRecorder struct {
	tasks []domainskill.TaskContext
	used  [][]string
	err   error
}

type mockPersonaRuntimeRecorder struct {
	triggers     []domainpersona.TriggerLog
	canonical    []domainpersona.CanonicalResponseLog
	observations []domainpersona.ObservationLog
	metaUpdates  []domainpersona.MetaProfileUpdate
	sessions     []domainpersona.InterfaceSession
	err          error
}

func (m *mockPersonaRuntimeRecorder) SaveTriggerLog(_ context.Context, item domainpersona.TriggerLog) error {
	if m.err != nil {
		return m.err
	}
	m.triggers = append(m.triggers, item)
	return nil
}

func (m *mockPersonaRuntimeRecorder) SaveCanonicalResponseLog(_ context.Context, item domainpersona.CanonicalResponseLog) error {
	if m.err != nil {
		return m.err
	}
	m.canonical = append(m.canonical, item)
	return nil
}

func (m *mockPersonaRuntimeRecorder) ListCanonicalResponseLogs(_ context.Context, _ int) ([]domainpersona.CanonicalResponseLog, error) {
	if m.err != nil {
		return nil, m.err
	}
	return append([]domainpersona.CanonicalResponseLog(nil), m.canonical...), nil
}

func (m *mockPersonaRuntimeRecorder) SaveObservationLog(_ context.Context, item domainpersona.ObservationLog) error {
	if m.err != nil {
		return m.err
	}
	m.observations = append(m.observations, item)
	return nil
}

func (m *mockPersonaRuntimeRecorder) SaveMetaProfileUpdate(_ context.Context, item domainpersona.MetaProfileUpdate) error {
	if m.err != nil {
		return m.err
	}
	m.metaUpdates = append(m.metaUpdates, item)
	return nil
}

func (m *mockPersonaRuntimeRecorder) SaveInterfaceSession(_ context.Context, item domainpersona.InterfaceSession) error {
	if m.err != nil {
		return m.err
	}
	m.sessions = append(m.sessions, item)
	return nil
}

func (m *mockSkillBootstrapRecorder) Record(_ context.Context, task domainskill.TaskContext, usedSkillIDs []string) ([]domainskill.SkillTriggerLog, error) {
	if m.err != nil {
		return nil, m.err
	}
	m.tasks = append(m.tasks, task)
	m.used = append(m.used, append([]string(nil), usedSkillIDs...))
	return nil, nil
}

func newMockSessionRepository() *mockSessionRepository {
	return &mockSessionRepository{
		sessions: make(map[string]*session.Session),
	}
}

func (m *mockSessionRepository) Save(ctx context.Context, sess *session.Session) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.sessions[sess.ID()] = sess
	return nil
}

func (m *mockSessionRepository) Load(ctx context.Context, id string) (*session.Session, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	sess, exists := m.sessions[id]
	if !exists {
		return nil, session.ErrSessionNotFound
	}
	return sess, nil
}

func (m *mockSessionRepository) Exists(ctx context.Context, id string) (bool, error) {
	_, exists := m.sessions[id]
	return exists, nil
}

func (m *mockSessionRepository) Delete(ctx context.Context, id string) error {
	delete(m.sessions, id)
	return nil
}

// mockMioAgent はテスト用のMioAgent（function pointer でエラー注入可能）
type mockMioAgent struct {
	decision   routing.Decision
	response   string
	decideFunc func(ctx context.Context, t task.Task) (routing.Decision, error)
	chatFunc   func(ctx context.Context, t task.Task) (string, error)
	cmdFunc    func(ctx context.Context, sessionID string, message string) (agent.ChatCommandResult, error)
}

func (m *mockMioAgent) DecideAction(ctx context.Context, t task.Task) (routing.Decision, error) {
	if m.decideFunc != nil {
		return m.decideFunc(ctx, t)
	}
	return m.decision, nil
}

func (m *mockMioAgent) Chat(ctx context.Context, t task.Task) (string, error) {
	if m.chatFunc != nil {
		return m.chatFunc(ctx, t)
	}
	return m.response, nil
}

func (m *mockMioAgent) HandleChatCommand(ctx context.Context, sessionID string, message string) (agent.ChatCommandResult, error) {
	if m.cmdFunc != nil {
		return m.cmdFunc(ctx, sessionID, message)
	}
	return agent.ChatCommandResult{Handled: false}, nil
}

// mockShiroAgent はテスト用のShiroAgent
type mockShiroAgent struct {
	response    string
	executeFunc func(ctx context.Context, t task.Task) (string, error)
}

func (m *mockShiroAgent) Execute(ctx context.Context, t task.Task) (string, error) {
	if m.executeFunc != nil {
		return m.executeFunc(ctx, t)
	}
	return m.response, nil
}

// mockCoderAgent はテスト用のCoderAgent
type mockCoderAgent struct {
	response string
}

func (m *mockCoderAgent) Generate(ctx context.Context, t task.Task, systemPrompt string) (string, error) {
	return m.response, nil
}

type mockWildAgent struct {
	response string
	called   bool
}

func (m *mockWildAgent) Generate(ctx context.Context, t task.Task) (string, error) {
	m.called = true
	return m.response, nil
}

type mockDCISearcher struct {
	trigger bool
	result  domaindci.SearchResult
	err     error
	query   string
	calls   int
}

func (m *mockDCISearcher) ShouldTrigger(query string) bool {
	return m.trigger
}

func (m *mockDCISearcher) Search(ctx context.Context, query string) (domaindci.SearchResult, error) {
	m.calls++
	m.query = query
	return m.result, m.err
}

type mockRecallTraceStore struct {
	traces []domainconversation.RecallTrace
	err    error
}

func (m *mockRecallTraceStore) SaveRecallTrace(ctx context.Context, trace domainconversation.RecallTrace) error {
	if m.err != nil {
		return m.err
	}
	m.traces = append(m.traces, trace)
	return nil
}

type mockWorkflowEventRecorder struct {
	events []domainai.WorkflowEvent
}

func (m *mockWorkflowEventRecorder) SaveWorkflowEvent(ctx context.Context, item domainai.WorkflowEvent) error {
	m.events = append(m.events, item)
	return nil
}

type mockCommandRegistryStore struct {
	commands []domainai.CommandRegistry
	err      error
}

func (m *mockCommandRegistryStore) ListCommandRegistries(_ context.Context, _ int) ([]domainai.CommandRegistry, error) {
	if m.err != nil {
		return nil, m.err
	}
	return append([]domainai.CommandRegistry(nil), m.commands...), nil
}

type mockSuperAgentRuntimeRecorder struct {
	runs         []domainsuperagent.AgentRun
	contextPacks []domainsuperagent.ContextPack
	traces       []domainsuperagent.TraceEvent
	err          error
}

func (m *mockSuperAgentRuntimeRecorder) SaveAgentRun(_ context.Context, item domainsuperagent.AgentRun) error {
	if m.err != nil {
		return m.err
	}
	m.runs = append(m.runs, item)
	return nil
}

func (m *mockSuperAgentRuntimeRecorder) SaveContextPack(_ context.Context, item domainsuperagent.ContextPack) error {
	if m.err != nil {
		return m.err
	}
	m.contextPacks = append(m.contextPacks, item)
	return nil
}

func (m *mockSuperAgentRuntimeRecorder) SaveTraceEvent(_ context.Context, item domainsuperagent.TraceEvent) error {
	if m.err != nil {
		return m.err
	}
	m.traces = append(m.traces, item)
	return nil
}

type pauseRequestedRunController struct{}

func (pauseRequestedRunController) RegisterRun(ctx context.Context, _ string) (context.Context, func()) {
	runCtx, cancel := context.WithCancel(ctx)
	cancel()
	return runCtx, func() {}
}

func (pauseRequestedRunController) IsPauseRequested(_ string) bool {
	return true
}

type mockHeavyAgent struct {
	response string
	called   bool
}

func (m *mockHeavyAgent) Generate(ctx context.Context, t task.Task) (string, error) {
	m.called = true
	return m.response, nil
}

// mockWorkerExecutionService はテスト用のWorkerExecutionService
type mockWorkerExecutionService struct{}

func (m *mockWorkerExecutionService) ExecuteProposal(ctx context.Context, jobID task.JobID, p interface{}) (interface{}, error) {
	return nil, nil
}

type mockResponseVerifier struct {
	req    appverification.Request
	result appverification.Result
}

func (m *mockResponseVerifier) VerifyResponse(_ context.Context, req appverification.Request) (appverification.Result, error) {
	m.req = req
	if m.result.Response == "" {
		m.result.Response = req.DraftResponse
	}
	return m.result, nil
}

type mockTTSBridge struct {
	startReqs []TTSSessionStart
	pushes    []string
	emotions  []*moduletts.EmotionState
	ended     []string
	startErr  error
}

func (m *mockTTSBridge) StartSession(ctx context.Context, req TTSSessionStart) error {
	m.startReqs = append(m.startReqs, req)
	return m.startErr
}

func (m *mockTTSBridge) PushText(ctx context.Context, sessionID string, text string, emotion *moduletts.EmotionState) error {
	m.pushes = append(m.pushes, text)
	m.emotions = append(m.emotions, emotion)
	return nil
}

func (m *mockTTSBridge) EndSession(ctx context.Context, sessionID string) error {
	m.ended = append(m.ended, sessionID)
	return nil
}

func TestNewMessageOrchestrator(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1.0, "test"),
		response: "Hello",
	}
	shiro := &mockShiroAgent{response: "executed"}

	orchestrator := NewMessageOrchestrator(repo, mio, shiro, nil, nil, nil, nil, nil)

	if orchestrator == nil {
		t.Fatal("NewMessageOrchestrator should not return nil")
	}
}

func TestMessageOrchestrator_ProcessMessage_NewSession(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1.0, "Chat route"),
		response: "こんにちは！",
	}
	shiro := &mockShiroAgent{response: "executed"}

	orchestrator := NewMessageOrchestrator(repo, mio, shiro, nil, nil, nil, nil, nil)

	req := ProcessMessageRequest{
		SessionID:   "20260302-line-U123",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "こんにちは",
	}

	resp, err := orchestrator.ProcessMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	if resp.Response != "こんにちは！" {
		t.Errorf("Expected response 'こんにちは！', got '%s'", resp.Response)
	}

	if resp.Route != routing.RouteCHAT {
		t.Errorf("Expected route CHAT, got '%s'", resp.Route)
	}

	// セッションが保存されているか確認
	exists, _ := repo.Exists(context.Background(), "20260302-line-U123")
	if !exists {
		t.Error("Session should be saved")
	}
}

func TestMessageOrchestrator_RecordsPersonaRuntimeObservation(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1.0, "Chat route"),
		response: "少し分けて考えます。",
	}
	recorder := &mockPersonaRuntimeRecorder{}
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetPersonaRuntimeRecorder(recorder, []domainpersona.TriggerDefinition{{
		TriggerID:   "mio_tired",
		CharacterID: "mio",
		Category:    "tiredness",
		Keywords:    []string{"疲れた"},
		Priority:    1,
	}})

	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "session-1",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "今日は疲れた",
	})
	if err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	if len(recorder.sessions) != 1 || recorder.sessions[0].SessionKey != "line:U123" {
		t.Fatalf("sessions = %#v", recorder.sessions)
	}
	if len(recorder.observations) != 1 || recorder.observations[0].ReviewStatus != "pending" || recorder.observations[0].ObservationType != "chat_message" {
		t.Fatalf("observations = %#v", recorder.observations)
	}
	if len(recorder.triggers) != 1 || recorder.triggers[0].TriggerID != "mio_tired" || recorder.triggers[0].TriggerCategory != "tiredness" {
		t.Fatalf("triggers = %#v", recorder.triggers)
	}
}

func TestMessageOrchestrator_CreatesPendingMetaUpdateCandidateFromUserStatement(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1.0, "Chat route"),
		response: "記録候補にします。",
	}
	recorder := &mockPersonaRuntimeRecorder{}
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetPersonaRuntimeRecorder(recorder, nil)

	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "session-1",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "私は映画の話題をよくアイデア源にします",
	})
	if err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	if len(recorder.metaUpdates) != 1 {
		t.Fatalf("metaUpdates = %#v", recorder.metaUpdates)
	}
	got := recorder.metaUpdates[0]
	if got.TargetID != "ren" || got.ReviewStatus != "pending" || got.Section != "flow_observation" {
		t.Fatalf("unexpected meta update = %#v", got)
	}
	if !strings.Contains(got.ProposedContent, "Human review is required") || !strings.Contains(got.ProposedContent, "映画の話題") {
		t.Fatalf("proposed content = %q", got.ProposedContent)
	}
	if len(got.EvidenceRefs) == 0 {
		t.Fatalf("evidence refs missing: %#v", got)
	}
}

func TestMessageOrchestrator_AppliesPersonaCanonicalResponse(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1.0, "Chat route"),
		response: "元の応答",
	}
	recorder := &mockPersonaRuntimeRecorder{}
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetPersonaRuntimeRecorder(recorder, []domainpersona.TriggerDefinition{{
		TriggerID:   "kuro_destructive",
		CharacterID: "kuro",
		Category:    "danger",
		Keywords:    []string{"削除"},
		Priority:    1,
	}})
	orch.SetPersonaCanonicalResponses([]domainpersona.CanonicalResponseDefinition{{
		ResponseID:       "kuro_destructive_block",
		CharacterID:      "kuro",
		Category:         "danger",
		Response:         "その操作は止めます。",
		RequiredContexts: []string{"danger"},
		CooldownTurns:    5,
		MaxPerSession:    3,
		Priority:         10,
	}})

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "session-1",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "このファイルを削除して",
	})
	if err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	if resp.Response != "その操作は止めます。" {
		t.Fatalf("response = %q", resp.Response)
	}
	if len(recorder.canonical) != 1 || recorder.canonical[0].ResponseID != "kuro_destructive_block" || !recorder.canonical[0].Used || recorder.canonical[0].Rewritten {
		t.Fatalf("canonical logs = %#v", recorder.canonical)
	}
}

func TestMessageOrchestrator_CanonicalResponseHonorsCooldown(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1.0, "Chat route"),
		response: "元の応答",
	}
	recorder := &mockPersonaRuntimeRecorder{
		canonical: []domainpersona.CanonicalResponseLog{{
			EventID:     "evt_recent",
			CharacterID: "kuro",
			ResponseID:  "kuro_destructive_block",
			Used:        true,
			CreatedAt:   time.Now().UTC(),
		}},
	}
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetPersonaRuntimeRecorder(recorder, []domainpersona.TriggerDefinition{{
		TriggerID:   "kuro_destructive",
		CharacterID: "kuro",
		Category:    "danger",
		Keywords:    []string{"削除"},
		Priority:    1,
	}})
	orch.SetPersonaCanonicalResponses([]domainpersona.CanonicalResponseDefinition{{
		ResponseID:       "kuro_destructive_block",
		CharacterID:      "kuro",
		Category:         "danger",
		Response:         "その操作は止めます。",
		RequiredContexts: []string{"danger"},
		CooldownTurns:    5,
		MaxPerSession:    3,
		Priority:         10,
	}})

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "session-1",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "このファイルを削除して",
	})
	if err != nil {
		t.Fatalf("ProcessMessage() error = %v", err)
	}
	if resp.Response != "元の応答" {
		t.Fatalf("response = %q", resp.Response)
	}
	if len(recorder.canonical) != 1 {
		t.Fatalf("canonical logs = %#v", recorder.canonical)
	}
}

func TestMessageOrchestrator_ProcessMessage_AttachesVerificationReport(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1.0, "Chat route"),
		response: "これは2014年公開です。",
	}
	shiro := &mockShiroAgent{response: "executed"}
	verifier := &mockResponseVerifier{result: appverification.Result{
		Response: "これは2014年公開です。",
		Report: domainverification.VerificationReport{
			ID:           "verify_job",
			JobID:        "job",
			SessionID:    "20260302-line-U123",
			Route:        "CHAT",
			Status:       domainverification.StatusWeaklySupported,
			TriggerLevel: domainverification.TriggerMedium,
			CreatedAt:    time.Now().UTC(),
		},
	}}

	orchestrator := NewMessageOrchestrator(repo, mio, shiro, nil, nil, nil, nil, nil)
	orchestrator.SetVerificationPipeline(verifier)

	resp, err := orchestrator.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "20260302-line-U123",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "作品情報を教えて",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.Response != "これは2014年公開です。" {
		t.Fatalf("verification dry-run response should preserve output, got %q", resp.Response)
	}
	if resp.Verification == nil {
		t.Fatal("expected verification report")
	}
	if resp.Verification.Status != domainverification.StatusWeaklySupported {
		t.Fatalf("unexpected verification status: %s", resp.Verification.Status)
	}
	if verifier.req.DraftResponse != "これは2014年公開です。" || verifier.req.UserMessage != "作品情報を教えて" {
		t.Fatalf("unexpected verifier request: %+v", verifier.req)
	}
}

func TestMessageOrchestrator_ProcessMessage_ExistingSession(t *testing.T) {
	repo := newMockSessionRepository()

	// 既存セッションを作成
	existingSession := session.NewSession("20260302-line-U123", "line", "U123")
	repo.Save(context.Background(), existingSession)

	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1.0, "Chat route"),
		response: "2回目の応答",
	}
	shiro := &mockShiroAgent{response: "executed"}

	orchestrator := NewMessageOrchestrator(repo, mio, shiro, nil, nil, nil, nil, nil)

	req := ProcessMessageRequest{
		SessionID:   "20260302-line-U123",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "2回目のメッセージ",
	}

	resp, err := orchestrator.ProcessMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	// セッションに履歴が追加されているか確認
	loadedSession, _ := repo.Load(context.Background(), "20260302-line-U123")
	if loadedSession.HistoryCount() != 1 {
		t.Errorf("Expected 1 task in history, got %d", loadedSession.HistoryCount())
	}

	if resp.Response != "2回目の応答" {
		t.Errorf("Expected response '2回目の応答', got '%s'", resp.Response)
	}
}

func TestMessageOrchestrator_ProcessMessage_OPSRoute(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteOPS, 0.9, "OPS route"),
		response: "chat response",
	}
	shiro := &mockShiroAgent{response: "Command executed successfully"}

	orchestrator := NewMessageOrchestrator(repo, mio, shiro, nil, nil, nil, nil, nil)

	req := ProcessMessageRequest{
		SessionID:   "20260302-line-U123",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "ls -la",
	}

	resp, err := orchestrator.ProcessMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	if resp.Route != routing.RouteOPS {
		t.Errorf("Expected route OPS, got '%s'", resp.Route)
	}

	if resp.Response != "Command executed successfully" {
		t.Errorf("Expected Shiro response, got '%s'", resp.Response)
	}
}

func TestMessageOrchestrator_ProcessMessage_OPSRoute_StartsMaleTTSVoice(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteOPS, 0.9, "OPS route"),
	}
	shiro := &mockShiroAgent{response: "Command executed successfully"}
	ttsBridge := &mockTTSBridge{}

	orchestrator := NewMessageOrchestrator(repo, mio, shiro, nil, nil, nil, nil, nil)
	orchestrator.SetTTSBridge(ttsBridge)

	req := ProcessMessageRequest{
		SessionID:   "20260302-line-U123",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "ls -la",
	}

	if _, err := orchestrator.ProcessMessage(context.Background(), req); err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	if len(ttsBridge.startReqs) != 1 {
		t.Fatalf("expected 1 TTS start request, got %d", len(ttsBridge.startReqs))
	}
	if ttsBridge.startReqs[0].VoiceID != "male_01" {
		t.Fatalf("expected male_01 voice for OPS/Shiro route, got %q", ttsBridge.startReqs[0].VoiceID)
	}
	if ttsBridge.startReqs[0].VoiceProfile != "lumina_male" {
		t.Fatalf("expected lumina_male voice profile for OPS/Shiro route, got %q", ttsBridge.startReqs[0].VoiceProfile)
	}
}

func TestMessageOrchestrator_TTSBridge_StreamAndEnd(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1.0, "Chat route"),
		chatFunc: func(ctx context.Context, t task.Task) (string, error) {
			if cb := llm.StreamCallbackFromContext(ctx); cb != nil {
				cb("tok1")
				cb("tok2")
			}
			return "final response", nil
		},
	}
	shiro := &mockShiroAgent{response: "executed"}
	bridge := &mockTTSBridge{}

	o := NewMessageOrchestrator(repo, mio, shiro, nil, nil, nil, nil, nil)
	o.SetTTSBridge(bridge)

	_, err := o.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "sess-1",
		Channel:     "viewer",
		ChatID:      "u1",
		UserMessage: "hello",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if len(bridge.startReqs) != 1 {
		t.Fatalf("expected one start request, got %d", len(bridge.startReqs))
	}
	if len(bridge.pushes) != 1 || bridge.pushes[0] != "final response" {
		t.Fatalf("expected single final push, got %v", bridge.pushes)
	}
	if len(bridge.emotions) != 1 || bridge.emotions[0] == nil {
		t.Fatalf("expected emotion payload, got %+v", bridge.emotions)
	}
	if len(bridge.ended) != 1 {
		t.Fatalf("expected end session once, got %d", len(bridge.ended))
	}
}

func TestMessageOrchestrator_TTSBridge_StreamsSentenceChunks(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1.0, "Chat route"),
		chatFunc: func(ctx context.Context, t task.Task) (string, error) {
			if cb := llm.StreamCallbackFromContext(ctx); cb != nil {
				cb("最初の説明文です。")
				cb("次の説明文です。")
			}
			return "最初の説明文です。次の説明文です。", nil
		},
	}
	shiro := &mockShiroAgent{response: "executed"}
	bridge := &mockTTSBridge{}

	o := NewMessageOrchestrator(repo, mio, shiro, nil, nil, nil, nil, nil)
	o.SetTTSBridge(bridge)

	_, err := o.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "sess-stream",
		Channel:     "viewer",
		ChatID:      "u1",
		UserMessage: "hello",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if len(bridge.pushes) != 2 {
		t.Fatalf("expected 2 chunk pushes, got %v", bridge.pushes)
	}
	if bridge.pushes[0] != "最初の説明文です。" {
		t.Fatalf("unexpected first chunk: %q", bridge.pushes[0])
	}
	if bridge.pushes[1] != "次の説明文です。" {
		t.Fatalf("unexpected second chunk: %q", bridge.pushes[1])
	}
	if len(bridge.ended) != 1 {
		t.Fatalf("expected end session once, got %d", len(bridge.ended))
	}
}

func TestMessageOrchestrator_TTSBridge_DegradedOnStartError(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1.0, "Chat route"),
		response: "ok",
	}
	shiro := &mockShiroAgent{response: "executed"}
	bridge := &mockTTSBridge{startErr: fmt.Errorf("down")}

	o := NewMessageOrchestrator(repo, mio, shiro, nil, nil, nil, nil, nil)
	o.SetTTSBridge(bridge)

	resp, err := o.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "sess-2",
		Channel:     "viewer",
		ChatID:      "u1",
		UserMessage: "hello",
	})
	if err != nil {
		t.Fatalf("ProcessMessage should continue in degraded mode, got err=%v", err)
	}
	if resp.Response != "ok" {
		t.Fatalf("unexpected response: %q", resp.Response)
	}
}

func TestMessageOrchestrator_ProcessMessage_CODERoute(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCODE, 0.85, "CODE route"),
		response: "chat response",
	}
	shiro := &mockShiroAgent{response: "executed"}
	coder := &mockCoderAgent{response: "```go\n// Generated code\nfunc main() {}\n```"}

	orchestrator := NewMessageOrchestrator(repo, mio, shiro, coder, nil, nil, nil, nil)

	req := ProcessMessageRequest{
		SessionID:   "20260302-line-U123",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "main関数を実装して",
	}

	resp, err := orchestrator.ProcessMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	if resp.Route != routing.RouteCODE {
		t.Errorf("Expected route CODE, got '%s'", resp.Route)
	}

	if resp.Response != "```go\n// Generated code\nfunc main() {}\n```" {
		t.Errorf("Expected Coder response, got '%s'", resp.Response)
	}
}

func TestMessageOrchestrator_ProcessMessage_CodeRouteRecordsSkillBootstrap(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteCODE, 0.85, "CODE route")}
	shiro := &mockShiroAgent{response: "executed"}
	coder := &mockCoderAgent{response: "```go\nfunc main() {}\n```"}
	recorder := &mockSkillBootstrapRecorder{}
	orch := NewMessageOrchestrator(repo, mio, shiro, coder, nil, nil, nil, nil)
	orch.SetSkillBootstrapRecorder(recorder)

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "sess-code",
		Channel:     "viewer",
		ChatID:      "chat",
		UserMessage: "実装して",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.Route != routing.RouteCODE {
		t.Fatalf("route=%s", resp.Route)
	}
	if len(recorder.tasks) != 1 {
		t.Fatalf("skill bootstrap tasks=%#v", recorder.tasks)
	}
	if recorder.tasks[0].Agent != "Coder" || recorder.tasks[0].Intent != "code" || recorder.tasks[0].WorkstreamID != "sess-code" {
		t.Fatalf("unexpected bootstrap task=%#v", recorder.tasks[0])
	}
	if len(recorder.used) != 1 || len(recorder.used[0]) != 1 || recorder.used[0][0] != "core.coder" {
		t.Fatalf("unexpected used skills=%#v", recorder.used)
	}
}

func TestMessageOrchestrator_ProcessMessage_SkillBootstrapFailureStopsRoute(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteCODE, 0.85, "CODE route")}
	shiro := &mockShiroAgent{response: "executed"}
	coder := &mockCoderAgent{response: "proposal"}
	orch := NewMessageOrchestrator(repo, mio, shiro, coder, nil, nil, nil, nil)
	orch.SetSkillBootstrapRecorder(&mockSkillBootstrapRecorder{err: fmt.Errorf("skill store failed")})

	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "sess-code",
		Channel:     "viewer",
		ChatID:      "chat",
		UserMessage: "実装して",
	})
	if err == nil {
		t.Fatal("expected skill bootstrap failure")
	}
	if !strings.Contains(err.Error(), "skill bootstrap failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMessageOrchestrator_ProcessMessage_CODERouteUsesOnlyCoder1(t *testing.T) {
	t.Run("Coder1利用可能_Coder1を使用", func(t *testing.T) {
		repo := newMockSessionRepository()
		mio := &mockMioAgent{
			decision: routing.NewDecision(routing.RouteCODE, 0.85, "CODE route"),
		}
		coder1 := &mockCoderAgent{response: "coder1 response\n```\ncode\n```"}
		coder2 := &mockCoderAgent{response: "coder2 response"}

		orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, coder1, coder2, nil, nil, nil)
		resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
			SessionID: "test-session", Channel: "line", ChatID: "U1", UserMessage: "実装して",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Response != "coder1 response\n```\ncode\n```" {
			t.Errorf("expected coder1 response, got '%s'", resp.Response)
		}
	})

	t.Run("Coder1なし_Coder2があってもエラー", func(t *testing.T) {
		repo := newMockSessionRepository()
		mio := &mockMioAgent{
			decision: routing.NewDecision(routing.RouteCODE, 0.85, "CODE route"),
		}
		coder2 := &mockCoderAgent{response: "coder2 response\n```\ncode\n```"}

		orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, coder2, nil, nil, nil)
		_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
			SessionID: "test-session", Channel: "line", ChatID: "U1", UserMessage: "実装して",
		})
		if err == nil {
			t.Fatal("expected CODE route to fail when coder1 is unavailable")
		}
		if !strings.Contains(err.Error(), "CODE route requested but coder1 is unavailable") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("Coder1なし_Coder3があってもエラー", func(t *testing.T) {
		repo := newMockSessionRepository()
		mio := &mockMioAgent{
			decision: routing.NewDecision(routing.RouteCODE, 0.85, "CODE route"),
		}
		coder3 := &mockCoderAgent{response: "coder3 response\n```\ncode\n```"}

		orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, coder3, nil, nil)
		_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
			SessionID: "test-session", Channel: "line", ChatID: "U1", UserMessage: "実装して",
		})
		if err == nil {
			t.Fatal("expected CODE route to fail when coder1 is unavailable")
		}
		if !strings.Contains(err.Error(), "CODE route requested but coder1 is unavailable") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("全Coderなし_エラー", func(t *testing.T) {
		repo := newMockSessionRepository()
		mio := &mockMioAgent{
			decision: routing.NewDecision(routing.RouteCODE, 0.85, "CODE route"),
		}

		orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
		_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
			SessionID: "test-session", Channel: "line", ChatID: "U1", UserMessage: "実装して",
		})
		if err == nil {
			t.Error("expected error when all coders are unavailable")
		}
	})
}

func TestMessageOrchestrator_ProcessMessage_ExplicitCommand(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCODE3, 1.0, "Explicit command"),
		response: "chat response",
	}
	shiro := &mockShiroAgent{response: "executed"}
	coder3 := &mockCoderAgent{response: "High quality code review\n```\nsuggestions\n```"}

	orchestrator := NewMessageOrchestrator(repo, mio, shiro, nil, nil, coder3, nil, nil)

	req := ProcessMessageRequest{
		SessionID:   "20260302-line-U123",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "/code3 このコードをレビューして",
	}

	resp, err := orchestrator.ProcessMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	if resp.Route != routing.RouteCODE3 {
		t.Errorf("Expected route CODE3, got '%s'", resp.Route)
	}

	if resp.Response != "High quality code review\n```\nsuggestions\n```" {
		t.Errorf("Expected Coder3 response, got '%s'", resp.Response)
	}
}

func TestMessageOrchestrator_ProcessMessage_TaskAddedToHistory(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1.0, "Chat"),
		response: "応答",
	}
	shiro := &mockShiroAgent{response: "executed"}

	orchestrator := NewMessageOrchestrator(repo, mio, shiro, nil, nil, nil, nil, nil)

	req := ProcessMessageRequest{
		SessionID:   "20260302-line-U123",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "テスト",
	}

	_, err := orchestrator.ProcessMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}

	// セッションをロードして履歴確認
	sess, _ := repo.Load(context.Background(), "20260302-line-U123")
	if sess.HistoryCount() != 1 {
		t.Errorf("Expected 1 task in history, got %d", sess.HistoryCount())
	}

	history := sess.GetHistory()
	if history[0].UserMessage() != "テスト" {
		t.Errorf("Expected user message 'テスト', got '%s'", history[0].UserMessage())
	}

	if history[0].Route() != routing.RouteCHAT {
		t.Errorf("Expected task route CHAT, got '%s'", history[0].Route())
	}
}

// === Phase 1D: Error path tests ===

func defaultReq() ProcessMessageRequest {
	return ProcessMessageRequest{
		SessionID:   "test-session",
		Channel:     "line",
		ChatID:      "U123",
		UserMessage: "test message",
	}
}

func TestMessageOrchestrator_ProcessMessage_SessionLoadError(t *testing.T) {
	repo := newMockSessionRepository()
	repo.loadErr = fmt.Errorf("database connection failed")

	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1.0, "Chat"),
		response: "hello",
	}

	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	_, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err == nil {
		t.Fatal("expected error for session load failure")
	}
	if !strings.Contains(err.Error(), "database connection failed") {
		t.Errorf("error should contain root cause, got: %v", err)
	}
}

func TestMessageOrchestrator_ProcessMessage_RoutingError(t *testing.T) {
	mio := &mockMioAgent{
		decideFunc: func(ctx context.Context, t task.Task) (routing.Decision, error) {
			return routing.Decision{}, fmt.Errorf("LLM classifier timeout")
		},
	}

	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	_, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err == nil {
		t.Fatal("expected error for routing failure")
	}
	if !strings.Contains(err.Error(), "routing decision failed") {
		t.Errorf("error should mention routing, got: %v", err)
	}
}

func TestMessageOrchestrator_ProcessMessage_ChatError(t *testing.T) {
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1.0, "Chat"),
		chatFunc: func(ctx context.Context, t task.Task) (string, error) {
			return "", fmt.Errorf("Ollama unavailable")
		},
	}

	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	_, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err == nil {
		t.Fatal("expected error for chat failure")
	}
	if !strings.Contains(err.Error(), "Ollama unavailable") {
		t.Errorf("error should contain root cause, got: %v", err)
	}
}

func TestMessageOrchestrator_ProcessMessage_ShiroError(t *testing.T) {
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteOPS, 0.9, "OPS"),
	}
	shiro := &mockShiroAgent{
		executeFunc: func(ctx context.Context, t task.Task) (string, error) {
			return "", fmt.Errorf("command execution failed")
		},
	}

	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, shiro, nil, nil, nil, nil, nil)
	_, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err == nil {
		t.Fatal("expected error for shiro failure")
	}
	if !strings.Contains(err.Error(), "command execution failed") {
		t.Errorf("error should contain root cause, got: %v", err)
	}
}

func TestMessageOrchestrator_ProcessMessage_NoCoder(t *testing.T) {
	cases := []struct {
		name    string
		route   routing.Route
		wantErr string
	}{
		{"CODE1", routing.RouteCODE1, "no coder1 available"},
		{"CODE2", routing.RouteCODE2, "no coder2 available"},
		{"CODE3", routing.RouteCODE3, "no coder3 available"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mio := &mockMioAgent{
				decision: routing.NewDecision(tc.route, 1.0, tc.name),
			}
			orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
			_, err := orch.ProcessMessage(context.Background(), defaultReq())
			if err == nil {
				t.Fatalf("expected error for %s with no coder", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error should mention coder unavailability, got: %v", err)
			}
		})
	}
}

func TestMessageOrchestrator_ProcessMessage_FallbackToChat(t *testing.T) {
	cases := []struct {
		name  string
		route routing.Route
	}{
		{"PLAN", routing.RoutePLAN},
		{"RESEARCH", routing.RouteRESEARCH},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mio := &mockMioAgent{
				decision: routing.NewDecision(tc.route, 0.8, tc.name),
				response: "fallback response",
			}
			orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
			resp, err := orch.ProcessMessage(context.Background(), defaultReq())
			if err != nil {
				t.Fatalf("ProcessMessage failed: %v", err)
			}
			if resp.Route != tc.route {
				t.Errorf("route: want %s, got %s", tc.route, resp.Route)
			}
		})
	}
}

func TestMessageOrchestrator_ProcessMessage_AnalyzeWithoutHeavyFailsInsteadOfFallback(t *testing.T) {
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteANALYZE, 1.0, "explicit analyze"),
		chatFunc: func(ctx context.Context, tk task.Task) (string, error) {
			t.Fatal("ANALYZE must not fall back to Mio chat when heavy agent is unavailable")
			return "", nil
		},
	}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)

	_, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err == nil {
		t.Fatal("expected missing heavy agent to fail")
	}
	if !strings.Contains(err.Error(), "no heavy agent available") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMessageOrchestrator_ProcessMessage_UnknownRoute(t *testing.T) {
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.Route("UNKNOWN"), 0.5, "unknown"),
	}

	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	_, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err == nil {
		t.Fatal("expected error for unknown route")
	}
	if !strings.Contains(err.Error(), "unknown route") {
		t.Errorf("error should mention unknown route, got: %v", err)
	}
}

func TestMessageOrchestrator_ProcessMessage_ChatCommand_Handled(t *testing.T) {
	mio := &mockMioAgent{
		cmdFunc: func(ctx context.Context, sessionID, message string) (agent.ChatCommandResult, error) {
			return agent.ChatCommandResult{Handled: true, Response: "status output"}, nil
		},
	}

	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	resp, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.Response != "status output" {
		t.Errorf("response: want 'status output', got %q", resp.Response)
	}
	if resp.Route != routing.RouteCHAT {
		t.Errorf("route for handled command should be CHAT, got %s", resp.Route)
	}
}

func TestMessageOrchestrator_ProcessMessage_ChatCommand_Error(t *testing.T) {
	mio := &mockMioAgent{
		cmdFunc: func(ctx context.Context, sessionID, message string) (agent.ChatCommandResult, error) {
			return agent.ChatCommandResult{}, fmt.Errorf("command processing failed")
		},
	}

	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	_, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err == nil {
		t.Fatal("expected error for command failure")
	}
	if !strings.Contains(err.Error(), "chat command failed") {
		t.Errorf("error should mention chat command, got: %v", err)
	}
}

func TestMessageOrchestrator_ProcessMessage_ExplicitDCIBypassesRouting(t *testing.T) {
	decideCalled := false
	mio := &mockMioAgent{
		decideFunc: func(ctx context.Context, t task.Task) (routing.Decision, error) {
			decideCalled = true
			return routing.NewDecision(routing.RouteCHAT, 1.0, "should not run"), nil
		},
		response: "chat fallback",
	}
	searcher := &mockDCISearcher{
		trigger: true,
		result: domaindci.SearchResult{
			Pack: domaindci.EvidencePack{
				EventID:     "evt_dci_test",
				Query:       "docs から DCI を探して",
				CorpusScope: []string{"docs/10_新仕様"},
				Evidence: []domaindci.Evidence{{
					FilePath:  "docs/10_新仕様/19_DCI_直接コーパス探索仕様.md",
					LineStart: 12,
					Snippet:   "DCIは原文確認能力である",
				}},
			},
			Trace: domaindci.SearchTrace{
				EventID: "evt_dci_test",
				Status:  "completed",
			},
		},
	}
	req := defaultReq()
	req.UserMessage = "docs から DCI を探して"
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetDCISearcher(searcher)

	resp, err := orch.ProcessMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if decideCalled {
		t.Fatal("explicit DCI trigger should bypass route decision")
	}
	if searcher.calls != 1 || searcher.query != req.UserMessage {
		t.Fatalf("DCI search call mismatch: calls=%d query=%q", searcher.calls, searcher.query)
	}
	if resp.Route != routing.RouteRESEARCH {
		t.Fatalf("route: want RESEARCH, got %s", resp.Route)
	}
	if !strings.Contains(resp.Response, "DCI探索結果") || !strings.Contains(resp.Response, "docs/10_新仕様/19_DCI_直接コーパス探索仕様.md:12") {
		t.Fatalf("DCI response should include evidence location, got %q", resp.Response)
	}
}

func TestMessageOrchestrator_ProcessMessage_ExplicitDCISavesRecallTrace(t *testing.T) {
	mio := &mockMioAgent{response: "chat fallback"}
	searcher := &mockDCISearcher{
		trigger: true,
		result: domaindci.SearchResult{
			Pack: domaindci.EvidencePack{
				EventID:     "evt_dci_recall",
				Query:       "DCI を探して",
				CorpusScope: []string{"docs/10_新仕様"},
				Evidence: []domaindci.Evidence{{
					FilePath:   "docs/10_新仕様/19_DCI_直接コーパス探索仕様.md",
					LineStart:  30,
					Snippet:    "Evidence Pack",
					Confidence: 0.8,
				}},
			},
			Trace: domaindci.SearchTrace{EventID: "evt_dci_recall", Status: "completed", EndedAt: time.Date(2026, 5, 18, 1, 2, 3, 0, time.UTC)},
		},
	}
	recall := &mockRecallTraceStore{}
	req := defaultReq()
	req.UserMessage = "DCI を探して"
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetDCISearcher(searcher)
	orch.SetRecallTraceStore(recall)

	resp, err := orch.ProcessMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.JobID == "" {
		t.Fatal("response should include job id")
	}
	if len(recall.traces) != 1 {
		t.Fatalf("expected one recall trace, got %d", len(recall.traces))
	}
	trace := recall.traces[0]
	if trace.SessionID != req.SessionID || trace.ResponseID != resp.JobID || trace.Role != "dci" {
		t.Fatalf("unexpected recall trace identity: %+v", trace)
	}
	if len(trace.Items) != 1 {
		t.Fatalf("expected one recall trace item, got %+v", trace.Items)
	}
	item := trace.Items[0]
	if item.Layer != "DCI" || item.Kind != "evidence" || item.Provider != "dci" || item.Query != req.UserMessage {
		t.Fatalf("unexpected recall trace item: %+v", item)
	}
	if !strings.Contains(item.Summary, "docs/10_新仕様/19_DCI_直接コーパス探索仕様.md:30") {
		t.Fatalf("recall trace item should include evidence location: %+v", item)
	}
}

func TestMessageOrchestrator_ProcessMessage_ExplicitDCIErrorDoesNotFallback(t *testing.T) {
	decideCalled := false
	mio := &mockMioAgent{
		decideFunc: func(ctx context.Context, t task.Task) (routing.Decision, error) {
			decideCalled = true
			return routing.NewDecision(routing.RouteCHAT, 1.0, "fallback"), nil
		},
		response: "chat fallback",
	}
	searcher := &mockDCISearcher{trigger: true, err: fmt.Errorf("trace store failed")}
	req := defaultReq()
	req.UserMessage = "ログを探して"
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetDCISearcher(searcher)

	_, err := orch.ProcessMessage(context.Background(), req)
	if err == nil {
		t.Fatal("expected explicit DCI search failure")
	}
	if decideCalled {
		t.Fatal("failed DCI trigger should not fall back to route decision")
	}
	if !strings.Contains(err.Error(), "dci search failed") || !strings.Contains(err.Error(), "trace store failed") {
		t.Fatalf("error should preserve DCI failure, got %v", err)
	}
}

func TestMessageOrchestrator_ProcessMessage_ExplicitDCIRecallTraceErrorFails(t *testing.T) {
	mio := &mockMioAgent{response: "chat fallback"}
	searcher := &mockDCISearcher{
		trigger: true,
		result: domaindci.SearchResult{
			Pack: domaindci.EvidencePack{
				EventID: "evt_dci_recall_fail",
				Query:   "DCI を探して",
				Evidence: []domaindci.Evidence{{
					FilePath:  "docs/spec.md",
					LineStart: 1,
					Snippet:   "DCI",
				}},
			},
			Trace: domaindci.SearchTrace{EventID: "evt_dci_recall_fail", Status: "completed"},
		},
	}
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetDCISearcher(searcher)
	orch.SetRecallTraceStore(&mockRecallTraceStore{err: fmt.Errorf("l1 unavailable")})

	req := defaultReq()
	req.UserMessage = "DCI を探して"
	_, err := orch.ProcessMessage(context.Background(), req)
	if err == nil {
		t.Fatal("expected recall trace save error")
	}
	if !strings.Contains(err.Error(), "failed to save dci recall trace") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMessageOrchestrator_ProcessMessage_SessionSaveError(t *testing.T) {
	repo := newMockSessionRepository()
	repo.saveErr = fmt.Errorf("disk full")

	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1.0, "Chat"),
		response: "hello",
	}

	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	_, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err == nil {
		t.Fatal("expected error for save failure")
	}
	if !strings.Contains(err.Error(), "disk full") {
		t.Errorf("error should contain root cause, got: %v", err)
	}
}

func TestProcessMessage_RouteWildUsesWildAgent(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteWILD, 1.0, "explicit wild")}
	wild := &mockWildAgent{response: "wild response"}
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetWildAgent(wild)

	resp, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if !wild.called {
		t.Fatal("wild agent should be called")
	}
	if resp.Route != routing.RouteWILD {
		t.Fatalf("route: want WILD, got %s", resp.Route)
	}
	if resp.Response != "wild response" {
		t.Fatalf("response: want wild response, got %q", resp.Response)
	}
}

func TestProcessMessage_RouteAnalyzeUsesHeavyAgent(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteANALYZE, 1.0, "explicit analyze")}
	heavy := &mockHeavyAgent{response: "heavy response"}
	workflowEvents := &mockWorkflowEventRecorder{}
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetHeavyAgent(heavy)
	orch.SetWorkflowEventRecorder(workflowEvents)

	resp, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if !heavy.called {
		t.Fatal("heavy agent should be called")
	}
	if resp.Route != routing.RouteANALYZE {
		t.Fatalf("route: want ANALYZE, got %s", resp.Route)
	}
	if resp.Response != "heavy response" {
		t.Fatalf("response: want heavy response, got %q", resp.Response)
	}
	if len(workflowEvents.events) != 2 {
		t.Fatalf("expected heavy lifecycle events, got %#v", workflowEvents.events)
	}
	if workflowEvents.events[0].EventType != "heavy_worker_started" || workflowEvents.events[1].EventType != "heavy_worker_completed" {
		t.Fatalf("unexpected heavy lifecycle events: %#v", workflowEvents.events)
	}
}

func TestProcessMessage_HeavyWorkerPolicyElevatesDeepDiveToAnalyze(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{decision: routing.NewDecision(routing.RouteCHAT, 0.7, "default chat")}
	heavy := &mockHeavyAgent{response: "heavy deep dive"}
	workflowEvents := &mockWorkflowEventRecorder{}
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetHeavyAgent(heavy)
	orch.SetHeavyWorkerPolicy(domainai.HeavyWorkerPolicy{
		Enabled:       true,
		RequireReason: true,
	})
	orch.SetWorkflowEventRecorder(workflowEvents)

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "deep-session",
		Channel:     "viewer",
		ChatID:      "viewer-user",
		UserMessage: "この件を深掘りして",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if !heavy.called {
		t.Fatal("heavy agent should be called after policy elevation")
	}
	if resp.Route != routing.RouteANALYZE || resp.Response != "heavy deep dive" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if len(workflowEvents.events) != 3 {
		t.Fatalf("expected requested + lifecycle events, got %#v", workflowEvents.events)
	}
	if workflowEvents.events[0].EventType != "heavy_worker_requested" ||
		workflowEvents.events[1].EventType != "heavy_worker_started" ||
		workflowEvents.events[2].EventType != "heavy_worker_completed" {
		t.Fatalf("unexpected heavy events: %#v", workflowEvents.events)
	}
}

func TestProcessMessage_RegisteredSlashCommandExpandsRuntimePrompt(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.MkdirAll("commands", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("commands/review-architecture.md", []byte("# /review-architecture\n\n## Steps\n1. 正本仕様を読む\n2. 差分を見る\n"), 0644); err != nil {
		t.Fatal(err)
	}
	repo := newMockSessionRepository()
	var routedMessage string
	var chatMessage string
	mio := &mockMioAgent{
		response: "review result",
		decideFunc: func(ctx context.Context, t task.Task) (routing.Decision, error) {
			routedMessage = t.UserMessage()
			return routing.NewDecision(routing.RouteCHAT, 0.9, "command expanded"), nil
		},
		chatFunc: func(ctx context.Context, t task.Task) (string, error) {
			chatMessage = t.UserMessage()
			return "review result", nil
		},
	}
	events := &mockWorkflowEventRecorder{}
	skills := &mockSkillBootstrapRecorder{}
	commands := &mockCommandRegistryStore{commands: []domainai.CommandRegistry{{
		CommandName:   "/review-architecture",
		FilePath:      "commands/review-architecture.md",
		Description:   "architecture review",
		DefaultAgent:  "Coder",
		RequiredSkill: "core.architecture-review",
		UpdatedAt:     time.Now().UTC(),
	}}}
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetWorkflowEventRecorder(events)
	orch.SetSkillBootstrapRecorder(skills)
	orch.SetCommandRegistry(commands)

	resp, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "sess-command",
		Channel:     "viewer",
		ChatID:      "chat-command",
		UserMessage: "/review-architecture docs/10_新仕様/31_未実装項目実装仕様.md を確認",
	})
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.Response != "review result" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	for _, got := range []string{routedMessage, chatMessage} {
		if !strings.Contains(got, "Slash command runtime expansion") ||
			!strings.Contains(got, "# /review-architecture") ||
			!strings.Contains(got, "User input:\ndocs/10_新仕様/31_未実装項目実装仕様.md を確認") {
			t.Fatalf("command prompt was not expanded:\n%s", got)
		}
	}
	if len(events.events) != 1 || events.events[0].EventType != "command_invoked" || events.events[0].CommandName != "/review-architecture" {
		t.Fatalf("expected command_invoked event, got %+v", events.events)
	}
	if len(skills.used) != 1 || len(skills.used[0]) != 1 || skills.used[0][0] != "core.architecture-review" {
		t.Fatalf("expected required skill bootstrap, got %+v", skills.used)
	}
}

func TestProcessMessage_RegisteredSlashCommandMissingFileFails(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1, "chat"),
		response: "should not run",
	}
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetCommandRegistry(&mockCommandRegistryStore{commands: []domainai.CommandRegistry{{
		CommandName: "/review-architecture",
		FilePath:    "commands/missing.md",
		UpdatedAt:   time.Now().UTC(),
	}}})

	_, err := orch.ProcessMessage(context.Background(), ProcessMessageRequest{
		SessionID:   "sess-command-missing",
		Channel:     "viewer",
		ChatID:      "chat-command-missing",
		UserMessage: "/review-architecture target",
	})
	if err == nil || !strings.Contains(err.Error(), "slash command expansion failed") {
		t.Fatalf("expected slash command expansion failure, got %v", err)
	}
}

func TestProcessMessage_RecordsLeadAgentRun(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1, "chat"),
		response: "hello",
	}
	super := &mockSuperAgentRuntimeRecorder{}
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetSuperAgentRuntimeRecorder(super)

	resp, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if resp.Route != routing.RouteCHAT {
		t.Fatalf("route=%s", resp.Route)
	}
	if len(super.runs) != 2 {
		t.Fatalf("expected running and completed agent_run records, got %+v", super.runs)
	}
	if super.runs[0].Status != "running" || super.runs[1].Status != "completed" {
		t.Fatalf("unexpected agent_run statuses: %+v", super.runs)
	}
	if super.runs[0].RunID != super.runs[1].RunID || super.runs[0].WorkstreamID != defaultReq().SessionID {
		t.Fatalf("unexpected agent_run identity: %+v", super.runs)
	}
	if len(super.contextPacks) != 1 || super.contextPacks[0].RunID != super.runs[0].RunID {
		t.Fatalf("unexpected context pack: %+v", super.contextPacks)
	}
	if len(super.traces) != 2 || super.traces[0].EventType != "lead_agent_started" || super.traces[1].EventType != "lead_agent_completed" {
		t.Fatalf("unexpected trace events: %+v", super.traces)
	}
}

func TestProcessMessage_RecordsPausedLeadAgentRunWhenRuntimePauseCancelsContext(t *testing.T) {
	repo := newMockSessionRepository()
	mio := &mockMioAgent{
		decision: routing.NewDecision(routing.RouteCHAT, 1, "chat"),
		chatFunc: func(ctx context.Context, _ task.Task) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	}
	super := &mockSuperAgentRuntimeRecorder{}
	orch := NewMessageOrchestrator(repo, mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetSuperAgentRuntimeRecorder(super)
	orch.SetSuperAgentRunController(pauseRequestedRunController{})

	_, err := orch.ProcessMessage(context.Background(), defaultReq())
	if err == nil || !strings.Contains(err.Error(), "task execution failed") {
		t.Fatalf("expected task execution failure after pause cancellation, got %v", err)
	}
	if len(super.runs) != 2 {
		t.Fatalf("expected running and paused agent_run records, got %+v", super.runs)
	}
	if super.runs[0].Status != "running" || super.runs[1].Status != "paused" {
		t.Fatalf("unexpected agent_run statuses: %+v", super.runs)
	}
	if !strings.Contains(super.runs[1].Summary, "pause requested") {
		t.Fatalf("paused run summary did not record pause reason: %+v", super.runs[1])
	}
	if len(super.traces) != 2 || super.traces[0].EventType != "lead_agent_started" || super.traces[1].EventType != "lead_agent_paused" {
		t.Fatalf("unexpected trace events: %+v", super.traces)
	}
}

func TestMessageOrchestrator_ProcessMessage_SlashCommandSkipsDCI(t *testing.T) {
	// スラッシュコマンド（/code3, /analyze 等）は DCI をスキップして通常ルーティングに進む
	decideCalled := false
	mio := &mockMioAgent{
		decideFunc: func(ctx context.Context, t task.Task) (routing.Decision, error) {
			decideCalled = true
			return routing.NewDecision(routing.RouteCHAT, 1.0, "explicit /code3 command (fallback to CHAT in test)"), nil
		},
		chatFunc: func(ctx context.Context, m task.Task) (string, error) {
			return "code3 response", nil
		},
	}

	searcher := &mockDCISearcher{
		trigger: true, // ShouldTrigger が true を返しても
	}

	req := defaultReq()
	req.UserMessage = "/code3 ルーティング関連ファイルを調査して"
	orch := NewMessageOrchestrator(newMockSessionRepository(), mio, &mockShiroAgent{}, nil, nil, nil, nil, nil)
	orch.SetDCISearcher(searcher)

	_, err := orch.ProcessMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("ProcessMessage failed: %v", err)
	}
	if searcher.calls != 0 {
		t.Fatalf("DCI should be skipped for slash commands, but Search was called %d time(s)", searcher.calls)
	}
	if !decideCalled {
		t.Fatal("route decision should be called when DCI is skipped")
	}
}
