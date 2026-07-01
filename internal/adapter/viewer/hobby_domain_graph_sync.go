package viewer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	conversationpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation"
)

const (
	defaultHobbyDomainGraphSyncLimit = 200
	maxHobbyDomainGraphSyncLimit     = 500
)

type HobbyDomainGraphAssertionStore interface {
	DomainGraphAssertions(ctx context.Context, q conversationpersistence.DomainGraphAssertionQuery) (int, []conversationpersistence.L1DomainGraphAssertion, error)
}

type hobbyDomainGraphSyncResult struct {
	Available   bool           `json:"available"`
	DBPath      string         `json:"db_path"`
	Domain      string         `json:"domain"`
	EntityType  string         `json:"entity_type"`
	Checked     int            `json:"checked"`
	Upserted    int            `json:"upserted"`
	Skipped     int            `json:"skipped"`
	ItemIDs     []string       `json:"item_ids"`
	SkipReasons map[string]int `json:"skip_reasons"`

	RelationChecked     int            `json:"relation_checked"`
	RelationUpserted    int            `json:"relation_upserted"`
	RelationSkipped     int            `json:"relation_skipped"`
	RelationSkipReasons map[string]int `json:"relation_skip_reasons,omitempty"`
}

type hobbyDomainGraphItemSyncResult struct {
	Checked     int
	Upserted    int
	Skipped     int
	ItemIDs     []string
	SkipReasons map[string]int
}

type hobbyDomainGraphRelationSyncResult struct {
	Checked     int
	Upserted    int
	Skipped     int
	SkipReasons map[string]int
}

type hobbyDomainGraphItemUpsert struct {
	ItemID          string
	Category        string
	ItemType        string
	Title           string
	NormalizedTitle string
	ExternalIDs     map[string]interface{}
	Metadata        map[string]interface{}
}

type hobbyDomainGraphRelationUpsert struct {
	RelationID   string
	FromItem     hobbyDomainGraphItemUpsert
	ToItem       hobbyDomainGraphItemUpsert
	RelationType string
	Source       string
	EvidenceURL  string
	Evidence     map[string]interface{}
}

func HandleHobbyDomainGraphSync(opts HobbyGraphOptions, store HobbyDomainGraphAssertionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if store == nil {
			http.Error(w, "hobby domain graph sync unavailable", http.StatusServiceUnavailable)
			return
		}
		limit, err := hobbyDomainGraphSyncLimit(r)
		if err != nil {
			http.Error(w, "invalid hobby domain graph sync request", http.StatusBadRequest)
			return
		}
		domain := normalizeHobbyGraphToken(r.URL.Query().Get("domain"))
		dbPath := resolveHobbyGraphWritableDBPath(opts.DBPath)
		if strings.TrimSpace(dbPath) == "" {
			http.Error(w, "hobby domain graph sync unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
			http.Error(w, "hobby domain graph sync unavailable", http.StatusServiceUnavailable)
			return
		}
		db, err := sql.Open("sqlite3", dbPath)
		if err != nil {
			http.Error(w, "hobby domain graph sync unavailable", http.StatusServiceUnavailable)
			return
		}
		defer db.Close()

		_, itemAssertions, err := store.DomainGraphAssertions(r.Context(), conversationpersistence.DomainGraphAssertionQuery{
			Domain:           domain,
			EntityType:       "work",
			ValidationStatus: conversationpersistence.L1StagingStatusValidated,
			Limit:            limit,
		})
		if err != nil {
			http.Error(w, "failed to sync hobby domain graph assertions", http.StatusInternalServerError)
			return
		}
		_, relationAssertions, err := store.DomainGraphAssertions(r.Context(), conversationpersistence.DomainGraphAssertionQuery{
			Domain:           domain,
			EntityType:       "work_relation",
			ValidationStatus: conversationpersistence.L1StagingStatusValidated,
			Limit:            limit,
		})
		if err != nil {
			http.Error(w, "failed to sync hobby domain graph assertions", http.StatusInternalServerError)
			return
		}
		itemResult, err := syncHobbyDomainGraphItemAssertions(r.Context(), db, itemAssertions)
		if err != nil {
			http.Error(w, "failed to sync hobby domain graph assertions", http.StatusInternalServerError)
			return
		}
		relationResult, err := syncHobbyDomainGraphRelationAssertions(r.Context(), db, relationAssertions)
		if err != nil {
			http.Error(w, "failed to sync hobby domain graph assertions", http.StatusInternalServerError)
			return
		}
		writeHobbyDomainGraphSyncJSON(w, hobbyDomainGraphSyncResult{
			Available:           true,
			DBPath:              dbPath,
			Domain:              domain,
			EntityType:          "work",
			Checked:             itemResult.Checked,
			Upserted:            itemResult.Upserted,
			Skipped:             itemResult.Skipped,
			ItemIDs:             itemResult.ItemIDs,
			SkipReasons:         itemResult.SkipReasons,
			RelationChecked:     relationResult.Checked,
			RelationUpserted:    relationResult.Upserted,
			RelationSkipped:     relationResult.Skipped,
			RelationSkipReasons: relationResult.SkipReasons,
		})
	}
}

func hobbyDomainGraphSyncLimit(r *http.Request) (int, error) {
	limit := defaultHobbyDomainGraphSyncLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid limit")
		}
		if n > maxHobbyDomainGraphSyncLimit {
			n = maxHobbyDomainGraphSyncLimit
		}
		limit = n
	}
	return limit, nil
}

func syncHobbyDomainGraphItemAssertions(ctx context.Context, db *sql.DB, items []conversationpersistence.L1DomainGraphAssertion) (hobbyDomainGraphItemSyncResult, error) {
	result := hobbyDomainGraphItemSyncResult{
		Checked:     len(items),
		ItemIDs:     []string{},
		SkipReasons: map[string]int{},
	}
	if err := ensureHobbyGraphTables(ctx, db); err != nil {
		return result, err
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].UpdatedAt.Before(items[j].UpdatedAt)
	})
	for _, item := range items {
		work, skipReason := hobbyDomainGraphWorkItemFromAssertion(item)
		if skipReason != "" {
			result.Skipped++
			result.SkipReasons[skipReason]++
			continue
		}
		if err := upsertHobbyDomainGraphItem(ctx, db, work); err != nil {
			return result, err
		}
		result.Upserted++
		result.ItemIDs = append(result.ItemIDs, work.ItemID)
	}
	return result, nil
}

func syncHobbyDomainGraphRelationAssertions(ctx context.Context, db *sql.DB, items []conversationpersistence.L1DomainGraphAssertion) (hobbyDomainGraphRelationSyncResult, error) {
	result := hobbyDomainGraphRelationSyncResult{
		Checked:     len(items),
		SkipReasons: map[string]int{},
	}
	if err := ensureHobbyGraphTables(ctx, db); err != nil {
		return result, err
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].UpdatedAt.Before(items[j].UpdatedAt)
	})
	for _, item := range items {
		relation, skipReason := hobbyDomainGraphRelationFromAssertion(item)
		if skipReason != "" {
			result.Skipped++
			result.SkipReasons[skipReason]++
			continue
		}
		if err := upsertHobbyDomainGraphItem(ctx, db, relation.FromItem); err != nil {
			return result, err
		}
		if err := upsertHobbyDomainGraphItem(ctx, db, relation.ToItem); err != nil {
			return result, err
		}
		if err := upsertHobbyDomainGraphRelation(ctx, db, relation); err != nil {
			return result, err
		}
		result.Upserted++
	}
	return result, nil
}

func hobbyDomainGraphWorkItemFromAssertion(item conversationpersistence.L1DomainGraphAssertion) (hobbyDomainGraphItemUpsert, string) {
	category, skipReason := hobbyDomainGraphCategory(item.Domain)
	if skipReason != "" {
		return hobbyDomainGraphItemUpsert{}, skipReason
	}
	entityID := strings.TrimSpace(item.EntityID)
	if entityID == "" {
		return hobbyDomainGraphItemUpsert{}, "missing_entity_id"
	}
	title := hobbyDomainGraphEvidenceString(item.Evidence, "title", "work_title", "source_title")
	if title == "" {
		title = strings.TrimSpace(item.Summary)
	}
	if title == "" {
		title = entityID
	}
	if strings.TrimSpace(item.Summary) == "" && strings.TrimSpace(item.SourceURL) == "" && title == entityID {
		return hobbyDomainGraphItemUpsert{}, "empty_work_payload"
	}
	return hobbyDomainGraphBuildItem(item, category, "work", entityID, title), ""
}

func hobbyDomainGraphRelationFromAssertion(item conversationpersistence.L1DomainGraphAssertion) (hobbyDomainGraphRelationUpsert, string) {
	category, skipReason := hobbyDomainGraphCategory(item.Domain)
	if skipReason != "" {
		return hobbyDomainGraphRelationUpsert{}, skipReason
	}
	relationType := normalizeHobbyGraphToken(item.RelationType)
	if relationType == "" {
		relationType = normalizeHobbyGraphToken(hobbyDomainGraphEvidenceString(item.Evidence, "relation_type"))
	}
	if relationType == "" {
		return hobbyDomainGraphRelationUpsert{}, "missing_relation_type"
	}
	sourceEntityID := hobbyDomainGraphEvidenceString(item.Evidence, "source_item_id", "from_item_id", "work_id")
	if sourceEntityID == "" {
		sourceEntityID = strings.TrimSpace(item.EntityID)
	}
	if sourceEntityID == "" {
		return hobbyDomainGraphRelationUpsert{}, "missing_source_entity_id"
	}
	targetEntityID := hobbyDomainGraphEvidenceString(item.Evidence, "target_item_id", "to_item_id", "object_id", "creator_id", "author_id", "person_id")
	if targetEntityID == "" {
		return hobbyDomainGraphRelationUpsert{}, "missing_target_entity_id"
	}
	sourceItemType := normalizeHobbyGraphToken(hobbyDomainGraphEvidenceString(item.Evidence, "source_item_type"))
	if sourceItemType == "" {
		sourceItemType = "work"
	}
	targetItemType := normalizeHobbyGraphToken(hobbyDomainGraphEvidenceString(item.Evidence, "target_item_type"))
	if targetItemType == "" {
		targetItemType = hobbyDomainGraphTargetItemType(relationType)
	}
	sourceTitle := hobbyDomainGraphEvidenceString(item.Evidence, "source_title", "work_title", "title")
	if sourceTitle == "" {
		sourceTitle = strings.TrimSpace(item.Summary)
	}
	if sourceTitle == "" {
		sourceTitle = sourceEntityID
	}
	targetTitle := hobbyDomainGraphEvidenceString(item.Evidence, "target_title", "target_label", "creator_name", "author_name", "person_name", "name")
	if targetTitle == "" {
		targetTitle = targetEntityID
	}
	fromItem := hobbyDomainGraphBuildItem(item, category, sourceItemType, sourceEntityID, sourceTitle)
	toItem := hobbyDomainGraphBuildItem(item, category, targetItemType, targetEntityID, targetTitle)
	evidence := map[string]interface{}{
		"assertion_id":  item.ID,
		"domain":        item.Domain,
		"entity_id":     item.EntityID,
		"relation_type": relationType,
		"source_url":    item.SourceURL,
		"summary":       item.Summary,
		"raw_evidence":  item.Evidence,
	}
	return hobbyDomainGraphRelationUpsert{
		RelationID:   hobbyGraphStableID("hobby_relation", fromItem.ItemID, toItem.ItemID, relationType, "domain_graph"),
		FromItem:     fromItem,
		ToItem:       toItem,
		RelationType: relationType,
		Source:       "domain_graph",
		EvidenceURL:  strings.TrimSpace(item.SourceURL),
		Evidence:     evidence,
	}, ""
}

func hobbyDomainGraphBuildItem(item conversationpersistence.L1DomainGraphAssertion, category string, itemType string, entityID string, title string) hobbyDomainGraphItemUpsert {
	normalizedTitle := normalizeHobbyGraphTitle(title)
	itemID := hobbyGraphStableID("hobby_item", category, itemType, strings.TrimSpace(entityID))
	return hobbyDomainGraphItemUpsert{
		ItemID:          itemID,
		Category:        category,
		ItemType:        itemType,
		Title:           strings.TrimSpace(title),
		NormalizedTitle: normalizedTitle,
		ExternalIDs: map[string]interface{}{
			"domain_graph_entity_id": strings.TrimSpace(entityID),
		},
		Metadata: map[string]interface{}{
			"assertion_id": item.ID,
			"source_url":   item.SourceURL,
			"summary":      item.Summary,
		},
	}
}

func upsertHobbyDomainGraphItem(ctx context.Context, db *sql.DB, item hobbyDomainGraphItemUpsert) error {
	externalIDsJSON, err := json.Marshal(item.ExternalIDs)
	if err != nil {
		return err
	}
	metadataJSON, err := json.Marshal(item.Metadata)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
INSERT INTO hobby_items(item_id, category, item_type, title, normalized_title, external_ids_json, metadata_json, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT(item_id) DO UPDATE SET
	category = excluded.category,
	item_type = excluded.item_type,
	title = excluded.title,
	normalized_title = excluded.normalized_title,
	external_ids_json = excluded.external_ids_json,
	metadata_json = excluded.metadata_json,
	updated_at = excluded.updated_at
`, item.ItemID, item.Category, item.ItemType, item.Title, item.NormalizedTitle, string(externalIDsJSON), string(metadataJSON))
	return err
}

func upsertHobbyDomainGraphRelation(ctx context.Context, db *sql.DB, relation hobbyDomainGraphRelationUpsert) error {
	evidenceJSON, err := json.Marshal(relation.Evidence)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
INSERT INTO hobby_relations(relation_id, from_item_id, to_item_id, relation_type, source, evidence_url, evidence_json, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(relation_id) DO UPDATE SET
	from_item_id = excluded.from_item_id,
	to_item_id = excluded.to_item_id,
	relation_type = excluded.relation_type,
	source = excluded.source,
	evidence_url = excluded.evidence_url,
	evidence_json = excluded.evidence_json
`, relation.RelationID, relation.FromItem.ItemID, relation.ToItem.ItemID, relation.RelationType, relation.Source, nullableString(relation.EvidenceURL), string(evidenceJSON))
	return err
}

func hobbyDomainGraphCategory(raw string) (string, string) {
	category := normalizeHobbyGraphToken(raw)
	if category == "" {
		return "", "missing_domain"
	}
	if category == "movie" {
		return "", "movie_domain"
	}
	return category, ""
}

func hobbyDomainGraphTargetItemType(relationType string) string {
	switch relationType {
	case "created_by":
		return "creator"
	case "performed_by":
		return "artist"
	case "directed_by":
		return "person"
	case "published_by":
		return "publisher"
	case "developed_by":
		return "studio"
	case "part_of_series":
		return "series"
	default:
		return "related"
	}
}

func hobbyDomainGraphEvidenceString(evidence map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if evidence == nil {
			return ""
		}
		value, ok := evidence[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		}
	}
	return ""
}

func writeHobbyDomainGraphSyncJSON(w http.ResponseWriter, payload hobbyDomainGraphSyncResult) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}
