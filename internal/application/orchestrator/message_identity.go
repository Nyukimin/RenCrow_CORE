package orchestrator

import (
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type correlatedSessionTurnLogger interface {
	WriteUserWithIdentity(sessionID, channel, messageID, traceID, content string)
	WriteAssistantWithIdentity(sessionID, channel, route, jobID, messageID, traceID, content string)
}

func ensureProcessRequestIdentity(req *ProcessMessageRequest) {
	if req == nil {
		return
	}
	if strings.TrimSpace(req.MessageID) == "" {
		req.MessageID = string(modulecore.NewMessageID())
	}
	// Preserve the canonical TraceID assigned by the ingress owner. Direct
	// callers without one receive a new root identity here.
	if modulecore.TraceID(strings.TrimSpace(req.TraceID)).Validate() != nil {
		req.TraceID = string(modulecore.NewTraceID())
	}
}

func ensureProcessResponseIdentity(
	resp ProcessMessageResponse,
	rootJobID string,
	rootTraceID string,
	takeResponseMessageID func(string) string,
) ProcessMessageResponse {
	resp.TraceID = rootTraceID
	if strings.TrimSpace(resp.MessageID) == "" && takeResponseMessageID != nil {
		resp.MessageID = strings.TrimSpace(takeResponseMessageID(rootJobID))
	}
	if strings.TrimSpace(resp.MessageID) == "" {
		resp.MessageID = string(modulecore.NewMessageID())
	}
	return resp
}

func writeUserSessionTurn(logger SessionTurnLogger, req ProcessMessageRequest) {
	if logger == nil {
		return
	}
	if correlated, ok := logger.(correlatedSessionTurnLogger); ok {
		correlated.WriteUserWithIdentity(req.SessionID, req.Channel, req.MessageID, req.TraceID, req.UserMessage)
		return
	}
	logger.WriteUser(req.SessionID, req.Channel, req.UserMessage)
}

func writeAssistantSessionTurn(logger SessionTurnLogger, req ProcessMessageRequest, resp ProcessMessageResponse) {
	if logger == nil {
		return
	}
	if correlated, ok := logger.(correlatedSessionTurnLogger); ok {
		correlated.WriteAssistantWithIdentity(
			req.SessionID,
			req.Channel,
			string(resp.Route),
			resp.JobID,
			resp.MessageID,
			resp.TraceID,
			resp.Response,
		)
		return
	}
	logger.WriteAssistant(req.SessionID, req.Channel, string(resp.Route), resp.JobID, resp.Response)
}
