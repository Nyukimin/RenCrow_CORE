package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type taskActivitiesReaderStub struct {
	current    map[string]domaintask.CurrentActivitySummary
	page       domaintask.TaskActivityInventoryPage
	pages      []domaintask.TaskActivityInventoryPage
	listCalls  int
	lastFilter domaintask.TaskActivityInventoryFilter
}

func (s *taskActivitiesReaderStub) SnapshotCurrentActivitiesAuthorized(context.Context, []string, domaintask.TaskReadAuthorizationCheck) (map[string]domaintask.CurrentActivitySummary, error) {
	return s.current, nil
}

func (s *taskActivitiesReaderStub) ListTaskActivityInventory(_ context.Context, filter domaintask.TaskActivityInventoryFilter, authorize domaintask.TaskReadAuthorizationCheck) (domaintask.TaskActivityInventoryPage, error) {
	s.listCalls++
	s.lastFilter = filter
	page := s.page
	if len(s.pages) > 0 {
		index := s.listCalls - 1
		if index >= len(s.pages) {
			index = len(s.pages) - 1
		}
		page = s.pages[index]
	}
	pageItems := page.Items
	page.Items = make([]domaintask.TaskActivityInventoryRow, 0, len(pageItems))
	for _, item := range pageItems {
		if filter.BeforeTaskID != "" && item.TaskID >= filter.BeforeTaskID {
			continue
		}
		input := domaintask.TaskReadAuthorizationInput{Resource: domaintask.TaskReadResourceTask, TaskID: item.TaskID}
		if err := authorize(context.Background(), input); err != nil {
			return domaintask.TaskActivityInventoryPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if len(s.pages) == 0 && filter.BeforeTaskID != "" {
		page.NextCursor = ""
	}
	return page, nil
}

func TestTaskActivitiesUnfinishedPaginationKeepsCurrentOverlayBehindSourceFrontier(t *testing.T) {
	queued := domaintask.TaskActivityInventoryRow{TaskID: taskActivityTestID("002"), Title: "queued", Status: domaintask.StatusQueued}
	currentRow := domaintask.TaskActivityInventoryRow{TaskID: taskActivityTestID("001"), Title: "current", Status: domaintask.StatusRunning}
	current := map[modulecore.TaskID]taskActivityCurrentListRow{currentRow.TaskID: {Task: currentRow}}
	reader := &taskActivitiesReaderStub{pages: []domaintask.TaskActivityInventoryPage{
		{Items: []domaintask.TaskActivityInventoryRow{}, NextCursor: taskActivityTestID("004")},
		{Items: []domaintask.TaskActivityInventoryRow{queued}},
	}}
	handler := &taskActivitiesHandler{reader: reader}
	query, err := parseTaskActivitiesQuery(&url.URL{RawQuery: "view=unfinished&limit=1"})
	if err != nil {
		t.Fatal(err)
	}
	items, cursor, err := handler.listTaskActivities(context.Background(), query, current)
	if err != nil || len(items) != 1 || items[0].TaskID != queued.TaskID || cursor != string(queued.TaskID) {
		t.Fatalf("first page = %v cursor=%q err=%v", items, cursor, err)
	}
	query.BeforeTaskID = modulecore.TaskID(cursor)
	items, cursor, err = handler.listTaskActivities(context.Background(), query, current)
	if err != nil || len(items) != 1 || items[0].TaskID != currentRow.TaskID || cursor != "" {
		t.Fatalf("current overlay continuation = %v cursor=%q err=%v", items, cursor, err)
	}
}

func TestTaskActivitiesBoundedScanDoesNotEmitCurrentRowsBehindFrontier(t *testing.T) {
	currentRow := domaintask.TaskActivityInventoryRow{TaskID: taskActivityTestID("001"), Title: "old current", Status: domaintask.StatusRunning}
	frontierRow := domaintask.TaskActivityInventoryRow{TaskID: taskActivityTestID("006"), Title: "frontier task", Status: domaintask.StatusQueued}
	current := map[modulecore.TaskID]taskActivityCurrentListRow{currentRow.TaskID: {Task: currentRow}}
	pages := make([]domaintask.TaskActivityInventoryPage, taskActivitiesPageScanLimit)
	for index := range pages {
		pages[index] = domaintask.TaskActivityInventoryPage{Items: []domaintask.TaskActivityInventoryRow{}, NextCursor: taskActivityTestID([]string{"010", "009", "008", "007", "006"}[index])}
	}
	pages[len(pages)-1].Items = []domaintask.TaskActivityInventoryRow{frontierRow}
	reader := &taskActivitiesReaderStub{pages: pages}
	handler := &taskActivitiesHandler{reader: reader}
	query, err := parseTaskActivitiesQuery(&url.URL{RawQuery: "view=unfinished&limit=2"})
	if err != nil {
		t.Fatal(err)
	}
	items, cursor, err := handler.listTaskActivities(context.Background(), query, current)
	if err != nil || len(items) != 1 || items[0].TaskID != frontierRow.TaskID || cursor != string(taskActivityTestID("006")) {
		t.Fatalf("bounded scan page = %v cursor=%q err=%v", items, cursor, err)
	}
	query.BeforeTaskID = modulecore.TaskID(cursor)
	reader.pages = []domaintask.TaskActivityInventoryPage{{Items: []domaintask.TaskActivityInventoryRow{}}}
	reader.listCalls = 0
	items, cursor, err = handler.listTaskActivities(context.Background(), query, current)
	if err != nil || len(items) != 1 || items[0].TaskID != currentRow.TaskID || cursor != "" {
		t.Fatalf("bounded scan continuation = %v cursor=%q err=%v", items, cursor, err)
	}
}

func TestTaskActivitiesUnfinishedViewExcludesSucceededAtInventorySource(t *testing.T) {
	reader := &taskActivitiesReaderStub{current: taskActivityCurrentFixture(), page: domaintask.TaskActivityInventoryPage{Items: []domaintask.TaskActivityInventoryRow{}}}
	handler := NewTaskActivitiesHandler(reader, nil, "owner-user", []byte("owner-token"))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, taskActivitiesRequest("?view=unfinished&limit=25", "127.0.0.1:41000"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if reader.lastFilter.ExcludeStatus != domaintask.StatusSucceeded || reader.lastFilter.Limit != 200 {
		t.Fatalf("unfinished inventory filter = %+v", reader.lastFilter)
	}
}

type taskActivitiesGoalStub struct {
	item taskcenter.Item
	err  error
}

type taskActivitiesPublicationStub struct {
	binding domainsession.CanonicalSessionPublicationBinding
	err     error
}

func (s taskActivitiesPublicationStub) CurrentPublicationBinding(context.Context, string) (domainsession.CanonicalSessionPublicationBinding, error) {
	return s.binding, s.err
}

func (s taskActivitiesGoalStub) Get(context.Context, string) (taskcenter.Item, error) {
	return s.item, s.err
}

func taskActivitiesRequest(rawQuery, remote string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:18790/viewer/board/task-activities"+rawQuery, nil)
	req.RemoteAddr = remote
	req.Header.Set("Authorization", "Bearer owner-token")
	req.Header.Set("X-RenCrow-Client", "RenCrow_BOARD")
	req.Header.Set("X-RenCrow-Interaction-Profile", "board-studio-readonly")
	return req
}

func taskActivityTestID(suffix string) modulecore.TaskID {
	return modulecore.TaskID("tsk_018f0000-0000-7000-8000-000000000" + suffix)
}

func taskActivityCurrentFixture() map[string]domaintask.CurrentActivitySummary {
	activity := &domaintask.TaskActivityInventoryActivity{Kind: domaintask.ActivityKindGame, Label: "NetHack", Phase: "playing", Revision: 4}
	row := domaintask.TaskActivityInventoryRow{TaskID: taskActivityTestID("003"), Title: "game task", Status: domaintask.StatusRunning, CreatedAt: time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC), Activity: activity, Scope: "shared"}
	current := make(map[string]domaintask.CurrentActivitySummary, len(taskActivityActorIDs))
	for _, actor := range taskActivityActorIDs {
		current[actor] = domaintask.CurrentActivitySummary{TaskActivityInventoryRow: row, Revision: 23}
	}
	return current
}

func TestTaskActivitiesHandlerEncryptsFilteredTaskIDCursorAndReturnsOwnerProjection(t *testing.T) {
	privateCursorID := taskActivityTestID("001")
	item := domaintask.TaskActivityInventoryRow{TaskID: taskActivityTestID("002"), Title: "safe title", Status: domaintask.StatusRunning, Assignee: "mio", CreatedAt: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC), Scope: "personal"}
	reader := &taskActivitiesReaderStub{current: taskActivityCurrentFixture(), page: domaintask.TaskActivityInventoryPage{Items: []domaintask.TaskActivityInventoryRow{item}, NextCursor: privateCursorID}}
	handler := NewTaskActivitiesHandler(reader, nil, "owner-user", []byte("owner-token"))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, taskActivitiesRequest("?view=all&limit=2", "127.0.0.1:41000"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control=%q", recorder.Header().Get("Cache-Control"))
	}
	var got taskActivitiesResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got.Current) != 8 || len(got.Items) != 2 || got.Items[0].TaskID != taskActivityTestID("003") || got.Items[1].TaskID != item.TaskID || got.NextCursor == "" {
		t.Fatalf("projection=%+v", got)
	}
	if got.Current[0].Revision != 23 || got.Current[0].CurrentActivity.Revision != 4 || got.Current[0].CurrentActivity.Label != "NetHackをプレイ中" {
		t.Fatalf("binding and activity revisions were conflated: %+v", got.Current[0])
	}
	if strings.Contains(recorder.Body.String(), string(privateCursorID)) {
		t.Fatalf("raw filtered TaskID leaked in cursor: %s", recorder.Body.String())
	}
	query, err := parseTaskActivitiesQuery(&url.URL{RawQuery: "view=all&limit=2"})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeTaskActivitiesCursor(got.NextCursor, []byte("owner-token"), query, "owner-user")
	if err != nil || decoded != string(item.TaskID) {
		t.Fatalf("cursor decode=%q err=%v", decoded, err)
	}
	unfinishedQuery, err := parseTaskActivitiesQuery(&url.URL{RawQuery: "view=unfinished&limit=2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeTaskActivitiesCursor(got.NextCursor, []byte("owner-token"), unfinishedQuery, "owner-user"); err == nil {
		t.Fatal("all-view cursor was accepted by unfinished view")
	}
}

func TestTaskActivitiesHandlerUsesCurrentSnapshotWhenInventoryChangesMidRead(t *testing.T) {
	current := taskActivityCurrentFixture()
	changed := current["mio"].TaskActivityInventoryRow
	changed.Status = domaintask.StatusSucceeded
	reader := &taskActivitiesReaderStub{current: current, page: domaintask.TaskActivityInventoryPage{Items: []domaintask.TaskActivityInventoryRow{changed}}}
	handler := NewTaskActivitiesHandler(reader, nil, "owner-user", []byte("owner-token"))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, taskActivitiesRequest("?view=all&status=running", "127.0.0.1:41000"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var got taskActivitiesResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Current) != 8 || len(got.Items) != 1 || got.Items[0].Status != domaintask.StatusRunning {
		t.Fatalf("current snapshot and inventory disagree in one response: current=%+v items=%+v", got.Current, got.Items)
	}
}

func TestTaskActivitiesHandlerRejectsTamperedAndCrossUserCursorBeforeReading(t *testing.T) {
	reader := &taskActivitiesReaderStub{current: taskActivityCurrentFixture(), page: domaintask.TaskActivityInventoryPage{Items: []domaintask.TaskActivityInventoryRow{}}}
	query := taskActivitiesQuery{View: "all", Limit: 50}
	cursor, err := encodeTaskActivitiesCursor(string(taskActivityTestID("001")), []byte("owner-token"), query, "owner-user")
	if err != nil {
		t.Fatal(err)
	}
	tampered := cursor
	if cursor[0] == 'A' {
		tampered = "B" + cursor[1:]
	} else {
		tampered = "A" + cursor[1:]
	}
	for _, test := range []struct{ name, user, cursor string }{{"tampered", "owner-user", tampered}, {"other user", "other-user", cursor}} {
		t.Run(test.name, func(t *testing.T) {
			handler := NewTaskActivitiesHandler(reader, nil, test.user, []byte("owner-token"))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, taskActivitiesRequest("?view=all&cursor="+test.cursor, "127.0.0.1:41000"))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if reader.listCalls != 0 {
				t.Fatalf("inventory reads=%d before cursor validation", reader.listCalls)
			}
		})
	}
}

func TestTaskActivitiesAuthorizationChecksExactGoalTaskBinding(t *testing.T) {
	handler := &taskActivitiesHandler{userID: "owner-user", goals: taskActivitiesGoalStub{item: taskcenter.Item{GoalID: "gol_018f0000-0000-7000-8000-000000000001", WorkstreamID: "ws_018f0000-0000-7000-8000-000000000001", TaskID: string(taskActivityTestID("001")), DataScope: "user"}}}
	input := domaintask.TaskReadAuthorizationInput{Resource: domaintask.TaskReadResourceTask, TaskID: taskActivityTestID("001"), WorkstreamID: "ws_018f0000-0000-7000-8000-000000000001", GoalID: "gol_018f0000-0000-7000-8000-000000000001", Scope: domaintask.TaskAccessScopeUser, AuthenticatedUserID: "owner-user"}
	if err := handler.authorizeTaskRead(context.Background(), input); err != nil {
		t.Fatalf("authorized Goal-bound Task rejected: %v", err)
	}
	input.TaskID = taskActivityTestID("002")
	if err := handler.authorizeTaskRead(context.Background(), input); err == nil {
		t.Fatal("Goal TaskID mismatch was authorized")
	}
}

func TestTaskActivitiesAuthorizationChecksCurrentPersonalPublicationOwner(t *testing.T) {
	for _, test := range []struct {
		name          string
		proofOwner    string
		currentOwner  string
		wantAllow     bool
		changeCurrent bool
	}{
		{name: "owner sees own personal task", proofOwner: "owner-user", currentOwner: "owner-user", wantAllow: true},
		{name: "foreign personal owner denied", proofOwner: "other-user", currentOwner: "other-user"},
		{name: "revoked publication incarnation denied", proofOwner: "owner-user", currentOwner: "owner-user", changeCurrent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			sessionID := modulecore.NewSessionID()
			address, err := domainconversation.NewChannelAddress("viewer", "cmd:"+test.proofOwner)
			if err != nil {
				t.Fatal(err)
			}
			createdAt := time.Now().UTC()
			proofSession, err := domainsession.NewCanonicalSessionWithOwner(sessionID, test.proofOwner, createdAt.Format("2006-01-02"), address, createdAt)
			if err != nil {
				t.Fatal(err)
			}
			proofBinding, err := domainconversation.NewSessionBinding(sessionID, address, domainconversation.SessionAudiencePrivate, test.proofOwner, 1, proofSession.PublicationIncarnation())
			if err != nil {
				t.Fatal(err)
			}
			proof, err := domaintask.NewOriginPublicationProvenance(proofBinding)
			if err != nil {
				t.Fatal(err)
			}
			currentIncarnation := proofSession.PublicationIncarnation()
			if test.changeCurrent {
				currentIncarnation, err = domainsession.NewPublicationIncarnation()
				if err != nil {
					t.Fatal(err)
				}
			}
			currentSession, err := domainsession.ReconstructCanonicalSessionWithOwnerAndPublicationIncarnation(sessionID, test.currentOwner, createdAt.Format("2006-01-02"), address, nil, nil, createdAt, createdAt, currentIncarnation)
			if err != nil {
				t.Fatal(err)
			}
			currentBinding, err := domainsession.NewCanonicalSessionPublicationBinding(currentSession)
			if err != nil {
				t.Fatal(err)
			}
			handler := &taskActivitiesHandler{
				userID:       "owner-user",
				publications: taskActivitiesPublicationStub{binding: currentBinding},
			}
			input := domaintask.TaskReadAuthorizationInput{
				Resource: domaintask.TaskReadResourceTask, TaskID: taskActivityTestID("001"),
				OriginSessionID: sessionID, OriginPublication: &proof,
			}
			err = handler.authorizeTaskRead(context.Background(), input)
			if test.wantAllow && err != nil {
				t.Fatalf("authorized current personal publication denied: %v", err)
			}
			if !test.wantAllow && err == nil {
				t.Fatal("foreign or revoked personal publication authorized")
			}
		})
	}
}

func TestTaskActivitiesAuthorizationFailsClosedForPublicationWithoutReader(t *testing.T) {
	handler := &taskActivitiesHandler{userID: "owner-user"}
	input := domaintask.TaskReadAuthorizationInput{
		TaskID: taskActivityTestID("001"), OriginSessionID: modulecore.NewSessionID(),
		OriginPublication: &domaintask.OriginPublicationProvenance{Audience: domainconversation.SessionAudiencePrivate, OwnerID: "owner-user"},
	}
	if err := handler.authorizeTaskRead(context.Background(), input); err == nil {
		t.Fatal("publication-bound Task authorized without canonical Session reader")
	}
}

func TestParseTaskActivitiesQueryRejectsMalformedFilters(t *testing.T) {
	for _, raw := range []string{"view=other", "assignee=somebody", "status=unknown", "category=unknown", "limit=201", "view=current&cursor=x", "view=all&view=history"} {
		if _, err := parseTaskActivitiesQuery(&url.URL{RawQuery: raw}); err == nil {
			t.Errorf("query %q accepted", raw)
		}
	}
}
