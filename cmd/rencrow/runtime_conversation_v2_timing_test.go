package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestResponseFirstTimingOptInRequiresExactOne(t *testing.T) {
	for _, value := range []string{"", "true", "01", " 1 "} {
		t.Run("off_"+value, func(t *testing.T) {
			t.Setenv(responseFirstTimingEnvironment, value)
			processing, err := newResponseFirstV2ProcessingFromEnvironment()
			if err != nil {
				t.Fatalf("construct disabled v2 processing: %v", err)
			}
			if processing == nil || processing.lifecycle == nil {
				t.Fatal("process lifecycle was not configured")
			}
			if processing.timing != nil {
				t.Fatalf("timing batch allocated for opt-in value %q", value)
			}
		})
	}
	t.Run("exact_1", func(t *testing.T) {
		t.Setenv(responseFirstTimingEnvironment, "1")
		processing, err := newResponseFirstV2ProcessingFromEnvironment()
		if err != nil {
			t.Fatalf("construct enabled v2 processing: %v", err)
		}
		if processing.timing == nil {
			t.Fatal("exact opt-in did not allocate the process batch")
		}
		if got := processing.timing.ReservedBytes(); got == 0 || got > turntiming.DefaultMemoryLimitBytes {
			t.Fatalf("timing batch reserved bytes = %d, outside fixed memory budget", got)
		}
	})
}

func TestResponseFirstTimingSnapshotRequiresFrozenBatch(t *testing.T) {
	batch, err := turntiming.NewBatchRecorderWithConfig(turntiming.RecorderConfig{BatchCapacity: 1, StampsPerTurn: 8, MemoryLimitBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &responseFirstConversationRuntime{processing: &responseFirstV2Processing{timing: batch}}
	if _, err := runtime.timingSnapshot(); err != turntiming.ErrNotFrozen {
		t.Fatalf("live timing snapshot error = %v, want %v", err, turntiming.ErrNotFrozen)
	}
	turn, err := batch.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}
	turn.FinishForeground()
	if err := runtime.processing.freezeTimingBatch(false); err != nil {
		t.Fatalf("freeze completed batch: %v", err)
	}
	snapshot, err := runtime.timingSnapshot()
	if err != nil {
		t.Fatalf("read frozen timing snapshot: %v", err)
	}
	if snapshot.IsIncomplete() || snapshot.TurnCount() != 1 {
		t.Fatalf("frozen snapshot incomplete=%t turn_count=%d", snapshot.IsIncomplete(), snapshot.TurnCount())
	}
}

func TestResponseFirstReportTimingBindsReceiptOnceAndReportsOverflowAndMissingEnd(t *testing.T) {
	batch, err := turntiming.NewBatchRecorderWithConfig(turntiming.RecorderConfig{
		BatchCapacity: 1, StampsPerTurn: 8, MemoryLimitBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &responseFirstConversationRuntime{processing: &responseFirstV2Processing{timing: batch}}
	accepted := conversationruntime.AcceptedTurn{
		TurnID:     modulecore.NewTurnID(),
		InputEvent: modulecore.EventEnvelope{TraceID: modulecore.NewTraceID()},
	}
	firstContext := runtime.bindAcceptedReportTiming(context.Background(), accepted)
	first, ok := turntiming.BatchTurnFromContext(firstContext)
	if !ok {
		t.Fatal("first accepted report did not receive a timing context")
	}
	retriedContext := runtime.bindAcceptedReportTiming(context.Background(), accepted)
	retried, ok := turntiming.BatchTurnFromContext(retriedContext)
	if !ok {
		t.Fatal("duplicate accepted report receipt did not reuse a timing context")
	}
	firstIndex, firstValid := first.Index()
	retryIndex, retryValid := retried.Index()
	if !firstValid || !retryValid || firstIndex != retryIndex {
		t.Fatalf("timing indices across receipt retry=(%d,%t),(%d,%t), want the same reserved slot", firstIndex, firstValid, retryIndex, retryValid)
	}
	if _, _, err := batch.FindOrBeginTurn(string(accepted.TurnID), string(modulecore.NewTraceID())); !errors.Is(err, turntiming.ErrIdentityConflict) {
		t.Fatalf("conflicting trace for accepted report turn error=%v, want identity conflict", err)
	}
	other := conversationruntime.AcceptedTurn{TurnID: modulecore.NewTurnID(), InputEvent: modulecore.EventEnvelope{TraceID: modulecore.NewTraceID()}}
	if _, ok := turntiming.BatchTurnFromContext(runtime.bindAcceptedReportTiming(context.Background(), other)); ok {
		t.Fatal("turn-capacity overflow unexpectedly bound a timing context")
	}
	if got := responseFirstTimingBindReason(turntiming.ErrTurnCapacity); got != "turn_capacity" {
		t.Fatalf("turn-capacity diagnostic reason=%q, want turn_capacity", got)
	}
	if !first.FinishForeground() {
		t.Fatal("accepted report timing turn could not finish")
	}
	if err := batch.Freeze(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := batch.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.TurnCount() != 1 {
		t.Fatalf("report timing turn count=%d, want one after receipt retry and overflow", snapshot.TurnCount())
	}

	missingBatch, err := turntiming.NewBatchRecorderWithConfig(turntiming.RecorderConfig{
		BatchCapacity: 1, StampsPerTurn: 4, MemoryLimitBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	missingTurn, created, err := missingBatch.FindOrBeginTurn(string(modulecore.NewTurnID()), string(modulecore.NewTraceID()))
	if err != nil || !created {
		t.Fatalf("begin missing-span test turn=(created=%t, err=%v)", created, err)
	}
	if _, ok := missingTurn.Begin(turntiming.PointV2AsyncStart, turntiming.ComponentV2ConversationRuntime, 1, 0, turntiming.Span{}); !ok {
		t.Fatal("could not begin span for missing-end diagnostic")
	}
	if !missingTurn.FinishForeground() {
		t.Fatal("missing-span test turn could not finish foreground")
	}
	if err := missingBatch.Freeze(); err != nil {
		t.Fatal(err)
	}
	missingSnapshot, err := missingBatch.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	complete, dropped, missing := responseFirstTimingCaptureQuality(missingSnapshot)
	if complete || dropped != 0 || missing != 1 {
		t.Fatalf("missing-end quality=(complete=%t dropped=%d missing=%d), want (false,0,1)", complete, dropped, missing)
	}
	export, err := encodeResponseFirstTimingExport(missingSnapshot, responseFirstTimingExportMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	var diagnostic struct {
		Complete        bool   `json:"complete"`
		DroppedStamps   uint64 `json:"dropped_stamps"`
		MissingSpanEnds uint64 `json:"missing_span_ends"`
	}
	if err := json.Unmarshal(export, &diagnostic); err != nil {
		t.Fatal(err)
	}
	if diagnostic.Complete || diagnostic.DroppedStamps != 0 || diagnostic.MissingSpanEnds != 1 {
		t.Fatalf("exported missing-span diagnostic=%+v, want incomplete with one missing end", diagnostic)
	}
}

func TestResponseFirstTimingControlRoutesAreLoopbackOnlyAndRAMOnly(t *testing.T) {
	t.Setenv(responseFirstTimingEnvironment, "1")
	batch, err := turntiming.NewBatchRecorderWithConfig(turntiming.RecorderConfig{BatchCapacity: 2, StampsPerTurn: 8, MemoryLimitBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := batch.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}
	turn.Mark(turntiming.PointV2Ingress, turntiming.ComponentV2ViewerHandler, 1, 0)
	if !turn.FinishForeground() {
		t.Fatal("finish timing turn")
	}
	runtime := &responseFirstConversationRuntime{processing: &responseFirstV2Processing{
		lifecycle: viewer.NewV2ProcessingLifecycle(context.Background()), timing: batch,
	}}
	mux := http.NewServeMux()
	registerResponseFirstTimingRoutes(mux, runtime)

	for _, path := range []string{
		responseFirstTimingEndpoint,
		responseFirstTimingEndpoint + "/start",
		responseFirstTimingEndpoint + "/stop",
		responseFirstTimingEndpoint + "/export",
	} {
		method := http.MethodGet
		if strings.HasSuffix(path, "/start") || strings.HasSuffix(path, "/stop") {
			method = http.MethodPost
		}
		response := serveTimingRoute(mux, method, path, "192.0.2.10:1234")
		if response.Code != http.StatusNotFound {
			t.Errorf("non-loopback %s %s status=%d, want 404", method, path, response.Code)
		}
	}

	status := serveTimingRoute(mux, http.MethodGet, responseFirstTimingEndpoint, "127.0.0.1:1234")
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"state":"collecting"`) {
		t.Fatalf("loopback status response = %d %s", status.Code, status.Body.String())
	}
	start := serveTimingRoute(mux, http.MethodPost, responseFirstTimingEndpoint+"/start", "127.0.0.1:1234")
	if start.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", start.Code, start.Body.String())
	}
	beforeStop := serveTimingRoute(mux, http.MethodGet, responseFirstTimingEndpoint+"/export", "127.0.0.1:1234")
	if beforeStop.Code != http.StatusConflict {
		t.Fatalf("export before freeze status=%d, want 409", beforeStop.Code)
	}

	stop := serveTimingRoute(mux, http.MethodPost, responseFirstTimingEndpoint+"/stop", "127.0.0.1:1234")
	if stop.Code != http.StatusOK {
		t.Fatalf("stop status=%d body=%s", stop.Code, stop.Body.String())
	}
	var stopResponse responseFirstTimingStatus
	if err := json.Unmarshal(stop.Body.Bytes(), &stopResponse); err != nil {
		t.Fatalf("decode stop status: %v", err)
	}
	if stopResponse.State != "frozen" || !stopResponse.Complete || !stopResponse.RestartRequired {
		t.Fatalf("stop response = %+v, want complete frozen batch requiring process restart", stopResponse)
	}

	export := serveTimingRoute(mux, http.MethodGet, responseFirstTimingEndpoint+"/export", "127.0.0.1:1234")
	if export.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", export.Code, export.Body.String())
	}
	var payload struct {
		SchemaVersion int  `json:"schema_version"`
		Complete      bool `json:"complete"`
		Turns         []struct {
			Stamps []struct {
				Point uint16 `json:"point"`
			} `json:"stamps"`
		} `json:"turns"`
	}
	if err := json.Unmarshal(export.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode timing export: %v", err)
	}
	if payload.SchemaVersion != responseFirstTimingExportSchema || !payload.Complete || len(payload.Turns) != 1 || len(payload.Turns[0].Stamps) != 1 || payload.Turns[0].Stamps[0].Point != turntiming.PointV2Ingress {
		t.Fatalf("timing export payload = %+v", payload)
	}
	if runtime.store != nil {
		t.Fatal("RAM timing control unexpectedly needs an EventStore")
	}
	statusAfterStop := serveTimingRoute(mux, http.MethodGet, responseFirstTimingEndpoint, "127.0.0.1:1234")
	if statusAfterStop.Code != http.StatusOK {
		t.Fatalf("post-stop status=%d body=%s", statusAfterStop.Code, statusAfterStop.Body.String())
	}
	var stoppedStatus responseFirstTimingStatus
	if err := json.Unmarshal(statusAfterStop.Body.Bytes(), &stoppedStatus); err != nil {
		t.Fatalf("decode post-stop status: %v", err)
	}
	if !stoppedStatus.RestartRequired {
		t.Fatalf("post-stop status = %+v, want restart_required", stoppedStatus)
	}
	if got := serveTimingRoute(mux, http.MethodPost, responseFirstTimingEndpoint+"/start", "127.0.0.1:1234").Code; got != http.StatusConflict {
		t.Fatalf("start after freeze status=%d, want 409", got)
	}
}

func TestResponseFirstTimingRoutesRequireExactOptIn(t *testing.T) {
	for _, value := range []string{"", "true", "01", " 1 "} {
		t.Run("value_"+value, func(t *testing.T) {
			t.Setenv(responseFirstTimingEnvironment, value)
			batch, err := turntiming.NewBatchRecorderWithConfig(turntiming.RecorderConfig{BatchCapacity: 1, StampsPerTurn: 8, MemoryLimitBytes: 1 << 20})
			if err != nil {
				t.Fatal(err)
			}
			runtime := &responseFirstConversationRuntime{processing: &responseFirstV2Processing{timing: batch}}
			mux := http.NewServeMux()
			registerResponseFirstTimingRoutes(mux, runtime)
			response := serveTimingRoute(mux, http.MethodGet, responseFirstTimingEndpoint, "127.0.0.1:1234")
			if response.Code != http.StatusNotFound {
				t.Fatalf("opt-in %q registered timing route: status=%d", value, response.Code)
			}
		})
	}
}

func TestResponseFirstTimingStopWaitsForRegisteredDrains(t *testing.T) {
	batch, err := turntiming.NewBatchRecorderWithConfig(turntiming.RecorderConfig{BatchCapacity: 1, StampsPerTurn: 8, MemoryLimitBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := batch.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}
	child, registered := turn.RegisterChild()
	if !registered {
		t.Fatal("register background child")
	}
	lifecycle := viewer.NewV2ProcessingLifecycle(context.Background())
	processCtx, started := lifecycle.Start()
	if !started {
		t.Fatal("register foreground lifecycle work")
	}
	runtime := &responseFirstConversationRuntime{processing: &responseFirstV2Processing{
		lifecycle: lifecycle,
		timing:    batch,
	}}
	type stopResult struct {
		snapshot turntiming.BatchSnapshot
		err      error
	}
	stopDone := make(chan stopResult, 1)
	go func() {
		snapshot, stopErr := runtime.stopTimingBatchWithTimeout(time.Second)
		stopDone <- stopResult{snapshot: snapshot, err: stopErr}
	}()
	waitForV2LifecycleAdmissionClosed(t, lifecycle)
	if err := processCtx.Err(); err != nil {
		t.Fatalf("diagnostic stop canceled foreground processing: %v", err)
	}
	if !turn.FinishForeground() {
		t.Fatal("finish foreground processing")
	}
	lifecycle.Done()
	successor, ok := child.Handoff()
	if !ok {
		t.Fatal("handoff live child after foreground completion")
	}
	if !successor.Complete() {
		t.Fatal("complete successor child")
	}
	result := <-stopDone
	snapshot, err := result.snapshot, result.err
	if err != nil {
		t.Fatalf("stop drained timing batch: %v", err)
	}
	if snapshot.IsIncomplete() || snapshot.TurnCount() != 1 {
		t.Fatalf("drained timing batch incomplete=%t turns=%d", snapshot.IsIncomplete(), snapshot.TurnCount())
	}
	turnSnapshot, ok := snapshot.Turn(0)
	if !ok || !turnSnapshot.ForegroundDone() || turnSnapshot.PendingChildren() != 0 {
		t.Fatalf("exported timing turn does not show completed foreground/background: ok=%t", ok)
	}
}

func waitForV2LifecycleAdmissionClosed(t *testing.T, lifecycle *viewer.V2ProcessingLifecycle) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, ok := lifecycle.Start(); !ok {
			return
		}
		lifecycle.Done()
		goruntime.Gosched()
	}
	t.Fatal("timing stop did not close foreground admission")
}

func TestResponseFirstTimingStopDoesNotCancelForegroundOrBusinessLanes(t *testing.T) {
	batch, err := turntiming.NewBatchRecorderWithConfig(turntiming.RecorderConfig{BatchCapacity: 1, StampsPerTurn: 8, MemoryLimitBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := batch.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}
	child, registered := turn.RegisterChild()
	if !registered {
		t.Fatal("register background child")
	}
	lifecycle := viewer.NewV2ProcessingLifecycle(context.Background())
	processCtx, started := lifecycle.Start()
	if !started {
		t.Fatal("register foreground processing")
	}
	backgroundCtx, cancelBackground := context.WithCancel(context.Background())
	defer cancelBackground()
	backgroundStopCalled := make(chan struct{}, 1)
	runtime := &responseFirstConversationRuntime{processing: &responseFirstV2Processing{
		lifecycle: lifecycle,
		timing:    batch,
		backgroundDrainStop: func(context.Context) error {
			backgroundStopCalled <- struct{}{}
			cancelBackground()
			return nil
		},
	}}
	type stopResult struct {
		snapshot turntiming.BatchSnapshot
		err      error
	}
	stopDone := make(chan stopResult, 1)
	go func() {
		snapshot, stopErr := runtime.stopTimingBatchWithTimeout(time.Second)
		stopDone <- stopResult{snapshot: snapshot, err: stopErr}
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, ok := lifecycle.Start(); !ok {
			break
		}
		lifecycle.Done()
		goruntime.Gosched()
	}
	if _, ok := lifecycle.Start(); ok {
		lifecycle.Done()
		t.Fatal("timing stop did not close foreground admission")
	}
	foregroundCanceled := processCtx.Err() != nil
	if !turn.FinishForeground() {
		t.Fatal("finish foreground processing")
	}
	lifecycle.Done()
	if !child.Complete() {
		t.Fatal("complete registered child")
	}
	result := <-stopDone
	select {
	case <-backgroundStopCalled:
		t.Error("timing stop called the process-shutdown hook for a business lane")
	default:
	}
	if err := backgroundCtx.Err(); err != nil {
		t.Errorf("timing stop canceled a business lane: %v", err)
	}
	if foregroundCanceled {
		t.Error("timing stop canceled running foreground processing")
	}
	if result.err != nil {
		t.Fatalf("timing stop: %v", result.err)
	}
}

func TestResponseFirstTimingDuplicateStopReturnsFrozenSnapshotWithoutSideEffects(t *testing.T) {
	batch, err := turntiming.NewBatchRecorderWithConfig(turntiming.RecorderConfig{BatchCapacity: 1, StampsPerTurn: 8, MemoryLimitBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	backgroundStopCalls := 0
	runtime := &responseFirstConversationRuntime{processing: &responseFirstV2Processing{
		lifecycle: viewer.NewV2ProcessingLifecycle(context.Background()),
		timing:    batch,
		backgroundDrainStop: func(context.Context) error {
			backgroundStopCalls++
			return nil
		},
	}}
	first, firstErr := runtime.stopTimingBatchWithTimeout(time.Second)
	second, secondErr := runtime.stopTimingBatchWithTimeout(time.Second)
	if firstErr != nil || secondErr != nil {
		t.Fatalf("duplicate stop errors = (%v, %v)", firstErr, secondErr)
	}
	if first.TurnCount() != second.TurnCount() || first.IsIncomplete() != second.IsIncomplete() {
		t.Fatalf("duplicate stop snapshots differ: first=(%d,%t) second=(%d,%t)", first.TurnCount(), first.IsIncomplete(), second.TurnCount(), second.IsIncomplete())
	}
	if backgroundStopCalls != 0 {
		t.Fatalf("diagnostic stop called the business-lane shutdown hook %d times", backgroundStopCalls)
	}
}

func TestResponseFirstTimingStopMarksDrainTimeoutIncomplete(t *testing.T) {
	batch, err := turntiming.NewBatchRecorderWithConfig(turntiming.RecorderConfig{BatchCapacity: 1, StampsPerTurn: 8, MemoryLimitBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := batch.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}
	turn.Mark(turntiming.PointV2Ingress, turntiming.ComponentV2ViewerHandler, 1, 0)
	runtime := &responseFirstConversationRuntime{processing: &responseFirstV2Processing{
		lifecycle: viewer.NewV2ProcessingLifecycle(context.Background()), timing: batch,
		backgroundDrainStop: func(context.Context) error { return context.DeadlineExceeded },
	}}
	snapshot, err := runtime.stopTimingBatchWithTimeout(50 * time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop timeout error=%v, want deadline exceeded", err)
	}
	if !snapshot.IsIncomplete() {
		t.Fatal("drain timeout was exported as a complete timing batch")
	}
}

func TestResponseFirstTimingStopRouteReportsIncompleteDrain(t *testing.T) {
	t.Setenv(responseFirstTimingEnvironment, "1")
	batch, err := turntiming.NewBatchRecorderWithConfig(turntiming.RecorderConfig{BatchCapacity: 1, StampsPerTurn: 8, MemoryLimitBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &responseFirstConversationRuntime{processing: &responseFirstV2Processing{
		lifecycle:           viewer.NewV2ProcessingLifecycle(context.Background()),
		timing:              batch,
		backgroundDrainStop: func(context.Context) error { return context.DeadlineExceeded },
	}}
	mux := http.NewServeMux()
	registerResponseFirstTimingRoutes(mux, runtime)

	response := serveTimingRoute(mux, http.MethodPost, responseFirstTimingEndpoint+"/stop", "127.0.0.1:1234")
	if response.Code != http.StatusOK {
		t.Fatalf("incomplete stop status=%d body=%s", response.Code, response.Body.String())
	}
	var status responseFirstTimingStatus
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode incomplete stop status: %v", err)
	}
	if status.State != "incomplete" || status.Complete || !status.RestartRequired {
		t.Fatalf("incomplete stop status = %+v, want explicit incomplete state", status)
	}
}

func TestResponseFirstTimingExportHasHardSizeBound(t *testing.T) {
	batch, err := turntiming.NewBatchRecorderWithConfig(turntiming.RecorderConfig{BatchCapacity: 1, StampsPerTurn: 8, MemoryLimitBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := batch.BeginTurn()
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 8; index++ {
		turn.Mark(turntiming.PointV2Ingress, turntiming.ComponentV2ViewerHandler, 1, uint32(index))
	}
	turn.FinishForeground()
	if err := batch.Freeze(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := batch.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encodeResponseFirstTimingExport(snapshot, 512); !errors.Is(err, errResponseFirstTimingExportTooLarge) {
		t.Fatalf("oversized export error=%v, want bounded-size error", err)
	}
}

func serveTimingRoute(mux *http.ServeMux, method, path, remoteAddr string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	request.RemoteAddr = remoteAddr
	request.Host = "127.0.0.1:8080"
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}
