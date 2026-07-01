package conversation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const (
	defaultDomainGraphAssertionLimit = 50
	maxDomainGraphAssertionLimit     = 200
)

func (s *L1SQLiteStore) PromoteValidatedStagingItemToDomainGraph(ctx context.Context, id string, domain string, entityType string, entityID string, relationType string, confidence float64) (*L1DomainGraphAssertion, error) {
	id = strings.TrimSpace(id)
	domain = normalizeNewsCategory(domain)
	entityType = normalizeDomainGraphToken(entityType)
	entityID = strings.TrimSpace(entityID)
	relationType = normalizeDomainGraphToken(relationType)
	if id == "" {
		return nil, errors.New("l1 staging item id is required")
	}
	if err := validateKnowledgeDomain(domain); err != nil {
		return nil, err
	}
	if entityType == "" {
		return nil, errors.New("domain graph entity_type is required")
	}
	if confidence <= 0 {
		confidence = 0.5
	}
	if confidence > 1 {
		return nil, errors.New("domain graph confidence must be <= 1")
	}
	item, err := s.stagingItemByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.ValidationStatus != L1StagingStatusValidated {
		return nil, fmt.Errorf("l1 staging item must be validated before domain graph promotion: %s", item.ValidationStatus)
	}
	if item.Kind != L1StagingKindExternalFetch && item.Kind != L1StagingKindSearchResult {
		return nil, fmt.Errorf("l1 staging item kind cannot be promoted to domain graph: %s", item.Kind)
	}
	now := time.Now().UTC()
	summary := strings.TrimSpace(item.SummaryDraft)
	if summary == "" {
		summary = strings.TrimSpace(item.RawText)
	}
	evidence := map[string]interface{}{}
	for k, v := range item.Meta {
		evidence[k] = v
	}
	evidence["staging_id"] = item.ID
	evidence["staging_namespace"] = item.Namespace
	evidence["staging_kind"] = item.Kind
	evidence["event_id"] = item.EventID
	evidence["source_id"] = item.SourceID
	evidence["source_url"] = item.SourceURL
	evidence["raw_hash"] = item.RawHash
	evidence["license_note"] = item.LicenseNote
	evidence["validation_status"] = item.ValidationStatus
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal domain graph assertion evidence: %w", err)
	}
	assertion := &L1DomainGraphAssertion{
		ID:               fmt.Sprintf("dg:%s:%s:%s", domain, item.EventID, item.RawHash[:12]),
		StagingID:        item.ID,
		Domain:           domain,
		EntityType:       entityType,
		EntityID:         entityID,
		RelationType:     relationType,
		SourceID:         item.SourceID,
		SourceURL:        item.SourceURL,
		RawHash:          item.RawHash,
		Summary:          summary,
		Confidence:       confidence,
		ValidationStatus: item.ValidationStatus,
		Evidence:         evidence,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO domain_graph_assertion (
	assertion_id, staging_id, domain, entity_type, entity_id, relation_type,
	source_id, source_url, raw_hash, summary, confidence, validation_status,
	evidence_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(staging_id) DO UPDATE SET
	domain = excluded.domain,
	entity_type = excluded.entity_type,
	entity_id = excluded.entity_id,
	relation_type = excluded.relation_type,
	source_id = excluded.source_id,
	source_url = excluded.source_url,
	raw_hash = excluded.raw_hash,
	summary = excluded.summary,
	confidence = excluded.confidence,
	validation_status = excluded.validation_status,
	evidence_json = excluded.evidence_json,
	updated_at = excluded.updated_at
`, assertion.ID, assertion.StagingID, assertion.Domain, assertion.EntityType, assertion.EntityID, assertion.RelationType,
		assertion.SourceID, assertion.SourceURL, assertion.RawHash, assertion.Summary, assertion.Confidence, assertion.ValidationStatus,
		string(evidenceJSON), assertion.CreatedAt, assertion.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to promote l1 staging item to domain graph assertion: %w", err)
	}
	namespace, err := BuildL1Namespace(NamespaceKindKnowledge, "domain_graph_"+domain)
	if err != nil {
		return nil, err
	}
	if _, err := s.AppendEvent(ctx, "domain_graph.promoted_from_staging", namespace, "", 0, map[string]interface{}{
		"assertion_id":  assertion.ID,
		"staging_id":    item.ID,
		"domain":        assertion.Domain,
		"entity_type":   assertion.EntityType,
		"entity_id":     assertion.EntityID,
		"relation_type": assertion.RelationType,
		"source_id":     assertion.SourceID,
		"raw_hash":      assertion.RawHash,
	}, "promoter"); err != nil {
		return nil, fmt.Errorf("failed to append domain graph promoted event log: %w", err)
	}
	return assertion, nil
}

func normalizeDomainGraphToken(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = strings.ReplaceAll(value, " ", "_")
	return value
}

func (s *L1SQLiteStore) DomainGraphAssertions(ctx context.Context, q DomainGraphAssertionQuery) (int, []L1DomainGraphAssertion, error) {
	q, err := normalizeDomainGraphAssertionQuery(q)
	if err != nil {
		return 0, nil, err
	}
	where, args := domainGraphAssertionWhere(q)
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM domain_graph_assertion "+where, args...).Scan(&total); err != nil {
		return 0, nil, fmt.Errorf("failed to count domain graph assertions: %w", err)
	}
	args = append(args, q.Limit, q.Offset)
	rows, err := s.db.QueryContext(ctx, `
SELECT assertion_id, staging_id, domain, entity_type, entity_id, relation_type,
       source_id, source_url, raw_hash, summary, confidence, validation_status,
       evidence_json, created_at, updated_at
FROM domain_graph_assertion
`+where+`
ORDER BY created_at DESC, assertion_id DESC
LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to query domain graph assertions: %w", err)
	}
	defer rows.Close()
	items, err := scanDomainGraphAssertions(rows)
	if err != nil {
		return 0, nil, err
	}
	return total, items, nil
}

func normalizeDomainGraphAssertionQuery(q DomainGraphAssertionQuery) (DomainGraphAssertionQuery, error) {
	q.Domain = normalizeDomainGraphToken(q.Domain)
	q.EntityType = normalizeDomainGraphToken(q.EntityType)
	q.EntityID = strings.TrimSpace(q.EntityID)
	q.RelationType = normalizeDomainGraphToken(q.RelationType)
	q.SourceID = strings.TrimSpace(q.SourceID)
	q.ValidationStatus = strings.TrimSpace(q.ValidationStatus)
	if q.ValidationStatus == "" {
		q.ValidationStatus = L1StagingStatusValidated
	}
	if err := validateDomainGraphValidationStatus(q.ValidationStatus); err != nil {
		return q, err
	}
	if q.Limit <= 0 {
		q.Limit = defaultDomainGraphAssertionLimit
	}
	if q.Limit > maxDomainGraphAssertionLimit {
		q.Limit = maxDomainGraphAssertionLimit
	}
	if q.Offset < 0 {
		return q, errors.New("domain graph assertion offset must be >= 0")
	}
	return q, nil
}

func domainGraphAssertionWhere(q DomainGraphAssertionQuery) (string, []any) {
	conds := []string{"validation_status = ?"}
	args := []any{q.ValidationStatus}
	if q.Domain != "" {
		conds = append(conds, "domain = ?")
		args = append(args, q.Domain)
	}
	if q.EntityType != "" {
		conds = append(conds, "entity_type = ?")
		args = append(args, q.EntityType)
	}
	if q.EntityID != "" {
		conds = append(conds, "entity_id = ?")
		args = append(args, q.EntityID)
	}
	if q.RelationType != "" {
		conds = append(conds, "relation_type = ?")
		args = append(args, q.RelationType)
	}
	if q.SourceID != "" {
		conds = append(conds, "source_id = ?")
		args = append(args, q.SourceID)
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

func scanDomainGraphAssertions(rows *sql.Rows) ([]L1DomainGraphAssertion, error) {
	items := []L1DomainGraphAssertion{}
	for rows.Next() {
		var item L1DomainGraphAssertion
		var evidenceJSON string
		if err := rows.Scan(
			&item.ID,
			&item.StagingID,
			&item.Domain,
			&item.EntityType,
			&item.EntityID,
			&item.RelationType,
			&item.SourceID,
			&item.SourceURL,
			&item.RawHash,
			&item.Summary,
			&item.Confidence,
			&item.ValidationStatus,
			&evidenceJSON,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan domain graph assertion: %w", err)
		}
		if strings.TrimSpace(evidenceJSON) == "" {
			item.Evidence = map[string]interface{}{}
		} else if err := json.Unmarshal([]byte(evidenceJSON), &item.Evidence); err != nil {
			return nil, fmt.Errorf("failed to decode domain graph assertion evidence: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate domain graph assertions: %w", err)
	}
	return items, nil
}

func validateDomainGraphValidationStatus(status string) error {
	switch strings.TrimSpace(status) {
	case L1StagingStatusPending, L1StagingStatusValidated, L1StagingStatusRejected:
		return nil
	default:
		return fmt.Errorf("invalid domain graph assertion validation_status: %s", status)
	}
}
