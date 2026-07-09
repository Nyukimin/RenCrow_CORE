package dci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
)

type L1StagingStore interface {
	SaveStagingItem(ctx context.Context, item l1sqlite.L1StagingItem) (*l1sqlite.L1StagingItem, error)
}

type L1SourceRegistryStore interface {
	SaveSourceRegistryEntry(ctx context.Context, entry l1sqlite.L1SourceRegistryEntry) (*l1sqlite.L1SourceRegistryEntry, error)
}

type L1SourceCandidateStore struct {
	store     L1StagingStore
	namespace string
	now       func() time.Time
}

func NewL1SourceCandidateStore(store L1StagingStore, namespace string) *L1SourceCandidateStore {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		namespace = "kb:dci"
	}
	return &L1SourceCandidateStore{
		store:     store,
		namespace: namespace,
		now:       time.Now,
	}
}

func (s *L1SourceCandidateStore) WithNow(now func() time.Time) *L1SourceCandidateStore {
	if now != nil {
		s.now = now
	}
	return s
}

func (s *L1SourceCandidateStore) SaveDCISourceCandidates(ctx context.Context, result domaindci.SearchResult) error {
	if s == nil || s.store == nil || len(result.Pack.Evidence) == 0 {
		return nil
	}
	fetchedAt := result.Trace.EndedAt
	if fetchedAt.IsZero() {
		fetchedAt = result.Trace.StartedAt
	}
	if fetchedAt.IsZero() {
		fetchedAt = s.now().UTC()
	}
	for _, evidence := range result.Pack.Evidence {
		if strings.TrimSpace(evidence.EvidenceID) == "" {
			return fmt.Errorf("dci source candidate evidence_id is required")
		}
		sourceID := fmt.Sprintf("dci:%s", result.Trace.EventID)
		sourceURL := dciSyntheticSourceURL(evidence.FilePath)
		if sourceURL != "" {
			sourceID = dciSourceID(evidence.FilePath)
			if err := s.saveSourceRegistryCandidate(ctx, sourceID, sourceURL, evidence, result); err != nil {
				return err
			}
		}
		item := l1sqlite.L1StagingItem{
			Kind:         l1sqlite.L1StagingKindSearchResult,
			Namespace:    s.namespace,
			EventID:      fmt.Sprintf("%s:%s", result.Trace.EventID, evidence.EvidenceID),
			SourceID:     sourceID,
			SourceURL:    sourceURL,
			FetchedAt:    fetchedAt,
			RawText:      evidence.Snippet,
			SummaryDraft: sourceCandidateSummary(evidence),
			Keywords:     append([]string(nil), result.Pack.DerivedTerms...),
			LicenseNote:  "local corpus evidence; review required before promote",
			Meta: map[string]interface{}{
				"source_kind":     "dci",
				"search_event_id": result.Trace.EventID,
				"evidence_id":     evidence.EvidenceID,
				"query":           result.Pack.Query,
				"file_path":       evidence.FilePath,
				"line_start":      evidence.LineStart,
				"line_end":        evidence.LineEnd,
				"heading":         evidence.Heading,
				"reason":          evidence.Reason,
				"confidence":      evidence.Confidence,
				"review_required": true,
			},
		}
		if _, err := s.store.SaveStagingItem(ctx, item); err != nil {
			return fmt.Errorf("failed to stage dci source candidate %s: %w", evidence.EvidenceID, err)
		}
	}
	return nil
}

func (s *L1SourceCandidateStore) saveSourceRegistryCandidate(ctx context.Context, sourceID string, sourceURL string, evidence domaindci.Evidence, result domaindci.SearchResult) error {
	registry, ok := s.store.(L1SourceRegistryStore)
	if !ok {
		return nil
	}
	entry := l1sqlite.L1SourceRegistryEntry{
		SourceID:      sourceID,
		URL:           sourceURL,
		Kind:          l1sqlite.L1SourceKindSearchFallback,
		TrustScore:    0.50,
		FetchInterval: 24 * time.Hour,
		LicenseNote:   "local corpus evidence discovered by DCI; review required before promote",
		Enabled:       false,
		Meta: map[string]interface{}{
			"source_kind":     "dci",
			"local_path":      evidence.FilePath,
			"search_event_id": result.Trace.EventID,
			"evidence_id":     evidence.EvidenceID,
			"query":           result.Pack.Query,
			"review_required": true,
			"auto_fetch":      false,
		},
	}
	if _, err := registry.SaveSourceRegistryEntry(ctx, entry); err != nil {
		return fmt.Errorf("failed to register dci source registry candidate %s: %w", evidence.EvidenceID, err)
	}
	return nil
}

func sourceCandidateSummary(evidence domaindci.Evidence) string {
	location := strings.TrimSpace(evidence.FilePath)
	if evidence.LineStart > 0 {
		location = fmt.Sprintf("%s:%d", location, evidence.LineStart)
	}
	if location == "" {
		return "DCI evidence candidate"
	}
	return "DCI evidence candidate from " + location
}

func dciSourceID(filePath string) string {
	normalized := filepath.ToSlash(strings.TrimSpace(filePath))
	sum := sha256.Sum256([]byte(normalized))
	return "dci:file:" + hex.EncodeToString(sum[:])[:16]
}

func dciSyntheticSourceURL(filePath string) string {
	normalized := filepath.ToSlash(strings.TrimSpace(filePath))
	if normalized == "" {
		return ""
	}
	return "https://local.rencrow.invalid/dci/" + url.PathEscape(normalized)
}
