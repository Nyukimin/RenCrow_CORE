package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appattachment "github.com/Nyukimin/RenCrow_CORE/internal/application/attachment"
	domainattachment "github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type testForegroundPinLease struct {
	releases atomic.Int64
	once     sync.Once
	done     chan struct{}
}

func newTestForegroundPinLease() *testForegroundPinLease {
	return &testForegroundPinLease{done: make(chan struct{})}
}

func (p *testForegroundPinLease) Release() error {
	if p == nil {
		return nil
	}
	p.releases.Add(1)
	p.once.Do(func() { close(p.done) })
	return nil
}

func (p *testForegroundPinLease) releaseCount() int64 {
	if p == nil {
		return 0
	}
	return p.releases.Load()
}

func validForegroundPresenceJSON() string {
	return `"presence":{"protocol_version":2,"runtime_epoch":"epoch-1","registration_receipt":"receipt-1","claim_seq":7}`
}

type testForegroundAttachmentSaver struct{}

func (testForegroundAttachmentSaver) SaveAll(context.Context, []appattachment.IncomingFile) ([]domainattachment.Attachment, error) {
	return nil, nil
}

func TestLegacySendPinsBeforeSessionAdmissionAndHoldsUntilWorkerEnds(t *testing.T) {
	sessionID := modulecore.NewSessionID()
	pin := newTestForegroundPinLease()
	var order []string
	var orderMu sync.Mutex
	appendOrder := func(value string) {
		orderMu.Lock()
		order = append(order, value)
		orderMu.Unlock()
	}
	assertOrder := func(want ...string) {
		t.Helper()
		orderMu.Lock()
		got := append([]string(nil), order...)
		orderMu.Unlock()
		if len(got) < len(want) {
			t.Fatalf("operation order=%v, want prefix=%v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("operation order=%v, want prefix=%v", got, want)
			}
		}
	}
	processStarted := make(chan struct{})
	finishProcess := make(chan struct{})
	options := &V2SendOptions{
		Admission: NewSessionProtocolAdmission(func(context.Context, modulecore.SessionID) (string, bool, error) {
			appendOrder("admission")
			return "", false, nil
		}),
		ForegroundPin: func(_ *http.Request, request ForegroundPinRequest) (ForegroundPinLease, error) {
			appendOrder("pin")
			if request.ViewerClientID != "portal-tab-7" || request.Presence == nil || request.Presence.ClaimSeq != 7 {
				t.Errorf("foreground pin request=%+v", request)
			}
			return pin, nil
		},
	}
	handler := HandleSendWithProtocolV2(func(_ context.Context, request SendRequest) (string, error) {
		appendOrder("process")
		if request.foregroundPin == nil {
			t.Error("accepted legacy request has no foreground pin")
		}
		close(processStarted)
		<-finishProcess
		return "ok", nil
	}, nil, nil, func(context.Context) (modulecore.SessionID, error) {
		appendOrder("session")
		return sessionID, nil
	}, nil, options)
	body := `{"viewer_client_id":"portal-tab-7",` + validForegroundPresenceJSON() + `,"message":"hello"}`
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	select {
	case <-processStarted:
	case <-time.After(time.Second):
		t.Fatal("legacy worker did not start")
	}
	assertOrder("pin", "session", "admission", "process")
	if got := pin.releaseCount(); got != 0 {
		t.Fatalf("pin released after HTTP response, releases=%d", got)
	}
	close(finishProcess)
	select {
	case <-pin.done:
	case <-time.After(time.Second):
		t.Fatal("pin was not released after legacy worker completion")
	}
	if got := pin.releaseCount(); got != 1 {
		t.Fatalf("pin release count=%d, want exactly once", got)
	}
}

func TestLegacySendDelegatesMissingForegroundPresencePolicyToRuntimeHook(t *testing.T) {
	var pinCalls atomic.Int64
	var workerCalls atomic.Int64
	options := &V2SendOptions{
		ForegroundPin: func(_ *http.Request, request ForegroundPinRequest) (ForegroundPinLease, error) {
			pinCalls.Add(1)
			if request.ViewerClientID != "portal-tab-7" || request.Presence != nil {
				t.Errorf("missing-reference request=%+v", request)
			}
			return nil, &ForegroundPinError{StatusCode: http.StatusConflict, ErrorCode: "presence_required", Message: "register and claim foreground presence before sending"}
		},
	}
	handler := HandleSendWithProtocolV2(func(context.Context, SendRequest) (string, error) {
		workerCalls.Add(1)
		return "ok", nil
	}, nil, nil, nil, nil, options)
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"viewer_client_id":"portal-tab-7","message":"hello"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409; body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		OK          bool   `json:"ok"`
		Protocol    int    `json:"protocol_version"`
		ErrorCode   string `json:"error_code"`
		ErrorDetail string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Protocol != ViewerProtocolVersionV2 || response.ErrorCode != "presence_required" || response.ErrorDetail == "" {
		t.Fatalf("typed error=%+v body=%s", response, recorder.Body.String())
	}
	if pinCalls.Load() != 1 || workerCalls.Load() != 0 {
		t.Fatalf("missing presence did not reach runtime pin policy or reached worker: pin=%d worker=%d", pinCalls.Load(), workerCalls.Load())
	}
}

func TestLegacyMultipartSendParsesForegroundPresenceReference(t *testing.T) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for name, value := range map[string]string{
		"viewer_client_id": "portal-tab-8",
		"presence":         `{"protocol_version":2,"runtime_epoch":"epoch-multipart","registration_receipt":"receipt-multipart","claim_seq":9}`,
		"message":          "multipart hello",
	} {
		if err := form.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	pin := newTestForegroundPinLease()
	pinRequests := make(chan ForegroundPinRequest, 1)
	finish := make(chan struct{})
	options := &V2SendOptions{ForegroundPin: func(_ *http.Request, request ForegroundPinRequest) (ForegroundPinLease, error) {
		pinRequests <- request
		return pin, nil
	}}
	handler := HandleSendWithProtocolV2(func(context.Context, SendRequest) (string, error) {
		<-finish
		return "ok", nil
	}, nil, testForegroundAttachmentSaver{}, nil, nil, options)
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	select {
	case got := <-pinRequests:
		if got.ViewerClientID != "portal-tab-8" || got.Presence == nil || got.Presence.RuntimeEpoch != "epoch-multipart" || got.Presence.RegistrationReceipt != "receipt-multipart" || got.Presence.ClaimSeq != 9 {
			t.Fatalf("multipart foreground presence=%+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("multipart presence did not reach the pin hook")
	}
	if pin.releaseCount() != 0 {
		t.Fatal("multipart pin was released before accepted worker completed")
	}
	close(finish)
	select {
	case <-pin.done:
	case <-time.After(time.Second):
		t.Fatal("multipart pin was not released after worker completion")
	}
}

func TestV2SendPinsBeforeAcceptAndRetainsPinThroughProcessing(t *testing.T) {
	sessionID := modulecore.NewSessionID()
	pin := newTestForegroundPinLease()
	var order []string
	var orderMu sync.Mutex
	appendOrder := func(value string) {
		orderMu.Lock()
		order = append(order, value)
		orderMu.Unlock()
	}
	processStarted := make(chan struct{})
	finishProcess := make(chan struct{})
	options := &V2SendOptions{
		Enabled: true, ActorID: "viewer-user",
		Admission: NewSessionProtocolAdmission(func(context.Context, modulecore.SessionID) (string, bool, error) {
			appendOrder("admission")
			return "", false, nil
		}),
		ForegroundPin: func(_ *http.Request, request ForegroundPinRequest) (ForegroundPinLease, error) {
			appendOrder("pin")
			if request.ViewerClientID != "portal-tab-v2" || request.Presence == nil || request.Presence.ClaimSeq != 11 {
				t.Errorf("foreground pin request=%+v", request)
			}
			return pin, nil
		},
		ResolveSessionID: func(context.Context) (modulecore.SessionID, error) {
			appendOrder("session")
			return sessionID, nil
		},
		Accept: func(_ context.Context, request V2SendRequest) (V2AcceptedTurn, error) {
			appendOrder("accept")
			return acceptedV2Turn(request, false), nil
		},
		Process: func(context.Context, V2AcceptedTurn) error {
			appendOrder("process")
			close(processStarted)
			<-finishProcess
			return nil
		},
	}
	handler := HandleSendWithProtocolV2(nil, nil, nil, nil, nil, options)
	presence := strings.Replace(validForegroundPresenceJSON(), `"claim_seq":7`, `"claim_seq":11`, 1)
	body := `{"protocol_version":2,"viewer_client_id":"portal-tab-v2",` + presence + `,"message":"hello","client_submission_id":"presence-submit"}`
	requestContext, cancelRequest := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(body)).WithContext(requestContext)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	cancelRequest()
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	select {
	case <-processStarted:
	case <-time.After(time.Second):
		t.Fatal("accepted V2 turn did not start")
	}
	orderMu.Lock()
	gotOrder := append([]string(nil), order...)
	orderMu.Unlock()
	wantOrder := []string{"pin", "session", "admission", "accept", "process"}
	if len(gotOrder) < len(wantOrder) {
		t.Fatalf("operation order=%v, want=%v", gotOrder, wantOrder)
	}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("operation order=%v, want prefix=%v", gotOrder, wantOrder)
		}
	}
	if pin.releaseCount() != 0 {
		t.Fatalf("pin released after HTTP response/request cancellation while turn still runs, releases=%d", pin.releaseCount())
	}
	close(finishProcess)
	select {
	case <-pin.done:
	case <-time.After(time.Second):
		t.Fatal("pin was not released after accepted V2 processing completed")
	}
	if pin.releaseCount() != 1 {
		t.Fatalf("pin release count=%d, want exactly once", pin.releaseCount())
	}
}

func TestLegacySendReleasesForegroundPinAfterOnErrorReturns(t *testing.T) {
	pin := newTestForegroundPinLease()
	processStarted := make(chan struct{})
	finishProcess := make(chan struct{})
	onErrorStarted := make(chan struct{})
	finishOnError := make(chan struct{})
	handler := HandleSendWithProtocolV2(
		func(context.Context, SendRequest) (string, error) {
			close(processStarted)
			<-finishProcess
			return "", errors.New("injected process failure")
		},
		func(context.Context, SendRequest, error) {
			close(onErrorStarted)
			<-finishOnError
		},
		nil,
		nil,
		nil,
		&V2SendOptions{ForegroundPin: func(*http.Request, ForegroundPinRequest) (ForegroundPinLease, error) {
			return pin, nil
		}},
	)
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"message":"hello"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	select {
	case <-processStarted:
	case <-time.After(time.Second):
		t.Fatal("legacy worker did not start")
	}
	if got := pin.releaseCount(); got != 0 {
		t.Fatalf("pin released after HTTP acceptance, releases=%d", got)
	}
	close(finishProcess)
	select {
	case <-onErrorStarted:
	case <-time.After(time.Second):
		t.Fatal("error handler did not start after Process returned")
	}
	if got := pin.releaseCount(); got != 0 {
		t.Fatalf("pin released before OnError returned, releases=%d", got)
	}
	close(finishOnError)
	select {
	case <-pin.done:
	case <-time.After(time.Second):
		t.Fatal("pin was not released after OnError returned")
	}
	if got := pin.releaseCount(); got != 1 {
		t.Fatalf("pin release count=%d, want exactly once", got)
	}
}

func TestLegacySendReleasesForegroundPinWhenSessionResolutionFails(t *testing.T) {
	pin := newTestForegroundPinLease()
	options := &V2SendOptions{ForegroundPin: func(*http.Request, ForegroundPinRequest) (ForegroundPinLease, error) {
		return pin, nil
	}}
	handler := HandleSendWithProtocolV2(func(context.Context, SendRequest) (string, error) {
		t.Fatal("worker must not run when session resolution fails")
		return "", nil
	}, nil, nil, func(context.Context) (modulecore.SessionID, error) {
		return "", errors.New("session lookup failed")
	}, nil, options)
	body := `{"viewer_client_id":"portal-tab-failure",` + validForegroundPresenceJSON() + `,"message":"hello"}`
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503; body=%s", recorder.Code, recorder.Body.String())
	}
	if got := pin.releaseCount(); got != 1 {
		t.Fatalf("pin release count=%d, want exactly once", got)
	}
}

func TestLegacySendReturnsTypedStalePresenceConflictBeforeAdmission(t *testing.T) {
	var admissionCalls atomic.Int64
	var workerCalls atomic.Int64
	options := &V2SendOptions{
		Admission: NewSessionProtocolAdmission(func(context.Context, modulecore.SessionID) (string, bool, error) {
			admissionCalls.Add(1)
			return "", false, nil
		}),
		ForegroundPin: func(*http.Request, ForegroundPinRequest) (ForegroundPinLease, error) {
			return nil, &ForegroundPinError{StatusCode: http.StatusConflict, ErrorCode: "registration_required", Message: "register again"}
		},
	}
	handler := HandleSendWithProtocolV2(func(context.Context, SendRequest) (string, error) {
		workerCalls.Add(1)
		return "", nil
	}, nil, nil, func(context.Context) (modulecore.SessionID, error) {
		t.Fatal("stale presence must be rejected before resolving the session")
		return "", nil
	}, nil, options)
	body := `{"viewer_client_id":"portal-tab-stale",` + validForegroundPresenceJSON() + `,"message":"hello"}`
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var response struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusConflict || response.ErrorCode != "registration_required" || admissionCalls.Load() != 0 || workerCalls.Load() != 0 {
		t.Fatalf("stale presence result status=%d error=%q admission=%d worker=%d body=%s", recorder.Code, response.ErrorCode, admissionCalls.Load(), workerCalls.Load(), recorder.Body.String())
	}
}

func TestV2SendReleasesForegroundPinOnAcceptFailureAndReplay(t *testing.T) {
	for _, test := range []struct {
		name      string
		acceptErr error
		replay    bool
	}{
		{name: "accept failure", acceptErr: errors.New("durable accept failed")},
		{name: "idempotent replay", replay: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			pin := newTestForegroundPinLease()
			var processCalls atomic.Int64
			options := &V2SendOptions{
				Enabled: true, ActorID: "viewer-user", Admission: testV2Admission(),
				ForegroundPin:    func(*http.Request, ForegroundPinRequest) (ForegroundPinLease, error) { return pin, nil },
				ResolveSessionID: func(context.Context) (modulecore.SessionID, error) { return modulecore.NewSessionID(), nil },
				Accept: func(_ context.Context, request V2SendRequest) (V2AcceptedTurn, error) {
					if test.acceptErr != nil {
						return V2AcceptedTurn{}, test.acceptErr
					}
					return acceptedV2Turn(request, test.replay), nil
				},
				Process: func(context.Context, V2AcceptedTurn) error {
					processCalls.Add(1)
					return nil
				},
			}
			handler := HandleSendWithProtocolV2(nil, nil, nil, nil, nil, options)
			recorder := postViewerV2(t, handler, `{"protocol_version":2,"viewer_client_id":"portal-tab-terminal",`+validForegroundPresenceJSON()+`,"message":"hello","client_submission_id":"terminal-case"}`, "application/json")
			if test.acceptErr != nil && recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("accept failure status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if test.replay && recorder.Code != http.StatusAccepted {
				t.Fatalf("replay status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if got := pin.releaseCount(); got != 1 {
				t.Fatalf("pin release count=%d, want exactly once", got)
			}
			if processCalls.Load() != 0 {
				t.Fatalf("process calls=%d, want 0", processCalls.Load())
			}
		})
	}
}

func TestAcceptedV2TurnReleasesForegroundPinOnlyAfterTerminalReturn(t *testing.T) {
	t.Run("cancellation while processor continues", func(t *testing.T) {
		pin := newTestForegroundPinLease()
		processing := NewV2ProcessingLifecycle(context.Background())
		processStarted := make(chan struct{})
		processCanceled := make(chan struct{})
		finishProcess := make(chan struct{})
		options := &V2SendOptions{
			Enabled: true, ActorID: "viewer-user", Admission: testV2Admission(), Processing: processing,
			ForegroundPin:    func(*http.Request, ForegroundPinRequest) (ForegroundPinLease, error) { return pin, nil },
			ResolveSessionID: func(context.Context) (modulecore.SessionID, error) { return modulecore.NewSessionID(), nil },
			Accept: func(_ context.Context, request V2SendRequest) (V2AcceptedTurn, error) {
				return acceptedV2Turn(request, false), nil
			},
			Process: func(ctx context.Context, _ V2AcceptedTurn) error {
				close(processStarted)
				<-ctx.Done()
				close(processCanceled)
				<-finishProcess
				return ctx.Err()
			},
		}
		handler := HandleSendWithProtocolV2(nil, nil, nil, nil, nil, options)
		recorder := postViewerV2(t, handler, `{"protocol_version":2,"viewer_client_id":"portal-tab-cancel",`+validForegroundPresenceJSON()+`,"message":"hello","client_submission_id":"cancel-case"}`, "application/json")
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		select {
		case <-processStarted:
		case <-time.After(time.Second):
			t.Fatal("accepted V2 turn did not start")
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer cancel()
		if err := processing.Shutdown(shutdownCtx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("processing shutdown error=%v, want deadline while processor remains active", err)
		}
		select {
		case <-processCanceled:
		case <-time.After(time.Second):
			t.Fatal("processor did not observe cancellation")
		}
		if got := pin.releaseCount(); got != 0 {
			t.Fatalf("pin released on cancellation while processor still runs, releases=%d", got)
		}
		close(finishProcess)
		waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
		defer waitCancel()
		if err := processing.Wait(waitCtx); err != nil {
			t.Fatalf("processing wait after terminal return: %v", err)
		}
		if got := pin.releaseCount(); got != 1 {
			t.Fatalf("pin release count=%d after terminal return, want exactly once", got)
		}
	})

	t.Run("timeout while processor continues", func(t *testing.T) {
		pin := newTestForegroundPinLease()
		processStarted := make(chan struct{})
		finishProcess := make(chan struct{})
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer cancel()
		options := &V2SendOptions{Process: func(ctx context.Context, _ V2AcceptedTurn) error {
			close(processStarted)
			<-ctx.Done()
			<-finishProcess
			return ctx.Err()
		}}
		accepted := V2AcceptedTurn{foregroundPin: newOnceForegroundPinLease(pin)}
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			runAcceptedViewerV2Turn(options, accepted, ctx, turntiming.TurnRecorder{}, false, false)
		}()
		select {
		case <-processStarted:
		case <-time.After(time.Second):
			t.Fatal("timed V2 turn did not start")
		}
		<-ctx.Done()
		select {
		case <-pin.done:
			t.Fatalf("pin released on timeout while processor still runs, releases=%d", pin.releaseCount())
		case <-time.After(25 * time.Millisecond):
		}
		close(finishProcess)
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("timed V2 turn did not finish")
		}
		if got := pin.releaseCount(); got != 1 {
			t.Fatalf("pin release count=%d after timeout terminal return, want exactly once", got)
		}
	})

	t.Run("panic", func(t *testing.T) {
		pin := newTestForegroundPinLease()
		accepted := V2AcceptedTurn{foregroundPin: newOnceForegroundPinLease(pin)}
		options := &V2SendOptions{Process: func(context.Context, V2AcceptedTurn) error { panic("test panic") }}
		var recovered any
		func() {
			defer func() { recovered = recover() }()
			runAcceptedViewerV2Turn(options, accepted, context.Background(), turntiming.TurnRecorder{}, false, false)
		}()
		if recovered == nil {
			t.Fatal("processor panic was not propagated")
		}
		if got := pin.releaseCount(); got != 1 {
			t.Fatalf("pin release count=%d after panic, want exactly once", got)
		}
	})
}

func TestV2SendDelegatesMissingForegroundPresencePolicyBeforeAccept(t *testing.T) {
	var pinCalls atomic.Int64
	var acceptCalls atomic.Int64
	var processCalls atomic.Int64
	options := &V2SendOptions{
		Enabled: true, ActorID: "viewer-user", Admission: testV2Admission(),
		ForegroundPin: func(request *http.Request, pinRequest ForegroundPinRequest) (ForegroundPinLease, error) {
			pinCalls.Add(1)
			if request.Header.Get("X-RenCrow-Client") != "RenCrow_PORTAL" || request.Header.Get("X-RenCrow-Interaction-Profile") != "portal-chat" {
				t.Errorf("runtime pin hook lost trusted profile headers: client=%q profile=%q", request.Header.Get("X-RenCrow-Client"), request.Header.Get("X-RenCrow-Interaction-Profile"))
			}
			if pinRequest.ViewerClientID != "portal-tab-v2" || pinRequest.Presence != nil {
				t.Errorf("missing-reference pin request=%+v", pinRequest)
			}
			if pinRequest.Presence == nil {
				return nil, &ForegroundPinError{StatusCode: http.StatusConflict, ErrorCode: "presence_required", Message: "register and claim foreground presence before sending"}
			}
			return newTestForegroundPinLease(), nil
		},
		ResolveSessionID: func(context.Context) (modulecore.SessionID, error) { return modulecore.NewSessionID(), nil },
		Accept: func(_ context.Context, request V2SendRequest) (V2AcceptedTurn, error) {
			acceptCalls.Add(1)
			return acceptedV2Turn(request, false), nil
		},
		Process: func(context.Context, V2AcceptedTurn) error {
			processCalls.Add(1)
			return nil
		},
	}
	handler := HandleSendWithProtocolV2(nil, nil, nil, nil, nil, options)
	request := httptest.NewRequest(http.MethodPost, "/viewer/send", strings.NewReader(`{"protocol_version":2,"viewer_client_id":"portal-tab-v2","message":"hello","client_submission_id":"missing-presence"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-RenCrow-Client", "RenCrow_PORTAL")
	request.Header.Set("X-RenCrow-Interaction-Profile", "portal-chat")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d, want 409; body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ErrorCode != "presence_required" || pinCalls.Load() != 1 || acceptCalls.Load() != 0 || processCalls.Load() != 0 {
		t.Fatalf("missing-presence result error=%q pin=%d accept=%d process=%d body=%s", response.ErrorCode, pinCalls.Load(), acceptCalls.Load(), processCalls.Load(), recorder.Body.String())
	}
}
