package toolharness

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domain "github.com/Nyukimin/RenCrow_CORE/internal/domain/toolharness"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestNewCanonicalRecorderRejectsNilStore(t *testing.T) {
	if recorder, err := NewCanonicalRecorder(nil); err == nil || recorder != nil {
		t.Fatalf("NewCanonicalRecorder(nil) = recorder=%#v err=%v", recorder, err)
	}

	var store *eventstore.SQLiteStore
	if recorder, err := NewCanonicalRecorder(store); err == nil || recorder != nil {
		t.Fatalf("NewCanonicalRecorder(typed nil) = recorder=%#v err=%v", recorder, err)
	}
}

func TestCanonicalRecorderPersistsEnvelopeAndProjectsExecutionLineage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	store, err := eventstore.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}

	recorder, err := NewCanonicalRecorder(store)
	if err != nil {
		_ = store.Close()
		t.Fatalf("NewCanonicalRecorder() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	ctx, identity, scope := canonicalRecorderContext(t)
	actionID := modulecore.NewActionID()
	attemptID := modulecore.NewAttemptID()
	ctx, err = execution.WithBoundActionAttempt(ctx, actionID, attemptID)
	if err != nil {
		t.Fatalf("WithBoundActionAttempt() error = %v", err)
	}
	createdAt := time.Date(2026, 9, 7, 20, 15, 30, 123456789, time.FixedZone("test", 9*60*60))
	event := domain.Event{
		EventID:          string(modulecore.NewEventID()),
		ToolName:         "file_read",
		RawInputHash:     "sha256:mediation-input",
		ValidationStatus: domain.ValidationStatusRepaired,
		Repairs: []domain.Repair{{
			Type:       "optional_null_omission",
			Path:       []string{"mode"},
			BeforeType: "null",
			AfterType:  "(omitted)",
			Note:       "removed null optional field",
		}},
		CreatedAt: createdAt,
	}

	if err := recorder.RecordToolMediationEvent(ctx, event); err != nil {
		t.Fatalf("RecordToolMediationEvent() error = %v", err)
	}

	stored, found, err := store.GetByID(context.Background(), modulecore.EventID(event.EventID))
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !found {
		t.Fatal("GetByID() found = false, want true")
	}
	if stored.EventID != modulecore.EventID(event.EventID) {
		t.Fatalf("stored EventID = %q, want %q", stored.EventID, event.EventID)
	}
	if !stored.OccurredAt.Equal(createdAt) {
		t.Fatalf("stored OccurredAt = %s, want %s", stored.OccurredAt, createdAt)
	}
	if stored.ComponentID != canonicalMediationComponentID || stored.EventType != canonicalMediationEventType {
		t.Fatalf("stored routing = component=%q type=%q", stored.ComponentID, stored.EventType)
	}
	if stored.TraceID != identity.TraceID || stored.TaskID != identity.TaskID || stored.RunID != identity.RunID {
		t.Fatalf("stored execution lineage = trace=%q task=%q run=%q", stored.TraceID, stored.TaskID, stored.RunID)
	}
	if stored.ActorKind != string(scope.ActorKind) || stored.ActorID != scope.ActorID {
		t.Fatalf("stored actor lineage = kind=%q id=%q", stored.ActorKind, stored.ActorID)
	}
	if stored.ActionID != actionID || stored.AttemptID != attemptID {
		t.Fatalf("stored action lineage = action=%q attempt=%q", stored.ActionID, stored.AttemptID)
	}
	if stored.RequestID != "" || stored.CausationEventID != "" || len(stored.DependencyEventIDs) != 0 {
		t.Fatalf("unexpected non-mediation envelope references: %#v", stored)
	}
	assertMediationPayloadKeys(t, stored.Payload)
	if stored.Payload["tool_name"] != event.ToolName || stored.Payload["raw_input_hash"] != event.RawInputHash || stored.Payload["validation_status"] != string(event.ValidationStatus) {
		t.Fatalf("mediation payload = %#v", stored.Payload)
	}

	got, err := recorder.ListRecent(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListRecent() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListRecent() length = %d, want 1", len(got))
	}
	if got[0].EventID != event.EventID || got[0].EventSeq != int64(stored.EventSeq) || got[0].TaskID != string(identity.TaskID) || got[0].RunID != string(identity.RunID) || got[0].TraceID != string(identity.TraceID) || got[0].ActorKind != string(scope.ActorKind) || got[0].ActorID != scope.ActorID {
		t.Fatalf("projected event = %#v", got[0])
	}
	if !got[0].CreatedAt.Equal(createdAt) || !reflect.DeepEqual(got[0].Repairs, event.Repairs) {
		t.Fatalf("projected mediation event = %#v, want %#v", got[0], event)
	}
}

func TestCanonicalRecorderListRecentOrdersNewestAndSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	store, err := eventstore.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(first) error = %v", err)
	}
	recorder, err := NewCanonicalRecorder(store)
	if err != nil {
		_ = store.Close()
		t.Fatalf("NewCanonicalRecorder(first) error = %v", err)
	}
	ctx, _, _ := canonicalRecorderContext(t)
	first := mediationFixture("first", time.Date(2026, 9, 7, 20, 0, 0, 0, time.UTC))
	second := mediationFixture("second", time.Date(2026, 9, 7, 20, 1, 0, 0, time.UTC))
	for _, event := range []domain.Event{first, second} {
		if err := recorder.RecordToolMediationEvent(ctx, event); err != nil {
			_ = store.Close()
			t.Fatalf("RecordToolMediationEvent(%q) error = %v", event.EventID, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(first) error = %v", err)
	}

	store, err = eventstore.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(reopen) error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close(reopen) error = %v", err)
		}
	})
	recorder, err = NewCanonicalRecorder(store)
	if err != nil {
		t.Fatalf("NewCanonicalRecorder(reopen) error = %v", err)
	}

	got, err := recorder.ListRecent(context.Background(), 2)
	if err != nil {
		t.Fatalf("ListRecent(reopen) error = %v", err)
	}
	if len(got) != 2 || got[0].EventID != second.EventID || got[1].EventID != first.EventID {
		t.Fatalf("ListRecent(reopen) = %#v, want second then first", got)
	}
	if got[0].EventSeq <= got[1].EventSeq || got[0].EventSeq <= 0 || got[1].EventSeq <= 0 {
		t.Fatalf("projected event sequence = %d, %d", got[0].EventSeq, got[1].EventSeq)
	}
}

func TestCanonicalRecorderRejectsInvalidInputsWithoutAppending(t *testing.T) {
	recorder, store := newCanonicalRecorderTestRecorder(t)
	validContext, _, _ := canonicalRecorderContext(t)
	validEvent := mediationFixture("valid", time.Date(2026, 9, 7, 20, 0, 0, 0, time.UTC))

	missingTrace, err := execution.WithIdentity(context.Background(), modulecore.NewTaskID(), modulecore.NewRunID(), "")
	if err != nil {
		t.Fatalf("WithIdentity(missing trace) error = %v", err)
	}
	missingScope, _, _ := canonicalRecorderIdentityContext(t)
	canceledContext, cancel := context.WithCancel(validContext)
	cancel()

	cases := []struct {
		name  string
		ctx   context.Context
		event domain.Event
	}{
		{name: "nil_context", ctx: nil, event: validEvent},
		{name: "missing_identity", ctx: context.Background(), event: validEvent},
		{name: "missing_trace", ctx: missingTrace, event: validEvent},
		{name: "missing_scope", ctx: missingScope, event: validEvent},
		{name: "canceled_context", ctx: canceledContext, event: validEvent},
		{name: "invalid_event_id", ctx: validContext, event: func() domain.Event {
			event := validEvent
			event.EventID = "event-not-canonical"
			return event
		}()},
		{name: "caller_task_id", ctx: validContext, event: func() domain.Event {
			event := validEvent
			event.TaskID = "caller-supplied"
			return event
		}()},
		{name: "caller_event_seq", ctx: validContext, event: func() domain.Event {
			event := validEvent
			event.EventSeq = 1
			return event
		}()},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := recorder.RecordToolMediationEvent(test.ctx, test.event); err == nil {
				t.Fatal("RecordToolMediationEvent() error = nil")
			}
			items, err := recorder.ListRecent(context.Background(), 50)
			if err != nil {
				t.Fatalf("ListRecent() error = %v", err)
			}
			if len(items) != 0 {
				t.Fatalf("invalid event changed store: %#v", items)
			}
		})
	}
	_ = store
}

func TestCanonicalRecorderRequiresBoundedRecentLimit(t *testing.T) {
	store := &canonicalRecorderStoreStub{}
	recorder, err := NewCanonicalRecorder(store)
	if err != nil {
		t.Fatalf("NewCanonicalRecorder() error = %v", err)
	}

	if _, err := recorder.ListRecent(context.Background(), -1); err == nil {
		t.Fatal("ListRecent(-1) error = nil")
	}
	if store.listCalls != 0 {
		t.Fatalf("ListRecent(-1) called store %d times", store.listCalls)
	}
	if _, err := recorder.ListRecent(context.Background(), canonicalMediationMaxLimit+1); err == nil {
		t.Fatal("ListRecent(over-bound) error = nil")
	}
	if store.listCalls != 0 {
		t.Fatalf("ListRecent(over-bound) called store %d times", store.listCalls)
	}
	if _, err := recorder.ListRecent(context.Background(), 0); err != nil {
		t.Fatalf("ListRecent(default) error = %v", err)
	}
	if store.listCalls != 1 || store.lastLimit != canonicalMediationDefaultLimit {
		t.Fatalf("ListRecent(default) calls=%d limit=%d, want one call at %d", store.listCalls, store.lastLimit, canonicalMediationDefaultLimit)
	}
}

func TestCanonicalRecorderPropagatesStoreFailures(t *testing.T) {
	appendFailure := errors.New("append failure")
	appendStore := &canonicalRecorderStoreStub{appendErr: appendFailure}
	recorder, err := NewCanonicalRecorder(appendStore)
	if err != nil {
		t.Fatalf("NewCanonicalRecorder(append) error = %v", err)
	}
	ctx, _, _ := canonicalRecorderContext(t)
	if err := recorder.RecordToolMediationEvent(ctx, mediationFixture("append-failure", time.Now().UTC())); !errors.Is(err, appendFailure) {
		t.Fatalf("RecordToolMediationEvent() error = %v, want %v", err, appendFailure)
	}
	if appendStore.appendCalls != 1 {
		t.Fatalf("Append calls = %d, want 1", appendStore.appendCalls)
	}

	listFailure := errors.New("list failure")
	listStore := &canonicalRecorderStoreStub{listErr: listFailure}
	recorder, err = NewCanonicalRecorder(listStore)
	if err != nil {
		t.Fatalf("NewCanonicalRecorder(list) error = %v", err)
	}
	if _, err := recorder.ListRecent(context.Background(), 1); !errors.Is(err, listFailure) {
		t.Fatalf("ListRecent() error = %v, want %v", err, listFailure)
	}
	if listStore.listCalls != 1 {
		t.Fatalf("ListByComponent calls = %d, want 1", listStore.listCalls)
	}
}

func TestCanonicalRecorderRejectsStoredLineageCorruption(t *testing.T) {
	ctx, identity, _ := canonicalRecorderContext(t)
	base := modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion,
		EventID:       modulecore.NewEventID(),
		EventSeq:      1,
		TraceID:       identity.TraceID,
		EventType:     canonicalMediationEventType,
		ComponentID:   canonicalMediationComponentID,
		OccurredAt:    time.Now().UTC(),
		TaskID:        identity.TaskID,
		RunID:         identity.RunID,
		ActorKind:     string(domaintool.ActorKindAgent),
		ActorID:       "shiro",
		Payload: map[string]any{
			"tool_name":                 "file_read",
			"raw_input_hash":            "sha256:stored",
			"validation_status":         string(domain.ValidationStatusValid),
			"repairs_applied":           nil,
			"relation_defaults_applied": nil,
		},
	}

	cases := []struct {
		name string
		edit func(*modulecore.EventEnvelope)
	}{
		{name: "missing_task", edit: func(event *modulecore.EventEnvelope) { event.TaskID = "" }},
		{name: "missing_run", edit: func(event *modulecore.EventEnvelope) { event.RunID = "" }},
		{name: "missing_event_seq", edit: func(event *modulecore.EventEnvelope) { event.EventSeq = 0 }},
		{name: "missing_actor", edit: func(event *modulecore.EventEnvelope) { event.ActorID = "" }},
		{name: "invalid_actor_kind", edit: func(event *modulecore.EventEnvelope) { event.ActorKind = "worker" }},
		{name: "payload_envelope_id", edit: func(event *modulecore.EventEnvelope) { event.Payload["event_id"] = string(event.EventID) }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			event := base
			event.Payload = clonePayload(base.Payload)
			test.edit(&event)
			recorder, err := NewCanonicalRecorder(&canonicalRecorderStoreStub{events: []modulecore.EventEnvelope{event}})
			if err != nil {
				t.Fatalf("NewCanonicalRecorder() error = %v", err)
			}
			if _, err := recorder.ListRecent(ctx, 1); err == nil {
				t.Fatal("ListRecent() accepted corrupted stored lineage")
			}
		})
	}
}

func newCanonicalRecorderTestRecorder(t *testing.T) (*CanonicalRecorder, *eventstore.SQLiteStore) {
	t.Helper()
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	recorder, err := NewCanonicalRecorder(store)
	if err != nil {
		_ = store.Close()
		t.Fatalf("NewCanonicalRecorder() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return recorder, store
}

func canonicalRecorderContext(t *testing.T) (context.Context, execution.Identity, domaintool.ToolExecutionScope) {
	t.Helper()
	ctx, identity, scope := canonicalRecorderIdentityContext(t)
	return domaintool.WithToolExecutionScope(ctx, scope), identity, scope
}

func canonicalRecorderIdentityContext(t *testing.T) (context.Context, execution.Identity, domaintool.ToolExecutionScope) {
	t.Helper()
	identity := execution.Identity{
		TaskID:  modulecore.NewTaskID(),
		RunID:   modulecore.NewRunID(),
		TraceID: modulecore.NewTraceID(),
	}
	ctx, err := execution.WithIdentity(context.Background(), identity.TaskID, identity.RunID, identity.TraceID)
	if err != nil {
		t.Fatalf("WithIdentity() error = %v", err)
	}
	scope, err := domaintool.NewToolExecutionScope(
		"opaque-request-id",
		domaintool.ActorKindAgent,
		"shiro",
		"",
		[]string{domaintool.DataScopePublic},
		domaintool.AuthenticationSourceAgentOrchestrator,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope() error = %v", err)
	}
	return ctx, identity, scope
}

func mediationFixture(suffix string, createdAt time.Time) domain.Event {
	return domain.Event{
		EventID:          string(modulecore.NewEventID()),
		ToolName:         "file_read",
		RawInputHash:     "sha256:" + suffix,
		ValidationStatus: domain.ValidationStatusValid,
		CreatedAt:        createdAt,
	}
}

func assertMediationPayloadKeys(t *testing.T, payload map[string]any) {
	t.Helper()
	if err := validateMediationPayload(payload); err != nil {
		t.Fatalf("payload validation error = %v", err)
	}
	for key := range payload {
		if strings.HasSuffix(key, "_id") || key == "created_at" {
			t.Fatalf("payload contains envelope data %q: %#v", key, payload)
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal(payload) error = %v", err)
	}
	if strings.Contains(string(encoded), "event_id") || strings.Contains(string(encoded), "task_id") || strings.Contains(string(encoded), "trace_id") {
		t.Fatalf("payload JSON contains duplicate envelope IDs: %s", encoded)
	}
}

func clonePayload(payload map[string]any) map[string]any {
	cloned := make(map[string]any, len(payload))
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
}

type canonicalRecorderStoreStub struct {
	appendErr   error
	listErr     error
	events      []modulecore.EventEnvelope
	appendCalls int
	listCalls   int
	lastLimit   int
}

func (s *canonicalRecorderStoreStub) Append(_ context.Context, event modulecore.EventEnvelope) error {
	s.appendCalls++
	if s.appendErr != nil {
		return s.appendErr
	}
	s.events = append(s.events, event)
	return nil
}

func (s *canonicalRecorderStoreStub) GetByID(_ context.Context, id modulecore.EventID) (modulecore.EventEnvelope, bool, error) {
	for _, event := range s.events {
		if event.EventID == id {
			return event, true, nil
		}
	}
	return modulecore.EventEnvelope{}, false, nil
}

func (s *canonicalRecorderStoreStub) ListByComponent(_ context.Context, componentID string, limit int) ([]modulecore.EventEnvelope, error) {
	s.listCalls++
	s.lastLimit = limit
	if s.listErr != nil {
		return nil, s.listErr
	}
	events := make([]modulecore.EventEnvelope, 0, len(s.events))
	for _, event := range s.events {
		if event.ComponentID == componentID {
			events = append(events, event)
		}
	}
	if len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}
