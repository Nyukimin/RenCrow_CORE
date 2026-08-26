package backlog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

var ErrRevalidationEvaluatorUnavailable = errors.New("Atlas revalidation evaluator unavailable")

// RevalidationProposal contains only the semantic residual that cannot be
// decided by a deterministic lifecycle rule. Identity, time, eligibility,
// merge targets, policy, transition and persistence remain owned by Service.
type RevalidationProposal struct {
	Decision            string   `json:"decision"`
	Reason              string   `json:"reason"`
	Necessity           string   `json:"necessity"`
	Duplication         string   `json:"duplication"`
	Mergeability        string   `json:"mergeability"`
	ArchitecturalFit    string   `json:"architectural_consistency"`
	TechnologyValidity  string   `json:"technology_validity"`
	ImplementationValue string   `json:"implementation_value"`
	Timing              string   `json:"timing"`
	RelatedBacklogs     []string `json:"related_backlogs"`
	ConflictingSpecs    []string `json:"conflicting_specs"`
	MergedInto          string   `json:"merged_into"`
	TechnologyChanges   []string `json:"technology_changes"`
	ArchitectureImpact  string   `json:"architecture_impact"`
	NextReviewTrigger   string   `json:"next_review_trigger"`
}

type RevalidationEvaluation struct {
	Proposal     RevalidationProposal
	ReviewAgents []string
}

type RevalidationEvaluationInput struct {
	Item         domainbacklog.Item
	RelatedItems []domainbacklog.Item
}

type RevalidationSweepReport struct {
	Eligible  int      `json:"eligible"`
	Attempted int      `json:"attempted"`
	Completed int      `json:"completed"`
	Failed    int      `json:"failed"`
	ItemIDs   []string `json:"item_ids"`
}

type RevalidationEvaluator interface {
	Evaluate(context.Context, RevalidationEvaluationInput) (RevalidationEvaluation, error)
}

func (s *Service) WithRevalidationEvaluator(evaluator RevalidationEvaluator) *Service {
	if s != nil {
		s.evaluator = evaluator
	}
	return s
}

// EvaluateAndRevalidate asks the semantic evaluator for one proposal, then
// passes it through the same deterministic CORE transition used by every
// other revalidation. The evaluator never receives a store or state writer.
func (s *Service) EvaluateAndRevalidate(ctx context.Context, id string) (domainbacklog.Item, error) {
	return s.EvaluateAndRevalidateOnTrigger(ctx, id, "")
}

func (s *Service) EvaluateAndRevalidateOnTrigger(ctx context.Context, id, trigger string) (domainbacklog.Item, error) {
	if s == nil || s.evaluator == nil {
		return domainbacklog.Item{}, ErrRevalidationEvaluatorUnavailable
	}
	item, err := s.find(ctx, id)
	if err != nil {
		return domainbacklog.Item{}, err
	}
	if err := ensureMaturationFields(&item, s.now(), false); err != nil {
		return domainbacklog.Item{}, err
	}
	eligibleAt, _, err := parseMaturationTime(item.MaturationEligibleAt)
	if err != nil {
		return domainbacklog.Item{}, err
	}
	if item.MaturationState == domainbacklog.MaturationStateHold && (strings.TrimSpace(item.NextReviewTrigger) == "" || !strings.EqualFold(strings.TrimSpace(trigger), item.NextReviewTrigger)) {
		return domainbacklog.Item{}, ErrMaturationNotEligible
	}
	if item.MaturationState != domainbacklog.MaturationStateHold && s.now().Before(eligibleAt) {
		return domainbacklog.Item{}, ErrMaturationNotEligible
	}
	items, err := s.list(ctx)
	if err != nil {
		return domainbacklog.Item{}, err
	}
	evaluation, err := s.evaluator.Evaluate(ctx, RevalidationEvaluationInput{
		Item:         item,
		RelatedItems: relatedRevalidationItems(item, items, 24),
	})
	if err != nil {
		return domainbacklog.Item{}, err
	}
	proposal := evaluation.Proposal
	return s.Revalidate(ctx, id, RevalidateRequest{
		Trigger:  trigger,
		Decision: proposal.Decision, Reason: proposal.Reason,
		Necessity: proposal.Necessity, Duplication: proposal.Duplication,
		Mergeability: proposal.Mergeability, ArchitecturalConsistency: proposal.ArchitecturalFit,
		TechnologyValidity: proposal.TechnologyValidity, Timing: proposal.Timing,
		RelatedBacklogs: proposal.RelatedBacklogs, ConflictingSpecs: proposal.ConflictingSpecs,
		MergedInto: proposal.MergedInto, TechnologyChanges: proposal.TechnologyChanges,
		ArchitectureImpact: proposal.ArchitectureImpact, ImplementationValue: proposal.ImplementationValue,
		NextReviewTrigger: proposal.NextReviewTrigger, ReviewAgents: evaluation.ReviewAgents,
	})
}

// RunEligibleRevalidations performs a bounded automatic sweep. HOLD remains
// event-driven and is therefore excluded until an explicit trigger request.
func (s *Service) RunEligibleRevalidations(ctx context.Context, limit int) (RevalidationSweepReport, error) {
	report := RevalidationSweepReport{ItemIDs: []string{}}
	if s == nil || s.evaluator == nil {
		return report, ErrRevalidationEvaluatorUnavailable
	}
	if limit < 1 || limit > 10 {
		limit = 1
	}
	items, err := s.list(ctx)
	if err != nil {
		return report, err
	}
	type eligibleItem struct {
		id string
		at time.Time
	}
	eligible := make([]eligibleItem, 0)
	for _, item := range items {
		if item.ConceptState != domainbacklog.ConceptCandidate || item.MaturationState != domainbacklog.MaturationStateMaturation {
			continue
		}
		at, set, parseErr := parseMaturationTime(item.MaturationEligibleAt)
		if parseErr != nil {
			return report, fmt.Errorf("Atlas item %s eligibility: %w", item.ItemID, parseErr)
		}
		if !set {
			created, createdErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(item.CreatedAt))
			if createdErr != nil {
				continue
			}
			at = created.UTC().Add(maturationPeriod)
		}
		if !s.now().Before(at) {
			eligible = append(eligible, eligibleItem{id: item.ItemID, at: at})
		}
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		if !eligible[i].at.Equal(eligible[j].at) {
			return eligible[i].at.Before(eligible[j].at)
		}
		return eligible[i].id < eligible[j].id
	})
	report.Eligible = len(eligible)
	for _, candidate := range eligible {
		if report.Attempted >= limit {
			break
		}
		report.Attempted++
		if _, evaluateErr := s.EvaluateAndRevalidate(ctx, candidate.id); evaluateErr != nil {
			report.Failed++
			continue
		}
		report.Completed++
		report.ItemIDs = append(report.ItemIDs, candidate.id)
	}
	return report, nil
}

func relatedRevalidationItems(subject domainbacklog.Item, items []domainbacklog.Item, limit int) []domainbacklog.Item {
	if limit < 1 {
		return []domainbacklog.Item{}
	}
	explicit := make(map[string]struct{}, len(subject.RelatedIDs)+len(subject.DependsOn))
	for _, id := range append(append([]string(nil), subject.RelatedIDs...), subject.DependsOn...) {
		explicit[strings.TrimSpace(id)] = struct{}{}
	}
	tokens := revalidationTokens(subject)
	type scored struct {
		item  domainbacklog.Item
		score int
	}
	rows := make([]scored, 0, len(items))
	for _, candidate := range items {
		if candidate.ItemID == subject.ItemID {
			continue
		}
		score := 0
		if _, ok := explicit[candidate.ItemID]; ok {
			score += 100
		}
		if strings.EqualFold(strings.TrimSpace(subject.Category), strings.TrimSpace(candidate.Category)) && strings.TrimSpace(subject.Category) != "" {
			score += 12
		}
		if overlapStrings(subject.TargetModules, candidate.TargetModules) || overlapStrings(subject.AffectedModules, candidate.AffectedModules) {
			score += 10
		}
		for token := range revalidationTokens(candidate) {
			if _, ok := tokens[token]; ok {
				score++
			}
		}
		// Rejected/DROPPED history remains searchable even when only weakly
		// related; it is evidence against rediscovering a discarded concept.
		if score > 0 {
			rows = append(rows, scored{item: candidate, score: score})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].score != rows[j].score {
			return rows[i].score > rows[j].score
		}
		return rows[i].item.ItemID < rows[j].item.ItemID
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]domainbacklog.Item, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.item)
	}
	return out
}

func overlapStrings(left, right []string) bool {
	seen := make(map[string]struct{}, len(left))
	for _, value := range left {
		seen[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
	}
	for _, value := range right {
		if _, ok := seen[strings.ToLower(strings.TrimSpace(value))]; ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func revalidationTokens(item domainbacklog.Item) map[string]struct{} {
	text := strings.ToLower(strings.Join([]string{item.Title, item.Purpose, item.Problem, item.Idea, strings.Join(item.Tags, " ")}, " "))
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && !(r >= '\u3040' && r <= '\u30ff') && !(r >= '\u4e00' && r <= '\u9fff')
	})
	out := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if len([]rune(field)) >= 2 {
			out[field] = struct{}{}
		}
	}
	return out
}

type LLMRevalidationEvaluator struct {
	provider domainllm.LLMProvider
	reviewer string
}

const (
	revalidationRelatedLimit = 24
	revalidationTextRunes    = 320
	revalidationListLimit    = 16
	revalidationPayloadBytes = 64 * 1024
)

// revalidationEvidence is the deliberately small semantic projection supplied
// to the LLM. Full item bodies, source bodies, evidence receipts and prior
// review prose remain in CORE and are never copied into a model request.
type revalidationEvidence struct {
	ItemID              string   `json:"item_id"`
	Title               string   `json:"title"`
	Purpose             string   `json:"purpose,omitempty"`
	Problem             string   `json:"problem,omitempty"`
	Idea                string   `json:"idea,omitempty"`
	Category            string   `json:"category,omitempty"`
	ConceptState        string   `json:"concept_state,omitempty"`
	DeliveryState       string   `json:"delivery_state,omitempty"`
	MaturationState     string   `json:"maturation_state,omitempty"`
	TargetModules       []string `json:"target_modules,omitempty"`
	AffectedModules     []string `json:"affected_modules,omitempty"`
	Tags                []string `json:"tags,omitempty"`
	DependsOn           []string `json:"depends_on,omitempty"`
	RelatedIDs          []string `json:"related_ids,omitempty"`
	SpecificationRefs   []string `json:"specification_refs,omitempty"`
	SourceLocators      []string `json:"source_locators,omitempty"`
	MaturationStartedAt string   `json:"maturation_started_at,omitempty"`
}

func NewLLMRevalidationEvaluator(provider domainllm.LLMProvider, reviewer string) *LLMRevalidationEvaluator {
	return &LLMRevalidationEvaluator{provider: provider, reviewer: strings.TrimSpace(reviewer)}
}

func (e *LLMRevalidationEvaluator) Evaluate(ctx context.Context, input RevalidationEvaluationInput) (RevalidationEvaluation, error) {
	if e == nil || e.provider == nil || e.reviewer == "" {
		return RevalidationEvaluation{}, ErrRevalidationEvaluatorUnavailable
	}
	related := input.RelatedItems
	if len(related) > revalidationRelatedLimit {
		related = related[:revalidationRelatedLimit]
	}
	payload := struct {
		Item         revalidationEvidence   `json:"item"`
		RelatedItems []revalidationEvidence `json:"related_items"`
	}{Item: compactRevalidationEvidence(input.Item), RelatedItems: make([]revalidationEvidence, 0, len(related))}
	for _, item := range related {
		payload.RelatedItems = append(payload.RelatedItems, compactRevalidationEvidence(item))
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return RevalidationEvaluation{}, err
	}
	if len(encoded) > revalidationPayloadBytes {
		return RevalidationEvaluation{}, fmt.Errorf("Atlas revalidation evidence exceeds %d bytes", revalidationPayloadBytes)
	}
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	response, err := e.provider.Generate(requestCtx, domainllm.GenerateRequest{
		SystemPrompt: "You evaluate one RenCrow Atlas Backlog item after maturation. Return exactly one JSON object and no prose. Evaluate all seven required dimensions: necessity, duplication, mergeability, architectural_consistency, technology_validity, implementation_value, timing. decision must be PROMOTE, MERGE, HOLD, or DROP. MERGE requires merged_into naming an existing related item. HOLD requires next_review_trigger. Use only supplied evidence; state uncertainty explicitly. Do not execute, adopt, implement, or mutate anything.",
		Messages:     []domainllm.Message{{Role: "user", Type: domainllm.PromptContextUser, Content: string(encoded)}},
		MaxTokens:    2048, Temperature: 0, ResponseFormat: domainllm.ResponseFormatJSONObject,
		ReasoningEffort: domainllm.ReasoningEffortLow,
	})
	if err != nil {
		return RevalidationEvaluation{}, err
	}
	var proposal RevalidationProposal
	decoder := json.NewDecoder(bytes.NewBufferString(strings.TrimSpace(response.Content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&proposal); err != nil {
		return RevalidationEvaluation{}, fmt.Errorf("decode Atlas revalidation proposal: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err == nil {
		return RevalidationEvaluation{}, errors.New("decode Atlas revalidation proposal: trailing JSON")
	} else if !errors.Is(err, io.EOF) {
		return RevalidationEvaluation{}, fmt.Errorf("decode Atlas revalidation proposal: %w", err)
	}
	if err := validateSemanticProposal(proposal); err != nil {
		return RevalidationEvaluation{}, err
	}
	return RevalidationEvaluation{Proposal: proposal, ReviewAgents: []string{e.reviewer}}, nil
}

func compactRevalidationEvidence(item domainbacklog.Item) revalidationEvidence {
	locators := make([]string, 0, min(len(item.SourceRefs), revalidationListLimit))
	for _, ref := range item.SourceRefs {
		if locator := boundedRevalidationText(ref.Locator); locator != "" {
			locators = append(locators, locator)
			if len(locators) == revalidationListLimit {
				break
			}
		}
	}
	return revalidationEvidence{
		ItemID: boundedRevalidationText(item.ItemID), Title: boundedRevalidationText(item.Title),
		Purpose: boundedRevalidationText(item.Purpose), Problem: boundedRevalidationText(item.Problem),
		Idea: boundedRevalidationText(item.Idea), Category: boundedRevalidationText(item.Category),
		ConceptState: item.ConceptState, DeliveryState: item.DeliveryState, MaturationState: item.MaturationState,
		TargetModules: boundedRevalidationList(item.TargetModules), AffectedModules: boundedRevalidationList(item.AffectedModules),
		Tags: boundedRevalidationList(item.Tags), DependsOn: boundedRevalidationList(item.DependsOn),
		RelatedIDs: boundedRevalidationList(item.RelatedIDs), SpecificationRefs: boundedRevalidationList(item.SpecificationRefs),
		SourceLocators: locators, MaturationStartedAt: item.MaturationStartedAt,
	}
}

func boundedRevalidationList(values []string) []string {
	if len(values) > revalidationListLimit {
		values = values[:revalidationListLimit]
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = boundedRevalidationText(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func boundedRevalidationText(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > revalidationTextRunes {
		runes = runes[:revalidationTextRunes]
	}
	return string(runes)
}

func validateSemanticProposal(proposal RevalidationProposal) error {
	if maturationTargetForDecision(normalizeDecision(proposal.Decision)) == "" || strings.TrimSpace(proposal.Reason) == "" {
		return ErrMaturationDecisionInvalid
	}
	required := []string{proposal.Necessity, proposal.Duplication, proposal.Mergeability, proposal.ArchitecturalFit, proposal.TechnologyValidity, proposal.ImplementationValue, proposal.Timing}
	for _, value := range required {
		if strings.TrimSpace(value) == "" {
			return errors.New("Atlas revalidation proposal omitted a required evaluation dimension")
		}
	}
	if normalizeDecision(proposal.Decision) == domainbacklog.RevalidationDecisionMerge && strings.TrimSpace(proposal.MergedInto) == "" {
		return errors.New("Atlas MERGE proposal requires merged_into")
	}
	if normalizeDecision(proposal.Decision) == domainbacklog.RevalidationDecisionHold && strings.TrimSpace(proposal.NextReviewTrigger) == "" {
		return errors.New("Atlas HOLD proposal requires next_review_trigger")
	}
	return nil
}
