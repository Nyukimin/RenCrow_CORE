package knowledge

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/rencrow.
type Dependencies struct {
	Ports  Ports
	Routes Routes
}

// Routes groups Knowledge route handlers supplied by cmd/rencrow.
// Handler implementations stay in legacy adapter/cmd packages during Ver0.80
// migration; this registrar owns only route registration and dependency handoff.
type Routes struct {
	GlossaryRecent             http.HandlerFunc
	KnowledgeMemoryStatus      http.HandlerFunc
	PersonalArchiveCreate      http.HandlerFunc
	CreativeKnowledgeCreate    http.HandlerFunc
	NewsKnowledgeCreate        http.HandlerFunc
	DailyIntakeRuleCreate      http.HandlerFunc
	TemporalMemoryCreate       http.HandlerFunc
	KnowledgeMemoryReview      http.HandlerFunc
	DreamConsolidationCreate   http.HandlerFunc
	DreamConsolidationProposal http.HandlerFunc
	DreamConsolidationReview   http.HandlerFunc
}

// RegisterRoutes reserves the feature route boundary. Existing routes remain in
// their legacy packages until a phase migrates them through this registrar.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/glossary/recent", routes.GlossaryRecent)
	registerRoute(mux, "/viewer/knowledge-memory", routes.KnowledgeMemoryStatus)
	registerRoute(mux, "/viewer/knowledge-memory/personal-archive", routes.PersonalArchiveCreate)
	registerRoute(mux, "/viewer/knowledge-memory/creative-knowledge", routes.CreativeKnowledgeCreate)
	registerRoute(mux, "/viewer/knowledge-memory/news-knowledge", routes.NewsKnowledgeCreate)
	registerRoute(mux, "/viewer/knowledge-memory/daily-intake-rules", routes.DailyIntakeRuleCreate)
	registerRoute(mux, "/viewer/knowledge-memory/temporal-markers", routes.TemporalMemoryCreate)
	registerRoute(mux, "/viewer/knowledge-memory/review", routes.KnowledgeMemoryReview)
	registerRoute(mux, "/viewer/knowledge-memory/dream-runs", routes.DreamConsolidationCreate)
	registerRoute(mux, "/viewer/knowledge-memory/dream-runs/propose", routes.DreamConsolidationProposal)
	registerRoute(mux, "/viewer/knowledge-memory/dream-runs/review", routes.DreamConsolidationReview)
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
