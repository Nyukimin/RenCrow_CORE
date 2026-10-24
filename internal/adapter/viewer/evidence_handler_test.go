package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type evidenceStoreStub struct {
	items []domainexecution.ExecutionReport
	err   error
	limit int
}

type blockingEvidenceStore struct {
	*evidenceStoreStub
	getEntered chan struct{}
	getRelease chan struct{}
}

func (s *blockingEvidenceStore) GetByTaskID(ctx context.Context, taskID modulecore.TaskID) (domainexecution.ExecutionReport, error) {
	close(s.getEntered)
	select {
	case <-s.getRelease:
	case <-ctx.Done():
		return domainexecution.ExecutionReport{}, ctx.Err()
	}
	return s.evidenceStoreStub.GetByTaskID(ctx, taskID)
}

func (s *evidenceStoreStub) ListRecent(_ context.Context, limit int) ([]domainexecution.ExecutionReport, error) {
	s.limit = limit
	if s.err != nil {
		return nil, s.err
	}
	return s.items, nil
}

func (s *evidenceStoreStub) GetByTaskID(_ context.Context, taskID modulecore.TaskID) (domainexecution.ExecutionReport, error) {
	for _, item := range s.items {
		if item.TaskID == taskID {
			return item, nil
		}
	}
	return domainexecution.ExecutionReport{}, context.Canceled
}

func (s *evidenceStoreStub) ListRecentUnique(_ context.Context, limit int) ([]domainexecution.ExecutionReport, error) {
	// For test purposes, just delegate to ListRecent (no duplicates in test data)
	return s.ListRecent(context.Background(), limit)
}

func (s *evidenceStoreStub) Summary(_ context.Context) (map[string]map[string]int, error) {
	out := map[string]map[string]int{
		"status": {
			"passed": 0,
			"failed": 0,
		},
		"error_kind": {
			"apply":  0,
			"verify": 0,
			"repair": 0,
			"none":   0,
		},
	}
	for _, it := range s.items {
		out["status"][it.Status]++
		k := it.ErrorKind
		if k == "" {
			k = "none"
		}
		out["error_kind"][k]++
	}
	return out, nil
}

func (s *evidenceStoreStub) SummaryUnique(ctx context.Context) (map[string]map[string]int, error) {
	// For test purposes, just delegate to Summary (no duplicates in test data)
	return s.Summary(ctx)
}

func TestHandleEvidenceRecent_Success(t *testing.T) {
	taskID := modulecore.NewTaskID()
	publicSessionID := modulecore.SessionID("public-session")
	store := &evidenceStoreStub{items: []domainexecution.ExecutionReport{{
		TaskID:     taskID,
		Goal:       "TTS実装して",
		Status:     "passed",
		CreatedAt:  time.Now().UTC(),
		FinishedAt: time.Now().UTC(),
	}}}
	tasks := &evidenceTaskReaderStub{tasks: map[modulecore.TaskID]domaintask.Task{
		taskID: {TaskID: taskID, OriginSessionID: publicSessionID},
	}}
	visibility := PublicTaskVisibility(func(_ context.Context, task domaintask.Task) (bool, error) {
		return task.OriginSessionID == publicSessionID, nil
	})
	h := HandleEvidenceRecentWithPublicVisibility(store, tasks, visibility)

	req := httptest.NewRequest(http.MethodGet, "/viewer/evidence/recent?limit=5", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if store.limit != 5 {
		t.Fatalf("expected limit=5, got %d", store.limit)
	}
	var out struct {
		Items []domainexecution.ExecutionReport `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(out.Items) != 1 || out.Items[0].TaskID != taskID {
		t.Fatalf("unexpected items: %+v", out.Items)
	}
}

func TestHandleEvidenceRecent_InvalidLimit(t *testing.T) {
	store := &evidenceStoreStub{}
	h := HandleEvidenceRecent(store)

	req := httptest.NewRequest(http.MethodGet, "/viewer/evidence/recent?limit=bad", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandleEvidenceDetail_Success(t *testing.T) {
	taskID := modulecore.NewTaskID()
	publicSessionID := modulecore.SessionID("public-session")
	store := &evidenceStoreStub{items: []domainexecution.ExecutionReport{{
		TaskID:     taskID,
		Goal:       "TTS実装して",
		Status:     "passed",
		CreatedAt:  time.Now().UTC(),
		FinishedAt: time.Now().UTC(),
	}}}
	tasks := &evidenceTaskReaderStub{tasks: map[modulecore.TaskID]domaintask.Task{
		taskID: {TaskID: taskID, OriginSessionID: publicSessionID},
	}}
	visibility := PublicTaskVisibility(func(_ context.Context, task domaintask.Task) (bool, error) {
		return task.OriginSessionID == publicSessionID, nil
	})
	h := HandleEvidenceDetailWithPublicVisibility(store, tasks, visibility)

	req := httptest.NewRequest(http.MethodGet, "/viewer/evidence/detail?task_id="+string(taskID), nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestHandleEvidenceDetailRejectsMissingInvalidAndLegacyIdentity(t *testing.T) {
	h := HandleEvidenceDetail(&evidenceStoreStub{})
	legacyKey := "job" + "_" + "id"
	for _, target := range []string{
		"/viewer/evidence/detail",
		"/viewer/evidence/detail?task_id=bad",
		"/viewer/evidence/detail?" + legacyKey + "=legacy",
	} {
		recorder := httptest.NewRecorder()
		h(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid identity accepted: target=%s status=%d", target, recorder.Code)
		}
	}
}

func TestHandleEvidenceRecentDoesNotExposeOwnerBoundReportWithoutVisibility(t *testing.T) {
	taskID := modulecore.NewTaskID()
	store := &evidenceStoreStub{items: []domainexecution.ExecutionReport{{
		TaskID: taskID,
		Goal:   "private owner-bound report goal",
		Status: "passed",
	}}}
	recorder := httptest.NewRecorder()
	HandleEvidenceRecent(store).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/viewer/evidence/recent", nil))

	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "private owner-bound report goal") {
		t.Fatalf("owner-bound report was exposed: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestHandleEvidenceDetailHidesOwnerBoundReportWithoutVisibility(t *testing.T) {
	taskID := modulecore.NewTaskID()
	store := &evidenceStoreStub{items: []domainexecution.ExecutionReport{{
		TaskID: taskID,
		Goal:   "private owner-bound report goal",
		Status: "passed",
	}}}
	recorder := httptest.NewRecorder()
	HandleEvidenceDetail(store).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/viewer/evidence/detail?task_id="+string(taskID), nil))

	if recorder.Code != http.StatusNotFound || strings.Contains(recorder.Body.String(), "private owner-bound report goal") {
		t.Fatalf("owner-bound report detail was exposed: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestHandleEvidenceSummaryFailsClosedWithoutScopedAggregate(t *testing.T) {
	store := &evidenceStoreStub{items: []domainexecution.ExecutionReport{{
		TaskID: modulecore.NewTaskID(),
		Goal:   "private owner-bound report goal",
		Status: "failed",
	}}}
	recorder := httptest.NewRecorder()
	HandleEvidenceSummary(store).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/viewer/evidence/summary", nil))

	if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), `"failed":1`) || strings.Contains(recorder.Body.String(), "private owner-bound report goal") {
		t.Fatalf("unscoped aggregate was exposed: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestHandleEvidenceRecentFiltersReportsByCanonicalTaskVisibility(t *testing.T) {
	publicID := modulecore.NewTaskID()
	privateID := modulecore.NewTaskID()
	unknownID := modulecore.NewTaskID()
	blankID := modulecore.NewTaskID()
	missingID := modulecore.NewTaskID()
	publicSessionID := modulecore.SessionID("public-session")
	privateSessionID := modulecore.SessionID("private-session")
	unknownSessionID := modulecore.SessionID("unknown-session")
	store := &evidenceStoreStub{items: []domainexecution.ExecutionReport{
		{TaskID: publicID, Goal: "public goal", Status: "passed"},
		{TaskID: privateID, Goal: "private owner-bound goal", Status: "passed"},
		{TaskID: unknownID, Goal: "unknown session goal", Status: "passed"},
		{TaskID: blankID, Goal: "blank session goal", Status: "passed"},
		{TaskID: missingID, Goal: "missing task goal", Status: "passed"},
	}}
	tasks := &evidenceTaskReaderStub{tasks: map[modulecore.TaskID]domaintask.Task{
		publicID:  {TaskID: publicID, OriginSessionID: publicSessionID},
		privateID: {TaskID: privateID, OriginSessionID: privateSessionID},
		unknownID: {TaskID: unknownID, OriginSessionID: unknownSessionID},
		blankID:   {TaskID: blankID},
	}}
	visibility := PublicTaskVisibility(func(_ context.Context, task domaintask.Task) (bool, error) {
		switch task.OriginSessionID {
		case publicSessionID:
			return true, nil
		case unknownSessionID:
			return false, errors.New("canonical Session is unknown")
		default:
			return false, nil
		}
	})
	recorder := httptest.NewRecorder()
	HandleEvidenceRecentWithPublicVisibility(store, tasks, visibility).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/viewer/evidence/recent?limit=5", nil),
	)

	if recorder.Code != http.StatusOK {
		t.Fatalf("recent status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var out struct {
		Items []domainexecution.ExecutionReport `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid recent response: %v", err)
	}
	if len(out.Items) != 1 || out.Items[0].TaskID != publicID {
		t.Fatalf("visible reports=%+v, want only public task %s", out.Items, publicID)
	}
	for _, private := range []string{"private owner-bound goal", "unknown session goal", "blank session goal", "missing task goal"} {
		if strings.Contains(recorder.Body.String(), private) {
			t.Fatalf("recent response exposed %q: %s", private, recorder.Body.String())
		}
	}
	if store.limit != 5 {
		t.Fatalf("store limit=%d, want 5", store.limit)
	}
}

func TestHandleEvidenceRecentRevalidatesEarlierTaskAfterLaterLookupBlocks(t *testing.T) {
	firstTaskID := modulecore.NewTaskID()
	secondTaskID := modulecore.NewTaskID()
	firstSessionID := modulecore.NewSessionID()
	secondSessionID := modulecore.NewSessionID()
	firstTask := domaintask.Task{TaskID: firstTaskID, OriginSessionID: firstSessionID}
	secondTask := domaintask.Task{TaskID: secondTaskID, OriginSessionID: secondSessionID}
	entered := make(chan struct{})
	release := make(chan struct{})
	var blockSecondLookup sync.Once
	tasks := &evidenceTaskReaderStub{
		tasks: map[modulecore.TaskID]domaintask.Task{firstTaskID: firstTask, secondTaskID: secondTask},
		onGet: func(taskID modulecore.TaskID) {
			if taskID == secondTaskID {
				blockSecondLookup.Do(func() {
					close(entered)
					<-release
				})
			}
		},
	}
	var firstRevoked atomic.Bool
	store := &evidenceStoreStub{items: []domainexecution.ExecutionReport{
		{TaskID: firstTaskID, Goal: "FIRST_TASK_SENTINEL", Status: "passed"},
		{TaskID: secondTaskID, Goal: "SECOND_TASK", Status: "passed"},
	}}
	visibility := PublicTaskVisibility(func(_ context.Context, task domaintask.Task) (bool, error) {
		return task.TaskID != firstTaskID || !firstRevoked.Load(), nil
	})
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		HandleEvidenceRecentWithPublicVisibility(store, tasks, visibility).ServeHTTP(
			recorder, httptest.NewRequest(http.MethodGet, "/viewer/evidence/recent", nil),
		)
		close(done)
	}()
	waitForViewerSignal(t, entered, "second Task lookup")
	firstRevoked.Store(true)
	close(release)
	waitForViewerSignal(t, done, "recent evidence response")

	if recorder.Code != http.StatusOK {
		t.Fatalf("recent status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var out struct {
		Items []domainexecution.ExecutionReport `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode recent response: %v", err)
	}
	if len(out.Items) != 1 || out.Items[0].TaskID != secondTaskID || strings.Contains(recorder.Body.String(), "FIRST_TASK_SENTINEL") {
		t.Fatalf("recent response retained revoked earlier Task: %+v body=%s", out.Items, recorder.Body.String())
	}
}

func TestHandleEvidenceDetailRevalidatesOriginAfterEvidenceReadBlocks(t *testing.T) {
	taskID := modulecore.NewTaskID()
	sessionID := modulecore.NewSessionID()
	publicTask := domaintask.Task{
		TaskID: taskID, OriginSessionID: sessionID,
		OriginPublication: &domaintask.OriginPublicationProvenance{PublicationIncarnation: "public-incarnation", Audience: "public"},
	}
	privateRecreatedTask := publicTask
	privateRecreatedTask.OriginPublication = &domaintask.OriginPublicationProvenance{
		PublicationIncarnation: "private-recreated-incarnation", Audience: "private", OwnerID: "ren",
	}
	tasks := &evidenceTaskReaderStub{tasks: map[modulecore.TaskID]domaintask.Task{taskID: publicTask}}
	store := &blockingEvidenceStore{
		evidenceStoreStub: &evidenceStoreStub{items: []domainexecution.ExecutionReport{{TaskID: taskID, Goal: "STALE_EVIDENCE_SENTINEL", Status: "passed"}}},
		getEntered:        make(chan struct{}),
		getRelease:        make(chan struct{}),
	}
	visibility := PublicTaskVisibility(func(_ context.Context, task domaintask.Task) (bool, error) {
		return task.OriginPublication != nil && task.OriginPublication.Audience == "public", nil
	})
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		HandleEvidenceDetailWithPublicVisibility(store, tasks, visibility).ServeHTTP(
			recorder,
			httptest.NewRequest(http.MethodGet, "/viewer/evidence/detail?task_id="+string(taskID), nil),
		)
		close(done)
	}()
	waitForViewerSignal(t, store.getEntered, "evidence detail read")
	tasks.tasks[taskID] = privateRecreatedTask
	close(store.getRelease)
	waitForViewerSignal(t, done, "evidence detail response")

	if recorder.Code != http.StatusNotFound || strings.Contains(recorder.Body.String(), "STALE_EVIDENCE_SENTINEL") {
		t.Fatalf("detail exposed evidence from a revoked Task origin: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

type evidenceTaskReaderStub struct {
	tasks map[modulecore.TaskID]domaintask.Task
	onGet func(modulecore.TaskID)
}

func (s *evidenceTaskReaderStub) GetTask(_ context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	if s.onGet != nil {
		s.onGet(taskID)
	}
	task, ok := s.tasks[taskID]
	if !ok {
		return domaintask.Task{}, errors.New("task not found")
	}
	return task, nil
}

func waitForViewerSignal(t *testing.T, signal <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", operation)
	}
}
