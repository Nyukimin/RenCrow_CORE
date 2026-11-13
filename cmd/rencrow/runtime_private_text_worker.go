package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainrouting "github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type privateActorTextWorker struct {
	actorID      string
	systemPrompt string
	provider     llm.LLMProvider
}

// privateTaskTextOnlyLLMProvider deliberately exposes only Generate even when
// an inner tracking middleware also implements the tool-calling interface.
type privateTaskTextOnlyLLMProvider struct {
	inner llm.LLMProvider
}

var privateTextWorkerActorIDs = [...]string{"kuro", "aka", "ao", "kin", "gin"}

func (p privateTaskTextOnlyLLMProvider) Generate(ctx context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	if p.inner == nil {
		return llm.GenerateResponse{}, errors.New("private text LLM provider is unavailable")
	}
	return p.inner.Generate(ctx, request)
}

func (p privateTaskTextOnlyLLMProvider) Name() string {
	if p.inner == nil {
		return ""
	}
	return p.inner.Name()
}

func isPrivateTextWorkerActor(actorID string) bool {
	for _, allowedActorID := range privateTextWorkerActorIDs {
		if actorID == allowedActorID {
			return true
		}
	}
	return false
}

func newPrivateTextWorkers(providers map[string]llm.LLMProvider, characterPrompts map[string]string) map[string]privateTaskWorker {
	workers := make(map[string]privateTaskWorker, len(privateTextWorkerActorIDs))
	for _, actorID := range privateTextWorkerActorIDs {
		provider := providers[actorID]
		prompt := strings.TrimSpace(characterPrompts[actorID])
		if provider == nil || prompt == "" {
			continue
		}
		workers[actorID] = &privateActorTextWorker{actorID: actorID, systemPrompt: prompt, provider: provider}
	}
	return workers
}

func (w *privateActorTextWorker) ExecutePrivateWork(ctx context.Context, input conversation.TurnInput, allowedCapabilities []string) (string, error) {
	if w == nil || w.provider == nil {
		return "", errors.New("private text worker provider is unavailable")
	}
	if !isPrivateTextWorkerActor(w.actorID) {
		return "", errors.New("private text worker actor is not allowed")
	}
	if strings.TrimSpace(w.systemPrompt) == "" {
		return "", errors.New("private text worker character prompt is unavailable")
	}
	if len(allowedCapabilities) != 0 {
		return "", errors.New("private text workers do not accept external capabilities")
	}
	if err := validatePrivateActorTextInput(ctx, input, w.actorID); err != nil {
		return "", err
	}
	ctx = llm.WithExecutionObservationDefaults(ctx, llm.ExecutionObservation{
		TaskID: input.RootTaskID(), TraceID: string(input.TraceID()),
		Initiator: w.actorID, Purpose: "private_task_execution",
	})
	response, err := w.provider.Generate(ctx, llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "system", Content: w.systemPrompt},
			{Role: "user", Content: input.MessageText()},
		},
		MaxTokens:      2048,
		Temperature:    0.4,
		ResponseFormat: llm.ResponseFormatText,
	})
	if err != nil {
		return "", err
	}
	return response.Content, nil
}

func validatePrivateActorTextInput(ctx context.Context, input conversation.TurnInput, actorID string) error {
	if ctx == nil {
		return errors.New("private text worker context is unavailable")
	}
	if err := input.Validate(); err != nil {
		return fmt.Errorf("validate private text worker input: %w", err)
	}
	if input.ViewerRecipient() != actorID || input.Route() != domainrouting.RouteCHAT || input.BackendSelection() != conversation.BackendSelectionNone {
		return errors.New("private text worker input does not match its CHAT actor")
	}
	if len(input.Attachments()) != 0 {
		return errors.New("private text worker accepts text-only input")
	}
	identity, err := execution.IdentityFromContext(ctx)
	if err != nil {
		return fmt.Errorf("private text worker execution identity: %w", err)
	}
	if identity.TaskID != input.RootTaskID() || identity.TraceID != input.TraceID() {
		return errors.New("private text worker execution identity does not match its input")
	}
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found {
		return errors.New("private text worker authorization scope is unavailable")
	}
	if err := scope.Validate(); err != nil {
		return fmt.Errorf("validate private text worker authorization scope: %w", err)
	}
	if scope.RequestID != string(input.TraceID()) || scope.ActorKind != domaintool.ActorKindAgent || scope.ActorID != actorID ||
		scope.AuthenticatedUserID != "" || len(scope.AllowedDataScopes) != 1 || scope.AllowedDataScopes[0] != domaintool.DataScopePublic ||
		scope.AuthenticationSource != domaintool.AuthenticationSourceAgentOrchestrator ||
		scope.AgentRole != "worker" || scope.Purpose != "private_task_execution" {
		return errors.New("private text worker authorization scope does not match its CHAT actor")
	}
	return nil
}
