package backlog

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type revalidationLLMStub struct {
	request domainllm.GenerateRequest
	content string
	err     error
}

func (s *revalidationLLMStub) Name() string { return "revalidation-test" }
func (s *revalidationLLMStub) Generate(_ context.Context, request domainllm.GenerateRequest) (domainllm.GenerateResponse, error) {
	s.request = request
	return domainllm.GenerateResponse{Content: s.content}, s.err
}

func validRevalidationJSON() string {
	return `{"decision":"HOLD","reason":"依存仕様が未確定","necessity":"問題は残る","duplication":"部分重複あり","mergeability":"現時点では独立","architectural_consistency":"CORE owner境界と整合","technology_validity":"前提は有効","implementation_value":"依存完成後に価値あり","timing":"現在は早い","related_backlogs":["related"],"conflicting_specs":[],"merged_into":"","technology_changes":[],"architecture_impact":"新規層は不要","next_review_trigger":"依存仕様の確定"}`
}

func TestLLMRevalidationEvaluatorUsesBoundedJSONContract(t *testing.T) {
	provider := &revalidationLLMStub{content: validRevalidationJSON()}
	evaluator := NewLLMRevalidationEvaluator(provider, "shiro")
	result, err := evaluator.Evaluate(context.Background(), RevalidationEvaluationInput{
		Item:         domainbacklog.Item{ItemID: "subject", Title: "subject"},
		RelatedItems: []domainbacklog.Item{{ItemID: "related", Title: "related"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Proposal.Decision != domainbacklog.RevalidationDecisionHold || len(result.ReviewAgents) != 1 || result.ReviewAgents[0] != "shiro" {
		t.Fatalf("unexpected evaluation: %+v", result)
	}
	if provider.request.ResponseFormat != domainllm.ResponseFormatJSONObject || provider.request.Temperature != 0 || provider.request.MaxTokens > 2048 {
		t.Fatalf("unbounded request contract: %+v", provider.request)
	}
	if len(provider.request.Messages) != 1 || !strings.Contains(provider.request.Messages[0].Content, `"item_id":"subject"`) || !strings.Contains(provider.request.Messages[0].Content, `"item_id":"related"`) {
		t.Fatalf("evaluation input missing: %+v", provider.request.Messages)
	}
}

func TestLLMRevalidationEvaluatorMinimizesAndBoundsEvidenceBeforeModel(t *testing.T) {
	provider := &revalidationLLMStub{content: validRevalidationJSON()}
	long := strings.Repeat("長", revalidationTextRunes+500)
	related := make([]domainbacklog.Item, revalidationRelatedLimit+8)
	for i := range related {
		related[i] = domainbacklog.Item{
			ItemID: "related-" + strings.Repeat("x", i), Title: long,
			Body: "DO_NOT_SEND_BODY", Background: "DO_NOT_SEND_BACKGROUND",
			SourceRefs:          []domainbacklog.SourceRef{{Locator: long, RawOrSummary: "DO_NOT_SEND_SOURCE_BODY"}},
			RevalidationRecords: []domainbacklog.RevalidationRecord{{Reason: "DO_NOT_SEND_PRIOR_REVIEW"}},
		}
	}
	_, err := NewLLMRevalidationEvaluator(provider, "shiro").Evaluate(context.Background(), RevalidationEvaluationInput{
		Item: domainbacklog.Item{ItemID: "subject", Title: long, Body: "DO_NOT_SEND_BODY"}, RelatedItems: related,
	})
	if err != nil {
		t.Fatal(err)
	}
	content := provider.request.Messages[0].Content
	for _, forbidden := range []string{"DO_NOT_SEND_BODY", "DO_NOT_SEND_BACKGROUND", "DO_NOT_SEND_SOURCE_BODY", "DO_NOT_SEND_PRIOR_REVIEW", `"revalidation_records"`} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("model payload leaked non-semantic evidence %q", forbidden)
		}
	}
	if len(content) > revalidationPayloadBytes {
		t.Fatalf("model payload is not byte-bounded: %d", len(content))
	}
	var payload struct {
		Item         revalidationEvidence   `json:"item"`
		RelatedItems []revalidationEvidence `json:"related_items"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.RelatedItems) != revalidationRelatedLimit {
		t.Fatalf("related evidence count=%d want=%d", len(payload.RelatedItems), revalidationRelatedLimit)
	}
	if len([]rune(payload.Item.Title)) != revalidationTextRunes || len([]rune(payload.RelatedItems[0].Title)) != revalidationTextRunes {
		t.Fatalf("text evidence was not rune-bounded")
	}
}

func TestLLMRevalidationEvaluatorRejectsMissingDimensionAndTrailingJSON(t *testing.T) {
	for _, content := range []string{
		`{"decision":"DROP","reason":"x"}`,
		validRevalidationJSON() + ` {}`,
	} {
		provider := &revalidationLLMStub{content: content}
		if _, err := NewLLMRevalidationEvaluator(provider, "shiro").Evaluate(context.Background(), RevalidationEvaluationInput{}); err == nil {
			t.Fatalf("invalid proposal accepted: %s", content)
		}
	}
}

type revalidationEvaluatorStub struct {
	input RevalidationEvaluationInput
}

func (s *revalidationEvaluatorStub) Evaluate(_ context.Context, input RevalidationEvaluationInput) (RevalidationEvaluation, error) {
	s.input = input
	return RevalidationEvaluation{Proposal: RevalidationProposal{
		Decision: domainbacklog.RevalidationDecisionHold, Reason: "wait",
		Necessity: "needed", Duplication: "none", Mergeability: "none",
		ArchitecturalFit: "fits", TechnologyValidity: "valid",
		ImplementationValue: "valuable later", Timing: "early",
		RelatedBacklogs: []string{"related"}, NextReviewTrigger: "dependency complete",
	}, ReviewAgents: []string{"shiro"}}, nil
}

func TestEvaluateAndRevalidateSearchesRelatedAndPersistsSevenDimensions(t *testing.T) {
	now := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	store := &memoryItemStore{items: []domainbacklog.Item{
		{SchemaVersion: 2, ItemID: "subject", Title: "Shared routing", Purpose: "routing owner", Category: "runtime", TargetModules: []string{"RenCrow_CORE"}, ConceptState: domainbacklog.ConceptCandidate, DeliveryState: domainbacklog.DeliveryNone, MaturationState: domainbacklog.MaturationStateMaturation, MaturationStartedAt: now.Add(-8 * 24 * time.Hour).Format(time.RFC3339), MaturationEligibleAt: now.Add(-24 * time.Hour).Format(time.RFC3339), CreatedAt: now.Add(-8 * 24 * time.Hour).Format(time.RFC3339)},
		{SchemaVersion: 2, ItemID: "related", Title: "Shared routing contract", Purpose: "routing owner", Category: "runtime", TargetModules: []string{"RenCrow_CORE"}, ConceptState: domainbacklog.ConceptRejected, DeliveryState: domainbacklog.DeliveryRejected, MaturationState: domainbacklog.MaturationStateDropped, CreatedAt: now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)},
	}}
	evaluator := &revalidationEvaluatorStub{}
	service := NewService(store, nil).WithClock(func() time.Time { return now }).WithRevalidationEvaluator(evaluator)
	item, err := service.EvaluateAndRevalidate(context.Background(), "subject")
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluator.input.RelatedItems) != 1 || evaluator.input.RelatedItems[0].ItemID != "related" {
		t.Fatalf("related search omitted dropped history: %+v", evaluator.input.RelatedItems)
	}
	if item.MaturationState != domainbacklog.MaturationStateHold || len(item.RevalidationRecords) != 1 {
		t.Fatalf("evaluation was not persisted: %+v", item)
	}
	record := item.RevalidationRecords[0]
	if record.Necessity != "needed" || record.ArchitecturalConsistency != "fits" || record.Timing != "early" {
		t.Fatalf("seven dimensions not persisted: %+v", record)
	}
}

func TestRunEligibleRevalidationsIsBoundedAndSkipsHold(t *testing.T) {
	now := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	eligible := func(id string, age time.Duration) domainbacklog.Item {
		started := now.Add(-age)
		return domainbacklog.Item{SchemaVersion: 2, ItemID: id, Title: id, Purpose: "review", ConceptState: domainbacklog.ConceptCandidate, DeliveryState: domainbacklog.DeliveryNone, MaturationState: domainbacklog.MaturationStateMaturation, MaturationStartedAt: started.Format(time.RFC3339), MaturationEligibleAt: started.Add(maturationPeriod).Format(time.RFC3339), CreatedAt: started.Format(time.RFC3339)}
	}
	hold := eligible("hold", 20*24*time.Hour)
	hold.ConceptState = domainbacklog.ConceptDeferred
	hold.MaturationState = domainbacklog.MaturationStateHold
	hold.NextReviewTrigger = "dependency complete"
	store := &memoryItemStore{items: []domainbacklog.Item{eligible("oldest", 10*24*time.Hour), eligible("second", 9*24*time.Hour), eligible("young", 2*24*time.Hour), hold}}
	service := NewService(store, nil).WithClock(func() time.Time { return now }).WithRevalidationEvaluator(&revalidationEvaluatorStub{})
	report, err := service.RunEligibleRevalidations(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if report.Eligible != 2 || report.Attempted != 1 || report.Completed != 1 || len(report.ItemIDs) != 1 || report.ItemIDs[0] != "oldest" {
		t.Fatalf("unexpected bounded sweep: %+v", report)
	}
	if got, _ := service.Get(context.Background(), "hold"); got.MaturationState != domainbacklog.MaturationStateHold {
		t.Fatalf("automatic sweep changed event-driven HOLD: %+v", got)
	}
}
