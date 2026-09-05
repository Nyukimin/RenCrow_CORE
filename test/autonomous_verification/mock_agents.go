//go:build e2e

package autonomousverification

import (
	"context"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/proposal"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

// ========================================
// Mock Agents
// ========================================

// MockMioAgent は MioAgent の mock 実装
type MockMioAgent struct {
	DecideActionFunc      func(ctx context.Context, input conversation.TurnInput) (routing.Decision, error)
	ChatFunc              func(ctx context.Context, input conversation.TurnInput) (string, error)
	HandleChatCommandFunc func(ctx context.Context, sessionID string, message string) (agent.ChatCommandResult, error)
}

func (m *MockMioAgent) DecideAction(ctx context.Context, input conversation.TurnInput) (routing.Decision, error) {
	if m.DecideActionFunc != nil {
		return m.DecideActionFunc(ctx, input)
	}
	// Default: return CHAT route
	return routing.Decision{
		Route:      routing.RouteCHAT,
		Confidence: 1.0,
		Reason:     "mock default",
	}, nil
}

func (m *MockMioAgent) Chat(ctx context.Context, input conversation.TurnInput) (string, error) {
	if m.ChatFunc != nil {
		return m.ChatFunc(ctx, input)
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
	ExecuteFunc func(ctx context.Context, input conversation.TurnInput) (string, error)
}

func (m *MockShiroAgent) Execute(ctx context.Context, input conversation.TurnInput) (string, error) {
	if m.ExecuteFunc != nil {
		return m.ExecuteFunc(ctx, input)
	}
	return "mock shiro response", nil
}

// MockCoderAgent は CoderAgent の mock 実装
type MockCoderAgent struct {
	GenerateFunc         func(ctx context.Context, input conversation.TurnInput, systemPrompt string) (string, error)
	GenerateProposalFunc func(ctx context.Context, input conversation.TurnInput) (*proposal.Proposal, error)
}

func (m *MockCoderAgent) Generate(ctx context.Context, input conversation.TurnInput, systemPrompt string) (string, error) {
	if m.GenerateFunc != nil {
		return m.GenerateFunc(ctx, input, systemPrompt)
	}
	return "mock coder response", nil
}

func (m *MockCoderAgent) GenerateProposal(ctx context.Context, input conversation.TurnInput) (*proposal.Proposal, error) {
	if m.GenerateProposalFunc != nil {
		return m.GenerateProposalFunc(ctx, input)
	}
	return proposal.NewProposal(
		"mock plan: create hello.go with HelloWorld function",
		`[{"type": "file_edit", "action": "create", "target": "/tmp/e2e-mock-hello.go", "content": "package main\n\nimport \"fmt\"\n\nfunc HelloWorld() {\n\tfmt.Println(\"Hello, World!\")\n}"}]`,
		"low",
		"simple function addition for testing",
	), nil
}
