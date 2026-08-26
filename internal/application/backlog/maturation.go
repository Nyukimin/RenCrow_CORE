package backlog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
)

const maturationPeriod = 7 * 24 * time.Hour

var (
	ErrMaturationNotEligible       = errors.New("atlas item has not reached maturation eligibility")
	ErrMaturationDecisionInvalid   = errors.New("invalid Atlas revalidation decision")
	ErrMaturationReasonRequired    = errors.New("Atlas revalidation reason is required")
	ErrMaturationReviewAgentNeeded = errors.New("Atlas revalidation review agent is required")
	ErrMaturationForceMerge        = errors.New("forced Atlas revalidation cannot merge")
	ErrMaturationBypassRequired    = errors.New("early forced revalidation requires an emergency bypass reason")
	ErrMaturationBypassInvalid     = errors.New("invalid Atlas maturation bypass reason")
	ErrMaturationTerminal          = errors.New("Atlas maturation state is terminal")
)

// RevalidateRequest is the CORE-owned semantic review payload.  The service
// stamps backlog identity, date, and maturation age; callers cannot choose
// those audit fields.
type RevalidateRequest struct {
	RequestID                string   `json:"request_id,omitempty"`
	Trigger                  string   `json:"trigger,omitempty"`
	Decision                 string   `json:"decision"`
	Reason                   string   `json:"reason"`
	Necessity                string   `json:"necessity,omitempty"`
	Duplication              string   `json:"duplication,omitempty"`
	Mergeability             string   `json:"mergeability,omitempty"`
	ArchitecturalConsistency string   `json:"architectural_consistency,omitempty"`
	TechnologyValidity       string   `json:"technology_validity,omitempty"`
	Timing                   string   `json:"timing,omitempty"`
	RelatedBacklogs          []string `json:"related_backlogs,omitempty"`
	ConflictingSpecs         []string `json:"conflicting_specs,omitempty"`
	MergedInto               string   `json:"merged_into,omitempty"`
	TechnologyChanges        []string `json:"technology_changes,omitempty"`
	ArchitectureImpact       string   `json:"architecture_impact,omitempty"`
	ImplementationValue      string   `json:"implementation_value,omitempty"`
	NextReviewTrigger        string   `json:"next_review_trigger,omitempty"`
	ReviewAgents             []string `json:"review_agents"`
	Forced                   bool     `json:"forced,omitempty"`
	BypassReason             string   `json:"bypass_reason,omitempty"`
}

// EnrichRequest describes the only fields that may be changed by maturation
// enrichment.  It deliberately has no arbitrary map or direct state fields.
type EnrichRequest struct {
	SourceRefs           []domainbacklog.SourceRef `json:"source_refs,omitempty"`
	RelatedIDs           []string                  `json:"related_ids,omitempty"`
	RelationRefs         []string                  `json:"relation_refs,omitempty"`
	Body                 string                    `json:"body,omitempty"`
	Background           string                    `json:"background,omitempty"`
	Priority             string                    `json:"priority,omitempty"`
	MaterialChange       bool                      `json:"material_change,omitempty"`
	Reason               string                    `json:"reason,omitempty"`
	MaterialChangeReason string                    `json:"material_change_reason,omitempty"`
}

func ensureMaturationFields(item *domainbacklog.Item, fallback time.Time, reset bool) error {
	if item == nil {
		return errors.New("Atlas item is required")
	}
	fallback = fallback.UTC()
	if reset {
		item.MaturationState = domainbacklog.MaturationStateMaturation
		item.MaturationStartedAt = formatMaturationTime(fallback)
		item.MaturationEligibleAt = formatMaturationTime(fallback.Add(maturationPeriod))
		item.MaturationBypass = false
		item.BypassReason = ""
		item.MergedInto = ""
		item.NextReviewTrigger = ""
		return nil
	}
	if strings.TrimSpace(item.MaturationState) == "" {
		item.MaturationState = domainbacklog.MaturationStateMaturation
	} else if err := domainbacklog.ValidateMaturationState(item.MaturationState); err != nil {
		return err
	} else {
		item.MaturationState = strings.ToUpper(strings.TrimSpace(item.MaturationState))
	}

	start, startSet, err := parseMaturationTime(item.MaturationStartedAt)
	if err != nil {
		return fmt.Errorf("invalid maturation_started_at: %w", err)
	}
	eligible, eligibleSet, err := parseMaturationTime(item.MaturationEligibleAt)
	if err != nil {
		return fmt.Errorf("invalid maturation_eligible_at: %w", err)
	}
	if !startSet {
		if created, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.CreatedAt)); parseErr == nil {
			start = created.UTC()
		} else if eligibleSet {
			start = eligible.Add(-maturationPeriod)
		} else {
			start = fallback
		}
		item.MaturationStartedAt = formatMaturationTime(start)
	}
	if !eligibleSet {
		item.MaturationEligibleAt = formatMaturationTime(start.Add(maturationPeriod))
	}
	return nil
}

func parseMaturationTime(value string) (time.Time, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, true, err
	}
	return parsed.UTC(), true, nil
}

func formatMaturationTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func trimStringList(values []string) ([]string, error) {
	if values == nil {
		return nil, nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, errors.New("list value must not be empty")
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out, nil
}

func appendUniqueStrings(existing, incoming []string) []string {
	out := append([]string(nil), existing...)
	seen := make(map[string]struct{}, len(out)+len(incoming))
	for _, value := range out {
		seen[value] = struct{}{}
	}
	for _, value := range incoming {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func appendUniqueSourceRefs(existing, incoming []domainbacklog.SourceRef) ([]domainbacklog.SourceRef, error) {
	out := append([]domainbacklog.SourceRef(nil), existing...)
	seen := make(map[string]struct{}, len(out)+len(incoming))
	for _, ref := range out {
		seen[ref.DedupeKey()] = struct{}{}
	}
	for _, ref := range incoming {
		if err := domainbacklog.ValidateSourceRef(ref); err != nil {
			return nil, err
		}
		key := ref.DedupeKey()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ref)
	}
	return out, nil
}

func normalizeDecision(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}

func maturationTargetForDecision(decision string) string {
	switch decision {
	case domainbacklog.RevalidationDecisionPromote:
		return domainbacklog.MaturationStatePromoted
	case domainbacklog.RevalidationDecisionMerge:
		return domainbacklog.MaturationStateMerged
	case domainbacklog.RevalidationDecisionHold:
		return domainbacklog.MaturationStateHold
	case domainbacklog.RevalidationDecisionDrop:
		return domainbacklog.MaturationStateDropped
	default:
		return ""
	}
}

func validateRevalidationRequest(request RevalidateRequest) (RevalidateRequest, error) {
	request.Decision = normalizeDecision(request.Decision)
	request.Trigger = strings.TrimSpace(request.Trigger)
	switch request.Decision {
	case domainbacklog.RevalidationDecisionPromote, domainbacklog.RevalidationDecisionMerge,
		domainbacklog.RevalidationDecisionHold, domainbacklog.RevalidationDecisionDrop:
	default:
		return request, ErrMaturationDecisionInvalid
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if request.Reason == "" {
		return request, ErrMaturationReasonRequired
	}
	agents, err := trimStringList(request.ReviewAgents)
	if err != nil || len(agents) == 0 {
		return request, ErrMaturationReviewAgentNeeded
	}
	request.ReviewAgents = agents
	request.RelatedBacklogs, err = trimStringList(request.RelatedBacklogs)
	if err != nil {
		return request, err
	}
	request.ConflictingSpecs, err = trimStringList(request.ConflictingSpecs)
	if err != nil {
		return request, err
	}
	request.TechnologyChanges, err = trimStringList(request.TechnologyChanges)
	if err != nil {
		return request, err
	}
	request.MergedInto = strings.TrimSpace(request.MergedInto)
	request.ArchitectureImpact = strings.TrimSpace(request.ArchitectureImpact)
	request.ImplementationValue = strings.TrimSpace(request.ImplementationValue)
	request.Necessity = strings.TrimSpace(request.Necessity)
	request.Duplication = strings.TrimSpace(request.Duplication)
	request.Mergeability = strings.TrimSpace(request.Mergeability)
	request.ArchitecturalConsistency = strings.TrimSpace(request.ArchitecturalConsistency)
	request.TechnologyValidity = strings.TrimSpace(request.TechnologyValidity)
	request.Timing = strings.TrimSpace(request.Timing)
	request.NextReviewTrigger = strings.TrimSpace(request.NextReviewTrigger)
	request.BypassReason = strings.ToLower(strings.TrimSpace(request.BypassReason))
	if request.BypassReason != "" && !domainbacklog.IsValidMaturationBypassReason(request.BypassReason) {
		return request, ErrMaturationBypassInvalid
	}
	if request.Forced && request.Decision == domainbacklog.RevalidationDecisionMerge {
		return request, ErrMaturationForceMerge
	}
	if request.Decision == domainbacklog.RevalidationDecisionHold && request.NextReviewTrigger == "" {
		return request, fmt.Errorf("%w: HOLD requires next_review_trigger", ErrMaturationDecisionInvalid)
	}
	return request, nil
}

func (s *Service) Revalidate(ctx context.Context, id string, request RevalidateRequest) (domainbacklog.Item, error) {
	request, err := validateRevalidationRequest(request)
	if err != nil {
		return domainbacklog.Item{}, err
	}
	item, err := s.find(ctx, id)
	if err != nil {
		return domainbacklog.Item{}, err
	}
	if item.ConceptState != domainbacklog.ConceptCandidate && item.ConceptState != domainbacklog.ConceptDeferred {
		return domainbacklog.Item{}, fmt.Errorf("atlas item %s is not revalidatable", id)
	}
	now := s.now()
	if err := ensureMaturationFields(&item, now, false); err != nil {
		return domainbacklog.Item{}, err
	}
	if item.MaturationState == domainbacklog.MaturationStateMerged || item.MaturationState == domainbacklog.MaturationStateDropped || item.MaturationState == domainbacklog.MaturationStatePromoted {
		return domainbacklog.Item{}, ErrMaturationTerminal
	}
	eligibleAt, _, err := parseMaturationTime(item.MaturationEligibleAt)
	if err != nil {
		return domainbacklog.Item{}, err
	}
	beforeEligible := now.Before(eligibleAt)
	bypassUsed := false
	if !request.Forced && item.MaturationState == domainbacklog.MaturationStateHold {
		// HOLD has no fixed expiry. Even after the original eligibility date it
		// re-opens only for the exact event trigger persisted by the decision.
		if strings.TrimSpace(item.NextReviewTrigger) == "" || !strings.EqualFold(request.Trigger, item.NextReviewTrigger) {
			return domainbacklog.Item{}, ErrMaturationNotEligible
		}
	} else if beforeEligible && !request.Forced {
		return domainbacklog.Item{}, ErrMaturationNotEligible
	}
	if beforeEligible && request.Forced {
		if request.BypassReason == "" {
			return domainbacklog.Item{}, ErrMaturationBypassRequired
		}
		item.MaturationBypass = true
		item.BypassReason = request.BypassReason
		bypassUsed = true
	} else {
		// A bypass is a fact about this decision, not a caller-selected label for
		// an otherwise eligible review. The item retains historical audit fields,
		// while each append-only record states whether its own gate was bypassed.
		request.BypassReason = ""
	}
	if request.Decision == domainbacklog.RevalidationDecisionMerge {
		if request.MergedInto == "" || request.MergedInto == item.ItemID {
			return domainbacklog.Item{}, errors.New("merge requires a different existing Atlas item")
		}
		target, targetErr := s.find(ctx, request.MergedInto)
		if targetErr != nil {
			return domainbacklog.Item{}, fmt.Errorf("merge target %q: %w", request.MergedInto, targetErr)
		}
		if target.ItemID == item.ItemID {
			return domainbacklog.Item{}, errors.New("merge requires a different existing Atlas item")
		}
	}
	targetState := maturationTargetForDecision(request.Decision)
	item, err = domainbacklog.TransitionMaturation(item, targetState)
	if err != nil {
		return domainbacklog.Item{}, err
	}
	item.SchemaVersion = domainbacklog.SchemaVersion2
	switch request.Decision {
	case domainbacklog.RevalidationDecisionPromote:
		item.ConceptState = domainbacklog.ConceptCandidate
	case domainbacklog.RevalidationDecisionMerge:
		item.ConceptState = domainbacklog.ConceptDeferred
		item.MergedInto = request.MergedInto
	case domainbacklog.RevalidationDecisionHold:
		item.ConceptState = domainbacklog.ConceptDeferred
	case domainbacklog.RevalidationDecisionDrop:
		item.ConceptState = domainbacklog.ConceptRejected
		item.DeliveryState = domainbacklog.DeliveryRejected
	}
	item.NextReviewTrigger = request.NextReviewTrigger
	start, _, _ := parseMaturationTime(item.MaturationStartedAt)
	maturationDays := 0
	if elapsed := now.Sub(start); elapsed > 0 {
		maturationDays = int(elapsed / (24 * time.Hour))
	}
	record := domainbacklog.RevalidationRecord{
		BacklogID: item.ItemID, RevalidationDate: formatMaturationTime(now), MaturationDays: maturationDays,
		Decision: request.Decision, Reason: request.Reason,
		Necessity: request.Necessity, Duplication: request.Duplication,
		Mergeability: request.Mergeability, ArchitecturalConsistency: request.ArchitecturalConsistency,
		TechnologyValidity: request.TechnologyValidity, Timing: request.Timing,
		RelatedBacklogs: append([]string(nil), request.RelatedBacklogs...), ConflictingSpecs: append([]string(nil), request.ConflictingSpecs...),
		MergedInto: request.MergedInto, TechnologyChanges: append([]string(nil), request.TechnologyChanges...),
		ArchitectureImpact: request.ArchitectureImpact, ImplementationValue: request.ImplementationValue,
		NextReviewTrigger: request.NextReviewTrigger, ReviewAgents: append([]string(nil), request.ReviewAgents...),
		Forced: request.Forced, MaturationBypass: bypassUsed, BypassReason: request.BypassReason,
	}
	if err := domainbacklog.ValidateRevalidationRecord(record); err != nil {
		return domainbacklog.Item{}, err
	}
	item.RevalidationRecords = append(append([]domainbacklog.RevalidationRecord(nil), item.RevalidationRecords...), record)
	if err := s.save(ctx, item); err != nil {
		return domainbacklog.Item{}, err
	}
	return item, nil
}

func (s *Service) Enrich(ctx context.Context, id string, request EnrichRequest) (domainbacklog.Item, error) {
	item, err := s.find(ctx, id)
	if err != nil {
		return domainbacklog.Item{}, err
	}
	if item.ConceptState == domainbacklog.ConceptAdopted || item.MaturationState == domainbacklog.MaturationStateMerged || item.MaturationState == domainbacklog.MaturationStateDropped {
		return domainbacklog.Item{}, ErrMaturationTerminal
	}
	if item.ConceptState != domainbacklog.ConceptCandidate && item.ConceptState != domainbacklog.ConceptDeferred {
		return domainbacklog.Item{}, fmt.Errorf("atlas item %s is not enrichable", id)
	}
	now := s.now()
	if err := ensureMaturationFields(&item, now, false); err != nil {
		return domainbacklog.Item{}, err
	}
	if request.MaterialChange {
		reason := strings.TrimSpace(request.Reason)
		if reason == "" {
			reason = strings.TrimSpace(request.MaterialChangeReason)
		}
		if reason == "" {
			return domainbacklog.Item{}, ErrMaturationReasonRequired
		}
	}
	if len(request.SourceRefs) > 0 {
		refs := append([]domainbacklog.SourceRef(nil), request.SourceRefs...)
		for index := range refs {
			if strings.TrimSpace(refs[index].CapturedAt) == "" {
				refs[index].CapturedAt = formatMaturationTime(now)
			}
		}
		item.SourceRefs, err = appendUniqueSourceRefs(item.SourceRefs, refs)
		if err != nil {
			return domainbacklog.Item{}, err
		}
	}
	item.RelatedIDs = appendUniqueStrings(item.RelatedIDs, request.RelatedIDs)
	item.RelationRefs = appendUniqueStrings(item.RelationRefs, request.RelationRefs)
	if strings.TrimSpace(request.Body) != "" {
		item.Body = strings.TrimSpace(request.Body)
	}
	if strings.TrimSpace(request.Background) != "" {
		item.Background = strings.TrimSpace(request.Background)
	}
	if strings.TrimSpace(request.Priority) != "" {
		item.Priority = strings.TrimSpace(request.Priority)
	}
	if request.MaterialChange {
		if item.MaturationState != domainbacklog.MaturationStateMaturation {
			if _, err := domainbacklog.TransitionMaturation(item, domainbacklog.MaturationStateMaturation); err != nil {
				return domainbacklog.Item{}, err
			}
		}
		item.MaturationState = domainbacklog.MaturationStateMaturation
		item.MaturationStartedAt = formatMaturationTime(now)
		item.MaturationEligibleAt = formatMaturationTime(now.Add(maturationPeriod))
		item.LastMaterialChangeAt = formatMaturationTime(now)
		item.MaturationBypass = false
		item.BypassReason = ""
		item.MergedInto = ""
		item.NextReviewTrigger = ""
		item.ConceptState = domainbacklog.ConceptCandidate
	}
	if err := s.save(ctx, item); err != nil {
		return domainbacklog.Item{}, err
	}
	return item, nil
}
