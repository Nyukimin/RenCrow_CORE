package main

import (
	"context"
	"log"
	"net/http"
	"time"

	chromeadapter "github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/chrome"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	entryadapter "github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/entry"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/viewer"
	attachmentapp "github.com/Nyukimin/picoclaw_multiLLM/internal/application/attachment"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
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
		attachmentStore := attachmentapp.NewStore(cfg.WorkspaceDir)
		return viewer.HandleSendWithAttachments(func(ctx context.Context, req viewer.SendRequest) (string, error) {
			log.Printf("[main] viewerSendFromOrch: calling ProcessMessage for viewer message: %q attachments=%d", req.Message, len(req.Attachments))
			resp, err := proc.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
				SessionID:   "viewer",
				Channel:     "viewer",
				ChatID:      "viewer-user",
				UserMessage: req.Message,
				To:          string(req.To),
				Attachments: req.Attachments,
			})
			if err != nil {
				log.Printf("[main] viewerSendFromOrch: ProcessMessage error: %v", err)
				return "", err
			}
			log.Printf("[main] viewerSendFromOrch: ProcessMessage completed, route=%s jobID=%s", resp.Route, resp.JobID)
			return resp.Response, nil
		}, func(err error) {
			if deps.eventRelay != nil {
				deps.eventRelay.OnEvent(orchestrator.NewEvent(
					"viewer.error", "system", "viewer", err.Error(),
					"", "", "viewer", "viewer", "viewer-user",
				))
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
					deps.eventRelay.OnEvent(orchestrator.NewEvent(
						"entry.stage",
						req.Platform,
						"system",
						string(stage),
						route,
						jobID,
						req.SessionID,
						req.Channel,
						req.UserID,
					))
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
