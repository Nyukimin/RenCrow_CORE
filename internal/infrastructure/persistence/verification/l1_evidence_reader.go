package verification

import (
	"context"
	"fmt"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"strings"
	"time"
	"unicode/utf8"

	appverification "github.com/Nyukimin/RenCrow_CORE/internal/application/verification"
	domainverification "github.com/Nyukimin/RenCrow_CORE/internal/domain/verification"
)

type L1EvidenceStore interface {
	SearchKnowledgeItemsFTS(ctx context.Context, domain string, query string, limit int) ([]l1sqlite.L1KnowledgeItem, error)
	GetSimilarFreshSearchCache(ctx context.Context, provider string, rawQuery string, now time.Time, threshold float64) (*l1sqlite.L1SearchCacheEntry, error)
	ListSourceRegistryEntries(ctx context.Context, enabledOnly bool) ([]l1sqlite.L1SourceRegistryEntry, error)
}

type L1EvidenceReader struct {
	store L1EvidenceStore
	now   func() time.Time
}

func NewL1EvidenceReader(store L1EvidenceStore) *L1EvidenceReader {
	return &L1EvidenceReader{
		store: store,
		now:   func() time.Time { return time.Now().UTC() },
	}
}

func (r *L1EvidenceReader) ReadEvidence(ctx context.Context, claim domainverification.Claim, req appverification.Request) ([]domainverification.EvidenceRef, error) {
	if r == nil || r.store == nil {
		return nil, nil
	}

	query := strings.TrimSpace(claim.Text)
	if query == "" {
		return nil, nil
	}

	var out []domainverification.EvidenceRef
	out = append(out, r.readKnowledge(ctx, query)...)
	out = append(out, r.readSearchCache(ctx, query)...)
	out = append(out, r.readSourceRegistry(ctx, query)...)
	return out, nil
}

func (r *L1EvidenceReader) readKnowledge(ctx context.Context, query string) []domainverification.EvidenceRef {
	items, err := r.store.SearchKnowledgeItemsFTS(ctx, "general", query, 3)
	if err != nil {
		return nil
	}
	out := make([]domainverification.EvidenceRef, 0, len(items))
	for i, item := range items {
		raw := strings.TrimSpace(item.RawText)
		summary := strings.TrimSpace(item.SummaryDraft)
		out = append(out, domainverification.EvidenceRef{
			ID:          fmt.Sprintf("l1_kb_%03d", i+1),
			SourceType:  domainverification.EvidenceL1SQLite,
			SourceID:    item.ID,
			SourceURL:   item.SourceURL,
			Field:       "raw_text",
			Value:       firstNonEmpty(raw, summary),
			Note:        "l1 knowledge item; raw_text is preferred over summary_draft",
			RetrievedAt: item.UpdatedAt,
			Supports:    supportsClaim(query, raw+"\n"+summary),
		})
	}
	return out
}

func (r *L1EvidenceReader) readSearchCache(ctx context.Context, query string) []domainverification.EvidenceRef {
	entry, err := r.store.GetSimilarFreshSearchCache(ctx, "web", query, r.now(), 0.40)
	if err != nil || entry == nil {
		return nil
	}
	return []domainverification.EvidenceRef{{
		ID:          "l1_search_cache_001",
		SourceType:  domainverification.EvidenceSearchCache,
		SourceID:    entry.QueryHash,
		Field:       "results_json",
		Value:       strings.TrimSpace(entry.ResultsJSON),
		Note:        "fresh search cache hit",
		RetrievedAt: entry.RetrievedAt,
		Supports:    supportsClaim(query, entry.ResultsJSON),
	}}
}

func (r *L1EvidenceReader) readSourceRegistry(ctx context.Context, query string) []domainverification.EvidenceRef {
	entries, err := r.store.ListSourceRegistryEntries(ctx, true)
	if err != nil {
		return nil
	}
	out := make([]domainverification.EvidenceRef, 0, len(entries))
	for _, entry := range entries {
		if !sourceRegistryEntryMatches(query, entry) {
			continue
		}
		out = append(out, domainverification.EvidenceRef{
			ID:          "source_registry_" + safeEvidenceID(entry.SourceID),
			SourceType:  domainverification.EvidenceSourceRegistry,
			SourceID:    entry.SourceID,
			SourceURL:   entry.URL,
			Field:       "registry_state",
			Value:       entry.LastStatus,
			Note:        "source registry entry is discovery metadata, not promoted memory",
			RetrievedAt: entry.LastFetchedAt,
			Supports:    false,
		})
	}
	return out
}

func supportsClaim(claimText, evidenceText string) bool {
	terms := significantTerms(claimText)
	if len(terms) == 0 {
		return false
	}
	lower := strings.ToLower(evidenceText)
	matched := 0
	for _, term := range terms {
		if strings.Contains(lower, term) {
			matched++
		}
	}
	if matched >= 2 || matched == len(terms) {
		return true
	}
	for _, gram := range japaneseNGrams(claimText, 4) {
		if strings.Contains(lower, strings.ToLower(gram)) {
			return true
		}
	}
	return false
}

func significantTerms(text string) []string {
	rawTerms := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '。' || r == '、' || r == ',' || r == '.' || r == ':' || r == ';' || r == '"' || r == '\'' || r == '「' || r == '」'
	})
	terms := make([]string, 0, len(rawTerms))
	seen := map[string]struct{}{}
	for _, term := range rawTerms {
		term = strings.TrimSpace(term)
		if utf8.RuneCountInString(term) < 2 || isStopTerm(term) {
			continue
		}
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}
	if len(terms) > 6 {
		terms = terms[:6]
	}
	return terms
}

func isStopTerm(term string) bool {
	switch term {
	case "です", "ます", "である", "これは", "それは", "について", "こと", "もの", "the", "and", "for", "with":
		return true
	default:
		return false
	}
}

func japaneseNGrams(text string, size int) []string {
	text = strings.TrimSpace(text)
	if text == "" || size <= 0 {
		return nil
	}
	runes := []rune(text)
	out := make([]string, 0, len(runes))
	for i := 0; i+size <= len(runes); i++ {
		gram := strings.TrimSpace(string(runes[i : i+size]))
		if gram == "" || strings.ContainsAny(gram, " \t\r\n。、,.") {
			continue
		}
		out = append(out, gram)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func sourceRegistryEntryMatches(query string, entry l1sqlite.L1SourceRegistryEntry) bool {
	haystack := strings.ToLower(entry.SourceID + "\n" + entry.URL + "\n" + entry.Kind)
	for _, term := range significantTerms(query) {
		if strings.Contains(haystack, term) {
			return true
		}
	}
	return false
}

func safeEvidenceID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer(":", "_", "/", "_", "\\", "_", " ", "_")
	return replacer.Replace(raw)
}
