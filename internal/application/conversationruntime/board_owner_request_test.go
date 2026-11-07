package conversationruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainnews "github.com/Nyukimin/RenCrow_CORE/internal/domain/newsbrief"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	boardOwnerRequestActorID = "core-owner-actor-7"
	boardOwnerRequestOwnerID = "core-owner-7"
	boardOwnerInstruction    = "Ask the peer to confirm Friday's maintenance window."
	boardOwnerGeneratedText  = "Would you confirm the maintenance window planned for Friday?"
)

type boardOwnerRequestFixture struct {
	input  AcceptedTurnInput
	source BoardOwnerRequestSource
	text   string
}

func newBoardOwnerRequestFixture() boardOwnerRequestFixture {
	sessionID := modulecore.NewSessionID()
	turnID := modulecore.NewTurnID()
	inputMessageID := modulecore.NewMessageID()
	traceID := modulecore.NewTraceID()
	inputEvent := modulecore.NewEventEnvelope(traceID, "", nil, "orchestrator", "conversation.board_owner_request", time.Date(2026, 11, 5, 2, 0, 0, 0, time.UTC), map[string]any{
		"from":            "board_owner",
		"source_actor_id": boardOwnerRequestActorID,
		"to":              "mio",
		"text":            boardOwnerInstruction,
		"content":         boardOwnerInstruction,
		"body":            map[string]any{"text": boardOwnerInstruction},
		"board_owner_request_binding": map[string]any{
			"board_instance_id": "board-instance-14",
			"core_principal_id": "core-principal-9",
			"channel_id":        "channel-33",
			"peer_principal_id": "peer-principal-88",
			"title":             "Friday maintenance window",
			"idempotency_key":   "board-owner-request:owner-7:request-42",
		},
	})
	inputEvent.EventSeq = 4
	inputEvent.SessionID = sessionID
	inputEvent.TurnID = turnID
	inputEvent.MessageID = inputMessageID
	accepted := AcceptedTurn{
		SessionID: sessionID, TurnID: turnID, ActorID: boardOwnerRequestActorID,
		TriggerKind: "board_owner_request", ClientSubmissionID: "owner-request-submission-42",
		InputMessageID: inputMessageID, AdmissionSeq: 4, GenerationEpoch: 3,
		State: "accepted", InputEvent: inputEvent,
	}
	session := SessionState{
		SessionID: sessionID, OwnerID: boardOwnerRequestOwnerID, RuntimeVersion: ConversationRuntimeV2,
		CutoverSeq: 1, AdmissionSeq: 4, ProjectionWatermark: 4, GenerationEpoch: 3,
	}
	request := ReadViewRequest{
		Principal: Principal{OwnerID: boardOwnerRequestOwnerID, ActorID: boardOwnerRequestActorID},
		Session:   session,
		Accepted:  accepted,
	}
	return boardOwnerRequestFixture{
		input: AcceptedTurnInput{ReadViewRequest: request},
		source: BoardOwnerRequestSource{
			OwnerID: boardOwnerRequestOwnerID, BoardInstanceID: "board-instance-14",
			CorePrincipalID: "core-principal-9", ChannelID: "channel-33",
			PeerPrincipalID: "peer-principal-88", Title: "Friday maintenance window",
			IdempotencyKey: "board-owner-request:owner-7:request-42",
		},
		text: boardOwnerInstruction,
	}
}

type boardOwnerRequestJournalFake struct {
	order                *[]string
	loads                []BoardOwnerRequestLookup
	commits              []BoardOwnerRequestCommitInput
	loadErr              error
	commitErr            error
	persistOnCommitError bool
	storedOutcome        BoardOwnerRequestOutcome
	found                bool
	loadOverride         func(BoardOwnerRequestLookup) (BoardOwnerRequestOutcome, bool, error)
}

func (journal *boardOwnerRequestJournalFake) LoadBoardOwnerRequestOutcome(_ context.Context, lookup BoardOwnerRequestLookup) (BoardOwnerRequestOutcome, bool, error) {
	if journal.order != nil {
		*journal.order = append(*journal.order, "load")
	}
	journal.loads = append(journal.loads, lookup)
	if journal.loadOverride != nil {
		return journal.loadOverride(lookup)
	}
	if journal.loadErr != nil {
		return BoardOwnerRequestOutcome{}, false, journal.loadErr
	}
	return journal.storedOutcome, journal.found, nil
}

func (journal *boardOwnerRequestJournalFake) CommitBoardOwnerRequest(_ context.Context, input BoardOwnerRequestCommitInput) (BoardOwnerRequestOutcome, error) {
	if journal.order != nil {
		*journal.order = append(*journal.order, "commit")
	}
	journal.commits = append(journal.commits, input)
	outcome := boardOwnerRequestOutcomeFromCommit(input)
	if journal.commitErr != nil {
		if journal.persistOnCommitError {
			journal.storedOutcome = outcome
			journal.found = true
		}
		return BoardOwnerRequestOutcome{}, journal.commitErr
	}
	journal.storedOutcome = outcome
	journal.found = true
	return outcome, nil
}

func boardOwnerRequestOutcomeFromCommit(input BoardOwnerRequestCommitInput) BoardOwnerRequestOutcome {
	return BoardOwnerRequestOutcome{
		Lookup: input.ExpectedLookup, Event: input.Event, MessageIndex: input.MessageIndex,
		InvocationReceipt: input.InvocationReceipt, Target: input.Target, IdempotencyKey: input.IdempotencyKey,
	}
}

func boardOwnerRequestTestOutcome(lookup BoardOwnerRequestLookup, accepted AcceptedTurn, text string) BoardOwnerRequestOutcome {
	messageID := modulecore.NewMessageID()
	event := modulecore.NewEventEnvelope(accepted.InputEvent.TraceID, accepted.InputEvent.EventID, nil, "conversation_runtime", "agent.response", time.Date(2026, 11, 5, 2, 1, 0, 0, time.UTC), map[string]any{
		"kind": "reply", "visibility": "public", "from": "mio", "source_actor_id": "mio", "to": "external_peer",
		"text": text, "content": text,
	})
	event.SessionID, event.TurnID, event.MessageID = accepted.SessionID, accepted.TurnID, messageID
	event.ActorKind, event.ActorID = "agent", "mio"
	index := MessageIndex{
		MessageID: messageID, EventID: event.EventID, SessionID: accepted.SessionID, TurnID: accepted.TurnID,
		FromActor: "mio", ToKind: "external_peer", ToActor: lookup.Source.PeerPrincipalID,
		Kind: "board_owner_request", Visibility: "public",
	}
	return BoardOwnerRequestOutcome{
		Lookup: lookup, Event: event, MessageIndex: index,
		InvocationReceipt: agent.InvocationReceipt{RequestID: "stored-request-7", ResponseID: "stored-response-7", ProviderLabel: "mio-test", TokensUsed: 12, Status: "completed"},
		Target: NewBoardThreadTarget{
			BoardInstanceID: lookup.Source.BoardInstanceID, CorePrincipalID: lookup.Source.CorePrincipalID,
			ChannelID: lookup.Source.ChannelID, PeerPrincipalID: lookup.Source.PeerPrincipalID, Title: lookup.Source.Title,
		},
		IdempotencyKey: lookup.Source.IdempotencyKey,
	}
}

type boardOwnerRequestComposerSpy struct {
	inner    PromptComposer
	order    *[]string
	calls    int
	scope    conversation.ConversationScope
	snapshot agent.PromptSnapshot
}

func (composer *boardOwnerRequestComposerSpy) ComposeConversationPrompt(ctx context.Context, scope conversation.ConversationScope, snapshot agent.PromptSnapshot) (agent.PromptSnapshot, error) {
	if composer.order != nil {
		*composer.order = append(*composer.order, "compose")
	}
	composer.calls++
	composer.scope = scope
	composer.snapshot = snapshot.Clone()
	return composer.inner.ComposeConversationPrompt(ctx, scope, snapshot)
}

type boardOwnerRequestProviderFake struct {
	order       *[]string
	calls       int
	scope       conversation.ConversationScope
	request     llm.GenerateRequest
	response    llm.GenerateResponse
	generateErr error
}

func (provider *boardOwnerRequestProviderFake) GenerateConversation(_ context.Context, scope conversation.ConversationScope, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	if provider.order != nil {
		*provider.order = append(*provider.order, "generate")
	}
	provider.calls++
	provider.scope = scope
	provider.request = request
	return provider.response, provider.generateErr
}

func (*boardOwnerRequestProviderFake) Name() string { return "mio-board-owner-test" }

type boardOwnerRequestEmissionGeneratorSpy struct {
	inner     EmissionGenerator
	calls     int
	scope     conversation.ConversationScope
	snapshot  agent.PromptSnapshot
	allowlist conversation.EmissionAllowlist
}

func (generator *boardOwnerRequestEmissionGeneratorSpy) GenerateEmission(ctx context.Context, scope conversation.ConversationScope, snapshot agent.PromptSnapshot, allowlist conversation.EmissionAllowlist) (conversation.AgentEmission, agent.InvocationReceipt, error) {
	generator.calls++
	generator.scope = scope
	generator.snapshot = snapshot.Clone()
	generator.allowlist = allowlist
	return generator.inner.GenerateEmission(ctx, scope, snapshot, allowlist)
}

type boardOwnerRequestConversationEngineFake struct {
	calls int
}

func (engine *boardOwnerRequestConversationEngineFake) BeginTurn(context.Context, string, string) (*conversation.RecallPack, error) {
	engine.calls++
	return &conversation.RecallPack{
		UserProfile: conversation.UserProfile{Facts: []string{"PRIVATE_BOARD_OWNER_MEMORY_MARKER"}},
		LongFacts:   []string{"PRIVATE_BOARD_OWNER_LONG_MEMORY_MARKER"},
	}, nil
}

func (*boardOwnerRequestConversationEngineFake) GetPersona() conversation.PersonaState {
	return conversation.PersonaState{}
}

func (*boardOwnerRequestConversationEngineFake) FlushCurrentThread(context.Context, string) error {
	return nil
}

func (*boardOwnerRequestConversationEngineFake) GetStatus(context.Context, string) (*conversation.ConversationStatus, error) {
	return nil, nil
}

func (*boardOwnerRequestConversationEngineFake) ResetSession(context.Context, string) error {
	return nil
}

func boardOwnerRequestExpectedSource(fixture boardOwnerRequestFixture) BoardOwnerRequestSource {
	return fixture.source
}

func boardOwnerRequestExpectedLookup(fixture boardOwnerRequestFixture) BoardOwnerRequestLookup {
	accepted := fixture.input.ReadViewRequest.Accepted
	source := boardOwnerRequestExpectedSource(fixture)
	return BoardOwnerRequestLookup{
		Source: source, OwnerID: fixture.input.ReadViewRequest.Session.OwnerID,
		SessionID: accepted.SessionID, TurnID: accepted.TurnID, GenerationEpoch: accepted.GenerationEpoch,
		OperationID: turnOperationID(accepted.TurnID), SourceFingerprint: boardOwnerRequestSourceFingerprint(source, fixture.text),
	}
}

func boardOwnerRequestSourceFingerprint(source BoardOwnerRequestSource, body string) string {
	material, _ := json.Marshal(struct {
		Source BoardOwnerRequestSource `json:"source"`
		Body   string                  `json:"body"`
	}{Source: source, Body: body})
	fingerprint := sha256.Sum256(material)
	return hex.EncodeToString(fingerprint[:])
}

func newBoardOwnerRequestRuntimeForTest(t *testing.T, composer PromptComposer, generator EmissionGenerator, journal BoardOwnerRequestJournal) *BoardOwnerRequestRuntime {
	t.Helper()
	runtime, err := NewBoardOwnerRequestRuntime(BoardOwnerRequestRuntimeDependencies{
		Composer: composer, Generator: generator, Journal: journal, PolicyVersion: "board-owner-request-v1",
	})
	if err != nil {
		t.Fatalf("NewBoardOwnerRequestRuntime(): %v", err)
	}
	return runtime
}

func ownerRequestMioComposer(engine *boardOwnerRequestConversationEngineFake, recentCalls, runtimeCalls *int) *agent.MioAgent {
	return agent.NewMioAgent(nil, nil, nil, nil, nil, engine).
		WithSystemPrompt("authored Mio system persona").
		WithStableRuntimeContexts(map[string]string{"mio": "PRIVATE_BOARD_OWNER_STABLE_OPS_MARKER"}).
		WithRuntimeContextProvider(func(context.Context, string) string {
			*runtimeCalls = *runtimeCalls + 1
			return "PRIVATE_BOARD_OWNER_RUNTIME_OPS_MARKER"
		}).
		WithRecentContextProvider(func(context.Context, int) (string, error) {
			*recentCalls = *recentCalls + 1
			return "PRIVATE_BOARD_OWNER_RECENT_MARKER", nil
		}).
		WithViewerRecipientPrompts(map[string]string{"mio": "PRIVATE_BOARD_OWNER_VIEWER_HANDLER_MARKER"})
}

func TestRunAcceptedBoardOwnerRequestUsesMioAndCommitsPrivateUnresolvedTarget(t *testing.T) {
	fixture := newBoardOwnerRequestFixture()
	var order []string
	journal := &boardOwnerRequestJournalFake{order: &order}
	engine := &boardOwnerRequestConversationEngineFake{}
	recentCalls, runtimeCalls := 0, 0
	composer := &boardOwnerRequestComposerSpy{
		inner: ownerRequestMioComposer(engine, &recentCalls, &runtimeCalls), order: &order,
	}
	requestID, responseID := modulecore.NewRequestID(), modulecore.NewResponseID()
	responseJSON, err := json.Marshal(conversation.AgentEmission{
		SchemaVersion: "mio.turn.v1", Kind: conversation.EmissionKindReply,
		UserMessage: conversation.AgentUserMessage{To: "external_peer", Text: boardOwnerGeneratedText},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &boardOwnerRequestProviderFake{
		order: &order,
		response: llm.GenerateResponse{
			Content: string(responseJSON), RequestID: requestID, ResponseID: responseID, TokensUsed: 31,
		},
	}
	generator := &boardOwnerRequestEmissionGeneratorSpy{inner: agent.NewMioEmissionGenerator(provider)}
	runtime := newBoardOwnerRequestRuntimeForTest(t, composer, generator, journal)

	ctx := agent.WithDailyNewsBrief(context.Background(), domainnews.DailyNewsBrief{
		Date: "2026-11-05", Source: domainnews.SourceScheduled, Status: domainnews.StatusReady,
		Items: []domainnews.Item{{ID: "private-board-owner-news", Title: "PRIVATE_BOARD_OWNER_DAILY_NEWS_MARKER"}},
	})
	result := runtime.RunAcceptedBoardOwnerRequest(ctx, fixture.input)
	if result.Status != BoardOwnerRequestStatusCommitted || result.Cause != nil || result.Replayed || result.Recovered {
		t.Fatalf("RunAcceptedBoardOwnerRequest() = %+v, want newly committed owner request", result)
	}
	if got, want := order, []string{"load", "compose", "generate", "commit"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	if len(journal.loads) != 1 || len(journal.commits) != 1 || composer.calls != 1 || generator.calls != 1 || provider.calls != 1 {
		t.Fatalf("load/compose/generate/provider/commit counts = %d/%d/%d/%d/%d, want 1/1/1/1/1", len(journal.loads), composer.calls, generator.calls, provider.calls, len(journal.commits))
	}
	accepted := fixture.input.ReadViewRequest.Accepted
	wantScope := conversation.ConversationScope{
		TurnID: accepted.TurnID, SessionID: accepted.SessionID, TraceID: accepted.InputEvent.TraceID,
		ActorID: accepted.ActorID, InputID: accepted.InputMessageID,
		GenerationID: string(accepted.TurnID) + ":3", Purpose: conversation.PurposeBoardOwnerRequest,
		PolicyVersion: "board-owner-request-v1",
	}
	if provider.scope != wantScope || composer.scope != wantScope || generator.scope != wantScope {
		t.Fatalf("Mio scope = provider:%+v composer:%+v generator:%+v, want %+v", provider.scope, composer.scope, generator.scope, wantScope)
	}
	if len(composer.snapshot.Messages) != 1 || composer.snapshot.Messages[0].Role != "user" || composer.snapshot.Messages[0].Content != fixture.text ||
		len(generator.allowlist.AllowedAssignees) != 0 || !reflect.DeepEqual(generator.allowlist.AllowedSourceMessageIDs, []modulecore.MessageID{accepted.InputMessageID}) {
		t.Fatalf("owner input or restricted allowlist changed across Mio seams: snapshot=%+v allowlist=%+v", composer.snapshot, generator.allowlist)
	}
	if len(journal.commits) != 1 {
		t.Fatalf("journal commits = %d, want 1", len(journal.commits))
	}
	commit := journal.commits[0]
	wantLookup := boardOwnerRequestExpectedLookup(fixture)
	if commit.ExpectedLookup != wantLookup {
		t.Fatalf("journal lookup = %+v, want exact accepted owner/source/scope/fingerprint %+v", commit.ExpectedLookup, wantLookup)
	}
	if len(commit.ExpectedLookup.SourceFingerprint) != sha256.Size*2 {
		t.Fatalf("source fingerprint length = %d, want %d hex characters", len(commit.ExpectedLookup.SourceFingerprint), sha256.Size*2)
	}
	target := NewBoardThreadTarget{
		BoardInstanceID: fixture.source.BoardInstanceID, CorePrincipalID: fixture.source.CorePrincipalID,
		ChannelID: fixture.source.ChannelID, PeerPrincipalID: fixture.source.PeerPrincipalID, Title: fixture.source.Title,
	}
	if commit.Target != target || commit.IdempotencyKey != fixture.source.IdempotencyKey {
		t.Fatalf("private new-thread target/key = %+v/%q, want %+v/%q", commit.Target, commit.IdempotencyKey, target, fixture.source.IdempotencyKey)
	}
	if result.Outcome.Target != target || result.Outcome.IdempotencyKey != fixture.source.IdempotencyKey {
		t.Fatalf("committed outcome lost private target/key: %+v", result.Outcome)
	}
	event := commit.Event
	if event.EventType != "agent.response" || event.ComponentID != "conversation_runtime" || event.ActorKind != "agent" || event.ActorID != "mio" ||
		event.SessionID != accepted.SessionID || event.TurnID != accepted.TurnID || event.TraceID != accepted.InputEvent.TraceID ||
		event.CausationEventID != accepted.InputEvent.EventID || event.ThreadID != "" || event.MessageID == accepted.InputMessageID || event.MessageID == "" {
		t.Fatalf("canonical Mio response envelope is invalid: %+v", event)
	}
	if err := modulecore.ValidateEventEnvelope(event); err != nil {
		t.Fatalf("canonical Mio response event validation: %v", err)
	}
	if event.Payload["from"] != "mio" || event.Payload["source_actor_id"] != "mio" || event.Payload["to"] != "external_peer" ||
		event.Payload["text"] != boardOwnerGeneratedText || event.Payload["content"] != boardOwnerGeneratedText ||
		event.Payload["text"] == fixture.text || event.Payload["board_owner_request_binding"] != nil || event.Payload["idempotency_key"] != nil {
		t.Fatalf("canonical response payload leaked owner input/target or changed generated text: %#v", event.Payload)
	}
	if commit.MessageIndex.MessageID != event.MessageID || commit.MessageIndex.EventID != event.EventID ||
		commit.MessageIndex.FromActor != "mio" || commit.MessageIndex.ToKind != "external_peer" ||
		commit.MessageIndex.ToActor != fixture.source.PeerPrincipalID || commit.MessageIndex.ReplyToMessageID != "" {
		t.Fatalf("canonical response index has a fake thread/reply or wrong peer: %+v", commit.MessageIndex)
	}
	wantReceipt := agent.InvocationReceipt{
		RequestID: string(requestID), ResponseID: string(responseID), ProviderLabel: "mio-board-owner-test",
		TokensUsed: 31, Status: "completed",
	}
	if commit.InvocationReceipt != wantReceipt || result.Outcome.InvocationReceipt != wantReceipt {
		t.Fatalf("invocation receipt = %+v / %+v, want %+v", commit.InvocationReceipt, result.Outcome.InvocationReceipt, wantReceipt)
	}
	if provider.request.ResponseFormat != llm.ResponseFormatJSONObject || provider.request.OnToken != nil {
		t.Fatalf("Mio provider request is not bounded non-stream JSON: format=%q onToken=%t", provider.request.ResponseFormat, provider.request.OnToken != nil)
	}
	if promptMessageCount(provider.request.Messages, fixture.text) != 1 || !promptMessagesContain(provider.request.Messages, "authored Mio system persona") {
		t.Fatalf("Mio request lost persona or exact owner instruction: %#v", provider.request.Messages)
	}
	for _, marker := range []string{
		"PRIVATE_BOARD_OWNER_MEMORY_MARKER", "PRIVATE_BOARD_OWNER_LONG_MEMORY_MARKER",
		"PRIVATE_BOARD_OWNER_STABLE_OPS_MARKER", "PRIVATE_BOARD_OWNER_RUNTIME_OPS_MARKER",
		"PRIVATE_BOARD_OWNER_RECENT_MARKER", "PRIVATE_BOARD_OWNER_VIEWER_HANDLER_MARKER",
		"PRIVATE_BOARD_OWNER_DAILY_NEWS_MARKER",
	} {
		if promptMessagesContain(provider.request.Messages, marker) {
			t.Fatalf("private context marker %q was injected into owner request prompt", marker)
		}
	}
	if engine.calls != 0 || recentCalls != 0 || runtimeCalls != 0 {
		t.Fatalf("private prompt suppliers were called: memory=%d recent=%d runtime=%d", engine.calls, recentCalls, runtimeCalls)
	}
	for _, key := range []string{"tools", "tool_choice", "functions", "function_call"} {
		if _, ok := provider.request.ProviderOptions[key]; ok {
			t.Fatalf("Mio request exposed tool option %q", key)
		}
	}
}

func TestRunAcceptedBoardOwnerRequestRejectsCompletedReceiptWithoutIDsBeforeCommit(t *testing.T) {
	for _, missingID := range []string{"request_id", "response_id"} {
		t.Run(missingID, func(t *testing.T) {
			fixture := newBoardOwnerRequestFixture()
			var order []string
			journal := &boardOwnerRequestJournalFake{order: &order}
			engine := &boardOwnerRequestConversationEngineFake{}
			recentCalls, runtimeCalls := 0, 0
			composer := &boardOwnerRequestComposerSpy{
				inner: ownerRequestMioComposer(engine, &recentCalls, &runtimeCalls), order: &order,
			}
			responseJSON, err := json.Marshal(conversation.AgentEmission{
				SchemaVersion: "mio.turn.v1", Kind: conversation.EmissionKindReply,
				UserMessage: conversation.AgentUserMessage{To: "external_peer", Text: boardOwnerGeneratedText},
			})
			if err != nil {
				t.Fatal(err)
			}
			response := llm.GenerateResponse{
				Content: string(responseJSON), RequestID: modulecore.NewRequestID(),
				ResponseID: modulecore.NewResponseID(), TokensUsed: 31,
			}
			switch missingID {
			case "request_id":
				response.RequestID = ""
			case "response_id":
				response.ResponseID = ""
			}
			provider := &boardOwnerRequestProviderFake{order: &order, response: response}
			runtime := newBoardOwnerRequestRuntimeForTest(
				t, composer, agent.NewMioEmissionGenerator(provider), journal,
			)

			result := runtime.RunAcceptedBoardOwnerRequest(context.Background(), fixture.input)
			if result.Status != BoardOwnerRequestStatusFailedBeforeCommit || result.Cause == nil {
				t.Fatalf("invalid completed Mio receipt result = %+v, want failure before commit", result)
			}
			if len(journal.commits) != 0 || journal.found {
				t.Fatalf("invalid completed Mio receipt reached journal/publication: commits=%d found=%t", len(journal.commits), journal.found)
			}
			if got, want := order, []string{"load", "compose", "generate"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("invalid receipt operation order = %v, want %v", got, want)
			}
			if provider.calls != 1 {
				t.Fatalf("synthetic Mio provider calls = %d, want 1", provider.calls)
			}
		})
	}
}

func TestRunAcceptedBoardOwnerRequestReplaysBeforeMioAndFailsClosed(t *testing.T) {
	t.Run("valid stored outcome", func(t *testing.T) {
		fixture := newBoardOwnerRequestFixture()
		var order []string
		journal := &boardOwnerRequestJournalFake{order: &order}
		journal.loadOverride = func(lookup BoardOwnerRequestLookup) (BoardOwnerRequestOutcome, bool, error) {
			return boardOwnerRequestTestOutcome(lookup, fixture.input.ReadViewRequest.Accepted, "Stored generated request text."), true, nil
		}
		composer := &boardOwnerRequestComposerSpy{inner: agent.NewMioAgent(nil, nil, nil, nil, nil, nil).WithSystemPrompt("authored Mio system persona"), order: &order}
		provider := &boardOwnerRequestProviderFake{order: &order}
		runtime := newBoardOwnerRequestRuntimeForTest(t, composer, agent.NewMioEmissionGenerator(provider), journal)

		result := runtime.RunAcceptedBoardOwnerRequest(context.Background(), fixture.input)
		if result.Status != BoardOwnerRequestStatusCommitted || result.Cause != nil || !result.Replayed {
			t.Fatalf("replayed board owner request = %+v, want stored outcome without generation", result)
		}
		if !reflect.DeepEqual(order, []string{"load"}) || len(journal.commits) != 0 || composer.calls != 0 || provider.calls != 0 {
			t.Fatalf("stored outcome replay performed new work: order=%v commits=%d compose=%d provider=%d", order, len(journal.commits), composer.calls, provider.calls)
		}
		if len(journal.loads) != 1 || journal.loads[0] != boardOwnerRequestExpectedLookup(fixture) {
			t.Fatalf("stored outcome lookup = %+v, want exact accepted scope %+v", journal.loads, boardOwnerRequestExpectedLookup(fixture))
		}
	})

	t.Run("stored scope mismatch", func(t *testing.T) {
		fixture := newBoardOwnerRequestFixture()
		journal := &boardOwnerRequestJournalFake{}
		journal.loadOverride = func(lookup BoardOwnerRequestLookup) (BoardOwnerRequestOutcome, bool, error) {
			outcome := boardOwnerRequestTestOutcome(lookup, fixture.input.ReadViewRequest.Accepted, "Stored request text.")
			outcome.Lookup.GenerationEpoch++
			return outcome, true, nil
		}
		provider := &boardOwnerRequestProviderFake{}
		runtime := newBoardOwnerRequestRuntimeForTest(t, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), agent.NewMioEmissionGenerator(provider), journal)

		result := runtime.RunAcceptedBoardOwnerRequest(context.Background(), fixture.input)
		if result.Status == BoardOwnerRequestStatusCommitted || result.Cause == nil || len(journal.commits) != 0 || provider.calls != 0 {
			t.Fatalf("mismatched stored scope was not failed closed: result=%+v commits=%d provider=%d", result, len(journal.commits), provider.calls)
		}
	})

	t.Run("load uncertainty", func(t *testing.T) {
		fixture := newBoardOwnerRequestFixture()
		journal := &boardOwnerRequestJournalFake{loadErr: errors.New("journal read uncertain")}
		provider := &boardOwnerRequestProviderFake{}
		runtime := newBoardOwnerRequestRuntimeForTest(t, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), agent.NewMioEmissionGenerator(provider), journal)

		result := runtime.RunAcceptedBoardOwnerRequest(context.Background(), fixture.input)
		if result.Status != BoardOwnerRequestStatusCommitUncertain || result.Cause == nil || len(journal.commits) != 0 || provider.calls != 0 {
			t.Fatalf("outcome lookup uncertainty did not fail closed: result=%+v commits=%d provider=%d", result, len(journal.commits), provider.calls)
		}
	})
}

func TestRunAcceptedBoardOwnerRequestRecoversUnknownCommitFromExactOutcome(t *testing.T) {
	fixture := newBoardOwnerRequestFixture()
	var order []string
	unknownCommitErr := errors.New("commit response unknown")
	journal := &boardOwnerRequestJournalFake{
		order: &order, commitErr: unknownCommitErr, persistOnCommitError: true,
	}
	composer := &boardOwnerRequestComposerSpy{
		inner: agent.NewMioAgent(nil, nil, nil, nil, nil, nil).WithSystemPrompt("authored Mio system persona"), order: &order,
	}
	responseJSON, _ := json.Marshal(conversation.AgentEmission{
		SchemaVersion: "mio.turn.v1", Kind: conversation.EmissionKindReply,
		UserMessage: conversation.AgentUserMessage{To: "external_peer", Text: boardOwnerGeneratedText},
	})
	provider := &boardOwnerRequestProviderFake{
		order:    &order,
		response: llm.GenerateResponse{Content: string(responseJSON), RequestID: modulecore.NewRequestID(), ResponseID: modulecore.NewResponseID(), TokensUsed: 19},
	}
	runtime := newBoardOwnerRequestRuntimeForTest(t, composer, agent.NewMioEmissionGenerator(provider), journal)

	result := runtime.RunAcceptedBoardOwnerRequest(context.Background(), fixture.input)
	if result.Status != BoardOwnerRequestStatusCommitted || result.Cause != nil || !result.Recovered {
		t.Fatalf("unknown commit recovery = %+v, want exact stored outcome", result)
	}
	if got, want := order, []string{"load", "compose", "generate", "commit", "load"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("commit recovery order = %v, want %v", got, want)
	}
	if len(journal.loads) != 2 || journal.loads[0] != journal.loads[1] || len(journal.commits) != 1 || provider.calls != 1 {
		t.Fatalf("recovery duplicated generation or changed lookup: loads=%+v commits=%d provider=%d", journal.loads, len(journal.commits), provider.calls)
	}
	if !reflect.DeepEqual(result.Outcome, journal.storedOutcome) {
		t.Fatalf("recovered result differs from durable outcome: got=%+v saved=%+v", result.Outcome, journal.storedOutcome)
	}
	if result.Outcome.IdempotencyKey != fixture.source.IdempotencyKey || result.Outcome.Event.Payload["text"] != boardOwnerGeneratedText {
		t.Fatalf("recovery changed the original target key or generated content: %+v", result.Outcome)
	}
}

func TestRunAcceptedBoardOwnerRequestDoesNotCommitGenerationFailureOrForwardInstruction(t *testing.T) {
	fixture := newBoardOwnerRequestFixture()
	var order []string
	journal := &boardOwnerRequestJournalFake{order: &order}
	composer := &boardOwnerRequestComposerSpy{
		inner: agent.NewMioAgent(nil, nil, nil, nil, nil, nil).WithSystemPrompt("authored Mio system persona"), order: &order,
	}
	providerErr := errors.New("provider unavailable")
	provider := &boardOwnerRequestProviderFake{
		order: &order, response: llm.GenerateResponse{RequestID: modulecore.NewRequestID(), ResponseID: modulecore.NewResponseID()}, generateErr: providerErr,
	}
	runtime := newBoardOwnerRequestRuntimeForTest(t, composer, agent.NewMioEmissionGenerator(provider), journal)

	result := runtime.RunAcceptedBoardOwnerRequest(context.Background(), fixture.input)
	if result.Status != BoardOwnerRequestStatusFailedBeforeCommit || !errors.Is(result.Cause, providerErr) || len(journal.commits) != 0 {
		t.Fatalf("generation failure produced a committed request or lost its error: %+v commits=%d", result, len(journal.commits))
	}
	if provider.calls != 1 || !reflect.DeepEqual(order, []string{"load", "compose", "generate"}) {
		t.Fatalf("generation failure work order/calls = %v / %d, want one generation and no commit", order, provider.calls)
	}
	if result.Outcome.Event.EventID != "" || result.Outcome.Event.Payload != nil || fixture.text == boardOwnerGeneratedText {
		t.Fatalf("generation failure exposed a fallback response: result=%+v source=%q", result, fixture.text)
	}
}

func TestRunAcceptedBoardOwnerRequestRejectsUntrustedAcceptedSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*boardOwnerRequestFixture)
	}{
		{
			name: "payload owner actor differs from accepted actor",
			mutate: func(fixture *boardOwnerRequestFixture) {
				fixture.input.ReadViewRequest.Accepted.InputEvent.Payload["source_actor_id"] = "forged-owner-actor"
			},
		},
		{
			name: "accepted envelope actor is stamped",
			mutate: func(fixture *boardOwnerRequestFixture) {
				fixture.input.ReadViewRequest.Accepted.InputEvent.ActorKind = "agent"
				fixture.input.ReadViewRequest.Accepted.InputEvent.ActorID = "mio"
			},
		},
		{
			name: "body copies differ",
			mutate: func(fixture *boardOwnerRequestFixture) {
				fixture.input.ReadViewRequest.Accepted.InputEvent.Payload["content"] = "different body"
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newBoardOwnerRequestFixture()
			tc.mutate(&fixture)
			journal := &boardOwnerRequestJournalFake{}
			provider := &boardOwnerRequestProviderFake{}
			runtime := newBoardOwnerRequestRuntimeForTest(t, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), agent.NewMioEmissionGenerator(provider), journal)

			result := runtime.RunAcceptedBoardOwnerRequest(context.Background(), fixture.input)
			if result.Status == BoardOwnerRequestStatusCommitted || result.Cause == nil || len(journal.loads) != 0 || len(journal.commits) != 0 || provider.calls != 0 {
				t.Fatalf("untrusted accepted source was consumed: result=%+v loads=%d commits=%d provider=%d", result, len(journal.loads), len(journal.commits), provider.calls)
			}
		})
	}
}

func promptMessageCount(messages []llm.Message, content string) int {
	count := 0
	for _, message := range messages {
		if message.Content == content {
			count++
		}
	}
	return count
}

func promptMessagesContain(messages []llm.Message, content string) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, content) {
			return true
		}
	}
	return false
}
