package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainrouting "github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	browseractorinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/browseractor"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

const (
	agentRoutineModuleID        = "agent_routine"
	agentRoutineDiaryBodyMax    = 120 << 10
	agentRoutineReflectionMax   = 24 << 10
	agentRoutineSnapshotMaxAge  = 250 * time.Millisecond
	agentRoutineProviderTimeout = 2 * time.Minute
	agentRoutineWebTimeout      = 45 * time.Second
	agentRoutinePersistTimeout  = 15 * time.Second
	agentRoutineRetryDelay      = 15 * time.Minute
)

var errAgentRoutineStopped = errors.New("agent routine stopped")

type agentRoutineRuntime struct {
	cfg           *config.Config
	deps          *Dependencies
	store         *agentroutine.Store
	tasks         *taskmanager.Manager
	journal       any
	ownerID       string
	workers       map[string]privateTaskWorker
	tools         agent.ToolRunner
	mediaRunner   routineMediaRunner
	world         *worldread.Client
	worldLife     *worldread.LifeClient
	worldActorIDs map[string]string // WORLD actor ID -> exact CORE actor ID alias configured by the operator.
	canStartIdle  func(string) bool
	webReady      bool
	now           func() time.Time
	worldNow      func() time.Time // authoritative UTC owner clock for WORLD due times; scheduler/test time is separate.

	snapshotMu         sync.Mutex
	snapshot           worldread.Snapshot
	snapshotReadAt     time.Time
	snapshotPending    chan struct{}
	amendmentMu        sync.Mutex
	lastAmendmentCheck map[string]time.Time
}

type routineSnapshotRead struct {
	snapshot worldread.Snapshot
	readAt   time.Time
}

type routineProgress struct {
	Actors               []routineActorProgress `json:"actors"`
	Media                *routineMediaProgress  `json:"media,omitempty"`
	World                *routineWorldProgress  `json:"world,omitempty"`
	SearchResult         string                 `json:"search_result,omitempty"`
	HobbyText            string                 `json:"hobby_text,omitempty"`
	DiaryBody            string                 `json:"diary_body,omitempty"`
	DiaryReflection      string                 `json:"diary_reflection,omitempty"`
	DiaryRevision        int64                  `json:"diary_revision,omitempty"`
	DiaryPostingKey      string                 `json:"diary_posting_key,omitempty"`
	DiaryReceipt         string                 `json:"diary_receipt,omitempty"`
	DiaryReceiptRevision string                 `json:"diary_receipt_revision,omitempty"`
	CompletedAt          time.Time              `json:"completed_at,omitempty"`
	EffectCompleted      bool                   `json:"effect_completed,omitempty"`
}

type routineActorProgress struct {
	ActorID string            `json:"actor_id"`
	TaskID  modulecore.TaskID `json:"task_id"`
	RunID   modulecore.RunID  `json:"run_id,omitempty"`
	Output  string            `json:"output,omitempty"`
}

type routinePreparedRun struct {
	progressIndex int
	task          domaintask.Task
	run           domaintask.Run
}

func newAgentRoutineRuntime(cfg *config.Config, deps *Dependencies, workers map[string]privateTaskWorker, tools agent.ToolRunner) (*agentRoutineRuntime, error) {
	if cfg == nil || deps == nil || !cfg.Heartbeat.Routine.Enabled {
		return nil, nil
	}
	if deps.taskManager == nil {
		return nil, errors.New("agent routine requires the canonical TaskManager")
	}
	storePath := filepath.Join(cfg.WorkspaceDir, "data", "agent_routine", "routine.sqlite")
	store, err := agentroutine.Open(storePath)
	if err != nil {
		return nil, fmt.Errorf("open agent routine state: %w", err)
	}
	runtime := &agentRoutineRuntime{
		cfg: cfg, deps: deps, store: store, tasks: deps.taskManager, journal: deps.canonicalEventStore,
		ownerID: strings.TrimSpace(cfg.LocalAgentOps.UserID), workers: cloneRoutineWorkers(workers),
		tools: tools, now: time.Now, worldActorIDs: make(map[string]string),
	}
	if routineWorldConfigured(cfg.Heartbeat.Routine) {
		credential, tokenErr := config.ReadRoutineWorldBearerToken(cfg.Heartbeat.Routine.WorldReadTokenFile)
		if tokenErr != nil {
			log.Printf("agent routine WORLD reader unavailable: %v", tokenErr)
		} else {
			worldActorIDs := make([]string, 0, len(cfg.Heartbeat.Routine.WorldActors))
			for coreActorID, worldActorID := range cfg.Heartbeat.Routine.WorldActors {
				runtime.worldActorIDs[worldActorID] = coreActorID
				worldActorIDs = append(worldActorIDs, worldActorID)
			}
			sort.Strings(worldActorIDs)
			worldClient, clientErr := worldread.New(worldread.Config{
				BaseURL: cfg.Heartbeat.Routine.WorldURL, Credential: string(credential),
				WorldID: cfg.Heartbeat.Routine.WorldID, TimelineID: cfg.Heartbeat.Routine.TimelineID,
				TimelineEpoch: cfg.Heartbeat.Routine.TimelineEpoch, ActorIDs: worldActorIDs,
				Timeout: 750 * time.Millisecond,
			})
			if clientErr != nil {
				log.Printf("agent routine WORLD reader unavailable: %v", clientErr)
			} else {
				runtime.world = worldClient
				if lifePath := cfg.Heartbeat.Routine.WorldLifeTokenFile; lifePath != "" {
					lifeCredential, lifeTokenErr := config.ReadRoutineWorldLifeBearerToken(lifePath)
					if lifeTokenErr != nil {
						log.Printf("agent routine WORLD life actions unavailable: %v", lifeTokenErr)
					} else if string(credential) == lifeCredential {
						log.Printf("agent routine WORLD life actions unavailable: life token must be distinct from the read token")
					} else {
						worldLife, lifeClientErr := worldread.NewLife(worldread.LifeConfig{
							BaseURL: cfg.Heartbeat.Routine.WorldURL, Credential: lifeCredential,
							WorldID: cfg.Heartbeat.Routine.WorldID, TimelineID: cfg.Heartbeat.Routine.TimelineID,
							TimelineEpoch: cfg.Heartbeat.Routine.TimelineEpoch, ActorIDs: worldActorIDs,
							Timeout: 750 * time.Millisecond,
						})
						if lifeClientErr != nil {
							log.Printf("agent routine WORLD life actions unavailable: %v", lifeClientErr)
						} else {
							runtime.worldLife = worldLife
						}
					}
				}
			}
		}
	}
	if cfg.BrowserActor.Enabled && len(cfg.Heartbeat.Routine.Media) > 0 {
		runtime.mediaRunner = browseractorinfra.NewRunner(browserActorConfigFromRuntime(cfg.BrowserActor))
	}
	if err := runtime.initializeActors(context.Background()); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("initialize actor routine schedules: %w", err)
	}
	runtime.webReady = routineWebGatherAvailable(context.Background(), tools)
	if runtime.world == nil {
		log.Printf("agent routine enabled; WORLD room-dependent activities remain unavailable until an authenticated reader is configured")
	}
	if !runtime.webReady {
		log.Printf("agent routine enabled; WebGather hobby work is unavailable")
	}
	return runtime, nil
}

func (r *agentRoutineRuntime) initializeActors(ctx context.Context) error {
	now := r.now().In(r.location())
	date := now.Format("2006-01-02")
	scheduleDate := date
	if now.Hour() >= 22 {
		scheduleDate = now.AddDate(0, 0, 1).Format("2006-01-02")
	}
	for _, actorID := range agentHeartbeatActorIDs {
		state, err := routinePersist(ctx, func(persistCtx context.Context) (agentroutine.State, error) {
			return r.store.EnsureSchedule(persistCtx, actorID, scheduleDate)
		})
		if err != nil {
			return fmt.Errorf("initialize schedule for actor %s: %w", actorID, err)
		}
		if state.ActivatedOn == "" {
			activationDate := date
			priorDate := now.AddDate(0, 0, -1).Format("2006-01-02")
			if facts, factsErr := r.collectRoutineGameFacts(ctx, actorID, priorDate); factsErr != nil {
				log.Printf("agent routine prior-day GAMES history unavailable for %s; activation starts today: %v", actorID, factsErr)
			} else if len(facts) > 0 {
				activationDate = priorDate
				log.Printf("agent routine confirmed prior-day GAMES history for %s; first diary date is %s", actorID, priorDate)
			}
			state.ActivatedOn, state.NextDiaryDate = activationDate, activationDate
			state, err = routinePersist(ctx, func(persistCtx context.Context) (agentroutine.State, error) {
				return r.store.Save(persistCtx, state, state.Revision)
			})
			if err != nil {
				return fmt.Errorf("persist activation date for actor %s: %w", actorID, err)
			}
		}
	}
	return nil
}

func routineWorldConfigured(cfg config.RoutineHeartbeatConfig) bool {
	return strings.TrimSpace(cfg.WorldURL) != "" && strings.TrimSpace(cfg.WorldID) != "" &&
		strings.TrimSpace(cfg.TimelineID) != "" && strings.TrimSpace(cfg.TimelineEpoch) != "" &&
		strings.TrimSpace(cfg.WorldReadTokenFile) != "" && len(cfg.WorldActors) > 0
}

func routineWebGatherAvailable(ctx context.Context, runner agent.ToolRunner) bool {
	if runner == nil {
		return false
	}
	listCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tools, err := runner.ListTools(listCtx)
	if err != nil {
		return false
	}
	for _, item := range tools {
		if item.ToolID == "web_gather.search_and_fetch" {
			return true
		}
	}
	return false
}

func cloneRoutineWorkers(input map[string]privateTaskWorker) map[string]privateTaskWorker {
	workers := make(map[string]privateTaskWorker, len(input))
	for actorID, worker := range input {
		if isAgentHeartbeatActor(actorID) && worker != nil {
			workers[actorID] = worker
		}
	}
	return workers
}

func agentRoutineWorkers(mio, shiroChat *agent.MioAgent, midori *agent.WildAgent, textWorkers map[string]privateTaskWorker) map[string]privateTaskWorker {
	workers := make(map[string]privateTaskWorker, len(textWorkers)+3)
	if mio != nil {
		workers["mio"] = mio
	}
	if shiroChat != nil {
		workers["shiro"] = shiroChat
	}
	if midori != nil {
		workers["midori"] = midori
	}
	for actorID, worker := range textWorkers {
		if worker != nil {
			workers[actorID] = worker
		}
	}
	return workers
}

func (r *agentRoutineRuntime) Close() error {
	if r == nil || r.store == nil {
		return nil
	}
	return r.store.Close()
}

func (r *agentRoutineRuntime) idleSource(daytimeFallback agentheartbeat.Source) agentheartbeat.Source {
	if r == nil || r.store == nil {
		return daytimeFallback
	}
	return func(ctx context.Context, actorID string) (agentheartbeat.Work, bool, error) {
		if err := ctx.Err(); err != nil {
			return agentheartbeat.Work{}, false, err
		}
		state, now, err := r.loadDecisionState(ctx, actorID)
		if err != nil {
			return agentheartbeat.Work{}, false, err
		}
		retryingDiary := state.Pending != nil && state.Pending.Kind == agentroutine.ActivityDiary && state.Pending.RetryAfter.After(now)
		if state.Pending != nil && state.Pending.RetryAfter.After(now) && !retryingDiary {
			return agentheartbeat.Work{}, false, nil
		}
		read, snapshotErr := r.readSnapshot(ctx)
		if snapshotErr != nil {
			// A failed WORLD read leaves all room facts unknown. Diary timing and
			// daytime delegation are still computable from CORE's persisted schedule.
			read = routineSnapshotRead{}
		}
		presence := r.presence(read.snapshot, actorID, read.readAt, true, nil)
		canHobby := r.canHobby(actorID)
		planState := state
		if retryingDiary {
			planState.Pending = nil
			planState.NextDiaryDate = "" // Retry backoff must not monopolize the daylight fallback.
		}
		plan := agentroutine.Decide(now, agentroutine.ActorState{ID: actorID, HobbyConfigured: canHobby}, planState, presence, canHobby)
		if retryingDiary && plan.Kind != agentroutine.ActivitySleep && plan.Kind != agentroutine.ActivityWake {
			hour := now.In(r.location()).Hour()
			if hour < 9 || hour >= 22 {
				return agentheartbeat.Work{}, false, nil
			}
		}
		if state.Pending == nil && plan.Kind == agentroutine.ActivityDaytime && now.In(r.location()).Hour() >= 9 && now.In(r.location()).Hour() < 22 {
			priorDate := now.In(r.location()).AddDate(0, 0, -1).Format("2006-01-02")
			if state.ActivatedOn == "" || priorDate >= state.ActivatedOn {
				if changed, checkErr := r.checkDiaryAmendment(ctx, actorID, priorDate); checkErr == nil && changed {
					plan.Kind, plan.Date, plan.Reason = agentroutine.ActivityDiary, priorDate, "previous-day confirmed facts changed after diary publication"
				}
			}
		}
		if state.Pending == nil && plan.Kind == agentroutine.ActivitySleep && plan.LogicalSleep && state.LogicalSleepUntil.After(now) {
			return agentheartbeat.Work{}, false, nil
		}
		if state.Pending == nil && plan.Kind == agentroutine.ActivityWait {
			return agentheartbeat.Work{}, false, nil
		}
		if plan.Kind == agentroutine.ActivityDaytime {
			if daytimeFallback == nil {
				return agentheartbeat.Work{}, false, nil
			}
			return daytimeFallback(ctx, actorID)
		}
		if retryingDiary && plan.Kind != agentroutine.ActivitySleep && plan.Kind != agentroutine.ActivityWake {
			return agentheartbeat.Work{}, false, nil
		}
		if plan.Kind == agentroutine.ActivityDiary && r.boardClient(actorID) == nil {
			// A per-actor BOARD grant is optional at runtime. The diary remains
			// eligible, but a missing pinned writer does not occupy an actor slot.
			return agentheartbeat.Work{}, false, nil
		}
		participants := []string(nil)
		if state.Pending != nil {
			if state.Pending.Kind == agentroutine.ActivityMorningChat {
				participants = append(participants, state.Pending.Participants...)
			}
		} else if plan.Kind == agentroutine.ActivityMorningChat && len(plan.Participants) > 0 {
			// A two-party exchange is enough for the daily morning chat and keeps
			// the exact participant set durable and auditable.
			participants = []string{plan.Participants[0]}
			plan.Participants = participants
		} else {
			plan.Participants = nil
		}
		workID := "agent-routine:" + actorID + ":" + plan.Date + ":" + string(plan.Kind)
		return agentheartbeat.Work{
			ID: workID, Priority: domaintask.PriorityLow, CreatedAt: now.UTC(),
			ActivityKind: agentheartbeat.ActivityKindRoutine,
			Participants: participants,
			Run: func(runCtx context.Context, stop <-chan struct{}) error {
				return r.run(runCtx, stop, actorID, plan)
			},
		}, true, nil
	}
}

func (r *agentRoutineRuntime) ensureState(ctx context.Context, actorID string) (agentroutine.State, time.Time, error) {
	now := r.now().In(r.location())
	date := now.Format("2006-01-02")
	scheduleDate := date
	if now.Hour() >= 22 {
		scheduleDate = now.AddDate(0, 0, 1).Format("2006-01-02")
	}
	state, err := r.store.EnsureSchedule(ctx, actorID, scheduleDate)
	if err != nil {
		return agentroutine.State{}, now, err
	}
	if state.ActivatedOn == "" {
		state.ActivatedOn = date
		state.NextDiaryDate = date
		saved, saveErr := r.store.Save(ctx, state, state.Revision)
		if saveErr != nil {
			return agentroutine.State{}, now, saveErr
		}
		state = saved
	}
	return state, now, nil
}

// loadDecisionState makes scheduler discovery read-only. A schedule is
// projected into this local copy; the reserved Work.Run persists it before
// drafting any intent or starting a canonical Task.
func (r *agentRoutineRuntime) loadDecisionState(ctx context.Context, actorID string) (agentroutine.State, time.Time, error) {
	now := r.now().In(r.location())
	state, err := r.store.Load(ctx, actorID)
	if err != nil {
		return agentroutine.State{}, now, err
	}
	date := now.Format("2006-01-02")
	scheduleDate := date
	if now.Hour() >= 22 {
		scheduleDate = now.AddDate(0, 0, 1).Format("2006-01-02")
	}
	if state.ScheduleDate != scheduleDate {
		sleepAt, wakeAt, scheduleErr := agentroutine.ScheduleFor(actorID, scheduleDate)
		if scheduleErr != nil {
			return agentroutine.State{}, now, scheduleErr
		}
		state.ScheduleDate, state.PlannedSleep, state.PlannedWake = scheduleDate, sleepAt, wakeAt
		state.LogicalSleepUntil = time.Time{}
	}
	if state.ActivatedOn == "" {
		state.ActivatedOn, state.NextDiaryDate = date, date
	}
	return state, now, nil
}

func (r *agentRoutineRuntime) location() *time.Location {
	loc, err := time.LoadLocation(agentroutine.JST)
	if err != nil {
		return time.FixedZone(agentroutine.JST, 9*60*60)
	}
	return loc
}

func (r *agentRoutineRuntime) canHobby(actorID string) bool {
	return len(r.availableRoutineHobbyOptions(actorID)) > 0
}

func (r *agentRoutineRuntime) availableRoutineHobbyOptions(actorID string) []string {
	options := make([]string, 0, len(routineHobbyOptions(actorID)))
	for _, label := range routineHobbyOptions(actorID) {
		if routineWorldHomeHobby(actorID, label) && r.worldLife != nil {
			options = append(options, label)
			continue
		}
		if r.routineMediaConfiguredFor(actorID, label) {
			options = append(options, label)
			continue
		}
		if r.webReady && r.tools != nil && r.workers[actorID] != nil {
			options = append(options, label)
		}
	}
	return options
}

func (r *agentRoutineRuntime) routineMediaConfiguredFor(actorID, label string) bool {
	if r == nil || r.cfg == nil || r.mediaRunner == nil || !r.cfg.BrowserActor.Enabled {
		return false
	}
	kind, ok := routineMediaKindForLabel(label)
	if !ok {
		return false
	}
	for _, item := range r.cfg.Heartbeat.Routine.Media[actorID] {
		if item.Kind == kind {
			return true
		}
	}
	return false
}

func (r *agentRoutineRuntime) readSnapshot(ctx context.Context) (routineSnapshotRead, error) {
	if r.world == nil {
		return routineSnapshotRead{}, nil
	}
	for {
		r.snapshotMu.Lock()
		now := r.now()
		if !r.snapshotReadAt.IsZero() && now.Sub(r.snapshotReadAt) >= 0 && now.Sub(r.snapshotReadAt) <= agentRoutineSnapshotMaxAge {
			result := routineSnapshotRead{snapshot: cloneWorldReadSnapshot(r.snapshot), readAt: r.snapshotReadAt}
			r.snapshotMu.Unlock()
			return result, nil
		}
		if pending := r.snapshotPending; pending != nil {
			r.snapshotMu.Unlock()
			select {
			case <-ctx.Done():
				return routineSnapshotRead{}, ctx.Err()
			case <-pending:
				continue
			}
		}
		pending := make(chan struct{})
		r.snapshotPending = pending
		r.snapshotMu.Unlock()

		snapshot, err := r.world.Snapshot(ctx)
		readAt := r.now()
		r.snapshotMu.Lock()
		if err == nil {
			r.snapshot = cloneWorldReadSnapshot(snapshot)
			r.snapshotReadAt = readAt
		}
		r.snapshotPending = nil
		close(pending)
		r.snapshotMu.Unlock()
		if err != nil {
			return routineSnapshotRead{}, err
		}
		return routineSnapshotRead{snapshot: snapshot, readAt: readAt}, nil
	}
}

func (r *agentRoutineRuntime) readSnapshotFresh(ctx context.Context) (routineSnapshotRead, error) {
	if r.world == nil {
		return routineSnapshotRead{}, nil
	}
	snapshot, err := r.world.Snapshot(ctx)
	if err != nil {
		return routineSnapshotRead{}, err
	}
	return routineSnapshotRead{snapshot: snapshot, readAt: r.now()}, nil
}

func cloneWorldReadSnapshot(input worldread.Snapshot) worldread.Snapshot {
	input.Actors = append([]worldread.Actor(nil), input.Actors...)
	return input
}

func (r *agentRoutineRuntime) presence(snapshot worldread.Snapshot, actorID string, observedAt time.Time, filterBusy bool, reservedParticipants []string) agentroutine.Presence {
	presence := agentroutine.Presence{SharedRoom: agentroutine.AvailabilityUnknown, OwnRoom: agentroutine.AvailabilityUnknown}
	if snapshot.WorldID == "" {
		return presence
	}
	actors := make(map[string]worldread.Actor, len(snapshot.Actors))
	for _, actor := range snapshot.Actors {
		coreActorID := r.worldActorIDs[actor.ActorID]
		if coreActorID != "" {
			actors[coreActorID] = actor
		}
	}
	actor, found := actors[actorID]
	actorSleeping := found && (strings.EqualFold(strings.TrimSpace(actor.ActivityKind), "sleep") || strings.EqualFold(strings.TrimSpace(actor.ActivityID), "sleep"))
	canMoveForRoutine := found && r.worldLife != nil && actor.Present && actor.ActivityID == "" && actor.ActivityKind == ""
	if !found || !actor.Present || actor.RoomID == "" || actorSleeping {
		if found && (!actor.Present || actorSleeping) {
			presence.SharedRoom = agentroutine.AvailabilityUnavailable
			presence.OwnRoom = agentroutine.AvailabilityUnavailable
		}
		return presence
	}
	for _, roomID := range r.cfg.Heartbeat.Routine.SharedRooms {
		if actor.RoomID == roomID {
			presence.SharedRoom = agentroutine.AvailabilityAvailable
			break
		}
	}
	if presence.SharedRoom != agentroutine.AvailabilityAvailable && canMoveForRoutine && len(r.cfg.Heartbeat.Routine.SharedRooms) > 0 {
		presence.SharedRoom = agentroutine.AvailabilityAvailable
	} else if presence.SharedRoom != agentroutine.AvailabilityAvailable {
		presence.SharedRoom = agentroutine.AvailabilityUnavailable
	}
	if roomID := r.cfg.Heartbeat.Routine.PrivateRooms[actorID]; roomID != "" && (actor.RoomID == roomID || canMoveForRoutine) {
		presence.OwnRoom = agentroutine.AvailabilityAvailable
	} else {
		presence.OwnRoom = agentroutine.AvailabilityUnavailable
	}
	if presence.SharedRoom == agentroutine.AvailabilityAvailable {
		for _, candidateID := range agentHeartbeatActorIDs {
			if candidateID == actorID || r.workers[candidateID] == nil || (filterBusy && !containsRoutineActor(reservedParticipants, candidateID) && r.canStartIdle != nil && !r.canStartIdle(candidateID)) {
				continue
			}
			candidate, ok := actors[candidateID]
			candidateSleeping := ok && (strings.EqualFold(strings.TrimSpace(candidate.ActivityKind), "sleep") || strings.EqualFold(strings.TrimSpace(candidate.ActivityID), "sleep"))
			coLocated := ok && candidate.RoomID == actor.RoomID
			canMoveCandidate := ok && r.worldLife != nil && candidate.ActivityID == "" && candidate.ActivityKind == ""
			if ok && candidate.Present && !candidateSleeping && (coLocated || (canMoveForRoutine && canMoveCandidate)) && r.routinePeerAvailableForChat(candidateID, observedAt) {
				presence.ChatParticipants = append(presence.ChatParticipants, candidateID)
			}
		}
		sort.Strings(presence.ChatParticipants)
	}
	return presence
}

func (r *agentRoutineRuntime) routinePeerAvailableForChat(actorID string, observedAt time.Time) bool {
	if r.store == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	state, err := r.store.Load(ctx, actorID)
	if err != nil || state.Pending != nil {
		return false
	}
	if observedAt.IsZero() {
		observedAt = r.now()
	}
	return !state.LogicalSleepUntil.After(observedAt)
}

func routineHobbyOptions(actorID string) []string {
	switch actorID {
	case "mio":
		return []string{"fashion", "video_creativity"}
	case "shiro":
		return []string{"film_research", "fitness_research"}
	case "kuro":
		return []string{"otaku_culture", "creative_mirror_ideas"}
	case "midori":
		return []string{"reading", "manga_culture", "music_culture", "bl_story_ideas"}
	default:
		return nil
	}
}

func routineHobbyLabel(actorID, date string, slot uint64) string {
	options := routineHobbyOptions(actorID)
	if len(options) == 0 {
		return ""
	}
	day, _ := time.Parse("2006-01-02", date)
	seed := uint64(day.Unix()/86400) + slot
	for _, char := range actorID {
		seed = seed*33 + uint64(char)
	}
	return options[seed%uint64(len(options))]
}

func (r *agentRoutineRuntime) availableRoutineHobbyLabel(actorID, date string, slot uint64) string {
	options := r.availableRoutineHobbyOptions(actorID)
	if len(options) == 0 {
		return ""
	}
	seed := sha256.Sum256([]byte(actorID + "\x00" + date + "\x00" + fmt.Sprint(slot)))
	index := int(uint64(seed[0])<<8|uint64(seed[1])) % len(options)
	return options[index]
}

func (r *agentRoutineRuntime) run(ctx context.Context, stop <-chan struct{}, actorID string, planned agentroutine.Plan) error {
	activityCtx, cancel := context.WithCancel(ctx)
	stopDone := make(chan struct{})
	go func() {
		defer close(stopDone)
		select {
		case <-stop:
			cancel()
		case <-activityCtx.Done():
		}
	}()
	defer func() {
		cancel()
		<-stopDone
	}()
	if routineStopRequested(activityCtx, stop) {
		state, loadErr := r.store.Load(context.Background(), actorID)
		if loadErr != nil || state.Pending == nil {
			return loadErr
		}
		progress, decodeErr := decodeRoutineProgress(state.Pending.Output)
		if decodeErr != nil {
			return decodeErr
		}
		_, _, settleErr := r.routineWorldStopActiveOperations(context.Background(), state, progress)
		return settleErr
	}

	state, err := r.store.Load(activityCtx, actorID)
	if err != nil {
		return err
	}
	if state.Pending == nil {
		fresh, now, err := r.planFresh(activityCtx, actorID, false, planned.Participants)
		if err != nil {
			return err
		}
		if fresh.Kind != planned.Kind || !sameRoutineParticipants(fresh.Participants, planned.Participants) {
			if planned.Kind != agentroutine.ActivityDiary || !r.diaryAmendmentWindow(now) || r.boardClient(actorID) == nil {
				return nil // The discovery snapshot changed before the reservation ran.
			}
			changed, checkErr := r.readDiaryAmendment(activityCtx, actorID, planned.Date)
			if checkErr != nil || !changed {
				return nil // Revalidate changed evidence before creating a canonical Task.
			}
			fresh = planned
		}
		state, err = r.draftIntent(activityCtx, actorID, fresh, now)
		if err != nil {
			return err
		}
	} else {
		pendingProgress, decodeErr := decodeRoutineProgress(state.Pending.Output)
		if decodeErr != nil {
			return decodeErr
		}
		fresh, now, err := r.planFresh(activityCtx, actorID, true, nil)
		if err != nil {
			return err
		}
		if state.Pending.Kind == agentroutine.ActivityDiary && (fresh.Kind == agentroutine.ActivitySleep || fresh.Kind == agentroutine.ActivityWake) {
			state, err = r.pauseDiaryForBoundary(activityCtx, state, pendingProgress, fresh, now)
			if err != nil || state.Pending == nil {
				return err
			}
			return r.run(activityCtx, stop, actorID, fresh)
		}
		if state.Pending.RetryAfter.After(now) {
			return nil
		}
		if routineStopRequested(activityCtx, stop) {
			_, _, settleErr := r.routineWorldStopActiveOperations(context.Background(), state, pendingProgress)
			return settleErr
		}
		if !pendingProgress.EffectCompleted && (state.Pending.Kind == agentroutine.ActivityHobby || state.Pending.Kind == agentroutine.ActivityMorningChat) &&
			!r.pendingIntentStillEligible(activityCtx, state, now, fresh, pendingProgress) {
			settledState, _, settleErr := r.routineWorldStopActiveOperations(context.Background(), state, pendingProgress)
			if settleErr != nil {
				return settleErr
			}
			return r.cancelStaleIntent(activityCtx, settledState)
		}
		if !pendingProgress.EffectCompleted && state.Pending.Kind == agentroutine.ActivityRelax && fresh.Kind != agentroutine.ActivityRelax {
			settledState, _, settleErr := r.routineWorldStopActiveOperations(context.Background(), state, pendingProgress)
			if settleErr != nil {
				return settleErr
			}
			return r.cancelStaleIntent(activityCtx, settledState)
		}
		state, err = r.store.Load(activityCtx, actorID)
		if err != nil {
			return err
		}
	}
	if state.Pending == nil {
		return nil
	}
	intent := *state.Pending
	progress, err := decodeRoutineProgress(intent.Output)
	if err != nil {
		return err
	}
	if intent.Kind == agentroutine.ActivityDiary && r.boardClient(actorID) == nil {
		return r.deferIntent(activityCtx, state, progress, "pinned BOARD diary writer is unavailable")
	}
	prepared, state, progress, err := r.prepareRuns(activityCtx, stop, state, progress)
	if err != nil {
		if routineStopRequested(activityCtx, stop) {
			_, _, settleErr := r.routineWorldStopActiveOperations(context.Background(), state, progress)
			pauseErr := r.pauseRuns(activityCtx, prepared, "routine stopped while starting canonical runs")
			if (errors.Is(err, errAgentRoutineStopped) || errors.Is(err, context.Canceled)) && !errors.Is(err, taskmanager.ErrStartedRunNotClosed) {
				err = nil
			}
			return errors.Join(err, settleErr, pauseErr)
		}
		return r.deferIntent(activityCtx, state, progress, err.Error())
	}
	if routineStopRequested(activityCtx, stop) {
		_, _, settleErr := r.routineWorldStopActiveOperations(context.Background(), state, progress)
		pauseErr := r.pauseRuns(activityCtx, prepared, "routine stopped before provider work")
		return errors.Join(settleErr, pauseErr)
	}
	if state.Pending.ChoiceEligible && !state.Pending.ChoiceCommitted && r.worldLife != nil {
		windowCtx, cancelWindow := r.withRoutineWindow(activityCtx, state.Pending, state)
		state, progress, err = r.prepareRoutineWorldChoice(windowCtx, stop, state, progress)
		cancelWindow()
		if err != nil {
			if routineStopRequested(activityCtx, stop) || errors.Is(err, errAgentRoutineStopped) {
				_, _, settleErr := r.routineWorldStopActiveOperations(context.Background(), state, progress)
				pauseErr := r.pauseRuns(activityCtx, prepared, "routine stopped before WORLD choice confirmation")
				if (errors.Is(err, errAgentRoutineStopped) || errors.Is(err, context.Canceled)) && !errors.Is(err, taskmanager.ErrStartedRunNotClosed) {
					err = nil
				}
				return errors.Join(err, settleErr, pauseErr)
			}
			pauseErr := r.pauseRuns(activityCtx, prepared, "WORLD choice location was not confirmed")
			return errors.Join(r.deferIntent(activityCtx, state, progress, err.Error()), pauseErr)
		}
	}
	if state.Pending.ChoiceEligible && !state.Pending.ChoiceCommitted {
		state, err = routinePersist(activityCtx, func(persistCtx context.Context) (agentroutine.State, error) {
			return r.store.CommitInitiatorSelection(persistCtx, actorID, state.Pending.ID, state.Revision, true)
		})
		if err != nil {
			pauseErr := r.pauseRuns(activityCtx, prepared, "routine stopped or failed before candidate commit")
			return errors.Join(r.deferIntent(activityCtx, state, progress, err.Error()), pauseErr)
		}
	}
	if !progress.EffectCompleted {
		switch intent.Kind {
		case agentroutine.ActivitySleep, agentroutine.ActivityWake, agentroutine.ActivityRelax:
			if r.worldLife != nil {
				state, progress, err = r.executeRoutineWorldActivity(activityCtx, stop, state, progress)
			} else {
				state, progress, err = r.applyLogicalActivity(activityCtx, state, progress)
			}
		case agentroutine.ActivityMorningChat:
			state, progress, err = r.executeMorningChat(activityCtx, stop, state, progress, prepared)
		case agentroutine.ActivityHobby:
			if progress.Media != nil || r.routineMediaConfiguredFor(intent.ActorID, intent.Label) {
				state, progress, err = r.executeRoutineMedia(activityCtx, stop, state, progress)
			} else if r.worldLife != nil && routineWorldHomeHobby(intent.ActorID, intent.Label) {
				state, progress, err = r.executeRoutineWorldActivity(activityCtx, stop, state, progress)
			} else {
				state, progress, err = r.executeHobby(activityCtx, stop, state, progress, prepared[0])
			}
		case agentroutine.ActivityDiary:
			state, progress, err = r.executeDiary(activityCtx, stop, state, progress, prepared[0])
		default:
			err = fmt.Errorf("unsupported routine intent kind %q", intent.Kind)
		}
		if err != nil && intent.Kind == agentroutine.ActivityDiary && errors.Is(err, context.DeadlineExceeded) && !routineStopRequested(activityCtx, stop) {
			boundary, boundaryAt, boundaryErr := r.planFresh(activityCtx, actorID, true, nil)
			if boundaryErr == nil && (boundary.Kind == agentroutine.ActivitySleep || boundary.Kind == agentroutine.ActivityWake) {
				state, progress, checkpointErr := r.saveProgress(activityCtx, state, "diary_window_checkpoint", progress)
				if checkpointErr != nil {
					pauseErr := r.pauseRuns(activityCtx, prepared, "routine reached sleep before its diary boundary checkpoint was saved")
					return errors.Join(err, checkpointErr, pauseErr)
				}
				paused, pauseErr := r.pauseDiaryForBoundary(activityCtx, state, progress, boundary, boundaryAt)
				if pauseErr != nil {
					return pauseErr
				}
				if paused.Pending == nil {
					return nil
				}
				return r.run(activityCtx, stop, actorID, boundary)
			}
		}
		if routineStopRequested(activityCtx, stop) || errors.Is(err, errAgentRoutineStopped) {
			_, _, settleErr := r.routineWorldStopActiveOperations(context.Background(), state, progress)
			pauseErr := r.pauseRuns(activityCtx, prepared, "routine stopped; checkpoint retained")
			if (errors.Is(err, errAgentRoutineStopped) || errors.Is(err, context.Canceled)) && !errors.Is(err, taskmanager.ErrStartedRunNotClosed) {
				err = nil
			}
			return errors.Join(err, settleErr, pauseErr)
		}
		if err != nil {
			pauseErr := r.pauseRuns(activityCtx, prepared, "routine step failed; checkpoint retained")
			return errors.Join(r.deferIntent(activityCtx, state, progress, err.Error()), pauseErr)
		}
	}
	if progress.Media != nil {
		if !progress.Media.Verified {
			return errors.New("routine media is marked complete without a verified playback receipt")
		}
		if err := r.saveRoutineMediaFact(activityCtx, state, *progress.Media); err != nil {
			return err
		}
	}
	if progress.CompletedAt.IsZero() {
		progress.CompletedAt = r.now().UTC()
		state, progress, err = r.saveProgress(activityCtx, state, "activity_timestamp_persisted", progress)
		if err != nil {
			return err
		}
	}
	if err := r.completeRuns(activityCtx, prepared, routineTaskSummary(intent.Kind, progress)); err != nil {
		return err
	}
	if err := r.recordRoutineFacts(activityCtx, intent, progress, progress.CompletedAt); err != nil {
		return err
	}
	if intent.Kind == agentroutine.ActivityDiary {
		state, err = r.advanceDiaryDate(activityCtx, actorID, intent.Date)
		if err != nil {
			return err
		}
	} else {
		state, err = r.store.Load(activityCtx, actorID)
		if err != nil {
			return err
		}
	}
	_, err = routinePersist(activityCtx, func(persistCtx context.Context) (agentroutine.State, error) {
		return r.store.FinishIntent(persistCtx, actorID, intent.ID, state.Revision, r.now().Add(routineCooldown(intent.Kind)))
	})
	return err
}

func (r *agentRoutineRuntime) planFresh(ctx context.Context, actorID string, clearPending bool, reservedParticipants []string) (agentroutine.Plan, time.Time, error) {
	state, now, err := r.ensureState(ctx, actorID)
	if err != nil {
		return agentroutine.Plan{}, now, err
	}
	if clearPending {
		state.Pending = nil
	}
	read, snapshotErr := r.readSnapshotFresh(ctx)
	if snapshotErr != nil {
		read = routineSnapshotRead{}
	}
	presence := r.presence(read.snapshot, actorID, read.readAt, !clearPending, reservedParticipants)
	canHobby := r.canHobby(actorID)
	plan := agentroutine.Decide(now, agentroutine.ActorState{ID: actorID, HobbyConfigured: canHobby}, state, presence, canHobby)
	if plan.Kind == agentroutine.ActivityMorningChat {
		if len(reservedParticipants) == 1 && containsRoutineActor(presence.ChatParticipants, reservedParticipants[0]) {
			plan.Participants = append([]string(nil), reservedParticipants...)
		} else {
			plan.Participants = firstRoutineParticipant(plan.Participants)
		}
	} else {
		plan.Participants = nil
	}
	return plan, now, nil
}

func containsRoutineActor(values []string, actorID string) bool {
	for _, value := range values {
		if value == actorID {
			return true
		}
	}
	return false
}

func (r *agentRoutineRuntime) pendingIntentStillEligible(ctx context.Context, state agentroutine.State, now time.Time, fresh agentroutine.Plan, progress routineProgress) bool {
	intent := state.Pending
	if intent == nil {
		return false
	}
	localNow := now.In(r.location())
	currentDate := localNow.Format("2006-01-02")
	if intent.Date != currentDate {
		previousDate := localNow.AddDate(0, 0, -1).Format("2006-01-02")
		if intent.Date != previousDate || !routineWorldHomeHobby(intent.ActorID, intent.Label) || localNow.Hour() >= 9 {
			return false
		}
	}
	if !state.LogicalSleepUntil.IsZero() && state.LogicalSleepUntil.After(now) {
		return false
	}
	read, err := r.readSnapshotFresh(ctx)
	if err != nil {
		return false
	}
	presence := r.presence(read.snapshot, intent.ActorID, read.readAt, false, nil)
	if intent.Kind == agentroutine.ActivityMorningChat {
		if progress.World != nil && r.worldLife != nil {
			return r.routineWorldPendingMorningEligible(ctx, state, now, fresh, progress)
		}
		if now.In(r.location()).Hour() >= 9 || fresh.PlannedWake.After(now) || presence.SharedRoom != agentroutine.AvailabilityAvailable || len(intent.Participants) != 1 {
			return false
		}
		for _, participant := range presence.ChatParticipants {
			if participant == intent.Participants[0] {
				return true
			}
		}
		return false
	}
	if intent.Kind == agentroutine.ActivityHobby {
		if routineWorldHomeHobby(intent.ActorID, intent.Label) && progress.World != nil && r.worldLife != nil {
			return r.routineWorldPendingHobbyEligible(ctx, state, now, progress)
		}
		if !r.canHobby(intent.ActorID) || presence.OwnRoom != agentroutine.AvailabilityAvailable {
			return false
		}
		hour := now.In(r.location()).Hour()
		if hour >= 22 || hour < 6 {
			if hour < 2 && !fresh.PlannedSleep.After(now) {
				return false
			}
			return true
		}
		return hour < 9 && !fresh.PlannedWake.After(now)
	}
	return true
}

func firstRoutineParticipant(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return []string{values[0]}
}

func sameRoutineParticipants(left, right []string) bool {
	left = firstRoutineParticipant(left)
	right = firstRoutineParticipant(right)
	return len(left) == len(right) && (len(left) == 0 || left[0] == right[0])
}

func (r *agentRoutineRuntime) draftIntent(ctx context.Context, actorID string, plan agentroutine.Plan, now time.Time) (agentroutine.State, error) {
	intent, err := r.newRoutineIntent(actorID, plan, now)
	if err != nil || intent.ID == "" {
		return agentroutine.State{}, err
	}
	state, err := r.store.DraftIntent(ctx, intent)
	if err != nil {
		return agentroutine.State{}, err
	}
	return state, nil
}

func (r *agentRoutineRuntime) newRoutineIntent(actorID string, plan agentroutine.Plan, now time.Time) (agentroutine.Intent, error) {
	if plan.Kind == agentroutine.ActivityWait || plan.Kind == agentroutine.ActivityDaytime {
		return agentroutine.Intent{}, nil
	}
	label := string(plan.Kind)
	if plan.Kind == agentroutine.ActivityHobby {
		slot := uint64(now.In(r.location()).UnixNano() / int64(30*time.Minute))
		label = r.availableRoutineHobbyLabel(actorID, plan.Date, slot)
		if label == "" {
			return agentroutine.Intent{}, errors.New("no configured routine hobby execution is available")
		}
	}
	progress := routineProgress{Actors: []routineActorProgress{{ActorID: actorID, TaskID: modulecore.NewTaskID()}}}
	if plan.Kind == agentroutine.ActivityMorningChat {
		if len(plan.Participants) != 1 || r.workers[actorID] == nil || r.workers[plan.Participants[0]] == nil {
			return agentroutine.Intent{}, errors.New("morning chat does not have two eligible private workers")
		}
		progress.Actors = append(progress.Actors, routineActorProgress{ActorID: plan.Participants[0], TaskID: modulecore.NewTaskID()})
	}
	encoded, err := json.Marshal(progress)
	if err != nil {
		return agentroutine.Intent{}, err
	}
	intentID := string(modulecore.NewRequestID())
	intent := agentroutine.Intent{
		ID: intentID, ActorID: actorID, Date: plan.Date, TaskID: string(progress.Actors[0].TaskID),
		Kind: plan.Kind, Label: label, Participants: append([]string(nil), plan.Participants...),
		ChoiceEligible: plan.CountChoice, Step: "planned", Output: string(encoded), CreatedAt: now.UTC(),
	}
	return intent, nil
}

func decodeRoutineProgress(raw string) (routineProgress, error) {
	var progress routineProgress
	if strings.TrimSpace(raw) == "" {
		return routineProgress{}, errors.New("pending routine intent has no persisted progress payload")
	}
	if err := json.Unmarshal([]byte(raw), &progress); err != nil {
		return routineProgress{}, fmt.Errorf("decode private routine progress: %w", err)
	}
	if len(progress.Actors) == 0 {
		return routineProgress{}, errors.New("pending routine intent has no persisted canonical TaskIDs")
	}
	for _, item := range progress.Actors {
		if !isAgentHeartbeatActor(item.ActorID) || item.TaskID == "" {
			return routineProgress{}, errors.New("pending routine progress contains an invalid actor or TaskID")
		}
		if err := item.TaskID.Validate(); err != nil {
			return routineProgress{}, fmt.Errorf("pending routine TaskID is invalid: %w", err)
		}
		if item.RunID != "" {
			if err := item.RunID.Validate(); err != nil {
				return routineProgress{}, fmt.Errorf("pending routine RunID is invalid: %w", err)
			}
		}
	}
	return progress, nil
}

func (r *agentRoutineRuntime) saveProgress(ctx context.Context, state agentroutine.State, step string, progress routineProgress) (agentroutine.State, routineProgress, error) {
	encoded, err := json.Marshal(progress)
	if err != nil {
		return state, progress, err
	}
	saved, err := routinePersist(ctx, func(persistCtx context.Context) (agentroutine.State, error) {
		return r.store.Checkpoint(persistCtx, state.ActorID, state.Pending.ID, step, string(encoded), state.Revision)
	})
	if err != nil {
		return state, progress, err
	}
	return saved, progress, nil
}

func routinePersist[T any](ctx context.Context, action func(context.Context) (T, error)) (T, error) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	persistCtx, cancel := context.WithTimeout(base, agentRoutinePersistTimeout)
	defer cancel()
	return action(persistCtx)
}

func routinePersistErr(ctx context.Context, action func(context.Context) error) error {
	_, err := routinePersist(ctx, func(persistCtx context.Context) (struct{}, error) {
		return struct{}{}, action(persistCtx)
	})
	return err
}

func routineStopRequested(ctx context.Context, stop <-chan struct{}) bool {
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

func (r *agentRoutineRuntime) prepareRuns(ctx context.Context, stop <-chan struct{}, state agentroutine.State, progress routineProgress) ([]routinePreparedRun, agentroutine.State, routineProgress, error) {
	prepared := make([]routinePreparedRun, 0, len(progress.Actors))
	abortPrepared := func(cause error) ([]routinePreparedRun, agentroutine.State, routineProgress, error) {
		if pauseErr := r.pauseRuns(ctx, prepared, "routine Run preparation ended before provider work: "+cause.Error()); pauseErr != nil {
			return prepared, state, progress, errors.Join(cause, fmt.Errorf("pause partially prepared routine Runs: %w", pauseErr))
		}
		return nil, state, progress, cause
	}
	for index := range progress.Actors {
		if routineStopRequested(ctx, stop) {
			return abortPrepared(errAgentRoutineStopped)
		}
		task, run, err := r.prepareRun(ctx, state.Pending, progress.Actors[index])
		if err != nil {
			return abortPrepared(err)
		}
		prepared = append(prepared, routinePreparedRun{progressIndex: index, task: task, run: run})
		progress.Actors[index].RunID = run.RunID
		state, progress, err = r.saveProgress(ctx, state, "canonical_run_ready", progress)
		if err != nil {
			return abortPrepared(err)
		}
	}
	return prepared, state, progress, nil
}

func (r *agentRoutineRuntime) prepareRun(ctx context.Context, intent *agentroutine.Intent, actorProgress routineActorProgress) (domaintask.Task, domaintask.Run, error) {
	taskID := actorProgress.TaskID
	task, err := r.tasks.Get(ctx, taskID)
	if errors.Is(err, domaintask.ErrNotFound) {
		draft := domaintask.Task{
			TaskID: taskID, Title: routineTaskTitle(intent.Kind), ModuleID: agentRoutineModuleID,
			OwnerID: r.ownerID, Assignee: actorProgress.ActorID, Route: domaintask.RouteCHAT,
			Priority: domaintask.PriorityLow, InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true,
		}
		started, run, createErr := r.tasks.CreateAndStartRun(ctx, draft, domaintask.SharedRoleContext{
			TaskID: taskID, UserIntent: "CORE routine step", CurrentPlan: routineTaskTitle(intent.Kind),
		})
		if createErr != nil {
			return domaintask.Task{}, domaintask.Run{}, fmt.Errorf("create canonical routine Task and Run: %w", createErr)
		}
		return started, run, nil
	}
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, fmt.Errorf("read canonical routine Task: %w", err)
	}
	if task.TaskID != taskID || task.OwnerID != r.ownerID || task.ModuleID != agentRoutineModuleID || task.Assignee != actorProgress.ActorID || task.Route != domaintask.RouteCHAT {
		return domaintask.Task{}, domaintask.Run{}, errors.New("pending routine Task does not match its exact module, assignee, and route")
	}
	runs, err := r.tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID})
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, fmt.Errorf("list canonical routine Runs: %w", err)
	}
	if len(runs) == 0 {
		return domaintask.Task{}, domaintask.Run{}, errors.New("persisted routine Task has no canonical Run")
	}
	latest := runs[len(runs)-1]
	if latest.TaskID != taskID || latest.Assignee != actorProgress.ActorID || latest.Validate() != nil {
		return domaintask.Task{}, domaintask.Run{}, errors.New("latest routine Run does not match its canonical Task")
	}
	switch {
	case task.Status == domaintask.StatusRunning && latest.Status == domaintask.RunStatusRunning:
		if !routineRunMatchesCheckpoint(intent, actorProgress, latest) {
			return domaintask.Task{}, domaintask.Run{}, errors.New("running routine Run does not match its persisted checkpoint")
		}
		if err := r.validateRoutineRun(ctx, task, latest, actorProgress.ActorID); err == nil {
			return task, latest, nil
		}
		_, recovered, recoverErr := r.tasks.RecoverRunAfterProcessRestart(ctx, taskID, latest.RunID, actorProgress.ActorID,
			domaintask.StatusWaiting, "CORE routine resumed after process restart", "CORE restarted during a routine step")
		if recoverErr != nil {
			return domaintask.Task{}, domaintask.Run{}, fmt.Errorf("recover stale routine Run: %w", recoverErr)
		}
		if !recovered {
			return domaintask.Task{}, domaintask.Run{}, errors.New("active routine Run is not owned by this process and is not an exact stale generation")
		}
		return r.resumeRoutineRun(ctx, taskID, latest.RunID, actorProgress.ActorID, intent)
	case task.Status == domaintask.StatusWaiting && latest.Status == domaintask.RunStatusWaiting:
		return r.resumeRoutineRun(ctx, taskID, latest.RunID, actorProgress.ActorID, intent)
	case task.Status == domaintask.StatusSucceeded && latest.Status == domaintask.RunStatusSucceeded:
		if !routineProgressHasEffect(intent, actorProgress.ActorID) {
			return domaintask.Task{}, domaintask.Run{}, errors.New("routine Task succeeded without a persisted routine result")
		}
		return task, latest, nil
	default:
		return domaintask.Task{}, domaintask.Run{}, fmt.Errorf("routine Task/Run lifecycle is not resumable: task=%s run=%s", task.Status, latest.Status)
	}
}

func routineRunMatchesCheckpoint(intent *agentroutine.Intent, progress routineActorProgress, run domaintask.Run) bool {
	if run.RunID == progress.RunID {
		return true
	}
	if progress.RunID == "" && run.StartReason == domaintask.RunStartReasonFirst {
		return true
	}
	return run.StartReason == domaintask.RunStartReasonCheckpointResume && run.StartCheckpointSHA256 == routineCheckpointDigest(intent)
}

func (r *agentRoutineRuntime) pauseDiaryForBoundary(ctx context.Context, state agentroutine.State, progress routineProgress, plan agentroutine.Plan, now time.Time) (agentroutine.State, error) {
	if state.Pending == nil || state.Pending.Kind != agentroutine.ActivityDiary ||
		(plan.Kind != agentroutine.ActivitySleep && plan.Kind != agentroutine.ActivityWake) {
		return agentroutine.State{}, errors.New("routine boundary cannot pause this pending intent")
	}
	if err := r.pauseExistingDiaryRuns(ctx, state.Pending, progress); err != nil {
		return agentroutine.State{}, err
	}
	var boundary *agentroutine.Intent
	alreadySleeping := plan.Kind == agentroutine.ActivitySleep && state.ScheduleDate == plan.ScheduleDate && state.LogicalSleepUntil.After(now)
	if !alreadySleeping {
		intent, err := r.newRoutineIntent(state.ActorID, plan, now)
		if err != nil {
			return agentroutine.State{}, err
		}
		boundary = &intent
	}
	saved, err := routinePersist(ctx, func(persistCtx context.Context) (agentroutine.State, error) {
		latest, loadErr := r.store.Load(persistCtx, state.ActorID)
		if loadErr != nil {
			return agentroutine.State{}, loadErr
		}
		if latest.Revision != state.Revision || latest.Pending == nil || latest.Pending.ID != state.Pending.ID {
			return agentroutine.State{}, agentroutine.ErrConflict
		}
		if latest.PausedDiary != nil {
			return agentroutine.State{}, agentroutine.ErrPendingIntent
		}
		latest.PausedDiary = latest.Pending
		latest.Pending = boundary
		return r.store.Save(persistCtx, latest, latest.Revision)
	})
	return saved, err
}

// pauseExistingDiaryRuns closes only the canonical Run already linked by the
// pending checkpoint. It never creates or resumes a Run merely to defer sleep.
func (r *agentRoutineRuntime) pauseExistingDiaryRuns(ctx context.Context, intent *agentroutine.Intent, progress routineProgress) error {
	if r.tasks == nil {
		return errors.New("routine boundary requires the canonical TaskManager to close a started Run")
	}
	if len(progress.Actors) != 1 || progress.Actors[0].ActorID != intent.ActorID {
		return errors.New("pending diary checkpoint does not contain exactly its canonical actor")
	}
	for _, actor := range progress.Actors {
		task, err := routinePersist(ctx, func(persistCtx context.Context) (domaintask.Task, error) {
			return r.tasks.Get(persistCtx, actor.TaskID)
		})
		if errors.Is(err, domaintask.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read pending diary Task before sleep: %w", err)
		}
		if task.TaskID != actor.TaskID || task.OwnerID != r.ownerID || task.ModuleID != agentRoutineModuleID ||
			task.Assignee != actor.ActorID || task.Route != domaintask.RouteCHAT {
			return errors.New("pending diary Task does not match its exact owner/module/assignee/route")
		}
		runs, err := routinePersist(ctx, func(persistCtx context.Context) ([]domaintask.Run, error) {
			return r.tasks.ListRuns(persistCtx, domaintask.RunFilter{TaskID: task.TaskID})
		})
		if err != nil {
			return fmt.Errorf("list pending diary Task Runs before sleep: %w", err)
		}
		if len(runs) == 0 {
			if task.Status == domaintask.StatusRunning {
				return errors.Join(taskmanager.ErrStartedRunNotClosed, errors.New("pending diary Task is running without a verifiable Run"))
			}
			continue
		}
		latest := runs[len(runs)-1]
		if latest.TaskID != task.TaskID || latest.Assignee != actor.ActorID || latest.Validate() != nil {
			return errors.New("latest pending diary Run does not match its canonical Task")
		}
		if latest.Status != domaintask.RunStatusRunning && task.Status != domaintask.StatusRunning {
			continue
		}
		if task.Status != domaintask.StatusRunning || latest.Status != domaintask.RunStatusRunning ||
			!routineRunMatchesCheckpoint(intent, actor, latest) {
			return errors.Join(taskmanager.ErrStartedRunNotClosed, errors.New("pending diary has a started Run that does not match its persisted checkpoint"))
		}
		if validateErr := r.validateRoutineRun(ctx, task, latest, actor.ActorID); validateErr != nil {
			_, recovered, recoverErr := r.tasks.RecoverRunAfterProcessRestart(ctx, task.TaskID, latest.RunID, actor.ActorID,
				domaintask.StatusWaiting, "CORE routine deferred for planned sleep", "started diary Run retained at its exact checkpoint")
			if recoverErr != nil {
				return errors.Join(taskmanager.ErrStartedRunNotClosed, fmt.Errorf("recover exact diary Run before sleep: %w", recoverErr))
			}
			if recovered {
				continue
			}
		}
		if err := routinePersistErr(ctx, func(persistCtx context.Context) error {
			_, completeErr := r.tasks.CompleteRun(persistCtx, task.TaskID, latest.RunID, actor.ActorID,
				domaintask.StatusWaiting, "CORE agent routine paused", "diary retained for after planned wake")
			return completeErr
		}); err != nil {
			return errors.Join(taskmanager.ErrStartedRunNotClosed, fmt.Errorf("pause exact diary Run before sleep: %w", err))
		}
	}
	return nil
}

func routineProgressHasEffect(intent *agentroutine.Intent, actorID string) bool {
	if intent == nil {
		return false
	}
	progress, err := decodeRoutineProgress(intent.Output)
	if err != nil {
		return false
	}
	if progress.EffectCompleted {
		return true
	}
	for _, actor := range progress.Actors {
		if actor.ActorID == actorID && actor.Output != "" {
			return true
		}
	}
	return false
}

func (r *agentRoutineRuntime) resumeRoutineRun(ctx context.Context, taskID modulecore.TaskID, predecessor modulecore.RunID, actorID string, intent *agentroutine.Intent) (domaintask.Task, domaintask.Run, error) {
	digest := routineCheckpointDigest(intent)
	run, err := r.tasks.StartRunFromCheckpoint(ctx, taskID, predecessor, actorID, domaintask.RunStartReasonCheckpointResume, digest)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, fmt.Errorf("resume routine from checkpoint: %w", err)
	}
	task, err := r.tasks.Get(ctx, taskID)
	if err != nil {
		cause := fmt.Errorf("read canonical Task after checkpoint resume: %w", err)
		pauseErr := r.pauseRuns(ctx, []routinePreparedRun{{
			task: domaintask.Task{TaskID: taskID, Assignee: actorID}, run: run,
		}}, "CORE routine checkpoint resume Task read failed: "+err.Error())
		if pauseErr != nil {
			return domaintask.Task{}, domaintask.Run{}, errors.Join(cause, fmt.Errorf("requeue started checkpoint Run %s: %w", run.RunID, pauseErr))
		}
		return domaintask.Task{}, domaintask.Run{}, cause
	}
	return task, run, nil
}

func routineCheckpointDigest(intent *agentroutine.Intent) string {
	if intent == nil {
		digest := sha256.Sum256(nil)
		return hex.EncodeToString(digest[:])
	}
	digest := sha256.Sum256([]byte(intent.Step + "\n" + intent.Output))
	return hex.EncodeToString(digest[:])
}

func (r *agentRoutineRuntime) validateRoutineRun(ctx context.Context, task domaintask.Task, run domaintask.Run, actorID string) error {
	traceID := modulecore.NewTraceID()
	actorCtx, err := execution.WithIdentity(ctx, task.TaskID, run.RunID, traceID)
	if err != nil {
		return err
	}
	scope, err := routineToolScope(traceID, actorID)
	if err != nil {
		return err
	}
	actorCtx = domaintool.WithToolExecutionScope(actorCtx, scope)
	_, err = r.tasks.ValidateExecutionContext(actorCtx)
	return err
}

func routineToolScope(traceID modulecore.TraceID, actorID string) (domaintool.ToolExecutionScope, error) {
	scope, err := domaintool.NewToolExecutionScope(string(traceID), domaintool.ActorKindAgent, actorID, "",
		[]string{domaintool.DataScopePublic}, domaintool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		return domaintool.ToolExecutionScope{}, err
	}
	scope.AgentRole = "worker"
	scope.Purpose = "private_task_execution"
	if err := scope.Validate(); err != nil {
		return domaintool.ToolExecutionScope{}, err
	}
	return scope, nil
}

func routineTaskTitle(kind agentroutine.ActivityKind) string {
	switch kind {
	case agentroutine.ActivityMorningChat:
		return "CORE agent routine: private conversation"
	case agentroutine.ActivityHobby:
		return "CORE agent routine: public research and private note"
	case agentroutine.ActivityDiary:
		return "CORE agent routine: daily diary"
	case agentroutine.ActivitySleep, agentroutine.ActivityWake:
		return "CORE agent routine: logical sleep eligibility"
	default:
		return "CORE agent routine: scheduled step"
	}
}

func routineTaskSummary(kind agentroutine.ActivityKind, progress routineProgress) string {
	switch kind {
	case agentroutine.ActivitySleep:
		if routineWorldHasVerifiedEvent(progress, worldread.HomeActionSleep, "life.home.sleep.started") {
			return "Started and verified sleep in the configured WORLD bed."
		}
		return "Logical sleep gate updated; no physical sleep or WORLD action was performed."
	case agentroutine.ActivityWake:
		if routineWorldHasVerifiedEvent(progress, worldread.HomeActionWake, "life.home.sleep.woke") {
			return "Completed and verified the WORLD wake transition."
		}
		return "Logical wake gate updated; no physical WORLD action was performed."
	case agentroutine.ActivityMorningChat:
		return "Completed a private two-actor text exchange; dialogue remains local."
	case agentroutine.ActivityHobby:
		if progress.Media != nil && progress.Media.Verified {
			return "Recorded a verified bounded playback interval from configured media."
		}
		if routineWorldHasVerifiedEvent(progress, worldread.HomeActionFinish, "life.home.exercise.completed") {
			return "Completed a verified WORLD exercise activity."
		}
		if routineWorldHasVerifiedEvent(progress, worldread.HomeActionFinish, "life.home.mirror_pose.completed") {
			return "Completed a verified WORLD mirror pose activity."
		}
		if progress.SearchResult != "" && progress.HobbyText != "" {
			return "Read bounded public WebGather results and saved a private generated note."
		}
		return "Completed a CORE hobby step; activity details were not confirmed."
	case agentroutine.ActivityRelax:
		if progress.World != nil && progress.World.RelaxRoomID != "" && !progress.World.RelaxObservedAt.IsZero() {
			return "Confirmed quiet time in the configured home room."
		}
		return "Completed the scheduled relaxation step; no room activity was confirmed."
	case agentroutine.ActivityDiary:
		return "Submitted the exact saved diary revision to the pinned BOARD client."
	default:
		return "Completed a CORE routine bookkeeping step."
	}
}

func routineWorldHasVerifiedEvent(progress routineProgress, action, eventKind string) bool {
	if progress.World == nil {
		return false
	}
	for _, operation := range progress.World.Operations {
		if operation.Action == action && operation.EventKind == eventKind && operation.ReceiptID != "" &&
			operation.ReceiptStatus == "committed" && operation.EventVerified && operation.ActivityID != "" {
			return true
		}
	}
	return false
}

func (r *agentRoutineRuntime) executeMorningChat(ctx context.Context, stop <-chan struct{}, state agentroutine.State, progress routineProgress, prepared []routinePreparedRun) (agentroutine.State, routineProgress, error) {
	if len(prepared) != 2 || len(state.Pending.Participants) != 1 {
		return state, progress, errors.New("morning chat checkpoint does not contain exactly two eligible actors")
	}
	windowCtx, cancelWindow := r.withRoutineWindow(ctx, state.Pending, state)
	defer cancelWindow()
	ctx = windowCtx
	if ctx.Err() != nil {
		return state, progress, errAgentRoutineStopped
	}
	if r.worldLife != nil {
		var err error
		state, progress, err = r.ensureRoutineMorningArrivals(ctx, stop, state, progress)
		if err != nil {
			return state, progress, err
		}
	}
	if err := r.revalidateChat(ctx, state.Pending); err != nil {
		return state, progress, err
	}
	for index := range prepared {
		if routineStopRequested(ctx, stop) {
			return state, progress, errAgentRoutineStopped
		}
		actor := &progress.Actors[prepared[index].progressIndex]
		if actor.Output != "" {
			continue
		}
		prompt := "朝の共有時間です。短い挨拶から始めて、今日の穏やかな過ごし方について一往復の会話をしてください。個人情報やユーザー向け報告は含めず、自然な一言で返してください。"
		if index > 0 {
			prompt = "相手からの朝の返事を受けて、会話を短く自然に続けてください。会話内容を外部へ持ち出さないでください。相手の発言:\n" + progress.Actors[0].Output
		}
		output, err := r.executePrivateWorker(ctx, prepared[index], actor.ActorID, prompt)
		if err != nil {
			if ctx.Err() != nil {
				return state, progress, errAgentRoutineStopped
			}
			return state, progress, err
		}
		actor.Output = truncateRoutineRunes(strings.TrimSpace(output), 4000)
		if actor.Output == "" {
			return state, progress, errors.New("private morning chat worker returned empty text")
		}
		state, progress, err = r.saveProgress(ctx, state, "morning_chat_turn_saved", progress)
		if err != nil {
			return state, progress, err
		}
	}
	progress.EffectCompleted = true
	return r.saveProgress(ctx, state, "morning_chat_completed", progress)
}

func (r *agentRoutineRuntime) revalidateChat(ctx context.Context, intent *agentroutine.Intent) error {
	read, err := r.readSnapshotFresh(ctx)
	if err != nil {
		return err
	}
	if read.snapshot.WorldID == "" || len(intent.Participants) != 1 {
		return errors.New("morning chat requires a fresh authenticated WORLD co-location snapshot")
	}
	presence := r.presence(read.snapshot, intent.ActorID, read.readAt, false, nil)
	for _, participant := range presence.ChatParticipants {
		if participant == intent.Participants[0] && presence.SharedRoom == agentroutine.AvailabilityAvailable {
			return nil
		}
	}
	return errors.New("morning chat participants are no longer confirmed together in a configured shared room")
}

func (r *agentRoutineRuntime) executeHobby(ctx context.Context, stop <-chan struct{}, state agentroutine.State, progress routineProgress, prepared routinePreparedRun) (agentroutine.State, routineProgress, error) {
	if !r.canHobby(state.Pending.ActorID) {
		return state, progress, errors.New("public hobby research is unavailable for this actor")
	}
	windowCtx, cancelWindow := r.withRoutineWindow(ctx, state.Pending, state)
	defer cancelWindow()
	ctx = windowCtx
	if ctx.Err() != nil {
		return state, progress, errAgentRoutineStopped
	}
	if r.worldLife != nil {
		roomID := r.cfg.Heartbeat.Routine.PrivateRooms[state.Pending.ActorID]
		if roomID == "" {
			return state, progress, errors.New("WORLD WebGather hobby requires the actor's configured private room")
		}
		var err error
		state, progress, _, err = r.ensureRoutineWorldArrival(ctx, stop, state, progress, state.Pending.ActorID, roomID, "", "hobby-private-room", routineWorldMoveWait)
		if err != nil {
			return state, progress, err
		}
	}
	read, err := r.readSnapshotFresh(ctx)
	if err != nil {
		return state, progress, err
	}
	if r.presence(read.snapshot, state.Pending.ActorID, read.readAt, false, nil).OwnRoom != agentroutine.AvailabilityAvailable {
		return state, progress, errors.New("private hobby requires the actor's configured private room in a fresh WORLD snapshot")
	}
	if progress.SearchResult == "" {
		if routineStopRequested(ctx, stop) {
			return state, progress, errAgentRoutineStopped
		}
		searchCtx, cancel := context.WithTimeout(ctx, agentRoutineWebTimeout)
		searchResult, err := r.searchHobby(searchCtx, prepared, state.Pending.ActorID, state.Pending.Label)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return state, progress, errAgentRoutineStopped
			}
			return state, progress, err
		}
		progress.SearchResult = searchResult
		state, progress, err = r.saveProgress(ctx, state, "web_gather_read_saved", progress)
		if err != nil {
			return state, progress, err
		}
	}
	if routineStopRequested(ctx, stop) {
		return state, progress, errAgentRoutineStopped
	}
	if progress.HobbyText == "" {
		prompt := fmt.Sprintf("次の公開検索結果を読んで、趣味として楽しめる短い創作メモを日本語で書いてください。検索結果の範囲を超えた事実は断定せず、動画再生・映画鑑賞・運動など実行していない行動をしたとは書かないでください。テーマ=%s\n検索結果:\n%s", state.Pending.Label, progress.SearchResult)
		output, err := r.executePrivateWorker(ctx, prepared, state.Pending.ActorID, prompt)
		if err != nil {
			if ctx.Err() != nil {
				return state, progress, errAgentRoutineStopped
			}
			return state, progress, err
		}
		progress.HobbyText = truncateRoutineRunes(strings.TrimSpace(output), 4000)
		if progress.HobbyText == "" {
			return state, progress, errors.New("named actor hobby provider returned empty text")
		}
		state, progress, err = r.saveProgress(ctx, state, "hobby_generation_saved", progress)
		if err != nil {
			return state, progress, err
		}
	}
	progress.Actors[prepared.progressIndex].Output = progress.HobbyText
	progress.EffectCompleted = true
	return r.saveProgress(ctx, state, "hobby_completed", progress)
}

func (r *agentRoutineRuntime) searchHobby(ctx context.Context, prepared routinePreparedRun, actorID, label string) (string, error) {
	if err := r.validateRoutineRun(ctx, prepared.task, prepared.run, actorID); err != nil {
		return "", fmt.Errorf("validate routine web search Run: %w", err)
	}
	actorCtx, traceID, err := r.executionContext(ctx, prepared, actorID)
	if err != nil {
		return "", err
	}
	actorCtx, err = withTrustedAgentPublicToolScope(actorCtx, string(traceID), actorID)
	if err != nil {
		return "", fmt.Errorf("bind public hobby search scope: %w", err)
	}
	actorCtx = llm.WithExecutionObservationDefaults(actorCtx, llm.ExecutionObservation{
		TaskID: prepared.task.TaskID, TraceID: string(traceID), Initiator: actorID, Caller: "agent_routine", Purpose: "public_hobby_research",
	})
	query := routineHobbyQuery(actorID, label)
	args := map[string]any{
		"query": query, "limit": 5, "max_fetches": 5, "language": "ja", "freshness": "month",
		"namespace": "agent-routine:" + actorID, "store_staging": false,
	}
	args["provider"] = routineHobbySearchProvider(r.cfg)
	callCtx, cancel := context.WithTimeout(actorCtx, agentRoutineWebTimeout)
	defer cancel()
	response, err := r.tools.ExecuteV2(callCtx, "web_gather.search_and_fetch", args)
	if err != nil {
		return "", errors.New("read public hobby sources: WebGather call failed")
	}
	if response == nil || response.IsError() {
		return "", errors.New("read public hobby sources: WebGather returned an error")
	}
	encoded, err := json.Marshal(response.Result)
	if err != nil {
		return "", errors.New("WebGather result could not be decoded")
	}
	var result modulewebgather.SearchAndFetchResponse
	if err := json.Unmarshal(encoded, &result); err != nil {
		return "", errors.New("WebGather returned an unexpected search-and-fetch shape")
	}
	type savedSource struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Preview string `json:"preview"`
	}
	sources := make([]savedSource, 0, 5)
	remaining := 10_000
	for _, item := range result.Items {
		if !strings.EqualFold(strings.TrimSpace(item.Fetch.Status), "ok") {
			continue
		}
		sourceURL := strings.TrimSpace(item.Fetch.FinalURL)
		if sourceURL == "" {
			sourceURL = strings.TrimSpace(item.SearchResult.URL)
		}
		parsed, parseErr := url.Parse(sourceURL)
		if parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
			continue
		}
		preview := strings.TrimSpace(item.Fetch.TextPreview)
		if preview == "" {
			continue
		}
		preview = truncateRoutineRunes(preview, min(900, remaining))
		if preview == "" {
			break
		}
		title := truncateRoutineRunes(strings.TrimSpace(item.Fetch.Title), 160)
		if title == "" {
			title = truncateRoutineRunes(strings.TrimSpace(item.SearchResult.Title), 160)
		}
		sources = append(sources, savedSource{Title: title, URL: parsed.String(), Preview: preview})
		remaining -= len([]rune(preview))
		if len(sources) >= 5 || remaining <= 0 {
			break
		}
	}
	if len(sources) == 0 {
		return "", errors.New("WebGather did not return a fetched readable source")
	}
	bounded, err := json.Marshal(sources)
	if err != nil {
		return "", errors.New("could not save bounded public references")
	}
	return string(bounded), nil
}

func routineHobbySearchProvider(cfg *config.Config) string {
	if cfg == nil {
		return "bing_rss"
	}
	if strings.TrimSpace(cfg.WebGather.SearXNGBaseURL) != "" {
		return "searxng"
	}
	if strings.TrimSpace(cfg.WebGather.YaCyBaseURL) != "" {
		return "yacy"
	}
	return "bing_rss"
}

func truncateRoutineRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) > limit {
		return strings.TrimSpace(string(runes[:limit]))
	}
	return value
}

func routineHobbyQuery(actorID, label string) string {
	queries := map[string]map[string]string{
		"mio":    {"fashion": "ファッション コーディネート", "video_creativity": "動画編集 アイデア"},
		"shiro":  {"film_research": "映画 映像表現", "fitness_research": "筋トレ 初心者"},
		"kuro":   {"otaku_culture": "anime manga culture", "creative_mirror_ideas": "cosplay mirror pose photography"},
		"midori": {"reading": "読書 書評", "manga_culture": "manga new releases", "music_culture": "音楽 新譜", "bl_story_ideas": "BL 小説"},
	}
	if query := queries[actorID][label]; query != "" {
		return query
	}
	return "日本 文化 創作 アイデア"
}

func (r *agentRoutineRuntime) executePrivateWorker(ctx context.Context, prepared routinePreparedRun, actorID, prompt string) (string, error) {
	worker := r.workers[actorID]
	if worker == nil {
		return "", fmt.Errorf("private worker for actor %s is unavailable", actorID)
	}
	actorCtx, traceID, err := r.executionContext(ctx, prepared, actorID)
	if err != nil {
		return "", err
	}
	address, err := conversation.NewChannelAddress("agent_routine", string(prepared.task.TaskID))
	if err != nil {
		return "", err
	}
	input, err := conversation.ReconstructTurnInput(
		prepared.task.TaskID, modulecore.NewTurnID(), traceID, modulecore.NewMessageID(), modulecore.NewMessageID(), prompt, address,
	)
	if err != nil {
		return "", fmt.Errorf("build private routine input: %w", err)
	}
	input = input.WithViewerRecipient(actorID).WithRoute(domainrouting.RouteCHAT)
	callCtx, cancel := context.WithTimeout(actorCtx, agentRoutineProviderTimeout)
	defer cancel()
	output, err := worker.ExecutePrivateWork(callCtx, input, []string{})
	if err != nil {
		return "", fmt.Errorf("named private routine provider %s failed", actorID)
	}
	return output, nil
}

func (r *agentRoutineRuntime) executionContext(ctx context.Context, prepared routinePreparedRun, actorID string) (context.Context, modulecore.TraceID, error) {
	traceID := modulecore.NewTraceID()
	actorCtx, err := execution.WithIdentity(ctx, prepared.task.TaskID, prepared.run.RunID, traceID)
	if err != nil {
		return nil, "", fmt.Errorf("bind routine Task/Run identity: %w", err)
	}
	scope, err := routineToolScope(traceID, actorID)
	if err != nil {
		return nil, "", fmt.Errorf("create routine actor scope: %w", err)
	}
	actorCtx = domaintool.WithToolExecutionScope(actorCtx, scope)
	actorCtx = llm.WithExecutionObservationDefaults(actorCtx, llm.ExecutionObservation{
		TaskID: prepared.task.TaskID, TraceID: string(traceID), Initiator: actorID, Caller: "agent_routine", Purpose: "private_task_execution",
	})
	if _, err := r.tasks.ValidateExecutionContext(actorCtx); err != nil {
		return nil, "", fmt.Errorf("validate routine canonical Task/Run: %w", err)
	}
	return actorCtx, traceID, nil
}

func (r *agentRoutineRuntime) applyLogicalActivity(ctx context.Context, state agentroutine.State, progress routineProgress) (agentroutine.State, routineProgress, error) {
	intent := state.Pending
	switch intent.Kind {
	case agentroutine.ActivitySleep:
		if !state.LogicalSleepUntil.Equal(state.PlannedWake) {
			state.LogicalSleepUntil = state.PlannedWake
			saved, err := routinePersist(ctx, func(persistCtx context.Context) (agentroutine.State, error) {
				return r.store.Save(persistCtx, state, state.Revision)
			})
			if err != nil {
				return state, progress, err
			}
			state = saved
		}
	case agentroutine.ActivityWake:
		if !state.LogicalSleepUntil.IsZero() {
			state.LogicalSleepUntil = time.Time{}
			saved, err := routinePersist(ctx, func(persistCtx context.Context) (agentroutine.State, error) {
				return r.store.Save(persistCtx, state, state.Revision)
			})
			if err != nil {
				return state, progress, err
			}
			state = saved
		}
	}
	progress.EffectCompleted = true
	return r.saveProgress(ctx, state, "logical_gate_updated", progress)
}

func (r *agentRoutineRuntime) executeDiary(ctx context.Context, stop <-chan struct{}, state agentroutine.State, progress routineProgress, prepared routinePreparedRun) (agentroutine.State, routineProgress, error) {
	actorID := state.Pending.ActorID
	boardClient := r.boardClient(actorID)
	var err error
	if boardClient == nil {
		return state, progress, errors.New("pinned BOARD diary writer is unavailable")
	}
	diary, diaryErr := r.store.GetDiary(ctx, actorID, state.Pending.Date)
	if diaryErr != nil && !errors.Is(diaryErr, agentroutine.ErrNotFound) {
		return state, progress, diaryErr
	}
	if diaryErr == nil && diary.Receipt != "" {
		if progress.DiaryRevision == diary.Revision && progress.DiaryBody == diary.Body && progress.DiaryReflection == diary.Reflection {
			progress.DiaryReceipt, progress.DiaryReceiptRevision = diary.Receipt, diary.ReceiptRevision
			progress.DiaryPostingKey = diary.PostingKey
			progress.EffectCompleted = true
			return r.saveProgress(ctx, state, "diary_receipt_recovered", progress)
		}
		latestBody, bodyErr := r.buildRoutineDiaryBody(ctx, state)
		if bodyErr != nil {
			return state, progress, bodyErr
		}
		if latestBody == diary.Body {
			progress.DiaryBody, progress.DiaryReflection = diary.Body, diary.Reflection
			progress.DiaryRevision, progress.DiaryPostingKey = diary.Revision, diary.PostingKey
			progress.DiaryReceipt, progress.DiaryReceiptRevision = diary.Receipt, diary.ReceiptRevision
			progress.EffectCompleted = true
			return r.saveProgress(ctx, state, "diary_receipt_recovered", progress)
		}
		// This is a bounded catch-up for the latest previously published day.
		// Only changed confirmed facts create a new revision.
		if progress.DiaryRevision != diary.Revision+1 || progress.DiaryBody != latestBody {
			progress.DiaryBody, progress.DiaryReflection = latestBody, ""
			progress.DiaryRevision, progress.DiaryPostingKey = diary.Revision+1, ""
		}
		progress.DiaryReceipt, progress.DiaryReceiptRevision = "", ""
		progress.EffectCompleted = false
		state, progress, err = r.saveProgress(ctx, state, "diary_amendment_body_saved", progress)
		if err != nil {
			return state, progress, err
		}
		diaryErr = agentroutine.ErrNotFound // The changed body now follows the new-revision path below.
	} else if diaryErr == nil {
		// An unacknowledged outbox draft must be replayed exactly before a newer
		// body or reflection can be generated.
		progress.DiaryBody, progress.DiaryReflection = diary.Body, diary.Reflection
		progress.DiaryRevision, progress.DiaryPostingKey = diary.Revision, diary.PostingKey
	}
	if errors.Is(diaryErr, agentroutine.ErrNotFound) {
		if progress.DiaryBody == "" {
			body, err := r.buildRoutineDiaryBody(ctx, state)
			if err != nil {
				return state, progress, err
			}
			progress.DiaryBody = body
		}
		if progress.DiaryReflection == "" {
			if routineStopRequested(ctx, stop) {
				return state, progress, errAgentRoutineStopped
			}
			prompt := routineDiaryReflectionPrompt(progress.DiaryBody)
			windowCtx, cancelWindow := r.withRoutineWindow(ctx, state.Pending, state)
			reflection, err := r.executePrivateWorker(windowCtx, prepared, actorID, prompt)
			windowErr := windowCtx.Err()
			cancelWindow()
			if err != nil {
				if errors.Is(windowErr, context.DeadlineExceeded) {
					return state, progress, errors.Join(err, context.DeadlineExceeded)
				}
				return state, progress, err
			}
			progress.DiaryReflection = truncateRoutineRunes(strings.TrimSpace(reflection), 2000)
			if progress.DiaryReflection == "" {
				return state, progress, errors.New("diary reflection provider returned empty text")
			}
			state, progress, err = r.saveProgress(ctx, state, "diary_reflection_saved", progress)
			if err != nil {
				return state, progress, err
			}
		}
		diary, err = routinePersist(ctx, func(persistCtx context.Context) (agentroutine.Diary, error) {
			return r.store.SaveDiary(persistCtx, agentroutine.Diary{
				ActorID: actorID, Date: state.Pending.Date, Revision: progress.DiaryRevision,
				Body: progress.DiaryBody, Reflection: progress.DiaryReflection, PostingKey: progress.DiaryPostingKey,
			})
		})
		if err != nil {
			return state, progress, err
		}
	}
	progress.DiaryRevision, progress.DiaryPostingKey = diary.Revision, diary.PostingKey
	state, progress, err = r.saveProgress(ctx, state, "diary_put_pending", progress)
	if err != nil {
		return state, progress, err
	}
	if routineStopRequested(ctx, stop) {
		return state, progress, errAgentRoutineStopped
	}
	if err := r.validateRoutineRun(ctx, prepared.task, prepared.run, actorID); err != nil {
		return state, progress, fmt.Errorf("validate diary BOARD write Run: %w", err)
	}
	write := boardcommunication.DiaryWrite{
		ActorID: actorID, Date: diary.Date, Revision: diary.Revision, Body: diary.Body, Reflection: diary.Reflection,
	}
	windowCtx, cancelWindow := r.withRoutineWindow(ctx, state.Pending, state)
	writeCtx, cancel := context.WithTimeout(windowCtx, agentRoutineWebTimeout)
	entry, putErr := boardClient.PutDiary(writeCtx, write)
	writeErr := writeCtx.Err()
	cancel()
	cancelWindow()
	if putErr != nil {
		publishErr := errors.New("publish exact routine diary to pinned BOARD failed; saved draft retained for exact retry")
		if errors.Is(writeErr, context.DeadlineExceeded) {
			return state, progress, errors.Join(publishErr, context.DeadlineExceeded)
		}
		return state, progress, publishErr
	}
	if entry.ActorID != write.ActorID || entry.Date != write.Date || entry.Revision != write.Revision ||
		entry.Body != write.Body || entry.Reflection != write.Reflection || entry.UpdatedAt.IsZero() {
		return state, progress, errors.New("pinned BOARD diary response does not match the exact submitted revision and content")
	}
	if strings.TrimSpace(entry.CorrelationID) == "" {
		return state, progress, errors.New("pinned BOARD diary response did not include its verified correlation ID")
	}
	acknowledged, err := routinePersist(ctx, func(persistCtx context.Context) (agentroutine.Diary, error) {
		return r.store.SaveDiary(persistCtx, agentroutine.Diary{
			ActorID: write.ActorID, Date: write.Date, Revision: write.Revision, Body: write.Body, Reflection: write.Reflection,
			PostingKey: diary.PostingKey, Receipt: entry.CorrelationID, ReceiptRevision: fmt.Sprintf("%d", entry.Revision), UpdatedAt: entry.UpdatedAt.UTC(),
		})
	})
	if err != nil {
		return state, progress, fmt.Errorf("persist validated BOARD diary receipt: %w", err)
	}
	progress.DiaryReceipt, progress.DiaryReceiptRevision = acknowledged.Receipt, acknowledged.ReceiptRevision
	progress.EffectCompleted = true
	return r.saveProgress(ctx, state, "diary_receipt_saved", progress)
}

func (r *agentRoutineRuntime) buildRoutineDiaryBody(ctx context.Context, state agentroutine.State) (string, error) {
	windowCtx, cancelWindow := r.withRoutineWindow(ctx, state.Pending, state)
	body, err := r.buildDiaryBody(windowCtx, state.Pending.ActorID, state.Pending.Date)
	windowErr := windowCtx.Err()
	cancelWindow()
	if err != nil && errors.Is(windowErr, context.DeadlineExceeded) {
		return "", errors.Join(err, context.DeadlineExceeded)
	}
	return body, err
}

func (r *agentRoutineRuntime) buildDiaryBody(ctx context.Context, actorID, date string) (string, error) {
	routineFacts, err := r.store.ListFacts(ctx, actorID, date)
	if err != nil {
		return "", err
	}
	workFacts, err := collectRoutineWorkFacts(ctx, r.tasks, r.journal, actorID, r.ownerID, date)
	if err != nil {
		return "", err
	}
	gameFacts, err := r.collectRoutineGameFacts(ctx, actorID, date)
	if err != nil {
		return "", err
	}
	worldFacts, err := r.collectRoutineWorldFacts(ctx, actorID, date)
	if err != nil {
		return "", err
	}
	facts := append(routineFacts, workFacts...)
	facts = append(facts, gameFacts...)
	facts = append(facts, worldFacts...)
	confirmed := make([]agentroutine.Activity, 0, len(facts))
	for _, fact := range facts {
		if fact.ActorID == actorID && fact.Date == date && fact.Confirmed && fact.Kind != agentroutine.ActivityDiary && strings.TrimSpace(fact.Summary) != "" {
			confirmed = append(confirmed, fact)
		}
	}
	sort.SliceStable(confirmed, func(i, j int) bool {
		if confirmed[i].At.IsZero() != confirmed[j].At.IsZero() {
			return !confirmed[i].At.IsZero()
		}
		if !confirmed[i].At.Equal(confirmed[j].At) {
			return confirmed[i].At.Before(confirmed[j].At)
		}
		return confirmed[i].Key < confirmed[j].Key
	})
	var builder strings.Builder
	fmt.Fprintf(&builder, "%s の記録\n\n", date)
	builder.WriteString("記録の範囲: COREの確定記録と、認証済みWORLDイベント・canonical GAMES sessionで確認できた事実のみです。\n")
	if len(confirmed) == 0 {
		builder.WriteString("この日に確認できたCORE、WORLD、GAMESの記録はありません。\n")
		return builder.String(), nil
	}
	builder.WriteString("\n")
	for _, fact := range confirmed {
		summary := routineDiaryActivitySummary(fact)
		line := ""
		if !fact.At.IsZero() {
			line = fmt.Sprintf("- %s %s\n", fact.At.In(r.location()).Format("15:04"), summary)
		} else {
			line = fmt.Sprintf("- %s\n", summary)
		}
		if builder.Len()+len(line) > agentRoutineDiaryBodyMax {
			return "", fmt.Errorf("confirmed activity facts exceed the %d-byte BOARD diary body limit; refusing incomplete diary", agentRoutineDiaryBodyMax)
		}
		builder.WriteString(line)
	}
	return builder.String(), nil
}

func routineDiaryActivitySummary(fact agentroutine.Activity) string {
	summary := truncateRoutineRunes(strings.TrimSpace(fact.Summary), 1200)
	if fact.Source != "routine_media_playback" {
		return summary
	}
	receipt, valid := validatedRoutineMediaFactReference(fact)
	if !valid {
		return summary + "（再生範囲は確認できません）"
	}
	return fmt.Sprintf("%s（再生範囲 %s〜%s）", summary, routineMediaOffsetLabel(receipt.StartMS), routineMediaOffsetLabel(receipt.EndMS))
}

func routineMediaOffsetLabel(milliseconds int64) string {
	return fmt.Sprintf("%02d:%02d:%02d.%03d", milliseconds/3_600_000, (milliseconds/60_000)%60,
		(milliseconds/1000)%60, milliseconds%1000)
}

func routineDiaryReflectionPrompt(body string) string {
	const prefix = "日記本文の記録だけを根拠に、短く中立的な感想を日本語で作成してください。記録にない出来事や行動は加えないでください。本文:\n"
	if len(prefix)+len(body) <= agentRoutineReflectionMax {
		return prefix + body
	}
	const marker = "\n（本文が長いため先頭と末尾の一部を示します。中略した事実を推測せず、表示された記録だけを踏まえてください。）\n"
	available := agentRoutineReflectionMax - len(prefix) - len(marker)
	headLimit, tailLimit := available/2, available-available/2
	headEnd := strings.LastIndexByte(body[:min(headLimit, len(body))], '\n')
	if headEnd >= 0 {
		headEnd++
	}
	tailStart := len(body) - tailLimit
	if tailStart < 0 {
		tailStart = 0
	}
	if nextLine := strings.IndexByte(body[tailStart:], '\n'); nextLine >= 0 {
		tailStart += nextLine + 1
	}
	if headEnd == 0 {
		headEnd = diaryUTF8PrefixEnd(body, headLimit)
	}
	if tailStart >= len(body) {
		tailStart = diaryUTF8SuffixStart(body, tailLimit)
	}
	return prefix + body[:headEnd] + marker + body[tailStart:]
}

func diaryUTF8PrefixEnd(value string, limit int) int {
	if limit >= len(value) {
		return len(value)
	}
	for limit > 0 && value[limit]&0xc0 == 0x80 {
		limit--
	}
	return limit
}

func diaryUTF8SuffixStart(value string, limit int) int {
	start := len(value) - limit
	if start < 0 {
		return 0
	}
	for start < len(value) && value[start]&0xc0 == 0x80 {
		start++
	}
	return start
}

func (r *agentRoutineRuntime) recordRoutineFacts(ctx context.Context, intent agentroutine.Intent, progress routineProgress, completedAt time.Time) error {
	if intent.Kind == agentroutine.ActivityHobby &&
		(progress.Media != nil || (r.worldLife != nil && routineWorldHomeHobby(intent.ActorID, intent.Label))) {
		return nil // Media receipts and committed WORLD lifecycle events own these activity facts.
	}
	var kind agentroutine.ActivityKind
	summary := ""
	switch intent.Kind {
	case agentroutine.ActivityMorningChat:
		kind = agentroutine.ActivityMorningChat
		summary = "朝に短いテキスト会話をしました。"
	case agentroutine.ActivityHobby:
		kind = agentroutine.ActivityHobby
		topic := routineHobbyTopic(intent.ActorID, intent.Label)
		summary = fmt.Sprintf("%sに関する公開情報を読み、個人用の創作メモを作成しました。", topic)
	case agentroutine.ActivityRelax:
		if progress.World == nil || progress.World.RelaxRoomID == "" || progress.World.RelaxObservedAt.IsZero() || r.cfg == nil {
			return nil
		}
		roomAllowed := r.cfg.Heartbeat.Routine.PrivateRooms[intent.ActorID] == progress.World.RelaxRoomID
		for _, sharedRoom := range r.cfg.Heartbeat.Routine.SharedRooms {
			roomAllowed = roomAllowed || sharedRoom == progress.World.RelaxRoomID
		}
		if !roomAllowed {
			return nil
		}
		kind = agentroutine.ActivityRelax
		completedAt = progress.World.RelaxObservedAt
		summary = fmt.Sprintf("%sで静かに過ごしました。", routineWorldRoomDiaryLabel(progress.World.RelaxRoomID))
	case agentroutine.ActivityDiary:
		kind = agentroutine.ActivityDiary
		summary = "日記の改訂版をBOARDへ投稿しました。"
	default:
		return nil // Logical sleep/wake gates and unexecuted relaxation are not physical activity facts.
	}
	date := completedAt.In(r.location()).Format("2006-01-02")
	if date != intent.Date {
		// The activity timestamp determines its JST day, even if the step crossed midnight.
		intent.Date = date
	}
	if intent.Kind == agentroutine.ActivityMorningChat {
		transcript := make([]map[string]string, 0, len(progress.Actors))
		for _, actor := range progress.Actors {
			transcript = append(transcript, map[string]string{"actor_id": actor.ActorID, "text": actor.Output})
		}
		privateTranscript, err := json.Marshal(transcript)
		if err != nil {
			return err
		}
		for _, participant := range intent.Participants {
			fact := agentroutine.Activity{
				ActorID: participant, Date: date, Key: intent.ID + ":conversation", Kind: kind,
				At: completedAt.UTC(), Source: agentRoutineModuleID, SourceID: intent.ID, Confirmed: true, Summary: summary, Reference: string(privateTranscript),
			}
			if _, err := routinePersist(ctx, func(persistCtx context.Context) (agentroutine.Activity, error) {
				return r.store.SaveFact(persistCtx, fact)
			}); err != nil {
				return err
			}
		}
	}
	fact := agentroutine.Activity{
		ActorID: intent.ActorID, Date: date, Key: intent.ID + ":" + string(kind), Kind: kind,
		At: completedAt.UTC(), Source: agentRoutineModuleID, SourceID: intent.ID, Confirmed: true, Summary: summary,
	}
	if intent.Kind == agentroutine.ActivityMorningChat {
		transcript := make([]map[string]string, 0, len(progress.Actors))
		for _, actor := range progress.Actors {
			transcript = append(transcript, map[string]string{"actor_id": actor.ActorID, "text": actor.Output})
		}
		privateTranscript, err := json.Marshal(transcript)
		if err != nil {
			return err
		}
		fact.Reference = string(privateTranscript)
	}
	if intent.Kind == agentroutine.ActivityHobby {
		artifact := struct {
			Label   string `json:"label"`
			Sources string `json:"sources"`
			Note    string `json:"note"`
		}{Label: intent.Label, Sources: progress.SearchResult, Note: progress.HobbyText}
		privateArtifact, err := json.Marshal(artifact)
		if err != nil {
			return err
		}
		fact.Reference = string(privateArtifact)
	}
	_, err := routinePersist(ctx, func(persistCtx context.Context) (agentroutine.Activity, error) {
		return r.store.SaveFact(persistCtx, fact)
	})
	return err
}

func routineHobbyTopic(actorID, label string) string {
	topics := map[string]map[string]string{
		"mio":    {"fashion": "ファッション", "video_creativity": "ショート動画の表現"},
		"shiro":  {"film_research": "映画の表現", "fitness_research": "運動科学"},
		"kuro":   {"otaku_culture": "オタクカルチャー", "creative_mirror_ideas": "鏡を題材にした創作"},
		"midori": {"reading": "読書と新刊情報", "manga_culture": "漫画文化", "music_culture": "音楽文化", "bl_story_ideas": "BL物語の創作"},
	}
	if topic := topics[actorID][label]; topic != "" {
		return topic
	}
	return "趣味のアイデア"
}

func (r *agentRoutineRuntime) advanceDiaryDate(ctx context.Context, actorID, diaryDate string) (agentroutine.State, error) {
	state, err := r.store.Load(ctx, actorID)
	if err != nil {
		return agentroutine.State{}, err
	}
	day, err := time.ParseInLocation("2006-01-02", diaryDate, r.location())
	if err != nil {
		return agentroutine.State{}, err
	}
	nextDate := day.AddDate(0, 0, 1).Format("2006-01-02")
	if state.NextDiaryDate == "" || state.NextDiaryDate < nextDate {
		state.NextDiaryDate = nextDate
	}
	return routinePersist(ctx, func(persistCtx context.Context) (agentroutine.State, error) {
		return r.store.Save(persistCtx, state, state.Revision)
	})
}

func (r *agentRoutineRuntime) completeRuns(ctx context.Context, prepared []routinePreparedRun, summary string) error {
	for _, item := range prepared {
		if item.run.Status == domaintask.RunStatusSucceeded {
			continue
		}
		if _, err := r.tasks.CompleteRun(ctx, item.task.TaskID, item.run.RunID, item.task.Assignee, domaintask.StatusSucceeded, summary, ""); err != nil {
			return fmt.Errorf("complete canonical routine Task/Run: %w", err)
		}
	}
	return nil
}

func (r *agentRoutineRuntime) pauseRuns(ctx context.Context, prepared []routinePreparedRun, reason string) error {
	var firstErr error
	for _, item := range prepared {
		if item.run.Status != domaintask.RunStatusRunning {
			continue
		}
		if err := routinePersistErr(ctx, func(persistCtx context.Context) error {
			_, completeErr := r.tasks.CompleteRun(persistCtx, item.task.TaskID, item.run.RunID, item.task.Assignee,
				domaintask.StatusWaiting, "CORE agent routine paused", reason)
			return completeErr
		}); err != nil && firstErr == nil {
			firstErr = errors.Join(taskmanager.ErrStartedRunNotClosed, fmt.Errorf("pause exact routine Run %s for Task %s: %w", item.run.RunID, item.task.TaskID, err))
		}
	}
	return firstErr
}

func (r *agentRoutineRuntime) deferIntent(ctx context.Context, state agentroutine.State, progress routineProgress, reason string) error {
	if state.Pending == nil {
		return errors.New(reason)
	}
	prepared := make([]routinePreparedRun, 0, len(progress.Actors))
	for index, actor := range progress.Actors {
		task, err := routinePersist(ctx, func(persistCtx context.Context) (domaintask.Task, error) {
			return r.tasks.Get(persistCtx, actor.TaskID)
		})
		if err != nil {
			continue
		}
		runs, err := routinePersist(ctx, func(persistCtx context.Context) ([]domaintask.Run, error) {
			return r.tasks.ListRuns(persistCtx, domaintask.RunFilter{TaskID: actor.TaskID})
		})
		if err != nil || len(runs) == 0 {
			continue
		}
		latest := runs[len(runs)-1]
		prepared = append(prepared, routinePreparedRun{progressIndex: index, task: task, run: latest})
	}
	pauseErr := r.pauseRuns(ctx, prepared, reason)
	state, err := routinePersist(ctx, func(persistCtx context.Context) (agentroutine.State, error) {
		return r.store.Load(persistCtx, state.ActorID)
	})
	if err != nil || state.Pending == nil {
		return errors.Join(errors.New(reason), err, pauseErr)
	}
	state.Pending.RetryAfter = r.now().Add(agentRoutineRetryDelay)
	_, saveErr := routinePersist(ctx, func(persistCtx context.Context) (agentroutine.State, error) {
		return r.store.Save(persistCtx, state, state.Revision)
	})
	if saveErr != nil {
		return errors.Join(errors.New(reason), saveErr, pauseErr)
	}
	deferredErr := fmt.Errorf("routine intent deferred until its retry window: %s", reason)
	return errors.Join(deferredErr, pauseErr)
}

func (r *agentRoutineRuntime) cancelStaleIntent(ctx context.Context, state agentroutine.State) error {
	if state.Pending == nil {
		return nil
	}
	progress, err := decodeRoutineProgress(state.Pending.Output)
	if err != nil {
		return err
	}
	for _, actor := range progress.Actors {
		task, getErr := routinePersist(ctx, func(persistCtx context.Context) (domaintask.Task, error) {
			return r.tasks.Get(persistCtx, actor.TaskID)
		})
		if errors.Is(getErr, domaintask.ErrNotFound) {
			continue
		}
		if getErr != nil {
			return fmt.Errorf("read stale routine Task %s: %w", actor.TaskID, getErr)
		}
		runs, listErr := routinePersist(ctx, func(persistCtx context.Context) ([]domaintask.Run, error) {
			return r.tasks.ListRuns(persistCtx, domaintask.RunFilter{TaskID: actor.TaskID})
		})
		if listErr != nil {
			return fmt.Errorf("list stale routine Runs for Task %s: %w", actor.TaskID, listErr)
		}
		if task.TaskID != actor.TaskID || task.OwnerID != r.ownerID || task.ModuleID != agentRoutineModuleID ||
			task.Assignee != actor.ActorID || task.Route != domaintask.RouteCHAT {
			return fmt.Errorf("stale routine Task %s does not match its exact owner/module/assignee/route", actor.TaskID)
		}
		if task.Status == domaintask.StatusSucceeded || domaintask.IsTerminal(task.Status) {
			continue // Preserve all terminal Task outcomes, especially successes.
		}
		if len(runs) == 0 {
			if err := routinePersistErr(ctx, func(persistCtx context.Context) error {
				_, cancelErr := r.tasks.Cancel(persistCtx, task.TaskID, "Routine activity window expired")
				return cancelErr
			}); err != nil {
				return fmt.Errorf("cancel stale routine Task without a Run: %w", err)
			}
			continue
		}
		latest := runs[len(runs)-1]
		if latest.TaskID != task.TaskID || latest.Assignee != actor.ActorID || latest.Validate() != nil {
			return fmt.Errorf("latest stale routine Run for Task %s has mismatched ownership or invalid data", actor.TaskID)
		}
		if latest.Status == domaintask.RunStatusSucceeded || latest.Status == domaintask.RunStatusFailed || latest.Status == domaintask.RunStatusCancelled {
			continue // Do not rewrite a terminal Run.
		}
		if latest.Status == domaintask.RunStatusRunning {
			if task.Status != domaintask.StatusRunning {
				return fmt.Errorf("stale routine Run %s is running while Task status is %s", latest.RunID, task.Status)
			}
			_, recovered, recoverErr := r.tasks.RecoverRunAfterProcessRestart(ctx, task.TaskID, latest.RunID, actor.ActorID,
				domaintask.StatusCancelled, "Routine activity window expired", "fresh WORLD/window confirmation is no longer available")
			if recoverErr != nil {
				return fmt.Errorf("recover expired routine Run after process restart: %w", recoverErr)
			}
			if recovered {
				continue
			}
			if err := routinePersistErr(ctx, func(persistCtx context.Context) error {
				_, completeErr := r.tasks.CompleteRun(persistCtx, task.TaskID, latest.RunID,
					actor.ActorID, domaintask.StatusCancelled, "Routine activity window expired", "fresh WORLD/window confirmation is no longer available")
				return completeErr
			}); err != nil {
				return fmt.Errorf("cancel expired routine Run: %w", err)
			}
			continue
		}
		if err := routinePersistErr(ctx, func(persistCtx context.Context) error {
			_, cancelErr := r.tasks.Cancel(persistCtx, task.TaskID, "Routine activity window expired")
			return cancelErr
		}); err != nil {
			return fmt.Errorf("cancel stale waiting routine Task: %w", err)
		}
	}
	state, err = routinePersist(ctx, func(persistCtx context.Context) (agentroutine.State, error) {
		return r.store.Load(persistCtx, state.ActorID)
	})
	if err != nil {
		return err
	}
	if state.Pending == nil {
		return nil // Another exact completion already closed this actor's intent.
	}
	actorID, intentID, revision := state.ActorID, state.Pending.ID, state.Revision
	_, err = routinePersist(ctx, func(persistCtx context.Context) (agentroutine.State, error) {
		return r.store.FinishIntent(persistCtx, actorID, intentID, revision, r.now().Add(30*time.Minute))
	})
	return err
}

func (r *agentRoutineRuntime) boardClient(actorID string) *boardcommunication.Client {
	if r == nil || r.cfg == nil || r.deps == nil || r.deps.boardCommunicationActorBoots == nil {
		return nil
	}
	boot := r.deps.boardCommunicationActorBoots[actorID]
	if boot == nil {
		return nil
	}
	composition := boot.currentComposition()
	if composition == nil || composition.boardClient == nil {
		return nil
	}
	return composition.boardClient
}

func routineCooldown(kind agentroutine.ActivityKind) time.Duration {
	switch kind {
	case agentroutine.ActivitySleep, agentroutine.ActivityWake, agentroutine.ActivityDiary:
		return 0
	case agentroutine.ActivityMorningChat:
		return 15 * time.Minute
	case agentroutine.ActivityHobby:
		return 30 * time.Minute
	case agentroutine.ActivityRelax:
		return 45 * time.Minute
	default:
		return 45 * time.Minute
	}
}

func (r *agentRoutineRuntime) diaryAmendmentWindow(now time.Time) bool {
	hour := now.In(r.location()).Hour()
	return hour >= 9 && hour < 22
}

func (r *agentRoutineRuntime) checkDiaryAmendment(ctx context.Context, actorID, date string) (bool, error) {
	key := actorID
	now := r.now()
	r.amendmentMu.Lock()
	if r.lastAmendmentCheck == nil {
		r.lastAmendmentCheck = make(map[string]time.Time)
	}
	if last := r.lastAmendmentCheck[key]; !last.IsZero() && now.Sub(last) >= 0 && now.Sub(last) < 30*time.Minute {
		r.amendmentMu.Unlock()
		return false, nil
	}
	r.lastAmendmentCheck[key] = now
	r.amendmentMu.Unlock()
	changed, err := r.readDiaryAmendment(ctx, actorID, date)
	if err != nil {
		r.amendmentMu.Lock()
		r.lastAmendmentCheck[key] = now.Add(-29 * time.Minute)
		r.amendmentMu.Unlock()
	}
	return changed, err
}

func (r *agentRoutineRuntime) readDiaryAmendment(ctx context.Context, actorID, date string) (bool, error) {
	if r.boardClient(actorID) == nil || !r.diaryAmendmentWindow(r.now()) ||
		date != r.now().In(r.location()).AddDate(0, 0, -1).Format("2006-01-02") {
		return false, nil
	}
	readCtx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
	defer cancel()
	diary, err := r.store.GetDiary(readCtx, actorID, date)
	if errors.Is(err, agentroutine.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if diary.Receipt == "" {
		return false, nil
	}
	latestBody, err := r.buildDiaryBody(readCtx, actorID, date)
	if err != nil {
		return false, err
	}
	return latestBody != diary.Body, nil
}

func (r *agentRoutineRuntime) withRoutineWindow(ctx context.Context, intent *agentroutine.Intent, state agentroutine.State) (context.Context, context.CancelFunc) {
	now := r.now().In(r.location())
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, r.location())
	var deadline time.Time
	if intent != nil && (intent.Kind == agentroutine.ActivityDiary || now.Hour() < 2) {
		sleepDate := day
		if now.Hour() >= 22 {
			sleepDate = day.AddDate(0, 0, 1)
		}
		sleepAt := state.PlannedSleep.In(r.location())
		if sleepAt.IsZero() || sleepAt.Format("2006-01-02") != sleepDate.Format("2006-01-02") || sleepAt.Hour() >= 2 {
			if scheduled, _, err := agentroutine.ScheduleFor(intent.ActorID, sleepDate.Format("2006-01-02")); err == nil {
				sleepAt = scheduled.In(r.location())
			}
		}
		if !sleepAt.IsZero() && sleepAt.Hour() < 2 {
			if sleepAt.After(now) {
				deadline = sleepAt
			} else if now.Hour() < 2 {
				deadline = now
			}
		}
	}
	if intent != nil {
		switch {
		case !deadline.IsZero():
			// The planned sleep entry is the earlier boundary in the 00:00-02:00 window.
		case intent.Kind == agentroutine.ActivityMorningChat:
			deadline = day.Add(9 * time.Hour)
		case intent.Kind == agentroutine.ActivityHobby:
			hour := now.Hour()
			if hour >= 22 {
				sleepDate := day.AddDate(0, 0, 1).Format("2006-01-02")
				if sleepAt, _, err := agentroutine.ScheduleFor(intent.ActorID, sleepDate); err == nil {
					deadline = sleepAt
				}
			} else if hour < 6 {
				if !state.PlannedWake.IsZero() && !state.PlannedWake.After(now) {
					deadline = day.Add(9 * time.Hour)
				} else {
					deadline = day.Add(6 * time.Hour)
				}
			} else if hour < 9 {
				deadline = day.Add(9 * time.Hour)
			}
		}
	}
	if deadline.IsZero() {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, time.Now().Add(deadline.Sub(now)))
}
