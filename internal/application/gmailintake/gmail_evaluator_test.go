package gmailintake

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

type gmailEvaluatorLLMStub struct {
	responses []domainllm.GenerateResponse
	errors    []error
	requests  []domainllm.GenerateRequest
}

func (s *gmailEvaluatorLLMStub) Name() string { return "gmail-evaluator-test" }

func (s *gmailEvaluatorLLMStub) Generate(_ context.Context, request domainllm.GenerateRequest) (domainllm.GenerateResponse, error) {
	s.requests = append(s.requests, request)
	index := len(s.requests) - 1
	if index >= len(s.responses) {
		return domainllm.GenerateResponse{}, errors.New("unexpected Generate call")
	}
	if index < len(s.errors) && s.errors[index] != nil {
		return domainllm.GenerateResponse{}, s.errors[index]
	}
	return s.responses[index], nil
}

type gmailEvaluatorFetcherStub struct {
	responses map[string]modulewebgather.FetchResponse
	err       error
	requests  []modulewebgather.FetchRequest
}

func (s *gmailEvaluatorFetcherStub) FetchURL(_ context.Context, request modulewebgather.FetchRequest) (modulewebgather.FetchResponse, error) {
	s.requests = append(s.requests, request)
	if s.err != nil {
		return modulewebgather.FetchResponse{}, s.err
	}
	return s.responses[request.URL], nil
}

func gmailEvaluatorContext(t *testing.T) context.Context {
	t.Helper()
	ctx, err := domainexecution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewTraceID())
	if err != nil {
		t.Fatal(err)
	}
	scope, err := domaintool.NewToolExecutionScope(string(modulecore.NewRequestID()), domaintool.ActorKindAgent, "shiro", "user-1", []string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	return domaintool.WithToolExecutionScope(ctx, scope)
}

func gmailEvaluatorMessage(body string) GmailMessage {
	return GmailMessage{Subject: "AI・政治デイリーブリーフ", Text: body}
}

func gmailEvaluatorExtraction(kind, rawURL string) string {
	return fmt.Sprintf(`{"coverage":{"mode":"unstructured","expected_sections":0,"covered_topics":1},"topics":[{"title":"AI source","claim":"The source describes a RenCrow-relevant AI change.","kind":%q,"urls":[%q]}]}`, kind, rawURL)
}

func gmailEvaluatorAssessment(decision, quote string) string {
	return gmailEvaluatorAssessmentAtURL(decision, "https://example.com/ai", quote)
}

func gmailEvaluatorAssessmentAtURL(decision, quoteURL, quote string) string {
	return fmt.Sprintf(`{"decision":%q,"verification_status":"verified","reason":"The verified source has concrete RenCrow value.","relevance":"It maps to the existing CORE and LLM route.","verification":"The primary source states the quoted claim; primary designation is based on the source's direct technical publication.","source_quotes":[{"url":%q,"quote":%q,"primary":true}],"title":"Verified AI route improvement","purpose":"Improve the RenCrow AI route using the verified source.","specification":"Define the bounded route behavior and preserve the CORE owner boundary.","acceptance_criteria":["The route behavior is covered by a deterministic acceptance check."],"affected_modules":["CORE","LLM"]}`, decision, quoteURL, quote)
}

func gmailEvaluatorFetcher(url, text string) *gmailEvaluatorFetcherStub {
	return &gmailEvaluatorFetcherStub{responses: map[string]modulewebgather.FetchResponse{url: {
		URL: url, FinalURL: url, Status: "ok", HTTPStatus: 200, ExtractedText: text,
		RawHash: modulewebgather.SHA256Text(text), RawBytes: int64(len([]byte(text))),
	}}}
}

func TestGmailBriefEvaluatorValidFlowKeepsFetchedEvidenceAndDoesNotWriteState(t *testing.T) {
	const sourceURL = "https://example.com/ai"
	const quote = "Official AI source says the route is useful for RenCrow."
	provider := &gmailEvaluatorLLMStub{responses: []domainllm.GenerateResponse{
		{Content: gmailEvaluatorExtraction("ai", sourceURL)},
		{Content: gmailEvaluatorAssessment("include", quote)},
	}}
	const sourceText = " Intro. " + quote + " More details. \n"
	fetcher := gmailEvaluatorFetcher(sourceURL, sourceText)
	evaluator := NewGmailBriefEvaluator(provider, fetcher)
	result, err := evaluator.EvaluateBrief(gmailEvaluatorContext(t), gmailEvaluatorMessage("Read this source: "+sourceURL+"."), []string{"title: CORE LLM route\npurpose: preserve the canonical owner"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != gmailBriefStatusVerified || len(result.Proposals) != 1 {
		t.Fatalf("result = %+v", result)
	}
	proposal := result.Proposals[0]
	if proposal.Title == "" || proposal.Purpose == "" || len(proposal.AcceptanceCriteria) != 1 || len(proposal.SourceRefs) != 1 {
		t.Fatalf("incomplete proposal = %+v", proposal)
	}
	for _, section := range []string{"検証:", "関連性:", "提案・仕様:", "受入条件:", "Evidence:", sourceURL, quote, "primary=true"} {
		if !strings.Contains(proposal.Body, section) {
			t.Fatalf("proposal body omitted %q: %s", section, proposal.Body)
		}
	}
	ref := proposal.SourceRefs[0]
	if ref.Type != "web" || ref.Strength != "primary" || ref.Locator != sourceURL || ref.ContentHash != modulewebgather.SHA256Text(sourceText) || ref.CapturedAt == "" || !strings.Contains(ref.RawOrSummary, quote) {
		t.Fatalf("unexpected source ref = %+v", ref)
	}
	if proposal.TargetModules[0] != "RenCrow_CORE" || proposal.TargetModules[1] != "RenCrow_LLM" || proposal.ConsumerModules[0] != "RenCrow_CORE" || proposal.AffectedModules[1] != "RenCrow_LLM" {
		t.Fatalf("non-canonical proposal modules = %+v", proposal)
	}
	if len(provider.requests) != 2 || provider.requests[0].MaxTokens != 4096 || provider.requests[1].Temperature != 0 || provider.requests[1].ResponseFormat != domainllm.ResponseFormatJSONObject {
		t.Fatalf("unexpected LLM contract: %+v", provider.requests)
	}
	if len(fetcher.requests) != 1 {
		t.Fatalf("fetch calls = %d", len(fetcher.requests))
	}
	fetchRequest := fetcher.requests[0]
	if fetchRequest.URL != sourceURL || fetchRequest.FetchProvider != modulewebgather.DefaultFetchProvider || fetchRequest.StoreStaging || !fetchRequest.StoreStagingSet || !fetchRequest.DryRun || fetchRequest.Policy.RequestTimeout.String() != "15s" || fetchRequest.Policy.MaxBodyBytes != 1<<20 || fetchRequest.Policy.MaxRedirects != 3 || fetchRequest.Policy.AllowLocalhost {
		t.Fatalf("unsafe fetch contract: %+v", fetchRequest)
	}
}

func TestGmailBriefEvaluatorRejectsURLNotLiteralInEmailBeforeFetch(t *testing.T) {
	provider := &gmailEvaluatorLLMStub{responses: []domainllm.GenerateResponse{{Content: gmailEvaluatorExtraction("ai", "https://evil.example/injected")}}}
	fetcher := gmailEvaluatorFetcher("https://example.com/ai", "safe source")
	result, err := NewGmailBriefEvaluator(provider, fetcher).EvaluateBrief(gmailEvaluatorContext(t), gmailEvaluatorMessage("Only this URL is present: https://example.com/ai."), []string{"CORE route"})
	if err == nil || result.Status != gmailBriefStatusBlocked || len(result.Proposals) != 0 || len(fetcher.requests) != 0 || len(provider.requests) != 1 {
		t.Fatalf("injected URL was accepted: result=%+v err=%v fetches=%d generations=%d", result, err, len(fetcher.requests), len(provider.requests))
	}
}

func TestGmailBriefEvaluatorCitationNormalizesFetchedURL(t *testing.T) {
	const emailURL = "https://EXAMPLE.com/ai#fragment"
	const fetchedURL = "https://example.com/ai"
	const quote = "The normalized source confirms the bounded route."
	provider := &gmailEvaluatorLLMStub{responses: []domainllm.GenerateResponse{
		{Content: gmailEvaluatorExtraction("ai", emailURL)},
		{Content: gmailEvaluatorAssessment("include", quote)},
	}}
	fetcher := gmailEvaluatorFetcher(fetchedURL, "Evidence: "+quote)
	result, err := NewGmailBriefEvaluator(provider, fetcher).EvaluateBrief(gmailEvaluatorContext(t), gmailEvaluatorMessage("Source: "+emailURL), []string{"CORE route"})
	if err != nil || result.Status != gmailBriefStatusVerified || len(result.Proposals) != 1 || len(fetcher.requests) != 1 || fetcher.requests[0].URL != fetchedURL {
		t.Fatalf("normalized citation URL was rejected: result=%+v err=%v requests=%+v", result, err, fetcher.requests)
	}
}

func TestGmailBriefEvaluatorPoliticsBecomesPrivateKnowledge(t *testing.T) {
	const sourceURL = "https://example.com/politics"
	const quote = "The publisher reports the public policy change."
	provider := &gmailEvaluatorLLMStub{responses: []domainllm.GenerateResponse{
		{Content: gmailEvaluatorExtraction("politics", sourceURL)},
		{Content: gmailEvaluatorAssessmentAtURL("skip", sourceURL, quote)},
	}}
	fetcher := gmailEvaluatorFetcher(sourceURL, quote)
	result, err := NewGmailBriefEvaluator(provider, fetcher).EvaluateBrief(gmailEvaluatorContext(t), gmailEvaluatorMessage("Politics source: "+sourceURL), []string{"CORE route"})
	if err != nil || result.Status != gmailBriefStatusVerified || len(result.Proposals) != 0 || len(result.KnowledgeProposals) != 1 || len(provider.requests) != 2 || len(fetcher.requests) != 1 {
		t.Fatalf("politics was not routed to Knowledge: result=%+v err=%v generations=%d fetches=%d", result, err, len(provider.requests), len(fetcher.requests))
	}
	if result.TopicOutcomes[0].Status != "reviewed" || result.KnowledgeProposals[0].VerificationStatus != "verified" {
		t.Fatalf("politics verification=%+v proposal=%+v", result.TopicOutcomes, result.KnowledgeProposals)
	}
}

func TestGmailBriefEvaluatorRetainsEvidenceFailureAndIsolatesMalformedAssessment(t *testing.T) {
	t.Run("fetch failure", func(t *testing.T) {
		provider := &gmailEvaluatorLLMStub{responses: []domainllm.GenerateResponse{{Content: gmailEvaluatorExtraction("ai", "https://example.com/ai")}}}
		fetcher := &gmailEvaluatorFetcherStub{err: errors.New("synthetic fetch failure")}
		result, err := NewGmailBriefEvaluator(provider, fetcher).EvaluateBrief(gmailEvaluatorContext(t), gmailEvaluatorMessage("Source: https://example.com/ai"), []string{"CORE route"})
		if err != nil || result.Status != gmailBriefStatusVerified || len(result.Proposals) != 0 || len(result.KnowledgeProposals) != 1 || result.KnowledgeProposals[0].VerificationStatus != "unverified" || len(provider.requests) != 1 {
			t.Fatalf("fetch failure was not retained as candidate: result=%+v err=%v", result, err)
		}
	})
	t.Run("hallucinated quote becomes candidate", func(t *testing.T) {
		provider := &gmailEvaluatorLLMStub{responses: []domainllm.GenerateResponse{
			{Content: gmailEvaluatorExtraction("ai", "https://example.com/ai")},
			{Content: gmailEvaluatorAssessment("include", "This quote is absent from fetched evidence.")},
		}}
		fetcher := gmailEvaluatorFetcher("https://example.com/ai", "Only the official text is here.")
		result, err := NewGmailBriefEvaluator(provider, fetcher).EvaluateBrief(gmailEvaluatorContext(t), gmailEvaluatorMessage("Source: https://example.com/ai"), []string{"CORE route"})
		if err != nil || result.Status != gmailBriefStatusVerified || len(result.Proposals) != 0 || len(result.KnowledgeProposals) != 1 || result.TopicOutcomes[0].Status != "candidate" {
			t.Fatalf("hallucinated quote was not isolated: result=%+v err=%v", result, err)
		}
		if len(result.KnowledgeProposals[0].Sources) != 1 || len(result.KnowledgeProposals[0].Sources[0].Quotes) != 0 || strings.Contains(buildGmailKnowledgeSummary(result.KnowledgeProposals[0]), "This quote is absent") {
			t.Fatalf("hallucinated quote survived fallback: %+v", result.KnowledgeProposals[0])
		}
	})
	t.Run("unrelated citation URL becomes candidate", func(t *testing.T) {
		provider := &gmailEvaluatorLLMStub{responses: []domainllm.GenerateResponse{
			{Content: gmailEvaluatorExtraction("ai", "https://example.com/ai")},
			{Content: gmailEvaluatorAssessmentAtURL("include", "https://example.com/unrelated", "Only the official text is here.")},
		}}
		fetcher := gmailEvaluatorFetcher("https://example.com/ai", "Only the official text is here.")
		result, err := NewGmailBriefEvaluator(provider, fetcher).EvaluateBrief(gmailEvaluatorContext(t), gmailEvaluatorMessage("Source: https://example.com/ai"), []string{"CORE route"})
		if err != nil || result.Status != gmailBriefStatusVerified || len(result.Proposals) != 0 || len(result.KnowledgeProposals) != 1 || result.TopicOutcomes[0].Status != "candidate" {
			t.Fatalf("unrelated citation URL was not isolated: result=%+v err=%v", result, err)
		}
		if len(result.KnowledgeProposals[0].Sources) != 1 || len(result.KnowledgeProposals[0].Sources[0].Quotes) != 0 {
			t.Fatalf("unrelated citation quote survived fallback: %+v", result.KnowledgeProposals[0])
		}
	})
}

func TestGmailBriefEvaluatorSkipsNoRenCrowValue(t *testing.T) {
	const sourceURL = "https://example.com/ai"
	provider := &gmailEvaluatorLLMStub{responses: []domainllm.GenerateResponse{
		{Content: gmailEvaluatorExtraction("ai", sourceURL)},
		{Content: `{"decision":"skip","verification_status":"unverified","reason":"The source has no actionable RenCrow value.","relevance":"No existing owner or route is affected.","verification":"The source was fetched, but the claim is not useful for this backlog.","source_quotes":[],"title":"","purpose":"","specification":"","acceptance_criteria":[],"affected_modules":[]}`},
	}}
	fetcher := gmailEvaluatorFetcher(sourceURL, "Fetched source text.")
	result, err := NewGmailBriefEvaluator(provider, fetcher).EvaluateBrief(gmailEvaluatorContext(t), gmailEvaluatorMessage("Source: "+sourceURL), []string{"title: CORE route"})
	if err != nil || result.Status != gmailBriefStatusVerified || len(result.Proposals) != 0 || len(result.KnowledgeProposals) != 1 || result.KnowledgeProposals[0].VerificationStatus != "unverified" {
		t.Fatalf("no-value topic was not retained as Knowledge candidate: result=%+v err=%v", result, err)
	}
}

func TestGmailBriefEvaluatorRequiresExplicitVerificationStatus(t *testing.T) {
	const sourceURL = "https://example.com/ai"
	provider := &gmailEvaluatorLLMStub{responses: []domainllm.GenerateResponse{
		{Content: gmailEvaluatorExtraction("ai", sourceURL)},
		{Content: `{"decision":"skip","reason":"The source was assessed.","relevance":"No adopted route.","verification":"The source was fetched.","source_quotes":[],"title":"","purpose":"","specification":"","acceptance_criteria":[],"affected_modules":[]}`},
	}}
	fetcher := gmailEvaluatorFetcher(sourceURL, "Fetched source text.")
	result, err := NewGmailBriefEvaluator(provider, fetcher).EvaluateBrief(gmailEvaluatorContext(t), gmailEvaluatorMessage("Source: "+sourceURL), []string{"title: CORE route"})
	if err != nil || result.Status != gmailBriefStatusVerified || len(result.KnowledgeProposals) != 1 || result.TopicOutcomes[0].Status != "candidate" {
		t.Fatalf("missing verification_status was not isolated: result=%+v err=%v", result, err)
	}
}

func TestGmailBriefEvaluatorRequiresIdentityAndUserShiroScopeBeforeDependencies(t *testing.T) {
	provider := &gmailEvaluatorLLMStub{responses: []domainllm.GenerateResponse{{Content: gmailEvaluatorExtraction("ai", "https://example.com/ai")}}}
	fetcher := gmailEvaluatorFetcher("https://example.com/ai", "text")
	result, err := NewGmailBriefEvaluator(provider, fetcher).EvaluateBrief(context.Background(), gmailEvaluatorMessage("Source: https://example.com/ai"), []string{"CORE route"})
	if !errors.Is(err, ErrGmailBriefEvaluatorUnavailable) || result.Status != "" || len(provider.requests) != 0 || len(fetcher.requests) != 0 {
		t.Fatalf("missing scope was not unavailable: result=%+v err=%v", result, err)
	}
}
