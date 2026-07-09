package browsertrace

import (
	"context"
	"fmt"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/l1sqlite"
	"strings"
	"time"

	domaintrace "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/browsertrace"
)

type L1StagingStore interface {
	SaveStagingItem(ctx context.Context, item l1sqlite.L1StagingItem) (*l1sqlite.L1StagingItem, error)
}

type L1APICandidateStore struct {
	store     L1StagingStore
	namespace string
	now       func() time.Time
}

func NewL1APICandidateStore(store L1StagingStore, namespace string) *L1APICandidateStore {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		namespace = "kb:browser_trace_api"
	}
	return &L1APICandidateStore{
		store:     store,
		namespace: namespace,
		now:       time.Now,
	}
}

func (s *L1APICandidateStore) WithNow(now func() time.Time) *L1APICandidateStore {
	if now != nil {
		s.now = now
	}
	return s
}

func (s *L1APICandidateStore) SaveBrowserTraceAPICandidates(ctx context.Context, result domaintrace.DiscoveryResult) error {
	if s == nil || s.store == nil || len(result.Candidates) == 0 {
		return nil
	}
	fetchedAt := result.Run.CapturedAt
	if fetchedAt.IsZero() {
		fetchedAt = result.Run.CreatedAt
	}
	if fetchedAt.IsZero() {
		fetchedAt = s.now().UTC()
	}
	for _, candidate := range result.Candidates {
		if strings.TrimSpace(candidate.CandidateID) == "" {
			return fmt.Errorf("browser trace api candidate_id is required")
		}
		if strings.TrimSpace(candidate.TraceRunID) == "" {
			return fmt.Errorf("browser trace api trace_run_id is required")
		}
		item := l1sqlite.L1StagingItem{
			Kind:         l1sqlite.L1StagingKindSearchResult,
			Namespace:    s.namespace,
			EventID:      fmt.Sprintf("%s:%s", candidate.TraceRunID, candidate.CandidateID),
			SourceID:     fmt.Sprintf("browser_trace:%s", candidate.TraceRunID),
			SourceURL:    candidate.ObservedURL,
			FetchedAt:    fetchedAt,
			RawText:      apiCandidateRawText(candidate),
			SummaryDraft: apiCandidateSummary(candidate),
			Keywords:     apiCandidateKeywords(candidate),
			LicenseNote:  "browser trace observed API candidate; terms and human review required before promote",
			Meta: map[string]interface{}{
				"source_kind":                "browser_trace_api",
				"trace_run_id":               candidate.TraceRunID,
				"candidate_id":               candidate.CandidateID,
				"site_id":                    candidate.SiteID,
				"method":                     candidate.Method,
				"observed_url":               candidate.ObservedURL,
				"templated_url":              candidate.TemplatedURL,
				"path_template":              candidate.PathTemplate,
				"auth_required":              candidate.AuthRequired,
				"contains_personal_data":     candidate.ContainsPersonalData,
				"risk_level":                 candidate.RiskLevel,
				"confidence":                 candidate.Confidence,
				"review_required":            true,
				"promote_requires_validator": true,
			},
		}
		if _, err := s.store.SaveStagingItem(ctx, item); err != nil {
			return fmt.Errorf("failed to stage browser trace api candidate %s: %w", candidate.CandidateID, err)
		}
	}
	return nil
}

func apiCandidateRawText(candidate domaintrace.APICandidate) string {
	parts := []string{
		"method: " + strings.TrimSpace(candidate.Method),
		"observed_url: " + strings.TrimSpace(candidate.ObservedURL),
	}
	if strings.TrimSpace(candidate.TemplatedURL) != "" {
		parts = append(parts, "templated_url: "+strings.TrimSpace(candidate.TemplatedURL))
	}
	if strings.TrimSpace(candidate.PathTemplate) != "" {
		parts = append(parts, "path_template: "+strings.TrimSpace(candidate.PathTemplate))
	}
	if strings.TrimSpace(candidate.RiskLevel) != "" {
		parts = append(parts, "risk_level: "+strings.TrimSpace(candidate.RiskLevel))
	}
	return strings.Join(parts, "\n")
}

func apiCandidateSummary(candidate domaintrace.APICandidate) string {
	target := strings.TrimSpace(candidate.PathTemplate)
	if target == "" {
		target = strings.TrimSpace(candidate.TemplatedURL)
	}
	if target == "" {
		target = strings.TrimSpace(candidate.ObservedURL)
	}
	method := strings.TrimSpace(candidate.Method)
	if method == "" {
		method = "API"
	}
	if target == "" {
		return "Browser trace API candidate"
	}
	return fmt.Sprintf("Browser trace API candidate %s %s", method, target)
}

func apiCandidateKeywords(candidate domaintrace.APICandidate) []string {
	keywords := []string{"browser_trace_api"}
	if method := strings.TrimSpace(candidate.Method); method != "" {
		keywords = append(keywords, method)
	}
	if site := strings.TrimSpace(candidate.SiteID); site != "" {
		keywords = append(keywords, site)
	}
	if path := strings.TrimSpace(candidate.PathTemplate); path != "" {
		keywords = append(keywords, path)
	}
	return keywords
}
