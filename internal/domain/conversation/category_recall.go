package conversation

import (
	"context"
	"sort"
	"strings"
	"time"
)

const (
	CategoryRecallStatusNotSelected        = "not_selected"
	CategoryRecallStatusCompleted          = "completed"
	CategoryRecallStatusPartial            = "partial"
	CategoryRecallStatusUnavailable        = "unavailable"
	CategoryRecordStateValidated           = "validated"
	CategoryRecordStateConfirmed           = "confirmed"
	CategoryRecordStateApproved            = "approved"
	CategoryRecordStateActive              = "active"
	CategoryRecallFailureSourceUnavailable = "source_unavailable"
	CategoryRecallFailureStale             = "stale"
	CategoryRecallFailureInvalid           = "invalid"
	CategoryRecallFailureMissingProvenance = "missing_provenance"
	CategoryRecallFailureScopeDenied       = "scope_denied"
	CategoryRecallFailureRoleDenied        = "role_denied"
)

// CategoryRecallQuery is the deterministic input to a category source.
// Category is populated internally by the registry after source selection; it
// is not a caller-provided routing signal.
type CategoryRecallQuery struct {
	Message      string    `json:"message"`
	ActiveDomain string    `json:"active_domain"`
	UserScope    string    `json:"user_scope"`
	Time         time.Time `json:"time"`
	Limit        int       `json:"limit"`
	Category     string    `json:"-"`
}

// CategoryRecallRecord is the source-owned record before prompt policy is
// applied. Source implementations must preserve provenance and lifecycle
// state instead of converting an unavailable or unvalidated record into text.
type CategoryRecallRecord struct {
	Category       string    `json:"category"`
	SourceID       string    `json:"source_id"`
	RecordID       string    `json:"record_id"`
	Title          string    `json:"title"`
	Summary        string    `json:"summary"`
	ProvenanceURLs []string  `json:"provenance_urls"`
	RetrievedAt    time.Time `json:"retrieved_at"`
	ValidatedAt    time.Time `json:"validated_at"`
	FreshUntil     time.Time `json:"fresh_until"`
	State          string    `json:"state"`
	Sensitivity    string    `json:"sensitivity"`
	Scope          string    `json:"scope"`
	Roles          []string  `json:"roles"`
	Score          float64   `json:"score"`
}

func (r CategoryRecallRecord) ToPromptText() string {
	return categorySnippetFromRecord(r).ToPromptText()
}

// CategoryRecallFailure is a machine-readable source or validation failure.
type CategoryRecallFailure struct {
	Category   string    `json:"category"`
	SourceID   string    `json:"source_id"`
	RecordID   string    `json:"record_id"`
	Code       string    `json:"code"`
	State      string    `json:"state"`
	Reason     string    `json:"reason"`
	Retryable  bool      `json:"retryable"`
	ObservedAt time.Time `json:"observed_at"`
}

type CategoryRecallResult struct {
	Records            []CategoryRecallRecord  `json:"records"`
	Failures           []CategoryRecallFailure `json:"failures"`
	SelectedCategories []string                `json:"selected_categories"`
	SourcesQueried     []string                `json:"sources_queried"`
	Status             string                  `json:"status"`
}

// CategoryRecallSource is the only interface a category DB exposes to CORE's
// deterministic registry. Implementations must be read-only for Search.
type CategoryRecallSource interface {
	ID() string
	Categories() []string
	Search(context.Context, CategoryRecallQuery) (CategoryRecallResult, error)
}

// CategoryRecallRegistry selects sources without querying every DB or using a
// generative model. The engine depends on this narrow port so tests can prove
// source selection independently from storage adapters.
type CategoryRecallRegistry interface {
	Recall(context.Context, CategoryRecallQuery) (CategoryRecallResult, error)
}

type DeterministicCategoryRecallRegistry struct {
	sources     []CategoryRecallSource
	markers     map[string][]string
	entityHints map[string][]string
	now         func() time.Time
}

type StaticCategoryRecallRegistry = DeterministicCategoryRecallRegistry

func NewCategoryRecallRegistry(sources ...CategoryRecallSource) *DeterministicCategoryRecallRegistry {
	registry := &DeterministicCategoryRecallRegistry{
		markers:     defaultCategoryRecallMarkers(),
		entityHints: map[string][]string{},
		now:         time.Now,
	}
	for _, source := range sources {
		registry.Register(source)
	}
	return registry
}

// NewStaticCategoryRecallRegistry is an explicit alias for callers that want
// the no-LLM, static-selection contract in the constructor name.
func NewStaticCategoryRecallRegistry(sources ...CategoryRecallSource) *DeterministicCategoryRecallRegistry {
	return NewCategoryRecallRegistry(sources...)
}

func (r *DeterministicCategoryRecallRegistry) Register(source CategoryRecallSource) *DeterministicCategoryRecallRegistry {
	if r == nil || source == nil {
		return r
	}
	for _, existing := range r.sources {
		if existing != nil && strings.EqualFold(strings.TrimSpace(existing.ID()), strings.TrimSpace(source.ID())) {
			return r
		}
	}
	r.sources = append(r.sources, source)
	return r
}

func (r *DeterministicCategoryRecallRegistry) SetMarkers(markers map[string][]string) *DeterministicCategoryRecallRegistry {
	if r == nil {
		return r
	}
	r.markers = cloneCategorySignals(markers)
	return r
}

func (r *DeterministicCategoryRecallRegistry) SetEntityHints(hints map[string][]string) *DeterministicCategoryRecallRegistry {
	if r == nil {
		return r
	}
	r.entityHints = cloneCategorySignals(hints)
	return r
}

func (r *DeterministicCategoryRecallRegistry) SetNow(now func() time.Time) *DeterministicCategoryRecallRegistry {
	if r == nil {
		return r
	}
	if now == nil {
		r.now = time.Now
	} else {
		r.now = now
	}
	return r
}

func (r *DeterministicCategoryRecallRegistry) Recall(ctx context.Context, query CategoryRecallQuery) (CategoryRecallResult, error) {
	if r == nil {
		return CategoryRecallResult{Status: CategoryRecallStatusNotSelected}, nil
	}
	if query.Time.IsZero() {
		query.Time = r.clockNow()
	}
	if query.Limit <= 0 {
		query.Limit = 3
	}
	if query.Limit > 24 {
		query.Limit = 24
	}
	selected := r.selectCategories(query)
	result := CategoryRecallResult{
		SelectedCategories: selected,
		Status:             CategoryRecallStatusNotSelected,
	}
	if len(selected) == 0 {
		return result, nil
	}

	for _, category := range selected {
		for _, source := range r.sourcesForCategory(category) {
			if source == nil {
				continue
			}
			searchQuery := query
			searchQuery.Category = category
			searchResult, err := source.Search(ctx, searchQuery)
			result.SourcesQueried = appendUniqueString(result.SourcesQueried, source.ID())
			for _, failure := range searchResult.Failures {
				result.Failures = append(result.Failures, normalizeCategoryFailure(failure, category, source.ID(), query.Time))
			}
			for _, record := range searchResult.Records {
				record = normalizeCategoryRecord(record, category, source.ID())
				if failure, ok := validateCategoryRecallRecord(record, searchQuery); ok {
					result.Failures = append(result.Failures, failure)
					continue
				}
				result.Records = append(result.Records, record)
			}
			if err != nil {
				result.Failures = append(result.Failures, CategoryRecallFailure{
					Category:   category,
					SourceID:   source.ID(),
					Code:       CategoryRecallFailureSourceUnavailable,
					State:      "unavailable",
					Reason:     err.Error(),
					Retryable:  true,
					ObservedAt: query.Time,
				})
			}
		}
	}

	if len(result.Failures) > 0 {
		result.Status = CategoryRecallStatusPartial
		if len(result.Records) == 0 {
			result.Status = CategoryRecallStatusUnavailable
		}
	} else {
		result.Status = CategoryRecallStatusCompleted
	}
	if len(result.Records) > query.Limit {
		result.Records = result.Records[:query.Limit]
	}
	return result, nil
}

func (r *DeterministicCategoryRecallRegistry) clockNow() time.Time {
	if r.now == nil {
		return time.Now().UTC()
	}
	return r.now().UTC()
}

func (r *DeterministicCategoryRecallRegistry) selectCategories(query CategoryRecallQuery) []string {
	selected := map[string]bool{}
	if category := normalizeCategory(query.ActiveDomain); category != "" && category != "general" {
		selected[category] = true
	}
	message := strings.ToLower(strings.TrimSpace(query.Message))
	for category, signals := range r.markers {
		if categorySignalsMatch(message, signals) {
			selected[normalizeCategory(category)] = true
		}
	}
	for category, hints := range r.entityHints {
		if categorySignalsMatch(message, hints) {
			selected[normalizeCategory(category)] = true
		}
	}
	categories := make([]string, 0, len(selected))
	for category := range selected {
		if category != "" {
			categories = append(categories, category)
		}
	}
	sort.Strings(categories)
	return categories
}

func (r *DeterministicCategoryRecallRegistry) sourcesForCategory(category string) []CategoryRecallSource {
	out := make([]CategoryRecallSource, 0)
	for _, source := range r.sources {
		if source == nil {
			continue
		}
		for _, sourceCategory := range source.Categories() {
			if normalizeCategory(sourceCategory) == category || normalizeCategory(sourceCategory) == "all" {
				out = append(out, source)
				break
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

func normalizeCategoryRecord(record CategoryRecallRecord, category string, sourceID string) CategoryRecallRecord {
	if strings.TrimSpace(record.Category) == "" {
		record.Category = category
	} else {
		record.Category = normalizeCategory(record.Category)
	}
	if strings.TrimSpace(record.SourceID) == "" {
		record.SourceID = sourceID
	}
	record.ProvenanceURLs = append([]string(nil), record.ProvenanceURLs...)
	record.Roles = append([]string(nil), record.Roles...)
	return record
}

func validateCategoryRecallRecord(record CategoryRecallRecord, query CategoryRecallQuery) (CategoryRecallFailure, bool) {
	observedAt := query.Time
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	base := CategoryRecallFailure{Category: record.Category, SourceID: record.SourceID, RecordID: record.RecordID, State: record.State, ObservedAt: observedAt}
	if query.Category != "" && normalizeCategory(record.Category) != normalizeCategory(query.Category) {
		base.Code = CategoryRecallFailureInvalid
		base.Reason = "category record does not match selected category"
		return base, true
	}
	if record.RecordID == "" || strings.TrimSpace(record.Title) == "" || strings.TrimSpace(record.Summary) == "" || record.RetrievedAt.IsZero() || record.ValidatedAt.IsZero() {
		base.Code = CategoryRecallFailureInvalid
		base.Reason = "category record requires record_id, title, summary, retrieved_at and validated_at"
		return base, true
	}
	if len(record.ProvenanceURLs) == 0 {
		base.Code = CategoryRecallFailureMissingProvenance
		base.Reason = CategoryRecallFailureMissingProvenance
		return base, true
	}
	if !categoryRecordStateInjectable(record.State) {
		base.Code = CategoryRecallFailureInvalid
		base.Reason = CategoryRecallFailureInvalid
		return base, true
	}
	if !record.FreshUntil.IsZero() && !observedAt.Before(record.FreshUntil) {
		base.Code = CategoryRecallFailureStale
		base.Reason = CategoryRecallFailureStale
		return base, true
	}
	if scope := strings.ToLower(strings.TrimSpace(record.Scope)); scope != "" && scope != "public" && scope != "all" {
		userScope := strings.ToLower(strings.TrimSpace(query.UserScope))
		if userScope == "" || scope != userScope {
			base.Code = CategoryRecallFailureScopeDenied
			base.Reason = CategoryRecallFailureScopeDenied
			return base, true
		}
	}
	return CategoryRecallFailure{}, false
}

func categoryRecordStateInjectable(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case CategoryRecordStateValidated, CategoryRecordStateConfirmed, CategoryRecordStateApproved, CategoryRecordStateActive, "ready":
		return true
	default:
		return false
	}
}

func normalizeCategoryFailure(failure CategoryRecallFailure, category string, sourceID string, observedAt time.Time) CategoryRecallFailure {
	if strings.TrimSpace(failure.Category) == "" {
		failure.Category = category
	}
	if strings.TrimSpace(failure.SourceID) == "" {
		failure.SourceID = sourceID
	}
	if failure.ObservedAt.IsZero() {
		failure.ObservedAt = observedAt
	}
	return failure
}

func categorySignalsMatch(message string, signals []string) bool {
	if message == "" {
		return false
	}
	for _, signal := range signals {
		if signal != "" && strings.Contains(message, strings.ToLower(strings.TrimSpace(signal))) {
			return true
		}
	}
	return false
}

func cloneCategorySignals(values map[string][]string) map[string][]string {
	result := make(map[string][]string, len(values))
	for category, signals := range values {
		category = normalizeCategory(category)
		if category == "" {
			continue
		}
		for _, signal := range signals {
			signal = strings.ToLower(strings.TrimSpace(signal))
			if signal != "" {
				result[category] = append(result[category], signal)
			}
		}
	}
	return result
}

func defaultCategoryRecallMarkers() map[string][]string {
	return map[string][]string{
		"movie":      {"映画", "movie", "film"},
		"drama":      {"ドラマ", "drama"},
		"music":      {"音楽", "楽曲", "歌手", "アルバム", "music", "song", "artist"},
		"anime":      {"アニメ", "anime"},
		"novel":      {"小説", "novel"},
		"manga":      {"漫画", "マンガ", "manga"},
		"award":      {"受賞", "賞", "award"},
		"person":     {"俳優", "女優", "人物", "監督", "actor", "director"},
		"hobby":      {"趣味", "ホビー", "hobby"},
		"book":       {"本を", "本の", "書籍", "読書", "小説", "book"},
		"game":       {"ゲーム", "game"},
		"news":       {"ニュース", "報道", "news"},
		"investment": {"投資", "株", "相場", "銘柄", "investment"},
	}
}

func normalizeCategory(category string) string {
	category = strings.ToLower(strings.TrimSpace(category))
	switch category {
	case "movies", "film", "films":
		return "movie"
	case "television", "tv":
		return "drama"
	case "people", "human":
		return "person"
	case "hobbies":
		return "hobby"
	case "books":
		return "book"
	case "games":
		return "game"
	case "news_articles":
		return "news"
	case "investments", "finance":
		return "investment"
	default:
		return category
	}
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// CategorySnippet is the prompt-safe projection of a validated category
// record. It is intentionally not a conversation Message and is never stored
// by EndTurn.
type CategorySnippet struct {
	Category       string    `json:"category"`
	SourceID       string    `json:"source_id"`
	RecordID       string    `json:"record_id"`
	Title          string    `json:"title"`
	Summary        string    `json:"summary"`
	ProvenanceURLs []string  `json:"provenance_urls"`
	RetrievedAt    time.Time `json:"retrieved_at"`
	ValidatedAt    time.Time `json:"validated_at"`
	FreshUntil     time.Time `json:"fresh_until"`
	State          string    `json:"state"`
	Sensitivity    string    `json:"sensitivity"`
	Scope          string    `json:"scope"`
	Roles          []string  `json:"roles"`
	Score          float64   `json:"score"`
}

func (s CategorySnippet) ToPromptText() string {
	parts := []string{}
	if s.Category != "" {
		parts = append(parts, "category="+s.Category)
	}
	if s.SourceID != "" {
		parts = append(parts, "source_id="+s.SourceID)
	}
	if s.RecordID != "" {
		parts = append(parts, "record_id="+s.RecordID)
	}
	if s.Title != "" {
		parts = append(parts, "title="+s.Title)
	}
	if s.Summary != "" {
		parts = append(parts, "summary="+s.Summary)
	}
	if len(s.ProvenanceURLs) > 0 {
		parts = append(parts, "sources="+strings.Join(s.ProvenanceURLs, ", "))
	}
	if !s.RetrievedAt.IsZero() {
		parts = append(parts, "retrieved_at="+s.RetrievedAt.UTC().Format(time.RFC3339))
	}
	if !s.ValidatedAt.IsZero() {
		parts = append(parts, "validated_at="+s.ValidatedAt.UTC().Format(time.RFC3339))
	}
	if !s.FreshUntil.IsZero() {
		parts = append(parts, "fresh_until="+s.FreshUntil.UTC().Format(time.RFC3339))
	}
	return strings.Join(parts, "; ")
}

func categorySnippetFromRecord(record CategoryRecallRecord) CategorySnippet {
	return CategorySnippet{
		Category: record.Category, SourceID: record.SourceID, RecordID: record.RecordID,
		Title: record.Title, Summary: record.Summary,
		ProvenanceURLs: append([]string(nil), record.ProvenanceURLs...),
		RetrievedAt:    record.RetrievedAt, ValidatedAt: record.ValidatedAt, FreshUntil: record.FreshUntil,
		State: record.State, Sensitivity: record.Sensitivity, Scope: record.Scope,
		Roles: append([]string(nil), record.Roles...), Score: record.Score,
	}
}

// CategorySnippetFromRecord converts a source result into the prompt-safe
// projection used by RecallPack.
func CategorySnippetFromRecord(record CategoryRecallRecord) CategorySnippet {
	return categorySnippetFromRecord(record)
}
