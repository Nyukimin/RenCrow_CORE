package viewer

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
)

type stubSchedulerStore struct {
	jobs []domainscheduler.Job
	logs []domainscheduler.RunLog
}

func (s *stubSchedulerStore) ListJobs(context.Context, int) ([]domainscheduler.Job, error) {
	return append([]domainscheduler.Job(nil), s.jobs...), nil
}

func (s *stubSchedulerStore) SaveJob(_ context.Context, job domainscheduler.Job) error {
	if err := domainscheduler.ValidateJob(job); err != nil {
		return err
	}
	for i := range s.jobs {
		if s.jobs[i].JobID == job.JobID {
			s.jobs[i] = job
			return nil
		}
	}
	s.jobs = append(s.jobs, job)
	return nil
}

func (s *stubSchedulerStore) SaveRunLog(_ context.Context, log domainscheduler.RunLog) error {
	if err := domainscheduler.ValidateRunLog(log); err != nil {
		return err
	}
	s.logs = append(s.logs, log)
	return nil
}

func (s *stubSchedulerStore) ListRunLogs(context.Context, int) ([]domainscheduler.RunLog, error) {
	return append([]domainscheduler.RunLog(nil), s.logs...), nil
}

func TestHandleSchedulerCreateRunDisableAndList(t *testing.T) {
	store := &stubSchedulerStore{}
	handler := HandleScheduler(store)

	create := httptest.NewRecorder()
	handler(create, httptest.NewRequest(http.MethodPost, "/viewer/scheduler", bytes.NewBufferString(`{
		"action":"create",
		"job":{
			"job_id":"sched_backlog",
			"name":"Backlog heartbeat",
			"schedule":"every 15m",
			"target":"backlog",
			"prompt":"process backlog"
		}
	}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	if len(store.jobs) != 1 || !store.jobs[0].Enabled || store.jobs[0].NextRunAt.IsZero() {
		t.Fatalf("jobs=%#v", store.jobs)
	}

	run := httptest.NewRecorder()
	handler(run, httptest.NewRequest(http.MethodPost, "/viewer/scheduler", bytes.NewBufferString(`{"action":"run","job_id":"sched_backlog","trigger":"manual"}`)))
	if run.Code != http.StatusCreated {
		t.Fatalf("run status=%d body=%s", run.Code, run.Body.String())
	}
	if len(store.logs) != 1 || store.logs[0].Trigger != "manual" {
		t.Fatalf("logs=%#v", store.logs)
	}

	disable := httptest.NewRecorder()
	handler(disable, httptest.NewRequest(http.MethodPost, "/viewer/scheduler", bytes.NewBufferString(`{"action":"disable","job_id":"sched_backlog","disabled_by":"coder"}`)))
	if disable.Code != http.StatusCreated {
		t.Fatalf("disable status=%d body=%s", disable.Code, disable.Body.String())
	}
	if store.jobs[0].Enabled || store.jobs[0].DisabledBy != "coder" {
		t.Fatalf("disabled job=%#v", store.jobs[0])
	}

	list := httptest.NewRecorder()
	handler(list, httptest.NewRequest(http.MethodGet, "/viewer/scheduler", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	for _, want := range []string{`"jobs"`, `"run_logs"`, `"sched_backlog"`, `"manual"`} {
		if !strings.Contains(list.Body.String(), want) {
			t.Fatalf("list body missing %s: %s", want, list.Body.String())
		}
	}
}
