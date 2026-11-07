package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type boardPeerChatTestProvider struct {
	calls    int
	scope    conversation.ConversationScope
	request  llm.GenerateRequest
	response string
	err      error
}

func (p *boardPeerChatTestProvider) GenerateConversation(_ context.Context, scope conversation.ConversationScope, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls++
	p.scope = scope
	p.request = request
	return llm.GenerateResponse{Content: p.response}, p.err
}

func (*boardPeerChatTestProvider) Name() string { return "board-peer-chat-test" }

func boardPeerChatTestScope() conversation.ConversationScope {
	return conversation.ConversationScope{
		TurnID: modulecore.NewTurnID(), SessionID: modulecore.NewSessionID(), TraceID: modulecore.NewTraceID(),
		ActorID: "viewer-user", InputID: modulecore.NewMessageID(), GenerationID: "board-peer-chat-generation",
		Purpose: conversation.PurposeExternalPeer, PolicyVersion: "policy-v1",
	}
}

func boardPeerChatTestSnapshot() PromptSnapshot {
	return PromptSnapshot{
		Messages: []llm.Message{
			{Role: "user", Content: "shared thread question"},
			{Role: "assistant", Content: "shared thread answer"},
			{Role: "user", Content: "shared thread follow-up"},
		},
		AllowedSourceMessageIDs: []modulecore.MessageID{modulecore.NewMessageID(), modulecore.NewMessageID(), modulecore.NewMessageID()},
		SourceRevision:          "thread-revision-8", CutoverRevision: "cutover-revision-3",
	}
}

func TestNewBoardPeerChatRejectsUnsupportedActorAndIncompleteConfiguration(t *testing.T) {
	provider := &boardPeerChatTestProvider{}
	for _, test := range []struct {
		name     string
		actor    BoardPeerChatActor
		prompt   string
		provider ConversationGenerationProvider
	}{
		{name: "heavy alias is unsupported", actor: "heavy", prompt: "authored prompt", provider: provider},
		{name: "unknown actor is unsupported", actor: "unknown", prompt: "authored prompt", provider: provider},
		{name: "blank authored prompt", actor: BoardPeerChatActorShiro, prompt: " \n", provider: provider},
		{name: "missing provider", actor: BoardPeerChatActorMidori, prompt: "authored prompt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if runtime, err := NewBoardPeerChatRuntime(test.actor, test.prompt, test.provider); err == nil || runtime != nil {
				t.Fatalf("NewBoardPeerChatRuntime() = (%#v, %v), want a configuration error", runtime, err)
			}
		})
	}
}

func TestBoardPeerChatComposerUsesAuthoredPersonaAndCanonicalExternalHistory(t *testing.T) {
	provider := &boardPeerChatTestProvider{}
	const authoredPrompt = "Shiro authored persona"
	runtime, err := NewBoardPeerChatRuntime(BoardPeerChatActorShiro, authoredPrompt, provider)
	if err != nil {
		t.Fatalf("NewBoardPeerChatRuntime(): %v", err)
	}
	snapshot := boardPeerChatTestSnapshot()
	composed, err := runtime.Composer.ComposeConversationPrompt(context.Background(), boardPeerChatTestScope(), snapshot)
	if err != nil {
		t.Fatalf("ComposeConversationPrompt(): %v", err)
	}
	if len(composed.Messages) != len(snapshot.Messages)+1 || composed.Messages[0].Content != authoredPrompt || composed.Messages[0].Type != llm.PromptContextCharacter {
		t.Fatalf("composed prompt did not use only the authored persona prefix and canonical history: %#v", composed.Messages)
	}
	for _, canonical := range snapshot.Messages {
		count := 0
		for _, message := range composed.Messages {
			if message.Role == canonical.Role && message.Content == canonical.Content {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("canonical message %q occurs %d times, want once", canonical.Content, count)
		}
	}
	if provider.calls != 0 {
		t.Fatalf("prompt composition called generation provider %d times, want zero", provider.calls)
	}
	if composed.SourceRevision != snapshot.SourceRevision || composed.CutoverRevision != snapshot.CutoverRevision {
		t.Fatalf("prompt composition lost canonical provenance: %#v", composed)
	}
}

func TestBoardPeerChatComposerRejectsNonExternalPurpose(t *testing.T) {
	runtime, err := NewBoardPeerChatRuntime(BoardPeerChatActorMidori, "Midori authored persona", &boardPeerChatTestProvider{})
	if err != nil {
		t.Fatalf("NewBoardPeerChatRuntime(): %v", err)
	}
	scope := boardPeerChatTestScope()
	scope.Purpose = conversation.PurposeMioTurn
	if _, err := runtime.Composer.ComposeConversationPrompt(context.Background(), scope, boardPeerChatTestSnapshot()); err == nil {
		t.Fatal("ComposeConversationPrompt() error = nil, want external-peer purpose rejection")
	}
}

func TestBoardPeerChatGeneratorUsesNeutralReplyContractAndRejectsDelegation(t *testing.T) {
	provider := &boardPeerChatTestProvider{response: `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"I can confirm that."}}`}
	runtime, err := NewBoardPeerChatRuntime(BoardPeerChatActorShiro, "Shiro authored persona", provider)
	if err != nil {
		t.Fatalf("NewBoardPeerChatRuntime(): %v", err)
	}
	snapshot := boardPeerChatTestSnapshot()
	allowlist := conversation.EmissionAllowlist{AllowedSourceMessageIDs: append([]modulecore.MessageID(nil), snapshot.AllowedSourceMessageIDs...)}
	emission, receipt, err := runtime.Generator.GenerateEmission(context.Background(), boardPeerChatTestScope(), snapshot, allowlist)
	if err != nil {
		t.Fatalf("GenerateEmission(): %v", err)
	}
	if provider.calls != 1 || provider.scope.Purpose != conversation.PurposeExternalPeer {
		t.Fatalf("generation calls=%d scope purpose=%q, want one external-peer call", provider.calls, provider.scope.Purpose)
	}
	if provider.request.ResponseFormat != llm.ResponseFormatJSONObject || emission.Kind != conversation.EmissionKindReply || emission.UserMessage.To != "external_peer" || emission.UserMessage.Text != "I can confirm that." || emission.Delegation != nil {
		t.Fatalf("unexpected external peer generation result: request=%#v emission=%#v", provider.request, emission)
	}
	if receipt.Status != "completed" || receipt.ProviderLabel != provider.Name() {
		t.Fatalf("unexpected generation receipt: %#v", receipt)
	}
	contracts := 0
	for _, message := range provider.request.Messages {
		if message.Metadata["runtime_context_kind"] != "agent_emission_contract" {
			continue
		}
		contracts++
		if strings.Contains(message.Content, "Mio external peer communication-only") || !strings.Contains(message.Content, "Do not delegate") || !strings.Contains(message.Content, "Do not call tools") {
			t.Fatalf("reply contract retained Mio identity or omitted its safe external-only restrictions: %q", message.Content)
		}
	}
	if contracts != 1 {
		t.Fatalf("neutral reply contract count = %d, want one", contracts)
	}
	if strings.Contains(strings.Join(promptContents(provider.request.Messages), "\n"), "private") {
		t.Fatal("private context appeared in provider request")
	}

	provider.response = `{"schema_version":"mio.turn.v1","kind":"delegate","user_message":{"to":"external_peer","text":"I will ask Midori."},"delegation":{"to":"midori","goal":"investigate","instruction":"inspect","constraints":[],"expected_output":"report","source_message_ids":[]}}`
	_, _, err = runtime.Generator.GenerateEmission(context.Background(), boardPeerChatTestScope(), snapshot, allowlist)
	if err == nil || provider.calls != 2 {
		t.Fatalf("delegation output error=%v calls=%d; want rejection after one additional generation", err, provider.calls)
	}
}

func TestBoardPeerChatGeneratorRejectsOtherPurposeAndAssigneesBeforeGeneration(t *testing.T) {
	provider := &boardPeerChatTestProvider{}
	runtime, err := NewBoardPeerChatRuntime(BoardPeerChatActorMidori, "Midori authored persona", provider)
	if err != nil {
		t.Fatalf("NewBoardPeerChatRuntime(): %v", err)
	}
	snapshot := boardPeerChatTestSnapshot()
	allowlist := conversation.EmissionAllowlist{AllowedSourceMessageIDs: append([]modulecore.MessageID(nil), snapshot.AllowedSourceMessageIDs...)}
	scope := boardPeerChatTestScope()
	scope.Purpose = conversation.PurposeBoardOwnerRequest
	if _, _, err := runtime.Generator.GenerateEmission(context.Background(), scope, snapshot, allowlist); err == nil {
		t.Fatal("GenerateEmission() error = nil, want external-peer purpose rejection")
	}
	scope = boardPeerChatTestScope()
	allowlist.AllowedAssignees = []string{"shiro"}
	if _, _, err := runtime.Generator.GenerateEmission(context.Background(), scope, snapshot, allowlist); err == nil {
		t.Fatal("GenerateEmission() error = nil, want assignee rejection")
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d, want zero on rejected scope or allowlist", provider.calls)
	}
}

func TestBoardPeerChatGeneratorPropagatesProviderFailureWithoutRetry(t *testing.T) {
	wantErr := errors.New("provider unavailable")
	provider := &boardPeerChatTestProvider{err: wantErr}
	runtime, err := NewBoardPeerChatRuntime(BoardPeerChatActorShiro, "Shiro authored persona", provider)
	if err != nil {
		t.Fatalf("NewBoardPeerChatRuntime(): %v", err)
	}
	snapshot := boardPeerChatTestSnapshot()
	allowlist := conversation.EmissionAllowlist{AllowedSourceMessageIDs: append([]modulecore.MessageID(nil), snapshot.AllowedSourceMessageIDs...)}
	_, _, err = runtime.Generator.GenerateEmission(context.Background(), boardPeerChatTestScope(), snapshot, allowlist)
	if !errors.Is(err, wantErr) || provider.calls != 1 {
		t.Fatalf("GenerateEmission() error=%v calls=%d, want wrapped provider error after one attempt", err, provider.calls)
	}
}

func promptContents(messages []llm.Message) []string {
	contents := make([]string, len(messages))
	for index, message := range messages {
		contents[index] = message.Content
	}
	return contents
}
