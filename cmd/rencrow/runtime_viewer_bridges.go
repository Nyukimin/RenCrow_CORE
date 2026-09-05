package main

import (
	"context"
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
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type canonicalViewerSessionRepository interface {
	LoadOrCreateCanonical(context.Context, string, domainconversation.ChannelAddress, time.Time) (*domainsession.Session, error)
}

type viewerBridgeFactories struct {
	ViewerSendFromOrch   func(messageProcessor) http.HandlerFunc
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
	viewerSendFromOrch := func(proc messageProcessor) http.HandlerFunc {
		attachmentStore := newRuntimeAttachmentStore(cfg)
		resolveSessionID := func(ctx context.Context) (modulecore.SessionID, error) {
			canonicalRepo, ok := sessionRepo.(canonicalViewerSessionRepository)
			if !ok {
				return "", fmt.Errorf("canonical Viewer session repository is unavailable")
			}
			address, err := domainconversation.NewChannelAddress("viewer", "viewer-user")
			if err != nil {
				return "", err
			}
			now := time.Now().UTC()
			sess, err := canonicalRepo.LoadOrCreateCanonical(ctx, now.Format("2006-01-02"), address, now)
			if err != nil {
				return "", err
			}
			sessionID := modulecore.SessionID(sess.ID())
			if err := sessionID.Validate(); err != nil {
				return "", err
			}
			return sessionID, nil
		}
		return viewer.HandleSendWithAttachmentsAndSessionResolver(func(ctx context.Context, req viewer.SendRequest) (string, error) {
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
		}, func(req viewer.SendRequest, err error) {
			if deps.eventRelay != nil {
				if publishErr := deps.eventRelay.OnEvent(orchestrator.NewEventWithTraceID(modulecore.TraceID(req.TraceID),
					"viewer.error", "system", "viewer", err.Error(),
					"", req.RootTaskID, req.SessionID, "viewer", "viewer-user",
				)); publishErr != nil {
					log.Printf("[Viewer] error event publication failed task=%s: %v", req.RootTaskID, publishErr)
				}
			}
		}, attachmentStore, resolveSessionID)
	}
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
		EntryFromOrch:        entryFromOrch,
		ChromeBridgeFromOrch: chromeBridgeFromOrch,
	}
}
