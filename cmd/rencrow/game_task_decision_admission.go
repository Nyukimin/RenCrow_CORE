package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type gameTaskDecisionAdmissionProvider struct {
	delegate viewer.GameAgentDecisionProvider
	tasks    *taskmanager.Manager
	client   *gameTaskLaunchClient
	groups   interface{ BindGameGroup(string, string) error }
	ownerID  string
}

func newGameTaskDecisionAdmissionProvider(
	delegate viewer.GameAgentDecisionProvider,
	tasks *taskmanager.Manager,
	client *gameTaskLaunchClient,
	groups interface{ BindGameGroup(string, string) error },
	ownerID string,
) viewer.GameAgentDecisionProvider {
	if delegate == nil || tasks == nil || client == nil {
		return delegate
	}
	return &gameTaskDecisionAdmissionProvider{delegate: delegate, tasks: tasks, client: client, groups: groups, ownerID: strings.TrimSpace(ownerID)}
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
		if provider.ownerID == "" {
			return viewer.GameBrainDecision{}, errors.New("configured Task owner is unavailable for a legacy GAMES session")
		}
		terminalSession, err := provider.tasks.HasTerminalGameTaskSession(ctx, request.GameID, request.SessionID, actor)
		if err != nil {
			return viewer.GameBrainDecision{}, fmt.Errorf("resolve terminal GAME Task session identity: %w", err)
		}
		if terminalSession {
			return viewer.GameBrainDecision{}, errors.New("GAMES session belongs to a terminal GAME Task")
		}
		admission, ok := provider.groups.(*gameTaskRuntimeAdmission)
		if !ok || admission.sessions == nil || admission.sessionDetails == nil {
			return viewer.GameBrainDecision{}, errors.New("canonical GAMES session observer is unavailable for a legacy turn")
		}
		session, err := admission.sessionDetails.SessionSummary(ctx, request.SessionID)
		if err != nil {
			return viewer.GameBrainDecision{}, fmt.Errorf("resolve exact legacy GAMES session before actor admission: %w", err)
		}
		if err := validateGameObserverSessionSummary(session); err != nil || session.GameID != request.GameID ||
			session.SessionID != request.SessionID || session.Status != "running" {
			return viewer.GameBrainDecision{}, errors.New("observer session does not authorize this legacy Agent turn")
		}
		personas := append([]string(nil), session.Personas...)
		if len(personas) == 0 && session.Persona != "" {
			personas = []string{session.Persona}
		}
		if !containsStringExact(personas, actor) {
			return viewer.GameBrainDecision{}, errors.New("observer session roster does not authorize this Agent persona")
		}
		participants := make([]domaintask.ActivityParticipant, 0, len(personas))
		for _, persona := range personas {
			participants = append(participants, domaintask.ActivityParticipant{ActorID: persona, Role: "player"})
		}
		parent, err := provider.tasks.EnsureExternalGameActivity(ctx, provider.ownerID, session.GameID, session.SessionID, "playing", participants)
		if err != nil {
			return viewer.GameBrainDecision{}, fmt.Errorf("adopt exact observer-confirmed legacy game session: %w", err)
		}
		if err := bindLegacyGameParticipant(ctx, provider.tasks, parent, actor); err != nil {
			return viewer.GameBrainDecision{}, err
		}
		ctx = withGameTurnExecution(ctx, gameTurnExecution{TaskID: parent.TaskID, GameID: session.GameID, SessionID: session.SessionID, ActorID: actor})
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
	ctx = withGameTurnExecution(ctx, gameTurnExecution{
		TaskID: reserved.Task.TaskID, GameID: request.GameID, SessionID: request.SessionID, ActorID: actor,
	})
	return provider.delegate.DecideGameTurn(ctx, request)
}

func bindLegacyGameParticipant(ctx context.Context, tasks *taskmanager.Manager, parent domaintask.Task, actorID string) error {
	if tasks == nil || parent.TaskID == "" || parent.Activity == nil || parent.Activity.Kind != domaintask.ActivityKindGame || parent.Activity.Game == nil {
		return domaintask.ErrCurrentActivityInvalid
	}
	current, err := tasks.SnapshotCurrentActivities(ctx, []string{actorID})
	if err != nil {
		return err
	}
	if current[actorID].Task.TaskID == parent.TaskID {
		return nil
	}
	prior := current[actorID]
	if prior.Task.Activity == nil || !prior.Task.Activity.Passive || prior.Task.Activity.Kind == domaintask.ActivityKindGame {
		return fmt.Errorf("actor %s has a foreground activity that cannot be replaced for a GAMES turn: %w", actorID, domaintask.ErrCurrentActivityConflict)
	}
	role := "player"
	for _, participant := range parent.Activity.Participants {
		if participant.ActorID == actorID && participant.Role != "" {
			role = participant.Role
			break
		}
	}
	metadata := domaintask.ActivityMetadata{
		Kind: domaintask.ActivityKindGame, Label: parent.Activity.Label, Phase: parent.Activity.Phase, Passive: parent.Activity.Passive,
		Game: parent.Activity.Game, Participants: []domaintask.ActivityParticipant{{ActorID: actorID, Role: role}},
	}
	if _, err := tasks.BindCurrentActivity(ctx, actorID, prior.Task.TaskID, prior.Participant.Revision, parent.TaskID, metadata); err != nil {
		return fmt.Errorf("bind confirmed legacy game activity for actor %s: %w", actorID, err)
	}
	return nil
}

type gameTurnExecution struct {
	TaskID    modulecore.TaskID
	GameID    string
	SessionID string
	ActorID   string
}

type gameTurnExecutionContextKey struct{}

func withGameTurnExecution(ctx context.Context, execution gameTurnExecution) context.Context {
	return context.WithValue(ctx, gameTurnExecutionContextKey{}, execution)
}

type gameTurnExecutionProvider struct {
	delegate viewer.GameAgentDecisionProvider
	tasks    *taskmanager.Manager
}

func newGameTurnExecutionProvider(delegate viewer.GameAgentDecisionProvider, tasks *taskmanager.Manager) viewer.GameAgentDecisionProvider {
	if delegate == nil || tasks == nil {
		return delegate
	}
	return &gameTurnExecutionProvider{delegate: delegate, tasks: tasks}
}

func (provider *gameTurnExecutionProvider) DecideGameTurn(ctx context.Context, request viewer.GameAgentDecisionRequest) (viewer.GameBrainDecision, error) {
	execution, ok := ctx.Value(gameTurnExecutionContextKey{}).(gameTurnExecution)
	if !ok || execution.TaskID.Validate() != nil || execution.GameID != request.GameID || execution.SessionID != request.SessionID || execution.ActorID != strings.ToLower(strings.TrimSpace(request.Persona)) {
		return viewer.GameBrainDecision{}, errors.New("exact Game activity execution identity is unavailable")
	}
	snapshot, err := provider.tasks.SnapshotCurrentActivities(ctx, []string{execution.ActorID})
	if err != nil {
		return viewer.GameBrainDecision{}, fmt.Errorf("read current Game activity before turn Run: %w", err)
	}
	current := snapshot[execution.ActorID]
	if current.Task.TaskID != execution.TaskID || current.Task.Activity == nil || current.Task.Activity.Kind != domaintask.ActivityKindGame ||
		current.Task.Activity.Game == nil || current.Task.Activity.Game.GameID != execution.GameID || current.Task.Activity.Game.SessionID != execution.SessionID {
		return viewer.GameBrainDecision{}, domaintask.ErrCurrentActivityConflict
	}
	task, run, err := provider.tasks.CreateAndStartCurrentGameTurnRun(ctx, execution.TaskID, execution.ActorID, current.Participant.Revision, execution.GameID, execution.SessionID,
		domaintask.Task{
			Title: "Game turn", OwnerID: execution.ActorID, Assignee: execution.ActorID,
			ParentTaskID: execution.TaskID, Route: domaintask.RouteGeneral, Priority: domaintask.PriorityNormal, ReadOnly: true,
		}, domaintask.SharedRoleContext{CurrentPlan: "execute the observer-authorized Agent game turn"})
	if err != nil {
		return viewer.GameBrainDecision{}, fmt.Errorf("start child Task for current Game turn: %w", err)
	}
	identityCtx, err := domainexecution.WithIdentity(ctx, task.TaskID, run.RunID, modulecore.NewTraceID())
	if err != nil {
		return viewer.GameBrainDecision{}, provider.tasks.AbortStartedRun(ctx, task.TaskID, run.RunID, execution.ActorID, err)
	}
	decision, decisionErr := provider.delegate.DecideGameTurn(identityCtx, request)
	closeErr := provider.tasks.CloseStartedRun(identityCtx, task.TaskID, run.RunID, execution.ActorID,
		taskmanager.RunOutcomeOf(decisionErr, "GAMES Agent turn completed"), decisionErr)
	if closeErr != nil {
		return viewer.GameBrainDecision{}, fmt.Errorf("close GAMES turn Run: %w", closeErr)
	}
	return decision, decisionErr
}

func containsGamePersona(personas []string, actor string) bool {
	for _, persona := range personas {
		if persona == actor {
			return true
		}
	}
	return false
}
