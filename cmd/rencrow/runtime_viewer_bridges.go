package main

import (
	"context"
	"log"
	"net/http"
	"time"

	chromeadapter "github.com/Nyukimin/RenCrow_CORE/internal/adapter/chrome"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	entryadapter "github.com/Nyukimin/RenCrow_CORE/internal/adapter/entry"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

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
) viewerBridgeFactories {
	viewerSendFromOrch := func(proc messageProcessor) http.HandlerFunc {
		attachmentStore := newRuntimeAttachmentStore(cfg)
		return viewer.HandleSendWithAttachments(func(ctx context.Context, req viewer.SendRequest) (string, error) {
			// Viewer metadata may identify a user for correlation, but it is not
			// an authentication claim. The trusted orchestrator route therefore
			// grants only reviewed public Knowledge projections here. A future
			// authenticated ingress may add a user scope explicitly to ctx.
			trustedCtx, scopeErr := withTrustedAgentPublicToolScope(ctx, req.TraceID, string(req.To))
			if scopeErr != nil {
				return "", scopeErr
			}
			ctx = trustedCtx
			log.Printf("[main] viewerSendFromOrch: start job_id=%s trace_id=%s message_id=%s viewer_client_id=%q recipient=%s attachments=%d %s", req.JobID, req.TraceID, req.MessageID, req.ViewerClientID, req.To, len(req.Attachments), req.Provenance.LogFields())
			resp, err := proc.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
				JobID:           req.JobID,
				MessageID:       req.MessageID,
				AgentMessageID:  req.AgentMessageID,
				TurnID:          req.TurnID,
				TraceID:         req.TraceID,
				RootTaskID:      req.RootTaskID,
				Channel:         "viewer",
				ChatID:          "viewer-user",
				UserMessage:     req.Message,
				To:              string(req.To),
				OperationSource: req.Provenance.OperationSource,
				AudioOutput:     orchestrator.AudioOutputIntent(req.AudioOutput),
				Attachments:     req.Attachments,
			})
			if err != nil {
				log.Printf("[main] viewerSendFromOrch: error job_id=%s trace_id=%s message_id=%s viewer_client_id=%q recipient=%s %s err=%v", req.JobID, req.TraceID, req.MessageID, req.ViewerClientID, req.To, req.Provenance.LogFields(), err)
				return "", err
			}
			log.Printf("[main] viewerSendFromOrch: complete job_id=%s trace_id=%s message_id=%s viewer_client_id=%q recipient=%s route=%s %s", resp.JobID, resp.TraceID, resp.MessageID, req.ViewerClientID, req.To, resp.Route, req.Provenance.LogFields())
			return resp.Response, nil
		}, func(req viewer.SendRequest, err error) {
			if deps.eventRelay != nil {
				if publishErr := deps.eventRelay.OnEvent(orchestrator.NewEventWithTraceID(modulecore.TraceID(req.TraceID),
					"viewer.error", "system", "viewer", err.Error(),
					"", req.JobID, "viewer", "viewer", "viewer-user",
				)); publishErr != nil {
					log.Printf("[Viewer] error event publication failed job=%s: %v", req.JobID, publishErr)
				}
			}
		}, attachmentStore)
	}
	entryFromOrch := func(proc messageProcessor) http.HandlerFunc {
		return entryadapter.HandleWithObserver(
			func(ctx context.Context, req entryadapter.Request) (entryadapter.Result, error) {
				return processEntryRequestWithRuntime(ctx, proc, req, reportPath, ttsRuntime)
			},
			func(ctx context.Context, stage entryadapter.Stage, req entryadapter.Request, result *entryadapter.Result, err error) {
				route := ""
				jobID := ""
				if result != nil {
					route = result.Route
					jobID = result.JobID
				}
				if deps.eventRelay != nil {
					if publishErr := deps.eventRelay.OnEvent(orchestrator.NewEvent(
						"entry.stage",
						req.Platform,
						"system",
						string(stage),
						route,
						jobID,
						req.SessionID,
						req.Channel,
						req.UserID,
					)); publishErr != nil {
						log.Printf("[Entry] stage event publication failed stage=%s session=%s job=%s: %v", stage, req.SessionID, jobID, publishErr)
					}
				}
				switch stage {
				case entryadapter.StageReceived:
					log.Printf("[entry] stage=%s channel=%s user=%s session=%s", stage, req.Channel, req.UserID, req.SessionID)
				case entryadapter.StagePlanning:
					log.Printf("[entry] stage=%s session=%s", stage, req.SessionID)
				case entryadapter.StageCompleted:
					log.Printf("[entry] stage=%s session=%s route=%s job=%s", stage, req.SessionID, route, jobID)
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
