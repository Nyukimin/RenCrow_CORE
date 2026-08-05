package xbookmarkworkflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/imagegeneration"
	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainworkflow "github.com/Nyukimin/RenCrow_CORE/internal/domain/xbookmarkworkflow"
)

type workflowSourceStub struct{ record domainworkflow.SourceRecord }

func (s workflowSourceStub) XBookmarkWorkflowSource(_ context.Context, id string) (domainworkflow.SourceRecord, error) {
	if id != s.record.ID {
		return domainworkflow.SourceRecord{}, ErrSourceNotFound
	}
	return s.record, nil
}

type workflowResultStoreStub struct {
	values map[string]domainworkflow.Result
}

func (s *workflowResultStoreStub) Get(_ context.Context, id string) (domainworkflow.Result, bool, error) {
	value, ok := s.values[id]
	return value, ok, nil
}

func (s *workflowResultStoreStub) Save(_ context.Context, result domainworkflow.Result) error {
	if s.values == nil {
		s.values = map[string]domainworkflow.Result{}
	}
	s.values[result.ID] = result
	return nil
}

func (s *workflowResultStoreStub) List(_ context.Context, query domainworkflow.ResultQuery) ([]domainworkflow.Result, error) {
	result := make([]domainworkflow.Result, 0, len(s.values))
	for _, value := range s.values {
		if query.SourceRecordID != "" && value.SourceRecordID != query.SourceRecordID {
			continue
		}
		if query.Workflow != "" && value.Workflow != query.Workflow {
			continue
		}
		result = append(result, value)
	}
	return result, nil
}

type workflowLLMStub struct {
	content string
	calls   int
	last    domainllm.GenerateRequest
	lastCtx context.Context
}

func (s *workflowLLMStub) Generate(ctx context.Context, request domainllm.GenerateRequest) (domainllm.GenerateResponse, error) {
	s.calls++
	s.last = request
	s.lastCtx = ctx
	return domainllm.GenerateResponse{Content: s.content}, nil
}

func (s *workflowLLMStub) Name() string { return "worker" }

type workflowImageStub struct {
	result domainimage.GenerateResult
	err    error
	calls  int
	last   domainimage.GenerateRequest
}

func (s *workflowImageStub) Generate(_ context.Context, request domainimage.GenerateRequest) (domainimage.GenerateResult, error) {
	s.calls++
	s.last = request
	return s.result, s.err
}

type workflowBacklogStub struct {
	items []domainbacklog.Item
	err   error
}

func (s *workflowBacklogStub) Save(_ context.Context, item domainbacklog.Item) error {
	if s.err != nil {
		return s.err
	}
	s.items = append(s.items, item)
	return nil
}

func workflowTestSource() domainworkflow.SourceRecord {
	return domainworkflow.SourceRecord{
		ID: "kb:general:x:123:abc", Title: "画像prompt", SourceURL: "https://x.com/example/status/123",
		RawText: "## Post\nA quiet library, cinematic light\n\n## Related\nunrelated prompt word",
		Media:   []domainworkflow.Media{{Type: "image", URL: "https://pbs.twimg.com/media/example.jpg", Alt: "静かな図書館"}},
	}
}

func TestServiceImagePromptDrawRejectsFalsePositive(t *testing.T) {
	llm := &workflowLLMStub{content: `{"decision":"not_applicable","prompt":"","negative_prompt":"","prompt_source":"post","association":"unknown","reason":"生成promptではない"}`}
	image := &workflowImageStub{}
	store := &workflowResultStoreStub{values: map[string]domainworkflow.Result{}}
	service := NewService(workflowSourceStub{workflowTestSource()}, store, llm, image, nil)

	result, err := service.Run(context.Background(), domainworkflow.RunRequest{
		Workflow: domainworkflow.WorkflowImagePromptDraw, SourceRecordID: workflowTestSource().ID,
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	observation, ok := domainllm.ExecutionObservationFromContext(llm.lastCtx)
	if !ok || observation.Initiator != "shiro" || observation.Caller != "x_bookmark.workflow" || observation.Purpose != "image_prompt_evaluation" {
		t.Fatalf("unexpected LLM observation: %+v ok=%v", observation, ok)
	}
	if result.Status != domainworkflow.StatusSkipped || result.Decision != "not_applicable" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if image.calls != 0 {
		t.Fatalf("false positive must not be drawn: calls=%d", image.calls)
	}
}

func TestImagePromptExtractorSystemPromptDefinesCompleteJSONSchema(t *testing.T) {
	llm := &workflowLLMStub{content: `{"decision":"not_applicable","prompt":"","negative_prompt":"","prompt_source":"unknown","association":"unknown","reason":"生成promptではない"}`}
	service := NewService(
		workflowSourceStub{workflowTestSource()},
		&workflowResultStoreStub{values: map[string]domainworkflow.Result{}},
		llm,
		&workflowImageStub{},
		nil,
	)
	if _, err := service.Run(context.Background(), domainworkflow.RunRequest{
		Workflow: domainworkflow.WorkflowImagePromptDraw, SourceRecordID: workflowTestSource().ID,
	}); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`"decision"`, `ready | not_applicable | needs_review`,
		`"prompt"`, `"negative_prompt"`,
		`"prompt_source"`, `post | thread | note | quote | external | unknown`,
		`"association"`, `confirmed | unknown`, `"reason"`,
		`not_applicable`, `needs_review`, `prompt_source=unknown`, `association=unknown`,
	} {
		if !strings.Contains(llm.last.SystemPrompt, required) {
			t.Errorf("system prompt does not define %q", required)
		}
	}
	if llm.last.MaxTokens != 1024 {
		t.Fatalf("image prompt extractor max tokens = %d, want 1024", llm.last.MaxTokens)
	}
}

func TestServiceImagePromptDrawPersistsPromptBeforeGenerationFailureAndResumes(t *testing.T) {
	llm := &workflowLLMStub{content: `{"decision":"ready","prompt":"A quiet library, cinematic light","negative_prompt":"text","prompt_source":"post","association":"confirmed","reason":"明示prompt"}`}
	image := &workflowImageStub{err: errors.New("image unavailable")}
	store := &workflowResultStoreStub{values: map[string]domainworkflow.Result{}}
	service := NewService(workflowSourceStub{workflowTestSource()}, store, llm, image, nil)

	first, err := service.Run(context.Background(), domainworkflow.RunRequest{
		Workflow: domainworkflow.WorkflowImagePromptDraw, SourceRecordID: workflowTestSource().ID,
	})
	if err != nil {
		t.Fatalf("blocked image run must return its persisted result: %v", err)
	}
	if first.Status != domainworkflow.StatusBlocked || first.FailureStage != "image_generation" || first.Prompt == "" {
		t.Fatalf("prompt was not preserved: %+v", first)
	}
	image.err = nil
	image.result = domainimage.GenerateResult{OK: true, ID: "img_123", Profile: "forge_zimage_4060", Image: domainimage.ImageResult{ID: "img_123", ContentType: "image/png", Width: 1024, Height: 1024}}
	second, err := service.Run(context.Background(), domainworkflow.RunRequest{
		Workflow: domainworkflow.WorkflowImagePromptDraw, SourceRecordID: workflowTestSource().ID,
	})
	if err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if second.Status != domainworkflow.StatusCompleted || second.ImageID != "img_123" {
		t.Fatalf("unexpected completed result: %+v", second)
	}
	if llm.calls != 1 || image.calls != 2 || image.last.Prompt != first.Prompt {
		t.Fatalf("resume repeated extraction or changed prompt: llm=%d image=%d request=%+v", llm.calls, image.calls, image.last)
	}
	third, err := service.Run(context.Background(), domainworkflow.RunRequest{
		Workflow: domainworkflow.WorkflowImagePromptDraw, SourceRecordID: workflowTestSource().ID,
	})
	if err != nil || third.ImageID != "img_123" || image.calls != 2 {
		t.Fatalf("completed workflow was not idempotent: result=%+v err=%v calls=%d", third, err, image.calls)
	}
}

func TestServiceAITipAddsOnlyValuableProposalReviewToBacklog(t *testing.T) {
	llm := &workflowLLMStub{content: `{"verdict":"valuable","summary":"URL取得を再利用する","rationale":"既存のSource Registryと整合する","evidence_urls":["https://example.com/spec"],"improvement":"取得cacheを共通化する","affected_modules":["RenCrow_CORE"],"prerequisites":["既存取得台帳の確認"],"cost":"medium","risks":["回帰"],"validation_plan":["重複取得test"],"priority":"normal"}`}
	store := &workflowResultStoreStub{values: map[string]domainworkflow.Result{}}
	backlog := &workflowBacklogStub{}
	service := NewService(workflowSourceStub{workflowTestSource()}, store, llm, nil, backlog)

	result, err := service.Run(context.Background(), domainworkflow.RunRequest{
		Workflow: domainworkflow.WorkflowAITipRenCrowEvaluation, SourceRecordID: workflowTestSource().ID,
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result.Status != domainworkflow.StatusCompleted || result.BacklogItemID == "" || len(backlog.items) != 1 {
		t.Fatalf("valuable evaluation was not proposed: result=%+v backlog=%+v", result, backlog.items)
	}
	if backlog.items[0].Status != domainbacklog.StatusProposalReview {
		t.Fatalf("proposal must not be executable before user review: %+v", backlog.items[0])
	}
	if backlog.items[0].Source != "x-bookmark" {
		t.Fatalf("unexpected backlog source: %+v", backlog.items[0])
	}
}

func TestServiceAITipDoesNotAddRejectedVerdictToBacklog(t *testing.T) {
	llm := &workflowLLMStub{content: `{"verdict":"not_applicable","summary":"対象外","rationale":"RenCrowと無関係","evidence_urls":[],"improvement":"","affected_modules":[],"prerequisites":[],"cost":"low","risks":[],"validation_plan":[],"priority":"low"}`}
	store := &workflowResultStoreStub{values: map[string]domainworkflow.Result{}}
	backlog := &workflowBacklogStub{}
	service := NewService(workflowSourceStub{workflowTestSource()}, store, llm, nil, backlog)

	result, err := service.Run(context.Background(), domainworkflow.RunRequest{
		Workflow: domainworkflow.WorkflowAITipRenCrowEvaluation, SourceRecordID: workflowTestSource().ID,
	})
	if err != nil || result.Decision != "not_applicable" || len(backlog.items) != 0 {
		t.Fatalf("non-valuable tip changed backlog: result=%+v backlog=%+v err=%v", result, backlog.items, err)
	}
}

func TestSourcePayloadBoundsExternalContentWithoutChangingSource(t *testing.T) {
	source := workflowTestSource()
	longBody := strings.Repeat("外", maxReferenceRunes+200)
	source.References = make([]map[string]string, maxReferenceItems+2)
	for index := range source.References {
		source.References[index] = map[string]string{"kind": "external", "body_text": longBody}
	}
	source.Media = make([]domainworkflow.Media, maxMediaItems+2)
	for index := range source.Media {
		source.Media[index] = domainworkflow.Media{Type: "image", URL: "https://example.com/image.png"}
	}

	var payload struct {
		RawText    string                 `json:"raw_text"`
		Media      []domainworkflow.Media `json:"media"`
		References []map[string]string    `json:"references"`
	}
	if err := json.Unmarshal([]byte(sourcePayload(source)), &payload); err != nil {
		t.Fatalf("sourcePayload returned invalid JSON: %v", err)
	}
	if strings.Contains(payload.RawText, "unrelated prompt word") {
		t.Fatalf("Related section leaked into evaluation payload: %q", payload.RawText)
	}
	if len(payload.Media) != maxMediaItems || len(payload.References) != maxReferenceItems {
		t.Fatalf("payload bounds were not applied: media=%d references=%d", len(payload.Media), len(payload.References))
	}
	if got := len([]rune(payload.References[0]["body_text"])); got != maxReferenceRunes {
		t.Fatalf("reference body was not bounded: %d", got)
	}
	if got := len([]rune(source.References[0]["body_text"])); got != maxReferenceRunes+200 {
		t.Fatalf("source record was mutated: %d", got)
	}
}
