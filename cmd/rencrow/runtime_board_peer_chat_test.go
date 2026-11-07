package main

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type boardPeerChatRuntimeProviderFake struct {
	calls    int
	request  llm.GenerateRequest
	response string
}

func (provider *boardPeerChatRuntimeProviderFake) GenerateConversation(_ context.Context, _ conversation.ConversationScope, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	provider.calls++
	provider.request = request
	return llm.GenerateResponse{Content: provider.response}, nil
}

func (*boardPeerChatRuntimeProviderFake) Name() string { return "runtime-board-peer-chat-fake" }

func TestBoardPeerChatWiringRequiresExplicitResponderAndExactAuthoredPrompt(t *testing.T) {
	akaProvider := &boardPeerChatRuntimeProviderFake{}
	aoProvider := &boardPeerChatRuntimeProviderFake{}
	cfg := &config.Config{
		BoardCommunication: config.BoardCommunicationConfig{Enabled: true, Responders: []config.BoardCommunicationResponderConfig{{ActorID: "aka"}}},
		Prompts:            &config.LoadedPrompts{CharacterPrompts: map[string]string{"aka": "exact Aka workspace bundle", "ao": "unconfigured Ao bundle"}},
	}
	wiring := boardPeerChatWiringFor(cfg, map[string]agent.ConversationGenerationProvider{"aka": akaProvider, "ao": aoProvider})
	if len(wiring) != 1 || wiring["aka"].Provider != akaProvider || wiring["aka"].SystemPrompt != "exact Aka workspace bundle" {
		t.Fatalf("BOARD character wiring = %#v, want only Aka's exact workspace prompt/provider", wiring)
	}
	if _, exists := wiring["ao"]; exists {
		t.Fatal("unconfigured Ao was wired from an available prompt/provider")
	}

	cfg.Prompts.CharacterPrompts["aka"] = "  "
	wiring = boardPeerChatWiringFor(cfg, map[string]agent.ConversationGenerationProvider{"aka": akaProvider})
	if len(wiring) != 0 {
		t.Fatalf("missing authored prompt wiring = %#v, want empty fail-closed wiring", wiring)
	}
	if _, err := newBoardPeerChatRuntime("aka", boardCommunicationResponderWiring{
		Readback: responseFirstReadbackWiring{Assignees: map[string]agent.ReadbackAssigneeConfig{
			"aka": {Provider: aoProvider, SystemPrompt: "Coder fallback"},
		}},
		PeerChat: wiring,
	}); err == nil {
		t.Fatal("missing authored prompt fell back to Coder/readback provider")
	}
}

func TestNewBoardPeerChatRuntimeUsesReviewedActorProviderAndAuthoredPrompt(t *testing.T) {
	for _, test := range []struct {
		actor   string
		prompt  string
		context string
	}{
		{actor: "shiro", prompt: "authored Shiro persona", context: "private Shiro stable runtime context"},
		{actor: "midori", prompt: "authored Midori persona", context: "private Midori stable runtime context"},
		{actor: "kuro", prompt: "authored Kuro persona", context: "private Kuro stable runtime context"},
		{actor: "aka", prompt: "authored Aka character bundle", context: "private Aka Coder runtime context"},
		{actor: "ao", prompt: "authored Ao character bundle", context: "private Ao Coder runtime context"},
		{actor: "kin", prompt: "authored Kin character bundle", context: "private Kin Coder runtime context"},
		{actor: "gin", prompt: "authored Gin character bundle", context: "private Gin Coder runtime context"},
		{actor: "lumina", prompt: "authored Lumina character bundle", context: "private Lumina runtime context"},
	} {
		t.Run(test.actor, func(t *testing.T) {
			provider := &boardPeerChatRuntimeProviderFake{
				response: `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"Confirmed."}}`,
			}
			fallbackProvider := &boardPeerChatRuntimeProviderFake{}
			wiring := boardCommunicationResponderWiring{Readback: responseFirstReadbackWiring{Assignees: map[string]agent.ReadbackAssigneeConfig{}}}
			cfg := &config.Config{
				BoardCommunication: config.BoardCommunicationConfig{Enabled: true, Responders: []config.BoardCommunicationResponderConfig{{ActorID: test.actor}}},
				Prompts: &config.LoadedPrompts{
					Heavy: test.context, CharacterPrompts: map[string]string{test.actor: test.prompt},
					StableRuntimeContexts: map[string]string{test.actor: test.context},
				},
			}
			if test.actor == "kuro" || conversation.IsBoardSpeechCharacter(test.actor) {
				wiring.Readback.Assignees[test.actor] = agent.ReadbackAssigneeConfig{Provider: fallbackProvider, SystemPrompt: "ordinary/Coder fallback must not be used"}
				wiring.PeerChat = boardPeerChatWiringFor(cfg, map[string]agent.ConversationGenerationProvider{test.actor: provider})
			} else {
				wiring.Readback.Assignees[test.actor] = agent.ReadbackAssigneeConfig{Provider: provider, SystemPrompt: test.prompt, StableRuntimeContext: test.context}
			}
			runtime, err := newBoardPeerChatRuntime(test.actor, wiring)
			if err != nil {
				t.Fatalf("newBoardPeerChatRuntime(): %v", err)
			}
			if runtime.Actor() != agent.BoardPeerChatActor(test.actor) {
				t.Fatalf("runtime actor = %q, want %q", runtime.Actor(), test.actor)
			}

			scope := boardPeerChatRuntimeTestScope()
			snapshot := boardPeerChatRuntimeTestSnapshot()
			composed, err := runtime.Composer.ComposeConversationPrompt(context.Background(), scope, snapshot)
			if err != nil {
				t.Fatalf("ComposeConversationPrompt(): %v", err)
			}
			if len(composed.Messages) == 0 || composed.Messages[0].Content != test.prompt {
				t.Fatalf("composed system prompt = %#v, want authored prompt %q", composed.Messages, test.prompt)
			}
			allowlist := conversation.EmissionAllowlist{AllowedSourceMessageIDs: append([]modulecore.MessageID(nil), snapshot.AllowedSourceMessageIDs...)}
			if _, _, err := runtime.Generator.GenerateEmission(context.Background(), scope, composed, allowlist); err != nil {
				t.Fatalf("GenerateEmission(): %v", err)
			}
			if provider.calls != 1 {
				t.Fatalf("selected provider calls = %d, want 1", provider.calls)
			}
			if provider.request.OnToken != nil || provider.request.OnLogicalOutcomeEvent != nil {
				t.Fatalf("BOARD CHAT request enabled streaming callbacks: token=%t logical=%t", provider.request.OnToken != nil, provider.request.OnLogicalOutcomeEvent != nil)
			}
			if fallbackProvider.calls != 0 {
				t.Fatalf("ordinary/Heavy/Coder/readback fallback calls = %d, want 0", fallbackProvider.calls)
			}
			for _, message := range provider.request.Messages {
				if strings.Contains(message.Content, test.context) {
					t.Fatalf("private stable runtime context reached provider: %q", message.Content)
				}
			}
		})
	}
}

func TestNewBoardPeerChatRuntimeRejectsKuroAndIncompleteWiring(t *testing.T) {
	provider := &boardPeerChatRuntimeProviderFake{}
	for _, test := range []struct {
		name   string
		actor  string
		wiring boardCommunicationResponderWiring
	}{
		{
			name:  "Kuro Heavy/readback cannot substitute for dedicated BOARD CHAT provider",
			actor: "kuro",
			wiring: boardCommunicationResponderWiring{Readback: responseFirstReadbackWiring{Assignees: map[string]agent.ReadbackAssigneeConfig{
				"kuro": {Provider: provider, SystemPrompt: "full access prompt"},
			}}},
		},
		{
			name:  "BOARD character ordinary/Coder/readback cannot substitute for dedicated provider",
			actor: "aka",
			wiring: boardCommunicationResponderWiring{Readback: responseFirstReadbackWiring{Assignees: map[string]agent.ReadbackAssigneeConfig{
				"aka": {Provider: provider, SystemPrompt: "Coder persona"},
			}}},
		},
		{name: "missing actor config", actor: "shiro", wiring: boardCommunicationResponderWiring{Readback: responseFirstReadbackWiring{Assignees: map[string]agent.ReadbackAssigneeConfig{}}}},
		{
			name:  "missing provider",
			actor: "shiro",
			wiring: boardCommunicationResponderWiring{Readback: responseFirstReadbackWiring{Assignees: map[string]agent.ReadbackAssigneeConfig{
				"shiro": {SystemPrompt: "authored Shiro persona"},
			}}},
		},
		{
			name:  "missing authored prompt",
			actor: "midori",
			wiring: boardCommunicationResponderWiring{Readback: responseFirstReadbackWiring{Assignees: map[string]agent.ReadbackAssigneeConfig{
				"midori": {Provider: provider, StableRuntimeContext: "not an authored persona"},
			}}},
		},
		{
			name:   "missing BOARD character authored prompt",
			actor:  "lumina",
			wiring: boardCommunicationResponderWiring{PeerChat: boardPeerChatWiring{"lumina": {Provider: provider}}},
		},
		{name: "unsupported heavy alias is rejected", actor: "heavy", wiring: boardCommunicationResponderWiring{PeerChat: boardPeerChatWiring{"heavy": {Provider: provider, SystemPrompt: "Heavy persona"}}}},
		{name: "unknown actor is rejected", actor: "unknown", wiring: boardCommunicationResponderWiring{PeerChat: boardPeerChatWiring{"unknown": {Provider: provider, SystemPrompt: "unknown persona"}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if runtime, err := newBoardPeerChatRuntime(test.actor, test.wiring); err == nil || runtime != nil {
				t.Fatalf("newBoardPeerChatRuntime() = (%#v, %v), want a fail-closed configuration error", runtime, err)
			}
		})
	}
}

func boardPeerChatRuntimeTestScope() conversation.ConversationScope {
	return conversation.ConversationScope{
		TurnID: modulecore.NewTurnID(), SessionID: modulecore.NewSessionID(), TraceID: modulecore.NewTraceID(),
		ActorID: "viewer-user", InputID: modulecore.NewMessageID(), GenerationID: "runtime-board-peer-chat-generation",
		Purpose: conversation.PurposeExternalPeer, PolicyVersion: "response-first-v1.0",
	}
}

func boardPeerChatRuntimeTestSnapshot() agent.PromptSnapshot {
	return agent.PromptSnapshot{
		Messages:                []llm.Message{{Role: "user", Content: "shared request"}},
		AllowedSourceMessageIDs: []modulecore.MessageID{modulecore.NewMessageID()},
		SourceRevision:          "canonical-thread-revision", CutoverRevision: "thread-cutover-revision",
	}
}
