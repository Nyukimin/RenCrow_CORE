package l1sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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

func (s *L1SQLiteStore) SaveKnowledgeEntity(ctx context.Context, entity L1KnowledgeEntity) error {
	return s.UpsertKnowledgeEntity(ctx, entity)
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

func (s *L1SQLiteStore) SaveKnowledgeItemEntity(ctx context.Context, link L1KnowledgeItemEntity) error {
	return s.LinkKnowledgeItemEntity(ctx, link)
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

func (s *L1SQLiteStore) SaveKnowledgeItemRelation(ctx context.Context, relation knowledgerelation.Relation) error {
	return s.UpsertKnowledgeRelation(ctx, relation)
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

func (s *L1SQLiteStore) RelatedKnowledgeItems(ctx context.Context, itemID string, maxHop int, limit int) ([]L1KnowledgeRelationHit, error) {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return nil, errors.New("l1 knowledge relation item_id is required")
	}
	if maxHop < 1 || maxHop > 2 {
		return nil, errors.New("l1 knowledge relation max_hop must be 1 or 2")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	type queueItem struct {
		itemID    string
		hop       int
		pathScore float64
		evidence  string
	}
	queue := []queueItem{{itemID: itemID, hop: 0}}
	visited := map[string]bool{itemID: true}
	hits := make([]L1KnowledgeRelationHit, 0, limit)
	adjacencyLimit := limit * 4
	if adjacencyLimit < 20 {
		adjacencyLimit = 20
	}
	if adjacencyLimit > 100 {
		adjacencyLimit = 100
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current.hop >= maxHop {
			continue
		}
		relations, err := s.RelatedKnowledgeRelations(ctx, current.itemID, adjacencyLimit)
		if err != nil {
			return nil, err
		}
		for _, relation := range relations {
			if visited[relation.DstItemID] {
				continue
			}
			item, err := s.knowledgeItemByID(ctx, relation.DstItemID)
			if err != nil {
				return nil, err
			}
			if item == nil {
				continue
			}
			visited[relation.DstItemID] = true
			hop := current.hop + 1
			score := relation.Score
			if current.pathScore > 0 && current.pathScore < score {
				score = current.pathScore
			}
			evidence := relation.Evidence
			if current.evidence != "" {
				evidence = current.evidence + "; " + evidence
			}
			hits = append(hits, L1KnowledgeRelationHit{
				Item: *item, Hop: hop, ViaItemID: current.itemID, RelationType: relation.RelationType,
				Score: score, Evidence: evidence,
			})
			queue = append(queue, queueItem{itemID: relation.DstItemID, hop: hop, pathScore: score, evidence: evidence})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			if hits[i].Hop == hits[j].Hop {
				return hits[i].Item.ID < hits[j].Item.ID
			}
			return hits[i].Hop < hits[j].Hop
		}
		return hits[i].Score > hits[j].Score
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func (s *L1SQLiteStore) knowledgeItemByID(ctx context.Context, itemID string) (*L1KnowledgeItem, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, staging_id, domain, title, source_id, source_url, raw_text, raw_hash,
       summary_draft, keywords_json, license_note, meta_json, created_at, updated_at
FROM l1_knowledge_item WHERE id = ? LIMIT 1`, itemID)
	if err != nil {
		return nil, fmt.Errorf("failed to query l1 knowledge item: %w", err)
	}
	defer rows.Close()
	items, err := ScanL1KnowledgeItems(rows)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	return &items[0], nil
}

func (s *L1SQLiteStore) KnowledgeRelationSummary(ctx context.Context) (KnowledgeRelationSummary, error) {
	if s == nil || s.db == nil {
		return KnowledgeRelationSummary{}, errors.New("l1 knowledge relation store is unavailable")
	}
	summary := KnowledgeRelationSummary{MaxHop: 2}
	for query, target := range map[string]*int{
		"SELECT COUNT(*) FROM l1_knowledge_entity":        &summary.EntityCount,
		"SELECT COUNT(*) FROM l1_knowledge_item_entity":   &summary.ItemEntityCount,
		"SELECT COUNT(*) FROM l1_knowledge_item_relation": &summary.RelationCount,
	} {
		if err := s.db.QueryRowContext(ctx, query).Scan(target); err != nil {
			return KnowledgeRelationSummary{}, fmt.Errorf("failed to count knowledge relations: %w", err)
		}
	}
	return summary, nil
}
