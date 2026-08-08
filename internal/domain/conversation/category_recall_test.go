package conversation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type categoryRecallTestSource struct {
	id         string
	categories []string
	result     CategoryRecallResult
	err        error
	queries    []CategoryRecallQuery
}

func (s *categoryRecallTestSource) ID() string { return s.id }

func (s *categoryRecallTestSource) Categories() []string {
	return append([]string(nil), s.categories...)
}

func (s *categoryRecallTestSource) Search(_ context.Context, query CategoryRecallQuery) (CategoryRecallResult, error) {
	s.queries = append(s.queries, query)
	return s.result, s.err
}

func TestCategoryRecallRegistry_SelectsOnlyRelevantSources(t *testing.T) {
	movie := &categoryRecallTestSource{id: "movie", categories: []string{"movie"}}
	hobby := &categoryRecallTestSource{id: "hobby", categories: []string{"hobby"}}
	news := &categoryRecallTestSource{id: "news", categories: []string{"news"}}
	registry := NewCategoryRecallRegistry(movie, hobby, news)
	registry.SetMarkers(map[string][]string{
		"movie": {"映画"},
		"hobby": {"趣味"},
		"news":  {"ニュース"},
	})

	result, err := registry.Recall(context.Background(), CategoryRecallQuery{Message: "映画の話をしよう", Limit: 3})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if len(movie.queries) != 1 || len(hobby.queries) != 0 || len(news.queries) != 0 {
		t.Fatalf("source selection movie=%d hobby=%d news=%d", len(movie.queries), len(hobby.queries), len(news.queries))
	}
	if len(result.SelectedCategories) != 1 || result.SelectedCategories[0] != "movie" {
		t.Fatalf("selected categories=%v", result.SelectedCategories)
	}
}

func TestCategoryRecallRegistry_UsesActiveDomainAndStartupEntityHints(t *testing.T) {
	movie := &categoryRecallTestSource{id: "movie", categories: []string{"movie"}}
	hobby := &categoryRecallTestSource{id: "hobby", categories: []string{"hobby"}}
	registry := NewCategoryRecallRegistry(movie, hobby)
	registry.SetMarkers(map[string][]string{"movie": {"映画"}, "hobby": {"趣味"}})
	registry.SetEntityHints(map[string][]string{"movie": {"マトリックス"}, "hobby": {"陶芸"}})

	if _, err := registry.Recall(context.Background(), CategoryRecallQuery{Message: "関連するもの", ActiveDomain: "hobby", Limit: 2}); err != nil {
		t.Fatalf("active-domain Recall failed: %v", err)
	}
	if len(movie.queries) != 0 || len(hobby.queries) != 1 {
		t.Fatalf("active-domain selection movie=%d hobby=%d", len(movie.queries), len(hobby.queries))
	}

	if _, err := registry.Recall(context.Background(), CategoryRecallQuery{Message: "マトリックスの話", Limit: 2}); err != nil {
		t.Fatalf("entity-hint Recall failed: %v", err)
	}
	if len(movie.queries) != 1 || len(hobby.queries) != 1 {
		t.Fatalf("entity-hint selection movie=%d hobby=%d", len(movie.queries), len(hobby.queries))
	}
}

func TestCategoryRecallRegistry_DoesNotSearchOnUnrelatedMessage(t *testing.T) {
	source := &categoryRecallTestSource{id: "movie", categories: []string{"movie"}}
	registry := NewCategoryRecallRegistry(source)
	registry.SetMarkers(map[string][]string{"movie": {"映画"}})

	if _, err := registry.Recall(context.Background(), CategoryRecallQuery{Message: "今日はいい天気だね", Limit: 2}); err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if len(source.queries) != 0 {
		t.Fatalf("unrelated source was searched: %#v", source.queries)
	}
}

func TestCategoryRecallRegistry_DoesNotTreatCommonBookCharacterAsMarker(t *testing.T) {
	book := &categoryRecallTestSource{id: "book", categories: []string{"book"}}
	registry := NewCategoryRecallRegistry(book)

	if _, err := registry.Recall(context.Background(), CategoryRecallQuery{Message: "本当に今日はいい天気だね", Limit: 2}); err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if len(book.queries) != 0 {
		t.Fatalf("common 本 character selected book source: %#v", book.queries)
	}
}

func TestCategoryRecallRegistry_ReturnsPartialFailureAndRejectsInvalidRecords(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	source := &categoryRecallTestSource{
		id:         "movie",
		categories: []string{"movie"},
		result: CategoryRecallResult{Records: []CategoryRecallRecord{
			{Category: "movie", SourceID: "movie", RecordID: "ok", Title: "Valid", Summary: "valid", ProvenanceURLs: []string{"https://example.test/movie/ok"}, State: CategoryRecordStateValidated, RetrievedAt: now, ValidatedAt: now},
			{Category: "movie", SourceID: "movie", RecordID: "stale", Title: "Stale", Summary: "stale", ProvenanceURLs: []string{"https://example.test/movie/stale"}, State: CategoryRecordStateValidated, RetrievedAt: now.Add(-48 * time.Hour), ValidatedAt: now.Add(-48 * time.Hour), FreshUntil: now.Add(-time.Hour)},
		}},
		err: errors.New("catalog unavailable"),
	}
	registry := NewCategoryRecallRegistry(source)
	registry.SetMarkers(map[string][]string{"movie": {"映画"}})
	registry.SetNow(func() time.Time { return now })

	result, err := registry.Recall(context.Background(), CategoryRecallQuery{Message: "映画", Time: now, Limit: 3})
	if err != nil {
		t.Fatalf("Recall should degrade to result: %v", err)
	}
	if result.Status != CategoryRecallStatusPartial || len(result.Records) != 1 || result.Records[0].RecordID != "ok" {
		t.Fatalf("unexpected partial result: %#v", result)
	}
	if len(result.Failures) != 2 || result.Failures[1].Code != CategoryRecallFailureSourceUnavailable {
		t.Fatalf("unexpected failures: %#v", result.Failures)
	}

	source.err = nil
	result, err = registry.Recall(context.Background(), CategoryRecallQuery{Message: "映画", Time: now, Limit: 3})
	if err != nil {
		t.Fatalf("Recall with records failed: %v", err)
	}
	if result.Status != CategoryRecallStatusPartial || len(result.Records) != 1 || result.Records[0].RecordID != "ok" {
		t.Fatalf("unexpected partial result: %#v", result)
	}
	if len(result.Failures) != 1 || result.Failures[0].Code != CategoryRecallFailureStale {
		t.Fatalf("stale record should be traced: %#v", result.Failures)
	}
}

func TestCategoryRecallRegistryRejectsPrivateRecordWithoutAuthorizedScope(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	source := &categoryRecallTestSource{
		id: "movie", categories: []string{"movie"},
		result: CategoryRecallResult{Records: []CategoryRecallRecord{{
			Category: "movie", SourceID: "movie", RecordID: "private", Title: "Private", Summary: "private summary",
			ProvenanceURLs: []string{"https://example.test/private"}, RetrievedAt: now, ValidatedAt: now,
			State: CategoryRecordStateValidated, Scope: "ren", Roles: []string{"chat"},
		}}},
	}
	registry := NewCategoryRecallRegistry(source).SetMarkers(map[string][]string{"movie": {"映画"}}).SetNow(func() time.Time { return now })
	result, err := registry.Recall(context.Background(), CategoryRecallQuery{Message: "映画", Time: now, Limit: 3})
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if len(result.Records) != 0 || len(result.Failures) != 1 || result.Failures[0].Code != CategoryRecallFailureScopeDenied {
		t.Fatalf("private record should be denied without scope: %#v", result)
	}

	result, err = registry.Recall(context.Background(), CategoryRecallQuery{Message: "映画", UserScope: "ren", Time: now, Limit: 3})
	if err != nil || len(result.Records) != 1 {
		t.Fatalf("authorized scope should accept private record: result=%#v err=%v", result, err)
	}
}

func TestRecallPack_CategorySnippetsFlowThroughPromptBudgetRoleAndTrace(t *testing.T) {
	now := time.Now().UTC()
	rp := &RecallPack{CategorySnippets: []CategorySnippet{
		{Category: "movie", SourceID: "movie", RecordID: "m1", Title: "映画A", Summary: "作品概要", ProvenanceURLs: []string{"https://example.test/m1"}, State: CategoryRecordStateValidated, RetrievedAt: now, ValidatedAt: now, Roles: []string{"chat", "worker", "heavy", "creative"}},
		{Category: "hobby", SourceID: "hobby", RecordID: "h1", Title: "趣味B", Summary: strings.Repeat("長い概要 ", 50), ProvenanceURLs: []string{"https://example.test/h1"}, State: CategoryRecordStateValidated, RetrievedAt: now, ValidatedAt: now, Roles: []string{"chat", "worker", "heavy", "creative"}},
	}}
	if !rp.HasContext() {
		t.Fatal("CategorySnippets should count as context")
	}
	messages := rp.ToPromptMessages()
	if len(messages) != 1 || !strings.Contains(messages[0].Content, "映画A") || !strings.Contains(messages[0].Content, "https://example.test/m1") {
		t.Fatalf("category prompt missing: %#v", messages)
	}
	trimmed := rp.ApplyRecallBudgetWithEstimator(20, 0.5, TokenEstimatorFunc(func(text string) int {
		if strings.Contains(text, "映画A") {
			return 2
		}
		return 100
	}))
	if len(trimmed.CategorySnippets) != 1 || trimmed.CategorySnippets[0].RecordID != "m1" {
		t.Fatalf("category budget result=%#v", trimmed.CategorySnippets)
	}
	if len(trimmed.RejectedTraceItems) != 1 || trimmed.RejectedTraceItems[0].Status != TraceStatusBudgetDropped {
		t.Fatalf("category budget trace=%#v", trimmed.RejectedTraceItems)
	}
	if len(trimmed.ToTraceItems()) != 2 {
		t.Fatalf("category trace items=%#v", trimmed.ToTraceItems())
	}
	for _, role := range []string{"chat", "worker", "heavy", "creative"} {
		if got := len(rp.FilterForRole(role).CategorySnippets); got != 2 {
			t.Fatalf("role %s category count=%d", role, got)
		}
	}
	for _, role := range []string{"coder", "wild", "ops"} {
		filtered := rp.FilterForRole(role)
		if len(filtered.CategorySnippets) != 0 {
			t.Fatalf("role %s must not receive category snippets: %#v", role, filtered.CategorySnippets)
		}
	}
}

func TestCategoryRecallPolicyLeavesRegistryFreshnessDecisionStable(t *testing.T) {
	policy := NewInjectionPolicy("chat")
	base := RecallCandidate{Kind: "category_snippet", State: CategoryRecordStateValidated, Scope: "public", Roles: []string{"chat"}, RetrievedAt: time.Now().UTC(), ValidatedAt: time.Now().UTC()}
	if got := policy.Decide(base); got.Status != TraceStatusFilteredStatus || got.Reason != CategoryRecallFailureMissingProvenance {
		t.Fatalf("missing provenance decision=%#v", got)
	}
	base.ProvenanceURLs = []string{"https://example.test/source"}
	base.FreshUntil = time.Now().UTC().Add(-time.Hour)
	if got := policy.Decide(base); got.Status != TraceStatusInjected {
		t.Fatalf("role policy must not re-evaluate request-time freshness=%#v", got)
	}
	base.State = "invalid"
	if got := policy.Decide(base); got.Status != TraceStatusFilteredStatus || got.Reason != CategoryRecallFailureInvalid {
		t.Fatalf("invalid decision=%#v", got)
	}

	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	source := &categoryRecallTestSource{id: "movie", categories: []string{"movie"}, result: CategoryRecallResult{Records: []CategoryRecallRecord{{
		Category: "movie", SourceID: "movie", RecordID: "stale", Title: "Stale", Summary: "stale summary",
		ProvenanceURLs: []string{"https://example.test/stale"}, RetrievedAt: now, ValidatedAt: now,
		FreshUntil: now.Add(-time.Hour), State: CategoryRecordStateValidated, Roles: []string{"chat"},
	}}}}
	registry := NewCategoryRecallRegistry(source).SetMarkers(map[string][]string{"movie": {"映画"}}).SetNow(func() time.Time { return now })
	result, err := registry.Recall(context.Background(), CategoryRecallQuery{Message: "映画", Time: now, Limit: 3})
	if err != nil || len(result.Records) != 0 || len(result.Failures) != 1 || result.Failures[0].Code != CategoryRecallFailureStale {
		t.Fatalf("registry should own stale decision: result=%#v err=%v", result, err)
	}
}
