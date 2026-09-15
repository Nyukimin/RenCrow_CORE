package dci

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// OwnedSearcher binds each DCI operation to the canonical Action owner.
type OwnedSearcher struct {
	explorer *Explorer
	actions  *actionmanager.Manager
}

func NewOwnedSearcher(explorer *Explorer, actions *actionmanager.Manager) (*OwnedSearcher, error) {
	if explorer == nil {
		return nil, errors.New("dci explorer is required")
	}
	if actions == nil {
		return nil, errors.New("dci action owner is required")
	}
	return &OwnedSearcher{explorer: explorer, actions: actions}, nil
}

func (s *OwnedSearcher) ShouldTrigger(query string) bool {
	return s != nil && s.explorer != nil && s.explorer.ShouldTrigger(query)
}

func (s *OwnedSearcher) Search(ctx context.Context, query string) (domaindci.SearchResult, error) {
	if s == nil || s.explorer == nil {
		return domaindci.SearchResult{}, errors.New("dci owned searcher is unavailable")
	}
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		return domaindci.SearchResult{}, fmt.Errorf("dci execution identity: %w", err)
	}
	return s.SearchAs(ctx, query, identity.TaskID, identity.RunID, s.explorer.cfg.ActorKind, s.explorer.cfg.ActorID, "")
}

func (s *OwnedSearcher) SearchAs(
	ctx context.Context,
	query string,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	actorKind string,
	actorID string,
	idempotencyKey string,
) (domaindci.SearchResult, error) {
	if s == nil || s.explorer == nil || s.actions == nil {
		return domaindci.SearchResult{}, errors.New("dci owned searcher is unavailable")
	}
	traceID := modulecore.NewTraceID()
	ownedCtx := ctx
	if identity, identityErr := domainexecution.IdentityFromContext(ctx); identityErr == nil {
		if identity.TaskID != taskID || identity.RunID != runID {
			return domaindci.SearchResult{}, errors.New("dci execution identity does not match requested Task and Run")
		}
		if identity.TraceID != "" {
			traceID = identity.TraceID
		}
	} else {
		var bindErr error
		ownedCtx, bindErr = domainexecution.WithIdentity(ctx, taskID, runID, traceID)
		if bindErr != nil {
			return domaindci.SearchResult{}, fmt.Errorf("bind dci execution identity: %w", bindErr)
		}
	}
	requestID := strings.TrimSpace(idempotencyKey)
	if parentScope, ok := domaintool.ToolExecutionScopeFromContext(ownedCtx); ok {
		requestID = parentScope.RequestID
	}
	if requestID == "" {
		requestID = string(modulecore.NewRequestID())
	}
	derivedCtx, err := domaintool.DeriveAgentToolExecutionScope(
		ownedCtx,
		requestID,
		s.explorer.cfg.ActorID,
		"worker",
		"dci_search",
		true,
	)
	if err != nil {
		return domaindci.SearchResult{}, fmt.Errorf("derive DCI executor scope: %w", err)
	}
	ownedCtx = derivedCtx
	action, attempt, err := s.actions.CreateAction(ownedCtx, actionmanager.CreateInput{
		TaskID: taskID,
		RunID:  runID,
		Kind:   domainaction.KindDCI,
		Name:   "dci_search",
	})
	if err != nil {
		return domaindci.SearchResult{}, fmt.Errorf("create dci action: %w", err)
	}
	ownedCtx, err = domainexecution.WithChildBoundActionAttempt(ownedCtx, action.ActionID, attempt.AttemptID)
	if err != nil {
		return domaindci.SearchResult{}, fmt.Errorf("bind dci Action and Attempt: %w", err)
	}
	result, searchErr := s.explorer.SearchWithIdentity(
		ownedCtx,
		query,
		traceID,
		action.ActionID,
		actorKind,
		actorID,
		idempotencyKey,
	)
	attemptStatus, actionStatus := dciTerminalStatus(searchErr)
	summary := "dci search completed"
	if searchErr != nil {
		summary = strings.TrimSpace(searchErr.Error())
	}
	completionCtx, cancel := newDCIRecoveryContext(ctx)
	defer cancel()
	if _, _, completeErr := s.actions.CompleteAttempt(
		completionCtx,
		action.ActionID,
		attempt.AttemptID,
		attemptStatus,
		actionStatus,
		summary,
	); completeErr != nil {
		if searchErr != nil {
			return domaindci.SearchResult{}, errors.Join(searchErr, fmt.Errorf("complete dci action: %w", completeErr))
		}
		return domaindci.SearchResult{}, fmt.Errorf("complete dci action: %w", completeErr)
	}
	if searchErr != nil {
		return domaindci.SearchResult{}, searchErr
	}
	return result, nil
}

func dciTerminalStatus(err error) (domainaction.AttemptStatus, domainaction.Status) {
	switch {
	case err == nil:
		return domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded
	case errors.Is(err, context.Canceled):
		return domainaction.AttemptStatusCancelled, domainaction.StatusCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return domainaction.AttemptStatusTimedOut, domainaction.StatusFailed
	default:
		return domainaction.AttemptStatusFailed, domainaction.StatusFailed
	}
}
