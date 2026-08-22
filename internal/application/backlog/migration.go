package backlog

import (
	"context"
	"errors"
	"fmt"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
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

func isLegacyAtlasLifecycle(item domainbacklog.Item) bool {
	return item.SchemaVersion == domainbacklog.SchemaVersion2 &&
		item.ItemID == legacyAtlasLifecycleItemID &&
		item.ImplementationUnit == legacyAtlasLifecycleUnitID &&
		item.ConceptState == domainbacklog.ConceptAdopted &&
		item.DeliveryState == domainbacklog.DeliveryLiveVerified &&
		item.ImplementationRevision < legacyAtlasLifecycleTargetRevision
}
