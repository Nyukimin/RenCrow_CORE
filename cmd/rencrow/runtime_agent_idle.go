package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

const gameAutoplayIdleOwnerActor = "mio"

type gameAutoplayRunOnce func(context.Context) time.Duration

type gameAutoplayIdleSourceState struct {
	mu          sync.Mutex
	runOnce     gameAutoplayRunOnce
	now         func() time.Time
	nextCheckAt time.Time
}

func gameAutoplayIdleSource(service *viewer.GameAutoplayService) agentheartbeat.Source {
	if service == nil {
		return nil
	}
	return newGameAutoplayIdleSource(service.RunOnceIdle, time.Now)
}

func newGameAutoplayIdleSource(runOnce gameAutoplayRunOnce, now func() time.Time) agentheartbeat.Source {
	if runOnce == nil {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	state := &gameAutoplayIdleSourceState{runOnce: runOnce, now: now}
	return func(ctx context.Context, actor string) (agentheartbeat.Work, bool, error) {
		if actor != gameAutoplayIdleOwnerActor {
			return agentheartbeat.Work{}, false, nil
		}
		if err := ctx.Err(); err != nil {
			return agentheartbeat.Work{}, false, err
		}

		createdAt := state.now().UTC()
		state.mu.Lock()
		if state.nextCheckAt.After(createdAt) {
			state.mu.Unlock()
			return agentheartbeat.Work{}, false, nil
		}
		state.mu.Unlock()

		return agentheartbeat.Work{
			ID:        "games:autoplay",
			Priority:  domaintask.PriorityLow,
			CreatedAt: createdAt,
			Run: func(runCtx context.Context, stop <-chan struct{}) error {
				select {
				case <-runCtx.Done():
					return nil
				case <-stop:
					return nil
				default:
				}

				activityCtx, cancel := context.WithCancel(runCtx)
				stopWatchDone := make(chan struct{})
				go func() {
					defer close(stopWatchDone)
					select {
					case <-stop:
						cancel()
					case <-activityCtx.Done():
					}
				}()
				delay := state.runOnce(activityCtx)
				cancel()
				<-stopWatchDone
				if delay <= 0 {
					delay = time.Hour
				}
				state.mu.Lock()
				state.nextCheckAt = state.now().Add(delay)
				state.mu.Unlock()
				return nil
			},
		}, true, nil
	}
}

type heartbeatGameDecisionProvider struct {
	provider        viewer.GameAgentDecisionProvider
	admission       func() conversationruntime.ActorAdmission
	admissionNeeded bool
}

func newHeartbeatGameDecisionProvider(
	provider viewer.GameAgentDecisionProvider,
	admission func() conversationruntime.ActorAdmission,
	admissionNeeded bool,
) viewer.GameAgentDecisionProvider {
	if provider == nil {
		return nil
	}
	return heartbeatGameDecisionProvider{
		provider:        provider,
		admission:       admission,
		admissionNeeded: admissionNeeded,
	}
}

func (p heartbeatGameDecisionProvider) DecideGameTurn(ctx context.Context, request viewer.GameAgentDecisionRequest) (viewer.GameBrainDecision, error) {
	var admission conversationruntime.ActorAdmission
	if p.admission != nil {
		admission = p.admission()
	}
	if admission == nil {
		if p.admissionNeeded {
			return viewer.GameBrainDecision{}, fmt.Errorf("heartbeat actor admission is required for game persona %q", request.Persona)
		}
		return p.provider.DecideGameTurn(ctx, request)
	}

	actor := strings.ToLower(strings.TrimSpace(request.Persona))
	release, err := admission.Acquire(ctx, actor, domaintask.PriorityLow)
	if err != nil {
		return viewer.GameBrainDecision{}, fmt.Errorf("acquire game turn actor %q: %w", actor, err)
	}
	if release == nil {
		return viewer.GameBrainDecision{}, fmt.Errorf("acquire game turn actor %q: admission returned no release", actor)
	}
	defer release()
	return p.provider.DecideGameTurn(ctx, request)
}
