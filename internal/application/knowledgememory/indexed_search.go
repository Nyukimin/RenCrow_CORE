package knowledgememory

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	SearchScopePublic = "public"
	SearchScopeUser   = "user"

	maxSearchQueryRunes = 160
	maxSearchTokens     = 16
	maxSearchResults    = 20
)

// SearchScope is the authenticated boundary supplied by the owning service.
// Public projections have no owner; user projections must name their owner.
type SearchScope struct {
	Scope  string
	UserID string
}

func (s SearchScope) Validate() error {
	switch strings.TrimSpace(s.Scope) {
	case SearchScopePublic:
		if strings.TrimSpace(s.UserID) != "" {
			return fmt.Errorf("public search scope must not include user_id")
		}
	case SearchScopeUser:
		if strings.TrimSpace(s.UserID) == "" {
			return fmt.Errorf("user search scope requires user_id")
		}
	default:
		return fmt.Errorf("unsupported search scope")
	}
	return nil
}

type SearchRequest struct {
	Scope SearchScope
	Query string
	// RecordType is optional for the internal compatibility API (blank means
	// both indexed public record types), but the knowledge.search Tool always
	// supplies one of the fixed enum values.
	RecordType string
	Limit      int
}

func (r SearchRequest) Validate() error {
	if err := r.Scope.Validate(); err != nil {
		return err
	}
	if _, err := SearchTokens(r.Query); err != nil {
		return err
	}
	if r.RecordType != "" && !isSearchRecordType(r.RecordType) {
		return fmt.Errorf("unsupported search record type")
	}
	if r.Limit < 0 || r.Limit > maxSearchResults {
		return fmt.Errorf("search limit must be 0 (default) or between 1 and %d", maxSearchResults)
	}
	return nil
}

func isSearchRecordType(recordType string) bool {
	switch recordType {
	case "creative_knowledge", "news_knowledge":
		return true
	default:
		return false
	}
}

// SearchResult is the safe projection returned by indexed lookup. It never
// contains the source payload or any private evidence field.
type SearchResult struct {
	RecordType      string `json:"record_type"`
	RecordID        string `json:"record_id"`
	Scope           string `json:"scope"`
	UserID          string `json:"user_id,omitempty"`
	Title           string `json:"title"`
	Summary         string `json:"summary,omitempty"`
	Visibility      string `json:"visibility"`
	SourceUpdatedAt string `json:"source_updated_at"`
	IndexedAt       string `json:"indexed_at"`
	ContentSHA256   string `json:"content_sha256"`
}

type IndexedSearcher interface {
	Search(ctx context.Context, request SearchRequest) ([]SearchResult, error)
}

// SearchTokens performs the fixed, deterministic query normalization used by
// the inverted index. Unicode text is NFKC-normalized; ASCII runs become one
// lower-case token and non-ASCII letter/number runs become overlapping
// two-rune tokens. Punctuation and whitespace are separators.
func SearchTokens(query string) ([]string, error) {
	return normalizeSearchTokens(query, maxSearchTokens)
}

// IndexTokens applies the same normalizer to a safe projection. Unlike a
// query, a stored projection may contain more than sixteen terms; the query
// side remains bounded independently.
func IndexTokens(text string) ([]string, error) {
	return normalizeSearchTokens(text, 0)
}

func normalizeSearchTokens(query string, maxTokens int) ([]string, error) {
	query = strings.TrimSpace(norm.NFKC.String(query))
	if query == "" {
		return nil, fmt.Errorf("search query is required")
	}
	if utf8.RuneCountInString(query) > maxSearchQueryRunes {
		return nil, fmt.Errorf("search query exceeds %d characters", maxSearchQueryRunes)
	}

	seen := make(map[string]struct{})
	tokens := make([]string, 0, maxSearchTokens)
	flush := func(run []rune) error {
		if len(run) == 0 {
			return nil
		}
		ascii := true
		for _, r := range run {
			if r > unicode.MaxASCII {
				ascii = false
				break
			}
		}
		if ascii {
			addSearchToken(strings.ToLower(string(run)), seen, &tokens)
			return nil
		}
		if len(run) < 2 {
			return nil
		}
		for i := 0; i+1 < len(run); i++ {
			addSearchToken(string(run[i:i+2]), seen, &tokens)
		}
		return nil
	}

	run := make([]rune, 0, 8)
	for _, r := range []rune(query) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			run = append(run, r)
			continue
		}
		if err := flush(run); err != nil {
			return nil, err
		}
		run = run[:0]
	}
	if err := flush(run); err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("search query has no searchable tokens")
	}
	if maxTokens > 0 && len(tokens) > maxTokens {
		return nil, fmt.Errorf("search query produces more than %d tokens", maxTokens)
	}
	return tokens, nil
}

func addSearchToken(token string, seen map[string]struct{}, tokens *[]string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	if _, ok := seen[token]; ok {
		return
	}
	seen[token] = struct{}{}
	*tokens = append(*tokens, token)
}
