package conversation

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

const (
	PreparedRecallMaxKeys                = 32
	PreparedRecallMaxPostingVisits       = 256
	PreparedRecallMaxCandidatesPerSource = 100
	PreparedRecallMaxRecordsPerSource    = 5000
	PreparedRecallMaxQueryRunes          = 256
	PreparedRecallMaxIndexKeysPerRecord  = 128
	PreparedRecallMaxIndexTextBytes      = 4096
	PreparedRecallMaxRecordBytes         = 16 * 1024
	PreparedRecallMaxViewBytes           = 8 * 1024 * 1024
	PreparedRecallDefaultKnowledgeLimit  = 3
	PreparedRecallMaxKnowledgeLimit      = 3

	PreparedRecallPreparationReady       = "ready"
	PreparedRecallPreparationStale       = "stale"
	PreparedRecallPreparationDenied      = "denied"
	PreparedRecallPreparationUnavailable = "unavailable"

	PreparedRecallMatchMatched       = "matched"
	PreparedRecallMatchNoMatch       = "no_match"
	PreparedRecallMatchBudgetLimited = "budget_limited"

	PreparedRecallCoverageFull    = "full"
	PreparedRecallCoveragePartial = "partial"
	PreparedRecallCoverageNone    = "none"

	PreparedRecallRepresentationStatement        = "confirmed_statement"
	PreparedRecallRepresentationValidatedSummary = "validated_summary"
)

const preparedRecallEstimatedIndexEntryBytes = 112

const (
	preparedRecallEstimatedUserMemoryBytes = 320
	preparedRecallEstimatedCategoryBytes   = 384
	preparedRecallMaxBindingBytes          = 512
	preparedRecallMaxRoleBytes             = 64
	preparedRecallBuildScratchBytes        = 128 * 1024
	PreparedRecallMaxAggregateBytes        = 2*PreparedRecallMaxViewBytes + preparedRecallBuildScratchBytes
)

var (
	ErrPreparedRecallCapacity     = errors.New("prepared recall memory or index capacity exceeded")
	ErrPreparedRecallMissingScope = errors.New("prepared recall category record is missing an explicit scope")
)

// PreparedRecallMemoryBudget reserves capacity across the snapshot being
// built and every other generation the owner still retains. RetainedBytes
// must include the current view, reader-held older views, and source/build
// input memory that remains live while this snapshot is constructed.
type PreparedRecallMemoryBudget struct {
	AggregateLimitBytes uint64
	RetainedBytes       uint64
}

type PreparedRecallCoverage struct {
	UserMemory string
	Knowledge  string
}

// PreparedRecallViewInput is the complete owner-bound input for one immutable
// source snapshot. SourceVersion is opaque and is copied from the owning
// layer; the selector never creates or orders versions.
type PreparedRecallViewInput struct {
	AuthenticatedOwnerID string
	UserScope            string
	SourceVersion        string
	// MemoryBudget is required. RetainedBytes accounts all other live views
	// and owner-supplied build data; the selector reserves this view plus its
	// bounded construction scratch against AggregateLimitBytes.
	MemoryBudget             PreparedRecallMemoryBudget
	Coverage                 PreparedRecallCoverage
	EligibleUserMemories     []domainmemory.UserMemory
	ValidatedCategoryRecords []CategoryRecallRecord
}

type preparedRecallSource uint8

const (
	preparedRecallUserMemory preparedRecallSource = iota + 1
	preparedRecallKnowledge
)

type preparedRecallPosting struct {
	source preparedRecallSource
	index  int
}

// PreparedRecallView is a read-only, in-memory inverse index. All record and
// slice data is copied at construction, and no mutable index is exposed.
type PreparedRecallView struct {
	authenticatedOwnerID string
	userScope            string
	sourceVersion        string
	coverage             PreparedRecallCoverage
	estimatedBytes       uint64
	userMemories         []domainmemory.UserMemory
	categoryRecords      []CategoryRecallRecord
	postings             map[string][]preparedRecallPosting
}

// EstimatedBytes returns the conservative accounted size of this immutable
// snapshot. The owner uses it when reserving memory for overlapping views.
func (v *PreparedRecallView) EstimatedBytes() uint64 {
	if v == nil {
		return 0
	}
	return v.estimatedBytes
}

type PreparedRecallSelectInput struct {
	AuthenticatedOwnerID  string
	UserScope             string
	ExpectedSourceVersion string
	Message               string
	Persona               string
	Role                  string
	// Now is the caller's evaluation time. A zero value makes the selection
	// unavailable instead of consulting an ambient clock.
	Now             time.Time
	UserMemoryLimit int
	KnowledgeLimit  int
}

// PreparedRecallKnowledge retains the validated source record and marks its
// Summary as a validated summary. Location references alone are never a
// selectable representation.
type PreparedRecallKnowledge struct {
	Record         CategoryRecallRecord
	Representation string
}

type PreparedRecallUserMemory struct {
	Decision       domainmemory.UserMemoryRecallDecision
	Representation string
}

type PreparedRecallTruncation struct {
	Keys bool
	// QueryRunes reports that only a bounded prefix was used for recall. The
	// caller's original message remains available to its normal prompt route.
	QueryRunes           bool
	PostingVisits        bool
	UserMemoryCandidates bool
	KnowledgeCandidates  bool
}

type PreparedRecallSelection struct {
	SourceVersion            string
	PreparationStatus        string
	MatchStatus              string
	Coverage                 PreparedRecallCoverage
	KeysUsed                 int
	PostingVisits            int
	UserMemoryCandidateCount int
	KnowledgeCandidateCount  int
	UserMemories             []PreparedRecallUserMemory
	Knowledge                []PreparedRecallKnowledge
	Truncated                PreparedRecallTruncation
}

// NewPreparedRecallView builds the index once from an owner-bound projection.
// Candidate records outside the authenticated user or scope are discarded
// before indexing, so their IDs and locations cannot appear in Select output.
// It rejects a snapshot that exceeds count, item, key, view, or aggregate
// budget limits instead of publishing it with misleading full coverage.
func NewPreparedRecallView(input PreparedRecallViewInput) (*PreparedRecallView, error) {
	if len(input.AuthenticatedOwnerID) > preparedRecallMaxBindingBytes || len(input.UserScope) > preparedRecallMaxBindingBytes || len(input.SourceVersion) > preparedRecallMaxBindingBytes {
		return nil, ErrPreparedRecallCapacity
	}
	if strings.TrimSpace(input.AuthenticatedOwnerID) == "" || strings.TrimSpace(input.UserScope) == "" || strings.TrimSpace(input.SourceVersion) == "" {
		return nil, errors.New("prepared recall owner, scope, and source version are required")
	}
	if !validPreparedRecallCoverage(input.Coverage.UserMemory) || !validPreparedRecallCoverage(input.Coverage.Knowledge) {
		return nil, errors.New("prepared recall coverage is invalid")
	}
	if input.MemoryBudget.AggregateLimitBytes == 0 || input.MemoryBudget.AggregateLimitBytes > PreparedRecallMaxAggregateBytes || input.MemoryBudget.RetainedBytes > input.MemoryBudget.AggregateLimitBytes {
		return nil, ErrPreparedRecallCapacity
	}
	estimatedBytes, err := estimatePreparedRecallViewBytes(input)
	if err != nil {
		return nil, err
	}
	aggregateAvailable := input.MemoryBudget.AggregateLimitBytes - input.MemoryBudget.RetainedBytes
	if estimatedBytes > PreparedRecallMaxViewBytes || uint64(preparedRecallBuildScratchBytes) > aggregateAvailable || estimatedBytes > aggregateAvailable-uint64(preparedRecallBuildScratchBytes) {
		return nil, ErrPreparedRecallCapacity
	}
	userMemoryCapacity := 0
	for _, candidate := range input.EligibleUserMemories {
		if candidate.UserID == input.AuthenticatedOwnerID {
			userMemoryCapacity++
		}
	}
	categoryCapacity := 0
	for _, candidate := range input.ValidatedCategoryRecords {
		if preparedRecallScopeMatches(candidate.Scope, input.UserScope) {
			categoryCapacity++
		}
	}

	view := &PreparedRecallView{
		authenticatedOwnerID: input.AuthenticatedOwnerID,
		userScope:            input.UserScope,
		sourceVersion:        input.SourceVersion,
		coverage:             input.Coverage,
		estimatedBytes:       estimatedBytes,
		postings:             make(map[string][]preparedRecallPosting),
		userMemories:         make([]domainmemory.UserMemory, 0, userMemoryCapacity),
		categoryRecords:      make([]CategoryRecallRecord, 0, categoryCapacity),
	}

	for _, candidate := range input.EligibleUserMemories {
		if candidate.UserID != input.AuthenticatedOwnerID {
			continue
		}
		memory := clonePreparedRecallUserMemory(candidate)
		index := len(view.userMemories)
		view.userMemories = append(view.userMemories, memory)
		keys, _ := preparedRecallKeys(memory.Statement, PreparedRecallMaxIndexKeysPerRecord)
		for _, key := range keys {
			view.postings[key] = append(view.postings[key], preparedRecallPosting{source: preparedRecallUserMemory, index: index})
		}
	}

	for _, candidate := range input.ValidatedCategoryRecords {
		if len(candidate.Scope) > PreparedRecallMaxRecordBytes {
			return nil, ErrPreparedRecallCapacity
		}
		if strings.TrimSpace(candidate.Scope) == "" {
			return nil, ErrPreparedRecallMissingScope
		}
		if !preparedRecallScopeMatches(candidate.Scope, input.UserScope) {
			continue
		}
		record := clonePreparedRecallCategoryRecord(candidate)
		index := len(view.categoryRecords)
		view.categoryRecords = append(view.categoryRecords, record)
		keys, _ := preparedRecallKeys(record.Title+" "+record.Summary, PreparedRecallMaxIndexKeysPerRecord)
		for _, key := range keys {
			view.postings[key] = append(view.postings[key], preparedRecallPosting{source: preparedRecallKnowledge, index: index})
		}
	}

	return view, nil
}

func estimatePreparedRecallViewBytes(input PreparedRecallViewInput) (uint64, error) {
	if len(input.EligibleUserMemories) > PreparedRecallMaxRecordsPerSource || len(input.ValidatedCategoryRecords) > PreparedRecallMaxRecordsPerSource {
		return 0, ErrPreparedRecallCapacity
	}
	total := uint64(512)
	for _, value := range []string{input.AuthenticatedOwnerID, input.UserScope, input.SourceVersion} {
		if !addPreparedRecallBytes(&total, uint64(len(value)), PreparedRecallMaxViewBytes) {
			return 0, ErrPreparedRecallCapacity
		}
	}
	for _, candidate := range input.EligibleUserMemories {
		if candidate.UserID != input.AuthenticatedOwnerID {
			continue
		}
		recordBytes, keys, err := estimatePreparedRecallUserMemory(candidate)
		if err != nil || !addPreparedRecallBytes(&total, recordBytes, PreparedRecallMaxViewBytes) {
			return 0, ErrPreparedRecallCapacity
		}
		for _, key := range keys {
			if !addPreparedRecallBytes(&total, uint64(preparedRecallEstimatedIndexEntryBytes+len(key)), PreparedRecallMaxViewBytes) {
				return 0, ErrPreparedRecallCapacity
			}
		}
	}
	for _, candidate := range input.ValidatedCategoryRecords {
		if len(candidate.Scope) > PreparedRecallMaxRecordBytes {
			return 0, ErrPreparedRecallCapacity
		}
		if strings.TrimSpace(candidate.Scope) == "" {
			return 0, ErrPreparedRecallMissingScope
		}
		if !preparedRecallScopeMatches(candidate.Scope, input.UserScope) {
			continue
		}
		recordBytes, keys, err := estimatePreparedRecallCategoryRecord(candidate)
		if err != nil || !addPreparedRecallBytes(&total, recordBytes, PreparedRecallMaxViewBytes) {
			return 0, ErrPreparedRecallCapacity
		}
		for _, key := range keys {
			if !addPreparedRecallBytes(&total, uint64(preparedRecallEstimatedIndexEntryBytes+len(key)), PreparedRecallMaxViewBytes) {
				return 0, ErrPreparedRecallCapacity
			}
		}
	}
	return total, nil
}

func estimatePreparedRecallUserMemory(item domainmemory.UserMemory) (uint64, []string, error) {
	if len(item.Statement) > PreparedRecallMaxIndexTextBytes || len(item.EvidenceEventIDs) > 128 {
		return 0, nil, ErrPreparedRecallCapacity
	}
	bytes := uint64(preparedRecallEstimatedUserMemoryBytes)
	for _, value := range []string{
		item.ID, item.Namespace, item.UserID, item.Type, item.Statement,
		string(item.CreatedByEventID), string(item.UpdatedByEventID), item.Sensitivity,
		item.State, item.Scope, item.LifecycleStatus, item.SupersededBy,
	} {
		if !addPreparedRecallBytes(&bytes, uint64(len(value)), PreparedRecallMaxRecordBytes) {
			return 0, nil, ErrPreparedRecallCapacity
		}
	}
	for _, eventID := range item.EvidenceEventIDs {
		if !addPreparedRecallBytes(&bytes, uint64(16+len(eventID)), PreparedRecallMaxRecordBytes) {
			return 0, nil, ErrPreparedRecallCapacity
		}
	}
	keys, truncated := preparedRecallKeys(item.Statement, PreparedRecallMaxIndexKeysPerRecord)
	if truncated {
		return 0, nil, ErrPreparedRecallCapacity
	}
	return bytes, keys, nil
}

func estimatePreparedRecallCategoryRecord(record CategoryRecallRecord) (uint64, []string, error) {
	if len(record.Title)+1+len(record.Summary) > PreparedRecallMaxIndexTextBytes || len(record.ProvenanceURLs)+len(record.Roles) > 128 {
		return 0, nil, ErrPreparedRecallCapacity
	}
	bytes := uint64(preparedRecallEstimatedCategoryBytes)
	for _, value := range []string{
		record.Category, record.SourceID, record.RecordID, record.Title, record.Summary,
		record.State, record.Sensitivity, record.Scope,
	} {
		if !addPreparedRecallBytes(&bytes, uint64(len(value)), PreparedRecallMaxRecordBytes) {
			return 0, nil, ErrPreparedRecallCapacity
		}
	}
	for _, value := range record.ProvenanceURLs {
		if !addPreparedRecallBytes(&bytes, uint64(16+len(value)), PreparedRecallMaxRecordBytes) {
			return 0, nil, ErrPreparedRecallCapacity
		}
	}
	for _, value := range record.Roles {
		if !addPreparedRecallBytes(&bytes, uint64(16+len(value)), PreparedRecallMaxRecordBytes) {
			return 0, nil, ErrPreparedRecallCapacity
		}
	}
	if source := record.PromptSource; source != nil {
		if !addPreparedRecallBytes(&bytes, 128, PreparedRecallMaxRecordBytes) {
			return 0, nil, ErrPreparedRecallCapacity
		}
		for _, value := range []string{source.Owner, source.SourceID, source.RawHash, source.ProjectionVersion, source.Origin} {
			if !addPreparedRecallBytes(&bytes, uint64(len(value)), PreparedRecallMaxRecordBytes) {
				return 0, nil, ErrPreparedRecallCapacity
			}
		}
	}
	keys, truncated := preparedRecallKeys(record.Title+" "+record.Summary, PreparedRecallMaxIndexKeysPerRecord)
	if truncated {
		return 0, nil, ErrPreparedRecallCapacity
	}
	return bytes, keys, nil
}

func addPreparedRecallBytes(total *uint64, amount, limit uint64) bool {
	if amount > limit || *total > limit-amount {
		return false
	}
	*total += amount
	return true
}

func validPreparedRecallCoverage(value string) bool {
	switch value {
	case PreparedRecallCoverageFull, PreparedRecallCoveragePartial, PreparedRecallCoverageNone:
		return true
	default:
		return false
	}
}

func preparedRecallScopeMatches(recordScope, userScope string) bool {
	scope := strings.ToLower(strings.TrimSpace(recordScope))
	switch scope {
	case "public", "all":
		return true
	default:
		return strings.EqualFold(strings.TrimSpace(recordScope), strings.TrimSpace(userScope))
	}
}

// Select reads only postings for at most 32 keys and visits at most 256
// posting entries. Candidate sets and ranking work are capped independently
// for personal memory and knowledge.
func (v *PreparedRecallView) Select(input PreparedRecallSelectInput) PreparedRecallSelection {
	if v == nil {
		return PreparedRecallSelection{
			PreparationStatus: PreparedRecallPreparationUnavailable,
			MatchStatus:       PreparedRecallMatchNoMatch,
			Coverage:          PreparedRecallCoverage{UserMemory: PreparedRecallCoverageNone, Knowledge: PreparedRecallCoverageNone},
		}
	}
	result := PreparedRecallSelection{
		PreparationStatus: PreparedRecallPreparationReady,
		MatchStatus:       PreparedRecallMatchNoMatch,
	}
	if input.AuthenticatedOwnerID != v.authenticatedOwnerID || input.UserScope != v.userScope {
		result.PreparationStatus = PreparedRecallPreparationDenied
		return result
	}
	result.SourceVersion = v.sourceVersion
	result.Coverage = v.coverage
	if input.ExpectedSourceVersion == "" || input.ExpectedSourceVersion != v.sourceVersion {
		result.PreparationStatus = PreparedRecallPreparationStale
		return result
	}
	if input.Now.IsZero() {
		result.PreparationStatus = PreparedRecallPreparationUnavailable
		return result
	}
	if len(input.Role) > preparedRecallMaxRoleBytes {
		result.PreparationStatus = PreparedRecallPreparationDenied
		return result
	}
	if !preparedRecallPersonaValid(input.Persona) {
		result.PreparationStatus = PreparedRecallPreparationDenied
		return result
	}

	boundedMessage, messageTruncated := preparedRecallBoundedQuery(input.Message)
	keys, keysTruncated := preparedRecallKeys(boundedMessage, PreparedRecallMaxKeys)
	result.KeysUsed = len(keys)
	result.Truncated.Keys = keysTruncated
	result.Truncated.QueryRunes = messageTruncated
	if len(keys) == 0 {
		return result
	}

	userMemoryIndices := make([]int, 0, PreparedRecallMaxCandidatesPerSource)
	knowledgeIndices := make([]int, 0, PreparedRecallMaxCandidatesPerSource)
	seenUserMemories := make(map[int]struct{}, PreparedRecallMaxCandidatesPerSource)
	seenKnowledge := make(map[int]struct{}, PreparedRecallMaxCandidatesPerSource)
	knowledgeKeyMatches := make(map[int]int, PreparedRecallMaxCandidatesPerSource)
	stop := false
	for _, key := range keys {
		postingList := v.postings[key]
		for i := 0; i < len(postingList); i++ {
			if result.PostingVisits >= PreparedRecallMaxPostingVisits {
				result.Truncated.PostingVisits = true
				stop = true
				break
			}
			posting := postingList[i]
			result.PostingVisits++
			switch posting.source {
			case preparedRecallUserMemory:
				if _, exists := seenUserMemories[posting.index]; exists {
					continue
				}
				if len(userMemoryIndices) >= PreparedRecallMaxCandidatesPerSource {
					result.Truncated.UserMemoryCandidates = true
					continue
				}
				seenUserMemories[posting.index] = struct{}{}
				userMemoryIndices = append(userMemoryIndices, posting.index)
			case preparedRecallKnowledge:
				if _, exists := seenKnowledge[posting.index]; exists {
					knowledgeKeyMatches[posting.index]++
					continue
				}
				if len(knowledgeIndices) >= PreparedRecallMaxCandidatesPerSource {
					result.Truncated.KnowledgeCandidates = true
					continue
				}
				seenKnowledge[posting.index] = struct{}{}
				knowledgeIndices = append(knowledgeIndices, posting.index)
				knowledgeKeyMatches[posting.index] = 1
			}
		}
		if stop {
			break
		}
	}
	result.UserMemoryCandidateCount = len(userMemoryIndices)
	result.KnowledgeCandidateCount = len(knowledgeIndices)

	personalCandidates := make([]domainmemory.UserMemory, 0, len(userMemoryIndices))
	for _, index := range userMemoryIndices {
		personalCandidates = append(personalCandidates, v.userMemories[index])
	}
	userMemoryLimit := input.UserMemoryLimit
	if userMemoryLimit <= 0 {
		userMemoryLimit = domainmemory.UserMemoryRecallDefaultLimit
	}
	if userMemoryLimit > domainmemory.UserMemoryRecallMaxLimit {
		userMemoryLimit = domainmemory.UserMemoryRecallMaxLimit
	}
	for _, decision := range domainmemory.RankUserMemoriesForRecallForPersona(boundedMessage, personalCandidates, userMemoryLimit, input.Persona) {
		if !decision.Selected {
			continue
		}
		decision.Item = clonePreparedRecallUserMemory(decision.Item)
		result.UserMemories = append(result.UserMemories, PreparedRecallUserMemory{
			Decision:       decision,
			Representation: PreparedRecallRepresentationStatement,
		})
	}

	now := input.Now
	role := input.Role
	if strings.TrimSpace(role) == "" {
		role = "chat"
	}
	knowledge := make([]preparedRecallRankedKnowledge, 0, len(knowledgeIndices))
	for _, index := range knowledgeIndices {
		record := v.categoryRecords[index]
		validationQuery := CategoryRecallQuery{Category: record.Category, UserScope: input.UserScope, Time: now}
		if _, rejected := validateCategoryRecallRecord(record, validationQuery); rejected {
			continue
		}
		if !preparedRecallCategoryRoleAllows(record, role) {
			continue
		}
		knowledge = append(knowledge, preparedRecallRankedKnowledge{
			record: record, keyMatches: knowledgeKeyMatches[index],
		})
	}
	sort.SliceStable(knowledge, func(i, j int) bool {
		if knowledge[i].keyMatches != knowledge[j].keyMatches {
			return knowledge[i].keyMatches > knowledge[j].keyMatches
		}
		if knowledge[i].record.Score != knowledge[j].record.Score {
			return knowledge[i].record.Score > knowledge[j].record.Score
		}
		if !knowledge[i].record.ValidatedAt.Equal(knowledge[j].record.ValidatedAt) {
			return knowledge[i].record.ValidatedAt.After(knowledge[j].record.ValidatedAt)
		}
		if knowledge[i].record.Category != knowledge[j].record.Category {
			return knowledge[i].record.Category < knowledge[j].record.Category
		}
		if knowledge[i].record.SourceID != knowledge[j].record.SourceID {
			return knowledge[i].record.SourceID < knowledge[j].record.SourceID
		}
		return knowledge[i].record.RecordID < knowledge[j].record.RecordID
	})
	knowledgeLimit := input.KnowledgeLimit
	if knowledgeLimit <= 0 {
		knowledgeLimit = PreparedRecallDefaultKnowledgeLimit
	}
	if knowledgeLimit > PreparedRecallMaxKnowledgeLimit {
		knowledgeLimit = PreparedRecallMaxKnowledgeLimit
	}
	if knowledgeLimit > len(knowledge) {
		knowledgeLimit = len(knowledge)
	}
	for _, candidate := range knowledge[:knowledgeLimit] {
		result.Knowledge = append(result.Knowledge, PreparedRecallKnowledge{
			Record:         clonePreparedRecallCategoryRecord(candidate.record),
			Representation: PreparedRecallRepresentationValidatedSummary,
		})
	}

	if result.Truncated.Keys || result.Truncated.QueryRunes || result.Truncated.PostingVisits || result.Truncated.UserMemoryCandidates || result.Truncated.KnowledgeCandidates {
		result.MatchStatus = PreparedRecallMatchBudgetLimited
	} else if len(result.UserMemories) > 0 || len(result.Knowledge) > 0 {
		result.MatchStatus = PreparedRecallMatchMatched
	}
	return result
}

type preparedRecallRankedKnowledge struct {
	record     CategoryRecallRecord
	keyMatches int
}

func preparedRecallCategoryRoleAllows(record CategoryRecallRecord, role string) bool {
	// The authenticated owner scope is already checked by the view and source
	// validator. Reuse prompt policy for role, state, sensitivity, and
	// provenance while keeping the two scope dimensions separate.
	candidate := categoryRecallCandidate(categorySnippetFromRecord(record))
	candidate.Scope = "public"
	return NewInjectionPolicy(role).Decide(candidate).Status == TraceStatusInjected
}

func preparedRecallKeys(text string, limit int) ([]string, bool) {
	if limit <= 0 || limit > PreparedRecallMaxIndexKeysPerRecord {
		limit = PreparedRecallMaxIndexKeysPerRecord
	}
	keys := make([]string, 0)
	seen := make(map[string]struct{}, limit)
	truncated := false
	add := func(key string) bool {
		if _, exists := seen[key]; exists {
			return true
		}
		if limit > 0 && len(keys) >= limit {
			truncated = true
			return false
		}
		ownedKey := strings.Clone(key)
		seen[ownedKey] = struct{}{}
		keys = append(keys, ownedKey)
		return true
	}

	for _, term := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	}) {
		if len(term) >= 2 && !add(term) {
			return keys, truncated
		}
	}

	normalized := strings.ToLower(strings.Join(strings.Fields(text), ""))
	runes := []rune(normalized)
	for i := 0; i+1 < len(runes); i++ {
		if !add(string(runes[i : i+2])) {
			return keys, truncated
		}
	}
	return keys, truncated
}

func preparedRecallBoundedQuery(text string) (string, bool) {
	var bounded strings.Builder
	runeCount := 0
	for _, value := range text {
		if runeCount >= PreparedRecallMaxQueryRunes {
			return bounded.String(), true
		}
		bounded.WriteRune(value)
		runeCount++
	}
	return bounded.String(), false
}

func preparedRecallPersonaValid(persona string) bool {
	if len(persona) > 64 || strings.TrimSpace(persona) == "" {
		return false
	}
	probes := []domainmemory.UserMemory{
		{ID: "persona-mio", Statement: "probe", State: domainmemory.MemoryStateConfirmed, Scope: "mio_only", Sensitivity: "normal", Active: true},
		{ID: "persona-shiro", Statement: "probe", State: domainmemory.MemoryStateConfirmed, Scope: "shiro_only", Sensitivity: "normal", Active: true},
		{ID: "persona-kuro", Statement: "probe", State: domainmemory.MemoryStateConfirmed, Scope: "kuro_only", Sensitivity: "normal", Active: true},
		{ID: "persona-midori", Statement: "probe", State: domainmemory.MemoryStateConfirmed, Scope: "midori_only", Sensitivity: "normal", Active: true},
	}
	selected := 0
	for _, decision := range domainmemory.RankUserMemoriesForRecallForPersona("probe", probes, len(probes), persona) {
		if decision.Selected {
			selected++
		}
	}
	return selected == 1
}

func clonePreparedRecallUserMemory(item domainmemory.UserMemory) domainmemory.UserMemory {
	item.EvidenceEventIDs = append([]string(nil), item.EvidenceEventIDs...)
	return item
}

func clonePreparedRecallCategoryRecord(record CategoryRecallRecord) CategoryRecallRecord {
	record.ProvenanceURLs = append([]string(nil), record.ProvenanceURLs...)
	record.Roles = append([]string(nil), record.Roles...)
	record.PromptSource = llm.ClonePromptSourceRef(record.PromptSource)
	return record
}
