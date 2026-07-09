package modulebridge

import (
	"context"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/modules/chat"
	"github.com/Nyukimin/RenCrow_CORE/modules/core"
	modulellm "github.com/Nyukimin/RenCrow_CORE/modules/llm"
)

type RouteDecisionProvider interface {
	DecideAction(ctx context.Context, t task.Task) (routing.Decision, error)
}

type ChatServiceAdapter struct {
	processor orchestrator.Orchestrator
	policy    chat.RoutePolicy
}

func NewChatServiceAdapter(processor orchestrator.Orchestrator) *ChatServiceAdapter {
	return &ChatServiceAdapter{processor: processor}
}

func NewChatServiceAdapterWithRoutePolicy(processor orchestrator.Orchestrator, policy chat.RoutePolicy) *ChatServiceAdapter {
	return &ChatServiceAdapter{processor: processor, policy: policy}
}

func NewRuntimeChatService(processor orchestrator.Orchestrator, decider RouteDecisionProvider) *ChatServiceAdapter {
	return NewChatServiceAdapterWithRoutePolicy(processor, NewMioRoutePolicy(decider))
}

func NewMioRoutePolicy(decider RouteDecisionProvider) chat.RoutePolicy {
	return mioRoutePolicy{decider: decider}
}

type mioRoutePolicy struct {
	decider RouteDecisionProvider
}

func (a *ChatServiceAdapter) Health(context.Context) core.HealthReport {
	if a == nil || a.processor == nil {
		return chat.BuildServiceHealth(chat.ServiceHealthSnapshot{})
	}
	return chat.BuildServiceHealth(chat.ServiceHealthSnapshot{Ready: true})
}

func (p mioRoutePolicy) DecideRoute(ctx context.Context, input chat.Input) (chat.RouteDecision, error) {
	if p.decider == nil {
		return chat.RouteDecision{
			Route:  chat.RouteChat,
			Reason: "route policy is not configured",
		}, nil
	}
	input = chat.NormalizeInput(input)
	decision, err := p.decider.DecideAction(ctx, task.NewTask(task.NewJobID(), input.Text, input.Channel, input.UserID))
	if err != nil {
		return chat.RouteDecision{}, err
	}
	return chat.NormalizeRouteDecision(string(decision.Route), decision.Reason), nil
}

func (a *ChatServiceAdapter) DecideRoute(ctx context.Context, input chat.Input) (chat.RouteDecision, error) {
	if a != nil && a.policy != nil {
		return a.policy.DecideRoute(ctx, input)
	}
	return chat.RouteDecision{
		Route:  chat.RouteChat,
		Reason: "legacy orchestrator decides the concrete route during Respond",
	}, nil
}

func (a *ChatServiceAdapter) Respond(ctx context.Context, input chat.Input, _ chat.RuntimePorts) (chat.Output, error) {
	input = chat.NormalizeInput(input)
	resp, err := a.processor.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
		SessionID:   string(input.SessionID),
		Channel:     input.Channel,
		ChatID:      input.UserID,
		UserMessage: input.Text,
	})
	if err != nil {
		return chat.Output{}, err
	}
	return chat.Output{
		SessionID: input.SessionID,
		Text:      resp.Response,
		Route:     chat.NormalizeRouteDecision(string(resp.Route), string(resp.Route)),
		JobID:     resp.JobID,
		Response: modulellm.BuildGenerateResponse(modulellm.GenerateOutput{
			Content: resp.Response,
		}),
	}, nil
}
