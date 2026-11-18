package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	taskmeetingapp "github.com/Nyukimin/RenCrow_CORE/internal/application/taskmeeting"
	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type taskMeetingOccurrenceRegistrationOwner interface {
	taskMeetingScheduleRegistrationOwner
	FindTaskMeetingScheduleRegistration(
		context.Context,
		string,
		string,
		uint64,
		eventstore.TaskMeetingRegistrationReadAuthorizationCheck,
	) (eventstore.TaskMeetingScheduleRegistrationReceipt, error)
	PinTaskMeetingOccurrenceRegistration(
		context.Context,
		eventstore.TaskMeetingScheduleRegistrationReceipt,
		domainmeeting.OccurrenceKey,
		eventstore.TaskMeetingOccurrenceRegistrationAuthorizationCheck,
	) (eventstore.TaskMeetingOccurrenceRegistrationReceipt, error)
	FindTaskMeetingOccurrenceRegistration(
		context.Context,
		domainmeeting.OccurrenceKey,
		string,
		eventstore.TaskMeetingOccurrenceRegistrationReadAuthorizationCheck,
	) (eventstore.TaskMeetingOccurrenceRegistrationReceipt, bool, error)
}

// runtimeTaskMeetingOccurrenceSnapshot is the exact owner config pinned to a
// scheduler slot. It carries the declaration needed by World and Task owners;
// Config only contains the domain's validated meeting snapshot.
type runtimeTaskMeetingOccurrenceSnapshot struct {
	Key          domainmeeting.OccurrenceKey
	Pin          eventstore.TaskMeetingOccurrenceRegistrationReceipt
	Registration eventstore.TaskMeetingScheduleRegistrationReceipt
	Declaration  domainmeeting.TaskMeetingDeclaration
	Config       domainmeeting.ConfigSnapshot
}

// resolveRuntimeTaskMeetingOccurrence reads a durable occurrence pin before it
// derives or writes the current declaration. Only an exact NotFound permits a
// new current-revision pin. Ambiguous writes are reconciled with a read.
func resolveRuntimeTaskMeetingOccurrence(
	ctx context.Context,
	cfg *config.Config,
	deps *Dependencies,
	schedule domainscheduler.Schedule,
	occurrence domainscheduler.ScheduledOccurrence,
	agents agentRuntime,
	coders ...*coderAdapter,
) (runtimeTaskMeetingOccurrenceSnapshot, error) {
	if ctx == nil || cfg == nil || deps == nil {
		return runtimeTaskMeetingOccurrenceSnapshot{}, errors.New("TaskMeeting occurrence runtime is unavailable")
	}
	if err := validateTaskMeetingScheduledOccurrence(schedule, occurrence); err != nil {
		return runtimeTaskMeetingOccurrenceSnapshot{}, err
	}
	owner, ok := deps.canonicalEventStore.(taskMeetingOccurrenceRegistrationOwner)
	if !ok || nilRuntimeOwner(owner) {
		return runtimeTaskMeetingOccurrenceSnapshot{}, errors.New("TaskMeeting occurrence registration owner is unavailable")
	}
	key, err := taskMeetingOccurrenceKey(schedule, occurrence)
	if err != nil {
		return runtimeTaskMeetingOccurrenceSnapshot{}, err
	}
	pin, found, err := findRuntimeTaskMeetingOccurrencePin(ctx, cfg, deps, owner, key)
	if err != nil && !errors.Is(err, domaintask.ErrTaskMeetingNotFound) {
		return runtimeTaskMeetingOccurrenceSnapshot{}, err
	}
	if !found {
		if enabled, reason := taskMeetingEffectivelyEnabled(cfg); !enabled {
			return runtimeTaskMeetingOccurrenceSnapshot{}, fmt.Errorf("TaskMeeting occurrence execution is blocked: %s", reason)
		}
		declaration, declarationErr := taskMeetingDeclarationFromRuntimeConfig(cfg)
		if declarationErr != nil {
			return runtimeTaskMeetingOccurrenceSnapshot{}, declarationErr
		}
		canonical, digest, canonicalErr := domainmeeting.CanonicalTaskMeetingDeclarationJSON(declaration)
		if canonicalErr != nil {
			return runtimeTaskMeetingOccurrenceSnapshot{}, canonicalErr
		}
		if !sameTaskMeetingTiming(declaration.Timing, occurrence.Timing) || !declaration.Timing.Ready(occurrence.ScheduledAt) {
			return runtimeTaskMeetingOccurrenceSnapshot{}, errors.New("current TaskMeeting declaration does not own this due slot")
		}
		if !taskMeetingRuntimeBindingsReady(cfg, agents, coders...) {
			return runtimeTaskMeetingOccurrenceSnapshot{}, errors.New("TaskMeeting actor bindings are not currently registered")
		}
		currentAuthorization := func(authCtx context.Context, requested domainmeeting.TaskMeetingDeclaration) error {
			if err := authCtx.Err(); err != nil {
				return err
			}
			if !cfg.Heartbeat.Enabled || !cfg.Heartbeat.Routine.DailyReportMeetings.Enabled || !cfg.LocalAgentOps.Enabled ||
				strings.TrimSpace(cfg.LocalAgentOps.UserID) != declaration.OwnerUserID || deps.taskCenterUserID != declaration.OwnerUserID || len(deps.taskCenterUserToken) == 0 {
				return errors.New("TaskMeeting owner is not currently authorized for occurrence pinning")
			}
			requestedBytes, requestedHash, err := domainmeeting.CanonicalTaskMeetingDeclarationJSON(requested)
			if err != nil || requestedHash != digest || !bytes.Equal(requestedBytes, canonical) {
				return errors.New("TaskMeeting occurrence declaration does not match current owner config")
			}
			if !taskMeetingRuntimeBindingsReady(cfg, agents, coders...) {
				return errors.New("TaskMeeting actor bindings are not currently registered")
			}
			return nil
		}
		registration, registerErr := owner.EnsureTaskMeetingScheduleRegistration(ctx, eventstore.TaskMeetingRegistrationRequest{
			Declaration: declaration, FrozenDeclaration: append([]byte(nil), canonical...),
		}, currentAuthorization)
		if registerErr != nil {
			return runtimeTaskMeetingOccurrenceSnapshot{}, registerErr
		}
		if err := validateTaskMeetingRegistrationReceipt(registration, &declaration, digest); err != nil {
			return runtimeTaskMeetingOccurrenceSnapshot{}, err
		}
		pinAuthorization := func(authCtx context.Context, requested eventstore.TaskMeetingScheduleRegistrationReceipt, requestedKey domainmeeting.OccurrenceKey) error {
			if err := authCtx.Err(); err != nil {
				return err
			}
			if requestedKey != key || requested.OwnerUserID != declaration.OwnerUserID || requested.Revision != declaration.Revision ||
				requested.ConfigSHA256 != digest || !bytes.Equal(requested.FrozenDeclaration, canonical) {
				return errors.New("TaskMeeting occurrence pin does not match current owner declaration")
			}
			return currentAuthorization(authCtx, declaration)
		}
		pin, err = owner.PinTaskMeetingOccurrenceRegistration(ctx, registration, key, pinAuthorization)
		if err != nil {
			pinErr := err
			// The append may have committed despite a lost reply. Read the exact
			// key before deciding the occurrence remains unknown.
			pin, found, err = findRuntimeTaskMeetingOccurrencePin(ctx, cfg, deps, owner, key)
			if err != nil || !found {
				return runtimeTaskMeetingOccurrenceSnapshot{}, errors.Join(pinErr, err)
			}
		}
		if err := validateRuntimeTaskMeetingOccurrencePin(pin, key, declaration.OwnerUserID); err != nil {
			return runtimeTaskMeetingOccurrenceSnapshot{}, err
		}
	}
	if pin.OwnerUserID == "" {
		return runtimeTaskMeetingOccurrenceSnapshot{}, errors.New("TaskMeeting occurrence pin has no owner")
	}
	if !cfg.LocalAgentOps.Enabled || strings.TrimSpace(cfg.LocalAgentOps.UserID) != pin.OwnerUserID ||
		strings.TrimSpace(cfg.Heartbeat.Routine.DailyReportMeetings.OwnerUserID) != pin.OwnerUserID ||
		deps.taskCenterUserID != pin.OwnerUserID || len(deps.taskCenterUserToken) == 0 {
		return runtimeTaskMeetingOccurrenceSnapshot{}, errors.New("TaskMeeting current owner execution authority is unavailable")
	}
	registration, declaration, err := readRuntimeTaskMeetingRegistrationByPin(ctx, cfg, deps, owner, pin)
	if err != nil {
		return runtimeTaskMeetingOccurrenceSnapshot{}, err
	}
	meetingConfig := runtimeTaskMeetingConfigSnapshot(registration, declaration, cfg, agents, coders...)
	if _, err := domainmeeting.NewOccurrence(meetingConfig, key); err != nil {
		return runtimeTaskMeetingOccurrenceSnapshot{}, fmt.Errorf("pinned TaskMeeting declaration is not ready for this slot: %w", err)
	}
	return runtimeTaskMeetingOccurrenceSnapshot{Key: key, Pin: pin, Registration: registration, Declaration: declaration, Config: meetingConfig}, nil
}

func findRuntimeTaskMeetingOccurrencePin(
	ctx context.Context,
	cfg *config.Config,
	deps *Dependencies,
	owner taskMeetingOccurrenceRegistrationOwner,
	key domainmeeting.OccurrenceKey,
) (eventstore.TaskMeetingOccurrenceRegistrationReceipt, bool, error) {
	if ctx == nil || cfg == nil || deps == nil || nilRuntimeOwner(owner) {
		return eventstore.TaskMeetingOccurrenceRegistrationReceipt{}, false, errors.New("TaskMeeting occurrence read is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return eventstore.TaskMeetingOccurrenceRegistrationReceipt{}, false, err
	}
	ownerIDs := taskMeetingConfiguredOwnerUserIDs(cfg)
	if len(ownerIDs) == 0 {
		return eventstore.TaskMeetingOccurrenceRegistrationReceipt{}, false, errors.New("TaskMeeting occurrence owner identity is unavailable")
	}
	var forbidden error
	for _, ownerUserID := range ownerIDs {
		pin, found, err := owner.FindTaskMeetingOccurrenceRegistration(
			ctx,
			key,
			ownerUserID,
			func(readCtx context.Context, authorizedOwnerID, occurrenceKey string) error {
				if occurrenceKey != key.String() {
					return errors.New("TaskMeeting occurrence read key changed")
				}
				return authorizeRuntimeTaskMeetingRegistrationRead(readCtx, cfg, deps, authorizedOwnerID, domainmeeting.TaskMeetingRegistrationKey)
			},
		)
		if errors.Is(err, domaintask.ErrTaskMeetingNotFound) {
			continue
		}
		if errors.Is(err, eventstore.ErrTaskMeetingForbidden) {
			forbidden = err
			continue
		}
		if err != nil {
			return eventstore.TaskMeetingOccurrenceRegistrationReceipt{}, false, err
		}
		if !found {
			continue
		}
		if err := validateRuntimeTaskMeetingOccurrencePin(pin, key, ownerUserID); err != nil {
			return eventstore.TaskMeetingOccurrenceRegistrationReceipt{}, false, err
		}
		return pin, true, nil
	}
	if forbidden != nil {
		return eventstore.TaskMeetingOccurrenceRegistrationReceipt{}, false, forbidden
	}
	return eventstore.TaskMeetingOccurrenceRegistrationReceipt{}, false, domaintask.ErrTaskMeetingNotFound
}

func readRuntimeTaskMeetingRegistrationByPin(
	ctx context.Context,
	cfg *config.Config,
	deps *Dependencies,
	owner taskMeetingOccurrenceRegistrationOwner,
	pin eventstore.TaskMeetingOccurrenceRegistrationReceipt,
) (eventstore.TaskMeetingScheduleRegistrationReceipt, domainmeeting.TaskMeetingDeclaration, error) {
	registration, err := owner.FindTaskMeetingScheduleRegistration(
		ctx,
		domainmeeting.TaskMeetingRegistrationKey,
		pin.OwnerUserID,
		pin.ConfigRevision,
		func(readCtx context.Context, ownerUserID, registrationKey string, revision uint64) error {
			if registrationKey != domainmeeting.TaskMeetingRegistrationKey || revision != pin.ConfigRevision {
				return errors.New("TaskMeeting historical registration read identity changed")
			}
			return authorizeRuntimeTaskMeetingRegistrationRead(readCtx, cfg, deps, ownerUserID, registrationKey)
		},
	)
	if err != nil {
		return eventstore.TaskMeetingScheduleRegistrationReceipt{}, domainmeeting.TaskMeetingDeclaration{}, err
	}
	if err := validateTaskMeetingRegistrationReceipt(registration, nil, ""); err != nil {
		return eventstore.TaskMeetingScheduleRegistrationReceipt{}, domainmeeting.TaskMeetingDeclaration{}, err
	}
	if registration.ScheduleID != pin.ScheduleID || registration.OwnerUserID != pin.OwnerUserID || registration.Revision != pin.ConfigRevision ||
		registration.ConfigSHA256 != pin.ConfigSHA256 || registration.RegistrationEvidenceID != pin.RegistrationEvidenceID || registration.EventID != pin.RegistrationEventID {
		return eventstore.TaskMeetingScheduleRegistrationReceipt{}, domainmeeting.TaskMeetingDeclaration{}, domaintask.ErrTaskMeetingConflict
	}
	declaration, digest, err := domainmeeting.DecodeCanonicalTaskMeetingDeclaration(registration.FrozenDeclaration)
	if err != nil || digest != pin.ConfigSHA256 {
		return eventstore.TaskMeetingScheduleRegistrationReceipt{}, domainmeeting.TaskMeetingDeclaration{}, domaintask.ErrTaskMeetingConflict
	}
	return registration, declaration, nil
}

func runtimeTaskMeetingConfigSnapshot(
	registration eventstore.TaskMeetingScheduleRegistrationReceipt,
	declaration domainmeeting.TaskMeetingDeclaration,
	cfg *config.Config,
	agents agentRuntime,
	coders ...*coderAdapter,
) domainmeeting.ConfigSnapshot {
	registeredActors := registeredRuntimeTaskMeetingActors(cfg, agents, coders...)
	registered := make([]string, 0, len(declaration.Audience))
	for _, actorID := range declaration.Audience {
		if registeredActors[actorID] {
			registered = append(registered, actorID)
		}
	}
	return domainmeeting.ConfigSnapshot{
		ScheduleID: registration.ScheduleID, Revision: declaration.Revision, Timing: declaration.Timing,
		ConfiguredActors: append([]string(nil), declaration.Audience...), WorldActorBindings: append([]domainmeeting.WorldActorBinding(nil), declaration.WorldActorBindings...),
		RegisteredActors: registered, RegistrationEvidenceID: registration.RegistrationEvidenceID,
		WorldBaseURL: declaration.WorldBaseURL, WorldCredentialEnv: declaration.WorldCredentialEnv,
		WorldID: declaration.WorldID, TimelineID: declaration.TimelineID, TimelineEpoch: declaration.TimelineEpoch,
		Rooms: declaration.Rooms, MeetingDeadlineAfterSecs: declaration.MeetingDeadlineAfterSecs,
	}
}

func taskMeetingOccurrenceKey(schedule domainscheduler.Schedule, occurrence domainscheduler.ScheduledOccurrence) (domainmeeting.OccurrenceKey, error) {
	if err := validateTaskMeetingScheduledOccurrence(schedule, occurrence); err != nil {
		return domainmeeting.OccurrenceKey{}, err
	}
	return taskmeetingapp.OccurrenceKeyFromScheduled(schedule, occurrence)
}

// taskMeetingPendingProjectionTiming preserves the exact timing snapshot for a
// durably pinned retry. A pending but unpinned slot may use the current
// declaration because no meeting-side effect has been admitted for it yet.
func taskMeetingPendingProjectionTiming(
	ctx context.Context,
	cfg *config.Config,
	deps *Dependencies,
	ownerScheduleID modulecore.ScheduleID,
	current modulecore.TaskTiming,
) (modulecore.TaskTiming, error) {
	if ctx == nil || ownerScheduleID == "" || cfg == nil || deps == nil || deps.schedulerStore == nil {
		return modulecore.TaskTiming{}, errors.New("TaskMeeting pending schedule lookup is unavailable")
	}
	exact, ok := deps.schedulerStore.(taskMeetingExactScheduleReader)
	if !ok {
		return modulecore.TaskTiming{}, errors.New("TaskMeeting pending schedule requires exact identity lookup")
	}
	schedule, found, err := exact.FindScheduleByID(ctx, string(ownerScheduleID))
	if err != nil {
		return modulecore.TaskTiming{}, err
	}
	if !found || schedule.ScheduleID != ownerScheduleID || !isTaskMeetingSchedule(schedule) {
		return current, nil
	}
	if schedule.PendingOccurrenceAt == nil {
		return current, nil
	}
	if schedule.Timing == nil || schedule.PendingOccurrenceAt.IsZero() || schedule.NextRunAt.IsZero() || schedule.PendingOccurrenceAt.After(schedule.NextRunAt) {
		return modulecore.TaskTiming{}, errors.New("TaskMeeting pending due identity is invalid")
	}
	pending := *schedule.PendingOccurrenceAt
	keyText, err := domainscheduler.OccurrenceKey(schedule, pending, domainmeeting.TimezoneJST)
	if err != nil {
		return modulecore.TaskTiming{}, err
	}
	occurrence := domainscheduler.ScheduledOccurrence{OccurrenceKey: keyText, ScheduledAt: pending, Timing: *cloneRuntimeTaskTiming(schedule.Timing)}
	key, err := taskMeetingOccurrenceKey(schedule, occurrence)
	if err != nil {
		return modulecore.TaskTiming{}, err
	}
	owner, ok := deps.canonicalEventStore.(taskMeetingOccurrenceRegistrationOwner)
	if !ok || nilRuntimeOwner(owner) {
		return modulecore.TaskTiming{}, errors.New("TaskMeeting occurrence registration owner is unavailable")
	}
	pin, found, err := findRuntimeTaskMeetingOccurrencePin(ctx, cfg, deps, owner, key)
	if errors.Is(err, domaintask.ErrTaskMeetingNotFound) || (!found && err == nil) {
		return current, nil
	}
	if err != nil {
		return modulecore.TaskTiming{}, err
	}
	registration, declaration, err := readRuntimeTaskMeetingRegistrationByPin(ctx, cfg, deps, owner, pin)
	if err != nil {
		return modulecore.TaskTiming{}, err
	}
	if registration.ScheduleID != ownerScheduleID || !taskMeetingTimingIncludesOccurrence(declaration.Timing, pending, key.Slot) {
		return modulecore.TaskTiming{}, domaintask.ErrTaskMeetingConflict
	}
	return declaration.Timing, nil
}

func taskMeetingTimingIncludesOccurrence(timing modulecore.TaskTiming, scheduledAt time.Time, slot string) bool {
	if domainmeeting.ValidateDailyMeetingTiming(timing) != nil || !timing.Ready(scheduledAt) || timing.Recurrence == nil {
		return false
	}
	for _, configuredSlot := range timing.Recurrence.Times {
		if configuredSlot == slot {
			return true
		}
	}
	return false
}

func validateRuntimeTaskMeetingOccurrencePin(pin eventstore.TaskMeetingOccurrenceRegistrationReceipt, key domainmeeting.OccurrenceKey, ownerUserID string) error {
	if pin.OccurrenceKey != key.String() || pin.ScheduleID != key.ScheduleID || pin.OwnerUserID != ownerUserID || pin.ConfigRevision == 0 ||
		!validTaskMeetingSHA256(pin.ConfigSHA256) || pin.RegistrationEvidenceID.Validate() != nil || pin.RegistrationEventID.Validate() != nil ||
		pin.PinEventID.Validate() != nil || pin.PinnedAt.IsZero() {
		return errors.New("TaskMeeting occurrence registration pin is invalid")
	}
	return nil
}

func validTaskMeetingSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func nilRuntimeOwner(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	return (reflected.Kind() == reflect.Ptr || reflected.Kind() == reflect.Interface) && reflected.IsNil()
}

func registeredRuntimeTaskMeetingActors(cfg *config.Config, agents agentRuntime, coders ...*coderAdapter) map[string]bool {
	registered := map[string]bool{
		"mio": agents.Mio != nil, "shiro": agents.Shiro != nil,
		"kuro": agents.Heavy != nil, "midori": agents.Wild != nil,
	}
	coderConfigs := []config.CoderConfig{}
	if cfg != nil {
		coderConfigs = []config.CoderConfig{cfg.Coder1, cfg.Coder2, cfg.Coder3, cfg.Coder4}
	}
	for index, actorID := range []string{"aka", "ao", "kin", "gin"} {
		if index < len(coders) && coders[index] != nil && index < len(coderConfigs) && coderConfigs[index].Enabled && strings.ToLower(strings.TrimSpace(coderConfigs[index].Name)) == actorID {
			registered[actorID] = true
		}
	}
	return registered
}
