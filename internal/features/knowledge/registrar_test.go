package knowledge

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesRegistersKnowledgePaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		GlossaryRecent:             statusHandler(http.StatusOK),
		KnowledgeMemoryStatus:      statusHandler(http.StatusCreated),
		PersonalArchiveCreate:      statusHandler(http.StatusAccepted),
		CreativeKnowledgeCreate:    statusHandler(http.StatusNoContent),
		NewsKnowledgeCreate:        statusHandler(http.StatusPartialContent),
		DailyIntakeRuleCreate:      statusHandler(http.StatusResetContent),
		TemporalMemoryCreate:       statusHandler(http.StatusAlreadyReported),
		KnowledgeMemoryReview:      statusHandler(http.StatusIMUsed),
		DreamConsolidationCreate:   statusHandler(http.StatusMultiStatus),
		DreamConsolidationProposal: statusHandler(http.StatusBadRequest),
		DreamConsolidationReview:   statusHandler(http.StatusConflict),
	}})

	tests := []struct {
		path string
		want int
	}{
		{path: "/viewer/glossary/recent", want: http.StatusOK},
		{path: "/viewer/knowledge-memory", want: http.StatusCreated},
		{path: "/viewer/knowledge-memory/personal-archive", want: http.StatusAccepted},
		{path: "/viewer/knowledge-memory/creative-knowledge", want: http.StatusNoContent},
		{path: "/viewer/knowledge-memory/news-knowledge", want: http.StatusPartialContent},
		{path: "/viewer/knowledge-memory/daily-intake-rules", want: http.StatusResetContent},
		{path: "/viewer/knowledge-memory/temporal-markers", want: http.StatusAlreadyReported},
		{path: "/viewer/knowledge-memory/review", want: http.StatusIMUsed},
		{path: "/viewer/knowledge-memory/dream-runs", want: http.StatusMultiStatus},
		{path: "/viewer/knowledge-memory/dream-runs/propose", want: http.StatusBadRequest},
		{path: "/viewer/knowledge-memory/dream-runs/review", want: http.StatusConflict},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.want {
				t.Fatalf("status=%d want=%d", rec.Code, tt.want)
			}
		})
	}
}

func TestRegisterRoutesSkipsNilHandlers(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/knowledge-memory", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusNotFound)
	}
}

func statusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}
}
