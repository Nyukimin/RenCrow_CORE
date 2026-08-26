package idlechat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	domainnews "github.com/Nyukimin/RenCrow_CORE/internal/domain/newsbrief"
)

// Read returns the prepared morning brief without starting collection,
// enrichment, or cache mutation. IdleChat remains the current producer of the
// cache, while the domain reader keeps its contents available to foreground
// Chat use cases without exposing IdleChat internals.
func (o *IdleChatOrchestrator) Read(ctx context.Context, now time.Time) (domainnews.DailyNewsBrief, error) {
	_ = ctx
	brief := domainnews.DailyNewsBrief{
		Slot:     domainnews.SlotMorning,
		Timezone: domainnews.TimezoneJST,
		Source:   domainnews.SourceScheduled,
		SkillID:  dailySourceBriefSkillID,
		Status:   domainnews.StatusEmpty,
		Items:    []domainnews.Item{},
	}

	cacheMu.RLock()
	defer cacheMu.RUnlock()
	if dailyCache == nil {
		return brief, nil
	}

	brief.Date = strings.TrimSpace(dailyCache.Date)
	brief.FetchedAt = dailyCache.FetchedAt
	brief.EnrichedAt = dailyCache.EnrichedAt
	brief.EnrichmentProvider = strings.TrimSpace(dailyCache.EnrichmentProvider)
	brief.EnrichmentError = strings.TrimSpace(dailyCache.EnrichmentError)
	brief.EnrichmentStatus = strings.TrimSpace(dailyCache.EnrichmentStatus)
	if brief.EnrichmentStatus == "" {
		brief.EnrichmentStatus = domainnews.StatusPending
	}

	if brief.Date != domainnews.ExpectedMorningDate(now) {
		brief.Status = domainnews.StatusStale
	} else if len(dailyCache.NewsSeedItems) == 0 {
		brief.Status = domainnews.StatusEmpty
	} else {
		brief.Status = brief.EnrichmentStatus
	}

	for index, seed := range dailyCache.NewsSeedItems {
		if !isDailyNewsBriefArticle(seed.SourceType) {
			continue
		}
		if !isDailyNewsBriefAnalyzed(seed) {
			continue
		}
		brief.Items = append(brief.Items, dailyNewsBriefItem(seed, index))
	}
	if len(brief.Items) == 0 && brief.Status != domainnews.StatusStale {
		brief.Status = domainnews.StatusEmpty
	}
	return brief, nil
}

func isDailyNewsBriefArticle(sourceType string) bool {
	switch strings.ToLower(strings.TrimSpace(sourceType)) {
	case "rss", "atom":
		return true
	default:
		return false
	}
}

func isDailyNewsBriefAnalyzed(seed NewsSeed) bool {
	sourceReadStatus := strings.ToLower(strings.TrimSpace(seed.SourceReadStatus))
	if sourceReadStatus != "ready" && sourceReadStatus != "unavailable" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(seed.ProcessingStatus)) {
	case dailyProcessingReady,
		dailyProcessingSourceUnavailable,
		dailyProcessingTranslationFailed,
		dailyProcessingTermExtractionFailed,
		dailyProcessingBriefFailed:
		return true
	default:
		return false
	}
}

func dailyNewsBriefItem(seed NewsSeed, index int) domainnews.Item {
	termNotes := make([]domainnews.TermNote, 0, len(seed.TermNotes))
	for _, note := range seed.TermNotes {
		termNotes = append(termNotes, domainnews.TermNote{
			Term:        strings.TrimSpace(note.Term),
			Explanation: strings.TrimSpace(note.Explanation),
			SourceKind:  strings.TrimSpace(note.SourceKind),
			SourceURL:   strings.TrimSpace(note.SourceURL),
			Status:      strings.TrimSpace(note.Status),
		})
	}
	return domainnews.Item{
		ID:               dailyNewsBriefItemID(seed, index),
		Title:            strings.TrimSpace(seed.Title),
		Category:         strings.TrimSpace(seed.Category),
		Source:           strings.TrimSpace(seed.Source),
		SourceType:       strings.TrimSpace(seed.SourceType),
		URL:              strings.TrimSpace(seed.URL),
		SourceReadStatus: strings.TrimSpace(seed.SourceReadStatus),
		SourceReadURL:    strings.TrimSpace(seed.SourceReadURL),
		TranslatedBody:   strings.TrimSpace(seed.TranslatedBody),
		Summary:          strings.TrimSpace(seed.Summary),
		Perspective:      strings.TrimSpace(seed.Perspective),
		TermNotes:        termNotes,
	}
}

func dailyNewsBriefItemID(seed NewsSeed, index int) string {
	key := strings.TrimSpace(seed.URL)
	if key == "" {
		key = strings.Join([]string{
			strings.TrimSpace(seed.Source),
			strings.TrimSpace(seed.Category),
			strings.TrimSpace(seed.Title),
		}, "|")
	}
	if key == "" {
		key = "item"
	}
	hash := sha256.Sum256([]byte(key))
	return "news-" + hex.EncodeToString(hash[:6]) + "-" + stringIndexSuffix(index)
}

func stringIndexSuffix(index int) string {
	if index < 0 {
		return "0"
	}
	// Keep the suffix human-readable while the hash remains the stable identity.
	return hex.EncodeToString([]byte{byte(index >> 8), byte(index)})
}
