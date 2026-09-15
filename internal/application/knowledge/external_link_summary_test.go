package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type summaryTestProvider struct {
	mu       sync.Mutex
	requests []llm.GenerateRequest
	fn       func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error)
}

func (p *summaryTestProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	if p.fn == nil {
		return llm.GenerateResponse{Content: `{"summary":"本文の要点","evidence_ids":[1]}`}, nil
	}
	return p.fn(ctx, req)
}

func (p *summaryTestProvider) Name() string { return "summary-test-provider" }

func (p *summaryTestProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func sourceSpansFromRequest(t *testing.T, req llm.GenerateRequest) []externalLinkSummarySourceSpan {
	t.Helper()
	if len(req.Messages) == 0 {
		t.Fatal("provider request has no messages")
	}
	var prompt externalLinkSummaryPrompt
	if err := json.Unmarshal([]byte(req.Messages[0].Content), &prompt); err != nil {
		t.Fatalf("summary prompt is not source_spans JSON: %v; content=%q", err, req.Messages[0].Content)
	}
	return prompt.SourceSpans
}

func sourceSpanIDContaining(t *testing.T, req llm.GenerateRequest, needle string) int {
	t.Helper()
	for _, span := range sourceSpansFromRequest(t, req) {
		if strings.Contains(span.Text, needle) {
			return span.ID
		}
	}
	t.Fatalf("source span %q was not supplied: %q", needle, req.Messages[0].Content)
	return 0
}

type summaryLookupStore struct {
	mu    sync.Mutex
	items map[string]l1sqlite.L1StagingItem
}

func (s *summaryLookupStore) SaveStagingItem(_ context.Context, item l1sqlite.L1StagingItem) (*l1sqlite.L1StagingItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.items == nil {
		s.items = map[string]l1sqlite.L1StagingItem{}
	}
	key := item.Namespace + "\x00" + item.EventID
	if previous, ok := s.items[key]; ok {
		if item.ID == "" {
			item.ID = previous.ID
		}
		if item.ValidationStatus == "" {
			item.ValidationStatus = previous.ValidationStatus
		}
	}
	if item.ID == "" {
		item.ID = "staging:" + item.EventID
	}
	s.items[key] = item
	return &item, nil
}

func (s *summaryLookupStore) FindStagingItemByNamespaceEventID(_ context.Context, namespace, eventID string) (l1sqlite.L1StagingItem, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[namespace+"\x00"+eventID]
	return item, ok, nil
}

func externalSummaryRecord(body string, extra map[string]interface{}) string {
	meta := map[string]interface{}{
		"references": []interface{}{map[string]interface{}{
			"kind":             "external_url",
			"url":              "https://example.com/article",
			"capture_status":   "content_fetched",
			"page_description": "説明は要約入力に使わない",
			"preview_text":     "previewは要約入力に使わない",
			"body_text":        body,
		}},
	}
	for key, value := range extra {
		meta[key] = value
	}
	data, _ := json.Marshal(map[string]interface{}{
		"id":       "x:summary-test",
		"domain":   "general",
		"title":    "テスト",
		"summary":  "元の要約",
		"raw_text": "元のraw capture",
		"meta":     meta,
	})
	return string(data)
}

func importSummaryRecord(t *testing.T, ctx context.Context, store StagingStore, provider llm.LLMProvider, body string) (ImportResult, l1sqlite.L1StagingItem, error) {
	t.Helper()
	result, err := ImportKnowledgeCoreJSONL(ctx, store, strings.NewReader(externalSummaryRecord(body, nil)), ImportOptions{
		Now:            func() time.Time { return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) },
		LinkSummarizer: NewExternalLinkSummarizer(provider, "shiro"),
	})
	lookup, ok := store.(interface {
		FindStagingItemByNamespaceEventID(context.Context, string, string) (l1sqlite.L1StagingItem, bool, error)
	})
	if !ok {
		return result, l1sqlite.L1StagingItem{}, err
	}
	item, _, lookupErr := lookup.FindStagingItemByNamespaceEventID(ctx, "kb:general", "x:summary-test")
	if err == nil {
		err = lookupErr
	}
	return result, item, err
}

func TestSummarizeBodyReturnsBoundedResultAndKnowledgeObservation(t *testing.T) {
	taskID := modulecore.NewTaskID()
	traceID := "trace_summary_body"
	sessionID := "session_summary_body"
	var observed llm.ExecutionObservation
	var observedOK bool
	provider := &summaryTestProvider{fn: func(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		observed, observedOK = llm.ExecutionObservationFromContext(ctx)
		if len(req.Messages) != 1 {
			t.Fatalf("unexpected provider input: %+v", req.Messages)
		}
		spans := sourceSpansFromRequest(t, req)
		if len(spans) != 1 || spans[0].Text != strings.Repeat("本文SOURCE ", 30) || spans[0].ID != 1 {
			t.Fatalf("summary prompt did not preserve the complete source: %+v", spans)
		}
		return llm.GenerateResponse{Content: `{"summary":"本文の要点","evidence_ids":[1]}`}, nil
	}}
	ctx := llm.WithExecutionObservation(context.Background(), llm.ExecutionObservation{
		TraceID:   traceID,
		TaskID:    taskID,
		SessionID: sessionID,
		Initiator: "upstream",
		Caller:    "upstream.caller",
		Purpose:   "upstream-purpose",
	})
	body := strings.Repeat("本文SOURCE ", 30)

	got, err := NewExternalLinkSummarizer(provider, "shiro").SummarizeBody(ctx, "https://Example.com/article#fragment", body)
	if err != nil {
		t.Fatalf("SummarizeBody failed: %v", err)
	}
	if got.Text != "本文の要点" || len(got.EvidenceQuotes) != 1 || got.EvidenceQuotes[0] != body {
		t.Fatalf("unexpected summary result: %+v", got)
	}
	if got.BodySHA256 != externalLinkBodySHAForText(body) || got.Chunks != 1 {
		t.Fatalf("unexpected summary identity: %+v", got)
	}
	if !observedOK || observed.TraceID != traceID || observed.TaskID != taskID || observed.SessionID != sessionID {
		t.Fatalf("canonical observation identity was not preserved: ok=%t observation=%+v", observedOK, observed)
	}
	if observed.Initiator != "shiro" || observed.Caller != ExternalBodySummaryCaller || observed.Purpose != ExternalLinkSummaryPurpose {
		t.Fatalf("unexpected knowledge summary observation: %+v", observed)
	}
}

func TestSummarizeBodyRejectsInvalidBoundsURLProviderAndContext(t *testing.T) {
	validURL := "https://example.com/article"
	validBody := strings.Repeat("本文", 100)
	tests := []struct {
		name string
		url  string
		body string
		ctx  context.Context
	}{
		{name: "empty_url", url: "", body: validBody, ctx: context.Background()},
		{name: "private_url", url: "http://127.0.0.1/article", body: validBody, ctx: context.Background()},
		{name: "empty_body", url: validURL, body: "", ctx: context.Background()},
		{name: "short_body", url: validURL, body: strings.Repeat("短", externalLinkSummaryMinRunes-1), ctx: context.Background()},
		{name: "large_body", url: validURL, body: strings.Repeat("長", externalLinkSummaryMaxBodyRunes+1), ctx: context.Background()},
		{name: "invalid_utf8", url: validURL, body: string([]byte{0xff, 0xfe}), ctx: context.Background()},
	}
	provider := &summaryTestProvider{}
	summarizer := NewExternalLinkSummarizer(provider, "shiro")
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := summarizer.SummarizeBody(test.ctx, test.url, test.body); err == nil {
				t.Fatal("invalid input was accepted")
			}
			if calls := provider.callCount(); calls != 0 {
				t.Fatalf("invalid input called provider %d times", calls)
			}
		})
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := summarizer.SummarizeBody(canceled, validURL, validBody); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context error=%v, want context.Canceled", err)
	}
	if _, err := summarizer.SummarizeBody(nil, validURL, validBody); err == nil {
		t.Fatal("nil context was accepted")
	}
	if _, err := NewExternalLinkSummarizer(nil, "shiro").SummarizeBody(context.Background(), validURL, validBody); err == nil {
		t.Fatal("missing provider was accepted")
	}
	if calls := provider.callCount(); calls != 0 {
		t.Fatalf("rejected input called provider %d times", calls)
	}
}

func referenceSummary(t *testing.T, item l1sqlite.L1StagingItem) map[string]interface{} {
	t.Helper()
	refs, ok := item.Meta["references"].([]interface{})
	if !ok || len(refs) != 1 {
		t.Fatalf("references = %#v", item.Meta["references"])
	}
	ref, ok := refs[0].(map[string]interface{})
	if !ok {
		t.Fatalf("reference = %#v", refs[0])
	}
	summary, ok := ref["summary"].(map[string]interface{})
	if !ok {
		t.Fatalf("summary = %#v", ref["summary"])
	}
	return summary
}

func TestExternalLinkSummaryBlocksShortAndTruncatedBodiesWithoutLLM(t *testing.T) {
	for name, bodyAndMeta := range map[string]struct {
		body string
		meta map[string]interface{}
	}{
		"short":     {body: strings.Repeat("短", 199)},
		"truncated": {body: strings.Repeat("本文", 120), meta: map[string]interface{}{"body_truncated": true}},
		"unfetched": {body: strings.Repeat("本文", 120), meta: map[string]interface{}{"capture_status": "url_only"}},
	} {
		t.Run(name, func(t *testing.T) {
			provider := &summaryTestProvider{}
			store := &summaryLookupStore{}
			input := externalSummaryRecord(bodyAndMeta.body, nil)
			if bodyAndMeta.meta != nil {
				var record map[string]interface{}
				if err := json.Unmarshal([]byte(input), &record); err != nil {
					t.Fatal(err)
				}
				refs := record["meta"].(map[string]interface{})["references"].([]interface{})
				for key, value := range bodyAndMeta.meta {
					refs[0].(map[string]interface{})[key] = value
				}
				encoded, _ := json.Marshal(record)
				input = string(encoded)
			}
			result, err := ImportKnowledgeCoreJSONL(context.Background(), store, strings.NewReader(input), ImportOptions{
				Now:            func() time.Time { return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) },
				LinkSummarizer: NewExternalLinkSummarizer(provider, "shiro"),
			})
			if err != nil {
				t.Fatalf("import failed: %v", err)
			}
			if provider.callCount() != 0 {
				t.Fatalf("blocked body called provider %d times", provider.callCount())
			}
			if result.LinkSummaries.Blocked != 1 || result.LinkSummaries.Ready != 0 {
				t.Fatalf("counts = %+v", result.LinkSummaries)
			}
			summary := referenceSummary(t, store.items["kb:general\x00x:summary-test"])
			if summary["status"] != "blocked" {
				t.Fatalf("summary = %#v", summary)
			}
		})
	}
}

func TestExternalLinkSummaryPromptUsesBoundedContiguousSourceSpans(t *testing.T) {
	body := strings.Repeat("前", 399) + "\n" + "中\\\t" + strings.Repeat("後", 393) + " 終端"
	prompt, spans := externalLinkSummaryPromptInput(body)
	if prompt == "" || len(spans) < 2 {
		t.Fatalf("prompt/spans = %q, %#v", prompt, spans)
	}
	var rebuilt strings.Builder
	for index, span := range spans {
		if span.ID != index+1 {
			t.Fatalf("span IDs are not stable 1-based values: %#v", spans)
		}
		if runes := utf8.RuneCountInString(span.Text); runes == 0 || runes > externalLinkSummarySourceSpanRunes {
			t.Fatalf("span %d has invalid rune length %d: %q", span.ID, runes, span.Text)
		}
		if strings.TrimSpace(span.Text) == "" {
			t.Fatalf("whitespace-only span was exposed: %#v", span)
		}
		rebuilt.WriteString(span.Text)
	}
	if rebuilt.String() != body {
		t.Fatalf("non-whitespace source was omitted or changed: rebuilt=%q body=%q", rebuilt.String(), body)
	}
	if !strings.Contains(spans[0].Text, "前") || !strings.HasSuffix(spans[len(spans)-1].Text, "終端") {
		t.Fatalf("source boundary was not retained: %#v", spans)
	}

	withWhitespaceGap := strings.Repeat("a", externalLinkSummarySourceSpanRunes) + strings.Repeat(" ", externalLinkSummarySourceSpanRunes) + "終端"
	_, gapSpans := externalLinkSummaryPromptInput(withWhitespaceGap)
	if len(gapSpans) != 2 || gapSpans[0].Text != strings.Repeat("a", externalLinkSummarySourceSpanRunes) || gapSpans[1].Text != "終端" {
		t.Fatalf("only whitespace-only span may be omitted: %#v", gapSpans)
	}
}

func TestDecodeExternalLinkSummaryResponseValidatesEvidenceIDs(t *testing.T) {
	spans := []externalLinkSummarySourceSpan{{ID: 1, Text: "先頭\\  "}, {ID: 2, Text: " 終端"}}
	tests := []struct {
		name string
		data string
		code string
	}{
		{name: "malformed", data: `not-json`, code: "invalid_response"},
		{name: "unknown_field", data: `{"summary":"要点","evidence_ids":[1],"extra":true}`, code: "invalid_response"},
		{name: "negative", data: `{"summary":"要点","evidence_ids":[-1]}`, code: "invalid_evidence"},
		{name: "zero", data: `{"summary":"要点","evidence_ids":[0]}`, code: "invalid_evidence"},
		{name: "out_of_range", data: `{"summary":"要点","evidence_ids":[3]}`, code: "invalid_evidence"},
		{name: "duplicate", data: `{"summary":"要点","evidence_ids":[1,1]}`, code: "invalid_evidence"},
		{name: "too_many", data: `{"summary":"要点","evidence_ids":[1,2,1,2]}`, code: "invalid_evidence"},
		{name: "fractional", data: `{"summary":"要点","evidence_ids":[1.5]}`, code: "invalid_response"},
		{name: "string_id", data: `{"summary":"要点","evidence_ids":["1"]}`, code: "invalid_response"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, code := decodeExternalLinkSummaryResponse([]byte(test.data), spans); code != test.code {
				t.Fatalf("decode code=%q, want %q", code, test.code)
			}
		})
	}
	decoded, code := decodeExternalLinkSummaryResponse([]byte(`{"summary":"要点","evidence_ids":[2,1]}`), spans)
	if code != "" || len(decoded.EvidenceQuotes) != 2 || decoded.EvidenceQuotes[0] != spans[1].Text || decoded.EvidenceQuotes[1] != spans[0].Text {
		t.Fatalf("valid IDs were not copied from exact spans: decoded=%+v code=%q", decoded, code)
	}
}

func TestExternalLinkSummaryCoversEveryUnicodeBodyChunkAndEnd(t *testing.T) {
	body := strings.Repeat("中間SOURCE ", 1450) + "終端SOURCE"
	provider := &summaryTestProvider{fn: func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		id := 1
		if strings.Contains(req.Messages[0].Content, "終端SOURCE") {
			id = sourceSpanIDContaining(t, req, "終端SOURCE")
		} else if strings.Contains(req.Messages[0].Content, "中間SOURCE") {
			id = sourceSpanIDContaining(t, req, "中間SOURCE")
		}
		return llm.GenerateResponse{Content: `{"summary":"本文の要点","evidence_ids":[` + strconv.Itoa(id) + `]}`}, nil
	}}
	store := &summaryLookupStore{}
	result, item, err := importSummaryRecord(t, context.Background(), store, provider, body)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}
	if provider.callCount() < 3 {
		t.Fatalf("expected body chunks and reduction, calls=%d", provider.callCount())
	}
	if result.LinkSummaries.Ready != 1 || result.LinkSummaries.Failed != 0 {
		t.Fatalf("counts = %+v", result.LinkSummaries)
	}
	if summary := referenceSummary(t, item); summary["status"] != "ready" || summary["chunks"] != 2 {
		t.Fatalf("summary = %#v", summary)
	}
	if quotes, ok := referenceSummary(t, item)["evidence_quotes"].([]string); !ok || len(quotes) == 0 || !strings.Contains(body, quotes[0]) {
		t.Fatalf("final evidence is not grounded in the original body: %#v", referenceSummary(t, item)["evidence_quotes"])
	}
	quotes, ok := referenceSummary(t, item)["evidence_quotes"].([]string)
	if !ok || len(quotes) < 2 || !strings.Contains(quotes[1], "終端SOURCE") {
		t.Fatalf("final evidence did not retain the original end span: %#v", referenceSummary(t, item)["evidence_quotes"])
	}
	allowedQuotes := make(map[string]struct{})
	for start := 0; start < len([]rune(body)); start += externalLinkSummaryChunkRunes {
		end := start + externalLinkSummaryChunkRunes
		if end > len([]rune(body)) {
			end = len([]rune(body))
		}
		for _, span := range externalLinkSummarySourceSpans(string([]rune(body)[start:end])) {
			allowedQuotes[span.Text] = struct{}{}
		}
	}
	for _, quote := range quotes {
		if _, exists := allowedQuotes[quote]; !exists {
			t.Fatalf("final evidence was not an exact original chunk span: %q", quote)
		}
	}
	if got := referenceSummary(t, item)["body_sha256"]; got != externalLinkBodySHAForText(body) {
		t.Fatalf("final body hash changed: got=%v want=%s", got, externalLinkBodySHAForText(body))
	}
	var sawBegin, sawEnd bool
	provider.mu.Lock()
	for _, req := range provider.requests {
		content := req.Messages[0].Content
		sawBegin = sawBegin || strings.Contains(content, "中間SOURCE")
		sawEnd = sawEnd || strings.Contains(content, "終端SOURCE")
	}
	provider.mu.Unlock()
	if !sawBegin || !sawEnd {
		t.Fatalf("body boundary was not presented to provider: begin=%v end=%v", sawBegin, sawEnd)
	}
}

func TestExternalLinkSummaryRejectsInventedEvidence(t *testing.T) {
	provider := &summaryTestProvider{fn: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
		return llm.GenerateResponse{Content: `{"summary":"本文の要点","evidence_ids":[999]}`}, nil
	}}
	store := &summaryLookupStore{}
	result, item, err := importSummaryRecord(t, context.Background(), store, provider, strings.Repeat("本文SOURCE ", 30))
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}
	if result.LinkSummaries.Failed != 1 {
		t.Fatalf("counts = %+v", result.LinkSummaries)
	}
	summary := referenceSummary(t, item)
	if summary["status"] != "error" || summary["error_code"] != "invalid_evidence" {
		t.Fatalf("summary = %#v", summary)
	}
	if provider.callCount() != 2 {
		t.Fatalf("invalid evidence was retried more or less than once: calls=%d", provider.callCount())
	}
}

func TestExternalLinkSummaryRepairsInvalidEvidenceAgainstOriginalInput(t *testing.T) {
	body := strings.Repeat("本文\\SOURCE、", 40)
	provider := &summaryTestProvider{}
	provider.fn = func(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
		switch providerCallCount := provider.callCount(); providerCallCount {
		case 1:
			return llm.GenerateResponse{Content: `{"summary":"本文の要点","evidence_ids":[999]}`}, nil
		case 2:
			if len(req.Messages) != 3 || req.Messages[0].Role != "user" {
				t.Fatalf("repair did not preserve the original input message: %+v", req.Messages)
			}
			spans := sourceSpansFromRequest(t, req)
			if len(spans) != 1 || spans[0].Text != body {
				t.Fatalf("repair did not preserve the original source spans: %+v", spans)
			}
			if req.Messages[1].Role != "assistant" || !strings.Contains(req.Messages[1].Content, "evidence_ids") {
				t.Fatalf("repair did not include the prior draft: %+v", req.Messages)
			}
			if req.Messages[2].Role != "user" || !strings.Contains(req.Messages[2].Content, "evidence_ids") {
				t.Fatalf("repair diagnostic is missing: %+v", req.Messages)
			}
			return llm.GenerateResponse{Content: `{"summary":"本文の要点","evidence_ids":[1]}`}, nil
		default:
			t.Fatalf("unexpected provider call %d", providerCallCount)
			return llm.GenerateResponse{}, nil
		}
	}
	store := &summaryLookupStore{}
	result, item, err := importSummaryRecord(t, context.Background(), store, provider, body)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}
	if provider.callCount() != 2 || result.LinkSummaries.Ready != 1 || result.LinkSummaries.Failed != 0 {
		t.Fatalf("repair counts/calls = %+v calls=%d", result.LinkSummaries, provider.callCount())
	}
	summary := referenceSummary(t, item)
	if summary["status"] != "ready" || summary["body_sha256"] != externalLinkBodySHAForText(body) {
		t.Fatalf("repaired summary = %#v", summary)
	}
	quotes, ok := summary["evidence_quotes"].([]string)
	if !ok || len(quotes) != 1 || quotes[0] != body || !strings.Contains(body, quotes[0]) {
		t.Fatalf("repaired evidence was not copied literally from the body: %#v", summary["evidence_quotes"])
	}
}

func TestExternalLinkSummaryRejectsEvidenceOnlyInRepairContext(t *testing.T) {
	for name, repairID := range map[string]int{
		"prior_draft":      999,
		"fixed_diagnostic": 998,
	} {
		t.Run(name, func(t *testing.T) {
			provider := &summaryTestProvider{}
			provider.fn = func(_ context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
				if provider.callCount() == 1 {
					return llm.GenerateResponse{Content: `{"summary":"本文の要点","evidence_ids":[999]}`}, nil
				}
				return llm.GenerateResponse{Content: `{"summary":"本文の要点","evidence_ids":[` + strconv.Itoa(repairID) + `]}`}, nil
			}
			store := &summaryLookupStore{}
			result, item, err := importSummaryRecord(t, context.Background(), store, provider, strings.Repeat("本文SOURCE ", 30))
			if err != nil {
				t.Fatalf("import failed: %v", err)
			}
			if provider.callCount() != 2 {
				t.Fatalf("repair call count = %d, want 2", provider.callCount())
			}
			if result.LinkSummaries.Failed != 1 {
				t.Fatalf("counts = %+v", result.LinkSummaries)
			}
			summary := referenceSummary(t, item)
			if summary["status"] != "error" || summary["error_code"] != "invalid_evidence" {
				t.Fatalf("context-only evidence was accepted: %#v", summary)
			}
		})
	}
}

func TestExternalLinkSummaryDoesNotRetryOtherInvalidResponses(t *testing.T) {
	for name, response := range map[string]string{
		"malformed_json": `not-json`,
		"empty_summary":  `{"summary":"","evidence_ids":[1]}`,
	} {
		t.Run(name, func(t *testing.T) {
			provider := &summaryTestProvider{fn: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
				return llm.GenerateResponse{Content: response}, nil
			}}
			store := &summaryLookupStore{}
			result, item, err := importSummaryRecord(t, context.Background(), store, provider, strings.Repeat("本文SOURCE ", 30))
			if err != nil {
				t.Fatalf("import failed: %v", err)
			}
			if provider.callCount() != 1 {
				t.Fatalf("non-evidence response was retried: calls=%d", provider.callCount())
			}
			if result.LinkSummaries.Failed != 1 || referenceSummary(t, item)["error_code"] != "invalid_response" {
				t.Fatalf("result = %+v summary = %#v", result.LinkSummaries, referenceSummary(t, item))
			}
		})
	}
}

func TestExternalLinkSummaryProviderErrorDoesNotStoreRawErrorAndPreservesCapture(t *testing.T) {
	provider := &summaryTestProvider{fn: func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
		return llm.GenerateResponse{}, errors.New("secret-provider-token")
	}}
	store := &summaryLookupStore{}
	body := strings.Repeat("本文SOURCE ", 30)
	result, item, err := importSummaryRecord(t, context.Background(), store, provider, body)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}
	if result.LinkSummaries.Failed != 1 {
		t.Fatalf("counts = %+v", result.LinkSummaries)
	}
	if provider.callCount() != 1 {
		t.Fatalf("provider error was retried: calls=%d", provider.callCount())
	}
	summary := referenceSummary(t, item)
	if summary["status"] != "error" || summary["error_code"] != "provider_error" {
		t.Fatalf("summary = %#v", summary)
	}
	encoded, _ := json.Marshal(item.Meta)
	if strings.Contains(string(encoded), "secret-provider-token") {
		t.Fatalf("raw provider error leaked into meta: %s", encoded)
	}
	refs := item.Meta["references"].([]interface{})
	ref := refs[0].(map[string]interface{})
	if ref["body_text"] != body || ref["page_description"] != "説明は要約入力に使わない" || item.RawText != "元のraw capture" {
		t.Fatalf("capture fields changed: item=%+v ref=%+v", item, ref)
	}
}

func TestExternalLinkSummaryRerunReusesReadySummaryAndPreservesIdentity(t *testing.T) {
	provider := &summaryTestProvider{}
	store := &summaryLookupStore{}
	_, firstItem, err := importSummaryRecord(t, context.Background(), store, provider, strings.Repeat("本文SOURCE ", 30))
	if err != nil {
		t.Fatalf("first import failed: %v", err)
	}
	firstSummary := referenceSummary(t, firstItem)
	firstCalls := provider.callCount()
	firstItem.ValidationStatus = l1sqlite.L1StagingStatusValidated
	store.items["kb:general\x00x:summary-test"] = firstItem
	second, secondItem, err := importSummaryRecord(t, context.Background(), store, provider, strings.Repeat("本文SOURCE ", 30))
	if err != nil {
		t.Fatalf("second import failed: %v", err)
	}
	if provider.callCount() != firstCalls || second.LinkSummaries.Reused != 1 || second.LinkSummaries.Ready != 1 {
		t.Fatalf("rerun calls=%d first=%d counts=%+v", provider.callCount(), firstCalls, second.LinkSummaries)
	}
	secondSummary := referenceSummary(t, secondItem)
	if secondSummary["body_sha256"] != firstSummary["body_sha256"] || secondSummary["generated_at"] != firstSummary["generated_at"] {
		t.Fatalf("reused summary changed: first=%#v second=%#v", firstSummary, secondSummary)
	}
	if !ValidExternalLinkSummary(secondItem.Meta["references"].([]interface{})[0].(map[string]interface{})) {
		t.Fatalf("generated summary does not satisfy owner validator: %#v", secondSummary)
	}
	if secondItem.ID != firstItem.ID || secondItem.ValidationStatus != l1sqlite.L1StagingStatusValidated {
		t.Fatalf("identity/state not preserved: first=%+v second=%+v", firstItem, secondItem)
	}
}

func TestExternalLinkSummaryPreservesCompleteCaptureOnFailedRefresh(t *testing.T) {
	provider := &summaryTestProvider{}
	store := &summaryLookupStore{}
	body := strings.Repeat("本文SOURCE ", 30)
	_, firstItem, err := importSummaryRecord(t, context.Background(), store, provider, body)
	if err != nil {
		t.Fatalf("first import failed: %v", err)
	}
	firstSummary := referenceSummary(t, firstItem)
	firstCalls := provider.callCount()
	firstItem.Meta["preserved_meta"] = "keep"
	firstItem.Meta["references"].([]interface{})[0].(map[string]interface{})["preserved_ref"] = "keep"
	store.items["kb:general\x00x:summary-test"] = firstItem

	input := externalSummaryRecord("", nil)
	var record map[string]interface{}
	if err := json.Unmarshal([]byte(input), &record); err != nil {
		t.Fatal(err)
	}
	ref := record["meta"].(map[string]interface{})["references"].([]interface{})[0].(map[string]interface{})
	ref["capture_status"] = "fetch_failed"
	ref["fetch_error"] = "empty_body"
	ref["page_description"] = "失敗更新の説明"
	encoded, _ := json.Marshal(record)
	result, err := ImportKnowledgeCoreJSONL(context.Background(), store, strings.NewReader(string(encoded)), ImportOptions{
		Now:            func() time.Time { return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) },
		LinkSummarizer: NewExternalLinkSummarizer(provider, "shiro"),
	})
	if err != nil {
		t.Fatalf("failed refresh import failed: %v", err)
	}
	if provider.callCount() != firstCalls || result.LinkSummaries.Reused != 1 {
		t.Fatalf("failed refresh regenerated or was not reused: calls=%d first=%d counts=%+v", provider.callCount(), firstCalls, result.LinkSummaries)
	}
	item := store.items["kb:general\x00x:summary-test"]
	refs := item.Meta["references"].([]interface{})
	ref = refs[0].(map[string]interface{})
	if ref["body_text"] != body || ref["capture_status"] != "content_fetched" || externalLinkString(ref, "fetch_error") != "" || ref["page_description"] != "説明は要約入力に使わない" {
		t.Fatalf("complete capture was overwritten by failed refresh: %#v", ref)
	}
	if ref["preserved_ref"] != "keep" || item.Meta["preserved_meta"] != "keep" {
		t.Fatalf("unrelated existing metadata was overwritten: item=%#v ref=%#v", item.Meta, ref)
	}
	if summary := referenceSummary(t, item); summary["body_sha256"] != firstSummary["body_sha256"] || summary["generated_at"] != firstSummary["generated_at"] {
		t.Fatalf("ready summary was overwritten by failed refresh: %#v", summary)
	}
}

func TestExternalLinkSummaryBodyChangeRegenerates(t *testing.T) {
	provider := &summaryTestProvider{}
	store := &summaryLookupStore{}
	_, firstItem, err := importSummaryRecord(t, context.Background(), store, provider, strings.Repeat("本文SOURCE ", 30))
	if err != nil {
		t.Fatalf("first import failed: %v", err)
	}
	firstHash := referenceSummary(t, firstItem)["body_sha256"]
	firstCalls := provider.callCount()
	_, secondItem, err := importSummaryRecord(t, context.Background(), store, provider, strings.Repeat("変更後SOURCE ", 30))
	if err != nil {
		t.Fatalf("second import failed: %v", err)
	}
	if provider.callCount() <= firstCalls {
		t.Fatalf("body change did not generate: calls=%d first=%d", provider.callCount(), firstCalls)
	}
	if got := referenceSummary(t, secondItem)["body_sha256"]; got == firstHash {
		t.Fatalf("body hash was not changed: %v", got)
	}
}

func TestExternalLinkSummaryInvalidPersistedSummaryRegenerates(t *testing.T) {
	for name, mutate := range map[string]func(map[string]interface{}){
		"missing_provenance": func(summary map[string]interface{}) { delete(summary, "provenance") },
		"invalid_evidence":   func(summary map[string]interface{}) { summary["evidence_quotes"] = []string{"not in body"} },
		"old_revision":       func(summary map[string]interface{}) { summary["revision"] = "x-link-summary-v1" },
	} {
		t.Run(name, func(t *testing.T) {
			provider := &summaryTestProvider{}
			store := &summaryLookupStore{}
			body := strings.Repeat("本文SOURCE ", 30)
			_, item, err := importSummaryRecord(t, context.Background(), store, provider, body)
			if err != nil {
				t.Fatalf("first import failed: %v", err)
			}
			refs := item.Meta["references"].([]interface{})
			mutate(refs[0].(map[string]interface{})["summary"].(map[string]interface{}))
			store.items["kb:general\x00x:summary-test"] = item
			calls := provider.callCount()
			result, regenerated, err := importSummaryRecord(t, context.Background(), store, provider, body)
			if err != nil {
				t.Fatalf("regeneration failed: %v", err)
			}
			if provider.callCount() <= calls || result.LinkSummaries.Reused != 0 || result.LinkSummaries.Ready != 1 {
				t.Fatalf("invalid persisted summary was reused: calls=%d before=%d counts=%+v", provider.callCount(), calls, result.LinkSummaries)
			}
			if !ValidExternalLinkSummary(regenerated.Meta["references"].([]interface{})[0].(map[string]interface{})) {
				t.Fatalf("regenerated summary is invalid: %#v", referenceSummary(t, regenerated))
			}
		})
	}
}

func TestExternalLinkSummaryCancellationStopsFurtherCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &summaryTestProvider{fn: func(ctx context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
		cancel()
		<-ctx.Done()
		return llm.GenerateResponse{}, ctx.Err()
	}}
	store := &summaryLookupStore{}
	result, err := ImportKnowledgeCoreJSONL(ctx, store, strings.NewReader(externalSummaryRecord(strings.Repeat("本文SOURCE ", 1000), nil)), ImportOptions{
		LinkSummarizer: NewExternalLinkSummarizer(provider, "shiro"),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, result=%+v err=%v", result, err)
	}
	if provider.callCount() != 1 {
		t.Fatalf("cancellation caused repeated calls: %d", provider.callCount())
	}
}

func TestImportWithoutLinkSummarizerPreservesExistingExternalReference(t *testing.T) {
	store := &summaryLookupStore{}
	body := strings.Repeat("本文SOURCE ", 30)
	claimed := map[string]interface{}{
		"status":          "ready",
		"text":            "自己申告サマリ",
		"body_sha256":     externalLinkBodySHAForText(body),
		"revision":        ExternalLinkSummaryRevision,
		"provenance":      ExternalLinkSummaryProvenance,
		"generated_at":    "2026-09-14T12:00:00Z",
		"chunks":          1,
		"error_code":      "",
		"evidence_quotes": []string{"SOURCE"},
	}
	input := externalSummaryRecord(body, map[string]interface{}{"unrelated": "keep"})
	var record map[string]interface{}
	if err := json.Unmarshal([]byte(input), &record); err != nil {
		t.Fatal(err)
	}
	references := record["meta"].(map[string]interface{})["references"].([]interface{})
	references[0].(map[string]interface{})["summary"] = claimed
	encoded, _ := json.Marshal(record)
	if _, err := ImportKnowledgeCoreJSONL(context.Background(), store, strings.NewReader(string(encoded)), ImportOptions{}); err != nil {
		t.Fatalf("import failed: %v", err)
	}
	item := store.items["kb:general\x00x:summary-test"]
	ref := item.Meta["references"].([]interface{})[0].(map[string]interface{})
	if _, exists := ref["summary"]; exists {
		t.Fatalf("incoming external summary was trusted: ref=%+v", ref)
	}
	if ref["body_text"] != body || ref["page_description"] != "説明は要約入力に使わない" || item.RawText != "元のraw capture" || item.Meta["unrelated"] != "keep" {
		t.Fatalf("non-summary import changed source/classification metadata: item=%+v", item)
	}
}

func TestMergeExternalLinkReferencesKeepsReorderedXPostFields(t *testing.T) {
	existing := []map[string]interface{}{
		{"kind": "x_post", "tweet_id": "tweet-1", "status_url": "https://x/1", "old_field": "one"},
		{"kind": "x_post", "tweet_id": "tweet-2", "status_url": "https://x/2", "old_field": "two"},
	}
	incoming := []map[string]interface{}{
		{"kind": "x_post", "tweet_id": "tweet-2", "status_url": "https://x/2", "new_field": "new-two"},
		{"kind": "x_post", "tweet_id": "tweet-1", "status_url": "https://x/1", "new_field": "new-one"},
	}
	merged := mergeExternalLinkReferences(existing, incoming)
	if len(merged) != 2 || merged[0]["old_field"] != "two" || merged[1]["old_field"] != "one" {
		t.Fatalf("reordered x_post fields crossed: %#v", merged)
	}
	if merged[0]["new_field"] != "new-two" || merged[1]["new_field"] != "new-one" {
		t.Fatalf("incoming x_post fields were not retained: %#v", merged)
	}
}
