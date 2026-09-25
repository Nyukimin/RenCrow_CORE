package browsertrace

import (
	"context"
	"errors"
	"testing"
	"time"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	artifactstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/browsertrace"
	eventstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Publication fixtures. Both sides are the real owners opened on real temporary files: the
// artifact store that keeps a creation intent next to the row it created, and the one
// canonical event store that assigns sequences. Nothing here stands in for either owner.

const publicationFixtureContent = "openapi: 3.1.0\ninfo:\n  title: publication route fixture\n"

func publicationFixtureArtifact(t *testing.T) domaintrace.APIArtifact {
	t.Helper()
	item := domaintrace.APIArtifact{
		ArtifactID:   modulecore.NewArtifactID(),
		Kind:         modulecore.ArtifactKindSpecification,
		TaskID:       modulecore.NewTaskID(),
		RunID:        modulecore.NewRunID(),
		ActorID:      "mio",
		WorkstreamID: string(modulecore.NewWorkstreamID()),
		Type:         domaintrace.APIArtifactTypeObservedOpenAPI,
		Title:        "Observed OpenAPI Draft",
		Status:       "generated",
		Content:      publicationFixtureContent,
		ContentHash:  modulecore.ContentHashOf([]byte(publicationFixtureContent)),
		CreatedAt:    time.Date(2026, 9, 22, 2, 0, 0, 0, time.UTC),
	}
	if err := domaintrace.ValidateAPIArtifact(item); err != nil {
		t.Fatalf("publication fixture artifact %s is invalid before any operation: %v", item.ArtifactID, err)
	}
	return item
}

// publicationFixturePair returns one valid artifact plus the creation fact the route under
// test would mint for it.
func publicationFixturePair(t *testing.T) (domaintrace.APIArtifact, modulecore.EventEnvelope) {
	t.Helper()
	item := publicationFixtureArtifact(t)
	intent, err := NewAPIArtifactCreationIntent(item, modulecore.NewTraceID(), time.Date(2026, 9, 22, 2, 0, 1, 0, time.UTC))
	if err != nil {
		t.Fatalf("mint creation fact for fixture %s: %v", item.ArtifactID, err)
	}
	return item, intent
}

// publicationStores is the pair of durable owners the publication path needs, with the
// paths kept so a test can reopen the very same files the way a restart does.
type publicationStores struct {
	artifacts   *artifactstore.SQLiteStore
	events      *eventstore.SQLiteStore
	artifactDir string
	eventPath   string
}

func newPublicationStores(t *testing.T) publicationStores {
	t.Helper()
	stores := publicationStores{
		artifactDir: t.TempDir(),
		eventPath:   t.TempDir() + "/canonical_events.db",
	}
	stores.artifacts = mustOpenArtifactStore(t, stores.artifactDir)
	stores.events = mustOpenEventStore(t, stores.eventPath)
	t.Cleanup(func() {
		if err := stores.artifacts.Close(); err != nil {
			t.Errorf("close artifact store: %v", err)
		}
		if err := stores.events.Close(); err != nil {
			t.Errorf("close canonical event store: %v", err)
		}
	})
	return stores
}

func mustOpenArtifactStore(t *testing.T, dir string) *artifactstore.SQLiteStore {
	t.Helper()
	store, err := artifactstore.NewSQLiteStore(dir + "/api_artifact.db")
	if err != nil {
		t.Fatalf("open artifact store in %s: %v", dir, err)
	}
	return store
}

func mustOpenEventStore(t *testing.T, path string) *eventstore.SQLiteStore {
	t.Helper()
	store, err := eventstore.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("open canonical event store %s: %v", path, err)
	}
	return store
}

// createPublicationPair durably stores one artifact and its creation fact through the
// artifact owner's atomic create, which is the state the route reaches on success.
func createPublicationPair(t *testing.T, stores publicationStores) (domaintrace.APIArtifact, modulecore.EventEnvelope) {
	t.Helper()
	item, intent := publicationFixturePair(t)
	if err := stores.artifacts.CreateAPIArtifactWithPublicationIntent(context.Background(), item, intent); err != nil {
		t.Fatalf("create artifact %s with publication intent %s: %v", item.ArtifactID, intent.EventID, err)
	}
	return item, intent
}

// publishedCreationFacts reads back every creation fact the canonical store holds for the
// component, which is how these tests separate an acknowledged append from a durable event.
func publishedCreationFacts(t *testing.T, events *eventstore.SQLiteStore) []modulecore.EventEnvelope {
	t.Helper()
	stored, err := events.ListByComponent(context.Background(), domaintrace.APIArtifactPublicationComponentID, 100)
	if err != nil {
		t.Fatalf("list canonical creation facts: %v", err)
	}
	return stored
}

func eventIDList(events []modulecore.EventEnvelope) []modulecore.EventID {
	ids := make([]modulecore.EventID, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.EventID)
	}
	return ids
}

func TestNewAPIArtifactCreationIntentMintsAFreshCanonicalCreationFact(t *testing.T) {
	item, intent := publicationFixturePair(t)

	if intent.EventSeq != 0 {
		t.Errorf("minted creation fact %s carries event_seq %d: the canonical event store owns sequences", intent.EventID, intent.EventSeq)
	}
	if err := intent.EventID.Validate(); err != nil {
		t.Errorf("minted event id: %v", err)
	}
	if intent.EventType != domaintrace.APIArtifactCreatedEventType || intent.ComponentID != domaintrace.APIArtifactPublicationComponentID {
		t.Errorf("minted creation fact uses type %q component %q, want %q and %q",
			intent.EventType, intent.ComponentID, domaintrace.APIArtifactCreatedEventType, domaintrace.APIArtifactPublicationComponentID)
	}
	if intent.ArtifactID != item.ArtifactID || intent.TaskID != item.TaskID || intent.RunID != item.RunID ||
		intent.ActorID != item.ActorID || intent.WorkstreamID != modulecore.WorkstreamID(item.WorkstreamID) {
		t.Errorf("minted creation fact %s does not carry the artifact identity: %+v", intent.EventID, intent)
	}
	if intent.ActorKind != domaintrace.APIArtifactPublicationActorKind {
		t.Errorf("minted creation fact actor_kind is %q, want %q", intent.ActorKind, domaintrace.APIArtifactPublicationActorKind)
	}
	if len(intent.Payload) != 3 {
		t.Errorf("minted creation fact payload has %d fields, want the three content-role fields: %v", len(intent.Payload), intent.Payload)
	}

	// Repeating a request mints a new fact instead of recovering an old event id: the
	// discover route has no request-id idempotency contract, so nothing may dedup here.
	second, err := NewAPIArtifactCreationIntent(item, intent.TraceID, time.Now().UTC())
	if err != nil {
		t.Fatalf("second mint: %v", err)
	}
	if second.EventID == intent.EventID {
		t.Errorf("two mints of the same artifact both produced event id %s", second.EventID)
	}

	edge := publicationFixtureArtifact(t)
	edge.SupersededBy = modulecore.NewArtifactID()
	edgeIntent, err := NewAPIArtifactCreationIntent(edge, modulecore.NewTraceID(), time.Now().UTC())
	if err == nil {
		t.Errorf("an artifact created with superseded_by %s was minted creation fact %s; only Supersede may set an edge",
			edge.SupersededBy, edgeIntent.EventID)
	}
}

func TestPublishAPIArtifactCreationIntentsAppendsOnceAndRerunsConfirm(t *testing.T) {
	ctx := context.Background()
	stores := newPublicationStores(t)
	created := make([]modulecore.EventEnvelope, 0, 3)
	for range 3 {
		_, intent := createPublicationPair(t, stores)
		created = append(created, intent)
	}

	report, err := PublishAPIArtifactCreationIntents(ctx, stores.events, created...)
	if err != nil {
		t.Fatalf("publish creation facts: %v (report %+v)", err, report)
	}
	if report.Scanned != 3 || report.Published != 3 || report.Confirmed != 0 || report.Failed != 0 {
		t.Errorf("first publication report = %+v, want 3 scanned and 3 published", report)
	}
	for _, intent := range created {
		stored, found, err := stores.events.GetByID(ctx, intent.EventID)
		if err != nil || !found {
			t.Fatalf("creation fact %s not readable from the canonical event store: found=%v err=%v", intent.EventID, found, err)
		}
		if stored.EventSeq <= 0 {
			t.Errorf("creation fact %s stored with event_seq %d; the canonical owner must assign a live sequence", intent.EventID, stored.EventSeq)
		}
		if compareErr := compareAPICreationFact(intent, stored); compareErr != nil {
			t.Errorf("stored creation fact %s differs from the persisted intent: %v", intent.EventID, compareErr)
		}
	}

	// A rerun, and a restart that reopens the very same files, reach the same decision:
	// every intent is already delivered, so nothing gets appended a second time.
	rerun, err := PublishPendingAPIArtifactCreationFacts(ctx, stores.artifacts, stores.events)
	if err != nil {
		t.Fatalf("rerun recovery: %v (report %+v)", err, rerun)
	}
	if rerun.Scanned != 3 || rerun.Published != 0 || rerun.Confirmed != 3 || rerun.Failed != 0 {
		t.Errorf("rerun recovery report = %+v, want 3 scanned, 3 confirmed, 0 published", rerun)
	}

	reopenedArtifacts := mustOpenArtifactStore(t, stores.artifactDir)
	defer reopenedArtifacts.Close()
	reopenedEvents := mustOpenEventStore(t, stores.eventPath)
	defer reopenedEvents.Close()
	restarted, err := PublishPendingAPIArtifactCreationFacts(ctx, reopenedArtifacts, reopenedEvents)
	if err != nil {
		t.Fatalf("restart recovery: %v (report %+v)", err, restarted)
	}
	if restarted.Scanned != 3 || restarted.Published != 0 || restarted.Confirmed != 3 || restarted.Failed != 0 {
		t.Errorf("restart recovery report = %+v, want 3 scanned, 3 confirmed, 0 published", restarted)
	}

	if storedEvents := publishedCreationFacts(t, stores.events); len(storedEvents) != 3 {
		t.Errorf("canonical event store holds %d browsertrace creation facts after two recovery passes, want 3: %v",
			len(storedEvents), eventIDList(storedEvents))
	}
}

func TestPublishAPIArtifactCreationIntentsReportsConflictAndWritesNothing(t *testing.T) {
	ctx := context.Background()
	stores := newPublicationStores(t)
	_, intent := createPublicationPair(t, stores)

	// Another writer already used this event id for a different fact. That event, not the
	// intent, is what the canonical store holds, and neither side may be overwritten.
	foreign := intent
	foreign.EventType = "artifact.superseded"
	foreign.Payload = map[string]any{
		domaintrace.APIArtifactPublicationPayloadArtifactKind: string(modulecore.ArtifactKindReport),
		domaintrace.APIArtifactPublicationPayloadArtifactType: domaintrace.APIArtifactTypeRiskAssessment,
		domaintrace.APIArtifactPublicationPayloadContentHash:  modulecore.ContentHashOf([]byte("competing body")),
	}
	appended, err := stores.events.AppendSequenced(ctx, foreign)
	if err != nil {
		t.Fatalf("append competing event under id %s: %v", foreign.EventID, err)
	}

	report, err := PublishAPIArtifactCreationIntents(ctx, stores.events, intent)
	if !errors.Is(err, ErrAPIArtifactPublicationConflict) {
		t.Fatalf("publish with a competing event id did not report ErrAPIArtifactPublicationConflict: %v", err)
	}
	if report.Scanned != 1 || report.Published != 0 || report.Confirmed != 0 || report.Failed != 1 {
		t.Errorf("conflict report = %+v, want 1 scanned and 1 failed", report)
	}

	stored, found, err := stores.events.GetByID(ctx, intent.EventID)
	if err != nil || !found {
		t.Fatalf("read back competing event: found=%v err=%v", found, err)
	}
	if stored.EventSeq != appended.EventSeq || stored.EventType != appended.EventType {
		t.Errorf("conflicting publish changed the canonical event to type %q seq %d, want the competing type %q seq %d",
			stored.EventType, stored.EventSeq, appended.EventType, appended.EventSeq)
	}
	intents, err := stores.artifacts.ListAPIArtifactPublicationIntents(ctx, "", 10)
	if err != nil || len(intents) != 1 {
		t.Errorf("artifact store intents after conflict = %d (err %v); an undelivered fact has to stay durable for a later retry", len(intents), err)
	}
}

func TestPublishPendingAPIArtifactCreationFactsDrainsEveryPage(t *testing.T) {
	ctx := context.Background()
	stores := newPublicationStores(t)
	created := make([]modulecore.EventEnvelope, 0, 5)
	for range 5 {
		_, intent := createPublicationPair(t, stores)
		created = append(created, intent)
	}

	// The drain asks the store for its own default page. This wrapper lowers that page to
	// two entries so the walk across page boundaries is observed instead of assumed; it
	// changes no production bound and no source of truth.
	source := &pagedPublicationSource{inner: stores.artifacts, pageSize: 2}
	report, err := PublishPendingAPIArtifactCreationFacts(ctx, source, stores.events)
	if err != nil {
		t.Fatalf("drain every intent: %v (report %+v)", err, report)
	}
	if report.Scanned != 5 || report.Published != 5 || report.Failed != 0 {
		t.Errorf("drain report = %+v, want 5 scanned and 5 published", report)
	}
	if source.pages < 4 {
		t.Errorf("drain visited %d pages of 2 intents covering 5 intents, want at least 4 so the walk past the first page is proven", source.pages)
	}
	if source.largestPage > 2 {
		t.Errorf("drain asked for a page of %d, above the bounded page this wrapper enforces", source.largestPage)
	}

	storedEvents := publishedCreationFacts(t, stores.events)
	if len(storedEvents) != 5 {
		t.Errorf("canonical event store holds %d of the 5 created facts: %v", len(storedEvents), eventIDList(storedEvents))
	}
	for _, intent := range created {
		if _, found, err := stores.events.GetByID(ctx, intent.EventID); err != nil || !found {
			t.Errorf("creation fact %s missing after drain: found=%v err=%v", intent.EventID, found, err)
		}
	}
}

func TestPublishAPIArtifactCreationIntentsKeepsUndeliveredFactsRetryable(t *testing.T) {
	ctx := context.Background()
	stores := newPublicationStores(t)
	_, intent := createPublicationPair(t, stores)

	failing := &failingPublicationPublisher{inner: stores.events, appendErr: errors.New("canonical event store unavailable")}
	report, err := PublishAPIArtifactCreationIntents(ctx, failing, intent)
	if err == nil {
		t.Fatalf("publish through a failing canonical store reported no error, although it published %d of 1 facts", report.Published)
	}
	if report.Published != 0 || report.Failed != 1 {
		t.Errorf("failed publish report = %+v, want 1 failed and 0 published", report)
	}
	if _, found, err := stores.events.GetByID(ctx, intent.EventID); err != nil || found {
		t.Errorf("a failed append left an event under id %s in the canonical store: found=%v err=%v", intent.EventID, found, err)
	}
	if _, err := stores.artifacts.ListAPIArtifactPublicationIntents(ctx, "", 10); err != nil {
		t.Fatalf("artifact store intents unreadable after a failed delivery: %v", err)
	}

	// The undelivered fact stayed durable, so the next recovery pass delivers it and the
	// pass after that confirms it without appending a second event.
	retried, err := PublishPendingAPIArtifactCreationFacts(ctx, stores.artifacts, stores.events)
	if err != nil {
		t.Fatalf("retry after delivery failure: %v (report %+v)", err, retried)
	}
	if retried.Published != 1 || retried.Failed != 0 {
		t.Errorf("retry report = %+v, want 1 published and 0 failed", retried)
	}
	confirmed, err := PublishPendingAPIArtifactCreationFacts(ctx, stores.artifacts, stores.events)
	if err != nil {
		t.Fatalf("confirm after retry: %v (report %+v)", err, confirmed)
	}
	if confirmed.Published != 0 || confirmed.Confirmed != 1 {
		t.Errorf("confirm report = %+v, want 1 confirmed and 0 published", confirmed)
	}
	if storedEvents := publishedCreationFacts(t, stores.events); len(storedEvents) != 1 {
		t.Errorf("canonical event store holds %d events after one failure and two retries, want 1: %v",
			len(storedEvents), eventIDList(storedEvents))
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled, err := PublishPendingAPIArtifactCreationFacts(canceled, stores.artifacts, stores.events)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("drain with a canceled context did not report context.Canceled: %v", err)
	}
	if cancelled.Published != 0 || cancelled.Failed != 0 {
		t.Errorf("canceled drain reported %+v before any delivery", cancelled)
	}
}

func TestPublishAPIArtifactCreationIntentsTreatsAWonRaceAsConfirmation(t *testing.T) {
	ctx := context.Background()
	stores := newPublicationStores(t)
	_, intent := createPublicationPair(t, stores)

	persisted, err := stores.events.AppendSequenced(ctx, intent)
	if err != nil {
		t.Fatalf("append the winning fact directly: %v", err)
	}

	// The wrapper hides the stored event from the first lookup and then refuses the append
	// the way a competing writer does, so the decision has to come from reading the
	// canonical store again: that is a confirmation, not a failure and not a second append.
	racing := &racingPublicationPublisher{inner: stores.events, persisted: persisted}
	report, err := PublishAPIArtifactCreationIntents(ctx, racing, intent)
	if err != nil {
		t.Fatalf("publish against a won race: %v (report %+v)", err, report)
	}
	if report.Published != 0 || report.Confirmed != 1 || report.Failed != 0 {
		t.Errorf("won-race report = %+v, want 1 confirmed, 0 published, 0 failed", report)
	}
	if racing.appendCalls != 1 {
		t.Errorf("won-race publish made %d append calls, want exactly 1", racing.appendCalls)
	}
	if stored, found, err := stores.events.GetByID(ctx, intent.EventID); err != nil || !found || stored.EventSeq != persisted.EventSeq {
		t.Errorf("canonical event changed under a won race: found=%v seq=%d err=%v", found, stored.EventSeq, err)
	}
}

// pagedPublicationSource bounds the page the drain sees and counts the pages it had to
// walk. It is a test-side observation point, not a production bound.
type pagedPublicationSource struct {
	inner       APIArtifactPublicationSource
	pageSize    int
	pages       int
	largestPage int
}

func (s *pagedPublicationSource) ListAPIArtifactPublicationIntents(ctx context.Context, after modulecore.ArtifactID, limit int) ([]modulecore.EventEnvelope, error) {
	asked := s.pageSize
	if limit > 0 && limit < asked {
		asked = limit
	}
	if asked > s.largestPage {
		s.largestPage = asked
	}
	s.pages++
	return s.inner.ListAPIArtifactPublicationIntents(ctx, after, asked)
}

// failingPublicationPublisher refuses the append, so the durability of the undelivered
// intent is what gets tested while every lookup still reaches the real canonical store.
type failingPublicationPublisher struct {
	inner     APIArtifactCreationPublisher
	appendErr error
}

func (p *failingPublicationPublisher) GetByID(ctx context.Context, eventID modulecore.EventID) (modulecore.EventEnvelope, bool, error) {
	return p.inner.GetByID(ctx, eventID)
}

func (p *failingPublicationPublisher) AppendSequenced(ctx context.Context, event modulecore.EventEnvelope) (modulecore.EventEnvelope, error) {
	if p.appendErr != nil {
		return modulecore.EventEnvelope{}, p.appendErr
	}
	return p.inner.AppendSequenced(ctx, event)
}

// racingPublicationPublisher answers the first lookup as if nothing were stored, refuses
// the append the way the canonical owner refuses a duplicate event id, and answers the
// readback from the real store.
type racingPublicationPublisher struct {
	inner       APIArtifactCreationPublisher
	persisted   modulecore.EventEnvelope
	lookups     int
	appendCalls int
}

func (p *racingPublicationPublisher) GetByID(ctx context.Context, eventID modulecore.EventID) (modulecore.EventEnvelope, bool, error) {
	p.lookups++
	if p.lookups == 1 {
		return modulecore.EventEnvelope{}, false, nil
	}
	return p.inner.GetByID(ctx, eventID)
}

func (p *racingPublicationPublisher) AppendSequenced(ctx context.Context, event modulecore.EventEnvelope) (modulecore.EventEnvelope, error) {
	p.appendCalls++
	return modulecore.EventEnvelope{}, errors.New("duplicate event_id")
}
