package backlog

import (
	"context"
	"errors"
	"fmt"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

const (
	legacyAtlasLifecycleItemID         = "atlas:atlas.lifecycle"
	legacyAtlasLifecycleUnitID         = "atlas-lifecycle-v1"
	legacyAtlasLifecycleTargetRevision = 2
)

// MigrateLegacyAtlasLifecycle moves the one known pre-revision-2 Atlas
// lifecycle record back to QUEUED so its evidence is revalidated by the
// revision-2 owner workflow. The backlog store owns append-only persistence;
// this method never rewrites or removes historical JSONL records.
//
// The migration is deliberately exact and idempotent. It does not infer
// completion from Passed=true, verify evidence, or touch any other item.
func (s *Service) MigrateLegacyAtlasLifecycle(ctx context.Context) (bool, error) {
	if s == nil || s.items == nil {
		return false, errors.New("atlas backlog store unavailable")
	}
	items, err := s.items.List(ctx, 5000)
	if err != nil {
		return false, fmt.Errorf("list Atlas items for lifecycle migration: %w", err)
	}
	for _, item := range items {
		if isLegacyCompletedAtlasLifecycle(item) {
			repaired, err := s.repairCompletedAtlasLifecycle(ctx, item)
			if err != nil {
				return false, err
			}
			if repaired {
				return true, nil
			}
			continue
		}
		if !isLegacyAtlasLifecycle(item) {
			continue
		}
		item.ImplementationRevision = legacyAtlasLifecycleTargetRevision
		item.InvalidatedFromStage = domainbacklog.DeliverySpec
		item.DeliveryState = domainbacklog.DeliveryQueued
		item.CheckOK = false
		item.Status = domainbacklog.LegacyStatus(item)
		if err := s.save(ctx, item); err != nil {
			return false, fmt.Errorf("save migrated Atlas lifecycle item: %w", err)
		}
		return true, nil
	}
	return false, nil
}

func (s *Service) repairCompletedAtlasLifecycle(ctx context.Context, item domainbacklog.Item) (bool, error) {
	store, ok := s.workstream.(LifecycleStore)
	if !ok {
		// A DONE repair is permitted only with an authoritative closure receipt.
		return false, nil
	}
	receipts, err := store.ListClosureReceipts(ctx, 5000)
	if err != nil {
		return false, fmt.Errorf("list Atlas lifecycle closure receipts: %w", err)
	}
	for _, receipt := range receipts {
		if receipt.ItemID != item.ItemID || receipt.UnitID != item.ImplementationUnit || receipt.ImplementationRevision != legacyAtlasLifecycleTargetRevision || receipt.Phase != domainworkstream.ClosurePhaseDone || receipt.Status != domainworkstream.ClosureStatusCompleted {
			continue
		}
		repaired := item
		repaired.ImplementationRevision = legacyAtlasLifecycleTargetRevision
		repaired.EvidenceRefs = append([]domainbacklog.EvidenceRef(nil), item.EvidenceRefs...)
		repaired.BlockerResolutionRefs = append([]domainbacklog.EvidenceRef(nil), item.BlockerResolutionRefs...)
		if err := s.items.Save(ctx, repaired); err != nil {
			return false, fmt.Errorf("save repaired Atlas lifecycle item: %w", err)
		}
		return true, nil
	}
	return false, nil
}

func isLegacyCompletedAtlasLifecycle(item domainbacklog.Item) bool {
	return item.SchemaVersion == domainbacklog.SchemaVersion2 &&
		item.ItemID == legacyAtlasLifecycleItemID &&
		item.ImplementationUnit == legacyAtlasLifecycleUnitID &&
		item.ConceptState == domainbacklog.ConceptAdopted &&
		item.DeliveryState == domainbacklog.DeliveryDone &&
		item.ImplementationRevision < legacyAtlasLifecycleTargetRevision
}

func isLegacyAtlasLifecycle(item domainbacklog.Item) bool {
	return item.SchemaVersion == domainbacklog.SchemaVersion2 &&
		item.ItemID == legacyAtlasLifecycleItemID &&
		item.ImplementationUnit == legacyAtlasLifecycleUnitID &&
		item.ConceptState == domainbacklog.ConceptAdopted &&
		item.DeliveryState == domainbacklog.DeliveryLiveVerified &&
		item.ImplementationRevision < legacyAtlasLifecycleTargetRevision
}
