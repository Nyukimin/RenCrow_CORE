package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	worldmeeting "github.com/Nyukimin/RenCrow_CORE/internal/adapter/taskmeeting"
	meetingapp "github.com/Nyukimin/RenCrow_CORE/internal/application/taskmeeting"
	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	workstreampersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const taskMeetingDrainLimit = 48

type runtimeTaskMeetingOccurrenceExecutor struct {
	cfg                      *config.Config
	deps                     *Dependencies
	agents                   map[string]domainagent.TaskMeetingActor
	actors                   agentRuntime
	coders                   []*coderAdapter
	clock                    func() time.Time
	worldSnapshotReader      worldmeeting.TaskMeetingWorldSnapshotReader
	worldGatherClientFactory func() (worldmeeting.TaskMeetingWorldGatherClient, error)
}

func newRuntimeTaskMeetingOccurrenceExecutor(cfg *config.Config, deps *Dependencies, actors agentRuntime, coders ...*coderAdapter) taskMeetingOccurrenceExecutor {
	registered := registeredTaskMeetingActors(cfg, actors, coders...)
	if !runtimeTaskMeetingOwnersReady(deps) || !taskMeetingRuntimeBindingsReady(cfg, actors, coders...) || len(registered) != domaintask.TaskMeetingAudienceSize {
		return nil
	}
	e := &runtimeTaskMeetingOccurrenceExecutor{cfg: cfg, deps: deps, agents: registered, actors: actors, coders: append([]*coderAdapter(nil), coders...), clock: time.Now}
	return e
}

func newRuntimeTaskMeetingOccurrenceExecutorWithClock(cfg *config.Config, deps *Dependencies, actors agentRuntime, clock func() time.Time, coders ...*coderAdapter) taskMeetingOccurrenceExecutor {
	created := newRuntimeTaskMeetingOccurrenceExecutor(cfg, deps, actors, coders...)
	executor, ok := created.(*runtimeTaskMeetingOccurrenceExecutor)
	if !ok || executor == nil {
		return nil
	}
	if clock != nil {
		executor.clock = clock
	}
	return executor
}

func registeredTaskMeetingActors(cfg *config.Config, actors agentRuntime, coders ...*coderAdapter) map[string]domainagent.TaskMeetingActor {
	registered := map[string]domainagent.TaskMeetingActor{}
	if actors.Mio != nil {
		registered["mio"] = actors.Mio.WithTaskMeetingActorID("mio")
	}
	if actors.Shiro != nil {
		registered["shiro"] = actors.Shiro.WithTaskMeetingActorID("shiro")
	}
	if actors.Heavy != nil {
		registered["kuro"] = actors.Heavy.WithTaskMeetingActorID("kuro")
	}
	if actors.Wild != nil {
		registered["midori"] = actors.Wild.WithTaskMeetingActorID("midori")
	}
	if cfg == nil {
		return registered
	}
	configs := []config.CoderConfig{cfg.Coder1, cfg.Coder2, cfg.Coder3, cfg.Coder4}
	for index, coder := range coders {
		if coder == nil || coder.domainCoder == nil || index >= len(configs) || !configs[index].Enabled {
			continue
		}
		actorID := configs[index].Name
		if actorID == "aka" || actorID == "ao" || actorID == "kin" || actorID == "gin" {
			registered[actorID] = coder.domainCoder.WithTaskMeetingActorID(actorID)
		}
	}
	return registered
}

func runtimeTaskMeetingOwnersReady(deps *Dependencies) bool {
	if deps == nil || deps.taskManager == nil || deps.canonicalEventStore == nil || deps.taskStore == nil || deps.workstreamStore == nil {
		return false
	}
	store, ok := deps.canonicalEventStore.(*eventstore.SQLiteStore)
	if !ok || store == nil {
		return false
	}
	if nativeTasks, ok := deps.taskStore.(*taskpersistence.JSONLStore); !ok || nativeTasks == nil {
		return false
	}
	if nativeWorkstream, ok := deps.workstreamStore.(*workstreampersistence.SQLiteStore); !ok || nativeWorkstream == nil {
		return false
	}
	_, invocationReader := deps.taskStore.(domaintask.TaskMeetingInvocationReader)
	_, outputStore := deps.taskStore.(domaintask.TaskMeetingGenerationOutputStore)
	_, unknownStore := deps.taskStore.(domaintask.TaskMeetingGenerationUnknownStore)
	_, executionFencer := deps.taskStore.(domaintask.TaskMeetingExecutionFencer)
	_, messageConsumer := deps.taskStore.(domaintask.TaskMeetingMessageConsumer)
	_, messageReader := deps.taskStore.(domaintask.TaskMeetingMessageConsumptionReader)
	_, operationStore := deps.taskStore.(domaintask.TaskMeetingInvocationOperationStore)
	_, unknownFenceRecovery := deps.taskStore.(domaintask.TaskMeetingUnknownFenceRecovery)
	if !invocationReader || !outputStore || !unknownStore || !executionFencer || !messageConsumer || !messageReader || !operationStore || !unknownFenceRecovery {
		return false
	}
	_, ok = deps.workstreamStore.(interface {
		LoadOrCreateMeetingOccurrence(context.Context, domainmeeting.StoreAccess, domainmeeting.OccurrenceKey, domainmeeting.Meeting) (domainmeeting.OccurrenceRecord, error)
		SaveMeetingOccurrence(context.Context, domainmeeting.StoreAccess, domainmeeting.OccurrenceKey, domainmeeting.Guard, string, domainmeeting.Meeting) (domainmeeting.OccurrenceRecord, error)
		PersistTaskMeetingMinutes(context.Context, workstreampersistence.TaskMeetingMinutesRequest, workstreampersistence.TaskMeetingMinutesProofCheck) (workstreampersistence.TaskMeetingMinutesReference, error)
		FindTaskMeetingMinutes(context.Context, string, domainmeeting.OccurrenceKey, string, domainmeeting.Guard, workstreampersistence.TaskMeetingMinutesFindAuthorizationCheck) (workstreampersistence.TaskMeetingMinutesReference, bool, error)
		FindMeetingOccurrence(context.Context, domainmeeting.StoreAccess, domainmeeting.OccurrenceKey) (domainmeeting.Meeting, bool, error)
	})
	return ok
}

func (e *runtimeTaskMeetingOccurrenceExecutor) ExecuteScheduledOccurrence(ctx context.Context, schedule domainscheduler.Schedule, occurrence domainscheduler.ScheduledOccurrence) (string, error) {
	if e == nil || e.cfg == nil || e.deps == nil || e.deps.taskManager == nil || ctx == nil {
		return "", meetingapp.ErrUnavailable
	}
	if err := validateTaskMeetingScheduledOccurrence(schedule, occurrence); err != nil {
		return "", err
	}
	snapshot, err := resolveRuntimeTaskMeetingOccurrence(ctx, e.cfg, e.deps, schedule, occurrence, e.actors, e.coders...)
	if err != nil {
		// The resolver must receive the exact registered objects used when the
		// executor was built. The method below supplies that immutable snapshot.
		return "", err
	}
	return e.executePinnedOccurrence(ctx, schedule, occurrence, snapshot)
}

func (e *runtimeTaskMeetingOccurrenceExecutor) executePinnedOccurrence(ctx context.Context, schedule domainscheduler.Schedule, occurrence domainscheduler.ScheduledOccurrence, snapshot runtimeTaskMeetingOccurrenceSnapshot) (string, error) {
	if e.deps == nil || e.deps.taskManager == nil {
		return "", meetingapp.ErrUnavailable
	}
	declarationBytes := append(json.RawMessage(nil), snapshot.Registration.FrozenDeclaration...)
	nativeTiming, err := taskMeetingOccurrenceTaskTiming(snapshot.Declaration.Timing, occurrence.ScheduledAt)
	if err != nil {
		return "", err
	}
	rootInput, err := json.Marshal(struct {
		Schema        string                `json:"schema"`
		OccurrenceKey string                `json:"occurrence_key"`
		DueAt         time.Time             `json:"due_at"`
		ScheduleID    modulecore.ScheduleID `json:"schedule_id"`
	}{"task_meeting.root.v1", snapshot.Key.String(), occurrence.ScheduledAt.UTC(), schedule.ScheduleID})
	if err != nil {
		return "", err
	}
	request := domaintask.TaskMeetingInvocationRequest{
		OccurrenceKey: snapshot.Key.String(), Purpose: domaintask.TaskMeetingPurposeRoot,
		CoordinatorActorID: snapshot.Declaration.CoordinatorActorID, ActorID: snapshot.Declaration.CoordinatorActorID,
		ConfiguredOwnerUserID: snapshot.Declaration.OwnerUserID, ConfigRevision: snapshot.Declaration.Revision,
		RegistrationEvidenceID: snapshot.Registration.RegistrationEvidenceID, FrozenConfig: declarationBytes,
		Audience: append([]string(nil), snapshot.Declaration.Audience...), FrozenInput: rootInput, Timing: nativeTiming,
	}
	// Root recovery uses its stable occurrence receipt before any session or
	// World side effect. Admission callbacks revalidate the live fixed owner;
	// state reads validate the immutable TaskManager descriptor.
	authorizeReceipt := func(authCtx context.Context) error {
		if err := authCtx.Err(); err != nil {
			return err
		}
		if e.deps.taskManager == nil || e.deps.taskCenterUserID != snapshot.Declaration.OwnerUserID {
			return eventstore.ErrTaskMeetingForbidden
		}
		return nil
	}
	authorizeState := e.authorizeTaskState(snapshot)
	root, run, proof, err := e.deps.taskManager.EnsureTaskMeetingRootInvocation(ctx, request, e.authorizeAdmission(snapshot), authorizeReceipt, authorizeState)
	if err != nil {
		return "", err
	}
	if root.TaskID != proof.TaskID || run.RunID != proof.RunID || proof.RegistrationEvidenceID != snapshot.Registration.RegistrationEvidenceID {
		return "", domaintask.ErrTaskMeetingConflict
	}
	if err := e.ensureTaskMeetingRecipientContexts(ctx, snapshot, root, run, proof, nativeTiming, authorizeReceipt); err != nil {
		return "", err
	}
	configPort := runtimeTaskMeetingConfiguration{snapshot: snapshot, scheduledAt: occurrence.ScheduledAt}
	authority := runtimeTaskMeetingAuthority{snapshot: snapshot}
	store, ok := e.deps.workstreamStore.(interface {
		LoadOrCreateMeetingOccurrence(context.Context, domainmeeting.StoreAccess, domainmeeting.OccurrenceKey, domainmeeting.Meeting) (domainmeeting.OccurrenceRecord, error)
		SaveMeetingOccurrence(context.Context, domainmeeting.StoreAccess, domainmeeting.OccurrenceKey, domainmeeting.Guard, string, domainmeeting.Meeting) (domainmeeting.OccurrenceRecord, error)
	})
	if !ok || store == nil {
		return "", meetingapp.ErrUnavailable
	}
	ctx, err = e.withOwnerScope(ctx, snapshot.Declaration.CoordinatorActorID, snapshot.Key.String())
	if err != nil {
		return "", err
	}
	participants := &runtimeTaskMeetingParticipants{executor: e, snapshot: snapshot, root: root, rootRun: run, rootProof: proof, authorizeReceipt: authorizeReceipt, authorizeState: authorizeState}
	conversation := &runtimeTaskMeetingConversation{executor: e, snapshot: snapshot, root: root, rootRun: run, rootProof: proof, authorizeReceipt: authorizeReceipt, authorizeState: authorizeState}
	world := e.worldPresence(snapshot, root, run, authorizeState, false)
	coordinator := meetingapp.NewCoordinator(configPort, authority, store, participants, world, conversation, e.clock)
	input := meetingapp.ScheduledInput{Schedule: schedule, Occurrence: occurrence, RootExecution: &meetingapp.NativeExecutionProof{
		TaskID: root.TaskID, RunID: run.RunID, Assignee: root.Assignee,
		AdmissionEvidenceID: proof.AdmissionEvidenceID,
	}}
	for checkpoint := 0; checkpoint < taskMeetingDrainLimit; checkpoint++ {
		opened, advanceErr := coordinator.Advance(ctx, input)
		if advanceErr != nil {
			return "", fmt.Errorf("TaskMeeting checkpoint %d: %w", checkpoint+1, advanceErr)
		}
		switch opened.Meeting.Status {
		case domainmeeting.StatusCompleted:
			if err := e.closeTaskMeetingControls(ctx, snapshot, opened.Meeting, proof, authorizeReceipt, authorizeState); err != nil {
				return "", err
			}
			return taskMeetingSchedulerSafeSummary, nil
		case domainmeeting.StatusPartial, domainmeeting.StatusMissed:
			// The coordinator first commits the terminal classification, then
			// persists and attaches the canonical partial/minutes artifact on a
			// later checkpoint. Keep draining until that proof is attached before
			// closing native controls or returning scheduler success.
			if opened.Meeting.PartialMinutes == nil {
				continue
			}
			if err := e.closeTaskMeetingControls(ctx, snapshot, opened.Meeting, proof, authorizeReceipt, authorizeState); err != nil {
				return "", err
			}
			return taskMeetingSchedulerSafeSummary, nil
		}
	}
	return "", errTaskMeetingCheckpointPending
}

func (e *runtimeTaskMeetingOccurrenceExecutor) ensureTaskMeetingRecipientContexts(ctx context.Context, snapshot runtimeTaskMeetingOccurrenceSnapshot, root domaintask.Task, rootRun domaintask.Run, proof domaintask.TaskMeetingRootProof, timing modulecore.TaskTiming, authorizeReceipt domaintask.TaskMeetingInvocationOperationAuthorizer) error {
	frozen, err := json.Marshal(struct {
		Schema     string `json:"schema"`
		Occurrence string `json:"occurrence_key"`
	}{"task_meeting.recipient_context.v1", snapshot.Key.String()})
	if err != nil {
		return err
	}
	for _, actorID := range snapshot.Declaration.Audience {
		if e.agents[actorID] == nil {
			return meetingapp.ErrUnavailable
		}
		request := domaintask.TaskMeetingInvocationRequest{
			OccurrenceKey: snapshot.Key.String(), Purpose: domaintask.TaskMeetingPurposeRecipientContext,
			CoordinatorActorID: snapshot.Declaration.CoordinatorActorID, ActorID: actorID,
			ConfiguredOwnerUserID: snapshot.Declaration.OwnerUserID, ConfigRevision: snapshot.Declaration.Revision,
			RegistrationEvidenceID: snapshot.Registration.RegistrationEvidenceID,
			FrozenConfig:           append(json.RawMessage(nil), snapshot.Registration.FrozenDeclaration...),
			Audience:               append([]string(nil), snapshot.Declaration.Audience...), FrozenInput: append(json.RawMessage(nil), frozen...),
			Timing: timing, ParentTaskID: root.TaskID, ParentRunID: rootRun.RunID,
		}
		if _, _, _, err := e.deps.taskManager.EnsureTaskMeetingRecipientContext(ctx, request, proof, e.authorizeAdmission(snapshot), e.authorizeTaskState(snapshot), authorizeReceipt, e.authorizeTaskState(snapshot)); err != nil {
			return err
		}
	}
	return nil
}

type runtimeTaskMeetingConfiguration struct {
	snapshot    runtimeTaskMeetingOccurrenceSnapshot
	scheduledAt time.Time
}

func (c runtimeTaskMeetingConfiguration) MeetingConfig(ctx context.Context, scheduleID modulecore.ScheduleID, scheduledAt time.Time) (domainmeeting.ConfigSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return domainmeeting.ConfigSnapshot{}, err
	}
	if scheduleID != c.snapshot.Key.ScheduleID || !scheduledAt.Equal(c.scheduledAt) {
		return domainmeeting.ConfigSnapshot{}, domaintask.ErrTaskMeetingConflict
	}
	return c.snapshot.Config, nil
}

type runtimeTaskMeetingAuthority struct {
	snapshot runtimeTaskMeetingOccurrenceSnapshot
}

func (a runtimeTaskMeetingAuthority) AuthorizeOccurrence(ctx context.Context, key domainmeeting.OccurrenceKey, config domainmeeting.ConfigSnapshot) (domainmeeting.StoreAccess, error) {
	if err := ctx.Err(); err != nil {
		return domainmeeting.StoreAccess{}, err
	}
	if key != a.snapshot.Key || config.ScheduleID != a.snapshot.Key.ScheduleID || config.Revision != a.snapshot.Declaration.Revision ||
		config.RegistrationEvidenceID != a.snapshot.Registration.RegistrationEvidenceID || !sameRuntimeStringSlice(config.ConfiguredActors, a.snapshot.Declaration.Audience) {
		return domainmeeting.StoreAccess{}, domaintask.ErrTaskMeetingConflict
	}
	return domainmeeting.StoreAccess{CoordinatorActorID: a.snapshot.Declaration.CoordinatorActorID, OwnerUserID: a.snapshot.Declaration.OwnerUserID,
		Audience: append([]string(nil), a.snapshot.Declaration.Audience...), AuthorityEvidence: a.snapshot.Registration.RegistrationEvidenceID}, nil
}

func (a runtimeTaskMeetingAuthority) IssueResumeEvidence(context.Context, domainmeeting.Meeting) (domainmeeting.ResumeEvidence, error) {
	return domainmeeting.ResumeEvidence{}, errors.New("TaskMeeting resume requires an explicit owner recovery action")
}

func sameRuntimeStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (e *runtimeTaskMeetingOccurrenceExecutor) authorizeAdmission(snapshot runtimeTaskMeetingOccurrenceSnapshot) domaintask.TaskMeetingInvocationAdmissionCheck {
	return func(ctx context.Context, request domaintask.TaskMeetingInvocationRequest, parent domaintask.Task, run domaintask.Run, _ domaintask.SharedRoleContext) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := e.authorizeCurrentTaskMeetingEffect(snapshot); err != nil {
			return err
		}
		if request.OccurrenceKey != snapshot.Key.String() || request.ConfigRevision != snapshot.Declaration.Revision ||
			request.RegistrationEvidenceID != snapshot.Registration.RegistrationEvidenceID || request.ConfiguredOwnerUserID != snapshot.Declaration.OwnerUserID {
			return eventstore.ErrTaskMeetingForbidden
		}
		if request.Purpose == domaintask.TaskMeetingPurposeReport || request.Purpose == domaintask.TaskMeetingPurposeAnswer {
			if _, ok := e.agents[request.ActorID]; !ok {
				return eventstore.ErrTaskMeetingForbidden
			}
		}
		return nil
	}
}

func (e *runtimeTaskMeetingOccurrenceExecutor) authorizeCurrentTaskMeetingEffect(snapshot runtimeTaskMeetingOccurrenceSnapshot) error {
	if e == nil || e.cfg == nil || e.deps == nil || !e.cfg.LocalAgentOps.Enabled || e.deps.taskCenterUserID != snapshot.Declaration.OwnerUserID ||
		strings.TrimSpace(e.cfg.LocalAgentOps.UserID) != snapshot.Declaration.OwnerUserID || len(e.deps.taskCenterUserToken) == 0 {
		return eventstore.ErrTaskMeetingForbidden
	}
	if enabled, _ := taskMeetingFeatureFlagsEnabled(e.cfg); !enabled {
		return eventstore.ErrTaskMeetingForbidden
	}
	return nil
}

func (e *runtimeTaskMeetingOccurrenceExecutor) authorizeTaskState(snapshot runtimeTaskMeetingOccurrenceSnapshot) domaintask.TaskMeetingReadAuthorizationCheck {
	return func(ctx context.Context, task domaintask.Task, run domaintask.Run, _ domaintask.SharedRoleContext) error {
		if ctx == nil || ctx.Err() != nil || task.TaskMeetingInvocation == nil || task.TaskMeetingInvocation.OccurrenceKey != snapshot.Key.String() ||
			task.TaskMeetingInvocation.ConfigRevision != snapshot.Declaration.Revision || task.TaskMeetingInvocation.RegistrationEvidenceID != snapshot.Registration.RegistrationEvidenceID ||
			task.TaskMeetingInvocation.ConfigSHA256 != snapshot.Registration.ConfigSHA256 || task.TaskMeetingInvocation.ConfiguredOwnerUserID != snapshot.Declaration.OwnerUserID ||
			run.TaskID != task.TaskID || run.RunID.Validate() != nil {
			return eventstore.ErrTaskMeetingForbidden
		}
		return nil
	}
}

func (e *runtimeTaskMeetingOccurrenceExecutor) withOwnerScope(ctx context.Context, actorID, requestID string) (context.Context, error) {
	scope, err := tool.NewToolExecutionScope(requestID, tool.ActorKindAgent, actorID, e.deps.taskCenterUserID,
		[]string{tool.DataScopeUser}, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		return nil, err
	}
	return tool.WithToolExecutionScope(ctx, scope), nil
}

type runtimeTaskMeetingOutput struct {
	Schema   string                        `json:"schema"`
	Message  modulecore.MessageID          `json:"message_id"`
	Evidence domainmeeting.SpeakerEvidence `json:"speaker_evidence"`
	Report   *domainmeeting.Report         `json:"report,omitempty"`
	Answer   *domainmeeting.Answer         `json:"answer,omitempty"`
}

type runtimeTaskMeetingParticipants struct {
	executor         *runtimeTaskMeetingOccurrenceExecutor
	snapshot         runtimeTaskMeetingOccurrenceSnapshot
	root             domaintask.Task
	rootRun          domaintask.Run
	rootProof        domaintask.TaskMeetingRootProof
	authorizeReceipt domaintask.TaskMeetingInvocationOperationAuthorizer
	authorizeState   domaintask.TaskMeetingReadAuthorizationCheck
}

type runtimeTaskMeetingConversation struct {
	executor         *runtimeTaskMeetingOccurrenceExecutor
	snapshot         runtimeTaskMeetingOccurrenceSnapshot
	root             domaintask.Task
	rootRun          domaintask.Run
	rootProof        domaintask.TaskMeetingRootProof
	authorizeReceipt domaintask.TaskMeetingInvocationOperationAuthorizer
	authorizeState   domaintask.TaskMeetingReadAuthorizationCheck
}

func (c *runtimeTaskMeetingConversation) EnsureSharedThread(ctx context.Context, meeting domainmeeting.Meeting, requestID string) (modulecore.ThreadID, error) {
	if c == nil || c.executor == nil || requestID == "" || meeting.Key != c.snapshot.Key {
		return "", meetingapp.ErrUnavailable
	}
	store, reader, err := c.owners()
	if err != nil {
		return "", err
	}
	request := c.sessionRequest(meeting, requestID)
	prior, found, err := store.FindTaskMeetingGroupSessionForRoot(ctx, meeting.Key.String(), c.root.TaskID, c.rootRun.RunID, reader, c.authorizeState)
	if err != nil {
		return "", err
	}
	if found {
		if prior.RequestID != requestID || prior.InputSHA256 != request.InputSHA256 {
			return "", domaintask.ErrTaskMeetingConflict
		}
		return prior.ThreadID, nil
	}
	receipt, err := store.EnsureTaskMeetingGroupSession(ctx, request, reader, c.authorizeState, func(authCtx context.Context, task domaintask.Task, run domaintask.Run, _ domaintask.SharedRoleContext) error {
		if err := c.authorizeState(authCtx, task, run, domaintask.SharedRoleContext{}); err != nil {
			return err
		}
		if task.TaskID != c.root.TaskID || run.RunID != c.rootRun.RunID || run.Status != domaintask.RunStatusRunning {
			return eventstore.ErrTaskMeetingForbidden
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return receipt.ThreadID, nil
}

func (c *runtimeTaskMeetingConversation) Dispatch(ctx context.Context, meeting domainmeeting.Meeting, utterance domainmeeting.Utterance) (domainmeeting.DeliveryEvidence, error) {
	request, err := c.groupMessageRequest(ctx, meeting, utterance)
	if err != nil {
		return domainmeeting.DeliveryEvidence{}, err
	}
	store, reader, outputReader, err := c.messageOwners()
	if err != nil {
		return domainmeeting.DeliveryEvidence{}, err
	}
	receipt, err := store.EnsureTaskMeetingGroupMessage(ctx, request, reader, outputReader, c.authorizeState, c.authorizeGroupMessage)
	if err != nil {
		return domainmeeting.DeliveryEvidence{}, fmt.Errorf("ensure frozen group message: %w", err)
	}
	evidence, found, err := c.deliverAndResolve(ctx, meeting, request, receipt)
	if err != nil {
		return domainmeeting.DeliveryEvidence{}, fmt.Errorf("deliver and resolve frozen group message: %w", err)
	}
	if !found {
		return domainmeeting.DeliveryEvidence{}, meetingapp.ErrUnavailable
	}
	return evidence, nil
}

func (c *runtimeTaskMeetingConversation) ReadDelivery(ctx context.Context, meeting domainmeeting.Meeting, utterance domainmeeting.Utterance) (domainmeeting.DeliveryEvidence, bool, error) {
	request, err := c.groupMessageRequest(ctx, meeting, utterance)
	if err != nil {
		return domainmeeting.DeliveryEvidence{}, false, err
	}
	store, reader, outputReader, err := c.messageOwners()
	if err != nil {
		return domainmeeting.DeliveryEvidence{}, false, err
	}
	receipt, found, err := store.FindTaskMeetingGroupMessage(ctx, request, reader, outputReader, c.authorizeState, c.authorizeGroupMessageRead)
	if err != nil || !found {
		return domainmeeting.DeliveryEvidence{}, found, err
	}
	evidence, found, err := c.deliverAndResolve(ctx, meeting, request, receipt)
	return evidence, found, err
}

func (c *runtimeTaskMeetingConversation) owners() (*eventstore.SQLiteStore, domaintask.TaskMeetingInvocationReader, error) {
	store, ok := c.executor.deps.canonicalEventStore.(*eventstore.SQLiteStore)
	reader, readerOK := c.executor.deps.taskStore.(domaintask.TaskMeetingInvocationReader)
	if !ok || store == nil || !readerOK || reader == nil {
		return nil, nil, meetingapp.ErrUnavailable
	}
	return store, reader, nil
}

func (c *runtimeTaskMeetingConversation) messageOwners() (*eventstore.SQLiteStore, domaintask.TaskMeetingInvocationReader, domaintask.TaskMeetingGenerationOutputStore, error) {
	store, reader, err := c.owners()
	if err != nil {
		return nil, nil, nil, err
	}
	outputReader, ok := c.executor.deps.taskStore.(domaintask.TaskMeetingGenerationOutputStore)
	if !ok || outputReader == nil {
		return nil, nil, nil, meetingapp.ErrUnavailable
	}
	return store, reader, outputReader, nil
}

func (c *runtimeTaskMeetingConversation) sessionRequest(meeting domainmeeting.Meeting, requestID string) eventstore.TaskMeetingGroupSessionRequest {
	return eventstore.TaskMeetingGroupSessionRequest{
		OccurrenceKey: meeting.Key.String(), TaskID: c.root.TaskID, RunID: c.rootRun.RunID,
		OwnerUserID: c.snapshot.Declaration.OwnerUserID, CoordinatorActorID: c.snapshot.Declaration.CoordinatorActorID,
		RegistrationEvidenceID: c.snapshot.Registration.RegistrationEvidenceID, ConfigRevision: c.snapshot.Declaration.Revision,
		ConfigSHA256: c.snapshot.Registration.ConfigSHA256, InputSHA256: c.root.TaskMeetingInvocation.InputSHA256,
		RequestID: requestID, Audience: append([]string(nil), c.snapshot.Declaration.Audience...),
	}
}

func (c *runtimeTaskMeetingConversation) groupMessageRequest(ctx context.Context, meeting domainmeeting.Meeting, utterance domainmeeting.Utterance) (eventstore.TaskMeetingGroupMessageRequest, error) {
	store, reader, outputReader, err := c.messageOwners()
	if err != nil {
		return eventstore.TaskMeetingGroupMessageRequest{}, err
	}
	if utterance.SourceTaskRef == nil || utterance.SourceRunRef == nil || utterance.ActorID == "" || utterance.Body == "" || utterance.DeliveryScopeEvidenceID.Validate() != nil || utterance.DeliveryGeneration == 0 || utterance.DeliveryEpoch == 0 {
		return eventstore.TaskMeetingGroupMessageRequest{}, domaintask.ErrTaskMeetingConflict
	}
	task, run, _, err := reader.GetTaskMeetingInvocationState(ctx, *utterance.SourceTaskRef, *utterance.SourceRunRef, c.authorizeState)
	if err != nil {
		return eventstore.TaskMeetingGroupMessageRequest{}, fmt.Errorf("read source generation invocation: %w", err)
	}
	output, resultRef, err := outputReader.GetTaskMeetingGenerationOutput(ctx, task.TaskID, run.RunID, c.authorizeState)
	if err != nil {
		return eventstore.TaskMeetingGroupMessageRequest{}, fmt.Errorf("read source generation output: %w", err)
	}
	var generated runtimeTaskMeetingOutput
	if json.Unmarshal(output, &generated) != nil || generated.Schema != runtimeTaskMeetingOutputSchema || generated.Message != utterance.MessageID || generated.Evidence.ActorID != utterance.ActorID || generated.Evidence.TaskRef != task.TaskID || generated.Evidence.RunRef != run.RunID || generated.Evidence.Key != meeting.Key || generated.Evidence.Body != utterance.Body {
		return eventstore.TaskMeetingGroupMessageRequest{}, domaintask.ErrTaskMeetingConflict
	}
	session, found, err := store.FindTaskMeetingGroupSessionForRoot(ctx, meeting.Key.String(), c.root.TaskID, c.rootRun.RunID, reader, c.authorizeState)
	if err != nil || !found {
		return eventstore.TaskMeetingGroupMessageRequest{}, errors.Join(fmt.Errorf("read root group session: %w", err), domaintask.ErrTaskMeetingNotFound)
	}
	return eventstore.TaskMeetingGroupMessageRequest{
		Session: session, TaskID: task.TaskID, RunID: run.RunID, ActorID: utterance.ActorID, MessageID: utterance.MessageID,
		Body: utterance.Body, GenerationResult: resultRef, AttemptGeneration: utterance.DeliveryGeneration,
		AttemptEpoch: utterance.DeliveryEpoch, ScopeEvidenceID: utterance.DeliveryScopeEvidenceID,
	}, nil
}

func (c *runtimeTaskMeetingConversation) authorizeGroupMessage(ctx context.Context, task domaintask.Task, run domaintask.Run, shared domaintask.SharedRoleContext, reference domaintask.TaskMeetingGenerationResultReference, output json.RawMessage, body string) error {
	if err := c.executor.authorizeCurrentTaskMeetingEffect(c.snapshot); err != nil {
		return err
	}
	if err := c.authorizeState(ctx, task, run, shared); err != nil {
		return err
	}
	var generated runtimeTaskMeetingOutput
	if json.Unmarshal(output, &generated) != nil || generated.Message == "" || generated.Evidence.Body != body || generated.Evidence.TaskRef != task.TaskID || generated.Evidence.RunRef != run.RunID || generated.Evidence.ActorID != task.Assignee || reference.ValidateRun(task, run) != nil {
		return eventstore.ErrTaskMeetingForbidden
	}
	current, found, err := c.executor.findCurrentOccurrence(ctx, c.snapshot)
	if err != nil || !found {
		return eventstore.ErrTaskMeetingForbidden
	}
	// Sample the clock only after the latest durable occurrence read so queue
	// latency and the read itself cannot leave an expired send authorized.
	now := c.executor.clock().UTC()
	if now.Before(current.ScheduledAt) || !now.Before(current.DeadlineAt) || !current.Timing.Ready(now) || current.Key != c.snapshot.Key ||
		current.ScheduleRevision != c.snapshot.Declaration.Revision || current.RegistrationEvidenceID != c.snapshot.Registration.RegistrationEvidenceID || current.ScopeEvidence == nil ||
		current.ScopeEvidence.EvidenceID != generated.Evidence.ScopeEvidenceID || current.Generation != generated.Evidence.Generation || current.Epoch != generated.Evidence.Epoch ||
		current.CurrentTurn == nil || current.CurrentTurn.ActorID != generated.Evidence.ActorID || current.CurrentTurn.Generation != generated.Evidence.Generation ||
		current.CurrentTurn.Epoch != generated.Evidence.Epoch || current.CurrentTurn.ScopeEvidenceID != generated.Evidence.ScopeEvidenceID ||
		current.CurrentTurn.GenerationRequestID != generated.Evidence.GenerationRequestID || current.CurrentTurn.GenerationInputSHA256 != generated.Evidence.GenerationInputSHA256 ||
		(current.Status != domainmeeting.StatusReporting && current.Status != domainmeeting.StatusAnswering) {
		return eventstore.ErrTaskMeetingForbidden
	}
	if current.Status == domainmeeting.StatusReporting && (current.CurrentTurn.Kind != domainmeeting.TurnReport || generated.Report == nil || generated.Answer != nil) ||
		current.Status == domainmeeting.StatusAnswering && (current.CurrentTurn.Kind != domainmeeting.TurnAnswer || generated.Answer == nil || generated.Report != nil) {
		return eventstore.ErrTaskMeetingForbidden
	}
	return nil
}

func (c *runtimeTaskMeetingConversation) authorizeGroupMessageRead(ctx context.Context, task domaintask.Task, run domaintask.Run, shared domaintask.SharedRoleContext, reference domaintask.TaskMeetingGenerationResultReference, output json.RawMessage, body string, message *domaintask.TaskMeetingMessageReference) error {
	if err := c.authorizeState(ctx, task, run, shared); err != nil {
		return err
	}
	var generated runtimeTaskMeetingOutput
	if json.Unmarshal(output, &generated) != nil || generated.Message == "" || generated.Evidence.Body != body || generated.Evidence.TaskRef != task.TaskID || generated.Evidence.RunRef != run.RunID || generated.Evidence.ActorID != task.Assignee || reference.ValidateRun(task, run) != nil {
		return eventstore.ErrTaskMeetingForbidden
	}
	if message != nil && (message.OccurrenceKey != c.snapshot.Key.String() || message.MessageID != taskMeetingMessageIDFromOutput(output) || message.ConfigSHA256 != c.snapshot.Registration.ConfigSHA256) {
		return eventstore.ErrTaskMeetingForbidden
	}
	return nil
}

func taskMeetingMessageIDFromOutput(raw []byte) modulecore.MessageID {
	var output runtimeTaskMeetingOutput
	if json.Unmarshal(raw, &output) != nil {
		return ""
	}
	return output.Message
}

func sameRuntimeJSON(left, right any) bool {
	a, errA := json.Marshal(left)
	b, errB := json.Marshal(right)
	return errA == nil && errB == nil && sameBytes(a, b)
}

func sameRuntimeConsumptionProofs(left, right []domaintask.TaskMeetingConsumedMessage) bool {
	return sameRuntimeJSON(left, right)
}

func (c *runtimeTaskMeetingConversation) deliverAndResolve(ctx context.Context, meeting domainmeeting.Meeting, request eventstore.TaskMeetingGroupMessageRequest, message eventstore.TaskMeetingGroupMessageReceipt) (domainmeeting.DeliveryEvidence, bool, error) {
	store, reader, err := c.owners()
	if err != nil {
		return domainmeeting.DeliveryEvidence{}, false, err
	}
	for _, actorID := range c.snapshot.Declaration.Audience {
		leases, err := store.ClaimTaskMeetingGroupDelivery(ctx, actorID, eventstore.WorkClaimInput{Limit: 8, LeaseDuration: time.Minute, Now: c.executor.clock().UTC()}, func(authCtx context.Context, requestedActor string, reference domaintask.TaskMeetingMessageReference) error {
			if requestedActor != actorID || reference != message.Reference || c.executor.agents[actorID] == nil {
				return eventstore.ErrTaskMeetingForbidden
			}
			_, _, _, findErr := c.executor.deps.taskManager.FindTaskMeetingRecipientContextByActor(authCtx, c.rootProof, actorID, c.authorizeState, c.authorizeState, c.authorizeReceipt)
			return findErr
		})
		if err != nil {
			return domainmeeting.DeliveryEvidence{}, false, fmt.Errorf("claim message delivery for %s: %w", actorID, err)
		}
		for _, lease := range leases {
			if err := c.consumeDelivery(ctx, actorID, meeting, message, lease, reader); err != nil {
				return domainmeeting.DeliveryEvidence{}, false, fmt.Errorf("consume message delivery for %s: %w", actorID, err)
			}
		}
	}
	proofReader, ok := c.executor.deps.taskStore.(domaintask.TaskMeetingMessageConsumptionReader)
	if !ok || proofReader == nil {
		return domainmeeting.DeliveryEvidence{}, false, meetingapp.ErrUnavailable
	}
	resolved, found, err := store.ResolveTaskMeetingGroupDeliveryReceipt(ctx, message.Reference, proofReader, func(authCtx context.Context, task domaintask.Task, run domaintask.Run, shared domaintask.SharedRoleContext, ref domaintask.TaskMeetingMessageReference) error {
		if ref != message.Reference || !containsStringExact(c.snapshot.Declaration.Audience, task.Assignee) {
			return eventstore.ErrTaskMeetingForbidden
		}
		return c.authorizeState(authCtx, task, run, shared)
	})
	if err != nil || !found {
		return domainmeeting.DeliveryEvidence{}, found, fmt.Errorf("resolve message delivery receipt: %w", err)
	}
	return resolved.Evidence, true, nil
}

func (c *runtimeTaskMeetingConversation) consumeDelivery(ctx context.Context, actorID string, meeting domainmeeting.Meeting, message eventstore.TaskMeetingGroupMessageReceipt, lease eventstore.LeasedTaskMeetingDelivery, reader domaintask.TaskMeetingInvocationReader) error {
	actor := c.executor.agents[actorID]
	if actor == nil {
		return meetingapp.ErrUnavailable
	}
	contextTask, contextRun, _, err := c.executor.deps.taskManager.FindTaskMeetingRecipientContextByActor(ctx, c.rootProof, actorID, c.authorizeState, c.authorizeState, c.authorizeReceipt)
	if err != nil {
		return fmt.Errorf("read recipient context: %w", err)
	}
	ref := lease.Reference
	authorizeMessage := func(authCtx context.Context, task domaintask.Task, run domaintask.Run, shared domaintask.SharedRoleContext, actual domaintask.TaskMeetingMessageReference) error {
		if task.TaskID != contextTask.TaskID || run.RunID != contextRun.RunID || task.Assignee != actorID || actual != ref || !sameRuntimeStringSlice(message.Audience, c.snapshot.Declaration.Audience) {
			return eventstore.ErrTaskMeetingForbidden
		}
		return c.authorizeState(authCtx, task, run, shared)
	}
	body, err := c.executor.deps.canonicalEventStore.(*eventstore.SQLiteStore).ReadTaskMeetingGroupMessage(ctx, ref, actorID, contextTask.TaskID, contextRun.RunID, reader, authorizeMessage)
	if err != nil {
		return fmt.Errorf("read canonical group message for recipient: %w", err)
	}
	prepared, err := actor.PrepareTaskMeetingReceive(ctx, domainagent.TaskMeetingReceiveRequest{
		RecipientTask: contextTask, RecipientRun: contextRun,
		Scope:     domaintask.TaskMeetingGenerationScope{ScopeEvidenceID: ref.ScopeEvidenceID, Generation: ref.AttemptGeneration, Epoch: ref.AttemptEpoch},
		Reference: ref, Body: body, Audience: append([]string(nil), message.Audience...), AuthorizeRead: authorizeMessage,
	})
	if err != nil {
		return fmt.Errorf("prepare actor message receipt: %w", err)
	}
	proof, err := actor.ReceiveTaskMeetingMessage(ctx, prepared, c.executor.deps.taskManager)
	if err != nil {
		return fmt.Errorf("persist actor message consumption: %w", err)
	}
	store := c.executor.deps.canonicalEventStore.(*eventstore.SQLiteStore)
	consumptionReader, ok := c.executor.deps.taskStore.(domaintask.TaskMeetingMessageConsumptionReader)
	if !ok || consumptionReader == nil {
		return meetingapp.ErrUnavailable
	}
	if _, err := store.AppendTaskMeetingGroupConsumption(ctx, proof, reader, consumptionReader, authorizeMessage); err != nil {
		return fmt.Errorf("append canonical consumption receipt: %w", err)
	}
	return store.AckTaskMeetingGroupDelivery(ctx, eventstore.WorkLeaseInput{WorkID: lease.Work.WorkID, LeaseToken: lease.Work.LeaseToken, Version: lease.Work.Version, Now: c.executor.clock().UTC()})
}

func (c *runtimeTaskMeetingConversation) FindTranscript(ctx context.Context, meeting domainmeeting.Meeting, requestID string) (domainmeeting.MinutesArtifactReference, bool, error) {
	store, ok := c.executor.deps.workstreamStore.(*workstreampersistence.SQLiteStore)
	if !ok || store == nil || requestID == "" || meeting.Key != c.snapshot.Key {
		return domainmeeting.MinutesArtifactReference{}, false, meetingapp.ErrUnavailable
	}
	access, err := (runtimeTaskMeetingAuthority{snapshot: c.snapshot}).AuthorizeOccurrence(ctx, meeting.Key, c.snapshot.Config)
	if err != nil {
		return domainmeeting.MinutesArtifactReference{}, false, err
	}
	ref, found, err := store.FindTaskMeetingMinutes(ctx, access.OwnerUserID, meeting.Key, requestID, meeting.Guard(), func(authCtx context.Context, owner string, key domainmeeting.OccurrenceKey, requested string, guard domainmeeting.Guard, actual *workstreampersistence.TaskMeetingMinutesReference) error {
		if owner != c.snapshot.Declaration.OwnerUserID || key != c.snapshot.Key || requested != requestID || guard != meeting.Guard() {
			return eventstore.ErrTaskMeetingForbidden
		}
		if actual != nil && (actual.ConfigSHA256 != c.snapshot.Registration.ConfigSHA256 || actual.RegistrationEvidenceID != c.snapshot.Registration.RegistrationEvidenceID || !sameRuntimeStringSlice(actual.Audience, c.snapshot.Declaration.Audience)) {
			return eventstore.ErrTaskMeetingForbidden
		}
		return authCtx.Err()
	})
	if err != nil || !found {
		return domainmeeting.MinutesArtifactReference{}, found, err
	}
	return domainmeeting.MinutesArtifactReference{
		RequestID: requestID, ArtifactID: ref.ArtifactID, WorkstreamID: ref.WorkstreamID, OwnerUserID: ref.OwnerUserID,
		CoordinatorActorID: ref.CoordinatorActorID, Key: c.snapshot.Key, RegistrationEvidenceID: ref.RegistrationEvidenceID,
		ConfigRevision: ref.ConfigRevision, ConfigSHA256: ref.ConfigSHA256, Audience: append([]string(nil), ref.Audience...),
		AudienceSHA256: ref.AudienceSHA256, Guard: ref.Guard, TranscriptSHA256: ref.TranscriptSHA256, CreatedAt: ref.CreatedAt,
	}, true, nil
}

func (c *runtimeTaskMeetingConversation) PersistTranscript(ctx context.Context, meeting domainmeeting.Meeting, requestID string) (domainmeeting.MinutesArtifactReference, error) {
	store, ok := c.executor.deps.workstreamStore.(*workstreampersistence.SQLiteStore)
	if !ok || store == nil || requestID == "" || meeting.Key != c.snapshot.Key || meeting.TranscriptRequestID != requestID {
		return domainmeeting.MinutesArtifactReference{}, meetingapp.ErrUnavailable
	}
	messages, err := c.minutesMessageProofs(ctx, meeting)
	if err != nil {
		return domainmeeting.MinutesArtifactReference{}, err
	}
	request := workstreampersistence.TaskMeetingMinutesRequest{
		OwnerUserID: c.snapshot.Declaration.OwnerUserID, CoordinatorActorID: c.snapshot.Declaration.CoordinatorActorID,
		RegistrationEvidenceID: c.snapshot.Registration.RegistrationEvidenceID, ConfigSHA256: c.snapshot.Registration.ConfigSHA256,
		Meeting: meeting, Messages: messages,
	}
	ref, err := store.PersistTaskMeetingMinutes(ctx, request, func(authCtx context.Context, candidate workstreampersistence.TaskMeetingMinutesRequest) error {
		if candidate.Meeting.Key != c.snapshot.Key || candidate.Meeting.Guard() != meeting.Guard() || candidate.Meeting.TranscriptRequestID != requestID ||
			candidate.OwnerUserID != c.snapshot.Declaration.OwnerUserID || candidate.ConfigSHA256 != c.snapshot.Registration.ConfigSHA256 {
			return eventstore.ErrTaskMeetingForbidden
		}
		return c.verifyMinutesProofs(authCtx, candidate)
	})
	if err != nil {
		return domainmeeting.MinutesArtifactReference{}, err
	}
	readback, found, err := store.FindTaskMeetingMinutes(ctx, ref.OwnerUserID, meeting.Key, requestID, meeting.Guard(), func(authCtx context.Context, owner string, key domainmeeting.OccurrenceKey, requested string, guard domainmeeting.Guard, actual *workstreampersistence.TaskMeetingMinutesReference) error {
		if owner != c.snapshot.Declaration.OwnerUserID || key != c.snapshot.Key || requested != requestID || guard != meeting.Guard() {
			return fmt.Errorf("%w: minutes owner/key/request/guard binding mismatch", eventstore.ErrTaskMeetingForbidden)
		}
		if actual != nil && !sameTaskMeetingMinutesReference(*actual, ref) {
			return fmt.Errorf("%w: canonical minutes tuple differs (artifact=%t workstream=%t owner=%t coordinator=%t occurrence=%t registration=%t revision=%t config=%t audience=%t audience_hash=%t guard=%t transcript=%t created_at=%t)", eventstore.ErrTaskMeetingForbidden,
				actual.ArtifactID == ref.ArtifactID, actual.WorkstreamID == ref.WorkstreamID, actual.OwnerUserID == ref.OwnerUserID, actual.CoordinatorActorID == ref.CoordinatorActorID,
				actual.OccurrenceKey == ref.OccurrenceKey, actual.RegistrationEvidenceID == ref.RegistrationEvidenceID, actual.ConfigRevision == ref.ConfigRevision,
				actual.ConfigSHA256 == ref.ConfigSHA256, sameRuntimeStringSlice(actual.Audience, ref.Audience), actual.AudienceSHA256 == ref.AudienceSHA256,
				actual.Guard == ref.Guard, actual.TranscriptSHA256 == ref.TranscriptSHA256, actual.CreatedAt.Equal(ref.CreatedAt))
		}
		return authCtx.Err()
	})
	if err != nil || !found || !sameTaskMeetingMinutesReference(readback, ref) {
		return domainmeeting.MinutesArtifactReference{}, errors.Join(err, domaintask.ErrTaskMeetingConflict)
	}
	return domainmeeting.MinutesArtifactReference{
		RequestID: requestID, ArtifactID: ref.ArtifactID, WorkstreamID: ref.WorkstreamID, OwnerUserID: ref.OwnerUserID,
		CoordinatorActorID: ref.CoordinatorActorID, Key: c.snapshot.Key, RegistrationEvidenceID: ref.RegistrationEvidenceID,
		ConfigRevision: ref.ConfigRevision, ConfigSHA256: ref.ConfigSHA256, Audience: append([]string(nil), ref.Audience...),
		AudienceSHA256: ref.AudienceSHA256, Guard: ref.Guard, TranscriptSHA256: ref.TranscriptSHA256, CreatedAt: ref.CreatedAt,
	}, nil
}

func sameTaskMeetingMinutesReference(left, right workstreampersistence.TaskMeetingMinutesReference) bool {
	return left.ArtifactID == right.ArtifactID && left.WorkstreamID == right.WorkstreamID && left.OwnerUserID == right.OwnerUserID &&
		left.CoordinatorActorID == right.CoordinatorActorID && left.OccurrenceKey == right.OccurrenceKey &&
		left.RegistrationEvidenceID == right.RegistrationEvidenceID && left.ConfigRevision == right.ConfigRevision &&
		left.ConfigSHA256 == right.ConfigSHA256 && sameRuntimeStringSlice(left.Audience, right.Audience) &&
		left.AudienceSHA256 == right.AudienceSHA256 && left.Guard == right.Guard && left.TranscriptSHA256 == right.TranscriptSHA256 &&
		left.CreatedAt.Equal(right.CreatedAt)
}

func (c *runtimeTaskMeetingConversation) minutesMessageProofs(ctx context.Context, meeting domainmeeting.Meeting) ([]workstreampersistence.TaskMeetingMinutesMessageProof, error) {
	ordered := make([]domainmeeting.Utterance, 0, len(meeting.Reports)+len(meeting.Answers))
	for _, report := range meeting.Reports {
		ordered = append(ordered, report.Utterance)
	}
	for _, answer := range meeting.Answers {
		ordered = append(ordered, answer.Utterance)
	}
	proofs := make([]workstreampersistence.TaskMeetingMinutesMessageProof, 0, len(ordered))
	for _, utterance := range ordered {
		proof := workstreampersistence.TaskMeetingMinutesMessageProof{DeliveryState: utterance.DeliveryState, MessageID: utterance.MessageID, SenderActorID: utterance.ActorID}
		if utterance.SourceTaskRef == nil || utterance.SourceRunRef == nil {
			return nil, domaintask.ErrTaskMeetingConflict
		}
		proof.SenderTaskID, proof.SenderRunID = *utterance.SourceTaskRef, *utterance.SourceRunRef
		if utterance.DeliveryState == domainmeeting.DeliveryDelivered {
			request, err := c.groupMessageRequest(ctx, meeting, utterance)
			if err != nil {
				return nil, err
			}
			store, reader, outputReader, err := c.messageOwners()
			if err != nil {
				return nil, err
			}
			message, found, err := store.FindTaskMeetingGroupMessage(ctx, request, reader, outputReader, c.authorizeState, c.authorizeGroupMessageRead)
			if err != nil || !found {
				return nil, errors.Join(fmt.Errorf("read canonical message for minutes %s: %w", utterance.MessageID, err), domaintask.ErrTaskMeetingNotFound)
			}
			consumptionReader, ok := c.executor.deps.taskStore.(domaintask.TaskMeetingMessageConsumptionReader)
			if !ok || consumptionReader == nil {
				return nil, meetingapp.ErrUnavailable
			}
			delivery, found, err := store.ResolveTaskMeetingGroupDeliveryReceipt(ctx, message.Reference, consumptionReader, func(authCtx context.Context, task domaintask.Task, run domaintask.Run, shared domaintask.SharedRoleContext, reference domaintask.TaskMeetingMessageReference) error {
				if reference != message.Reference {
					return eventstore.ErrTaskMeetingForbidden
				}
				return c.authorizeState(authCtx, task, run, shared)
			})
			if err != nil || !found || delivery.Reference != message.Reference {
				return nil, errors.Join(fmt.Errorf("resolve canonical delivery for minutes %s: %w", utterance.MessageID, err), domaintask.ErrTaskMeetingConflict)
			}
			proof.Reference, proof.Delivery = delivery.Reference, delivery.Evidence
			proof.ConsumptionProofs = append([]domaintask.TaskMeetingConsumedMessage(nil), delivery.ConsumptionProofs...)
		}
		proofs = append(proofs, proof)
	}
	return proofs, nil
}

func (c *runtimeTaskMeetingConversation) verifyMinutesProofs(ctx context.Context, request workstreampersistence.TaskMeetingMinutesRequest) error {
	for _, expected := range request.Messages {
		if expected.DeliveryState != domainmeeting.DeliveryDelivered {
			if len(expected.ConsumptionProofs) != 0 || expected.Reference != (domaintask.TaskMeetingMessageReference{}) {
				return domaintask.ErrTaskMeetingConflict
			}
			continue
		}
		var utterance domainmeeting.Utterance
		found := false
		for _, report := range request.Meeting.Reports {
			if report.Utterance.MessageID == expected.MessageID {
				utterance, found = report.Utterance, true
				break
			}
		}
		if !found {
			for _, answer := range request.Meeting.Answers {
				if answer.Utterance.MessageID == expected.MessageID {
					utterance, found = answer.Utterance, true
					break
				}
			}
		}
		if !found {
			return domaintask.ErrTaskMeetingConflict
		}
		message, err := c.groupMessageRequest(ctx, request.Meeting, utterance)
		if err != nil {
			return err
		}
		store, reader, outputReader, err := c.messageOwners()
		if err != nil {
			return err
		}
		stored, found, err := store.FindTaskMeetingGroupMessage(ctx, message, reader, outputReader, c.authorizeState, c.authorizeGroupMessageRead)
		if err != nil || !found || stored.Reference != expected.Reference || stored.SenderTaskID != expected.SenderTaskID || stored.SenderRunID != expected.SenderRunID {
			return errors.Join(fmt.Errorf("verify canonical message for minutes %s: %w", expected.MessageID, err), domaintask.ErrTaskMeetingConflict)
		}
		consumptionReader, ok := c.executor.deps.taskStore.(domaintask.TaskMeetingMessageConsumptionReader)
		if !ok || consumptionReader == nil {
			return meetingapp.ErrUnavailable
		}
		delivery, delivered, err := store.ResolveTaskMeetingGroupDeliveryReceipt(ctx, stored.Reference, consumptionReader, func(authCtx context.Context, task domaintask.Task, run domaintask.Run, shared domaintask.SharedRoleContext, ref domaintask.TaskMeetingMessageReference) error {
			return c.authorizeState(authCtx, task, run, shared)
		})
		if err != nil || !delivered || !sameRuntimeConsumptionProofs(delivery.ConsumptionProofs, expected.ConsumptionProofs) || !sameRuntimeJSON(delivery.Evidence, expected.Delivery) {
			return errors.Join(fmt.Errorf("verify canonical delivery for minutes %s: %w", expected.MessageID, err), domaintask.ErrTaskMeetingConflict)
		}
	}
	return nil
}

const runtimeTaskMeetingOutputSchema = "task_meeting.runtime_output.v1"

func (p *runtimeTaskMeetingParticipants) EnsureReport(ctx context.Context, meeting domainmeeting.Meeting, requestID, seedSHA string, frozenSeed []byte, actorID string) (domainmeeting.Report, domainmeeting.SpeakerEvidence, error) {
	if p == nil || p.executor == nil || meeting.CurrentTurn == nil || meeting.CurrentTurn.Kind != domainmeeting.TurnReport {
		return domainmeeting.Report{}, domainmeeting.SpeakerEvidence{}, meetingapp.ErrUnavailable
	}
	output, evidence, err := p.ensureGeneration(ctx, meeting, requestID, seedSHA, frozenSeed, actorID, domaintask.TaskMeetingPurposeReport, nil)
	if err != nil || output.Report == nil || output.Answer != nil {
		return domainmeeting.Report{}, domainmeeting.SpeakerEvidence{}, errors.Join(err, domaintask.ErrTaskMeetingConflict)
	}
	return *output.Report, evidence, nil
}

func (p *runtimeTaskMeetingParticipants) EnsureAnswer(ctx context.Context, meeting domainmeeting.Meeting, requestID, seedSHA string, frozenSeed []byte, actorID string, concernID domainmeeting.ConcernID) (domainmeeting.Answer, domainmeeting.SpeakerEvidence, error) {
	if p == nil || p.executor == nil || meeting.CurrentTurn == nil || meeting.CurrentTurn.Kind != domainmeeting.TurnAnswer || meeting.CurrentTurn.ConcernID == nil || *meeting.CurrentTurn.ConcernID != concernID {
		return domainmeeting.Answer{}, domainmeeting.SpeakerEvidence{}, meetingapp.ErrUnavailable
	}
	output, evidence, err := p.ensureGeneration(ctx, meeting, requestID, seedSHA, frozenSeed, actorID, domaintask.TaskMeetingPurposeAnswer, &concernID)
	if errors.Is(err, domaintask.ErrTaskMeetingExecutionUnknown) {
		storedTask, storedRun, findErr := p.executor.deps.taskManager.FindTaskMeetingGenerationInvocation(ctx, meeting.Key.String(), domaintask.TaskMeetingPurposeAnswer, actorID, modulecore.RequestID(requestID), p.authorizeReceipt, p.authorizeState, p.authorizeState)
		if findErr == nil {
			unknown, unknownErr := p.executor.deps.taskManager.GetTaskMeetingGenerationUnknown(ctx, storedTask.TaskID, storedRun.RunID, p.authorizeState)
			reporter, reporterFound := taskMeetingReporter(meeting, concernID)
			if unknownErr == nil && reporterFound {
				return domainmeeting.Answer{}, domainmeeting.SpeakerEvidence{}, &meetingapp.TerminalAnswerAttemptError{Attempt: domainmeeting.AnswerAttempt{ConcernID: concernID, ReporterID: reporter, UnknownReceipt: unknown}}
			}
		}
	}
	if err != nil || output.Answer == nil || output.Report != nil {
		return domainmeeting.Answer{}, domainmeeting.SpeakerEvidence{}, errors.Join(err, domaintask.ErrTaskMeetingConflict)
	}
	return *output.Answer, evidence, nil
}

func (p *runtimeTaskMeetingParticipants) ensureGeneration(ctx context.Context, meeting domainmeeting.Meeting, requestID, seedSHA string, frozenSeed []byte, actorID string, purpose domaintask.TaskMeetingPurpose, concernID *domainmeeting.ConcernID) (runtimeTaskMeetingOutput, domainmeeting.SpeakerEvidence, error) {
	e := p.executor
	actor := e.agents[actorID]
	if actor == nil || ctx == nil || meeting.CurrentTurn == nil || meeting.CurrentTurn.ActorID != actorID || meeting.CurrentTurn.GenerationRequestID != requestID || meeting.CurrentTurn.GenerationInputSHA256 != seedSHA || runtimeTaskMeetingDigest(frozenSeed) != seedSHA || !sameBytes(frozenSeed, meeting.CurrentTurn.GenerationInputJSON) {
		return runtimeTaskMeetingOutput{}, domainmeeting.SpeakerEvidence{}, meetingapp.ErrUnavailable
	}
	rootProof := p.rootProof
	authorizeState := p.authorizeState
	authorizeReceipt := p.authorizeReceipt
	authorizeSource := func(authCtx context.Context, task domaintask.Task, run domaintask.Run, shared domaintask.SharedRoleContext) error {
		if err := authorizeState(authCtx, task, run, shared); err != nil {
			return err
		}
		if task.Assignee != actorID || task.TaskMeetingInvocation == nil || task.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeRecipientContext || task.ParentTaskID != rootProof.TaskID {
			return eventstore.ErrTaskMeetingForbidden
		}
		return nil
	}
	storedTask, storedRun, findErr := e.deps.taskManager.FindTaskMeetingGenerationInvocation(ctx, meeting.Key.String(), purpose, actorID, modulecore.RequestID(requestID), authorizeReceipt, authorizeState, authorizeSource)
	if findErr == nil {
		prepared, restoreErr := actor.RestoreTaskMeetingPreparedGeneration(storedTask, storedRun, seedSHA)
		if restoreErr != nil {
			return runtimeTaskMeetingOutput{}, domainmeeting.SpeakerEvidence{}, restoreErr
		}
		output, evidence, readErr := e.readFinishedGeneration(ctx, meeting, storedTask, storedRun, prepared, authorizeState, authorizeSource)
		if errors.Is(readErr, domaintask.ErrTaskMeetingExecutionUnknown) {
			return runtimeTaskMeetingOutput{}, domainmeeting.SpeakerEvidence{}, p.recoverUnknownGenerationFence(ctx, storedTask, storedRun, authorizeState)
		}
		return output, evidence, readErr
	}
	if !errors.Is(findErr, domaintask.ErrTaskMeetingNotFound) {
		return runtimeTaskMeetingOutput{}, domainmeeting.SpeakerEvidence{}, findErr
	}
	contextTask, contextRun, shared, err := e.deps.taskManager.FindTaskMeetingRecipientContextByActor(ctx, rootProof, actorID, authorizeState, authorizeState, authorizeReceipt)
	if err != nil {
		return runtimeTaskMeetingOutput{}, domainmeeting.SpeakerEvidence{}, err
	}
	sourceMessages, err := e.taskMeetingSourceMessages(ctx, meeting, p.snapshot, actorID, contextTask, contextRun, shared, authorizeState)
	if err != nil {
		return runtimeTaskMeetingOutput{}, domainmeeting.SpeakerEvidence{}, err
	}
	turn := *meeting.CurrentTurn
	intent, err := e.taskMeetingPrepareIntent(p.snapshot, meeting, actorID, purpose, turn, seedSHA, contextTask, contextRun, rootProof, shared)
	if err != nil {
		return runtimeTaskMeetingOutput{}, domainmeeting.SpeakerEvidence{}, err
	}
	concernDescription := ""
	if concernID != nil {
		concernDescription, err = taskMeetingConcernDescription(meeting, *concernID)
		if err != nil {
			return runtimeTaskMeetingOutput{}, domainmeeting.SpeakerEvidence{}, err
		}
	}
	var shareableStatuses []domainagent.TaskMeetingShareableStatus
	if purpose == domaintask.TaskMeetingPurposeReport {
		shareableStatuses, err = e.taskMeetingShareableStatuses(ctx, actorID)
		if err != nil {
			return runtimeTaskMeetingOutput{}, domainmeeting.SpeakerEvidence{}, err
		}
	}
	prepared, err := actor.PrepareTaskMeeting(ctx, domainagent.TaskMeetingPrepareRequest{
		Intent: intent, CurrentTurn: turn, ConcernDescription: concernDescription,
		SourceMessages: sourceMessages, ShareableStatuses: shareableStatuses,
	})
	if err != nil {
		return runtimeTaskMeetingOutput{}, domainmeeting.SpeakerEvidence{}, err
	}
	preparedInput := prepared.FrozenInputJSON()
	request, err := e.taskMeetingGenerationRequest(p.snapshot, meeting, actorID, purpose, modulecore.RequestID(requestID), preparedInput, prepared.GenerationIntent(), contextTask, contextRun, shared)
	if err != nil {
		return runtimeTaskMeetingOutput{}, domainmeeting.SpeakerEvidence{}, err
	}
	if err := request.Validate(); err != nil {
		return runtimeTaskMeetingOutput{}, domainmeeting.SpeakerEvidence{}, err
	}
	if err := e.authorizeAdmission(p.snapshot)(ctx, request, contextTask, contextRun, shared); err != nil {
		return runtimeTaskMeetingOutput{}, domainmeeting.SpeakerEvidence{}, err
	}
	storedTask, storedRun, err = e.deps.taskManager.EnsureTaskMeetingGeneration(ctx, request, contextTask.TaskID, contextRun.RunID, e.authorizeAdmission(p.snapshot), authorizeSource, authorizeReceipt, authorizeState)
	if err != nil {
		return runtimeTaskMeetingOutput{}, domainmeeting.SpeakerEvidence{}, err
	}
	prepared, err = actor.RestoreTaskMeetingPreparedGeneration(storedTask, storedRun, seedSHA)
	if err != nil {
		return runtimeTaskMeetingOutput{}, domainmeeting.SpeakerEvidence{}, err
	}
	if _, err := e.deps.taskManager.ExecuteTaskMeetingGeneration(ctx, storedTask.TaskID, storedRun.RunID, authorizeState, authorizeSource,
		func(effectCtx context.Context, current domaintask.Task, currentRun domaintask.Run, currentShared domaintask.SharedRoleContext) error {
			return e.authorizeNewGenerationEffect(effectCtx, p.snapshot, actorID, current, currentRun, currentShared, meeting)
		},
		func(effectCtx context.Context, current domaintask.Task, currentRun domaintask.Run, _ domaintask.SharedRoleContext, _ domaintask.SharedRoleContext, _ json.RawMessage) (json.RawMessage, error) {
			callCtx, identityErr := execution.WithIdentity(effectCtx, current.TaskID, currentRun.RunID, currentRun.TraceID)
			if identityErr != nil {
				return nil, identityErr
			}
			admission := llm.NewGenerateRequestAdmission(func() error { return effectCtx.Err() })
			response, generationErr := actor.GenerateTaskMeeting(callCtx, prepared, current, currentRun, admission)
			if generationErr != nil {
				return nil, generationErr
			}
			output, outputErr := buildRuntimeTaskMeetingOutput(meeting, current, currentRun, response, purpose, concernID, e.clock())
			if outputErr != nil {
				return nil, outputErr
			}
			encoded, encodeErr := json.Marshal(output)
			return encoded, encodeErr
		}); err != nil {
		if errors.Is(err, domaintask.ErrTaskMeetingExecutionUnknown) {
			return runtimeTaskMeetingOutput{}, domainmeeting.SpeakerEvidence{}, p.recoverUnknownGenerationFence(ctx, storedTask, storedRun, authorizeState)
		}
		return runtimeTaskMeetingOutput{}, domainmeeting.SpeakerEvidence{}, err
	}
	return e.readFinishedGeneration(ctx, meeting, storedTask, storedRun, prepared, authorizeState, authorizeSource)
}

// taskMeetingShareableStatuses exposes only a bounded, title-free projection
// of the actor's own public Tasks. TaskManager.List uses the generic read
// boundary, which excludes private Tasks; owner-managed meeting Tasks are
// filtered here as well. This is deliberately not a complete work inventory.
func (e *runtimeTaskMeetingOccurrenceExecutor) taskMeetingShareableStatuses(ctx context.Context, actorID string) ([]domainagent.TaskMeetingShareableStatus, error) {
	if e == nil || e.deps == nil || e.deps.taskManager == nil || ctx == nil || strings.TrimSpace(actorID) == "" {
		return nil, meetingapp.ErrUnavailable
	}
	const scanLimit = 256
	tasks, err := e.deps.taskManager.List(ctx, domaintask.Filter{Assignee: actorID, Limit: scanLimit})
	if err != nil {
		return nil, fmt.Errorf("read public self Task statuses: %w", err)
	}
	publicSelf := make([]domaintask.Task, 0, len(tasks))
	for _, task := range tasks {
		if task.Assignee != actorID {
			return nil, fmt.Errorf("Task owner returned a record outside the requested assignee scope")
		}
		if task.PrivateAccess != nil || domaintask.IsOwnerManagedTask(task) {
			continue
		}
		if err := task.Validate(); err != nil {
			return nil, fmt.Errorf("public self Task status record is invalid: %w", err)
		}
		publicSelf = append(publicSelf, task)
	}
	sort.Slice(publicSelf, func(i, j int) bool {
		if !publicSelf[i].UpdatedAt.Equal(publicSelf[j].UpdatedAt) {
			return publicSelf[i].UpdatedAt.After(publicSelf[j].UpdatedAt)
		}
		return publicSelf[i].TaskID < publicSelf[j].TaskID
	})
	if len(publicSelf) > domainagent.TaskMeetingMaxStatusRows {
		publicSelf = publicSelf[:domainagent.TaskMeetingMaxStatusRows]
	}
	statuses := make([]domainagent.TaskMeetingShareableStatus, 0, len(publicSelf))
	for _, task := range publicSelf {
		statuses = append(statuses, domainagent.TaskMeetingShareableStatus{
			ActorID: actorID, TaskRef: task.TaskID, Status: task.Status,
		})
	}
	return statuses, nil
}

func (p *runtimeTaskMeetingParticipants) recoverUnknownGenerationFence(ctx context.Context, task domaintask.Task, run domaintask.Run, authorize domaintask.TaskMeetingReadAuthorizationCheck) error {
	unknown, err := p.executor.deps.taskManager.GetTaskMeetingGenerationUnknown(ctx, task.TaskID, run.RunID, authorize)
	if err != nil {
		return errors.Join(domaintask.ErrTaskMeetingExecutionUnknown, err)
	}
	if err := p.executor.deps.taskManager.RecoverTaskMeetingUnknownGenerationFence(ctx, task.TaskID, run.RunID, unknown, authorize); err != nil {
		return errors.Join(domaintask.ErrTaskMeetingExecutionUnknown, err)
	}
	return domaintask.ErrTaskMeetingExecutionUnknown
}

func sameBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (e *runtimeTaskMeetingOccurrenceExecutor) taskMeetingSourceMessages(ctx context.Context, meeting domainmeeting.Meeting, snapshot runtimeTaskMeetingOccurrenceSnapshot, actorID string, task domaintask.Task, run domaintask.Run, shared domaintask.SharedRoleContext, authorize domaintask.TaskMeetingReadAuthorizationCheck) ([]domainagent.TaskMeetingSourceMessage, error) {
	store, ok := e.deps.canonicalEventStore.(*eventstore.SQLiteStore)
	reader, readerOK := e.deps.taskStore.(domaintask.TaskMeetingInvocationReader)
	if !ok || store == nil || !readerOK || reader == nil {
		return nil, meetingapp.ErrUnavailable
	}
	messages := make([]domainagent.TaskMeetingSourceMessage, 0, len(shared.TaskMeetingConsumedMessages))
	for _, proof := range shared.TaskMeetingConsumedMessages {
		ref := proof.Reference
		authorizeMessage := func(authCtx context.Context, current domaintask.Task, currentRun domaintask.Run, currentShared domaintask.SharedRoleContext, actual domaintask.TaskMeetingMessageReference) error {
			if err := authorize(authCtx, current, currentRun, currentShared); err != nil {
				return err
			}
			if current.TaskID != task.TaskID || currentRun.RunID != run.RunID || actual != ref || !sameRuntimeStringSlice(snapshot.Declaration.Audience, meeting.Participants) || !containsStringExact(snapshot.Declaration.Audience, actorID) {
				return eventstore.ErrTaskMeetingForbidden
			}
			return nil
		}
		body, err := store.ReadTaskMeetingGroupMessage(ctx, ref, actorID, task.TaskID, run.RunID, reader, authorizeMessage)
		if err != nil {
			return nil, err
		}
		speaker, found := taskMeetingSpeaker(meeting, ref.MessageID)
		if !found {
			return nil, domaintask.ErrTaskMeetingConflict
		}
		messages = append(messages, domainagent.TaskMeetingSourceMessage{Proof: proof, SpeakerActorID: speaker, Body: body, Audience: append([]string(nil), snapshot.Declaration.Audience...)})
	}
	return messages, nil
}

func taskMeetingSpeaker(meeting domainmeeting.Meeting, messageID modulecore.MessageID) (string, bool) {
	for _, report := range meeting.Reports {
		if report.Utterance.MessageID == messageID {
			return report.Utterance.ActorID, true
		}
	}
	for _, answer := range meeting.Answers {
		if answer.Utterance.MessageID == messageID {
			return answer.Utterance.ActorID, true
		}
	}
	return "", false
}

func taskMeetingConcernDescription(meeting domainmeeting.Meeting, concernID domainmeeting.ConcernID) (string, error) {
	for _, report := range meeting.Reports {
		if report.Utterance.MessageID != concernID.ReportMessageID || concernID.Ordinal == 0 || int(concernID.Ordinal) > len(report.Concerns) {
			continue
		}
		return report.Concerns[concernID.Ordinal-1].Description, nil
	}
	return "", domaintask.ErrTaskMeetingConflict
}

func (e *runtimeTaskMeetingOccurrenceExecutor) taskMeetingPrepareIntent(snapshot runtimeTaskMeetingOccurrenceSnapshot, meeting domainmeeting.Meeting, actorID string, purpose domaintask.TaskMeetingPurpose, turn domainmeeting.Turn, seedSHA string, contextTask domaintask.Task, contextRun domaintask.Run, rootProof domaintask.TaskMeetingRootProof, shared domaintask.SharedRoleContext) (domainagent.TaskMeetingPrepareIntent, error) {
	scope := domaintask.TaskMeetingGenerationScope{ScopeEvidenceID: turn.ScopeEvidenceID, Generation: turn.Generation, Epoch: turn.Epoch}
	if scope.Validate() != nil || modulecore.RequestID(turn.GenerationRequestID).Validate() != nil {
		return domainagent.TaskMeetingPrepareIntent{}, domaintask.ErrTaskMeetingConflict
	}
	contextSHA, err := domaintask.TaskMeetingConsumedMessagesSHA256(shared.TaskMeetingConsumedMessages)
	if err != nil {
		return domainagent.TaskMeetingPrepareIntent{}, err
	}
	return domainagent.TaskMeetingPrepareIntent{
		OccurrenceKey: meeting.Key.String(), Purpose: purpose, CoordinatorActorID: snapshot.Declaration.CoordinatorActorID,
		ActorID: actorID, RequestID: modulecore.RequestID(turn.GenerationRequestID), SeedSHA256: seedSHA, Scope: scope,
		ConfigRevision: snapshot.Declaration.Revision, ConfigSHA256: snapshot.Registration.ConfigSHA256,
		RegistrationEvidenceID: snapshot.Registration.RegistrationEvidenceID, Audience: append([]string(nil), snapshot.Declaration.Audience...),
		RootTaskID: rootProof.TaskID, RootRunID: rootProof.RunID, RecipientTaskID: contextTask.TaskID,
		RecipientRunID: contextRun.RunID, SourceContextSHA256: contextSHA,
	}, nil
}

func (e *runtimeTaskMeetingOccurrenceExecutor) taskMeetingGenerationRequest(snapshot runtimeTaskMeetingOccurrenceSnapshot, meeting domainmeeting.Meeting, actorID string, purpose domaintask.TaskMeetingPurpose, requestID modulecore.RequestID, frozenInput []byte, intent domainagent.TaskMeetingGenerationIntent, contextTask domaintask.Task, contextRun domaintask.Run, shared domaintask.SharedRoleContext) (domaintask.TaskMeetingInvocationRequest, error) {
	timing, err := taskMeetingOccurrenceTaskTiming(snapshot.Declaration.Timing, meeting.ScheduledAt)
	if err != nil {
		return domaintask.TaskMeetingInvocationRequest{}, err
	}
	return domaintask.TaskMeetingInvocationRequest{
		OccurrenceKey: meeting.Key.String(), Purpose: purpose, CoordinatorActorID: snapshot.Declaration.CoordinatorActorID, ActorID: actorID,
		ConfiguredOwnerUserID: snapshot.Declaration.OwnerUserID, ConfigRevision: snapshot.Declaration.Revision,
		RegistrationEvidenceID: snapshot.Registration.RegistrationEvidenceID, FrozenConfig: append(json.RawMessage(nil), snapshot.Registration.FrozenDeclaration...),
		Audience: append([]string(nil), snapshot.Declaration.Audience...), FrozenInput: append(json.RawMessage(nil), frozenInput...),
		SourceContextSHA256: intent.SourceContextSHA256, FrozenSourceMessages: append([]domaintask.TaskMeetingConsumedMessage(nil), shared.TaskMeetingConsumedMessages...),
		GenerationScope: intent.Scope, GenerationRequestID: requestID, Timing: timing,
		ParentTaskID: contextTask.TaskID, ParentRunID: contextRun.RunID,
	}, nil
}

func (e *runtimeTaskMeetingOccurrenceExecutor) authorizeNewGenerationEffect(ctx context.Context, snapshot runtimeTaskMeetingOccurrenceSnapshot, actorID string, task domaintask.Task, run domaintask.Run, _ domaintask.SharedRoleContext, meeting domainmeeting.Meeting) error {
	if err := e.authorizeCurrentTaskMeetingEffect(snapshot); err != nil {
		return err
	}
	if ctx == nil || ctx.Err() != nil || task.TaskMeetingInvocation == nil || task.TaskMeetingInvocation.ActorID != actorID || task.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeReport && task.TaskMeetingInvocation.Purpose != domaintask.TaskMeetingPurposeAnswer ||
		task.TaskMeetingInvocation.OccurrenceKey != snapshot.Key.String() || task.TaskMeetingInvocation.ConfigSHA256 != snapshot.Registration.ConfigSHA256 ||
		run.Status != domaintask.RunStatusRunning || task.Status != domaintask.StatusRunning ||
		meeting.Status != domainmeeting.StatusReporting && meeting.Status != domainmeeting.StatusAnswering || meeting.ScopeEvidence == nil || meeting.ScopeEvidence.EvidenceID != task.TaskMeetingInvocation.GenerationScope.ScopeEvidenceID ||
		meeting.Generation != task.TaskMeetingInvocation.GenerationScope.Generation || meeting.Epoch != task.TaskMeetingInvocation.GenerationScope.Epoch {
		return domaintask.ErrTaskMeetingNotReady
	}
	if _, ok := e.agents[actorID]; !ok {
		return eventstore.ErrTaskMeetingForbidden
	}
	current, found, err := e.findCurrentOccurrence(ctx, snapshot)
	if err != nil || !found {
		return errors.Join(domaintask.ErrTaskMeetingNotReady, err)
	}
	turn := current.CurrentTurn
	now := e.clock().UTC()
	if now.Before(current.ScheduledAt) || !now.Before(current.DeadlineAt) || !current.Timing.Ready(now) ||
		(current.Status != domainmeeting.StatusReporting && current.Status != domainmeeting.StatusAnswering) || turn == nil || current.Guard() != meeting.Guard() ||
		current.Generation != task.TaskMeetingInvocation.GenerationScope.Generation || current.Epoch != task.TaskMeetingInvocation.GenerationScope.Epoch ||
		current.ScopeEvidence == nil || current.ScopeEvidence.EvidenceID != task.TaskMeetingInvocation.GenerationScope.ScopeEvidenceID ||
		turn.ActorID != actorID || turn.GenerationRequestID != string(task.TaskMeetingInvocation.GenerationRequestID) || turn.GenerationInputSHA256 == "" ||
		task.TaskMeetingInvocation.Purpose == domaintask.TaskMeetingPurposeReport && turn.Kind != domainmeeting.TurnReport ||
		task.TaskMeetingInvocation.Purpose == domaintask.TaskMeetingPurposeAnswer && turn.Kind != domainmeeting.TurnAnswer {
		return domaintask.ErrTaskMeetingNotReady
	}
	return nil
}

func (e *runtimeTaskMeetingOccurrenceExecutor) findCurrentOccurrence(ctx context.Context, snapshot runtimeTaskMeetingOccurrenceSnapshot) (domainmeeting.Meeting, bool, error) {
	store, ok := e.deps.workstreamStore.(interface {
		FindMeetingOccurrence(context.Context, domainmeeting.StoreAccess, domainmeeting.OccurrenceKey) (domainmeeting.Meeting, bool, error)
	})
	if !ok || store == nil {
		return domainmeeting.Meeting{}, false, meetingapp.ErrUnavailable
	}
	access, err := (runtimeTaskMeetingAuthority{snapshot: snapshot}).AuthorizeOccurrence(ctx, snapshot.Key, snapshot.Config)
	if err != nil {
		return domainmeeting.Meeting{}, false, err
	}
	return store.FindMeetingOccurrence(ctx, access, snapshot.Key)
}

func buildRuntimeTaskMeetingOutput(meeting domainmeeting.Meeting, task domaintask.Task, run domaintask.Run, response domainagent.TaskMeetingGeneratedResponse, purpose domaintask.TaskMeetingPurpose, concernID *domainmeeting.ConcernID, now time.Time) (runtimeTaskMeetingOutput, error) {
	if now.IsZero() || meeting.CurrentTurn == nil || task.TaskMeetingInvocation == nil || task.TaskMeetingInvocation.Purpose != purpose || run.TaskID != task.TaskID || run.Status != domaintask.RunStatusRunning || meeting.SharedThreadID.Validate() != nil {
		return runtimeTaskMeetingOutput{}, domaintask.ErrTaskMeetingConflict
	}
	messageID := modulecore.NewMessageID()
	evidence := domainmeeting.SpeakerEvidence{
		EvidenceID: modulecore.NewEvidenceID(), Key: meeting.Key, Generation: meeting.CurrentTurn.Generation, Epoch: meeting.CurrentTurn.Epoch,
		ScopeEvidenceID: meeting.CurrentTurn.ScopeEvidenceID, ThreadID: meeting.SharedThreadID, MessageID: messageID,
		ActorID: task.Assignee, ObservedAt: now.UTC(), GenerationRequestID: taskMeetingInvocationRequestString(task),
		GenerationInputSHA256: meeting.CurrentTurn.GenerationInputSHA256, TaskRef: task.TaskID, RunRef: run.RunID,
		TaskAssignee: task.Assignee, AdmissionEvidenceID: task.TaskMeetingInvocation.AdmissionEvidenceID,
	}
	output := runtimeTaskMeetingOutput{Schema: runtimeTaskMeetingOutputSchema, Message: messageID, Evidence: evidence}
	switch purpose {
	case domaintask.TaskMeetingPurposeReport:
		generated := response.Report
		if generated == nil || response.Answer != nil {
			return runtimeTaskMeetingOutput{}, domaintask.ErrTaskMeetingConflict
		}
		publishedBody, bodyErr := runtimeTaskMeetingReportBody(generated)
		if bodyErr != nil {
			return runtimeTaskMeetingOutput{}, bodyErr
		}
		evidence.Body = publishedBody
		concerns := make([]domainmeeting.Concern, 0, len(generated.Concerns))
		for _, concern := range generated.Concerns {
			concerns = append(concerns, domainmeeting.Concern{Description: concern})
		}
		output.Report = &domainmeeting.Report{
			Utterance:          domainmeeting.Utterance{MessageID: messageID, ActorID: task.Assignee, Body: publishedBody},
			Current:            domainmeeting.ReportField{State: generated.Current.State, Text: generated.Current.Text, TaskRef: generated.Current.TaskRef},
			Next:               domainmeeting.ReportField{State: generated.Next.State, Text: generated.Next.Text, TaskRef: generated.Next.TaskRef},
			ConcernDeclaration: generated.ConcernDeclaration, Concerns: concerns, SpeakerEvidenceID: evidence.EvidenceID,
		}
	case domaintask.TaskMeetingPurposeAnswer:
		if response.Answer == nil || response.Report != nil || concernID == nil {
			return runtimeTaskMeetingOutput{}, domaintask.ErrTaskMeetingConflict
		}
		reporter, found := taskMeetingReporter(meeting, *concernID)
		if !found {
			return runtimeTaskMeetingOutput{}, domaintask.ErrTaskMeetingConflict
		}
		concernDescription, descriptionErr := taskMeetingConcernDescription(meeting, *concernID)
		if descriptionErr != nil {
			return runtimeTaskMeetingOutput{}, descriptionErr
		}
		publishedBody := "Concern: " + concernDescription + "\nAnswer type: " + response.Answer.Kind + "\nAnswer: " + response.Answer.Body
		if !validTaskMeetingPublishedBody(publishedBody) {
			return runtimeTaskMeetingOutput{}, domaintask.ErrTaskMeetingConflict
		}
		evidence.Body = publishedBody
		output.Answer = &domainmeeting.Answer{
			Utterance: domainmeeting.Utterance{MessageID: messageID, ActorID: task.Assignee, Body: publishedBody},
			ConcernID: *concernID, ReporterID: reporter, Kind: response.Answer.Kind, SpeakerEvidenceID: evidence.EvidenceID,
		}
	default:
		return runtimeTaskMeetingOutput{}, domaintask.ErrTaskMeetingConflict
	}
	// Evidence is copied into the output before the purpose-specific body is
	// assigned above. Refresh the value so both the typed claim and utterance
	// prove the exact same generated body.
	output.Evidence = evidence
	return output, nil
}

func runtimeTaskMeetingReportBody(generated *domainagent.TaskMeetingGeneratedReport) (string, error) {
	if generated == nil {
		return "", domaintask.ErrTaskMeetingConflict
	}
	parts := []string{
		strings.TrimSpace(generated.Body),
		"Current: " + runtimeTaskMeetingReportField(generated.Current),
		"Next: " + runtimeTaskMeetingReportField(generated.Next),
	}
	switch generated.ConcernDeclaration {
	case domainmeeting.ConcernsNone:
		parts = append(parts, "Concerns: none")
	case domainmeeting.ConcernsNotDisclosed:
		parts = append(parts, "Concerns: not disclosed")
	case domainmeeting.ConcernsListed:
		if len(generated.Concerns) == 0 {
			return "", domaintask.ErrTaskMeetingConflict
		}
		parts = append(parts, "Concerns:")
		for _, concern := range generated.Concerns {
			parts = append(parts, "- "+strings.TrimSpace(concern))
		}
	default:
		return "", domaintask.ErrTaskMeetingConflict
	}
	body := strings.Join(parts, "\n")
	if !validTaskMeetingPublishedBody(body) {
		return "", domaintask.ErrTaskMeetingConflict
	}
	return body, nil
}

func runtimeTaskMeetingReportField(field domainagent.TaskMeetingGeneratedReportField) string {
	state := strings.TrimSpace(field.State)
	text := strings.TrimSpace(field.Text)
	if text == "" {
		return state
	}
	return state + " — " + text
}

func validTaskMeetingPublishedBody(body string) bool {
	return strings.TrimSpace(body) != "" && len(body) <= domainagent.TaskMeetingMaxMessageBytes && !strings.ContainsRune(body, '\x00')
}

func taskMeetingReporter(meeting domainmeeting.Meeting, concern domainmeeting.ConcernID) (string, bool) {
	for _, report := range meeting.Reports {
		if report.Utterance.MessageID == concern.ReportMessageID && concern.Ordinal > 0 && int(concern.Ordinal) <= len(report.Concerns) {
			return report.Utterance.ActorID, true
		}
	}
	return "", false
}

func taskMeetingInvocationRequestString(task domaintask.Task) string {
	if task.TaskMeetingInvocation == nil {
		return ""
	}
	return string(task.TaskMeetingInvocation.GenerationRequestID)
}

func (e *runtimeTaskMeetingOccurrenceExecutor) readFinishedGeneration(ctx context.Context, meeting domainmeeting.Meeting, task domaintask.Task, run domaintask.Run, _ domainagent.TaskMeetingPreparedGeneration, authorizeState, authorizeSource domaintask.TaskMeetingReadAuthorizationCheck) (runtimeTaskMeetingOutput, domainmeeting.SpeakerEvidence, error) {
	body, reference, err := e.deps.taskManager.ReadTaskMeetingGenerationResult(ctx, task.TaskID, run.RunID, authorizeState, authorizeSource)
	if err != nil {
		return runtimeTaskMeetingOutput{}, domainmeeting.SpeakerEvidence{}, err
	}
	var output runtimeTaskMeetingOutput
	if err := json.Unmarshal(body, &output); err != nil || output.Schema != runtimeTaskMeetingOutputSchema || output.Message.Validate() != nil || output.Evidence.MessageID != output.Message || output.Evidence.TaskRef != task.TaskID || output.Evidence.RunRef != run.RunID || output.Evidence.ActorID != task.Assignee || output.Evidence.GenerationRequestID != string(task.TaskMeetingInvocation.GenerationRequestID) {
		return runtimeTaskMeetingOutput{}, domainmeeting.SpeakerEvidence{}, domaintask.ErrTaskMeetingConflict
	}
	verify := func(_ context.Context, actualTask domaintask.Task, actualRun domaintask.Run, _ domaintask.SharedRoleContext, actual domaintask.TaskMeetingGenerationResultReference) error {
		if actual != reference || actual.ValidateRun(actualTask, actualRun) != nil || actualTask.TaskID != task.TaskID || actualRun.RunID != run.RunID {
			return domaintask.ErrTaskMeetingConflict
		}
		return nil
	}
	if err := e.deps.taskManager.FinishTaskMeetingInvocation(ctx, task.TaskID, run.RunID, reference, domaintask.TaskMeetingStateAuthorizationCheck(authorizeState), verify); err != nil {
		return runtimeTaskMeetingOutput{}, domainmeeting.SpeakerEvidence{}, err
	}
	if output.Evidence.Key != meeting.Key || output.Evidence.ActorID != meeting.CurrentTurn.ActorID || output.Evidence.GenerationRequestID != meeting.CurrentTurn.GenerationRequestID || output.Evidence.GenerationInputSHA256 != meeting.CurrentTurn.GenerationInputSHA256 {
		return runtimeTaskMeetingOutput{}, domainmeeting.SpeakerEvidence{}, domaintask.ErrTaskMeetingConflict
	}
	return output, output.Evidence, nil
}

func containsStringExact(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func (e *runtimeTaskMeetingOccurrenceExecutor) worldPresence(snapshot runtimeTaskMeetingOccurrenceSnapshot, root domaintask.Task, run domaintask.Run, authorize domaintask.TaskMeetingReadAuthorizationCheck, allowMissingReader bool) meetingapp.WorldPresencePort {
	declaration := snapshot.Declaration
	var snapshotReader worldmeeting.TaskMeetingWorldSnapshotReader = e.worldSnapshotReader
	if snapshotReader == nil && !allowMissingReader {
		reader, err := newRuntimeTaskMeetingPinnedWorldReader(e.cfg, declaration)
		if err != nil {
			return unavailableTaskMeetingWorld{cause: err}
		}
		snapshotReader = reader
	}
	store, ok := e.deps.canonicalEventStore.(*eventstore.SQLiteStore)
	if !ok || store == nil {
		return unavailableTaskMeetingWorld{cause: meetingapp.ErrUnavailable}
	}
	taskReader, readerOK := e.deps.taskStore.(domaintask.TaskMeetingInvocationReader)
	if !readerOK || taskReader == nil {
		return unavailableTaskMeetingWorld{cause: meetingapp.ErrUnavailable}
	}
	gatherClientFactory := e.worldGatherClientFactory
	if gatherClientFactory == nil {
		gatherClientFactory = func() (worldmeeting.TaskMeetingWorldGatherClient, error) {
			if e.cfg == nil {
				return nil, meetingapp.ErrUnavailable
			}
			return worldmeeting.NewConfiguredWorldGatherClient(declaration, e.cfg.Heartbeat.Routine.WorldLifeTokenFile,
				worldProjectionDuration(e.cfg.WorldProjection.TimeoutSeconds, defaultWorldProjectionTimeout))
		}
	}
	return &worldmeeting.WorldPresenceOwner{SnapshotReader: snapshotReader, GatherRequired: true,
		GatherClientFactory: gatherClientFactory,
		EventStore:          store, TaskReader: taskReader,
		AuthorizeRead: authorize, CredentialEnv: declaration.WorldCredentialEnv, Now: e.clock,
		AuthorizeGather: func(ctx context.Context, task domaintask.Task, actualRun domaintask.Run, _ domaintask.SharedRoleContext, intent domainmeeting.WorldGatherCommandIntent) error {
			if err := e.authorizeCurrentTaskMeetingEffect(snapshot); err != nil {
				return err
			}
			if task.TaskID != root.TaskID || actualRun.RunID != run.RunID || actualRun.Status != domaintask.RunStatusRunning ||
				intent.Scope.TaskID != root.TaskID || intent.Scope.RunID != run.RunID || intent.Scope.OwnerUserID != declaration.OwnerUserID ||
				intent.Scope.ConfigSHA256 != snapshot.Registration.ConfigSHA256 || intent.Scope.RegistrationEvidenceID != snapshot.Registration.RegistrationEvidenceID {
				return eventstore.ErrTaskMeetingForbidden
			}
			return nil
		},
		AuthorizeObserve: func(ctx context.Context, task domaintask.Task, actualRun domaintask.Run, _ domaintask.SharedRoleContext, request domainmeeting.WorldObservationRequest) error {
			if err := e.authorizeCurrentTaskMeetingEffect(snapshot); err != nil {
				return err
			}
			if task.TaskID != root.TaskID || actualRun.RunID != run.RunID || request.OwnerUserID != declaration.OwnerUserID || request.ConfigSHA256 != snapshot.Registration.ConfigSHA256 {
				return eventstore.ErrTaskMeetingForbidden
			}
			return nil
		}}
}

// newRuntimeTaskMeetingPinnedWorldReader keeps the source-level World client
// inside the approved adapter boundary. The frozen declaration is copied into
// a narrow config projection; credentials are resolved lazily by the adapter
// only when a new snapshot is actually required.
func newRuntimeTaskMeetingPinnedWorldReader(cfg *config.Config, declaration domainmeeting.TaskMeetingDeclaration) (worldmeeting.TaskMeetingWorldSnapshotScopeReader, error) {
	if cfg == nil {
		return nil, meetingapp.ErrUnavailable
	}
	worldConfig := *cfg
	worldConfig.WorldProjection = config.WorldProjectionConfig{
		BaseURL: declaration.WorldBaseURL, TokenEnv: declaration.WorldCredentialEnv,
		WorldID: declaration.WorldID, TimelineID: declaration.TimelineID, TimelineEpoch: declaration.TimelineEpoch,
		Actors: runtimeTaskMeetingWorldProjectionActors(declaration.WorldActorBindings),
	}
	return worldmeeting.NewPinnedWorldSnapshotReader(declaration, func() (worldmeeting.TaskMeetingScopedWorldSnapshotClient, error) {
		return worldmeeting.NewConfiguredWorldSnapshotClient(
			worldConfig.WorldProjection,
			worldProjectionDuration(worldConfig.WorldProjection.TimeoutSeconds, defaultWorldProjectionTimeout),
		)
	})
}

func (e *runtimeTaskMeetingOccurrenceExecutor) closeTaskMeetingControls(ctx context.Context, snapshot runtimeTaskMeetingOccurrenceSnapshot, meeting domainmeeting.Meeting, proof domaintask.TaskMeetingRootProof, authorizeReceipt domaintask.TaskMeetingInvocationOperationAuthorizer, authorize domaintask.TaskMeetingReadAuthorizationCheck) error {
	if meeting.Key != snapshot.Key || meeting.Status != domainmeeting.StatusCompleted && meeting.Status != domainmeeting.StatusPartial && meeting.Status != domainmeeting.StatusMissed {
		return domaintask.ErrTaskMeetingConflict
	}
	if meeting.Status == domainmeeting.StatusCompleted && meeting.FinishEvidence == nil || meeting.Status != domainmeeting.StatusCompleted && (meeting.PartialEvidence == nil || meeting.PartialMinutes == nil) {
		return domaintask.ErrTaskMeetingConflict
	}
	if meeting.Status == domainmeeting.StatusPartial || meeting.Status == domainmeeting.StatusMissed {
		if err := e.settleTaskMeetingGatherBeforeClose(ctx, snapshot, meeting, proof, authorize); err != nil {
			return err
		}
	}
	verify := func(_ context.Context, task domaintask.Task, run domaintask.Run, _ domaintask.SharedRoleContext, outcome domaintask.Status) error {
		inv := task.TaskMeetingInvocation
		if inv == nil || inv.OccurrenceKey != snapshot.Key.String() || inv.ConfigSHA256 != snapshot.Registration.ConfigSHA256 ||
			inv.RegistrationEvidenceID != snapshot.Registration.RegistrationEvidenceID || inv.ConfiguredOwnerUserID != snapshot.Declaration.OwnerUserID ||
			run.TaskID != task.TaskID || run.RunID == "" || (meeting.Status != domainmeeting.StatusCompleted && meeting.Status != domainmeeting.StatusPartial && meeting.Status != domainmeeting.StatusMissed) {
			return domaintask.ErrTaskMeetingConflict
		}
		if outcome == domaintask.StatusSucceeded && meeting.Status != domainmeeting.StatusCompleted || outcome == domaintask.StatusFailed && meeting.Status == domainmeeting.StatusCompleted {
			return domaintask.ErrTaskMeetingConflict
		}
		return nil
	}
	for _, actorID := range snapshot.Declaration.Audience {
		contextTask, contextRun, _, err := e.deps.taskManager.FindTaskMeetingRecipientContextByActor(ctx, proof, actorID, authorize, authorize, authorizeReceipt)
		if err != nil {
			return err
		}
		if meeting.Status == domainmeeting.StatusCompleted {
			err = e.deps.taskManager.CompleteTaskMeetingRecipientContext(ctx, contextTask.TaskID, contextRun.RunID, authorize, verify)
		} else {
			err = e.deps.taskManager.FailTaskMeetingRecipientContext(ctx, contextTask.TaskID, contextRun.RunID, authorize, verify)
		}
		if err != nil {
			return err
		}
	}
	if meeting.Status == domainmeeting.StatusCompleted {
		return e.deps.taskManager.CompleteTaskMeetingRootInvocation(ctx, proof.TaskID, proof.RunID, authorize, verify)
	}
	return e.deps.taskManager.FailTaskMeetingRootInvocation(ctx, proof.TaskID, proof.RunID, authorize, verify)
}

func (e *runtimeTaskMeetingOccurrenceExecutor) settleTaskMeetingGatherBeforeClose(
	ctx context.Context,
	snapshot runtimeTaskMeetingOccurrenceSnapshot,
	meeting domainmeeting.Meeting,
	proof domaintask.TaskMeetingRootProof,
	authorize domaintask.TaskMeetingReadAuthorizationCheck,
) error {
	reader, ok := e.deps.taskStore.(domaintask.TaskMeetingInvocationReader)
	if !ok || reader == nil {
		return meetingapp.ErrUnavailable
	}
	root, rootRun, _, err := reader.GetTaskMeetingInvocationState(ctx, proof.TaskID, proof.RunID, authorize)
	if err != nil {
		return err
	}
	store, ok := e.deps.canonicalEventStore.(*eventstore.SQLiteStore)
	if !ok || store == nil {
		return meetingapp.ErrUnavailable
	}
	session, found, err := store.FindTaskMeetingGroupSessionForRoot(ctx, snapshot.Key.String(), root.TaskID, rootRun.RunID, reader, authorize)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if session.ConfigSHA256 != snapshot.Registration.ConfigSHA256 || session.RegistrationEvidence != snapshot.Registration.RegistrationEvidenceID {
		return eventstore.ErrTaskMeetingUnavailable
	}
	owner, ok := e.worldPresence(snapshot, root, rootRun, authorize, true).(*worldmeeting.WorldPresenceOwner)
	if !ok {
		return meetingapp.ErrUnavailable
	}
	request := domainmeeting.WorldObservationRequest{
		Key: meeting.Key, TaskID: root.TaskID, RunID: rootRun.RunID, ConfigRevision: meeting.ScheduleRevision,
		ConfigSHA256: snapshot.Registration.ConfigSHA256, OwnerUserID: snapshot.Declaration.OwnerUserID,
		RegistrationEvidenceID: snapshot.Registration.RegistrationEvidenceID, CoordinatorActorID: snapshot.Declaration.CoordinatorActorID,
		Session: session.SessionID, Thread: session.ThreadID, Generation: meeting.Generation, Epoch: meeting.Epoch,
		Audience:      append([]string(nil), snapshot.Declaration.Audience...),
		ActorBindings: append([]domainmeeting.WorldActorBinding(nil), snapshot.Declaration.WorldActorBindings...), Rooms: snapshot.Declaration.Rooms,
	}
	return owner.SettlePendingGather(ctx, request, snapshot.Declaration)
}

type unavailableTaskMeetingWorld struct{ cause error }

func (w unavailableTaskMeetingWorld) GatherAndRead(context.Context, domainmeeting.Meeting, string, modulecore.ThreadID) (domainmeeting.WorldScopeEvidence, []domainmeeting.PresenceEvidence, error) {
	return domainmeeting.WorldScopeEvidence{}, nil, errors.Join(meetingapp.ErrUnavailable, w.cause)
}

func worldActorIDs(bindings []domainmeeting.WorldActorBinding) []string {
	ids := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		ids = append(ids, binding.WorldActorID)
	}
	return ids
}

func runtimeTaskMeetingWorldProjectionActors(bindings []domainmeeting.WorldActorBinding) []config.WorldProjectionActorConfig {
	actors := make([]config.WorldProjectionActorConfig, 0, len(bindings))
	for _, binding := range bindings {
		actors = append(actors, config.WorldProjectionActorConfig{ActorID: binding.WorldActorID})
	}
	return actors
}

func runtimeTaskMeetingDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func taskMeetingOccurrenceTaskTiming(scheduleTiming modulecore.TaskTiming, due time.Time) (modulecore.TaskTiming, error) {
	if due.IsZero() || scheduleTiming.Validate() != nil {
		return modulecore.TaskTiming{}, domaintask.ErrTaskMeetingConflict
	}
	start := due
	timing := scheduleTiming
	timing.ScheduledStartAt = &start
	timing.Recurrence = &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceNone, Times: []string{}, Weekdays: []time.Weekday{}}
	if err := timing.Validate(); err != nil {
		return modulecore.TaskTiming{}, err
	}
	return timing, nil
}
