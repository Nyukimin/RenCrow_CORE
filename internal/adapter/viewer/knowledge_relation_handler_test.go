package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

type knowledgeRelationHandlerStore struct{}

func (knowledgeRelationHandlerStore) KnowledgeRelationSummary(context.Context) (l1sqlite.KnowledgeRelationSummary, error) {
	return l1sqlite.KnowledgeRelationSummary{EntityCount: 2, ItemEntityCount: 3, RelationCount: 1, MaxHop: 2}, nil
}

func (knowledgeRelationHandlerStore) RelatedKnowledgeItems(context.Context, string, int, int) ([]l1sqlite.L1KnowledgeRelationHit, error) {
	return []l1sqlite.L1KnowledgeRelationHit{{
		Item: l1sqlite.L1KnowledgeItem{ID: "related", Domain: "github", Title: "Related", SummaryDraft: "safe summary", RawText: "raw secret"},
		Hop:  1, ViaItemID: "seed", RelationType: "same_entity", Score: 5, Evidence: "same entity: mlx",
	}}, nil
}

func TestHandleKnowledgeRelationsReturnsSafeReadModel(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/viewer/knowledge-relations?item_id=seed&max_hop=2&limit=20", nil)
	HandleKnowledgeRelations(KnowledgeRelationHandlerOptions{Store: knowledgeRelationHandlerStore{}, Enabled: true, MaxHops: 2}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "raw secret") || !strings.Contains(rec.Body.String(), "safe summary") {
		t.Fatalf("unsafe or missing response: %s", rec.Body.String())
	}
	var payload struct {
		Relations []struct {
			Hop int `json:"hop"`
		} `json:"relations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil || len(payload.Relations) != 1 || payload.Relations[0].Hop != 1 {
		t.Fatalf("payload=%#v err=%v", payload, err)
	}
}

func TestHandleKnowledgeRelationUnavailableReturnsWarningNot500(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleKnowledgeRelationSummary(KnowledgeRelationHandlerOptions{Enabled: false, MaxHops: 2}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/knowledge-relations/summary", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "unavailable") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleKnowledgeRelationsRejectsHopThree(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleKnowledgeRelations(KnowledgeRelationHandlerOptions{Store: knowledgeRelationHandlerStore{}, Enabled: true, MaxHops: 2}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/knowledge-relations?item_id=seed&max_hop=3", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
