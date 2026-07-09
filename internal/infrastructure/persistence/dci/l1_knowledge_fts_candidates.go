package dci

import (
	"context"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/l1sqlite"
	"net/url"
	"path/filepath"
	"strings"

	domaindci "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/dci"
)

type L1KnowledgeFTSStore interface {
	SearchKnowledgeItemsFTS(ctx context.Context, domain string, query string, limit int) ([]l1sqlite.L1KnowledgeItem, error)
}

type L1KnowledgeFTSCandidateProvider struct {
	store   L1KnowledgeFTSStore
	domains []string
}

func NewL1KnowledgeFTSCandidateProvider(store L1KnowledgeFTSStore, domains []string) *L1KnowledgeFTSCandidateProvider {
	return &L1KnowledgeFTSCandidateProvider{store: store, domains: normalizeKnowledgeFTSDomains(domains)}
}

func (p *L1KnowledgeFTSCandidateProvider) CandidateFiles(ctx context.Context, query string, terms []string, allowlist []string, limit int) ([]domaindci.SourceMetadataRank, error) {
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
		items, err := p.store.SearchKnowledgeItemsFTS(ctx, domain, query, perDomainLimit)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			path := knowledgeItemLocalPath(item)
			if path == "" || !pathWithinAnyAllowlist(path, allowlist) {
				continue
			}
			score := 1.0 + knowledgeItemTermScore(item, terms)
			rank := domaindci.SourceMetadataRank{
				FilePath: path,
				SourceID: firstNonEmptyString(item.SourceID, item.ID),
				Score:    score,
				Reason:   "l1 knowledge FTS matched local corpus candidate",
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

func normalizeKnowledgeFTSDomains(domains []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(domains))
	for _, domain := range domains {
		domain = strings.TrimSpace(domain)
		if domain == "" || seen[domain] {
			continue
		}
		seen[domain] = true
		out = append(out, domain)
	}
	if len(out) == 0 {
		return []string{"general"}
	}
	return out
}

func knowledgeItemLocalPath(item l1sqlite.L1KnowledgeItem) string {
	for _, key := range []string{"local_path", "file_path", "path"} {
		if value := stringMetaValue(item.Meta, key); value != "" {
			return value
		}
	}
	const dciSyntheticPrefix = "/dci/"
	parsed, err := url.Parse(item.SourceURL)
	if err != nil || parsed.Host != "local.rencrow.invalid" || !strings.HasPrefix(parsed.Path, dciSyntheticPrefix) {
		return ""
	}
	unescaped, err := url.PathUnescape(strings.TrimPrefix(parsed.Path, dciSyntheticPrefix))
	if err != nil {
		return ""
	}
	return unescaped
}

func knowledgeItemTermScore(item l1sqlite.L1KnowledgeItem, terms []string) float64 {
	haystack := strings.ToLower(item.ID + " " + item.StagingID + " " + item.Domain + " " + item.Title + " " + item.SourceID + " " + item.SourceURL + " " + item.SummaryDraft + " " + strings.Join(item.Keywords, " ") + " " + sourceRegistryMetaText(item.Meta))
	score := 0.0
	for _, term := range terms {
		term = strings.ToLower(strings.TrimSpace(term))
		if term != "" && strings.Contains(haystack, term) {
			score += 0.10
		}
	}
	if score > 0.50 {
		return 0.50
	}
	return score
}

func pathWithinAnyAllowlist(path string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return false
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, root := range allowlist {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if target == absRoot {
			return true
		}
		rel, err := filepath.Rel(absRoot, target)
		if err != nil {
			continue
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
