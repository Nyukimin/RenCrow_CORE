package knowledgememory

import (
	"context"
	"fmt"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"net/url"
	"reflect"
	"strings"
	"time"

	kmapp "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgememory"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/sourcefetcher"
	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
)

type Store interface {
	SavePersonalArchiveEntry(ctx context.Context, item domainkm.PersonalArchiveEntry) error
	ListPersonalArchiveEntries(ctx context.Context, limit int) ([]domainkm.PersonalArchiveEntry, error)
	SaveCreativeKnowledgeItem(ctx context.Context, item domainkm.CreativeKnowledgeItem) error
	ListCreativeKnowledgeItems(ctx context.Context, limit int) ([]domainkm.CreativeKnowledgeItem, error)
	SaveNewsKnowledgeItem(ctx context.Context, item domainkm.NewsKnowledgeItem) error
	ListNewsKnowledgeItems(ctx context.Context, limit int) ([]domainkm.NewsKnowledgeItem, error)
	SaveDailyIntakeRule(ctx context.Context, item domainkm.DailyIntakeRule) error
	ListDailyIntakeRules(ctx context.Context, limit int) ([]domainkm.DailyIntakeRule, error)
	SaveTemporalMemoryMarker(ctx context.Context, item domainkm.TemporalMemoryMarker) error
	ListTemporalMemoryMarkers(ctx context.Context, limit int) ([]domainkm.TemporalMemoryMarker, error)
	SaveDreamConsolidationRun(ctx context.Context, item domainkm.DreamConsolidationRun) error
	ListDreamConsolidationRuns(ctx context.Context, limit int) ([]domainkm.DreamConsolidationRun, error)
}

type L1StagingStore interface {
	SaveStagingItem(ctx context.Context, item l1sqlite.L1StagingItem) (*l1sqlite.L1StagingItem, error)
}

type L1SourceRegistryStore interface {
	SaveSourceRegistryEntry(ctx context.Context, entry l1sqlite.L1SourceRegistryEntry) (*l1sqlite.L1SourceRegistryEntry, error)
}

type DailyIntakeL1Store interface {
	L1SourceRegistryStore
	sourcefetcher.RegistryStore
}

type DailyIntakeRegistryAdapter struct {
	store DailyIntakeL1Store
}

func NewDailyIntakeRegistryAdapter(store DailyIntakeL1Store) *DailyIntakeRegistryAdapter {
	if store == nil {
		return nil
	}
	return &DailyIntakeRegistryAdapter{store: store}
}

func (a *DailyIntakeRegistryAdapter) SaveSourceRegistryEntry(ctx context.Context, entry kmapp.SourceRegistryEntry) (*kmapp.SourceRegistryEntry, error) {
	if a == nil || a.store == nil {
		return nil, fmt.Errorf("daily intake L1 store is nil")
	}
	saved, err := a.store.SaveSourceRegistryEntry(ctx, toL1SourceRegistryEntry(entry))
	if err != nil {
		return nil, err
	}
	out := fromL1SourceRegistryEntry(*saved)
	return &out, nil
}

func (a *DailyIntakeRegistryAdapter) SweepDueSources(ctx context.Context, now time.Time, opts kmapp.SourceRegistrySweepOptions) (kmapp.SourceRegistrySweepResult, error) {
	if a == nil || a.store == nil {
		return kmapp.SourceRegistrySweepResult{}, fmt.Errorf("daily intake L1 store is nil")
	}
	result, err := sourcefetcher.SweepDueSources(ctx, a.store, now, sourcefetcher.SweepOptions{
		LimitPerSource:    opts.LimitPerSource,
		MinimumTrustScore: opts.MinimumTrustScore,
	})
	return kmapp.SourceRegistrySweepResult{
		Sources:           result.Sources,
		Staged:            result.Staged,
		Warnings:          result.Warnings,
		Validated:         result.Validated,
		PromotedNews:      result.PromotedNews,
		PromotedKnowledge: result.PromotedKnowledge,
		Failed:            result.Failed,
	}, err
}

type L1ConnectedStore struct {
	base     Store
	staging  L1StagingStore
	registry L1SourceRegistryStore
	now      func() time.Time
}

func WithL1Connection(base Store, l1 any) Store {
	if base == nil || isNilL1Store(l1) {
		return base
	}
	staging, ok := l1.(L1StagingStore)
	if !ok {
		return base
	}
	registry, _ := l1.(L1SourceRegistryStore)
	return &L1ConnectedStore{
		base:     base,
		staging:  staging,
		registry: registry,
		now:      time.Now,
	}
}

func toL1SourceRegistryEntry(entry kmapp.SourceRegistryEntry) l1sqlite.L1SourceRegistryEntry {
	return l1sqlite.L1SourceRegistryEntry{
		SourceID:      entry.SourceID,
		URL:           entry.URL,
		Kind:          entry.Kind,
		TrustScore:    entry.TrustScore,
		FetchInterval: entry.FetchInterval,
		LicenseNote:   entry.LicenseNote,
		Enabled:       entry.Enabled,
		Meta:          entry.Meta,
	}
}

func fromL1SourceRegistryEntry(entry l1sqlite.L1SourceRegistryEntry) kmapp.SourceRegistryEntry {
	return kmapp.SourceRegistryEntry{
		SourceID:      entry.SourceID,
		URL:           entry.URL,
		Kind:          entry.Kind,
		TrustScore:    entry.TrustScore,
		FetchInterval: entry.FetchInterval,
		LicenseNote:   entry.LicenseNote,
		Enabled:       entry.Enabled,
		Meta:          entry.Meta,
	}
}

func isNilL1Store(l1 any) bool {
	if l1 == nil {
		return true
	}
	v := reflect.ValueOf(l1)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func (s *L1ConnectedStore) SavePersonalArchiveEntry(ctx context.Context, item domainkm.PersonalArchiveEntry) error {
	if err := s.base.SavePersonalArchiveEntry(ctx, item); err != nil {
		return err
	}
	namespace := userNamespace(item.UserID)
	raw := strings.TrimSpace(item.OriginalText)
	return s.stage(ctx, l1sqlite.L1StagingKindMemoryCandidate, namespace, item.EntryID, "personal_archive", item.SourceRef, raw, "", []string{"personal_archive"}, map[string]interface{}{
		"knowledge_memory_type": "personal_archive",
		"protected_original":    item.Protected,
		"source_ref":            item.SourceRef,
		"review_required":       true,
		"auto_promote":          false,
	})
}

func (s *L1ConnectedStore) ListPersonalArchiveEntries(ctx context.Context, limit int) ([]domainkm.PersonalArchiveEntry, error) {
	return s.base.ListPersonalArchiveEntries(ctx, limit)
}

func (s *L1ConnectedStore) SaveCreativeKnowledgeItem(ctx context.Context, item domainkm.CreativeKnowledgeItem) error {
	if err := s.base.SaveCreativeKnowledgeItem(ctx, item); err != nil {
		return err
	}
	raw := strings.Join(nonEmptyStrings([]string{
		item.Title,
		"creators: " + strings.Join(item.CreatorNames, ", "),
		"work_type: " + item.WorkType,
		"related: " + strings.Join(item.RelatedWorks, ", "),
		"hints: " + strings.Join(item.ContentHints, ", "),
	}), "\n")
	return s.stage(ctx, l1sqlite.L1StagingKindExternalFetch, "kb:creative", item.ItemID, "creative_knowledge", "", raw, item.Title, item.ContentHints, map[string]interface{}{
		"knowledge_memory_type": "creative_knowledge",
		"work_type":             item.WorkType,
		"creator_names":         item.CreatorNames,
		"review_required":       true,
		"auto_promote":          false,
	})
}

func (s *L1ConnectedStore) ListCreativeKnowledgeItems(ctx context.Context, limit int) ([]domainkm.CreativeKnowledgeItem, error) {
	return s.base.ListCreativeKnowledgeItems(ctx, limit)
}

func (s *L1ConnectedStore) SaveNewsKnowledgeItem(ctx context.Context, item domainkm.NewsKnowledgeItem) error {
	if err := s.base.SaveNewsKnowledgeItem(ctx, item); err != nil {
		return err
	}
	raw := strings.Join(nonEmptyStrings([]string{
		item.Topic,
		item.Summary,
		"source: " + item.Source,
		"event_date: " + item.EventDate,
	}), "\n")
	if item.URL != "" && s.registry != nil {
		if err := s.saveSourceRegistryCandidate(ctx, item.ItemID, item.URL, "news_knowledge", item.Source); err != nil {
			return err
		}
	}
	return s.stage(ctx, l1sqlite.L1StagingKindExternalFetch, "kb:news", item.ItemID, "news_knowledge", item.URL, raw, item.Summary, []string{item.Topic, item.Source}, map[string]interface{}{
		"knowledge_memory_type": "news_knowledge",
		"durable":               item.Durable,
		"event_date":            item.EventDate,
		"review_required":       true,
		"auto_promote":          false,
	})
}

func (s *L1ConnectedStore) ListNewsKnowledgeItems(ctx context.Context, limit int) ([]domainkm.NewsKnowledgeItem, error) {
	return s.base.ListNewsKnowledgeItems(ctx, limit)
}

func (s *L1ConnectedStore) SaveDailyIntakeRule(ctx context.Context, item domainkm.DailyIntakeRule) error {
	if err := s.base.SaveDailyIntakeRule(ctx, item); err != nil {
		return err
	}
	if item.SourceHint != "" && s.registry != nil && isHTTPURL(item.SourceHint) {
		if err := s.saveSourceRegistryCandidate(ctx, item.RuleID, item.SourceHint, "daily_intake_rule", item.Topic); err != nil {
			return err
		}
	}
	raw := strings.Join(nonEmptyStrings([]string{
		"topic: " + item.Topic,
		"source_hint: " + item.SourceHint,
		"cadence: " + item.Cadence,
		"status: " + item.Status,
	}), "\n")
	return s.stage(ctx, l1sqlite.L1StagingKindMemoryCandidate, userNamespace(item.UserID), item.RuleID, "daily_intake_rule", "", raw, item.Topic, []string{"daily_intake", item.Topic}, map[string]interface{}{
		"knowledge_memory_type": "daily_intake_rule",
		"source_hint":           item.SourceHint,
		"cadence":               item.Cadence,
		"review_required":       true,
		"auto_promote":          false,
	})
}

func (s *L1ConnectedStore) ListDailyIntakeRules(ctx context.Context, limit int) ([]domainkm.DailyIntakeRule, error) {
	return s.base.ListDailyIntakeRules(ctx, limit)
}

func (s *L1ConnectedStore) SaveTemporalMemoryMarker(ctx context.Context, item domainkm.TemporalMemoryMarker) error {
	if err := s.base.SaveTemporalMemoryMarker(ctx, item); err != nil {
		return err
	}
	return nil
}

func (s *L1ConnectedStore) ListTemporalMemoryMarkers(ctx context.Context, limit int) ([]domainkm.TemporalMemoryMarker, error) {
	return s.base.ListTemporalMemoryMarkers(ctx, limit)
}

func (s *L1ConnectedStore) SaveDreamConsolidationRun(ctx context.Context, item domainkm.DreamConsolidationRun) error {
	if err := s.base.SaveDreamConsolidationRun(ctx, item); err != nil {
		return err
	}
	raw := strings.Join(nonEmptyStrings(append([]string{
		"status: " + item.Status,
		"review_status: " + item.ReviewStatus,
	}, item.IdeaSeeds...)), "\n")
	return s.stage(ctx, l1sqlite.L1StagingKindMemoryCandidate, "kb:dream", item.RunID, "dream_consolidation_run", "", raw, strings.Join(item.IdeaSeeds, "\n"), item.IdeaSeeds, map[string]interface{}{
		"knowledge_memory_type": "dream_consolidation_run",
		"scope":                 item.Scope,
		"review_status":         item.ReviewStatus,
		"review_required":       true,
		"auto_promote":          false,
	})
}

func (s *L1ConnectedStore) ListDreamConsolidationRuns(ctx context.Context, limit int) ([]domainkm.DreamConsolidationRun, error) {
	return s.base.ListDreamConsolidationRuns(ctx, limit)
}

func (s *L1ConnectedStore) stage(ctx context.Context, kind string, namespace string, eventID string, sourceType string, sourceRef string, rawText string, summary string, keywords []string, meta map[string]interface{}) error {
	if s.staging == nil {
		return nil
	}
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return fmt.Errorf("knowledge memory %s id is required for L1 staging", sourceType)
	}
	if strings.TrimSpace(rawText) == "" {
		return nil
	}
	now := s.now().UTC()
	sourceURL := sourceRef
	if !isHTTPURL(sourceURL) {
		sourceURL = ""
	}
	sourceID := "knowledge_memory:" + sourceType + ":" + eventID
	if meta == nil {
		meta = map[string]interface{}{}
	}
	meta["source_kind"] = "knowledge_memory"
	meta["source_type"] = sourceType
	meta["review_required"] = true
	meta["auto_promote"] = false
	_, err := s.staging.SaveStagingItem(ctx, l1sqlite.L1StagingItem{
		Kind:             kind,
		Namespace:        namespace,
		EventID:          eventID,
		SourceID:         sourceID,
		SourceURL:        sourceURL,
		FetchedAt:        now,
		RawText:          rawText,
		SummaryDraft:     summary,
		Keywords:         compactStrings(keywords),
		LicenseNote:      "knowledge memory candidate; review required before promote",
		ValidationStatus: l1sqlite.L1StagingStatusPending,
		Meta:             meta,
	})
	return err
}

func (s *L1ConnectedStore) saveSourceRegistryCandidate(ctx context.Context, id string, rawURL string, sourceType string, sourceName string) error {
	if s.registry == nil || !isHTTPURL(rawURL) {
		return nil
	}
	_, err := s.registry.SaveSourceRegistryEntry(ctx, l1sqlite.L1SourceRegistryEntry{
		SourceID:      "knowledge_memory:" + sourceType + ":" + id,
		URL:           rawURL,
		Kind:          l1sqlite.L1SourceKindSearchFallback,
		TrustScore:    0.50,
		FetchInterval: 24 * time.Hour,
		LicenseNote:   "knowledge memory source candidate; review required before promote",
		Enabled:       false,
		Meta: map[string]interface{}{
			"source_kind":           "knowledge_memory",
			"source_type":           sourceType,
			"source_name":           sourceName,
			"review_required":       true,
			"auto_fetch":            false,
			"created_from_l1_stage": true,
		},
	})
	return err
}

func userNamespace(userID string) string {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		userID = "unknown"
	}
	return "user:" + userID
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !strings.HasSuffix(value, ":") {
			out = append(out, value)
		}
	}
	return out
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
