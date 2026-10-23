package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/aiworkflow"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/persona"
)

func TestRuntimeHealthChecksIncludeOnlyOpenedSQLiteCheckpointStores(t *testing.T) {
	if got := (&Dependencies{}).runtimeHealthChecks(); len(got) != 0 {
		t.Fatalf("checks = %v with no stores open, want none", got)
	}

	root := filepath.Join(t.TempDir(), "private-checkpoint-store-path")
	events, err := eventstore.NewSQLiteStore(filepath.Join(root, "event_store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	personaStore, err := persona.NewSQLiteStore(filepath.Join(root, "persona.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer personaStore.Close()
	aiWorkflowStore, err := aiworkflow.NewSQLiteStore(filepath.Join(root, "ai_workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer aiWorkflowStore.Close()

	deps := &Dependencies{
		canonicalEventStore:  events,
		personaRuntimeStore:  personaStore,
		aiWorkflowStateStore: aiWorkflowStore,
	}
	checks := deps.runtimeHealthChecks()
	wantNames := []string{
		"event_store_checkpoint",
		"persona_architecture_checkpoint",
		"ai_workflow_checkpoint",
	}
	if len(checks) != len(wantNames) {
		t.Fatalf("checks = %v, want %v", checks, wantNames)
	}
	for i, check := range checks {
		if check.Name() != wantNames[i] {
			t.Fatalf("check[%d].Name() = %q, want %q", i, check.Name(), wantNames[i])
		}
	}

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"mio"},{"id":"worker"},{"id":"shiro"},{"id":"kuro"},{"id":"midori"}]}`))
	}))
	defer gateway.Close()
	cfg := &config.Config{LLMGateway: config.LLMGatewayConfig{Enabled: true, BaseURL: gateway.URL, TimeoutSec: 1}}
	handler := deps.buildHealthHandler(cfg)
	recorder := httptest.NewRecorder()
	handler.HandleHealth(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /health status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "private-checkpoint-store-path") {
		t.Fatalf("GET /health disclosed a store path: %s", recorder.Body.String())
	}
	var report struct {
		Checks []struct {
			Name    string         `json:"name"`
			Status  string         `json:"status"`
			Details map[string]any `json:"details"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Checks) != 8 {
		t.Fatalf("GET /health checks = %+v", report.Checks)
	}
	wantedNames := make(map[string]struct{}, len(wantNames))
	for _, name := range wantNames {
		wantedNames[name] = struct{}{}
	}
	found := make(map[string]bool, len(wantNames))
	for _, check := range report.Checks {
		if _, wanted := wantedNames[check.Name]; !wanted {
			continue
		}
		found[check.Name] = true
		if check.Status != "ok" || check.Details["last_checkpoint_result"] != "pending" || check.Details["last_success_age_sec"] != nil || check.Details["target_interval_sec"] != float64(60) {
			t.Fatalf("GET /health check = %+v, want initial checkpoint grace", check)
		}
	}
	if len(found) != len(wantNames) {
		t.Fatalf("GET /health is missing a checkpoint check: found=%v report=%+v", found, report.Checks)
	}
	ready := httptest.NewRecorder()
	handler.HandleReady(ready, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("GET /health/ready status = %d, body=%s", ready.Code, ready.Body.String())
	}
}

func TestRuntimeHealthChecksIncludeCheckpointOnlyForOpenSQLiteStore(t *testing.T) {
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "event_store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	checks := (&Dependencies{canonicalEventStore: store}).runtimeHealthChecks()
	if len(checks) != 1 || checks[0].Name() != "event_store_checkpoint" {
		t.Fatalf("checks = %v, want only the opened event store checkpoint", checks)
	}
}
