package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type registeredCronSchedulesStub struct {
	records      []RegisteredScheduleCronRecord
	availability string
	err          error
}

func (stub registeredCronSchedulesStub) ListRegisteredCronSchedules(context.Context) ([]RegisteredScheduleCronRecord, string, error) {
	return stub.records, stub.availability, stub.err
}

func TestRegisteredSchedulesCronPagesAndReportsPartial(t *testing.T) {
	reader := registeredCronSchedulesStub{availability: "partial", records: []RegisteredScheduleCronRecord{
		{ID: "cron-a", Title: "ログ整理", Schedule: "0 * * * *", Status: "unknown", RecordType: "job"},
		{ID: "cron-b", Title: "監視基盤", Schedule: "* * * * *", Status: "unknown", RecordType: "execution_mechanism"},
	}}
	handler := NewRegisteredSchedulesHandler(nil, nil, reader, "ren", []byte(registeredScheduleTestToken), nil, nil)
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, newRegisteredSchedulesRequest(registeredSchedulesPath+"?source=cron&limit=1"))
	var page registeredSchedulesResponse
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &page) != nil || page.Availability != "partial" ||
		len(page.Items) != 1 || page.Items[0].RecordType != "job" || page.NextCursor == "" {
		t.Fatalf("first cron page status=%d body=%s", first.Code, first.Body.String())
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, newRegisteredSchedulesRequest(registeredSchedulesPath+"?source=cron&limit=1&cursor="+page.NextCursor))
	page = registeredSchedulesResponse{}
	if second.Code != http.StatusOK || json.Unmarshal(second.Body.Bytes(), &page) != nil || page.Availability != "partial" ||
		len(page.Items) != 1 || page.Items[0].RecordType != "execution_mechanism" || page.NextCursor != "" {
		t.Fatalf("second cron page status=%d body=%s", second.Code, second.Body.String())
	}
}

func TestRegisteredSchedulesCronFailureIsNotEmptySuccess(t *testing.T) {
	reader := registeredCronSchedulesStub{err: errors.New("private command error")}
	handler := NewRegisteredSchedulesHandler(nil, nil, reader, "ren", []byte(registeredScheduleTestToken), nil, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, newRegisteredSchedulesRequest(registeredSchedulesPath+"?source=cron"))
	var page registeredSchedulesResponse
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &page) != nil ||
		page.Availability != "unavailable" || page.Items == nil || len(page.Items) != 0 {
		t.Fatalf("unavailable cron status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
