package orchestrator

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domainrouting "github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	infrarouting "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type mioRetrievalTestQualification struct {
	ownerID       string
	userScope     string
	sourceVersion string
}

func (q mioRetrievalTestQualification) OwnerID() string       { return q.ownerID }
func (q mioRetrievalTestQualification) UserScope() string     { return q.userScope }
func (q mioRetrievalTestQualification) SourceVersion() string { return q.sourceVersion }

func validMioUserMemoryResultForOrchestratorTest(input domainconversation.TurnInput, text string) domainagent.MioRetrievalResult {
	ownerID := "test-owner"
	sourceVersion := "early-stream-test-v1"
	updatedAt := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	return domainagent.MioRetrievalResult{
		SourceKind: domainagent.MioRetrievalSourceUserMemory,
		Status:     domainagent.MioRetrievalStatusFound,
		Items:      []domainagent.MioRetrievalItem{{Text: text}},
		Qualification: mioRetrievalTestQualification{
			ownerID: ownerID, userScope: "user:" + ownerID, sourceVersion: sourceVersion,
		},
		Provenance: []domainagent.MioRetrievalProvenance{{
			Reference: domainmemory.UserMemoryChatRecallSourceReference{
				OwnerID: ownerID, SessionID: modulecore.SessionID(input.SessionID()), MemoryID: "memory-test-1",
				Namespace: "user:" + ownerID, SourceType: "user_memory", Source: "owner confirmed memory",
				EvidenceEventIDs: []string{"evt-test-1"}, PersonaScope: "mio", State: domainmemory.MemoryStateConfirmed,
				Sensitivity: "normal", Active: true, UpdatedAt: updatedAt,
				OwnerVersion: updatedAt.UTC().Format(time.RFC3339Nano),
			},
			TurnID: string(input.TurnID()), TraceID: string(input.TraceID()), RootTaskID: string(input.RootTaskID()),
			StatementSHA256: sha256.Sum256([]byte(text)),
		}},
	}
}

type mioRetrievalPortFunc func(context.Context, domainconversation.TurnInput, domainagent.MioRetrievalRequest) (domainagent.MioRetrievalResult, error)

func (f mioRetrievalPortFunc) Retrieve(ctx context.Context, input domainconversation.TurnInput, request domainagent.MioRetrievalRequest) (domainagent.MioRetrievalResult, error) {
	return f(ctx, input, request)
}

type mioRetrievalRouteFixture struct {
	provider   *sequentialChatReplyProvider
	mio        *domainagent.MioAgent
	input      domainconversation.TurnInput
	draft      *domainagent.MioChatDraft
	dispatcher *messageRouteDispatcher
	engine     *chatReplyConversationEngine
	bridge     *mockTTSBridge
	events     []OrchestratorEvent
}

func newMioRetrievalRouteFixture(t *testing.T, responses ...string) *mioRetrievalRouteFixture {
	t.Helper()
	taskManager := newRecordingTaskLifecycleManager()
	engine := &chatReplyConversationEngine{manager: taskManager}
	preparedEngine := &earlyStreamPreparedChatEngine{chatReplyConversationEngine: engine}
	provider := &sequentialChatReplyProvider{responses: responses}
	mio := domainagent.NewMioAgent(
		provider,
		infrarouting.NewLLMClassifier(provider, "current Mio classifier route policy"),
		chatReplyRuleDictionary{}, nil, nil, preparedEngine,
	).WithSystemPrompt("canonical Mio persona")
	req := defaultReq()
	req.UserMessage = "何時に出発するんだっけ？"
	if err := ensureProcessRequestIdentity(&req); err != nil {
		t.Fatalf("ensureProcessRequestIdentity: %v", err)
	}
	input, err := buildTurnInputFromProcessRequest(req)
	if err != nil {
		t.Fatalf("buildTurnInputFromProcessRequest: %v", err)
	}
	decision, draft, err := mio.DecideActionWithChatDraft(context.Background(), input, true)
	if err != nil || decision.Route != domainrouting.RouteCHAT || draft == nil {
		t.Fatalf("DecideActionWithChatDraft = route %s draft=%v err=%v", decision.Route, draft != nil, err)
	}
	bridge := &mockTTSBridge{}
	fixture := &mioRetrievalRouteFixture{
		provider: provider, mio: mio, input: input, draft: draft, engine: engine, bridge: bridge,
	}
	fixture.dispatcher = newMessageRouteDispatcher(
		mio,
		&mockShiroAgent{},
		nil,
		func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {
			fixture.events = append(fixture.events, NewEvent(eventType, from, to, content, route, taskID, sessionID, channel, chatID))
		},
		func(ctx context.Context, route domainrouting.Route, speaker, taskID, sessionID, channel, chatID, ttsSessionID string) (context.Context, *streamBundle) {
			stream := newTTSStreamForwarder(bridge, ttsSessionID, route, speaker, "agent.response", "[retrieval-test]", context.Background())
			return ctx, &streamBundle{tts: stream}
		},
		func(context.Context, string, domainrouting.Route, string, string, string) {},
	)
	return fixture
}

func (f *mioRetrievalRouteFixture) execute(ctx context.Context, t *testing.T) (string, error) {
	t.Helper()
	defer f.mio.ReleaseChatDraft(ctx, f.draft)
	return f.dispatcher.executeChatRouteWithDraft(ctx, f.input, modulecore.NewTaskID(), "tts-retrieval-test", f.draft)
}

func TestMessageRouteDispatcherContinuesRetrievedMioAnswerOnSameTurn(t *testing.T) {
	fixture := newMioRetrievalRouteFixture(t,
		`{"schema_version":2,"route":"CHAT","outcome":{"kind":"retrieve","requests":[{"source_kind":"user_memory","query":"出発時刻","references":[],"information_needed":"確定した出発時刻"}]}}`,
		`{"schema_version":2,"route":"CHAT","outcome":{"kind":"answer","text":"10時に出発する予定だよ。"}}`,
	)
	portCalls := 0
	fixture.dispatcher.SetMioRetrievalPort(mioRetrievalPortFunc(func(_ context.Context, input domainconversation.TurnInput, request domainagent.MioRetrievalRequest) (domainagent.MioRetrievalResult, error) {
		portCalls++
		if input.TurnID() != fixture.input.TurnID() || input.SessionID() != fixture.input.SessionID() {
			t.Fatalf("retrieval input identity turn=%s session=%s, want turn=%s session=%s", input.TurnID(), input.SessionID(), fixture.input.TurnID(), fixture.input.SessionID())
		}
		if request.SourceKind != domainagent.MioRetrievalSourceUserMemory {
			t.Fatalf("retrieval source = %q, want user_memory", request.SourceKind)
		}
		return validMioUserMemoryResultForOrchestratorTest(input, "確定した出発時刻は10時。"), nil
	}))

	response, err := fixture.execute(context.Background(), t)
	if err != nil {
		t.Fatalf("executeChatRouteWithDraft() error = %v", err)
	}
	if response != "10時に出発する予定だよ。" {
		t.Fatalf("response = %q, want final continuation answer", response)
	}
	if got := len(fixture.provider.requests); got != 2 {
		t.Fatalf("LLM generations = %d, want initial outcome plus one continuation", got)
	}
	if portCalls != 1 {
		t.Fatalf("retrieval calls = %d, want 1", portCalls)
	}
	if fixture.engine.commitCalls != 1 {
		t.Fatalf("conversation EndTurn/commit calls = %d, want exactly 1", fixture.engine.commitCalls)
	}
	if fixture.engine.request.TurnID != fixture.input.TurnID() || fixture.engine.request.SessionID != fixture.input.SessionID() {
		t.Fatalf("committed identity turn=%s session=%s, want turn=%s session=%s", fixture.engine.request.TurnID, fixture.engine.request.SessionID, fixture.input.TurnID(), fixture.input.SessionID())
	}
	if !strings.Contains(fixture.provider.requests[1].Messages[len(fixture.provider.requests[1].Messages)-1].Content, "確定した出発時刻は10時") {
		t.Fatalf("continuation omitted owner result: %#v", fixture.provider.requests[1].Messages)
	}
	responses := 0
	for _, event := range fixture.events {
		if event.Type == "agent.response" {
			responses++
			if event.Content != response {
				t.Fatalf("published response = %q, want final answer", event.Content)
			}
		}
	}
	if responses != 1 {
		t.Fatalf("agent.response events = %d, want exactly 1", responses)
	}
	if len(fixture.bridge.pushes) != 1 || fixture.bridge.pushes[0] != response {
		t.Fatalf("TTS pushes = %#v, want final answer once", fixture.bridge.pushes)
	}
}

func TestMessageRouteDispatcherRetrievalFailsClosed(t *testing.T) {
	tests := []struct {
		name         string
		responses    []string
		port         MioRetrievalPort
		wantErr      error
		wantLLMCalls int
		wantPortCall int
	}{
		{
			name: "source port unavailable",
			responses: []string{
				`{"schema_version":2,"route":"CHAT","outcome":{"kind":"retrieve","requests":[{"source_kind":"user_memory","query":"出発時刻","references":[],"information_needed":"確定した出発時刻"}]}}`,
			},
			wantErr: ErrMioRetrievalUnavailable, wantLLMCalls: 1,
		},
		{
			name: "denied source result",
			responses: []string{
				`{"schema_version":2,"route":"CHAT","outcome":{"kind":"retrieve","requests":[{"source_kind":"user_memory","query":"出発時刻","references":[],"information_needed":"確定した出発時刻"}]}}`,
			},
			port: mioRetrievalPortFunc(func(_ context.Context, _ domainconversation.TurnInput, request domainagent.MioRetrievalRequest) (domainagent.MioRetrievalResult, error) {
				return domainagent.MioRetrievalResult{SourceKind: request.SourceKind, Status: domainagent.MioRetrievalStatusDenied}, nil
			}),
			wantErr: ErrMioRetrievalDenied, wantLLMCalls: 1, wantPortCall: 1,
		},
		{
			name: "result envelope over budget",
			responses: []string{
				`{"schema_version":2,"route":"CHAT","outcome":{"kind":"retrieve","requests":[{"source_kind":"knowledge_memory","query":"出発時刻","references":[],"information_needed":"確定した出発時刻"}]}}`,
			},
			port: mioRetrievalPortFunc(func(_ context.Context, _ domainconversation.TurnInput, request domainagent.MioRetrievalRequest) (domainagent.MioRetrievalResult, error) {
				return domainagent.MioRetrievalResult{SourceKind: request.SourceKind, Status: domainagent.MioRetrievalStatusFound, Items: []domainagent.MioRetrievalItem{{Text: strings.Repeat("x", 65*1024)}}}, nil
			}),
			wantErr: ErrMioRetrievalBudgetExceeded, wantLLMCalls: 1, wantPortCall: 1,
		},
		{
			name: "result count over budget",
			responses: []string{
				`{"schema_version":2,"route":"CHAT","outcome":{"kind":"retrieve","requests":[{"source_kind":"knowledge_memory","query":"出発時刻","references":[],"information_needed":"確定した出発時刻"}]}}`,
			},
			port: mioRetrievalPortFunc(func(_ context.Context, _ domainconversation.TurnInput, request domainagent.MioRetrievalRequest) (domainagent.MioRetrievalResult, error) {
				items := make([]domainagent.MioRetrievalItem, 9)
				for index := range items {
					items[index].Text = "result"
				}
				return domainagent.MioRetrievalResult{SourceKind: request.SourceKind, Status: domainagent.MioRetrievalStatusFound, Items: items}, nil
			}),
			wantErr: ErrMioRetrievalBudgetExceeded, wantLLMCalls: 1, wantPortCall: 1,
		},
		{
			name: "invalid owner response",
			responses: []string{
				`{"schema_version":2,"route":"CHAT","outcome":{"kind":"retrieve","requests":[{"source_kind":"user_memory","query":"出発時刻","references":[],"information_needed":"確定した出発時刻"}]}}`,
			},
			port: mioRetrievalPortFunc(func(_ context.Context, _ domainconversation.TurnInput, request domainagent.MioRetrievalRequest) (domainagent.MioRetrievalResult, error) {
				return domainagent.MioRetrievalResult{SourceKind: domainagent.MioRetrievalSourceKnowledgeMemory, Status: domainagent.MioRetrievalStatusFound, Items: []domainagent.MioRetrievalItem{{Text: "別sourceの情報"}}}, nil
			}),
			wantErr: domainagent.ErrInvalidMioRetrievalResult, wantLLMCalls: 1, wantPortCall: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newMioRetrievalRouteFixture(t, tc.responses...)
			portCalls := 0
			if tc.port != nil {
				fixture.dispatcher.SetMioRetrievalPort(mioRetrievalPortFunc(func(ctx context.Context, input domainconversation.TurnInput, request domainagent.MioRetrievalRequest) (domainagent.MioRetrievalResult, error) {
					portCalls++
					return tc.port.Retrieve(ctx, input, request)
				}))
			}
			_, err := fixture.execute(context.Background(), t)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("executeChatRouteWithDraft() error = %v, want %v", err, tc.wantErr)
			}
			if got := len(fixture.provider.requests); got != tc.wantLLMCalls {
				t.Fatalf("LLM generations = %d, want %d", got, tc.wantLLMCalls)
			}
			if portCalls != tc.wantPortCall {
				t.Fatalf("retrieval calls = %d, want %d", portCalls, tc.wantPortCall)
			}
			if fixture.engine.commitCalls != 0 {
				t.Fatalf("conversation commits = %d, want none after retrieval failure", fixture.engine.commitCalls)
			}
			for _, event := range fixture.events {
				if event.Type == "agent.response" {
					t.Fatalf("agent.response published after retrieval failure: %#v", event)
				}
			}
			if len(fixture.bridge.pushes) != 0 {
				t.Fatalf("retrieval failure produced TTS text: %#v", fixture.bridge.pushes)
			}
		})
	}
}

func TestMessageRouteDispatcherRunsAtMostTwoOwnerReadsConcurrently(t *testing.T) {
	fixture := newMioRetrievalRouteFixture(t,
		`{"schema_version":2,"route":"CHAT","outcome":{"kind":"retrieve","requests":[{"source_kind":"user_memory","query":"出発時刻","references":[],"information_needed":"確定した時刻"},{"source_kind":"knowledge_memory","query":"駅までの経路","references":[],"information_needed":"確定した経路"}]}}`,
		`{"schema_version":2,"route":"CHAT","outcome":{"kind":"answer","text":"結果を確認したよ。"}}`,
	)
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var active int32
	var maxActive int32
	fixture.dispatcher.SetMioRetrievalPort(mioRetrievalPortFunc(func(ctx context.Context, _ domainconversation.TurnInput, request domainagent.MioRetrievalRequest) (domainagent.MioRetrievalResult, error) {
		current := atomic.AddInt32(&active, 1)
		for observed := atomic.LoadInt32(&maxActive); current > observed; observed = atomic.LoadInt32(&maxActive) {
			if atomic.CompareAndSwapInt32(&maxActive, observed, current) {
				break
			}
		}
		defer atomic.AddInt32(&active, -1)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > mioRetrievalDatabaseLimit {
			return domainagent.MioRetrievalResult{}, errors.New("database retrieval did not receive its bounded deadline")
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return domainagent.MioRetrievalResult{}, ctx.Err()
		}
		if request.SourceKind == domainagent.MioRetrievalSourceUserMemory {
			return validMioUserMemoryResultForOrchestratorTest(fixture.input, request.Query+" owner result"), nil
		}
		return domainagent.MioRetrievalResult{
			SourceKind: request.SourceKind,
			Status:     domainagent.MioRetrievalStatusFound,
			Items:      []domainagent.MioRetrievalItem{{Text: request.Query + " owner result"}},
		}, nil
	}))

	type execution struct {
		response string
		err      error
	}
	finished := make(chan execution, 1)
	go func() {
		response, err := fixture.execute(context.Background(), t)
		finished <- execution{response: response, err: err}
	}()
	for index := 0; index < 2; index++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("both owner reads did not start concurrently")
		}
	}
	if got := atomic.LoadInt32(&maxActive); got != 2 {
		close(release)
		t.Fatalf("maximum concurrent owner reads = %d, want 2", got)
	}
	close(release)
	select {
	case result := <-finished:
		if result.err != nil || result.response != "結果を確認したよ。" {
			t.Fatalf("executeChatRouteWithDraft() = %q, %v", result.response, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retrieval continuation did not finish")
	}
}

func TestMessageRouteDispatcherBoundsRepeatedMioRetrieval(t *testing.T) {
	retrieve := `{"schema_version":2,"route":"CHAT","outcome":{"kind":"retrieve","requests":[{"source_kind":"user_memory","query":"出発時刻","references":[],"information_needed":"確定した出発時刻"}]}}`
	fixture := newMioRetrievalRouteFixture(t, retrieve, retrieve, retrieve, `{"schema_version":2,"route":"CHAT","outcome":{"kind":"answer","text":"must not generate a fourth time"}}`)
	portCalls := 0
	fixture.dispatcher.SetMioRetrievalPort(mioRetrievalPortFunc(func(_ context.Context, _ domainconversation.TurnInput, request domainagent.MioRetrievalRequest) (domainagent.MioRetrievalResult, error) {
		portCalls++
		return validMioUserMemoryResultForOrchestratorTest(fixture.input, "出発は10時。"), nil
	}))
	_, err := fixture.execute(context.Background(), t)
	if !errors.Is(err, domainagent.ErrMioRetrievalRoundsExceeded) {
		t.Fatalf("executeChatRouteWithDraft() error = %v, want retrieval round limit", err)
	}
	if got := len(fixture.provider.requests); got != 3 {
		t.Fatalf("LLM generations = %d, want initial plus two bounded continuations", got)
	}
	if portCalls != 1 {
		t.Fatalf("repeated retrieval calls = %d, want cached/deduplicated source read once", portCalls)
	}
	if fixture.engine.commitCalls != 0 {
		t.Fatalf("conversation commits = %d, want none after retrieve limit", fixture.engine.commitCalls)
	}
}

func TestMessageRouteDispatcherStopsRetrievalOnCancel(t *testing.T) {
	retrieve := `{"schema_version":2,"route":"CHAT","outcome":{"kind":"retrieve","requests":[{"source_kind":"user_memory","query":"出発時刻","references":[],"information_needed":"確定した出発時刻"}]}}`
	fixture := newMioRetrievalRouteFixture(t, retrieve, `{"schema_version":2,"route":"CHAT","outcome":{"kind":"answer","text":"must not continue after cancellation"}}`)
	ctx, cancel := context.WithCancel(context.Background())
	fixture.dispatcher.SetMioRetrievalPort(mioRetrievalPortFunc(func(ctx context.Context, _ domainconversation.TurnInput, request domainagent.MioRetrievalRequest) (domainagent.MioRetrievalResult, error) {
		cancel()
		<-ctx.Done()
		return domainagent.MioRetrievalResult{}, ctx.Err()
	}))
	_, err := fixture.execute(ctx, t)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("executeChatRouteWithDraft() error = %v, want context cancellation", err)
	}
	if got := len(fixture.provider.requests); got != 1 {
		t.Fatalf("LLM generations = %d, want no continuation after cancellation", got)
	}
	if fixture.engine.commitCalls != 0 {
		t.Fatalf("conversation commits = %d, want none after cancellation", fixture.engine.commitCalls)
	}
}
