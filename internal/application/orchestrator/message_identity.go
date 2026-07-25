package orchestrator

import (
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type correlatedSessionTurnLogger interface {
	WriteUserWithIdentity(sessionID, channel, messageID, traceID, content string)
	WriteAssistantWithIdentity(sessionID, channel, route, jobID, messageID, traceID, content string)
}

func ensureProcessRequestIdentity(req *ProcessMessageRequest, rootJobID string) {
	if req == nil {
		return
	}
	if strings.TrimSpace(req.MessageID) == "" {
		req.MessageID = string(modulecore.NewMessageID())
	}
	// The current conversation path has exactly one root job. Keep the trace
	// value equal to that root job until child jobs are introduced.
	req.TraceID = rootJobID
}

func ensureProcessResponseIdentity(
	resp ProcessMessageResponse,
	rootJobID string,
	takeResponseMessageID func(string) string,
) ProcessMessageResponse {
	resp.TraceID = rootJobID
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
