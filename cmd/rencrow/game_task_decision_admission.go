package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type gameTaskDecisionAdmissionProvider struct {
	delegate viewer.GameAgentDecisionProvider
	tasks    *taskmanager.Manager
	client   *gameTaskLaunchClient
	groups   interface{ BindGameGroup(string, string) error }
}

func newGameTaskDecisionAdmissionProvider(
	delegate viewer.GameAgentDecisionProvider,
	tasks *taskmanager.Manager,
	client *gameTaskLaunchClient,
	groups interface{ BindGameGroup(string, string) error },
) viewer.GameAgentDecisionProvider {
	if delegate == nil || tasks == nil || client == nil {
		return delegate
	}
	return &gameTaskDecisionAdmissionProvider{delegate: delegate, tasks: tasks, client: client, groups: groups}
}

func (provider *gameTaskDecisionAdmissionProvider) DecideGameTurn(ctx context.Context, request viewer.GameAgentDecisionRequest) (viewer.GameBrainDecision, error) {
	states, err := provider.tasks.ListGameTaskStates(ctx)
	if err != nil {
		return viewer.GameBrainDecision{}, fmt.Errorf("resolve GAME Task actor reservation: %w", err)
	}
	actor := strings.ToLower(strings.TrimSpace(request.Persona))
	var reserved *taskmanager.GameTaskState
	for index := range states {
		state := &states[index]
		// A queued or merely started controller Task has no authority over a
		// GAMES session yet. In particular, do not reject an existing legacy
		// session because an unrelated launch has only been queued.
		if state.Progress.Status != domaintask.GameExecutionLaunching &&
			state.Progress.Status != domaintask.GameExecutionRunning &&
			state.Progress.Status != domaintask.GameExecutionUnknown {
			continue
		}
		if !containsGamePersona(state.Spec.Personas, actor) {
			continue
		}
		if reserved != nil {
			return viewer.GameBrainDecision{}, errors.New("game actor has multiple outstanding Task reservations")
		}
		reserved = state
	}
	if reserved == nil {
		terminalSession, err := provider.tasks.HasTerminalGameTaskSession(ctx, request.GameID, request.SessionID, actor)
		if err != nil {
			return viewer.GameBrainDecision{}, fmt.Errorf("resolve terminal GAME Task session identity: %w", err)
		}
		if terminalSession {
			return viewer.GameBrainDecision{}, errors.New("GAMES session belongs to a terminal GAME Task")
		}
		return provider.delegate.DecideGameTurn(ctx, request)
	}
	if reserved.Spec.GameID != request.GameID {
		return viewer.GameBrainDecision{}, errors.New("game actor is reserved by a different GAME Task")
	}
	if reserved.Progress.SessionID != "" && reserved.Progress.SessionID != request.SessionID {
		return viewer.GameBrainDecision{}, errors.New("GAMES session does not match the reserved GAME Task session")
	}
	if reserved.Progress.Status != domaintask.GameExecutionLaunching && reserved.Progress.Status != domaintask.GameExecutionRunning && reserved.Progress.Status != domaintask.GameExecutionUnknown {
		return viewer.GameBrainDecision{}, errors.New("GAME Task is not authorized for an Agent turn")
	}
	receipt, err := provider.client.receipt(ctx, reserved.Progress.LaunchKey)
	if err != nil {
		return viewer.GameBrainDecision{}, fmt.Errorf("resolve GAME Task launch receipt before actor admission: %w", err)
	}
	if receipt.Validate(reserved.Spec, reserved.Progress.LaunchKey) != nil ||
		receipt.Request.GameID != request.GameID || receipt.SessionID != request.SessionID ||
		(receipt.Status != domaintask.GameExecutionLaunching && receipt.Status != domaintask.GameExecutionRunning) {
		return viewer.GameBrainDecision{}, errors.New("GAMES receipt does not authorize this Agent turn")
	}
	if _, err := provider.tasks.RecordGameLaunchReceipt(ctx, reserved.Task.TaskID, receipt); err != nil {
		return viewer.GameBrainDecision{}, fmt.Errorf("bind GAME Task to GAMES session: %w", err)
	}
	if provider.groups == nil {
		return viewer.GameBrainDecision{}, errors.New("GAME Task group scheduler is unavailable")
	}
	if err := provider.groups.BindGameGroup(reserved.Progress.LaunchKey, receipt.SessionID); err != nil {
		return viewer.GameBrainDecision{}, fmt.Errorf("bind GAME Task scheduler group: %w", err)
	}
	ctx = withGameTaskTurnAuthorization(ctx, gameTaskTurnAuthorization{
		LaunchKey: reserved.Progress.LaunchKey, GameID: request.GameID, SessionID: request.SessionID,
	})
	return provider.delegate.DecideGameTurn(ctx, request)
}

func containsGamePersona(personas []string, actor string) bool {
	for _, persona := range personas {
		if persona == actor {
			return true
		}
	}
	return false
}
