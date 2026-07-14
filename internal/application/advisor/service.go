package advisor

import (
	"context"
	"fmt"
	"strings"
	"time"

	advisorDomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type Provider interface {
	Profile() advisorDomain.Profile
	RequestAdvice(ctx context.Context, req advisorDomain.AdviceRequest) (advisorDomain.AdviceResult, error)
}

type Service struct {
	providers map[advisorDomain.AdvisorID]Provider
}

func NewService(providers ...Provider) *Service {
	service := &Service{providers: map[advisorDomain.AdvisorID]Provider{}}
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		profile := provider.Profile()
		if profile.ID == "" || profile.Disabled {
			continue
		}
		service.providers[profile.ID] = provider
	}
	return service
}

func (s *Service) RequestAdvice(ctx context.Context, req advisorDomain.AdviceRequest) (advisorDomain.AdviceResult, error) {
	if err := req.Validate(); err != nil {
		return advisorDomain.AdviceResult{}, err
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now()
	}
	provider := s.providers[req.AdvisorID]
	if provider == nil {
		return advisorDomain.AdviceResult{
			RequestID:   req.ID,
			AdvisorID:   req.AdvisorID,
			Status:      advisorDomain.StatusUnavailable,
			Summary:     "advisor is not registered",
			StartedAt:   req.CreatedAt,
			CompletedAt: time.Now(),
		}, fmt.Errorf("advisor %q is not registered", req.AdvisorID)
	}
	return provider.RequestAdvice(ctx, req)
}

type ToolRunner interface {
	ExecuteV2(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error)
	ListTools(ctx context.Context) ([]tool.ToolMetadata, error)
}

type CodexToolAdvisor struct {
	tools ToolRunner
}

func NewCodexToolAdvisor(tools ToolRunner) *CodexToolAdvisor {
	return &CodexToolAdvisor{tools: tools}
}

func (a *CodexToolAdvisor) Profile() advisorDomain.Profile {
	return advisorDomain.Profile{
		ID:          advisorDomain.AdvisorCodex,
		DisplayName: "Codex",
		Provider:    "codex-cli",
		Capabilities: []advisorDomain.Capability{
			{Domain: "go", Level: 5, Description: "Go code investigation and patch proposals"},
			{Domain: "test", Level: 5, Description: "Test planning and execution advice"},
			{Domain: "refactor", Level: 5, Description: "Scoped refactoring advice"},
		},
		AllowedModes: []string{"read-only"},
	}
}

func (a *CodexToolAdvisor) RequestAdvice(ctx context.Context, req advisorDomain.AdviceRequest) (advisorDomain.AdviceResult, error) {
	started := time.Now()
	if err := req.Validate(); err != nil {
		return advisorDomain.AdviceResult{}, err
	}
	if a.tools == nil || !a.hasCodexRun(ctx) {
		return advisorDomain.AdviceResult{
			RequestID:   req.ID,
			AdvisorID:   advisorDomain.AdvisorCodex,
			Status:      advisorDomain.StatusUnavailable,
			Summary:     "codex.run is not available",
			StartedAt:   started,
			CompletedAt: time.Now(),
		}, fmt.Errorf("codex.run is not available")
	}
	resp, err := a.tools.ExecuteV2(ctx, "codex.run", map[string]any{
		"prompt":  strings.TrimSpace(req.Prompt),
		"sandbox": "read-only",
	})
	if err != nil {
		return advisorDomain.AdviceResult{
			RequestID:   req.ID,
			AdvisorID:   advisorDomain.AdvisorCodex,
			Status:      advisorDomain.StatusFailed,
			Summary:     err.Error(),
			StartedAt:   started,
			CompletedAt: time.Now(),
		}, err
	}
	if resp == nil {
		return advisorDomain.AdviceResult{
			RequestID:   req.ID,
			AdvisorID:   advisorDomain.AdvisorCodex,
			Status:      advisorDomain.StatusFailed,
			Summary:     "codex.run returned nil response",
			StartedAt:   started,
			CompletedAt: time.Now(),
		}, fmt.Errorf("codex.run returned nil response")
	}
	if resp.IsError() {
		return advisorDomain.AdviceResult{
			RequestID:   req.ID,
			AdvisorID:   advisorDomain.AdvisorCodex,
			Status:      advisorDomain.StatusFailed,
			Summary:     resp.Error.Message,
			StartedAt:   started,
			CompletedAt: time.Now(),
		}, fmt.Errorf("%s", resp.Error.Message)
	}
	return advisorDomain.AdviceResult{
		RequestID:   req.ID,
		AdvisorID:   advisorDomain.AdvisorCodex,
		Status:      advisorDomain.StatusCompleted,
		Summary:     resp.String(),
		StartedAt:   started,
		CompletedAt: time.Now(),
	}, nil
}

func (a *CodexToolAdvisor) hasCodexRun(ctx context.Context) bool {
	metas, err := a.tools.ListTools(ctx)
	if err != nil {
		return false
	}
	for _, meta := range metas {
		if meta.ToolID == "codex.run" {
			return true
		}
	}
	return false
}
