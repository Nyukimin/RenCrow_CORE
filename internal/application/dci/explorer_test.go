package dci

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	skillbootstrap "github.com/Nyukimin/RenCrow_CORE/internal/application/skillgovernance"
	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type memoryTraceStore struct {
	traces   []domaindci.SearchTrace
	contexts []eventAppendContext
}

type recordingEventAppender struct {
	events   []modulecore.EventEnvelope
	contexts []eventAppendContext
	failAt   int
	err      error
}

type eventAppendContext struct {
	err         error
	hasDeadline bool
	deadline    time.Time
}

type cancelAfterFileReadAppender struct {
	recordingEventAppender
	cancel context.CancelFunc
}

func (a *cancelAfterFileReadAppender) Append(ctx context.Context, event modulecore.EventEnvelope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.recordingEventAppender.Append(ctx, event); err != nil {
		return err
	}
	if event.EventType == dciFileReadEventType {
		a.cancel()
	}
	return nil
}

func (a *recordingEventAppender) Append(ctx context.Context, event modulecore.EventEnvelope) error {
	deadline, hasDeadline := ctx.Deadline()
	a.contexts = append(a.contexts, eventAppendContext{
		err:         ctx.Err(),
		hasDeadline: hasDeadline,
		deadline:    deadline,
	})
	if a.failAt > 0 && len(a.events)+1 >= a.failAt {
		if a.err != nil {
			return a.err
		}
		return errors.New("recording event appender failure")
	}
	a.events = append(a.events, event)
	return nil
}

func newTestExplorer(cfg Config, store TraceStore, opts ...Option) *Explorer {
	if cfg.ActorKind == "" {
		cfg.ActorKind = "agent"
	}
	if cfg.ActorID == "" {
		cfg.ActorID = "shiro"
	}
	return NewExplorer(cfg, store, append([]Option{WithEventAppender(&recordingEventAppender{})}, opts...)...)
}

func (s *memoryTraceStore) SaveSearchTrace(ctx context.Context, trace domaindci.SearchTrace) error {
	s.traces = append(s.traces, trace)
	deadline, hasDeadline := ctx.Deadline()
	s.contexts = append(s.contexts, eventAppendContext{
		err:         ctx.Err(),
		hasDeadline: hasDeadline,
		deadline:    deadline,
	})
	return nil
}

func TestExplorerSearchFindsEvidenceInsideAllowlist(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "spec.md"), "# DCI\nDirect Corpus Interaction is evidence lookup.\n")
	store := &memoryTraceStore{}
	explorer := newTestExplorer(Config{
		Enabled:         true,
		Allowlist:       []string{dir},
		MaxEvidence:     3,
		MaxFilesRead:    5,
		MaxSnippetChars: 120,
		Now:             fixedNow,
	}, store)

	result, err := explorer.Search(context.Background(), "Corpus Interaction")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result.Pack.Evidence) == 0 {
		t.Fatal("expected evidence")
	}
	ev := result.Pack.Evidence[0]
	if ev.FilePath != filepath.Join(dir, "spec.md") {
		t.Fatalf("file path = %s", ev.FilePath)
	}
	if ev.LineStart != 2 {
		t.Fatalf("line start = %d", ev.LineStart)
	}
	if len(store.traces) != 1 || store.traces[0].FinalEvidenceCount == 0 || store.traces[0].ActorKind != "agent" || store.traces[0].ActorID != "shiro" || store.traces[0].Mode != "dci" || store.traces[0].ActionID == "" || store.traces[0].TraceID == "" {
		t.Fatalf("trace not saved with evidence: %#v", store.traces)
	}
}

func TestExplorerSearchAppendsCanonicalEventGraph(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "spec.md"), "DCI canonical evidence\n")
	appender := &recordingEventAppender{}
	explorer := NewExplorer(Config{
		Enabled:      true,
		Allowlist:    []string{dir},
		MaxEvidence:  1,
		MaxFilesRead: 1,
		Now:          fixedNow,
	}, nil, WithEventAppender(appender))
	traceID := modulecore.NewTraceID()
	actionID := modulecore.NewActionID()
	result, err := explorer.SearchWithIdentity(context.Background(), "canonical", traceID, actionID, "agent", "shiro", "idem-canonical")
	if err != nil {
		t.Fatalf("SearchWithIdentity failed: %v", err)
	}
	if err := domaindci.ValidateSearchResult(result); err != nil {
		t.Fatalf("result validation failed: %v", err)
	}
	if len(result.Pack.DerivedTerms) != 1 || result.Pack.DerivedTerms[0] != "canonical" {
		t.Fatalf("derived terms = %#v, want [canonical]", result.Pack.DerivedTerms)
	}
	wantTypes := []string{
		dciSearchRequestedEventType,
		dciSearchStartedEventType,
		dciSourceSelectedEventType,
		dciFileReadEventType,
		dciEvidenceCreatedEventType,
		dciSearchCompletedEventType,
	}
	if len(appender.events) != len(wantTypes) {
		t.Fatalf("event count = %d, want %d: %#v", len(appender.events), len(wantTypes), appender.events)
	}
	for index, event := range appender.events {
		if event.EventType != wantTypes[index] {
			t.Fatalf("event[%d] type = %q, want %q", index, event.EventType, wantTypes[index])
		}
		if err := modulecore.ValidateEventEnvelope(event); err != nil {
			t.Fatalf("event[%d] invalid: %v", index, err)
		}
		if event.TraceID != traceID || event.ActionID != actionID || event.ComponentID != dciComponentID || event.ActorKind != "agent" || event.ActorID != "shiro" {
			t.Fatalf("event[%d] identity mismatch: %#v", index, event)
		}
		if index == 0 && event.CausationEventID != "" {
			t.Fatalf("requested event must be root: %#v", event)
		}
		if index > 0 && event.CausationEventID != appender.events[index-1].EventID && event.EventType != dciEvidenceCreatedEventType {
			t.Fatalf("event[%d] causation = %q, previous = %q", index, event.CausationEventID, appender.events[index-1].EventID)
		}
	}
	if err := modulecore.ValidateEventEnvelopeGraph(appender.events); err != nil {
		t.Fatalf("event graph invalid: %v", err)
	}
	var readEvent, evidenceEvent modulecore.EventEnvelope
	for _, event := range appender.events {
		switch event.EventType {
		case dciFileReadEventType:
			readEvent = event
		case dciEvidenceCreatedEventType:
			evidenceEvent = event
		}
	}
	if len(result.Trace.Steps) != 1 || result.Trace.Steps[0].EventID != readEvent.EventID || result.Trace.Steps[0].EventType != dciFileReadEventType {
		t.Fatalf("file-read reverse reference missing: trace=%#v read=%#v", result.Trace.Steps, readEvent)
	}
	if len(result.Pack.Evidence) != 1 || result.Pack.Evidence[0].CreatedByEventID != evidenceEvent.EventID || result.Pack.Evidence[0].EvidenceID != evidenceEvent.EvidenceID {
		t.Fatalf("evidence reverse reference missing: pack=%#v event=%#v", result.Pack.Evidence, evidenceEvent)
	}
	if evidenceEvent.CausationEventID != readEvent.EventID {
		t.Fatalf("evidence event must be caused by file-read event: evidence=%#v read=%#v", evidenceEvent, readEvent)
	}
	terminal := appender.events[len(appender.events)-1]
	if terminal.CausationEventID != evidenceEvent.EventID || terminal.DependencyEventIDs != nil {
		t.Fatalf("single-evidence terminal join = cause %q dependencies %#v, want evidence cause and no dependencies", terminal.CausationEventID, terminal.DependencyEventIDs)
	}
	if !strings.HasPrefix(string(result.Pack.Evidence[0].EvidenceID), "evd_") || strings.HasPrefix(string(result.Pack.Evidence[0].EvidenceID), string(evidenceEvent.EventID)) {
		t.Fatalf("evidence ID is not independent: %#v event=%q", result.Pack.Evidence[0].EvidenceID, evidenceEvent.EventID)
	}
}

func TestTerminalEventJoin(t *testing.T) {
	const (
		lastRead = modulecore.EventID("evt_00000000-0000-7000-8000-000000000009")
		first    = modulecore.EventID("evt_00000000-0000-7000-8000-000000000003")
		second   = modulecore.EventID("evt_00000000-0000-7000-8000-000000000001")
		third    = modulecore.EventID("evt_00000000-0000-7000-8000-000000000002")
	)
	tests := []struct {
		name        string
		lastEventID modulecore.EventID
		evidenceIDs []modulecore.EventID
		wantCause   modulecore.EventID
		wantDeps    []modulecore.EventID
		wantErr     bool
	}{
		{name: "zero", lastEventID: lastRead, wantCause: lastRead},
		{name: "one", lastEventID: lastRead, evidenceIDs: []modulecore.EventID{first}, wantCause: first},
		{name: "multiple", lastEventID: lastRead, evidenceIDs: []modulecore.EventID{first, second, third}, wantCause: third, wantDeps: []modulecore.EventID{second, first}},
		{name: "empty cause", wantErr: true},
		{name: "invalid cause", lastEventID: "not-an-event", wantErr: true},
		{name: "empty evidence", lastEventID: lastRead, evidenceIDs: []modulecore.EventID{first, ""}, wantErr: true},
		{name: "invalid evidence", lastEventID: lastRead, evidenceIDs: []modulecore.EventID{first, "not-an-event"}, wantErr: true},
		{name: "duplicate dependency", lastEventID: lastRead, evidenceIDs: []modulecore.EventID{first, second, first, third}, wantErr: true},
		{name: "duplicate cause", lastEventID: lastRead, evidenceIDs: []modulecore.EventID{first, second, third, third}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := append([]modulecore.EventID(nil), tt.evidenceIDs...)
			cause, dependencies, err := terminalEventJoin(tt.lastEventID, input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("terminalEventJoin unexpectedly succeeded: cause=%q dependencies=%#v", cause, dependencies)
				}
				if cause != "" || dependencies != nil {
					t.Fatalf("failed terminalEventJoin returned values: cause=%q dependencies=%#v", cause, dependencies)
				}
				return
			}
			if err != nil {
				t.Fatalf("terminalEventJoin failed: %v", err)
			}
			if cause != tt.wantCause {
				t.Fatalf("cause = %q, want %q", cause, tt.wantCause)
			}
			wantDeps := append([]modulecore.EventID(nil), tt.wantDeps...)
			sort.Slice(wantDeps, func(left, right int) bool { return wantDeps[left] < wantDeps[right] })
			if len(dependencies) != len(wantDeps) {
				t.Fatalf("dependencies = %#v, want %#v", dependencies, wantDeps)
			}
			for index := range wantDeps {
				if dependencies[index] != wantDeps[index] {
					t.Fatalf("dependencies = %#v, want %#v", dependencies, wantDeps)
				}
			}
			if len(wantDeps) == 0 && dependencies != nil {
				t.Fatalf("dependencies = %#v, want nil", dependencies)
			}
			for index := range input {
				if input[index] != tt.evidenceIDs[index] {
					t.Fatalf("input evidence IDs mutated: %#v", input)
				}
			}
		})
	}
}

func TestExplorerSearchCompletedTerminalJoinsMultipleEvidenceBranches(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.md"), "needle first\nneedle second\n")
	writeFile(t, filepath.Join(dir, "b.md"), "needle third\n")
	appender := &recordingEventAppender{}
	explorer := NewExplorer(Config{
		Enabled:           true,
		Allowlist:         []string{dir},
		MaxCandidateFiles: 2,
		MaxSteps:          4,
		MaxFilesRead:      2,
		MaxEvidence:       3,
		Now:               fixedNow,
	}, nil, WithEventAppender(appender))

	result, err := explorer.SearchWithIdentity(context.Background(), "needle", modulecore.NewTraceID(), modulecore.NewActionID(), "agent", "shiro", "")
	if err != nil {
		t.Fatalf("SearchWithIdentity failed: %v", err)
	}
	if len(result.Pack.Evidence) != 3 {
		t.Fatalf("evidence count = %d, want 3: %#v", len(result.Pack.Evidence), result.Pack.Evidence)
	}
	terminal := appender.events[len(appender.events)-1]
	if terminal.EventType != dciSearchCompletedEventType {
		t.Fatalf("terminal event = %#v, want completed", terminal)
	}
	assertTerminalEvidenceJoin(t, terminal, appender.events)
	for _, event := range appender.events[:len(appender.events)-1] {
		if event.DependencyEventIDs != nil {
			t.Fatalf("nonterminal event %q unexpectedly has dependencies: %#v", event.EventType, event.DependencyEventIDs)
		}
	}
	if err := modulecore.ValidateEventEnvelopeGraph(appender.events); err != nil {
		t.Fatalf("event graph invalid: %v", err)
	}
}

func assertTerminalEvidenceJoin(t *testing.T, terminal modulecore.EventEnvelope, events []modulecore.EventEnvelope) {
	t.Helper()
	evidenceIDs := make([]modulecore.EventID, 0)
	for _, event := range events {
		if event.EventType == dciEvidenceCreatedEventType {
			evidenceIDs = append(evidenceIDs, event.EventID)
		}
	}
	if len(evidenceIDs) < 2 {
		t.Fatalf("evidence events = %#v, want at least two", evidenceIDs)
	}
	wantCause := evidenceIDs[len(evidenceIDs)-1]
	if terminal.CausationEventID != wantCause {
		t.Fatalf("terminal cause = %q, want last evidence %q", terminal.CausationEventID, wantCause)
	}
	wantDependencies := append([]modulecore.EventID(nil), evidenceIDs[:len(evidenceIDs)-1]...)
	sort.Slice(wantDependencies, func(left, right int) bool { return wantDependencies[left] < wantDependencies[right] })
	if len(terminal.DependencyEventIDs) != len(wantDependencies) {
		t.Fatalf("terminal dependencies = %#v, want %#v", terminal.DependencyEventIDs, wantDependencies)
	}
	for index := range wantDependencies {
		if terminal.DependencyEventIDs[index] != wantDependencies[index] {
			t.Fatalf("terminal dependencies = %#v, want %#v", terminal.DependencyEventIDs, wantDependencies)
		}
		if terminal.DependencyEventIDs[index] == terminal.CausationEventID {
			t.Fatalf("terminal cause is duplicated as dependency: %#v", terminal)
		}
	}
	refs := append([]modulecore.EventID{terminal.CausationEventID}, terminal.DependencyEventIDs...)
	wantRefs := append([]modulecore.EventID(nil), evidenceIDs...)
	sort.Slice(refs, func(left, right int) bool { return refs[left] < refs[right] })
	sort.Slice(wantRefs, func(left, right int) bool { return wantRefs[left] < wantRefs[right] })
	if len(refs) != len(wantRefs) {
		t.Fatalf("terminal evidence references = %#v, want %#v", refs, wantRefs)
	}
	for index := range wantRefs {
		if refs[index] != wantRefs[index] {
			t.Fatalf("terminal evidence references = %#v, want %#v", refs, wantRefs)
		}
		if index > 0 && refs[index] == refs[index-1] {
			t.Fatalf("terminal evidence reference duplicated: %#v", refs)
		}
	}
}

func TestExplorerSearchCompletesNoEvidenceWithoutEvidenceEvent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "spec.md"), "unrelated text\n")
	appender := &recordingEventAppender{}
	explorer := NewExplorer(Config{
		Enabled:      true,
		Allowlist:    []string{dir},
		MaxEvidence:  2,
		MaxFilesRead: 2,
		Now:          fixedNow,
	}, nil, WithEventAppender(appender))
	result, err := explorer.SearchWithIdentity(context.Background(), "missing", modulecore.NewTraceID(), modulecore.NewActionID(), "agent", "shiro", "")
	if err != nil {
		t.Fatalf("SearchWithIdentity failed: %v", err)
	}
	if result.Trace.Status != "completed" || result.Trace.FinalEvidenceCount != 0 || len(result.Pack.Evidence) != 0 {
		t.Fatalf("unexpected no-evidence result: %#v", result)
	}
	if len(result.Pack.Limitations) == 0 || result.Pack.Limitations[len(result.Pack.Limitations)-1] != "no evidence found in allowed corpus" {
		t.Fatalf("missing no-evidence limitation: %#v", result.Pack.Limitations)
	}
	for _, event := range appender.events {
		if event.EventType == dciEvidenceCreatedEventType || event.EventType == dciSearchFailedEventType {
			t.Fatalf("unexpected event for completed no-evidence search: %#v", event)
		}
	}
	terminal := appender.events[len(appender.events)-1]
	previous := appender.events[len(appender.events)-2]
	if terminal.CausationEventID != previous.EventID || terminal.DependencyEventIDs != nil {
		t.Fatalf("zero-evidence terminal join = cause %q dependencies %#v, want previous event cause and no dependencies", terminal.CausationEventID, terminal.DependencyEventIDs)
	}
}

func TestExplorerSearchNoAllowlistStillDerivesTerms(t *testing.T) {
	appender := &recordingEventAppender{}
	explorer := NewExplorer(Config{
		Enabled: true,
		Now:     fixedNow,
	}, nil, WithEventAppender(appender))
	result, err := explorer.SearchWithIdentity(context.Background(), "Direct Corpus", modulecore.NewTraceID(), modulecore.NewActionID(), "agent", "shiro", "")
	if err != nil {
		t.Fatalf("SearchWithIdentity failed: %v", err)
	}
	if result.Trace.Status != "completed" || len(result.Pack.Evidence) != 0 {
		t.Fatalf("unexpected no-allowlist result: %#v", result)
	}
	wantTerms := []string{"direct", "corpus"}
	if len(result.Pack.DerivedTerms) != len(wantTerms) {
		t.Fatalf("derived terms = %#v, want %#v", result.Pack.DerivedTerms, wantTerms)
	}
	for index, want := range wantTerms {
		if result.Pack.DerivedTerms[index] != want {
			t.Fatalf("derived terms = %#v, want %#v", result.Pack.DerivedTerms, wantTerms)
		}
	}
	if len(appender.events) == 0 || appender.events[len(appender.events)-1].EventType != dciSearchCompletedEventType {
		t.Fatalf("terminal event = %#v", appender.events)
	}
}

func TestExplorerSearchLimitIsLimitationNotSyntheticStepOrEvent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.md"), "not relevant\n")
	writeFile(t, filepath.Join(dir, "b.md"), "also not relevant\n")
	appender := &recordingEventAppender{}
	explorer := NewExplorer(Config{
		Enabled:      true,
		Allowlist:    []string{dir},
		MaxSteps:     1,
		MaxEvidence:  2,
		MaxFilesRead: 2,
		Now:          fixedNow,
	}, nil, WithEventAppender(appender))
	result, err := explorer.SearchWithIdentity(context.Background(), "missing", modulecore.NewTraceID(), modulecore.NewActionID(), "agent", "shiro", "")
	if err != nil {
		t.Fatalf("SearchWithIdentity failed: %v", err)
	}
	if len(result.Trace.Steps) != 1 || result.Trace.Steps[0].EventType != dciFileReadEventType {
		t.Fatalf("limit created a synthetic step: %#v", result.Trace.Steps)
	}
	if len(result.Pack.Limitations) == 0 || result.Pack.Limitations[0] != "max search steps reached" {
		t.Fatalf("missing limit limitation: %#v", result.Pack.Limitations)
	}
	for _, event := range appender.events {
		if event.EventType == "dci.limit" || event.EventType == dciSearchFailedEventType {
			t.Fatalf("limit created an invalid event: %#v", event)
		}
	}
	if appender.events[len(appender.events)-1].EventType != dciSearchCompletedEventType {
		t.Fatalf("limit did not complete search: %#v", appender.events)
	}
}

func TestExplorerSearchFileReadFailureAppendsErrorStepAndCompletes(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.md")
	provider := &dciSourceCandidateProvider{ranks: []domaindci.SourceMetadataRank{{FilePath: missing}}}
	appender := &recordingEventAppender{}
	explorer := NewExplorer(Config{
		Enabled:      true,
		Allowlist:    []string{dir},
		MaxEvidence:  1,
		MaxFilesRead: 1,
		Now:          fixedNow,
	}, nil, WithEventAppender(appender), WithSourceCandidateProvider(provider))
	result, err := explorer.SearchWithIdentity(context.Background(), "missing", modulecore.NewTraceID(), modulecore.NewActionID(), "agent", "shiro", "")
	if err != nil {
		t.Fatalf("file-read error should be a completed search: %v", err)
	}
	if result.Trace.Status != "completed" || len(result.Trace.Steps) != 1 || result.Trace.Steps[0].Status != "error" || result.Trace.Steps[0].EventType != dciFileReadEventType {
		t.Fatalf("unexpected file-read failure result: %#v", result)
	}
	if result.Trace.Steps[0].EventID == "" {
		t.Fatal("file-read error step missing event ID")
	}
	if len(appender.events) != 5 || appender.events[3].EventType != dciFileReadEventType || appender.events[3].Payload["status"] != "error" || appender.events[4].EventType != dciSearchCompletedEventType {
		t.Fatalf("unexpected file-read error events: %#v", appender.events)
	}
}

func TestExplorerSearchTimeoutAppendsFailedEventAndReturnsError(t *testing.T) {
	provider := &blockingDCISourceCandidateProvider{}
	appender := &recordingEventAppender{}
	traceStore := &memoryTraceStore{}
	explorer := NewExplorer(Config{
		Enabled:    true,
		Allowlist:  []string{t.TempDir()},
		MaxSeconds: 1,
		Now:        fixedNow,
	}, traceStore, WithEventAppender(appender), WithSourceCandidateProvider(provider))
	result, err := explorer.SearchWithIdentity(context.Background(), "timeout", modulecore.NewTraceID(), modulecore.NewActionID(), "agent", "shiro", "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v, want deadline exceeded", err)
	}
	if result.Trace.Status != "failed" || result.Trace.ErrorMessage == "" {
		t.Fatalf("timeout did not return failed trace: %#v", result)
	}
	if len(appender.events) != 3 || appender.events[2].EventType != dciSearchFailedEventType {
		t.Fatalf("timeout event sequence = %#v", appender.events)
	}
	if len(traceStore.traces) != 1 || traceStore.traces[0].Status != "failed" {
		t.Fatalf("timeout failure was not best-effort persisted: %#v", traceStore.traces)
	}
}

func TestExplorerSearchFailsClosedWhenEventAppendFails(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "spec.md"), "append failure evidence\n")
	sentinel := errors.New("event store unavailable")
	appender := &recordingEventAppender{failAt: 4, err: sentinel}
	store := &memoryTraceStore{}
	explorer := NewExplorer(Config{
		Enabled:      true,
		Allowlist:    []string{dir},
		MaxEvidence:  1,
		MaxFilesRead: 1,
		Now:          fixedNow,
	}, store, WithEventAppender(appender))
	_, err := explorer.SearchWithIdentity(context.Background(), "append", modulecore.NewTraceID(), modulecore.NewActionID(), "agent", "shiro", "")
	if !errors.Is(err, sentinel) {
		t.Fatalf("append failure error = %v, want %v", err, sentinel)
	}
	if len(appender.events) != 3 {
		t.Fatalf("partial append count = %d, want 3", len(appender.events))
	}
	if len(store.traces) != 0 {
		t.Fatalf("projection saved after append failure: %#v", store.traces)
	}
}

func TestExplorerSearchRequiresEventAppender(t *testing.T) {
	explorer := NewExplorer(Config{Enabled: true, Now: fixedNow}, nil)
	_, err := explorer.SearchWithIdentity(context.Background(), "query", modulecore.NewTraceID(), modulecore.NewActionID(), "agent", "shiro", "")
	if err == nil || !strings.Contains(err.Error(), "event appender") {
		t.Fatalf("missing appender error = %v", err)
	}
}

func TestExplorerSearchSkipsDenylist(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "public.md"), "public DCI note\n")
	writeFile(t, filepath.Join(dir, ".env"), "DCI_SECRET=do-not-read\n")
	explorer := newTestExplorer(Config{
		Enabled:          true,
		Allowlist:        []string{dir},
		DenylistPatterns: []string{".env", "secret"},
		MaxEvidence:      10,
		MaxFilesRead:     10,
		Now:              fixedNow,
	}, nil)

	result, err := explorer.Search(context.Background(), "DCI_SECRET")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result.Pack.Evidence) != 0 {
		t.Fatalf("denylisted evidence leaked: %#v", result.Pack.Evidence)
	}
	if len(result.Pack.Limitations) == 0 {
		t.Fatal("expected limitation when no evidence found")
	}
}

func TestExplorerSearchStopsAtMaxSteps(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.md"), "not relevant\n")
	writeFile(t, filepath.Join(dir, "b.md"), "DCI late evidence\n")
	explorer := newTestExplorer(Config{
		Enabled:      true,
		Allowlist:    []string{dir},
		MaxSteps:     1,
		MaxEvidence:  10,
		MaxFilesRead: 10,
		Now:          fixedNow,
	}, nil)

	result, err := explorer.Search(context.Background(), "missing")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result.Trace.Steps) != 1 || result.Trace.Steps[0].EventType != dciFileReadEventType {
		t.Fatalf("expected only the file-read step, got %#v", result.Trace.Steps)
	}
	if len(result.Pack.Limitations) == 0 || result.Pack.Limitations[0] != "max search steps reached" {
		t.Fatalf("expected max steps limitation, got %#v", result.Pack.Limitations)
	}
	if len(result.Pack.Evidence) != 0 {
		t.Fatalf("expected no evidence after step limit, got %#v", result.Pack.Evidence)
	}
}

func TestExplorerSearchRanksPathMatchesBeforeWalkOrder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.md"), "not relevant\n")
	target := filepath.Join(dir, "zz_dci_target.md")
	writeFile(t, target, "DCI ranked evidence\n")
	explorer := newTestExplorer(Config{
		Enabled:           true,
		Allowlist:         []string{dir},
		MaxCandidateFiles: 10,
		MaxFilesRead:      1,
		MaxEvidence:       1,
		Now:               fixedNow,
	}, nil)

	result, err := explorer.Search(context.Background(), "DCI")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result.Pack.Evidence) != 1 {
		t.Fatalf("expected one ranked evidence, got %#v", result.Pack.Evidence)
	}
	if result.Pack.Evidence[0].FilePath != target {
		t.Fatalf("expected ranked target first, got %s", result.Pack.Evidence[0].FilePath)
	}
	if len(result.Trace.Steps) != 1 || result.Trace.Steps[0].FilePath != target {
		t.Fatalf("expected only ranked target to be read, got %#v", result.Trace.Steps)
	}
}

func TestExplorerSearchRanksContentMatchesBeforeWalkOrder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.md"), "not relevant\n")
	target := filepath.Join(dir, "z.md")
	writeFile(t, target, "本文だけに Direct Corpus Interaction の根拠がある\n")
	explorer := newTestExplorer(Config{
		Enabled:           true,
		Allowlist:         []string{dir},
		MaxCandidateFiles: 10,
		MaxFilesRead:      1,
		MaxEvidence:       1,
		Now:               fixedNow,
	}, nil)

	result, err := explorer.Search(context.Background(), "Direct Corpus Interaction")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result.Pack.Evidence) != 1 {
		t.Fatalf("expected one content-ranked evidence, got %#v", result.Pack.Evidence)
	}
	if result.Pack.Evidence[0].FilePath != target {
		t.Fatalf("expected content-ranked target first, got %s", result.Pack.Evidence[0].FilePath)
	}
	if len(result.Trace.Steps) != 1 || result.Trace.Steps[0].FilePath != target {
		t.Fatalf("expected only content-ranked target to be read, got %#v", result.Trace.Steps)
	}
}

type dciSourceMetadataRanker struct {
	ranks []domaindci.SourceMetadataRank
	err   error
	paths []string
	terms []string
}

func (r *dciSourceMetadataRanker) RankDCICandidateFiles(_ context.Context, paths []string, terms []string) ([]domaindci.SourceMetadataRank, error) {
	r.paths = append([]string(nil), paths...)
	r.terms = append([]string(nil), terms...)
	if r.err != nil {
		return nil, r.err
	}
	return append([]domaindci.SourceMetadataRank(nil), r.ranks...), nil
}

type dciSourceCandidateProvider struct {
	ranks []domaindci.SourceMetadataRank
	err   error
	query string
	terms []string
	limit int
	calls int
}

type blockingDCISourceCandidateProvider struct{}

func (*blockingDCISourceCandidateProvider) CandidateFiles(ctx context.Context, _ string, _ []string, _ []string, _ int) ([]domaindci.SourceMetadataRank, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestExplorerSearchTimeoutRecoveryUsesFreshBoundedContext(t *testing.T) {
	provider := &blockingDCISourceCandidateProvider{}
	appender := &recordingEventAppender{}
	traceStore := &memoryTraceStore{}
	explorer := NewExplorer(Config{
		Enabled:    true,
		Allowlist:  []string{t.TempDir()},
		MaxSeconds: 1,
		Now:        fixedNow,
	}, traceStore, WithEventAppender(appender), WithSourceCandidateProvider(provider))
	_, err := explorer.SearchWithIdentity(context.Background(), "timeout", modulecore.NewTraceID(), modulecore.NewActionID(), "agent", "shiro", "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v, want deadline exceeded", err)
	}
	if len(appender.contexts) != 3 {
		t.Fatalf("append contexts = %#v", appender.contexts)
	}
	recoveredAppend := appender.contexts[2]
	if recoveredAppend.err != nil || !recoveredAppend.hasDeadline || !recoveredAppend.deadline.After(time.Now()) {
		t.Fatalf("failed terminal append did not receive a fresh bounded context: %#v", recoveredAppend)
	}
	if len(traceStore.contexts) != 1 {
		t.Fatalf("trace store contexts = %#v", traceStore.contexts)
	}
	recoveredSave := traceStore.contexts[0]
	if recoveredSave.err != nil || !recoveredSave.hasDeadline || !recoveredSave.deadline.After(time.Now()) {
		t.Fatalf("failed trace save did not receive a fresh bounded context: %#v", recoveredSave)
	}
}

func TestExplorerSearchTimeoutAfterFileReadPersistsFailedTerminal(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "spec.md"), "identity architecture evidence\n")
	searchCtx, cancel := context.WithCancel(context.Background())
	appender := &cancelAfterFileReadAppender{cancel: cancel}
	traceStore := &memoryTraceStore{}
	explorer := NewExplorer(Config{
		Enabled:      true,
		Allowlist:    []string{dir},
		MaxEvidence:  1,
		MaxFilesRead: 1,
		Now:          fixedNow,
	}, traceStore, WithEventAppender(appender))

	result, err := explorer.SearchWithIdentity(searchCtx, "identity", modulecore.NewTraceID(), modulecore.NewActionID(), "agent", "shiro", "timeout-after-read")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("timeout-after-read error = %v, want context canceled", err)
	}
	if result.Trace.Status != "failed" || result.Trace.ErrorMessage != context.Canceled.Error() {
		t.Fatalf("timeout-after-read trace = %#v", result.Trace)
	}
	if len(appender.events) != 6 || appender.events[4].EventType != dciEvidenceCreatedEventType || appender.events[5].EventType != dciSearchFailedEventType {
		t.Fatalf("timeout-after-read events = %#v", appender.events)
	}
	for index, appendContext := range appender.contexts[4:] {
		if appendContext.err != nil || !appendContext.hasDeadline || !appendContext.deadline.After(time.Now()) {
			t.Fatalf("timeout-after-read recovery context %d = %#v", index, appendContext)
		}
	}
	if len(traceStore.traces) != 1 || traceStore.traces[0].Status != "failed" {
		t.Fatalf("timeout-after-read persistence = %#v", traceStore.traces)
	}
}

func (p *dciSourceCandidateProvider) CandidateFiles(_ context.Context, query string, terms []string, _ []string, limit int) ([]domaindci.SourceMetadataRank, error) {
	p.calls++
	p.query = query
	p.terms = append([]string(nil), terms...)
	p.limit = limit
	if p.err != nil {
		return nil, p.err
	}
	return append([]domaindci.SourceMetadataRank(nil), p.ranks...), nil
}

func TestExplorerSearchUsesFTSCandidateProviderBeforeWalkLimit(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.md"), "not relevant\n")
	target := filepath.Join(dir, "z.md")
	writeFile(t, target, "DCI FTS narrowed evidence\n")
	provider := &dciSourceCandidateProvider{
		ranks: []domaindci.SourceMetadataRank{{
			FilePath: target,
			SourceID: "kb_fts_src",
			Score:    1.20,
			Reason:   "l1 knowledge FTS matched local corpus candidate",
		}},
	}
	explorer := newTestExplorer(Config{
		Enabled:           true,
		Allowlist:         []string{dir},
		MaxCandidateFiles: 1,
		MaxFilesRead:      1,
		MaxEvidence:       1,
		Now:               fixedNow,
	}, nil, WithSourceCandidateProvider(provider))

	result, err := explorer.Search(context.Background(), "DCI")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if provider.query != "DCI" || provider.limit != 1 || len(provider.terms) != 1 || provider.terms[0] != "dci" {
		t.Fatalf("provider input mismatch query=%q limit=%d terms=%#v", provider.query, provider.limit, provider.terms)
	}
	if len(result.Pack.Evidence) != 1 {
		t.Fatalf("expected one evidence, got %#v", result.Pack.Evidence)
	}
	if result.Pack.Evidence[0].FilePath != target || result.Pack.Evidence[0].SourceID != "kb_fts_src" {
		t.Fatalf("expected FTS narrowed target with source id, got %#v", result.Pack.Evidence[0])
	}
}

func TestExplorerCandidateProviderResultsSuppressFilesystemFallbackWalk(t *testing.T) {
	dir := t.TempDir()
	providerTarget := filepath.Join(dir, "provider.md")
	walkOnlyTarget := filepath.Join(dir, "walk-only.md")
	writeFile(t, providerTarget, "provider evidence\n")
	writeFile(t, walkOnlyTarget, "walk fallback evidence\n")
	provider := &dciSourceCandidateProvider{ranks: []domaindci.SourceMetadataRank{{FilePath: providerTarget, Score: 1}}}
	explorer := NewExplorer(Config{Enabled: true, Allowlist: []string{dir}, MaxCandidateFiles: 10, Now: fixedNow}, nil, WithSourceCandidateProvider(provider))

	candidates, _, err := explorer.collectCandidateFiles(context.Background(), "evidence", []string{"evidence"}, nil)
	if err != nil {
		t.Fatalf("collectCandidateFiles failed: %v", err)
	}
	if len(candidates) != 1 || candidates[0] != providerTarget {
		t.Fatalf("provider candidates = %#v, want only %q", candidates, providerTarget)
	}
}

func TestExplorerSearchCombinesMultipleCandidateProviders(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.md"), "not relevant\n")
	semanticTarget := filepath.Join(dir, "semantic.md")
	writeFile(t, semanticTarget, "DCI semantic narrowed evidence\n")
	ftsTarget := filepath.Join(dir, "fts.md")
	writeFile(t, ftsTarget, "DCI FTS narrowed evidence\n")
	fts := &dciSourceCandidateProvider{ranks: []domaindci.SourceMetadataRank{{
		FilePath: ftsTarget,
		SourceID: "kb_fts_src",
		Score:    1.10,
		Reason:   "l1 knowledge FTS matched local corpus candidate",
	}}}
	semantic := &dciSourceCandidateProvider{ranks: []domaindci.SourceMetadataRank{{
		FilePath: semanticTarget,
		SourceID: "kb_vector_src",
		Score:    2.10,
		Reason:   "vector kb semantic match narrowed local corpus candidate",
	}}}
	explorer := newTestExplorer(Config{
		Enabled:           true,
		Allowlist:         []string{dir},
		MaxCandidateFiles: 3,
		MaxFilesRead:      1,
		MaxEvidence:       1,
		Now:               fixedNow,
	}, nil, WithSourceCandidateProvider(fts), WithSourceCandidateProvider(semantic))

	result, err := explorer.Search(context.Background(), "DCI")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if fts.calls != 1 || semantic.calls != 1 {
		t.Fatalf("expected both providers to be called: fts=%d semantic=%d", fts.calls, semantic.calls)
	}
	if len(result.Pack.Evidence) != 1 {
		t.Fatalf("expected one evidence, got %#v", result.Pack.Evidence)
	}
	if result.Pack.Evidence[0].FilePath != semanticTarget || result.Pack.Evidence[0].SourceID != "kb_vector_src" {
		t.Fatalf("expected semantic-ranked target first, got %#v", result.Pack.Evidence[0])
	}
}

func TestExplorerSearchUsesSourceRegistryMetadataRank(t *testing.T) {
	dir := t.TempDir()
	early := filepath.Join(dir, "a.md")
	writeFile(t, early, "DCI low priority evidence\n")
	target := filepath.Join(dir, "z.md")
	writeFile(t, target, "DCI metadata ranked evidence\n")
	ranker := &dciSourceMetadataRanker{
		ranks: []domaindci.SourceMetadataRank{{
			FilePath: target,
			SourceID: "src_ranked_spec",
			Score:    0.95,
			Reason:   "validated source registry metadata match",
		}},
	}
	explorer := newTestExplorer(Config{
		Enabled:           true,
		Allowlist:         []string{dir},
		MaxCandidateFiles: 10,
		MaxFilesRead:      1,
		MaxEvidence:       1,
		Now:               fixedNow,
	}, nil, WithSourceMetadataRanker(ranker))

	result, err := explorer.Search(context.Background(), "DCI")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(ranker.paths) != 2 {
		t.Fatalf("expected ranker to receive candidates, got %#v", ranker.paths)
	}
	if len(result.Pack.Evidence) != 1 {
		t.Fatalf("expected one evidence, got %#v", result.Pack.Evidence)
	}
	if result.Pack.Evidence[0].FilePath != target {
		t.Fatalf("expected metadata ranked target, got %s", result.Pack.Evidence[0].FilePath)
	}
	if result.Pack.Evidence[0].SourceID != "src_ranked_spec" {
		t.Fatalf("expected source id from metadata rank, got %#v", result.Pack.Evidence[0])
	}
	if len(result.Trace.Steps) != 1 || result.Trace.Steps[0].FilePath != target {
		t.Fatalf("expected only metadata ranked file to be read, got %#v", result.Trace.Steps)
	}
}

func TestExplorerSearchContinuesWhenSourceMetadataRankerFails(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "spec.md")
	writeFile(t, target, "DCI direct evidence\n")
	ranker := &dciSourceMetadataRanker{err: errors.New("source registry offline")}
	explorer := newTestExplorer(Config{
		Enabled:      true,
		Allowlist:    []string{dir},
		MaxFilesRead: 1,
		MaxEvidence:  1,
		Now:          fixedNow,
	}, nil, WithSourceMetadataRanker(ranker))

	result, err := explorer.Search(context.Background(), "DCI")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result.Pack.Evidence) != 1 || result.Pack.Evidence[0].FilePath != target {
		t.Fatalf("expected direct evidence after metadata rank failure, got %#v", result.Pack.Evidence)
	}
	if len(result.Pack.Limitations) == 0 {
		t.Fatalf("expected metadata ranking limitation")
	}
	if result.Pack.Limitations[0] != "source registry metadata ranking unavailable: source registry offline" {
		t.Fatalf("unexpected limitation: %#v", result.Pack.Limitations)
	}
}

type captureToolRunner struct {
	calls []toolCall
}

type toolCall struct {
	name string
	args map[string]any
}

func (r *captureToolRunner) ExecuteV2(_ context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) {
	r.calls = append(r.calls, toolCall{name: toolName, args: args})
	return tool.NewSuccess("tool mediated DCI evidence\n"), nil
}

func (r *captureToolRunner) ListTools(context.Context) ([]tool.ToolMetadata, error) {
	return []tool.ToolMetadata{{ToolID: "file_read"}}, nil
}

func TestExplorerSearchUsesToolRunnerForFileReadWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "spec.md")
	writeFile(t, target, "fallback content should not be used\n")
	runner := &captureToolRunner{}
	explorer := newTestExplorer(Config{
		Enabled:      true,
		Allowlist:    []string{dir},
		MaxEvidence:  3,
		MaxFilesRead: 5,
		Now:          fixedNow,
	}, nil, WithToolRunner(runner))

	result, err := explorer.Search(context.Background(), "mediated")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected one cached content-ranking file_read, got %d", len(runner.calls))
	}
	for _, call := range runner.calls {
		if call.name != "file_read" {
			t.Fatalf("tool = %s", call.name)
		}
		if call.args["path"] != target {
			t.Fatalf("path arg = %#v", call.args["path"])
		}
		if _, ok := call.args["limit"]; !ok {
			t.Fatalf("expected bounded file_read limit, got %#v", call.args)
		}
	}
	if len(result.Pack.Evidence) != 1 || result.Pack.Evidence[0].Snippet != "tool mediated DCI evidence" {
		t.Fatalf("expected tool response evidence, got %#v", result.Pack.Evidence)
	}
}

func TestExplorerContentRankingReadsOnlyFilesWithinExecutionBudget(t *testing.T) {
	dir := t.TempDir()
	ranks := make([]domaindci.SourceMetadataRank, 0, 5)
	for index := 0; index < 5; index++ {
		path := filepath.Join(dir, fmt.Sprintf("candidate-%d.md", index))
		writeFile(t, path, "bounded ranking evidence\n")
		ranks = append(ranks, domaindci.SourceMetadataRank{FilePath: path, Score: float64(5 - index), SourceID: fmt.Sprintf("src_%d", index)})
	}
	runner := &captureToolRunner{}
	explorer := newTestExplorer(Config{Enabled: true, Allowlist: []string{dir}, MaxCandidateFiles: 5, MaxFilesRead: 2, MaxEvidence: 6, Now: fixedNow}, nil,
		WithToolRunner(runner), WithSourceCandidateProvider(&dciSourceCandidateProvider{ranks: ranks}))
	if _, err := explorer.Search(context.Background(), "bounded"); err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("file reads=%d, want 2 (each executable candidate read once and reused for scan)", len(runner.calls))
	}
}

func TestExplorerShouldTriggerOnlyExplicitKeywords(t *testing.T) {
	explorer := newTestExplorer(Config{
		Enabled:          true,
		ExplicitKeywords: []string{"探して", "grep", "原文"},
	}, nil)

	if !explorer.ShouldTrigger("仕様書から探して") {
		t.Fatal("expected explicit DCI trigger")
	}
	if explorer.ShouldTrigger("普通に雑談しよう") {
		t.Fatal("did not expect DCI trigger")
	}
}

type dciBootstrapStore struct {
	manifests []domainskill.SkillManifest
	logs      []domainskill.SkillTriggerLog
}

func (s *dciBootstrapStore) ListSkillManifests(_ context.Context, _ int) ([]domainskill.SkillManifest, error) {
	return append([]domainskill.SkillManifest(nil), s.manifests...), nil
}

func (s *dciBootstrapStore) SaveSkillTriggerLog(_ context.Context, log domainskill.SkillTriggerLog) error {
	s.logs = append(s.logs, log)
	return nil
}

func TestExplorerSearchRecordsSkillBootstrap(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "spec.md"), "DCI evidence\n")
	store := &dciBootstrapStore{
		manifests: []domainskill.SkillManifest{{
			SkillID:         "core.dci-search",
			Enabled:         true,
			IntentTriggers:  []string{"dci_search"},
			KeywordTriggers: []string{"原文"},
		}},
	}
	skills := skillbootstrap.NewBootstrapService(store).WithNow(fixedNow)
	explorer := newTestExplorer(Config{
		Enabled:      true,
		Allowlist:    []string{dir},
		MaxEvidence:  1,
		MaxFilesRead: 1,
		Now:          fixedNow,
	}, nil, WithSkillBootstrap(skills))

	if _, err := explorer.Search(context.Background(), "原文を探して"); err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(store.logs) != 1 {
		t.Fatalf("expected one skill log, got %#v", store.logs)
	}
	if store.logs[0].SkillID != "core.dci-search" || store.logs[0].Status != domainskill.TriggerStatusTriggered {
		t.Fatalf("unexpected skill log: %#v", store.logs[0])
	}
}

type dciSourceCandidateStore struct {
	results []domaindci.SearchResult
}

func (s *dciSourceCandidateStore) SaveDCISourceCandidates(_ context.Context, result domaindci.SearchResult) error {
	if err := domaindci.ValidateSearchResult(result); err != nil {
		return err
	}
	s.results = append(s.results, result)
	return nil
}

type blockingDCISourceCandidateStore struct{}

func (*blockingDCISourceCandidateStore) SaveDCISourceCandidates(ctx context.Context, _ domaindci.SearchResult) error {
	<-ctx.Done()
	return ctx.Err()
}

type cancelingDCISourceCandidateStore struct {
	cancel context.CancelFunc
}

func (s *cancelingDCISourceCandidateStore) SaveDCISourceCandidates(context.Context, domaindci.SearchResult) error {
	if s.cancel != nil {
		s.cancel()
	}
	return context.Canceled
}

func TestExplorerSearchFailedTerminalJoinsMultipleEvidenceBranches(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.md"), "needle first\nneedle second\n")
	writeFile(t, filepath.Join(dir, "b.md"), "needle third\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	appender := &recordingEventAppender{}
	explorer := NewExplorer(Config{
		Enabled:           true,
		Allowlist:         []string{dir},
		MaxCandidateFiles: 2,
		MaxSteps:          4,
		MaxFilesRead:      2,
		MaxEvidence:       3,
		Now:               fixedNow,
	}, nil, WithEventAppender(appender), WithSourceCandidateStore(&cancelingDCISourceCandidateStore{cancel: cancel}))

	result, err := explorer.SearchWithIdentity(ctx, "needle", modulecore.NewTraceID(), modulecore.NewActionID(), "agent", "shiro", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SearchWithIdentity error = %v, want context canceled", err)
	}
	if result.Trace.Status != "failed" || len(result.Pack.Evidence) != 3 {
		t.Fatalf("failed result = %#v, want failed with three evidence rows", result)
	}
	terminal := appender.events[len(appender.events)-1]
	if terminal.EventType != dciSearchFailedEventType {
		t.Fatalf("terminal event = %#v, want failed", terminal)
	}
	assertTerminalEvidenceJoin(t, terminal, appender.events)
	if err := modulecore.ValidateEventEnvelopeGraph(appender.events); err != nil {
		t.Fatalf("event graph invalid: %v", err)
	}
}

func TestExplorerSearchIncludesProjectionFailureBeforeCompletedEvent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "spec.md"), "projection limitation evidence\n")
	appender := &recordingEventAppender{}
	candidateStore := &failingDCISourceCandidateStore{err: errors.New("candidate backend unavailable")}
	explorer := NewExplorer(Config{
		Enabled:      true,
		Allowlist:    []string{dir},
		MaxEvidence:  1,
		MaxFilesRead: 1,
		Now:          fixedNow,
	}, nil, WithEventAppender(appender), WithSourceCandidateStore(candidateStore))
	result, err := explorer.SearchWithIdentity(context.Background(), "projection", modulecore.NewTraceID(), modulecore.NewActionID(), "agent", "shiro", "")
	if err != nil {
		t.Fatalf("SearchWithIdentity failed: %v", err)
	}
	wantLimitation := "dci source candidate save failed"
	if len(result.Pack.Limitations) == 0 || result.Pack.Limitations[len(result.Pack.Limitations)-1] != wantLimitation {
		t.Fatalf("returned limitations = %#v", result.Pack.Limitations)
	}
	terminal := appender.events[len(appender.events)-1]
	if terminal.EventType != dciSearchCompletedEventType {
		t.Fatalf("terminal event = %#v", terminal)
	}
	payloadLimitations, ok := terminal.Payload["limitations"].([]string)
	if !ok || len(payloadLimitations) != len(result.Pack.Limitations) {
		t.Fatalf("terminal payload limitations = %#v, result = %#v", terminal.Payload["limitations"], result.Pack.Limitations)
	}
	for index := range result.Pack.Limitations {
		if payloadLimitations[index] != result.Pack.Limitations[index] {
			t.Fatalf("terminal payload limitations = %#v, result = %#v", payloadLimitations, result.Pack.Limitations)
		}
	}
}

func TestExplorerSearchProjectionDeadlineProducesFailedTerminal(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "spec.md"), "projection timeout evidence\n")
	appender := &recordingEventAppender{}
	traceStore := &memoryTraceStore{}
	explorer := NewExplorer(Config{
		Enabled:      true,
		Allowlist:    []string{dir},
		MaxSeconds:   1,
		MaxEvidence:  1,
		MaxFilesRead: 1,
		Now:          fixedNow,
	}, traceStore, WithEventAppender(appender), WithSourceCandidateStore(&blockingDCISourceCandidateStore{}))
	result, err := explorer.SearchWithIdentity(context.Background(), "projection", modulecore.NewTraceID(), modulecore.NewActionID(), "agent", "shiro", "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("projection timeout error = %v, want deadline exceeded", err)
	}
	if result.Trace.Status != "failed" || len(result.Pack.Limitations) == 0 || result.Pack.Limitations[len(result.Pack.Limitations)-1] != "dci source candidate save failed" {
		t.Fatalf("projection timeout result = %#v", result)
	}
	if appender.events[len(appender.events)-1].EventType != dciSearchFailedEventType {
		t.Fatalf("projection timeout terminal event = %#v", appender.events[len(appender.events)-1])
	}
	for _, event := range appender.events {
		if event.EventType == dciSearchCompletedEventType {
			t.Fatalf("projection timeout emitted completed event: %#v", appender.events)
		}
	}
	if len(traceStore.traces) != 1 || traceStore.traces[0].Status != "failed" {
		t.Fatalf("projection timeout trace persistence = %#v", traceStore.traces)
	}
}

func TestExplorerSearchSavesSourceCandidatesForEvidence(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "spec.md"), "DCI candidate evidence\n")
	candidates := &dciSourceCandidateStore{}
	explorer := newTestExplorer(Config{
		Enabled:      true,
		Allowlist:    []string{dir},
		MaxEvidence:  1,
		MaxFilesRead: 1,
		Now:          fixedNow,
	}, &memoryTraceStore{}, WithSourceCandidateStore(candidates))

	result, err := explorer.Search(context.Background(), "candidate")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result.Pack.Evidence) != 1 {
		t.Fatalf("expected one evidence, got %#v", result.Pack.Evidence)
	}
	if len(candidates.results) != 1 || len(candidates.results[0].Pack.Evidence) != 1 {
		t.Fatalf("source candidates were not saved: %#v", candidates.results)
	}
	saved := candidates.results[0]
	if saved.Trace.Status != "completed" || saved.Trace.FinalEvidenceCount != len(saved.Pack.Evidence) || saved.Trace.EndedAt.IsZero() {
		t.Fatalf("source candidate projection was not a complete terminal result: %#v", saved)
	}
}

func TestExplorerSearchWithIdentityUsesSuppliedIdentity(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "spec.md"), "Corpus identity evidence\n")
	traceStore := &memoryTraceStore{}
	skillStore := &dciBootstrapStore{manifests: []domainskill.SkillManifest{{
		SkillID:         "core.dci-search",
		Enabled:         true,
		IntentTriggers:  []string{"dci_search"},
		KeywordTriggers: []string{"corpus"},
	}}}
	appender := &recordingEventAppender{}
	explorer := NewExplorer(Config{
		Enabled:      true,
		Allowlist:    []string{dir},
		MaxEvidence:  1,
		MaxFilesRead: 1,
		Now:          fixedNow,
	}, traceStore, WithEventAppender(appender), WithSkillBootstrap(skillbootstrap.NewBootstrapService(skillStore).WithNow(fixedNow)))

	traceID := modulecore.NewTraceID()
	actionID := modulecore.NewActionID()
	result, err := explorer.SearchWithIdentity(context.Background(), "  Corpus  ", traceID, actionID, "  agent  ", "  shiro  ", "  idem-1  ")
	if err != nil {
		t.Fatalf("SearchWithIdentity failed: %v", err)
	}
	if result.Trace.TraceID != traceID || result.Trace.ActionID != actionID || result.Pack.ActionID != actionID || result.Trace.UserQuery != "Corpus" || result.Pack.Query != "Corpus" || result.Trace.ActorKind != "agent" || result.Trace.ActorID != "shiro" || result.Trace.IdempotencyKey != "idem-1" || result.Trace.Mode != "dci" {
		t.Fatalf("identity not propagated: trace=%#v pack=%#v", result.Trace, result.Pack)
	}
	if len(skillStore.logs) != 1 || skillStore.logs[0].Agent != "shiro" {
		t.Fatalf("skill bootstrap identity=%#v", skillStore.logs)
	}
	if len(traceStore.traces) != 1 || traceStore.traces[0].TraceID != traceID || traceStore.traces[0].ActionID != actionID {
		t.Fatalf("saved traces=%#v", traceStore.traces)
	}
}

func TestExplorerSearchWithIdentityRequiresTrimmedIdentity(t *testing.T) {
	explorer := newTestExplorer(Config{Enabled: true, Now: fixedNow}, nil)
	for _, tc := range []struct {
		name      string
		query     string
		traceID   modulecore.TraceID
		actionID  modulecore.ActionID
		actorKind string
		actorID   string
	}{
		{name: "query", query: " ", traceID: modulecore.NewTraceID(), actionID: modulecore.NewActionID(), actorKind: "agent", actorID: "shiro"},
		{name: "trace", query: "query", traceID: " ", actionID: modulecore.NewActionID(), actorKind: "agent", actorID: "shiro"},
		{name: "action", query: "query", traceID: modulecore.NewTraceID(), actionID: " ", actorKind: "agent", actorID: "shiro"},
		{name: "actor kind", query: "query", traceID: modulecore.NewTraceID(), actionID: modulecore.NewActionID(), actorKind: " ", actorID: "shiro"},
		{name: "actor id", query: "query", traceID: modulecore.NewTraceID(), actionID: modulecore.NewActionID(), actorKind: "agent", actorID: " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := explorer.SearchWithIdentity(context.Background(), tc.query, tc.traceID, tc.actionID, tc.actorKind, tc.actorID, ""); err == nil {
				t.Fatalf("SearchWithIdentity(%q, %q, %q, %q) unexpectedly succeeded", tc.query, tc.traceID, tc.actionID, tc.actorID)
			}
		})
	}
}

type failingDCISourceCandidateStore struct {
	traceStore     *memoryTraceStore
	calls          int
	sawTraceBefore bool
	err            error
}

func (s *failingDCISourceCandidateStore) SaveDCISourceCandidates(_ context.Context, _ domaindci.SearchResult) error {
	s.calls++
	s.sawTraceBefore = s.traceStore == nil || len(s.traceStore.traces) == 0
	return s.err
}

func TestExplorerSearchWithIdentitySavesTraceAfterAncillaryCandidateFailure(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "spec.md"), "DCI ancillary failure evidence\n")
	traceStore := &memoryTraceStore{}
	candidateStore := &failingDCISourceCandidateStore{traceStore: traceStore, err: errors.New("candidate backend unavailable")}
	explorer := newTestExplorer(Config{
		Enabled:      true,
		Allowlist:    []string{dir},
		MaxEvidence:  1,
		MaxFilesRead: 1,
		Now:          fixedNow,
	}, traceStore, WithSourceCandidateStore(candidateStore))

	result, err := explorer.SearchWithIdentity(context.Background(), "ancillary", modulecore.NewTraceID(), modulecore.NewActionID(), "agent", "shiro", "")
	if err != nil {
		t.Fatalf("SearchWithIdentity failed on ancillary candidate error: %v", err)
	}
	if candidateStore.calls != 1 || !candidateStore.sawTraceBefore {
		t.Fatalf("candidate save ordering calls=%d sawTraceBefore=%v", candidateStore.calls, candidateStore.sawTraceBefore)
	}
	if len(traceStore.traces) != 1 || len(result.Pack.Limitations) == 0 || result.Pack.Limitations[len(result.Pack.Limitations)-1] != "dci source candidate save failed" {
		t.Fatalf("trace limitation/result=%#v traces=%#v", result, traceStore.traces)
	}
}

type failingDCITraceStore struct {
	err   error
	calls int
}

func (s *failingDCITraceStore) SaveSearchTrace(context.Context, domaindci.SearchTrace) error {
	s.calls++
	return s.err
}

func TestExplorerSearchWithIdentityPropagatesFinalSaveFailureWithoutAllowlist(t *testing.T) {
	sentinel := errors.New("final dci store unavailable")
	store := &failingDCITraceStore{err: sentinel}
	explorer := newTestExplorer(Config{Enabled: true, Now: fixedNow}, store)

	_, err := explorer.SearchWithIdentity(context.Background(), "query", modulecore.NewTraceID(), modulecore.NewActionID(), "agent", "shiro", "")
	if !errors.Is(err, sentinel) {
		t.Fatalf("SearchWithIdentity error=%v, want %v", err, sentinel)
	}
	if store.calls != 1 {
		t.Fatalf("final store calls=%d, want 1", store.calls)
	}
}

func writeFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func fixedNow() time.Time {
	return time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
}
