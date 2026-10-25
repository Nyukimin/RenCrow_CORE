package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

const coderProposalFixture = `## Plan
Step 1

## Patch
[]

## Risk
Low

## CostHint
Low`

func TestCoderGenerationPathsAssemblePersonaAndCommonContractOnce(t *testing.T) {
	var requests []llm.GenerateRequest
	provider := &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		requests = append(requests, req)
		return llm.GenerateResponse{Content: coderProposalFixture, FinishReason: "stop"}, nil
	}}
	coder := NewCoderAgent(provider, nil, nil, "proposal format").
		WithPersona(AgentPersona{Name: "Aka", Personality: "Aka identity marker"}).
		WithStableRuntimeContext("runtime capability marker")
	task := newAgentTurnInput(t, "assigned task", "line", "conversation-1")

	if _, err := coder.GenerateProposal(context.Background(), task); err != nil {
		t.Fatalf("GenerateProposal: %v", err)
	}
	if _, err := coder.GenerateWithPrompt(context.Background(), task, "route output format"); err != nil {
		t.Fatalf("GenerateWithPrompt: %v", err)
	}
	if len(requests) < 2 {
		t.Fatalf("captured %d requests, want 2", len(requests))
	}

	// Re-enter a copied request, as CoderLoop does on later turns. The CoderAgent
	// must own and replace only its persona/contract blocks.
	loopMessages := append([]llm.Message(nil), requests[1].Messages...)
	loopMessages = append(loopMessages, llm.Message{Role: "assistant", Content: "prior response"})
	loopMessages = append(loopMessages, llm.Message{
		Role:     "system",
		Content:  "unowned caller system",
		Metadata: map[string]string{"origin": "caller"},
	})
	before := cloneMessagesForContractTest(loopMessages)
	if _, err := coder.GenerateWithContext(context.Background(), loopMessages); err != nil {
		t.Fatalf("GenerateWithContext: %v", err)
	}
	if !reflect.DeepEqual(loopMessages, before) {
		t.Fatal("GenerateWithContext mutated caller messages or metadata")
	}

	for i, req := range requests {
		if countMessageContent(req.Messages, "Aka identity marker") != 1 {
			t.Errorf("request %d persona count = %d, want 1", i, countMessageContent(req.Messages, "Aka identity marker"))
		}
		if countMessageContaining(req.Messages, "CoderLoopでは指定されたCoderMessage JSON") != 1 {
			t.Errorf("request %d common contract count = %d, want 1", i, countMessageContaining(req.Messages, "CoderLoopでは指定されたCoderMessage JSON"))
		}
		if countMessageContent(req.Messages, "runtime capability marker") != 1 {
			t.Errorf("request %d runtime context count = %d, want 1", i, countMessageContent(req.Messages, "runtime capability marker"))
		}
	}

	last := requests[len(requests)-1].Messages
	if countMessageContent(last, "unowned caller system") != 1 {
		t.Fatalf("unowned system block was not preserved exactly once: %#v", last)
	}
	stableIndex, variableIndex := -1, -1
	for i, message := range last {
		if message.Content == "runtime capability marker" && message.Type == llm.PromptContextStable {
			stableIndex = i
		}
		if strings.HasPrefix(message.Content, llm.CurrentJSTTimePrefix) && message.Type == llm.PromptContextVariable {
			variableIndex = i
		}
	}
	if stableIndex < 0 || variableIndex < 0 || stableIndex >= variableIndex {
		t.Fatalf("stable runtime context must precede typed variable time context: %#v", last)
	}
}

func TestCoderAgentReplacesOtherCoderOwnedPersonaOnReentry(t *testing.T) {
	var requests []llm.GenerateRequest
	provider := &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		requests = append(requests, req)
		return llm.GenerateResponse{Content: "ok", FinishReason: "stop"}, nil
	}}
	turn := newAgentTurnInput(t, "assigned task", "line", "conversation-2")

	ao := NewCoderAgent(provider, nil, nil, "").WithPersona(AgentPersona{Name: "Ao", Personality: "Ao private identity marker"})
	if _, err := ao.GenerateWithPrompt(context.Background(), turn, "loop protocol"); err != nil {
		t.Fatalf("Ao GenerateWithPrompt: %v", err)
	}
	aka := NewCoderAgent(provider, nil, nil, "").WithPersona(AgentPersona{Name: "Aka", Personality: "Aka private identity marker"})
	if _, err := aka.GenerateWithContext(context.Background(), requests[0].Messages); err != nil {
		t.Fatalf("Aka GenerateWithContext: %v", err)
	}

	got := requests[1].Messages
	if countMessageContent(got, "Ao private identity marker") != 0 {
		t.Fatalf("another coder persona leaked into re-entered messages: %#v", got)
	}
	if countMessageContent(got, "Aka private identity marker") != 1 {
		t.Fatalf("current coder persona count = %d, want 1", countMessageContent(got, "Aka private identity marker"))
	}
	if countMessageContaining(got, "CoderLoopでは指定されたCoderMessage JSON") != 1 {
		t.Fatalf("common contract count = %d, want 1", countMessageContaining(got, "CoderLoopでは指定されたCoderMessage JSON"))
	}
}

func TestAllCoderSlotsReceiveTheSameWorkContract(t *testing.T) {
	var requests []llm.GenerateRequest
	provider := &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		requests = append(requests, req)
		return llm.GenerateResponse{Content: "ok", FinishReason: "stop"}, nil
	}}
	coders := []struct {
		name        string
		personality string
	}{
		{name: "Aka", personality: "Aka attention"},
		{name: "Ao", personality: "Ao attention"},
		{name: "Kin", personality: "Kin attention"},
		{name: "Gin", personality: "Gin attention"},
	}
	for _, tc := range coders {
		coder := NewCoderAgent(provider, nil, nil, "").WithPersona(AgentPersona{Name: tc.name, Personality: tc.personality})
		if _, err := coder.GenerateWithContext(context.Background(), []llm.Message{{Role: "user", Content: "assigned task"}}); err != nil {
			t.Fatalf("%s GenerateWithContext: %v", tc.name, err)
		}
	}
	var commonContract string
	for i, req := range requests {
		var contracts []llm.Message
		for _, message := range req.Messages {
			if message.Metadata[coderPromptOwnerMetadataKey] == coderPromptOwnerContract {
				contracts = append(contracts, message)
			}
		}
		if len(contracts) != 1 || contracts[0].Content != CommonCoderContract || contracts[0].Type != llm.PromptContextCharacter {
			t.Fatalf("slot %d contract blocks = %#v", i, contracts)
		}
		if i == 0 {
			commonContract = contracts[0].Content
		} else if contracts[0].Content != commonContract {
			t.Fatalf("slot %d has a different work contract", i)
		}
		for j, other := range coders {
			count := countMessageContent(req.Messages, other.personality)
			want := 0
			if i == j {
				want = 1
			}
			if count != want {
				t.Errorf("slot %d persona %s count = %d, want %d", i, other.name, count, want)
			}
		}
	}
}

func TestCoderAgentKeepsSharedTokenLimitAndLeavesTemperatureUnspecified(t *testing.T) {
	var requests []llm.GenerateRequest
	provider := &mockLLMProvider{generateFunc: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		requests = append(requests, req)
		return llm.GenerateResponse{Content: coderProposalFixture, FinishReason: "stop"}, nil
	}}
	coder := NewCoderAgent(provider, nil, nil, "proposal format")
	turn := newAgentTurnInput(t, "assigned task", "line", "conversation-3")
	if _, err := coder.GenerateProposal(context.Background(), turn); err != nil {
		t.Fatalf("GenerateProposal: %v", err)
	}
	if _, err := coder.GenerateWithPrompt(context.Background(), turn, "prompt"); err != nil {
		t.Fatalf("GenerateWithPrompt: %v", err)
	}
	if _, err := coder.GenerateWithContext(context.Background(), []llm.Message{{Role: "user", Content: "task"}}); err != nil {
		t.Fatalf("GenerateWithContext: %v", err)
	}
	for i, req := range requests {
		if req.MaxTokens != 8192 || req.Temperature != 0 {
			t.Errorf("request %d generation options: max_tokens=%d temperature=%v, want 8192 and unspecified temperature", i, req.MaxTokens, req.Temperature)
		}
	}
}

func TestCoderAgentRejectsIncompleteAndNativeToolCallResponsesOnEveryPath(t *testing.T) {
	cases := []struct {
		name         string
		finishReason string
		hasToolCalls bool
	}{
		{name: "length", finishReason: "length"},
		{name: "error", finishReason: "error"},
		{name: "tool_calls finish", finishReason: "tool_calls", hasToolCalls: true},
		{name: "tool_calls with stop", finishReason: "stop", hasToolCalls: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &mockLLMProvider{generateFunc: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
				return llm.GenerateResponse{
					Content:      coderProposalFixture,
					FinishReason: tc.finishReason,
					HasToolCalls: tc.hasToolCalls,
				}, nil
			}}
			coder := NewCoderAgent(provider, nil, nil, "proposal format")
			turn := newAgentTurnInput(t, "assigned task", "line", "conversation-response")

			proposal, err := coder.GenerateProposal(context.Background(), turn)
			if err == nil || proposal != nil {
				t.Fatalf("GenerateProposal returned proposal=%#v err=%v; want rejection", proposal, err)
			}
			kind, _, _, ok := ProposalFailureInfo(err)
			if !ok || kind != ProposalFailureIncompleteResponse {
				t.Fatalf("GenerateProposal failure = %q, classified=%v; want %q", kind, ok, ProposalFailureIncompleteResponse)
			}

			if output, err := coder.GenerateWithPrompt(context.Background(), turn, "output contract"); err == nil || output != "" {
				t.Fatalf("GenerateWithPrompt returned output=%q err=%v; want rejection", output, err)
			}
			if output, err := coder.GenerateWithContext(context.Background(), []llm.Message{{Role: "user", Content: "task"}}); err == nil || output != "" {
				t.Fatalf("GenerateWithContext returned output=%q err=%v; want rejection", output, err)
			}
		})
	}
}

func TestCoderAgentAcceptsMissingFinishReasonForLegacyProviders(t *testing.T) {
	provider := &mockLLMProvider{generateFunc: func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
		return llm.GenerateResponse{Content: coderProposalFixture}, nil
	}}
	coder := NewCoderAgent(provider, nil, nil, "proposal format")
	turn := newAgentTurnInput(t, "assigned task", "line", "conversation-legacy-finish")
	if _, err := coder.GenerateProposal(context.Background(), turn); err != nil {
		t.Fatalf("GenerateProposal should retain compatibility with providers omitting finish_reason: %v", err)
	}
	if output, err := coder.GenerateWithPrompt(context.Background(), turn, "output contract"); err != nil || output != coderProposalFixture {
		t.Fatalf("GenerateWithPrompt output=%q err=%v", output, err)
	}
	if output, err := coder.GenerateWithContext(context.Background(), []llm.Message{{Role: "user", Content: "task"}}); err != nil || output != coderProposalFixture {
		t.Fatalf("GenerateWithContext output=%q err=%v", output, err)
	}
}

func countMessageContent(messages []llm.Message, content string) int {
	count := 0
	for _, message := range messages {
		if message.Content == content {
			count++
		}
	}
	return count
}

func countMessageContaining(messages []llm.Message, substring string) int {
	count := 0
	for _, message := range messages {
		if strings.Contains(message.Content, substring) {
			count++
		}
	}
	return count
}

func indexOfMessageContent(messages []llm.Message, content string) int {
	for i, message := range messages {
		if message.Content == content {
			return i
		}
	}
	return -1
}

func indexOfMessageContentWithType(messages []llm.Message, content string, contextType llm.PromptContextType) int {
	for i, message := range messages {
		if message.Content == content && message.Type == contextType {
			return i
		}
	}
	return -1
}

func hasVariableJSTTime(messages []llm.Message) bool {
	for _, message := range messages {
		if message.Type == llm.PromptContextVariable && strings.HasPrefix(message.Content, llm.CurrentJSTTimePrefix) {
			return true
		}
	}
	return false
}

func cloneMessagesForContractTest(messages []llm.Message) []llm.Message {
	clone := make([]llm.Message, len(messages))
	for i, message := range messages {
		clone[i] = message
		if message.Metadata != nil {
			clone[i].Metadata = make(map[string]string, len(message.Metadata))
			for key, value := range message.Metadata {
				clone[i].Metadata[key] = value
			}
		}
		clone[i].Parts = append([]llm.MessagePart(nil), message.Parts...)
	}
	return clone
}
