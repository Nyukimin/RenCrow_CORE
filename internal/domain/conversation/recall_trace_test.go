package conversation

import (
	"testing"
	"time"
)

func TestRecallPackToTraceItems(t *testing.T) {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	rp := &RecallPack{
		ShortContext: []Message{{Speaker: SpeakerUser, Msg: "今の話題"}},
		MidSummaries: []ThreadSummary{{Summary: "mid memory", Domain: "movie", Score: 0.75}},
		LongFacts:    []string{"long fact"},
		KBSnippets:   []string{"kb snippet"},
		WikiSnippets: []WikiSnippet{{
			PageID:      "concept:recall-pack",
			Title:       "RecallPack",
			Path:        "docs/wiki/concepts/recall-pack.md",
			Summary:     "wiki summary",
			SourcePaths: []string{"internal/domain/conversation/recall_pack.go"},
			UpdatedAt:   now,
		}},
		SearchCacheSnippets: []SearchCacheSnippet{{
			Query:       "latest ai",
			Provider:    "web",
			ResultsJSON: `{"ok":true}`,
			SourceURLs:  []string{"https://example.com/a"},
			RetrievedAt: now,
		}},
	}

	items := rp.ToTraceItems()
	if len(items) != 6 {
		t.Fatalf("expected 6 trace items, got %d: %+v", len(items), items)
	}
	if items[0].Layer != "L0" || items[0].Kind != "short_context" || items[0].Summary != "今の話題" {
		t.Fatalf("unexpected L0 trace: %+v", items[0])
	}
	if items[1].Layer != "L2" || items[1].Kind != "thread_summary" || items[1].Summary != "mid memory" {
		t.Fatalf("unexpected mid trace: %+v", items[0])
	}
	if items[1].Score != 0.75 || items[1].Decision != "included" || items[1].Reason == "" || items[1].PromptIndex != 1 {
		t.Fatalf("mid trace should include score/decision/reason/prompt index: %+v", items[1])
	}
	if items[4].Layer != "L4" || items[4].Kind != "wiki_page" || items[4].SourceID != "concept:recall-pack" {
		t.Fatalf("unexpected wiki trace: %+v", items[4])
	}
	if items[5].Layer != "L1" || items[5].Kind != "search_cache" || items[5].SourceURLs[0] != "https://example.com/a" {
		t.Fatalf("unexpected search trace: %+v", items[5])
	}
}

func TestRecallPackToTraceItemsIncludesRelationSnippets(t *testing.T) {
	pack := &RecallPack{
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
	items := pack.ToTraceItems()
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if items[0].Kind != "knowledge_relation" || items[0].SourceID != "github-mlx" || items[0].Score != 3 {
		t.Fatalf("unexpected trace item: %#v", items[0])
	}
}

func TestRecallPackFilterForRoleKeepsRejectedTraceItems(t *testing.T) {
	rp := &RecallPack{
		MidSummaries: []ThreadSummary{
			{Summary: "chat memory", Roles: []string{"chat"}, Score: 0.8},
			{Summary: "worker memory", Roles: []string{"worker"}, Score: 0.7},
		},
		KBSnippets: []string{"kb blocked for chat"},
		WikiSnippets: []WikiSnippet{
			{Title: "wiki blocked for chat"},
		},
		SearchCacheSnippets: []SearchCacheSnippet{
			{Query: "fresh search", Provider: "web", ResultsJSON: `{"hit":true}`},
		},
	}

	filtered := rp.FilterForRole("chat")
	items := filtered.ToTraceItems()

	var rejected []RecallTraceItem
	for _, item := range items {
		if item.Decision == "rejected" {
			rejected = append(rejected, item)
		}
	}
	if len(rejected) != 4 {
		t.Fatalf("expected rejected worker/KB/search trace items, got %+v", items)
	}
	if rejected[0].Kind != "thread_summary" || rejected[0].Summary != "worker memory" || rejected[0].PromptIndex != -1 {
		t.Fatalf("unexpected rejected summary trace: %+v", rejected[0])
	}
	if rejected[1].Kind != "knowledge" || rejected[2].Kind != "wiki_page" || rejected[3].Kind != "search_cache" {
		t.Fatalf("unexpected rejected trace kinds: %+v", rejected)
	}
	for _, item := range rejected {
		if item.Reason == "" {
			t.Fatalf("rejected trace should include reason: %+v", item)
		}
	}
}
