package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	routineRestModeReading       = "reading"
	routineRestModeConversation  = "conversation"
	routineRestModeTea           = "tea"
	routineRestSeatPurposePrefix = "rest-seat-"
	routineRestUtteranceMax      = 4000
)

var (
	errRoutineRestUnavailable       = errors.New("a confirmed shared rest seat and an executable rest activity are unavailable")
	errRoutineRestMoveRequired      = errors.New("rest requires arrival in a configured shared seat")
	errRoutineRestSeatOccupied      = errors.New("the requested rest seat is confirmed occupied by another actor")
	errRoutineRestSeatMoveExhausted = errors.New("repeated rest seat moves were rejected without a confirmed competing occupant")
)

type routineRestProgress struct {
	Scope             agentroutine.RelaxRoomScope          `json:"scope"`
	Mode              string                               `json:"mode,omitempty"`
	ParticipantID     string                               `json:"participant_id,omitempty"`
	TeaVirtual        bool                                 `json:"tea_virtual,omitempty"`
	TeaStartedAt      time.Time                            `json:"tea_started_at,omitempty"`
	TeaCompletedAt    time.Time                            `json:"tea_completed_at,omitempty"`
	TeaReflection     string                               `json:"tea_reflection,omitempty"`
	PrivateReflection string                               `json:"private_reflection,omitempty"`
	FailureReason     string                               `json:"failure_reason,omitempty"`
	StartedAt         time.Time                            `json:"started_at,omitempty"`
	CompletedAt       time.Time                            `json:"completed_at,omitempty"`
	Seats             map[string]routineRestSeatCheckpoint `json:"seats,omitempty"`
	Read              *routineRestReadProgress             `json:"read,omitempty"`
	Conversation      *routineRestConversationProgress     `json:"conversation,omitempty"`
	PlannedAt         time.Time                            `json:"planned_at,omitempty"`
}

type routineRestSeatCheckpoint struct {
	RoomID           string    `json:"room_id"`
	SlotID           string    `json:"slot_id"`
	RequestedAt      time.Time `json:"requested_at,omitempty"`
	MoveActivityID   string    `json:"move_activity_id,omitempty"`
	ObservedStateSeq uint64    `json:"observed_state_seq,omitempty"`
	ConfirmedAt      time.Time `json:"confirmed_at,omitempty"`
	Confirmed        bool      `json:"confirmed,omitempty"`
}

type routineRestReadProgress struct {
	Title       string    `json:"title"`
	Author      string    `json:"author"`
	SourceURL   string    `json:"source_url"`
	RightsURL   string    `json:"rights_url"`
	BodyHash    string    `json:"body_hash"`
	BodyRunes   int       `json:"body_runes"`
	StartRune   int       `json:"start_rune"`
	EndRune     int       `json:"end_rune"`
	NextRune    int       `json:"next_rune"`
	Reflection  string    `json:"reflection,omitempty"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
}

type routineRestConversationProgress struct {
	StartedAt   time.Time `json:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
	Turns       int       `json:"turns,omitempty"`
}

type routineRestFactReference struct {
	Mode                    string                               `json:"mode"`
	TeaVirtual              bool                                 `json:"tea_virtual,omitempty"`
	TeaStartedAt            time.Time                            `json:"tea_started_at,omitempty"`
	TeaCompletedAt          time.Time                            `json:"tea_completed_at,omitempty"`
	Seats                   map[string]routineRestSeatCheckpoint `json:"seats,omitempty"`
	Read                    *routineRestReadProgress             `json:"read,omitempty"`
	Conversation            []map[string]string                  `json:"conversation,omitempty"`
	ConversationStartedAt   time.Time                            `json:"conversation_started_at,omitempty"`
	ConversationCompletedAt time.Time                            `json:"conversation_completed_at,omitempty"`
	TeaReflection           string                               `json:"tea_reflection,omitempty"`
	PrivateReflection       string                               `json:"private_reflection,omitempty"`
	FailureReason           string                               `json:"failure_reason,omitempty"`
}

func (r *agentRoutineRuntime) routineRestEffectVerified(progress routineProgress) bool {
	if !progress.EffectCompleted || progress.Rest == nil || progress.Rest.CompletedAt.IsZero() ||
		!progress.CompletedAt.Equal(progress.Rest.CompletedAt) || progress.World == nil ||
		progress.World.RelaxRoomID == "" || progress.World.RelaxObservedAt.IsZero() {
		return false
	}
	if progress.Rest.Scope == agentroutine.RelaxRoomPrivate {
		return !progress.Rest.StartedAt.IsZero() &&
			strings.TrimSpace(progress.Rest.PrivateReflection) != ""
	}
	if progress.Rest.Scope != agentroutine.RelaxRoomShared {
		return false
	}
	reference, err := r.routineRestFactReference(progress)
	if err != nil {
		return false
	}
	switch reference.Mode {
	case routineRestModeReading:
		return progress.Rest.Read != nil && !progress.Rest.Read.StartedAt.IsZero() &&
			progress.Rest.Read.CompletedAt.After(progress.Rest.Read.StartedAt)
	case routineRestModeTea:
		return progress.Rest.TeaVirtual && !progress.Rest.TeaStartedAt.IsZero() &&
			progress.Rest.TeaCompletedAt.After(progress.Rest.TeaStartedAt) &&
			strings.TrimSpace(progress.Rest.TeaReflection) != ""
	case routineRestModeConversation:
		return progress.Rest.Conversation != nil && !progress.Rest.Conversation.StartedAt.IsZero() &&
			progress.Rest.Conversation.CompletedAt.After(progress.Rest.Conversation.StartedAt) &&
			len(progress.Actors) == 2 && progress.Actors[0].Output != "" && progress.Actors[1].Output != ""
	default:
		return false
	}
}

func (r *agentRoutineRuntime) executeRelaxation(ctx context.Context, stop <-chan struct{}, state agentroutine.State, progress routineProgress, prepared ...[]routinePreparedRun) (agentroutine.State, routineProgress, error) {
	var runs []routinePreparedRun
	if len(prepared) > 0 {
		runs = prepared[0]
	}
	if progress.Rest == nil || progress.Rest.Scope == agentroutine.RelaxRoomUnknown {
		return state, progress, errRoutineRestUnavailable
	}
	if progress.Rest.Scope == agentroutine.RelaxRoomShared {
		return r.executeRoutineRestActivity(ctx, stop, state, progress, runs)
	}
	if progress.Rest.Scope != agentroutine.RelaxRoomPrivate {
		return state, progress, errRoutineRestUnavailable
	}
	state, progress, err := r.ensureRoutinePrivateRelaxationRoom(ctx, stop, state, progress)
	if err != nil {
		return state, progress, err
	}
	return r.executeRoutinePrivateRoomReflection(ctx, stop, state, progress, runs)
}

// Shared room presence alone is never evidence that a new rest activity completed.
// Private room relaxation retains its existing policy through confirmPrivateRoomRelaxation.
func (r *agentRoutineRuntime) confirmInPlaceRelaxation(ctx context.Context, state agentroutine.State, progress routineProgress) (agentroutine.State, routineProgress, error) {
	if progress.Rest == nil || progress.Rest.Scope != agentroutine.RelaxRoomPrivate {
		return state, progress, errRoutineRestUnavailable
	}
	return r.confirmPrivateRoomRelaxation(ctx, state, progress)
}

func (r *agentRoutineRuntime) confirmPrivateRoomRelaxation(ctx context.Context, state agentroutine.State, progress routineProgress) (agentroutine.State, routineProgress, error) {
	return r.ensureRoutinePrivateRelaxationRoom(ctx, nil, state, progress)
}

func (r *agentRoutineRuntime) ensureRoutinePrivateRelaxationRoom(ctx context.Context, stop <-chan struct{}, state agentroutine.State, progress routineProgress) (agentroutine.State, routineProgress, error) {
	if r.world == nil || state.Pending == nil || progress.Rest == nil || progress.Rest.Scope != agentroutine.RelaxRoomPrivate {
		return state, progress, errRoutineRestUnavailable
	}
	fresh, _, err := r.planFresh(ctx, state.Pending.ActorID, true, nil)
	if err != nil || fresh.Kind != agentroutine.ActivityRelax || fresh.RelaxRoom != agentroutine.RelaxRoomPrivate {
		return state, progress, errRoutineRestUnavailable
	}
	roomID := r.routineWorldRelaxRoom(state.Pending.ActorID, fresh.RelaxRoom)
	if roomID == "" {
		return state, progress, errRoutineRestUnavailable
	}
	_, actor, err := r.routineWorldFreshActor(ctx, state.Pending.ActorID)
	if err != nil {
		return state, progress, err
	}
	if actor.RoomID != roomID || actor.ActivityKind == "move" {
		if r.worldLife == nil {
			return state, progress, errRoutineRestMoveRequired
		}
		state, progress, _, err = r.ensureRoutineWorldArrival(ctx, stop, state, progress, state.Pending.ActorID,
			roomID, "", "relax-room", routineWorldMoveWait)
		if err != nil {
			return state, progress, err
		}
	}
	read, actor, err := r.routineWorldFreshActor(ctx, state.Pending.ActorID)
	if err != nil {
		return state, progress, err
	}
	if !actor.Present || actor.RoomID != roomID || actor.ActivityID != "" || actor.ActivityKind != "" {
		return state, progress, errRoutineRestMoveRequired
	}
	if progress.World == nil {
		progress.World = &routineWorldProgress{}
	}
	progress.World.RelaxRoomID = roomID
	progress.World.RelaxObservedAt = read.readAt.UTC()
	return r.saveProgress(ctx, state, "private_rest_room_confirmed", progress)
}

func (r *agentRoutineRuntime) executeRoutinePrivateRoomReflection(ctx context.Context, stop <-chan struct{}, state agentroutine.State, progress routineProgress, prepared []routinePreparedRun) (agentroutine.State, routineProgress, error) {
	if state.Pending == nil || progress.Rest == nil || progress.Rest.Scope != agentroutine.RelaxRoomPrivate ||
		len(progress.Actors) != 1 || len(prepared) != 1 || r.workers[state.Pending.ActorID] == nil {
		return state, progress, errRoutineRestUnavailable
	}
	_, actor, err := r.routineWorldFreshActor(ctx, state.Pending.ActorID)
	if err != nil || !actor.Present || actor.RoomID != progress.World.RelaxRoomID ||
		actor.ActivityID != "" || actor.ActivityKind != "" {
		return state, progress, errRoutineRestMoveRequired
	}
	if progress.Rest.StartedAt.IsZero() {
		progress.Rest.StartedAt = r.now().UTC()
		state, progress, err = r.saveProgress(ctx, state, "private_rest_reflection_started", progress)
		if err != nil {
			return state, progress, err
		}
	}
	if progress.Rest.PrivateReflection == "" {
		if routineStopRequested(ctx, stop) {
			return state, progress, errAgentRoutineStopped
		}
		prompt := fmt.Sprintf("あなたは%sです。今いる自室で短い静かな休憩をしています。実際にしていない物理的な行動を事実のように述べず、今の気持ちや考えを一人称で短く振り返ってください。", state.Pending.ActorID)
		output, err := r.executePrivateWorker(ctx, prepared[0], state.Pending.ActorID, prompt)
		if err != nil {
			return state, progress, err
		}
		reflection, err := boundedRoutineRestOutput(output, routineRestUtteranceMax)
		if err != nil {
			return state, progress, err
		}
		progress.Rest.PrivateReflection = reflection
		state, progress, err = r.saveProgress(ctx, state, "private_rest_reflection_saved", progress)
		if err != nil {
			return state, progress, err
		}
	}
	progress.Rest.CompletedAt = r.now().UTC()
	progress.CompletedAt = progress.Rest.CompletedAt
	progress.EffectCompleted = true
	return r.saveProgress(ctx, state, "private_rest_reflection_completed", progress)
}

func (r *agentRoutineRuntime) prepareRoutineRestPlan(ctx context.Context, state agentroutine.State, progress routineProgress) (agentroutine.State, routineProgress, error) {
	if state.Pending == nil || state.Pending.Kind != agentroutine.ActivityRelax || r == nil || r.world == nil || r.worldLife == nil {
		return state, progress, errRoutineRestUnavailable
	}
	if progress.Rest == nil {
		progress.Rest = &routineRestProgress{}
	}
	reservedParticipants := []string(nil)
	conversationStarted := progress.Rest.Conversation != nil && !progress.Rest.Conversation.StartedAt.IsZero()
	if conversationStarted && progress.Rest.ParticipantID != "" {
		reservedParticipants = []string{progress.Rest.ParticipantID}
	}
	fresh, now, err := r.planFresh(ctx, state.Pending.ActorID, true, reservedParticipants)
	if err != nil {
		return state, progress, err
	}
	if fresh.Kind != agentroutine.ActivityRelax || fresh.RelaxRoom != agentroutine.RelaxRoomShared {
		return state, progress, errors.New("shared rest is no longer eligible under fresh routine policy")
	}
	if progress.Rest.Scope == agentroutine.RelaxRoomUnknown {
		progress.Rest.Scope = fresh.RelaxRoom
	}
	if progress.Rest.Scope != agentroutine.RelaxRoomShared {
		return state, progress, errors.New("private-room rest is outside the shared-seat activity")
	}
	mode := progress.Rest.Mode
	switch mode {
	case "", routineRestModeReading, routineRestModeConversation, routineRestModeTea:
	default:
		return state, progress, errors.New("persisted rest activity mode is invalid")
	}
	participantID := progress.Rest.ParticipantID
	if participantID == "" && len(fresh.Participants) == 1 {
		participantID = fresh.Participants[0]
	}
	if !conversationStarted && participantID != "" && !containsRoutineActor(fresh.Participants, participantID) {
		if err := r.dropRoutineRestParticipant(ctx, &progress); err != nil {
			return state, progress, err
		}
		mode, participantID = "", ""
		progress.Rest.Seats = nil
	}

	read, err := r.readSnapshotFresh(ctx)
	if err != nil {
		return state, progress, err
	}
	planStillPossible := r.restSeatPlanStillPossible(read.snapshot, *state.Pending, progress)
	if planStillPossible {
		return state, progress, nil
	}
	if mode == routineRestModeConversation &&
		(!isAgentHeartbeatActor(participantID) || r.workers[participantID] == nil) {
		return state, progress, errRoutineRestUnavailable
	}
	worldHostID, ok := r.routineWorldActorID(state.Pending.ActorID)
	if !ok {
		return state, progress, errors.New("rest actor has no exact authenticated WORLD binding")
	}
	hostSeats := livingRestSeatCandidates(read.snapshot, worldHostID)
	var peerSeats []livingSeatActivity
	if participantID != "" && isAgentHeartbeatActor(participantID) && r.workers[participantID] != nil {
		worldPeerID, peerOK := r.routineWorldActorID(participantID)
		if peerOK {
			peerSeats = livingRestSeatCandidates(read.snapshot, worldPeerID)
		}
	}
	if mode == routineRestModeConversation && !conversationStarted {
		canSeatTogether := false
		for _, hostSeat := range hostSeats {
			if routineRestSeatAllowed(r, hostSeat) {
				if _, found := routineRestPeerSeat(peerSeats, hostSeat); found {
					canSeatTogether = true
					break
				}
			}
		}
		if !canSeatTogether {
			if err := r.dropRoutineRestParticipant(ctx, &progress); err != nil {
				return state, progress, err
			}
			mode, participantID = "", ""
			progress.Rest.Seats = nil
		}
	}
	for _, hostSeat := range hostSeats {
		if !routineRestSeatAllowed(r, hostSeat) {
			continue
		}
		if mode == routineRestModeReading && hostSeat.room != "living" {
			continue
		}
		if mode == routineRestModeTea && hostSeat.room != "dining" {
			continue
		}
		peerSeat, hasPeerSeat := routineRestPeerSeat(peerSeats, hostSeat)
		candidateMode := mode
		if candidateMode == "" {
			switch {
			case hasPeerSeat && participantID != "" && r.workers[participantID] != nil:
				candidateMode = routineRestModeConversation
			case hostSeat.room == "living":
				candidateMode = routineRestModeReading
			case hostSeat.room == "dining":
				candidateMode = routineRestModeTea
			}
		}
		if candidateMode == routineRestModeConversation && (!hasPeerSeat || participantID == "" || r.workers[participantID] == nil) {
			continue
		}
		if candidateMode == routineRestModeReading && hostSeat.room != "living" {
			continue
		}
		if candidateMode == routineRestModeTea && hostSeat.room != "dining" {
			continue
		}
		if mode != "" && candidateMode != mode {
			continue
		}

		progress.Rest.Mode = candidateMode
		progress.Rest.TeaVirtual = candidateMode == routineRestModeTea ||
			(candidateMode == routineRestModeConversation && hostSeat.room == "dining")
		if candidateMode == routineRestModeConversation {
			progress.Rest.ParticipantID = participantID
		} else {
			progress.Rest.ParticipantID = ""
			if candidateMode == routineRestModeReading {
				progress.Rest.TeaVirtual = false
			}
		}
		progress.Rest.Seats = map[string]routineRestSeatCheckpoint{
			state.Pending.ActorID: {RoomID: hostSeat.room, SlotID: hostSeat.slot, RequestedAt: now.UTC(), ObservedStateSeq: read.snapshot.StateSeq},
		}
		if candidateMode == routineRestModeConversation {
			progress.Rest.Seats[participantID] = routineRestSeatCheckpoint{
				RoomID: peerSeat.room, SlotID: peerSeat.slot, RequestedAt: now.UTC(), ObservedStateSeq: read.snapshot.StateSeq,
			}
			if err := r.ensureRoutineRestActorRun(state.Pending, &progress, participantID); err != nil {
				return state, progress, err
			}
		} else if err := r.dropRoutineRestParticipant(ctx, &progress); err != nil {
			return state, progress, err
		}
		progress.Rest.PlannedAt = now.UTC()
		return r.saveProgress(ctx, state, "rest_seats_requested", progress)
	}
	return state, progress, errRoutineRestUnavailable
}

func (r *agentRoutineRuntime) dropRoutineRestParticipant(ctx context.Context, progress *routineProgress) error {
	if progress == nil || progress.Rest == nil {
		return errRoutineRestUnavailable
	}
	if progress.Rest.Conversation != nil && !progress.Rest.Conversation.StartedAt.IsZero() ||
		!progress.Rest.TeaStartedAt.IsZero() {
		return errors.New("started rest activity cannot change participants")
	}
	if len(progress.Actors) <= 1 {
		return nil
	}
	if len(progress.Actors) != 2 {
		return errors.New("unstarted rest activity has an unexpected actor count")
	}
	peer := progress.Actors[1]
	if peer.TaskID == "" {
		return errors.New("unstarted rest companion has no canonical Task ID")
	}
	if r.tasks == nil {
		if peer.RunID != "" {
			return errors.New("unstarted rest companion Run cannot be resolved without TaskManager")
		}
		progress.Actors = progress.Actors[:1]
		return nil
	}
	task, err := r.tasks.Get(ctx, peer.TaskID)
	if errors.Is(err, domaintask.ErrNotFound) {
		if peer.RunID != "" {
			return errors.New("unstarted rest companion Run exists without its canonical Task")
		}
		progress.Actors = progress.Actors[:1]
		return nil
	}
	if err != nil {
		return fmt.Errorf("read unstarted rest companion Task: %w", err)
	}
	if task.TaskID != peer.TaskID || task.OwnerID != r.ownerID ||
		task.ModuleID != agentRoutineModuleID || task.Assignee != peer.ActorID ||
		task.Route != domaintask.RouteCHAT {
		return errors.New("unstarted rest companion Task does not match its exact owner and actor")
	}
	runs, err := r.tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: peer.TaskID})
	if err != nil {
		return fmt.Errorf("list unstarted rest companion Runs: %w", err)
	}
	if len(runs) > 0 {
		latest := runs[len(runs)-1]
		if peer.RunID != "" && latest.RunID != peer.RunID {
			return errors.New("unstarted rest companion Run ID changed before cancellation")
		}
		if task.Status == domaintask.StatusCancelled && latest.Status == domaintask.RunStatusCancelled {
			progress.Actors = progress.Actors[:1]
			return nil
		}
		if domaintask.IsTerminal(task.Status) ||
			(latest.Status != domaintask.RunStatusRunning && latest.Status != domaintask.RunStatusWaiting) {
			return fmt.Errorf("unstarted rest companion is not safely cancellable: task=%s run=%s", task.Status, latest.Status)
		}
	} else if domaintask.IsTerminal(task.Status) {
		if task.Status == domaintask.StatusCancelled {
			progress.Actors = progress.Actors[:1]
			return nil
		}
		return fmt.Errorf("unstarted rest companion Task is already terminal: %s", task.Status)
	}
	cancelled, err := r.tasks.Cancel(ctx, peer.TaskID, "Shared rest companion became unavailable before the conversation started")
	if err != nil {
		return fmt.Errorf("cancel unstarted rest companion Task and Run: %w", err)
	}
	if cancelled.Status != domaintask.StatusCancelled {
		return errors.New("unstarted rest companion Task did not reach cancelled status")
	}
	if len(runs) > 0 {
		latestRuns, listErr := r.tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: peer.TaskID})
		if listErr != nil || len(latestRuns) == 0 || latestRuns[len(latestRuns)-1].Status != domaintask.RunStatusCancelled {
			return errors.Join(errors.New("unstarted rest companion Run did not reach cancelled status"), listErr)
		}
	}
	progress.Actors = progress.Actors[:1]
	return nil
}

func routinePreparedRunsForActors(prepared []routinePreparedRun, actors []routineActorProgress) []routinePreparedRun {
	allowed := make(map[string]struct{}, len(actors))
	for _, actor := range actors {
		allowed[actor.ActorID] = struct{}{}
	}
	filtered := make([]routinePreparedRun, 0, len(prepared))
	for _, item := range prepared {
		if _, ok := allowed[item.task.Assignee]; ok {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func (r *agentRoutineRuntime) ensureRoutineRestActorRun(intent *agentroutine.Intent, progress *routineProgress, actorID string) error {
	if intent == nil || progress == nil || actorID == "" || r.workers[actorID] == nil {
		return errRoutineRestUnavailable
	}
	for _, actor := range progress.Actors {
		if actor.ActorID == actorID {
			return nil
		}
	}
	if len(progress.Actors) != 1 {
		return errors.New("rest conversation checkpoint has an unexpected canonical actor count")
	}
	progress.Actors = append(progress.Actors, routineActorProgress{ActorID: actorID, TaskID: modulecore.NewTaskID()})
	return nil
}

func (r *agentRoutineRuntime) restSeatPlanStillPossible(snapshot worldread.Snapshot, intent agentroutine.Intent, progress routineProgress) bool {
	if progress.Rest == nil || progress.Rest.Mode == "" || len(progress.Rest.Seats) == 0 {
		return false
	}
	actorIDs := []string{intent.ActorID}
	if progress.Rest.Mode == routineRestModeConversation {
		if progress.Rest.ParticipantID == "" {
			return false
		}
		actorIDs = append(actorIDs, progress.Rest.ParticipantID)
	}
	for _, actorID := range actorIDs {
		seat, ok := progress.Rest.Seats[actorID]
		if !ok || seat.RoomID == "" || seat.SlotID == "" {
			return false
		}
		worldID, ok := r.routineWorldActorID(actorID)
		if !ok {
			return false
		}
		found := false
		for _, candidate := range livingRestSeatCandidates(snapshot, worldID) {
			if candidate.room == seat.RoomID && candidate.slot == seat.SlotID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func routineRestSeatAllowed(r *agentRoutineRuntime, seat livingSeatActivity) bool {
	if r == nil || r.cfg == nil || seat.room == "" || seat.slot == "" || livingSeatRoom(seat.slot) != seat.room {
		return false
	}
	canonical := false
	switch seat.slot {
	case "sofa", "sofa-guest", "dining", "dining-guest", "dining-aisle-north", "dining-aisle-south", "dining-window-north", "dining-window-south":
		canonical = true
	}
	if !canonical {
		return false
	}
	for _, room := range r.cfg.Heartbeat.Routine.SharedRooms {
		if room == seat.room {
			return true
		}
	}
	return false
}

func routineRestPeerSeat(candidates []livingSeatActivity, host livingSeatActivity) (livingSeatActivity, bool) {
	for _, candidate := range candidates {
		if candidate.room == host.room && candidate.slot != host.slot {
			return candidate, true
		}
	}
	return livingSeatActivity{}, false
}

func (r *agentRoutineRuntime) executeRoutineRestActivity(ctx context.Context, stop <-chan struct{}, state agentroutine.State, progress routineProgress, prepared []routinePreparedRun) (agentroutine.State, routineProgress, error) {
	if state.Pending == nil || state.Pending.Kind != agentroutine.ActivityRelax || progress.Rest == nil {
		return state, progress, errRoutineRestUnavailable
	}
	var err error
	seated := false
	for attempt := 0; attempt < 8; attempt++ {
		state, progress, err = r.prepareRoutineRestPlan(ctx, state, progress)
		if err != nil {
			return state, progress, err
		}
		prepared = routinePreparedRunsForActors(prepared, progress.Actors)
		state, progress, err = r.ensureRoutineRestSeats(ctx, stop, state, progress)
		if errors.Is(err, errRoutineRestSeatOccupied) {
			continue
		}
		if err != nil {
			return state, progress, err
		}
		seated = true
		break
	}
	if !seated {
		return state, progress, errRoutineRestUnavailable
	}
	prepared = routinePreparedRunsForActors(prepared, progress.Actors)
	if routineStopRequested(ctx, stop) {
		return state, progress, errAgentRoutineStopped
	}
	switch progress.Rest.Mode {
	case routineRestModeReading:
		state, progress, err = r.executeRoutineRestReading(ctx, stop, state, progress, prepared)
		if errors.Is(err, errRoutineRestBookFinished) {
			progress.Rest.Mode = routineRestModeTea
			progress.Rest.ParticipantID = ""
			progress.Rest.TeaVirtual = true
			progress.Rest.Seats = nil
			progress.Rest.FailureReason = "book_catalog_exhausted"
			state, progress, err = r.saveProgress(ctx, state, "rest_reading_catalog_exhausted_fallback", progress)
			if err == nil {
				state, progress, err = r.prepareRoutineRestPlan(ctx, state, progress)
			}
			if err == nil {
				state, progress, err = r.ensureRoutineRestSeats(ctx, stop, state, progress)
			}
			if err == nil {
				state, progress, err = r.executeRoutineRestTea(ctx, stop, state, progress, prepared)
			}
		}
	case routineRestModeConversation:
		state, progress, err = r.executeRoutineRestConversation(ctx, stop, state, progress, prepared)
	case routineRestModeTea:
		state, progress, err = r.executeRoutineRestTea(ctx, stop, state, progress, prepared)
	default:
		return state, progress, errRoutineRestUnavailable
	}
	if err != nil {
		if !errors.Is(err, errAgentRoutineStopped) && !routineStopRequested(ctx, stop) {
			progress.Rest.FailureReason = routineRestFailureCode(err)
			var checkpointErr error
			state, progress, checkpointErr = r.saveProgress(ctx, state, "rest_activity_failed", progress)
			err = errors.Join(err, checkpointErr)
		}
		return state, progress, err
	}
	if progress.Rest.CompletedAt.IsZero() {
		progress.Rest.CompletedAt = r.now().UTC()
		progress.CompletedAt = progress.Rest.CompletedAt
		progress.EffectCompleted = true
		state, progress, err = r.saveProgress(ctx, state, "rest_activity_completed", progress)
	}
	return state, progress, err
}

func (r *agentRoutineRuntime) ensureRoutineRestSeats(ctx context.Context, stop <-chan struct{}, state agentroutine.State, progress routineProgress) (agentroutine.State, routineProgress, error) {
	if state.Pending == nil || progress.Rest == nil {
		return state, progress, errRoutineRestUnavailable
	}
	actorIDs := []string{state.Pending.ActorID}
	if progress.Rest.Mode == routineRestModeConversation {
		actorIDs = append(actorIDs, progress.Rest.ParticipantID)
	}
	for _, actorID := range actorIDs {
		seat, ok := progress.Rest.Seats[actorID]
		if !ok || seat.RoomID == "" || seat.SlotID == "" || !routineRestSeatAllowed(r, livingSeatActivity{room: seat.RoomID, slot: seat.SlotID}) {
			return state, progress, errRoutineRestUnavailable
		}
		if confirmed, readAt, seq := r.confirmRoutineRestSeat(ctx, actorID, seat); confirmed {
			if !seat.Confirmed {
				seat.Confirmed, seat.ConfirmedAt, seat.ObservedStateSeq = true, readAt.UTC(), seq
				progress.Rest.Seats[actorID] = seat
			}
			if actorID == state.Pending.ActorID {
				if progress.World == nil {
					progress.World = &routineWorldProgress{}
				}
				progress.World.RelaxRoomID, progress.World.RelaxObservedAt = seat.RoomID, readAt.UTC()
			}
			if !seat.Confirmed || progress.World == nil || progress.World.RelaxRoomID != seat.RoomID || progress.World.RelaxObservedAt.IsZero() {
				var saveErr error
				state, progress, saveErr = r.saveProgress(ctx, state, "rest_seat_confirmed", progress)
				if saveErr != nil {
					return state, progress, saveErr
				}
			}
			continue
		}
		if r.routineRestSeatRejectedOccupied(ctx, progress, actorID, seat) {
			return state, progress, errRoutineRestSeatOccupied
		}
		purpose, purposeErr := routineRestSeatMovePurpose(progress, actorID, seat)
		if purposeErr != nil {
			return state, progress, purposeErr
		}
		state, progress, _, err := r.ensureRoutineWorldArrival(ctx, stop, state, progress, actorID, seat.RoomID, seat.SlotID, purpose, routineWorldMoveWait)
		if err != nil {
			if errors.Is(err, worldread.ErrCommandRejected) && r.routineRestSeatRejectedOccupied(ctx, progress, actorID, seat) {
				return state, progress, errRoutineRestSeatOccupied
			}
			return state, progress, err
		}
		confirmed, readAt, seq := r.confirmRoutineRestSeat(ctx, actorID, seat)
		if !confirmed {
			return state, progress, errors.New("fresh WORLD snapshot does not confirm the exact idle rest seat")
		}
		seat.Confirmed, seat.ConfirmedAt, seat.ObservedStateSeq = true, readAt.UTC(), seq
		if moveIndex, ok := r.routineWorldOperation(progress, purpose+":move"); ok {
			move := progress.World.Operations[moveIndex]
			seat.MoveActivityID = move.ActivityID
		}
		progress.Rest.Seats[actorID] = seat
		if actorID == state.Pending.ActorID {
			if progress.World == nil {
				progress.World = &routineWorldProgress{}
			}
			progress.World.RelaxRoomID, progress.World.RelaxObservedAt = seat.RoomID, seat.ConfirmedAt
		}
		state, progress, err = r.saveProgress(ctx, state, "rest_seat_confirmed", progress)
		if err != nil {
			return state, progress, err
		}
	}
	return state, progress, nil
}

func (r *agentRoutineRuntime) confirmRoutineRestSeat(ctx context.Context, actorID string, seat routineRestSeatCheckpoint) (bool, time.Time, uint64) {
	read, actor, err := r.routineWorldFreshActor(ctx, actorID)
	if err != nil || read.snapshot.StateSeq < seat.ObservedStateSeq || !actor.Present ||
		actor.RoomID != seat.RoomID || actor.SlotID != seat.SlotID || actor.ActivityID != "" || actor.ActivityKind != "" {
		return false, time.Time{}, 0
	}
	return true, read.readAt, read.snapshot.StateSeq
}

func routineRestSeatMoveOperations(progress routineProgress, actorID string, seat routineRestSeatCheckpoint) []routineWorldOperation {
	if progress.World == nil {
		return nil
	}
	base := routineRestSeatPurposePrefix + actorID + "-" + seat.SlotID
	operations := make([]routineWorldOperation, 0, 3)
	for _, operation := range progress.World.Operations {
		basePurpose := operation.Purpose == base+":move"
		retryPurpose := strings.HasPrefix(operation.Purpose, base+":retry:") && strings.HasSuffix(operation.Purpose, ":move")
		if (basePurpose || retryPurpose) && operation.Action == worldread.HomeActionMove &&
			operation.ActorID == actorID && operation.Payload.RoomID == seat.RoomID && operation.Payload.SlotID == seat.SlotID {
			operations = append(operations, operation)
		}
	}
	return operations
}

const routineRestSeatMaxMoveAttempts = 3

func routineRestSeatMovePurpose(progress routineProgress, actorID string, seat routineRestSeatCheckpoint) (string, error) {
	base := routineRestSeatPurposePrefix + actorID + "-" + seat.SlotID
	operations := routineRestSeatMoveOperations(progress, actorID, seat)
	if len(operations) == 0 {
		return base, nil
	}
	last := operations[len(operations)-1]
	if last.ReceiptStatus != "rejected" {
		return strings.TrimSuffix(last.Purpose, ":move"), nil
	}
	if len(operations) >= routineRestSeatMaxMoveAttempts {
		return "", errRoutineRestSeatMoveExhausted
	}
	return routineWorldRetryPurpose(progress, base), nil
}

func (r *agentRoutineRuntime) routineRestSeatRejectedOccupied(ctx context.Context, progress routineProgress, actorID string, seat routineRestSeatCheckpoint) bool {
	operations := routineRestSeatMoveOperations(progress, actorID, seat)
	if len(operations) == 0 {
		return false
	}
	operation := operations[len(operations)-1]
	if operation.ReceiptID == "" || operation.ReceiptStatus != "rejected" ||
		(operation.ReceiptReason != "resource_conflict" && operation.ReceiptReason != "handler_error") {
		return false
	}
	read, err := r.readSnapshotFresh(ctx)
	if err != nil || read.snapshot.StateSeq < operation.StateSeq {
		return false
	}
	worldActorID, ok := r.routineWorldActorID(actorID)
	if !ok {
		return false
	}
	for _, actor := range read.snapshot.Actors {
		if actor.ActorID == worldActorID || !actor.Present {
			continue
		}
		if (actor.RoomID == seat.RoomID && actor.SlotID == seat.SlotID) ||
			(actor.TargetRoomID == seat.RoomID && actor.TargetSlotID == seat.SlotID) {
			return true
		}
	}
	return false
}

func (r *agentRoutineRuntime) executeRoutineRestConversation(ctx context.Context, stop <-chan struct{}, state agentroutine.State, progress routineProgress, prepared []routinePreparedRun) (agentroutine.State, routineProgress, error) {
	if state.Pending == nil || progress.Rest == nil || progress.Rest.Mode != routineRestModeConversation ||
		progress.Rest.ParticipantID == "" || len(prepared) != 2 || len(progress.Actors) != 2 {
		return state, progress, errRoutineRestUnavailable
	}
	var err error
	for _, actorID := range []string{state.Pending.ActorID, progress.Rest.ParticipantID} {
		seat := progress.Rest.Seats[actorID]
		if !seat.Confirmed {
			return state, progress, errors.New("conversation cannot start before both seats are confirmed")
		}
		if confirmed, _, _ := r.confirmRoutineRestSeat(ctx, actorID, seat); !confirmed {
			return state, progress, errors.New("conversation cannot start until both exact shared seats are confirmed idle")
		}
	}
	if progress.Rest.Conversation == nil {
		progress.Rest.Conversation = &routineRestConversationProgress{}
	}
	if progress.Rest.TeaVirtual && progress.Rest.TeaStartedAt.IsZero() {
		progress.Rest.TeaStartedAt = r.now().UTC()
		state, progress, err = r.saveProgress(ctx, state, "rest_virtual_tea_started", progress)
		if err != nil {
			return state, progress, err
		}
	}
	if progress.Rest.Conversation.StartedAt.IsZero() {
		progress.Rest.Conversation.StartedAt = r.now().UTC()
		progress.Rest.StartedAt = progress.Rest.Conversation.StartedAt
		state, progress, err = r.saveProgress(ctx, state, "rest_conversation_started", progress)
		if err != nil {
			return state, progress, err
		}
	}
	for index := range progress.Actors {
		if progress.Actors[index].Output != "" {
			continue
		}
		if routineStopRequested(ctx, stop) {
			return state, progress, errAgentRoutineStopped
		}
		otherTurn := ""
		if index > 0 {
			otherTurn = progress.Actors[index-1].Output
		}
		prompt := routineRestConversationPrompt(progress.Rest.TeaVirtual, progress.Actors[index].ActorID, otherTurn)
		output, err := r.executePrivateWorker(ctx, prepared[index], progress.Actors[index].ActorID, prompt)
		if err != nil {
			return state, progress, err
		}
		utterance, err := boundedRoutineRestOutput(output, routineRestUtteranceMax)
		if err != nil {
			return state, progress, err
		}
		progress.Actors[index].Output = utterance
		progress.Rest.Conversation.Turns++
		state, progress, err = r.saveProgress(ctx, state, "rest_conversation_turn_saved", progress)
		if err != nil {
			return state, progress, err
		}
	}
	progress.Rest.Conversation.CompletedAt = r.now().UTC()
	progress.Rest.CompletedAt = progress.Rest.Conversation.CompletedAt
	if progress.Rest.TeaVirtual {
		progress.Rest.TeaCompletedAt = progress.Rest.Conversation.CompletedAt
	}
	progress.CompletedAt = progress.Rest.CompletedAt
	progress.EffectCompleted = true
	return r.saveProgress(ctx, state, "rest_conversation_completed", progress)
}

func (r *agentRoutineRuntime) executeRoutineRestReading(ctx context.Context, stop <-chan struct{}, state agentroutine.State, progress routineProgress, prepared []routinePreparedRun) (agentroutine.State, routineProgress, error) {
	if state.Pending == nil || progress.Rest == nil || progress.Rest.Mode != routineRestModeReading ||
		len(prepared) != 1 || len(progress.Actors) != 1 || r.restReader == nil {
		return state, progress, errRoutineRestUnavailable
	}
	seat := progress.Rest.Seats[state.Pending.ActorID]
	if !seat.Confirmed || seat.RoomID != "living" || livingSeatRoom(seat.SlotID) != "living" {
		return state, progress, errors.New("reading cannot start before an exact living sofa seat is confirmed")
	}
	if confirmed, _, _ := r.confirmRoutineRestSeat(ctx, state.Pending.ActorID, seat); !confirmed {
		return state, progress, errors.New("reading cannot start without a fresh idle living sofa seat")
	}
	cursor, err := r.latestRoutineRestReadCursor(ctx, state.Pending.ActorID)
	if err != nil {
		return state, progress, err
	}
	var chunk routineRestBookChunk
	for attempt := 0; attempt <= len(routineRestBookCatalog); attempt++ {
		chunk, err = r.restReader.FetchNext(ctx, cursor)
		if !errors.Is(err, errRoutineRestBookFinished) {
			break
		}
		next, ok := nextRoutineRestBookCursor(cursor)
		if !ok {
			return state, progress, errRoutineRestBookFinished
		}
		cursor = next
	}
	if err != nil {
		return state, progress, err
	}
	if chunk.Text == "" || chunk.EndRune <= chunk.StartRune || chunk.NextRune != chunk.EndRune || chunk.BodyRunes < chunk.EndRune ||
		chunk.Title == "" || chunk.Author == "" || chunk.SourceURL == "" || chunk.RightsURL == "" || chunk.BodyHash == "" {
		return state, progress, errors.New("reading source returned an incomplete or empty bounded chunk")
	}
	if routineStopRequested(ctx, stop) {
		return state, progress, errAgentRoutineStopped
	}
	now := r.now().UTC()
	progress.Rest.Read = &routineRestReadProgress{
		Title: chunk.Title, Author: chunk.Author, SourceURL: chunk.SourceURL, RightsURL: chunk.RightsURL,
		BodyHash: chunk.BodyHash, BodyRunes: chunk.BodyRunes, StartRune: chunk.StartRune, EndRune: chunk.EndRune, NextRune: chunk.NextRune,
		StartedAt: now,
	}
	progress.Rest.StartedAt = now
	state, progress, err = r.saveProgress(ctx, state, "rest_reading_chunk_presented", progress)
	if err != nil {
		return state, progress, err
	}
	prompt := routineRestReadingPrompt(state.Pending.ActorID, chunk)
	output, err := r.executePrivateWorker(ctx, prepared[0], state.Pending.ActorID, prompt)
	if err != nil {
		return state, progress, err
	}
	reflection, err := boundedRoutineRestOutput(output, agentRoutineReflectionMax)
	if err != nil {
		return state, progress, err
	}
	progress.Rest.Read.Reflection = reflection
	progress.Rest.Read.CompletedAt = r.now().UTC()
	progress.Rest.CompletedAt = progress.Rest.Read.CompletedAt
	progress.CompletedAt = progress.Rest.CompletedAt
	progress.EffectCompleted = true
	return r.saveProgress(ctx, state, "rest_reading_reflection_saved", progress)
}

func (r *agentRoutineRuntime) latestRoutineRestReadCursor(ctx context.Context, actorID string) (routineRestReadCursor, error) {
	through := r.now().In(r.location()).Format("2006-01-02")
	facts, err := r.store.ListFactsInDateRange(ctx, actorID, "0001-01-01", through)
	if err != nil {
		return routineRestReadCursor{}, err
	}
	for index := len(facts) - 1; index >= 0; index-- {
		fact := facts[index]
		if fact.Kind != agentroutine.ActivityRelax || !fact.Confirmed {
			continue
		}
		var reference routineRestFactReference
		if err := json.Unmarshal([]byte(fact.Reference), &reference); err != nil || reference.Mode != routineRestModeReading || reference.Read == nil {
			continue
		}
		read := reference.Read
		if read.SourceURL == "" || read.BodyHash == "" || read.BodyRunes <= 0 || read.NextRune <= 0 ||
			read.CompletedAt.IsZero() || read.NextRune != read.EndRune || read.NextRune > read.BodyRunes {
			continue
		}
		return routineRestReadCursor{SourceURL: read.SourceURL, BodyHash: read.BodyHash, BodyRunes: read.BodyRunes, NextRune: read.NextRune}, nil
	}
	return routineRestReadCursor{}, nil
}

func routineRestReadingPrompt(actorID string, chunk routineRestBookChunk) string {
	return fmt.Sprintf(`あなたは%sです。次の実際に取得した本文の範囲だけを読んで、内容の短い要約と自分の感想を日本語で記してください。本文にない出来事や、読んでいない先の内容は補わないでください。作品『%s』(%s)、出典: %s、範囲: %d〜%d文字。
---本文---
%s`,
		actorID, chunk.Title, chunk.Author, chunk.SourceURL, chunk.StartRune, chunk.EndRune, chunk.Text)
}

func routineRestFailureCode(err error) string {
	switch {
	case errors.Is(err, errRoutineRestBookUnavailable):
		return "book_fetch_unavailable"
	case errors.Is(err, errRoutineRestBookChanged):
		return "book_content_changed"
	case errors.Is(err, errRoutineRestBookFinished):
		return "book_catalog_exhausted"
	case errors.Is(err, errRoutineRestSeatOccupied), errors.Is(err, errRoutineRestMoveRequired):
		return "shared_seat_unavailable"
	case errors.Is(err, errRoutineRestUnavailable):
		return "rest_activity_unavailable"
	default:
		return "rest_activity_failed"
	}
}

func (r *agentRoutineRuntime) executeRoutineRestTea(ctx context.Context, stop <-chan struct{}, state agentroutine.State, progress routineProgress, prepared []routinePreparedRun) (agentroutine.State, routineProgress, error) {
	if state.Pending == nil || progress.Rest == nil || progress.Rest.Mode != routineRestModeTea ||
		!progress.Rest.TeaVirtual || len(prepared) != 1 || len(progress.Actors) != 1 {
		return state, progress, errRoutineRestUnavailable
	}
	var err error
	seat := progress.Rest.Seats[state.Pending.ActorID]
	if !seat.Confirmed || seat.RoomID != "dining" || seat.SlotID == "" {
		return state, progress, errors.New("virtual tea cannot start before exact dining seat confirmation")
	}
	if confirmed, _, _ := r.confirmRoutineRestSeat(ctx, state.Pending.ActorID, seat); !confirmed {
		return state, progress, errors.New("virtual tea cannot start without a fresh idle dining seat")
	}
	if progress.Rest.TeaStartedAt.IsZero() {
		progress.Rest.TeaStartedAt = r.now().UTC()
		progress.Rest.StartedAt = progress.Rest.TeaStartedAt
		state, progress, err = r.saveProgress(ctx, state, "rest_virtual_tea_started", progress)
		if err != nil {
			return state, progress, err
		}
	}
	if progress.Rest.TeaReflection == "" {
		if routineStopRequested(ctx, stop) {
			return state, progress, errAgentRoutineStopped
		}
		prompt := routineRestTeaPrompt(state.Pending.ActorID)
		output, err := r.executePrivateWorker(ctx, prepared[0], state.Pending.ActorID, prompt)
		if err != nil {
			return state, progress, err
		}
		reflection, err := boundedRoutineRestOutput(output, routineRestUtteranceMax)
		if err != nil {
			return state, progress, err
		}
		progress.Rest.TeaReflection = reflection
		state, progress, err = r.saveProgress(ctx, state, "rest_virtual_tea_reflection_saved", progress)
		if err != nil {
			return state, progress, err
		}
	}
	progress.Rest.TeaCompletedAt = r.now().UTC()
	progress.Rest.CompletedAt = progress.Rest.TeaCompletedAt
	progress.CompletedAt = progress.Rest.CompletedAt
	progress.EffectCompleted = true
	return r.saveProgress(ctx, state, "rest_virtual_tea_completed", progress)
}

func routineRestTeaPrompt(actorID string) string {
	return fmt.Sprintf("あなたは%sです。ダイニングで、COREに記録する仮想のお茶休憩をしています。WORLDで飲用は確認できないため、飲んだ・茶器を持ったなどの現実の行動を事実として主張しないでください。休憩中にふと思ったことを短く一人称で記してください。", actorID)
}

func routineRestConversationPrompt(virtualTea bool, actorID, previous string) string {
	setting := "ソファで一緒に休憩中です。自然な短い会話を一往復してください。現実に観測されていない行動は主張しないでください。"
	if virtualTea {
		setting = "ダイニングで仮想のお茶を囲んで休憩中です。これはCOREが記録する仮想活動で、WORLDで飲用を確認したものではありません。自然な短い会話を一往復してください。"
	}
	if previous != "" {
		return fmt.Sprintf("あなたは%sです。%s 相手の直前の実際の発話に自然に返答してください。\n相手の発話:\n%s", actorID, setting, previous)
	}
	return fmt.Sprintf("あなたは%sです。%s 話題を一つ選んで会話を始めてください。", actorID, setting)
}

func boundedRoutineRestOutput(value string, limit int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len([]rune(value)) > limit {
		return "", errors.New("rest activity returned an empty or oversized actor response")
	}
	return value, nil
}

func (r *agentRoutineRuntime) pendingRoutineSharedRestSeat(actorID string) (livingSeatActivity, bool) {
	if r == nil || r.store == nil || !isAgentHeartbeatActor(actorID) {
		return livingSeatActivity{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	for _, ownerID := range agentHeartbeatActorIDs {
		state, err := r.store.Load(ctx, ownerID)
		if err != nil || state.Pending == nil || state.Pending.Kind != agentroutine.ActivityRelax {
			continue
		}
		progress, err := decodeRoutineProgress(state.Pending.Output)
		if err != nil || progress.Rest == nil || progress.Rest.Scope != agentroutine.RelaxRoomShared {
			continue
		}
		seat, ok := progress.Rest.Seats[actorID]
		if ok && seat.RoomID != "" && seat.SlotID != "" {
			return livingSeatActivity{room: seat.RoomID, slot: seat.SlotID}, true
		}
	}
	return livingSeatActivity{}, false
}

func (r *agentRoutineRuntime) activeRoutineRestSeat(actorID string) (livingSeatActivity, bool) {
	if r == nil || r.store == nil || r.world == nil || !isAgentHeartbeatActor(actorID) {
		return livingSeatActivity{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	read, err := r.readSnapshotFresh(ctx)
	if err != nil {
		return livingSeatActivity{}, false
	}
	for _, ownerID := range agentHeartbeatActorIDs {
		state, loadErr := r.store.Load(ctx, ownerID)
		if loadErr != nil || state.Pending == nil || state.Pending.Kind != agentroutine.ActivityRelax {
			continue
		}
		progress, decodeErr := decodeRoutineProgress(state.Pending.Output)
		if decodeErr != nil || progress.Rest == nil || progress.Rest.Scope != agentroutine.RelaxRoomShared {
			continue
		}
		seat, ok := progress.Rest.Seats[actorID]
		if ok && seat.Confirmed && routineRestSeatOccupiedByActor(read.snapshot, r.worldActorIDFor(actorID), seat) {
			return livingSeatActivity{room: seat.RoomID, slot: seat.SlotID}, true
		}
	}
	state, err := r.store.Load(ctx, actorID)
	if err != nil {
		return livingSeatActivity{}, false
	}
	return r.activeRoutineRestSeatFromState(ctx, state, actorID, read.snapshot)
}

func (r *agentRoutineRuntime) activeRoutineRestSeatFromState(ctx context.Context, state agentroutine.State, actorID string, snapshot worldread.Snapshot) (livingSeatActivity, bool) {
	var seat routineRestSeatCheckpoint
	if state.Pending != nil && state.Pending.Kind == agentroutine.ActivityRelax {
		progress, err := decodeRoutineProgress(state.Pending.Output)
		if err != nil || progress.Rest == nil || progress.Rest.Scope != agentroutine.RelaxRoomShared {
			return livingSeatActivity{}, false
		}
		checkpoint, ok := progress.Rest.Seats[actorID]
		if !ok || !checkpoint.Confirmed {
			return livingSeatActivity{}, false
		}
		seat = checkpoint
	} else {
		persisted, ok := r.persistedRoutineRestSeatFromState(ctx, state, actorID)
		if !ok {
			return livingSeatActivity{}, false
		}
		seat = routineRestSeatCheckpoint{RoomID: persisted.room, SlotID: persisted.slot}
	}
	if !routineRestSeatOccupiedByActor(snapshot, r.worldActorIDFor(actorID), seat) {
		return livingSeatActivity{}, false
	}
	return livingSeatActivity{room: seat.RoomID, slot: seat.SlotID}, true
}

func (r *agentRoutineRuntime) persistedRoutineRestSeatFromState(ctx context.Context, state agentroutine.State, actorID string) (livingSeatActivity, bool) {
	if state.RestWindow == nil || state.LastFinishedIntentID == "" {
		return livingSeatActivity{}, false
	}
	center := state.RestWindow.Until.In(r.location()).Add(-routineCooldown(agentroutine.ActivityRelax))
	fromDate := center.AddDate(0, 0, -1).Format("2006-01-02")
	throughDate := center.AddDate(0, 0, 1).Format("2006-01-02")
	facts, err := r.store.ListFactsInDateRange(ctx, actorID, fromDate, throughDate)
	if err != nil {
		return livingSeatActivity{}, false
	}
	for index := len(facts) - 1; index >= 0; index-- {
		fact := facts[index]
		if fact.Kind != agentroutine.ActivityRelax || !fact.Confirmed || fact.SourceID != state.LastFinishedIntentID {
			continue
		}
		var reference routineRestFactReference
		if json.Unmarshal([]byte(fact.Reference), &reference) != nil {
			continue
		}
		seat, ok := reference.Seats[actorID]
		if ok && seat.Confirmed && seat.RoomID != "" && seat.SlotID != "" {
			return livingSeatActivity{room: seat.RoomID, slot: seat.SlotID}, true
		}
	}
	return livingSeatActivity{}, false
}

func routineRestSeatOccupiedByActor(snapshot worldread.Snapshot, worldActorID string, seat routineRestSeatCheckpoint) bool {
	if worldActorID == "" || seat.RoomID == "" || seat.SlotID == "" {
		return false
	}
	for _, actor := range snapshot.Actors {
		if actor.ActorID == worldActorID {
			return actor.Present && actor.RoomID == seat.RoomID && actor.SlotID == seat.SlotID &&
				actor.ActivityID == "" && actor.ActivityKind == ""
		}
	}
	return false
}

func (r *agentRoutineRuntime) worldActorIDFor(actorID string) string {
	for worldID, coreID := range r.worldActorIDs {
		if coreID == actorID {
			return worldID
		}
	}
	return ""
}

func (r *agentRoutineRuntime) pendingRoutineRestSeatCheckpoint(actorID string) (routineRestSeatCheckpoint, bool) {
	for _, ownerID := range agentHeartbeatActorIDs {
		state, err := r.store.Load(context.Background(), ownerID)
		if err != nil || state.Pending == nil || state.Pending.Kind != agentroutine.ActivityRelax {
			continue
		}
		progress, err := decodeRoutineProgress(state.Pending.Output)
		if err != nil || progress.Rest == nil || progress.Rest.Scope != agentroutine.RelaxRoomShared {
			continue
		}
		seat, ok := progress.Rest.Seats[actorID]
		if ok && seat.RoomID != "" && seat.SlotID != "" {
			return seat, true
		}
	}
	return routineRestSeatCheckpoint{}, false
}

func (r *agentRoutineRuntime) persistedRoutineRestSeat(ctx context.Context, actorID string) (livingSeatActivity, bool) {
	state, err := r.store.Load(ctx, actorID)
	if err != nil {
		return livingSeatActivity{}, false
	}
	return r.persistedRoutineRestSeatFromState(ctx, state, actorID)
}

func (r *agentRoutineRuntime) routineRestActivityProjectionFromSnapshot(
	ctx context.Context,
	state agentroutine.State,
	progress routineProgress,
	snapshot worldread.Snapshot,
) string {
	if state.Pending == nil || state.Pending.Kind != agentroutine.ActivityRelax || progress.Rest == nil ||
		progress.EffectCompleted || !progress.Rest.CompletedAt.IsZero() || r.tasks == nil ||
		!routineRestSeatConfirmedInSnapshot(snapshot, r.worldActorIDFor(state.ActorID), progress.Rest.Seats[state.ActorID]) {
		return ""
	}
	seat := progress.Rest.Seats[state.ActorID]
	if !routineRestRunsRunning(ctx, r.tasks, progress.Actors) {
		return ""
	}
	switch progress.Rest.Mode {
	case routineRestModeReading:
		if seat.RoomID == "living" && progress.Rest.Read != nil && !progress.Rest.Read.StartedAt.IsZero() &&
			progress.Rest.Read.CompletedAt.IsZero() {
			return "sofa_reading"
		}
	case routineRestModeTea:
		if progress.Rest.TeaVirtual && seat.RoomID == "dining" &&
			!progress.Rest.TeaStartedAt.IsZero() && progress.Rest.TeaCompletedAt.IsZero() {
			return "dining_tea"
		}
	case routineRestModeConversation:
		if progress.Rest.Conversation == nil || progress.Rest.Conversation.StartedAt.IsZero() ||
			!progress.Rest.Conversation.CompletedAt.IsZero() {
			return ""
		}
		peerSeat, ok := progress.Rest.Seats[progress.Rest.ParticipantID]
		if !ok || !peerSeat.Confirmed ||
			!routineRestSeatConfirmedInSnapshot(snapshot, r.worldActorIDFor(progress.Rest.ParticipantID), peerSeat) {
			return ""
		}
		if progress.Rest.TeaVirtual && seat.RoomID == "dining" && peerSeat.RoomID == "dining" &&
			!progress.Rest.TeaStartedAt.IsZero() && progress.Rest.TeaCompletedAt.IsZero() {
			return "dining_tea_chat"
		}
		if !progress.Rest.TeaVirtual && seat.RoomID == "living" && peerSeat.RoomID == "living" {
			return "sofa_chat"
		}
	}
	return ""
}

func routineRestSeatConfirmedInSnapshot(snapshot worldread.Snapshot, worldActorID string, seat routineRestSeatCheckpoint) bool {
	return worldActorID != "" && snapshot.StateSeq >= seat.ObservedStateSeq &&
		routineRestSeatOccupiedByActor(snapshot, worldActorID, seat)
}

func routineRestRunsRunning(ctx context.Context, tasks *taskmanager.Manager, actors []routineActorProgress) bool {
	if tasks == nil || len(actors) == 0 {
		return false
	}
	for _, actor := range actors {
		if actor.RunID == "" {
			return false
		}
		run, err := tasks.GetRun(ctx, actor.RunID)
		if err != nil || run.Status != domaintask.RunStatusRunning {
			return false
		}
	}
	return true
}

func (r *agentRoutineRuntime) routineRestMoveProjectionFromSnapshot(
	state agentroutine.State,
	progress routineProgress,
	snapshot worldread.Snapshot,
) bool {
	if state.Pending == nil || state.Pending.Kind != agentroutine.ActivityRelax || progress.Rest == nil ||
		progress.World == nil {
		return false
	}
	seat, ok := progress.Rest.Seats[state.ActorID]
	if !ok || seat.RoomID == "" || seat.SlotID == "" {
		return false
	}
	worldActorID, ok := r.routineWorldActorID(state.ActorID)
	if !ok {
		return false
	}
	var actor *worldread.Actor
	for index := range snapshot.Actors {
		if snapshot.Actors[index].ActorID == worldActorID {
			actor = &snapshot.Actors[index]
			break
		}
	}
	if actor == nil || !actor.Present || snapshot.StateSeq < seat.ObservedStateSeq ||
		actor.ActivityKind != "move" || actor.ActivityID == "" ||
		actor.TargetRoomID != seat.RoomID || actor.TargetSlotID != seat.SlotID {
		return false
	}
	for _, operation := range progress.World.Operations {
		if operation.Action == worldread.HomeActionMove && (operation.Payload.ActorID == worldActorID || operation.Payload.ActorID == state.ActorID) &&
			operation.Payload.RoomID == seat.RoomID && operation.Payload.SlotID == seat.SlotID &&
			operation.ActivityID == actor.ActivityID && operation.ReceiptID != "" &&
			operation.ReceiptStatus == "committed" && operation.EventVerified &&
			snapshot.StateSeq >= operation.StateSeq && actor.MoveRouteID == operation.RouteID &&
			actor.HomeSceneSHA256 == operation.SceneSHA256 && snapshot.HomeSceneSHA256 == operation.SceneSHA256 {
			return true
		}
	}
	return false
}

func (r *agentRoutineRuntime) routineRestFactReference(progress routineProgress) (routineRestFactReference, error) {
	if progress.Rest == nil || progress.Rest.CompletedAt.IsZero() || len(progress.Rest.Seats) == 0 {
		return routineRestFactReference{}, errors.New("completed rest has no exact seat or activity checkpoint")
	}
	for _, seat := range progress.Rest.Seats {
		if !seat.Confirmed || seat.RoomID == "" || seat.SlotID == "" {
			return routineRestFactReference{}, errors.New("completed rest is missing a verified exact seat")
		}
	}
	reference := routineRestFactReference{
		Mode: progress.Rest.Mode, TeaVirtual: progress.Rest.TeaVirtual,
		TeaStartedAt: progress.Rest.TeaStartedAt, TeaCompletedAt: progress.Rest.TeaCompletedAt,
		Seats: progress.Rest.Seats, Read: progress.Rest.Read, TeaReflection: progress.Rest.TeaReflection,
		FailureReason: progress.Rest.FailureReason,
	}
	if progress.Rest.Conversation != nil {
		reference.ConversationStartedAt = progress.Rest.Conversation.StartedAt
		reference.ConversationCompletedAt = progress.Rest.Conversation.CompletedAt
	}
	if progress.Rest.Mode == routineRestModeReading {
		if progress.Rest.Read == nil || progress.Rest.Read.CompletedAt.IsZero() ||
			strings.TrimSpace(progress.Rest.Read.Reflection) == "" || progress.Rest.Read.StartRune < 0 ||
			progress.Rest.Read.EndRune <= progress.Rest.Read.StartRune || progress.Rest.Read.NextRune != progress.Rest.Read.EndRune {
			return routineRestFactReference{}, errors.New("completed reading has no verified range and actual reader reflection")
		}
	} else if progress.Rest.Mode == routineRestModeTea {
		if !progress.Rest.TeaVirtual || progress.Rest.TeaStartedAt.IsZero() || progress.Rest.TeaCompletedAt.IsZero() ||
			strings.TrimSpace(progress.Rest.TeaReflection) == "" || len(progress.Actors) != 1 {
			return routineRestFactReference{}, errors.New("completed virtual tea has no start, end, or actual reflection checkpoint")
		}
	} else if progress.Rest.Mode == routineRestModeConversation {
		if progress.Rest.Conversation == nil || progress.Rest.Conversation.StartedAt.IsZero() ||
			!progress.Rest.Conversation.CompletedAt.After(progress.Rest.Conversation.StartedAt) || len(progress.Actors) != 2 {
			return routineRestFactReference{}, errors.New("completed rest conversation has no two-actor transcript")
		}
		for _, actor := range progress.Actors {
			if strings.TrimSpace(actor.Output) == "" || len([]rune(actor.Output)) > routineRestUtteranceMax {
				return routineRestFactReference{}, errors.New("completed rest conversation has an empty or oversized actor turn")
			}
			reference.Conversation = append(reference.Conversation, map[string]string{"actor_id": actor.ActorID, "text": actor.Output})
		}
		if progress.Rest.TeaVirtual && (progress.Rest.TeaStartedAt.IsZero() || progress.Rest.TeaCompletedAt.IsZero()) {
			return routineRestFactReference{}, errors.New("virtual tea conversation lacks its start and completion checkpoints")
		}
	} else {
		return routineRestFactReference{}, errRoutineRestUnavailable
	}
	return reference, nil
}

func (r *agentRoutineRuntime) marshalRoutineRestFactReference(progress routineProgress) (string, error) {
	reference, err := r.routineRestFactReference(progress)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(reference)
	return string(encoded), err
}

func (r *agentRoutineRuntime) routineRestSummary(intent agentroutine.Intent, progress routineProgress) (string, error) {
	reference, err := r.routineRestFactReference(progress)
	if err != nil {
		return "", err
	}
	switch reference.Mode {
	case routineRestModeReading:
		seat := progress.Rest.Seats[intent.ActorID]
		return fmt.Sprintf("ソファ(%s)で『%s』(%s)の%d〜%d文字を読み、感想を記録しました。感想: %s",
			seat.SlotID, reference.Read.Title, reference.Read.Author, reference.Read.StartRune, reference.Read.EndRune, reference.Read.Reflection), nil
	case routineRestModeConversation:
		if reference.TeaVirtual {
			return "ダイニングでお茶休憩をしながら、二人の会話を記録しました。", nil
		}
		return "ソファで二人の実際の発話を記録しました。", nil
	case routineRestModeTea:
		summary := "ダイニングでお茶休憩をし、短い記録を残しました。"
		if reference.FailureReason == "book_catalog_exhausted" {
			summary += "読了済みの本一覧に続きがないため、お茶休憩へ切り替えました。"
		}
		return summary, nil
	default:
		return "", errRoutineRestUnavailable
	}
}
