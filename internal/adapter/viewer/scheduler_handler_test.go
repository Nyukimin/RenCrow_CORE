package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	schedulerpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/scheduler"
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
	var response struct {
		RunLog domainscheduler.RunLog `json:"run_log"`
	}
	if err := json.Unmarshal(run.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.RunLog.Status != "failed" || response.RunLog.Error == "" || store.logs[0].Status != "failed" {
		t.Fatalf("unconfigured executor must surface failure: response=%+v stored=%+v", response.RunLog, store.logs[0])
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
	for _, want := range []string{`"schedules"`, `"run_logs"`, string(scheduleID), `"manual"`, `"status":"failed"`, `"error":"scheduler executor unavailable"`} {
		if !strings.Contains(list.Body.String(), want) {
			t.Fatalf("list body missing %s: %s", want, list.Body.String())
		}
	}
}

func TestHandleSchedulerProtectsOwnerManagedTargets(t *testing.T) {
	now := time.Now().UTC()
	meetingID := modulecore.NewScheduleID()
	publicID := modulecore.NewScheduleID()
	store := &stubSchedulerStore{schedules: []domainscheduler.Schedule{
		{
			ScheduleID: meetingID, Name: "private meeting cadence", Schedule: "every 1h", Target: "taskmeeting.daily_report",
			Enabled: true, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), NextRunAt: now.Add(-time.Minute),
		},
		{
			ScheduleID: publicID, Name: "public routine", Schedule: "every 1h", Target: "backlog",
			Enabled: true, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), NextRunAt: now.Add(-time.Minute),
		},
	}}
	store.logs = []domainscheduler.RunLog{
		{RunID: modulecore.NewRunID(), ScheduleID: meetingID, Trigger: "due", Status: "completed", StartedAt: now.Add(-time.Minute)},
		{RunID: modulecore.NewRunID(), ScheduleID: publicID, Trigger: "due", Status: "completed", StartedAt: now.Add(-time.Minute)},
	}
	handler := HandleSchedulerWithExecutorAndProtectedTargets(store, nil, []string{"taskmeeting.daily_report"}, nil)

	list := httptest.NewRecorder()
	handler(list, httptest.NewRequest(http.MethodGet, "/viewer/scheduler", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.Code, list.Body.String())
	}
	if strings.Contains(list.Body.String(), string(meetingID)) || strings.Contains(list.Body.String(), "private meeting cadence") {
		t.Fatalf("owner-managed schedule leaked from list: %s", list.Body.String())
	}
	if !strings.Contains(list.Body.String(), string(publicID)) || !strings.Contains(list.Body.String(), string(store.logs[1].RunID)) {
		t.Fatalf("unrelated schedule was not preserved: %s", list.Body.String())
	}

	create := httptest.NewRecorder()
	handler(create, httptest.NewRequest(http.MethodPost, "/viewer/scheduler", bytes.NewBufferString(
		`{"action":"create","schedule":{"name":"duplicate meeting","schedule":"every 1h","target":"taskmeeting.daily_report"}}`,
	)))
	if create.Code != http.StatusForbidden || len(store.schedules) != 2 {
		t.Fatalf("protected create status=%d schedules=%d body=%s", create.Code, len(store.schedules), create.Body.String())
	}

	for _, action := range []string{"run", "disable"} {
		response := httptest.NewRecorder()
		body := fmt.Sprintf(`{"action":%q,"schedule_id":%q}`, action, string(meetingID))
		handler(response, httptest.NewRequest(http.MethodPost, "/viewer/scheduler", bytes.NewBufferString(body)))
		if response.Code != http.StatusNotFound {
			t.Fatalf("protected %s status=%d body=%s", action, response.Code, response.Body.String())
		}
	}

	due := httptest.NewRecorder()
	handler(due, httptest.NewRequest(http.MethodPost, "/viewer/scheduler", bytes.NewBufferString(`{"action":"due","limit":50}`)))
	if due.Code != http.StatusOK || strings.Contains(due.Body.String(), string(meetingID)) || !strings.Contains(due.Body.String(), string(publicID)) {
		t.Fatalf("protected due response status=%d body=%s", due.Code, due.Body.String())
	}
}

func TestHandleSchedulerProtectedScheduleIDHidesOwnerTarget(t *testing.T) {
	now := time.Now().UTC()
	meetingID := modulecore.NewScheduleID()
	store := &stubSchedulerStore{schedules: []domainscheduler.Schedule{{
		ScheduleID: meetingID, Name: "private meeting cadence", Schedule: "every 1h", Target: "legacy.owner.target",
		Enabled: true, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), NextRunAt: now.Add(-time.Minute),
	}}}
	handler := HandleSchedulerWithExecutorAndProtectedTargets(store, nil, []string{" taskmeeting.daily_report "}, []string{" " + string(meetingID) + " "})
	response := httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodGet, "/viewer/scheduler", nil))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), string(meetingID)) || strings.Contains(response.Body.String(), "private meeting cadence") {
		t.Fatalf("protected ID leaked: status=%d body=%s", response.Code, response.Body.String())
	}

	paddedDisable := httptest.NewRecorder()
	handler(paddedDisable, httptest.NewRequest(http.MethodPost, "/viewer/scheduler", bytes.NewBufferString(
		fmt.Sprintf(`{"action":"disable","schedule_id":%q}`, " "+string(meetingID)+" "),
	)))
	if paddedDisable.Code != http.StatusNotFound || !store.schedules[0].Enabled {
		t.Fatalf("padded protected disable escaped owner policy: status=%d schedule=%+v body=%s", paddedDisable.Code, store.schedules[0], paddedDisable.Body.String())
	}
}

func TestHandleSchedulerRejectsPaddedProtectedTargetAndKeepsNormalTarget(t *testing.T) {
	store := &stubSchedulerStore{}
	handler := HandleSchedulerWithExecutorAndProtectedTargets(store, nil, []string{"taskmeeting.daily_report"}, nil)
	protected := httptest.NewRecorder()
	handler(protected, httptest.NewRequest(http.MethodPost, "/viewer/scheduler", bytes.NewBufferString(
		`{"action":"create","schedule":{"name":"padded owner target","schedule":"every 1h","target":" taskmeeting.daily_report "}}`,
	)))
	if protected.Code != http.StatusBadRequest || len(store.schedules) != 0 {
		t.Fatalf("padded owner target was not rejected before create: status=%d schedules=%d body=%s", protected.Code, len(store.schedules), protected.Body.String())
	}
	normal := httptest.NewRecorder()
	handler(normal, httptest.NewRequest(http.MethodPost, "/viewer/scheduler", bytes.NewBufferString(
		`{"action":"create","schedule":{"name":"public routine","schedule":"every 1h","target":"backlog"}}`,
	)))
	if normal.Code != http.StatusCreated || len(store.schedules) != 1 || store.schedules[0].Target != "backlog" {
		t.Fatalf("unrelated scheduler create regressed: status=%d schedules=%+v body=%s", normal.Code, store.schedules, normal.Body.String())
	}
}

func TestHandleSchedulerFiltersOwnerRowsBeforeBoundsAtNativeStoreScale(t *testing.T) {
	ctx := context.Background()
	store := schedulerpersistence.NewJSONLStore(t.TempDir())
	now := time.Now().UTC()
	publicIDs := make([]modulecore.ScheduleID, 0, 1005)
	for index := 0; index < 1005; index++ {
		scheduleID := modulecore.NewScheduleID()
		publicIDs = append(publicIDs, scheduleID)
		schedule := domainscheduler.Schedule{
			ScheduleID: scheduleID,
			Name:       fmt.Sprintf("public routine %04d", index),
			Schedule:   "every 1h",
			Target:     "backlog",
			Enabled:    true,
			CreatedAt:  now.Add(-time.Hour),
			UpdatedAt:  now.Add(time.Duration(index) * time.Second),
			NextRunAt:  now.Add(-time.Minute),
			Timing:     &modulecore.TaskTiming{Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceNone}},
		}
		if err := store.SaveSchedule(ctx, schedule); err != nil {
			t.Fatalf("save public schedule %d: %v", index, err)
		}
	}
	meetingID := modulecore.NewScheduleID()
	meeting := domainscheduler.Schedule{
		ScheduleID: meetingID, Name: "private daily meeting", Schedule: "every 1h", Target: "taskmeeting.daily_report",
		Enabled: true, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(time.Hour), NextRunAt: now.Add(-time.Minute),
		Timing: &modulecore.TaskTiming{Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceNone}},
	}
	if err := store.SaveSchedule(ctx, meeting); err != nil {
		t.Fatalf("save private meeting schedule: %v", err)
	}
	privateLogID, publicLogID := modulecore.NewRunID(), modulecore.NewRunID()
	for _, entry := range []domainscheduler.RunLog{
		{RunID: publicLogID, ScheduleID: publicIDs[0], Trigger: "due", Status: "completed", StartedAt: now.Add(-time.Minute), CompletedAt: now},
		{RunID: privateLogID, ScheduleID: meetingID, Trigger: "due", Status: "completed", StartedAt: now, CompletedAt: now.Add(time.Second)},
	} {
		if err := store.SaveRunLog(ctx, entry); err != nil {
			t.Fatalf("save scheduler run log: %v", err)
		}
	}
	handler := HandleSchedulerWithExecutorAndProtectedTargets(store, nil, []string{"taskmeeting.daily_report"}, []string{string(meetingID)})

	list := httptest.NewRecorder()
	handler(list, httptest.NewRequest(http.MethodGet, "/viewer/scheduler?limit=2", nil))
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), string(meetingID)) || strings.Contains(list.Body.String(), "private daily meeting") {
		t.Fatalf("large native schedule list did not filter before limit: status=%d body=%s", list.Code, list.Body.String())
	}
	if !strings.Contains(list.Body.String(), string(publicIDs[len(publicIDs)-1])) || !strings.Contains(list.Body.String(), string(publicLogID)) || strings.Contains(list.Body.String(), string(privateLogID)) {
		t.Fatalf("public scheduler history was lost or private history leaked: %s", list.Body.String())
	}

	due := httptest.NewRecorder()
	handler(due, httptest.NewRequest(http.MethodPost, "/viewer/scheduler", bytes.NewBufferString(`{"action":"due","limit":2}`)))
	if due.Code != http.StatusOK || strings.Contains(due.Body.String(), string(meetingID)) || strings.Contains(due.Body.String(), "private daily meeting") || !strings.Contains(due.Body.String(), string(publicIDs[len(publicIDs)-1])) {
		t.Fatalf("large native due list did not filter before limit: status=%d body=%s", due.Code, due.Body.String())
	}
}
