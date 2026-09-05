package orchestrator

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/transport"
)

type distributedTimeoutResolver func(targetAgent string, msg domaintransport.Message) time.Duration

type distributedProgressEmitter func(eventType, from, to, content string, msg domaintransport.Message)

type distributedTransportExecutor struct {
	router        *transport.MessageRouter
	sshTransports map[string]domaintransport.Transport
	memory        *session.CentralMemory
	emitProgress  distributedProgressEmitter
	waitTimeout   distributedTimeoutResolver
}

func newDistributedTransportExecutor(
	router *transport.MessageRouter,
	sshTransports map[string]domaintransport.Transport,
	memory *session.CentralMemory,
	emitProgress distributedProgressEmitter,
	waitTimeout distributedTimeoutResolver,
) *distributedTransportExecutor {
	return &distributedTransportExecutor{
		router:        router,
		sshTransports: sshTransports,
		memory:        memory,
		emitProgress:  emitProgress,
		waitTimeout:   waitTimeout,
	}
}

func validateDistributedResponse(request, result domaintransport.Message) error {
	if err := result.Validate(); err != nil {
		return fmt.Errorf("invalid transport response: %w", err)
	}
	if result.TaskID != request.TaskID {
		return fmt.Errorf("transport response task_id mismatch: got %s want %s", result.TaskID, request.TaskID)
	}
	return nil
}

func (e *distributedTransportExecutor) ExecuteViaSSH(ctx context.Context, sshTransport domaintransport.Transport, targetAgent string, msg domaintransport.Message) (string, error) {
	if err := msg.Validate(); err != nil {
		return "", fmt.Errorf("invalid transport message: %w", err)
	}
	if err := sshTransport.Send(ctx, msg); err != nil {
		return "", fmt.Errorf("failed to send message to %s via SSH: %w", targetAgent, err)
	}
	log.Printf("[DistributedOrch] Sent task to %s via SSH (task=%s)", targetAgent, msg.TaskID)

	waitTimeout := e.waitTimeout(targetAgent, msg)
	timeoutCtx, cancel := context.WithTimeout(ctx, waitTimeout)
	defer cancel()

	result, err := sshTransport.Receive(timeoutCtx)
	if err != nil {
		return "", fmt.Errorf("waiting for SSH response from %s: %w", targetAgent, err)
	}
	if err := validateDistributedResponse(msg, result); err != nil {
		log.Printf("[DistributedOrch] invalid SSH response from %s (task=%s): %v", targetAgent, msg.TaskID, err)
		e.emitProgress("mailbox.error", targetAgent, msg.From, err.Error(), msg)
		return "", fmt.Errorf("invalid SSH response from %s: %w", targetAgent, err)
	}
	e.memory.RecordMessage(result)
	log.Printf("[DistributedOrch] Received SSH response from %s (type=%s)", result.From, result.Type)

	if result.Type == domaintransport.MessageTypeError {
		return "", fmt.Errorf("agent %s returned error: %s", result.From, result.Content)
	}
	return result.Content, nil
}

func (e *distributedTransportExecutor) ExecuteToAgent(ctx context.Context, targetAgent string, msg domaintransport.Message) (domaintransport.Message, error) {
	return e.ExecuteToAgentViaMailbox(ctx, targetAgent, msg, msg.From)
}

func (e *distributedTransportExecutor) ExecuteToAgentViaMailbox(ctx context.Context, targetAgent string, msg domaintransport.Message, receiveOnAgent string) (domaintransport.Message, error) {
	if err := msg.Validate(); err != nil {
		return domaintransport.Message{}, fmt.Errorf("invalid transport message: %w", err)
	}
	log.Printf("[DistributedOrch] mailbox send target=%s receive_on=%s via=%s task=%s type=%s has_proposal=%t", targetAgent, receiveOnAgent, transportMode(e.sshTransports, targetAgent), msg.TaskID, msg.Type, msg.Proposal != nil)
	e.emitProgress("mailbox.sent", msg.From, targetAgent, fmt.Sprintf("via=%s receive_on=%s type=%s", transportMode(e.sshTransports, targetAgent), receiveOnAgent, msg.Type), msg)
	if sshTransport, ok := e.sshTransports[targetAgent]; ok {
		return e.executeToAgentViaSSH(ctx, sshTransport, targetAgent, msg, receiveOnAgent)
	}
	return e.ExecuteViaLocal(ctx, targetAgent, msg, receiveOnAgent)
}

func (e *distributedTransportExecutor) executeToAgentViaSSH(ctx context.Context, sshTransport domaintransport.Transport, targetAgent string, msg domaintransport.Message, receiveOnAgent string) (domaintransport.Message, error) {
	if err := sshTransport.Send(ctx, msg); err != nil {
		e.emitProgress("mailbox.error", targetAgent, receiveOnAgent, err.Error(), msg)
		return domaintransport.Message{}, fmt.Errorf("failed to send message to %s via SSH: %w", targetAgent, err)
	}
	waitTimeout := e.waitTimeout(targetAgent, msg)
	timeoutCtx, cancel := context.WithTimeout(ctx, waitTimeout)
	defer cancel()
	log.Printf("[DistributedOrch] mailbox wait target=%s via=ssh timeout=%s task=%s", targetAgent, waitTimeout, msg.TaskID)
	e.emitProgress("mailbox.waiting", receiveOnAgent, targetAgent, fmt.Sprintf("via=ssh timeout=%s", waitTimeout), msg)

	result, err := sshTransport.Receive(timeoutCtx)
	if err != nil {
		log.Printf("[DistributedOrch] mailbox wait error target=%s via=ssh task=%s err=%v", targetAgent, msg.TaskID, err)
		e.emitProgress("mailbox.error", targetAgent, receiveOnAgent, err.Error(), msg)
		return domaintransport.Message{}, fmt.Errorf("waiting for SSH response from %s: %w", targetAgent, err)
	}
	if err := validateDistributedResponse(msg, result); err != nil {
		log.Printf("[DistributedOrch] invalid mailbox response target=%s via=ssh task=%s err=%v", targetAgent, msg.TaskID, err)
		e.emitProgress("mailbox.error", targetAgent, receiveOnAgent, err.Error(), msg)
		return domaintransport.Message{}, fmt.Errorf("invalid SSH response from %s: %w", targetAgent, err)
	}
	e.memory.RecordMessage(result)
	log.Printf("[DistributedOrch] mailbox recv target=%s via=ssh from=%s type=%s task=%s", targetAgent, result.From, result.Type, result.TaskID)
	e.emitProgress("mailbox.received", result.From, receiveOnAgent, fmt.Sprintf("via=ssh type=%s", result.Type), msg)
	if result.Type == domaintransport.MessageTypeError {
		e.emitProgress("agent.error", result.From, receiveOnAgent, result.Content, msg)
		return domaintransport.Message{}, fmt.Errorf("agent %s returned error: %s", result.From, result.Content)
	}
	return result, nil
}

func (e *distributedTransportExecutor) ExecuteViaLocal(ctx context.Context, targetAgent string, msg domaintransport.Message, receiveOnAgent string) (domaintransport.Message, error) {
	if err := msg.Validate(); err != nil {
		return domaintransport.Message{}, fmt.Errorf("invalid transport message: %w", err)
	}
	agentTransport, ok := e.router.GetAgent(targetAgent)
	if !ok {
		return domaintransport.Message{}, fmt.Errorf("agent '%s' not registered in router", targetAgent)
	}
	if err := agentTransport.PutInboundMessage(msg); err != nil {
		e.emitProgress("mailbox.error", targetAgent, receiveOnAgent, err.Error(), msg)
		return domaintransport.Message{}, fmt.Errorf("failed to send message to %s: %w", targetAgent, err)
	}

	log.Printf("[DistributedOrch] Sent task to %s via Local (task=%s type=%s receive_on=%s)", targetAgent, msg.TaskID, msg.Type, receiveOnAgent)
	e.emitProgress("mailbox.waiting", receiveOnAgent, targetAgent, fmt.Sprintf("via=local timeout=%s", e.waitTimeout(targetAgent, msg)), msg)

	receiveTransport, ok := e.router.GetAgent(receiveOnAgent)
	if !ok {
		receiveTransport, ok = e.router.GetAgent("mio")
	}
	if !ok {
		e.emitProgress("mailbox.error", targetAgent, receiveOnAgent, "receive transport not registered", msg)
		return domaintransport.Message{}, fmt.Errorf("receive transport not registered (agent=%s)", receiveOnAgent)
	}

	waitTimeout := e.waitTimeout(targetAgent, msg)
	timeoutCtx, cancel := context.WithTimeout(ctx, waitTimeout)
	defer cancel()
	log.Printf("[DistributedOrch] wait local response target=%s receive_on=%s timeout=%s task=%s", targetAgent, receiveOnAgent, waitTimeout, msg.TaskID)

	result, err := receiveTransport.Receive(timeoutCtx)
	if err != nil {
		log.Printf("[DistributedOrch] wait local response error target=%s receive_on=%s task=%s err=%v", targetAgent, receiveOnAgent, msg.TaskID, err)
		e.emitProgress("mailbox.error", targetAgent, receiveOnAgent, err.Error(), msg)
		return domaintransport.Message{}, fmt.Errorf("waiting for response from %s: %w", targetAgent, err)
	}
	if err := validateDistributedResponse(msg, result); err != nil {
		log.Printf("[DistributedOrch] invalid local response target=%s receive_on=%s task=%s err=%v", targetAgent, receiveOnAgent, msg.TaskID, err)
		e.emitProgress("mailbox.error", targetAgent, receiveOnAgent, err.Error(), msg)
		return domaintransport.Message{}, fmt.Errorf("invalid local response from %s: %w", targetAgent, err)
	}
	e.memory.RecordMessage(result)
	log.Printf("[DistributedOrch] Received response from %s (type=%s task=%s to=%s)", result.From, result.Type, result.TaskID, result.To)
	e.emitProgress("mailbox.received", result.From, receiveOnAgent, fmt.Sprintf("via=local type=%s", result.Type), msg)
	if result.Type == domaintransport.MessageTypeError {
		e.emitProgress("agent.error", result.From, receiveOnAgent, result.Content, msg)
		return domaintransport.Message{}, fmt.Errorf("agent %s returned error: %s", result.From, result.Content)
	}
	return result, nil
}
