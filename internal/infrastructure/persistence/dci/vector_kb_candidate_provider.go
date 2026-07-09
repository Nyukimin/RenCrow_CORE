package dci

import (
	"context"
	"net/url"
	"strings"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
)

type VectorKBSearchStore interface {
	SearchKB(ctx context.Context, domain string, query string, topK int) ([]*domconv.Document, error)
}

type VectorKBCandidateProvider struct {
	store   VectorKBSearchStore
	domains []string
}

func NewVectorKBCandidateProvider(store VectorKBSearchStore, domains []string) *VectorKBCandidateProvider {
	return &VectorKBCandidateProvider{store: store, domains: normalizeKnowledgeFTSDomains(domains)}
}

func (p *VectorKBCandidateProvider) CandidateFiles(ctx context.Context, query string, terms []string, allowlist []string, limit int) ([]domaindci.SourceMetadataRank, error) {
	if p == nil || p.store == nil || strings.TrimSpace(query) == "" || limit <= 0 {
		return nil, nil
	}
	out := make([]domaindci.SourceMetadataRank, 0, limit)
	bestByPath := map[string]domaindci.SourceMetadataRank{}
	perDomainLimit := limit
	if len(p.domains) > 1 {
		perDomainLimit = (limit / len(p.domains)) + 1
	}
	for _, domain := range p.domains {
		docs, err := p.store.SearchKB(ctx, domain, query, perDomainLimit)
		if err != nil {
			return nil, err
		}
		for _, doc := range docs {
			path := vectorKBDocumentLocalPath(doc)
			if path == "" || !pathWithinAnyAllowlist(path, allowlist) {
				continue
			}
			score := 1.30 + float64(doc.Score) + vectorKBDocumentTermScore(doc, terms)
			rank := domaindci.SourceMetadataRank{
				FilePath: path,
				SourceID: firstNonEmptyString(vectorKBMetaString(doc.Meta, "source_id"), doc.ID),
				Score:    score,
				Reason:   "vector kb semantic match narrowed local corpus candidate",
			}
			if current, ok := bestByPath[path]; !ok || rank.Score > current.Score {
				bestByPath[path] = rank
			}
		}
	}
	for _, rank := range bestByPath {
		out = append(out, rank)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func vectorKBDocumentLocalPath(doc *domconv.Document) string {
	if doc == nil {
		return ""
	}
	for _, key := range []string{"local_path", "file_path", "path"} {
		if value := vectorKBMetaString(doc.Meta, key); value != "" {
			return value
		}
	}
	const dciSyntheticPrefix = "/dci/"
	parsed, err := url.Parse(doc.Source)
	if err != nil || parsed.Host != "local.rencrow.invalid" || !strings.HasPrefix(parsed.Path, dciSyntheticPrefix) {
		return ""
	}
	unescaped, err := url.PathUnescape(strings.TrimPrefix(parsed.Path, dciSyntheticPrefix))
	if err != nil {
		return ""
	}
	return unescaped
}

func vectorKBDocumentTermScore(doc *domconv.Document, terms []string) float64 {
	if doc == nil {
		return 0
	}
	haystack := strings.ToLower(doc.ID + " " + doc.Domain + " " + doc.Source + " " + doc.Content + " " + vectorKBMetaText(doc.Meta))
	score := 0.0
	for _, term := range terms {
		term = strings.ToLower(strings.TrimSpace(term))
		if term != "" && strings.Contains(haystack, term) {
			score += 0.08
		}
	}
	if score > 0.40 {
		return 0.40
	}
	return score
}

func vectorKBMetaString(meta map[string]interface{}, key string) string {
	if meta == nil {
		return ""
	}
	value, ok := meta[key]
	if !ok {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []string:
		return strings.TrimSpace(strings.Join(v, " "))
	case []interface{}:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				parts = append(parts, strings.TrimSpace(s))
			}
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}

func vectorKBMetaText(meta map[string]interface{}) string {
	if meta == nil {
		return ""
	}
	parts := make([]string, 0, len(meta))
	for key, value := range meta {
		switch v := value.(type) {
		case string:
			parts = append(parts, key, v)
		case []string:
			parts = append(parts, key, strings.Join(v, " "))
		case []interface{}:
			for _, item := range v {
				if s, ok := item.(string); ok {
					parts = append(parts, key, s)
				}
			}
		}
	}
	return strings.Join(parts, " ")
}
