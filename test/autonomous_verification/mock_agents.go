//go:build e2e

package autonomousverification

import (
	"context"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// ========================================
// Mock Agents
// ========================================

// MockMioAgent は MioAgent の mock 実装
type MockMioAgent struct {
	DecideActionFunc     func(ctx context.Context, t task.Task) (routing.Decision, error)
	ChatFunc             func(ctx context.Context, t task.Task) (string, error)
	HandleChatCommandFunc func(ctx context.Context, sessionID string, message string) (agent.ChatCommandResult, error)
}

func (m *MockMioAgent) DecideAction(ctx context.Context, t task.Task) (routing.Decision, error) {
	if m.DecideActionFunc != nil {
		return m.DecideActionFunc(ctx, t)
	}
	// Default: return CHAT route
	return routing.Decision{
		Route:      routing.RouteCHAT,
		Confidence: 1.0,
		Reason:     "mock default",
	}, nil
}

func (m *MockMioAgent) Chat(ctx context.Context, t task.Task) (string, error) {
	if m.ChatFunc != nil {
		return m.ChatFunc(ctx, t)
	}
	return "mock chat response", nil
}

func (m *MockMioAgent) HandleChatCommand(ctx context.Context, sessionID string, message string) (agent.ChatCommandResult, error) {
	if m.HandleChatCommandFunc != nil {
		return m.HandleChatCommandFunc(ctx, sessionID, message)
	}
	return agent.ChatCommandResult{Handled: false}, nil
}

// MockShiroAgent は ShiroAgent の mock 実装
type MockShiroAgent struct {
	ExecuteFunc func(ctx context.Context, t task.Task) (string, error)
}

func (m *MockShiroAgent) Execute(ctx context.Context, t task.Task) (string, error) {
	if m.ExecuteFunc != nil {
		return m.ExecuteFunc(ctx, t)
	}
	return "mock shiro response", nil
}

// MockCoderAgent は CoderAgent の mock 実装
type MockCoderAgent struct {
	GenerateFunc         func(ctx context.Context, t task.Task, systemPrompt string) (string, error)
	GenerateProposalFunc func(ctx context.Context, t task.Task) (*proposal.Proposal, error)
}

func (m *MockCoderAgent) Generate(ctx context.Context, t task.Task, systemPrompt string) (string, error) {
	if m.GenerateFunc != nil {
		return m.GenerateFunc(ctx, t, systemPrompt)
	}
	return "mock coder response", nil
}

func (m *MockCoderAgent) GenerateProposal(ctx context.Context, t task.Task) (*proposal.Proposal, error) {
	if m.GenerateProposalFunc != nil {
		return m.GenerateProposalFunc(ctx, t)
	}
	return proposal.NewProposal(
		"mock plan: create hello.go with HelloWorld function",
		`[{"type": "file_edit", "action": "create", "target": "/tmp/e2e-mock-hello.go", "content": "package main\n\nimport \"fmt\"\n\nfunc HelloWorld() {\n\tfmt.Println(\"Hello, World!\")\n}"}]`,
		"low",
		"simple function addition for testing",
	), nil
}
