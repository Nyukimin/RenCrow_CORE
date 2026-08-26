package backlog

import (
	"context"
	"errors"
	"reflect"
	"strings"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	featurebacklog "github.com/Nyukimin/RenCrow_CORE/internal/features/backlog"
)

// BackfillReconcileStore is an optional atomic batch extension on the existing
// backlog store. Stores without it use the same List/Save compatibility path.
type BackfillReconcileStore interface {
	ReconcileBackfill(context.Context, []domainbacklog.Item, domainbacklog.BackfillImportReceipt) (domainbacklog.BackfillReconcileResult, error)
}

type BackfillReconcileReport struct {
	domainbacklog.BackfillReconcileResult
	ReceiptWritten bool `json:"receipt_written"`
}

func (s *Service) ReconcileBackfill(ctx context.Context, pkg featurebacklog.BackfillPackage) (BackfillReconcileReport, error) {
	if s == nil || s.items == nil {
		return BackfillReconcileReport{}, errors.New("atlas backlog store unavailable")
	}
	if pkg.DatasetID != featurebacklog.CanonicalBackfillDatasetID || pkg.PackageSHA256 != featurebacklog.CanonicalBackfillSHA256 || pkg.Revision != featurebacklog.CanonicalBackfillRevision {
		return BackfillReconcileReport{}, errors.New("unsupported Atlas backfill package identity")
	}
	if len(pkg.Items) != 114 || len(pkg.SeedFeatures) != 114 {
		return BackfillReconcileReport{}, errors.New("incomplete Atlas backfill package")
	}
	existing, err := s.items.List(ctx, 5000)
	if err != nil {
		return BackfillReconcileReport{}, err
	}
	byID := make(map[string]domainbacklog.Item, len(existing))
	for _, item := range existing {
		byID[item.ItemID] = item
	}

	desired := make([]domainbacklog.Item, 0, len(pkg.Items))
	changed := make([]domainbacklog.Item, 0, len(pkg.Items))
	skipped := 0
	for _, source := range pkg.Items {
		incoming := backfillItemToDomain(source)
		if err := domainbacklog.ValidateItem(incoming); err != nil {
			return BackfillReconcileReport{}, err
		}
		current, found := byID[incoming.ItemID]
		if found {
			incoming = preserveRuntimeOverlay(current, incoming)
		}
		desired = append(desired, incoming)
		if found && reflect.DeepEqual(current, incoming) {
			skipped++
			continue
		}
		changed = append(changed, incoming)
	}
	receipt := domainbacklog.BackfillImportReceipt{
		RecordType:         "atlas_backfill_import",
		ImportID:           domainbacklog.BackfillImportID(pkg.PackageSHA256, pkg.Revision),
		DatasetID:          pkg.DatasetID,
		PackageSHA256:      pkg.PackageSHA256,
		Revision:           pkg.Revision,
		ItemCount:          len(pkg.Items),
		SpecificationCount: len(pkg.SpecificationArtifacts),
		ImportedAt:         s.now().Format("2006-01-02T15:04:05Z07:00"),
	}
	if batch, ok := s.items.(BackfillReconcileStore); ok {
		result, err := batch.ReconcileBackfill(ctx, changed, receipt)
		if err != nil {
			return BackfillReconcileReport{}, err
		}
		result.Skipped += skipped
		return BackfillReconcileReport{BackfillReconcileResult: result, ReceiptWritten: true}, nil
	}
	for _, item := range changed {
		if err := s.items.Save(ctx, item); err != nil {
			return BackfillReconcileReport{}, err
		}
	}
	return BackfillReconcileReport{
		BackfillReconcileResult: domainbacklog.BackfillReconcileResult{Imported: countNew(byID, desired), Updated: len(changed) - countNew(byID, desired), Skipped: skipped},
		ReceiptWritten:          false,
	}, nil
}

func backfillItemToDomain(source featurebacklog.BackfillItem) domainbacklog.Item {
	refs := make([]domainbacklog.SourceRef, 0, len(source.SourceRefs))
	for _, ref := range source.SourceRefs {
		refs = append(refs, domainbacklog.SourceRef{
			Type: ref.Type, Locator: ref.Locator, Strength: ref.Strength,
			CapturedAt: source.CreatedAt,
		})
	}
	item := domainbacklog.Item{
		SchemaVersion:         domainbacklog.SchemaVersion2,
		ItemID:                "atlas:" + strings.TrimSpace(source.FeatureID),
		FeatureID:             strings.TrimSpace(source.FeatureID),
		Kind:                  "idea",
		Title:                 strings.TrimSpace(source.Title),
		Purpose:               source.Purpose,
		Problem:               source.Problem,
		Idea:                  source.Idea,
		Background:            source.Background,
		ExpectedEffect:        append([]string(nil), source.ExpectedEffect...),
		RelationRefs:          append([]string(nil), source.RelationRefs...),
		TargetModules:         append([]string(nil), source.TargetModules...),
		ConsumerModules:       append([]string(nil), source.ConsumerModules...),
		AffectedModules:       append([]string(nil), source.AffectedModules...),
		Category:              source.Category,
		Source:                "atlas_backfill_v1",
		SourceRefs:            refs,
		OwnerModule:           domainbacklog.LifecycleOwnerModule,
		ConceptState:          strings.ToUpper(strings.TrimSpace(source.ConceptState)),
		DeliveryState:         domainbacklog.DeliveryNone,
		DeclaredDeliveryState: strings.ToUpper(strings.TrimSpace(source.DeliveryState)),
		ReconstructionBasis:   source.ReconstructionBasis,
		SpecificationRefs:     append([]string(nil), source.SpecificationRefs...),
		MigrationStatus:       source.MigrationStatus,
		OriginAtlas:           append([]string(nil), source.OriginAtlas...),
		CreatedAt:             source.CreatedAt,
		UpdatedAt:             source.UpdatedAt,
	}
	item.Status = domainbacklog.LegacyStatus(item)
	return item
}

func preserveRuntimeOverlay(current, incoming domainbacklog.Item) domainbacklog.Item {
	if current.ConceptState != "" {
		incoming.ConceptState = current.ConceptState
	}
	if current.DeliveryState != "" {
		incoming.DeliveryState = current.DeliveryState
	}
	incoming.EvidenceRefs = append([]domainbacklog.EvidenceRef(nil), current.EvidenceRefs...)
	incoming.WorkstreamID = current.WorkstreamID
	incoming.ImplementationUnit = current.ImplementationUnit
	incoming.ImplementationRevision = current.ImplementationRevision
	incoming.InvalidatedFromStage = current.InvalidatedFromStage
	incoming.SupersedesUnitID = current.SupersedesUnitID
	incoming.BlockerResolutionRefs = append([]domainbacklog.EvidenceRef(nil), current.BlockerResolutionRefs...)
	incoming.AdoptionReason = current.AdoptionReason
	incoming.AdoptedAt = current.AdoptedAt
	incoming.MaturationState = current.MaturationState
	incoming.MaturationStartedAt = current.MaturationStartedAt
	incoming.MaturationEligibleAt = current.MaturationEligibleAt
	incoming.LastMaterialChangeAt = current.LastMaterialChangeAt
	incoming.MergedInto = current.MergedInto
	incoming.NextReviewTrigger = current.NextReviewTrigger
	incoming.MaturationBypass = current.MaturationBypass
	incoming.BypassReason = current.BypassReason
	incoming.RevalidationRecords = cloneRevalidationRecords(current.RevalidationRecords)
	incoming.Owner = current.Owner
	// Lifecycle ownership is a CORE contract and is repaired even when an
	// existing record is empty or names a different module.
	incoming.OwnerModule = domainbacklog.LifecycleOwnerModule
	if len(current.TargetModules) > 0 {
		incoming.TargetModules = append([]string(nil), current.TargetModules...)
	}
	if len(current.ConsumerModules) > 0 {
		incoming.ConsumerModules = append([]string(nil), current.ConsumerModules...)
	}
	if len(current.AffectedModules) > 0 {
		incoming.AffectedModules = append([]string(nil), current.AffectedModules...)
	}
	incoming.Priority = current.Priority
	incoming.QueueRank = current.QueueRank
	incoming.Tags = append([]string(nil), current.Tags...)
	incoming.DependsOn = append([]string(nil), current.DependsOn...)
	incoming.RelatedIDs = append([]string(nil), current.RelatedIDs...)
	incoming.AcceptanceCriteria = append([]string(nil), current.AcceptanceCriteria...)
	incoming.Implementer = current.Implementer
	incoming.Implementation = current.Implementation
	incoming.TestResult = current.TestResult
	incoming.CheckedBy = current.CheckedBy
	// A legacy check_ok flag is only meaningful for terminal v2 states. Do not
	// carry a forged/nonterminal value across a backfill reconciliation.
	incoming.CheckOK = false
	incoming.Status = current.Status
	if current.CreatedAt != "" {
		incoming.CreatedAt = current.CreatedAt
	}
	if current.UpdatedAt != "" {
		incoming.UpdatedAt = current.UpdatedAt
	}
	incoming.Status = domainbacklog.LegacyStatus(incoming)
	if current.DeliveryState == domainbacklog.DeliveryLiveVerified || current.DeliveryState == domainbacklog.DeliveryDone {
		incoming.CheckOK = current.CheckOK
	}
	return incoming
}

func cloneRevalidationRecords(records []domainbacklog.RevalidationRecord) []domainbacklog.RevalidationRecord {
	if records == nil {
		return nil
	}
	out := make([]domainbacklog.RevalidationRecord, len(records))
	for index, record := range records {
		out[index] = record
		out[index].RelatedBacklogs = append([]string(nil), record.RelatedBacklogs...)
		out[index].ConflictingSpecs = append([]string(nil), record.ConflictingSpecs...)
		out[index].TechnologyChanges = append([]string(nil), record.TechnologyChanges...)
		out[index].ReviewAgents = append([]string(nil), record.ReviewAgents...)
	}
	return out
}

func countNew(existing map[string]domainbacklog.Item, desired []domainbacklog.Item) int {
	count := 0
	for _, item := range desired {
		if _, ok := existing[item.ItemID]; !ok {
			count++
		}
	}
	return count
}
