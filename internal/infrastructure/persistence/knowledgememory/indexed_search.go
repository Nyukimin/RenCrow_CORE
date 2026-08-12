package knowledgememory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	appkm "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgememory"
	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
)

const (
	creativeKnowledgeRecordType = "creative_knowledge"
	newsKnowledgeRecordType     = "news_knowledge"
	searchVisibilityPublic      = "public"
	maxSearchCandidates         = 50
)

var searchableKnowledgeRecordTypes = []string{
	creativeKnowledgeRecordType,
	newsKnowledgeRecordType,
}

type safeSearchProjection struct {
	recordType      string
	recordID        string
	scope           string
	userID          string
	title           string
	summary         string
	visibility      string
	sourceUpdatedAt string
	contentSHA256   string
	tokens          []string
}

func (s *SQLiteStore) saveCreativeKnowledgeItem(ctx context.Context, item domainkm.CreativeKnowledgeItem) error {
	projection := creativeSearchProjection(item)
	return s.saveKnowledgeItemWithProjection(ctx, creativeKnowledgeRecordType, item.ItemID, item.CreatedAt.Format(timeFormatRFC3339Nano), item, projection)
}

func (s *SQLiteStore) saveNewsKnowledgeItem(ctx context.Context, item domainkm.NewsKnowledgeItem) error {
	projection := newsSearchProjection(item)
	return s.saveKnowledgeItemWithProjection(ctx, newsKnowledgeRecordType, item.ItemID, item.CreatedAt.Format(timeFormatRFC3339Nano), item, projection)
}

func (s *SQLiteStore) saveKnowledgeItemWithProjection(ctx context.Context, recordType, recordID, createdAt string, item any, projection *safeSearchProjection) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("knowledge memory sqlite store is closed")
	}
	payload, err := marshalKnowledgeItem(item)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO `+recordType+` (item_id, created_at, payload) VALUES (?, ?, ?)`, recordID, createdAt, payload); err != nil {
		return err
	}
	if err := deleteSearchProjectionTx(ctx, tx, recordType, recordID); err != nil {
		return err
	}
	if projection != nil {
		if err := insertSearchProjectionTx(ctx, tx, *projection); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func marshalKnowledgeItem(item any) (string, error) {
	data, err := json.Marshal(item)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func creativeSearchProjection(item domainkm.CreativeKnowledgeItem) *safeSearchProjection {
	if !isSearchableKnowledgeStatus(item.Status) {
		return nil
	}
	summary := strings.Join(nonEmptySearchStrings(append(append([]string{}, item.CreatorNames...), append([]string{item.WorkType}, item.RelatedWorks...)...)), " ")
	return newSafeSearchProjectionWithOwner(
		creativeKnowledgeRecordType,
		item.ItemID,
		item.Title,
		summary,
		item.CreatedAt,
		item.UserID,
		item.Visibility,
	)
}

func newsSearchProjection(item domainkm.NewsKnowledgeItem) *safeSearchProjection {
	if !isSearchableKnowledgeStatus(item.Status) {
		return nil
	}
	return newSafeSearchProjectionWithOwner(
		newsKnowledgeRecordType,
		item.ItemID,
		item.Topic,
		item.Summary,
		item.CreatedAt,
		item.UserID,
		item.Visibility,
	)
}

func newSafeSearchProjection(recordType, recordID, title, summary string, sourceUpdatedAt time.Time) *safeSearchProjection {
	return newSafeSearchProjectionWithOwner(recordType, recordID, title, summary, sourceUpdatedAt, "", "")
}

func newSafeSearchProjectionWithOwner(recordType, recordID, title, summary string, sourceUpdatedAt time.Time, userID, visibility string) *safeSearchProjection {
	title = strings.TrimSpace(title)
	summary = strings.TrimSpace(summary)
	userID = strings.TrimSpace(userID)
	visibility = strings.TrimSpace(visibility)
	scope := appkm.SearchScopePublic
	if userID != "" {
		scope = appkm.SearchScopeUser
		if visibility == "" {
			visibility = "private"
		}
	} else if visibility == "" {
		visibility = searchVisibilityPublic
	}
	if scope == appkm.SearchScopePublic && visibility != searchVisibilityPublic {
		return nil
	}
	text := strings.TrimSpace(strings.Join([]string{title, summary}, " "))
	tokens, err := appkm.IndexTokens(text)
	if err != nil {
		return nil
	}
	source := sourceUpdatedAt.UTC().Format(timeFormatRFC3339Nano)
	hashInput := strings.Join([]string{recordType, recordID, scope, userID, title, summary, visibility, source}, "\x00")
	hash := sha256.Sum256([]byte(hashInput))
	return &safeSearchProjection{
		recordType:      recordType,
		recordID:        strings.TrimSpace(recordID),
		scope:           scope,
		userID:          userID,
		title:           title,
		summary:         summary,
		visibility:      visibility,
		sourceUpdatedAt: source,
		contentSHA256:   hex.EncodeToString(hash[:]),
		tokens:          tokens,
	}
}

func isSearchableKnowledgeStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "reviewed", "promoted":
		return true
	default:
		return false
	}
}

func nonEmptySearchStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func deleteSearchProjectionTx(ctx context.Context, tx *sql.Tx, recordType, recordID string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM knowledge_memory_search_terms WHERE record_type = ? AND record_id = ?`, recordType, recordID); err != nil {
		return fmt.Errorf("delete knowledge memory search terms: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM knowledge_memory_search_documents WHERE record_type = ? AND record_id = ?`, recordType, recordID); err != nil {
		return fmt.Errorf("delete knowledge memory search document: %w", err)
	}
	return nil
}

func insertSearchProjectionTx(ctx context.Context, tx *sql.Tx, projection safeSearchProjection) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_memory_search_documents
		(record_type, record_id, scope, user_id, title, summary, visibility, source_updated_at, indexed_at, content_sha256)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projection.recordType,
		projection.recordID,
		projection.scope,
		projection.userID,
		projection.title,
		projection.summary,
		projection.visibility,
		projection.sourceUpdatedAt,
		time.Now().UTC().Format(timeFormatRFC3339Nano),
		projection.contentSHA256,
	); err != nil {
		return fmt.Errorf("insert knowledge memory search document: %w", err)
	}
	for _, token := range projection.tokens {
		if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_memory_search_terms
			(scope, user_id, token, record_type, record_id) VALUES (?, ?, ?, ?, ?)`,
			projection.scope, projection.userID, token, projection.recordType, projection.recordID); err != nil {
			return fmt.Errorf("insert knowledge memory search term: %w", err)
		}
	}
	return nil
}

// Search performs only indexed term candidate selection followed by bounded
// projection fetches. It never reads a raw payload or accepts SQL structure
// from the caller.
func (s *SQLiteStore) Search(ctx context.Context, request appkm.SearchRequest) ([]appkm.SearchResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("knowledge memory sqlite store is unavailable")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if err := s.validateSearchSchema(ctx); err != nil {
		return nil, fmt.Errorf("knowledge memory search index unavailable: %w", err)
	}
	tokens, err := appkm.SearchTokens(request.Query)
	if err != nil {
		return nil, err
	}
	candidateSQL, candidateArgs := buildCandidateQuery(tokens, request.Scope, request.RecordType)
	candidates, err := s.queryCandidates(ctx, candidateSQL, candidateArgs)
	if err != nil {
		return nil, fmt.Errorf("knowledge memory search index unavailable: %w", err)
	}
	if len(candidates) == 0 {
		return []appkm.SearchResult{}, nil
	}
	documentSQL, documentArgs := buildDocumentQuery(candidates, request.Scope)
	rows, err := s.db.QueryContext(ctx, documentSQL, documentArgs...)
	if err != nil {
		return nil, fmt.Errorf("knowledge memory search projection unavailable: %w", err)
	}
	defer rows.Close()
	results := make([]appkm.SearchResult, 0, len(candidates))
	for rows.Next() {
		var result appkm.SearchResult
		if err := rows.Scan(
			&result.RecordType,
			&result.RecordID,
			&result.Scope,
			&result.UserID,
			&result.Title,
			&result.Summary,
			&result.Visibility,
			&result.SourceUpdatedAt,
			&result.IndexedAt,
			&result.ContentSHA256,
		); err != nil {
			return nil, fmt.Errorf("scan knowledge memory search projection: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read knowledge memory search projection: %w", err)
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].SourceUpdatedAt != results[j].SourceUpdatedAt {
			return results[i].SourceUpdatedAt > results[j].SourceUpdatedAt
		}
		if results[i].RecordType != results[j].RecordType {
			return results[i].RecordType < results[j].RecordType
		}
		return results[i].RecordID < results[j].RecordID
	})
	limit := request.Limit
	if limit <= 0 {
		limit = 20
	}
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (s *SQLiteStore) validateSearchSchema(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM sqlite_master
		WHERE (type = 'table' AND name IN (?, ?))
		   OR (type = 'index' AND name IN (?, ?))`,
		"knowledge_memory_search_documents",
		"knowledge_memory_search_terms",
		"idx_knowledge_memory_search_documents_lookup",
		"idx_knowledge_memory_search_terms_lookup",
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		seen[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, required := range []string{
		"knowledge_memory_search_documents",
		"knowledge_memory_search_terms",
		"idx_knowledge_memory_search_documents_lookup",
		"idx_knowledge_memory_search_terms_lookup",
	} {
		if !seen[required] {
			return fmt.Errorf("required search schema object %q is missing", required)
		}
	}
	return nil
}

type searchCandidate struct {
	recordType string
	recordID   string
}

func buildCandidateQuery(tokens []string, scope appkm.SearchScope, recordType string) (string, []any) {
	var query strings.Builder
	query.WriteString(`SELECT t0.record_type, t0.record_id
		FROM knowledge_memory_search_terms AS t0 INDEXED BY idx_knowledge_memory_search_terms_lookup`)
	recordTypes := searchRecordTypes(recordType)
	args := make([]any, 0, len(tokens)*6)
	for i := 1; i < len(tokens); i++ {
		alias := fmt.Sprintf("t%d", i)
		query.WriteString(` JOIN knowledge_memory_search_terms AS `)
		query.WriteString(alias)
		query.WriteString(` INDEXED BY idx_knowledge_memory_search_terms_lookup ON `)
		query.WriteString(alias)
		query.WriteString(`.scope = t0.scope AND `)
		query.WriteString(alias)
		query.WriteString(`.user_id = t0.user_id AND `)
		query.WriteString(alias)
		query.WriteString(`.record_type = t0.record_type AND `)
		query.WriteString(alias)
		query.WriteString(`.record_id = t0.record_id AND `)
		query.WriteString(alias)
		query.WriteString(`.scope = ? AND `)
		query.WriteString(alias)
		query.WriteString(`.user_id = ? AND `)
		appendRecordTypePredicate(&query, alias+`.record_type`, recordTypes)
		query.WriteString(` AND `)
		query.WriteString(alias)
		query.WriteString(`.token = ?`)
		args = append(args, scope.Scope, scope.UserID)
		args = append(args, recordTypeArgs(recordTypes)...)
		args = append(args, tokens[i])
	}
	query.WriteString(` WHERE t0.scope = ? AND t0.user_id = ? AND `)
	appendRecordTypePredicate(&query, "t0.record_type", recordTypes)
	query.WriteString(` AND t0.token = ?`)
	args = append(args, scope.Scope, scope.UserID)
	args = append(args, recordTypeArgs(recordTypes)...)
	args = append(args, tokens[0])
	query.WriteString(fmt.Sprintf(` GROUP BY t0.record_type, t0.record_id LIMIT %d`, maxSearchCandidates))
	return query.String(), args
}

func searchRecordTypes(recordType string) []string {
	switch recordType {
	case creativeKnowledgeRecordType:
		return []string{creativeKnowledgeRecordType}
	case newsKnowledgeRecordType:
		return []string{newsKnowledgeRecordType}
	default:
		return searchableKnowledgeRecordTypes
	}
}

func appendRecordTypePredicate(query *strings.Builder, column string, recordTypes []string) {
	query.WriteString(column)
	if len(recordTypes) == 1 {
		query.WriteString(` = ?`)
		return
	}
	query.WriteString(` IN (`)
	for i := range recordTypes {
		if i > 0 {
			query.WriteString(`, `)
		}
		query.WriteString(`?`)
	}
	query.WriteString(`)`)
}

func recordTypeArgs(recordTypes []string) []any {
	args := make([]any, len(recordTypes))
	for i, recordType := range recordTypes {
		args[i] = recordType
	}
	return args
}

func (s *SQLiteStore) queryCandidates(ctx context.Context, query string, args []any) ([]searchCandidate, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []searchCandidate
	for rows.Next() {
		var candidate searchCandidate
		if err := rows.Scan(&candidate.recordType, &candidate.recordID); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func buildDocumentQuery(candidates []searchCandidate, scope appkm.SearchScope) (string, []any) {
	var query strings.Builder
	query.WriteString(`SELECT record_type, record_id, scope, user_id, title, summary, visibility,
		source_updated_at, indexed_at, content_sha256
		FROM knowledge_memory_search_documents INDEXED BY idx_knowledge_memory_search_documents_lookup
		WHERE `)
	args := make([]any, 0, len(candidates)*4)
	for i, candidate := range candidates {
		if i > 0 {
			query.WriteString(" OR ")
		}
		query.WriteString("(scope = ? AND user_id = ? AND record_type = ? AND record_id = ?")
		args = append(args, scope.Scope, scope.UserID, candidate.recordType, candidate.recordID)
		if scope.Scope == appkm.SearchScopePublic {
			query.WriteString(" AND visibility = 'public'")
		}
		query.WriteString(")")
	}
	return query.String(), args
}

func (s *SQLiteStore) explainIndexedSearch(ctx context.Context, request appkm.SearchRequest) ([]string, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	tokens, err := appkm.SearchTokens(request.Query)
	if err != nil {
		return nil, err
	}
	candidateSQL, candidateArgs := buildCandidateQuery(tokens, request.Scope, request.RecordType)
	plans, err := explainQuery(ctx, s.db, candidateSQL, candidateArgs)
	if err != nil {
		return nil, err
	}
	documentSQL, documentArgs := buildDocumentQuery([]searchCandidate{{recordType: creativeKnowledgeRecordType, recordID: "probe"}}, request.Scope)
	documentPlans, err := explainQuery(ctx, s.db, documentSQL, documentArgs)
	if err != nil {
		return nil, err
	}
	return append(plans, documentPlans...), nil
}

func explainQuery(ctx context.Context, db *sql.DB, query string, args []any) ([]string, error) {
	rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var plans []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			return nil, err
		}
		plans = append(plans, detail)
	}
	return plans, rows.Err()
}
