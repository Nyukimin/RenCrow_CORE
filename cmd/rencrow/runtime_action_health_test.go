package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domainhealth "github.com/Nyukimin/RenCrow_CORE/internal/domain/health"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
)

// /health reports the Action store the runtime opened (design IU-6). The owner
// the runtime builds carries that store, and it is the one the Manager uses.
func TestRuntimeActionOwnerCarriesTheStoreItsManagerUses(t *testing.T) {
	workspace := t.TempDir()
	opts := actionpersistence.OpenOptions{Index: true}
	owner, err := newRuntimeActionOwner(workspace, opts)
	if err != nil {
		t.Fatal(err)
	}
	if owner.manager == nil || owner.store == nil {
		t.Fatalf("owner = %+v", owner)
	}
	if h := owner.store.Health(); !h.Indexed || h.Layout != "v1" {
		t.Fatalf("health = %+v", h)
	}
	// The shared owner of one store has one store, and newRuntimeActionManager is
	// the same owner's manager.
	again, err := newRuntimeActionOwner(workspace, opts)
	if err != nil {
		t.Fatal(err)
	}
	if again.store != owner.store || again.manager != owner.manager {
		t.Fatal("a second request for the owner of one store returned another store or manager")
	}
	manager, err := newRuntimeActionManager(workspace, opts)
	if err != nil {
		t.Fatal(err)
	}
	if manager != owner.manager {
		t.Fatal("newRuntimeActionManager is not the manager of the owner")
	}
}

func TestRuntimeActionOwnerWithoutTheSnapshotStillCarriesItsStore(t *testing.T) {
	owner, err := newRuntimeActionOwner(t.TempDir(), actionpersistence.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if h := owner.store.Health(); h.Indexed || h.Layout != "v1" {
		t.Fatalf("health = %+v", h)
	}
}

func TestHealthChecksOfTheRuntimeIncludeTheActionStoreOnlyWhenItIsOpen(t *testing.T) {
	if got := (&Dependencies{}).runtimeHealthChecks(); len(got) != 0 {
		t.Fatalf("checks = %v for a runtime with no Action store", got)
	}
	owner, err := newRuntimeActionOwner(filepath.Join(t.TempDir(), "ws"), actionpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	deps := &Dependencies{actionStoreHealth: owner.store}
	checks := deps.runtimeHealthChecks()
	if len(checks) != 1 || checks[0].Name() != "action_store" {
		t.Fatalf("checks = %v, want the action_store check", checks)
	}
}

func TestBuildHealthServiceRunsTheChecksItIsGiven(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"mio"},{"id":"worker"},{"id":"shiro"},{"id":"kuro"},{"id":"midori"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cfg := &config.Config{LLMGateway: config.LLMGatewayConfig{Enabled: true, BaseURL: srv.URL, TimeoutSec: 1}}

	owner, err := newRuntimeActionOwner(t.TempDir(), actionpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	deps := &Dependencies{actionStoreHealth: owner.store}
	service := buildHealthService(cfg, deps.runtimeHealthChecks()...)

	report := service.RunChecks(context.Background())
	var found bool
	for _, c := range report.Checks {
		if c.Name == "action_store" {
			found = true
			if c.Status != domainhealth.StatusOK || c.Details["layout"] != "v1" {
				t.Fatalf("check = %+v", c)
			}
		}
	}
	if !found {
		t.Fatalf("no action_store check in %+v", report.Checks)
	}
	if !service.IsReady(context.Background()) {
		t.Fatalf("an open Action store takes the runtime out of ready: %+v", report)
	}
	// The CLI health commands have no store and build the service without it.
	for _, c := range buildHealthService(cfg).RunChecks(context.Background()).Checks {
		if c.Name == "action_store" {
			t.Fatal("a service built without the extra checks has an action_store check")
		}
	}
}
