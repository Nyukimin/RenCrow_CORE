package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/imagegeneration"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
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

	resp, err := wild.Generate(context.Background(), newAgentTurnInput(t, "/wild 森の魔女の画像プロンプト", "line", "U123"))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if resp != "vivid prompt" {
		t.Fatalf("response should be trimmed, got %q", resp)
	}
	if captured.SystemPrompt != "" || len(captured.Messages) < 3 || captured.Messages[0].Content != "creative system" || captured.Messages[0].Type != llm.PromptContextCharacter || captured.Messages[len(captured.Messages)-2].Type != llm.PromptContextVariable {
		t.Fatalf("canonical prompt context not assembled: %#v", captured)
	}
	if captured.Messages[len(captured.Messages)-1].Role != "user" {
		t.Fatalf("expected final user message, got %#v", captured.Messages)
	}
	if strings.Contains(captured.Messages[0].Content, "/wild") {
		t.Fatalf("wild command should be stripped, got %q", captured.Messages[0].Content)
	}
	if captured.OnToken == nil {
		t.Fatal("Wild must always select the streaming provider path")
	}
}

func TestWildAgentGenerateForwardsStreamCallback(t *testing.T) {
	var streamed []string
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			req.OnToken("green")
			return llm.GenerateResponse{Content: "green"}, nil
		},
	}
	wild := NewWildAgent(provider, "creative system")
	ctx := llm.ContextWithStreamCallback(context.Background(), func(token string) {
		streamed = append(streamed, token)
	})

	if _, err := wild.Generate(ctx, newAgentTurnInput(t, "/wild color", "viewer", "viewer-user")); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if len(streamed) != 1 || streamed[0] != "green" {
		t.Fatalf("streamed tokens = %#v, want [green]", streamed)
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
	if got := empty.FormatForUser(); got != "RenCrow_Image generation completed." {
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

func TestWildAgentGenerateSharesMemoryAndFiltersExternalRecallByRole(t *testing.T) {
	var storedSpeaker conversation.Speaker
	engine := &mockConversationEngine{
		beginTurnFunc: func(ctx context.Context, sessionID, msg string) (*conversation.RecallPack, error) {
			return &conversation.RecallPack{
				Persona: conversation.PersonaState{Name: "Mio", SystemPrompt: "Mio recall persona must not override Wild"},
				MidSummaries: []conversation.ThreadSummary{
					{Summary: "wild mood board", Roles: []string{"wild"}},
					{Summary: "worker plan", Roles: []string{"worker"}},
				},
				SearchCacheSnippets: []conversation.SearchCacheSnippet{
					{Query: "worker report", Roles: []string{"worker"}},
				},
			}, nil
		},
		endTurnAsFunc: func(_ context.Context, _, _, _ string, speaker conversation.Speaker) error {
			storedSpeaker = speaker
			return nil
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

	if _, err := wild.Generate(context.Background(), newAgentTurnInput(t, "/wild 森の魔女", "line", "U123")); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	var prompt strings.Builder
	for _, msg := range captured.Messages {
		prompt.WriteString(msg.Content)
		prompt.WriteString("\n")
	}
	got := prompt.String()
	if !strings.Contains(got, "wild mood board") || !strings.Contains(got, "worker plan") {
		t.Fatalf("all conversation memory should be shared with Wild, got:\n%s", got)
	}
	if !strings.Contains(got, "Mio, Shiro, Kuro, and Midori") {
		t.Fatalf("shared conversation continuity contract is missing, got:\n%s", got)
	}
	if strings.Contains(got, "worker report") {
		t.Fatalf("worker external search context should be filtered for Wild, got:\n%s", got)
	}
	if strings.Contains(got, "Mio recall persona") {
		t.Fatalf("Mio recall persona leaked into Wild system context:\n%s", got)
	}
	if storedSpeaker != conversation.SpeakerMidori {
		t.Fatalf("stored speaker=%q, want midori", storedSpeaker)
	}
}

func TestWildAgentGenerateUsesImageGeneratorForImageGeneration(t *testing.T) {
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			t.Fatal("LLM provider should not be called for RenCrow_Image generation")
			return llm.GenerateResponse{}, nil
		},
	}
	imageTool := &mockWildImageGenerator{
		result: ImageGenerationResult{
			PromptID: "prompt-1",
			ImageURL: "http://image.local/output/out.png",
		},
	}
	wild := NewWildAgent(provider, "creative system").WithImageGenerator(imageTool)

	resp, err := wild.Generate(context.Background(), newAgentTurnInput(t, "/wild RenCrow_ImageでMioの画像生成をして", "viewer", "viewer-user"))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if !imageTool.called {
		t.Fatal("image generator should be called")
	}
	if !strings.Contains(imageTool.prompt, "Mio") {
		t.Fatalf("image generator prompt should preserve user request, got %q", imageTool.prompt)
	}
	if !strings.Contains(resp, "prompt-1") || !strings.Contains(resp, "http://image.local/output/out.png") {
		t.Fatalf("unexpected response: %q", resp)
	}
}

func TestIsImageGenerationRequestRecognizesNaturalJapaneseRequests(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    bool
	}{
		{
			name:    "background art",
			message: "青い海に立つ白い灯台、夏空、爽やかなアニメ背景画を生成して",
			want:    true,
		},
		{
			name:    "draw illustration",
			message: "白い灯台のイラストを描いて",
			want:    true,
		},
		{
			name:    "prompt only",
			message: "白い灯台の画像プロンプトを作って",
			want:    false,
		},
		{
			name:    "bare ComfyUI shorthand",
			message: "画像をComfyUIで",
			want:    true,
		},
		{
			name:    "bare ComfyUI shorthand with sentence punctuation",
			message: "画像をComfyUIで。",
			want:    true,
		},
		{
			name:    "bare ForgeNeo shorthand",
			message: "画像をForgeNeoで",
			want:    true,
		},
		{
			name:    "bare ImageGen shorthand",
			message: "画像をImageGenで",
			want:    true,
		},
		{
			name:    "engine comparison mention",
			message: "ForgeNeoとComfyUIの違いを説明して",
			want:    false,
		},
		{
			name:    "generic generation without visual context",
			message: "旅行計画を生成して",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isImageGenerationRequest(tt.message); got != tt.want {
				t.Fatalf("isImageGenerationRequest(%q) = %v, want %v", tt.message, got, tt.want)
			}
		})
	}
}

func TestWildAgentPassesExplicitImageEngineOnlyForGenerationRequests(t *testing.T) {
	tests := []struct {
		name       string
		message    string
		wantEngine domainimage.EngineConstraint
		wantError  bool
	}{
		{name: "ComfyUI", message: "ComfyUIで灯台の画像を生成して", wantEngine: domainimage.EngineConstraintComfyUI},
		{name: "engine-named generation", message: "ComfyUIの画像生成をして", wantEngine: domainimage.EngineConstraintComfyUI},
		{name: "ForgeNeo", message: "灯台の画像をForgeNeoで生成して", wantEngine: domainimage.EngineConstraintForgeNeo},
		{name: "ImageGen normal route is unavailable", message: "画像をImageGenで", wantEngine: domainimage.EngineConstraintImageGen, wantError: true},
		{name: "bare ComfyUI shorthand", message: "画像をComfyUIで", wantEngine: domainimage.EngineConstraintComfyUI},
		{name: "bare ComfyUI shorthand with punctuation", message: "画像をComfyUIで。", wantEngine: domainimage.EngineConstraintComfyUI},
		{name: "generation with URL follow-up", message: "ComfyUIで灯台の画像を生成して、URLを教えて", wantEngine: domainimage.EngineConstraintComfyUI},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
				t.Fatal("LLM provider should not handle an explicit image generation request")
				return llm.GenerateResponse{}, nil
			}}
			generator := &mockWildImageGenerator{result: ImageGenerationResult{ImageURL: "/image.png"}}
			wild := NewWildAgent(provider, "creative system").WithImageGenerator(generator)
			_, err := wild.Generate(context.Background(), newAgentTurnInput(t, tt.message, "viewer", "viewer-user"))
			if tt.wantError {
				if !errors.Is(err, ErrImageGeneratorUnavailable) {
					t.Fatalf("error = %v, want ErrImageGeneratorUnavailable", err)
				}
				if err == ErrImageGeneratorUnavailable {
					t.Fatal("unavailability error must include the requested engine reason")
				}
			} else if err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			if tt.wantError {
				if generator.engineCalls != 0 || generator.called {
					t.Fatalf("ImageGen calls: constrained=%d default=%v, want no adapter or fallback calls", generator.engineCalls, generator.called)
				}
			} else if generator.engineCalls != 1 || generator.engine != tt.wantEngine {
				t.Fatalf("engine calls=%d engine=%q, want 1 and %q", generator.engineCalls, generator.engine, tt.wantEngine)
			}
			if generator.called {
				t.Fatal("explicit engine request must not call the unconstrained generation method")
			}
		})
	}
}

func TestWildAgentKeepsPromptOnlyAndHowToRequestsOutOfImageExecution(t *testing.T) {
	for _, message := range []string{
		"ComfyUIで画像を生成する方法を教えて",
		"ComfyUIで画像生成プロンプトを考えて",
		"ComfyUIの画像生成について教えて",
		"ForgeNeoとComfyUIの違いを説明して",
	} {
		t.Run(message, func(t *testing.T) {
			provider := &mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
				return llm.GenerateResponse{Content: "text answer"}, nil
			}}
			generator := &mockWildImageGenerator{result: ImageGenerationResult{ImageURL: "/image.png"}}
			wild := NewWildAgent(provider, "creative system").WithImageGenerator(generator)
			if _, err := wild.Generate(context.Background(), newAgentTurnInput(t, message, "viewer", "viewer-user")); err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			if generator.called || generator.engineCalls != 0 {
				t.Fatalf("image generation calls: default=%v engine=%d, want none", generator.called, generator.engineCalls)
			}
		})
	}
}

func TestWildAgentKeepsExistingDefaultGenerationWithURLFollowUp(t *testing.T) {
	provider := &mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
		t.Fatal("LLM provider should not handle an image generation request")
		return llm.GenerateResponse{}, nil
	}}
	generator := &mockWildImageGenerator{result: ImageGenerationResult{ImageURL: "/image.png"}}
	wild := NewWildAgent(provider, "creative system").WithImageGenerator(generator)
	_, err := wild.Generate(context.Background(), newAgentTurnInput(t, "灯台の画像を生成して、URLを教えて", "viewer", "viewer-user"))
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !generator.called || generator.engineCalls != 0 {
		t.Fatalf("image calls: default=%v engine=%d, want default generation only", generator.called, generator.engineCalls)
	}
}

func TestWildAgentDoesNotFallbackForUnverifiableExplicitEngine(t *testing.T) {
	legacy := &legacyOnlyWildImageGenerator{}
	provider := &mockLLMProvider{generateFunc: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
		t.Fatal("LLM provider should not handle an explicit image generation request")
		return llm.GenerateResponse{}, nil
	}}
	wild := NewWildAgent(provider, "creative system").WithImageGenerator(legacy)
	_, err := wild.Generate(context.Background(), newAgentTurnInput(t, "ComfyUIで灯台の画像を生成して", "viewer", "viewer-user"))
	if !errors.Is(err, ErrImageGeneratorUnavailable) {
		t.Fatalf("Generate() error = %v, want ErrImageGeneratorUnavailable", err)
	}
	if err == ErrImageGeneratorUnavailable {
		t.Fatal("unavailability error must include the reason")
	}
	if legacy.calls != 0 {
		t.Fatalf("unconstrained GenerateImage calls = %d, want 0", legacy.calls)
	}
}

func TestWildAgentImageGenerationRequiresRenCrowImage(t *testing.T) {
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{}, errors.New("llm fallback called")
		},
	}
	wild := NewWildAgent(provider, "creative system")

	_, err := wild.Generate(context.Background(), newAgentTurnInput(t, "/wild Mioの画像を生成して", "viewer", "viewer-user"))
	if !errors.Is(err, ErrImageGeneratorUnavailable) {
		t.Fatalf("error = %v, want RenCrow_Image unavailable error", err)
	}
}

func TestWildAgentGenerateReturnsImageGeneratorError(t *testing.T) {
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			t.Fatal("LLM provider should not be called when image generator handles request")
			return llm.GenerateResponse{}, nil
		},
	}
	imageTool := &mockWildImageGenerator{err: errors.New("rencrow image unavailable")}
	wild := NewWildAgent(provider, "creative system").WithImageGenerator(imageTool)

	_, err := wild.Generate(context.Background(), newAgentTurnInput(t, "/wild RenCrow_Imageで画像生成して", "viewer", "viewer-user"))
	if err == nil {
		t.Fatal("expected image generator error")
	}
	if err.Error() != "rencrow image unavailable" {
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

	resp, err := wild.Generate(context.Background(), newAgentTurnInput(t, "/wild 森の魔女の画像プロンプトを作って", "viewer", "viewer-user"))
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

func TestWildAgentGenerateFallsBackToLLMForImageInterfaceDocumentQuestion(t *testing.T) {
	var called bool
	provider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			called = true
			return llm.GenerateResponse{Content: "spec summary"}, nil
		},
	}
	imageTool := &mockWildImageGenerator{}
	wild := NewWildAgent(provider, "creative system").WithImageGenerator(imageTool)

	resp, err := wild.Generate(context.Background(), newAgentTurnInput(t, "/wild RenCrow_Imageの仕様を説明して", "viewer", "viewer-user"))
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if !called {
		t.Fatal("LLM provider should be called for RenCrow_Image documentation question")
	}
	if imageTool.called {
		t.Fatal("image generator should not be called for RenCrow_Image documentation question")
	}
	if resp != "spec summary" {
		t.Fatalf("response = %q", resp)
	}
}

type mockWildImageGenerator struct {
	called      bool
	prompt      string
	engineCalls int
	engine      domainimage.EngineConstraint
	result      ImageGenerationResult
	err         error
}

type legacyOnlyWildImageGenerator struct {
	calls int
}

func (g *legacyOnlyWildImageGenerator) GenerateImage(context.Context, string) (ImageGenerationResult, error) {
	g.calls++
	return ImageGenerationResult{ImageURL: "/image.png"}, nil
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

func (m *mockWildImageGenerator) GenerateImageWithEngine(ctx context.Context, prompt string, engine domainimage.EngineConstraint) (ImageGenerationResult, error) {
	m.engineCalls++
	m.engine = engine
	m.prompt = prompt
	if engine == domainimage.EngineConstraintImageGen {
		return ImageGenerationResult{}, fmt.Errorf("%w: ImageGen is unavailable in the normal image route", ErrImageGeneratorUnavailable)
	}
	if m.err != nil {
		return ImageGenerationResult{}, m.err
	}
	if m.result.ImageURL == "" {
		return ImageGenerationResult{}, errors.New("missing result")
	}
	return m.result, nil
}
