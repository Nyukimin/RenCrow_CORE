package viewer

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

type KnowledgeRelationReadStore interface {
	KnowledgeRelationSummary(ctx context.Context) (l1sqlite.KnowledgeRelationSummary, error)
	RelatedKnowledgeItems(ctx context.Context, itemID string, maxHop int, limit int) ([]l1sqlite.L1KnowledgeRelationHit, error)
}

type KnowledgeRelationHandlerOptions struct {
	Store   KnowledgeRelationReadStore
	Enabled bool
	MaxHops int
}

type knowledgeRelationItemView struct {
	ItemID     string `json:"item_id"`
	Domain     string `json:"domain"`
	Title      string `json:"title"`
	Summary    string `json:"summary"`
	SourceType string `json:"source_type"`
}

type knowledgeRelationView struct {
	SrcItemID    string  `json:"src_item_id"`
	DstItemID    string  `json:"dst_item_id"`
	RelationType string  `json:"relation_type"`
	Score        float64 `json:"score"`
	Evidence     string  `json:"evidence"`
	Hop          int     `json:"hop"`
}

func HandleKnowledgeRelationSummary(options KnowledgeRelationHandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		summary, warnings := loadKnowledgeRelationSummary(r.Context(), options)
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": options.Enabled, "status": availabilityStatus(warnings), "warnings": warnings, "summary": summary,
		})
	}
}

func HandleKnowledgeRelations(options KnowledgeRelationHandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		itemID := strings.TrimSpace(r.URL.Query().Get("item_id"))
		if itemID == "" {
			http.Error(w, "item_id is required", http.StatusBadRequest)
			return
		}
		maxHop := effectiveKnowledgeRelationMaxHops(options.MaxHops)
		var err error
		if raw := strings.TrimSpace(r.URL.Query().Get("max_hop")); raw != "" {
			maxHop, err = strconv.Atoi(raw)
		}
		if err != nil || maxHop < 1 || maxHop > 2 {
			http.Error(w, "invalid max_hop", http.StatusBadRequest)
			return
		}
		limit, err := parseViewerLimit(r.URL.Query().Get("limit"), 20, 100)
		if err != nil {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		summary, warnings := loadKnowledgeRelationSummary(r.Context(), options)
		items := []knowledgeRelationItemView{}
		relations := []knowledgeRelationView{}
		if options.Store != nil {
			hits, loadErr := options.Store.RelatedKnowledgeItems(r.Context(), itemID, maxHop, limit)
			if loadErr != nil {
				warnings = append(warnings, "knowledge relations unavailable: "+loadErr.Error())
			} else {
				for _, hit := range hits {
					summaryText := strings.TrimSpace(hit.Item.SummaryDraft)
					if summaryText == "" {
						summaryText = "summary unavailable"
					}
					items = append(items, knowledgeRelationItemView{
						ItemID: hit.Item.ID, Domain: hit.Item.Domain, Title: hit.Item.Title, Summary: summaryText, SourceType: hit.Item.Domain,
					})
					relations = append(relations, knowledgeRelationView{
						SrcItemID: hit.ViaItemID, DstItemID: hit.Item.ID, RelationType: hit.RelationType,
						Score: hit.Score, Evidence: hit.Evidence, Hop: hit.Hop,
					})
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled": options.Enabled, "status": availabilityStatus(warnings), "warnings": warnings,
			"summary": summary, "items": items, "relations": relations,
		})
	}
}

func loadKnowledgeRelationSummary(ctx context.Context, options KnowledgeRelationHandlerOptions) (l1sqlite.KnowledgeRelationSummary, []string) {
	summary := l1sqlite.KnowledgeRelationSummary{MaxHop: effectiveKnowledgeRelationMaxHops(options.MaxHops)}
	if options.Store == nil {
		return summary, []string{"knowledge relation store unavailable"}
	}
	loaded, err := options.Store.KnowledgeRelationSummary(ctx)
	if err != nil {
		return summary, []string{"knowledge relation summary unavailable: " + err.Error()}
	}
	loaded.MaxHop = summary.MaxHop
	return loaded, []string{}
}

func effectiveKnowledgeRelationMaxHops(value int) int {
	if value < 1 || value > 2 {
		return 2
	}
	return value
}
