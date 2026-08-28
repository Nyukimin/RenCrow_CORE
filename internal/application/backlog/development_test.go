package backlog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	methodology "github.com/Nyukimin/RenCrow_CORE/internal/domain/developmentmethodology"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

type developmentEventSinkStub struct{ events []DevelopmentEvent }

func (s *developmentEventSinkStub) AppendDevelopmentEvent(_ context.Context, event DevelopmentEvent) error {
	s.events = append(s.events, event)
	return nil
}

func TestDevelopmentProjectionFailsClosedWhenCompleteArtifactSetExceedsBound(t *testing.T) {
	service, unitID := developmentServiceFixture(t)
	store := service.workstream.(*memoryWorkstreamStore)
	store.artifacts = make([]domainworkstream.Artifact, developmentArtifactProjectionLimit+1)
	if _, err := service.Development(context.Background(), unitID); err == nil || !strings.Contains(err.Error(), "complete-read limit") {
		t.Fatalf("Development error=%v, want bounded complete-read failure", err)
	}
}

func developmentServiceFixture(t *testing.T) (*Service, string) {
	t.Helper()
	now := time.Date(2026, 8, 28, 20, 0, 0, 0, time.UTC)
	items := &memoryItemStore{items: []domainbacklog.Item{{SchemaVersion: 2, ItemID: "methodology", Title: "Methodology", ConceptState: domainbacklog.ConceptAdopted, DeliveryState: domainbacklog.DeliverySpec, ImplementationUnit: "rencrow-development-methodology-v1", ImplementationRevision: 1, WorkstreamID: "ws-methodology", OwnerModule: domainbacklog.LifecycleOwnerModule, AdoptedAt: now.Add(-time.Hour).Format(time.RFC3339), CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339)}}}
	return NewService(items, &memoryWorkstreamStore{}).WithClock(func() time.Time { return now }), "rencrow-development-methodology-v1"
}

func saveDevelopmentValue(t *testing.T, service *Service, unitID, kind string, value any) (DevelopmentProjection, bool, error) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return service.SaveDevelopmentArtifact(context.Background(), unitID, SaveDevelopmentArtifactRequest{ArtifactType: kind, Payload: payload, TraceID: "trace-methodology"})
}

func developmentInitialLedger(unitID string, plan methodology.Plan, spec methodology.Specification, now time.Time) methodology.Ledger {
	ledger := methodology.NewLedger(unitID, plan.PlanID, spec.SpecID, spec.ContentHash)
	ledger.Revision = plan.Revision
	ledger.LastCheckpointAt = now
	ledger.Tasks = append([]methodology.Task(nil), plan.Tasks...)
	ledger.Worktrees = []methodology.WorktreeEvidence{{WorktreePath: "/tmp/methodology", Branch: "feat/methodology", BaseRevision: plan.Revision, GitRevision: plan.Revision, Isolated: true, Verified: true, CreatedAt: now}}
	ledger.BaselineEvidence = []methodology.BaselineEvidence{{UnitID: unitID, PlanID: plan.PlanID, SpecRef: spec.SpecID, SpecHash: spec.ContentHash, ValidForRevision: plan.Revision, WorktreePath: "/tmp/methodology", Branch: "feat/methodology", BaseRevision: plan.Revision, Command: "git status --porcelain", GitRevision: plan.Revision, Verified: true, CreatedAt: now}}
	return ledger
}

func seedDevelopmentPlan(t *testing.T, service *Service, unitID string) (methodology.Specification, methodology.Plan) {
	t.Helper()
	now := service.now()
	hash := methodology.HashContent("seed spec")
	spec := methodology.Specification{SchemaVersion: 1, SpecID: "seed-spec", Title: "Seed", Revision: 1, Status: methodology.SpecificationApproved, Source: "test", ContentHash: hash, Purpose: "test", Problem: "test", Scope: []string{"CORE"}, AcceptanceCriteria: []string{"verified"}, CreatedAt: now, UpdatedAt: now}
	plan := methodology.Plan{SchemaVersion: 1, PlanID: "seed-plan", ImplementationUnitID: unitID, SpecRef: spec.SpecID, SpecHash: hash, Revision: "rev-1", Tasks: []methodology.Task{{TaskID: "seed-task", PlanID: "seed-plan", Purpose: "test", State: methodology.TaskPending}}, CreatedAt: now, UpdatedAt: now}
	if _, _, err := saveDevelopmentValue(t, service, unitID, DevelopmentArtifactSpecification, spec); err != nil {
		t.Fatal(err)
	}
	if _, _, err := saveDevelopmentValue(t, service, unitID, DevelopmentArtifactPlan, plan); err != nil {
		t.Fatal(err)
	}
	return spec, plan
}

func TestDevelopmentArtifactsUseExistingWorkstreamStoreAndProjectLedger(t *testing.T) {
	service, unitID := developmentServiceFixture(t)
	now := service.now()
	hash := methodology.HashContent("approved spec")
	spec := methodology.Specification{SchemaVersion: 1, SpecID: "spec-methodology", Title: "Methodology", Revision: 1, Status: methodology.SpecificationApproved, Source: "owner request", ContentHash: hash, Purpose: "reproducible delivery", Problem: "partial checks", Scope: []string{"CORE"}, AcceptanceCriteria: []string{"live evidence"}, CreatedAt: now, UpdatedAt: now}
	if _, changed, err := saveDevelopmentValue(t, service, unitID, DevelopmentArtifactSpecification, spec); err != nil || !changed {
		t.Fatalf("save spec changed=%v err=%v", changed, err)
	}
	plan := methodology.Plan{SchemaVersion: 1, PlanID: "plan-methodology", ImplementationUnitID: unitID, SpecRef: spec.SpecID, SpecHash: hash, Revision: "rev-1", Tasks: []methodology.Task{{TaskID: "task-domain", PlanID: "plan-methodology", Purpose: "domain gates", State: methodology.TaskPending}}, CreatedAt: now, UpdatedAt: now}
	if _, changed, err := saveDevelopmentValue(t, service, unitID, DevelopmentArtifactPlan, plan); err != nil || !changed {
		t.Fatalf("save plan changed=%v err=%v", changed, err)
	}
	ledger := developmentInitialLedger(unitID, plan, spec, now)
	projection, changed, err := saveDevelopmentValue(t, service, unitID, DevelopmentArtifactLedger, ledger)
	if err != nil || !changed {
		t.Fatalf("save ledger changed=%v err=%v", changed, err)
	}
	if projection.Specification == nil || projection.Plan == nil || projection.Ledger == nil || len(projection.Tasks) != 1 || len(projection.Artifacts) != 3 {
		t.Fatalf("projection=%+v", projection)
	}
	if _, changed, err := saveDevelopmentValue(t, service, unitID, DevelopmentArtifactLedger, ledger); err != nil || changed {
		t.Fatalf("idempotent replay changed=%v err=%v", changed, err)
	}
	ledger.CurrentState = string(methodology.TaskReady)
	ledger.Tasks[0].State = methodology.TaskReady
	ledger.LastCheckpointAt = now.Add(time.Second)
	if _, changed, err := saveDevelopmentValue(t, service, unitID, DevelopmentArtifactLedger, ledger); err != nil || !changed {
		t.Fatalf("ledger checkpoint changed=%v err=%v", changed, err)
	}
}

func TestDevelopmentVersionedArtifactsAppendAndProjectNewestDeterministically(t *testing.T) {
	service, unitID := developmentServiceFixture(t)
	store := service.workstream.(*memoryWorkstreamStore)
	now := service.now()
	hash := methodology.HashContent("approved spec")
	spec := methodology.Specification{SchemaVersion: 1, SpecID: "spec-methodology", Title: "Methodology", Revision: 1, Status: methodology.SpecificationApproved, Source: "owner request", ContentHash: hash, Purpose: "reproducible delivery", Problem: "partial checks", Scope: []string{"CORE"}, AcceptanceCriteria: []string{"live evidence"}, CreatedAt: now, UpdatedAt: now}
	if _, _, err := saveDevelopmentValue(t, service, unitID, DevelopmentArtifactSpecification, spec); err != nil {
		t.Fatal(err)
	}
	plan := methodology.Plan{SchemaVersion: 1, PlanID: "plan-methodology", ImplementationUnitID: unitID, SpecRef: spec.SpecID, SpecHash: hash, Revision: "rev-1", Tasks: []methodology.Task{{TaskID: "task-domain", PlanID: "plan-methodology", Purpose: "domain gates", State: methodology.TaskPending}}, CreatedAt: now, UpdatedAt: now}
	if _, _, err := saveDevelopmentValue(t, service, unitID, DevelopmentArtifactPlan, plan); err != nil {
		t.Fatal(err)
	}
	ledgerV1 := developmentInitialLedger(unitID, plan, spec, now)
	if _, changed, err := saveDevelopmentValue(t, service, unitID, DevelopmentArtifactLedger, ledgerV1); err != nil || !changed {
		t.Fatalf("save ledger changed=%v err=%v", changed, err)
	}
	countBeforeReplay := len(store.artifacts)
	if _, changed, err := saveDevelopmentValue(t, service, unitID, DevelopmentArtifactLedger, ledgerV1); err != nil || changed {
		t.Fatalf("exact ledger replay changed=%v err=%v", changed, err)
	}
	if len(store.artifacts) != countBeforeReplay {
		t.Fatalf("exact ledger replay created an artifact: got=%d want=%d", len(store.artifacts), countBeforeReplay)
	}

	ledgerV2 := ledgerV1
	ledgerV2.CurrentState = string(methodology.TaskReady)
	ledgerV2.Tasks[0].State = methodology.TaskReady
	ledgerV2.LastCheckpointAt = now.Add(time.Second)
	ledgerProjection, changed, err := saveDevelopmentValue(t, service, unitID, DevelopmentArtifactLedger, ledgerV2)
	if err != nil || !changed {
		t.Fatalf("save ledger v2 changed=%v err=%v", changed, err)
	}
	if ledgerProjection.Ledger == nil || ledgerProjection.Ledger.Revision != plan.Revision || ledgerProjection.Ledger.CurrentState != string(methodology.TaskReady) {
		t.Fatalf("newest ledger was not projected: %+v", ledgerProjection.Ledger)
	}
	if len(store.artifacts) != countBeforeReplay+1 {
		t.Fatalf("ledger version did not append exactly one artifact: got=%d want=%d", len(store.artifacts), countBeforeReplay+1)
	}

	for left, right := 0, len(store.artifacts)-1; left < right; left, right = left+1, right-1 {
		store.artifacts[left], store.artifacts[right] = store.artifacts[right], store.artifacts[left]
	}
	projection, err := service.Development(context.Background(), unitID)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Specification == nil || projection.Specification.Revision != 1 || projection.Plan == nil || projection.Plan.Revision != plan.Revision || projection.Ledger == nil || projection.Ledger.Revision != plan.Revision || projection.Ledger.CurrentState != string(methodology.TaskReady) {
		t.Fatalf("reverse store order lost newest projections: spec=%+v plan=%+v ledger=%+v", projection.Specification, projection.Plan, projection.Ledger)
	}
	for index := 1; index < len(projection.Artifacts); index++ {
		previous, current := projection.Artifacts[index-1], projection.Artifacts[index]
		if previous.CreatedAt.After(current.CreatedAt) || (previous.CreatedAt.Equal(current.CreatedAt) && previous.ArtifactID >= current.ArtifactID) {
			t.Fatalf("artifacts are not deterministically oldest-first: index=%d previous=%+v current=%+v", index, previous, current)
		}
	}
}

func TestDevelopmentImmutableArtifactChangedIdentityConflicts(t *testing.T) {
	service, unitID := developmentServiceFixture(t)
	now := service.now()
	spec, plan := seedDevelopmentPlan(t, service, unitID)
	receipt := methodology.EvidenceReceipt{EvidenceID: "evidence-immutable", UnitID: unitID, PlanID: plan.PlanID, SpecHash: spec.ContentHash, Stage: "TDD_RED", EvidenceType: "tdd_red", Command: "go test ./...", ExitCode: 1, ExpectedFailure: "missing gate", ActualFailure: "missing gate", GitRevision: "abc", TraceID: "trace-red", ValidForRevision: plan.Revision, CreatedAt: now, Passed: true, MachineGenerated: true, Verified: true, VerificationResult: "verified"}
	if _, changed, err := saveDevelopmentValue(t, service, unitID, DevelopmentArtifactEvidence, receipt); err != nil || !changed {
		t.Fatalf("save immutable evidence changed=%v err=%v", changed, err)
	}
	receipt.ResultSummary = "different payload"
	if _, changed, err := saveDevelopmentValue(t, service, unitID, DevelopmentArtifactEvidence, receipt); !errors.Is(err, methodology.ErrIdempotencyConflict) || changed {
		t.Fatalf("changed immutable evidence changed=%v err=%v", changed, err)
	}
}

func TestDevelopmentArtifactRejectsCrossUnitUnknownAndStaleImplementationAuthority(t *testing.T) {
	service, unitID := developmentServiceFixture(t)
	now := service.now()
	hash := methodology.HashContent("spec")
	plan := methodology.Plan{SchemaVersion: 1, PlanID: "wrong", ImplementationUnitID: "another-unit", SpecRef: "spec", SpecHash: hash, Tasks: []methodology.Task{{TaskID: "t", PlanID: "wrong", Purpose: "x", State: methodology.TaskPending}}, CreatedAt: now, UpdatedAt: now}
	if _, _, err := saveDevelopmentValue(t, service, unitID, DevelopmentArtifactPlan, plan); err == nil {
		t.Fatal("cross-unit plan accepted")
	}
	if _, _, err := service.SaveDevelopmentArtifact(context.Background(), unitID, SaveDevelopmentArtifactRequest{ArtifactType: "unknown", Payload: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("unknown artifact accepted")
	}
	token := methodology.ImplementationAuthorityToken{ImplementationAuthorityTokenID: "expired", UnitID: unitID, SpecRef: "spec", SpecHash: hash, Issuer: "owner", IssuedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour), Scope: []string{"implementation"}, Reason: "approved"}
	if _, _, err := saveDevelopmentValue(t, service, unitID, DevelopmentArtifactImplementationAuthority, token); err == nil {
		t.Fatal("expired implementation_authority accepted")
	}
}

func TestDevelopmentEvidenceEmitsFactEventWithStableArtifactIdentity(t *testing.T) {
	service, unitID := developmentServiceFixture(t)
	spec, plan := seedDevelopmentPlan(t, service, unitID)
	sink := &developmentEventSinkStub{}
	service.WithDevelopmentEventSink(sink)
	now := service.now()
	receipt := methodology.EvidenceReceipt{EvidenceID: "evidence-red", UnitID: unitID, PlanID: plan.PlanID, SpecHash: spec.ContentHash, Stage: "TDD_RED", EvidenceType: "tdd_red", Command: "go test ./...", ExitCode: 1, ExpectedFailure: "missing gate", ActualFailure: "missing gate", GitRevision: "abc", TraceID: "trace-red", ValidForRevision: plan.Revision, CreatedAt: now, Passed: true, MachineGenerated: true, Verified: true, VerificationResult: "verified"}
	if _, changed, err := saveDevelopmentValue(t, service, unitID, DevelopmentArtifactEvidence, receipt); err != nil || !changed {
		t.Fatalf("save evidence changed=%v err=%v", changed, err)
	}
	if len(sink.events) != 1 || sink.events[0].Type != "tdd_red_verified" || sink.events[0].ArtifactID == "" || sink.events[0].UnitID != unitID {
		t.Fatalf("events=%+v", sink.events)
	}
	if _, changed, err := saveDevelopmentValue(t, service, unitID, DevelopmentArtifactEvidence, receipt); err != nil || changed {
		t.Fatalf("replay changed=%v err=%v", changed, err)
	}
	if len(sink.events) != 2 || sink.events[0].ArtifactID != sink.events[1].ArtifactID {
		t.Fatalf("replay event identity=%+v", sink.events)
	}
}

func TestDevelopmentImplementationAuthorityIsIssuedFromAdoptedStateAndStageGateFailsClosed(t *testing.T) {
	service, unitID := developmentServiceFixture(t)
	now := service.now()
	hash := methodology.HashContent("approved spec")
	spec := methodology.Specification{SchemaVersion: 1, SpecID: "spec-methodology", Title: "Methodology", Revision: 1, Status: methodology.SpecificationApproved, Source: "owner request", ContentHash: hash, Purpose: "complete delivery", Problem: "partial checks", Scope: []string{"CORE"}, AcceptanceCriteria: []string{"live"}, CreatedAt: now, UpdatedAt: now}
	if _, _, err := saveDevelopmentValue(t, service, unitID, DevelopmentArtifactSpecification, spec); err != nil {
		t.Fatal(err)
	}
	projection, created, err := service.IssueDevelopmentImplementationAuthority(context.Background(), unitID, IssueDevelopmentImplementationAuthorityRequest{Issuer: "ren", Scope: []string{"implementation"}, Reason: "explicit owner adoption", ExpiresAt: now.Add(24 * time.Hour)})
	if err != nil || !created || projection.ImplementationAuthorityToken == nil || projection.ImplementationAuthorityToken.SpecHash != hash {
		t.Fatalf("implementation_authority created=%v projection=%+v err=%v", created, projection, err)
	}
	plan := methodology.Plan{SchemaVersion: 1, PlanID: "plan-methodology", ImplementationUnitID: unitID, SpecRef: spec.SpecID, SpecHash: hash, Tasks: []methodology.Task{{TaskID: "task", PlanID: "plan-methodology", Purpose: "implement", State: methodology.TaskPending}}, CreatedAt: now, UpdatedAt: now}
	if _, _, err := saveDevelopmentValue(t, service, unitID, DevelopmentArtifactPlan, plan); err != nil {
		t.Fatal(err)
	}
	ledger := methodology.NewLedger(unitID, plan.PlanID, spec.SpecID, hash)
	ledger.Revision = "rev-1"
	ledger.LastCheckpointAt = now
	ledger.Tasks = append([]methodology.Task(nil), plan.Tasks...)
	if _, _, err := saveDevelopmentValue(t, service, unitID, DevelopmentArtifactLedger, ledger); err == nil || !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("initial ledger without worktree err=%v", err)
	}
}
