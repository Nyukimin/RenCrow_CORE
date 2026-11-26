package viewer

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const registeredScheduleTestToken = "registered-schedules-test-token"

type registeredSchedulesSchedulerStub struct {
	schedules []domainscheduler.Schedule
	err       error
}

func (s *registeredSchedulesSchedulerStub) ListSchedules(context.Context, int) ([]domainscheduler.Schedule, error) {
	return append([]domainscheduler.Schedule(nil), s.schedules...), s.err
}
func (*registeredSchedulesSchedulerStub) SaveSchedule(context.Context, domainscheduler.Schedule) error {
	return nil
}
func (*registeredSchedulesSchedulerStub) SaveRunLog(context.Context, domainscheduler.RunLog) error {
	return nil
}
func (*registeredSchedulesSchedulerStub) ListRunLogs(context.Context, int) ([]domainscheduler.RunLog, error) {
	return nil, nil
}

type registeredSchedulesSchedulerPageStub struct {
	*registeredSchedulesSchedulerStub
	ignoreKeep bool
}

func (s *registeredSchedulesSchedulerPageStub) ListSchedulesPage(
	ctx context.Context,
	afterID string,
	limit int,
	keep func(domainscheduler.Schedule) bool,
) ([]domainscheduler.Schedule, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	schedules := append([]domainscheduler.Schedule(nil), s.schedules...)
	sort.Slice(schedules, func(i, j int) bool { return schedules[i].ScheduleID < schedules[j].ScheduleID })
	items := make([]domainscheduler.Schedule, 0, limit+1)
	for _, schedule := range schedules {
		if string(schedule.ScheduleID) <= afterID || !s.ignoreKeep && keep != nil && !keep(schedule) {
			continue
		}
		items = append(items, schedule)
		if len(items) > limit {
			return items[:limit], true, nil
		}
	}
	return items, false, nil
}

type registeredSchedulesHeartbeatStub struct {
	schedules []domainworkstream.HeartbeatSchedule
	err       error
}

func (s *registeredSchedulesHeartbeatStub) ListHeartbeatSchedules(context.Context, int) ([]domainworkstream.HeartbeatSchedule, error) {
	return append([]domainworkstream.HeartbeatSchedule(nil), s.schedules...), s.err
}

type registeredSchedulesHeartbeatPageStub struct {
	*registeredSchedulesHeartbeatStub
}

func (s *registeredSchedulesHeartbeatPageStub) ListHeartbeatSchedulesPage(
	ctx context.Context,
	afterID string,
	limit int,
) ([]domainworkstream.HeartbeatSchedule, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	schedules := append([]domainworkstream.HeartbeatSchedule(nil), s.schedules...)
	sort.Slice(schedules, func(i, j int) bool { return schedules[i].ScheduleID < schedules[j].ScheduleID })
	items := make([]domainworkstream.HeartbeatSchedule, 0, limit+1)
	for _, schedule := range schedules {
		if string(schedule.ScheduleID) <= afterID {
			continue
		}
		items = append(items, schedule)
		if len(items) > limit {
			return items[:limit], true, nil
		}
	}
	return items, false, nil
}

func newRegisteredSchedulesRequest(path string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil)
	request.RemoteAddr = "127.0.0.1:49152"
	request.Header.Set("Authorization", "Bearer "+registeredScheduleTestToken)
	request.Header.Set("X-RenCrow-Client", "RenCrow_BOARD")
	request.Header.Set("X-RenCrow-Interaction-Profile", "board-studio-readonly")
	return request
}

func TestRegisteredSchedulesProjectsOwnerDataAndPages(t *testing.T) {
	now := time.Date(2026, 11, 24, 2, 0, 0, 0, time.UTC)
	due := now.Add(time.Hour)
	scheduler := &registeredSchedulesSchedulerStub{schedules: []domainscheduler.Schedule{
		{ScheduleID: modulecore.ScheduleID("sched_b"), Name: "Second", Description: "safe description", Schedule: "every 15m", Prompt: "secret command", Enabled: false, NextRunAt: due},
		{ScheduleID: modulecore.ScheduleID("sched_a"), Name: "First", Schedule: "every 15m", Enabled: true},
		{ScheduleID: modulecore.ScheduleID("protected"), Name: "Protected", Schedule: "every 1h", Target: "protected.target", Enabled: true},
		{ScheduleID: modulecore.ScheduleID("protected-id"), Name: "Protected by ID", Schedule: "every 1h", Enabled: true},
	}}
	handler := NewRegisteredSchedulesHandler(scheduler, nil, nil, "ren", []byte(registeredScheduleTestToken), []string{"protected.target"}, []string{"protected-id"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, newRegisteredSchedulesRequest(registeredSchedulesPath+"?source=scheduler&limit=1"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("first page status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var first registeredSchedulesResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.Source != "scheduler" || first.Availability != "available" || len(first.Items) != 1 || first.Items[0].ID != "sched_a" || first.Items[0].Status != "enabled" || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	secondPath := registeredSchedulesPath + "?source=scheduler&limit=1&cursor=" + first.NextCursor
	secondRecorder := httptest.NewRecorder()
	handler.ServeHTTP(secondRecorder, newRegisteredSchedulesRequest(secondPath))
	var second registeredSchedulesResponse
	if secondRecorder.Code != http.StatusOK || json.Unmarshal(secondRecorder.Body.Bytes(), &second) != nil || len(second.Items) != 1 || second.Items[0].ID != "sched_b" || second.Items[0].Status != "disabled" || second.Items[0].Description != "safe description" || second.Items[0].NextRunAt == nil {
		t.Fatalf("second page status=%d body=%s", secondRecorder.Code, secondRecorder.Body.String())
	}
	if strings.Contains(secondRecorder.Body.String(), "secret command") || strings.Contains(secondRecorder.Body.String(), "protected") {
		t.Fatalf("sensitive or protected schedule leaked: %s", secondRecorder.Body.String())
	}
}

func TestRegisteredSchedulesProjectsWorkstreamAndRejectsInvalidCursor(t *testing.T) {
	store := &registeredSchedulesHeartbeatStub{schedules: []domainworkstream.HeartbeatSchedule{
		{ScheduleID: modulecore.ScheduleID("heartbeat-a"), Task: "daily review", ScheduleText: "daily 00:00", Status: "paused"},
	}}
	handler := NewRegisteredSchedulesHandler(nil, store, nil, "ren", []byte(registeredScheduleTestToken), nil, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, newRegisteredSchedulesRequest(registeredSchedulesPath+"?source=workstream"))
	var response registeredSchedulesResponse
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &response) != nil || len(response.Items) != 1 || response.Items[0].Title != "daily review" || response.Items[0].Description != "daily review" || response.Items[0].Schedule != "daily 00:00" || response.Items[0].Status != "paused" {
		t.Fatalf("workstream projection status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	bad := httptest.NewRecorder()
	handler.ServeHTTP(bad, newRegisteredSchedulesRequest(registeredSchedulesPath+"?source=workstream&cursor=not-base64"))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status=%d body=%s", bad.Code, bad.Body.String())
	}
}

func TestRegisteredScheduleDescriptionsRetainContentAndMarkTruncation(t *testing.T) {
	text := strings.Repeat("説明", registeredSchedulesMaxDescriptionRunes/2+20)
	workstream := projectRegisteredHeartbeatSchedule(domainworkstream.HeartbeatSchedule{
		ScheduleID: modulecore.ScheduleID("heartbeat-long"), Task: text,
	})
	if len([]rune(workstream.Title)) != 200 || len([]rune(workstream.Description)) != registeredSchedulesMaxDescriptionRunes || !workstream.DescriptionTruncated {
		t.Fatalf("workstream description projection title=%d description=%d truncated=%v", len([]rune(workstream.Title)), len([]rune(workstream.Description)), workstream.DescriptionTruncated)
	}
	scheduler := projectRegisteredSchedulerSchedule(domainscheduler.Schedule{
		ScheduleID: modulecore.ScheduleID("scheduler-long"), Name: "Job", Description: text,
	})
	if len([]rune(scheduler.Description)) != registeredSchedulesMaxDescriptionRunes || !scheduler.DescriptionTruncated {
		t.Fatalf("scheduler description projection length=%d truncated=%v", len([]rune(scheduler.Description)), scheduler.DescriptionTruncated)
	}
}

func TestRegisteredSchedulesFailsClosedAndReportsUnavailable(t *testing.T) {
	handler := NewRegisteredSchedulesHandler(&registeredSchedulesSchedulerStub{err: errors.New("private store error")}, nil, nil, "ren", []byte(registeredScheduleTestToken), []string{"protected.target"}, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, newRegisteredSchedulesRequest(registeredSchedulesPath+"?source=scheduler"))
	var response registeredSchedulesResponse
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &response) != nil || response.Availability != "unavailable" || len(response.Items) != 0 || strings.Contains(recorder.Body.String(), "private store error") {
		t.Fatalf("unavailable response status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	unauthenticated := httptest.NewRecorder()
	request := newRegisteredSchedulesRequest(registeredSchedulesPath + "?source=scheduler")
	request.Header.Del("Authorization")
	handler.ServeHTTP(unauthenticated, request)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}
}

func TestRegisteredSchedulesMarksSkippedInvalidIDsPartial(t *testing.T) {
	store := &registeredSchedulesSchedulerStub{schedules: []domainscheduler.Schedule{{
		ScheduleID: modulecore.ScheduleID("invalid\u0085id"), Name: "Invalid ID", Schedule: "every 15m", Enabled: true,
	}}}
	handler := NewRegisteredSchedulesHandler(store, nil, nil, "ren", []byte(registeredScheduleTestToken), nil, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, newRegisteredSchedulesRequest(registeredSchedulesPath+"?source=scheduler"))
	var response registeredSchedulesResponse
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &response) != nil || response.Availability != "partial" || len(response.Items) != 0 {
		t.Fatalf("invalid ID omission was not marked partial: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRegisteredSchedulesEmptyPagesUseArrayAndMissingTitlesGetFallback(t *testing.T) {
	emptyHandler := NewRegisteredSchedulesHandler(&registeredSchedulesSchedulerStub{}, nil, nil, "ren", []byte(registeredScheduleTestToken), nil, nil)
	empty := httptest.NewRecorder()
	emptyHandler.ServeHTTP(empty, newRegisteredSchedulesRequest(registeredSchedulesPath+"?source=scheduler"))
	var emptyResponse registeredSchedulesResponse
	if empty.Code != http.StatusOK || json.Unmarshal(empty.Body.Bytes(), &emptyResponse) != nil || emptyResponse.Items == nil || len(emptyResponse.Items) != 0 {
		t.Fatalf("empty response must contain items array: status=%d body=%s", empty.Code, empty.Body.String())
	}

	store := &registeredSchedulesSchedulerStub{schedules: []domainscheduler.Schedule{{ScheduleID: modulecore.ScheduleID("sched-no-title"), Schedule: "every 1h"}}}
	handler := NewRegisteredSchedulesHandler(store, nil, nil, "ren", []byte(registeredScheduleTestToken), nil, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, newRegisteredSchedulesRequest(registeredSchedulesPath+"?source=scheduler"))
	var response registeredSchedulesResponse
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &response) != nil || len(response.Items) != 1 || response.Items[0].Title == "" {
		t.Fatalf("empty title was not projected safely: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestSafeRegisteredScheduleIDPreservesDistinctLongIDs(t *testing.T) {
	left := safeRegisteredScheduleID(strings.Repeat("x", 2048) + "left")
	right := safeRegisteredScheduleID(strings.Repeat("x", 2048) + "right")
	if len([]rune(left)) != 2048 || len([]rune(right)) != 2048 || left == right || safeRegisteredScheduleID("bad\nid") != "" {
		t.Fatalf("IDs were not safely projected: left length=%d right length=%d equal=%v", len([]rune(left)), len([]rune(right)), left == right)
	}
}

func TestRegisteredSchedulesCursorSupportsMaximumUTF8IDsAcrossPages(t *testing.T) {
	firstID := "🧑" + strings.Repeat("😀", registeredSchedulesMaxIDRunes-1)
	secondID := "🧒" + strings.Repeat("😀", registeredSchedulesMaxIDRunes-1)
	if len([]rune(firstID)) != registeredSchedulesMaxIDRunes || len([]byte(firstID)) != registeredSchedulesMaxIDBytes {
		t.Fatalf("fixture ID length = %d runes / %d bytes", len([]rune(firstID)), len([]byte(firstID)))
	}
	pager := &registeredSchedulesSchedulerPageStub{registeredSchedulesSchedulerStub: &registeredSchedulesSchedulerStub{
		schedules: []domainscheduler.Schedule{
			{ScheduleID: modulecore.ScheduleID(secondID), Name: "second", Schedule: "every 1h"},
			{ScheduleID: modulecore.ScheduleID(firstID), Name: "first", Schedule: "every 1h"},
		},
	}}
	handler := NewRegisteredSchedulesHandler(pager, nil, nil, "ren", []byte(registeredScheduleTestToken), nil, nil)
	firstRecorder := httptest.NewRecorder()
	handler.ServeHTTP(firstRecorder, newRegisteredSchedulesRequest(registeredSchedulesPath+"?source=scheduler&limit=1"))
	var first registeredSchedulesResponse
	if firstRecorder.Code != http.StatusOK || json.Unmarshal(firstRecorder.Body.Bytes(), &first) != nil ||
		len(first.Items) != 1 || first.Items[0].ID != firstID || first.NextCursor == "" {
		t.Fatalf("maximum-ID first page status=%d body=%s", firstRecorder.Code, firstRecorder.Body.String())
	}
	if len(first.NextCursor) <= 4096 || len(first.NextCursor) > registeredSchedulesMaxCursor {
		t.Fatalf("cursor length=%d, want >4096 and <=%d", len(first.NextCursor), registeredSchedulesMaxCursor)
	}
	path := registeredSchedulesPath + "?source=scheduler&limit=1&cursor=" + first.NextCursor
	if len(strings.SplitN(path, "?", 2)[1]) > registeredSchedulesMaxQuery {
		t.Fatalf("request query length=%d exceeds max=%d", len(strings.SplitN(path, "?", 2)[1]), registeredSchedulesMaxQuery)
	}
	secondRecorder := httptest.NewRecorder()
	handler.ServeHTTP(secondRecorder, newRegisteredSchedulesRequest(path))
	var second registeredSchedulesResponse
	if secondRecorder.Code != http.StatusOK || json.Unmarshal(secondRecorder.Body.Bytes(), &second) != nil ||
		len(second.Items) != 1 || second.Items[0].ID != secondID || second.NextCursor != "" {
		t.Fatalf("maximum-ID second page status=%d body=%s", secondRecorder.Code, secondRecorder.Body.String())
	}
}

func TestRegisteredSchedulesPagedOwnersReachEndAndRecheckProtectedRows(t *testing.T) {
	const count = 1005
	schedules := make([]domainscheduler.Schedule, 0, count)
	protectedID := modulecore.NewScheduleID()
	for index := 0; index < count; index++ {
		id := modulecore.NewScheduleID()
		schedule := domainscheduler.Schedule{ScheduleID: id, Name: "schedule", Schedule: "every 1h"}
		if index == 0 {
			schedule.ScheduleID = protectedID
			schedule.Target = "protected.target"
		}
		schedules = append(schedules, schedule)
	}
	base := &registeredSchedulesSchedulerStub{schedules: schedules}
	scheduler := &registeredSchedulesSchedulerPageStub{registeredSchedulesSchedulerStub: base, ignoreKeep: true}
	handler := NewRegisteredSchedulesHandler(scheduler, nil, nil, "ren", []byte(registeredScheduleTestToken), []string{"protected.target"}, nil)
	seen := make(map[string]struct{}, count-1)
	cursor := ""
	partialSeen := false
	for pageNumber := 0; ; pageNumber++ {
		path := registeredSchedulesPath + "?source=scheduler&limit=100"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, newRegisteredSchedulesRequest(path))
		var response registeredSchedulesResponse
		if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &response) != nil {
			t.Fatalf("scheduler page %d status=%d body=%s", pageNumber, recorder.Code, recorder.Body.String())
		}
		partialSeen = partialSeen || response.Availability == "partial"
		for index, item := range response.Items {
			if index > 0 && response.Items[index-1].ID >= item.ID {
				t.Fatalf("scheduler page %d not sorted: %+v", pageNumber, response.Items)
			}
			if item.ID == string(protectedID) || item.ID == "" {
				t.Fatalf("protected or empty ID leaked: %+v", item)
			}
			if _, duplicate := seen[item.ID]; duplicate {
				t.Fatalf("duplicate scheduler ID %q", item.ID)
			}
			seen[item.ID] = struct{}{}
		}
		cursor = response.NextCursor
		if cursor == "" {
			break
		}
		if pageNumber >= 20 {
			t.Fatal("scheduler pagination did not terminate")
		}
	}
	if len(seen) != count-1 || !partialSeen {
		t.Fatalf("scheduler visible count=%d want=%d partial=%t", len(seen), count-1, partialSeen)
	}

	heartbeats := make([]domainworkstream.HeartbeatSchedule, 0, count)
	for index := 0; index < count; index++ {
		heartbeats = append(heartbeats, domainworkstream.HeartbeatSchedule{
			ScheduleID: modulecore.NewScheduleID(), Task: "workstream", ScheduleText: "daily 08:00", Status: "active",
		})
	}
	workstream := &registeredSchedulesHeartbeatPageStub{registeredSchedulesHeartbeatStub: &registeredSchedulesHeartbeatStub{schedules: heartbeats}}
	workstreamHandler := NewRegisteredSchedulesHandler(nil, workstream, nil, "ren", []byte(registeredScheduleTestToken), nil, nil)
	seen = make(map[string]struct{}, count)
	cursor = ""
	for pageNumber := 0; ; pageNumber++ {
		path := registeredSchedulesPath + "?source=workstream&limit=100"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		recorder := httptest.NewRecorder()
		workstreamHandler.ServeHTTP(recorder, newRegisteredSchedulesRequest(path))
		var response registeredSchedulesResponse
		if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &response) != nil {
			t.Fatalf("workstream page %d status=%d body=%s", pageNumber, recorder.Code, recorder.Body.String())
		}
		for _, item := range response.Items {
			if _, duplicate := seen[item.ID]; duplicate {
				t.Fatalf("duplicate workstream ID %q", item.ID)
			}
			seen[item.ID] = struct{}{}
		}
		cursor = response.NextCursor
		if cursor == "" {
			break
		}
		if pageNumber >= 20 {
			t.Fatal("workstream pagination did not terminate")
		}
	}
	if len(seen) != count {
		t.Fatalf("workstream visible count=%d want=%d", len(seen), count)
	}
}

func TestRegisteredSchedulesLegacyOwnerStopsAtCapAndKeepsPartialSignal(t *testing.T) {
	schedules := make([]domainscheduler.Schedule, registeredSchedulesScanLimit)
	for index := range schedules {
		schedules[index] = domainscheduler.Schedule{
			ScheduleID: modulecore.ScheduleID(fmt.Sprintf("schedule-%04d", index)),
			Name:       "legacy", Schedule: "every 1h",
		}
	}
	handler := NewRegisteredSchedulesHandler(&registeredSchedulesSchedulerStub{schedules: schedules}, nil, nil, "ren", []byte(registeredScheduleTestToken), nil, nil)
	seen := make(map[string]struct{}, len(schedules))
	cursor := ""
	for pageNumber := 0; ; pageNumber++ {
		path := registeredSchedulesPath + "?source=scheduler&limit=100"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, newRegisteredSchedulesRequest(path))
		var response registeredSchedulesResponse
		if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &response) != nil {
			t.Fatalf("legacy page %d status=%d body=%s", pageNumber, recorder.Code, recorder.Body.String())
		}
		if response.Availability != "partial" || response.IncompleteReason != "owner_pagination_unavailable" {
			t.Fatalf("legacy cap not reported: %+v", response)
		}
		for _, item := range response.Items {
			seen[item.ID] = struct{}{}
		}
		cursor = response.NextCursor
		if cursor == "" {
			break
		}
		if pageNumber >= 20 {
			t.Fatal("legacy pagination did not terminate")
		}
	}
	if len(seen) != registeredSchedulesScanLimit {
		t.Fatalf("legacy visible count=%d want=%d", len(seen), registeredSchedulesScanLimit)
	}
}

func TestRegisteredSchedulesRejectsOldWrongSourceAndStaleCursors(t *testing.T) {
	handler := NewRegisteredSchedulesHandler(&registeredSchedulesSchedulerStub{schedules: []domainscheduler.Schedule{{
		ScheduleID: "schedule-a", Name: "A", Schedule: "every 1h",
	}}}, nil, nil, "ren", []byte(registeredScheduleTestToken), nil, nil)
	wrongSource := encodeRegisteredSchedulesCursor("workstream", "schedule-a")
	oldCursor := base64.RawURLEncoding.EncodeToString([]byte("scheduler\x00" + strings.Repeat("a", sha256.Size*2)))
	for name, cursor := range map[string]string{
		"wrong source": wrongSource,
		"old version":  oldCursor,
		"stale ID":     encodeRegisteredSchedulesCursor("scheduler", "missing-id"),
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, newRegisteredSchedulesRequest(registeredSchedulesPath+"?source=scheduler&cursor="+cursor))
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s cursor status=%d body=%s", name, recorder.Code, recorder.Body.String())
		}
	}
}

func TestRegisteredSchedulesDroppedPageItemsBecomePartialWithoutEmptyCursorLoop(t *testing.T) {
	store := &registeredSchedulesSchedulerPageStub{
		registeredSchedulesSchedulerStub: &registeredSchedulesSchedulerStub{schedules: []domainscheduler.Schedule{{
			ScheduleID: "bad\nid", Name: "invalid", Schedule: "every 1h",
		}}},
		ignoreKeep: true,
	}
	// This test double reports a continuation after a page containing only an
	// invalid identifier; the handler must stop rather than issue a looping cursor.
	page := registeredSchedulesSchedulerPageStubWithMore{registeredSchedulesSchedulerPageStub: store}
	handler := NewRegisteredSchedulesHandler(page, nil, nil, "ren", []byte(registeredScheduleTestToken), nil, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, newRegisteredSchedulesRequest(registeredSchedulesPath+"?source=scheduler&limit=1"))
	var response registeredSchedulesResponse
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &response) != nil ||
		response.Availability != "partial" || response.NextCursor != "" || response.IncompleteReason != "" || len(response.Items) != 0 {
		t.Fatalf("invalid-only page should stop partially: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

type registeredSchedulesSchedulerPageStubWithMore struct {
	*registeredSchedulesSchedulerPageStub
}

func (s registeredSchedulesSchedulerPageStubWithMore) ListSchedulesPage(
	ctx context.Context,
	afterID string,
	limit int,
	keep func(domainscheduler.Schedule) bool,
) ([]domainscheduler.Schedule, bool, error) {
	items, _, err := s.registeredSchedulesSchedulerPageStub.ListSchedulesPage(ctx, afterID, limit, keep)
	return items, true, err
}
