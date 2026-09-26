package llmops

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/rencrow.
type Dependencies struct {
	Ports  Ports
	Routes Routes
}

// Routes groups the LLM Ops (llmfit observation) viewer handlers. All routes
// are Debug Viewer read APIs; Refresh only re-reads llmfit and has no other
// side effect. The handlers are supplied by cmd/rencrow.
type Routes struct {
	Nodes       http.HandlerFunc // GET  /viewer/llm-ops/nodes
	Node        http.HandlerFunc // GET  /viewer/llm-ops/node?node_id=
	NodeModels  http.HandlerFunc // GET  /viewer/llm-ops/node/models?node_id=
	ModelMatrix http.HandlerFunc // GET  /viewer/llm-ops/model/matrix?model_id=
	Refresh     http.HandlerFunc // POST /viewer/llm-ops/refresh
}

// RegisterRoutes registers handlers at the feature route boundary.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/llm-ops/nodes", routes.Nodes)
	registerRoute(mux, "/viewer/llm-ops/node", routes.Node)
	registerRoute(mux, "/viewer/llm-ops/node/models", routes.NodeModels)
	registerRoute(mux, "/viewer/llm-ops/model/matrix", routes.ModelMatrix)
	registerRoute(mux, "/viewer/llm-ops/refresh", routes.Refresh)
}

// StartBackground reserves the feature background-job boundary. Phase 1 has
// no background job: observations are fetched lazily with TTL caching.
func StartBackground(ctx context.Context, deps Dependencies) error {
	_ = ctx
	_ = deps
	return nil
}

func registerRoute(mux *http.ServeMux, pattern string, handler http.HandlerFunc) {
	if mux == nil || pattern == "" || handler == nil {
		return
	}
	mux.HandleFunc(pattern, handler)
}
