package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
)

func TestWildAgentGenerateUsesWildPromptAndStripsCommand(t *testing.T) {
	var captured llm.GenerateRequest
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			captured = req
			return llm.GenerateResponse{Content: " vivid prompt "}, nil
		},
	}
	wild := NewWildAgent(provider, "creative system")

	resp, err := wild.Generate(context.Background(), task.NewTask(task.NewJobID(), "/wild 森の魔女の画像プロンプト", "line", "U123"))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if resp != "vivid prompt" {
		t.Fatalf("response should be trimmed, got %q", resp)
	}
	if captured.SystemPrompt != "creative system" {
		t.Fatalf("SystemPrompt: want custom prompt, got %q", captured.SystemPrompt)
	}
	if len(captured.Messages) != 1 || captured.Messages[0].Role != "user" {
		t.Fatalf("expected one user message, got %#v", captured.Messages)
	}
	if strings.Contains(captured.Messages[0].Content, "/wild") {
		t.Fatalf("wild command should be stripped, got %q", captured.Messages[0].Content)
	}
}

func TestImageGenerationResultFormatForUserVariants(t *testing.T) {
	withURLOnly := ImageGenerationResult{ImageURL: " https://example.test/out.png "}
	got := withURLOnly.FormatForUser()
	if !strings.Contains(got, "image_url: https://example.test/out.png") {
		t.Fatalf("URL-only result should include trimmed URL, got %q", got)
	}
	if strings.Contains(got, "prompt_id:") {
		t.Fatalf("URL-only result should not include prompt ID, got %q", got)
	}

	empty := ImageGenerationResult{PromptID: " prompt-without-url "}
	if got := empty.FormatForUser(); got != "ComfyUI image generation completed." {
		t.Fatalf("empty image URL should return completion message, got %q", got)
	}
}

func TestNewWildAgentUsesDefaultSystemPrompt(t *testing.T) {
	wild := NewWildAgent(&mockLLMProvider{}, " \t\n ")
	if wild.systemPrompt != defaultWildSystemPrompt {
		t.Fatalf("expected default prompt, got %q", wild.systemPrompt)
	}
}

func TestWildAgentBuilderOptionsAndCommandStrip(t *testing.T) {
	wild := NewWildAgent(&mockLLMProvider{}, "creative")
	engine := &mockConversationEngine{}
	generator := &mockWildImageGenerator{}

	if got := wild.WithConversationEngine(engine); got != wild {
		t.Fatal("WithConversationEngine should return the same agent")
	}
	if wild.conversationEngine != engine {
		t.Fatal("conversation engine was not set")
	}
	if got := wild.WithImageGenerator(generator); got != wild {
		t.Fatal("WithImageGenerator should return the same agent")
	}
	if wild.imageGenerator != generator {
		t.Fatal("image generator was not set")
	}

	if got := stripWildCommand("  /wild   draw a room  "); got != "draw a room" {
		t.Fatalf("unexpected stripped command: %q", got)
	}
	if got := stripWildCommand("  draw a room  "); got != "draw a room" {
		t.Fatalf("message without command should only be trimmed, got %q", got)
	}
}

func TestWildAgentGenerateAppliesWildRecallRoleFilter(t *testing.T) {
	engine := &mockConversationEngine{
		beginTurnFunc: func(ctx context.Context, sessionID, msg string) (*conversation.RecallPack, error) {
			return &conversation.RecallPack{
				MidSummaries: []conversation.ThreadSummary{
					{Summary: "wild mood board", Roles: []string{"wild"}},
					{Summary: "worker plan", Roles: []string{"worker"}},
				},
				SearchCacheSnippets: []conversation.SearchCacheSnippet{
					{Query: "worker report", Roles: []string{"worker"}},
				},
			}, nil
		},
	}
	var captured llm.GenerateRequest
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			captured = req
			return llm.GenerateResponse{Content: " vivid prompt "}, nil
		},
	}
	wild := NewWildAgent(provider, "creative system").WithConversationEngine(engine)

	if _, err := wild.Generate(context.Background(), task.NewTask(task.NewJobID(), "/wild 森の魔女", "line", "U123")); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	var prompt strings.Builder
	for _, msg := range captured.Messages {
		prompt.WriteString(msg.Content)
		prompt.WriteString("\n")
	}
	got := prompt.String()
	if !strings.Contains(got, "wild mood board") {
		t.Fatalf("wild recall should be included, got:\n%s", got)
	}
	if strings.Contains(got, "worker plan") || strings.Contains(got, "worker report") {
		t.Fatalf("worker recall should be filtered for wild, got:\n%s", got)
	}
}

func TestWildAgentGenerateUsesImageGeneratorForImageGeneration(t *testing.T) {
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			t.Fatal("LLM provider should not be called for ComfyUI image generation")
			return llm.GenerateResponse{}, nil
		},
	}
	imageTool := &mockWildImageGenerator{
		result: ImageGenerationResult{
			PromptID: "prompt-1",
			ImageURL: "http://comfy.local/view?filename=out.png&type=output",
		},
	}
	wild := NewWildAgent(provider, "creative system").WithImageGenerator(imageTool)

	resp, err := wild.Generate(context.Background(), task.NewTask(task.NewJobID(), "/wild ComfyUIでMioの画像生成をして", "viewer", "viewer-user"))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if !imageTool.called {
		t.Fatal("image generator should be called")
	}
	if !strings.Contains(imageTool.prompt, "Mio") {
		t.Fatalf("image generator prompt should preserve user request, got %q", imageTool.prompt)
	}
	if !strings.Contains(resp, "prompt-1") || !strings.Contains(resp, "http://comfy.local/view?filename=out.png&type=output") {
		t.Fatalf("unexpected response: %q", resp)
	}
}

func TestWildAgentGenerateReturnsImageGeneratorError(t *testing.T) {
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			t.Fatal("LLM provider should not be called when image generator handles request")
			return llm.GenerateResponse{}, nil
		},
	}
	imageTool := &mockWildImageGenerator{err: errors.New("comfyui unavailable")}
	wild := NewWildAgent(provider, "creative system").WithImageGenerator(imageTool)

	_, err := wild.Generate(context.Background(), task.NewTask(task.NewJobID(), "/wild ComfyUIで画像生成して", "viewer", "viewer-user"))
	if err == nil {
		t.Fatal("expected image generator error")
	}
	if err.Error() != "comfyui unavailable" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWildAgentGenerateFallsBackToLLMForImagePromptOnly(t *testing.T) {
	var called bool
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			called = true
			return llm.GenerateResponse{Content: "prompt text"}, nil
		},
	}
	imageTool := &mockWildImageGenerator{}
	wild := NewWildAgent(provider, "creative system").WithImageGenerator(imageTool)

	resp, err := wild.Generate(context.Background(), task.NewTask(task.NewJobID(), "/wild 森の魔女の画像プロンプトを作って", "viewer", "viewer-user"))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if !called {
		t.Fatal("LLM provider should be called for prompt generation")
	}
	if imageTool.called {
		t.Fatal("image generator should not be called for prompt-only request")
	}
	if resp != "prompt text" {
		t.Fatalf("response = %q", resp)
	}
}

func TestWildAgentGenerateFallsBackToLLMForComfyUIDocumentQuestion(t *testing.T) {
	var called bool
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			called = true
			return llm.GenerateResponse{Content: "spec summary"}, nil
		},
	}
	imageTool := &mockWildImageGenerator{}
	wild := NewWildAgent(provider, "creative system").WithImageGenerator(imageTool)

	resp, err := wild.Generate(context.Background(), task.NewTask(task.NewJobID(), "/wild ComfyUI仕様を説明して", "viewer", "viewer-user"))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if !called {
		t.Fatal("LLM provider should be called for ComfyUI documentation question")
	}
	if imageTool.called {
		t.Fatal("image generator should not be called for ComfyUI documentation question")
	}
	if resp != "spec summary" {
		t.Fatalf("response = %q", resp)
	}
}

type mockWildImageGenerator struct {
	called bool
	prompt string
	result ImageGenerationResult
	err    error
}

func (m *mockWildImageGenerator) GenerateImage(ctx context.Context, prompt string) (ImageGenerationResult, error) {
	m.called = true
	m.prompt = prompt
	if m.err != nil {
		return ImageGenerationResult{}, m.err
	}
	if m.result.ImageURL == "" {
		return ImageGenerationResult{}, errors.New("missing result")
	}
	return m.result, nil
}
