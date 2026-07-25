package revenue

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesOwnsRevenuePaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		Status: statusHandler(http.StatusOK), DailyRoutine: statusHandler(http.StatusCreated),
		ExternalSend: statusHandler(http.StatusAccepted), OpportunityWorkstreamGoal: statusHandler(http.StatusNoContent),
	}})
	for _, tt := range []struct {
		path string
		want int
	}{
		{"/viewer/revenue", http.StatusOK},
		{"/viewer/revenue/daily-routine", http.StatusCreated},
		{"/viewer/revenue/channel-drafts/external-send-apply", http.StatusAccepted},
		{"/viewer/revenue/opportunities/workstream-goal", http.StatusNoContent},
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
