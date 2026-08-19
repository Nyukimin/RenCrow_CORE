package personrelatedcatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const CollectionPlanRevision = "person-related-plan.v1/1"

const (
	StopReasonEnoughValidatedResults = "enough_validated_results"
	StopReasonAllSourcesTerminal     = "all_sources_terminal"
	StopReasonIdentityAmbiguous      = "identity_ambiguous"
	StopReasonUnavailable            = "unavailable"
)

// CollectionAttempt is the durable, source/category-level receipt. Times are
// kept as RFC3339 text so SQLite can use the same values for exact indexed
// freshness comparisons on every supported platform.
type CollectionAttempt struct {
	RunID                string `json:"run_id"`
	Source               string `json:"source"`
	Category             string `json:"category"`
	PersonRefID          string `json:"person_ref_id"`
	MovieCatalogPersonID string `json:"movie_catalog_person_id"`
	Status               string `json:"status"`
	ReasonCode           string `json:"reason_code,omitempty"`
	Retryable            bool   `json:"retryable"`
	RetryAfterSeconds    int    `json:"retry_after_seconds,omitempty"`
	RetrievedAt          string `json:"retrieved_at"`
	ExpiresAt            string `json:"expires_at"`
	PlanRevision         string `json:"plan_revision"`
	StopReason           string `json:"stop_reason,omitempty"`
	NextSource           string `json:"next_source,omitempty"`
	ItemCount            int    `json:"item_count,omitempty"`
}

type Attempt = CollectionAttempt

// CollectionPlanResult is returned by the runtime when a semantic provider
// result terminates without an artifact. It is deliberately not an error: the
// caller receives the source reason and the next applicable source decision.
type CollectionPlanResult struct {
	PlanRevision         string              `json:"plan_revision"`
	RunID                string              `json:"run_id"`
	PersonRefID          string              `json:"person_ref_id"`
	MovieCatalogPersonID string              `json:"movie_catalog_person_id"`
	Category             string              `json:"category"`
	Status               string              `json:"status"`
	ReasonCode           string              `json:"reason_code,omitempty"`
	StopReason           string              `json:"stop_reason"`
	NextSource           string              `json:"next_source,omitempty"`
	Attempts             []CollectionAttempt `json:"attempts"`
}

type PlanRequest struct {
	PersonRefID          string
	MovieCatalogPersonID string
	Categories           []string
	FreshAttempts        []CollectionAttempt
	Now                  time.Time
}

type Batch struct {
	Source     string   `json:"source"`
	Categories []string `json:"categories"`
	Tier       int      `json:"tier"`
	Priority   int      `json:"priority"`
	Reason     string   `json:"reason"`
}

type PlanBatch = Batch

type Plan struct {
	PlanRevision         string              `json:"plan_revision"`
	PersonRefID          string              `json:"person_ref_id"`
	MovieCatalogPersonID string              `json:"movie_catalog_person_id"`
	Categories           []string            `json:"categories"`
	Batches              []Batch             `json:"provider_batches"`
	Attempts             []CollectionAttempt `json:"attempts"`
	StopReason           string              `json:"stop_reason,omitempty"`
	NextSource           string              `json:"next_source,omitempty"`
}

type plannerSource struct {
	Source string
	Tier   int
	Reason string
}

type plannerCandidate struct {
	Source        string
	Category      string
	CategoryOrder int
	SourceOrder   int
	Tier          int
	Reason        string
}

type plannerBatch struct {
	Batch
	firstCategoryOrder int
	firstSourceOrder   int
}

var plannerCategoryOrder = map[string]int{
	CategoryDrama: 0,
	CategoryAward: 1,
	CategoryMusic: 2,
	CategoryAnime: 3,
	CategoryNovel: 4,
	CategoryManga: 5,
}

var plannerSourcesByCategory = map[string][]plannerSource{
	CategoryDrama: {
		{Source: "jpsearch", Tier: 2, Reason: "tier2_direct_relation"},
	},
	CategoryAward: {
		{Source: "wikidata_award", Tier: 2, Reason: "tier2_exact_wikidata_qid"},
		{Source: "japan_academy_prize", Tier: 3, Reason: "tier3_exact_name_match"},
	},
	CategoryMusic: {
		{Source: "musicbrainz", Tier: 2, Reason: "tier2_confirmed_mbid"},
	},
	CategoryAnime: {
		{Source: "mediaarts_db", Tier: 2, Reason: "tier2_direct_relation"},
	},
	CategoryNovel: {
		{Source: "ndl_bibliography", Tier: 2, Reason: "tier2_direct_relation"},
	},
	CategoryManga: {
		{Source: "mediaarts_db", Tier: 2, Reason: "tier2_direct_relation"},
	},
}

// BuildCollectionPlan deterministically expands one person/category request
// into fixed provider batches. Fresh receipts suppress only their exact
// source/category pair; another applicable source remains eligible.
func BuildCollectionPlan(request PlanRequest) (Plan, error) {
	personRefID := strings.TrimSpace(request.PersonRefID)
	personID := strings.TrimSpace(request.MovieCatalogPersonID)
	if personRefID == "" || personID == "" {
		return Plan{}, fmt.Errorf("%w: person reference and movie catalog person id are required", ErrInvalidArtifact)
	}
	categories, err := canonicalPlanCategories(request.Categories)
	if err != nil {
		return Plan{}, err
	}
	now := request.Now
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	plan := Plan{
		PlanRevision:         CollectionPlanRevision,
		PersonRefID:          personRefID,
		MovieCatalogPersonID: personID,
		Categories:           categories,
	}
	fresh := make(map[string]CollectionAttempt)
	for _, attempt := range request.FreshAttempts {
		if strings.TrimSpace(attempt.PersonRefID) != personRefID || strings.TrimSpace(attempt.MovieCatalogPersonID) != personID || !validHobbyCategory(attempt.Category) || !validCollectorCategory(attempt.Category) || strings.TrimSpace(attempt.Source) == "" {
			continue
		}
		expires, err := time.Parse(time.RFC3339, strings.TrimSpace(attempt.ExpiresAt))
		if err != nil || !expires.After(now) {
			continue
		}
		key := attempt.Category + "\x00" + strings.ToLower(strings.TrimSpace(attempt.Source))
		if previous, exists := fresh[key]; !exists || previous.RetrievedAt < attempt.RetrievedAt {
			fresh[key] = attempt
		}
	}
	for _, category := range categories {
		for _, source := range plannerSourcesByCategory[category] {
			key := category + "\x00" + source.Source
			if attempt, exists := fresh[key]; exists {
				plan.Attempts = append(plan.Attempts, attempt)
			}
		}
	}
	readyCategories := make(map[string]struct{}, len(categories))
	for _, attempt := range plan.Attempts {
		if normalizeCollectionStatus(attempt.Status) == CollectionStatusReady {
			readyCategories[attempt.Category] = struct{}{}
		}
	}

	candidates := make([]plannerCandidate, 0)
	for _, category := range categories {
		if _, ready := readyCategories[category]; ready {
			continue
		}
		for sourceOrder, source := range plannerSourcesByCategory[category] {
			key := category + "\x00" + source.Source
			if _, exists := fresh[key]; exists {
				continue
			}
			candidates = append(candidates, plannerCandidate{Source: source.Source, Category: category, CategoryOrder: plannerCategoryOrder[category], SourceOrder: sourceOrder, Tier: source.Tier, Reason: source.Reason})
		}
	}
	groups := make(map[string]*plannerBatch)
	for _, candidate := range candidates {
		batch := groups[candidate.Source]
		if batch == nil {
			batch = &plannerBatch{Batch: Batch{Source: candidate.Source, Tier: candidate.Tier, Priority: candidate.CategoryOrder*100 + candidate.SourceOrder, Reason: candidate.Reason}, firstCategoryOrder: candidate.CategoryOrder, firstSourceOrder: candidate.SourceOrder}
			groups[candidate.Source] = batch
		}
		if batch.Tier > candidate.Tier || candidate.Tier == batch.Tier && candidate.CategoryOrder < batch.firstCategoryOrder {
			batch.Tier = candidate.Tier
			batch.firstCategoryOrder = candidate.CategoryOrder
			batch.Priority = candidate.CategoryOrder*100 + candidate.SourceOrder
			batch.Reason = candidate.Reason
		}
		if candidate.CategoryOrder == batch.firstCategoryOrder && candidate.SourceOrder < batch.firstSourceOrder {
			batch.firstSourceOrder = candidate.SourceOrder
		}
		if !containsString(batch.Categories, candidate.Category) {
			batch.Categories = append(batch.Categories, candidate.Category)
		}
	}
	ordered := make([]*plannerBatch, 0, len(groups))
	for _, batch := range groups {
		sort.Slice(batch.Categories, func(i, j int) bool {
			return plannerCategoryOrder[batch.Categories[i]] < plannerCategoryOrder[batch.Categories[j]]
		})
		ordered = append(ordered, batch)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Tier != ordered[j].Tier {
			return ordered[i].Tier < ordered[j].Tier
		}
		if ordered[i].firstCategoryOrder != ordered[j].firstCategoryOrder {
			return ordered[i].firstCategoryOrder < ordered[j].firstCategoryOrder
		}
		if ordered[i].firstSourceOrder != ordered[j].firstSourceOrder {
			return ordered[i].firstSourceOrder < ordered[j].firstSourceOrder
		}
		return ordered[i].Source < ordered[j].Source
	})
	for _, batch := range ordered {
		plan.Batches = append(plan.Batches, batch.Batch)
	}
	if len(plan.Batches) == 0 {
		plan.StopReason = StopReasonAllSourcesTerminal
		for _, attempt := range plan.Attempts {
			if normalizeCollectionStatus(attempt.Status) == CollectionStatusReady {
				plan.StopReason = StopReasonEnoughValidatedResults
				break
			}
		}
	} else {
		plan.NextSource = plan.Batches[0].Source
	}
	return plan, nil
}

func BuildPlan(request PlanRequest) (Plan, error) {
	return BuildCollectionPlan(request)
}

func canonicalPlanCategories(categories []string) ([]string, error) {
	seen := make(map[string]struct{}, len(categories))
	result := make([]string, 0, len(categories))
	for _, category := range categories {
		category = strings.ToLower(strings.TrimSpace(category))
		if !validHobbyCategory(category) {
			return nil, fmt.Errorf("%w: category %q is not supported by collection planner", ErrInvalidArtifact, category)
		}
		if _, exists := seen[category]; exists {
			continue
		}
		seen[category] = struct{}{}
		result = append(result, category)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w: at least one category is required", ErrInvalidArtifact)
	}
	sort.Slice(result, func(i, j int) bool { return plannerCategoryOrder[result[i]] < plannerCategoryOrder[result[j]] })
	return result, nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// RecordCollectionAttempt writes one source/category receipt and assigns a
// positive or negative TTL when the caller did not provide an expiry.
func RecordCollectionAttempt(ctx context.Context, db *sql.DB, attempt CollectionAttempt) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := requireCollectionAttemptSchema(ctx, db); err != nil {
		return err
	}
	if err := validateCollectionAttempt(attempt); err != nil {
		return err
	}
	if strings.TrimSpace(attempt.PlanRevision) == "" {
		attempt.PlanRevision = CollectionPlanRevision
	}
	if strings.TrimSpace(attempt.RetrievedAt) == "" {
		attempt.RetrievedAt = time.Now().UTC().Format(time.RFC3339)
	}
	retrieved, err := time.Parse(time.RFC3339, attempt.RetrievedAt)
	if err != nil {
		return fmt.Errorf("%w: retrieved_at must be RFC3339", ErrInvalidArtifact)
	}
	if strings.TrimSpace(attempt.ExpiresAt) == "" {
		ttl := PositiveCollectionAttemptTTL
		if normalizeCollectionStatus(attempt.Status) != CollectionStatusReady {
			ttl = NegativeCollectionAttemptTTL
		}
		attempt.ExpiresAt = retrieved.Add(ttl).UTC().Format(time.RFC3339)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO hobby_collection_attempts(
  run_id,source,category,person_ref_id,movie_catalog_person_id,status,reason_code,
  retryable,retry_after_seconds,retrieved_at,expires_at,plan_revision,stop_reason,next_source,item_count,created_at,updated_at
)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)
ON CONFLICT(run_id,source,category) DO UPDATE SET
  person_ref_id=excluded.person_ref_id,
  movie_catalog_person_id=excluded.movie_catalog_person_id,
  status=excluded.status,
  reason_code=excluded.reason_code,
  retryable=excluded.retryable,
  retry_after_seconds=excluded.retry_after_seconds,
  retrieved_at=excluded.retrieved_at,
  expires_at=excluded.expires_at,
  plan_revision=excluded.plan_revision,
  stop_reason=excluded.stop_reason,
  next_source=excluded.next_source,
  item_count=excluded.item_count,
  updated_at=CURRENT_TIMESTAMP`,
		attempt.RunID, attempt.Source, attempt.Category, attempt.PersonRefID, attempt.MovieCatalogPersonID,
		canonicalAttemptStatus(attempt.Status), attempt.ReasonCode, boolInt(attempt.Retryable), attempt.RetryAfterSeconds,
		attempt.RetrievedAt, attempt.ExpiresAt, attempt.PlanRevision, attempt.StopReason, attempt.NextSource, attempt.ItemCount); err != nil {
		return fmt.Errorf("record collection attempt: %w", err)
	}
	return nil
}

func RecordAttempt(ctx context.Context, db *sql.DB, attempt Attempt) error {
	return RecordCollectionAttempt(ctx, db, attempt)
}

// LookupFreshCollectionAttempt returns only an exact person/category/source
// receipt whose expiry is after now. The named index is intentionally forced so
// the query cannot silently degrade to a full receipt scan.
func LookupFreshCollectionAttempt(ctx context.Context, db *sql.DB, personRefID, movieCatalogPersonID, category, source string, now time.Time) (CollectionAttempt, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := requireCollectionAttemptSchema(ctx, db); err != nil {
		return CollectionAttempt{}, false, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var attempt CollectionAttempt
	var retryable int
	err := db.QueryRowContext(ctx, `
SELECT run_id,source,category,person_ref_id,movie_catalog_person_id,status,reason_code,
       retryable,retry_after_seconds,retrieved_at,expires_at,plan_revision,stop_reason,next_source,item_count
FROM hobby_collection_attempts INDEXED BY idx_hobby_collection_attempts_person_category_source
WHERE person_ref_id=? AND movie_catalog_person_id=? AND category=? AND source=? AND expires_at>?
ORDER BY retrieved_at DESC LIMIT 1`, personRefID, movieCatalogPersonID, category, source, now.UTC().Format(time.RFC3339)).Scan(
		&attempt.RunID, &attempt.Source, &attempt.Category, &attempt.PersonRefID, &attempt.MovieCatalogPersonID,
		&attempt.Status, &attempt.ReasonCode, &retryable, &attempt.RetryAfterSeconds, &attempt.RetrievedAt,
		&attempt.ExpiresAt, &attempt.PlanRevision, &attempt.StopReason, &attempt.NextSource, &attempt.ItemCount)
	if errors.Is(err, sql.ErrNoRows) {
		return CollectionAttempt{}, false, nil
	}
	if err != nil {
		return CollectionAttempt{}, false, fmt.Errorf("lookup fresh collection attempt: %w", err)
	}
	attempt.Retryable = retryable != 0
	return attempt, true, nil
}

func FreshCollectionAttempt(ctx context.Context, db *sql.DB, personRefID, movieCatalogPersonID, category, source string, now time.Time) (CollectionAttempt, bool, error) {
	return LookupFreshCollectionAttempt(ctx, db, personRefID, movieCatalogPersonID, category, source, now)
}

func validateCollectionAttempt(attempt CollectionAttempt) error {
	if strings.TrimSpace(attempt.RunID) == "" || strings.TrimSpace(attempt.Source) == "" || strings.TrimSpace(attempt.PersonRefID) == "" || strings.TrimSpace(attempt.MovieCatalogPersonID) == "" || !validHobbyCategory(attempt.Category) || !contractFreeSourceAllowed(attempt.Category, attempt.Source) {
		return fmt.Errorf("%w: collection attempt identity is invalid", ErrInvalidArtifact)
	}
	if !validCollectionStatus(normalizeCollectionStatus(attempt.Status)) && strings.TrimSpace(attempt.Status) != "error" {
		return fmt.Errorf("%w: collection attempt status %q is invalid", ErrInvalidArtifact, attempt.Status)
	}
	return nil
}

func canonicalAttemptStatus(status string) string {
	if normalized := normalizeCollectionStatus(status); normalized != "" {
		return normalized
	}
	return status
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func requireCollectionAttemptSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("%w: hobby database is nil", ErrUnavailable)
	}
	if err := requireObjects(ctx, db, []string{"hobby_collection_attempts"}, []string{"idx_hobby_collection_attempts_person_category_source"}); err != nil {
		return err
	}
	return nil
}
