package main

import (
	"context"
	"testing"

	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func dataRecallInternalContext(t *testing.T) context.Context {
	t.Helper()
	scope := domaintool.ToolExecutionScope{
		RequestID:            "data-recall-internal",
		ActorKind:            domaintool.ActorKindAgent,
		ActorID:              "mio",
		AllowedDataScopes:    []string{domaintool.DataScopeInternal},
		AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator,
		AgentRole:            "worker",
		Purpose:              "ops",
	}
	if err := scope.Validate(); err != nil {
		t.Fatalf("internal scope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func dataRecallUserContext(t *testing.T) context.Context {
	t.Helper()
	scope, err := domaintool.NewToolExecutionScope("data-recall-user", domaintool.ActorKindAgent, "mio", "user-1", []string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatalf("user scope: %v", err)
	}
	scope.AgentRole = "worker"
	scope.Purpose = "ops"
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}
