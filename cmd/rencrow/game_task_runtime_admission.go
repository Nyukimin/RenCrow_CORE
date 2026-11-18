package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type gameTaskGroupScheduler interface {
	TryReserveGameGroup(context.Context, string, string, []string) (bool, error)
	RestoreGameGroups([]agentheartbeat.GameGroupReservation) error
	BindGameGroup(string, string) error
	MarkGameGroupClosing(string, string) error
	ReleaseGameGroup(string) error
	AcquireGameTaskTurn(context.Context, string, task.Priority, string, string, string) (func(), error)
	CanLaunchLegacyGame([]string) (bool, error)
}

type gameTaskRuntimeAdmission struct {
	mu             sync.Mutex
	sessions       gameObserverActiveSessionsReader
	sessionDetails interface {
		SessionSummary(context.Context, string) (gameObserverSessionSummary, error)
	}
	tasks interface {
		ListGameTaskStates(context.Context) ([]taskmanager.GameTaskState, error)
	}
	scheduler func() gameTaskGroupScheduler
}

func newGameTaskRuntimeAdmission(sessions gameObserverActiveSessionsReader, tasks interface {
	ListGameTaskStates(context.Context) ([]taskmanager.GameTaskState, error)
}) *gameTaskRuntimeAdmission {
	admission := &gameTaskRuntimeAdmission{sessions: sessions, tasks: tasks}
	if details, ok := sessions.(interface {
		SessionSummary(context.Context, string) (gameObserverSessionSummary, error)
	}); ok {
		admission.sessionDetails = details
	}
	return admission
}

func (admission *gameTaskRuntimeAdmission) setScheduler(scheduler func() gameTaskGroupScheduler) {
	if admission == nil {
		return
	}
	admission.mu.Lock()
	admission.scheduler = scheduler
	admission.mu.Unlock()
}

// BeginGameLaunch is shared by the manual launch handler and autoplay. It
// holds the short coordination lock through the GAMES POST, preventing a Task
// from claiming actors between the legacy conflict check and GAMES reservation.
func (admission *gameTaskRuntimeAdmission) BeginGameLaunch(ctx context.Context, request viewer.GameLaunchRequest) (func(), int, error) {
	if admission == nil {
		return func() {}, 0, nil
	}
	admission.mu.Lock()
	actors := canonicalGameLaunchCheckActors(request.Personas)
	scheduler := admission.currentSchedulerLocked()
	if scheduler == nil {
		allowed, err := admission.checkDurableGroups(ctx, actors)
		admission.mu.Unlock()
		if err != nil {
			return nil, http.StatusServiceUnavailable, err
		}
		if !allowed {
			return nil, http.StatusConflict, errors.New("one or more game actors are reserved by a GAME Task")
		}
		return func() {}, 0, nil
	}
	allowed, err := scheduler.CanLaunchLegacyGame(actors)
	if err != nil {
		admission.mu.Unlock()
		return nil, http.StatusServiceUnavailable, fmt.Errorf("check GAME Task reservations: %w", err)
	}
	if !allowed {
		admission.mu.Unlock()
		return nil, http.StatusConflict, errors.New("one or more game actors are reserved by a GAME Task")
	}
	return admission.mu.Unlock, 0, nil
}

func (admission *gameTaskRuntimeAdmission) BeginGameSessionAction(ctx context.Context, sessionID, action string) (func(), int, error) {
	if admission == nil || (action != "retry" && action != "start_over") {
		return nil, http.StatusBadRequest, errors.New("game session action is invalid")
	}
	admission.mu.Lock()
	if admission.sessionDetails == nil {
		admission.mu.Unlock()
		return nil, http.StatusServiceUnavailable, errors.New("canonical GAMES source session is unavailable")
	}
	session, err := admission.sessionDetails.SessionSummary(ctx, sessionID)
	if err != nil {
		admission.mu.Unlock()
		return nil, http.StatusServiceUnavailable, fmt.Errorf("read canonical GAMES source session: %w", err)
	}
	actors := session.Personas
	if len(actors) == 0 {
		// Older session summaries may expose only one Persona even when the
		// launch had several participants. Conservatively check the full actor
		// set until the canonical source roster is available.
		actors = append([]string(nil), agentHeartbeatActorIDs...)
	}
	scheduler := admission.currentSchedulerLocked()
	var allowed bool
	if scheduler != nil {
		allowed, err = scheduler.CanLaunchLegacyGame(actors)
	} else {
		allowed, err = admission.checkDurableGroups(ctx, actors)
	}
	if err != nil {
		admission.mu.Unlock()
		return nil, http.StatusServiceUnavailable, fmt.Errorf("check GAME Task reservations: %w", err)
	}
	if !allowed {
		admission.mu.Unlock()
		return nil, http.StatusConflict, errors.New("one or more game actors are reserved by a GAME Task")
	}
	return admission.mu.Unlock, 0, nil
}

func canonicalGameLaunchCheckActors(personas []string) []string {
	actors := append([]string(nil), personas...)
	validRoster := len(actors) != 0
	seen := make(map[string]struct{}, len(actors))
	for _, actor := range actors {
		if actor == "" || strings.TrimSpace(actor) != actor || !isAgentHeartbeatActor(actor) {
			validRoster = false
		}
		if _, duplicate := seen[actor]; duplicate {
			validRoster = false
		}
		seen[actor] = struct{}{}
	}
	if !validRoster {
		// Empty means the title's launcher defaults. Use the complete canonical
		// actor set for a conservative overlap check without changing the
		// launch request. This also leaves malformed roster validation to GAMES
		// when no Task group exists.
		actors = append(actors, agentHeartbeatActorIDs...)
	}
	return actors
}

func (admission *gameTaskRuntimeAdmission) checkDurableGroups(ctx context.Context, actors []string) (bool, error) {
	if admission.tasks == nil {
		return true, nil
	}
	states, err := admission.tasks.ListGameTaskStates(ctx)
	if err != nil {
		return false, fmt.Errorf("read durable GAME Task reservations: %w", err)
	}
	for _, state := range states {
		switch state.Progress.Status {
		case task.GameExecutionLaunching, task.GameExecutionRunning, task.GameExecutionUnknown:
			if gameTaskPersonasOverlap(actors, state.Spec.Personas) {
				return false, nil
			}
		}
	}
	return true, nil
}

func (admission *gameTaskRuntimeAdmission) TryReserveTaskGroup(ctx context.Context, launchKey string, spec task.GameSpec) (bool, error) {
	if admission == nil || admission.sessions == nil {
		return false, errors.New("GAME Task admission is unavailable")
	}
	admission.mu.Lock()
	defer admission.mu.Unlock()
	scheduler := admission.currentSchedulerLocked()
	if scheduler == nil {
		return false, errors.New("GAME Task scheduler is unavailable")
	}
	sessions, err := admission.sessions.ActiveAgentSessions(ctx)
	if err != nil {
		return false, fmt.Errorf("read active GAMES sessions before Task launch: %w", err)
	}
	for _, session := range sessions {
		if gameTaskPersonasOverlap(spec.Personas, session.Personas) {
			return false, nil
		}
	}
	return scheduler.TryReserveGameGroup(ctx, launchKey, spec.GameID, append([]string(nil), spec.Personas...))
}

func (admission *gameTaskRuntimeAdmission) BindGameGroup(launchKey, sessionID string) error {
	scheduler := admission.currentScheduler()
	if scheduler == nil {
		return errors.New("GAME Task scheduler is unavailable")
	}
	return scheduler.BindGameGroup(launchKey, sessionID)
}

func (admission *gameTaskRuntimeAdmission) MarkGameGroupClosing(launchKey, sessionID string) error {
	scheduler := admission.currentScheduler()
	if scheduler == nil {
		return errors.New("GAME Task scheduler is unavailable")
	}
	return scheduler.MarkGameGroupClosing(launchKey, sessionID)
}

func (admission *gameTaskRuntimeAdmission) ReleaseGameGroup(launchKey string) error {
	scheduler := admission.currentScheduler()
	if scheduler == nil {
		return errors.New("GAME Task scheduler is unavailable")
	}
	return scheduler.ReleaseGameGroup(launchKey)
}

func (admission *gameTaskRuntimeAdmission) RestoreGameGroups(groups []agentheartbeat.GameGroupReservation) error {
	scheduler := admission.currentScheduler()
	if scheduler == nil {
		return errors.New("GAME Task scheduler is unavailable")
	}
	return scheduler.RestoreGameGroups(groups)
}

func (admission *gameTaskRuntimeAdmission) currentScheduler() gameTaskGroupScheduler {
	if admission == nil {
		return nil
	}
	admission.mu.Lock()
	defer admission.mu.Unlock()
	return admission.currentSchedulerLocked()
}

func (admission *gameTaskRuntimeAdmission) currentSchedulerLocked() gameTaskGroupScheduler {
	if admission == nil || admission.scheduler == nil {
		return nil
	}
	return admission.scheduler()
}

type gameTaskTurnAuthorization struct {
	LaunchKey string
	GameID    string
	SessionID string
}

type gameTaskTurnAuthorizationContextKey struct{}

func withGameTaskTurnAuthorization(ctx context.Context, auth gameTaskTurnAuthorization) context.Context {
	return context.WithValue(ctx, gameTaskTurnAuthorizationContextKey{}, auth)
}

func gameTaskTurnAuthorizationFromContext(ctx context.Context) (gameTaskTurnAuthorization, bool) {
	if ctx == nil {
		return gameTaskTurnAuthorization{}, false
	}
	auth, ok := ctx.Value(gameTaskTurnAuthorizationContextKey{}).(gameTaskTurnAuthorization)
	return auth, ok
}

func gameTaskControllerID(item *domainsuperagent.RunQueueItem) string {
	if item == nil || !domainsuperagent.IsGameTaskQueueItem(*item) {
		return ""
	}
	return string(item.TaskID)
}
