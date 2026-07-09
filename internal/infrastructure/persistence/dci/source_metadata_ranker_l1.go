package dci

import (
	"context"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"net/url"
	"path/filepath"
	"strings"

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
)

type L1SourceMetadataStore interface {
	ListSourceRegistryEntries(ctx context.Context, enabledOnly bool) ([]l1sqlite.L1SourceRegistryEntry, error)
}

type L1SourceMetadataRanker struct {
	store       L1SourceMetadataStore
	enabledOnly bool
}

func NewL1SourceMetadataRanker(store L1SourceMetadataStore) *L1SourceMetadataRanker {
	return &L1SourceMetadataRanker{store: store, enabledOnly: true}
}

func (r *L1SourceMetadataRanker) WithEnabledOnly(enabledOnly bool) *L1SourceMetadataRanker {
	r.enabledOnly = enabledOnly
	return r
}

func (r *L1SourceMetadataRanker) RankDCICandidateFiles(ctx context.Context, paths []string, terms []string) ([]domaindci.SourceMetadataRank, error) {
	if r == nil || r.store == nil || len(paths) == 0 {
		return nil, nil
	}
	entries, err := r.store.ListSourceRegistryEntries(ctx, r.enabledOnly)
	if err != nil {
		return nil, err
	}
	pathByNormalized := make(map[string]string, len(paths))
	for _, path := range paths {
		normalized := normalizeLocalCorpusPath(path)
		if normalized != "" {
			pathByNormalized[normalized] = path
		}
	}
	bestByPath := map[string]domaindci.SourceMetadataRank{}
	for _, entry := range entries {
		localPath := sourceRegistryLocalPath(entry)
		if localPath == "" {
			continue
		}
		candidatePath := pathByNormalized[normalizeLocalCorpusPath(localPath)]
		if candidatePath == "" {
			continue
		}
		score := 0.50 + clamp01(entry.TrustScore)
		score += sourceRegistryTermScore(entry, terms)
		rank := domaindci.SourceMetadataRank{
			FilePath: candidatePath,
			SourceID: entry.SourceID,
			Score:    score,
			Reason:   "source registry local_path metadata matched DCI candidate",
		}
		if current, ok := bestByPath[candidatePath]; !ok || rank.Score > current.Score {
			bestByPath[candidatePath] = rank
		}
	}
	out := make([]domaindci.SourceMetadataRank, 0, len(bestByPath))
	for _, rank := range bestByPath {
		out = append(out, rank)
	}
	return out, nil
}

func sourceRegistryLocalPath(entry l1sqlite.L1SourceRegistryEntry) string {
	for _, key := range []string{"local_path", "file_path", "path"} {
		if value := stringMetaValue(entry.Meta, key); value != "" {
			return value
		}
	}
	const dciSyntheticPrefix = "/dci/"
	parsed, err := url.Parse(entry.URL)
	if err != nil || parsed.Host != "local.rencrow.invalid" || !strings.HasPrefix(parsed.Path, dciSyntheticPrefix) {
		return ""
	}
	unescaped, err := url.PathUnescape(strings.TrimPrefix(parsed.Path, dciSyntheticPrefix))
	if err != nil {
		return ""
	}
	return unescaped
}

func sourceRegistryTermScore(entry l1sqlite.L1SourceRegistryEntry, terms []string) float64 {
	haystack := strings.ToLower(entry.SourceID + " " + entry.Kind + " " + entry.URL + " " + entry.LicenseNote + " " + sourceRegistryMetaText(entry.Meta))
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

func sourceRegistryMetaText(meta map[string]interface{}) string {
	if len(meta) == 0 {
		return ""
	}
	var b strings.Builder
	for key, value := range meta {
		b.WriteString(key)
		b.WriteByte(' ')
		if text, ok := value.(string); ok {
			b.WriteString(text)
			b.WriteByte(' ')
		}
	}
	return b.String()
}

func stringMetaValue(meta map[string]interface{}, key string) string {
	if meta == nil {
		return ""
	}
	value, ok := meta[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func normalizeLocalCorpusPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(path))
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
