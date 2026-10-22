package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestHandleSendExplicitV2FailsClosedWhenV2IsDisabled(t *testing.T) {
	legacyCalled := false
	h := HandleSend(func(context.Context, SendRequest) (string, error) {
		legacyCalled = true
		return "legacy", nil
	}, nil)
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"protocol_version":2,"message":"hello"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	h.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
	if legacyCalled {
		t.Fatal("explicit v2 request fell through to the legacy RootTask handler")
	}
}

func TestHandleSendV2AcceptsDurablyBeforeAckAndReturnsCanonicalReceipt(t *testing.T) {
	sessionID := modulecore.NewSessionID()
	var accepted atomic.Bool
	acceptCalls := 0
	processStarted := make(chan V2AcceptedTurn, 1)
	accept := func(_ context.Context, request V2SendRequest) (V2AcceptedTurn, error) {
		acceptCalls++
		if request.SessionID != sessionID || request.ActorID != "viewer-user" || request.Message != "hello from v2" || request.To != modulechat.ViewerRecipientMio || request.ClientSubmissionID != "portal-submit-1" {
			t.Fatalf("Accept request = %+v", request)
		}
		if request.TraceID.Validate() != nil || request.TurnID.Validate() != nil || request.InputMessageID.Validate() != nil {
			t.Fatalf("Accept request had invalid generated IDs: %+v", request)
		}
		accepted.Store(true)
		return acceptedV2Turn(request, false), nil
	}
	options := &V2SendOptions{
		Admission: testV2Admission(),
		Enabled:   true, ActorID: "viewer-user", Accept: accept,
		ResolveSessionID: func(context.Context) (modulecore.SessionID, error) { return sessionID, nil },
		Process: func(_ context.Context, turn V2AcceptedTurn) error {
			if !accepted.Load() {
				t.Error("Process ran before durable Accept completed")
			}
			processStarted <- turn
			return nil
		},
	}
	legacyCalled := false
	h := HandleSendWithProtocolV2(func(context.Context, SendRequest) (string, error) {
		legacyCalled = true
		return "legacy", nil
	}, nil, nil, nil, nil, options)
	body := `{"protocol_version":2,"message":"hello from v2","client_submission_id":"portal-submit-1","to":"mio","viewer_client_id":"portal-tab-1","input_source":"text","user_id":"untrusted-client-id","device_name":"Linux x86_64","audio_output":"disabled"}`
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	if acceptCalls != 1 {
		t.Fatalf("Accept calls = %d, want 1", acceptCalls)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}
	if legacyCalled {
		t.Fatal("v2 request called the legacy RootTask handler")
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["status"] != "accepted" || response["session_id"] != string(sessionID) || response["client_submission_id"] != "portal-submit-1" {
		t.Fatalf("acceptance response = %#v", response)
	}
	for _, key := range []string{"turn_id", "trace_id", "input_message_id"} {
		if value, ok := response[key].(string); !ok || value == "" {
			t.Fatalf("response field %q missing: %#v", key, response)
		}
	}
	for _, forbidden := range []string{"root_task_id", "task_id", "run_id", "agent_message_id"} {
		if _, exists := response[forbidden]; exists {
			t.Fatalf("v2 response included forbidden field %q: %#v", forbidden, response)
		}
	}
	select {
	case turn := <-processStarted:
		if turn.SessionID != sessionID || turn.ActorID != "viewer-user" || turn.ClientSubmissionID != "portal-submit-1" {
			t.Fatalf("Process turn = %+v", turn)
		}
	case <-time.After(time.Second):
		t.Fatal("foreground processing was not scheduled after durable acceptance")
	}
}

func TestHandleSendV2AcceptsCanonicalReplyTarget(t *testing.T) {
	sessionID := modulecore.NewSessionID()
	replyTo := modulecore.NewMessageID()
	acceptCalls := 0
	options := &V2SendOptions{
		Admission: testV2Admission(),
		Enabled:   true, ActorID: "viewer-user",
		ResolveSessionID: func(context.Context) (modulecore.SessionID, error) { return sessionID, nil },
		Accept: func(_ context.Context, request V2SendRequest) (V2AcceptedTurn, error) {
			acceptCalls++
			if request.ReplyToMessageID != replyTo || request.ActorID != "viewer-user" {
				t.Errorf("Accept request reply target or trusted actor = %+v", request)
			}
			return acceptedV2Turn(request, false), nil
		},
		Process: func(context.Context, V2AcceptedTurn) error { return nil },
	}
	h := HandleSendWithProtocolV2(nil, nil, nil, nil, nil, options)
	body, err := json.Marshal(map[string]any{
		"protocol_version":     ViewerProtocolVersionV2,
		"message":              "answer to clarification",
		"client_submission_id": "clarification-reply-1",
		"reply_to_message_id":  string(replyTo),
		"user_id":              "spoofed-user",
	})
	if err != nil {
		t.Fatal(err)
	}
	response := postViewerV2(t, h, string(body), "application/json")
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", response.Code, response.Body.String())
	}
	if acceptCalls != 1 {
		t.Fatalf("Accept calls = %d, want one", acceptCalls)
	}
}

func TestHandleSendV2DuplicateReusesReceiptAndDoesNotProcessTwice(t *testing.T) {
	sessionID := modulecore.NewSessionID()
	replyTo := modulecore.NewMessageID()
	changedReplyTo := modulecore.NewMessageID()
	processStarted := make(chan V2AcceptedTurn, 2)
	var mu sync.Mutex
	var saved V2AcceptedTurn
	var savedMessage string
	var savedReplyTo modulecore.MessageID
	acceptCalls := 0
	accept := func(_ context.Context, request V2SendRequest) (V2AcceptedTurn, error) {
		mu.Lock()
		defer mu.Unlock()
		acceptCalls++
		if saved.TurnID != "" {
			if savedMessage != request.Message || savedReplyTo != request.ReplyToMessageID {
				return V2AcceptedTurn{}, ErrV2SubmissionConflict
			}
			saved.IdempotentReplay = true
			return saved, nil
		}
		saved = acceptedV2Turn(request, false)
		savedMessage = request.Message
		savedReplyTo = request.ReplyToMessageID
		return saved, nil
	}
	options := &V2SendOptions{
		Admission: testV2Admission(),
		Enabled:   true, ActorID: "viewer-user", Accept: accept,
		ResolveSessionID: func(context.Context) (modulecore.SessionID, error) { return sessionID, nil },
		Process: func(_ context.Context, turn V2AcceptedTurn) error {
			processStarted <- turn
			return nil
		},
	}
	h := HandleSendWithProtocolV2(nil, nil, nil, nil, nil, options)
	firstBody := fmt.Sprintf(`{"protocol_version":2,"message":"same text","client_submission_id":"retry-key","to":"mio","reply_to_message_id":%q}`, replyTo)
	first := postViewerV2(t, h, firstBody, "application/json")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	select {
	case <-processStarted:
	case <-time.After(time.Second):
		t.Fatal("first accepted turn was not processed")
	}
	second := postViewerV2(t, h, firstBody, "application/json")
	if second.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d body=%s", second.Code, second.Body.String())
	}
	var firstResponse, secondResponse map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &firstResponse); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondResponse); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"session_id", "turn_id", "trace_id", "input_message_id", "client_submission_id"} {
		if firstResponse[key] != secondResponse[key] {
			t.Fatalf("retry changed %s: first=%#v second=%#v", key, firstResponse, secondResponse)
		}
	}
	changedTargetBody := fmt.Sprintf(`{"protocol_version":2,"message":"same text","client_submission_id":"retry-key","to":"mio","reply_to_message_id":%q}`, changedReplyTo)
	changedTarget := postViewerV2(t, h, changedTargetBody, "application/json")
	if changedTarget.Code != http.StatusConflict {
		t.Fatalf("changed reply target status = %d, want %d; body=%s", changedTarget.Code, http.StatusConflict, changedTarget.Body.String())
	}
	if acceptCalls != 3 {
		t.Fatalf("Accept calls = %d, want owner to see initial, exact retry, and changed-target retry", acceptCalls)
	}
	select {
	case duplicate := <-processStarted:
		t.Fatalf("idempotent retry scheduled processing again: %+v", duplicate)
	default:
	}
}

func TestHandleSendV2SameSubmissionWithDifferentTextConflicts(t *testing.T) {
	sessionID := modulecore.NewSessionID()
	var mu sync.Mutex
	var saved V2AcceptedTurn
	var savedMessage string
	var processCount atomic.Int32
	processStarted := make(chan struct{}, 2)
	options := &V2SendOptions{
		Admission: testV2Admission(),
		Enabled:   true, ActorID: "viewer-user",
		ResolveSessionID: func(context.Context) (modulecore.SessionID, error) { return sessionID, nil },
		Accept: func(_ context.Context, request V2SendRequest) (V2AcceptedTurn, error) {
			mu.Lock()
			defer mu.Unlock()
			if saved.TurnID != "" {
				if request.Message != savedMessage {
					return V2AcceptedTurn{}, ErrV2SubmissionConflict
				}
				saved.IdempotentReplay = true
				return saved, nil
			}
			saved, savedMessage = acceptedV2Turn(request, false), request.Message
			return saved, nil
		},
		Process: func(context.Context, V2AcceptedTurn) error {
			processCount.Add(1)
			processStarted <- struct{}{}
			return nil
		},
	}
	h := HandleSendWithProtocolV2(nil, nil, nil, nil, nil, options)
	first := postViewerV2(t, h, `{"protocol_version":2,"message":"first","client_submission_id":"fixed-key"}`, "application/json")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	select {
	case <-processStarted:
	case <-time.After(time.Second):
		t.Fatal("first accepted turn was not processed")
	}
	second := postViewerV2(t, h, `{"protocol_version":2,"message":"changed","client_submission_id":"fixed-key"}`, "application/json")
	if second.Code != http.StatusConflict {
		t.Fatalf("changed retry status = %d, want %d body=%s", second.Code, http.StatusConflict, second.Body.String())
	}
	if strings.Contains(second.Body.String(), `"status":"accepted"`) {
		t.Fatalf("conflicting retry received an acceptance: %s", second.Body.String())
	}
	if processCount.Load() != 1 {
		t.Fatalf("process count = %d, want one for the original acceptance", processCount.Load())
	}
}

func TestHandleSendV2AcceptanceFailureDoesNotAckOrProcess(t *testing.T) {
	processCalled := atomic.Bool{}
	options := &V2SendOptions{
		Admission: testV2Admission(),
		Enabled:   true, ActorID: "viewer-user",
		ResolveSessionID: func(context.Context) (modulecore.SessionID, error) { return modulecore.NewSessionID(), nil },
		Accept: func(context.Context, V2SendRequest) (V2AcceptedTurn, error) {
			return V2AcceptedTurn{}, errors.New("storage unavailable")
		},
		Process: func(context.Context, V2AcceptedTurn) error { processCalled.Store(true); return nil },
	}
	h := HandleSendWithProtocolV2(nil, nil, nil, nil, nil, options)
	recorder := postViewerV2(t, h, `{"protocol_version":2,"message":"durable","client_submission_id":"accept-failure"}`, "application/json")
	if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), `"status":"accepted"`) {
		t.Fatalf("acceptance failure response = %d %s", recorder.Code, recorder.Body.String())
	}
	if processCalled.Load() {
		t.Fatal("foreground processor ran after durable acceptance failed")
	}
}

func TestHandleSendV2ProcessingUsesContextIndependentOfRequest(t *testing.T) {
	sessionID := modulecore.NewSessionID()
	processContext := make(chan error, 1)
	options := &V2SendOptions{
		Admission: testV2Admission(),
		Enabled:   true, ActorID: "viewer-user",
		ResolveSessionID: func(context.Context) (modulecore.SessionID, error) { return sessionID, nil },
		Accept: func(_ context.Context, request V2SendRequest) (V2AcceptedTurn, error) {
			return acceptedV2Turn(request, false), nil
		},
		Process: func(ctx context.Context, _ V2AcceptedTurn) error {
			if _, ok := turntiming.BatchTurnFromContext(ctx); ok {
				processContext <- errors.New("diagnostic context unexpectedly present while timing is off")
				return nil
			}
			processContext <- ctx.Err()
			return nil
		},
	}
	h := HandleSendWithProtocolV2(nil, nil, nil, nil, nil, options)
	requestContext, cancelRequest := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"protocol_version":2,"message":"hello","client_submission_id":"context-key"}`)).WithContext(requestContext)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	cancelRequest()
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	select {
	case err := <-processContext:
		if err != nil {
			t.Fatalf("processing context inherited request cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("foreground processing was not scheduled")
	}
}

func TestHandleSendV2OptInTimingCorrelatesDetachedProcessingAndFreezes(t *testing.T) {
	batch, err := turntiming.NewBatchRecorderWithConfig(turntiming.RecorderConfig{BatchCapacity: 1, StampsPerTurn: 32, MemoryLimitBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := NewV2ProcessingLifecycle(context.Background())
	type processInfo struct {
		turn turntiming.TurnRecorder
		ok   bool
		err  error
	}
	processStarted := make(chan processInfo, 1)
	processDone := make(chan struct{})
	sessionID := modulecore.NewSessionID()
	options := &V2SendOptions{
		Admission: testV2Admission(),
		Enabled:   true, ActorID: "viewer-user", Timing: batch, Processing: lifecycle,
		ResolveSessionID: func(context.Context) (modulecore.SessionID, error) { return sessionID, nil },
		Accept: func(_ context.Context, request V2SendRequest) (V2AcceptedTurn, error) {
			return acceptedV2Turn(request, false), nil
		},
		Process: func(ctx context.Context, _ V2AcceptedTurn) error {
			turn, ok := turntiming.BatchTurnFromContext(ctx)
			processStarted <- processInfo{turn: turn, ok: ok, err: ctx.Err()}
			close(processDone)
			return nil
		},
	}
	h := HandleSendWithProtocolV2(nil, nil, nil, nil, nil, options)
	requestContext, cancelRequest := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"protocol_version":2,"message":"timed input","client_submission_id":"timing-key"}`)).WithContext(requestContext)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	cancelRequest()
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", response.Code, response.Body.String())
	}
	var info processInfo
	select {
	case info = <-processStarted:
	case <-time.After(time.Second):
		t.Fatal("async foreground processing did not start")
	}
	if !info.ok || info.err != nil {
		t.Fatalf("processing context did not carry timing or inherited request cancellation: ok=%t err=%v", info.ok, info.err)
	}
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := lifecycle.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("foreground shutdown: %v", err)
	}
	<-processDone
	if err := batch.Freeze(); err != nil {
		t.Fatalf("freeze completed timing batch: %v", err)
	}
	snapshot, err := batch.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	turn, ok := snapshot.Turn(0)
	if !ok {
		t.Fatal("timing batch has no turn")
	}
	turnID, traceID, identified := turn.Identity()
	if !identified || modulecore.TurnID(turnID).Validate() != nil || modulecore.TraceID(traceID).Validate() != nil {
		t.Fatalf("timing identity missing or invalid: turn=%q trace=%q identified=%t", turnID, traceID, identified)
	}
	seen := make(map[uint16]bool)
	for index := uint32(0); index < turn.StampCount(); index++ {
		stamp, ok := turn.Stamp(index)
		if !ok {
			t.Fatalf("stamp %d missing", index)
		}
		if stamp.Phase == turntiming.PhasePoint {
			seen[stamp.Point] = true
		}
	}
	for _, point := range []uint16{turntiming.PointV2Ingress, turntiming.PointV2Accepted, turntiming.PointV2AsyncStart, turntiming.PointV2ForegroundComplete} {
		if !seen[point] {
			t.Errorf("timing point %d was not recorded", point)
		}
	}
	if !turn.ForegroundDone() || turn.MissingSpanEnds() != 0 {
		t.Fatalf("timing turn did not drain cleanly: foreground_done=%t missing_span_ends=%d", turn.ForegroundDone(), turn.MissingSpanEnds())
	}
}

func TestHandleSendV2AcceptErrorRecordsFailureAndCompletesForeground(t *testing.T) {
	batch, err := turntiming.NewBatchRecorderWithConfig(turntiming.RecorderConfig{BatchCapacity: 1, StampsPerTurn: 16, MemoryLimitBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	options := &V2SendOptions{
		Admission: testV2Admission(),
		Enabled:   true, ActorID: "viewer-user", Timing: batch,
		ResolveSessionID: func(context.Context) (modulecore.SessionID, error) { return modulecore.NewSessionID(), nil },
		Accept: func(context.Context, V2SendRequest) (V2AcceptedTurn, error) {
			return V2AcceptedTurn{}, errors.New("journal unavailable")
		},
		Process: func(context.Context, V2AcceptedTurn) error {
			t.Fatal("processor ran after failed acceptance")
			return nil
		},
	}
	h := HandleSendWithProtocolV2(nil, nil, nil, nil, nil, options)
	response := postViewerV2(t, h, `{"protocol_version":2,"message":"input","client_submission_id":"failed-accept"}`, "application/json")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("accept failure status = %d, want 503", response.Code)
	}
	if err := batch.Freeze(); err != nil {
		t.Fatalf("freeze failed acceptance batch: %v", err)
	}
	snapshot, err := batch.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	turn, _ := snapshot.Turn(0)
	if !turn.ForegroundDone() || turn.MissingSpanEnds() != 0 {
		t.Fatalf("failed accept left incomplete timing turn: done=%t missing=%d", turn.ForegroundDone(), turn.MissingSpanEnds())
	}
	foundFailure := false
	for index := uint32(0); index < turn.StampCount(); index++ {
		stamp, _ := turn.Stamp(index)
		if stamp.Point == turntiming.PointV2Error && stamp.Phase == turntiming.PhasePoint && stamp.Ref == uint32(turntiming.PointV2Accept) && stamp.Result == uint8(turntiming.ResultError) {
			foundFailure = true
		}
	}
	if !foundFailure {
		t.Fatal("durable acceptance error was not recorded")
	}
}

func TestHandleSendV2OversizedIngressIsRecordedWhenTimingIsEnabled(t *testing.T) {
	batch, err := turntiming.NewBatchRecorderWithConfig(turntiming.RecorderConfig{BatchCapacity: 1, StampsPerTurn: 8, MemoryLimitBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	options := &V2SendOptions{
		Admission: testV2Admission(),
		Enabled:   true, ActorID: "viewer-user", Timing: batch,
		ResolveSessionID: func(context.Context) (modulecore.SessionID, error) { return modulecore.NewSessionID(), nil },
		Accept: func(context.Context, V2SendRequest) (V2AcceptedTurn, error) {
			t.Fatal("oversized request reached durable acceptance")
			return V2AcceptedTurn{}, nil
		},
		Process: func(context.Context, V2AcceptedTurn) error {
			t.Fatal("oversized request reached processing")
			return nil
		},
	}
	body := `{"protocol_version":2,"message":"` + strings.Repeat("x", viewerV2MaxRequestBytes) + `","client_submission_id":"large"}`
	h := HandleSendWithProtocolV2(nil, nil, nil, nil, nil, options)
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized v2 status = %d, want 413", response.Code)
	}
	if err := batch.Freeze(); err != nil {
		t.Fatalf("freeze oversized ingress batch: %v", err)
	}
	snapshot, err := batch.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	turn, ok := snapshot.Turn(0)
	if !ok || !turn.ForegroundDone() {
		t.Fatalf("oversized ingress did not complete timing turn: ok=%t", ok)
	}
	seenIngress, seenError := false, false
	for index := uint32(0); index < turn.StampCount(); index++ {
		stamp, _ := turn.Stamp(index)
		seenIngress = seenIngress || stamp.Point == turntiming.PointV2Ingress && stamp.Phase == turntiming.PhasePoint
		seenError = seenError || stamp.Point == turntiming.PointV2Error && stamp.Ref == uint32(turntiming.PointV2Ingress)
	}
	if !seenIngress || !seenError {
		t.Fatalf("oversized v2 ingress stamps missing: ingress=%t error=%t", seenIngress, seenError)
	}
}

func TestV2ProcessingShutdownIsBoundedAndAllowsIncompleteTimingFreeze(t *testing.T) {
	batch, err := turntiming.NewBatchRecorderWithConfig(turntiming.RecorderConfig{BatchCapacity: 1, StampsPerTurn: 16, MemoryLimitBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := NewV2ProcessingLifecycle(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	options := &V2SendOptions{
		Admission: testV2Admission(),
		Enabled:   true, ActorID: "viewer-user", Timing: batch, Processing: lifecycle,
		ResolveSessionID: func(context.Context) (modulecore.SessionID, error) { return modulecore.NewSessionID(), nil },
		Accept: func(_ context.Context, request V2SendRequest) (V2AcceptedTurn, error) {
			return acceptedV2Turn(request, false), nil
		},
		Process: func(context.Context, V2AcceptedTurn) error { close(started); <-release; return nil },
	}
	h := HandleSendWithProtocolV2(nil, nil, nil, nil, nil, options)
	response := postViewerV2(t, h, `{"protocol_version":2,"message":"pending durable input","client_submission_id":"drain-timeout"}`, "application/json")
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", response.Code)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("foreground processing did not start")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err = lifecycle.Shutdown(shutdownCtx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded shutdown error = %v, want deadline exceeded", err)
	}
	if err := batch.FreezeIncomplete(); err != nil {
		t.Fatalf("freeze incomplete timing batch: %v", err)
	}
	snapshot, err := batch.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.IsIncomplete() {
		t.Fatal("timed out timing batch was not marked incomplete")
	}
	close(release)
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), time.Second)
	defer cancelDrain()
	if err := lifecycle.Shutdown(drainCtx); err != nil {
		t.Fatalf("foreground did not finish after release: %v", err)
	}
}

func TestV2ProcessingLifecycleRequestTrackingDoesNotAllocatePerRequest(t *testing.T) {
	lifecycle := NewV2ProcessingLifecycle(context.Background())
	allocations := testing.AllocsPerRun(1000, func() {
		ctx, ok := lifecycle.Start()
		if !ok || ctx == nil {
			panic("lifecycle rejected request")
		}
		lifecycle.Done()
	})
	if allocations != 0 {
		t.Fatalf("process lifecycle added %.2f allocations per request", allocations)
	}
}

func TestV2ProcessingLifecycleCloseAdmissionWaitDoesNotCancelAcceptedWork(t *testing.T) {
	lifecycle := NewV2ProcessingLifecycle(context.Background())
	processCtx, started := lifecycle.Start()
	if !started {
		t.Fatal("start accepted foreground work")
	}
	lifecycle.CloseAdmission()
	if _, ok := lifecycle.Start(); ok {
		lifecycle.Done()
		t.Fatal("closed lifecycle admitted a new request")
	}
	if err := processCtx.Err(); err != nil {
		t.Fatalf("closing admission canceled accepted work: %v", err)
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err := lifecycle.Wait(waitCtx)
	cancelWait()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait with active foreground error=%v, want deadline exceeded", err)
	}
	lifecycle.Done()
	if err := lifecycle.Wait(context.Background()); err != nil {
		t.Fatalf("wait after foreground completion: %v", err)
	}
	if err := processCtx.Err(); err != nil {
		t.Fatalf("accepted work context was canceled after completion: %v", err)
	}
}

func TestV2ProcessingLifecycleShutdownStillCancelsAcceptedWork(t *testing.T) {
	lifecycle := NewV2ProcessingLifecycle(context.Background())
	processCtx, started := lifecycle.Start()
	if !started {
		t.Fatal("start accepted foreground work")
	}
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- lifecycle.Shutdown(context.Background())
	}()
	select {
	case <-processCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("process shutdown did not cancel accepted work")
	}
	lifecycle.Done()
	if err := <-shutdownDone; err != nil {
		t.Fatalf("process shutdown: %v", err)
	}
}

func TestHandleSendV2RejectsMultipartWithoutSavingAttachments(t *testing.T) {
	var accepted atomic.Bool
	saver := &fakeAttachmentSaver{}
	options := &V2SendOptions{
		Admission: testV2Admission(),
		Enabled:   true, ActorID: "viewer-user",
		ResolveSessionID: func(context.Context) (modulecore.SessionID, error) { return modulecore.NewSessionID(), nil },
		Accept: func(context.Context, V2SendRequest) (V2AcceptedTurn, error) {
			accepted.Store(true)
			return V2AcceptedTurn{}, nil
		},
		Process: func(context.Context, V2AcceptedTurn) error { return nil },
	}
	h := HandleSendWithProtocolV2(nil, nil, saver, nil, nil, options)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{
		"protocol_version": "2", "client_submission_id": "multipart-key", "message": "text", "to": "mio",
	} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("attachments[]", "photo.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("image bytes")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || accepted.Load() || len(saver.files) != 0 {
		t.Fatalf("multipart v2 status=%d accepted=%t saved_files=%d body=%s", recorder.Code, accepted.Load(), len(saver.files), recorder.Body.String())
	}
}

func TestHandleSendWithProtocolV2PreservesExplicitV1(t *testing.T) {
	sessionID := modulecore.NewSessionID()
	legacyCalled := make(chan struct{}, 1)
	h := HandleSendWithProtocolV2(func(context.Context, SendRequest) (string, error) {
		legacyCalled <- struct{}{}
		return "legacy", nil
	}, nil, nil, func(context.Context) (modulecore.SessionID, error) { return sessionID, nil }, nil,
		&V2SendOptions{Admission: testV2Admission(), Enabled: true})
	recorder := postViewerV2(t, h, `{"protocol_version":1,"message":"legacy text"}`, "application/json")
	if recorder.Code != http.StatusOK {
		t.Fatalf("explicit v1 status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	select {
	case <-legacyCalled:
	case <-time.After(time.Second):
		t.Fatal("explicit v1 request did not reach legacy handler")
	}
}

func TestHandleSendV2CannotAcceptAcrossAnInFlightV1Turn(t *testing.T) {
	sessionID := modulecore.NewSessionID()
	var processorCalls atomic.Int32
	var acceptCalls atomic.Int32
	v1Started := make(chan struct{}, 1)
	v1Done := make(chan struct{}, 1)
	releaseV1 := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseV1) }) }
	defer release()
	var versionMu sync.Mutex
	storedVersion := ""
	options := &V2SendOptions{
		Admission: NewSessionProtocolAdmission(func(_ context.Context, _ modulecore.SessionID) (string, bool, error) {
			versionMu.Lock()
			defer versionMu.Unlock()
			return storedVersion, storedVersion != "", nil
		}),
		Enabled: true, ActorID: "viewer-user",
		ResolveSessionID: func(context.Context) (modulecore.SessionID, error) { return sessionID, nil },
		Accept: func(_ context.Context, request V2SendRequest) (V2AcceptedTurn, error) {
			acceptCalls.Add(1)
			versionMu.Lock()
			storedVersion = "v2"
			versionMu.Unlock()
			return acceptedV2Turn(request, false), nil
		},
		Process: func(context.Context, V2AcceptedTurn) error { return nil },
	}
	h := HandleSendWithProtocolV2(func(context.Context, SendRequest) (string, error) {
		if processorCalls.Add(1) == 1 {
			v1Started <- struct{}{}
			<-releaseV1
		}
		v1Done <- struct{}{}
		return "legacy", nil
	}, nil, nil, func(context.Context) (modulecore.SessionID, error) { return sessionID, nil }, nil, options)

	v1 := postViewerV2(t, h, `{"protocol_version":1,"message":"legacy turn"}`, "application/json")
	if v1.Code != http.StatusOK {
		t.Fatalf("v1 status=%d body=%s, want accepted legacy request", v1.Code, v1.Body.String())
	}
	select {
	case <-v1Started:
	case <-time.After(time.Second):
		t.Fatal("accepted v1 processor did not reach its barrier")
	}

	whileV1Runs := postViewerV2(t, h, `{"protocol_version":2,"message":"v2 turn","client_submission_id":"fence-1","to":"mio"}`, "application/json")
	if whileV1Runs.Code != http.StatusConflict {
		t.Fatalf("v2 status while accepted v1 is in flight=%d body=%s, want conflict", whileV1Runs.Code, whileV1Runs.Body.String())
	}
	if got := acceptCalls.Load(); got != 0 {
		t.Fatalf("v2 Accept calls while v1 is in flight=%d, want 0", got)
	}

	release()
	select {
	case <-v1Done:
	case <-time.After(time.Second):
		t.Fatal("accepted v1 processor did not leave its barrier")
	}
	var afterV1 *httptest.ResponseRecorder
	cutoverDeadline := time.NewTimer(3 * time.Second)
	defer cutoverDeadline.Stop()
	cutoverRetry := time.NewTicker(time.Millisecond)
	defer cutoverRetry.Stop()
	for {
		afterV1 = postViewerV2(t, h, `{"protocol_version":2,"message":"v2 turn","client_submission_id":"fence-2","to":"mio"}`, "application/json")
		if afterV1.Code != http.StatusConflict {
			break
		}
		select {
		case <-cutoverDeadline.C:
			t.Fatal("v2 remained fenced after the accepted v1 handler completed")
		case <-cutoverRetry.C:
		}
	}
	if afterV1.Code != http.StatusAccepted {
		t.Fatalf("v2 status after v1 completion=%d body=%s, want accepted", afterV1.Code, afterV1.Body.String())
	}
	if got := acceptCalls.Load(); got != 1 {
		t.Fatalf("v2 Accept calls after v1 completion=%d, want 1", got)
	}

	v1AfterV2 := postViewerV2(t, h, `{"protocol_version":1,"message":"late legacy turn"}`, "application/json")
	if v1AfterV2.Code != http.StatusConflict {
		t.Fatalf("v1 status after v2 pin=%d body=%s, want conflict", v1AfterV2.Code, v1AfterV2.Body.String())
	}
	if got := processorCalls.Load(); got != 1 {
		t.Fatalf("legacy processor calls after v2 pin=%d, want only the original in-flight v1 turn", got)
	}
	versionMu.Lock()
	defer versionMu.Unlock()
	if storedVersion != "v2" {
		t.Fatalf("stored runtime version=%q, want v2", storedVersion)
	}
}

func TestHandleSendProtocolAdmissionFailsClosedForUnknownVersionOrLookupError(t *testing.T) {
	for _, lookupCase := range []struct {
		name   string
		lookup SessionRuntimeVersionLookup
	}{
		{
			name: "unknown persisted version",
			lookup: func(context.Context, modulecore.SessionID) (string, bool, error) {
				return "future-runtime", true, nil
			},
		},
		{
			name: "version lookup failed",
			lookup: func(context.Context, modulecore.SessionID) (string, bool, error) {
				return "", false, errors.New("store unavailable")
			},
		},
	} {
		t.Run(lookupCase.name, func(t *testing.T) {
			for _, protocol := range []int{1, 2} {
				t.Run(fmt.Sprintf("protocol_%d", protocol), func(t *testing.T) {
					sessionID := modulecore.NewSessionID()
					var legacyCalls atomic.Int32
					var acceptCalls atomic.Int32
					admission := NewSessionProtocolAdmission(lookupCase.lookup)
					options := &V2SendOptions{
						Admission: admission, Enabled: true, ActorID: "viewer-user",
						ResolveSessionID: func(context.Context) (modulecore.SessionID, error) { return sessionID, nil },
						Accept: func(context.Context, V2SendRequest) (V2AcceptedTurn, error) {
							acceptCalls.Add(1)
							return V2AcceptedTurn{}, nil
						},
						Process: func(context.Context, V2AcceptedTurn) error { return nil },
					}
					h := HandleSendWithProtocolV2(func(context.Context, SendRequest) (string, error) {
						legacyCalls.Add(1)
						return "legacy", nil
					}, nil, nil, func(context.Context) (modulecore.SessionID, error) { return sessionID, nil }, nil, options)
					body := `{"protocol_version":1,"message":"legacy"}`
					if protocol == 2 {
						body = `{"protocol_version":2,"message":"v2","client_submission_id":"fail-closed"}`
					}
					response := postViewerV2(t, h, body, "application/json")
					if response.Code != http.StatusServiceUnavailable {
						t.Fatalf("status=%d body=%s, want lookup failure 503", response.Code, response.Body.String())
					}
					if legacyCalls.Load() != 0 || acceptCalls.Load() != 0 {
						t.Fatalf("legacy calls=%d Accept calls=%d, want neither path to run", legacyCalls.Load(), acceptCalls.Load())
					}
				})
			}
		})
	}
}

func TestHandleSendLegacyRejectsOwnerReservedUnpinnedV2Session(t *testing.T) {
	sessionID := modulecore.NewSessionID()
	var legacyCalls atomic.Int32
	var acceptCalls atomic.Int32
	options := &V2SendOptions{
		Admission: NewSessionProtocolAdmissionWithReservationLookup(
			func(context.Context, modulecore.SessionID) (string, bool, error) { return "", false, nil },
			func(_ context.Context, got modulecore.SessionID) (bool, error) { return got == sessionID, nil },
		),
		Enabled: true, ActorID: "viewer-user",
		ResolveSessionID: func(context.Context) (modulecore.SessionID, error) { return sessionID, nil },
		Accept: func(_ context.Context, request V2SendRequest) (V2AcceptedTurn, error) {
			acceptCalls.Add(1)
			return acceptedV2Turn(request, false), nil
		},
		Process: func(context.Context, V2AcceptedTurn) error { return nil },
	}
	h := HandleSendWithProtocolV2(func(context.Context, SendRequest) (string, error) {
		legacyCalls.Add(1)
		return "legacy", nil
	}, nil, nil, func(context.Context) (modulecore.SessionID, error) { return sessionID, nil }, nil, options)

	response := postViewerV2(t, h, `{"protocol_version":1,"message":"legacy"}`, "application/json")
	if response.Code != http.StatusConflict {
		t.Fatalf("v1 status=%d body=%s, want conflict for an owner-reserved unpinned session", response.Code, response.Body.String())
	}
	if legacyCalls.Load() != 0 {
		t.Fatalf("legacy processor calls=%d, want zero for a reserved v2 session", legacyCalls.Load())
	}

	response = postViewerV2(t, h, `{"protocol_version":2,"message":"resume","client_submission_id":"reserved-retry"}`, "application/json")
	if response.Code != http.StatusAccepted {
		t.Fatalf("v2 status=%d body=%s, want acceptance from the matching reservation", response.Code, response.Body.String())
	}
	if acceptCalls.Load() != 1 {
		t.Fatalf("v2 Accept calls=%d, want one", acceptCalls.Load())
	}
}

func TestHandleSendV2RejectsUnsupportedInputBeforeAcceptance(t *testing.T) {
	base := `{"protocol_version":2,"message":"text","client_submission_id":"validation-key","to":"mio"}`
	for _, tc := range []struct {
		name        string
		body        string
		contentType string
	}{
		{name: "missing submission id", body: `{"protocol_version":2,"message":"text"}`},
		{name: "submission id too long", body: `{"protocol_version":2,"message":"text","client_submission_id":"` + strings.Repeat("x", 129) + `"}`},
		{name: "whitespace submission id", body: `{"protocol_version":2,"message":"text","client_submission_id":" bad "}`},
		{name: "empty message", body: `{"protocol_version":2,"message":" ","client_submission_id":"valid"}`},
		{name: "STT input", body: strings.Replace(base, `"to":"mio"`, `"to":"mio","input_source":"stt"`, 1)},
		{name: "TTS requested", body: strings.Replace(base, `"to":"mio"`, `"to":"mio","audio_output":"requested"`, 1)},
		{name: "non-Mio recipient", body: strings.Replace(base, `"to":"mio"`, `"to":"shiro"`, 1)},
		{name: "null reply target", body: strings.TrimSuffix(base, "}") + `,"reply_to_message_id":null}`},
		{name: "empty reply target", body: strings.TrimSuffix(base, "}") + `,"reply_to_message_id":""}`},
		{name: "non-string reply target", body: strings.TrimSuffix(base, "}") + `,"reply_to_message_id":17}`},
		{name: "malformed reply target", body: strings.TrimSuffix(base, "}") + `,"reply_to_message_id":"msg_not-a-canonical-id"}`},
		{name: "duplicate reply target", body: strings.TrimSuffix(base, "}") + `,"reply_to_message_id":"msg_not-a-canonical-id","reply_to_message_id":"msg_not-a-canonical-id"}`},
		{name: "unknown field", body: strings.TrimSuffix(base, "}") + `,"root_task_id":"tsk_fake"}`},
		{name: "duplicate field", body: `{"protocol_version":2,"message":"first","message":"second","client_submission_id":"duplicate"}`},
		{name: "multipart request", body: "", contentType: "multipart/form-data; boundary=invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var acceptCount atomic.Int32
			options := &V2SendOptions{
				Admission: testV2Admission(),
				Enabled:   true, ActorID: "viewer-user",
				ResolveSessionID: func(context.Context) (modulecore.SessionID, error) { return modulecore.NewSessionID(), nil },
				Accept: func(context.Context, V2SendRequest) (V2AcceptedTurn, error) {
					acceptCount.Add(1)
					return V2AcceptedTurn{}, nil
				},
				Process: func(context.Context, V2AcceptedTurn) error { return nil },
			}
			h := HandleSendWithProtocolV2(nil, nil, nil, nil, nil, options)
			contentType := tc.contentType
			if contentType == "" {
				contentType = "application/json"
			}
			recorder := postViewerV2(t, h, tc.body, contentType)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
			}
			if acceptCount.Load() != 0 {
				t.Fatalf("Accept calls = %d, want 0", acceptCount.Load())
			}
		})
	}
}

func TestHandleSendV2AsyncErrorRetainsAcceptedTurnCorrelationWithoutLoggingContent(t *testing.T) {
	var logs bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previousWriter)
	sessionID := modulecore.NewSessionID()
	completed := make(chan V2AcceptedTurn, 1)
	options := &V2SendOptions{
		Admission: testV2Admission(),
		Enabled:   true, ActorID: "viewer-user",
		ResolveSessionID: func(context.Context) (modulecore.SessionID, error) { return sessionID, nil },
		Accept: func(_ context.Context, request V2SendRequest) (V2AcceptedTurn, error) {
			return acceptedV2Turn(request, false), nil
		},
		Process: func(context.Context, V2AcceptedTurn) error {
			return errors.New("secret input text from failed generation")
		},
		OnError: func(_ context.Context, turn V2AcceptedTurn, _ error) { completed <- turn },
	}
	h := HandleSendWithProtocolV2(nil, nil, nil, nil, nil, options)
	recorder := postViewerV2(t, h, `{"protocol_version":2,"message":"secret input text from failed generation","client_submission_id":"log-key"}`, "application/json")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", recorder.Code, recorder.Body.String())
	}
	select {
	case turn := <-completed:
		if turn.TurnID == "" || turn.TraceID == "" || turn.InputMessageID == "" || turn.SessionID != sessionID {
			t.Fatalf("async error was not correlated to accepted turn: %+v", turn)
		}
	case <-time.After(time.Second):
		t.Fatal("async error handler was not called")
	}
	if strings.Contains(logs.String(), "secret input text from failed generation") || strings.Contains(logs.String(), "client_submission_id") {
		t.Fatalf("v2 ingress logs contained input content or submission payload: %s", logs.String())
	}
}

func TestHandleSendV2RequiresCompleteServerDependencies(t *testing.T) {
	var acceptCalled atomic.Bool
	options := &V2SendOptions{
		Admission: testV2Admission(),
		Enabled:   true, ActorID: "viewer-user",
		ResolveSessionID: func(context.Context) (modulecore.SessionID, error) { return modulecore.NewSessionID(), nil },
		Accept: func(context.Context, V2SendRequest) (V2AcceptedTurn, error) {
			acceptCalled.Store(true)
			return V2AcceptedTurn{}, nil
		},
	}
	h := HandleSendWithProtocolV2(nil, nil, nil, nil, nil, options)
	recorder := postViewerV2(t, h, `{"protocol_version":2,"message":"hello","client_submission_id":"missing-processor"}`, "application/json")
	if recorder.Code != http.StatusServiceUnavailable || acceptCalled.Load() {
		t.Fatalf("incomplete dependency response = %d accept_called=%t body=%s", recorder.Code, acceptCalled.Load(), recorder.Body.String())
	}
}

func acceptedV2Turn(request V2SendRequest, replay bool) V2AcceptedTurn {
	return V2AcceptedTurn{
		SessionID: request.SessionID, TurnID: request.TurnID, TraceID: request.TraceID,
		InputMessageID: request.InputMessageID, ClientSubmissionID: request.ClientSubmissionID,
		ActorID: request.ActorID, IdempotentReplay: replay,
	}
}

func testV2Admission() *SessionProtocolAdmission {
	return NewSessionProtocolAdmission(func(context.Context, modulecore.SessionID) (string, bool, error) {
		return "", false, nil
	})
}

func postViewerV2(t *testing.T, handler http.Handler, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
