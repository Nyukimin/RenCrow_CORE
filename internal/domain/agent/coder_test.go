package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/proposal"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
)

func TestNewCoderAgent(t *testing.T) {
	llmProvider := &mockLLMProvider{}
	toolRunner := &mockToolRunner{}
	mcpClient := &mockMCPClient{}

	coder := NewCoderAgent(llmProvider, toolRunner, mcpClient, "test prompt")

	if coder == nil {
		t.Fatal("NewCoderAgent should not return nil")
	}

	if coder.llmProvider != llmProvider {
		t.Error("llmProvider not set correctly")
	}
}

func TestCoderAgentBuilderOptionsAndGenerateWithContext(t *testing.T) {
	var gotReq llm.GenerateRequest
	llmProvider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			gotReq = req
			return llm.GenerateResponse{Content: "context response"}, nil
		},
	}
	memory := NewLightMemory(2)
	coder := NewCoderAgent(llmProvider, &mockToolRunner{}, &mockMCPClient{}, "base prompt").
		WithPersona(AgentPersona{Name: "Aka", Personality: "Be direct"}).
		WithLightMemory(memory)

	if coder.persona == nil || coder.persona.Name != "Aka" || coder.lightMemory != memory {
		t.Fatalf("builder options not set: %#v", coder)
	}
	resp, err := coder.GenerateWithContext(context.Background(), []llm.Message{{Role: "user", Content: "hello"}})
	if err != nil {
		t.Fatalf("GenerateWithContext failed: %v", err)
	}
	if resp != "context response" || len(gotReq.Messages) != 1 || gotReq.MaxTokens != 8192 || gotReq.Temperature != 0.5 {
		t.Fatalf("resp=%q req=%#v", resp, gotReq)
	}

	llmProvider.generateFunc = func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		return llm.GenerateResponse{}, errors.New("context failed")
	}
	if _, err := coder.GenerateWithContext(context.Background(), nil); err == nil {
		t.Fatal("expected GenerateWithContext error")
	}
}

func TestCoderAgentGenerateProposal(t *testing.T) {
	llmProvider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			// システムプロンプトにCoder指示が含まれているか確認
			if len(req.Messages) > 0 && req.Messages[0].Role == "system" {
				if req.Messages[0].Content == "" {
					t.Error("System prompt should not be empty")
				}
			}

			// Proposal形式のレスポンスを返す
			response := `## Plan
1. Create main.go file
2. Implement main function
3. Add error handling

## Patch
` + "```go:main.go\npackage main\n\nfunc main() {}\n```" + `

## Risk
Low risk - simple implementation

## CostHint
5 minutes`

			return llm.GenerateResponse{
				Content:      response,
				TokensUsed:   200,
				FinishReason: "stop",
			}, nil
		},
	}

	coder := NewCoderAgent(llmProvider, &mockToolRunner{}, &mockMCPClient{}, "test prompt")

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "main.goファイルを作成して", "line", "U123")

	proposal, err := coder.GenerateProposal(context.Background(), testTask)
	if err != nil {
		t.Fatalf("GenerateProposal failed: %v", err)
	}

	if proposal == nil {
		t.Fatal("Proposal should not be nil")
	}

	if proposal.Plan() == "" {
		t.Error("Plan should not be empty")
	}

	if proposal.Patch() == "" {
		t.Error("Patch should not be empty")
	}

	if proposal.Risk() == "" {
		t.Error("Risk should not be empty")
	}

	if proposal.CostHint() == "" {
		t.Error("CostHint should not be empty")
	}
}

func TestCoderAgentGenerateProposal_LLMError(t *testing.T) {
	llmProvider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{}, errors.New("API rate limit exceeded")
		},
	}

	coder := NewCoderAgent(llmProvider, &mockToolRunner{}, &mockMCPClient{}, "test prompt")

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "テスト", "line", "U123")

	_, err := coder.GenerateProposal(context.Background(), testTask)
	if err == nil {
		t.Error("Expected error when LLM fails")
	}

	if err.Error() != "API rate limit exceeded" {
		t.Errorf("Expected 'API rate limit exceeded', got '%s'", err.Error())
	}
}

func TestCoderAgentGenerateProposal_InvalidFormat(t *testing.T) {
	llmProvider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			// Plan/Patchセクションが欠けているレスポンス
			return llm.GenerateResponse{
				Content:      "This is not a valid proposal format",
				TokensUsed:   50,
				FinishReason: "stop",
			}, nil
		},
	}

	coder := NewCoderAgent(llmProvider, &mockToolRunner{}, &mockMCPClient{}, "test prompt")

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "テスト", "line", "U123")

	proposal, err := coder.GenerateProposal(context.Background(), testTask)
	if err == nil {
		t.Fatal("expected invalid format error")
	}

	if proposal != nil {
		t.Error("Proposal should be nil for invalid format")
	}

	kind, _, retryable, ok := ProposalFailureInfo(err)
	if !ok {
		t.Fatalf("expected classified proposal error, got %v", err)
	}
	if kind != ProposalFailureEmpty {
		t.Fatalf("expected %s, got %s", ProposalFailureEmpty, kind)
	}
	if !retryable {
		t.Fatal("expected invalid format to be retryable")
	}
}

func TestCoderAgentGenerateProposal_SelfCheckRejectsBarePip(t *testing.T) {
	llmProvider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{
				Content: `## Plan
Step 1

## Patch
[
  {
    "type": "shell_command",
    "action": "run",
    "target": "pip install foo"
  }
]

## Risk
Low

## CostHint
Low`,
			}, nil
		},
	}

	coder := NewCoderAgent(llmProvider, &mockToolRunner{}, &mockMCPClient{}, "test prompt")
	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "テスト", "line", "U123")

	_, err := coder.GenerateProposal(context.Background(), testTask)
	if err == nil {
		t.Fatal("expected self-check error")
	}
	if !strings.Contains(err.Error(), "bare pip") {
		t.Fatalf("unexpected error: %v", err)
	}
	kind, _, retryable, ok := ProposalFailureInfo(err)
	if !ok {
		t.Fatalf("expected classified proposal error, got %v", err)
	}
	if kind != ProposalFailureDisallowedCommand {
		t.Fatalf("expected %s, got %s", ProposalFailureDisallowedCommand, kind)
	}
	if retryable {
		t.Fatal("expected bare pip failure to be non-retryable")
	}
}

func TestCoderAgentExtractSection(t *testing.T) {
	coder := NewCoderAgent(&mockLLMProvider{}, &mockToolRunner{}, &mockMCPClient{}, "test prompt")

	content := `## Plan
This is the plan section

## Patch
This is the patch section

## Risk
This is the risk section
`

	plan := coder.extractSection(content, "## Plan", "##")
	if plan != "This is the plan section" {
		t.Errorf("Expected 'This is the plan section', got '%s'", plan)
	}

	patch := coder.extractSection(content, "## Patch", "##")
	if patch != "This is the patch section" {
		t.Errorf("Expected 'This is the patch section', got '%s'", patch)
	}

	risk := coder.extractSection(content, "## Risk", "##")
	if risk != "This is the risk section" {
		t.Errorf("Expected 'This is the risk section', got '%s'", risk)
	}
}

func TestCoderAgentExtractSection_NotFound(t *testing.T) {
	coder := NewCoderAgent(&mockLLMProvider{}, &mockToolRunner{}, &mockMCPClient{}, "test prompt")

	content := "No sections here"

	result := coder.extractSection(content, "## Plan", "##")
	if result != "" {
		t.Errorf("Expected empty string for non-existent section, got '%s'", result)
	}
}

func TestCoderAgentExtractSection_LastSection(t *testing.T) {
	coder := NewCoderAgent(&mockLLMProvider{}, &mockToolRunner{}, &mockMCPClient{}, "test prompt")

	// 最後のセクション（次のセクションマーカーがない）
	content := `## Plan
First section

## CostHint
This is the last section with no next marker`

	costHint := coder.extractSection(content, "## CostHint", "##")
	if costHint != "This is the last section with no next marker" {
		t.Errorf("Expected full last section, got '%s'", costHint)
	}
}

func TestCoderAgentExtractProposal_CompleteProposal(t *testing.T) {
	coder := NewCoderAgent(&mockLLMProvider{}, &mockToolRunner{}, &mockMCPClient{}, "test prompt")

	content := `## Plan
Step 1: Create file
Step 2: Test

## Patch
` + "```go:main.go\npackage main\n```" + `

## Risk
Low risk

## CostHint
10 minutes`

	proposal, err := coder.extractProposal(content)
	if err != nil {
		t.Fatalf("extractProposal failed: %v", err)
	}

	if proposal == nil {
		t.Fatal("Proposal should not be nil")
	}

	if !proposal.IsValid() {
		t.Error("Proposal should be valid")
	}

	if proposal.Plan() == "" || proposal.Patch() == "" {
		t.Error("Plan and Patch should not be empty")
	}
}

func TestCoderAgentExtractProposal_UnwrapsJSONFenceInPatch(t *testing.T) {
	coder := NewCoderAgent(&mockLLMProvider{}, &mockToolRunner{}, &mockMCPClient{}, "test prompt")

	content := `## Plan
Step 1

## Patch
` + "```json\n[\n  {\n    \"type\": \"shell_command\",\n    \"action\": \"run\",\n    \"target\": \"go test ./...\"\n  }\n]\n```" + `

## Risk
Low

## CostHint
Low`

	proposal, err := coder.extractProposal(content)
	if err != nil {
		t.Fatalf("extractProposal failed: %v", err)
	}
	if proposal == nil {
		t.Fatal("Proposal should not be nil")
	}
	if got := proposal.Patch(); got == "" || got[0] != '[' {
		t.Fatalf("expected raw json patch, got %q", got)
	}
}

func TestCoderAgentExtractProposal_UnwrapsMarkdownFenceInPatch(t *testing.T) {
	coder := NewCoderAgent(&mockLLMProvider{}, &mockToolRunner{}, &mockMCPClient{}, "test prompt")

	content := `## Plan
Step 1

## Patch
` + "```markdown\n```go:main.go\npackage main\n```\n```\n" + `

## Risk
Low

## CostHint
Low`

	proposal, err := coder.extractProposal(content)
	if err != nil {
		t.Fatalf("extractProposal failed: %v", err)
	}
	if proposal == nil {
		t.Fatal("Proposal should not be nil")
	}
	if got := proposal.Patch(); !strings.Contains(got, "```go:main.go") {
		t.Fatalf("expected markdown patch blocks, got %q", got)
	}
}

func TestCoderAgentExtractProposal_PatchOnlySynthesizesPlan(t *testing.T) {
	coder := NewCoderAgent(&mockLLMProvider{}, &mockToolRunner{}, &mockMCPClient{}, "test prompt")

	content := `## Patch
` + "```go:main.go\npackage main\n```"

	proposal, err := coder.extractProposal(content)
	if err != nil {
		t.Fatalf("expected patch-only proposal to be recovered, got %v", err)
	}
	if proposal == nil {
		t.Fatal("Proposal should not be nil")
	}
	if proposal.Plan() == "" {
		t.Fatal("expected synthesized plan")
	}
	if !strings.Contains(proposal.Plan(), "update file: main.go") {
		t.Fatalf("expected synthesized plan to name the patch target, got %q", proposal.Plan())
	}
	if !strings.Contains(proposal.Patch(), "```go:main.go") {
		t.Fatalf("unexpected patch: %q", proposal.Patch())
	}
}

func TestCoderAgentExtractProposal_PatchOnlyJSONSynthesizesSpecificPlan(t *testing.T) {
	coder := NewCoderAgent(&mockLLMProvider{}, &mockToolRunner{}, &mockMCPClient{}, "test prompt")

	content := `## Patch
[
  {"type":"file_edit","action":"update","target":"internal/app.go","content":"package main"},
  {"type":"shell_command","action":"run","target":"go test ./..."}
]`

	proposal, err := coder.extractProposal(content)
	if err != nil {
		t.Fatalf("expected patch-only proposal to be recovered, got %v", err)
	}
	if strings.Contains(proposal.Plan(), "Apply the requested code changes") {
		t.Fatalf("synthesized plan must not use generic audit text: %q", proposal.Plan())
	}
	for _, want := range []string{"update file: internal/app.go", "run verification command: go test ./..."} {
		if !strings.Contains(proposal.Plan(), want) {
			t.Fatalf("synthesized plan missing %q: %q", want, proposal.Plan())
		}
	}
}

func TestCoderAgentGenerateWithPrompt(t *testing.T) {
	var capturedPrompt string
	llmProvider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			if len(req.Messages) > 0 && req.Messages[0].Role == "system" {
				capturedPrompt = req.Messages[0].Content
			}
			return llm.GenerateResponse{
				Content:      "Generated code response",
				TokensUsed:   100,
				FinishReason: "stop",
			}, nil
		},
	}

	coder := NewCoderAgent(llmProvider, &mockToolRunner{}, &mockMCPClient{}, "test prompt")

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "main.goを作成して", "line", "U123")

	result, err := coder.GenerateWithPrompt(context.Background(), testTask, "You are a specification design assistant.")
	if err != nil {
		t.Fatalf("GenerateWithPrompt failed: %v", err)
	}

	if result != "Generated code response" {
		t.Errorf("Expected 'Generated code response', got '%s'", result)
	}

	if capturedPrompt != "You are a specification design assistant." {
		t.Errorf("Expected system prompt to be passed through, got '%s'", capturedPrompt)
	}
}

func TestCoderAgentGenerateWithPromptUsesLightMemory(t *testing.T) {
	var captured []llm.Message
	llmProvider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			captured = append([]llm.Message(nil), req.Messages...)
			return llm.GenerateResponse{Content: "second response"}, nil
		},
	}
	memory := NewLightMemory(3)
	memory.Record("U123", "first user", "first assistant")
	coder := NewCoderAgent(llmProvider, &mockToolRunner{}, &mockMCPClient{}, "test prompt").WithLightMemory(memory)

	_, err := coder.GenerateWithPrompt(context.Background(), task.NewTask(task.NewJobID(), "second user", "line", "U123"), "coder prompt")
	if err != nil {
		t.Fatalf("GenerateWithPrompt failed: %v", err)
	}

	if len(captured) != 4 {
		t.Fatalf("messages=%#v", captured)
	}
	if captured[1].Role != "user" || captured[1].Content != "first user" ||
		captured[2].Role != "assistant" || captured[2].Content != "first assistant" ||
		captured[3].Content != "second user" {
		t.Fatalf("LightMemory messages not injected in order: %#v", captured)
	}
	recent := memory.RecentMessages("U123")
	if len(recent) != 4 || recent[3].Content != "second response" {
		t.Fatalf("LightMemory did not record response: %#v", recent)
	}
}

func TestCoderAgentGenerateWithPrompt_Error(t *testing.T) {
	llmProvider := &mockLLMProvider{
		generateFunc: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
			return llm.GenerateResponse{}, errors.New("connection timeout")
		},
	}

	coder := NewCoderAgent(llmProvider, &mockToolRunner{}, &mockMCPClient{}, "test prompt")

	jobID := task.NewJobID()
	testTask := task.NewTask(jobID, "テスト", "line", "U123")

	_, err := coder.GenerateWithPrompt(context.Background(), testTask, "test prompt")
	if err == nil {
		t.Error("Expected error when LLM fails")
	}
}

func TestCoderAgentExtractProposal_MissingPatch(t *testing.T) {
	coder := NewCoderAgent(&mockLLMProvider{}, &mockToolRunner{}, &mockMCPClient{}, "test prompt")

	content := `## Plan
Step 1: Create file`

	proposal, err := coder.extractProposal(content)
	if err == nil {
		t.Fatal("expected missing patch error")
	}
	if proposal != nil {
		t.Error("Proposal should be nil when Patch is missing")
	}
	kind, _, retryable, ok := ProposalFailureInfo(err)
	if !ok {
		t.Fatalf("expected classified proposal error, got %v", err)
	}
	if kind != ProposalFailureMissingPatch {
		t.Fatalf("expected %s, got %s", ProposalFailureMissingPatch, kind)
	}
	if !retryable {
		t.Fatal("expected missing patch to be retryable")
	}
}

func TestProposalErrorAndFailureInfoBranches(t *testing.T) {
	if got := (&ProposalError{Reason: "  failed  "}).Error(); got != "failed" {
		t.Fatalf("error without kind should trim reason, got %q", got)
	}
	if got := (&ProposalError{Kind: "kind"}).Error(); got != "kind: proposal generation failed" {
		t.Fatalf("error without reason should use default reason, got %q", got)
	}

	kind, reason, retryable, ok := ProposalFailureInfo(errors.New("plain error"))
	if ok || kind != "" || reason != "" || retryable {
		t.Fatalf("plain error should not expose proposal metadata: %q %q %v %v", kind, reason, retryable, ok)
	}
}

func TestCoderAgentSelfCheckProposalFailures(t *testing.T) {
	coder := NewCoderAgent(&mockLLMProvider{}, &mockToolRunner{}, &mockMCPClient{}, "test prompt")

	if err := coder.selfCheckProposal(nil); err == nil || err.Error() != "proposal is nil" {
		t.Fatalf("expected nil proposal error, got %v", err)
	}

	emptyPatch := proposal.NewProposal("plan", "   ", "", "")
	err := coder.selfCheckProposal(emptyPatch)
	if err == nil {
		t.Fatal("expected missing patch error")
	}
	kind, _, retryable, ok := ProposalFailureInfo(err)
	if !ok || kind != ProposalFailureMissingPatch || !retryable {
		t.Fatalf("unexpected missing patch metadata: %q %v %v", kind, retryable, ok)
	}

	invalidPatch := proposal.NewProposal("plan", "not a runnable patch", "", "")
	err = coder.selfCheckProposal(invalidPatch)
	if err == nil {
		t.Fatal("expected invalid patch error")
	}
	kind, _, retryable, ok = ProposalFailureInfo(err)
	if !ok || kind != ProposalFailureInvalidPatch || !retryable {
		t.Fatalf("unexpected invalid patch metadata: %q %v %v", kind, retryable, ok)
	}

	barePip := proposal.NewProposal("plan", `[{"type":"shell_command","action":"run","target":"pip install demo"}]`, "", "")
	err = coder.selfCheckProposal(barePip)
	if err == nil {
		t.Fatal("expected bare pip error")
	}
	kind, _, retryable, ok = ProposalFailureInfo(err)
	if !ok || kind != ProposalFailureDisallowedCommand || retryable {
		t.Fatalf("unexpected bare pip metadata: %q %v %v", kind, retryable, ok)
	}
}

func TestCoderAgentExtractProposal_FlexibleHeadings(t *testing.T) {
	coder := NewCoderAgent(&mockLLMProvider{}, &mockToolRunner{}, &mockMCPClient{}, "test prompt")

	content := `### Implementation Plan
- Update the file

### Changes
` + "```go:main.go\npackage main\n\nfunc main() {}\n```" + `

### Risks
- Low`

	proposal, err := coder.extractProposal(content)
	if err != nil {
		t.Fatalf("extractProposal failed: %v", err)
	}
	if proposal == nil {
		t.Fatal("Proposal should not be nil")
	}
	if !strings.Contains(proposal.Plan(), "Update the file") {
		t.Fatalf("unexpected plan: %q", proposal.Plan())
	}
	if !strings.Contains(proposal.Patch(), "```go:main.go") {
		t.Fatalf("unexpected patch: %q", proposal.Patch())
	}
}

func TestCoderAgentExtractProposal_WholeContentPatchFallback(t *testing.T) {
	coder := NewCoderAgent(&mockLLMProvider{}, &mockToolRunner{}, &mockMCPClient{}, "test prompt")

	content := "```go:main.go\npackage main\n\nfunc main() {}\n```\n\n```bash\ngo test ./...\n```"

	proposal, err := coder.extractProposal(content)
	if err != nil {
		t.Fatalf("extractProposal failed: %v", err)
	}
	if proposal == nil {
		t.Fatal("Proposal should not be nil")
	}
	if proposal.Plan() == "" {
		t.Fatal("expected synthesized plan")
	}
	if !strings.Contains(proposal.Patch(), "```go:main.go") {
		t.Fatalf("unexpected patch: %q", proposal.Patch())
	}
}
