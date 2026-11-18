package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRuntimeTaskMeetingBoardPublicationFreezesRouteAndPreservesOldDeclarations(t *testing.T) {
	cfg := validRuntimeTaskMeetingConfig()
	old, err := taskMeetingDeclarationFromRuntimeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	oldBytes, oldHash, err := domainmeeting.CanonicalTaskMeetingDeclarationJSON(old)
	if err != nil || bytes.Contains(oldBytes, []byte("board_publication")) {
		t.Fatalf("old declaration must retain previous canonical shape: %v", err)
	}
	decodedOld, decodedHash, err := domainmeeting.DecodeCanonicalTaskMeetingDeclaration(oldBytes)
	if err != nil || decodedHash != oldHash || decodedOld.BoardPublication != nil {
		t.Fatalf("old canonical declaration cannot be replayed: %v", err)
	}
	cfg.Heartbeat.Routine.DailyReportMeetings.BoardPublication = &domainmeeting.BoardPublication{ChannelID: "rencrow", SenderPrincipalID: "rencrow-comm", Recipients: []string{"ren-browser"}}
	declaration, err := taskMeetingDeclarationFromRuntimeConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	encoded, digest, err := domainmeeting.CanonicalTaskMeetingDeclarationJSON(declaration)
	if err != nil || digest == oldHash {
		t.Fatalf("publication route must be part of frozen registration hash: %v", err)
	}
	cfg.Heartbeat.Routine.DailyReportMeetings.BoardPublication.ChannelID = "elsewhere"
	cfg.Heartbeat.Routine.DailyReportMeetings.BoardPublication.Recipients[0] = "outsider"
	if declaration.BoardPublication.ChannelID != "rencrow" || declaration.BoardPublication.Recipients[0] != "ren-browser" {
		t.Fatal("runtime config mutation changed frozen route")
	}
	decoded, actualDigest, err := domainmeeting.DecodeCanonicalTaskMeetingDeclaration(encoded)
	if err != nil || actualDigest != digest || decoded.BoardPublication.ChannelID != "rencrow" || decoded.BoardPublication.Recipients[0] != "ren-browser" {
		t.Fatalf("canonical publication route lost on recovery: %v", err)
	}
	cfg.Heartbeat.Routine.DailyReportMeetings.BoardPublication.Recipients = nil
	if _, err := taskMeetingDeclarationFromRuntimeConfig(cfg); err == nil {
		t.Fatal("declaration constructor normalized missing recipients into explicit empty recipients")
	}
}

func TestRuntimeTaskMeetingRegistrationPersistsCanonicalOwnerDeclaration(t *testing.T) {
	setTaskMeetingWorldReadCredential(t)
	cfg := validRuntimeTaskMeetingConfig()
	deps := &Dependencies{taskCenterUserID: "owner-1", taskCenterUserToken: []byte("memory-only-auth-token")}
	owner := &taskMeetingRegistrationTestOwner{}
	deps.canonicalEventStore = &taskMeetingRegistrationTestEventStore{taskMeetingRegistrationTestOwner: owner}
	owner.ensure = func(ctx context.Context, request eventstore.TaskMeetingRegistrationRequest, authorize eventstore.TaskMeetingRegistrationAuthorizationCheck) (eventstore.TaskMeetingScheduleRegistrationReceipt, error) {
		if err := authorize(ctx, request.Declaration); err != nil {
			return eventstore.TaskMeetingScheduleRegistrationReceipt{}, err
		}
		owner.request = request
		return taskMeetingReceiptForRequest(request), nil
	}

	scheduleID, registered, err := registerRuntimeTaskMeetingSchedule(
		context.Background(), cfg, deps, registeredMeetingActors(), registeredMeetingCoders()...,
	)
	if err != nil || !registered || scheduleID == "" {
		t.Fatalf("registration result schedule=%q registered=%t err=%v", scheduleID, registered, err)
	}
	if owner.request.Declaration.WorldBaseURL != cfg.WorldProjection.BaseURL || owner.request.Declaration.WorldCredentialEnv != cfg.WorldProjection.TokenEnv {
		t.Fatalf("declaration did not retain safe WORLD source references: %+v", owner.request.Declaration)
	}
	if bytes.Contains(owner.request.FrozenDeclaration, []byte("memory-only-auth-token")) {
		t.Fatal("LocalAgentOps token was copied into the frozen declaration")
	}
	if deps.taskCenterUserToken[0] != 'm' {
		t.Fatal("registration mutated the in-memory LocalAgentOps token")
	}
}

func TestRuntimeTaskMeetingRegistrationDeniesMissingActorOrLocalOwner(t *testing.T) {
	tests := []struct {
		name string
		edit func(*config.Config, *Dependencies)
	}{
		{
			name: "missing runtime actor",
			edit: func(cfg *config.Config, _ *Dependencies) {
				cfg.Heartbeat.Routine.DailyReportMeetings.Actors[7].CoreActorID = "unknown"
			},
		},
		{
			name: "LocalAgentOps owner mismatch",
			edit: func(_ *config.Config, deps *Dependencies) { deps.taskCenterUserID = "other-owner" },
		},
		{
			name: "missing runtime credential",
			edit: func(_ *config.Config, deps *Dependencies) { deps.taskCenterUserToken = nil },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setTaskMeetingWorldReadCredential(t)
			cfg := validRuntimeTaskMeetingConfig()
			deps := &Dependencies{taskCenterUserID: "owner-1", taskCenterUserToken: []byte("memory-only-auth-token")}
			owner := &taskMeetingRegistrationTestOwner{}
			deps.canonicalEventStore = &taskMeetingRegistrationTestEventStore{taskMeetingRegistrationTestOwner: owner}
			owner.ensure = func(ctx context.Context, request eventstore.TaskMeetingRegistrationRequest, authorize eventstore.TaskMeetingRegistrationAuthorizationCheck) (eventstore.TaskMeetingScheduleRegistrationReceipt, error) {
				if err := authorize(ctx, request.Declaration); err != nil {
					return eventstore.TaskMeetingScheduleRegistrationReceipt{}, err
				}
				return taskMeetingReceiptForRequest(request), nil
			}
			test.edit(cfg, deps)

			scheduleID, registered, err := registerRuntimeTaskMeetingSchedule(
				context.Background(), cfg, deps, registeredMeetingActors(), registeredMeetingCoders()...,
			)
			if err == nil || registered || scheduleID != "" {
				t.Fatalf("invalid owner registration was accepted: schedule=%q registered=%t err=%v", scheduleID, registered, err)
			}
			if owner.request.Declaration.OwnerUserID != "" {
				t.Fatal("rejected authorization retained a request as accepted")
			}
		})
	}
}

func TestRuntimeTaskMeetingMissingWorldCredentialRecoversAndDoesNotRegister(t *testing.T) {
	t.Setenv("WORLD_READ_TOKEN", "")
	cfg := validRuntimeTaskMeetingConfig()
	prior := taskMeetingReceiptForConfig(t, cfg)
	deps := &Dependencies{taskCenterUserID: "owner-1", taskCenterUserToken: []byte("memory-only-auth-token")}
	owner := &taskMeetingRegistrationTestOwner{prior: prior}
	deps.canonicalEventStore = &taskMeetingRegistrationTestEventStore{taskMeetingRegistrationTestOwner: owner}

	scheduleID, registered, err := registerRuntimeTaskMeetingSchedule(
		context.Background(), cfg, deps, registeredMeetingActors(), registeredMeetingCoders()...,
	)
	if err == nil || registered || scheduleID != prior.ScheduleID {
		t.Fatalf("missing WORLD credential should recover only the owned schedule ID: id=%q registered=%t err=%v", scheduleID, registered, err)
	}
	if owner.ensureCalls != 1 || owner.latestReadCalls != 1 {
		t.Fatalf("missing credential skipped authorization or exact-owner metadata recovery: ensure=%d read=%d", owner.ensureCalls, owner.latestReadCalls)
	}
	if strings.Contains(err.Error(), "memory-only-auth-token") {
		t.Fatal("registration error exposed an in-memory owner credential")
	}
}

func TestRuntimeTaskMeetingDisabledReadUsesOwnerReceiptWithoutActivationAuthority(t *testing.T) {
	tests := []struct {
		name       string
		clearOwner bool
	}{
		{name: "retained nested owner"},
		{name: "retained LocalAgentOps owner", clearOwner: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validRuntimeTaskMeetingConfig()
			prior := taskMeetingReceiptForConfig(t, cfg)
			cfg.Heartbeat.Routine.DailyReportMeetings.Enabled = false
			cfg.LocalAgentOps.Enabled = false
			if test.clearOwner {
				cfg.Heartbeat.Routine.DailyReportMeetings.OwnerUserID = ""
			}
			deps := &Dependencies{}
			owner := &taskMeetingRegistrationTestOwner{prior: prior}
			deps.canonicalEventStore = &taskMeetingRegistrationTestEventStore{taskMeetingRegistrationTestOwner: owner}

			scheduleID, registered, err := registerRuntimeTaskMeetingSchedule(context.Background(), cfg, deps, agentRuntime{})
			if err != nil || registered || scheduleID != owner.prior.ScheduleID {
				t.Fatalf("disabled recovery schedule=%q registered=%t err=%v", scheduleID, registered, err)
			}
			if owner.latestReadCalls != 1 || owner.lastReadRevision != 0 {
				t.Fatalf("disabled recovery did not use authorized latest metadata read: calls=%d revision=%d", owner.latestReadCalls, owner.lastReadRevision)
			}
		})
	}
}

func TestRuntimeTaskMeetingDisabledReadDoesNotScanWithoutOwnerIdentity(t *testing.T) {
	cfg := validRuntimeTaskMeetingConfig()
	cfg.Heartbeat.Routine.DailyReportMeetings.Enabled = false
	cfg.Heartbeat.Routine.DailyReportMeetings.OwnerUserID = ""
	cfg.LocalAgentOps.UserID = ""
	owner := &taskMeetingRegistrationTestOwner{}
	deps := &Dependencies{canonicalEventStore: taskMeetingRegistrationTestEventStore{taskMeetingRegistrationTestOwner: owner}}

	scheduleID, registered, err := registerRuntimeTaskMeetingSchedule(context.Background(), cfg, deps, agentRuntime{})
	if err != nil || registered || scheduleID != "" || owner.latestReadCalls != 0 {
		t.Fatalf("ownerless disabled lookup should be closed: schedule=%q registered=%t calls=%d err=%v", scheduleID, registered, owner.latestReadCalls, err)
	}
}

func TestRuntimeTaskMeetingRegistrationReadIsLimitedToConfiguredOwnerIdentities(t *testing.T) {
	cfg := validRuntimeTaskMeetingConfig()
	cfg.Heartbeat.Routine.DailyReportMeetings.Enabled = false
	cfg.LocalAgentOps.Enabled = false
	deps := &Dependencies{}
	if err := authorizeRuntimeTaskMeetingRegistrationRead(context.Background(), cfg, deps, "owner-1", domainmeeting.TaskMeetingRegistrationKey); err != nil {
		t.Fatalf("read-only owner metadata lookup required activation credentials: %v", err)
	}
	if err := authorizeRuntimeTaskMeetingRegistrationRead(context.Background(), cfg, deps, "other-owner", domainmeeting.TaskMeetingRegistrationKey); err == nil {
		t.Fatal("foreign registration owner was accepted")
	}
	if err := authorizeRuntimeTaskMeetingRegistrationRead(context.Background(), cfg, deps, "owner-1", "other.registration"); err == nil {
		t.Fatal("foreign registration key was accepted")
	}
}

func TestRuntimeTaskMeetingFailedUpdateRecoversOnlyPriorIssuedScheduleID(t *testing.T) {
	setTaskMeetingWorldReadCredential(t)
	cfg := validRuntimeTaskMeetingConfig()
	prior := taskMeetingReceiptForConfig(t, cfg)
	deps := &Dependencies{taskCenterUserID: "owner-1", taskCenterUserToken: []byte("memory-only-auth-token")}
	owner := &taskMeetingRegistrationTestOwner{prior: prior, ensureErr: errors.New("registration conflict")}
	deps.canonicalEventStore = &taskMeetingRegistrationTestEventStore{taskMeetingRegistrationTestOwner: owner}

	scheduleID, registered, err := registerRuntimeTaskMeetingSchedule(
		context.Background(), cfg, deps, registeredMeetingActors(), registeredMeetingCoders()...,
	)
	if err == nil || registered || scheduleID != prior.ScheduleID {
		t.Fatalf("failed update recovery schedule=%q registered=%t err=%v", scheduleID, registered, err)
	}
	if owner.latestReadCalls != 1 {
		t.Fatalf("failed update did not read the current owner receipt: calls=%d", owner.latestReadCalls)
	}
}

func TestRuntimeTaskMeetingRegistrationRejectsMalformedReceipt(t *testing.T) {
	setTaskMeetingWorldReadCredential(t)
	cfg := validRuntimeTaskMeetingConfig()
	deps := &Dependencies{taskCenterUserID: "owner-1", taskCenterUserToken: []byte("memory-only-auth-token")}
	owner := &taskMeetingRegistrationTestOwner{}
	deps.canonicalEventStore = &taskMeetingRegistrationTestEventStore{taskMeetingRegistrationTestOwner: owner}
	owner.ensure = func(ctx context.Context, request eventstore.TaskMeetingRegistrationRequest, authorize eventstore.TaskMeetingRegistrationAuthorizationCheck) (eventstore.TaskMeetingScheduleRegistrationReceipt, error) {
		if err := authorize(ctx, request.Declaration); err != nil {
			return eventstore.TaskMeetingScheduleRegistrationReceipt{}, err
		}
		receipt := taskMeetingReceiptForRequest(request)
		receipt.ConfigSHA256 = "corrupt"
		return receipt, nil
	}
	scheduleID, registered, err := registerRuntimeTaskMeetingSchedule(
		context.Background(), cfg, deps, registeredMeetingActors(), registeredMeetingCoders()...,
	)
	if err == nil || registered || scheduleID != "" {
		t.Fatalf("malformed owner receipt was accepted: schedule=%q registered=%t err=%v", scheduleID, registered, err)
	}
}

type taskMeetingRegistrationTestEventStore struct {
	runtimeCanonicalEventStore
	*taskMeetingRegistrationTestOwner
}

type taskMeetingRegistrationTestOwner struct {
	request          eventstore.TaskMeetingRegistrationRequest
	prior            eventstore.TaskMeetingScheduleRegistrationReceipt
	ensureErr        error
	ensure           func(context.Context, eventstore.TaskMeetingRegistrationRequest, eventstore.TaskMeetingRegistrationAuthorizationCheck) (eventstore.TaskMeetingScheduleRegistrationReceipt, error)
	ensureCalls      int
	latestReadCalls  int
	lastReadRevision uint64
}

func (o *taskMeetingRegistrationTestOwner) EnsureTaskMeetingScheduleRegistration(
	ctx context.Context,
	request eventstore.TaskMeetingRegistrationRequest,
	authorize eventstore.TaskMeetingRegistrationAuthorizationCheck,
) (eventstore.TaskMeetingScheduleRegistrationReceipt, error) {
	o.ensureCalls++
	if o.ensure != nil {
		return o.ensure(ctx, request, authorize)
	}
	if err := authorize(ctx, request.Declaration); err != nil {
		return eventstore.TaskMeetingScheduleRegistrationReceipt{}, err
	}
	if o.ensureErr != nil {
		return eventstore.TaskMeetingScheduleRegistrationReceipt{}, o.ensureErr
	}
	return taskMeetingReceiptForRequest(request), nil
}

func setTaskMeetingWorldReadCredential(t *testing.T) {
	t.Helper()
	t.Setenv("WORLD_READ_TOKEN", "fixture-world-read-credential")
	lifePath := filepath.Join(t.TempDir(), "world-life.token")
	if err := os.WriteFile(lifePath, []byte("fixture-world-life-credential\n"), 0o600); err != nil {
		t.Fatalf("write fixture WORLD life credential: %v", err)
	}
	t.Setenv("TASKMEETING_TEST_WORLD_LIFE_TOKEN_FILE", lifePath)
}

func (o *taskMeetingRegistrationTestOwner) FindTaskMeetingScheduleRegistration(
	ctx context.Context,
	registrationKey, ownerUserID string,
	revision uint64,
	authorize eventstore.TaskMeetingRegistrationReadAuthorizationCheck,
) (eventstore.TaskMeetingScheduleRegistrationReceipt, error) {
	if err := authorize(ctx, ownerUserID, registrationKey, revision); err != nil {
		return eventstore.TaskMeetingScheduleRegistrationReceipt{}, err
	}
	if o.prior.Revision == revision && o.prior.OwnerUserID == ownerUserID {
		return o.prior, nil
	}
	return eventstore.TaskMeetingScheduleRegistrationReceipt{}, domaintask.ErrTaskMeetingNotFound
}

func (o *taskMeetingRegistrationTestOwner) FindLatestTaskMeetingScheduleRegistration(
	ctx context.Context,
	registrationKey, ownerUserID string,
	authorize eventstore.TaskMeetingRegistrationReadAuthorizationCheck,
) (eventstore.TaskMeetingScheduleRegistrationReceipt, error) {
	o.latestReadCalls++
	if err := authorize(ctx, ownerUserID, registrationKey, 0); err != nil {
		return eventstore.TaskMeetingScheduleRegistrationReceipt{}, err
	}
	o.lastReadRevision = 0
	if o.prior.OwnerUserID == ownerUserID && o.prior.RegistrationKey == registrationKey {
		return o.prior, nil
	}
	return eventstore.TaskMeetingScheduleRegistrationReceipt{}, domaintask.ErrTaskMeetingNotFound
}

func taskMeetingReceiptForRequest(request eventstore.TaskMeetingRegistrationRequest) eventstore.TaskMeetingScheduleRegistrationReceipt {
	canonical, digest, _ := domainmeeting.CanonicalTaskMeetingDeclarationJSON(request.Declaration)
	return eventstore.TaskMeetingScheduleRegistrationReceipt{
		RegistrationKey:        request.Declaration.RegistrationKey,
		OwnerUserID:            request.Declaration.OwnerUserID,
		ScheduleID:             modulecore.NewScheduleID(),
		RegistrationEvidenceID: modulecore.NewEvidenceID(),
		Revision:               request.Declaration.Revision,
		FrozenDeclaration:      canonical,
		ConfigSHA256:           digest,
		EventID:                modulecore.NewEventID(),
		AcceptedAt:             time.Unix(1_800_000_000, 0).UTC(),
	}
}

func taskMeetingReceiptForConfig(t *testing.T, cfg *config.Config) eventstore.TaskMeetingScheduleRegistrationReceipt {
	t.Helper()
	declaration, err := taskMeetingDeclarationFromRuntimeConfig(cfg)
	if err != nil {
		t.Fatalf("build registration fixture declaration: %v", err)
	}
	canonical, _, err := domainmeeting.CanonicalTaskMeetingDeclarationJSON(declaration)
	if err != nil {
		t.Fatal(err)
	}
	return taskMeetingReceiptForRequest(eventstore.TaskMeetingRegistrationRequest{Declaration: declaration, FrozenDeclaration: canonical})
}

func registeredMeetingActors() agentRuntime {
	return agentRuntime{Mio: &agent.MioAgent{}, Shiro: &agent.ShiroAgent{}, Heavy: &agent.HeavyAgent{}, Wild: &agent.WildAgent{}}
}

func registeredMeetingCoders() []*coderAdapter { return []*coderAdapter{{}, {}, {}, {}} }
