package l1sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgerelation"
)

func (s *L1SQLiteStore) UpsertKnowledgeEntity(ctx context.Context, entity L1KnowledgeEntity) error {
	entity.EntityID = strings.TrimSpace(entity.EntityID)
	entity.CanonicalName = strings.TrimSpace(entity.CanonicalName)
	entity.EntityType = strings.TrimSpace(entity.EntityType)
	if entity.EntityID == "" {
		return errors.New("l1 knowledge entity_id is required")
	}
	if entity.CanonicalName == "" {
		return errors.New("l1 knowledge canonical_name is required")
	}
	if entity.EntityType == "" {
		return errors.New("l1 knowledge entity_type is required")
	}
	now := time.Now().UTC()
	if entity.CreatedAt.IsZero() {
		entity.CreatedAt = now
	}
	if entity.UpdatedAt.IsZero() {
		entity.UpdatedAt = now
	}
	aliasesJSON, err := json.Marshal(entity.Aliases)
	if err != nil {
		return fmt.Errorf("failed to marshal l1 knowledge entity aliases: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO l1_knowledge_entity (entity_id, canonical_name, entity_type, aliases_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(entity_id) DO UPDATE SET
	canonical_name = excluded.canonical_name,
	entity_type = excluded.entity_type,
	aliases_json = excluded.aliases_json,
	updated_at = excluded.updated_at
`, entity.EntityID, entity.CanonicalName, entity.EntityType, string(aliasesJSON), entity.CreatedAt, entity.UpdatedAt); err != nil {
		return fmt.Errorf("failed to upsert l1 knowledge entity: %w", err)
	}
	return nil
}

func (s *L1SQLiteStore) LinkKnowledgeItemEntity(ctx context.Context, link L1KnowledgeItemEntity) error {
	link.ItemID = strings.TrimSpace(link.ItemID)
	link.EntityID = strings.TrimSpace(link.EntityID)
	link.RelationKind = strings.TrimSpace(link.RelationKind)
	if link.ItemID == "" {
		return errors.New("l1 knowledge item entity item_id is required")
	}
	if link.EntityID == "" {
		return errors.New("l1 knowledge item entity entity_id is required")
	}
	if link.RelationKind == "" {
		return errors.New("l1 knowledge item entity relation_kind is required")
	}
	if link.CreatedAt.IsZero() {
		link.CreatedAt = time.Now().UTC()
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO l1_knowledge_item_entity (item_id, entity_id, relation_kind, score, evidence, created_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(item_id, entity_id, relation_kind) DO UPDATE SET
	score = excluded.score,
	evidence = excluded.evidence
`, link.ItemID, link.EntityID, link.RelationKind, link.Score, link.Evidence, link.CreatedAt); err != nil {
		return fmt.Errorf("failed to link l1 knowledge item entity: %w", err)
	}
	return nil
}

func (s *L1SQLiteStore) UpsertKnowledgeRelation(ctx context.Context, relation knowledgerelation.Relation) error {
	relation.SrcItemID = strings.TrimSpace(relation.SrcItemID)
	relation.DstItemID = strings.TrimSpace(relation.DstItemID)
	relation.RelationType = strings.TrimSpace(relation.RelationType)
	relation.Evidence = strings.TrimSpace(relation.Evidence)
	if relation.SrcItemID == "" {
		return errors.New("l1 knowledge relation src_item_id is required")
	}
	if relation.DstItemID == "" {
		return errors.New("l1 knowledge relation dst_item_id is required")
	}
	if relation.RelationType == "" {
		return errors.New("l1 knowledge relation relation_type is required")
	}
	if relation.Evidence == "" {
		return errors.New("l1 knowledge relation evidence is required")
	}
	now := time.Now().UTC()
	if relation.CreatedAt.IsZero() {
		relation.CreatedAt = now
	}
	if relation.UpdatedAt.IsZero() {
		relation.UpdatedAt = now
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO l1_knowledge_item_relation (src_item_id, dst_item_id, relation_type, score, evidence, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(src_item_id, dst_item_id, relation_type) DO UPDATE SET
	score = excluded.score,
	evidence = excluded.evidence,
	updated_at = excluded.updated_at
`, relation.SrcItemID, relation.DstItemID, relation.RelationType, relation.Score, relation.Evidence, relation.CreatedAt, relation.UpdatedAt); err != nil {
		return fmt.Errorf("failed to upsert l1 knowledge relation: %w", err)
	}
	return nil
}

func (s *L1SQLiteStore) RelatedKnowledgeRelations(ctx context.Context, srcItemID string, limit int) ([]L1KnowledgeItemRelation, error) {
	srcItemID = strings.TrimSpace(srcItemID)
	if srcItemID == "" {
		return nil, errors.New("l1 knowledge relation src_item_id is required")
	}
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT src_item_id, dst_item_id, relation_type, score, evidence, created_at, updated_at
FROM l1_knowledge_item_relation
WHERE src_item_id = ?
ORDER BY score DESC, updated_at DESC
LIMIT ?
`, srcItemID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query l1 knowledge relations: %w", err)
	}
	defer rows.Close()
	return scanL1KnowledgeItemRelations(rows)
}

func scanL1KnowledgeItemRelations(rows *sql.Rows) ([]L1KnowledgeItemRelation, error) {
	var relations []L1KnowledgeItemRelation
	for rows.Next() {
		var relation L1KnowledgeItemRelation
		if err := rows.Scan(
			&relation.SrcItemID,
			&relation.DstItemID,
			&relation.RelationType,
			&relation.Score,
			&relation.Evidence,
			&relation.CreatedAt,
			&relation.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan l1 knowledge relation: %w", err)
		}
		relations = append(relations, relation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate l1 knowledge relations: %w", err)
	}
	return relations, nil
}
