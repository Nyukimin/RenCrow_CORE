package viewer

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type stubSchedulerStore struct {
	schedules []domainscheduler.Schedule
	logs      []domainscheduler.RunLog
}

func (s *stubSchedulerStore) ListSchedules(context.Context, int) ([]domainscheduler.Schedule, error) {
	return append([]domainscheduler.Schedule(nil), s.schedules...), nil
}

func (s *stubSchedulerStore) SaveSchedule(_ context.Context, schedule domainscheduler.Schedule) error {
	if err := domainscheduler.ValidateSchedule(schedule); err != nil {
		return err
	}
	for i := range s.schedules {
		if s.schedules[i].ScheduleID == schedule.ScheduleID {
			s.schedules[i] = schedule
			return nil
		}
	}
	s.schedules = append(s.schedules, schedule)
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
	scheduleID := modulecore.NewScheduleID()
	store := &stubSchedulerStore{}
	handler := HandleScheduler(store)

	createBody := fmt.Sprintf(`{"action":"create","schedule":{"schedule_id":%q,"name":"Backlog heartbeat","schedule":"every 15m","target":"backlog","prompt":"process backlog"}}`, string(scheduleID))
	create := httptest.NewRecorder()
	handler(create, httptest.NewRequest(http.MethodPost, "/viewer/scheduler", bytes.NewBufferString(createBody)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	if len(store.schedules) != 1 || !store.schedules[0].Enabled || store.schedules[0].NextRunAt.IsZero() {
		t.Fatalf("schedules=%#v", store.schedules)
	}

	run := httptest.NewRecorder()
	handler(run, httptest.NewRequest(http.MethodPost, "/viewer/scheduler", bytes.NewBufferString(fmt.Sprintf(`{"action":"run","schedule_id":%q,"trigger":"manual"}`, string(scheduleID)))))
	if run.Code != http.StatusCreated {
		t.Fatalf("run status=%d body=%s", run.Code, run.Body.String())
	}
	if len(store.logs) != 1 || store.logs[0].Trigger != "manual" {
		t.Fatalf("logs=%#v", store.logs)
	}

	disable := httptest.NewRecorder()
	handler(disable, httptest.NewRequest(http.MethodPost, "/viewer/scheduler", bytes.NewBufferString(fmt.Sprintf(`{"action":"disable","schedule_id":%q,"disabled_by":"coder"}`, string(scheduleID)))))
	if disable.Code != http.StatusCreated {
		t.Fatalf("disable status=%d body=%s", disable.Code, disable.Body.String())
	}
	if store.schedules[0].Enabled || store.schedules[0].DisabledBy != "coder" {
		t.Fatalf("disabled schedule=%#v", store.schedules[0])
	}

	list := httptest.NewRecorder()
	handler(list, httptest.NewRequest(http.MethodGet, "/viewer/scheduler", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	for _, want := range []string{`"schedules"`, `"run_logs"`, string(scheduleID), `"manual"`} {
		if !strings.Contains(list.Body.String(), want) {
			t.Fatalf("list body missing %s: %s", want, list.Body.String())
		}
	}
}
