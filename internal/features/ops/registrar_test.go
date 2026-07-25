package ops

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesKeepsOpsViewerPaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		Status:           statusHandler(http.StatusOK),
		Jobs:             statusHandler(http.StatusAccepted),
		JobDetail:        statusHandler(http.StatusNoContent),
		Logs:             statusHandler(http.StatusPartialContent),
		RepairRun:        statusHandler(http.StatusCreated),
		Backlog:          statusHandler(http.StatusResetContent),
		Scheduler:        statusHandler(http.StatusAlreadyReported),
		ParallelJobs:     statusHandler(http.StatusCreated),
		JobNotifications: statusHandler(http.StatusAccepted),
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
		{path: "/viewer/parallel-jobs", want: http.StatusCreated},
		{path: "/viewer/job-notifications", want: http.StatusAccepted},
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
