package orchestrator

import (
	"errors"
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type correlatedSessionTurnLogger interface {
	WriteUserWithIdentity(sessionID, channel, messageID, traceID, content string)
	WriteAssistantWithIdentity(sessionID, channel, route, jobID, messageID, traceID, content string)
}

func ensureProcessRequestIdentity(req *ProcessMessageRequest) error {
	if req == nil {
		return errors.New("process message request is nil")
	}
	turnID, err := canonicalProcessID(req.TurnID, func() string { return string(modulecore.NewTurnID()) }, func(value string) error { return modulecore.TurnID(value).Validate() })
	if err != nil {
		return err
	}
	traceID, err := canonicalProcessID(req.TraceID, func() string { return string(modulecore.NewTraceID()) }, func(value string) error { return modulecore.TraceID(value).Validate() })
	if err != nil {
		return err
	}
	rootTaskID, err := canonicalProcessID(req.RootTaskID, func() string { return string(modulecore.NewTaskID()) }, func(value string) error { return modulecore.TaskID(value).Validate() })
	if err != nil {
		return err
	}
	messageID, err := canonicalProcessID(req.MessageID, func() string { return string(modulecore.NewMessageID()) }, func(value string) error { return modulecore.MessageID(value).Validate() })
	if err != nil {
		return err
	}
	agentMessageID, err := canonicalProcessID(req.AgentMessageID, func() string { return string(modulecore.NewMessageID()) }, func(value string) error { return modulecore.MessageID(value).Validate() })
	if err != nil {
		return err
	}
	if messageID == agentMessageID {
		return errors.New("user and agent message IDs must differ")
	}
	req.TurnID = turnID
	req.TraceID = traceID
	req.RootTaskID = rootTaskID
	req.MessageID = messageID
	req.AgentMessageID = agentMessageID
	return nil
}

func canonicalProcessID(raw string, generate func() string, validate func(string) error) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return generate(), nil
	}
	if validate(value) != nil {
		return "", errors.New("process message identity is malformed or has the wrong type")
	}
	return value, nil
}

func ensureProcessResponseIdentity(
	resp ProcessMessageResponse,
	rootJobID string,
	rootTurnID string,
	rootTraceID string,
	rootTaskID string,
	fallbackMessageID string,
	takeResponseMessageID func(string) string,
) ProcessMessageResponse {
	resp.TurnID = rootTurnID
	resp.TraceID = rootTraceID
	resp.RootTaskID = rootTaskID
	if strings.TrimSpace(resp.MessageID) == "" && takeResponseMessageID != nil {
		resp.MessageID = strings.TrimSpace(takeResponseMessageID(rootJobID))
	}
	if strings.TrimSpace(resp.MessageID) == "" {
		resp.MessageID = fallbackMessageID
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
