package jobmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	domainjob "github.com/Nyukimin/RenCrow_CORE/internal/domain/job"
)

var (
	ErrNotFound      = domainjob.ErrNotFound
	ErrParallelLimit = errors.New("parallel limit exceeded")
)

type Store interface {
	SaveJob(ctx context.Context, j domainjob.Job) error
	GetJob(ctx context.Context, jobID string) (domainjob.Job, error)
	ListJobs(ctx context.Context, filter domainjob.Filter) ([]domainjob.Job, error)
	SaveContext(ctx context.Context, c domainjob.SharedRoleContext) error
	GetContext(ctx context.Context, jobID string) (domainjob.SharedRoleContext, error)
	SaveNotification(ctx context.Context, n domainjob.Notification) error
	ListNotifications(ctx context.Context, limit int, interruptOnly bool) ([]domainjob.Notification, error)
}

type ParallelLimits struct {
	Global           int
	PerModule        int
	CodingJobs       int
	LongResearchJobs int
	DestructiveOps   int
}

func DefaultParallelLimits() ParallelLimits {
	return ParallelLimits{
		Global:           3,
		PerModule:        1,
		CodingJobs:       2,
		LongResearchJobs: 1,
		DestructiveOps:   1,
	}
}

type Manager struct {
	store  Store
	limits ParallelLimits
	now    func() time.Time
}

func New(store Store, limits ParallelLimits) *Manager {
	if limits.Global <= 0 {
		limits = DefaultParallelLimits()
	}
	return &Manager{
		store:  store,
		limits: limits,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
}

func (m *Manager) CreateJob(ctx context.Context, draft domainjob.Job, shared domainjob.SharedRoleContext) (domainjob.Job, error) {
	now := m.now()
	draft.ApplyDefaults(now)
	if err := draft.Validate(); err != nil {
		return domainjob.Job{}, err
	}
	if err := m.store.SaveJob(ctx, draft); err != nil {
		return domainjob.Job{}, err
	}
	if shared.JobID == "" {
		shared.JobID = draft.JobID
	}
	if shared.JobID == draft.JobID {
		if shared.ModuleID == "" {
			shared.ModuleID = draft.ModuleID
		}
		if shared.ModuleRoot == "" {
			shared.ModuleRoot = draft.ModuleRoot
		}
		shared.UpdatedAt = now
		if err := m.store.SaveContext(ctx, shared); err != nil {
			return domainjob.Job{}, err
		}
	}
	return draft, nil
}

func (m *Manager) StartJob(ctx context.Context, jobID string) (domainjob.Job, error) {
	j, err := m.store.GetJob(ctx, jobID)
	if err != nil {
		return domainjob.Job{}, err
	}
	if ok, reason, err := m.CanStart(ctx, j); err != nil {
		return domainjob.Job{}, err
	} else if !ok {
		return domainjob.Job{}, fmt.Errorf("%w: %s", ErrParallelLimit, reason)
	}
	return m.UpdateStatus(ctx, jobID, domainjob.StatusRunning, "", nil)
}

func (m *Manager) CancelJob(ctx context.Context, jobID string, summary string) (domainjob.Job, error) {
	return m.UpdateStatus(ctx, jobID, domainjob.StatusCancelled, summary, nil)
}

func (m *Manager) UpdateStatus(ctx context.Context, jobID string, status domainjob.Status, summary string, nextActions []string) (domainjob.Job, error) {
	j, err := m.store.GetJob(ctx, jobID)
	if err != nil {
		return domainjob.Job{}, err
	}
	if !domainjob.CanTransition(j.Status, status) {
		return domainjob.Job{}, fmt.Errorf("invalid status transition: %s -> %s", j.Status, status)
	}
	now := m.now()
	j.Status = status
	j.UpdatedAt = now
	if status == domainjob.StatusRunning && j.StartedAt == nil {
		t := now
		j.StartedAt = &t
	}
	if domainjob.IsTerminal(status) && j.FinishedAt == nil {
		t := now
		j.FinishedAt = &t
	}
	if strings.TrimSpace(summary) != "" {
		j.Summary = strings.TrimSpace(summary)
	}
	if nextActions != nil {
		j.NextActions = append([]string(nil), nextActions...)
	}
	if err := m.store.SaveJob(ctx, j); err != nil {
		return domainjob.Job{}, err
	}
	if domainjob.ShouldNotify(j) {
		if err := m.store.SaveNotification(ctx, domainjob.NewNotification(j, now)); err != nil {
			return domainjob.Job{}, err
		}
	}
	return j, nil
}

func (m *Manager) UpdateContext(ctx context.Context, shared domainjob.SharedRoleContext) error {
	if strings.TrimSpace(shared.JobID) == "" {
		return fmt.Errorf("job_id is required")
	}
	if _, err := m.store.GetJob(ctx, shared.JobID); err != nil {
		return err
	}
	shared.UpdatedAt = m.now()
	return m.store.SaveContext(ctx, shared)
}

func (m *Manager) ListJobs(ctx context.Context, filter domainjob.Filter) ([]domainjob.Job, error) {
	return m.store.ListJobs(ctx, filter)
}

func (m *Manager) GetJob(ctx context.Context, jobID string) (domainjob.Job, error) {
	return m.store.GetJob(ctx, jobID)
}

func (m *Manager) GetContext(ctx context.Context, jobID string) (domainjob.SharedRoleContext, error) {
	return m.store.GetContext(ctx, jobID)
}

func (m *Manager) ListNotifications(ctx context.Context, limit int, interruptOnly bool) ([]domainjob.Notification, error) {
	return m.store.ListNotifications(ctx, limit, interruptOnly)
}

func (m *Manager) CanStart(ctx context.Context, j domainjob.Job) (bool, string, error) {
	running, err := m.store.ListJobs(ctx, domainjob.Filter{Status: domainjob.StatusRunning, Limit: 10000})
	if err != nil {
		return false, "", err
	}
	global := 0
	sameModule := 0
	coding := 0
	research := 0
	ops := 0
	for _, item := range running {
		global++
		if j.ModuleID != "" && item.ModuleID == j.ModuleID && !j.ReadOnly {
			sameModule++
		}
		switch item.Route {
		case domainjob.RouteCode:
			coding++
		case domainjob.RouteResearch:
			research++
		case domainjob.RouteOperations:
			ops++
		}
	}
	if m.limits.Global > 0 && global >= m.limits.Global {
		return false, "global running job limit reached", nil
	}
	if m.limits.PerModule > 0 && sameModule >= m.limits.PerModule {
		return false, "module running job limit reached", nil
	}
	switch j.Route {
	case domainjob.RouteCode:
		if m.limits.CodingJobs > 0 && coding >= m.limits.CodingJobs {
			return false, "coding job limit reached", nil
		}
	case domainjob.RouteResearch:
		if m.limits.LongResearchJobs > 0 && research >= m.limits.LongResearchJobs {
			return false, "long research job limit reached", nil
		}
	case domainjob.RouteOperations:
		if m.limits.DestructiveOps > 0 && ops >= m.limits.DestructiveOps {
			return false, "operations job limit reached", nil
		}
	}
	return true, "", nil
}
