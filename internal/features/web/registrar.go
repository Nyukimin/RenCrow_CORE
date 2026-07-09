package web

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/rencrow.
type Dependencies struct {
	Ports  Ports
	Routes Routes
}

// Routes groups Web/browser route handlers supplied by cmd/rencrow.
// Handler implementations stay in legacy adapter/cmd packages during Ver0.80
// migration; this registrar owns only route registration and dependency handoff.
type Routes struct {
	BrowserTraceAPIStatus          http.HandlerFunc
	BrowserTraceAPIDiscover        http.HandlerFunc
	BrowserTraceAPIValidation      http.HandlerFunc
	BrowserTraceAPIFetcherProposal http.HandlerFunc
	ComplexityHotspotStatus        http.HandlerFunc
	ComplexityHotspotScan          http.HandlerFunc
	ComplexityHotspotProposal      http.HandlerFunc
	ComplexityHotspotConcreteDiff  http.HandlerFunc
	ComplexityHotspotCoderDiff     http.HandlerFunc
}

// RegisterRoutes reserves the feature route boundary. Existing routes remain in
// their legacy packages until a phase migrates them through this registrar.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/browser-trace-api", routes.BrowserTraceAPIStatus)
	registerRoute(mux, "/viewer/browser-trace-api/discover", routes.BrowserTraceAPIDiscover)
	registerRoute(mux, "/viewer/browser-trace-api/validations", routes.BrowserTraceAPIValidation)
	registerRoute(mux, "/viewer/browser-trace-api/fetcher-proposals", routes.BrowserTraceAPIFetcherProposal)
	registerRoute(mux, "/viewer/complexity-hotspots", routes.ComplexityHotspotStatus)
	registerRoute(mux, "/viewer/complexity-hotspots/scan", routes.ComplexityHotspotScan)
	registerRoute(mux, "/viewer/complexity-hotspots/proposals", routes.ComplexityHotspotProposal)
	registerRoute(mux, "/viewer/complexity-hotspots/concrete-diffs", routes.ComplexityHotspotConcreteDiff)
	registerRoute(mux, "/viewer/complexity-hotspots/coder-diffs", routes.ComplexityHotspotCoderDiff)
}

// StartBackground reserves the feature background-job boundary.
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
