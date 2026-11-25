package taskcenter

import (
	"context"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

type gameDesignStore interface {
	EnsureGameProjectDesign(context.Context, tool.ToolExecutionScope, string) (domainworkstream.GameProjectDesignResult, error)
}

func (s *Service) EnsureGameProjectDesign(ctx context.Context, workstreamID string) (domainworkstream.GameProjectDesignResult, error) {
	scope, err := s.scopeFromContext(ctx)
	if err != nil {
		return domainworkstream.GameProjectDesignResult{}, err
	}
	store, ok := s.store.(gameDesignStore)
	if !ok {
		return domainworkstream.GameProjectDesignResult{}, ErrUnsupported
	}
	result, err := store.EnsureGameProjectDesign(ctx, scope, workstreamID)
	return result, mapOwnerError(err)
}

func (c *Center) EnsureGameProjectDesign(ctx context.Context, workstreamID string) (domainworkstream.GameProjectDesignResult, error) {
	if c == nil || c.managed == nil {
		return domainworkstream.GameProjectDesignResult{}, ErrUnsupported
	}
	return c.managed.EnsureGameProjectDesign(ctx, workstreamID)
}
