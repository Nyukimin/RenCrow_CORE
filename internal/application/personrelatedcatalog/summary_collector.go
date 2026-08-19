package personrelatedcatalog

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const summaryCollectorEndpointPath = "/v1/person-related-catalog/summaries"

type SummaryTarget struct {
	Category       string `json:"category"`
	ItemID         string `json:"item_id"`
	Source         string `json:"source"`
	SourceRecordID string `json:"source_record_id"`
	CanonicalURL   string `json:"canonical_url"`
	ISBN           string `json:"isbn,omitempty"`
	MBID           string `json:"mbid,omitempty"`
	MediaArtsID    string `json:"mediaarts_id,omitempty"`
	JapanSearchID  string `json:"jpsearch_id,omitempty"`
	NDLID          string `json:"ndl_id,omitempty"`
}

type SummaryCollectionRequest struct {
	RequestID string          `json:"request_id"`
	Targets   []SummaryTarget `json:"targets"`
}

type SummaryCollectionResult struct {
	Status        string
	ReasonCode    string
	Retryable     bool
	RetryAfter    time.Duration
	RetrievedAt   string
	Source        string
	ArtifactURL   string
	ArtifactHash  string
	ArtifactBytes int64
	Patches       []SummaryPatch
}

type SummaryCollector interface {
	CollectSummaries(context.Context, SummaryCollectionRequest) (SummaryCollectionResult, error)
}

type HTTPSummaryCollector struct{ transport *HTTPCollector }

func NewHTTPSummaryCollector(baseURL string, timeout time.Duration) *HTTPSummaryCollector {
	return &HTTPSummaryCollector{transport: NewHTTPCollector(baseURL, timeout)}
}

type summaryCollectorResponse struct {
	Status            string `json:"status"`
	Source            string `json:"source"`
	ReasonCode        string `json:"reason_code"`
	Retryable         bool   `json:"retryable"`
	RetryAfterSeconds int    `json:"retry_after_seconds"`
	RetrievedAt       string `json:"retrieved_at"`
	ArtifactURL       string `json:"artifact_url"`
	ArtifactSHA256    string `json:"artifact_sha256"`
	ArtifactBytes     int64  `json:"artifact_bytes"`
}

func (c *HTTPSummaryCollector) CollectSummaries(ctx context.Context, request SummaryCollectionRequest) (SummaryCollectionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || c.transport == nil || strings.TrimSpace(c.transport.baseURL) == "" {
		return SummaryCollectionResult{}, fmt.Errorf("%w: summary provider is not configured", ErrCollectorUnavailable)
	}
	request, err := normalizeSummaryCollectionRequest(request)
	if err != nil {
		return SummaryCollectionResult{}, err
	}
	origin, err := collectorOrigin(c.transport.baseURL)
	if err != nil {
		return SummaryCollectionResult{}, fmt.Errorf("%w: invalid provider URL: %v", ErrCollectorProtocol, err)
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return SummaryCollectionResult{}, fmt.Errorf("%w: encode summary request: %v", ErrCollectorProtocol, err)
	}
	wire, err := c.post(ctx, origin, payload)
	if err != nil {
		return SummaryCollectionResult{}, err
	}
	status := normalizeCollectionStatus(wire.Status)
	if !validCollectionStatus(status) {
		return SummaryCollectionResult{}, fmt.Errorf("%w: summary status %q is invalid", ErrCollectorProtocol, wire.Status)
	}
	if wire.ArtifactBytes < 1 || wire.ArtifactBytes > maxCollectorArtifactBytes {
		return SummaryCollectionResult{}, fmt.Errorf("%w: summary artifact_bytes is invalid", ErrCollectorProtocol)
	}
	expectedHash := strings.ToLower(strings.TrimSpace(wire.ArtifactSHA256))
	if len(expectedHash) != sha256.Size*2 {
		return SummaryCollectionResult{}, fmt.Errorf("%w: summary artifact_sha256 is invalid", ErrCollectorProtocol)
	}
	if _, err := hex.DecodeString(expectedHash); err != nil {
		return SummaryCollectionResult{}, fmt.Errorf("%w: summary artifact_sha256 is invalid", ErrCollectorProtocol)
	}
	artifactURL, err := resolveCollectorURL(origin, wire.ArtifactURL)
	if err != nil {
		return SummaryCollectionResult{}, fmt.Errorf("%w: summary artifact URL: %v", ErrCollectorProtocol, err)
	}
	artifact, err := c.transport.download(ctx, artifactURL, expectedHash, wire.ArtifactBytes)
	if err != nil {
		return SummaryCollectionResult{}, err
	}
	patches, err := validateSummaryArtifact(artifact, request)
	if err != nil {
		return SummaryCollectionResult{}, err
	}
	return SummaryCollectionResult{
		Status: status, ReasonCode: strings.TrimSpace(wire.ReasonCode), Retryable: wire.Retryable,
		RetryAfter: time.Duration(wire.RetryAfterSeconds) * time.Second, RetrievedAt: strings.TrimSpace(wire.RetrievedAt),
		Source: strings.TrimSpace(wire.Source), ArtifactURL: artifactURL, ArtifactHash: expectedHash,
		ArtifactBytes: int64(len(artifact)), Patches: patches,
	}, nil
}

func normalizeSummaryCollectionRequest(request SummaryCollectionRequest) (SummaryCollectionRequest, error) {
	request.RequestID = strings.TrimSpace(request.RequestID)
	if request.RequestID == "" || len(request.Targets) < 1 || len(request.Targets) > 20 {
		return SummaryCollectionRequest{}, fmt.Errorf("%w: summary request requires 1..20 targets", ErrCollectorProtocol)
	}
	seen := make(map[string]struct{}, len(request.Targets))
	for index := range request.Targets {
		target := &request.Targets[index]
		target.Category = strings.ToLower(strings.TrimSpace(target.Category))
		target.ItemID = strings.TrimSpace(target.ItemID)
		target.Source = strings.ToLower(strings.TrimSpace(target.Source))
		target.SourceRecordID = strings.TrimSpace(target.SourceRecordID)
		target.CanonicalURL = strings.TrimSpace(target.CanonicalURL)
		// Wikidata canonical entity URIs are stored in their http:// form by
		// the award collection; the summary route uses their https form.
		if target.Source == "wikidata_award" && strings.HasPrefix(target.CanonicalURL, "http://") {
			target.CanonicalURL = "https://" + strings.TrimPrefix(target.CanonicalURL, "http://")
		}
		if !validCollectorCategory(target.Category) || target.ItemID == "" || target.SourceRecordID == "" || !contractFreeSourceAllowed(target.Category, target.Source) {
			return SummaryCollectionRequest{}, fmt.Errorf("%w: summary target %d is invalid", ErrCollectorProtocol, index)
		}
		parsed, err := url.Parse(target.CanonicalURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return SummaryCollectionRequest{}, fmt.Errorf("%w: summary target %d canonical URL is invalid", ErrCollectorProtocol, index)
		}
		key := summaryTargetKey(*target)
		if _, ok := seen[key]; ok {
			return SummaryCollectionRequest{}, fmt.Errorf("%w: duplicate summary target", ErrCollectorProtocol)
		}
		seen[key] = struct{}{}
	}
	return request, nil
}

func (c *HTTPSummaryCollector) post(ctx context.Context, origin *url.URL, payload []byte) (summaryCollectorResponse, error) {
	endpoint := origin.ResolveReference(&url.URL{Path: summaryCollectorEndpointPath}).String()
	var lastErr error
	for attempt := 1; attempt <= c.transport.maxAttempts; attempt++ {
		wire, retryAfter, retryable, err := c.postOnce(ctx, endpoint, payload)
		if err == nil {
			return wire, nil
		}
		lastErr = err
		if !retryable || attempt == c.transport.maxAttempts {
			break
		}
		if err := c.transport.wait(ctx, retryAfterOrBackoff(retryAfter, attempt)); err != nil {
			return summaryCollectorResponse{}, err
		}
	}
	return summaryCollectorResponse{}, lastErr
}

func (c *HTTPSummaryCollector) postOnce(ctx context.Context, endpoint string, payload []byte) (summaryCollectorResponse, time.Duration, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return summaryCollectorResponse{}, 0, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.transport.httpClient.Do(req)
	if err != nil {
		return summaryCollectorResponse{}, 0, temporaryNetworkError(err), fmt.Errorf("summary provider request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCollectorResponse+1))
	if err != nil {
		return summaryCollectorResponse{}, 0, temporaryNetworkError(err), err
	}
	if len(body) > maxCollectorResponse {
		return summaryCollectorResponse{}, 0, false, fmt.Errorf("%w: summary response is too large", ErrCollectorProtocol)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return summaryCollectorResponse{}, parseRetryAfter(resp.Header.Get("Retry-After"), c.transport.now()), retryable, fmt.Errorf("%w: summary provider status %s", ErrCollectorProtocol, resp.Status)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var wire summaryCollectorResponse
	if err := decoder.Decode(&wire); err != nil {
		return summaryCollectorResponse{}, 0, false, fmt.Errorf("%w: decode summary response: %v", ErrCollectorProtocol, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return summaryCollectorResponse{}, 0, false, fmt.Errorf("%w: summary response has trailing data", ErrCollectorProtocol)
	}
	return wire, 0, false, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("expected EOF")
	}
	return nil
}

type summaryArtifactManifest struct {
	SchemaVersion    string `json:"schema_version"`
	RecordType       string `json:"record_type"`
	RunID            string `json:"run_id"`
	RequestID        string `json:"request_id"`
	Source           string `json:"source"`
	RetrievedAt      string `json:"retrieved_at"`
	Status           string `json:"status"`
	ReasonCode       string `json:"reason_code"`
	ItemCount        int    `json:"item_count"`
	ReadyCount       int    `json:"ready_count"`
	UnavailableCount int    `json:"unavailable_count"`
}

type summaryArtifactPatch struct {
	SchemaVersion          string `json:"schema_version"`
	RecordType             string `json:"record_type"`
	Category               string `json:"category"`
	ItemID                 string `json:"item_id"`
	Source                 string `json:"source"`
	SourceRecordID         string `json:"source_record_id"`
	CanonicalURL           string `json:"canonical_url"`
	EvidenceURL            string `json:"evidence_url"`
	DescriptionOriginal    string `json:"description_original"`
	DescriptionLanguage    string `json:"description_language"`
	DescriptionJA          string `json:"description_ja"`
	DescriptionTranslation string `json:"description_translation_state"`
	SourceStatus           string `json:"source_status"`
	TranslationStatus      string `json:"translation_status"`
	RetrievedAt            string `json:"retrieved_at"`
	ContentSHA256          string `json:"content_sha256"`
	Rights                 string `json:"rights"`
	Reason                 string `json:"reason"`
}

func validateSummaryArtifact(artifact []byte, request SummaryCollectionRequest) ([]SummaryPatch, error) {
	scanner := bufio.NewScanner(bytes.NewReader(artifact))
	scanner.Buffer(make([]byte, 64*1024), 2<<20)
	line := 0
	var manifest summaryArtifactManifest
	patches := make([]SummaryPatch, 0, len(request.Targets))
	wanted := make(map[string]SummaryTarget, len(request.Targets))
	for _, target := range request.Targets {
		wanted[summaryTargetKey(target)] = target
	}
	seen := make(map[string]struct{}, len(request.Targets))
	readyCount := 0
	unavailableCount := 0
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		line++
		if line == 1 {
			if err := decodeStrictJSON(scanner.Bytes(), &manifest); err != nil {
				return nil, fmt.Errorf("%w: summary manifest: %v", ErrCollectorProtocol, err)
			}
			if manifest.SchemaVersion != SchemaVersion || manifest.RecordType != "summary_manifest" || manifest.RequestID != request.RequestID || manifest.RunID == "" || manifest.ItemCount != len(request.Targets) {
				return nil, fmt.Errorf("%w: summary manifest does not match request", ErrCollectorProtocol)
			}
			continue
		}
		var row summaryArtifactPatch
		if err := decodeStrictJSON(scanner.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("%w: summary patch: %v", ErrCollectorProtocol, err)
		}
		if row.SchemaVersion != SchemaVersion || row.RecordType != "summary_patch" {
			return nil, fmt.Errorf("%w: summary patch schema is invalid", ErrCollectorProtocol)
		}
		key := summaryTargetKey(SummaryTarget{Category: row.Category, ItemID: row.ItemID, Source: row.Source, SourceRecordID: row.SourceRecordID, CanonicalURL: row.CanonicalURL})
		if _, ok := wanted[key]; !ok {
			return nil, fmt.Errorf("%w: summary patch target mismatch", ErrCollectorProtocol)
		}
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("%w: duplicate summary patch", ErrCollectorProtocol)
		}
		seen[key] = struct{}{}
		patch := SummaryPatch{Category: row.Category, ItemID: row.ItemID, Source: row.Source, DescriptionOriginal: row.DescriptionOriginal, DescriptionLanguage: row.DescriptionLanguage, DescriptionJA: row.DescriptionJA, SourceStatus: row.SourceStatus, TranslationStatus: row.TranslationStatus, SourceRecordID: row.SourceRecordID, CanonicalURL: row.CanonicalURL, EvidenceURL: row.EvidenceURL, RetrievedAt: row.RetrievedAt, ContentSHA256: row.ContentSHA256, Rights: row.Rights, Reason: row.Reason}
		if err := validateSummaryPatch(patch); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCollectorProtocol, err)
		}
		if patch.ContentSHA256 != "" {
			sum := sha256.Sum256([]byte(patch.DescriptionOriginal))
			if !strings.EqualFold(strings.TrimSpace(patch.ContentSHA256), hex.EncodeToString(sum[:])) {
				return nil, fmt.Errorf("%w: summary content hash mismatch", ErrCollectorProtocol)
			}
		}
		if patch.SourceStatus == SummarySourceReady {
			readyCount++
		} else {
			unavailableCount++
		}
		patches = append(patches, patch)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: read summary artifact: %v", ErrCollectorProtocol, err)
	}
	if line == 0 || len(patches) != len(request.Targets) || len(seen) != len(wanted) {
		return nil, fmt.Errorf("%w: summary artifact is incomplete", ErrCollectorProtocol)
	}
	if manifest.ReadyCount != readyCount || manifest.UnavailableCount != unavailableCount || readyCount+unavailableCount != manifest.ItemCount {
		return nil, fmt.Errorf("%w: summary manifest counts do not match patches", ErrCollectorProtocol)
	}
	return patches, nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func summaryTargetKey(target SummaryTarget) string {
	return strings.Join([]string{strings.ToLower(strings.TrimSpace(target.Category)), strings.TrimSpace(target.ItemID), strings.ToLower(strings.TrimSpace(target.Source)), strings.TrimSpace(target.SourceRecordID), strings.TrimSpace(target.CanonicalURL)}, "\x00")
}
