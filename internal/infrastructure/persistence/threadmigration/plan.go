// Package threadmigration owns the one-shot Step 05 identity migration logic.
//
// Its mapping and transformation core is deterministic. Read-only inventory
// consumes caller-owned stores, while later materialization is restricted to
// disposable migration outputs; runtime routes must never import this package.
package threadmigration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	"github.com/google/uuid"
)

const (
	genericSourceTable  = "conversation_thread"
	genericSourceField  = "session_id+legacy_thread_id"
	chatGPTSourceTable  = "l1_raw_record"
	chatGPTSessionField = "session_id"
	chatGPTThreadField  = "thread_id"
)

// LegacyThreadFact is one observed legacy thread identity. Surface and
// RecordKey identify where the fact came from; they never influence the
// canonical generic ThreadID. ChatGPTConversationID selects the special
// ChatGPT projection and must not be combined with generic identity fields.
type LegacyThreadFact struct {
	Surface               string `json:"surface"`
	RecordKey             string `json:"record_key"`
	SessionID             string `json:"session_id"`
	LegacyThreadID        int64  `json:"legacy_thread_id"`
	KindHint              string `json:"kind_hint"`
	ChatGPTConversationID string `json:"chatgpt_conversation_id,omitempty"`
}

// ThreadSource records the source references that contributed to one
// deduplicated semantic mapping. SourceSessionID preserves the original
// session value so a legacy session conversion remains auditable while the
// mapping's SessionID is canonical.
type ThreadSource struct {
	Surface         string `json:"surface"`
	RecordKey       string `json:"record_key"`
	SourceSessionID string `json:"source_session_id,omitempty"`
}

// ThreadMapping is one canonical conversation-thread identity. Generic
// mappings are grouped by (SessionID, LegacyThreadID), while ChatGPT mappings
// are grouped by ChatGPTConversationID. Sources contains every deduplicated
// source reference for the group and is sorted deterministically.
type ThreadMapping struct {
	SemanticKey string `json:"semantic_key"`

	// Surface and RecordKey are the deterministic first source reference. The
	// complete set is available in Sources for lookup and audit consumers.
	Surface   string         `json:"surface"`
	RecordKey string         `json:"record_key"`
	Sources   []ThreadSource `json:"sources"`

	SessionID  modulecore.SessionID  `json:"session_id"`
	ThreadID   modulecore.ThreadID   `json:"thread_id"`
	ThreadSeq  modulecore.ThreadSeq  `json:"thread_seq"`
	ThreadKind modulecore.ThreadKind `json:"thread_kind"`

	// LegacyThreadID is present only for generic mappings. It makes the
	// ThreadID -> ThreadSeq replacement explicit in the plan without retaining
	// a legacy field in any runtime route.
	LegacyThreadID int64 `json:"legacy_thread_id,omitempty"`

	// ChatGPTConversationID is present only for ChatGPT mappings.
	ChatGPTConversationID string `json:"chatgpt_conversation_id,omitempty"`
}

// Plan is the complete deterministic Step 05 mapping output. The lookup
// indexes are derived from Generic and ChatGPT and are intentionally private;
// they are not an additional source of truth and never enter the hash.
type Plan struct {
	Generic []ThreadMapping `json:"generic"`
	ChatGPT []ThreadMapping `json:"chatgpt"`

	MappingSHA256 string `json:"mapping_sha256"`

	recordLookup  map[sourceReference]ThreadMapping
	genericLookup map[genericGroupKey]ThreadMapping
	chatGPTLookup map[string]ThreadMapping
}

type sourceReference struct {
	surface   string
	recordKey string
}

type semanticReference struct {
	chatGPT bool
	key     string
}

type genericGroupKey struct {
	sessionID      string
	legacyThreadID int64
}

type genericGroup struct {
	key       genericGroupKey
	kind      modulecore.ThreadKind
	sourceSet map[ThreadSource]struct{}
}

type chatGPTGroup struct {
	conversationID string
	sourceSet      map[ThreadSource]struct{}
}

type sourceIdentity struct {
	chatGPT        bool
	sessionID      string
	legacyThreadID int64
	kind           modulecore.ThreadKind
	conversationID string
}

type semanticTuple struct {
	sessionID string
	threadSeq modulecore.ThreadSeq
}

type normalizedFact struct {
	surface               string
	recordKey             string
	sourceSessionID       string
	sessionID             string
	legacyThreadID        int64
	kind                  modulecore.ThreadKind
	chatGPTConversationID string
	chatGPT               bool
}

// GenericSemanticKey returns the canonical, unambiguous source value used by
// NewMigrationID for a generic legacy thread. A NUL separator is used by the
// existing migration contracts for composite source fields; the old number is
// always rendered in canonical base-10 form. The session ID is already the
// resulting canonical value; it is intentionally not trimmed here because
// NewMigrationID source values are byte-exact.
func GenericSemanticKey(sessionID string, legacyThreadID int64) string {
	return sessionID + "\x00" + strconv.FormatInt(legacyThreadID, 10)
}

// ChatGPTSemanticKey returns the exact nonempty source value used as the
// semantic key for a ChatGPT conversation. The derived SessionID and ThreadID
// use separate source fields and are exposed on the resulting mapping.
func ChatGPTSemanticKey(conversationID string) string {
	return conversationID
}

// BuildPlan validates and maps legacy facts without touching any external
// state. Equal generic (SessionID, LegacyThreadID) groups and equal ChatGPT
// conversation groups are emitted once, while every source record remains
// available through LookupBySource.
func BuildPlan(facts []LegacyThreadFact) (Plan, error) {
	genericGroups := make(map[genericGroupKey]*genericGroup)
	chatGPTGroups := make(map[string]*chatGPTGroup)
	recordIdentities := make(map[sourceReference]sourceIdentity)

	for index, fact := range facts {
		normalized, err := normalizeFact(fact)
		if err != nil {
			return Plan{}, fmt.Errorf("legacy thread fact %d: %w", index, err)
		}

		identity := normalized.identity()
		reference := sourceReference{surface: normalized.surface, recordKey: normalized.recordKey}
		if previous, exists := recordIdentities[reference]; exists && previous != identity {
			return Plan{}, fmt.Errorf("source %q/%q carries contradictory thread identity", reference.surface, reference.recordKey)
		}
		recordIdentities[reference] = identity

		source := ThreadSource{Surface: normalized.surface, RecordKey: normalized.recordKey, SourceSessionID: normalized.sourceSessionID}
		if normalized.chatGPT {
			group := chatGPTGroups[normalized.chatGPTConversationID]
			if group == nil {
				group = &chatGPTGroup{
					conversationID: normalized.chatGPTConversationID,
					sourceSet:      make(map[ThreadSource]struct{}),
				}
				chatGPTGroups[normalized.chatGPTConversationID] = group
			}
			group.sourceSet[source] = struct{}{}
			continue
		}

		key := genericGroupKey{sessionID: normalized.sessionID, legacyThreadID: normalized.legacyThreadID}
		group := genericGroups[key]
		if group == nil {
			group = &genericGroup{
				key:       key,
				kind:      normalized.kind,
				sourceSet: make(map[ThreadSource]struct{}),
			}
			genericGroups[key] = group
		} else if group.kind != normalized.kind {
			return Plan{}, fmt.Errorf("generic thread group (%q, %d) carries contradictory thread kind", key.sessionID, key.legacyThreadID)
		}
		group.sourceSet[source] = struct{}{}
	}

	generic := make([]ThreadMapping, 0, len(genericGroups))
	for _, group := range genericGroups {
		mapping, err := newGenericMapping(*group)
		if err != nil {
			return Plan{}, err
		}
		generic = append(generic, mapping)
	}
	chatGPT := make([]ThreadMapping, 0, len(chatGPTGroups))
	for _, group := range chatGPTGroups {
		mapping, err := newChatGPTMapping(*group)
		if err != nil {
			return Plan{}, err
		}
		chatGPT = append(chatGPT, mapping)
	}
	sortThreadMappings(generic)
	sortThreadMappings(chatGPT)

	if err := validateMappingSets(generic, chatGPT); err != nil {
		return Plan{}, err
	}
	plan := Plan{Generic: generic, ChatGPT: chatGPT}
	hash, err := plan.ComputeMappingSHA256()
	if err != nil {
		return Plan{}, fmt.Errorf("hash thread mapping plan: %w", err)
	}
	plan.MappingSHA256 = hash
	plan.buildLookupIndexes()
	return plan, nil
}

// MergePlans combines independently inventoried mapping plans into one
// deterministic cohort plan. A semantic mapping may be repeated by multiple
// surfaces, but only its source evidence is unioned; canonical identity
// fields are never reconciled or guessed. A disagreement is rejected before
// any merged plan is returned.
func MergePlans(plans ...Plan) (Plan, error) {
	if len(plans) == 0 {
		return Plan{}, fmt.Errorf("merge thread mapping plans: at least one plan is required")
	}

	mergedGeneric := make(map[semanticReference]ThreadMapping)
	mergedChatGPT := make(map[semanticReference]ThreadMapping)
	for planIndex, plan := range plans {
		if err := plan.Validate(); err != nil {
			return Plan{}, fmt.Errorf("merge thread mapping plan %d: %w", planIndex, err)
		}
		for _, mapping := range plan.Generic {
			if err := mergeThreadMapping(mergedGeneric, mapping, false); err != nil {
				return Plan{}, fmt.Errorf("merge generic mapping from plan %d: %w", planIndex, err)
			}
		}
		for _, mapping := range plan.ChatGPT {
			if err := mergeThreadMapping(mergedChatGPT, mapping, true); err != nil {
				return Plan{}, fmt.Errorf("merge ChatGPT mapping from plan %d: %w", planIndex, err)
			}
		}
	}

	generic := make([]ThreadMapping, 0, len(mergedGeneric))
	for _, mapping := range mergedGeneric {
		mapping.Sources = sortedSources(sourceSetForMapping(mapping))
		setPrimarySource(&mapping)
		generic = append(generic, mapping)
	}
	chatGPT := make([]ThreadMapping, 0, len(mergedChatGPT))
	for _, mapping := range mergedChatGPT {
		mapping.Sources = sortedSources(sourceSetForMapping(mapping))
		setPrimarySource(&mapping)
		chatGPT = append(chatGPT, mapping)
	}
	sortThreadMappings(generic)
	sortThreadMappings(chatGPT)
	if err := validateMappingSets(generic, chatGPT); err != nil {
		return Plan{}, fmt.Errorf("validate merged thread mapping plan: %w", err)
	}
	merged := Plan{Generic: generic, ChatGPT: chatGPT}
	hash, err := merged.ComputeMappingSHA256()
	if err != nil {
		return Plan{}, fmt.Errorf("hash merged thread mapping plan: %w", err)
	}
	merged.MappingSHA256 = hash
	merged.buildLookupIndexes()
	if err := merged.Validate(); err != nil {
		return Plan{}, fmt.Errorf("validate merged thread mapping plan: %w", err)
	}
	return merged, nil
}

func mergeThreadMapping(destination map[semanticReference]ThreadMapping, incoming ThreadMapping, chatGPT bool) error {
	key := semanticReference{chatGPT: chatGPT, key: incoming.SemanticKey}
	if existing, ok := destination[key]; ok {
		if !sameThreadMappingIdentity(existing, incoming) {
			return fmt.Errorf("semantic mapping %q has contradictory canonical identity", incoming.SemanticKey)
		}
		for _, source := range incoming.Sources {
			existing.Sources = append(existing.Sources, source)
		}
		destination[key] = existing
		return nil
	}
	destination[key] = cloneMapping(incoming)
	return nil
}

func sameThreadMappingIdentity(left, right ThreadMapping) bool {
	return left.SessionID == right.SessionID &&
		left.ThreadID == right.ThreadID &&
		left.ThreadSeq == right.ThreadSeq &&
		left.ThreadKind == right.ThreadKind &&
		left.LegacyThreadID == right.LegacyThreadID &&
		left.ChatGPTConversationID == right.ChatGPTConversationID
}

func sourceSetForMapping(mapping ThreadMapping) map[ThreadSource]struct{} {
	set := make(map[ThreadSource]struct{}, len(mapping.Sources))
	for _, source := range mapping.Sources {
		set[source] = struct{}{}
	}
	return set
}

func normalizeFact(fact LegacyThreadFact) (normalizedFact, error) {
	normalized := normalizedFact{
		surface:               strings.TrimSpace(fact.Surface),
		recordKey:             strings.TrimSpace(fact.RecordKey),
		sourceSessionID:       fact.SessionID,
		legacyThreadID:        fact.LegacyThreadID,
		chatGPTConversationID: fact.ChatGPTConversationID,
	}
	if normalized.surface == "" {
		return normalizedFact{}, fmt.Errorf("surface is required")
	}
	if normalized.recordKey == "" {
		return normalizedFact{}, fmt.Errorf("record key is required")
	}
	if normalized.chatGPTConversationID != "" && strings.TrimSpace(normalized.chatGPTConversationID) == "" {
		return normalizedFact{}, fmt.Errorf("ChatGPT conversation ID is required")
	}
	if normalized.chatGPTConversationID != "" {
		if normalized.sourceSessionID != "" || fact.LegacyThreadID != 0 || strings.TrimSpace(fact.KindHint) != "" {
			return normalizedFact{}, fmt.Errorf("ChatGPT conversation %q is mixed with generic thread identity", normalized.chatGPTConversationID)
		}
		normalized.chatGPT = true
		normalized.kind = modulecore.ThreadKindUserConversation
		return normalized, nil
	}
	if strings.TrimSpace(normalized.sourceSessionID) == "" {
		return normalizedFact{}, fmt.Errorf("session ID is required")
	}
	canonicalSessionID, err := canonicalGenericSessionID(normalized.sourceSessionID)
	if err != nil {
		return normalizedFact{}, fmt.Errorf("session ID: %w", err)
	}
	normalized.sessionID = canonicalSessionID
	if normalized.legacyThreadID <= 0 {
		return normalizedFact{}, fmt.Errorf("legacy thread ID must be positive")
	}
	normalized.kind = modulecore.ThreadKindUserConversation
	if kindHint := strings.TrimSpace(fact.KindHint); kindHint != "" {
		normalized.kind = modulecore.ThreadKind(kindHint)
	}
	if err := normalized.kind.Validate(); err != nil {
		return normalizedFact{}, fmt.Errorf("thread kind: %w", err)
	}
	return normalized, nil
}

func (fact normalizedFact) identity() sourceIdentity {
	return sourceIdentity{
		chatGPT:        fact.chatGPT,
		sessionID:      fact.sessionID,
		legacyThreadID: fact.legacyThreadID,
		kind:           fact.kind,
		conversationID: fact.chatGPTConversationID,
	}
}

func (mapping ThreadMapping) identity(chatGPT bool) sourceIdentity {
	return sourceIdentity{
		chatGPT:        chatGPT,
		sessionID:      string(mapping.SessionID),
		legacyThreadID: mapping.LegacyThreadID,
		kind:           mapping.ThreadKind,
		conversationID: mapping.ChatGPTConversationID,
	}
}

func canonicalGenericSessionID(sourceSessionID string) (string, error) {
	canonical := modulecore.SessionID(sourceSessionID)
	if err := canonical.Validate(); err == nil {
		return string(canonical), nil
	}
	raw, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "session_files", "id", sourceSessionID)
	if err != nil {
		return "", err
	}
	canonical = modulecore.SessionID(raw)
	if err := canonical.Validate(); err != nil {
		return "", err
	}
	return string(canonical), nil
}

func newGenericMapping(group genericGroup) (ThreadMapping, error) {
	semanticKey := GenericSemanticKey(group.key.sessionID, group.key.legacyThreadID)
	rawThreadID, err := modulecore.NewMigrationID(modulecore.CanonicalThreadID, genericSourceTable, genericSourceField, semanticKey)
	if err != nil {
		return ThreadMapping{}, fmt.Errorf("map generic thread (%q, %d): %w", group.key.sessionID, group.key.legacyThreadID, err)
	}
	mapping := ThreadMapping{
		SemanticKey:    semanticKey,
		SessionID:      modulecore.SessionID(group.key.sessionID),
		ThreadID:       modulecore.ThreadID(rawThreadID),
		ThreadSeq:      modulecore.ThreadSeq(group.key.legacyThreadID),
		ThreadKind:     group.kind,
		LegacyThreadID: group.key.legacyThreadID,
		Sources:        sortedSources(group.sourceSet),
	}
	setPrimarySource(&mapping)
	if err := validateMigrationMapping(mapping, false); err != nil {
		return ThreadMapping{}, fmt.Errorf("generic thread mapping: %w", err)
	}
	return mapping, nil
}

func newChatGPTMapping(group chatGPTGroup) (ThreadMapping, error) {
	conversationID := ChatGPTSemanticKey(group.conversationID)
	sessionRaw, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, chatGPTSourceTable, chatGPTSessionField, conversationID)
	if err != nil {
		return ThreadMapping{}, fmt.Errorf("map ChatGPT conversation %q to session: %w", conversationID, err)
	}
	threadRaw, err := modulecore.NewMigrationID(modulecore.CanonicalThreadID, chatGPTSourceTable, chatGPTThreadField, conversationID)
	if err != nil {
		return ThreadMapping{}, fmt.Errorf("map ChatGPT conversation %q to thread: %w", conversationID, err)
	}
	mapping := ThreadMapping{
		SemanticKey:           conversationID,
		SessionID:             modulecore.SessionID(sessionRaw),
		ThreadID:              modulecore.ThreadID(threadRaw),
		ThreadSeq:             modulecore.ThreadSeq(1),
		ThreadKind:            modulecore.ThreadKindUserConversation,
		ChatGPTConversationID: conversationID,
		Sources:               sortedSources(group.sourceSet),
	}
	setPrimarySource(&mapping)
	if err := validateMigrationMapping(mapping, true); err != nil {
		return ThreadMapping{}, fmt.Errorf("ChatGPT thread mapping: %w", err)
	}
	return mapping, nil
}

func sortedSources(sourceSet map[ThreadSource]struct{}) []ThreadSource {
	sources := make([]ThreadSource, 0, len(sourceSet))
	for source := range sourceSet {
		sources = append(sources, source)
	}
	sort.Slice(sources, func(left, right int) bool {
		if sources[left].Surface != sources[right].Surface {
			return sources[left].Surface < sources[right].Surface
		}
		if sources[left].RecordKey != sources[right].RecordKey {
			return sources[left].RecordKey < sources[right].RecordKey
		}
		return sources[left].SourceSessionID < sources[right].SourceSessionID
	})
	return sources
}

func setPrimarySource(mapping *ThreadMapping) {
	if len(mapping.Sources) == 0 {
		return
	}
	mapping.Surface = mapping.Sources[0].Surface
	mapping.RecordKey = mapping.Sources[0].RecordKey
}

func sortThreadMappings(mappings []ThreadMapping) {
	sort.Slice(mappings, func(left, right int) bool {
		return mappingLess(mappings[left], mappings[right])
	})
}

func validateMappingSets(generic, chatGPT []ThreadMapping) error {
	seenThreadIDs := make(map[modulecore.ThreadID]struct{}, len(generic)+len(chatGPT))
	seenTuples := make(map[semanticTuple]struct {
		threadID modulecore.ThreadID
		kind     modulecore.ThreadKind
	}, len(generic)+len(chatGPT))
	seenSemanticKeys := make(map[semanticReference]struct{}, len(generic)+len(chatGPT))
	seenSources := make(map[sourceReference]sourceIdentity)
	for _, set := range [][]ThreadMapping{generic, chatGPT} {
		for _, mapping := range set {
			isChatGPT := mapping.ChatGPTConversationID != ""
			if err := validateMigrationMapping(mapping, isChatGPT); err != nil {
				return err
			}
			semantic := semanticReference{chatGPT: isChatGPT, key: mapping.SemanticKey}
			if _, exists := seenSemanticKeys[semantic]; exists {
				return fmt.Errorf("duplicate thread semantic key %q", mapping.SemanticKey)
			}
			seenSemanticKeys[semantic] = struct{}{}
			identity := mapping.identity(isChatGPT)
			for _, source := range mapping.Sources {
				reference := sourceReference{surface: source.Surface, recordKey: source.RecordKey}
				if previous, exists := seenSources[reference]; exists && previous != identity {
					return fmt.Errorf("source %q/%q maps to contradictory thread identities", reference.surface, reference.recordKey)
				}
				seenSources[reference] = identity
			}

			threadID := mapping.ThreadID
			tuple := semanticTuple{sessionID: string(mapping.SessionID), threadSeq: mapping.ThreadSeq}
			if _, exists := seenThreadIDs[threadID]; exists {
				return fmt.Errorf("duplicate resulting canonical ThreadID %q", threadID)
			}
			seenThreadIDs[threadID] = struct{}{}
			if previous, exists := seenTuples[tuple]; exists && (previous.threadID != threadID || previous.kind != mapping.ThreadKind) {
				return fmt.Errorf("same (SessionID, ThreadSeq) maps to different ThreadID or ThreadKind: session=%q seq=%d", tuple.sessionID, tuple.threadSeq)
			}
			seenTuples[tuple] = struct {
				threadID modulecore.ThreadID
				kind     modulecore.ThreadKind
			}{threadID: threadID, kind: mapping.ThreadKind}
		}
	}
	return nil
}

func validateMigrationMapping(mapping ThreadMapping, chatGPT bool) error {
	if mapping.SemanticKey == "" {
		return fmt.Errorf("thread mapping semantic key is required")
	}
	if err := mapping.ThreadID.Validate(); err != nil {
		return fmt.Errorf("thread ID %q: %w", mapping.ThreadID, err)
	}
	parsed, err := uuid.Parse(strings.TrimPrefix(string(mapping.ThreadID), "thr_"))
	if err != nil || parsed.Version() != 5 {
		return fmt.Errorf("thread ID %q must contain UUIDv5", mapping.ThreadID)
	}
	if err := mapping.ThreadSeq.Validate(); err != nil {
		return fmt.Errorf("thread sequence: %w", err)
	}
	if err := mapping.ThreadKind.Validate(); err != nil {
		return fmt.Errorf("thread kind: %w", err)
	}
	if len(mapping.Sources) == 0 {
		return fmt.Errorf("thread mapping %q has no source records", mapping.SemanticKey)
	}
	if mapping.Surface != mapping.Sources[0].Surface || mapping.RecordKey != mapping.Sources[0].RecordKey {
		return fmt.Errorf("thread mapping %q has unstable primary source", mapping.SemanticKey)
	}
	for index, source := range mapping.Sources {
		if strings.TrimSpace(source.Surface) == "" || strings.TrimSpace(source.RecordKey) == "" {
			return fmt.Errorf("thread mapping %q has an incomplete source record", mapping.SemanticKey)
		}
		if chatGPT && source.SourceSessionID != "" {
			return fmt.Errorf("ChatGPT thread mapping %q has a generic source session", mapping.SemanticKey)
		}
		if !chatGPT && source.SourceSessionID == "" {
			return fmt.Errorf("generic thread mapping %q has no source session", mapping.SemanticKey)
		}
		if index == 0 {
			continue
		}
		previous, current := mapping.Sources[index-1], mapping.Sources[index]
		if sourceLess(current, previous) || !sourceLess(previous, current) {
			return fmt.Errorf("thread mapping %q sources are not strictly sorted", mapping.SemanticKey)
		}
	}
	if chatGPT {
		if mapping.ChatGPTConversationID == "" || mapping.SemanticKey != mapping.ChatGPTConversationID || mapping.LegacyThreadID != 0 || mapping.ThreadSeq != 1 || mapping.ThreadKind != modulecore.ThreadKindUserConversation {
			return fmt.Errorf("invalid ChatGPT thread mapping %q", mapping.SemanticKey)
		}
		if err := mapping.SessionID.Validate(); err != nil {
			return fmt.Errorf("ChatGPT session ID %q: %w", mapping.SessionID, err)
		}
		expectedSessionID, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, chatGPTSourceTable, chatGPTSessionField, mapping.ChatGPTConversationID)
		if err != nil || string(mapping.SessionID) != expectedSessionID {
			return fmt.Errorf("ChatGPT session ID %q does not match conversation %q", mapping.SessionID, mapping.ChatGPTConversationID)
		}
		expectedThreadID, err := modulecore.NewMigrationID(modulecore.CanonicalThreadID, chatGPTSourceTable, chatGPTThreadField, mapping.ChatGPTConversationID)
		if err != nil || string(mapping.ThreadID) != expectedThreadID {
			return fmt.Errorf("ChatGPT thread ID %q does not match conversation %q", mapping.ThreadID, mapping.ChatGPTConversationID)
		}
		return nil
	}
	if err := mapping.SessionID.Validate(); err != nil {
		return fmt.Errorf("session ID %q: %w", mapping.SessionID, err)
	}
	if mapping.ChatGPTConversationID != "" || mapping.LegacyThreadID <= 0 || mapping.ThreadSeq != modulecore.ThreadSeq(mapping.LegacyThreadID) || mapping.SemanticKey != GenericSemanticKey(string(mapping.SessionID), mapping.LegacyThreadID) {
		return fmt.Errorf("invalid generic thread mapping %q", mapping.SemanticKey)
	}
	expectedThreadID, err := modulecore.NewMigrationID(modulecore.CanonicalThreadID, genericSourceTable, genericSourceField, mapping.SemanticKey)
	if err != nil || string(mapping.ThreadID) != expectedThreadID {
		return fmt.Errorf("generic thread ID %q does not match semantic key %q", mapping.ThreadID, mapping.SemanticKey)
	}
	return nil
}

// CanonicalJSON returns the deterministic JSON payload that is hashed for the
// plan. MappingSHA256 is intentionally excluded to avoid hashing a value that
// contains its own digest. Struct field order and sorted arrays make this
// independent of Go map iteration order and input fact order.
func (plan Plan) CanonicalJSON() ([]byte, error) {
	payload := struct {
		Generic []ThreadMapping `json:"generic"`
		ChatGPT []ThreadMapping `json:"chatgpt"`
	}{
		Generic: canonicalMappings(plan.Generic),
		ChatGPT: canonicalMappings(plan.ChatGPT),
	}
	return json.Marshal(payload)
}

// ComputeMappingSHA256 computes the lowercase SHA-256 digest of CanonicalJSON.
func (plan Plan) ComputeMappingSHA256() (string, error) {
	encoded, err := plan.CanonicalJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalMappings(mappings []ThreadMapping) []ThreadMapping {
	ordered := make([]ThreadMapping, len(mappings))
	for index, mapping := range mappings {
		ordered[index] = mapping
		ordered[index].Sources = append([]ThreadSource(nil), mapping.Sources...)
	}
	sortThreadMappings(ordered)
	for index := range ordered {
		sort.Slice(ordered[index].Sources, func(left, right int) bool {
			return sourceLess(ordered[index].Sources[left], ordered[index].Sources[right])
		})
	}
	if ordered == nil {
		return []ThreadMapping{}
	}
	return ordered
}

func sourceLess(left, right ThreadSource) bool {
	if left.Surface != right.Surface {
		return left.Surface < right.Surface
	}
	if left.RecordKey != right.RecordKey {
		return left.RecordKey < right.RecordKey
	}
	return left.SourceSessionID < right.SourceSessionID
}

// Validate checks mapping shape, UUIDv5/thread enum contracts, identity
// uniqueness, and the required stored mapping digest.
func (plan Plan) Validate() error {
	if err := validateMappingSets(plan.Generic, plan.ChatGPT); err != nil {
		return err
	}
	if !mappingSliceSorted(plan.Generic) || !mappingSliceSorted(plan.ChatGPT) {
		return fmt.Errorf("thread mappings are not stably sorted")
	}
	if len(plan.MappingSHA256) != sha256.Size*2 || plan.MappingSHA256 != strings.ToLower(plan.MappingSHA256) {
		return fmt.Errorf("mapping SHA256 is invalid")
	}
	if _, err := hex.DecodeString(plan.MappingSHA256); err != nil {
		return fmt.Errorf("mapping SHA256 is invalid: %w", err)
	}
	computed, err := plan.ComputeMappingSHA256()
	if err != nil {
		return fmt.Errorf("compute mapping SHA256: %w", err)
	}
	if computed != plan.MappingSHA256 {
		return fmt.Errorf("mapping SHA256 does not match canonical JSON")
	}
	return nil
}

func mappingSliceSorted(mappings []ThreadMapping) bool {
	for index := 1; index < len(mappings); index++ {
		left, right := mappings[index-1], mappings[index]
		if mappingLess(right, left) {
			return false
		}
	}
	return true
}

func mappingLess(left, right ThreadMapping) bool {
	if left.ChatGPTConversationID != "" || right.ChatGPTConversationID != "" {
		if left.SemanticKey != right.SemanticKey {
			return left.SemanticKey < right.SemanticKey
		}
	}
	if string(left.SessionID) != string(right.SessionID) {
		return string(left.SessionID) < string(right.SessionID)
	}
	if left.ThreadSeq != right.ThreadSeq {
		return left.ThreadSeq < right.ThreadSeq
	}
	if left.ThreadKind != right.ThreadKind {
		return left.ThreadKind < right.ThreadKind
	}
	return string(left.ThreadID) < string(right.ThreadID)
}

func (plan *Plan) buildLookupIndexes() {
	if plan == nil {
		return
	}
	plan.recordLookup = make(map[sourceReference]ThreadMapping, len(plan.Generic)+len(plan.ChatGPT))
	plan.genericLookup = make(map[genericGroupKey]ThreadMapping, len(plan.Generic))
	plan.chatGPTLookup = make(map[string]ThreadMapping, len(plan.ChatGPT))
	for _, mapping := range append(append([]ThreadMapping{}, plan.Generic...), plan.ChatGPT...) {
		isChatGPT := mapping.ChatGPTConversationID != ""
		if isChatGPT {
			plan.chatGPTLookup[mapping.ChatGPTConversationID] = cloneMapping(mapping)
		} else {
			key := genericGroupKey{sessionID: string(mapping.SessionID), legacyThreadID: mapping.LegacyThreadID}
			plan.genericLookup[key] = cloneMapping(mapping)
		}
		for _, source := range mapping.Sources {
			reference := sourceReference{surface: source.Surface, recordKey: source.RecordKey}
			plan.recordLookup[reference] = cloneMapping(mapping)
		}
	}
}

func cloneMapping(mapping ThreadMapping) ThreadMapping {
	mapping.Sources = append([]ThreadSource(nil), mapping.Sources...)
	return mapping
}

// LookupBySource resolves a source surface and record key to its deduplicated
// canonical mapping. Surface and record key follow the planner's audit
// normalization; semantic source values remain byte-exact.
func (plan Plan) LookupBySource(surface, recordKey string) (ThreadMapping, bool) {
	reference := sourceReference{surface: strings.TrimSpace(surface), recordKey: strings.TrimSpace(recordKey)}
	if mapping, ok := plan.recordLookup[reference]; ok {
		return cloneMapping(mapping), true
	}
	for _, mapping := range append(append([]ThreadMapping{}, plan.Generic...), plan.ChatGPT...) {
		for _, source := range mapping.Sources {
			if source.Surface == reference.surface && source.RecordKey == reference.recordKey {
				return cloneMapping(mapping), true
			}
		}
	}
	return ThreadMapping{}, false
}

// LookupGeneric resolves a canonical generic session and legacy numeric thread
// identity. The session argument is the resulting canonical SessionID.
func (plan Plan) LookupGeneric(sessionID string, legacyThreadID int64) (ThreadMapping, bool) {
	if legacyThreadID <= 0 {
		return ThreadMapping{}, false
	}
	key := genericGroupKey{sessionID: sessionID, legacyThreadID: legacyThreadID}
	if mapping, ok := plan.genericLookup[key]; ok {
		return cloneMapping(mapping), true
	}
	for _, mapping := range plan.Generic {
		if string(mapping.SessionID) == sessionID && mapping.LegacyThreadID == legacyThreadID {
			return cloneMapping(mapping), true
		}
	}
	return ThreadMapping{}, false
}

// LookupChatGPT resolves the exact ChatGPT conversation source value.
func (plan Plan) LookupChatGPT(conversationID string) (ThreadMapping, bool) {
	if conversationID == "" {
		return ThreadMapping{}, false
	}
	if mapping, ok := plan.chatGPTLookup[conversationID]; ok {
		return cloneMapping(mapping), true
	}
	for _, mapping := range plan.ChatGPT {
		if mapping.ChatGPTConversationID == conversationID {
			return cloneMapping(mapping), true
		}
	}
	return ThreadMapping{}, false
}
