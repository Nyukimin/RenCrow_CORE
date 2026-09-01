package dci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestIdentityEvidenceHappyDeterministicAndBounded(t *testing.T) {
	fixture := newIdentityEvidenceFixture()
	verifier, search, trace, current, archive := fixture.verifier()
	receipt, err := verifier.VerifyAction(context.Background(), fixture.result.Trace.ActionID)
	if err != nil {
		t.Fatalf("VerifyAction() error = %v", err)
	}
	if err := ValidateIdentityEvidence(receipt); err != nil {
		t.Fatalf("ValidateIdentityEvidence() error = %v", err)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("IdentityEvidence.Validate() error = %v", err)
	}
	if receipt.SchemaVersion != "rencrow.dci.identity-evidence/v1" || receipt.Status != "passed" {
		t.Fatalf("unexpected receipt identity: %#v", receipt)
	}
	if receipt.EventCount != 9 || receipt.StepCount != 2 || receipt.EvidenceCount != 2 || receipt.CurrentProjectionCount != 2 || receipt.ArchiveProjectionCount != 2 {
		t.Fatalf("unexpected receipt counts: %#v", receipt)
	}
	if search.calls != 1 || trace.calls != 1 || trace.limit != MaxIdentityEvidenceEvents || current.calls != 2 || archive.calls != 2 {
		t.Fatalf("unexpected reader calls: search=%d trace=%d limit=%d current=%d archive=%d", search.calls, trace.calls, trace.limit, current.calls, archive.calls)
	}
	for index := range current.namespaces {
		if current.namespaces[index] != "kb:dci" || current.eventIDs[index] != string(fixture.result.Pack.Evidence[index].CreatedByEventID) {
			t.Fatalf("current lookup[%d] = (%q, %q)", index, current.namespaces[index], current.eventIDs[index])
		}
		if archive.namespaces[index] != "kb:dci" || archive.eventIDs[index] != string(fixture.result.Pack.Evidence[index].CreatedByEventID) {
			t.Fatalf("archive lookup[%d] = (%q, %q)", index, archive.namespaces[index], archive.eventIDs[index])
		}
	}

	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	for _, key := range []string{
		"schema_version", "status", "action_id", "trace_id", "actor_kind", "actor_id",
		"search_status", "event_count", "step_count", "evidence_count",
		"current_projection_count", "archive_projection_count", "event_graph_sha256",
	} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("receipt JSON key %q is missing: %s", key, encoded)
		}
	}
	for _, key := range []string{"current_count", "archive_count", "graph_sha256", "query", "paths", "snippets", "urls", "payload", "meta"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("receipt JSON must not expose key %q: %s", key, encoded)
		}
	}
	for _, secret := range []string{"identity query secret", "private/path/one.md", "identity snippet one", "https://private.example/source-one", "private metadata secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("receipt JSON leaked %q: %s", secret, encoded)
		}
	}

	reversed := newIdentityEvidenceFixture()
	for left, right := 0, len(reversed.events)-1; left < right; left, right = left+1, right-1 {
		reversed.events[left], reversed.events[right] = reversed.events[right], reversed.events[left]
	}
	reversedReceipt, err := reversed.verifierOnly().VerifyAction(context.Background(), reversed.result.Trace.ActionID)
	if err != nil {
		t.Fatalf("VerifyAction() with reverse reader order error = %v", err)
	}
	if !reflect.DeepEqual(receipt, reversedReceipt) {
		t.Fatalf("event reader order changed receipt:\nfirst=%#v\nreverse=%#v", receipt, reversedReceipt)
	}

	withoutLimitations := newIdentityEvidenceFixture()
	withoutLimitations.result.Pack.Limitations = nil
	withoutLimitations.events[8].Payload["limitations"] = nil
	if _, err := withoutLimitations.verifierOnly().VerifyAction(context.Background(), withoutLimitations.result.Trace.ActionID); err != nil {
		t.Fatalf("VerifyAction() with a nil limitations payload error = %v", err)
	}
}

func TestIdentityEvidenceRejectsMissingAndUnreadyResults(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*identityEvidenceFixture)
	}{
		{
			name: "missing dci",
			mutate: func(f *identityEvidenceFixture) {
				f.searchFound = false
			},
		},
		{
			name: "legacy actor",
			mutate: func(f *identityEvidenceFixture) {
				f.result.Trace.ActorAttribution = domaindci.ActorAttributionLegacyUnattributed
				f.result.Trace.ActorKind = ""
				f.result.Trace.ActorID = ""
				for index := range f.events {
					f.events[index].ActorKind = ""
					f.events[index].ActorID = ""
				}
			},
		},
		{
			name: "failed result",
			mutate: func(f *identityEvidenceFixture) {
				f.result.Trace.Status = "failed"
			},
		},
		{
			name: "wrong mode",
			mutate: func(f *identityEvidenceFixture) {
				f.result.Trace.Mode = "other"
			},
		},
		{
			name: "completed result error",
			mutate: func(f *identityEvidenceFixture) {
				f.result.Trace.ErrorMessage = "unexpected result error"
			},
		},
		{
			name: "zero evidence",
			mutate: func(f *identityEvidenceFixture) {
				f.result.Pack.Evidence = nil
				f.result.Trace.FinalEvidenceCount = 0
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newIdentityEvidenceFixture()
			tt.mutate(&fixture)
			assertIdentityEvidenceRejects(t, fixture)
		})
	}
}

func TestIdentityEvidenceRejectsNilDependenciesAndContext(t *testing.T) {
	fixture := newIdentityEvidenceFixture()
	validAction := fixture.result.Trace.ActionID
	if _, err := fixture.verifierOnly().VerifyAction(nil, validAction); err == nil {
		t.Fatal("VerifyAction(nil context) error = nil")
	}
	if _, err := (*IdentityEvidenceVerifier)(nil).VerifyAction(context.Background(), validAction); err == nil {
		t.Fatal("nil verifier VerifyAction() error = nil")
	}
	if _, err := NewIdentityEvidenceVerifier(nil, nil, nil, nil).VerifyAction(context.Background(), validAction); err == nil {
		t.Fatal("nil readers VerifyAction() error = nil")
	}
	if _, err := fixture.verifierOnly().VerifyAction(context.Background(), modulecore.ActionID("act_not-an-id")); err == nil {
		t.Fatal("invalid action VerifyAction() error = nil")
	}
}

func TestIdentityEvidenceRejectsEventBindingsAndShape(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*identityEvidenceFixture)
	}{
		{
			name: "wrong action",
			mutate: func(f *identityEvidenceFixture) {
				f.events[0].ActionID = identityTestActionID(99)
			},
		},
		{
			name: "wrong trace",
			mutate: func(f *identityEvidenceFixture) {
				f.events[0].TraceID = identityTestTraceID(99)
			},
		},
		{
			name: "wrong actor",
			mutate: func(f *identityEvidenceFixture) {
				f.events[0].ActorID = "user-other"
			},
		},
		{
			name: "wrong component",
			mutate: func(f *identityEvidenceFixture) {
				f.events[0].ComponentID = "other"
			},
		},
		{
			name: "missing event",
			mutate: func(f *identityEvidenceFixture) {
				f.events = f.events[:len(f.events)-1]
			},
		},
		{
			name: "extra event",
			mutate: func(f *identityEvidenceFixture) {
				f.events = append(f.events, identityTestEvent(identityTestEventID(10), "dci.search.completed", f.base.Add(10*time.Second), identityTestEventID(9), nil, nil))
			},
		},
		{
			name: "unknown event",
			mutate: func(f *identityEvidenceFixture) {
				f.events[0].EventType = "dci.unknown"
			},
		},
		{
			name: "duplicate event",
			mutate: func(f *identityEvidenceFixture) {
				f.events[1].EventID = f.events[0].EventID
			},
		},
		{
			name: "overbound events",
			mutate: func(f *identityEvidenceFixture) {
				for index := len(f.events); index < MaxIdentityEvidenceEvents+1; index++ {
					f.events = append(f.events, identityTestEvent(identityTestEventID(1000+index), "dci.search.requested", f.base.Add(time.Duration(index)*time.Second), "", nil, map[string]any{"query": f.result.Pack.Query}))
				}
			},
		},
		{
			name: "bad graph",
			mutate: func(f *identityEvidenceFixture) {
				f.events[1].CausationEventID = identityTestEventID(99)
			},
		},
		{
			name: "bad step event",
			mutate: func(f *identityEvidenceFixture) {
				f.result.Trace.Steps[0].EventID = identityTestEventID(7)
			},
		},
		{
			name: "bad step order",
			mutate: func(f *identityEvidenceFixture) {
				f.result.Trace.Steps[0].StepNo = 2
			},
		},
		{
			name: "bad evidence binding",
			mutate: func(f *identityEvidenceFixture) {
				f.result.Pack.Evidence[0].EvidenceID = identityTestEvidenceID(99)
			},
		},
		{
			name: "bad evidence payload",
			mutate: func(f *identityEvidenceFixture) {
				f.events[4].Payload["snippet"] = "different snippet"
			},
		},
		{
			name: "bad source chain",
			mutate: func(f *identityEvidenceFixture) {
				f.events[5].CausationEventID = identityTestEventID(4)
			},
		},
		{
			name: "selected dependencies",
			mutate: func(f *identityEvidenceFixture) {
				f.events[2].DependencyEventIDs = []modulecore.EventID{identityTestEventID(1)}
			},
		},
		{
			name: "read dependencies",
			mutate: func(f *identityEvidenceFixture) {
				f.events[3].DependencyEventIDs = []modulecore.EventID{identityTestEventID(1)}
			},
		},
		{
			name: "evidence dependencies",
			mutate: func(f *identityEvidenceFixture) {
				f.events[4].DependencyEventIDs = []modulecore.EventID{identityTestEventID(2)}
			},
		},
		{
			name: "bad terminal",
			mutate: func(f *identityEvidenceFixture) {
				f.events[8].Payload["status"] = "failed"
			},
		},
		{
			name: "bad terminal join",
			mutate: func(f *identityEvidenceFixture) {
				f.events[8].DependencyEventIDs = []modulecore.EventID{identityTestEventID(7)}
			},
		},
		{
			name: "extra payload",
			mutate: func(f *identityEvidenceFixture) {
				f.events[0].Payload["secret"] = "must be rejected"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newIdentityEvidenceFixture()
			tt.mutate(&fixture)
			assertIdentityEvidenceRejects(t, fixture)
		})
	}
}

func TestIdentityEvidenceRejectsProjectionFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*identityEvidenceFixture)
	}{
		{
			name: "current missing",
			mutate: func(f *identityEvidenceFixture) {
				delete(f.currentItems, string(f.result.Pack.Evidence[0].CreatedByEventID))
			},
		},
		{
			name: "archive missing",
			mutate: func(f *identityEvidenceFixture) {
				delete(f.archiveItems, string(f.result.Pack.Evidence[0].CreatedByEventID))
			},
		},
		{
			name: "current archive mismatch",
			mutate: func(f *identityEvidenceFixture) {
				item := f.archiveItems[string(f.result.Pack.Evidence[0].CreatedByEventID)]
				item.SummaryDraft = "different projection"
				f.archiveItems[item.EventID] = item
			},
		},
		{
			name: "bad hash",
			mutate: func(f *identityEvidenceFixture) {
				item := f.currentItems[string(f.result.Pack.Evidence[0].CreatedByEventID)]
				item.RawHash = strings.Repeat("a", sha256.Size*2)
				f.currentItems[item.EventID] = item
			},
		},
		{
			name: "bad metadata",
			mutate: func(f *identityEvidenceFixture) {
				item := f.archiveItems[string(f.result.Pack.Evidence[0].CreatedByEventID)]
				item.Meta["trace_id"] = "wrong-trace"
				f.archiveItems[item.EventID] = item
			},
		},
		{
			name: "current reader error",
			mutate: func(f *identityEvidenceFixture) {
				f.currentErrorOnCall = 2
			},
		},
		{
			name: "archive reader error",
			mutate: func(f *identityEvidenceFixture) {
				f.archiveErrorOnCall = 2
			},
		},
		{
			name: "source id mismatch",
			mutate: func(f *identityEvidenceFixture) {
				item := f.currentItems[string(f.result.Pack.Evidence[0].CreatedByEventID)]
				item.SourceID = "different-source"
				f.currentItems[item.EventID] = item
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newIdentityEvidenceFixture()
			tt.mutate(&fixture)
			assertIdentityEvidenceRejects(t, fixture)
		})
	}
}

func TestIdentityEvidenceValidatorRejectsTampering(t *testing.T) {
	fixture := newIdentityEvidenceFixture()
	valid, err := fixture.verifierOnly().VerifyAction(context.Background(), fixture.result.Trace.ActionID)
	if err != nil {
		t.Fatalf("fixture VerifyAction() error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*IdentityEvidence)
	}{
		{name: "schema", mutate: func(e *IdentityEvidence) { e.SchemaVersion = "wrong" }},
		{name: "status", mutate: func(e *IdentityEvidence) { e.Status = "rejected" }},
		{name: "action", mutate: func(e *IdentityEvidence) { e.ActionID = "" }},
		{name: "trace", mutate: func(e *IdentityEvidence) { e.TraceID = "" }},
		{name: "actor kind", mutate: func(e *IdentityEvidence) { e.ActorKind = "legacy" }},
		{name: "actor id", mutate: func(e *IdentityEvidence) { e.ActorID = "" }},
		{name: "search status", mutate: func(e *IdentityEvidence) { e.SearchStatus = "failed" }},
		{name: "event count", mutate: func(e *IdentityEvidence) { e.EventCount = 8 }},
		{name: "step count", mutate: func(e *IdentityEvidence) { e.StepCount = 0 }},
		{name: "evidence count", mutate: func(e *IdentityEvidence) { e.EvidenceCount = 0 }},
		{name: "current count", mutate: func(e *IdentityEvidence) { e.CurrentProjectionCount = 1 }},
		{name: "archive count", mutate: func(e *IdentityEvidence) { e.ArchiveProjectionCount = 1 }},
		{name: "hash uppercase", mutate: func(e *IdentityEvidence) { e.EventGraphSHA256 = strings.ToUpper(e.EventGraphSHA256) }},
		{name: "hash length", mutate: func(e *IdentityEvidence) { e.EventGraphSHA256 = "abc" }},
		{name: "hash nonhex", mutate: func(e *IdentityEvidence) { e.EventGraphSHA256 = strings.Repeat("z", sha256.Size*2) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := valid
			tt.mutate(&candidate)
			if err := ValidateIdentityEvidence(candidate); err == nil {
				t.Fatal("ValidateIdentityEvidence() error = nil")
			}
			if err := candidate.Validate(); err == nil {
				t.Fatal("IdentityEvidence.Validate() error = nil")
			}
		})
	}
}

func TestIdentityEvidenceErrorsDoNotLeakOwnerValues(t *testing.T) {
	secrets := []string{
		"identity query secret",
		"private/path/one.md",
		"identity snippet one",
		"https://private.example/source-one",
		"private metadata secret",
		string(identityTestActionID(1)),
	}
	tests := []struct {
		name   string
		mutate func(*identityEvidenceFixture)
	}{
		{
			name: "dci reader",
			mutate: func(f *identityEvidenceFixture) {
				f.searchError = errors.New(strings.Join(secrets, " "))
			},
		},
		{
			name: "event reader",
			mutate: func(f *identityEvidenceFixture) {
				f.traceError = errors.New(strings.Join(secrets, " "))
			},
		},
		{
			name: "current reader",
			mutate: func(f *identityEvidenceFixture) {
				f.currentErrorOnCall = 1
			},
		},
		{
			name: "archive reader",
			mutate: func(f *identityEvidenceFixture) {
				f.archiveErrorOnCall = 1
			},
		},
		{
			name: "invalid payload",
			mutate: func(f *identityEvidenceFixture) {
				f.events[0].Payload["query"] = "invalid " + secrets[0]
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newIdentityEvidenceFixture()
			tt.mutate(&fixture)
			_, err := fixture.verifierOnly().VerifyAction(context.Background(), fixture.result.Trace.ActionID)
			if err == nil {
				t.Fatal("VerifyAction() error = nil")
			}
			for _, secret := range secrets {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked %q: %v", secret, err)
				}
			}
		})
	}
}

type identityEvidenceFixture struct {
	base               time.Time
	result             domaindci.SearchResult
	events             []modulecore.EventEnvelope
	currentItems       map[string]l1sqlite.L1StagingItem
	archiveItems       map[string]l1sqlite.L1StagingItem
	searchFound        bool
	searchError        error
	traceError         error
	currentErrorOnCall int
	archiveErrorOnCall int
}

func newIdentityEvidenceFixture() identityEvidenceFixture {
	base := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)
	actionID := identityTestActionID(1)
	traceID := identityTestTraceID(1)
	query := "identity query secret"
	evidenceOne := domaindci.Evidence{
		EvidenceID:       identityTestEvidenceID(1),
		CreatedByEventID: identityTestEventID(5),
		SourceID:         "source-one",
		FilePath:         "private/path/one.md",
		LineStart:        3,
		LineEnd:          4,
		Snippet:          "identity snippet one",
		Reason:           "query match",
		Confidence:       0.91,
	}
	evidenceTwo := domaindci.Evidence{
		EvidenceID:       identityTestEvidenceID(2),
		CreatedByEventID: identityTestEventID(8),
		SourceID:         "source-two",
		FilePath:         "private/path/two.md",
		LineStart:        7,
		LineEnd:          8,
		Snippet:          "identity snippet two",
		Reason:           "second query match",
		Confidence:       0.82,
	}
	pack := domaindci.EvidencePack{
		ActionID:    actionID,
		Query:       query,
		Intent:      "identity evidence lookup",
		CorpusScope: []string{"fixture-corpus"},
		Evidence:    []domaindci.Evidence{evidenceOne, evidenceTwo},
		Confidence:  0.87,
		Limitations: []string{"fixture is bounded"},
	}
	trace := domaindci.SearchTrace{
		TraceID:          traceID,
		ActionID:         actionID,
		StartedAt:        base.Add(time.Second),
		EndedAt:          base.Add(9 * time.Second),
		ActorAttribution: domaindci.ActorAttributionAuthenticated,
		ActorKind:        "agent",
		ActorID:          "mio",
		Mode:             "dci",
		UserQuery:        query,
		CorpusScope:      []string{"fixture-corpus"},
		Steps: []domaindci.SearchStep{
			{StepNo: 1, EventID: identityTestEventID(4), EventType: "dci.file.read", Tool: "read_file", FilePath: evidenceOne.FilePath, ResultCount: 1, Status: "ok", CreatedAt: base.Add(4 * time.Second)},
			{StepNo: 2, EventID: identityTestEventID(7), EventType: "dci.file.read", Tool: "read_file", FilePath: evidenceTwo.FilePath, ResultCount: 1, Status: "ok", CreatedAt: base.Add(7 * time.Second)},
		},
		FinalEvidenceCount: 2,
		Status:             "completed",
	}
	result := domaindci.SearchResult{Pack: pack, Trace: trace}
	events := []modulecore.EventEnvelope{
		identityTestEvent(identityTestEventID(1), "dci.search.requested", base.Add(time.Second), "", nil, map[string]any{"query": query}),
		identityTestEvent(identityTestEventID(2), "dci.search.started", base.Add(2*time.Second), identityTestEventID(1), nil, map[string]any{"query": query}),
		identityTestEvent(identityTestEventID(3), "dci.source.selected", base.Add(3*time.Second), identityTestEventID(2), nil, map[string]any{"file_path": evidenceOne.FilePath}),
		identityTestEvent(identityTestEventID(4), "dci.file.read", base.Add(4*time.Second), identityTestEventID(3), nil, map[string]any{"file_path": evidenceOne.FilePath, "status": "ok", "result_count": 1, "error": ""}),
		identityTestEventWithEvidence(identityTestEventID(5), "dci.evidence.created", base.Add(5*time.Second), identityTestEventID(4), evidenceOne.EvidenceID, map[string]any{"file_path": evidenceOne.FilePath, "line_start": 3, "line_end": 4, "snippet": evidenceOne.Snippet, "source_id": evidenceOne.SourceID, "reason": evidenceOne.Reason, "confidence": evidenceOne.Confidence}),
		identityTestEvent(identityTestEventID(6), "dci.source.selected", base.Add(6*time.Second), identityTestEventID(5), nil, map[string]any{"file_path": evidenceTwo.FilePath}),
		identityTestEvent(identityTestEventID(7), "dci.file.read", base.Add(7*time.Second), identityTestEventID(6), nil, map[string]any{"file_path": evidenceTwo.FilePath, "status": "ok", "result_count": 1, "error": ""}),
		identityTestEventWithEvidence(identityTestEventID(8), "dci.evidence.created", base.Add(8*time.Second), identityTestEventID(7), evidenceTwo.EvidenceID, map[string]any{"file_path": evidenceTwo.FilePath, "line_start": 7, "line_end": 8, "snippet": evidenceTwo.Snippet, "source_id": evidenceTwo.SourceID, "reason": evidenceTwo.Reason, "confidence": evidenceTwo.Confidence}),
		identityTestEvent(identityTestEventID(9), "dci.search.completed", base.Add(9*time.Second), identityTestEventID(8), []modulecore.EventID{identityTestEventID(5)}, map[string]any{"status": "completed", "evidence_count": 2, "limitations": []string{"fixture is bounded"}}),
	}
	currentItems := map[string]l1sqlite.L1StagingItem{
		string(evidenceOne.CreatedByEventID): identityTestProjection(evidenceOne, actionID, traceID, base, 1),
		string(evidenceTwo.CreatedByEventID): identityTestProjection(evidenceTwo, actionID, traceID, base, 2),
	}
	archiveItems := cloneIdentityTestProjections(currentItems)
	return identityEvidenceFixture{
		base:         base,
		result:       result,
		events:       events,
		currentItems: currentItems,
		archiveItems: archiveItems,
		searchFound:  true,
	}
}

func (f identityEvidenceFixture) verifier() (*IdentityEvidenceVerifier, *identityEvidenceSearchFake, *identityEvidenceTraceFake, *identityEvidenceL1Fake, *identityEvidenceL1Fake) {
	search := &identityEvidenceSearchFake{result: f.result, found: f.searchFound, err: f.searchError}
	trace := &identityEvidenceTraceFake{events: append([]modulecore.EventEnvelope(nil), f.events...), err: f.traceError}
	current := &identityEvidenceL1Fake{items: cloneIdentityTestProjections(f.currentItems), errOnCall: f.currentErrorOnCall}
	archive := &identityEvidenceL1Fake{items: cloneIdentityTestProjections(f.archiveItems), errOnCall: f.archiveErrorOnCall}
	return NewIdentityEvidenceVerifier(search, trace, current, archive), search, trace, current, archive
}

func (f identityEvidenceFixture) verifierOnly() *IdentityEvidenceVerifier {
	verifier, _, _, _, _ := f.verifier()
	return verifier
}

func assertIdentityEvidenceRejects(t *testing.T, fixture identityEvidenceFixture) {
	t.Helper()
	_, err := fixture.verifierOnly().VerifyAction(context.Background(), fixture.result.Trace.ActionID)
	if err == nil {
		t.Fatal("VerifyAction() error = nil")
	}
}

type identityEvidenceSearchFake struct {
	result   domaindci.SearchResult
	found    bool
	err      error
	calls    int
	actionID modulecore.ActionID
}

func (f *identityEvidenceSearchFake) FindSearchResultByActionID(_ context.Context, actionID modulecore.ActionID) (domaindci.SearchResult, bool, error) {
	f.calls++
	f.actionID = actionID
	if f.err != nil {
		return domaindci.SearchResult{}, false, f.err
	}
	return f.result, f.found, nil
}

type identityEvidenceTraceFake struct {
	events []modulecore.EventEnvelope
	err    error
	calls  int
	trace  modulecore.TraceID
	limit  int
}

func (f *identityEvidenceTraceFake) ListByTraceID(_ context.Context, traceID modulecore.TraceID, limit int) ([]modulecore.EventEnvelope, error) {
	f.calls++
	f.trace = traceID
	f.limit = limit
	if f.err != nil {
		return nil, f.err
	}
	return append([]modulecore.EventEnvelope(nil), f.events...), nil
}

type identityEvidenceL1Fake struct {
	items      map[string]l1sqlite.L1StagingItem
	errOnCall  int
	calls      int
	namespaces []string
	eventIDs   []string
}

func (f *identityEvidenceL1Fake) FindStagingItemByNamespaceEventID(_ context.Context, namespace, eventID string) (l1sqlite.L1StagingItem, bool, error) {
	f.calls++
	f.namespaces = append(f.namespaces, namespace)
	f.eventIDs = append(f.eventIDs, eventID)
	if f.errOnCall > 0 && f.calls == f.errOnCall {
		return l1sqlite.L1StagingItem{}, false, errors.New("owner reader private metadata secret")
	}
	item, found := f.items[eventID]
	return item, found, nil
}

func identityTestActionID(number int) modulecore.ActionID {
	return modulecore.ActionID(fmt.Sprintf("act_00000000-0000-7000-8000-%012x", number))
}

func identityTestTraceID(number int) modulecore.TraceID {
	return modulecore.TraceID(fmt.Sprintf("trc_00000000-0000-7000-8000-%012x", number))
}

func identityTestEventID(number int) modulecore.EventID {
	return modulecore.EventID(fmt.Sprintf("evt_00000000-0000-7000-8000-%012x", number))
}

func identityTestEvidenceID(number int) modulecore.EvidenceID {
	return modulecore.EvidenceID(fmt.Sprintf("evd_00000000-0000-7000-8000-%012x", number))
}

func identityTestEvent(id modulecore.EventID, eventType string, occurredAt time.Time, cause modulecore.EventID, dependencies []modulecore.EventID, payload map[string]any) modulecore.EventEnvelope {
	return modulecore.EventEnvelope{
		SchemaVersion:      modulecore.EventEnvelopeSchemaVersion,
		EventID:            id,
		TraceID:            identityTestTraceID(1),
		CausationEventID:   cause,
		DependencyEventIDs: append([]modulecore.EventID(nil), dependencies...),
		EventType:          eventType,
		ComponentID:        "dci",
		OccurredAt:         occurredAt,
		ActionID:           identityTestActionID(1),
		ActorKind:          "agent",
		ActorID:            "mio",
		Payload:            payload,
	}
}

func identityTestEventWithEvidence(id modulecore.EventID, eventType string, occurredAt time.Time, cause modulecore.EventID, evidenceID modulecore.EvidenceID, payload map[string]any) modulecore.EventEnvelope {
	event := identityTestEvent(id, eventType, occurredAt, cause, nil, payload)
	event.EvidenceID = evidenceID
	return event
}

func identityTestProjection(evidence domaindci.Evidence, actionID modulecore.ActionID, traceID modulecore.TraceID, base time.Time, number int) l1sqlite.L1StagingItem {
	hash := sha256.Sum256([]byte(evidence.Snippet))
	return l1sqlite.L1StagingItem{
		ID:               fmt.Sprintf("projection-%d", number),
		Kind:             l1sqlite.L1StagingKindSearchResult,
		Namespace:        "kb:dci",
		EventID:          string(evidence.CreatedByEventID),
		SourceID:         dciSourceID(evidence.FilePath),
		SourceURL:        dciSyntheticSourceURL(evidence.FilePath),
		FetchedAt:        base.Add(time.Duration(10+number) * time.Second),
		PublishedAt:      base.Add(time.Duration(20+number) * time.Second),
		RawText:          evidence.Snippet,
		RawHash:          hex.EncodeToString(hash[:]),
		SummaryDraft:     "summary",
		Keywords:         []string{"identity", "evidence"},
		LicenseNote:      "private license",
		ValidationStatus: l1sqlite.L1StagingStatusPending,
		Meta: map[string]interface{}{
			"source_kind":               "dci",
			"search_action_id":          string(actionID),
			"trace_id":                  string(traceID),
			"evidence_id":               string(evidence.EvidenceID),
			"evidence_created_event_id": string(evidence.CreatedByEventID),
			"private_meta":              "private metadata secret",
		},
		CreatedAt: base.Add(time.Duration(30+number) * time.Second),
		UpdatedAt: base.Add(time.Duration(40+number) * time.Second),
	}
}

func cloneIdentityTestProjections(items map[string]l1sqlite.L1StagingItem) map[string]l1sqlite.L1StagingItem {
	cloned := make(map[string]l1sqlite.L1StagingItem, len(items))
	for key, item := range items {
		item.Keywords = append([]string(nil), item.Keywords...)
		if item.Meta != nil {
			originalMeta := item.Meta
			item.Meta = make(map[string]interface{}, len(item.Meta))
			for metaKey, value := range originalMeta {
				item.Meta[metaKey] = value
			}
		}
		cloned[key] = item
	}
	return cloned
}
