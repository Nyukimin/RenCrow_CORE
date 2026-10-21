package routing

import (
	"context"
	"reflect"
	"strings"
	"testing"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

func TestLLMClassifier_ClassifyWithChatReplyUsesOneStrictJSONGeneration(t *testing.T) {
	var captured llm.GenerateRequest
	generateCalls := 0
	provider := &mockLLMProvider{
		response: `{"route":"CHAT","reply":"Mio answer mentioning CODE4 as text"}`,
		onGenerate: func(req llm.GenerateRequest) {
			generateCalls++
			captured = req
		},
	}
	classifier := NewLLMClassifier(provider, "existing route policy and classifier prompt")
	chatMessages := []llm.Message{
		{Role: "system", Content: "Mio persona prompt", Type: llm.PromptContextCharacter},
		{Role: "system", Content: "RecallPack conversation context", Type: llm.PromptContextRecall},
		{Role: "user", Content: "こんにちは", Type: llm.PromptContextUser},
	}

	result, err := classifier.ClassifyWithChatReply(context.Background(), llm.GenerateRequest{
		Messages:    chatMessages,
		MaxTokens:   512,
		Temperature: 0.7,
		OnToken:     func(string) {},
	})
	if err != nil {
		t.Fatalf("ClassifyWithChatReply() error = %v", err)
	}
	if result.Decision.Route != routing.RouteCHAT {
		t.Fatalf("route = %s, want CHAT (route names in reply text must not affect parsing)", result.Decision.Route)
	}
	if !result.ReplyValid || result.Reply != "Mio answer mentioning CODE4 as text" {
		t.Fatalf("reply = %#v, want valid Mio answer", result)
	}
	if generateCalls != 1 {
		t.Fatalf("Generate calls = %d, want 1", generateCalls)
	}
	req := captured
	if req.ResponseFormat != llm.ResponseFormatJSONObject {
		t.Fatalf("ResponseFormat = %q, want JSON object mode", req.ResponseFormat)
	}
	if req.OnToken != nil {
		t.Fatal("combined JSON request must not expose provider tokens to stream hooks")
	}
	if req.SystemPrompt != "" {
		t.Fatalf("SystemPrompt = %q, want typed Messages to preserve prompt order", req.SystemPrompt)
	}
	if len(req.Messages) != len(chatMessages)+1 {
		t.Fatalf("messages = %#v, want Mio persona, typed classifier contract, RecallPack, and user in order", req.Messages)
	}
	if !reflect.DeepEqual(req.Messages[0], chatMessages[0]) {
		t.Fatalf("message[0] = %#v, want Character prompt %#v first", req.Messages[0], chatMessages[0])
	}
	contract := req.Messages[1]
	if contract.Role != "system" || contract.Type != llm.PromptContextStable || contract.Metadata["runtime_context_kind"] != "classifier_route_contract" ||
		!strings.Contains(contract.Content, "existing route policy and classifier prompt") || !strings.Contains(contract.Content, "Return exactly one JSON object") {
		t.Fatalf("typed classifier policy/JSON contract = %#v", contract)
	}
	for reqIndex, originalIndex := 2, 1; originalIndex < len(chatMessages); reqIndex, originalIndex = reqIndex+1, originalIndex+1 {
		if !reflect.DeepEqual(req.Messages[reqIndex], chatMessages[originalIndex]) {
			t.Fatalf("message[%d] = %#v, want original message[%d] %#v", reqIndex, req.Messages[reqIndex], originalIndex, chatMessages[originalIndex])
		}
	}
}

func TestLLMClassifier_ClassifyWithChatReplyRejectsNonCanonicalRouteField(t *testing.T) {
	tests := []struct {
		name     string
		response string
	}{
		{name: "route embedded in prose", response: `{"route":"please use CHAT","reply":"hello"}`},
		{name: "route only embedded in reply", response: `{"route":"","reply":"CHAT"}`},
		{name: "empty CHAT reply", response: `{"route":"CHAT","reply":"  "}`},
		{name: "non-CHAT reply is not empty", response: `{"route":"CODE2","reply":"do this"}`},
		{name: "null reply is not a string", response: `{"route":"CODE2","reply":null}`},
		{name: "null route is not a string", response: `{"route":null,"reply":"hello"}`},
		{name: "duplicate route field", response: `{"route":"CHAT","route":"CODE2","reply":"hello"}`},
		{name: "trailing JSON", response: `{"route":"CHAT","reply":"hello"} trailing`},
		{name: "unknown field", response: `{"route":"CHAT","reply":"hello","extra":"value"}`},
		{name: "oversized JSON reply", response: `{"route":"CHAT","reply":"` + strings.Repeat("x", maxRouteReplyJSONBytes) + `"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classifier := NewLLMClassifier(&mockLLMProvider{response: tt.response}, "existing route policy")
			if _, err := classifier.ClassifyWithChatReply(context.Background(), llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: "hello"}}}); err == nil {
				t.Fatal("ClassifyWithChatReply() error = nil, want strict JSON/route validation error")
			}
		})
	}
}

func TestLLMClassifier_ClassifyWithChatReplyPreservesValidNonChatDecisionWithoutReply(t *testing.T) {
	classifier := NewLLMClassifier(&mockLLMProvider{response: `{"route":"CODE2","reply":""}`}, "existing route policy")
	result, err := classifier.ClassifyWithChatReply(context.Background(), llm.GenerateRequest{})
	if err != nil {
		t.Fatalf("ClassifyWithChatReply() error = %v", err)
	}
	if result.Decision.Route != routing.RouteCODE2 || result.ReplyValid || result.Reply != "" {
		t.Fatalf("result = %#v, want CODE2 decision with no adoptable reply", result)
	}
}

var _ domainagent.RouteReplyClassifier = (*LLMClassifier)(nil)
