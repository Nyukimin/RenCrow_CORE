package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// recoveryStubArtifactStore is the artifact side of the recovery pass: it holds the
// creation facts a previous process stored but could not deliver, and it hands them out
// by artifact id keyset the way the real stores do.
type recoveryStubArtifactStore struct {
	intents  map[modulecore.ArtifactID]modulecore.EventEnvelope
	listErr  error
	listCall int
}

func (s *recoveryStubArtifactStore) ListAPIArtifactPublicationIntents(_ context.Context, after modulecore.ArtifactID, _ int) ([]modulecore.EventEnvelope, error) {
	s.listCall++
	if s.listErr != nil {
		return nil, s.listErr
	}
	ids := make([]modulecore.ArtifactID, 0, len(s.intents))
	for id := range s.intents {
		if id > after {
			ids = append(ids, id)
		}
	}
	// Ascending, and one page only, so the pass has to come back for the rest.
	sortArtifactIDs(ids)
	if len(ids) > 1 {
		ids = ids[:1]
	}
	page := make([]modulecore.EventEnvelope, 0, len(ids))
	for _, id := range ids {
		page = append(page, s.intents[id])
	}
	return page, nil
}

func sortArtifactIDs(ids []modulecore.ArtifactID) {
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
}

type recoveryStubEventStore struct {
	stored      map[modulecore.EventID]modulecore.EventEnvelope
	appendErr   error
	appendCalls int
}

func (s *recoveryStubEventStore) GetByID(_ context.Context, eventID modulecore.EventID) (modulecore.EventEnvelope, bool, error) {
	event, found := s.stored[eventID]
	return event, found, nil
}

func (s *recoveryStubEventStore) AppendSequenced(_ context.Context, event modulecore.EventEnvelope) (modulecore.EventEnvelope, error) {
	s.appendCalls++
	if s.appendErr != nil {
		return modulecore.EventEnvelope{}, s.appendErr
	}
	if s.stored == nil {
		s.stored = make(map[modulecore.EventID]modulecore.EventEnvelope)
	}
	if _, exists := s.stored[event.EventID]; exists {
		return modulecore.EventEnvelope{}, fmt.Errorf("duplicate event_id %q", event.EventID)
	}
	event.EventSeq = modulecore.EventSeq(len(s.stored) + 1)
	s.stored[event.EventID] = event
	return event, nil
}

func recoveryTestIntent(artifactSuffix, eventSuffix string) modulecore.EventEnvelope {
	return modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion,
		EventID:       modulecore.EventID("evt_00000000-0000-7000-8000-00000000000" + eventSuffix),
		TraceID:       modulecore.NewTraceID(),
		EventType:     "artifact.created",
		ComponentID:   "browsertrace",
		OccurredAt:    time.Now().UTC(),
		WorkstreamID:  modulecore.WorkstreamID("ws_00000000-0000-7000-8000-00000000000a"),
		TaskID:        modulecore.TaskID("tsk_00000000-0000-5000-8000-000000000001"),
		RunID:         modulecore.RunID("run_00000000-0000-5000-8000-000000000002"),
		ActorKind:     "agent",
		ActorID:       "mio",
		ArtifactID:    modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000" + artifactSuffix),
		Payload: map[string]any{
			"artifact_kind": "specification",
			"artifact_type": "observed_openapi",
			"content_hash":  "sha256:9d7f27d546c0c617052f474f4e0202c1409f8c0d9aea341714ef34219d956cd1",
		},
	}
}

func TestRecoverBrowserTraceAPICreationFactsDeliversEveryStoredFactOnce(t *testing.T) {
	store := &recoveryStubArtifactStore{intents: map[modulecore.ArtifactID]modulecore.EventEnvelope{}}
	for _, suffix := range []string{"3", "4"} {
		intent := recoveryTestIntent(suffix, suffix)
		store.intents[intent.ArtifactID] = intent
	}
	events := &recoveryStubEventStore{}

	if err := recoverBrowserTraceAPICreationFacts(context.Background(), store, events); err != nil {
		t.Fatalf("recover undelivered creation facts: %v", err)
	}
	if len(events.stored) != 2 || events.appendCalls != 2 {
		t.Fatalf("canonical store stored=%d appends=%d, want 2 and 2", len(events.stored), events.appendCalls)
	}
	for eventID, event := range events.stored {
		if event.EventSeq == 0 {
			t.Fatalf("creation fact %s was stored without a canonical sequence", eventID)
		}
	}

	// A second start must not append the same creation fact twice: it confirms against what
	// the canonical store already holds.
	appends := events.appendCalls
	if err := recoverBrowserTraceAPICreationFacts(context.Background(), store, events); err != nil {
		t.Fatalf("second recovery pass: %v", err)
	}
	if events.appendCalls != appends || len(events.stored) != 2 {
		t.Fatalf("second pass re-appended facts: appends=%d stored=%d", events.appendCalls, len(events.stored))
	}
}

func TestRecoverBrowserTraceAPICreationFactsReportsFailureAndKeepsFactsDurable(t *testing.T) {
	store := &recoveryStubArtifactStore{intents: map[modulecore.ArtifactID]modulecore.EventEnvelope{}}
	intent := recoveryTestIntent("3", "3")
	store.intents[intent.ArtifactID] = intent
	events := &recoveryStubEventStore{appendErr: fmt.Errorf("canonical event store unavailable")}

	err := recoverBrowserTraceAPICreationFacts(context.Background(), store, events)
	if err == nil {
		t.Fatal("an undeliverable creation fact reported a clean recovery pass")
	}
	if len(store.intents) != 1 {
		t.Fatalf("a failed pass removed the durable fact: intents=%d", len(store.intents))
	}
	if len(events.stored) != 0 {
		t.Fatalf("failed pass left a partially written event: %#v", events.stored)
	}

	// The fact is still there, so once the canonical store answers the next pass delivers it.
	events.appendErr = nil
	if err := recoverBrowserTraceAPICreationFacts(context.Background(), store, events); err != nil {
		t.Fatalf("retry after a failed pass: %v", err)
	}
	if len(events.stored) != 1 {
		t.Fatalf("retry delivered %d events, want 1", len(events.stored))
	}
}
