package l1sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	_ "modernc.org/sqlite"
)

type L1MemoryEvent struct {
	ID          string
	Namespace   string
	SessionID   string
	ThreadID    int64
	Speaker     domconv.Speaker
	Message     string
	Meta        map[string]interface{}
	MemoryState string
	Layer       string
	Source      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

var (
	// ErrL1RawLifecycleArchiveUnavailable means that a selected raw event could
	// not reach the canonical Conversation Archive boundary. The source row is
	// retained and the durable outbox records the failed attempt.
	ErrL1RawLifecycleArchiveUnavailable = errors.New("l1 raw lifecycle archive unavailable")
	// ErrL1RawLifecycleArchiveConflict means that the source or archive binding
	// no longer matches the event hash captured by the outbox.
	ErrL1RawLifecycleArchiveConflict = errors.New("l1 raw lifecycle archive conflict")
)

const (
	L1RawLifecycleArchiveOutboxStatusPending  = "pending"
	L1RawLifecycleArchiveOutboxStatusFailed   = "failed"
	L1RawLifecycleArchiveOutboxStatusArchived = "archived"
)

// L1RawLifecycleArchiveReceipt binds an archive write to the exact L1 event
// and durable outbox entry. It contains identity and hash only; the source
// event remains in L1 until the finalize transaction deletes it.
type L1RawLifecycleArchiveReceipt struct {
	OutboxID    string
	EventID     string
	EventSHA256 string
	CreatedAt   time.Time
}

// L1RawLifecycleArchiveStore is the optional archive boundary used by raw
// conversation lifecycle maintenance. Its implementation must commit the
// exact archive row and receipt atomically and must never mutate L1.
type L1RawLifecycleArchiveStore interface {
	ArchiveL1RawLifecycleEvent(context.Context, L1MemoryEvent, L1RawLifecycleArchiveReceipt) error
}

// CanonicalL1MemoryEventBytes returns the stable full-event representation
// used for raw lifecycle archive binding. Metadata is canonical JSON and
// timestamps are UTC RFC3339Nano strings; volatile values are excluded.
func CanonicalL1MemoryEventBytes(item L1MemoryEvent) ([]byte, error) {
	meta := item.Meta
	if meta == nil {
		meta = map[string]interface{}{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal canonical l1 memory metadata: %w", err)
	}
	canonical := struct {
		ID          string          `json:"id"`
		Namespace   string          `json:"namespace"`
		SessionID   string          `json:"session_id"`
		ThreadID    int64           `json:"thread_id"`
		Speaker     string          `json:"speaker"`
		Message     string          `json:"message"`
		Meta        json.RawMessage `json:"meta"`
		MemoryState string          `json:"memory_state"`
		Layer       string          `json:"layer"`
		Source      string          `json:"source"`
		CreatedAt   string          `json:"created_at"`
		UpdatedAt   string          `json:"updated_at"`
	}{
		ID:          item.ID,
		Namespace:   item.Namespace,
		SessionID:   item.SessionID,
		ThreadID:    item.ThreadID,
		Speaker:     string(item.Speaker),
		Message:     item.Message,
		Meta:        json.RawMessage(metaJSON),
		MemoryState: item.MemoryState,
		Layer:       item.Layer,
		Source:      item.Source,
		CreatedAt:   canonicalL1Timestamp(item.CreatedAt),
		UpdatedAt:   canonicalL1Timestamp(item.UpdatedAt),
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, fmt.Errorf("failed to encode canonical l1 memory event: %w", err)
	}
	return encoded, nil
}

// CanonicalL1MemoryEventSHA256 returns the deterministic full-event hash used
// by the lifecycle outbox and the archive adapter.
func CanonicalL1MemoryEventSHA256(item L1MemoryEvent) (string, error) {
	encoded, err := CanonicalL1MemoryEventBytes(item)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// L1RawLifecycleOutboxID deterministically binds event identity and content
// hash without carrying volatile timestamps or retry state.
func L1RawLifecycleOutboxID(eventID, eventSHA256 string) string {
	binding := strings.TrimSpace(eventID) + "\x00" + strings.TrimSpace(eventSHA256)
	digest := sha256.Sum256([]byte(binding))
	return "raw-archive:" + hex.EncodeToString(digest[:])
}

func canonicalL1Timestamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

type L1SearchCacheEntry struct {
	QueryHash       string
	NormalizedQuery string
	Provider        string
	RawQuery        string
	ResultsJSON     string
	SourceURLs      []string
	RetrievedAt     time.Time
	ExpiresAt       time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type L1WebGatherFetchCacheEntry struct {
	CacheKey      string
	URL           string
	FetchProvider string
	Extractor     string
	Status        string
	ResponseJSON  string
	ErrorCode     string
	RetrievedAt   time.Time
	ExpiresAt     time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type L1WebGatherRateState struct {
	Domain      string
	LastFetchAt time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type L1NewsArticleFetch struct {
	NormalizedURL  string
	Status         string
	FinalURL       string
	FetchURL       string
	ContentType    string
	FetchProvider  string
	Extractor      string
	RawBytes       int64
	ArticleText    string
	ContentSHA256  string
	ErrorCode      string
	AttemptCount   int
	LeaseExpiresAt time.Time
	NextAttemptAt  time.Time
	CompletedAt    time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type L1NewsArticleFetchCompletion struct {
	FinalURL      string
	FetchURL      string
	ContentType   string
	FetchProvider string
	Extractor     string
	RawBytes      int64
	ArticleText   string
	ContentSHA256 string
}

type L1EventLogEntry struct {
	ID        string
	EventType string
	Namespace string
	SessionID string
	ThreadID  int64
	Payload   map[string]interface{}
	Source    string
	CreatedAt time.Time
}

type L1StagingItem struct {
	ID               string
	Kind             string
	Namespace        string
	EventID          string
	SourceID         string
	SourceURL        string
	FetchedAt        time.Time
	PublishedAt      time.Time
	RawText          string
	RawHash          string
	SummaryDraft     string
	Keywords         []string
	LicenseNote      string
	ValidationStatus string
	Meta             map[string]interface{}
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type L1StagingValidationPolicy struct {
	SourceTrustScores          map[string]float64
	MinimumTrustScore          float64
	Now                        time.Time
	AutoPromoteMemoryCandidate bool
}

type L1StagingValidationIssue struct {
	Code    string
	Message string
}

type L1StagingValidationResult struct {
	ItemID            string
	Passed            bool
	Status            string
	Issues            []L1StagingValidationIssue
	PromotedMemoryID  string
	PromotedNamespace string
}

func (r L1StagingValidationResult) HasIssue(code string) bool {
	for _, issue := range r.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

type L1SourceRegistryEntry struct {
	SourceID      string
	URL           string
	Kind          string
	TrustScore    float64
	FetchInterval time.Duration
	LicenseNote   string
	Enabled       bool
	Meta          map[string]interface{}
	LastFetchedAt time.Time
	LastStatus    string
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type L1SourceFetchPayload struct {
	EventID      string
	SourceURL    string
	FetchedAt    time.Time
	PublishedAt  time.Time
	RawText      string
	SummaryDraft string
	Keywords     []string
	Meta         map[string]interface{}
}

type WikiPageIndexItem struct {
	PageID          string
	Path            string
	Title           string
	Type            string
	Status          string
	Owner           string
	CanonicalSource string
	SourcePaths     []string
	Related         []string
	Summary         string
	ContentHash     string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type L1DomainGraphAssertion struct {
	ID               string
	StagingID        string
	Domain           string
	EntityType       string
	EntityID         string
	RelationType     string
	SourceID         string
	SourceURL        string
	RawHash          string
	Summary          string
	Confidence       float64
	ValidationStatus string
	Evidence         map[string]interface{}
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type DomainGraphAssertionQuery struct {
	Domain           string
	EntityType       string
	EntityID         string
	RelationType     string
	SourceID         string
	ValidationStatus string
	Limit            int
	Offset           int
}

type L1NewsItem struct {
	ID           string
	StagingID    string
	Category     string
	SourceID     string
	SourceURL    string
	PublishedAt  time.Time
	FetchedAt    time.Time
	RawText      string
	RawHash      string
	SummaryDraft string
	Keywords     []string
	LicenseNote  string
	Meta         map[string]interface{}
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type L1DailyDigest struct {
	ID         string
	DigestDate string
	Category   string
	DigestSlot string
	NewsIDs    []string
	DigestText string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type L1MonthlyHighlight struct {
	ID        string
	Month     string
	Category  string
	SourceIDs []string
	Highlight string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type L1KnowledgeItem struct {
	ID           string
	StagingID    string
	Domain       string
	Title        string
	SourceID     string
	SourceURL    string
	RawText      string
	RawHash      string
	SummaryDraft string
	Keywords     []string
	LicenseNote  string
	Meta         map[string]interface{}
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type L1KnowledgeEntity struct {
	EntityID      string
	CanonicalName string
	EntityType    string
	Aliases       []string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type L1KnowledgeItemEntity struct {
	ItemID       string
	EntityID     string
	RelationKind string
	Score        float64
	Evidence     string
	CreatedAt    time.Time
}

type L1KnowledgeItemRelation struct {
	SrcItemID    string
	DstItemID    string
	RelationType string
	Score        float64
	Evidence     string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type L1KnowledgeRelationHit struct {
	Item         L1KnowledgeItem
	Hop          int
	ViaItemID    string
	RelationType string
	Score        float64
	Evidence     string
}

type KnowledgeRelationSummary struct {
	EntityCount     int `json:"entity_count"`
	ItemEntityCount int `json:"item_entity_count"`
	RelationCount   int `json:"relation_count"`
	MaxHop          int `json:"max_hop"`
}

type L1ArchiveStore interface {
	ArchiveL1MemoryEvents(ctx context.Context, items []L1MemoryEvent) error
	ArchiveL1NewsItems(ctx context.Context, items []L1NewsItem) error
	ArchiveL1KnowledgeItems(ctx context.Context, items []L1KnowledgeItem) error
	ArchiveL1StagingItems(ctx context.Context, items []L1StagingItem) error
}

type DailyDigestSummarizer interface {
	SummarizeDailyDigest(ctx context.Context, digestDate time.Time, category string, slot string, news []L1NewsItem) (string, error)
}

type L1KnowledgeVectorSink interface {
	SaveL1KnowledgeItem(ctx context.Context, item L1KnowledgeItem) error
}

type L1VectorCleanupItem struct {
	MemoryID     string
	Namespace    string
	SupersededBy string
	Reason       string
}

type L1VectorCleanupResult struct {
	Deleted int
}

type L1VectorCleanupSink interface {
	CleanupMemoryVectors(ctx context.Context, items []L1VectorCleanupItem) (*L1VectorCleanupResult, error)
}
