package agent

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesOwnsAdvisorAndAgentProfilePaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		Advisors: statusHandler(http.StatusOK), AdvisorScores: statusHandler(http.StatusCreated),
		Profiles: statusHandler(http.StatusAccepted),
	}})
	for _, tt := range []struct {
		path string
		want int
	}{
		{"/viewer/advisors", http.StatusOK},
		{"/viewer/advisors/scores", http.StatusCreated},
		{"/viewer/agents/profiles", http.StatusAccepted},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
		if rec.Code != tt.want {
			t.Fatalf("%s status=%d want=%d", tt.path, rec.Code, tt.want)
		}
	}
}

func statusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }
}
