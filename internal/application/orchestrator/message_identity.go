package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type correlatedSessionTurnLogger interface {
	WriteUserWithIdentity(sessionID, channel, messageID, traceID, content string)
	WriteAssistantWithIdentity(sessionID, channel, route, taskID, messageID, traceID, content string)
}

type contextAwareSessionTurnLogger interface {
	WriteUserWithIdentityContext(ctx context.Context, sessionID, channel, messageID, traceID, content string) error
	WriteAssistantWithIdentityContext(ctx context.Context, sessionID, channel, route, taskID, messageID, traceID, content string) error
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
	executionTaskID string,
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
		resp.MessageID = strings.TrimSpace(takeResponseMessageID(executionTaskID))
	}
	if strings.TrimSpace(resp.MessageID) == "" {
		resp.MessageID = fallbackMessageID
	}
	return resp
}

func writeUserSessionTurn(ctx context.Context, logger SessionTurnLogger, req ProcessMessageRequest) error {
	if logger == nil {
		return nil
	}
	if _, admitted := persistencequeue.AdmissionFromContext(ctx); admitted {
		if contextAware, ok := logger.(contextAwareSessionTurnLogger); ok {
			if err := contextAware.WriteUserWithIdentityContext(ctx, req.SessionID, req.Channel, req.MessageID, req.TraceID, req.UserMessage); err != nil {
				return fmt.Errorf("write user session turn: %w", err)
			}
			return nil
		}
	}
	if correlated, ok := logger.(correlatedSessionTurnLogger); ok {
		correlated.WriteUserWithIdentity(req.SessionID, req.Channel, req.MessageID, req.TraceID, req.UserMessage)
		return nil
	}
	logger.WriteUser(req.SessionID, req.Channel, req.UserMessage)
	return nil
}

func writeAssistantSessionTurn(ctx context.Context, logger SessionTurnLogger, req ProcessMessageRequest, resp ProcessMessageResponse) error {
	if logger == nil {
		return nil
	}
	if _, admitted := persistencequeue.AdmissionFromContext(ctx); admitted {
		if contextAware, ok := logger.(contextAwareSessionTurnLogger); ok {
			if err := contextAware.WriteAssistantWithIdentityContext(
				ctx,
				req.SessionID,
				req.Channel,
				string(resp.Route),
				resp.TaskID,
				resp.MessageID,
				resp.TraceID,
				resp.Response,
			); err != nil {
				return fmt.Errorf("write assistant session turn: %w", err)
			}
			return nil
		}
	}
	if correlated, ok := logger.(correlatedSessionTurnLogger); ok {
		correlated.WriteAssistantWithIdentity(
			req.SessionID,
			req.Channel,
			string(resp.Route),
			resp.TaskID,
			resp.MessageID,
			resp.TraceID,
			resp.Response,
		)
		return nil
	}
	logger.WriteAssistant(req.SessionID, req.Channel, string(resp.Route), resp.TaskID, resp.Response)
	return nil
}
