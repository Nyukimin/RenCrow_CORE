package governance

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesRegistersGovernancePaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		ToolHarnessRecent:           statusHandler(http.StatusOK),
		DCIRecent:                   statusHandler(http.StatusCreated),
		DCISearch:                   statusHandler(http.StatusAccepted),
		SkillGovernanceRecent:       statusHandler(http.StatusNoContent),
		SkillGovernanceBoot:         statusHandler(http.StatusPartialContent),
		SkillContributionGate:       statusHandler(http.StatusResetContent),
		SkillChangeGate:             statusHandler(http.StatusAlreadyReported),
		SkillChangeEval:             statusHandler(http.StatusIMUsed),
		SkillExternalPRSubmit:       statusHandler(http.StatusMultiStatus),
		PersonaObservation:          statusHandler(http.StatusBadRequest),
		PersonaDiscomfort:           statusHandler(http.StatusConflict),
		PersonaTrigger:              statusHandler(http.StatusForbidden),
		PersonaCanonical:            statusHandler(http.StatusGone),
		PersonaObservationLog:       statusHandler(http.StatusTeapot),
		PersonaObservationAggregate: statusHandler(http.StatusTooEarly),
		PersonaMetaUpdate:           statusHandler(http.StatusUpgradeRequired),
		PersonaMetaUpdateReview:     statusHandler(http.StatusPreconditionRequired),
		PersonaSession:              statusHandler(http.StatusTooManyRequests),
	}})

	tests := []struct {
		path string
		want int
	}{
		{path: "/viewer/tool-harness/recent", want: http.StatusOK},
		{path: "/viewer/dci/recent", want: http.StatusCreated},
		{path: "/viewer/dci/search", want: http.StatusAccepted},
		{path: "/viewer/skill-governance/recent", want: http.StatusNoContent},
		{path: "/viewer/skill-governance/bootstrap", want: http.StatusPartialContent},
		{path: "/viewer/skill-governance/contribution-gate", want: http.StatusResetContent},
		{path: "/viewer/skill-governance/skill-changes", want: http.StatusAlreadyReported},
		{path: "/viewer/skill-governance/skill-change-evals", want: http.StatusIMUsed},
		{path: "/viewer/skill-governance/external-pr-submit", want: http.StatusMultiStatus},
		{path: "/viewer/persona-observation", want: http.StatusBadRequest},
		{path: "/viewer/persona-observation/discomforts", want: http.StatusConflict},
		{path: "/viewer/persona-observation/triggers", want: http.StatusForbidden},
		{path: "/viewer/persona-observation/canonical-responses", want: http.StatusGone},
		{path: "/viewer/persona-observation/observations", want: http.StatusTeapot},
		{path: "/viewer/persona-observation/aggregate", want: http.StatusTooEarly},
		{path: "/viewer/persona-observation/meta-updates", want: http.StatusUpgradeRequired},
		{path: "/viewer/persona-observation/meta-updates/review", want: http.StatusPreconditionRequired},
		{path: "/viewer/persona-observation/sessions", want: http.StatusTooManyRequests},
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
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/skill-governance/recent", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusNotFound)
	}
}

func statusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}
}
