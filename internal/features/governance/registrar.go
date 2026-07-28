package governance

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/rencrow.
type Dependencies struct {
	Ports  Ports
	Routes Routes
}

// Routes groups Governance route handlers supplied by cmd/rencrow.
// Handler implementations are supplied by their owning adapter and command packages.
type Routes struct {
	ToolHarnessRecent           http.HandlerFunc
	DCIRecent                   http.HandlerFunc
	DCISearch                   http.HandlerFunc
	SkillGovernanceRecent       http.HandlerFunc
	SkillGovernanceBoot         http.HandlerFunc
	SkillContributionGate       http.HandlerFunc
	SkillChangeGate             http.HandlerFunc
	SkillChangeEval             http.HandlerFunc
	SkillExternalPRSubmit       http.HandlerFunc
	PersonaObservation          http.HandlerFunc
	PersonaDiscomfort           http.HandlerFunc
	PersonaTrigger              http.HandlerFunc
	PersonaCanonical            http.HandlerFunc
	PersonaObservationLog       http.HandlerFunc
	PersonaObservationAggregate http.HandlerFunc
	PersonaMetaUpdate           http.HandlerFunc
	PersonaMetaUpdateReview     http.HandlerFunc
	PersonaSession              http.HandlerFunc
}

// RegisterRoutes registers handlers at the feature route boundary.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/tool-harness/recent", routes.ToolHarnessRecent)
	registerRoute(mux, "/viewer/dci/recent", routes.DCIRecent)
	registerRoute(mux, "/viewer/dci/search", routes.DCISearch)
	registerRoute(mux, "/viewer/skill-governance/recent", routes.SkillGovernanceRecent)
	registerRoute(mux, "/viewer/skill-governance/bootstrap", routes.SkillGovernanceBoot)
	registerRoute(mux, "/viewer/skill-governance/contribution-gate", routes.SkillContributionGate)
	registerRoute(mux, "/viewer/skill-governance/skill-changes", routes.SkillChangeGate)
	registerRoute(mux, "/viewer/skill-governance/skill-change-evals", routes.SkillChangeEval)
	registerRoute(mux, "/viewer/skill-governance/external-pr-submit", routes.SkillExternalPRSubmit)
	registerRoute(mux, "/viewer/persona-observation", routes.PersonaObservation)
	registerRoute(mux, "/viewer/persona-observation/discomforts", routes.PersonaDiscomfort)
	registerRoute(mux, "/viewer/persona-observation/triggers", routes.PersonaTrigger)
	registerRoute(mux, "/viewer/persona-observation/canonical-responses", routes.PersonaCanonical)
	registerRoute(mux, "/viewer/persona-observation/observations", routes.PersonaObservationLog)
	registerRoute(mux, "/viewer/persona-observation/aggregate", routes.PersonaObservationAggregate)
	registerRoute(mux, "/viewer/persona-observation/meta-updates", routes.PersonaMetaUpdate)
	registerRoute(mux, "/viewer/persona-observation/meta-updates/review", routes.PersonaMetaUpdateReview)
	registerRoute(mux, "/viewer/persona-observation/sessions", routes.PersonaSession)
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
