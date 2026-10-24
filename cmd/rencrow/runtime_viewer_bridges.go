package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	chromeadapter "github.com/Nyukimin/RenCrow_CORE/internal/adapter/chrome"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	entryadapter "github.com/Nyukimin/RenCrow_CORE/internal/adapter/entry"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type canonicalViewerSessionRepository interface {
	LoadOrCreateCanonical(context.Context, string, domainconversation.ChannelAddress, time.Time) (*domainsession.Session, bool, error)
}

type canonicalViewerSessionReader interface {
	LoadCanonical(context.Context, string, domainconversation.ChannelAddress) (*domainsession.Session, bool, error)
}

type canonicalViewerV2SessionRepository interface {
	LoadCanonicalWithCreationIntent(context.Context, string, domainconversation.ChannelAddress) (*domainsession.Session, *domainsession.CreationIntent, bool, error)
	ReserveCanonicalV2(context.Context, string, domainconversation.ChannelAddress, string, time.Time) (*domainsession.Session, bool, error)
}

type viewerSessionRuntimeVersionReader interface {
	ReadSessionState(context.Context, modulecore.SessionID, string) (eventstore.SessionState, bool, error)
}

type viewerBridgeFactories struct {
	ViewerSendFromOrch   func(messageProcessor) http.HandlerFunc
	V2ConversationEvents http.HandlerFunc
	SpeechCancel         http.HandlerFunc
	SpeechProgress       http.HandlerFunc
	EntryFromOrch        func(messageProcessor) http.HandlerFunc
	ChromeBridgeFromOrch func(messageProcessor) (http.HandlerFunc, http.HandlerFunc, http.HandlerFunc)
}

func buildViewerBridgeHandlers(
	cfg *config.Config,
	deps *Dependencies,
	reportPath string,
	ttsRuntime ttsEntryRuntime,
	sessionRepo orchestrator.SessionRepository,
) viewerBridgeFactories {
	speechCancellationRegistry := viewer.NewSpeechCancellationRegistry()
	sessionProtocolAdmission := newViewerSessionProtocolAdmission(cfg, deps, sessionRepo)
	if deps != nil && deps.responseFirst != nil {
		if reservationReader, ok := sessionRepo.(responseFirstReservationReader); ok {
			deps.responseFirst.reservations = reservationReader
		}
	}
	resolveSession := func(ctx context.Context) (*domainsession.Session, bool, error) {
		canonicalRepo, ok := sessionRepo.(canonicalViewerSessionRepository)
		if !ok {
			return nil, false, fmt.Errorf("canonical Viewer session repository is unavailable")
		}
		canonicalReader, ok := sessionRepo.(canonicalViewerSessionReader)
		if !ok {
			return nil, false, fmt.Errorf("canonical Viewer session reader is unavailable")
		}
		address, err := domainconversation.NewChannelAddress("viewer", "viewer-user")
		if err != nil {
			return nil, false, err
		}
		now := time.Now().UTC()
		logicalDate := now.Format("2006-01-02")
		if sess, found, readErr := canonicalReader.LoadCanonical(ctx, logicalDate, address); readErr != nil {
			return nil, false, readErr
		} else if found {
			if sess == nil {
				return nil, false, fmt.Errorf("canonical Viewer session reader returned no session")
			}
			return sess, false, nil
		}
		sess, created, err := canonicalRepo.LoadOrCreateCanonical(ctx, logicalDate, address, now)
		if err != nil {
			// A v2 reservation can win after the read-only lookup. Re-resolve the
			// canonical identity so protocol admission can reject the legacy path
			// using the owner pin/reservation instead of losing that decision to
			// the mutating lookup's generic error.
			existing, found, readErr := canonicalReader.LoadCanonical(ctx, logicalDate, address)
			if readErr == nil && found && existing != nil {
				return existing, false, nil
			}
			if readErr != nil {
				return nil, false, fmt.Errorf("canonical Viewer session lookup failed: %w", err)
			}
			return nil, false, err
		}
		return sess, created, nil
	}
	loadExistingSession := func(ctx context.Context) (*domainsession.Session, bool, error) {
		canonicalRepo, ok := sessionRepo.(canonicalViewerSessionReader)
		if !ok {
			return nil, false, fmt.Errorf("canonical Viewer session reader is unavailable")
		}
		address, err := domainconversation.NewChannelAddress("viewer", "viewer-user")
		if err != nil {
			return nil, false, err
		}
		logicalDate := time.Now().UTC().Format("2006-01-02")
		return canonicalRepo.LoadCanonical(ctx, logicalDate, address)
	}
	viewerSendFromOrch := func(proc messageProcessor) http.HandlerFunc {
		attachmentStore := newRuntimeAttachmentStore(cfg)
		resolveSessionID := func(ctx context.Context) (modulecore.SessionID, error) {
			sess, _, err := resolveSession(ctx)
			if err != nil {
				return "", err
			}
			sessionID := modulecore.SessionID(sess.ID())
			if err := sessionID.Validate(); err != nil {
				return "", err
			}
			return sessionID, nil
		}
		resolveV2SessionID := func(ctx context.Context) (modulecore.SessionID, error) {
			canonicalRepo, ok := sessionRepo.(canonicalViewerV2SessionRepository)
			if !ok || deps.responseFirst == nil {
				return "", fmt.Errorf("canonical Viewer v2 reservation owner is unavailable")
			}
			address, err := domainconversation.NewChannelAddress("viewer", "viewer-user")
			if err != nil {
				return "", err
			}
			now := time.Now().UTC()
			logicalDate := now.Format("2006-01-02")
			if existing, intent, found, lookupErr := canonicalRepo.LoadCanonicalWithCreationIntent(ctx, logicalDate, address); lookupErr != nil {
				return "", lookupErr
			} else if found && existing != nil {
				sessionID := modulecore.SessionID(existing.ID())
				if err := sessionID.Validate(); err != nil {
					return "", err
				}
				if intent != nil {
					if intent.Runtime != "v2" || intent.OwnerID != deps.responseFirst.ownerID {
						return "", fmt.Errorf("canonical Viewer v2 reservation belongs to a different owner")
					}
					return sessionID, nil
				}
				if err := deps.responseFirst.ValidateExistingSession(ctx, sessionID); err == nil {
					return sessionID, nil
				}
			}
			createdAt, err := time.Parse("2006-01-02", logicalDate)
			if err != nil {
				return "", err
			}
			sess, _, err := canonicalRepo.ReserveCanonicalV2(ctx, logicalDate, address, deps.responseFirst.ownerID, createdAt.UTC())
			if err != nil {
				return "", err
			}
			if sess == nil {
				return "", fmt.Errorf("canonical Viewer v2 reservation returned no session")
			}
			sessionID := modulecore.SessionID(sess.ID())
			if err := sessionID.Validate(); err != nil {
				return "", err
			}
			return sessionID, nil
		}
		messageHandler := func(ctx context.Context, req viewer.SendRequest) (string, error) {
			// Viewer metadata may identify a user for correlation, but it is not
			// an authentication claim. The trusted orchestrator route therefore
			// grants only reviewed public Knowledge projections here. A future
			// authenticated ingress may add a user scope explicitly to ctx.
			trustedCtx, scopeErr := withTrustedAgentPublicToolScope(ctx, req.TraceID, string(req.To))
			if scopeErr != nil {
				return "", scopeErr
			}
			ctx = trustedCtx
			log.Printf("[main] viewerSendFromOrch: start root_task_id=%s trace_id=%s message_id=%s viewer_client_id=%q recipient=%s attachments=%d %s", req.RootTaskID, req.TraceID, req.MessageID, req.ViewerClientID, req.To, len(req.Attachments), req.Provenance.LogFields())
			resp, err := proc.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
				MessageID:       req.MessageID,
				AgentMessageID:  req.AgentMessageID,
				TurnID:          req.TurnID,
				TraceID:         req.TraceID,
				RootTaskID:      req.RootTaskID,
				SessionID:       req.SessionID,
				Channel:         "viewer",
				ChatID:          "viewer-user",
				UserMessage:     req.Message,
				To:              string(req.To),
				OperationSource: req.Provenance.OperationSource,
				AudioOutput:     orchestrator.AudioOutputIntent(req.AudioOutput),
				Attachments:     req.Attachments,
			})
			if err != nil {
				log.Printf("[main] viewerSendFromOrch: error root_task_id=%s trace_id=%s message_id=%s viewer_client_id=%q recipient=%s %s err=%v", req.RootTaskID, req.TraceID, req.MessageID, req.ViewerClientID, req.To, req.Provenance.LogFields(), err)
				return "", err
			}
			log.Printf("[main] viewerSendFromOrch: complete task_id=%s root_task_id=%s trace_id=%s message_id=%s viewer_client_id=%q recipient=%s route=%s %s", resp.TaskID, resp.RootTaskID, resp.TraceID, resp.MessageID, req.ViewerClientID, req.To, resp.Route, req.Provenance.LogFields())
			return resp.Response, nil
		}
		errorHandler := func(ctx context.Context, req viewer.SendRequest, err error) {
			if deps.eventRelay != nil {
				event := orchestrator.NewEventWithTraceID(modulecore.TraceID(req.TraceID),
					"viewer.error", "system", "viewer", err.Error(),
					"", req.RootTaskID, req.SessionID, "viewer", "viewer-user",
				)
				if recorder := turntiming.FromContext(ctx); recorder != nil {
					snapshot := recorder.Snapshot()
					event.Timing = &snapshot
				}
				if publishErr := deps.eventRelay.OnEvent(event); publishErr != nil {
					log.Printf("[Viewer] error event publication failed task=%s: %v", req.RootTaskID, publishErr)
				}
			}
		}
		var v2Options *viewer.V2SendOptions
		var timing *turntiming.BatchRecorder
		var processing *viewer.V2ProcessingLifecycle
		if deps.responseFirst != nil && deps.responseFirst.processing != nil {
			timing = deps.responseFirst.processing.timing
			processing = deps.responseFirst.processing.lifecycle
		}
		var legacyPreSessionAdmission viewer.LegacyPreSessionAdmissionHook
		if deps != nil && deps.combinedChatProcessorEligible {
			legacyPreSessionAdmission = func(requestContext context.Context, identity viewer.LegacyPreSessionAdmissionIdentity) (context.Context, func(), error) {
				return admitRuntimeLegacyViewerChat(
					requestContext,
					deps.persistenceQueue,
					deps.taskStore,
					deps.combinedChatAdmissionEnabled,
					deps.eventRelay,
					identity,
				)
			}
		}
		v2Options = &viewer.V2SendOptions{
			Admission:                 sessionProtocolAdmission,
			LegacyPreSessionAdmission: legacyPreSessionAdmission,
			ForegroundPin: func(r *http.Request, request viewer.ForegroundPinRequest) (viewer.ForegroundPinLease, error) {
				return pinViewerForegroundPresence(deps, r, request)
			},
		}
		if cfg != nil && strings.EqualFold(strings.TrimSpace(cfg.ConversationRuntime.Version), "v2") && deps.responseFirst != nil {
			v2Options.Enabled = true
			v2Options.ActorID = responseFirstActorID
			v2Options.ResolveSessionID = resolveV2SessionID
			v2Options.Accept = deps.responseFirst.Accept
			v2Options.Process = deps.responseFirst.Process
			v2Options.Timing = timing
			v2Options.Processing = processing
			v2Options.OnError = func(_ context.Context, accepted viewer.V2AcceptedTurn, err error) {
				log.Printf("[Viewer] response-first processing failed session_id=%s turn_id=%s error_type=%T", accepted.SessionID, accepted.TurnID, err)
			}
		}
		return viewer.HandleSendWithProtocolV2(messageHandler, errorHandler, attachmentStore, resolveSessionID, speechCancellationRegistry, v2Options)
	}
	v2ConversationEvents := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if deps == nil || deps.eventHub == nil || deps.responseFirst == nil {
			http.Error(w, "response-first conversation stream is unavailable", http.StatusServiceUnavailable)
			return
		}
		sess, found, err := loadExistingSession(r.Context())
		if err != nil || !found || sess == nil {
			http.Error(w, "canonical Viewer session is unavailable", http.StatusServiceUnavailable)
			return
		}
		sessionID := modulecore.SessionID(sess.ID())
		if sessionID.Validate() != nil {
			http.Error(w, "canonical Viewer session is invalid", http.StatusServiceUnavailable)
			return
		}
		state, found, err := deps.responseFirst.store.ReadSessionState(r.Context(), sessionID, deps.responseFirst.ownerID)
		if err != nil || !found || state.RuntimeVersion != responseFirstRuntimeVer {
			// EventSource retries this temporary failure after the first v2 send
			// pins a newly-created canonical session.
			http.Error(w, "owner-authorized response-first session is unavailable", http.StatusServiceUnavailable)
			return
		}
		deps.eventHub.HandleSessionPublicSSE(w, r, viewer.SessionPublicReplayScope{
			SessionID: sessionID, OwnerID: deps.responseFirst.ownerID, ActorID: responseFirstActorID, CutoverSeq: state.CutoverSeq,
		})
	})
	entryFromOrch := func(proc messageProcessor) http.HandlerFunc {
		return entryadapter.HandleWithObserver(
			func(ctx context.Context, req entryadapter.Request) (entryadapter.Result, error) {
				return processEntryRequestWithRuntime(ctx, proc, req, reportPath, ttsRuntime)
			},
			func(ctx context.Context, stage entryadapter.Stage, req entryadapter.Request, result *entryadapter.Result, err error) {
				route := ""
				taskID := ""
				eventSessionID := strings.TrimSpace(req.SessionID)
				if result != nil {
					route = result.Route
					taskID = result.TaskID
					if strings.TrimSpace(result.SessionID) != "" {
						eventSessionID = strings.TrimSpace(result.SessionID)
					}
				}
				if modulecore.SessionID(eventSessionID).Validate() != nil {
					eventSessionID = ""
				}
				if deps.eventRelay != nil {
					if publishErr := deps.eventRelay.OnEvent(orchestrator.NewEvent(
						"entry.stage",
						req.Platform,
						"system",
						string(stage),
						route,
						taskID,
						eventSessionID,
						req.Channel,
						req.UserID,
					)); publishErr != nil {
						log.Printf("[Entry] stage event publication failed stage=%s session=%s task=%s: %v", stage, req.SessionID, taskID, publishErr)
					}
				}
				switch stage {
				case entryadapter.StageReceived:
					log.Printf("[entry] stage=%s channel=%s user=%s session=%s", stage, req.Channel, req.UserID, req.SessionID)
				case entryadapter.StagePlanning:
					log.Printf("[entry] stage=%s session=%s", stage, req.SessionID)
				case entryadapter.StageCompleted:
					log.Printf("[entry] stage=%s session=%s route=%s task=%s", stage, req.SessionID, route, taskID)
				case entryadapter.StageFailed:
					log.Printf("[entry] stage=%s session=%s err=%v", stage, req.SessionID, err)
				default:
					log.Printf("[entry] stage=%s session=%s", stage, req.SessionID)
				}
			},
		)
	}
	chromeBridgeFromOrch := func(proc messageProcessor) (http.HandlerFunc, http.HandlerFunc, http.HandlerFunc) {
		bridge := chromeadapter.HandleBridge(func(ctx context.Context, req entryadapter.Request) (entryadapter.Result, error) {
			return processEntryRequestWithRuntime(ctx, proc, req, reportPath, ttsRuntime)
		})
		status := chromeadapter.HandleBridgeStatus(func() []orchestrator.OrchestratorEvent {
			if deps.eventHub == nil {
				return nil
			}
			return deps.eventHub.History()
		}, func() time.Time {
			return time.Now().UTC()
		})
		events := chromeadapter.HandleBridgeEvents(deps.eventHub)
		return bridge, status, events
	}
	return viewerBridgeFactories{
		ViewerSendFromOrch:   viewerSendFromOrch,
		V2ConversationEvents: v2ConversationEvents,
		SpeechCancel:         speechCancellationRegistry.HandleCancel(),
		SpeechProgress:       speechCancellationRegistry.HandleProgress(activeViewerControl.IsActiveAudio),
		EntryFromOrch:        entryFromOrch,
		ChromeBridgeFromOrch: chromeBridgeFromOrch,
	}
}

func pinViewerForegroundPresence(deps *Dependencies, r *http.Request, request viewer.ForegroundPinRequest) (viewer.ForegroundPinLease, error) {
	if deps == nil || deps.surfacePresence == nil {
		return nil, &viewer.ForegroundPinError{StatusCode: http.StatusServiceUnavailable, ErrorCode: "foreground_not_ready", Message: "foreground presence is unavailable"}
	}
	client, profile, ok := surfacePresenceRequestProfile(r)
	if !ok {
		return nil, &viewer.ForegroundPinError{StatusCode: http.StatusForbidden, ErrorCode: "profile_surface_mismatch", Message: "trusted interaction profile is required"}
	}
	principal, ok := surfacePresenceRequestPrincipal(r)
	if !ok {
		return nil, &viewer.ForegroundPinError{StatusCode: http.StatusForbidden, ErrorCode: "authenticated_actor_invalid", Message: "authenticated actor is invalid"}
	}
	if client == "RenCrow_ASSISTANT" && profile == "assistant-core" {
		pin, err := deps.surfacePresence.PinForegroundRequest()
		if err != nil {
			return nil, surfacePresenceForegroundPinError(err)
		}
		return pin, nil
	}
	surface, ok := viewerForegroundPresenceSurface(profile)
	if !ok || !surfacePresenceProfileAllows(client, profile, surface) {
		return nil, &viewer.ForegroundPinError{StatusCode: http.StatusForbidden, ErrorCode: "profile_surface_mismatch", Message: "interaction profile does not allow foreground send"}
	}
	if !validSurfacePresenceViewerClientIDV2(request.ViewerClientID) {
		return nil, &viewer.ForegroundPinError{StatusCode: http.StatusBadRequest, ErrorCode: "invalid_viewer_client_id", Message: "viewer_client_id must be 1 to 128 UTF-8 bytes"}
	}
	if request.Presence == nil {
		return nil, &viewer.ForegroundPinError{StatusCode: http.StatusConflict, ErrorCode: "presence_required", Message: "register and claim foreground presence before sending"}
	}
	identity := surfacePresenceIdentity{principal: principal, clientType: client, viewerClientID: request.ViewerClientID}
	pin, err := deps.surfacePresence.PinForeground(identity,
		request.Presence.RuntimeEpoch,
		request.Presence.RegistrationReceipt,
		request.Presence.ClaimSeq,
	)
	if err == nil {
		return pin, nil
	}
	return nil, surfacePresenceForegroundPinError(err)
}

func surfacePresenceForegroundPinError(err error) *viewer.ForegroundPinError {
	var apiErr *surfacePresenceAPIError
	if errors.As(err, &apiErr) && apiErr != nil {
		return &viewer.ForegroundPinError{StatusCode: apiErr.status, ErrorCode: apiErr.code, Message: apiErr.message}
	}
	return &viewer.ForegroundPinError{StatusCode: http.StatusServiceUnavailable, ErrorCode: "foreground_not_ready", Message: "foreground presence could not be pinned"}
}

func viewerForegroundPresenceSurface(profile string) (string, bool) {
	switch profile {
	case "portal-chat":
		return "chat", true
	case "portal-idlechat":
		return "idlechat", true
	case "portal-games":
		return "games", true
	case "core-viewer":
		return "other", true
	case "cmd-chat", "cmd-idlechat":
		return "cmd", true
	default:
		return "", false
	}
}

func newViewerSessionProtocolAdmission(cfg *config.Config, deps *Dependencies, sessionRepo orchestrator.SessionRepository) *viewer.SessionProtocolAdmission {
	var store viewerSessionRuntimeVersionReader
	ownerID := conversationRuntimeUserID(cfg)
	if deps != nil {
		if deps.responseFirst != nil {
			store = deps.responseFirst.store
			ownerID = deps.responseFirst.ownerID
		} else if canonicalStore, ok := deps.canonicalEventStore.(viewerSessionRuntimeVersionReader); ok {
			store = canonicalStore
		}
	}
	if store == nil || strings.TrimSpace(ownerID) == "" {
		return viewer.NewSessionProtocolAdmission(func(context.Context, modulecore.SessionID) (string, bool, error) {
			return "", false, fmt.Errorf("owner-checked session runtime version lookup is unavailable")
		})
	}
	versionLookup := func(ctx context.Context, sessionID modulecore.SessionID) (string, bool, error) {
		state, found, err := store.ReadSessionState(ctx, sessionID, ownerID)
		if err != nil || !found {
			return "", found, err
		}
		return state.RuntimeVersion, true, nil
	}
	reservationLookup := func(ctx context.Context, sessionID modulecore.SessionID) (bool, error) {
		reader, ok := sessionRepo.(responseFirstReservationReader)
		if !ok {
			return false, fmt.Errorf("owner-checked session reservation lookup is unavailable")
		}
		return reader.IsCanonicalV2Reserved(ctx, string(sessionID), ownerID)
	}
	return viewer.NewSessionProtocolAdmissionWithReservationLookup(versionLookup, reservationLookup)
}
