package ops

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesKeepsOpsViewerPaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		Status:                 statusHandler(http.StatusOK),
		Jobs:                   statusHandler(http.StatusAccepted),
		JobDetail:              statusHandler(http.StatusNoContent),
		Logs:                   statusHandler(http.StatusPartialContent),
		RepairRun:              statusHandler(http.StatusCreated),
		Backlog:                statusHandler(http.StatusResetContent),
		Scheduler:              statusHandler(http.StatusAlreadyReported),
		Workstreams:            statusHandler(http.StatusConflict),
		WorkstreamGoals:        statusHandler(http.StatusMultiStatus),
		WorkstreamHeartbeats:   statusHandler(http.StatusIMUsed),
		Revenue:                statusHandler(http.StatusNonAuthoritativeInfo),
		RevenueDailyRoutine:    statusHandler(http.StatusUseProxy),
		RevenueExternalSend:    statusHandler(http.StatusTemporaryRedirect),
		ParallelJobs:           statusHandler(http.StatusCreated),
		JobNotifications:       statusHandler(http.StatusAccepted),
		RevenueDecisionReview:  statusHandler(http.StatusOK),
		WorkstreamVaultPreview: statusHandler(http.StatusOK),
	}})

	tests := []struct {
		path string
		want int
	}{
		{path: "/viewer/status", want: http.StatusOK},
		{path: "/viewer/jobs", want: http.StatusAccepted},
		{path: "/viewer/job/detail", want: http.StatusNoContent},
		{path: "/viewer/logs", want: http.StatusPartialContent},
		{path: "/viewer/repair/run", want: http.StatusCreated},
		{path: "/viewer/backlog", want: http.StatusResetContent},
		{path: "/viewer/scheduler", want: http.StatusAlreadyReported},
		{path: "/viewer/workstreams", want: http.StatusConflict},
		{path: "/viewer/workstreams/goals", want: http.StatusMultiStatus},
		{path: "/viewer/workstreams/heartbeats", want: http.StatusIMUsed},
		{path: "/viewer/workstreams/vault-updates/preview", want: http.StatusOK},
		{path: "/viewer/revenue", want: http.StatusNonAuthoritativeInfo},
		{path: "/viewer/revenue/daily-routine", want: http.StatusUseProxy},
		{path: "/viewer/revenue/channel-drafts/external-send-apply", want: http.StatusTemporaryRedirect},
		{path: "/viewer/parallel-jobs", want: http.StatusCreated},
		{path: "/viewer/job-notifications", want: http.StatusAccepted},
		{path: "/viewer/revenue/human-decision-gate/review", want: http.StatusOK},
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
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/backlog", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusNotFound)
	}
}

func statusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}
}
