package conversation

import (
	"strings"
	"testing"
	"time"
)

func TestRecallPack_HasContext_Empty(t *testing.T) {
	rp := &RecallPack{}
	if rp.HasContext() {
		t.Error("empty RecallPack should not have context")
	}
}

func TestRecallPack_HasContext_WithShortContext(t *testing.T) {
	rp := &RecallPack{
		ShortContext: []Message{{Speaker: SpeakerUser, Msg: "hello"}},
	}
	if !rp.HasContext() {
		t.Error("RecallPack with ShortContext should have context")
	}
}

func TestRecallPack_HasContext_WithMidSummaries(t *testing.T) {
	rp := &RecallPack{
		MidSummaries: []ThreadSummary{{Summary: "test"}},
	}
	if !rp.HasContext() {
		t.Error("RecallPack with MidSummaries should have context")
	}
}

func TestRecallPack_HasContext_WithLongFacts(t *testing.T) {
	rp := &RecallPack{
		LongFacts: []string{"fact1"},
	}
	if !rp.HasContext() {
		t.Error("RecallPack with LongFacts should have context")
	}
}

func TestRecallPack_HasContext_WithKBSnippets(t *testing.T) {
	rp := &RecallPack{
		KBSnippets: []string{"snippet1"},
	}
	if !rp.HasContext() {
		t.Error("RecallPack with KBSnippets should have context")
	}
}

func TestRecallPack_HasContext_WithSearchCacheSnippets(t *testing.T) {
	rp := &RecallPack{
		SearchCacheSnippets: []SearchCacheSnippet{{Query: "RenCrow"}},
	}
	if !rp.HasContext() {
		t.Error("RecallPack with SearchCacheSnippets should have context")
	}
}

func TestRecallPack_HasContext_WithWikiSnippets(t *testing.T) {
	rp := &RecallPack{
		WikiSnippets: []WikiSnippet{{PageID: "concept:recall-pack", Title: "RecallPack"}},
	}
	if !rp.HasContext() {
		t.Error("RecallPack with WikiSnippets should have context")
	}
}

func TestRecallPack_ToPromptMessages_Empty(t *testing.T) {
	rp := &RecallPack{}
	msgs := rp.ToPromptMessages()
	if len(msgs) != 0 {
		t.Errorf("empty RecallPack should produce 0 messages, got %d", len(msgs))
	}
}

func TestRecallPack_ToPromptMessages_PersonaOnly(t *testing.T) {
	rp := &RecallPack{
		Persona: PersonaState{SystemPrompt: "You are Mio."},
	}
	msgs := rp.ToPromptMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message (system prompt), got %d", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("expected role 'system', got %q", msgs[0].Role)
	}
	if msgs[0].Content != "You are Mio." {
		t.Errorf("expected content 'You are Mio.', got %q", msgs[0].Content)
	}
}

func TestRecallPack_ToPromptMessages_WithUserProfile(t *testing.T) {
	rp := &RecallPack{
		Persona: PersonaState{SystemPrompt: "You are Mio."},
		UserProfile: UserProfile{
			Preferences: map[string]string{"lang": "Go"},
			Facts:       []string{},
		},
	}
	msgs := rp.ToPromptMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 system message, got %d", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("expected role 'system', got %q", msgs[0].Role)
	}
	// SystemPrompt + UserProfile
	if !contains(msgs[0].Content, "You are Mio.") {
		t.Error("system prompt should contain persona")
	}
	if !contains(msgs[0].Content, "lang: Go") {
		t.Error("system prompt should contain user profile preferences")
	}
}

func TestRecallPack_ToPromptMessages_WithMidSummaries(t *testing.T) {
	rp := &RecallPack{
		MidSummaries: []ThreadSummary{
			{Summary: "Discussed Go testing"},
			{Summary: "Talked about CI/CD"},
		},
	}
	msgs := rp.ToPromptMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 context message, got %d", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("expected role 'system', got %q", msgs[0].Role)
	}
	if !contains(msgs[0].Content, "Discussed Go testing") {
		t.Error("context should contain mid summary 1")
	}
	if !contains(msgs[0].Content, "Talked about CI/CD") {
		t.Error("context should contain mid summary 2")
	}
}

func TestRecallPack_ToPromptMessages_WithLongFacts(t *testing.T) {
	rp := &RecallPack{
		LongFacts: []string{"User prefers Go", "User works at startup"},
	}
	msgs := rp.ToPromptMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 context message, got %d", len(msgs))
	}
	if !contains(msgs[0].Content, "User prefers Go") {
		t.Error("context should contain long fact 1")
	}
	if !contains(msgs[0].Content, "User works at startup") {
		t.Error("context should contain long fact 2")
	}
}

func TestRecallPack_ToPromptMessages_WithKBSnippets(t *testing.T) {
	rp := &RecallPack{
		KBSnippets: []string{"Go is a statically typed language"},
	}
	msgs := rp.ToPromptMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 context message, got %d", len(msgs))
	}
	if !contains(msgs[0].Content, "参考知識") {
		t.Error("context should contain KB header")
	}
	if !contains(msgs[0].Content, "Go is a statically typed language") {
		t.Error("context should contain KB snippet")
	}
}

func TestRecallPack_ToPromptMessages_WithWikiSnippets(t *testing.T) {
	rp := &RecallPack{
		WikiSnippets: []WikiSnippet{{
			PageID:      "concept:recall-pack",
			Title:       "RecallPack",
			Path:        "docs/wiki/concepts/recall-pack.md",
			Summary:     "RecallPack は選別済み文脈。",
			SourcePaths: []string{"internal/domain/conversation/recall_pack.go"},
			Related:     []string{"docs/wiki/concepts/memory-lifecycle.md"},
			UpdatedAt:   time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC),
		}},
	}
	msgs := rp.ToPromptMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 context message, got %d", len(msgs))
	}
	if !contains(msgs[0].Content, "Knowledge Wiki") {
		t.Error("context should contain wiki header")
	}
	if !contains(msgs[0].Content, "docs/wiki/concepts/recall-pack.md") {
		t.Error("context should contain wiki path")
	}
	if !contains(msgs[0].Content, "internal/domain/conversation/recall_pack.go") {
		t.Error("context should contain wiki source")
	}
}

func TestRecallPack_ToPromptMessages_WithSearchCacheSnippets(t *testing.T) {
	rp := &RecallPack{
		SearchCacheSnippets: []SearchCacheSnippet{
			{
				Query:       "RenCrow 最新仕様",
				Provider:    "web",
				ResultsJSON: `[{"title":"RenCrow memo"}]`,
				SourceURLs:  []string{"https://example.com/rencrow"},
			},
		},
	}
	msgs := rp.ToPromptMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 context message, got %d", len(msgs))
	}
	if !contains(msgs[0].Content, "検索キャッシュ") {
		t.Error("context should contain search cache header")
	}
	if !contains(msgs[0].Content, "RenCrow 最新仕様") {
		t.Error("context should contain cached query")
	}
	if !contains(msgs[0].Content, "https://example.com/rencrow") {
		t.Error("context should contain cached source URL")
	}
}

func TestRecallPack_ToPromptMessages_WithRelationSnippets(t *testing.T) {
	rp := &RecallPack{
		RelationSnippets: []RelationSnippet{{
			ItemID:       "github-mlx",
			Title:        "mlx-lm",
			Summary:      "MLX local LLM implementation",
			SourceType:   "github",
			RelationType: "same_entity",
			Score:        3,
			Evidence:     "same entity: MLX",
			Hop:          1,
		}},
	}
	msgs := rp.ToPromptMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 context message, got %d", len(msgs))
	}
	if !contains(msgs[0].Content, "Knowledge Relation") {
		t.Error("context should contain relation header")
	}
	if !contains(msgs[0].Content, "same_entity") || !contains(msgs[0].Content, "github-mlx") {
		t.Fatalf("context should contain relation detail: %s", msgs[0].Content)
	}
}

func TestRecallPack_ToPromptMessages_ShortContextRoles(t *testing.T) {
	rp := &RecallPack{
		ShortContext: []Message{
			{Speaker: SpeakerUser, Msg: "hello"},
			{Speaker: SpeakerMio, Msg: "hi there"},
			{Speaker: SpeakerSystem, Msg: "tool result"},
		},
	}
	msgs := rp.ToPromptMessages()
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	expected := []struct {
		role    string
		content string
	}{
		{"user", "hello"},
		{"assistant", "hi there"},
		{"system", "tool result"},
	}
	for i, e := range expected {
		if msgs[i].Role != e.role {
			t.Errorf("msg[%d] role: want %q, got %q", i, e.role, msgs[i].Role)
		}
		if msgs[i].Content != e.content {
			t.Errorf("msg[%d] content: want %q, got %q", i, e.content, msgs[i].Content)
		}
	}
}

func TestRecallPack_ToPromptMessages_FullPack(t *testing.T) {
	rp := &RecallPack{
		Persona:      PersonaState{SystemPrompt: "You are Mio."},
		UserProfile:  UserProfile{Preferences: map[string]string{"theme": "dark"}, Facts: []string{}},
		MidSummaries: []ThreadSummary{{Summary: "Past topic"}},
		LongFacts:    []string{"Long fact"},
		KBSnippets:   []string{"KB info"},
		SearchCacheSnippets: []SearchCacheSnippet{
			{Query: "cached topic", ResultsJSON: `[]`},
		},
		ShortContext: []Message{
			{Speaker: SpeakerUser, Msg: "recent msg"},
		},
	}
	msgs := rp.ToPromptMessages()
	// Expected: system(persona+profile), system(context), user(shortcontext)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	// First: persona system prompt
	if msgs[0].Role != "system" {
		t.Errorf("msg[0] role: want 'system', got %q", msgs[0].Role)
	}
	if !contains(msgs[0].Content, "You are Mio.") {
		t.Error("msg[0] should contain persona")
	}
	if !contains(msgs[0].Content, "theme: dark") {
		t.Error("msg[0] should contain user profile")
	}
	// Second: context block
	if msgs[1].Role != "system" {
		t.Errorf("msg[1] role: want 'system', got %q", msgs[1].Role)
	}
	if !contains(msgs[1].Content, "Past topic") {
		t.Error("msg[1] should contain mid summary")
	}
	if !contains(msgs[1].Content, "Long fact") {
		t.Error("msg[1] should contain long fact")
	}
	if !contains(msgs[1].Content, "KB info") {
		t.Error("msg[1] should contain KB snippet")
	}
	if !contains(msgs[1].Content, "cached topic") {
		t.Error("msg[1] should contain search cache snippet")
	}
	// Third: short context
	if msgs[2].Role != "user" {
		t.Errorf("msg[2] role: want 'user', got %q", msgs[2].Role)
	}
	if msgs[2].Content != "recent msg" {
		t.Errorf("msg[2] content: want 'recent msg', got %q", msgs[2].Content)
	}
}

func TestRecallPack_ToPromptMessages_MidAndLongMergedInSameBlock(t *testing.T) {
	rp := &RecallPack{
		MidSummaries: []ThreadSummary{{Summary: "mid1"}},
		LongFacts:    []string{"long1"},
	}
	msgs := rp.ToPromptMessages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 context message, got %d", len(msgs))
	}
	// Both mid summaries and long facts under same header
	if !contains(msgs[0].Content, "過去の会話から思い出したこと") {
		t.Error("should contain recall header")
	}
	if !contains(msgs[0].Content, "L2 中期記憶") {
		t.Error("should contain L2 layer label")
	}
	if !contains(msgs[0].Content, "L3 長期記憶") {
		t.Error("should contain L3 layer label")
	}
	if !contains(msgs[0].Content, "mid1") {
		t.Error("should contain mid summary")
	}
	if !contains(msgs[0].Content, "long1") {
		t.Error("should contain long fact")
	}
}

func TestDefaultConstraints(t *testing.T) {
	c := DefaultConstraints()
	if c.MaxTotalTokens != 8192 {
		t.Errorf("MaxTotalTokens: want 8192, got %d", c.MaxTotalTokens)
	}
	if c.MaxPromptTokens != 4000 {
		t.Errorf("MaxPromptTokens: want 4000, got %d", c.MaxPromptTokens)
	}
	if c.MaxResponseTokens != 512 {
		t.Errorf("MaxResponseTokens: want 512, got %d", c.MaxResponseTokens)
	}
	if c.RecallBudgetRatio != 0.10 {
		t.Errorf("RecallBudgetRatio: want 0.10, got %f", c.RecallBudgetRatio)
	}
}

func TestRecallPack_ApplyRecallBudgetTrimsRecallSections(t *testing.T) {
	rp := &RecallPack{
		ShortContext: []Message{{Speaker: SpeakerUser, Msg: "short context is preserved"}},
		MidSummaries: []ThreadSummary{
			{Summary: "small mid"},
			{Summary: strings.Repeat("large mid ", 80)},
		},
		LongFacts:  []string{"small long", strings.Repeat("large long ", 80)},
		KBSnippets: []string{"small kb", strings.Repeat("large kb ", 80)},
		WikiSnippets: []WikiSnippet{
			{Title: "small wiki", Path: "docs/wiki/concepts/recall-pack.md", Summary: "small wiki"},
			{Title: "large wiki", Path: "docs/wiki/concepts/large.md", Summary: strings.Repeat("large wiki ", 80)},
		},
		SearchCacheSnippets: []SearchCacheSnippet{
			{Query: "small search", ResultsJSON: `[]`},
			{Query: "large search", ResultsJSON: strings.Repeat("x", 500)},
		},
	}

	trimmed := rp.ApplyRecallBudget(200, 0.20)
	if len(trimmed.ShortContext) != 1 {
		t.Fatalf("ShortContext should be preserved, got %d", len(trimmed.ShortContext))
	}
	if len(trimmed.MidSummaries) != 1 || trimmed.MidSummaries[0].Summary != "small mid" {
		t.Fatalf("unexpected mid summaries after budget: %+v", trimmed.MidSummaries)
	}
	if len(trimmed.LongFacts) != 1 || trimmed.LongFacts[0] != "small long" {
		t.Fatalf("unexpected long facts after budget: %+v", trimmed.LongFacts)
	}
	if len(trimmed.KBSnippets) > 1 {
		t.Fatalf("budget should trim large KB snippets: %+v", trimmed.KBSnippets)
	}
	if len(trimmed.WikiSnippets) > 1 {
		t.Fatalf("budget should trim large wiki snippets: %+v", trimmed.WikiSnippets)
	}
	if len(trimmed.SearchCacheSnippets) > 1 {
		t.Fatalf("budget should trim large search snippets: %+v", trimmed.SearchCacheSnippets)
	}
	var budgetDropped int
	for _, item := range trimmed.RejectedTraceItems {
		if item.Status == TraceStatusBudgetDropped {
			budgetDropped++
		}
	}
	if budgetDropped == 0 {
		t.Fatalf("budget dropped candidates should be retained as rejected trace items: %+v", trimmed.RejectedTraceItems)
	}
}

func TestRecallPack_ApplyRecallBudgetNoopsWithoutBudget(t *testing.T) {
	rp := &RecallPack{
		MidSummaries: []ThreadSummary{{Summary: "mid"}},
		LongFacts:    []string{"long"},
		KBSnippets:   []string{"kb"},
		WikiSnippets: []WikiSnippet{{Title: "wiki", Summary: "wiki"}},
	}
	trimmed := rp.ApplyRecallBudget(0, 0.10)
	if len(trimmed.MidSummaries) != 1 || len(trimmed.LongFacts) != 1 || len(trimmed.KBSnippets) != 1 || len(trimmed.WikiSnippets) != 1 {
		t.Fatalf("budget should no-op without max context: %+v", trimmed)
	}
}

func TestRecallPack_ApplyRecallBudgetWithTokenEstimator(t *testing.T) {
	rp := &RecallPack{
		MidSummaries: []ThreadSummary{
			{Summary: "one"},
			{Summary: "two"},
			{Summary: "three"},
		},
	}
	estimator := TokenEstimatorFunc(func(text string) int {
		switch text {
		case "one":
			return 4
		case "two":
			return 4
		case "three":
			return 20
		default:
			return 1
		}
	})

	trimmed := rp.ApplyRecallBudgetWithEstimator(100, 0.10, estimator)
	if len(trimmed.MidSummaries) != 2 {
		t.Fatalf("expected precise estimator to keep first two summaries, got %+v", trimmed.MidSummaries)
	}
	if trimmed.MidSummaries[0].Summary != "one" || trimmed.MidSummaries[1].Summary != "two" {
		t.Fatalf("unexpected budget order: %+v", trimmed.MidSummaries)
	}
}

func TestRecallPack_FilterForRole(t *testing.T) {
	rp := &RecallPack{
		MidSummaries: []ThreadSummary{
			{Summary: "chat only", Roles: []string{"chat"}},
			{Summary: "worker only", Roles: []string{"worker"}},
			{Summary: "shared"},
		},
		SearchCacheSnippets: []SearchCacheSnippet{
			{Query: "wild search", Roles: []string{"wild"}},
			{Query: "worker search", Roles: []string{"worker"}},
		},
		WikiSnippets: []WikiSnippet{
			{Title: "worker wiki", Roles: []string{"worker"}},
			{Title: "chat wiki", Roles: []string{"chat"}},
		},
	}

	filtered := rp.FilterForRole("Worker")
	if len(filtered.MidSummaries) != 2 {
		t.Fatalf("expected worker and shared summaries, got %+v", filtered.MidSummaries)
	}
	if filtered.MidSummaries[0].Summary != "worker only" || filtered.MidSummaries[1].Summary != "shared" {
		t.Fatalf("unexpected filtered summaries: %+v", filtered.MidSummaries)
	}
	if len(filtered.SearchCacheSnippets) != 1 || filtered.SearchCacheSnippets[0].Query != "worker search" {
		t.Fatalf("unexpected filtered search snippets: %+v", filtered.SearchCacheSnippets)
	}
	if len(filtered.WikiSnippets) != 1 || filtered.WikiSnippets[0].Title != "worker wiki" {
		t.Fatalf("unexpected filtered wiki snippets: %+v", filtered.WikiSnippets)
	}
}

func TestRecallPack_FilterForRoleAppliesDefaultUseCasePolicy(t *testing.T) {
	rp := &RecallPack{
		MidSummaries: []ThreadSummary{{Summary: "mid shared"}},
		LongFacts:    []string{"long memory"},
		KBSnippets:   []string{"kb knowledge"},
		WikiSnippets: []WikiSnippet{
			{Title: "generic wiki"},
			{Title: "chat wiki", Roles: []string{"chat"}},
		},
		SearchCacheSnippets: []SearchCacheSnippet{
			{Query: "worker search"},
		},
	}

	chat := rp.FilterForRole("chat")
	if len(chat.LongFacts) != 1 || len(chat.KBSnippets) != 0 || len(chat.WikiSnippets) != 1 || len(chat.SearchCacheSnippets) != 0 {
		t.Fatalf("chat should keep memory and drop generic KB/search by default: %+v", chat)
	}
	worker := rp.FilterForRole("worker")
	if len(worker.LongFacts) != 1 || len(worker.KBSnippets) != 1 || len(worker.WikiSnippets) != 1 || len(worker.SearchCacheSnippets) != 1 {
		t.Fatalf("worker should keep practical recall sources: %+v", worker)
	}
	wild := rp.FilterForRole("wild")
	if len(wild.LongFacts) != 1 || len(wild.KBSnippets) != 1 || len(wild.WikiSnippets) != 1 || len(wild.SearchCacheSnippets) != 0 {
		t.Fatalf("wild should keep memory and KB but drop search cache by default: %+v", wild)
	}
	shiro := rp.FilterForRole("Shiro")
	if len(shiro.KBSnippets) != 1 || len(shiro.WikiSnippets) != 1 || len(shiro.SearchCacheSnippets) != 1 {
		t.Fatalf("Shiro should use worker recall policy: %+v", shiro)
	}
	ao := rp.FilterForRole("Ao")
	if len(ao.KBSnippets) != 1 || len(ao.WikiSnippets) != 1 || len(ao.SearchCacheSnippets) != 1 {
		t.Fatalf("Ao should use coder recall policy: %+v", ao)
	}

	localFirst := (&RecallPack{
		KBSnippets: []string{"[L1KB] local knowledge"},
		WikiSnippets: []WikiSnippet{
			{Title: "local wiki", Roles: []string{"chat"}},
		},
		SearchCacheSnippets: []SearchCacheSnippet{
			{Query: "fresh local cache", Roles: []string{"chat"}},
		},
	}).FilterForRole("Mio")
	if len(localFirst.KBSnippets) != 1 || len(localFirst.WikiSnippets) != 1 || len(localFirst.SearchCacheSnippets) != 1 {
		t.Fatalf("Mio should keep explicit local-first freshness recall: %+v", localFirst)
	}
}

func TestRecallPack_FilterForRoleKeepsRelationForWorker(t *testing.T) {
	rp := &RecallPack{
		RelationSnippets: []RelationSnippet{{
			ItemID:       "qiita-mlx",
			Title:        "MLXでLLM",
			Summary:      "MLX local LLM",
			SourceType:   "qiita",
			RelationType: "same_entity",
			Score:        3,
			Roles:        []string{"worker"},
		}},
	}
	worker := rp.FilterForRole("worker")
	if len(worker.RelationSnippets) != 1 {
		t.Fatalf("worker should keep relation snippet: %+v", worker)
	}
	chat := rp.FilterForRole("chat")
	if len(chat.RelationSnippets) != 0 || len(chat.RejectedTraceItems) == 0 {
		t.Fatalf("chat should reject generic relation snippet by default: %+v", chat)
	}
}

func TestRecallPack_FilterForRoleRejectsRelationForUnknownRole(t *testing.T) {
	rp := &RecallPack{
		RelationSnippets: []RelationSnippet{{
			ItemID:       "github-mlx",
			Title:        "mlx-lm",
			Summary:      "MLX repository",
			SourceType:   "github",
			RelationType: "same_entity",
			Score:        4,
		}},
	}
	filtered := rp.FilterForRole("unknown")
	if len(filtered.RelationSnippets) != 0 || len(filtered.RejectedTraceItems) != 1 {
		t.Fatalf("unknown role should reject relation snippet: %+v", filtered)
	}
}

// contains is a test helper
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
