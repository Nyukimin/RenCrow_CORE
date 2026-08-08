package durablestore

import (
	"context"
	"testing"

	domain "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
)

type memoryStore struct {
	byKey map[string]domain.WorkflowResult
}

type countingImplementer struct {
	calls    int
	evidence domain.ActivationEvidence
}

func (i *countingImplementer) Implement(_ context.Context, req domain.StorageRequirement, classification domain.Classification) (*domain.StorageProposal, domain.ActivationEvidence, error) {
	i.calls++
	return &domain.StorageProposal{ProposalID: "sp-1", RequirementID: req.RequirementID, OwnerModule: classification.OwnerModule, Class: classification.Class, ChangeClass: classification.ChangeClass, ProposalRevision: 1, ValidationPassed: true}, i.evidence, nil
}

func (s *memoryStore) FindByDedupeKey(_ context.Context, key string) (*domain.WorkflowResult, error) {
	r, ok := s.byKey[key]
	if !ok {
		return nil, nil
	}
	return &r, nil
}
func (s *memoryStore) Save(_ context.Context, r domain.WorkflowResult) error {
	if s.byKey == nil {
		s.byKey = map[string]domain.WorkflowResult{}
	}
	s.byKey[r.Requirement.DedupeKey] = r
	return nil
}

func TestServiceAssessAndDedupe(t *testing.T) {
	store := &memoryStore{}
	svc := NewService([]domain.Manifest{domainTestManifest()}, store, nil)
	in := Input{RequestID: "req-1", TraceID: "trace-1", RequestedBy: "ren", UserScope: "user:ren", Message: "XのBookmarkを保存するDBの設計を確認して"}
	first, handled, err := svc.Handle(context.Background(), in)
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if first.Status != domain.StatusCompleted || first.Classification.Class != domain.ClassExistingStore {
		t.Fatalf("unexpected first result: %+v", first)
	}
	second, handled, err := svc.Handle(context.Background(), in)
	if err != nil || !handled || !second.Deduplicated || second.Requirement.RequirementID != first.Requirement.RequirementID {
		t.Fatalf("unexpected duplicate result: %+v handled=%v err=%v", second, handled, err)
	}
}

func TestServiceImplementNewStoreBlocksWithoutValidatedImplementer(t *testing.T) {
	svc := NewService([]domain.Manifest{domainTestManifest()}, &memoryStore{}, nil)
	got, handled, err := svc.Handle(context.Background(), Input{RequestID: "req-2", RequestedBy: "ren", Message: "ゲームの観測値を新しいDBに保存する仕組みを実装して"})
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if got.Status != domain.StatusBlocked || got.Lifecycle != domain.LifecycleProposed {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestServiceDoesNotActivateWithIncompleteOperationalEvidence(t *testing.T) {
	implementer := &countingImplementer{evidence: domain.ActivationEvidence{MigrationPassed: true, BackupDryRun: true, ScratchRestore: true, IntegrityPassed: true}}
	svc := NewService([]domain.Manifest{domainTestManifest()}, &memoryStore{}, implementer)
	got, handled, err := svc.Handle(context.Background(), Input{RequestID: "req-3", RequestedBy: "ren", Message: "ゲームの観測値を新しいDBに保存する仕組みを実装して"})
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if implementer.calls != 1 || got.Status != domain.StatusBlocked || got.Lifecycle != domain.LifecycleImplemented || got.ReasonCode != "activation_evidence_incomplete" {
		t.Fatalf("calls=%d result=%+v", implementer.calls, got)
	}
}

func domainTestManifest() domain.Manifest {
	return domain.Manifest{ContractVersion: "rencrow-durable-stores/v1", ModuleID: "RenCrow_CORE", Stores: []domain.StoreManifest{{
		StoreID: "core.conversation_l1", OwnerModule: "RenCrow_CORE", StoreKind: "sqlite", DurabilityClass: "durable", DataClasses: []string{"x_bookmark"}, CanonicalConfigKeys: []string{"storage.databases.conversation_l1"}, ProductionRootTemplate: "/srv/rencrow/db/core",
		AuthoritativeWriter: "rencrow-core", SchemaRevision: "conversation-l1/v1", MigrationOwner: "RenCrow_CORE", RetentionPolicy: "class-specific", BackupProfile: "core-snapshot/v1", RestoreCheck: "sqlite-integrity/v1", RPO: "PT24H", RTO: "PT4H", Sensitivity: "private", FallbackPolicy: "fail_closed", ChangeClass: domain.ChangeS1, ProposalRevision: 1, LifecycleStatus: domain.LifecycleValidated,
	}}}
}
