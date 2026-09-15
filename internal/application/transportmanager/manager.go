package transportmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type Store = domaintransport.Store

type Manager struct {
	store   Store
	actions *actionmanager.Manager
	now     func() time.Time
	mu      sync.RWMutex
}

func New(store Store, actions *actionmanager.Manager) *Manager {
	return &Manager{store: store, actions: actions, now: func() time.Time { return time.Now().UTC() }}
}

func (m *Manager) BeginRequest(ctx context.Context, operation string) (domaintransport.Request, error) {
	return m.beginRequest(ctx, operation, modulecore.NewRequestID())
}

func (m *Manager) BeginAssignedRequest(ctx context.Context, operation string, requestID modulecore.RequestID) (domaintransport.Request, error) {
	if err := requestID.Validate(); err != nil {
		return domaintransport.Request{}, fmt.Errorf("request_id is invalid: %w", err)
	}
	return m.beginRequest(ctx, operation, requestID)
}

func (m *Manager) beginRequest(ctx context.Context, operation string, requestID modulecore.RequestID) (domaintransport.Request, error) {
	identity, actionID, attemptID, err := m.boundLineage(ctx)
	if err != nil {
		return domaintransport.Request{}, err
	}
	operation = strings.TrimSpace(operation)
	if operation == "" {
		return domaintransport.Request{}, errors.New("transport operation is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var created domaintransport.Request
	err = m.store.Transaction(ctx, func(tx Store) error {
		if _, err := tx.GetRequest(ctx, requestID); err == nil {
			return fmt.Errorf("request %s already exists", requestID)
		} else if !errors.Is(err, domaintransport.ErrNotFound) {
			return err
		}
		requests, err := tx.ListRequests(ctx, domaintransport.RequestFilter{AttemptID: attemptID})
		if err != nil {
			return err
		}
		now := m.now()
		created = domaintransport.Request{
			RequestID: requestID,
			TaskID:    identity.TaskID, RunID: identity.RunID,
			ActionID: actionID, AttemptID: attemptID, TraceID: identity.TraceID,
			Operation: operation, Sequence: len(requests) + 1,
			Status: domaintransport.RequestStatusSent, SentAt: now,
		}
		if err := created.Validate(); err != nil {
			return err
		}
		return tx.SaveRequest(ctx, created)
	})
	if err != nil {
		return domaintransport.Request{}, err
	}
	return created, nil
}

func (m *Manager) CompleteResponse(ctx context.Context, requestID modulecore.RequestID, externalRef string) (domaintransport.Response, error) {
	identity, actionID, attemptID, err := m.boundLineage(ctx)
	if err != nil {
		return domaintransport.Response{}, err
	}
	if err := requestID.Validate(); err != nil {
		return domaintransport.Response{}, fmt.Errorf("request_id is invalid: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var response domaintransport.Response
	err = m.store.Transaction(ctx, func(tx Store) error {
		request, err := tx.GetRequest(ctx, requestID)
		if err != nil {
			return err
		}
		if err := requireRequestLineage(request, identity, actionID, attemptID); err != nil {
			return err
		}
		if request.Status != domaintransport.RequestStatusSent {
			return fmt.Errorf("request %s is already terminal", requestID)
		}
		now := m.now()
		request.Status = domaintransport.RequestStatusResponded
		request.CompletedAt = &now
		if err := request.Validate(); err != nil {
			return err
		}
		response = domaintransport.Response{
			ResponseID: modulecore.NewResponseID(), RequestID: request.RequestID,
			TaskID: request.TaskID, RunID: request.RunID, ActionID: request.ActionID,
			AttemptID: request.AttemptID, TraceID: request.TraceID,
			ExternalRef: strings.TrimSpace(externalRef), ReceivedAt: now,
		}
		if err := response.Validate(); err != nil {
			return err
		}
		if err := tx.SaveRequest(ctx, request); err != nil {
			return err
		}
		return tx.SaveResponse(ctx, response)
	})
	if err != nil {
		return domaintransport.Response{}, err
	}
	return response, nil
}

func (m *Manager) FailWithoutResponse(ctx context.Context, requestID modulecore.RequestID, summary string) error {
	identity, actionID, attemptID, err := m.boundLineage(ctx)
	if err != nil {
		return err
	}
	if err := requestID.Validate(); err != nil {
		return fmt.Errorf("request_id is invalid: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.Transaction(ctx, func(tx Store) error {
		request, err := tx.GetRequest(ctx, requestID)
		if err != nil {
			return err
		}
		if err := requireRequestLineage(request, identity, actionID, attemptID); err != nil {
			return err
		}
		if request.Status != domaintransport.RequestStatusSent {
			return fmt.Errorf("request %s is already terminal", requestID)
		}
		now := m.now()
		request.Status = domaintransport.RequestStatusFailed
		request.CompletedAt = &now
		request.Summary = strings.TrimSpace(summary)
		return tx.SaveRequest(ctx, request)
	})
}

func (m *Manager) ListRequests(ctx context.Context, filter domaintransport.RequestFilter) ([]domaintransport.Request, error) {
	if m == nil || m.store == nil {
		return nil, errors.New("transport store is unavailable")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domaintransport.Request
	err := m.store.ReadTransaction(ctx, func(tx Store) error {
		var err error
		result, err = tx.ListRequests(ctx, filter)
		return err
	})
	return result, err
}

func (m *Manager) ListResponses(ctx context.Context, filter domaintransport.ResponseFilter) ([]domaintransport.Response, error) {
	if m == nil || m.store == nil {
		return nil, errors.New("transport store is unavailable")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []domaintransport.Response
	err := m.store.ReadTransaction(ctx, func(tx Store) error {
		var err error
		result, err = tx.ListResponses(ctx, filter)
		return err
	})
	return result, err
}

func (m *Manager) RecoverAfterRestart(ctx context.Context) (int, error) {
	pending, err := m.ListRequests(ctx, domaintransport.RequestFilter{Status: domaintransport.RequestStatusSent})
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, request := range pending {
		action, err := m.actions.GetAction(ctx, request.ActionID)
		if err != nil {
			return recovered, fmt.Errorf("load Action for stale request %s: %w", request.RequestID, err)
		}
		attempts, err := m.actions.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: request.ActionID})
		if err != nil {
			return recovered, fmt.Errorf("load Attempt for stale request %s: %w", request.RequestID, err)
		}
		var attempt *domainaction.Attempt
		for index := range attempts {
			if attempts[index].AttemptID == request.AttemptID {
				attempt = &attempts[index]
				break
			}
		}
		if attempt == nil {
			return recovered, fmt.Errorf("stale request %s has no referenced Attempt", request.RequestID)
		}
		if !domainaction.IsTerminal(action.Status) || !domainaction.IsAttemptTerminal(attempt.Status) {
			continue
		}
		m.mu.Lock()
		err = m.store.Transaction(ctx, func(tx Store) error {
			current, err := tx.GetRequest(ctx, request.RequestID)
			if err != nil {
				return err
			}
			if current.Status != domaintransport.RequestStatusSent {
				return nil
			}
			now := m.now()
			current.Status = domaintransport.RequestStatusFailed
			current.CompletedAt = &now
			current.Summary = "process restarted before transport response"
			if err := current.Validate(); err != nil {
				return err
			}
			if err := tx.SaveRequest(ctx, current); err != nil {
				return err
			}
			recovered++
			return nil
		})
		m.mu.Unlock()
		if err != nil {
			return recovered, err
		}
	}
	return recovered, nil
}

func (m *Manager) boundLineage(ctx context.Context) (domainexecution.Identity, modulecore.ActionID, modulecore.AttemptID, error) {
	if m == nil || m.store == nil || m.actions == nil {
		return domainexecution.Identity{}, "", "", errors.New("transport owner is unavailable")
	}
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		return domainexecution.Identity{}, "", "", err
	}
	actionID, attemptID, ok := domainexecution.BoundActionAttemptFromContext(ctx)
	if !ok {
		return domainexecution.Identity{}, "", "", errors.New("transport Action Attempt identity is required")
	}
	if err := m.actions.ValidateAttemptLineage(ctx, actionID, attemptID, identity.TaskID, identity.RunID); err != nil {
		return domainexecution.Identity{}, "", "", err
	}
	return identity, actionID, attemptID, nil
}

func requireRequestLineage(request domaintransport.Request, identity domainexecution.Identity, actionID modulecore.ActionID, attemptID modulecore.AttemptID) error {
	if request.TaskID != identity.TaskID || request.RunID != identity.RunID ||
		request.ActionID != actionID || request.AttemptID != attemptID ||
		request.TraceID != identity.TraceID {
		return errors.New("transport request lineage does not match bound execution")
	}
	return nil
}
