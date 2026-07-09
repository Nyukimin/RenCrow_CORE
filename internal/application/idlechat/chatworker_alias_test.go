package idlechat

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
)

type idlechatAliasTestProvider struct {
	name string
}

func (p idlechatAliasTestProvider) Generate(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
	return llm.GenerateResponse{Content: "ok"}, nil
}

func (p idlechatAliasTestProvider) Name() string {
	return p.name
}

type idlechatTopicProvider struct {
	name      string
	requests  []llm.GenerateRequest
	responses []string
}

func (p *idlechatTopicProvider) Generate(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.requests = append(p.requests, req)
	if len(p.responses) == 0 {
		return llm.GenerateResponse{Content: "ok"}, nil
	}
	response := p.responses[0]
	p.responses = p.responses[1:]
	return llm.GenerateResponse{Content: response}, nil
}

func (p *idlechatTopicProvider) Name() string {
	return p.name
}

func TestProviderForSpeakerPrefersChatWorkerAlias(t *testing.T) {
	orch := NewIdleChatOrchestrator(idlechatAliasTestProvider{name: "mio"}, &session.CentralMemory{}, nil, 1, 1, 0.5, nil, "")
	orch.SetSpeakerProviders(map[string]llm.LLMProvider{
		"shiro":      idlechatAliasTestProvider{name: "Worker"},
		"chatworker": idlechatAliasTestProvider{name: "ChatWorker"},
	})

	provider := orch.providerForSpeaker("shiro")
	if provider == nil {
		t.Fatal("expected provider")
	}
	if got := provider.Name(); got != "ChatWorker" {
		t.Fatalf("providerForSpeaker(shiro) = %q, want ChatWorker", got)
	}
}

func TestChatWorkerDefaultsToNonThinking(t *testing.T) {
	orch := NewIdleChatOrchestrator(idlechatAliasTestProvider{name: "mio"}, &session.CentralMemory{}, nil, 1, 1, 0.5, nil, "")
	if got := orch.speakerThinkEnabled("chatworker"); got {
		t.Fatalf("speakerThinkEnabled(chatworker) = %t, want false", got)
	}
}

func TestGenerateTopicFromChatUsesChatWorkerProvider(t *testing.T) {
	chatProvider := &idlechatTopicProvider{name: "Chat", responses: []string{
		topicCandidatesJSON("Chatが使われた場合のお題", "観察"),
		topicJudgeJSON("Chatが使われた場合のお題"),
	}}
	workerProvider := &idlechatTopicProvider{name: "ChatWorker", responses: []string{
		topicCandidatesJSON("郵便と古書店に残る、宛先不明の手紙の扱い方", "観察"),
		topicJudgeJSON("郵便と古書店に残る、宛先不明の手紙の扱い方"),
	}}
	orch := NewIdleChatOrchestrator(chatProvider, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 10, 0.7, nil, "")
	orch.SetSpeakerProviders(map[string]llm.LLMProvider{
		"mio":        chatProvider,
		"chatworker": workerProvider,
	})

	topic, strategy := orch.generateTopicFromChat("idle-topic-worker", StrategySingleGenre)
	if strategy != StrategySingleGenre {
		t.Fatalf("strategy = %q, want %q", strategy, StrategySingleGenre)
	}
	if topic != "郵便と古書店に残る、宛先不明の手紙の扱い方" {
		t.Fatalf("topic = %q", topic)
	}
	if got := countTopicGenerationRequests(chatProvider.requests); got != 0 {
		t.Fatalf("chat topic generation requests = %d, want 0", got)
	}
	if got := countTopicGenerationRequests(workerProvider.requests); got != 1 {
		t.Fatalf("worker topic generation requests = %d, want 1", got)
	}
	if orch.currentTopicResult == nil || orch.currentTopicResult.Provider != "chatworker" {
		t.Fatalf("topic provider = %#v, want chatworker", orch.currentTopicResult)
	}
}

func TestShiroDialogueUsesChatWorkerMaxTokens(t *testing.T) {
	if got := idleMaxTokensForSpeaker("shiro", idleChatResponseMaxTokens); got != idleChatShiroResponseMaxTokens {
		t.Fatalf("idleMaxTokensForSpeaker(shiro, response) = %d, want %d", got, idleChatShiroResponseMaxTokens)
	}
	if got := idleMaxTokensForSpeaker("shiro", idleChatRetryMaxTokens); got != idleChatShiroRetryMaxTokens {
		t.Fatalf("idleMaxTokensForSpeaker(shiro, retry) = %d, want %d", got, idleChatShiroRetryMaxTokens)
	}
}
