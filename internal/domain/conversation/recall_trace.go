package conversation

import (
	"context"
	"time"
)

type RecallTrace struct {
	ResponseID string
	SessionID  string
	Role       string
	Items      []RecallTraceItem
	CreatedAt  time.Time
}
type RecallTraceItem struct {
	Layer         string
	Kind          string
	MemoryID      string
	SourceID      string
	SourceType    string
	Summary       string
	Query         string
	Provider      string
	SourceURLs    []string
	RetrievedAt   time.Time
	Score         float32
	Decision      string
	Status        string
	Reason        string
	PromptSection string
	TokenCount    int
	PromptIndex   int
}

type RecallTraceRecord struct {
	TraceID             string
	TurnID              string
	ChatID              string
	Persona             string
	Route               string
	UserMessageHash     string
	QueryTextRedacted   string
	CreatedAt           time.Time
	ModelID             string
	PromptVersion       string
	RecallPolicyVersion string
	TotalCandidates     int
	InjectedCount       int
	TotalInjectedTokens int
	Status              string
}

type RecallTraceItemRecord struct {
	ItemID         string
	TraceID        string
	Layer          string
	MemoryID       string
	SourceID       string
	SourceURL      string
	SourceType     string
	Status         string
	Score          float64
	Relevance      float64
	Recency        float64
	Confidence     float64
	SourceTrust    float64
	Reason         string
	Injected       bool
	PromptSection  string
	TokenCount     int
	Sensitivity    string
	IsRawOrSummary string
	RetrievedAt    time.Time
	PublishedAt    time.Time
	EventID        string
	Summary        string
	Kind           string
}

type PromptInjectionEventRecord struct {
	InjectionID    string
	TraceID        string
	PromptSection  string
	OrderIndex     int
	ItemIDs        []string
	TokenCount     int
	RedactionLevel string
	CreatedAt      time.Time
}

type RecallTraceStore interface {
	StartRecallTrace(ctx context.Context, trace RecallTraceRecord) error
	AddRecallTraceItems(ctx context.Context, traceID string, items []RecallTraceItemRecord) error
	AddPromptInjectionEvents(ctx context.Context, traceID string, events []PromptInjectionEventRecord) error
	FinishRecallTrace(ctx context.Context, traceID string, status string, injectedCount int, totalTokens int) error
}

func (rp *RecallPack) ToTraceItems() []RecallTraceItem {
	if rp == nil {
		return nil
	}
	items := make([]RecallTraceItem, 0, len(rp.ShortContext)+len(rp.MidSummaries)+len(rp.LongFacts)+len(rp.KBSnippets)+len(rp.WikiSnippets)+len(rp.SearchCacheSnippets)+len(rp.RelationSnippets)+len(rp.RejectedTraceItems)+1)
	if rp.RollingSummary != "" {
		items = append(items, RecallTraceItem{
			Layer:         "L0",
			Kind:          "rolling_summary",
			Summary:       rp.RollingSummary,
			Decision:      "included",
			Status:        TraceStatusInjected,
			PromptSection: PromptSectionCurrentTurn,
			TokenCount:    estimateRecallTokens(rp.RollingSummary),
			Reason:        "L0 rolling summary keeps older current-thread context compact",
			PromptIndex:   len(items),
		})
	}
	for _, msg := range rp.ShortContext {
		items = append(items, RecallTraceItem{
			Layer:         "L0",
			Kind:          "short_context",
			Summary:       msg.Msg,
			Decision:      "included",
			Status:        TraceStatusInjected,
			PromptSection: PromptSectionConversation,
			TokenCount:    estimateRecallTokens(msg.Msg),
			Reason:        "recent L0 turn preserved as short context",
			PromptIndex:   len(items),
		})
	}
	for _, summary := range rp.MidSummaries {
		items = append(items, RecallTraceItem{
			Layer:         "L2",
			Kind:          "thread_summary",
			Summary:       summary.Summary,
			Score:         summary.Score,
			Decision:      "included",
			Status:        TraceStatusInjected,
			PromptSection: PromptSectionConversation,
			TokenCount:    estimateRecallTokens(summary.Summary),
			Reason:        "shared L2 thread summary selected for prompt",
			PromptIndex:   len(items),
		})
	}
	for _, fact := range rp.LongFacts {
		items = append(items, RecallTraceItem{
			Layer:         "L3",
			Kind:          "long_fact",
			Summary:       fact,
			Decision:      "included",
			Status:        TraceStatusInjected,
			PromptSection: PromptSectionKnowledge,
			TokenCount:    estimateRecallTokens(fact),
			Reason:        "L3 long-term memory selected for prompt",
			PromptIndex:   len(items),
		})
	}
	for _, snippet := range rp.KBSnippets {
		items = append(items, RecallTraceItem{
			Layer:         "L3",
			Kind:          "knowledge",
			Summary:       snippet,
			Decision:      "included",
			Status:        TraceStatusInjected,
			PromptSection: PromptSectionKnowledge,
			TokenCount:    estimateRecallTokens(snippet),
			Reason:        "Knowledge DB snippet selected for prompt",
			PromptIndex:   len(items),
		})
	}
	for _, snippet := range rp.WikiSnippets {
		promptText := snippet.ToPromptText()
		items = append(items, RecallTraceItem{
			Layer:         "L4",
			Kind:          "wiki_page",
			SourceID:      snippet.PageID,
			SourceType:    "knowledge_wiki",
			Summary:       promptText,
			SourceURLs:    append([]string(nil), snippet.SourcePaths...),
			RetrievedAt:   snippet.UpdatedAt,
			Decision:      "included",
			Status:        TraceStatusInjected,
			PromptSection: PromptSectionKnowledge,
			TokenCount:    estimateRecallTokens(promptText),
			Reason:        "Knowledge Wiki page selected for prompt",
			PromptIndex:   len(items),
		})
	}
	for _, cache := range rp.SearchCacheSnippets {
		items = append(items, RecallTraceItem{
			Layer:         "L1",
			Kind:          "search_cache",
			Summary:       cache.ResultsJSON,
			Query:         cache.Query,
			Provider:      cache.Provider,
			SourceURLs:    append([]string(nil), cache.SourceURLs...),
			RetrievedAt:   cache.RetrievedAt,
			Decision:      "included",
			Status:        TraceStatusInjected,
			PromptSection: PromptSectionNews,
			TokenCount:    estimateRecallTokens(cache.ToPromptText()),
			Reason:        "fresh L1 search cache hit selected for prompt",
			PromptIndex:   len(items),
		})
	}
	for _, relation := range rp.RelationSnippets {
		promptText := relation.ToPromptText()
		items = append(items, RecallTraceItem{
			Layer:         "L1",
			Kind:          "knowledge_relation",
			SourceID:      relation.ItemID,
			SourceType:    relation.SourceType,
			Summary:       promptText,
			Score:         float32(relation.Score),
			Decision:      "included",
			Status:        TraceStatusInjected,
			PromptSection: PromptSectionKnowledge,
			TokenCount:    estimateRecallTokens(promptText),
			Reason:        "Knowledge Relation snippet selected for prompt",
			PromptIndex:   len(items),
		})
	}
	items = append(items, rp.RejectedTraceItems...)
	return items
}
