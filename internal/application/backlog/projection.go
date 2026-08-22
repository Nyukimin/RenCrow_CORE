package backlog

import (
	"context"
	"sort"
	"strings"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

// StageStatus is deliberately a small, viewer-facing vocabulary.  A claim
// such as EvidenceRef.Passed never produces one of these statuses by itself.
const (
	StageStatusPending = "pending"
	StageStatusActive  = "active"
	StageStatusPassed  = "passed"
	StageStatusFailed  = "failed"
	StageStatusBlocked = "blocked"
)

// PipelineStage is a derived stage record. EvidenceRefs retains claims for
// inspection while VerifiedEvidenceRefs contains only CORE-verified refs.
type PipelineStage struct {
	Name                 string                      `json:"name"`
	Stage                string                      `json:"stage"`
	Status               string                      `json:"status"`
	EvidenceRefs         []domainbacklog.EvidenceRef `json:"evidence_refs,omitempty"`
	VerifiedEvidenceRefs []domainbacklog.EvidenceRef `json:"verified_evidence_refs,omitempty"`
	Reason               string                      `json:"reason,omitempty"`
}

// PipelineEntry is one implementation unit, never one raw append-only item.
// Nested lifecycle records make freeze, replacement, active lease, and DONE
// closure state visible without asking the Viewer to reconstruct authority.
type PipelineEntry struct {
	UnitID                 string                                `json:"unit_id"`
	ItemID                 string                                `json:"item_id"`
	Title                  string                                `json:"title"`
	OwnerModule            string                                `json:"owner_module,omitempty"`
	ConceptState           string                                `json:"concept_state"`
	DeliveryState          string                                `json:"delivery_state"`
	ImplementationRevision int                                   `json:"implementation_revision"`
	InvalidatedFromStage   string                                `json:"invalidated_from_stage,omitempty"`
	SupersedesUnitID       string                                `json:"supersedes_unit_id,omitempty"`
	Stages                 []PipelineStage                       `json:"stages"`
	EvidenceRefs           []domainbacklog.EvidenceRef           `json:"evidence_refs,omitempty"`
	QueueFreezes           []domainworkstream.QueueFreeze        `json:"queue_freezes,omitempty"`
	ClosureReceipts        []domainworkstream.ClosureReceipt     `json:"closure_receipts,omitempty"`
	ActiveLease            *domainworkstream.ImplementationLease `json:"active_lease,omitempty"`
}

// PipelineUnit is kept as a descriptive alias for callers that used the
// domain term before the projection field was introduced.
type PipelineUnit = PipelineEntry

type pipelineStageDefinition struct {
	name  string
	state string
	kinds []string
}

var atlasPipelineStages = []pipelineStageDefinition{
	{name: "Specification", state: domainbacklog.DeliverySpec, kinds: []string{"spec"}},
	{name: "TDD Red", state: domainbacklog.DeliveryTDDRed, kinds: []string{"tdd_red"}},
	{name: "TDD Green", state: domainbacklog.DeliveryTDDGreen, kinds: []string{"tdd_green", "unit_test", "contract_test"}},
	{name: "Refactor", state: domainbacklog.DeliveryRefactor, kinds: []string{"refactor", "unit_test"}},
	{name: "E2E", state: domainbacklog.DeliveryE2EPredeploy, kinds: []string{"e2e", "e2e_predeploy"}},
	{name: "Build", state: domainbacklog.DeliveryBuild, kinds: []string{"build", "artifact"}},
	{name: "Deploy", state: domainbacklog.DeliveryDeploy, kinds: []string{"deploy", "deploy_receipt"}},
	{name: "Restart", state: domainbacklog.DeliveryRestart, kinds: []string{"restart", "restart_receipt"}},
	{name: "Verify", state: domainbacklog.DeliveryPostDeployVerify, kinds: []string{"post_deploy_verify", "health", "readiness"}},
	{name: "Live Verified", state: domainbacklog.DeliveryLiveVerified, kinds: []string{"live_verified", "production_smoke"}},
	{name: "Done", state: domainbacklog.DeliveryDone, kinds: []string{"done", "closure"}},
}

func stageIndex(state string) int {
	normalized := strings.ToUpper(strings.TrimSpace(state))
	normalized = strings.NewReplacer("-", "_", " ", "_").Replace(normalized)
	switch normalized {
	case "SPECIFICATION":
		normalized = domainbacklog.DeliverySpec
	case "TDD_RED":
		normalized = domainbacklog.DeliveryTDDRed
	case "TDD_GREEN":
		normalized = domainbacklog.DeliveryTDDGreen
	case "REFACTOR":
		normalized = domainbacklog.DeliveryRefactor
	case "E2E", "E2E_PREDEPLOY":
		normalized = domainbacklog.DeliveryE2EPredeploy
	case "BUILD", "DEPLOY", "RESTART", "POST_DEPLOY_VERIFY", "LIVE_VERIFIED", "DONE":
		// Already canonical.
	case "VERIFY":
		normalized = domainbacklog.DeliveryPostDeployVerify
	}
	for index, definition := range atlasPipelineStages {
		if definition.state == normalized {
			return index
		}
	}
	return -1
}

func verifiedEvidence(ref domainbacklog.EvidenceRef) bool {
	return ref.IsVerified()
}

func evidenceMatchesStage(ref domainbacklog.EvidenceRef, definition pipelineStageDefinition) bool {
	if strings.EqualFold(strings.TrimSpace(ref.Stage), definition.state) {
		return true
	}
	kind := strings.ToLower(strings.TrimSpace(ref.Kind))
	for _, accepted := range definition.kinds {
		if kind == accepted {
			return true
		}
	}
	return false
}

func stageEvidence(refs []domainbacklog.EvidenceRef, definition pipelineStageDefinition) (claims, verified []domainbacklog.EvidenceRef) {
	for _, ref := range refs {
		if !evidenceMatchesStage(ref, definition) {
			continue
		}
		claims = append(claims, ref)
		if verifiedEvidence(ref) {
			verified = append(verified, ref)
		}
	}
	return claims, verified
}

func unitKey(item domainbacklog.Item) string {
	if unit := strings.TrimSpace(item.ImplementationUnit); unit != "" {
		return unit
	}
	return strings.TrimSpace(item.ItemID)
}

func newerPipelineItem(current, candidate domainbacklog.Item) bool {
	if candidate.ImplementationRevision != current.ImplementationRevision {
		return candidate.ImplementationRevision > current.ImplementationRevision
	}
	if candidate.UpdatedAt != current.UpdatedAt {
		return candidate.UpdatedAt > current.UpdatedAt
	}
	return candidate.ItemID > current.ItemID
}

func latestPipelineItems(items []domainbacklog.Item) []domainbacklog.Item {
	latest := make(map[string]domainbacklog.Item, len(items))
	for _, item := range items {
		key := unitKey(item)
		if key == "" {
			continue
		}
		if current, ok := latest[key]; !ok || newerPipelineItem(current, item) {
			latest[key] = item
		}
	}
	out := make([]domainbacklog.Item, 0, len(latest))
	for _, item := range latest {
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool { return unitKey(out[i]) < unitKey(out[j]) })
	return out
}

func activeFreeze(freeze domainworkstream.QueueFreeze) bool {
	// Only an explicit RESOLVED record reopens the queue. Empty and unknown
	// statuses fail closed so a malformed receipt cannot look runnable.
	return strings.TrimSpace(freeze.Status) != domainworkstream.QueueFreezeResolved
}

func sameFreezeUnit(freeze domainworkstream.QueueFreeze, unitID string) bool {
	return strings.TrimSpace(freeze.BlockedUnitID) == unitID || strings.TrimSpace(freeze.ReplacementUnitID) == unitID || strings.TrimSpace(freeze.SupersedesUnitID) == unitID
}

func sameClosureUnit(receipt domainworkstream.ClosureReceipt, item domainbacklog.Item, unitID string) bool {
	return strings.TrimSpace(receipt.UnitID) == unitID || strings.TrimSpace(receipt.ItemID) == strings.TrimSpace(item.ItemID)
}

func sameStageReceiptUnit(receipt domainworkstream.StageRunReceipt, unitID string) bool {
	return strings.TrimSpace(receipt.UnitID) == unitID
}

func closureCompleted(receipts []domainworkstream.ClosureReceipt) bool {
	for _, receipt := range receipts {
		if receipt.Status == domainworkstream.ClosureStatusCompleted && (receipt.Phase == "" || receipt.Phase == domainworkstream.ClosurePhaseDone || receipt.LeaseReleased) {
			return true
		}
	}
	return false
}

func latestRecord[T any](records []T, key func(T) string, newer func(T, T) bool) []T {
	latest := make(map[string]T, len(records))
	for _, record := range records {
		identity := key(record)
		if identity == "" {
			continue
		}
		if current, ok := latest[identity]; !ok || newer(current, record) {
			latest[identity] = record
		}
	}
	out := make([]T, 0, len(latest))
	for _, record := range latest {
		out = append(out, record)
	}
	return out
}

func latestFreezeRecords(records []domainworkstream.QueueFreeze) []domainworkstream.QueueFreeze {
	return latestRecord(records, func(record domainworkstream.QueueFreeze) string { return strings.TrimSpace(record.FreezeID) }, func(current, candidate domainworkstream.QueueFreeze) bool {
		if candidate.FreezeRevision != current.FreezeRevision {
			return candidate.FreezeRevision > current.FreezeRevision
		}
		return candidate.UpdatedAt.After(current.UpdatedAt)
	})
}

func latestClosureRecords(records []domainworkstream.ClosureReceipt) []domainworkstream.ClosureReceipt {
	return latestRecord(records, func(record domainworkstream.ClosureReceipt) string {
		if key := strings.TrimSpace(record.IdempotencyKey); key != "" {
			return key
		}
		return strings.TrimSpace(record.ReceiptID)
	}, newerClosureReceipt)
}

func newerClosureReceipt(current, candidate domainworkstream.ClosureReceipt) bool {
	if !candidate.UpdatedAt.Equal(current.UpdatedAt) {
		return candidate.UpdatedAt.After(current.UpdatedAt)
	}
	if !candidate.CompletedAt.Equal(current.CompletedAt) {
		return candidate.CompletedAt.After(current.CompletedAt)
	}
	return closurePhaseRank(candidate.Phase) > closurePhaseRank(current.Phase)
}

func closurePhaseRank(phase string) int {
	switch strings.TrimSpace(phase) {
	case domainworkstream.ClosurePhasePrepared:
		return 1
	case domainworkstream.ClosurePhaseResources:
		return 2
	case domainworkstream.ClosurePhaseLease:
		return 3
	case domainworkstream.ClosurePhaseDone:
		return 4
	default:
		return 0
	}
}

func currentItemClosureCompleted(item domainbacklog.Item, closures []domainworkstream.ClosureReceipt) bool {
	unitID := strings.TrimSpace(item.ImplementationUnit)
	if item.SchemaVersion != domainbacklog.SchemaVersion2 || unitID == "" || item.ImplementationRevision < 1 {
		// Records without the v2 lifecycle identity retain the legacy Current
		// projection contract; they have no authoritative closure to match.
		return true
	}

	var latest domainworkstream.ClosureReceipt
	found := false
	for _, receipt := range closures {
		if strings.TrimSpace(receipt.UnitID) != unitID || receipt.ImplementationRevision != item.ImplementationRevision {
			continue
		}
		if receiptItemID := strings.TrimSpace(receipt.ItemID); receiptItemID != "" && receiptItemID != strings.TrimSpace(item.ItemID) {
			continue
		}
		if !found || newerClosureReceipt(latest, receipt) {
			latest = receipt
			found = true
		}
	}
	return found && latest.Status == domainworkstream.ClosureStatusCompleted && latest.Phase == domainworkstream.ClosurePhaseDone
}

func latestStageRecords(records []domainworkstream.StageRunReceipt) []domainworkstream.StageRunReceipt {
	return latestRecord(records, func(record domainworkstream.StageRunReceipt) string {
		if key := strings.TrimSpace(record.IdempotencyKey); key != "" {
			return key
		}
		return strings.TrimSpace(record.ReceiptID)
	}, func(current, candidate domainworkstream.StageRunReceipt) bool {
		return candidate.CompletedAt.After(current.CompletedAt) || candidate.CreatedAt.After(current.CreatedAt)
	})
}

func stageReceiptFor(records []domainworkstream.StageRunReceipt, definition pipelineStageDefinition) (domainworkstream.StageRunReceipt, bool) {
	for _, receipt := range records {
		if strings.EqualFold(strings.TrimSpace(receipt.TargetStage), definition.state) {
			return receipt, true
		}
	}
	return domainworkstream.StageRunReceipt{}, false
}

func pipelineStages(item domainbacklog.Item, freezes []domainworkstream.QueueFreeze, closures []domainworkstream.ClosureReceipt, stageReceipts []domainworkstream.StageRunReceipt, lease *domainworkstream.ImplementationLease) []PipelineStage {
	stages := make([]PipelineStage, len(atlasPipelineStages))
	for index, definition := range atlasPipelineStages {
		claims, verified := stageEvidence(item.EvidenceRefs, definition)
		stages[index] = PipelineStage{Name: definition.name, Stage: definition.state, Status: StageStatusPending, EvidenceRefs: claims, VerifiedEvidenceRefs: verified}
	}

	currentIndex := stageIndex(item.DeliveryState)
	if item.DeliveryState == domainbacklog.DeliveryQueued || item.DeliveryState == domainbacklog.DeliveryNone {
		currentIndex = -1
	}
	closureDone := closureCompleted(closures)
	activeLease := lease != nil
	freezeIsActive := false
	for _, freeze := range freezes {
		if activeFreeze(freeze) {
			freezeIsActive = true
			break
		}
	}

	failureIndex := stageIndex(item.InvalidatedFromStage)
	if failureIndex < 0 && (item.DeliveryState == domainbacklog.DeliveryBlocked || item.DeliveryState == domainbacklog.DeliveryRejected) {
		if lease != nil {
			failureIndex = stageIndex(lease.Stage)
		}
		if failureIndex < 0 {
			failureIndex = 0
		}
	}

	for index, definition := range atlasPipelineStages {
		_, verified := stageEvidence(item.EvidenceRefs, definition)
		hasVerifiedEvidence := len(verified) > 0
		if index == len(atlasPipelineStages)-1 {
			hasVerifiedEvidence = closureDone
		}
		if receipt, ok := stageReceiptFor(stageReceipts, definition); ok && receipt.Status == domainworkstream.StageRunFailed {
			stages[index].Status = StageStatusFailed
			stages[index].Reason = firstNonEmpty(receipt.ReasonCode, receipt.Error)
		}

		if item.DeliveryState == domainbacklog.DeliveryBlocked && failureIndex >= 0 && index >= failureIndex {
			stages[index].Status = StageStatusBlocked
			if stages[index].Reason == "" {
				stages[index].Reason = firstNonEmpty(item.Implementation, "queue frozen")
			}
			continue
		}
		if item.DeliveryState == domainbacklog.DeliveryRejected && failureIndex >= 0 && index == failureIndex {
			stages[index].Status = StageStatusFailed
			if stages[index].Reason == "" {
				stages[index].Reason = firstNonEmpty(item.Implementation, "rejected")
			}
			continue
		}
		if index == len(atlasPipelineStages)-1 {
			if item.DeliveryState == domainbacklog.DeliveryDone && closureDone {
				stages[index].Status = StageStatusPassed
			} else if activeLease && item.DeliveryState == domainbacklog.DeliveryLiveVerified && !freezeIsActive {
				stages[index].Status = StageStatusActive
			}
			continue
		}
		if currentIndex >= 0 && index < currentIndex {
			if hasVerifiedEvidence {
				stages[index].Status = StageStatusPassed
			}
			continue
		}
		if currentIndex >= 0 && index == currentIndex {
			if activeLease && !freezeIsActive {
				stages[index].Status = StageStatusActive
			} else if hasVerifiedEvidence {
				stages[index].Status = StageStatusPassed
			}
			continue
		}
		if currentIndex < 0 && index == 0 && activeLease && !freezeIsActive {
			stages[index].Status = StageStatusActive
		}
	}
	if freezeIsActive && item.DeliveryState != domainbacklog.DeliveryBlocked {
		// A freeze attached to a replacement or stale unit is still visible, but
		// never turns an unrelated current stage into a claimed success.
		for index := range stages {
			if stages[index].Status == StageStatusActive {
				stages[index].Status = StageStatusBlocked
				stages[index].Reason = "queue frozen"
			}
		}
	}
	return stages
}

func projectModules(modules []map[string]any) []map[string]any {
	out := cloneMaps(modules)
	for _, module := range out {
		available, _ := module["runtime_evidence_available"].(bool)
		if !available {
			if value, ok := module["revision"]; ok && strings.TrimSpace(toString(value)) != "" {
				module["catalog_revision"] = value
			}
			if value, ok := module["runtime_health"]; ok && strings.TrimSpace(toString(value)) != "" {
				module["catalog_runtime_health"] = value
			}
			module["revision"] = "unavailable"
			module["runtime_health"] = "unavailable"
			module["last_verified"] = "unavailable"
			module["runtime_evidence_available"] = false
		} else {
			if strings.TrimSpace(toString(module["revision"])) == "" {
				module["revision"] = "unavailable"
			}
			if strings.TrimSpace(toString(module["runtime_health"])) == "" {
				module["runtime_health"] = "unavailable"
			}
			if strings.TrimSpace(toString(module["last_verified"])) == "" {
				module["last_verified"] = "unavailable"
			}
		}
	}
	return out
}

func toString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return ""
	}
}

func (s *Service) buildProjection(ctx context.Context) (Projection, error) {
	items, err := s.list(ctx)
	if err != nil {
		return Projection{}, err
	}
	store, ok := s.workstream.(LifecycleStore)
	if !ok {
		return Projection{}, ErrLifecycleStoreUnavailable
	}
	freezes, err := store.ListQueueFreezes(ctx, 5000)
	if err != nil {
		return Projection{}, err
	}
	closures, err := store.ListClosureReceipts(ctx, 5000)
	if err != nil {
		return Projection{}, err
	}
	stageReceipts, err := store.ListStageRunReceipts(ctx, 5000)
	if err != nil {
		return Projection{}, err
	}
	freezes = latestFreezeRecords(freezes)
	closures = latestClosureRecords(closures)
	stageReceipts = latestStageRecords(stageReceipts)
	lease, leaseFound, err := s.getLease(ctx, domainbacklog.ImplementationLeaseName)
	if err != nil {
		return Projection{}, err
	}

	p := Projection{
		Catalog: cloneMaps(s.catalog), Features: cloneMaps(s.features), Modules: projectModules(s.modules),
		Current: []domainbacklog.Item{}, Radar: []domainbacklog.Item{}, Backlog: []domainbacklog.Item{}, Queue: []domainbacklog.Item{}, Evidence: []domainbacklog.EvidenceRef{}, Pipeline: []PipelineEntry{},
		QueueFreezes: append([]domainworkstream.QueueFreeze(nil), freezes...), ClosureReceipts: append([]domainworkstream.ClosureReceipt(nil), closures...), StageReceipts: append([]domainworkstream.StageRunReceipt(nil), stageReceipts...),
	}
	for _, item := range items {
		switch item.ConceptState {
		case domainbacklog.ConceptRadar:
			p.Radar = append(p.Radar, item)
		case domainbacklog.ConceptCandidate, domainbacklog.ConceptAdopted, domainbacklog.ConceptDeferred, domainbacklog.ConceptRejected:
			p.Backlog = append(p.Backlog, item)
		}
		if item.DeliveryState == domainbacklog.DeliveryDone && currentItemClosureCompleted(item, closures) {
			p.Current = append(p.Current, item)
		}
		p.Evidence = append(p.Evidence, item.EvidenceRefs...)
	}
	p.Queue = s.queue(items)
	if leaseFound {
		for i := range items {
			if unitKey(items[i]) == strings.TrimSpace(lease.HolderUnitID) {
				active := items[i]
				p.Active = &active
				break
			}
		}
	}
	for _, item := range latestPipelineItems(items) {
		unitID := unitKey(item)
		entry := PipelineEntry{
			UnitID: unitID, ItemID: item.ItemID, Title: item.Title, OwnerModule: item.OwnerModule,
			ConceptState: item.ConceptState, DeliveryState: item.DeliveryState, ImplementationRevision: item.ImplementationRevision,
			InvalidatedFromStage: item.InvalidatedFromStage, SupersedesUnitID: item.SupersedesUnitID,
			Stages: pipelineStages(item, nil, nil, nil, nil), EvidenceRefs: append([]domainbacklog.EvidenceRef(nil), item.EvidenceRefs...),
		}
		for _, freeze := range freezes {
			if sameFreezeUnit(freeze, unitID) {
				entry.QueueFreezes = append(entry.QueueFreezes, freeze)
			}
		}
		for _, closure := range closures {
			if sameClosureUnit(closure, item, unitID) {
				entry.ClosureReceipts = append(entry.ClosureReceipts, closure)
			}
		}
		var activeLease *domainworkstream.ImplementationLease
		if leaseFound && strings.TrimSpace(lease.HolderUnitID) == unitID {
			copyLease := lease
			activeLease = &copyLease
			entry.ActiveLease = activeLease
		}
		entry.Stages = pipelineStages(item, entry.QueueFreezes, entry.ClosureReceipts, func() []domainworkstream.StageRunReceipt {
			records := make([]domainworkstream.StageRunReceipt, 0)
			for _, receipt := range stageReceipts {
				if sameStageReceiptUnit(receipt, unitID) {
					records = append(records, receipt)
				}
			}
			return records
		}(), activeLease)
		p.Pipeline = append(p.Pipeline, entry)
	}
	return p, nil
}
