package reports

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/rencrow.
type Dependencies struct {
	Ports  Ports
	Routes Routes
}

// Routes groups Reports/Evidence route handlers supplied by cmd/rencrow.
// Handler implementations are supplied by their owning adapter and command packages.
type Routes struct {
	EvidenceRecent      http.HandlerFunc
	EvidenceDetail      http.HandlerFunc
	EvidenceSummary     http.HandlerFunc
	VerificationRecent  http.HandlerFunc
	VerificationDetail  http.HandlerFunc
	VerificationSummary http.HandlerFunc
}

// RegisterRoutes registers handlers at the feature route boundary.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/evidence/recent", routes.EvidenceRecent)
	registerRoute(mux, "/viewer/evidence/detail", routes.EvidenceDetail)
	registerRoute(mux, "/viewer/evidence/summary", routes.EvidenceSummary)
	registerRoute(mux, "/viewer/verification/recent", routes.VerificationRecent)
	registerRoute(mux, "/viewer/verification/detail", routes.VerificationDetail)
	registerRoute(mux, "/viewer/verification/summary", routes.VerificationSummary)
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
