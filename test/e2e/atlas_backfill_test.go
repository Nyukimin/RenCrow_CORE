package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	viewer "github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	backlogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	backlogfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/backlog"
	backloginfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/backlog"
	workstreaminfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
)

func TestAtlasBackfillPackageThroughHTTPIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pkg, err := backlogfeature.LoadBackfillPackage()
	if err != nil {
		t.Fatal(err)
	}
	store := backloginfra.NewJSONLStore(filepath.Join(t.TempDir(), "backlog.jsonl"))
	service := backlogapp.NewService(store, workstreaminfra.NewJSONLStore(t.TempDir())).WithFeatures(pkg.FeatureMaps())
	first, err := service.ReconcileBackfill(ctx, pkg)
	if err != nil || first.Imported != 114 {
		t.Fatalf("first reconcile=%+v err=%v", first, err)
	}
	second, err := service.ReconcileBackfill(ctx, pkg)
	if err != nil || second.Imported != 0 || second.Updated != 0 || second.Skipped != 114 {
		t.Fatalf("second reconcile=%+v err=%v", second, err)
	}

	mux := http.NewServeMux()
	backlogfeature.RegisterRoutes(mux, backlogfeature.Dependencies{Routes: backlogfeature.Routes{Atlas: viewer.HandleAtlas(service)}})
	server := httptest.NewServer(mux)
	defer server.Close()

	var projection struct {
		Features []map[string]any `json:"features"`
		Backlog  []map[string]any `json:"backlog"`
	}
	getAtlasJSON(t, server.URL+"/viewer/atlas", &projection)
	if len(projection.Features) != 114 || len(projection.Backlog) != 114 {
		t.Fatalf("projection features=%d backlog=%d", len(projection.Features), len(projection.Backlog))
	}

	var detail struct {
		Item struct {
			Purpose           string   `json:"purpose"`
			Problem           string   `json:"problem"`
			Idea              string   `json:"idea"`
			SpecificationRefs []string `json:"specification_refs"`
		} `json:"item"`
		Resolved []map[string]any `json:"resolved_specifications"`
	}
	getAtlasJSON(t, server.URL+"/viewer/atlas/items/atlas:atlas.lifecycle", &detail)
	if detail.Item.Purpose == "" || detail.Item.Problem == "" || detail.Item.Idea == "" || len(detail.Item.SpecificationRefs) == 0 || len(detail.Resolved) == 0 {
		t.Fatalf("incomplete item detail: %+v", detail)
	}

	var specification struct {
		Specification struct {
			SpecID        string `json:"spec_id"`
			Content       string `json:"content"`
			BodyAvailable bool   `json:"body_available"`
		} `json:"specification"`
	}
	getAtlasJSON(t, server.URL+"/viewer/atlas/specifications/spec_atlas_lifecycle_functional_v1", &specification)
	if specification.Specification.SpecID == "" || !specification.Specification.BodyAvailable || specification.Specification.Content == "" {
		t.Fatalf("specification body unavailable: %+v", specification.Specification)
	}
}

func getAtlasJSON(t *testing.T, url string, target any) {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status=%d", url, response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatal(err)
	}
}
