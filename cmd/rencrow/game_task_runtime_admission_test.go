package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type gameTaskAdmissionSessions struct {
	mu        sync.Mutex
	sessions  []gameObserverActiveAgentSession
	source    gameObserverSessionSummary
	err       error
	sourceErr error
	calls     int
}

func (reader *gameTaskAdmissionSessions) ActiveAgentSessions(context.Context) ([]gameObserverActiveAgentSession, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.calls++
	return append([]gameObserverActiveAgentSession(nil), reader.sessions...), reader.err
}

func (reader *gameTaskAdmissionSessions) SessionSummary(context.Context, string) (gameObserverSessionSummary, error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	return reader.source, reader.sourceErr
}

type gameTaskAdmissionScheduler struct {
	mu           sync.Mutex
	groups       map[string][]string
	reserveCalls int
}

func (scheduler *gameTaskAdmissionScheduler) TryReserveGameGroup(_ context.Context, key, _ string, actors []string) (bool, error) {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	scheduler.reserveCalls++
	if existing, ok := scheduler.groups[key]; ok {
		return equalGameTaskStrings(existing, actors), nil
	}
	for _, group := range scheduler.groups {
		if gameTaskPersonasOverlap(actors, group) {
			return false, nil
		}
	}
	scheduler.groups[key] = append([]string(nil), actors...)
	return true, nil
}

func (scheduler *gameTaskAdmissionScheduler) RestoreGameGroups(groups []agentheartbeat.GameGroupReservation) error {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	for _, group := range groups {
		scheduler.groups[group.LaunchKey] = append([]string(nil), group.Actors...)
	}
	return nil
}

func (scheduler *gameTaskAdmissionScheduler) BindGameGroup(string, string) error        { return nil }
func (scheduler *gameTaskAdmissionScheduler) MarkGameGroupClosing(string, string) error { return nil }
func (scheduler *gameTaskAdmissionScheduler) ReleaseGameGroup(key string) error {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	delete(scheduler.groups, key)
	return nil
}
func (scheduler *gameTaskAdmissionScheduler) AcquireGameTaskTurn(context.Context, string, domaintask.Priority, string, string, string) (func(), error) {
	return func() {}, nil
}
func (scheduler *gameTaskAdmissionScheduler) CanLaunchLegacyGame(actors []string) (bool, error) {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	for _, group := range scheduler.groups {
		if gameTaskPersonasOverlap(actors, group) {
			return false, nil
		}
	}
	return true, nil
}

func TestGameTaskLaunchGateRejectsLegacyLaunchAfterTaskGroupClaim(t *testing.T) {
	reader := &gameTaskAdmissionSessions{}
	scheduler := &gameTaskAdmissionScheduler{groups: make(map[string][]string)}
	gate := newGameTaskRuntimeAdmission(reader, nil)
	gate.setScheduler(func() gameTaskGroupScheduler { return scheduler })
	reserved, err := gate.TryReserveTaskGroup(context.Background(), "task-key", domaintask.GameSpec{
		GameID: "survival_garden", Personas: []string{"aka", "ao", "kin", "gin"}, Turns: 120, Mode: "auto",
	})
	if err != nil || !reserved {
		t.Fatalf("Task reservation=%t err=%v", reserved, err)
	}
	if reader.calls != 1 {
		t.Fatalf("fresh active-session reads=%d, want one before Task group claim", reader.calls)
	}
	_, status, err := gate.BeginGameLaunch(context.Background(), gameLaunchRequestForActors("aka"))
	if status != http.StatusConflict || err == nil {
		t.Fatalf("legacy overlap status=%d err=%v, want conflict", status, err)
	}
}

func TestGameTaskLaunchGateRejectsObservedActiveSessionAndReaderFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sessions []gameObserverActiveAgentSession
		err      error
		wantErr  bool
	}{
		{name: "active overlap", sessions: []gameObserverActiveAgentSession{{SessionID: "sg_existing", GameID: "survival_garden", Personas: []string{"aka"}, Status: "running"}}},
		{name: "unknown snapshot", err: errors.New("observer unavailable"), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &gameTaskAdmissionSessions{sessions: tc.sessions, err: tc.err}
			scheduler := &gameTaskAdmissionScheduler{groups: make(map[string][]string)}
			gate := newGameTaskRuntimeAdmission(reader, nil)
			gate.setScheduler(func() gameTaskGroupScheduler { return scheduler })
			reserved, err := gate.TryReserveTaskGroup(context.Background(), "task-key", domaintask.GameSpec{
				GameID: "herzog_zwei", Personas: []string{"aka", "ao", "kin", "gin"}, Turns: 40, Mode: "realtime",
			})
			if reserved || (tc.wantErr != (err != nil)) || scheduler.reserveCalls != 0 {
				t.Fatalf("reservation=%t err=%v reserve_calls=%d; want conflict/error before scheduler claim", reserved, err, scheduler.reserveCalls)
			}
		})
	}
}

func TestGameTaskLaunchGateGuardsCanonicalRetryAndStartOverSourceRoster(t *testing.T) {
	for _, action := range []string{"retry", "start_over"} {
		t.Run(action, func(t *testing.T) {
			reader := &gameTaskAdmissionSessions{source: gameObserverSessionSummary{
				GameID: "survival_garden", SessionID: "sg_source", Personas: []string{"aka", "ao", "kin", "gin"}, Status: "completed",
			}}
			scheduler := &gameTaskAdmissionScheduler{groups: map[string][]string{"task-key": {"aka", "ao", "kin", "gin"}}}
			gate := newGameTaskRuntimeAdmission(reader, nil)
			gate.setScheduler(func() gameTaskGroupScheduler { return scheduler })
			release, status, err := gate.BeginGameSessionAction(context.Background(), "sg_source", action)
			if status != http.StatusConflict || err == nil || release != nil {
				t.Fatalf("release=%t status=%d err=%v, want reserved actor conflict", release != nil, status, err)
			}
		})
	}
}

func gameLaunchRequestForActors(actor string) viewer.GameLaunchRequest {
	return viewer.GameLaunchRequest{GameID: "nethack", Personas: []string{actor}}
}
