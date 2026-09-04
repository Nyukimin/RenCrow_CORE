package threadmigration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	"github.com/google/uuid"
)

func genericFact(surface, recordKey, sessionID string, threadID int64, kind string) LegacyThreadFact {
	return LegacyThreadFact{
		Surface:        surface,
		RecordKey:      recordKey,
		SessionID:      sessionID,
		LegacyThreadID: threadID,
		KindHint:       kind,
	}
}

func canonicalSessionID(t *testing.T, seed string) string {
	t.Helper()
	raw, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "threadmigration_test", "session_id", seed)
	if err != nil {
		t.Fatalf("canonical test session ID: %v", err)
	}
	return raw
}

func chatGPTFact(surface, recordKey, conversationID string) LegacyThreadFact {
	return LegacyThreadFact{
		Surface:               surface,
		RecordKey:             recordKey,
		ChatGPTConversationID: conversationID,
	}
}

func TestBuildPlanMapsGenericThreadOnceAcrossSurfacesAndRetainsSequence(t *testing.T) {
	sessionID := canonicalSessionID(t, "session-1")
	facts := []LegacyThreadFact{
		genericFact("archive", "archive:42", sessionID, 42, ""),
		genericFact("l1", "l1:42", sessionID, 42, string(modulecore.ThreadKindUserConversation)),
	}

	plan, err := BuildPlan(facts)
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if len(plan.Generic) != 1 || len(plan.ChatGPT) != 0 {
		t.Fatalf("plan mappings = generic %d, ChatGPT %d; want 1, 0", len(plan.Generic), len(plan.ChatGPT))
	}
	mapping := plan.Generic[0]
	if mapping.SessionID != modulecore.SessionID(sessionID) || mapping.ThreadSeq != modulecore.ThreadSeq(42) || mapping.ThreadKind != modulecore.ThreadKindUserConversation {
		t.Fatalf("generic mapping = %+v", mapping)
	}
	if mapping.SemanticKey != GenericSemanticKey(sessionID, 42) {
		t.Fatalf("semantic key = %q, want %q", mapping.SemanticKey, GenericSemanticKey(sessionID, 42))
	}
	wantThreadID, err := modulecore.NewMigrationID(modulecore.CanonicalThreadID, "conversation_thread", "session_id+legacy_thread_id", GenericSemanticKey(sessionID, 42))
	if err != nil {
		t.Fatal(err)
	}
	if mapping.ThreadID != modulecore.ThreadID(wantThreadID) {
		t.Fatalf("thread ID = %q, want %q", mapping.ThreadID, wantThreadID)
	}
	for _, source := range []struct {
		surface   string
		recordKey string
	}{
		{surface: "archive", recordKey: "archive:42"},
		{surface: "l1", recordKey: "l1:42"},
	} {
		got, ok := plan.LookupBySource(source.surface, source.recordKey)
		if !ok || got.ThreadID != mapping.ThreadID {
			t.Fatalf("LookupBySource(%q, %q) = %+v, %v; want %q, true", source.surface, source.recordKey, got, ok, mapping.ThreadID)
		}
	}
	got, ok := plan.LookupGeneric(sessionID, 42)
	if !ok || got.ThreadID != mapping.ThreadID {
		t.Fatalf("LookupGeneric() = %+v, %v", got, ok)
	}
}

func TestBuildPlanSeparatesReusedLegacyNumberBySession(t *testing.T) {
	sessionA := canonicalSessionID(t, "session-a")
	sessionB := canonicalSessionID(t, "session-b")
	plan, err := BuildPlan([]LegacyThreadFact{
		genericFact("surface", "record-a", sessionA, 7, ""),
		genericFact("surface", "record-b", sessionB, 7, ""),
	})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if len(plan.Generic) != 2 || plan.Generic[0].ThreadID == plan.Generic[1].ThreadID {
		t.Fatalf("generic mappings = %+v; reused numeric ID must be session-scoped", plan.Generic)
	}
}

func TestBuildPlanSeparatesSameRecordKeyBySurfaceAndQualifiesLookup(t *testing.T) {
	sessionA := canonicalSessionID(t, "same-record-session-a")
	sessionB := canonicalSessionID(t, "same-record-session-b")
	plan, err := BuildPlan([]LegacyThreadFact{
		genericFact("archive", "shared-record", sessionA, 7, ""),
		genericFact("l1", "shared-record", sessionB, 7, ""),
	})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if len(plan.Generic) != 2 || plan.Generic[0].ThreadID == plan.Generic[1].ThreadID {
		t.Fatalf("generic mappings = %+v; same record key on separate surfaces must remain distinct", plan.Generic)
	}
	archiveMapping, ok := plan.LookupBySource("archive", "shared-record")
	if !ok || archiveMapping.SessionID != modulecore.SessionID(sessionA) {
		t.Fatalf("archive source lookup = %+v, %v", archiveMapping, ok)
	}
	l1Mapping, ok := plan.LookupBySource("l1", "shared-record")
	if !ok || l1Mapping.SessionID != modulecore.SessionID(sessionB) {
		t.Fatalf("l1 source lookup = %+v, %v", l1Mapping, ok)
	}
	if archiveMapping.ThreadID == l1Mapping.ThreadID {
		t.Fatal("surface-qualified lookups unexpectedly collapsed distinct identities")
	}
	if _, ok := plan.LookupBySource("other", "shared-record"); ok {
		t.Fatal("lookup with an unknown source surface unexpectedly succeeded")
	}
}

func TestBuildPlanCanonicalizesLegacySessionAndConvergesWithCanonicalReference(t *testing.T) {
	const rawSessionID = "legacy-session"
	canonicalSessionID, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "session_files", "id", rawSessionID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan([]LegacyThreadFact{
		genericFact("archive", "legacy-record", rawSessionID, 12, ""),
		genericFact("l1", "canonical-record", canonicalSessionID, 12, ""),
	})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if len(plan.Generic) != 1 {
		t.Fatalf("generic mappings = %d, want one converged mapping", len(plan.Generic))
	}
	mapping := plan.Generic[0]
	if mapping.SessionID != modulecore.SessionID(canonicalSessionID) || mapping.ThreadSeq != 12 {
		t.Fatalf("converged mapping = %+v", mapping)
	}
	if len(mapping.Sources) != 2 {
		t.Fatalf("source session audit = %+v", mapping.Sources)
	}
	sourceSessions := map[string]bool{}
	for _, source := range mapping.Sources {
		sourceSessions[source.SourceSessionID] = true
	}
	if !sourceSessions[rawSessionID] || !sourceSessions[canonicalSessionID] {
		t.Fatalf("source session audit = %+v", mapping.Sources)
	}
	for _, source := range []struct {
		surface   string
		recordKey string
	}{
		{surface: "archive", recordKey: "legacy-record"},
		{surface: "l1", recordKey: "canonical-record"},
	} {
		got, ok := plan.LookupBySource(source.surface, source.recordKey)
		if !ok || got.ThreadID != mapping.ThreadID || got.SessionID != mapping.SessionID {
			t.Fatalf("lookup %q/%q = %+v, %v; want converged mapping", source.surface, source.recordKey, got, ok)
		}
	}
}

func TestBuildPlanUsesExactLegacySessionValue(t *testing.T) {
	const (
		paddedSessionID   = " legacy-session "
		unpaddedSessionID = "legacy-session"
	)

	paddedPlan, err := BuildPlan([]LegacyThreadFact{
		genericFact("surface", "same-record", paddedSessionID, 17, ""),
	})
	if err != nil {
		t.Fatalf("padded BuildPlan() error = %v", err)
	}
	unpaddedPlan, err := BuildPlan([]LegacyThreadFact{
		genericFact("surface", "same-record", unpaddedSessionID, 17, ""),
	})
	if err != nil {
		t.Fatalf("unpadded BuildPlan() error = %v", err)
	}

	wantPaddedSessionID, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "session_files", "id", paddedSessionID)
	if err != nil {
		t.Fatal(err)
	}
	wantPaddedSemanticKey := GenericSemanticKey(wantPaddedSessionID, 17)
	wantPaddedThreadID, err := modulecore.NewMigrationID(modulecore.CanonicalThreadID, "conversation_thread", "session_id+legacy_thread_id", wantPaddedSemanticKey)
	if err != nil {
		t.Fatal(err)
	}
	paddedMapping, ok := paddedPlan.LookupBySource("surface", "same-record")
	if !ok {
		t.Fatal("padded mapping lookup failed")
	}
	if paddedMapping.SessionID != modulecore.SessionID(wantPaddedSessionID) || paddedMapping.ThreadID != modulecore.ThreadID(wantPaddedThreadID) || len(paddedMapping.Sources) != 1 || paddedMapping.Sources[0].SourceSessionID != paddedSessionID {
		t.Fatalf("padded mapping = %+v, want exact legacy source session %q", paddedMapping, paddedSessionID)
	}
	if got, ok := paddedPlan.LookupGeneric(string(wantPaddedSessionID), 17); !ok || got.ThreadID != paddedMapping.ThreadID {
		t.Fatalf("padded generic semantic lookup = %+v, %v", got, ok)
	}

	wantUnpaddedSessionID, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "session_files", "id", unpaddedSessionID)
	if err != nil {
		t.Fatal(err)
	}
	unpaddedMapping := unpaddedPlan.Generic[0]
	if unpaddedMapping.SessionID != modulecore.SessionID(wantUnpaddedSessionID) {
		t.Fatalf("unpadded session ID = %q, want %q", unpaddedMapping.SessionID, wantUnpaddedSessionID)
	}
	if paddedMapping.SessionID == unpaddedMapping.SessionID || paddedMapping.ThreadID == unpaddedMapping.ThreadID || paddedPlan.MappingSHA256 == unpaddedPlan.MappingSHA256 {
		t.Fatal("padded and unpadded legacy session values must not collapse")
	}
}

func TestBuildPlanTreatsPaddedCanonicalSessionAsExactLegacySource(t *testing.T) {
	canonicalSession := canonicalSessionID(t, "padded-canonical")
	paddedSession := " " + canonicalSession + " "
	plan, err := BuildPlan([]LegacyThreadFact{
		genericFact("surface", "padded-canonical-record", paddedSession, 4, ""),
	})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	wantSession, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "session_files", "id", paddedSession)
	if err != nil {
		t.Fatal(err)
	}
	mapping := plan.Generic[0]
	if mapping.SessionID != modulecore.SessionID(wantSession) || mapping.SessionID == modulecore.SessionID(canonicalSession) {
		t.Fatalf("padded canonical session mapping = %+v, want exact session_files recipe", mapping)
	}
	if len(mapping.Sources) != 1 || mapping.Sources[0].SourceSessionID != paddedSession {
		t.Fatalf("padded canonical source audit = %+v, want %q", mapping.Sources, paddedSession)
	}
}

func TestBuildPlanAcceptsEquivalentCanonicalAndLegacyIdentityForOneRecord(t *testing.T) {
	const rawSessionID = "legacy-record-session"
	canonicalSessionID, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "session_files", "id", rawSessionID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan([]LegacyThreadFact{
		genericFact("archive", "same-record", rawSessionID, 5, ""),
		genericFact("l1", "same-record", canonicalSessionID, 5, string(modulecore.ThreadKindUserConversation)),
	})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if len(plan.Generic) != 1 || len(plan.Generic[0].Sources) != 2 {
		t.Fatalf("equivalent identity sources = %+v", plan.Generic)
	}
}

func TestBuildPlanMapsChatGPTConversationWithCanonicalIDs(t *testing.T) {
	const conversationID = "conversation-identity"
	plan, err := BuildPlan([]LegacyThreadFact{
		chatGPTFact("chatgpt", "chatgpt:message-1", conversationID),
		chatGPTFact("l1", "chatgpt:message-2", conversationID),
	})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if len(plan.Generic) != 0 || len(plan.ChatGPT) != 1 {
		t.Fatalf("plan mappings = generic %d, ChatGPT %d; want 0, 1", len(plan.Generic), len(plan.ChatGPT))
	}
	mapping := plan.ChatGPT[0]
	wantSessionID, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "l1_raw_record", "session_id", conversationID)
	if err != nil {
		t.Fatal(err)
	}
	wantThreadID, err := modulecore.NewMigrationID(modulecore.CanonicalThreadID, "l1_raw_record", "thread_id", conversationID)
	if err != nil {
		t.Fatal(err)
	}
	if mapping.SessionID != modulecore.SessionID(wantSessionID) || mapping.ThreadID != modulecore.ThreadID(wantThreadID) || mapping.ThreadSeq != 1 || mapping.ThreadKind != modulecore.ThreadKindUserConversation {
		t.Fatalf("ChatGPT mapping = %+v, want session=%q thread=%q seq=1 kind=%q", mapping, wantSessionID, wantThreadID, modulecore.ThreadKindUserConversation)
	}
	if err := mapping.SessionID.Validate(); err != nil {
		t.Fatalf("ChatGPT session ID is invalid: %v", err)
	}
	if err := mapping.ThreadID.Validate(); err != nil {
		t.Fatalf("ChatGPT thread ID is invalid: %v", err)
	}
	if mapping.SemanticKey != ChatGPTSemanticKey(conversationID) {
		t.Fatalf("ChatGPT semantic key = %q, want %q", mapping.SemanticKey, ChatGPTSemanticKey(conversationID))
	}
}

func TestBuildPlanUsesExactChatGPTConversationValue(t *testing.T) {
	const (
		paddedConversationID   = " conversation-identity "
		unpaddedConversationID = "conversation-identity"
	)
	paddedPlan, err := BuildPlan([]LegacyThreadFact{
		chatGPTFact("chatgpt", "same-record", paddedConversationID),
	})
	if err != nil {
		t.Fatalf("padded ChatGPT BuildPlan() error = %v", err)
	}
	unpaddedPlan, err := BuildPlan([]LegacyThreadFact{
		chatGPTFact("chatgpt", "same-record", unpaddedConversationID),
	})
	if err != nil {
		t.Fatalf("unpadded ChatGPT BuildPlan() error = %v", err)
	}

	wantPaddedSessionID, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "l1_raw_record", "session_id", paddedConversationID)
	if err != nil {
		t.Fatal(err)
	}
	wantPaddedThreadID, err := modulecore.NewMigrationID(modulecore.CanonicalThreadID, "l1_raw_record", "thread_id", paddedConversationID)
	if err != nil {
		t.Fatal(err)
	}
	paddedMapping := paddedPlan.ChatGPT[0]
	if paddedMapping.ChatGPTConversationID != paddedConversationID || paddedMapping.SemanticKey != paddedConversationID || paddedMapping.SessionID != modulecore.SessionID(wantPaddedSessionID) || paddedMapping.ThreadID != modulecore.ThreadID(wantPaddedThreadID) {
		t.Fatalf("padded ChatGPT mapping = %+v, want exact conversation bytes", paddedMapping)
	}
	if got, ok := paddedPlan.LookupChatGPT(paddedConversationID); !ok || got.ThreadID != paddedMapping.ThreadID {
		t.Fatalf("padded ChatGPT semantic lookup = %+v, %v", got, ok)
	}
	if _, ok := paddedPlan.LookupChatGPT(strings.TrimSpace(paddedConversationID)); ok {
		t.Fatal("padded ChatGPT lookup unexpectedly normalized its semantic input")
	}
	unpaddedMapping := unpaddedPlan.ChatGPT[0]
	if paddedMapping.SessionID == unpaddedMapping.SessionID || paddedMapping.ThreadID == unpaddedMapping.ThreadID || paddedPlan.MappingSHA256 == unpaddedPlan.MappingSHA256 {
		t.Fatal("padded and unpadded ChatGPT conversation values must not collapse")
	}
}

func TestBuildPlanIsStableUnderInputPermutationAndExposesCanonicalHash(t *testing.T) {
	sessionA := canonicalSessionID(t, "session-a")
	sessionZ := canonicalSessionID(t, "session-z")
	facts := []LegacyThreadFact{
		genericFact("z-surface", "z-record", sessionZ, 9, "system"),
		chatGPTFact("chatgpt", "chat-record", "conversation-z"),
		genericFact("a-surface", "a-record", sessionA, 2, "agent_discussion"),
		genericFact("a-surface", "a-record-duplicate", sessionA, 2, "agent_discussion"),
	}
	permuted := []LegacyThreadFact{facts[3], facts[1], facts[0], facts[2]}

	first, err := BuildPlan(facts)
	if err != nil {
		t.Fatalf("first BuildPlan() error = %v", err)
	}
	second, err := BuildPlan(permuted)
	if err != nil {
		t.Fatalf("second BuildPlan() error = %v", err)
	}
	if !reflect.DeepEqual(first.Generic, second.Generic) || !reflect.DeepEqual(first.ChatGPT, second.ChatGPT) || first.MappingSHA256 != second.MappingSHA256 {
		t.Fatalf("permutation changed plan: first=%+v second=%+v", first, second)
	}
	firstJSON, err := first.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := second.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("permutation changed canonical JSON:\nfirst=%s\nsecond=%s", firstJSON, secondJSON)
	}
	wantHash := sha256Hex(firstJSON)
	if first.MappingSHA256 != wantHash {
		t.Fatalf("mapping SHA256 = %q, want %q", first.MappingSHA256, wantHash)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("plan Validate() = %v", err)
	}
	withoutHash := first
	withoutHash.MappingSHA256 = ""
	if err := withoutHash.Validate(); err == nil {
		t.Fatal("plan without mapping SHA256 must be rejected")
	}
	tamperedHash := first
	tamperedHash.MappingSHA256 = strings.Repeat("0", sha256.Size*2)
	if err := tamperedHash.Validate(); err == nil {
		t.Fatal("plan with a tampered mapping SHA256 must be rejected")
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(firstJSON, &decoded); err != nil {
		t.Fatalf("canonical JSON is invalid: %v", err)
	}
	if _, ok := decoded["mapping_sha256"]; ok {
		t.Fatal("canonical mapping JSON must not hash the hash field itself")
	}
}

func TestTypedSemanticLookupsResolveGenericAndChatGPTRawCollision(t *testing.T) {
	sessionID := canonicalSessionID(t, "typed-lookup-session")
	conversationID := GenericSemanticKey(sessionID, 19)
	plan, err := BuildPlan([]LegacyThreadFact{
		genericFact("generic", "generic-record", sessionID, 19, ""),
		chatGPTFact("chatgpt", "chat-record", conversationID),
	})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	generic, ok := plan.LookupGeneric(sessionID, 19)
	if !ok || generic.ChatGPTConversationID != "" || generic.ThreadSeq != 19 {
		t.Fatalf("generic typed lookup = %+v, %v", generic, ok)
	}
	chatGPT, ok := plan.LookupChatGPT(conversationID)
	if !ok || chatGPT.ChatGPTConversationID != conversationID || chatGPT.ThreadSeq != 1 {
		t.Fatalf("ChatGPT typed lookup = %+v, %v", chatGPT, ok)
	}
	if generic.ThreadID == chatGPT.ThreadID {
		t.Fatal("typed semantic collision unexpectedly shared a canonical thread ID")
	}
}

func TestPlanValidateRejectsContradictoryDuplicateSourceReference(t *testing.T) {
	sessionA := canonicalSessionID(t, "tampered-source-a")
	sessionB := canonicalSessionID(t, "tampered-source-b")
	plan, err := BuildPlan([]LegacyThreadFact{
		genericFact("surface", "record-a", sessionA, 1, ""),
		genericFact("surface", "record-b", sessionB, 2, ""),
	})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	tampered := plan
	tampered.Generic = append([]ThreadMapping(nil), plan.Generic...)
	tampered.Generic[1] = plan.Generic[1]
	tampered.Generic[1].Sources = append([]ThreadSource(nil), plan.Generic[1].Sources...)
	tampered.Generic[1].Surface = plan.Generic[0].Surface
	tampered.Generic[1].RecordKey = plan.Generic[0].RecordKey
	tampered.Generic[1].Sources[0].Surface = plan.Generic[0].Sources[0].Surface
	tampered.Generic[1].Sources[0].RecordKey = plan.Generic[0].Sources[0].RecordKey
	if err := tampered.Validate(); err == nil {
		t.Fatal("Validate() accepted one source reference mapped to contradictory identities")
	}
}

func TestBuildPlanRejectsInvalidFactsAndIdentityConflicts(t *testing.T) {
	conversationID := "conversation-conflict"
	sessionA := canonicalSessionID(t, "session-a")
	sessionB := canonicalSessionID(t, "session-b")
	legacySessionA := "legacy-session-a"
	legacySessionB := "legacy-session-b"
	chatSession, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "l1_raw_record", "session_id", conversationID)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		facts []LegacyThreadFact
	}{
		{name: "empty session", facts: []LegacyThreadFact{genericFact("surface", "record", " ", 1, "")}},
		{name: "nonpositive legacy ID", facts: []LegacyThreadFact{genericFact("surface", "record", sessionA, 0, "")}},
		{name: "negative legacy ID", facts: []LegacyThreadFact{genericFact("surface", "record", sessionA, -1, "")}},
		{name: "invalid kind", facts: []LegacyThreadFact{genericFact("surface", "record", sessionA, 1, "not-a-thread-kind")}},
		{name: "record key contradictory generic identity", facts: []LegacyThreadFact{
			genericFact("surface", "same-record", sessionA, 1, ""),
			genericFact("surface", "same-record", sessionB, 1, ""),
		}},
		{name: "record key contradictory legacy session identity", facts: []LegacyThreadFact{
			genericFact("surface", "same-legacy-record", legacySessionA, 1, ""),
			genericFact("surface", "same-legacy-record", legacySessionB, 1, ""),
		}},
		{name: "record key mixes generic and ChatGPT", facts: []LegacyThreadFact{
			genericFact("surface", "same-record", sessionA, 1, ""),
			chatGPTFact("surface", "same-record", conversationID),
		}},
		{name: "ChatGPT mixed generic fields", facts: []LegacyThreadFact{{
			RecordKey:             "record",
			SessionID:             sessionA,
			LegacyThreadID:        1,
			ChatGPTConversationID: conversationID,
		}}},
		{name: "same tuple maps to different identity", facts: []LegacyThreadFact{
			genericFact("surface", "generic-record", string(chatSession), 1, ""),
			chatGPTFact("surface", "chat-record", conversationID),
		}},
		{name: "empty ChatGPT conversation", facts: []LegacyThreadFact{chatGPTFact("surface", "record", " ")}},
		{name: "empty surface", facts: []LegacyThreadFact{{RecordKey: "record", SessionID: sessionA, LegacyThreadID: 1}}},
		{name: "empty record key", facts: []LegacyThreadFact{{Surface: "surface", SessionID: sessionA, LegacyThreadID: 1}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := BuildPlan(tc.facts); err == nil {
				t.Fatal("BuildPlan() accepted invalid facts")
			}
		})
	}
}

func TestPlanValidatesUUIDAndEnumContracts(t *testing.T) {
	sessionID := canonicalSessionID(t, "session")
	plan, err := BuildPlan([]LegacyThreadFact{
		genericFact("surface", "generic", sessionID, 3, "document"),
		chatGPTFact("surface", "chat", "conversation"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, mapping := range append(plan.Generic, plan.ChatGPT...) {
		if err := mapping.ThreadID.Validate(); err != nil {
			t.Fatalf("ThreadID %q validation failed: %v", mapping.ThreadID, err)
		}
		parsed, err := uuid.Parse(strings.TrimPrefix(string(mapping.ThreadID), "thr_"))
		if err != nil || parsed.Version() != 5 {
			t.Fatalf("ThreadID %q must contain UUIDv5: version=%v err=%v", mapping.ThreadID, parsed.Version(), err)
		}
		if err := mapping.ThreadSeq.Validate(); err != nil {
			t.Fatalf("ThreadSeq %d validation failed: %v", mapping.ThreadSeq, err)
		}
		if err := mapping.ThreadKind.Validate(); err != nil {
			t.Fatalf("ThreadKind %q validation failed: %v", mapping.ThreadKind, err)
		}
	}
	if _, err := hex.DecodeString(plan.MappingSHA256); err != nil || len(plan.MappingSHA256) != 64 {
		t.Fatalf("invalid plan hash %q: %v", plan.MappingSHA256, err)
	}
}

func TestMergePlansRetainsDisjointMappingsAndCanonicalLookups(t *testing.T) {
	sessionA := canonicalSessionID(t, "merge-disjoint-a")
	sessionB := canonicalSessionID(t, "merge-disjoint-b")
	first, err := BuildPlan([]LegacyThreadFact{genericFact("sqlite", "thread-a", sessionA, 3, "")})
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildPlan([]LegacyThreadFact{genericFact("topic", "thread-b", sessionB, 4, "document")})
	if err != nil {
		t.Fatal(err)
	}
	merged, err := MergePlans(first, second)
	if err != nil {
		t.Fatalf("MergePlans() error = %v", err)
	}
	if len(merged.Generic) != 2 || len(merged.ChatGPT) != 0 {
		t.Fatalf("merged mappings = generic %d ChatGPT %d; want 2, 0", len(merged.Generic), len(merged.ChatGPT))
	}
	for _, source := range []struct {
		surface, record string
		session         string
		seq             modulecore.ThreadSeq
	}{
		{surface: "sqlite", record: "thread-a", session: sessionA, seq: 3},
		{surface: "topic", record: "thread-b", session: sessionB, seq: 4},
	} {
		mapping, ok := merged.LookupBySource(source.surface, source.record)
		if !ok || mapping.SessionID != modulecore.SessionID(source.session) || mapping.ThreadSeq != source.seq {
			t.Fatalf("merged source lookup %s/%s = %+v, %v", source.surface, source.record, mapping, ok)
		}
		semantic, ok := merged.LookupGeneric(source.session, int64(source.seq))
		if !ok || semantic.ThreadID != mapping.ThreadID {
			t.Fatalf("merged semantic lookup %q/%d = %+v, %v", source.session, source.seq, semantic, ok)
		}
	}
	if err := merged.Validate(); err != nil {
		t.Fatalf("merged Validate() error = %v", err)
	}
}

func TestMergePlansMergesExactDuplicateAcrossSurfacesAndDeduplicatesSources(t *testing.T) {
	sessionID := canonicalSessionID(t, "merge-duplicate-generic")
	first, err := BuildPlan([]LegacyThreadFact{genericFact("sqlite", "same-record", sessionID, 7, "")})
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildPlan([]LegacyThreadFact{genericFact("topic", "same-record", sessionID, 7, "user_conversation")})
	if err != nil {
		t.Fatal(err)
	}
	third, err := BuildPlan([]LegacyThreadFact{genericFact("sqlite", "same-record", sessionID, 7, "")})
	if err != nil {
		t.Fatal(err)
	}
	merged, err := MergePlans(first, second, third)
	if err != nil {
		t.Fatalf("MergePlans() error = %v", err)
	}
	if len(merged.Generic) != 1 {
		t.Fatalf("merged generic mappings = %+v", merged.Generic)
	}
	mapping := merged.Generic[0]
	if mapping.Surface != "sqlite" || mapping.RecordKey != "same-record" || len(mapping.Sources) != 2 {
		t.Fatalf("merged primary/source set = %+v", mapping)
	}
	if mapping.Sources[0].Surface != "sqlite" || mapping.Sources[1].Surface != "topic" {
		t.Fatalf("merged source ordering = %+v", mapping.Sources)
	}
	if got, ok := merged.LookupBySource("topic", "same-record"); !ok || got.ThreadID != mapping.ThreadID {
		t.Fatalf("merged topic lookup = %+v, %v", got, ok)
	}
}

func TestMergePlansRejectsContradictorySemanticTupleKindAndResultThreadID(t *testing.T) {
	sessionID := canonicalSessionID(t, "merge-conflict")
	userPlan, err := BuildPlan([]LegacyThreadFact{genericFact("sqlite", "user", sessionID, 9, "user_conversation")})
	if err != nil {
		t.Fatal(err)
	}
	discussionPlan, err := BuildPlan([]LegacyThreadFact{genericFact("topic", "discussion", sessionID, 9, "agent_discussion")})
	if err != nil {
		t.Fatal(err)
	}
	if merged, err := MergePlans(userPlan, discussionPlan); err == nil || merged.MappingSHA256 != "" {
		t.Fatalf("same tuple/different kind accepted: merged=%+v err=%v", merged, err)
	}

	otherSession := canonicalSessionID(t, "merge-thread-id-conflict")
	otherPlan, err := BuildPlan([]LegacyThreadFact{genericFact("other", "other", otherSession, 10, "")})
	if err != nil {
		t.Fatal(err)
	}
	tampered := otherPlan
	tampered.Generic = append([]ThreadMapping(nil), otherPlan.Generic...)
	tampered.Generic[0].ThreadID = userPlan.Generic[0].ThreadID
	if merged, err := MergePlans(userPlan, tampered); err == nil || merged.MappingSHA256 != "" {
		t.Fatalf("duplicate resulting ThreadID accepted: merged=%+v err=%v", merged, err)
	}
}

func TestMergePlansMergesExactDuplicateChatGPTMappings(t *testing.T) {
	const conversationID = "merge-chatgpt-duplicate"
	first, err := BuildPlan([]LegacyThreadFact{chatGPTFact("sqlite", "chat-record", conversationID)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildPlan([]LegacyThreadFact{chatGPTFact("raw", "chat-record", conversationID)})
	if err != nil {
		t.Fatal(err)
	}
	merged, err := MergePlans(first, second)
	if err != nil {
		t.Fatalf("MergePlans() error = %v", err)
	}
	if len(merged.Generic) != 0 || len(merged.ChatGPT) != 1 || len(merged.ChatGPT[0].Sources) != 2 {
		t.Fatalf("merged ChatGPT mappings = %+v", merged.ChatGPT)
	}
	mapping := merged.ChatGPT[0]
	if mapping.Surface != "raw" || mapping.RecordKey != "chat-record" {
		t.Fatalf("ChatGPT primary source = %s/%s", mapping.Surface, mapping.RecordKey)
	}
	if got, ok := merged.LookupChatGPT(conversationID); !ok || got.ThreadID != mapping.ThreadID || got.SessionID != mapping.SessionID || got.ThreadSeq != 1 {
		t.Fatalf("merged ChatGPT lookup = %+v, %v", got, ok)
	}
}

func TestMergePlansIsStableUnderInputPermutation(t *testing.T) {
	sessionA := canonicalSessionID(t, "merge-permutation-a")
	sessionB := canonicalSessionID(t, "merge-permutation-b")
	plans := make([]Plan, 0, 3)
	for _, fact := range []LegacyThreadFact{
		genericFact("z-surface", "z-record", sessionB, 8, "system"),
		chatGPTFact("chatgpt", "chat-record", "merge-permutation-chat"),
		genericFact("a-surface", "a-record", sessionA, 2, "agent_discussion"),
	} {
		plan, err := BuildPlan([]LegacyThreadFact{fact})
		if err != nil {
			t.Fatal(err)
		}
		plans = append(plans, plan)
	}
	first, err := MergePlans(plans...)
	if err != nil {
		t.Fatal(err)
	}
	second, err := MergePlans(plans[2], plans[0], plans[1])
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := first.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := second.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) || first.MappingSHA256 != second.MappingSHA256 {
		t.Fatalf("plan order changed merged output/hash:\nfirst=%s\nsecond=%s", firstJSON, secondJSON)
	}
	if !reflect.DeepEqual(first.Generic, second.Generic) || !reflect.DeepEqual(first.ChatGPT, second.ChatGPT) {
		t.Fatalf("plan order changed mappings: first=%+v second=%+v", first, second)
	}
}

func TestMergePlansRejectsInvalidOrTamperedInput(t *testing.T) {
	sessionID := canonicalSessionID(t, "merge-tampered")
	valid, err := BuildPlan([]LegacyThreadFact{genericFact("surface", "record", sessionID, 1, "")})
	if err != nil {
		t.Fatal(err)
	}
	tampered := valid
	tampered.MappingSHA256 = strings.Repeat("0", 64)
	if merged, err := MergePlans(valid, tampered); err == nil || merged.MappingSHA256 != "" {
		t.Fatalf("tampered input accepted: merged=%+v err=%v", merged, err)
	}
	if merged, err := MergePlans(); err == nil || merged.MappingSHA256 != "" {
		t.Fatalf("empty plan list accepted: merged=%+v err=%v", merged, err)
	}
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
