package webgather

import (
	"context"
	"time"
)

type FetchRequest struct {
	URL             string      `json:"url"`
	Namespace       string      `json:"namespace"`
	SourceID        string      `json:"source_id"`
	FetchProvider   string      `json:"fetch_provider"`
	Extractor       string      `json:"extractor"`
	StoreStaging    bool        `json:"store_staging"`
	StoreStagingSet bool        `json:"-"`
	Refresh         bool        `json:"refresh"`
	DryRun          bool        `json:"dry_run"`
	LicenseNote     string      `json:"license_note"`
	Policy          FetchPolicy `json:"policy"`
}

type SearchRequest struct {
	Query     string `json:"query"`
	Provider  string `json:"provider"`
	Limit     int    `json:"limit"`
	Language  string `json:"language"`
	Freshness string `json:"freshness"`
	Namespace string `json:"namespace"`
	Refresh   bool   `json:"refresh"`
}

type SearchResult struct {
	URL          string `json:"url"`
	Title        string `json:"title"`
	Snippet      string `json:"snippet"`
	Rank         int    `json:"rank"`
	SourceEngine string `json:"source_engine"`
}

type SearchResponse struct {
	Query       string         `json:"query"`
	Provider    string         `json:"provider"`
	Results     []SearchResult `json:"results"`
	Diagnostics map[string]any `json:"diagnostics"`
}

type SearchAndFetchRequest struct {
	Query           string      `json:"query"`
	Provider        string      `json:"provider"`
	Limit           int         `json:"limit"`
	MaxFetches      int         `json:"max_fetches"`
	Language        string      `json:"language"`
	Freshness       string      `json:"freshness"`
	Namespace       string      `json:"namespace"`
	Refresh         bool        `json:"refresh"`
	FetchProvider   string      `json:"fetch_provider"`
	Extractor       string      `json:"extractor"`
	StoreStaging    bool        `json:"store_staging"`
	StoreStagingSet bool        `json:"-"`
	Policy          FetchPolicy `json:"policy"`
}

type SearchAndFetchItem struct {
	SearchResult SearchResult  `json:"search_result"`
	Fetch        FetchResponse `json:"fetch"`
}

type SearchAndFetchResponse struct {
	Query       string               `json:"query"`
	Provider    string               `json:"provider"`
	Items       []SearchAndFetchItem `json:"items"`
	Diagnostics map[string]any       `json:"diagnostics"`
}

type FetchResponse struct {
	URL         string `json:"url"`
	FinalURL    string `json:"final_url,omitempty"`
	Status      string `json:"status"`
	HTTPStatus  int    `json:"http_status,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Title       string `json:"title,omitempty"`
	TextPreview string `json:"text_preview,omitempty"`
	// ExtractedText は同一プロセス内の一次情報処理だけで使う全文であり、
	// API応答・ツール出力・永続キャッシュには含めない。
	ExtractedText    string         `json:"-"`
	RawHash          string         `json:"raw_hash,omitempty"`
	RawBytes         int64          `json:"raw_bytes,omitempty"`
	ExtractedChars   int            `json:"extracted_chars,omitempty"`
	StagingID        string         `json:"staging_id,omitempty"`
	ValidationStatus string         `json:"validation_status,omitempty"`
	SecurityWarnings []string       `json:"security_warnings,omitempty"`
	ErrorCode        ErrorCode      `json:"error_code,omitempty"`
	ErrorMessage     string         `json:"error_message,omitempty"`
	Diagnostics      map[string]any `json:"diagnostics,omitempty"`
}

type FetchArtifact struct {
	OriginalURL  string
	FinalURL     string
	StatusCode   int
	ContentType  string
	Body         []byte
	RawBytes     int64
	Elapsed      time.Duration
	Redirects    []string
	FetchedAt    time.Time
	ProviderName string
	Meta         map[string]any
}

type ExtractedDocument struct {
	Text         string
	Title        string
	Excerpt      string
	Byline       string
	SiteName     string
	CanonicalURL string
	PublishedAt  time.Time
	Keywords     []string
	Meta         map[string]any
	Extractor    string
}

type StagingRecord struct {
	ID               string
	ValidationStatus string
	RawHash          string
}

type FetchProvider interface {
	Fetch(ctx context.Context, url string, policy FetchPolicy) (FetchArtifact, error)
}

type Extractor interface {
	Extract(ctx context.Context, artifact FetchArtifact, requestedExtractor string) (ExtractedDocument, error)
}

type StagingWriter interface {
	Save(ctx context.Context, req FetchRequest, artifact FetchArtifact, doc ExtractedDocument, meta map[string]any) (StagingRecord, error)
}

type SearchProvider interface {
	Search(ctx context.Context, req SearchRequest) (SearchResponse, error)
}
