package l1sqlite

import (
	"context"
	"time"

	domconv "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
	_ "github.com/mattn/go-sqlite3"
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
