package gmailintake

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	appbacklog "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

func TestGmailDailyNumberedCoverageKeepsTenSections(t *testing.T) {
	body := strings.Join([]string{
		"1. Topic one", "source one", "2. Topic two", "source two", "3. Topic three", "source three",
		"4. Topic four", "source four", "5. Topic five", "source five", "6. Topic six", "source six",
		"7. Topic seven", "source seven", "8. Topic eight", "source eight", "9. Topic nine", "source nine",
		"10. Topic ten", "source ten",
	}, "\n")
	input, coverage, err := buildGmailDailyExtractionInput("AI・政治デイリーブリーフ", body)
	if err != nil {
		t.Fatal(err)
	}
	if !coverage.Required || coverage.Mode != gmailDailyExtractionModeNumbered || coverage.ExpectedSections != 10 || input.Body != "" || len(input.Sections) != 10 {
		t.Fatalf("input=%+v coverage=%+v", input, coverage)
	}
	for index, section := range input.Sections {
		if section.Index != index+1 || !strings.Contains(section.Text, fmt.Sprintf("%d.", index+1)) {
			t.Fatalf("section %d=%+v", index+1, section)
		}
	}
}

func TestGmailDailyNumberedCoverageHandlesEmbeddedAlternateDelimiter(t *testing.T) {
	const embeddedURL = "https://example.com/embedded"
	lines := []string{
		"1. Topic one",
		"section one context",
		"1) Embedded detail " + embeddedURL,
		"2) Another embedded detail",
		"3) Final embedded detail",
	}
	for index := 2; index <= 10; index++ {
		lines = append(lines, fmt.Sprintf("%d. Topic %d", index, index), fmt.Sprintf("section %d context", index))
	}

	input, coverage, err := buildGmailDailyExtractionInput("AI・政治デイリーブリーフ", strings.Join(lines, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if coverage.Mode != gmailDailyExtractionModeNumbered || coverage.ExpectedSections != 10 || len(input.Sections) != 10 {
		t.Fatalf("input=%+v coverage=%+v", input, coverage)
	}
	if !strings.Contains(input.Sections[0].Text, "1) Embedded detail "+embeddedURL) || !strings.Contains(input.Sections[0].Text, "2) Another embedded detail") {
		t.Fatalf("alternate-delimiter list was not preserved in section one: %+v", input.Sections[0])
	}
	if _, ok := coverage.SectionURLs[1][embeddedURL]; !ok {
		t.Fatalf("embedded URL was not retained in section-one coverage: %+v", coverage.SectionURLs[1])
	}
	for index, section := range input.Sections {
		if section.Index != index+1 {
			t.Fatalf("section %d=%+v", index+1, section)
		}
	}
}

func TestGmailDailyNumberedCoverageRejectsMalformedCanonicalSequence(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "missing section", body: "1. Topic one\n3. Topic three"},
		{name: "duplicate section", body: "1. Topic one\n2. Topic two\n2. Duplicate topic"},
		{name: "out of order section", body: "1. Topic one\n3. Topic three\n2. Topic two"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := buildGmailDailyExtractionInput("AI・政治デイリーブリーフ", test.body); err == nil {
				t.Fatal("malformed canonical numbered sequence was accepted")
			}
		})
	}
}

func TestGmailUnstructuredExtractionOmitsNumberedSectionFields(t *testing.T) {
	content := `{"coverage":{"mode":"unstructured","expected_sections":0,"covered_topics":1},"topics":[{"title":"Unstructured topic","claim":"A bounded claim","kind":"other","urls":[]}]}`
	topics, err := decodeGmailExtractionForCoverage(content, gmailDailyExtractionCoverage{Mode: gmailDailyExtractionModeUnstructured, Required: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) != 1 || topics[0].SectionIndex != 0 {
		t.Fatalf("topics=%+v", topics)
	}
}

func TestGmailBriefEvaluatorAllowsSixAtlasTopicsAndOtherKnowledge(t *testing.T) {
	const otherURL = "https://example.com/other"
	var extraction strings.Builder
	extraction.WriteString(`{"coverage":{"mode":"numbered","expected_sections":7,"section_indices":[1,2,3,4,5,6,7],"covered_topics":7},"topics":[`)
	responses := make([]domainllm.GenerateResponse, 0, 8)
	for index := 1; index <= 6; index++ {
		url := fmt.Sprintf("https://example.com/ai-%d", index)
		if index > 1 {
			extraction.WriteByte(',')
		}
		fmt.Fprintf(&extraction, `{"title":"AI %d","claim":"A useful AI claim %d","kind":"ai","section_index":%d,"urls":[%q]}`, index, index, index, url)
		quote := fmt.Sprintf("The primary source confirms AI route %d.", index)
		responses = append(responses, domainllm.GenerateResponse{Content: gmailEvaluatorAssessmentAtURL("include", url, quote)})
	}
	extraction.WriteString(fmt.Sprintf(`,{"title":"Other","claim":"An informational claim","kind":"other","section_index":7,"urls":[%q]}]}`, otherURL))
	responses = append(responses, domainllm.GenerateResponse{Content: gmailEvaluatorAssessmentAtURL("skip", otherURL, "The publisher reports an informational claim.")})
	provider := &gmailEvaluatorLLMStub{responses: append([]domainllm.GenerateResponse{{Content: extraction.String()}}, responses...)}
	fetcher := &gmailEvaluatorFetcherStub{responses: map[string]modulewebgather.FetchResponse{}}
	var body strings.Builder
	for index := 1; index <= 6; index++ {
		url := fmt.Sprintf("https://example.com/ai-%d", index)
		quote := fmt.Sprintf("The primary source confirms AI route %d.", index)
		fetcher.responses[url] = modulewebgather.FetchResponse{URL: url, FinalURL: url, Status: "ok", HTTPStatus: 200, ExtractedText: quote, RawHash: modulewebgather.SHA256Text(quote), RawBytes: int64(len([]byte(quote)))}
		fmt.Fprintf(&body, "%d. AI %d %s\n", index, index, url)
	}
	otherQuote := "The publisher reports an informational claim."
	fetcher.responses[otherURL] = modulewebgather.FetchResponse{URL: otherURL, FinalURL: otherURL, Status: "ok", HTTPStatus: 200, ExtractedText: otherQuote, RawHash: modulewebgather.SHA256Text(otherQuote), RawBytes: int64(len([]byte(otherQuote)))}
	fmt.Fprintf(&body, "7. Other %s\n", otherURL)
	result, err := NewGmailBriefEvaluator(provider, fetcher).EvaluateBrief(gmailEvaluatorContext(t), gmailEvaluatorMessage(body.String()), []string{"CORE route"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Proposals) != 6 || len(result.KnowledgeProposals) != 1 || len(result.TopicOutcomes) != 7 {
		t.Fatalf("result=%+v", result)
	}
}

func TestGmailBriefEvaluatorIsolatesMalformedTopicAndContinues(t *testing.T) {
	const (
		goodURL     = "https://example.com/good"
		badURL      = "https://example.com/bad"
		politicsURL = "https://example.com/politics-isolated"
	)
	goodQuote := "The primary source confirms the useful RenCrow AI route."
	badQuote := "This forged quote is absent from the source."
	politicsQuote := "The publisher reports the policy change."
	extraction := `{"coverage":{"mode":"numbered","expected_sections":3,"section_indices":[1,2,3],"covered_topics":3},"topics":[` +
		fmt.Sprintf(`{"title":"Good AI","claim":"A useful AI claim","kind":"ai","section_index":1,"urls":[%q]},`, goodURL) +
		fmt.Sprintf(`{"title":"Malformed AI","claim":"A malformed AI assessment","kind":"ai","section_index":2,"urls":[%q]},`, badURL) +
		fmt.Sprintf(`{"title":"Politics","claim":"A reported policy claim","kind":"politics","section_index":3,"urls":[%q]}]}`, politicsURL)
	provider := &gmailEvaluatorLLMStub{responses: []domainllm.GenerateResponse{
		{Content: extraction},
		{Content: gmailEvaluatorAssessmentAtURL("include", goodURL, goodQuote)},
		{Content: gmailEvaluatorAssessmentAtURL("include", badURL, badQuote)},
		{Content: gmailEvaluatorAssessmentAtURL("skip", politicsURL, politicsQuote)},
	}}
	fetcher := &gmailEvaluatorFetcherStub{responses: map[string]modulewebgather.FetchResponse{
		goodURL:     {URL: goodURL, FinalURL: goodURL, Status: "ok", HTTPStatus: 200, ExtractedText: goodQuote, RawHash: modulewebgather.SHA256Text(goodQuote), RawBytes: int64(len([]byte(goodQuote)))},
		badURL:      {URL: badURL, FinalURL: badURL, Status: "ok", HTTPStatus: 200, ExtractedText: "The source contains only the original bad topic text.", RawHash: modulewebgather.SHA256Text("The source contains only the original bad topic text."), RawBytes: int64(len([]byte("The source contains only the original bad topic text.")))},
		politicsURL: {URL: politicsURL, FinalURL: politicsURL, Status: "ok", HTTPStatus: 200, ExtractedText: politicsQuote, RawHash: modulewebgather.SHA256Text(politicsQuote), RawBytes: int64(len([]byte(politicsQuote)))},
	}}
	body := strings.Join([]string{"1. Good AI ", goodURL, "\n2. Malformed AI ", badURL, "\n3. Politics ", politicsURL}, "")
	message := gmailEvaluatorMessage(body)
	message.ID = "mixed-topic"
	message.ReceivedAt = "2026-09-14T00:01:00Z"
	result, err := NewGmailBriefEvaluator(provider, fetcher).EvaluateBrief(gmailEvaluatorContext(t), message, []string{"CORE route"})
	if err != nil || result.Status != gmailBriefStatusVerified || len(result.Proposals) != 1 || len(result.KnowledgeProposals) != 2 || len(result.TopicOutcomes) != 3 {
		t.Fatalf("isolated topic result=%+v err=%v", result, err)
	}
	wantStatuses := []string{"atlas", "candidate", "reviewed"}
	for index, want := range wantStatuses {
		if result.TopicOutcomes[index].Status != want {
			t.Fatalf("outcome %d=%+v, want %s", index, result.TopicOutcomes[index], want)
		}
	}
	var malformed GmailKnowledgeProposal
	for _, proposal := range result.KnowledgeProposals {
		if proposal.Topic == "Malformed AI" {
			malformed = proposal
		}
	}
	if malformed.VerificationStatus != "unverified" || len(malformed.Sources) != 1 || len(malformed.Sources[0].Quotes) != 0 || malformed.Sources[0].ContentHash == "" || malformed.Sources[0].FinalURL != badURL || strings.Contains(buildGmailKnowledgeSummary(malformed), badQuote) {
		t.Fatalf("malformed assessment evidence was not fail-closed: %+v", malformed)
	}
	intake := NewGmailIntake("user@example.com", "user-1", nil, nil, nil, nil)
	prepared, err := intake.prepareKnowledgeProposals(message, result.KnowledgeProposals)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range prepared {
		if item.Item.Topic == "Malformed AI" && strings.Contains(item.Item.Summary, badQuote) {
			t.Fatalf("forged quote reached prepared Knowledge item: %+v", item.Item)
		}
	}
}

func TestGmailBriefEvaluatorMixedTopicsRouteAtlasAndKnowledge(t *testing.T) {
	const (
		valuableURL = "https://example.com/valuable"
		lowURL      = "https://example.com/low"
		politicsURL = "https://example.com/politics"
		failedURL   = "https://example.com/unavailable"
	)
	valuableQuote := "The primary AI publication describes a bounded RenCrow integration."
	lowQuote := "The source reports a low value AI announcement."
	politicsQuote := "The publisher reports a public policy change."
	extraction := `{"coverage":{"mode":"numbered","expected_sections":4,"section_indices":[1,2,3,4],"covered_topics":4},"topics":[` +
		fmt.Sprintf(`{"title":"Valuable AI","claim":"A useful AI route","kind":"ai","section_index":1,"urls":[%q]},`, valuableURL) +
		fmt.Sprintf(`{"title":"Low AI","claim":"A low value AI item","kind":"ai","section_index":2,"urls":[%q]},`, lowURL) +
		fmt.Sprintf(`{"title":"Politics","claim":"A policy report","kind":"politics","section_index":3,"urls":[%q]},`, politicsURL) +
		fmt.Sprintf(`{"title":"Unavailable","claim":"A source that cannot be fetched","kind":"other","section_index":4,"urls":[%q]}]}`, failedURL)
	provider := &gmailEvaluatorLLMStub{responses: []domainllm.GenerateResponse{
		{Content: extraction},
		{Content: gmailEvaluatorAssessmentAtURL("include", valuableURL, valuableQuote)},
		{Content: gmailEvaluatorAssessmentAtURL("skip", lowURL, lowQuote)},
		{Content: gmailEvaluatorAssessmentAtURL("skip", politicsURL, politicsQuote)},
	}}
	fetcher := &gmailEvaluatorFetcherStub{responses: map[string]modulewebgather.FetchResponse{
		valuableURL: {URL: valuableURL, FinalURL: valuableURL, Status: "ok", HTTPStatus: 200, ExtractedText: valuableQuote, RawHash: modulewebgather.SHA256Text(valuableQuote), RawBytes: int64(len(valuableQuote))},
		lowURL:      {URL: lowURL, FinalURL: lowURL, Status: "ok", HTTPStatus: 200, ExtractedText: lowQuote, RawHash: modulewebgather.SHA256Text(lowQuote), RawBytes: int64(len(lowQuote))},
		politicsURL: {URL: politicsURL, FinalURL: politicsURL, Status: "ok", HTTPStatus: 200, ExtractedText: politicsQuote, RawHash: modulewebgather.SHA256Text(politicsQuote), RawBytes: int64(len(politicsQuote))},
	}}
	body := strings.Join([]string{
		"1. Valuable AI ", valuableURL, "\n2. Low AI ", lowURL, "\n3. Politics ", politicsURL, "\n4. Unavailable ", failedURL,
	}, "")
	result, err := NewGmailBriefEvaluator(provider, fetcher).EvaluateBrief(gmailEvaluatorContext(t), gmailEvaluatorMessage(body), []string{"id: atlas-1 title: existing CORE route"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != gmailBriefStatusVerified || len(result.Proposals) != 1 || len(result.KnowledgeProposals) != 3 || len(result.TopicOutcomes) != 4 {
		t.Fatalf("result=%+v", result)
	}
	wantStatuses := []string{"atlas", "reviewed", "reviewed", "candidate"}
	for index, want := range wantStatuses {
		if result.TopicOutcomes[index].Status != want {
			t.Fatalf("outcome %d=%+v, want %s", index, result.TopicOutcomes[index], want)
		}
	}
	if len(result.TopicOutcomes[3].FailedURLs) != 1 || result.TopicOutcomes[3].FailedURLs[0] != failedURL {
		t.Fatalf("failed URL was not retained: %+v", result.TopicOutcomes[3])
	}
}

func TestGmailIntakeRejectsVerifiedKnowledgeWithoutSourceEvidence(t *testing.T) {
	intake := NewGmailIntake("user@example.com", "user-1", nil, nil, nil, nil)
	message := gmailIntakeMessage("AI・政治デイリーブリーフ", "daily body", "a1")
	_, err := intake.prepareKnowledgeProposals(message, []GmailKnowledgeProposal{{
		Topic: "Politics", Claim: "A reported claim", Kind: "politics", Summary: "The claim was assessed.",
		VerificationStatus: "verified", VerificationReason: "The claim is verified.",
	}})
	if err == nil {
		t.Fatal("verified Knowledge proposal without fetched evidence was accepted")
	}
}

func TestGmailIntakeRejectsPreparedKnowledgeForDifferentConfiguredUser(t *testing.T) {
	items := &memoryItemStore{}
	atlas := appbacklog.NewService(items, nil)
	receipts := newGmailIntakeReceiptMemory()
	message := gmailIntakeMessage("AI・政治デイリーブリーフ", "daily https://example.com/source", "a1")
	intake := NewGmailIntake("user@example.com", "user-1", &gmailIntakeCollectorStub{page: gmailIntakePage(message)}, nil, receipts, atlas)
	proposal := GmailKnowledgeProposal{
		Topic: "Politics", Claim: "A reported claim", Kind: "politics", Summary: "The claim was assessed.",
		VerificationStatus: "verified", VerificationReason: "The publisher verifies the attributed report.",
		Sources: []GmailKnowledgeSource{{URL: "https://example.com/source", FinalURL: "https://example.com/source", ContentHash: modulewebgather.SHA256Text("quoted evidence"), CapturedAt: "2026-09-13T00:01:00Z", VerificationStatus: "verified", VerificationReason: "publisher quote", Quotes: []GmailKnowledgeQuote{{URL: "https://example.com/source", Quote: "quoted evidence", Primary: false}}}},
	}
	prepared, err := intake.prepareKnowledgeProposals(message, []GmailKnowledgeProposal{proposal})
	if err != nil {
		t.Fatal(err)
	}
	prepared[0].Item.UserID = "user-2"
	ctx := gmailIntakeAgentContext(t, "user-1")
	receipt := intake.newReceipt(ctx, message, "prepared", "resume", nil, nil)
	receipt.PreparedKnowledge = prepared
	if err := receipts.Save(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if report, err := intake.Run(ctx); err == nil || report.Processed != 1 || len(items.items) != 0 || receipts.cursor != "" {
		t.Fatalf("cross-user prepared Knowledge was accepted: report=%+v err=%v items=%d cursor=%q", report, err, len(items.items), receipts.cursor)
	}
}

type gmailDailyKnowledgeWriterStub struct {
	mu     sync.Mutex
	items  map[string]domainkm.NewsKnowledgeItem
	calls  int
	failAt int
}

type gmailLongBodyProvider struct {
	extraction string
	assessment string
	calls      int
}

func (p *gmailLongBodyProvider) Name() string { return "gmail-long-body-test" }

func (p *gmailLongBodyProvider) Generate(_ context.Context, request domainllm.GenerateRequest) (domainllm.GenerateResponse, error) {
	p.calls++
	switch {
	case request.SystemPrompt == gmailBriefExtractionSystemPrompt:
		return domainllm.GenerateResponse{Content: p.extraction}, nil
	case strings.Contains(request.SystemPrompt, "evidence_ids"):
		return domainllm.GenerateResponse{Content: `{"summary":"長文本文の要点","evidence_ids":[1]}`}, nil
	case request.SystemPrompt == gmailBriefAssessmentSystemPrompt:
		return domainllm.GenerateResponse{Content: p.assessment}, nil
	default:
		return domainllm.GenerateResponse{}, errors.New("unexpected Gmail long-body prompt")
	}
}

func gmailLongBodyTestProvider(t *testing.T, sourceURL, quote string) *gmailLongBodyProvider {
	t.Helper()
	return &gmailLongBodyProvider{
		extraction: fmt.Sprintf(`{"coverage":{"mode":"unstructured","expected_sections":0,"covered_topics":1},"topics":[{"title":"Politics","claim":"A long source-backed policy claim","kind":"politics","urls":[%q]}]}`, sourceURL),
		assessment: gmailEvaluatorAssessmentAtURL("skip", sourceURL, quote),
	}
}

func TestGmailSummarizeEvidenceAcceptsBareAndPrefixedHashForms(t *testing.T) {
	const sourceURL = "https://example.com/long"
	quote := "The long source confirms the reported policy change."
	body := quote + strings.Repeat(" Additional source context.", 400)
	digest := modulewebgather.SHA256Text(body)
	for _, contentHash := range []string{digest, strings.TrimPrefix(digest, "sha256:")} {
		t.Run(contentHash[:8], func(t *testing.T) {
			provider := gmailLongBodyTestProvider(t, sourceURL, quote)
			evaluator := NewGmailBriefEvaluator(provider, &gmailEvaluatorFetcherStub{}).(*gmailBriefEvaluator)
			input := gmailEvidence{URL: sourceURL, FinalURL: sourceURL, ContentHash: contentHash, Text: body, FullText: body, CapturedAt: "2026-09-14T00:01:00Z"}
			result, err := evaluator.summarizeGmailEvidence(gmailEvaluatorContext(t), input)
			if err != nil || result.Text != "長文本文の要点" || len(result.SummaryEvidenceQuotes) == 0 {
				t.Fatalf("summary=%+v err=%v", result, err)
			}
		})
	}
}

func TestGmailSummarizeEvidenceRejectsMalformedOrMismatchedHash(t *testing.T) {
	const sourceURL = "https://example.com/long"
	quote := "The long source confirms the reported policy change."
	body := quote + strings.Repeat(" Additional source context.", 400)
	for _, test := range []struct {
		name string
		hash string
	}{
		{name: "malformed", hash: "not-a-sha256"},
		{name: "mismatch", hash: "sha256:" + strings.Repeat("0", 64)},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := gmailLongBodyTestProvider(t, sourceURL, quote)
			evaluator := NewGmailBriefEvaluator(provider, &gmailEvaluatorFetcherStub{}).(*gmailBriefEvaluator)
			input := gmailEvidence{URL: sourceURL, FinalURL: sourceURL, ContentHash: test.hash, Text: body, FullText: body, CapturedAt: "2026-09-14T00:01:00Z"}
			_, err := evaluator.summarizeGmailEvidence(gmailEvaluatorContext(t), input)
			if !errors.Is(err, ErrGmailBriefEvidenceInvalid) {
				t.Fatalf("error=%v, want evidence-invalid", err)
			}
		})
	}
}

func TestGmailIntakeRunUsesSharedLongBodySummaryAndPersistsResult(t *testing.T) {
	const sourceURL = "https://example.com/long"
	quote := "The long source confirms the reported policy change."
	sourceBody := quote + strings.Repeat(" Additional source context.", 400)
	provider := gmailLongBodyTestProvider(t, sourceURL, quote)
	fetcher := &gmailEvaluatorFetcherStub{responses: map[string]modulewebgather.FetchResponse{
		sourceURL: {URL: sourceURL, FinalURL: sourceURL, Status: "ok", HTTPStatus: 200, ExtractedText: sourceBody, RawHash: modulewebgather.SHA256Text(sourceBody), RawBytes: int64(len([]byte(sourceBody)))},
	}}
	evaluator := NewGmailBriefEvaluator(provider, fetcher)
	message := gmailIntakeMessage(gmailDailySubject, "Read this source: "+sourceURL, "a4")
	collector := &gmailIntakeCollectorStub{page: gmailIntakePage(message)}
	receipts := newGmailIntakeReceiptMemory()
	writer := &gmailDailyKnowledgeWriterStub{items: make(map[string]domainkm.NewsKnowledgeItem)}
	atlas := appbacklog.NewService(&memoryItemStore{}, nil)
	intake := NewGmailIntake("user@example.com", "user-1", collector, evaluator, receipts, atlas).WithKnowledgeWriter(writer)

	report, err := intake.Run(gmailIntakeAgentContext(t, "user-1"))
	if err != nil || report.Created != 1 || len(report.KnowledgeItemIDs) != 1 || collector.calls != 1 || writer.calls != 1 || receipts.cursor != "next" {
		t.Fatalf("report=%+v err=%v collector_calls=%d writer_calls=%d cursor=%q provider_calls=%d", report, err, collector.calls, writer.calls, receipts.cursor, provider.calls)
	}
	receipt, found, err := receipts.Get(context.Background(), "user@example.com", message.ID)
	if err != nil || !found || receipt.Status != "complete" || receipt.PolicyRevision != gmailPolicyRevision {
		t.Fatalf("receipt=%+v found=%v err=%v", receipt, found, err)
	}
}

func (s *gmailDailyKnowledgeWriterStub) SaveNewsKnowledgeItemWithReceipt(_ context.Context, item domainkm.NewsKnowledgeItem, sourceKey string, actorID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.failAt != 0 && s.calls == s.failAt {
		return false, errors.New("synthetic knowledge write failure")
	}
	if actorID != "shiro" || sourceKey == "" {
		return false, errors.New("invalid writer identity")
	}
	if existing, ok := s.items[sourceKey]; ok {
		if existing != item {
			return false, errors.New("knowledge source collision")
		}
		return true, nil
	}
	s.items[sourceKey] = item
	return false, nil
}

func TestGmailIntakeMissingKnowledgeWriterLeavesPreparedReceiptForRetry(t *testing.T) {
	items := &memoryItemStore{}
	atlas := appbacklog.NewService(items, nil)
	receipts := newGmailIntakeReceiptMemory()
	message := gmailIntakeMessage("AI・政治デイリーブリーフ", "daily https://example.com/source", "a1")
	webHash := modulewebgather.SHA256Text("quoted evidence")
	evaluator := &gmailIntakeEvaluatorStub{evaluation: GmailBriefEvaluation{
		Status: "verified", Reason: "knowledge only",
		KnowledgeProposals: []GmailKnowledgeProposal{{Topic: "Politics", Claim: "A report", Kind: "politics", VerificationStatus: "verified", VerificationReason: "publisher quote", Sources: []GmailKnowledgeSource{{URL: "https://example.com/source", FinalURL: "https://example.com/source", ContentHash: webHash, CapturedAt: "2026-09-13T00:01:00Z", VerificationStatus: "verified", VerificationReason: "publisher quote", Quotes: []GmailKnowledgeQuote{{URL: "https://example.com/source", Quote: "quoted evidence", Primary: false}}}}}},
		TopicOutcomes:      []GmailTopicOutcome{{TopicIndex: 1, Kind: "politics", Topic: "Politics", Status: "reviewed", Reason: "publisher quote", SourceURLs: []string{"https://example.com/source"}}},
	}}
	intake := NewGmailIntake("user@example.com", "user-1", &gmailIntakeCollectorStub{page: gmailIntakePage(message)}, evaluator, receipts, atlas)
	if _, err := intake.Run(gmailIntakeAgentContext(t, "user-1")); !errors.Is(err, ErrGmailKnowledgeWriterUnavailable) {
		t.Fatalf("missing writer error=%v", err)
	}
	prepared, found, err := receipts.Get(context.Background(), "user@example.com", "a1")
	if err != nil || !found || prepared.Status != "prepared" || len(prepared.PreparedKnowledge) != 1 || receipts.cursor != "" {
		t.Fatalf("prepared retry state=%+v found=%v err=%v cursor=%q", prepared, found, err, receipts.cursor)
	}
	writer := &gmailDailyKnowledgeWriterStub{items: make(map[string]domainkm.NewsKnowledgeItem)}
	intake.WithKnowledgeWriter(writer)
	report, err := intake.Run(gmailIntakeAgentContext(t, "user-1"))
	if err != nil || evaluator.calls != 1 || report.Created != 1 || len(report.KnowledgeItemIDs) != 1 || receipts.cursor != "next" {
		t.Fatalf("prepared retry report=%+v err=%v evaluator_calls=%d cursor=%q", report, err, evaluator.calls, receipts.cursor)
	}
}

func TestGmailIntakeKnowledgeResumePreservesPrivateIdentityAndNoSemanticRepeat(t *testing.T) {
	items := &memoryItemStore{}
	atlas := appbacklog.NewService(items, nil)
	receipts := newGmailIntakeReceiptMemory()
	writer := &gmailDailyKnowledgeWriterStub{items: make(map[string]domainkm.NewsKnowledgeItem), failAt: 1}
	message := gmailIntakeMessage("AI・政治デイリーブリーフ", "daily https://example.com/source", "a1")
	webText := "source quote for Atlas"
	webHash := modulewebgather.SHA256Text(webText)
	evaluator := &gmailIntakeEvaluatorStub{evaluation: GmailBriefEvaluation{
		Status: "verified", Reason: "mixed daily",
		Proposals:          []appbacklog.IntakeRequest{{Title: "Atlas topic", Purpose: "purpose", Body: "body", AcceptanceCriteria: []string{"accept"}, SourceRefs: []domainbacklog.SourceRef{{Type: "web", Strength: "primary", Locator: "https://example.com/source", ContentHash: webHash, CapturedAt: "2026-09-13T00:01:00Z", RawOrSummary: webText}}}},
		KnowledgeProposals: []GmailKnowledgeProposal{{Topic: "Politics topic", Claim: "reported claim", Kind: "politics", VerificationStatus: "verified", VerificationReason: "publisher quote verifies the attributed report", Sources: []GmailKnowledgeSource{{URL: "https://example.com/source", FinalURL: "https://example.com/source", ContentHash: webHash, CapturedAt: "2026-09-13T00:01:00Z", VerificationStatus: "verified", VerificationReason: "publisher quote", Quotes: []GmailKnowledgeQuote{{URL: "https://example.com/source", Quote: webText, Primary: true}}}}}},
		TopicOutcomes:      []GmailTopicOutcome{{TopicIndex: 1, Kind: "ai", Topic: "Atlas topic", Status: "atlas", Reason: "include", SourceURLs: []string{"https://example.com/source"}}, {TopicIndex: 2, Kind: "politics", Topic: "Politics topic", Status: "reviewed", Reason: "verified", SourceURLs: []string{"https://example.com/source"}}},
	}}
	collector := &gmailIntakeCollectorStub{page: gmailIntakePage(message)}
	intake := NewGmailIntake("user@example.com", "user-1", collector, evaluator, receipts, atlas).WithKnowledgeWriter(writer)
	if prepared, prepErr := intake.prepareKnowledgeProposals(message, evaluator.evaluation.KnowledgeProposals); prepErr != nil {
		t.Fatalf("prepare knowledge: %v", prepErr)
	} else if len(prepared) != 1 {
		t.Fatalf("prepared knowledge=%+v", prepared)
	}
	if firstReport, err := intake.Run(gmailIntakeAgentContext(t, "user-1")); err == nil {
		t.Fatalf("synthetic Knowledge write failure was not returned: report=%+v writer_calls=%d receipt=%+v", firstReport, writer.calls, receipts.receipts)
	}
	if evaluator.calls != 1 || receipts.cursor != "" || len(items.items) != 1 || len(writer.items) != 0 {
		t.Fatalf("partial run evaluator=%d cursor=%q atlas=%d knowledge=%d", evaluator.calls, receipts.cursor, len(items.items), len(writer.items))
	}
	writer.mu.Lock()
	writer.failAt = 0
	writer.mu.Unlock()
	report, err := intake.Run(gmailIntakeAgentContext(t, "user-1"))
	if err != nil {
		t.Fatal(err)
	}
	if evaluator.calls != 1 || report.Created != 1 || len(report.KnowledgeItemIDs) != 1 || len(items.items) != 1 || len(writer.items) != 1 || receipts.cursor != "next" {
		t.Fatalf("resume report=%+v evaluator=%d atlas=%d knowledge=%d cursor=%q", report, evaluator.calls, len(items.items), len(writer.items), receipts.cursor)
	}
	for _, item := range writer.items {
		if item.UserID != "user-1" || item.Visibility != "private" || item.Status != "reviewed" || item.URL != "https://example.com/source" || !strings.Contains(item.Summary, webText) || !strings.Contains(item.Summary, webHash) {
			t.Fatalf("private source-backed item=%+v", item)
		}
	}
}

type gmailRetryEvaluatorStub struct {
	evaluation GmailBriefEvaluation
	err        error
	calls      int
}

func (s *gmailRetryEvaluatorStub) EvaluateBrief(_ context.Context, _ GmailMessage, _ []string) (GmailBriefEvaluation, error) {
	s.calls++
	return s.evaluation, s.err
}

func TestGmailIntakeRetriesTransientEvaluatorFailureWithoutConsumingMessage(t *testing.T) {
	items := &memoryItemStore{}
	atlas := appbacklog.NewService(items, nil)
	receipts := newGmailIntakeReceiptMemory()
	message := gmailIntakeMessage("AI・政治デイリーブリーフ", "daily https://example.com/source", "a1")
	evaluator := &gmailRetryEvaluatorStub{
		err:        &GmailBriefRetryableError{Reason: "provider temporarily unavailable", Err: errors.New("temporary")},
		evaluation: GmailBriefEvaluation{Status: "verified", Reason: "retry succeeded", Proposals: []appbacklog.IntakeRequest{{Title: "retry item", Purpose: "purpose", Body: "body", AcceptanceCriteria: []string{"accept"}, SourceRefs: []domainbacklog.SourceRef{{Type: "web", Strength: "primary", Locator: "https://example.com/source", ContentHash: modulewebgather.SHA256Text("body"), CapturedAt: "2026-09-13T00:01:00Z", RawOrSummary: "body"}}}}},
	}
	collector := &gmailIntakeCollectorStub{page: gmailIntakePage(message)}
	intake := NewGmailIntake("user@example.com", "user-1", collector, evaluator, receipts, atlas)
	if _, err := intake.Run(gmailIntakeAgentContext(t, "user-1")); !errors.Is(err, ErrGmailBriefRetryable) {
		t.Fatalf("transient evaluator error=%v", err)
	}
	if evaluator.calls != 1 || receipts.cursor != "" || len(receipts.receipts) != 0 || len(items.items) != 0 {
		t.Fatalf("transient failure consumed message: calls=%d cursor=%q receipts=%d items=%d", evaluator.calls, receipts.cursor, len(receipts.receipts), len(items.items))
	}
	evaluator.err = nil
	if report, err := intake.Run(gmailIntakeAgentContext(t, "user-1")); err != nil || report.Created != 1 || evaluator.calls != 2 || receipts.cursor != "next" || len(items.items) != 1 {
		t.Fatalf("retry result report=%+v err=%v calls=%d cursor=%q items=%d", report, err, evaluator.calls, receipts.cursor, len(items.items))
	}
}
