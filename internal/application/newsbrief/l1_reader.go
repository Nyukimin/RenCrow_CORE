package newsbrief

import (
	"context"
	"errors"
	"strings"
	"time"

	domainnews "github.com/Nyukimin/RenCrow_CORE/internal/domain/newsbrief"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

type L1NewsReaderStore interface {
	RecentNewsItems(ctx context.Context, category string, limit int) ([]l1sqlite.L1NewsItem, error)
}

type L1Reader struct {
	store L1NewsReaderStore
}

func NewL1Reader(store L1NewsReaderStore) *L1Reader {
	if store == nil {
		return nil
	}
	return &L1Reader{store: store}
}

// Read maps already validated source-registry rows into a read-only scheduled
// brief. It never performs network collection in the foreground request path.
func (r *L1Reader) Read(ctx context.Context, now time.Time) (domainnews.DailyNewsBrief, error) {
	brief := domainnews.DailyNewsBrief{
		Date:               domainnews.ExpectedMorningDate(now),
		Slot:               domainnews.SlotMorning,
		Timezone:           domainnews.TimezoneJST,
		Source:             domainnews.SourcePersistent,
		SkillID:            "persistent_daily_news",
		Status:             domainnews.StatusEmpty,
		EnrichmentStatus:   domainnews.EnrichmentPartial,
		EnrichmentProvider: "source_registry.rss_atom",
		Items:              []domainnews.Item{},
	}
	if r == nil || r.store == nil {
		return brief, nil
	}
	items, err := r.store.RecentNewsItems(ctx, "", 200)
	if err != nil {
		return brief, err
	}
	for _, item := range items {
		itemTime := item.PublishedAt
		if itemTime.IsZero() {
			itemTime = item.FetchedAt
		}
		if itemTime.IsZero() || itemTime.In(newsJST).Format("2006-01-02") != brief.Date {
			continue
		}
		brief.Items = append(brief.Items, l1NewsBriefItem(item))
		if item.FetchedAt.After(brief.FetchedAt) {
			brief.FetchedAt = item.FetchedAt
		}
	}
	if len(brief.Items) > 0 {
		brief.Status = domainnews.StatusPartial
	}
	return brief, nil
}

func l1NewsBriefItem(item l1sqlite.L1NewsItem) domainnews.Item {
	title := stringNewsMeta(item.Meta, "feed_item_title")
	if title == "" {
		title = firstNonEmptyLine(item.RawText)
	}
	if title == "" {
		title = strings.TrimSpace(item.SummaryDraft)
	}
	summary := compactNewsText(newsTextWithoutTitleLine(item.RawText, title), 360)
	if summary == "" {
		summary = compactNewsText(item.SummaryDraft, 360)
	}
	if summary == "" {
		summary = title
	}
	sourceType := stringNewsMeta(item.Meta, "source_kind")
	if sourceType == "" {
		sourceType = strings.TrimSpace(strings.SplitN(item.SourceID, ":", 2)[0])
	}
	sourceName := stringNewsMeta(item.Meta, "source_name")
	if sourceName == "" {
		sourceName = item.SourceID
	}
	return domainnews.Item{
		ID:               item.ID,
		Title:            title,
		Category:         item.Category,
		Source:           sourceName,
		SourceType:       sourceType,
		URL:              item.SourceURL,
		SourceReadStatus: "feed_fetched",
		SourceReadURL:    item.SourceURL,
		Summary:          summary,
	}
}

func newsTextWithoutTitleLine(text string, title string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for index, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.TrimSpace(line) == title {
			lines = append(lines[:index], lines[index+1:]...)
		}
		break
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func compactNewsText(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	runes := []rune(text)
	if limit > 0 && len(runes) > limit {
		return strings.TrimSpace(string(runes[:limit])) + "..."
	}
	return text
}

func stringNewsMeta(meta map[string]interface{}, key string) string {
	if value, ok := meta[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

type FallbackReader struct {
	readers []domainnews.DailyNewsBriefReader
}

func NewFallbackReader(readers ...domainnews.DailyNewsBriefReader) *FallbackReader {
	filtered := make([]domainnews.DailyNewsBriefReader, 0, len(readers))
	for _, reader := range readers {
		if reader != nil {
			filtered = append(filtered, reader)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return &FallbackReader{readers: filtered}
}

func (r *FallbackReader) Read(ctx context.Context, now time.Time) (domainnews.DailyNewsBrief, error) {
	var first domainnews.DailyNewsBrief
	var readErrors []error
	for index, reader := range r.readers {
		brief, err := reader.Read(ctx, now)
		if index == 0 || (len(first.Items) == 0 && len(brief.Items) > 0) {
			first = brief
		}
		if err != nil {
			readErrors = append(readErrors, err)
			continue
		}
		if brief.IsUsable(now) {
			return brief, nil
		}
	}
	if len(readErrors) == len(r.readers) {
		return first, errors.Join(readErrors...)
	}
	return first, nil
}

var newsJST = time.FixedZone("JST", 9*60*60)

var _ domainnews.DailyNewsBriefReader = (*L1Reader)(nil)
var _ domainnews.DailyNewsBriefReader = (*FallbackReader)(nil)
