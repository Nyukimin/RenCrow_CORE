package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	llmopsapp "github.com/Nyukimin/RenCrow_CORE/internal/application/llmops"
)

func TestNewLLMOpsCapabilityServiceBuildsProvidersPerNode(t *testing.T) {
	cfg := &config.Config{}
	cfg.LLMCapability.LLMFit.Nodes = []config.LLMFitNodeConfig{
		{ID: "node-a", Mode: "http", Endpoint: "http://127.0.0.1:1"},
		{ID: "local", Mode: "cli"},
		{ID: "broken", Mode: "ssh"},
	}
	cfg.LLMCapability.LLMFit.RequestTimeout = "1s"
	cfg.LLMCapability.LLMFit.TopLimit = 7

	svc := newLLMOpsCapabilityService(cfg)
	ids := svc.NodeIDs()
	if len(ids) != 2 || ids[0] != "node-a" || ids[1] != "local" {
		t.Fatalf("node ids=%v", ids)
	}
}

func TestNewLLMOpsCapabilityServiceHonorsDisabledFlag(t *testing.T) {
	disabled := false
	cfg := &config.Config{}
	cfg.LLMCapability.LLMFit.Enabled = &disabled
	cfg.LLMCapability.LLMFit.Nodes = []config.LLMFitNodeConfig{{ID: "node-a", Mode: "http", Endpoint: "http://127.0.0.1:1"}}

	svc := newLLMOpsCapabilityService(cfg)
	snaps := svc.Nodes(context.Background())
	if len(snaps) != 1 || snaps[0].Status != llmopsapp.StatusDisabled {
		t.Fatalf("snaps=%+v", snaps)
	}
}

func TestBuildLLMOpsHandlersWiresAllRoutes(t *testing.T) {
	cfg := &config.Config{}
	deps := &Dependencies{}
	buildLLMOpsHandlers(cfg, deps)
	if deps.llmOpsNodes == nil || deps.llmOpsNode == nil || deps.llmOpsNodeModels == nil || deps.llmOpsModelMatrix == nil || deps.llmOpsRefresh == nil {
		t.Fatal("all llm ops handlers must be wired")
	}
	rec := httptest.NewRecorder()
	deps.llmOpsNodes(rec, httptest.NewRequest(http.MethodGet, "/viewer/llm-ops/nodes", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"nodes":[]`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLLMOpsDurationFallsBack(t *testing.T) {
	if got := llmOpsDuration("", time.Second); got != time.Second {
		t.Fatalf("empty -> fallback, got %s", got)
	}
	if got := llmOpsDuration("-5s", time.Second); got != time.Second {
		t.Fatalf("negative -> fallback, got %s", got)
	}
	if got := llmOpsDuration(" 2m ", time.Second); got != 2*time.Minute {
		t.Fatalf("valid duration must parse, got %s", got)
	}
}
