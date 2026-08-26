package backlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	featurebacklog "github.com/Nyukimin/RenCrow_CORE/internal/features/backlog"
)

type ItemStore interface {
	List(context.Context, int) ([]domainbacklog.Item, error)
	Save(context.Context, domainbacklog.Item) error
}

type WorkstreamCreator interface {
	SaveWorkstream(context.Context, domainworkstream.Workstream) error
	SaveGoal(context.Context, domainworkstream.Goal) error
	SaveArtifact(context.Context, domainworkstream.Artifact) error
}

// ImplementationLeaseStore is optional so existing Workstream test doubles
// and adapters do not need a signature-wide migration.
type ImplementationLeaseStore interface {
	AcquireImplementationLease(context.Context, domainworkstream.ImplementationLease) (bool, error)
	ReleaseImplementationLease(context.Context, string, string) error
	GetImplementationLease(context.Context, string) (domainworkstream.ImplementationLease, bool, error)
	HeartbeatImplementationLease(context.Context, domainworkstream.ImplementationLease) error
}

type IntakeRequest struct {
	ItemID             string                    `json:"item_id,omitempty"`
	FeatureID          string                    `json:"feature_id,omitempty"`
	Kind               string                    `json:"kind,omitempty"`
	Title              string                    `json:"title"`
	Body               string                    `json:"body,omitempty"`
	Purpose            string                    `json:"purpose,omitempty"`
	Problem            string                    `json:"problem,omitempty"`
	Idea               string                    `json:"idea,omitempty"`
	Background         string                    `json:"background,omitempty"`
	ExpectedEffect     []string                  `json:"expected_effect,omitempty"`
	RelationRefs       []string                  `json:"relation_refs,omitempty"`
	Category           string                    `json:"category,omitempty"`
	Source             string                    `json:"source,omitempty"`
	SourceRefs         []domainbacklog.SourceRef `json:"source_refs,omitempty"`
	SpecificationRefs  []string                  `json:"specification_refs,omitempty"`
	Owner              string                    `json:"owner,omitempty"`
	OwnerModule        string                    `json:"owner_module,omitempty"`
	Priority           string                    `json:"priority,omitempty"`
	Tags               []string                  `json:"tags,omitempty"`
	DependsOn          []string                  `json:"depends_on,omitempty"`
	RelatedIDs         []string                  `json:"related_ids,omitempty"`
	TargetModules      []string                  `json:"target_modules,omitempty"`
	ConsumerModules    []string                  `json:"consumer_modules,omitempty"`
	AffectedModules    []string                  `json:"affected_modules,omitempty"`
	AcceptanceCriteria []string                  `json:"acceptance_criteria,omitempty"`
	Reason             string                    `json:"reason,omitempty"`
}

type ReviseRequest struct {
	RequestID           string                      `json:"request_id,omitempty"`
	ExpectedRevision    int                         `json:"expected_revision,omitempty"`
	TargetDeliveryState string                      `json:"delivery_state"`
	EvidenceRefs        []domainbacklog.EvidenceRef `json:"evidence_refs"`
	Reason              string                      `json:"reason,omitempty"`
}

type IntakeResult struct {
	Item      domainbacklog.Item `json:"item"`
	ItemID    string             `json:"item_id"`
	Duplicate bool               `json:"duplicate"`
}

type AdoptionResult struct {
	Item          domainbacklog.Item                   `json:"item"`
	Unit          domainbacklog.ImplementationUnit     `json:"implementation_unit"`
	Lease         domainworkstream.ImplementationLease `json:"lease,omitempty"`
	LeaseAcquired bool                                 `json:"lease_acquired"`
}

type Projection struct {
	Catalog           []map[string]any            `json:"catalog"`
	Features          []map[string]any            `json:"features"`
	Current           []domainbacklog.Item        `json:"current"`
	Radar             []domainbacklog.Item        `json:"radar"`
	Backlog           []domainbacklog.Item        `json:"backlog"`
	Queue             []domainbacklog.Item        `json:"queue"`
	Active            *domainbacklog.Item         `json:"active"`
	Evidence          []domainbacklog.EvidenceRef `json:"evidence"`
	Modules           []map[string]any            `json:"modules"`
	Pipeline          []PipelineEntry             `json:"pipeline"`
	MaturationMetrics MaturationMetrics           `json:"maturation_metrics"`
	// QueueFreezes and ClosureReceipts are the lifecycle-store projection used
	// by the Viewer to explain a blocked queue or an in-flight DONE closure.
	QueueFreezes    []domainworkstream.QueueFreeze     `json:"queue_freezes"`
	ClosureReceipts []domainworkstream.ClosureReceipt  `json:"closure_receipts"`
	StageReceipts   []domainworkstream.StageRunReceipt `json:"stage_receipts"`
}

type Service struct {
	items      ItemStore
	workstream WorkstreamCreator
	clock      func() time.Time
	verifier   EvidenceVerifier
	evaluator  RevalidationEvaluator

	catalog  []map[string]any
	features []map[string]any
	modules  []map[string]any
}

func NewService(items ItemStore, workstream WorkstreamCreator) *Service {
	return &Service{items: items, workstream: workstream, clock: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) WithClock(clock func() time.Time) *Service {
	if clock != nil {
		s.clock = clock
	}
	return s
}

func (s *Service) WithCatalog(catalog []map[string]any) *Service {
	s.catalog = cloneMaps(catalog)
	return s
}

func (s *Service) WithFeatures(features []map[string]any) *Service {
	s.features = cloneMaps(features)
	return s
}

func (s *Service) WithModules(modules []map[string]any) *Service {
	s.modules = cloneMaps(modules)
	return s
}

func cloneMaps(input []map[string]any) []map[string]any {
	if input == nil {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(input))
	for _, item := range input {
		copyItem := make(map[string]any, len(item))
		for key, value := range item {
			copyItem[key] = value
		}
		out = append(out, copyItem)
	}
	return out
}

func (s *Service) now() time.Time {
	if s != nil && s.clock != nil {
		return s.clock().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) list(ctx context.Context) ([]domainbacklog.Item, error) {
	if s == nil || s.items == nil {
		return nil, errors.New("atlas backlog store unavailable")
	}
	items, err := s.items.List(ctx, 5000)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i] = domainbacklog.ProjectLegacy(items[i])
	}
	return items, nil
}

func (s *Service) find(ctx context.Context, id string) (domainbacklog.Item, error) {
	items, err := s.list(ctx)
	if err != nil {
		return domainbacklog.Item{}, err
	}
	for _, item := range items {
		if item.ItemID == strings.TrimSpace(id) {
			return item, nil
		}
	}
	return domainbacklog.Item{}, fmt.Errorf("atlas item %q not found", id)
}

func (s *Service) findByUnit(ctx context.Context, unitID string) (domainbacklog.Item, error) {
	unitID = strings.TrimSpace(unitID)
	if unitID == "" {
		return domainbacklog.Item{}, errors.New("implementation unit id is required")
	}
	items, err := s.list(ctx)
	if err != nil {
		return domainbacklog.Item{}, err
	}
	for _, item := range items {
		if item.ImplementationUnit == unitID {
			return item, nil
		}
	}
	return domainbacklog.Item{}, fmt.Errorf("implementation unit %q not found", unitID)
}

func (s *Service) save(ctx context.Context, item domainbacklog.Item) error {
	if s.items == nil {
		return errors.New("atlas backlog store unavailable")
	}
	if item.CreatedAt == "" {
		item.CreatedAt = s.now().Format(time.RFC3339)
	}
	item.UpdatedAt = s.now().Format(time.RFC3339)
	item.SchemaVersion = domainbacklog.SchemaVersion2
	if item.ImplementationRevision < 1 {
		item.ImplementationRevision = 1
	}
	if item.ConceptState == "" {
		item.ConceptState = domainbacklog.ConceptCandidate
	}
	if item.DeliveryState == "" {
		item.DeliveryState = domainbacklog.DeliveryNone
	}
	item.Status = domainbacklog.LegacyStatus(item)
	item.CheckOK = item.DeliveryState == domainbacklog.DeliveryLiveVerified || item.DeliveryState == domainbacklog.DeliveryDone
	return s.items.Save(ctx, item)
}

func (s *Service) Intake(ctx context.Context, request IntakeRequest) (IntakeResult, error) {
	if strings.TrimSpace(request.Title) == "" {
		return IntakeResult{}, errors.New("title is required")
	}
	specificationRefs := append([]string(nil), request.SpecificationRefs...)
	if err := validateSpecificationRefs(specificationRefs); err != nil {
		return IntakeResult{}, err
	}
	refs := append([]domainbacklog.SourceRef(nil), request.SourceRefs...)
	if len(refs) == 0 {
		locator := strings.TrimSpace(request.Source)
		if locator == "" {
			locator = strings.TrimSpace(request.Title)
		}
		refs = append(refs, domainbacklog.SourceRef{Type: "manual", Locator: locator, RawOrSummary: strings.TrimSpace(request.Body), CapturedAt: s.now().Format(time.RFC3339)})
	}
	for i := range refs {
		if strings.TrimSpace(refs[i].CapturedAt) == "" {
			refs[i].CapturedAt = s.now().Format(time.RFC3339)
		}
		if err := domainbacklog.ValidateSourceRef(refs[i]); err != nil {
			return IntakeResult{}, err
		}
	}
	items, err := s.list(ctx)
	if err != nil {
		return IntakeResult{}, err
	}
	keys := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		keys[ref.DedupeKey()] = struct{}{}
	}
	for _, existing := range items {
		for _, ref := range existing.SourceRefs {
			if _, ok := keys[ref.DedupeKey()]; ok && ref.DedupeKey() != "\x00\x00" {
				return IntakeResult{Item: existing, ItemID: existing.ItemID, Duplicate: true}, nil
			}
		}
	}
	now := s.now()
	id := strings.TrimSpace(request.ItemID)
	if id == "" {
		id = domainbacklog.NewDeterministicID(refs, request.Title)
	}
	item := domainbacklog.Item{
		SchemaVersion: domainbacklog.SchemaVersion2, ItemID: id, FeatureID: request.FeatureID,
		Kind: request.Kind, Title: strings.TrimSpace(request.Title), Body: strings.TrimSpace(request.Body),
		Purpose: strings.TrimSpace(request.Purpose), Problem: request.Problem, Idea: request.Idea, Background: request.Background,
		ExpectedEffect: append([]string(nil), request.ExpectedEffect...), RelationRefs: append([]string(nil), request.RelationRefs...),
		TargetModules:   append([]string(nil), request.TargetModules...),
		ConsumerModules: append([]string(nil), request.ConsumerModules...),
		AffectedModules: append([]string(nil), request.AffectedModules...), AcceptanceCriteria: append([]string(nil), request.AcceptanceCriteria...),
		Category: strings.TrimSpace(request.Category), Source: strings.TrimSpace(request.Source), SourceRefs: refs,
		SpecificationRefs: specificationRefs,
		Owner:             strings.TrimSpace(request.Owner), OwnerModule: domainbacklog.LifecycleOwnerModule,
		ConceptState: domainbacklog.ConceptRadar, DeliveryState: domainbacklog.DeliveryNone,
		Priority: request.Priority, Tags: append([]string(nil), request.Tags...), DependsOn: append([]string(nil), request.DependsOn...), RelatedIDs: append([]string(nil), request.RelatedIDs...),
		CreatedAt: now.Format(time.RFC3339), UpdatedAt: now.Format(time.RFC3339), Status: "open",
	}
	item.AdoptionReason = strings.TrimSpace(request.Reason)
	if err := s.save(ctx, item); err != nil {
		return IntakeResult{}, err
	}
	return IntakeResult{Item: item, ItemID: item.ItemID}, nil
}

func validateSpecificationRefs(specificationRefs []string) error {
	if len(specificationRefs) == 0 {
		return nil
	}
	pkg, err := featurebacklog.LoadBackfillPackage()
	if err != nil {
		return fmt.Errorf("load embedded Atlas specification package: %w", err)
	}
	for _, specID := range specificationRefs {
		if strings.TrimSpace(specID) == "" {
			return errors.New("specification reference is required")
		}
		if _, ok := pkg.Specification(specID); !ok {
			return fmt.Errorf("unknown Atlas specification reference %q", specID)
		}
	}
	return nil
}

func (s *Service) Candidate(ctx context.Context, id string) (domainbacklog.Item, error) {
	item, err := s.find(ctx, id)
	if err != nil {
		return domainbacklog.Item{}, err
	}
	if strings.TrimSpace(item.Purpose) == "" {
		return domainbacklog.Item{}, errors.New("purpose is required for candidate promotion")
	}
	if item.ConceptState == domainbacklog.ConceptCandidate {
		item.SchemaVersion = domainbacklog.SchemaVersion2
		if err := ensureMaturationFields(&item, s.now(), false); err != nil {
			return domainbacklog.Item{}, err
		}
		if err := s.save(ctx, item); err != nil {
			return domainbacklog.Item{}, err
		}
		return item, nil
	}
	if err := domainbacklog.ValidateConceptTransition(item.ConceptState, domainbacklog.ConceptCandidate); err != nil {
		return domainbacklog.Item{}, err
	}
	item.SchemaVersion = domainbacklog.SchemaVersion2
	item.ConceptState = domainbacklog.ConceptCandidate
	item.DeliveryState = domainbacklog.DeliveryNone
	if err := ensureMaturationFields(&item, s.now(), true); err != nil {
		return domainbacklog.Item{}, err
	}
	return item, s.save(ctx, item)
}

func (s *Service) Defer(ctx context.Context, id, reason string) (domainbacklog.Item, error) {
	return s.changeConcept(ctx, id, domainbacklog.ConceptDeferred, reason)
}

func (s *Service) Reject(ctx context.Context, id, reason string) (domainbacklog.Item, error) {
	return s.changeConcept(ctx, id, domainbacklog.ConceptRejected, reason)
}

func (s *Service) changeConcept(ctx context.Context, id, target, reason string) (domainbacklog.Item, error) {
	item, err := s.find(ctx, id)
	if err != nil {
		return domainbacklog.Item{}, err
	}
	if err := domainbacklog.ValidateConceptTransition(item.ConceptState, target); err != nil {
		return domainbacklog.Item{}, err
	}
	item.SchemaVersion = domainbacklog.SchemaVersion2
	item.ConceptState = target
	if strings.TrimSpace(reason) != "" {
		item.AdoptionReason = strings.TrimSpace(reason)
	}
	if target == domainbacklog.ConceptRejected {
		item.DeliveryState = domainbacklog.DeliveryRejected
	}
	return item, s.save(ctx, item)
}

func (s *Service) Adopt(ctx context.Context, id, reason string) (AdoptionResult, error) {
	if strings.TrimSpace(reason) == "" {
		return AdoptionResult{}, errors.New("adoption reason is required")
	}
	item, err := s.find(ctx, id)
	if err != nil {
		return AdoptionResult{}, err
	}
	if item.ConceptState != domainbacklog.ConceptCandidate {
		return AdoptionResult{}, fmt.Errorf("atlas item %s is %s, want %s", id, item.ConceptState, domainbacklog.ConceptCandidate)
	}
	if strings.TrimSpace(item.MaturationState) != domainbacklog.MaturationStatePromoted {
		return AdoptionResult{}, fmt.Errorf("atlas item %s is not maturation PROMOTED", id)
	}
	now := s.now()
	item.SchemaVersion = domainbacklog.SchemaVersion2
	item.ConceptState = domainbacklog.ConceptAdopted
	item.DeliveryState = domainbacklog.DeliveryQueued
	item.AdoptionReason = strings.TrimSpace(reason)
	item.AdoptedAt = now.Format(time.RFC3339)
	item.ImplementationUnit = "unit_atlas_" + safeSegment(item.ItemID)
	item.WorkstreamID = "ws_atlas_" + safeSegment(item.ItemID)

	lease := domainworkstream.ImplementationLease{
		LeaseName: domainbacklog.ImplementationLeaseName, HolderUnitID: item.ImplementationUnit,
		HolderWorkstreamID: item.WorkstreamID, Stage: domainbacklog.DeliveryQueued,
		AcquiredAt: now, HeartbeatAt: now,
	}
	acquired, err := s.acquireLease(ctx, lease)
	if err != nil {
		return AdoptionResult{}, err
	}
	if s.workstream != nil {
		status := domainworkstream.StatusWaiting
		if acquired {
			status = domainworkstream.StatusActive
		}
		if err := s.workstream.SaveWorkstream(ctx, domainworkstream.Workstream{
			WorkstreamID: item.WorkstreamID, Name: "Atlas: " + item.Title,
			Description: "Implementation Unit " + item.ImplementationUnit + " for Atlas item " + item.ItemID,
			Status:      status, PrimaryAgent: "Coder", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			if acquired {
				_ = s.releaseLease(ctx, lease.LeaseName, lease.HolderUnitID)
			}
			return AdoptionResult{}, err
		}
		if err := s.workstream.SaveGoal(ctx, domainworkstream.Goal{
			GoalID: "goal_atlas_" + safeSegment(item.ItemID), WorkstreamID: item.WorkstreamID,
			Title: item.Title, Description: item.Body,
			SuccessCriteria: []string{"required Atlas evidence exists", "the unit reaches LIVE_VERIFIED before DONE"},
			Verification:    []string{"owner API stage transitions", "post-deploy/readiness evidence"},
			Status:          domainworkstream.StatusWaiting, CreatedAt: now,
		}); err != nil {
			return AdoptionResult{}, err
		}
		if err := s.workstream.SaveArtifact(ctx, domainworkstream.Artifact{
			ArtifactID: "artifact_atlas_" + safeSegment(item.ItemID), WorkstreamID: item.WorkstreamID,
			Type: "atlas_implementation_unit", Title: item.Title, Status: "pending", CreatedAt: now,
		}); err != nil {
			return AdoptionResult{}, err
		}
	}
	if err := s.save(ctx, item); err != nil {
		if acquired {
			_ = s.releaseLease(ctx, lease.LeaseName, lease.HolderUnitID)
		}
		return AdoptionResult{}, err
	}
	return AdoptionResult{Item: item, Unit: item.Unit(), Lease: lease, LeaseAcquired: acquired}, nil
}

func safeSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "item"
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func (s *Service) Revise(ctx context.Context, id string, request ReviseRequest) (domainbacklog.Item, error) {
	// Never mutate the caller's slice while replacing claims with the owner
	// verifier result; callers may replay the exact request value.
	request.EvidenceRefs = append([]domainbacklog.EvidenceRef(nil), request.EvidenceRefs...)
	item, err := s.find(ctx, id)
	if err != nil {
		return domainbacklog.Item{}, err
	}
	if item.ConceptState != domainbacklog.ConceptAdopted {
		return domainbacklog.Item{}, fmt.Errorf("atlas item %s is not adopted", id)
	}
	item = domainbacklog.ProjectLegacy(item)
	if request.ExpectedRevision > 0 && request.ExpectedRevision != item.ImplementationRevision {
		return domainbacklog.Item{}, fmt.Errorf("%w: expected revision %d, current %d", ErrLifecycleConflict, request.ExpectedRevision, item.ImplementationRevision)
	}
	revision := item.ImplementationRevision
	if revision < 1 {
		revision = 1
	}
	unitID := strings.TrimSpace(item.ImplementationUnit)
	if unitID == "" {
		return domainbacklog.Item{}, errors.New("adopted Atlas item has no implementation unit")
	}
	target := strings.ToUpper(strings.TrimSpace(request.TargetDeliveryState))
	if target == "" {
		return domainbacklog.Item{}, errors.New("target delivery state is required")
	}
	for index, ref := range request.EvidenceRefs {
		if err := domainbacklog.ValidateEvidenceRef(ref); err != nil {
			return domainbacklog.Item{}, err
		}
		// Normalize the caller claim before hashing. Verification metadata is
		// produced only by the CORE verifier and must not alter idempotency.
		normalized, normalizeErr := normalizeEvidenceRefStage(clearVerification(ref), target)
		if normalizeErr != nil {
			return domainbacklog.Item{}, normalizeErr
		}
		request.EvidenceRefs[index] = normalized
	}
	key := stageRunKey(unitID, revision, target)
	payloadHash := stagePayloadHash(request)
	existingReceipt, receiptFound, lookupErr := s.findStageReceipt(ctx, key)
	if lookupErr != nil {
		return domainbacklog.Item{}, lookupErr
	}
	if receiptFound {
		if existingReceipt.PayloadHash != payloadHash {
			return domainbacklog.Item{}, fmt.Errorf("%w: stage %s", ErrLifecycleConflict, key)
		}
		if existingReceipt.Status == domainworkstream.StageRunCompleted && strings.TrimSpace(existingReceipt.ResultJSON) != "" {
			var original domainbacklog.Item
			if err := json.Unmarshal([]byte(existingReceipt.ResultJSON), &original); err != nil {
				return domainbacklog.Item{}, fmt.Errorf("decode stage receipt result: %w", err)
			}
			if target == domainbacklog.DeliveryLiveVerified && original.DeliveryState == domainbacklog.DeliveryLiveVerified {
				if strings.TrimSpace(request.RequestID) == "" {
					request.RequestID = existingReceipt.RequestID
				}
				return s.completeLiveVerifiedClosure(ctx, original, request)
			}
			return original, nil
		}
		// A prepared receipt means the process may have stopped between
		// persistence and the state mutation.  Continue the same operation;
		// if the target state is already present, only finalize its receipt.
		if existingReceipt.Status != domainworkstream.StageRunPrepared {
			return domainbacklog.Item{}, fmt.Errorf("atlas stage receipt %s has status %q", key, existingReceipt.Status)
		}
		if strings.EqualFold(item.DeliveryState, target) && strings.TrimSpace(existingReceipt.ResultJSON) != "" {
			var original domainbacklog.Item
			if err := json.Unmarshal([]byte(existingReceipt.ResultJSON), &original); err != nil {
				return domainbacklog.Item{}, fmt.Errorf("decode prepared stage receipt result: %w", err)
			}
			existingReceipt.Status = domainworkstream.StageRunCompleted
			existingReceipt.CompletedAt = s.now()
			if err := s.saveStageReceipt(ctx, existingReceipt); err != nil {
				return domainbacklog.Item{}, err
			}
			if target == domainbacklog.DeliveryLiveVerified && original.DeliveryState == domainbacklog.DeliveryLiveVerified {
				if strings.TrimSpace(request.RequestID) == "" {
					request.RequestID = existingReceipt.RequestID
				}
				return s.completeLiveVerifiedClosure(ctx, original, request)
			}
			return original, nil
		}
	}

	for index, ref := range request.EvidenceRefs {
		if target != domainbacklog.DeliveryBlocked && target != domainbacklog.DeliveryRejected {
			verified, verifyErr := s.verifyEvidence(ctx, EvidenceVerificationRequest{
				Ref:                    ref,
				ItemID:                 item.ItemID,
				ImplementationUnitID:   unitID,
				ImplementationRevision: revision,
				TargetDeliveryState:    target,
				Purpose:                "delivery_stage",
			})
			if verifyErr != nil {
				return domainbacklog.Item{}, verifyErr
			}
			request.EvidenceRefs[index] = verified
		} else {
			// Failed/blocked evidence is retained as a claim for the failure
			// record, but cannot be mistaken for a successful gate later.
			request.EvidenceRefs[index] = clearVerification(ref)
		}
	}
	if target == item.DeliveryState {
		return domainbacklog.Item{}, fmt.Errorf("%w: delivery %s -> %s", domainbacklog.ErrInvalidTransition, item.DeliveryState, target)
	}
	for _, ref := range request.EvidenceRefs {
		if err := domainbacklog.ValidateEvidenceRef(ref); err != nil {
			return domainbacklog.Item{}, err
		}
	}
	// TransitionDelivery validates a copy before the single append. No partial
	// evidence or state revision is saved when the gate rejects the request.
	next, err := domainbacklog.TransitionDelivery(item, request.TargetDeliveryState, request.EvidenceRefs)
	if err != nil {
		return domainbacklog.Item{}, err
	}
	if strings.TrimSpace(request.Reason) != "" {
		next.Implementation = request.Reason
	}
	next.ImplementationRevision = revision
	resultJSON, err := json.Marshal(next)
	if err != nil {
		return domainbacklog.Item{}, err
	}
	preparedReceipt := existingReceipt
	if !receiptFound {
		preparedReceipt = domainworkstream.StageRunReceipt{
			ReceiptID: stageRunReceiptID(key), IdempotencyKey: key, RequestID: strings.TrimSpace(request.RequestID),
			UnitID: unitID, ItemID: item.ItemID, ImplementationRevision: revision,
			TargetStage: target, PayloadHash: payloadHash, Status: domainworkstream.StageRunPrepared,
			DeliveryState: next.DeliveryState, ResultJSON: string(resultJSON), CreatedAt: s.now(),
		}
	}
	preparedReceipt.TargetStage = target
	preparedReceipt.PayloadHash = payloadHash
	preparedReceipt.DeliveryState = next.DeliveryState
	preparedReceipt.ResultJSON = string(resultJSON)
	if err := s.saveStageReceipt(ctx, preparedReceipt); err != nil {
		return domainbacklog.Item{}, err
	}

	var result domainbacklog.Item
	switch next.DeliveryState {
	case domainbacklog.DeliveryDone:
		if err := s.completeDone(ctx, item, next, request, key, payloadHash); err != nil {
			return domainbacklog.Item{}, err
		}
		result = next
	case domainbacklog.DeliveryBlocked:
		if err := s.completeBlocked(ctx, item, next, request); err != nil {
			return domainbacklog.Item{}, err
		}
		result = next
	case domainbacklog.DeliveryRejected:
		if err := s.releaseLease(ctx, domainbacklog.ImplementationLeaseName, next.ImplementationUnit); err != nil {
			return domainbacklog.Item{}, err
		}
		if err := s.save(ctx, next); err != nil {
			return domainbacklog.Item{}, err
		}
		result = next
	case domainbacklog.DeliveryLiveVerified:
		// Persist LIVE_VERIFIED before marking its stage receipt complete. The
		// lease remains held while the same service operation prepares and runs
		// the DONE closure below.
		if err := s.save(ctx, next); err != nil {
			return domainbacklog.Item{}, err
		}
		result = next
	default:
		if err := s.save(ctx, next); err != nil {
			return domainbacklog.Item{}, err
		}
		result = next
	}
	preparedReceipt.Status = domainworkstream.StageRunCompleted
	preparedReceipt.DeliveryState = result.DeliveryState
	preparedReceipt.ResultJSON = string(resultJSON)
	preparedReceipt.CompletedAt = s.now()
	if err := s.saveStageReceipt(ctx, preparedReceipt); err != nil {
		return domainbacklog.Item{}, err
	}
	if next.DeliveryState == domainbacklog.DeliveryLiveVerified {
		return s.completeLiveVerifiedClosure(ctx, result, request)
	}
	return result, nil
}

func (s *Service) Get(ctx context.Context, id string) (domainbacklog.Item, error) {
	return s.find(ctx, id)
}

func (s *Service) List(ctx context.Context, limit int) ([]domainbacklog.Item, error) {
	items, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *Service) queue(items []domainbacklog.Item) []domainbacklog.Item {
	queue := make([]domainbacklog.Item, 0)
	byID := make(map[string]domainbacklog.Item, len(items))
	for _, item := range items {
		byID[item.ItemID] = item
	}
	for _, item := range items {
		if item.ConceptState == domainbacklog.ConceptAdopted && item.DeliveryState == domainbacklog.DeliveryQueued && dependenciesDone(item, byID, map[string]bool{}) {
			queue = append(queue, item)
		}
	}
	sort.SliceStable(queue, func(i, j int) bool {
		depthI := dependencyDepth(queue[i], byID, map[string]bool{}, map[string]int{})
		depthJ := dependencyDepth(queue[j], byID, map[string]bool{}, map[string]int{})
		if depthI != depthJ {
			return depthI < depthJ
		}
		if queue[i].QueueRank != queue[j].QueueRank {
			return queue[i].QueueRank < queue[j].QueueRank
		}
		if queue[i].Priority != queue[j].Priority {
			return priorityRank(queue[i].Priority) < priorityRank(queue[j].Priority)
		}
		if queue[i].AdoptedAt != queue[j].AdoptedAt {
			return queue[i].AdoptedAt < queue[j].AdoptedAt
		}
		return queue[i].ItemID < queue[j].ItemID
	})
	return queue
}

// dependencyDepth places dependency roots before dependent work in the
// eligible queue.  Dependency eligibility is checked separately below.
func dependencyDepth(item domainbacklog.Item, byID map[string]domainbacklog.Item, visiting map[string]bool, memo map[string]int) int {
	if value, ok := memo[item.ItemID]; ok {
		return value
	}
	if visiting[item.ItemID] {
		return 1
	}
	visiting[item.ItemID] = true
	depth := 0
	for _, dependencyID := range item.DependsOn {
		dependency, ok := byID[strings.TrimSpace(dependencyID)]
		if !ok || dependency.DeliveryState == domainbacklog.DeliveryDone {
			continue
		}
		candidate := 1 + dependencyDepth(dependency, byID, visiting, memo)
		if candidate > depth {
			depth = candidate
		}
	}
	delete(visiting, item.ItemID)
	memo[item.ItemID] = depth
	return depth
}

// dependenciesDone is fail-closed: every declared dependency must exist and
// already be DONE.  A dependency cycle is rejected while traversing the
// current path instead of being treated as an ordering hint.
func dependenciesDone(item domainbacklog.Item, byID map[string]domainbacklog.Item, visiting map[string]bool) bool {
	if visiting[item.ItemID] {
		return false
	}
	visiting[item.ItemID] = true
	defer delete(visiting, item.ItemID)
	for _, dependencyID := range item.DependsOn {
		dependencyID = strings.TrimSpace(dependencyID)
		dependency, ok := byID[dependencyID]
		if !ok || visiting[dependencyID] || dependency.DeliveryState != domainbacklog.DeliveryDone {
			return false
		}
	}
	return true
}

func priorityRank(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "urgent":
		return 0
	case "high":
		return 1
	case "low":
		return 3
	default:
		return 2
	}
}

func (s *Service) Projection(ctx context.Context) (Projection, error) {
	return s.buildProjection(ctx)
}

func (s *Service) Evidence(ctx context.Context, unitID string) ([]domainbacklog.EvidenceRef, error) {
	items, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ImplementationUnit == strings.TrimSpace(unitID) || item.ItemID == strings.TrimSpace(unitID) {
			return append([]domainbacklog.EvidenceRef(nil), item.EvidenceRefs...), nil
		}
	}
	return []domainbacklog.EvidenceRef{}, nil
}

func (s *Service) acquireLease(ctx context.Context, lease domainworkstream.ImplementationLease) (bool, error) {
	if external, ok := s.workstream.(LifecycleStore); ok {
		acquired, _, err := external.AcquireImplementationLeaseIfUnfrozen(ctx, lease)
		return acquired, err
	}
	if external, ok := s.workstream.(ImplementationLeaseStore); ok {
		return external.AcquireImplementationLease(ctx, lease)
	}
	return false, errors.New("durable Atlas implementation lease store unavailable")
}

func (s *Service) releaseLease(ctx context.Context, name, holder string) error {
	if external, ok := s.workstream.(ImplementationLeaseStore); ok {
		return external.ReleaseImplementationLease(ctx, name, holder)
	}
	return errors.New("durable Atlas implementation lease store unavailable")
}

func (s *Service) getLease(ctx context.Context, name string) (domainworkstream.ImplementationLease, bool, error) {
	if external, ok := s.workstream.(ImplementationLeaseStore); ok {
		return external.GetImplementationLease(ctx, name)
	}
	return domainworkstream.ImplementationLease{}, false, errors.New("durable Atlas implementation lease store unavailable")
}

// ensureBlockedQueueFreeze repairs the only safe recoverable state for a
// BLOCKED item that still owns the implementation lease.  The item write is
// durable before the freeze write in the normal path; a restart must therefore
// reconstruct the exact active freeze from the item before releasing the lease.
// A missing/failed/mismatched freeze keeps the lease held and fails closed.
func (s *Service) ensureBlockedQueueFreeze(ctx context.Context, item domainbacklog.Item) error {
	unitID := strings.TrimSpace(item.ImplementationUnit)
	if unitID == "" {
		return errors.New("blocked Atlas item has no implementation unit")
	}
	revision := item.ImplementationRevision
	if revision < 1 {
		revision = 1
	}
	now := s.now()
	createdAt := now
	if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.CreatedAt)); err == nil {
		createdAt = parsed
	}
	expected := domainworkstream.QueueFreeze{
		FreezeID:             queueFreezeID(unitID, revision),
		BlockedUnitID:        unitID,
		BlockedRevision:      revision,
		FreezeRevision:       1,
		ReasonCode:           firstNonEmpty(strings.TrimSpace(item.Implementation), "stage_failed"),
		InvalidatedFromStage: strings.TrimSpace(item.InvalidatedFromStage),
		EvidenceRefs:         blockedEvidenceRefs(item),
		Status:               domainworkstream.QueueFreezeActive,
		CreatedAt:            createdAt,
		UpdatedAt:            now,
	}
	current, found, err := s.findFreeze(ctx, expected.FreezeID)
	if err != nil {
		return err
	}
	if found {
		if current.Status != domainworkstream.QueueFreezeActive && strings.TrimSpace(current.Status) != "" {
			return fmt.Errorf("%w: blocked unit freeze %q is %s", ErrLifecycleConflict, expected.FreezeID, current.Status)
		}
		if !queueFreezeMatchesBlockedItem(current, expected) {
			return fmt.Errorf("%w: blocked unit freeze %q does not match persisted item", ErrLifecycleConflict, expected.FreezeID)
		}
		return nil
	}
	return s.saveFreeze(ctx, expected)
}

func blockedEvidenceRefs(item domainbacklog.Item) []domainbacklog.EvidenceRef {
	refs := make([]domainbacklog.EvidenceRef, 0)
	for _, ref := range item.EvidenceRefs {
		// Persisted BLOCKED items contain cumulative evidence. Only retain
		// deterministic failed/unverified claims when rebuilding a missing
		// freeze; old external Passed=true claims are not authoritative.
		if !ref.Passed && !ref.IsVerified() {
			refs = append(refs, ref)
		}
	}
	return refs
}

func queueFreezeMatchesBlockedItem(current, expected domainworkstream.QueueFreeze) bool {
	return current.FreezeID == expected.FreezeID &&
		current.BlockedUnitID == expected.BlockedUnitID &&
		current.BlockedRevision == expected.BlockedRevision &&
		current.ReasonCode == expected.ReasonCode &&
		current.InvalidatedFromStage == expected.InvalidatedFromStage
}

// resumeLiveVerifiedClosure closes a durable LIVE_VERIFIED item before any
// queue selection.  LIVE_VERIFIED is not a runnable worker state; it is the
// owner boundary immediately before DONE.  This also repairs a crash after
// lease release but before the DONE item append, when no lease remains.
func (s *Service) resumeLiveVerifiedClosure(ctx context.Context) (domainbacklog.Item, bool, error) {
	items, err := s.list(ctx)
	if err != nil {
		return domainbacklog.Item{}, false, err
	}
	for _, item := range items {
		if item.DeliveryState != domainbacklog.DeliveryLiveVerified {
			continue
		}
		return s.resumeLiveVerifiedClosureForItem(ctx, item)
	}
	return domainbacklog.Item{}, false, nil
}

func (s *Service) resumeLiveVerifiedClosureForItem(ctx context.Context, item domainbacklog.Item) (domainbacklog.Item, bool, error) {
	if item.DeliveryState != domainbacklog.DeliveryLiveVerified {
		return domainbacklog.Item{}, false, nil
	}
	unitID := strings.TrimSpace(item.ImplementationUnit)
	if unitID == "" {
		unitID = strings.TrimSpace(item.ItemID)
	}
	revision := item.ImplementationRevision
	if revision < 1 {
		revision = 1
	}
	key := stageRunKey(unitID, revision, domainbacklog.DeliveryDone)
	closure, found, lookupErr := s.findClosureReceipt(ctx, key)
	if lookupErr != nil {
		return domainbacklog.Item{}, true, lookupErr
	}
	request := ReviseRequest{TargetDeliveryState: domainbacklog.DeliveryDone}
	if found {
		request.RequestID = closure.RequestID
	}
	done, closeErr := s.completeLiveVerifiedClosure(ctx, item, request)
	return done, true, closeErr
}

// Recover removes terminal/orphaned durable leases. It intentionally does not
// start work; heartbeat may only observe the resulting active projection.
func (s *Service) Recover(ctx context.Context) error {
	lease, ok, err := s.getLease(ctx, domainbacklog.ImplementationLeaseName)
	if err != nil || !ok {
		if err != nil {
			return err
		}
		_, _, resumeErr := s.resumeLiveVerifiedClosure(ctx)
		return resumeErr
	}
	item, findErr := s.findByUnit(ctx, lease.HolderUnitID)
	if findErr != nil {
		return findErr
	}
	if item.DeliveryState == domainbacklog.DeliveryLiveVerified {
		_, _, resumeErr := s.resumeLiveVerifiedClosureForItem(ctx, item)
		return resumeErr
	}
	if item.DeliveryState == domainbacklog.DeliveryBlocked {
		if err := s.ensureBlockedQueueFreeze(ctx, item); err != nil {
			return err
		}
		return s.releaseLease(ctx, lease.LeaseName, lease.HolderUnitID)
	}
	if item.DeliveryState == domainbacklog.DeliveryDone || item.DeliveryState == domainbacklog.DeliveryRejected {
		return s.releaseLease(ctx, lease.LeaseName, lease.HolderUnitID)
	}
	return nil
}

func (s *Service) HeartbeatLease(ctx context.Context, stage, revision string, now time.Time) error {
	lease, ok, err := s.getLease(ctx, domainbacklog.ImplementationLeaseName)
	if err != nil || !ok {
		return err
	}
	lease.Stage = strings.TrimSpace(stage)
	lease.Revision = strings.TrimSpace(revision)
	lease.HeartbeatAt = now.UTC()
	if external, ok := s.workstream.(ImplementationLeaseStore); ok {
		return external.HeartbeatImplementationLease(ctx, lease)
	}
	return errors.New("durable Atlas implementation lease store unavailable")
}
