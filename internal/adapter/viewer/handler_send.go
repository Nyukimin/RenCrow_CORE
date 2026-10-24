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
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"

	appattachment "github.com/Nyukimin/RenCrow_CORE/internal/application/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainattachment "github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/security"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type MessageHandler func(ctx context.Context, req SendRequest) (string, error)
type MessageErrorHandler func(ctx context.Context, req SendRequest, err error)
type SessionIDResolver func(ctx context.Context) (modulecore.SessionID, error)
type SessionPublicationBindingResolver func(ctx context.Context) (domainsession.CanonicalSessionPublicationBinding, error)

const (
	viewerCMDClient          = "RenCrow_CMD"
	viewerCMDChatProfile     = "cmd-chat"
	viewerSendMaxAuthBody    = 128 << 10
	viewerSendOwnerAuthError = "viewer send authorization unavailable"
)

// WithCMDMioOwnerAuthentication validates the configured owner credential at
// Viewer ingress. It protects Mio sends and the matching CMD chat event stream;
// other clients continue with their existing public scope.
func WithCMDMioOwnerAuthentication(next http.Handler, ownerID string, token []byte) http.HandlerFunc {
	ownerID = strings.TrimSpace(ownerID)
	token = append([]byte(nil), token...)
	return func(w http.ResponseWriter, r *http.Request) {
		authorization := r.Header.Values("Authorization")
		if r.Method == http.MethodGet {
			if len(authorization) == 0 && !viewerSendHasCMDMarker(r) {
				next.ServeHTTP(w, r)
				return
			}
			if !security.IsDirectLoopbackRequest(r) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			client, clientOK := exactlyOneMemoryOwnerHeader(r.Header, "X-RenCrow-Client")
			profile, profileOK := exactlyOneMemoryOwnerHeader(r.Header, "X-RenCrow-Interaction-Profile")
			if !clientOK || !profileOK || client != viewerCMDClient || profile != viewerCMDChatProfile {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			recipient, recipientErr := viewerEventsRecipientForAuth(r)
			if recipientErr != nil {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			if recipient != modulechat.ViewerRecipientMio {
				if len(authorization) > 0 {
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			if ownerID == "" || len(token) == 0 {
				http.Error(w, viewerSendOwnerAuthError, http.StatusServiceUnavailable)
				return
			}
			if len(authorization) != 1 || !memoryOwnerBearerAuthorized(r, token) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			r = withViewerAuthenticatedOwner(r, ownerID)
			if r == nil {
				http.Error(w, viewerSendOwnerAuthError, http.StatusServiceUnavailable)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if len(authorization) > 0 && !security.IsDirectLoopbackRequest(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodPost {
			if len(authorization) > 0 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		recipient, recipientErr := viewerSendRecipientForAuth(r)
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		cmdMarker := viewerSendHasCMDMarker(r)
		if recipientErr != nil {
			if len(authorization) > 0 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if cmdMarker {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		client, clientOK := exactlyOneMemoryOwnerHeader(r.Header, "X-RenCrow-Client")
		profile, profileOK := exactlyOneMemoryOwnerHeader(r.Header, "X-RenCrow-Interaction-Profile")
		cmdContract := clientOK && profileOK && client == viewerCMDClient && profile == viewerCMDChatProfile
		requiresOwnerAuth := recipient == modulechat.ViewerRecipientMio && cmdMarker
		if len(authorization) > 0 && (!cmdContract || recipient != modulechat.ViewerRecipientMio) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if requiresOwnerAuth && !cmdContract {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if !requiresOwnerAuth {
			next.ServeHTTP(w, r)
			return
		}
		if ownerID == "" || len(token) == 0 {
			http.Error(w, viewerSendOwnerAuthError, http.StatusServiceUnavailable)
			return
		}
		if len(authorization) != 1 || !memoryOwnerBearerAuthorized(r, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r = withViewerAuthenticatedOwner(r, ownerID)
		if r == nil {
			http.Error(w, viewerSendOwnerAuthError, http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	}
}

func withViewerAuthenticatedOwner(r *http.Request, ownerID string) *http.Request {
	scope, err := domaintool.NewToolExecutionScope(
		string(modulecore.NewRequestID()), domaintool.ActorKindUser, ownerID, ownerID,
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		return nil
	}
	return r.WithContext(domaintool.WithToolExecutionScope(r.Context(), scope))
}

func viewerEventsRecipientForAuth(r *http.Request) (modulechat.ViewerRecipient, error) {
	values, found := r.URL.Query()["to"]
	if found && len(values) != 1 {
		return "", errors.New("duplicate Viewer event recipient")
	}
	rawRecipient := ""
	if len(values) == 1 {
		rawRecipient = values[0]
	}
	return modulechat.NormalizeViewerRecipient(rawRecipient)
}

func viewerSendHasCMDMarker(r *http.Request) bool {
	for _, value := range r.Header.Values("X-RenCrow-Client") {
		if strings.TrimSpace(value) == viewerCMDClient {
			return true
		}
	}
	for _, value := range r.Header.Values("X-RenCrow-Interaction-Profile") {
		if strings.TrimSpace(value) == viewerCMDChatProfile {
			return true
		}
	}
	return false
}

func viewerSendRecipientForAuth(r *http.Request) (modulechat.ViewerRecipient, error) {
	contentType := ""
	if rawContentType := strings.TrimSpace(r.Header.Get("Content-Type")); rawContentType != "" {
		parsed, _, err := mime.ParseMediaType(rawContentType)
		if err != nil {
			return "", err
		}
		contentType = parsed
	}
	rawRecipient := ""
	if contentType == "multipart/form-data" {
		if r.Body == nil {
			return "", errors.New("multipart body is required")
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			return "", err
		}
		if r.MultipartForm != nil {
			values := r.MultipartForm.Value["to"]
			if len(values) > 1 {
				return "", errors.New("duplicate viewer recipient")
			}
			if len(values) == 1 {
				rawRecipient = values[0]
			}
		}
	} else {
		var reader io.Reader = strings.NewReader("")
		if r.Body != nil {
			reader = r.Body
		}
		body, err := io.ReadAll(io.LimitReader(reader, viewerSendMaxAuthBody+1))
		if err != nil {
			r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), reader))
			return "", err
		}
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), reader))
		if len(body) > viewerSendMaxAuthBody {
			return "", errViewerSendBodyTooLarge
		}
		if len(body) > 0 {
			var request struct {
				To string `json:"to"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				return "", err
			}
			rawRecipient = request.To
		}
	}
	return modulechat.NormalizeViewerRecipient(rawRecipient)
}

func validatedViewerOwnerScope(ctx context.Context) (domaintool.ToolExecutionScope, bool, error) {
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || !scope.Allows(domaintool.DataScopeUser) {
		return domaintool.ToolExecutionScope{}, false, nil
	}
	if err := scope.Validate(); err != nil ||
		scope.ActorKind != domaintool.ActorKindUser ||
		scope.AuthenticationSource != domaintool.AuthenticationSourceHTTP ||
		scope.ActorID != scope.AuthenticatedUserID {
		return domaintool.ToolExecutionScope{}, false, errors.New("viewer owner scope is invalid")
	}
	return scope, true, nil
}

// AuthenticatedViewerOwnerID returns only a previously validated HTTP owner
// scope installed by the Viewer CMD authentication middleware.
func AuthenticatedViewerOwnerID(ctx context.Context) (string, bool, error) {
	scope, found, err := validatedViewerOwnerScope(ctx)
	if err != nil || !found {
		return "", found, err
	}
	return scope.AuthenticatedUserID, true, nil
}

// ForegroundPresenceReference carries only the lookup fields for an already
// claimed foreground lease. It is not an authentication or authorization
// credential by itself.
type ForegroundPresenceReference struct {
	ProtocolVersion     int    `json:"protocol_version"`
	RuntimeEpoch        string `json:"runtime_epoch"`
	RegistrationReceipt string `json:"registration_receipt"`
	ClaimSeq            uint64 `json:"claim_seq"`
}

// ForegroundPinRequest combines the body reference with the viewer client key.
// The runtime hook derives trusted actor and client identity from the request.
type ForegroundPinRequest struct {
	ViewerClientID string
	Presence       *ForegroundPresenceReference
}

// ForegroundPinLease is held by one accepted send until its processing ends.
type ForegroundPinLease interface {
	Release() error
}

// ForegroundPinHook validates trusted request provenance and atomically pins
// the referenced foreground lease before send admission.
type ForegroundPinHook func(*http.Request, ForegroundPinRequest) (ForegroundPinLease, error)

// ForegroundPinError is a typed, client-safe rejection from the runtime pin
// owner. It is returned synchronously before the send is accepted.
type ForegroundPinError struct {
	StatusCode int
	ErrorCode  string
	Message    string
}

func (e *ForegroundPinError) Error() string {
	if e == nil {
		return "foreground presence pin failed"
	}
	return e.Message
}

type AudioOutputIntent string

const (
	AudioOutputRequested AudioOutputIntent = "requested"
	AudioOutputDisabled  AudioOutputIntent = "disabled"
)

// AttachmentSaver persists uploaded Viewer files before they enter orchestration.

type AttachmentSaver interface {
	SaveAll(ctx context.Context, files []appattachment.IncomingFile) ([]domainattachment.Attachment, error)
}

// SendRequest is the adapter-neutral payload passed from Viewer to orchestration.

type SendRequest struct {
	SessionID          string
	PublicationBinding domainsession.CanonicalSessionPublicationBinding
	MessageID          string
	AgentMessageID     string
	TurnID             string
	TraceID            string
	RootTaskID         string
	ViewerClientID     string
	AudioOutput        AudioOutputIntent
	Provenance         RequestProvenance
	Message            string
	To                 modulechat.ViewerRecipient
	Attachments        []domainattachment.Attachment
	foregroundPin      ForegroundPinLease
}

type viewerSendRequest struct {
	ViewerClientID string                       `json:"viewer_client_id,omitempty"`
	AudioOutput    string                       `json:"audio_output,omitempty"`
	Timings        bool                         `json:"timings,omitempty"`
	InputSource    string                       `json:"input_source,omitempty"`
	UserID         string                       `json:"user_id,omitempty"`
	DeviceName     string                       `json:"device_name,omitempty"`
	Message        string                       `json:"message"`
	To             string                       `json:"to,omitempty"`
	PresenceRaw    json.RawMessage              `json:"presence,omitempty"`
	Presence       *ForegroundPresenceReference `json:"-"`
}

// HandleSend creates an HTTP handler that receives messages from the viewer input.
// onError is called with the processing error if the async handler fails (may be nil).

func HandleSend(handler MessageHandler, onError MessageErrorHandler) http.HandlerFunc {
	return HandleSendWithAttachments(handler, onError, nil)
}

// HandleSendWithAttachments receives text and optional file attachments from the Viewer.

func HandleSendWithAttachments(handler MessageHandler, onError MessageErrorHandler, saver AttachmentSaver) http.HandlerFunc {
	return HandleSendWithAttachmentsAndSessionResolver(handler, onError, saver, nil)
}

// HandleSendWithAttachmentsAndSessionResolver resolves the canonical Session
// before accepting an asynchronous Viewer turn. This keeps the accepted
// session_id identical to the Session repository owner used by orchestration.
func HandleSendWithAttachmentsAndSessionResolver(handler MessageHandler, onError MessageErrorHandler, saver AttachmentSaver, resolveSessionID SessionIDResolver) http.HandlerFunc {
	return HandleSendWithSpeechCancellation(handler, onError, saver, resolveSessionID, nil)
}

// HandleSendWithSpeechCancellation accepts asynchronous Viewer turns with a
// per-turn speech cancellation capability owned by the shared registry.
func HandleSendWithSpeechCancellation(handler MessageHandler, onError MessageErrorHandler, saver AttachmentSaver, resolveSessionID SessionIDResolver, cancellations *SpeechCancellationRegistry) http.HandlerFunc {
	return HandleSendWithProtocolV2(handler, onError, saver, resolveSessionID, cancellations, nil)
}

func handleSendLegacyWithSpeechCancellation(handler MessageHandler, onError MessageErrorHandler, saver AttachmentSaver, resolveSessionID SessionIDResolver, cancellations *SpeechCancellationRegistry) http.HandlerFunc {
	return handleSendLegacyWithSpeechCancellationAndAdmission(handler, onError, saver, resolveSessionID, cancellations, nil, nil)
}

func handleSendLegacyWithSpeechCancellationAndAdmission(handler MessageHandler, onError MessageErrorHandler, saver AttachmentSaver, resolveSessionID SessionIDResolver, cancellations *SpeechCancellationRegistry, admission *SessionProtocolAdmission, preSessionAdmission LegacyPreSessionAdmissionHook) http.HandlerFunc {
	return handleSendLegacyWithForegroundPin(handler, onError, saver, resolveSessionID, nil, cancellations, admission, preSessionAdmission, nil)
}

func handleSendLegacyWithForegroundPin(handler MessageHandler, onError MessageErrorHandler, saver AttachmentSaver, resolveSessionID SessionIDResolver, resolveSessionBinding SessionPublicationBindingResolver, cancellations *SpeechCancellationRegistry, admission *SessionProtocolAdmission, preSessionAdmission LegacyPreSessionAdmissionHook, foregroundPinHook ForegroundPinHook) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		handlerReceivedAt := time.Now()
		if r.Method != http.MethodPost {
			log.Printf("[Viewer] HandleSend: method not allowed: %s", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ownerScope, hasOwnerScope, scopeErr := validatedViewerOwnerScope(r.Context())
		if scopeErr != nil {
			http.Error(w, "owner scope unavailable", http.StatusServiceUnavailable)
			return
		}

		req, attachments, foregroundPin, err := parseViewerSendRequestAndPin(r, saver, foregroundPinHook)
		if err != nil {
			var pinErr *ForegroundPinError
			if errors.As(err, &pinErr) {
				writeViewerForegroundPinError(w, err)
				return
			}
			log.Printf("[Viewer] HandleSend: invalid request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		foregroundPinTransferred := false
		defer func() {
			if !foregroundPinTransferred {
				releaseViewerForegroundPin(foregroundPin)
			}
		}()
		var timing *turntiming.Recorder
		if req.Timings {
			timing = turntiming.New(handlerReceivedAt)
			timing.Mark(turntiming.PointRequestParsed)
		}
		endHandlerValidation := func() {
			if timing != nil {
				timing.Mark(turntiming.PointHandlerValidationEnded)
			}
		}
		if strings.TrimSpace(req.Message) == "" && len(attachments) == 0 {
			endHandlerValidation()
			log.Printf("[Viewer] HandleSend: empty message and no attachments")
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		recipient, err := modulechat.NormalizeViewerRecipient(req.To)
		if err != nil {
			endHandlerValidation()
			log.Printf("[Viewer] HandleSend: invalid recipient: %q", req.To)
			http.Error(w, "invalid recipient", http.StatusBadRequest)
			return
		}
		req.To = string(recipient)
		req.ViewerClientID = strings.TrimSpace(req.ViewerClientID)
		audioOutput, err := normalizeAudioOutputIntent(req.AudioOutput)
		if err != nil {
			endHandlerValidation()
			log.Printf("[Viewer] HandleSend: invalid audio output intent: %q", req.AudioOutput)
			http.Error(w, "invalid audio_output", http.StatusBadRequest)
			return
		}
		provenance, err := buildViewerRequestProvenance(r, req)
		if err != nil {
			endHandlerValidation()
			log.Printf("[Viewer] HandleSend: invalid request provenance: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		endHandlerValidation()
		rootTaskID := string(modulecore.NewTaskID())
		turnID := ""
		traceID := ""
		useRAMAdmission := len(attachments) == 0 &&
			preSessionAdmission != nil &&
			legacyRAMAdmissionScope(recipient, provenance.OperationSource)
		if useRAMAdmission {
			turnID = string(modulecore.NewTurnID())
			traceID = string(modulecore.NewTraceID())
		}
		resolverContext := r.Context()
		asyncBaseContext := context.Background()
		var releasePreSessionAdmission func()
		if useRAMAdmission {
			identity := LegacyPreSessionAdmissionIdentity{
				TraceID:         modulecore.TraceID(traceID),
				TurnID:          modulecore.TurnID(turnID),
				RootTaskID:      modulecore.TaskID(rootTaskID),
				Recipient:       recipient,
				OperationSource: provenance.OperationSource,
			}
			admissionContext, release, admissionErr := admitLegacyPreSession(r.Context(), identity, preSessionAdmission)
			releasePreSessionAdmission = release
			defer func() {
				if releasePreSessionAdmission != nil {
					releasePreSessionAdmission()
				}
			}()
			if admissionErr != nil {
				log.Printf("[Viewer] HandleSend: pre-session admission unavailable error_type=%T", admissionErr)
				http.Error(w, "message admission unavailable", http.StatusServiceUnavailable)
				return
			}
			resolverContext = admissionContext
			asyncBaseContext = admissionContext
		}
		if hasOwnerScope {
			asyncBaseContext = domaintool.WithToolExecutionScope(asyncBaseContext, ownerScope)
		}
		sessionID := ""
		var publicationBinding domainsession.CanonicalSessionPublicationBinding
		if resolveSessionBinding != nil {
			if timing != nil {
				timing.Mark(turntiming.PointHandlerSessionStarted)
			}
			resolved, err := resolveSessionBinding(resolverContext)
			if err != nil || resolved.Validate() != nil {
				if timing != nil {
					timing.Mark(turntiming.PointHandlerSessionEnded)
				}
				log.Printf("[Viewer] HandleSend: canonical session publication binding unavailable")
				http.Error(w, "session unavailable", http.StatusServiceUnavailable)
				return
			}
			if timing != nil {
				timing.Mark(turntiming.PointHandlerSessionEnded)
			}
			publicationBinding = resolved
			sessionID = string(resolved.SessionID())
		} else if resolveSessionID != nil {
			if timing != nil {
				timing.Mark(turntiming.PointHandlerSessionStarted)
			}
			resolved, err := resolveSessionID(resolverContext)
			if err != nil {
				if timing != nil {
					timing.Mark(turntiming.PointHandlerSessionEnded)
				}
				log.Printf("[Viewer] HandleSend: canonical session resolution failed: %v", err)
				http.Error(w, "session unavailable", http.StatusServiceUnavailable)
				return
			}
			if err := resolved.Validate(); err != nil {
				if timing != nil {
					timing.Mark(turntiming.PointHandlerSessionEnded)
				}
				log.Printf("[Viewer] HandleSend: canonical session resolver returned invalid identity: %v", err)
				http.Error(w, "session unavailable", http.StatusServiceUnavailable)
				return
			}
			if timing != nil {
				timing.Mark(turntiming.PointHandlerSessionEnded)
			}
			sessionID = string(resolved)
		}
		if admission != nil && sessionID == "" {
			log.Printf("[Viewer] HandleSend: session protocol admission requires canonical session identity")
			http.Error(w, "session unavailable", http.StatusServiceUnavailable)
			return
		}
		messageID := string(modulecore.NewMessageID())
		agentMessageID := string(modulecore.NewMessageID())
		if turnID == "" {
			turnID = string(modulecore.NewTurnID())
			traceID = string(modulecore.NewTraceID())
		}
		var speechCancellation *orchestrator.SpeechTurnCancellation
		speechCancelToken := ""
		var releaseSpeechCancellation func()
		if cancellations != nil {
			speechCancellation = orchestrator.NewSpeechTurnCancellation()
			speechCancelToken, releaseSpeechCancellation, err = cancellations.RegisterTurn(rootTaskID, turnID, speechCancellation)
			if err != nil {
				log.Printf("[Viewer] HandleSend: speech cancellation registration failed turn_id=%s: %v", turnID, err)
				http.Error(w, "speech cancellation unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		sendReq := SendRequest{
			SessionID:          sessionID,
			PublicationBinding: publicationBinding,
			MessageID:          messageID,
			AgentMessageID:     agentMessageID,
			TurnID:             turnID,
			TraceID:            traceID,
			RootTaskID:         rootTaskID,
			ViewerClientID:     req.ViewerClientID,
			AudioOutput:        audioOutput,
			Provenance:         provenance,
			To:                 recipient,
			Attachments:        attachments,
			foregroundPin:      foregroundPin,
		}

		effectiveMessage := strings.TrimSpace(req.Message)
		if strings.TrimSpace(effectiveMessage) == "" && len(attachments) > 0 {
			effectiveMessage = defaultAttachmentMessage(attachments)
		}
		sendReq.Message = effectiveMessage
		var releaseAdmission func()
		if admission != nil {
			releaseAdmission, err = admission.AdmitLegacy(resolverContext, modulecore.SessionID(sessionID))
			if err != nil {
				if releaseSpeechCancellation != nil {
					releaseSpeechCancellation()
				}
				if errors.Is(err, ErrSessionProtocolConflict) {
					http.Error(w, "session protocol conflict", http.StatusConflict)
				} else {
					http.Error(w, "session protocol state unavailable", http.StatusServiceUnavailable)
				}
				return
			}
		}
		log.Printf("[Viewer] HandleSend: accepted root_task_id=%s trace_id=%s message_id=%s viewer_client_id=%q recipient=%s attachment_count=%d message_len=%d %s",
			rootTaskID, traceID, messageID, req.ViewerClientID, recipient, len(attachments), len([]rune(effectiveMessage)), provenance.LogFields())
		log.Printf("[Viewer] HandleSend: message received: %q", req.Message)
		if timing != nil {
			timing.Mark(turntiming.PointAccepted)
		}

		// Process asynchronously — events flow back via SSE.
		asyncReleasePreSessionAdmission := releasePreSessionAdmission
		releasePreSessionAdmission = nil
		foregroundPinTransferred = true
		go func() {
			defer releaseViewerForegroundPin(foregroundPin)
			if asyncReleasePreSessionAdmission != nil {
				defer asyncReleasePreSessionAdmission()
			}
			if releaseAdmission != nil {
				defer releaseAdmission()
			}
			if releaseSpeechCancellation != nil {
				defer releaseSpeechCancellation()
			}
			ctx, cancel := context.WithTimeout(asyncBaseContext, 5*time.Minute)
			defer cancel()
			ctx = orchestrator.WithSpeechTurnCancellation(ctx, speechCancellation)
			if timing != nil {
				ctx = turntiming.WithRecorder(ctx, timing)
				timing.Mark(turntiming.PointAsyncStart)
			}
			log.Printf("[Viewer] HandleSend: async start root_task_id=%s trace_id=%s message_id=%s viewer_client_id=%q recipient=%s %s", rootTaskID, traceID, messageID, req.ViewerClientID, recipient, provenance.LogFields())
			response, err := handler(ctx, sendReq)
			if err != nil {
				log.Printf("[Viewer] HandleSend: async error root_task_id=%s trace_id=%s message_id=%s viewer_client_id=%q recipient=%s %s err=%v", rootTaskID, traceID, messageID, req.ViewerClientID, recipient, provenance.LogFields(), err)
				if onError != nil {
					onError(ctx, sendReq, err)
				}
			} else {
				log.Printf("[Viewer] HandleSend: async complete root_task_id=%s trace_id=%s message_id=%s viewer_client_id=%q recipient=%s response_len=%d %s", rootTaskID, traceID, messageID, req.ViewerClientID, recipient, len(response), provenance.LogFields())
			}
		}()

		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		resp := struct {
			OK                bool   `json:"ok"`
			SessionID         string `json:"session_id"`
			MessageID         string `json:"message_id"`
			AgentMessageID    string `json:"agent_message_id"`
			TurnID            string `json:"turn_id"`
			SpeechCancelToken string `json:"speech_cancel_token,omitempty"`
			TraceID           string `json:"trace_id"`
			RootTaskID        string `json:"root_task_id"`
			ViewerClientID    string `json:"viewer_client_id,omitempty"`
			Recipient         string `json:"recipient"`
			Attachments       int    `json:"attachment_count"`
		}{
			OK:                true,
			SessionID:         sessionID,
			MessageID:         messageID,
			AgentMessageID:    agentMessageID,
			TurnID:            turnID,
			SpeechCancelToken: speechCancelToken,
			TraceID:           traceID,
			RootTaskID:        rootTaskID,
			ViewerClientID:    req.ViewerClientID,
			Recipient:         string(recipient),
			Attachments:       len(attachments),
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.Printf("[Viewer] HandleSend: response encode error: %v", err)
		}
		log.Printf("[Viewer] HandleSend: sent OK response")
	}
}

func normalizeAudioOutputIntent(raw string) (AudioOutputIntent, error) {
	intent := AudioOutputIntent(strings.TrimSpace(raw))
	switch intent {
	case "", AudioOutputRequested, AudioOutputDisabled:
		return intent, nil
	default:
		return "", fmt.Errorf("unknown audio_output %q", raw)
	}
}

func defaultAttachmentMessage(attachments []domainattachment.Attachment) string {
	for _, att := range attachments {
		if att.Kind == domainattachment.KindVideo {
			return "添付動画を解析してください。"
		}
	}
	return "添付ファイルを確認してください。"
}

func parseViewerSendRequest(r *http.Request, saver AttachmentSaver) (viewerSendRequest, []domainattachment.Attachment, error) {
	req, attachments, _, err := parseViewerSendRequestAndPin(r, saver, nil)
	return req, attachments, err
}

func parseViewerSendRequestAndPin(r *http.Request, saver AttachmentSaver, hook ForegroundPinHook) (viewerSendRequest, []domainattachment.Attachment, ForegroundPinLease, error) {
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		return parseViewerMultipartSendRequestAndPin(r, saver, hook)
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		return viewerSendRequest{}, nil, nil, fmt.Errorf("read body: %w", err)
	}
	var req viewerSendRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return viewerSendRequest{}, nil, nil, fmt.Errorf("json decode: %w", err)
	}
	req.Presence, err = parseForegroundPresenceReference(req.PresenceRaw)
	if err != nil {
		return viewerSendRequest{}, nil, nil, fmt.Errorf("presence reference: %w", err)
	}
	pin, err := acquireViewerForegroundPin(r, hook, ForegroundPinRequest{ViewerClientID: req.ViewerClientID, Presence: req.Presence})
	if err != nil {
		return viewerSendRequest{}, nil, nil, err
	}
	return req, nil, pin, nil
}

func parseViewerMultipartSendRequest(r *http.Request, saver AttachmentSaver) (viewerSendRequest, []domainattachment.Attachment, error) {
	req, attachments, _, err := parseViewerMultipartSendRequestAndPin(r, saver, nil)
	return req, attachments, err
}

func parseViewerMultipartSendRequestAndPin(r *http.Request, saver AttachmentSaver, hook ForegroundPinHook) (viewerSendRequest, []domainattachment.Attachment, ForegroundPinLease, error) {
	if saver == nil {
		return viewerSendRequest{}, nil, nil, fmt.Errorf("attachment saver is nil")
	}
	if err := r.ParseMultipartForm(domainattachment.DefaultLimits.MaxTotalBytes + (1 << 20)); err != nil {
		return viewerSendRequest{}, nil, nil, fmt.Errorf("parse multipart: %w", err)
	}
	req := viewerSendRequest{
		ViewerClientID: r.FormValue("viewer_client_id"),
		AudioOutput:    r.FormValue("audio_output"),
		InputSource:    r.FormValue("input_source"),
		UserID:         r.FormValue("user_id"),
		DeviceName:     r.FormValue("device_name"),
		Message:        r.FormValue("message"),
		To:             r.FormValue("to"),
	}
	if presenceValues := r.MultipartForm.Value["presence"]; len(presenceValues) > 1 {
		return viewerSendRequest{}, nil, nil, fmt.Errorf("duplicate presence field")
	} else if len(presenceValues) == 1 {
		var err error
		req.Presence, err = parseForegroundPresenceReference(json.RawMessage(presenceValues[0]))
		if err != nil {
			return viewerSendRequest{}, nil, nil, fmt.Errorf("presence reference: %w", err)
		}
	}
	pin, err := acquireViewerForegroundPin(r, hook, ForegroundPinRequest{ViewerClientID: req.ViewerClientID, Presence: req.Presence})
	if err != nil {
		return viewerSendRequest{}, nil, nil, err
	}
	timingValue := strings.TrimSpace(r.FormValue("timings"))
	switch timingValue {
	case "":
	case "true":
		req.Timings = true
	case "false":
	default:
		releaseViewerForegroundPin(pin)
		return viewerSendRequest{}, nil, nil, fmt.Errorf("invalid timings value")
	}

	files, err := incomingViewerFiles(r.MultipartForm)
	if err != nil {
		releaseViewerForegroundPin(pin)
		return viewerSendRequest{}, nil, nil, err
	}
	attachments, err := saver.SaveAll(r.Context(), files)
	if err != nil {
		releaseViewerForegroundPin(pin)
		return viewerSendRequest{}, nil, nil, err
	}
	return req, attachments, pin, nil
}

func parseForegroundPresenceReference(raw []byte) (*ForegroundPresenceReference, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var reference ForegroundPresenceReference
	if err := decoder.Decode(&reference); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("unexpected trailing JSON data")
	}
	if reference.ProtocolVersion != 2 ||
		reference.RuntimeEpoch == "" || reference.RuntimeEpoch != strings.TrimSpace(reference.RuntimeEpoch) || !validOptionalV2Text(reference.RuntimeEpoch, 256) ||
		reference.RegistrationReceipt == "" || reference.RegistrationReceipt != strings.TrimSpace(reference.RegistrationReceipt) || !validOptionalV2Text(reference.RegistrationReceipt, 256) ||
		reference.ClaimSeq == 0 {
		return nil, errors.New("protocol_version, runtime_epoch, registration_receipt, and positive claim_seq are required")
	}
	return &reference, nil
}

func acquireViewerForegroundPin(r *http.Request, hook ForegroundPinHook, request ForegroundPinRequest) (ForegroundPinLease, error) {
	if hook == nil {
		return nil, nil
	}
	lease, err := hook(r, request)
	if err != nil {
		if lease != nil {
			releaseViewerForegroundPin(newOnceForegroundPinLease(lease))
		}
		return nil, err
	}
	if lease == nil {
		return nil, &ForegroundPinError{StatusCode: http.StatusServiceUnavailable, ErrorCode: "foreground_not_ready", Message: "foreground presence could not be pinned"}
	}
	return newOnceForegroundPinLease(lease), nil
}

type onceForegroundPinLease struct {
	lease ForegroundPinLease
	once  sync.Once
	err   error
}

func newOnceForegroundPinLease(lease ForegroundPinLease) *onceForegroundPinLease {
	if lease == nil {
		return nil
	}
	if once, ok := lease.(*onceForegroundPinLease); ok {
		return once
	}
	return &onceForegroundPinLease{lease: lease}
}

func (p *onceForegroundPinLease) Release() error {
	if p == nil || p.lease == nil {
		return nil
	}
	p.once.Do(func() { p.err = p.lease.Release() })
	return p.err
}

func releaseViewerForegroundPin(lease ForegroundPinLease) {
	if lease == nil {
		return
	}
	if err := lease.Release(); err != nil {
		log.Printf("[Viewer] foreground presence pin release failed error_type=%T", err)
	}
}

func writeViewerForegroundPinError(w http.ResponseWriter, err error) {
	var pinErr *ForegroundPinError
	if !errors.As(err, &pinErr) || pinErr == nil {
		pinErr = &ForegroundPinError{StatusCode: http.StatusServiceUnavailable, ErrorCode: "foreground_not_ready", Message: "foreground presence is unavailable"}
	}
	status := pinErr.StatusCode
	if status < 400 || status > 599 {
		status = http.StatusServiceUnavailable
	}
	code := strings.TrimSpace(pinErr.ErrorCode)
	if code == "" {
		code = "foreground_not_ready"
	}
	message := strings.TrimSpace(pinErr.Message)
	if message == "" {
		message = "foreground presence is unavailable"
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		OK              bool   `json:"ok"`
		ProtocolVersion int    `json:"protocol_version"`
		ErrorCode       string `json:"error_code"`
		Error           string `json:"error"`
	}{OK: false, ProtocolVersion: ViewerProtocolVersionV2, ErrorCode: code, Error: message})
}

func incomingViewerFiles(form *multipart.Form) ([]appattachment.IncomingFile, error) {
	if form == nil || len(form.File) == 0 {
		return nil, nil
	}
	headers := append([]*multipart.FileHeader{}, form.File["attachments"]...)
	headers = append(headers, form.File["attachments[]"]...)
	files := make([]appattachment.IncomingFile, 0, len(headers))
	for _, fh := range headers {
		f, err := fh.Open()
		if err != nil {
			return nil, fmt.Errorf("open attachment: %w", err)
		}
		files = append(files, appattachment.IncomingFile{
			Filename:    fh.Filename,
			ContentType: fh.Header.Get("Content-Type"),
			SizeBytes:   fh.Size,
			Reader:      f,
		})
	}
	return files, nil
}
