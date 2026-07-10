package l1sqlite

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	_ "modernc.org/sqlite"
)

func (s *L1SQLiteStore) RecentKnowledgeItems(ctx context.Context, domain string, limit int) ([]L1KnowledgeItem, error) {
	if err := ValidateKnowledgeDomain(domain); err != nil {
		return nil, err
	}
	domain = NormalizeNewsCategory(domain)
	if err := ValidateL1Namespace("kb:" + domain); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, staging_id, domain, title, source_id, source_url, raw_text, raw_hash,
       summary_draft, keywords_json, license_note, meta_json, created_at, updated_at
FROM l1_knowledge_item
WHERE domain = ?
ORDER BY updated_at DESC
LIMIT ?
`, domain, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query l1 knowledge items: %w", err)
	}
	defer rows.Close()
	return ScanL1KnowledgeItems(rows)
}

func (s *L1SQLiteStore) SearchKnowledgeItemsFTS(ctx context.Context, domain string, query string, limit int) ([]L1KnowledgeItem, error) {
	if err := ValidateKnowledgeDomain(domain); err != nil {
		return nil, err
	}
	domain = NormalizeNewsCategory(domain)
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("l1 knowledge fts query is required")
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT k.id, k.staging_id, k.domain, k.title, k.source_id, k.source_url, k.raw_text, k.raw_hash,
       k.summary_draft, k.keywords_json, k.license_note, k.meta_json, k.created_at, k.updated_at
FROM l1_knowledge_item_fts f
JOIN l1_knowledge_item k ON k.id = f.id
WHERE (
	f.title LIKE ?
	OR f.raw_text LIKE ?
	OR f.summary_draft LIKE ?
	OR f.keywords_text LIKE ?
)
  AND f.domain = ?
ORDER BY k.updated_at DESC
LIMIT ?
`, LikeQuery(query), LikeQuery(query), LikeQuery(query), LikeQuery(query), domain, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to search l1 knowledge fts: %w", err)
	}
	defer rows.Close()
	items, err := ScanL1KnowledgeItems(rows)
	if err != nil || len(items) > 0 {
		return items, err
	}
	return s.searchKnowledgeItemsByTerms(ctx, domain, query, limit)
}

func (s *L1SQLiteStore) searchKnowledgeItemsByTerms(ctx context.Context, domain string, query string, limit int) ([]L1KnowledgeItem, error) {
	terms := knowledgeSearchTerms(query)
	if len(terms) == 0 {
		return []L1KnowledgeItem{}, nil
	}
	clauses := make([]string, 0, len(terms))
	args := make([]interface{}, 0, len(terms)*4+2)
	for _, term := range terms {
		clauses = append(clauses, `(f.title LIKE ? OR f.raw_text LIKE ? OR f.summary_draft LIKE ? OR f.keywords_text LIKE ?)`)
		like := LikeQuery(term)
		args = append(args, like, like, like, like)
	}
	args = append(args, domain, limit)
	rows, err := s.db.QueryContext(ctx, `
SELECT k.id, k.staging_id, k.domain, k.title, k.source_id, k.source_url, k.raw_text, k.raw_hash,
       k.summary_draft, k.keywords_json, k.license_note, k.meta_json, k.created_at, k.updated_at
FROM l1_knowledge_item_fts f
JOIN l1_knowledge_item k ON k.id = f.id
WHERE (`+strings.Join(clauses, " OR ")+`)
  AND f.domain = ?
ORDER BY k.updated_at DESC
LIMIT ?
`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to search l1 knowledge fts by terms: %w", err)
	}
	defer rows.Close()
	return ScanL1KnowledgeItems(rows)
}

func knowledgeSearchTerms(query string) []string {
	replacer := strings.NewReplacer("、", " ", "。", " ", ":", " ", "：", " ", ",", " ", ".", " ", "　", " ")
	parts := strings.Fields(replacer.Replace(strings.ToLower(query)))
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if len([]rune(part)) < 3 || seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
		if len(out) >= 6 {
			break
		}
	}
	return out
}

func upsertKnowledgeFTS(ctx context.Context, execer l1SQLExecer, item *L1KnowledgeItem) error {
	if item == nil {
		return errors.New("l1 knowledge fts item is required")
	}
	if _, err := execer.ExecContext(ctx, `DELETE FROM l1_knowledge_item_fts WHERE id = ?`, item.ID); err != nil {
		return fmt.Errorf("failed to delete l1 knowledge fts row: %w", err)
	}
	if _, err := execer.ExecContext(ctx, `
INSERT INTO l1_knowledge_item_fts (id, domain, title, raw_text, summary_draft, keywords_text)
VALUES (?, ?, ?, ?, ?, ?)
`, item.ID, item.Domain, item.Title, item.RawText, item.SummaryDraft, strings.Join(item.Keywords, " ")); err != nil {
		return fmt.Errorf("failed to upsert l1 knowledge fts row: %w", err)
	}
	return nil
}

func ValidateKnowledgeDomain(domain string) error {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return errors.New("l1 knowledge domain is required")
	}
	if strings.ContainsAny(domain, " \t\r\n:") {
		return fmt.Errorf("invalid l1 knowledge domain: %s", domain)
	}
	return nil
}

func validateOptionalSourceURL(sourceURL string) error {
	if sourceURL == "" {
		return nil
	}
	parsed, err := url.ParseRequestURI(sourceURL)
	if err != nil {
		return fmt.Errorf("invalid l1 staging source_url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("invalid l1 staging source_url scheme: %s", parsed.Scheme)
	}
	if parsed.Host == "" {
		return errors.New("invalid l1 staging source_url host")
	}
	return nil
}
