package viewer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	appbacklog "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	workstreampersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
)

func TestRevision2AtlasViewerExposesDerivedPipelineStageStates(t *testing.T) {
	items := &atlasHTTPItemStore{items: []domainbacklog.Item{{
		SchemaVersion:      domainbacklog.SchemaVersion2,
		ItemID:             "pipeline-item",
		ImplementationUnit: "unit-pipeline-item",
		Title:              "pipeline item",
		Purpose:            "make stage state visible",
		ConceptState:       domainbacklog.ConceptAdopted,
		DeliveryState:      domainbacklog.DeliveryBuild,
	}}}
	service := appbacklog.NewService(items, workstreampersistence.NewJSONLStore(t.TempDir()))
	handler := HandleAtlas(service)
	record := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/viewer/atlas", nil)
	handler.ServeHTTP(record, request)
	if record.Code != http.StatusOK {
		t.Fatalf("viewer status=%d body=%s", record.Code, record.Body.String())
	}
	var projection map[string]json.RawMessage
	if err := json.Unmarshal(record.Body.Bytes(), &projection); err != nil {
		t.Fatal(err)
	}
	if _, ok := projection["pipeline"]; !ok {
		t.Fatalf("Atlas projection must include derived pipeline stage states: %s", record.Body.String())
	}
}
