package durablestore

import (
	"context"
	"errors"
	"testing"

	domain "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
)

type memoryStore struct {
	byKey         map[string]domain.WorkflowResult
	byRequirement map[string]domain.WorkflowResult
	receipts      map[string]domain.RequestReceipt
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
	if s.byKey == nil {
		return nil, nil
	}
	r, ok := s.byKey[key]
	if !ok {
		return nil, nil
	}
	return &r, nil
}

func (s *memoryStore) FindByRequestID(_ context.Context, requestID string) (*domain.RequestReceipt, error) {
	if s.receipts == nil {
		return nil, nil
	}
	r, ok := s.receipts[requestID]
	if !ok {
		return nil, nil
	}
	return &r, nil
}

func (s *memoryStore) FindByRequirementID(_ context.Context, requirementID string) (*domain.WorkflowResult, error) {
	if s.byRequirement != nil {
		if r, ok := s.byRequirement[requirementID]; ok {
			return &r, nil
		}
	}
	for _, r := range s.byKey {
		if r.Requirement.RequirementID == requirementID {
			return &r, nil
		}
	}
	return nil, nil
}

func (s *memoryStore) SaveWithReceipt(_ context.Context, result *domain.WorkflowResult, receipt domain.RequestReceipt) error {
	if s.receipts == nil {
		s.receipts = map[string]domain.RequestReceipt{}
	}
	if _, exists := s.receipts[receipt.RequestID]; exists {
		return ErrRequestConflict
	}
	if result != nil {
		if s.byKey == nil {
			s.byKey = map[string]domain.WorkflowResult{}
		}
		if s.byRequirement == nil {
			s.byRequirement = map[string]domain.WorkflowResult{}
		}
		if _, exists := s.byKey[result.Requirement.DedupeKey]; exists {
			return ErrRequestConflict
		}
		s.byKey[result.Requirement.DedupeKey] = *result
		s.byRequirement[result.Requirement.RequirementID] = *result
	}
	s.receipts[receipt.RequestID] = receipt
	return nil
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

func TestServiceRequestReceiptReplayConflictAndSemanticDedupe(t *testing.T) {
	store := &memoryStore{}
	svc := NewService([]domain.Manifest{domainTestManifest()}, store, nil)
	input := Input{RequestID: "request-1", TraceID: "trace-1", RequestedBy: "shiro", UserScope: "user-1", Message: "XのBookmarkを保存するDBの設計を確認して"}
	first, handled, err := svc.Handle(context.Background(), input)
	if err != nil || !handled {
		t.Fatalf("first handled=%v err=%v", handled, err)
	}
	replayed, handled, err := svc.Handle(context.Background(), input)
	if err != nil || !handled || !replayed.RequestReplay || replayed.Requirement.RequirementID != first.Requirement.RequirementID {
		t.Fatalf("replayed=%+v handled=%v err=%v", replayed, handled, err)
	}
	changed := input
	changed.Message = "XのBookmarkを保存するDBを別方式で実装して"
	if _, handled, err := svc.Handle(context.Background(), changed); !handled || !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("changed handled=%v err=%v, want request conflict", handled, err)
	}
	if len(store.byKey) != 1 || len(store.receipts) != 1 {
		t.Fatalf("conflicting request mutated store: byKey=%d receipts=%d", len(store.byKey), len(store.receipts))
	}
	semantic, handled, err := svc.Handle(context.Background(), Input{RequestID: "request-2", TraceID: "trace-2", RequestedBy: "shiro", UserScope: "user-1", Message: input.Message})
	if err != nil || !handled || semantic.RequestReplay || !semantic.Deduplicated || semantic.Requirement.RequirementID != first.Requirement.RequirementID {
		t.Fatalf("semantic result=%+v handled=%v err=%v", semantic, handled, err)
	}
	if receipt, ok := store.receipts["request-2"]; !ok || receipt.RequirementID != first.Requirement.RequirementID {
		t.Fatalf("semantic receipt=%+v", receipt)
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
