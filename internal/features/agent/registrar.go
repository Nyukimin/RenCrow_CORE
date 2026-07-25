package agent

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/rencrow.
type Dependencies struct {
	Ports  Ports
	Routes Routes
}

type Routes struct {
	Advisors        http.HandlerFunc
	AdvisorRuns     http.HandlerFunc
	AdvisorScores   http.HandlerFunc
	Profiles        http.HandlerFunc
	PolicyDecisions http.HandlerFunc
}

func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/advisors", routes.Advisors)
	registerRoute(mux, "/viewer/advisors/runs", routes.AdvisorRuns)
	registerRoute(mux, "/viewer/advisors/scores", routes.AdvisorScores)
	registerRoute(mux, "/viewer/agents/profiles", routes.Profiles)
	registerRoute(mux, "/viewer/agents/policy-decisions", routes.PolicyDecisions)
}

func registerRoute(mux *http.ServeMux, pattern string, handler http.HandlerFunc) {
	if mux != nil && pattern != "" && handler != nil {
		mux.HandleFunc(pattern, handler)
	}
}

// StartBackground reserves the feature background-job boundary.
func StartBackground(ctx context.Context, deps Dependencies) error {
	_ = ctx
	_ = deps
	return nil
}
