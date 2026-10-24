package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domainattachment "github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	ViewerProtocolVersionV2         = 2
	viewerV2MaxRequestBytes         = 128 * 1024
	viewerV2MaxMessageBytes         = 64 * 1024
	viewerV2MaxClientSubmissionByte = 128
)

var ErrV2SubmissionConflict = errors.New("viewer v2 client submission conflict")

var (
	ErrSessionProtocolConflict    = errors.New("viewer session is pinned to another protocol")
	ErrSessionProtocolUnavailable = errors.New("viewer session protocol state is unavailable")
	errLegacyPreSessionAdmission  = errors.New("viewer legacy RAM admission capability is unavailable")
)

const (
	sessionRuntimeVersionLegacy = "legacy"
	sessionRuntimeVersionV2     = "v2"
)

// SessionRuntimeVersionLookup reads the owner-checked persisted protocol pin
// for a canonical session. found=false means the session has no runtime pin.
// A lookup error or an unrecognized found version fails closed.
type SessionRuntimeVersionLookup func(context.Context, modulecore.SessionID) (version string, found bool, err error)
type SessionV2ReservationLookup func(context.Context, modulecore.SessionID) (reserved bool, err error)

// SessionProtocolAdmission serializes protocol admission for one session in
// this process. Legacy admissions remain counted until their asynchronous
// handler returns; v2 holds the per-session lock until durable acceptance has
// written the v2 pin.
type SessionProtocolAdmission struct {
	lookup            SessionRuntimeVersionLookup
	reservationLookup SessionV2ReservationLookup

	mu     sync.Mutex
	states map[modulecore.SessionID]*sessionProtocolAdmissionState
}

type sessionProtocolAdmissionState struct {
	mu           sync.Mutex
	activeLegacy int
	refs         int
}

func NewSessionProtocolAdmission(lookup SessionRuntimeVersionLookup) *SessionProtocolAdmission {
	return &SessionProtocolAdmission{lookup: lookup, states: make(map[modulecore.SessionID]*sessionProtocolAdmissionState)}
}

// NewSessionProtocolAdmissionWithReservationLookup also rejects legacy
// admissions for sessions reserved by the response-first owner but not yet
// pinned by EventStore.
func NewSessionProtocolAdmissionWithReservationLookup(lookup SessionRuntimeVersionLookup, reservationLookup SessionV2ReservationLookup) *SessionProtocolAdmission {
	return &SessionProtocolAdmission{
		lookup: lookup, reservationLookup: reservationLookup,
		states: make(map[modulecore.SessionID]*sessionProtocolAdmissionState),
	}
}

func (a *SessionProtocolAdmission) available() bool {
	return a != nil && a.lookup != nil && a.states != nil
}

// AdmitLegacy checks the durable session version and registers one accepted
// legacy operation. The release callback must run only after the operation
// and its writes have completed.
func (a *SessionProtocolAdmission) AdmitLegacy(ctx context.Context, sessionID modulecore.SessionID) (func(), error) {
	if !a.available() || ctx == nil || sessionID.Validate() != nil {
		return nil, ErrSessionProtocolUnavailable
	}
	state := a.acquireState(sessionID)
	state.mu.Lock()
	if err := ctx.Err(); err != nil {
		state.mu.Unlock()
		a.releaseState(sessionID, state)
		return nil, ErrSessionProtocolUnavailable
	}
	version, found, err := a.lookup(ctx, sessionID)
	if err != nil || (found && version != sessionRuntimeVersionLegacy && version != sessionRuntimeVersionV2) {
		state.mu.Unlock()
		a.releaseState(sessionID, state)
		return nil, ErrSessionProtocolUnavailable
	}
	if found && version == sessionRuntimeVersionV2 {
		state.mu.Unlock()
		a.releaseState(sessionID, state)
		return nil, ErrSessionProtocolConflict
	}
	if !found && a.reservationLookup != nil {
		reserved, err := a.reservationLookup(ctx, sessionID)
		if err != nil {
			state.mu.Unlock()
			a.releaseState(sessionID, state)
			return nil, ErrSessionProtocolUnavailable
		}
		if reserved {
			state.mu.Unlock()
			a.releaseState(sessionID, state)
			return nil, ErrSessionProtocolConflict
		}
	}
	state.activeLegacy++
	state.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			state.mu.Lock()
			state.activeLegacy--
			state.mu.Unlock()
			a.releaseState(sessionID, state)
		})
	}, nil
}

// AdmitV2 returns while holding the per-session lock. The caller must keep the
// lock through the durable Accept call so no legacy request can enter between
// the version check and the persisted v2 pin.
func (a *SessionProtocolAdmission) AdmitV2(ctx context.Context, sessionID modulecore.SessionID) (func(), error) {
	if !a.available() || ctx == nil || sessionID.Validate() != nil {
		return nil, ErrSessionProtocolUnavailable
	}
	state := a.acquireState(sessionID)
	state.mu.Lock()
	if err := ctx.Err(); err != nil {
		state.mu.Unlock()
		a.releaseState(sessionID, state)
		return nil, ErrSessionProtocolUnavailable
	}
	if state.activeLegacy > 0 {
		state.mu.Unlock()
		a.releaseState(sessionID, state)
		return nil, ErrSessionProtocolConflict
	}
	version, found, err := a.lookup(ctx, sessionID)
	if err != nil || (found && version != sessionRuntimeVersionLegacy && version != sessionRuntimeVersionV2) {
		state.mu.Unlock()
		a.releaseState(sessionID, state)
		return nil, ErrSessionProtocolUnavailable
	}
	if found && version == sessionRuntimeVersionLegacy {
		state.mu.Unlock()
		a.releaseState(sessionID, state)
		return nil, ErrSessionProtocolConflict
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			state.mu.Unlock()
			a.releaseState(sessionID, state)
		})
	}, nil
}

func (a *SessionProtocolAdmission) acquireState(sessionID modulecore.SessionID) *sessionProtocolAdmissionState {
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.states[sessionID]
	if state == nil {
		state = &sessionProtocolAdmissionState{}
		a.states[sessionID] = state
	}
	state.refs++
	return state
}

func (a *SessionProtocolAdmission) releaseState(sessionID modulecore.SessionID, state *sessionProtocolAdmissionState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	state.refs--
	if state.refs == 0 && a.states[sessionID] == state {
		delete(a.states, sessionID)
	}
}

// V2SendRequest is the trusted ingress value supplied to the durable turn
// acceptor. ActorID is assigned by the server constructor, never by the body.
type V2SendRequest struct {
	SessionID          modulecore.SessionID
	Message            string
	ClientSubmissionID string
	To                 modulechat.ViewerRecipient
	ReplyToMessageID   modulecore.MessageID
	TraceID            modulecore.TraceID
	TurnID             modulecore.TurnID
	InputMessageID     modulecore.MessageID
	ActorID            string
}

// V2AcceptedTurn is the durable acceptance receipt returned by the owner.
// IdempotentReplay tells this process not to schedule foreground work twice.
type V2AcceptedTurn struct {
	SessionID          modulecore.SessionID
	TurnID             modulecore.TurnID
	TraceID            modulecore.TraceID
	InputMessageID     modulecore.MessageID
	ClientSubmissionID string
	ActorID            string
	IdempotentReplay   bool
	foregroundPin      ForegroundPinLease
}

type V2TurnAcceptor func(context.Context, V2SendRequest) (V2AcceptedTurn, error)
type V2TurnProcessor func(context.Context, V2AcceptedTurn) error
type V2ProcessingErrorHandler func(context.Context, V2AcceptedTurn, error)

// V2ProcessingLifecycle gives detached response-first work a process-owned
// context and tracks ingress plus foreground processing. HTTP request
// cancellation is deliberately not used after acceptance.
type V2ProcessingLifecycle struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	closing bool
	active  uint64
	done    chan struct{}
	doneSet bool
}

func NewV2ProcessingLifecycle(parent context.Context) *V2ProcessingLifecycle {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &V2ProcessingLifecycle{ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

// Start registers one request. The returned context belongs to the process,
// not to the HTTP request. A false result means shutdown has begun.
func (l *V2ProcessingLifecycle) Start() (context.Context, bool) {
	if l == nil {
		return context.Background(), true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closing {
		return nil, false
	}
	l.active++
	return l.ctx, true
}

// Done releases the request's one lifecycle token after synchronous ingress
// or detached foreground processing has completed.
func (l *V2ProcessingLifecycle) Done() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active == 0 {
		return
	}
	l.active--
	if l.active == 0 && l.closing && !l.doneSet {
		close(l.done)
		l.doneSet = true
	}
}

// CloseAdmission rejects new requests without canceling accepted foreground
// processing. Existing work can continue while a diagnostic drain waits.
func (l *V2ProcessingLifecycle) CloseAdmission() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.closing = true
	if l.active == 0 && !l.doneSet {
		close(l.done)
		l.doneSet = true
	}
	l.mu.Unlock()
}

// Wait blocks until all admitted foreground work calls Done or ctx expires.
// Call CloseAdmission first when the caller needs a stable drain boundary.
func (l *V2ProcessingLifecycle) Wait(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	l.mu.Lock()
	done := l.done
	l.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Shutdown rejects new requests, cancels process-owned work, and waits only
// until ctx expires. Diagnostic drains use CloseAdmission and Wait instead.
func (l *V2ProcessingLifecycle) Shutdown(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.CloseAdmission()
	l.cancel()
	return l.Wait(ctx)
}

// V2SendOptions enables the v2 request path when all durable and processing
// dependencies are supplied. LegacyPreSessionAdmission optionally adds a
// legacy/v1 RAM admission step on the same route.
type V2SendOptions struct {
	Enabled                   bool
	ActorID                   string
	ResolveSessionID          SessionIDResolver
	Admission                 *SessionProtocolAdmission
	LegacyPreSessionAdmission LegacyPreSessionAdmissionHook
	Accept                    V2TurnAcceptor
	Process                   V2TurnProcessor
	OnError                   V2ProcessingErrorHandler
	Timing                    *turntiming.BatchRecorder
	Processing                *V2ProcessingLifecycle
	ForegroundPin             ForegroundPinHook
}

// LegacyPreSessionAdmissionIdentity contains the canonical correlation IDs
// allocated once before legacy/v1 admission and session resolution.
type LegacyPreSessionAdmissionIdentity struct {
	TraceID         modulecore.TraceID
	TurnID          modulecore.TurnID
	RootTaskID      modulecore.TaskID
	Recipient       modulechat.ViewerRecipient
	OperationSource string
}

// legacyRAMAdmissionScope selects only the combined RenCrow_CMD -> Mio CHAT
// surface. OperationSource is observation metadata, never an authentication or
// authorization claim; the canonical upper route still owns those checks.
func legacyRAMAdmissionScope(recipient modulechat.ViewerRecipient, operationSource string) bool {
	return recipient == modulechat.ViewerRecipient("mio") && operationSource == "RenCrow_CMD"
}

// LegacyPreSessionAdmissionHook admits one legacy/v1 Viewer turn before its
// canonical session is resolved. The returned context is inspected only for
// the trusted RAM persistence admission capability; request cancellation and
// all other context values are discarded before downstream processing.
type LegacyPreSessionAdmissionHook func(context.Context, LegacyPreSessionAdmissionIdentity) (context.Context, func(), error)

func admitLegacyPreSession(requestContext context.Context, identity LegacyPreSessionAdmissionIdentity, hook LegacyPreSessionAdmissionHook) (context.Context, func(), error) {
	returnedContext, release, err := hook(requestContext, identity)
	var admission *persistencequeue.Admission
	if returnedContext != nil {
		admission, _ = persistencequeue.AdmissionFromContext(returnedContext)
	}
	releaseAdmission := releaseLegacyRAMAdmission(release, admission)
	if err != nil {
		return nil, releaseAdmission, err
	}
	if admission == nil {
		return nil, releaseAdmission, errLegacyPreSessionAdmission
	}
	ownerScope, hasOwnerScope, scopeErr := validatedViewerOwnerScope(requestContext)
	if scopeErr != nil {
		releaseAdmission()
		return nil, releaseAdmission, fmt.Errorf("validate Viewer owner scope for session resolution: %w", scopeErr)
	}

	// Keep the worker independent of request cancellation and copy only the
	// explicit persistence capabilities supplied by the trusted runtime hook.
	// The validated HTTP User scope is the one additional value needed by the
	// pre-session resolver; arbitrary request and hook context values stay on
	// the synchronous ingress.
	detachedContext := persistencequeue.WithAdmission(context.Background(), admission)
	if hasOwnerScope {
		detachedContext = domaintool.WithToolExecutionScope(detachedContext, ownerScope)
	}
	if fallback, ok := persistencequeue.SynchronousFallbackFromContext(returnedContext); ok {
		detachedContext = persistencequeue.WithSynchronousFallback(detachedContext, fallback)
	}
	return detachedContext, releaseAdmission, nil
}

func releaseLegacyRAMAdmission(release func(), admission *persistencequeue.Admission) func() {
	if release == nil && admission == nil {
		return nil
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			if admission != nil {
				defer func() {
					if err := admission.Release(); err != nil {
						log.Printf("[Viewer] HandleSend: RAM admission release failed error_type=%T", err)
					}
				}()
			}
			if release != nil {
				release()
			}
		})
	}
}

// HandleSendWithProtocolV2 keeps the legacy handler for unversioned and v1
// requests while adding an optional durable v2 ingress on the same route.
func HandleSendWithProtocolV2(handler MessageHandler, onError MessageErrorHandler, saver AttachmentSaver, resolveSessionID SessionIDResolver, cancellations *SpeechCancellationRegistry, options *V2SendOptions) http.HandlerFunc {
	var admission *SessionProtocolAdmission
	var preSessionAdmission LegacyPreSessionAdmissionHook
	var foregroundPinHook ForegroundPinHook
	if options != nil {
		admission = options.Admission
		preSessionAdmission = options.LegacyPreSessionAdmission
		foregroundPinHook = options.ForegroundPin
	}
	legacy := handleSendLegacyWithForegroundPin(handler, onError, saver, resolveSessionID, cancellations, admission, preSessionAdmission, foregroundPinHook)
	return dispatchViewerSendProtocol(legacy, options)
}

func dispatchViewerSendProtocol(legacy http.Handler, options *V2SendOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			legacy.ServeHTTP(w, r)
			return
		}

		body, multipartBody, err := captureViewerSendBody(r)
		if err != nil {
			if errors.Is(err, errViewerSendBodyTooLarge) {
				version, specified, invalid := viewerSendProtocolVersion(r, body, multipartBody)
				// The bounded body may end inside a v2 string after its protocol
				// field was already parsed. The size limit is then the useful
				// response, while malformed non-v2 protocol fields remain 400s.
				if invalid && !(specified && version == ViewerProtocolVersionV2) {
					http.Error(w, "invalid protocol_version", http.StatusBadRequest)
					return
				}
				if specified && version == ViewerProtocolVersionV2 {
					if !options.available() {
						http.Error(w, "viewer v2 unavailable", http.StatusServiceUnavailable)
						return
					}
					recordRejectedViewerV2Ingress(options, errViewerSendBodyTooLarge)
					http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
					return
				}
				if specified && version != 1 {
					http.Error(w, "unsupported protocol_version", http.StatusBadRequest)
					return
				}
				legacy.ServeHTTP(w, r)
				return
			}
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		version, specified, invalid := viewerSendProtocolVersion(r, body, multipartBody)
		if invalid {
			http.Error(w, "invalid protocol_version", http.StatusBadRequest)
			return
		}
		if !specified || version == 1 {
			legacy.ServeHTTP(w, r)
			return
		}
		if version != ViewerProtocolVersionV2 {
			http.Error(w, "unsupported protocol_version", http.StatusBadRequest)
			return
		}
		if !options.available() {
			http.Error(w, "viewer v2 unavailable", http.StatusServiceUnavailable)
			return
		}
		handleViewerSendV2(w, r, body, multipartBody, options)
	}
}

func recordRejectedViewerV2Ingress(options *V2SendOptions, err error) {
	if options.Processing != nil {
		if _, started := options.Processing.Start(); !started {
			return
		}
		defer options.Processing.Done()
	}
	if options.Timing == nil {
		return
	}
	turn, reserveErr := options.Timing.BeginTurn()
	if reserveErr != nil {
		return
	}
	turnID, traceID := modulecore.NewTurnID(), modulecore.NewTraceID()
	turn.SetIdentity(string(turnID), string(traceID))
	turn.Mark(turntiming.PointV2Ingress, turntiming.ComponentV2ViewerHandler, 1, 0)
	markViewerV2TimingError(turn, true, turntiming.PointV2Ingress, err)
	turn.Mark(turntiming.PointV2ForegroundComplete, turntiming.ComponentV2ViewerHandler, 1, 0)
	turn.FinishForeground()
}

var errViewerSendBodyTooLarge = errors.New("viewer send body too large")

func captureViewerSendBody(r *http.Request) ([]byte, bool, error) {
	contentType, _, contentTypeErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if contentTypeErr == nil && contentType == "multipart/form-data" {
		if err := r.ParseMultipartForm(domainattachment.DefaultLimits.MaxTotalBytes + (1 << 20)); err != nil {
			return nil, true, err
		}
		return nil, true, nil
	}
	if r.Body == nil {
		return nil, false, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, viewerV2MaxRequestBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(body) > viewerV2MaxRequestBytes {
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
		return body, false, errViewerSendBodyTooLarge
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, false, nil
}

func viewerSendProtocolVersion(r *http.Request, body []byte, multipartBody bool) (int, bool, bool) {
	if multipartBody {
		values := r.MultipartForm.Value["protocol_version"]
		if len(values) == 0 {
			return 0, false, false
		}
		if len(values) != 1 {
			for _, value := range values {
				if value == "2" {
					return 2, true, false
				}
			}
			return 0, true, true
		}
		version, err := parseProtocolVersionNumber([]byte(values[0]))
		return version, true, err != nil
	}
	return scanJSONViewerProtocolVersion(body)
}

func scanJSONViewerProtocolVersion(body []byte) (int, bool, bool) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	first, err := decoder.Token()
	if err != nil {
		return 0, false, false
	}
	if delimiter, ok := first.(json.Delim); !ok || delimiter != '{' {
		return 0, false, false
	}
	version := 0
	found := false
	seen := false
	sawV2 := false
	invalid := false
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return version, found || sawV2, found || sawV2
		}
		key, ok := keyToken.(string)
		if !ok {
			return version, found || sawV2, found || sawV2
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return version, found || sawV2, found || sawV2
		}
		if key != "protocol_version" {
			continue
		}
		found = true
		if seen {
			invalid = true
		}
		seen = true
		parsed, err := parseProtocolVersionNumber(raw)
		if err != nil {
			invalid = true
			continue
		}
		if parsed == ViewerProtocolVersionV2 {
			sawV2 = true
		}
		if version == 0 {
			version = parsed
		} else if version != parsed {
			invalid = true
		}
	}
	if _, err := decoder.Token(); err != nil {
		return version, found || sawV2, found || sawV2
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return version, found || sawV2, found || sawV2
	}
	if sawV2 {
		return ViewerProtocolVersionV2, true, false
	}
	return version, found, invalid
}

func parseProtocolVersionNumber(raw []byte) (int, error) {
	var version int
	if err := json.Unmarshal(raw, &version); err != nil {
		return 0, err
	}
	return version, nil
}

func (options *V2SendOptions) available() bool {
	return options != nil && options.Enabled && validTrustedActorID(options.ActorID) && options.ResolveSessionID != nil && options.Admission.available() && options.Accept != nil && options.Process != nil
}

func validTrustedActorID(actorID string) bool {
	return actorID != "" && actorID == strings.TrimSpace(actorID) && validOptionalV2Text(actorID, 255)
}

type viewerV2Body struct {
	ClientSubmissionID string
	Message            string
	To                 string
	ReplyToMessageID   modulecore.MessageID
	ViewerClientID     string
	InputSource        string
	UserID             string
	DeviceName         string
	AudioOutput        string
	Presence           *ForegroundPresenceReference
}

func handleViewerSendV2(w http.ResponseWriter, r *http.Request, body []byte, multipartBody bool, options *V2SendOptions) {
	ownerScope, hasOwnerScope, scopeErr := validatedViewerOwnerScope(r.Context())
	if scopeErr != nil {
		http.Error(w, "owner scope unavailable", http.StatusServiceUnavailable)
		return
	}
	processCtx := context.Background()
	tracked := false
	if options.Processing != nil {
		var started bool
		processCtx, started = options.Processing.Start()
		if !started {
			http.Error(w, "viewer v2 is shutting down", http.StatusServiceUnavailable)
			return
		}
		tracked = true
	}
	if hasOwnerScope {
		processCtx = domaintool.WithToolExecutionScope(processCtx, ownerScope)
	}
	var turn turntiming.TurnRecorder
	var timingEnabled bool
	var traceID modulecore.TraceID
	var turnID modulecore.TurnID
	var inputMessageID modulecore.MessageID
	if options.Timing != nil {
		if reserved, err := options.Timing.BeginTurn(); err == nil {
			turn, timingEnabled = reserved, true
			turn.Mark(turntiming.PointV2Ingress, turntiming.ComponentV2ViewerHandler, 1, 0)
			traceID = modulecore.NewTraceID()
			turnID = modulecore.NewTurnID()
			inputMessageID = modulecore.NewMessageID()
			turn.SetIdentity(string(turnID), string(traceID))
			processCtx = turntiming.WithBatchTurnRecorder(processCtx, turn)
		}
	}
	asyncOwnsLifecycle := false
	defer func() {
		if !asyncOwnsLifecycle {
			if timingEnabled {
				turn.Mark(turntiming.PointV2ForegroundComplete, turntiming.ComponentV2ViewerHandler, 1, 0)
				turn.FinishForeground()
			}
			if tracked {
				options.Processing.Done()
			}
		}
	}()
	if multipartBody {
		markViewerV2TimingError(turn, timingEnabled, turntiming.PointV2Ingress, errors.New("multipart v2 is unsupported"))
		http.Error(w, "v2 requires a JSON text request", http.StatusBadRequest)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if (err != nil && r.Header.Get("Content-Type") != "") || (mediaType != "" && mediaType != "application/json") {
		markViewerV2TimingError(turn, timingEnabled, turntiming.PointV2Ingress, errors.New("unsupported v2 content type"))
		http.Error(w, "v2 requires application/json", http.StatusBadRequest)
		return
	}
	request, err := decodeViewerV2Body(body)
	if err != nil {
		markViewerV2TimingError(turn, timingEnabled, turntiming.PointV2Ingress, err)
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if !validClientSubmissionID(request.ClientSubmissionID) || !validViewerV2Message(request.Message) {
		markViewerV2TimingError(turn, timingEnabled, turntiming.PointV2Ingress, errors.New("invalid v2 request fields"))
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if (request.InputSource != "" && request.InputSource != "text") || (request.AudioOutput != "" && request.AudioOutput != "disabled") {
		markViewerV2TimingError(turn, timingEnabled, turntiming.PointV2Ingress, errors.New("unsupported v2 input mode"))
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	recipient, err := modulechat.NormalizeViewerRecipient(request.To)
	if err != nil || recipient != modulechat.ViewerRecipientMio {
		markViewerV2TimingError(turn, timingEnabled, turntiming.PointV2Ingress, errors.New("invalid v2 recipient"))
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if !validOptionalV2Text(request.ViewerClientID, 255) || !validOptionalV2Text(request.UserID, 255) || !validOptionalV2Text(request.DeviceName, 255) {
		markViewerV2TimingError(turn, timingEnabled, turntiming.PointV2Ingress, errors.New("invalid v2 metadata"))
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	foregroundPin, err := acquireViewerForegroundPin(r, options.ForegroundPin, ForegroundPinRequest{
		ViewerClientID: request.ViewerClientID,
		Presence:       request.Presence,
	})
	if err != nil {
		markViewerV2TimingError(turn, timingEnabled, turntiming.PointV2Ingress, err)
		writeViewerForegroundPinError(w, err)
		return
	}
	foregroundPinTransferred := false
	defer func() {
		if !foregroundPinTransferred {
			releaseViewerForegroundPin(foregroundPin)
		}
	}()

	acceptCtx := r.Context()
	if timingEnabled {
		acceptCtx = turntiming.WithBatchTurnRecorder(acceptCtx, turn)
	}
	sessionID, err := options.ResolveSessionID(acceptCtx)
	if err != nil || sessionID.Validate() != nil {
		if err == nil {
			err = errors.New("canonical session id is invalid")
		}
		markViewerV2TimingError(turn, timingEnabled, turntiming.PointV2Accept, err)
		log.Printf("[Viewer] HandleSend v2: canonical session unavailable")
		http.Error(w, "session unavailable", http.StatusServiceUnavailable)
		return
	}
	if turnID == "" {
		traceID = modulecore.NewTraceID()
		turnID = modulecore.NewTurnID()
		inputMessageID = modulecore.NewMessageID()
	}
	acceptRequest := V2SendRequest{
		SessionID:          sessionID,
		Message:            request.Message,
		ClientSubmissionID: request.ClientSubmissionID,
		To:                 recipient,
		ReplyToMessageID:   request.ReplyToMessageID,
		TraceID:            traceID,
		TurnID:             turnID,
		InputMessageID:     inputMessageID,
		ActorID:            options.ActorID,
	}
	acceptSpan, acceptSpanActive := turntiming.Span{}, false
	if timingEnabled {
		acceptSpan, acceptSpanActive = turn.Begin(turntiming.PointV2Accept, turntiming.ComponentV2ViewerHandler, 1, 0, turntiming.Span{})
	}
	releaseAdmission, admissionErr := options.Admission.AdmitV2(acceptCtx, sessionID)
	if admissionErr != nil {
		if timingEnabled && acceptSpanActive {
			turn.End(acceptSpan, viewerV2TimingResult(admissionErr))
		}
		markViewerV2TimingError(turn, timingEnabled, turntiming.PointV2Accept, admissionErr)
		if errors.Is(admissionErr, ErrSessionProtocolConflict) {
			http.Error(w, "session protocol conflict", http.StatusConflict)
		} else {
			http.Error(w, "session protocol state unavailable", http.StatusServiceUnavailable)
		}
		return
	}
	defer releaseAdmission()
	accepted, err := options.Accept(acceptCtx, acceptRequest)
	releaseAdmission()
	if timingEnabled && acceptSpanActive {
		turn.End(acceptSpan, viewerV2TimingResult(err))
	}
	if err != nil {
		markViewerV2TimingError(turn, timingEnabled, turntiming.PointV2Accept, err)
		if errors.Is(err, ErrV2SubmissionConflict) {
			http.Error(w, "submission conflict", http.StatusConflict)
			return
		}
		log.Printf("[Viewer] HandleSend v2: durable acceptance failed error_type=%T", err)
		http.Error(w, "turn acceptance unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := validateV2AcceptedTurn(accepted, acceptRequest); err != nil {
		markViewerV2TimingError(turn, timingEnabled, turntiming.PointV2Accept, err)
		log.Printf("[Viewer] HandleSend v2: durable acceptance returned invalid identity")
		http.Error(w, "turn acceptance unavailable", http.StatusServiceUnavailable)
		return
	}
	if timingEnabled {
		turn.Mark(turntiming.PointV2Accepted, turntiming.ComponentV2ViewerHandler, 1, 0)
	}
	log.Printf("[Viewer] HandleSend v2: accepted session_id=%s turn_id=%s trace_id=%s input_message_id=%s replay=%t",
		accepted.SessionID, accepted.TurnID, accepted.TraceID, accepted.InputMessageID, accepted.IdempotentReplay)

	if !accepted.IdempotentReplay {
		accepted.foregroundPin = foregroundPin
		foregroundPinTransferred = true
		asyncOwnsLifecycle = true
		go runAcceptedViewerV2Turn(options, accepted, processCtx, turn, timingEnabled, tracked)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	response := struct {
		Status             string `json:"status"`
		SessionID          string `json:"session_id"`
		TurnID             string `json:"turn_id"`
		TraceID            string `json:"trace_id"`
		InputMessageID     string `json:"input_message_id"`
		ClientSubmissionID string `json:"client_submission_id"`
	}{
		Status: "accepted", SessionID: string(accepted.SessionID), TurnID: string(accepted.TurnID),
		TraceID: string(accepted.TraceID), InputMessageID: string(accepted.InputMessageID), ClientSubmissionID: accepted.ClientSubmissionID,
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("[Viewer] HandleSend v2: acceptance response encode failed")
	}
}

func runAcceptedViewerV2Turn(options *V2SendOptions, accepted V2AcceptedTurn, ctx context.Context, turn turntiming.TurnRecorder, timingEnabled, tracked bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	defer func() {
		if timingEnabled {
			turn.Mark(turntiming.PointV2ForegroundComplete, turntiming.ComponentV2ViewerHandler, 1, 0)
			turn.FinishForeground()
		}
		if tracked {
			options.Processing.Done()
		}
	}()
	defer releaseViewerForegroundPin(accepted.foregroundPin)
	if timingEnabled {
		turn.Mark(turntiming.PointV2AsyncStart, turntiming.ComponentV2ViewerHandler, 1, 0)
	}
	if err := options.Process(ctx, accepted); err != nil {
		if timingEnabled {
			turn.MarkResult(turntiming.PointV2Error, turntiming.ComponentV2ViewerHandler, 1, uint32(turntiming.PointV2AsyncStart), viewerV2TimingResult(err))
		}
		log.Printf("[Viewer] HandleSend v2: async error session_id=%s turn_id=%s trace_id=%s input_message_id=%s error_type=%T",
			accepted.SessionID, accepted.TurnID, accepted.TraceID, accepted.InputMessageID, err)
		if options.OnError != nil {
			options.OnError(ctx, accepted, err)
		}
	}
}

func viewerV2TimingResult(err error) turntiming.Result {
	switch {
	case err == nil:
		return turntiming.ResultOK
	case errors.Is(err, context.Canceled):
		return turntiming.ResultCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return turntiming.ResultTimeout
	default:
		return turntiming.ResultError
	}
}

func markViewerV2TimingError(turn turntiming.TurnRecorder, enabled bool, failedPoint uint16, err error) {
	if enabled && err != nil {
		turn.MarkResult(turntiming.PointV2Error, turntiming.ComponentV2ViewerHandler, 1, uint32(failedPoint), viewerV2TimingResult(err))
	}
}

func decodeViewerV2Body(body []byte) (viewerV2Body, error) {
	if len(body) == 0 || len(body) > viewerV2MaxRequestBytes {
		return viewerV2Body{}, errors.New("invalid v2 body size")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	first, err := decoder.Token()
	if err != nil {
		return viewerV2Body{}, err
	}
	if delimiter, ok := first.(json.Delim); !ok || delimiter != '{' {
		return viewerV2Body{}, errors.New("v2 request must be a JSON object")
	}
	allowed := map[string]struct{}{
		"protocol_version": {}, "message": {}, "client_submission_id": {}, "to": {},
		"reply_to_message_id": {}, "viewer_client_id": {}, "input_source": {}, "user_id": {}, "device_name": {}, "audio_output": {}, "presence": {},
	}
	values := make(map[string]json.RawMessage, len(allowed))
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return viewerV2Body{}, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return viewerV2Body{}, errors.New("v2 field name must be a string")
		}
		if _, ok := allowed[key]; !ok {
			return viewerV2Body{}, fmt.Errorf("unknown v2 field %q", key)
		}
		if _, exists := values[key]; exists {
			return viewerV2Body{}, fmt.Errorf("duplicate v2 field %q", key)
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return viewerV2Body{}, err
		}
		values[key] = raw
	}
	if _, err := decoder.Token(); err != nil {
		return viewerV2Body{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return viewerV2Body{}, errors.New("unexpected trailing JSON data")
	}
	var version int
	if raw := values["protocol_version"]; len(raw) == 0 || json.Unmarshal(raw, &version) != nil || version != ViewerProtocolVersionV2 {
		return viewerV2Body{}, errors.New("protocol_version must be 2")
	}
	request := viewerV2Body{}
	for key, destination := range map[string]*string{
		"message": &request.Message, "client_submission_id": &request.ClientSubmissionID,
		"to": &request.To, "viewer_client_id": &request.ViewerClientID, "input_source": &request.InputSource,
		"user_id": &request.UserID, "device_name": &request.DeviceName, "audio_output": &request.AudioOutput,
	} {
		raw, exists := values[key]
		if !exists {
			continue
		}
		if err := json.Unmarshal(raw, destination); err != nil {
			return viewerV2Body{}, fmt.Errorf("v2 field %q must be a string", key)
		}
	}
	if raw, exists := values["reply_to_message_id"]; exists {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return viewerV2Body{}, errors.New(`v2 field "reply_to_message_id" must be a canonical message ID`)
		}
		request.ReplyToMessageID = modulecore.MessageID(value)
		if request.ReplyToMessageID.Validate() != nil {
			return viewerV2Body{}, errors.New(`v2 field "reply_to_message_id" must be a canonical message ID`)
		}
	}
	if raw, exists := values["presence"]; exists {
		request.Presence, err = parseForegroundPresenceReference(raw)
		if err != nil {
			return viewerV2Body{}, fmt.Errorf("v2 presence reference: %w", err)
		}
	}
	return request, nil
}

func validClientSubmissionID(value string) bool {
	return value != "" && len([]byte(value)) <= viewerV2MaxClientSubmissionByte && value == strings.TrimSpace(value) && validOptionalV2Text(value, viewerV2MaxClientSubmissionByte)
}

func validViewerV2Message(value string) bool {
	if strings.TrimSpace(value) == "" || len([]byte(value)) > viewerV2MaxMessageBytes || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) && character != '\n' && character != '\r' && character != '\t' {
			return false
		}
	}
	return true
}

func validOptionalV2Text(value string, maxBytes int) bool {
	if len([]byte(value)) > maxBytes || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validateV2AcceptedTurn(accepted V2AcceptedTurn, request V2SendRequest) error {
	if accepted.SessionID != request.SessionID || accepted.SessionID.Validate() != nil ||
		accepted.TurnID.Validate() != nil || accepted.TraceID.Validate() != nil || accepted.InputMessageID.Validate() != nil ||
		accepted.ClientSubmissionID != request.ClientSubmissionID || accepted.ActorID != request.ActorID ||
		accepted.TurnID == modulecore.TurnID(accepted.TraceID) {
		return errors.New("accepted v2 turn identity mismatch")
	}
	return nil
}
