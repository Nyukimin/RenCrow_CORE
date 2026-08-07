package newsbrief

import (
	"context"
	"time"
)

const (
	SlotMorning       = "morning"
	TimezoneJST       = "JST"
	SourceScheduled   = "scheduled_daily_cache"
	SourcePersistent  = "persistent_news_db"
	SourceLiveSearch  = "live_news_search"
	StatusEmpty       = "empty"
	StatusPending     = "pending"
	StatusEnriching   = "enriching"
	StatusReady       = "ready"
	StatusPartial     = "partial"
	StatusFallback    = "fallback"
	StatusStale       = "stale"
	EnrichmentReady   = "ready"
	EnrichmentPartial = "partial"
)

// TermNote is a verified or explicitly unresolved term annotation attached to
// one source article.
type TermNote struct {
	Term        string
	Explanation string
	SourceKind  string
	SourceURL   string
	Status      string
}

// Item is one factual news item in a prepared daily brief.
type Item struct {
	ID               string
	Title            string
	Category         string
	Source           string
	SourceType       string
	URL              string
	SourceReadStatus string
	SourceReadURL    string
	TranslatedBody   string
	Summary          string
	Perspective      string
	TermNotes        []TermNote
}

// DailyNewsBrief is a read-only, scheduled morning news snapshot. It is not
// a user-specific delivery record and it does not trigger collection.
type DailyNewsBrief struct {
	Date               string
	Slot               string
	Timezone           string
	Source             string
	SkillID            string
	Status             string
	EnrichmentStatus   string
	EnrichmentProvider string
	EnrichmentError    string
	FetchedAt          time.Time
	EnrichedAt         time.Time
	Items              []Item
}

// DailyNewsBriefReader is the only dependency a foreground Chat use case needs
// in order to consume a prepared brief. Implementations must be read-only.
type DailyNewsBriefReader interface {
	Read(ctx context.Context, now time.Time) (DailyNewsBrief, error)
}

// DailyNewsBriefCollector performs foreground collection when the scheduled
// morning cache is unavailable. It is a work/tool boundary, not an Agent
// personality boundary; callers pass the structured result to the narrator.
type DailyNewsBriefCollector interface {
	Collect(ctx context.Context, query string, now time.Time) (DailyNewsBrief, error)
}

// Reader is kept as a short alias for internal callers.
type Reader = DailyNewsBriefReader

// ReaderFunc adapts a function to Reader.
type ReaderFunc func(context.Context, time.Time) (DailyNewsBrief, error)

func (f ReaderFunc) Read(ctx context.Context, now time.Time) (DailyNewsBrief, error) {
	return f(ctx, now)
}

// ExpectedMorningDate returns the JST date whose 04:00 morning brief should
// answer a request at now. Before 04:00, the previous day's brief is the most
// recent completed morning slot.
func ExpectedMorningDate(now time.Time) string {
	local := now.In(jst)
	if local.Hour() < 4 {
		local = local.AddDate(0, 0, -1)
	}
	return local.Format("2006-01-02")
}

// IsUsable reports whether the brief has completed prepared content for the
// morning slot expected at now.
func (b DailyNewsBrief) IsUsable(now time.Time) bool {
	if (b.Source != SourceScheduled && b.Source != SourcePersistent) || b.Date != ExpectedMorningDate(now) || len(b.Items) == 0 {
		return false
	}
	return b.EnrichmentStatus == EnrichmentReady || b.EnrichmentStatus == EnrichmentPartial
}

var jst = time.FixedZone("JST", 9*60*60)
