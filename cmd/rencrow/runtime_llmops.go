package main

import (
	"log"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	llmopsapp "github.com/Nyukimin/RenCrow_CORE/internal/application/llmops"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llmfit"
)

const llmOpsCLIExecutable = "llmfit"

// buildLLMOpsHandlers wires the LLM Ops (llmfit observation) viewer handlers.
// The service is always constructed so the viewer never sees 503 for a
// configured CORE; when llm_ops.llmfit.enabled is false every node reports
// "disabled" and no llmfit call is made.
func buildLLMOpsHandlers(cfg *config.Config, deps *Dependencies) {
	if cfg == nil || deps == nil {
		return
	}
	opts := viewer.LLMOpsHandlerOptions{Service: newLLMOpsCapabilityService(cfg)}
	deps.llmOpsNodes = viewer.HandleLLMOpsNodes(opts)
	deps.llmOpsNode = viewer.HandleLLMOpsNode(opts)
	deps.llmOpsNodeModels = viewer.HandleLLMOpsNodeModels(opts)
	deps.llmOpsModelMatrix = viewer.HandleLLMOpsModelMatrix(opts)
	deps.llmOpsRefresh = viewer.HandleLLMOpsRefresh(opts)
}

// newLLMOpsCapabilityService builds providers from llm_ops.llmfit.nodes.
// Config validation already rejected unknown modes and missing endpoints;
// anything unexpected here is logged and skipped rather than failing startup.
func newLLMOpsCapabilityService(cfg *config.Config) *llmopsapp.CapabilityService {
	fit := cfg.LLMOps.LLMFit
	timeout := llmOpsDuration(fit.RequestTimeout, 5*time.Second)
	nodes := make([]llmopsapp.Node, 0, len(fit.Nodes))
	for _, node := range fit.Nodes {
		id := strings.TrimSpace(node.ID)
		var provider llmfit.CapabilityProvider
		switch strings.ToLower(strings.TrimSpace(node.Mode)) {
		case "http":
			provider = llmfit.NewHTTPProvider(id, node.Endpoint, timeout)
		case "cli":
			provider = llmfit.NewCLIProvider(id, llmOpsCLIExecutable, timeout)
		default:
			log.Printf("[llm-ops] node %q skipped: unsupported mode %q", id, node.Mode)
			continue
		}
		nodes = append(nodes, llmopsapp.Node{ID: id, Provider: provider})
	}
	return llmopsapp.NewCapabilityService(nodes, llmopsapp.Options{
		Disabled:  !fit.IsEnabled(),
		SystemTTL: llmOpsDuration(fit.SystemTTL, 5*time.Minute),
		ModelsTTL: llmOpsDuration(fit.ModelsTTL, 15*time.Minute),
		HealthTTL: llmOpsDuration(fit.HealthTTL, 30*time.Second),
		TopLimit:  fit.TopLimit,
	})
}

func llmOpsDuration(raw string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
