package backlog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
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
	Kind               string                    `json:"kind,omitempty"`
	Title              string                    `json:"title"`
	Body               string                    `json:"body,omitempty"`
	Purpose            string                    `json:"purpose,omitempty"`
	Category           string                    `json:"category,omitempty"`
	Source             string                    `json:"source,omitempty"`
	SourceRefs         []domainbacklog.SourceRef `json:"source_refs,omitempty"`
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
	Catalog  []map[string]any            `json:"catalog"`
	Features []map[string]any            `json:"features"`
	Current  []domainbacklog.Item        `json:"current"`
	Radar    []domainbacklog.Item        `json:"radar"`
	Backlog  []domainbacklog.Item        `json:"backlog"`
	Queue    []domainbacklog.Item        `json:"queue"`
	Active   *domainbacklog.Item         `json:"active"`
	Evidence []domainbacklog.EvidenceRef `json:"evidence"`
	Modules  []map[string]any            `json:"modules"`
}

type Service struct {
	items      ItemStore
	workstream WorkstreamCreator
	clock      func() time.Time

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
		SchemaVersion: domainbacklog.SchemaVersion2, ItemID: id,
		Kind: request.Kind, Title: strings.TrimSpace(request.Title), Body: strings.TrimSpace(request.Body),
		Purpose:         strings.TrimSpace(request.Purpose),
		TargetModules:   append([]string(nil), request.TargetModules...),
		ConsumerModules: append([]string(nil), request.ConsumerModules...),
		AffectedModules: append([]string(nil), request.AffectedModules...), AcceptanceCriteria: append([]string(nil), request.AcceptanceCriteria...),
		Category: strings.TrimSpace(request.Category), Source: strings.TrimSpace(request.Source), SourceRefs: refs,
		Owner: strings.TrimSpace(request.Owner), OwnerModule: domainbacklog.LifecycleOwnerModule,
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
	item, err := s.find(ctx, id)
	if err != nil {
		return domainbacklog.Item{}, err
	}
	if item.ConceptState != domainbacklog.ConceptAdopted {
		return domainbacklog.Item{}, fmt.Errorf("atlas item %s is not adopted", id)
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
	if err := s.save(ctx, next); err != nil {
		return domainbacklog.Item{}, err
	}
	if next.DeliveryState == domainbacklog.DeliveryLiveVerified || next.DeliveryState == domainbacklog.DeliveryDone {
		_ = s.releaseLease(ctx, domainbacklog.ImplementationLeaseName, next.ImplementationUnit)
	}
	return next, nil
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
	for _, item := range items {
		if item.ConceptState == domainbacklog.ConceptAdopted && item.DeliveryState == domainbacklog.DeliveryQueued {
			queue = append(queue, item)
		}
	}
	sort.SliceStable(queue, func(i, j int) bool {
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
	items, err := s.list(ctx)
	if err != nil {
		return Projection{}, err
	}
	p := Projection{Catalog: cloneMaps(s.catalog), Features: cloneMaps(s.features), Modules: cloneMaps(s.modules), Current: []domainbacklog.Item{}, Radar: []domainbacklog.Item{}, Backlog: []domainbacklog.Item{}, Queue: []domainbacklog.Item{}, Evidence: []domainbacklog.EvidenceRef{}}
	for _, item := range items {
		switch item.ConceptState {
		case domainbacklog.ConceptRadar:
			p.Radar = append(p.Radar, item)
		case domainbacklog.ConceptCandidate, domainbacklog.ConceptAdopted, domainbacklog.ConceptDeferred, domainbacklog.ConceptRejected:
			p.Backlog = append(p.Backlog, item)
		}
		if item.DeliveryState == domainbacklog.DeliveryLiveVerified || item.DeliveryState == domainbacklog.DeliveryDone {
			p.Current = append(p.Current, item)
		}
		if item.ConceptState == domainbacklog.ConceptAdopted && item.DeliveryState == domainbacklog.DeliveryQueued {
			p.Queue = append(p.Queue, item)
		}
		p.Evidence = append(p.Evidence, item.EvidenceRefs...)
	}
	p.Queue = s.queue(p.Queue)
	if lease, ok, err := s.getLease(ctx, domainbacklog.ImplementationLeaseName); err != nil {
		return Projection{}, err
	} else if ok {
		for i := range items {
			if items[i].ImplementationUnit == lease.HolderUnitID {
				active := items[i]
				p.Active = &active
				break
			}
		}
	}
	return p, nil
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

// Recover removes terminal/orphaned durable leases. It intentionally does not
// start work; heartbeat may only observe the resulting active projection.
func (s *Service) Recover(ctx context.Context) error {
	lease, ok, err := s.getLease(ctx, domainbacklog.ImplementationLeaseName)
	if err != nil || !ok {
		return err
	}
	item, findErr := s.findByUnit(ctx, lease.HolderUnitID)
	if findErr != nil || item.DeliveryState == domainbacklog.DeliveryLiveVerified || item.DeliveryState == domainbacklog.DeliveryDone || item.DeliveryState == domainbacklog.DeliveryBlocked || item.DeliveryState == domainbacklog.DeliveryRejected {
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
