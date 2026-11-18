package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	worldmeeting "github.com/Nyukimin/RenCrow_CORE/internal/adapter/taskmeeting"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type taskMeetingScheduleRegistrationOwner interface {
	EnsureTaskMeetingScheduleRegistration(
		context.Context,
		eventstore.TaskMeetingRegistrationRequest,
		eventstore.TaskMeetingRegistrationAuthorizationCheck,
	) (eventstore.TaskMeetingScheduleRegistrationReceipt, error)
	FindLatestTaskMeetingScheduleRegistration(
		context.Context,
		string,
		string,
		eventstore.TaskMeetingRegistrationReadAuthorizationCheck,
	) (eventstore.TaskMeetingScheduleRegistrationReceipt, error)
}

// registerRuntimeTaskMeetingSchedule obtains scheduler identity only from the
// canonical owner receipt. It returns an existing issued identity even when
// current registration fails, so the caller can disable that exact projection.
func registerRuntimeTaskMeetingSchedule(
	ctx context.Context,
	cfg *config.Config,
	deps *Dependencies,
	agents agentRuntime,
	coders ...*coderAdapter,
) (modulecore.ScheduleID, bool, error) {
	if ctx == nil || cfg == nil || deps == nil {
		return "", false, errors.New("TaskMeeting registration runtime is unavailable")
	}
	owner, ok := deps.canonicalEventStore.(taskMeetingScheduleRegistrationOwner)
	if !ok || owner == nil || (reflect.ValueOf(owner).Kind() == reflect.Ptr && reflect.ValueOf(owner).IsNil()) {
		if cfg.Heartbeat.Routine.DailyReportMeetings.Enabled {
			return "", false, errors.New("TaskMeeting registration owner is unavailable")
		}
		return "", false, nil
	}
	meeting := cfg.Heartbeat.Routine.DailyReportMeetings

	if !meeting.Enabled {
		scheduleID, err := findLatestRuntimeTaskMeetingScheduleID(ctx, owner, cfg, deps, taskMeetingConfiguredOwnerUserIDs(cfg))
		if errors.Is(err, domaintask.ErrTaskMeetingNotFound) {
			return "", false, nil
		}
		return scheduleID, false, err
	}

	declaration, err := taskMeetingDeclarationFromRuntimeConfig(cfg)
	if err != nil {
		scheduleID, readErr := findLatestRuntimeTaskMeetingScheduleID(ctx, owner, cfg, deps, taskMeetingConfiguredOwnerUserIDs(cfg))
		if errors.Is(readErr, domaintask.ErrTaskMeetingNotFound) {
			return "", false, err
		}
		return scheduleID, false, errors.Join(err, readErr)
	}
	frozenDeclaration, configSHA256, err := domainmeeting.CanonicalTaskMeetingDeclarationJSON(declaration)
	if err != nil {
		return "", false, err
	}
	currentAuthorization := func(authCtx context.Context, requested domainmeeting.TaskMeetingDeclaration) error {
		if err := authCtx.Err(); err != nil {
			return err
		}
		if !cfg.LocalAgentOps.Enabled || strings.TrimSpace(cfg.LocalAgentOps.UserID) != declaration.OwnerUserID ||
			deps.taskCenterUserID != declaration.OwnerUserID || len(deps.taskCenterUserToken) == 0 {
			return errors.New("TaskMeeting LocalAgentOps owner is not currently authenticated")
		}
		if !taskMeetingRuntimeBindingsReady(cfg, agents, coders...) {
			return errors.New("TaskMeeting configured actor bindings are not registered")
		}
		requestedBytes, requestedHash, err := domainmeeting.CanonicalTaskMeetingDeclarationJSON(requested)
		if err != nil || requestedHash != configSHA256 || !bytes.Equal(requestedBytes, frozenDeclaration) {
			return errors.New("TaskMeeting declaration does not match the current owner configuration")
		}
		if taskMeetingWorldReadAvailabilityForDeclaration(cfg, requested) != "" {
			return errors.New("TaskMeeting WORLD read credential is unavailable")
		}
		return nil
	}
	receipt, ensureErr := owner.EnsureTaskMeetingScheduleRegistration(ctx, eventstore.TaskMeetingRegistrationRequest{
		Declaration:       declaration,
		FrozenDeclaration: append([]byte(nil), frozenDeclaration...),
	}, currentAuthorization)
	if ensureErr == nil {
		if err := validateTaskMeetingRegistrationReceipt(receipt, &declaration, configSHA256); err != nil {
			return "", false, err
		}
		return receipt.ScheduleID, true, nil
	}

	// A denied or conflicting update must not leave an older projection running.
	// Readback is separately authorized and never turns the old declaration live.
	priorID, readErr := findLatestRuntimeTaskMeetingScheduleID(ctx, owner, cfg, deps, []string{declaration.OwnerUserID})
	if readErr == nil {
		return priorID, false, ensureErr
	}
	if errors.Is(readErr, domaintask.ErrTaskMeetingNotFound) {
		return "", false, ensureErr
	}
	return "", false, errors.Join(ensureErr, readErr)
}

// taskMeetingWorldReadAvailabilityReason checks the same configured client
// contract the execution path uses, without making a WORLD request or exposing
// the configured credential. Registration metadata reads deliberately do not
// use this gate, so an existing owner-issued schedule can still be recovered
// and disabled when runtime credentials are absent.
func taskMeetingWorldReadAvailabilityReason(cfg *config.Config) string {
	declaration, err := taskMeetingDeclarationFromRuntimeConfig(cfg)
	if err != nil {
		return "world_declaration_unavailable"
	}
	return taskMeetingWorldReadAvailabilityForDeclaration(cfg, declaration)
}

func taskMeetingWorldReadAvailabilityForDeclaration(cfg *config.Config, declaration domainmeeting.TaskMeetingDeclaration) string {
	if cfg == nil {
		return "world_declaration_unavailable"
	}
	worldConfig := *cfg
	actors := make([]config.WorldProjectionActorConfig, 0, len(declaration.WorldActorBindings))
	for _, binding := range declaration.WorldActorBindings {
		actors = append(actors, config.WorldProjectionActorConfig{ActorID: binding.WorldActorID})
	}
	worldConfig.WorldProjection = config.WorldProjectionConfig{
		BaseURL: declaration.WorldBaseURL, TokenEnv: declaration.WorldCredentialEnv,
		WorldID: declaration.WorldID, TimelineID: declaration.TimelineID, TimelineEpoch: declaration.TimelineEpoch,
		Actors: actors,
	}
	client, err := worldmeeting.NewConfiguredWorldSnapshotClient(
		worldConfig.WorldProjection,
		worldProjectionDuration(worldConfig.WorldProjection.TimeoutSeconds, defaultWorldProjectionTimeout),
	)
	if err != nil || client == nil {
		return "world_credential_unavailable"
	}
	gatherClient, err := worldmeeting.NewConfiguredWorldGatherClient(
		declaration,
		cfg.Heartbeat.Routine.WorldLifeTokenFile,
		worldProjectionDuration(worldConfig.WorldProjection.TimeoutSeconds, defaultWorldProjectionTimeout),
	)
	if err != nil || gatherClient == nil {
		return "world_life_credential_unavailable"
	}
	return ""
}

func findLatestRuntimeTaskMeetingScheduleID(
	ctx context.Context,
	owner taskMeetingScheduleRegistrationOwner,
	cfg *config.Config,
	deps *Dependencies,
	ownerUserIDs []string,
) (modulecore.ScheduleID, error) {
	readAuthorization := func(readCtx context.Context, ownerUserID, registrationKey string, _ uint64) error {
		return authorizeRuntimeTaskMeetingRegistrationRead(readCtx, cfg, deps, ownerUserID, registrationKey)
	}
	for _, ownerUserID := range ownerUserIDs {
		receipt, err := owner.FindLatestTaskMeetingScheduleRegistration(
			ctx,
			domainmeeting.TaskMeetingRegistrationKey,
			ownerUserID,
			readAuthorization,
		)
		if err != nil {
			if errors.Is(err, domaintask.ErrTaskMeetingNotFound) {
				continue
			}
			return "", err
		}
		if err := validateTaskMeetingRegistrationReceipt(receipt, nil, ""); err != nil {
			return "", err
		}
		return receipt.ScheduleID, nil
	}
	return "", domaintask.ErrTaskMeetingNotFound
}

func taskMeetingDeclarationFromRuntimeConfig(cfg *config.Config) (domainmeeting.TaskMeetingDeclaration, error) {
	if cfg == nil {
		return domainmeeting.TaskMeetingDeclaration{}, errors.New("TaskMeeting configuration is unavailable")
	}
	meeting := cfg.Heartbeat.Routine.DailyReportMeetings
	if !meeting.Enabled || !cfg.LocalAgentOps.Enabled || strings.TrimSpace(cfg.LocalAgentOps.UserID) == "" ||
		meeting.OwnerUserID != cfg.LocalAgentOps.UserID {
		return domainmeeting.TaskMeetingDeclaration{}, errors.New("TaskMeeting requires the enabled LocalAgentOps owner")
	}
	bindings := make([]domainmeeting.WorldActorBinding, len(meeting.Actors))
	audience := make([]string, len(meeting.Actors))
	for index, actor := range meeting.Actors {
		audience[index] = actor.CoreActorID
		bindings[index] = domainmeeting.WorldActorBinding{CoreActorID: actor.CoreActorID, WorldActorID: actor.WorldActorID}
	}
	world := cfg.WorldProjection
	declaration := domainmeeting.TaskMeetingDeclaration{
		RegistrationKey:          domainmeeting.TaskMeetingRegistrationKey,
		OwnerUserID:              meeting.OwnerUserID,
		Revision:                 meeting.Revision,
		DataScope:                meeting.DataScope,
		CoordinatorActorID:       meeting.CoordinatorActorID,
		Audience:                 audience,
		WorldActorBindings:       bindings,
		WorldBaseURL:             world.BaseURL,
		WorldCredentialEnv:       world.TokenEnv,
		WorldID:                  world.WorldID,
		TimelineID:               world.TimelineID,
		TimelineEpoch:            world.TimelineEpoch,
		Rooms:                    domainmeeting.RoomRefs{Living: meeting.LivingRoomRef, Dining: meeting.DiningRoomRef},
		Timing:                   meeting.TaskTiming(),
		MeetingDeadlineAfterSecs: meeting.MeetingDeadlineAfterSecs,
	}
	if err := declaration.Validate(); err != nil {
		return domainmeeting.TaskMeetingDeclaration{}, fmt.Errorf("TaskMeeting owner declaration is invalid: %w", err)
	}
	return declaration, nil
}

func authorizeRuntimeTaskMeetingRegistrationRead(
	ctx context.Context,
	cfg *config.Config,
	deps *Dependencies,
	ownerUserID string,
	registrationKey string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cfg == nil || deps == nil || registrationKey != domainmeeting.TaskMeetingRegistrationKey || ownerUserID == "" {
		return errors.New("TaskMeeting registration read is not authorized for the current local owner")
	}
	if !taskMeetingRegistrationReadOwnerMatches(cfg, ownerUserID) {
		return errors.New("TaskMeeting registration owner changed")
	}
	return nil
}

func taskMeetingConfiguredOwnerUserIDs(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	ownerIDs := make([]string, 0, 2)
	for _, ownerUserID := range []string{
		strings.TrimSpace(cfg.Heartbeat.Routine.DailyReportMeetings.OwnerUserID),
		strings.TrimSpace(cfg.LocalAgentOps.UserID),
	} {
		if ownerUserID == "" || (len(ownerIDs) > 0 && ownerIDs[0] == ownerUserID) {
			continue
		}
		ownerIDs = append(ownerIDs, ownerUserID)
	}
	return ownerIDs
}

func taskMeetingRegistrationReadOwnerMatches(cfg *config.Config, ownerUserID string) bool {
	if cfg == nil || ownerUserID == "" {
		return false
	}
	return ownerUserID == strings.TrimSpace(cfg.Heartbeat.Routine.DailyReportMeetings.OwnerUserID) ||
		ownerUserID == strings.TrimSpace(cfg.LocalAgentOps.UserID)
}

func validateTaskMeetingRegistrationReceipt(
	receipt eventstore.TaskMeetingScheduleRegistrationReceipt,
	expected *domainmeeting.TaskMeetingDeclaration,
	expectedHash string,
) error {
	if receipt.RegistrationKey != domainmeeting.TaskMeetingRegistrationKey || receipt.OwnerUserID == "" ||
		receipt.ScheduleID.Validate() != nil || receipt.RegistrationEvidenceID.Validate() != nil ||
		receipt.EventID.Validate() != nil || receipt.Revision == 0 || receipt.AcceptedAt.IsZero() {
		return errors.New("TaskMeeting registration receipt identity is invalid")
	}
	declaration, digest, err := domainmeeting.DecodeCanonicalTaskMeetingDeclaration(receipt.FrozenDeclaration)
	if err != nil || digest != receipt.ConfigSHA256 || declaration.RegistrationKey != receipt.RegistrationKey ||
		declaration.OwnerUserID != receipt.OwnerUserID || declaration.Revision != receipt.Revision {
		return errors.New("TaskMeeting registration receipt does not match its frozen declaration")
	}
	if expected != nil {
		canonical, expectedDigest, err := domainmeeting.CanonicalTaskMeetingDeclarationJSON(*expected)
		if err != nil || expectedDigest != expectedHash || digest != expectedHash || !bytes.Equal(canonical, receipt.FrozenDeclaration) {
			return errors.New("TaskMeeting registration receipt does not match the current declaration")
		}
	}
	return nil
}
