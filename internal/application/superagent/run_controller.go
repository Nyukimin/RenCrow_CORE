package superagent

import (
	"context"
	"strings"
	"sync"
	"time"
)

type RuntimeControlResult struct {
	RunID       string
	Applied     bool
	Action      string
	Reason      string
	RequestedAt time.Time
}

type RunController struct {
	mu     sync.Mutex
	active map[string]*runRegistration
	paused map[string]RuntimeControlResult
}

type runRegistration struct {
	cancel context.CancelFunc
}

func NewRunController() *RunController {
	return &RunController{
		active: make(map[string]*runRegistration),
		paused: make(map[string]RuntimeControlResult),
	}
}

func (c *RunController) RegisterRun(ctx context.Context, runID string) (context.Context, func()) {
	if c == nil || strings.TrimSpace(runID) == "" {
		return ctx, func() {}
	}
	runCtx, cancel := context.WithCancel(ctx)
	registration := &runRegistration{cancel: cancel}
	c.mu.Lock()
	c.active[runID] = registration
	c.mu.Unlock()
	return runCtx, func() {
		c.mu.Lock()
		if current, ok := c.active[runID]; ok && current == registration {
			delete(c.active, runID)
		}
		c.mu.Unlock()
		cancel()
	}
}

func (c *RunController) PauseRun(runID string, reason string) RuntimeControlResult {
	result := RuntimeControlResult{
		RunID:       strings.TrimSpace(runID),
		Action:      "none",
		Reason:      strings.TrimSpace(reason),
		RequestedAt: time.Now().UTC(),
	}
	if c == nil || result.RunID == "" {
		return result
	}
	c.mu.Lock()
	registration := c.active[result.RunID]
	if registration != nil {
		result.Applied = true
		result.Action = "cancel_requested"
	}
	c.paused[result.RunID] = result
	c.mu.Unlock()
	if registration != nil {
		registration.cancel()
	}
	return result
}

func (c *RunController) ResumeRun(runID string, reason string) RuntimeControlResult {
	result := RuntimeControlResult{
		RunID:       strings.TrimSpace(runID),
		Action:      "none",
		Reason:      strings.TrimSpace(reason),
		RequestedAt: time.Now().UTC(),
	}
	if c == nil || result.RunID == "" {
		return result
	}
	c.mu.Lock()
	if _, ok := c.paused[result.RunID]; ok {
		result.Applied = true
		result.Action = "resume_marker_cleared"
		delete(c.paused, result.RunID)
	}
	c.mu.Unlock()
	return result
}

func (c *RunController) IsPauseRequested(runID string) bool {
	if c == nil || strings.TrimSpace(runID) == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.paused[strings.TrimSpace(runID)]
	return ok
}
