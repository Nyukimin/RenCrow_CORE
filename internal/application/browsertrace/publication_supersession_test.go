package browsertrace

import (
	"context"
	"errors"
	"testing"
	"time"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	artifactstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/browsertrace"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Supersession fixtures. The pair shares one task, run, actor, workstream, content role and
// kind, because that is the only scope a supersede may join, and each row keeps its own
// body digest so the fact has two distinct digests to name. Both owners are the real stores
// on real temporary files, and the canonical event store is the real sequence-assigning one.

const supersessionFixtureSuccessorContent = "openapi: 3.1.0\ninfo:\n  title: publication route fixture revised\n"

func supersessionFixturePair(t *testing.T) (domaintrace.APIArtifact, domaintrace.APIArtifact) {
	t.Helper()
	predecessor := publicationFixtureArtifact(t)
	successor := publicationFixtureArtifact(t)
	successor.TaskID = predecessor.TaskID
	successor.RunID = predecessor.RunID
	successor.ActorID = predecessor.ActorID
	successor.WorkstreamID = predecessor.WorkstreamID
	successor.Kind = predecessor.Kind
	successor.Type = predecessor.Type
	successor.Title = "Observed OpenAPI Revision"
	successor.Content = supersessionFixtureSuccessorContent
	successor.ContentHash = modulecore.ContentHashOf([]byte(supersessionFixtureSuccessorContent))
	if err := domaintrace.ValidateAPIArtifact(successor); err != nil {
		t.Fatalf("supersession fixture successor %s is invalid before any operation: %v", successor.ArtifactID, err)
	}
	if err := domaintrace.ValidateAPIArtifactScope(predecessor, successor); err != nil {
		t.Fatalf("supersession fixture pair does not share one scope: %v", err)
	}
	return predecessor, successor
}

// createSupersessionEdge reaches the state a successful supersede leaves behind: both rows
// durable with their creation facts, the edge stored, and the supersession fact durable
// beside it in the artifact owner. It does not publish anything, which is what the
// publication tests below have to decide.
func createSupersessionEdge(t *testing.T, stores publicationStores) (domaintrace.APIArtifact, domaintrace.APIArtifact, modulecore.EventEnvelope) {
	t.Helper()
	ctx := context.Background()
	predecessor, successor := supersessionFixturePair(t)
	for _, item := range []domaintrace.APIArtifact{predecessor, successor} {
		intent, err := NewAPIArtifactCreationIntent(item, modulecore.NewTraceID(), item.CreatedAt.Add(time.Second))
		if err != nil {
			t.Fatalf("mint creation fact for %s: %v", item.ArtifactID, err)
		}
		if err := stores.artifacts.CreateAPIArtifactWithPublicationIntent(ctx, item, intent); err != nil {
			t.Fatalf("create artifact %s with creation fact %s: %v", item.ArtifactID, intent.EventID, err)
		}
	}
	fact, err := NewAPIArtifactSupersessionFact(predecessor, successor, modulecore.NewTraceID(), predecessor.CreatedAt.Add(2*time.Second))
	if err != nil {
		t.Fatalf("mint supersession fact of %s -> %s: %v", predecessor.ArtifactID, successor.ArtifactID, err)
	}
	if err := stores.artifacts.SupersedeAPIArtifactWithPublicationIntent(ctx, predecessor.ArtifactID, successor.ArtifactID, fact); err != nil {
		t.Fatalf("supersede %s with fact %s: %v", predecessor.ArtifactID, fact.EventID, err)
	}
	return predecessor, successor, fact
}

// publishedSupersessionFacts reads back the supersession events the canonical store holds,
// which is how these tests separate an acknowledged append from a durable event. Creation
// facts share the component, so the type filter keeps the two decisions from counting
// each other.
func publishedSupersessionFacts(t *testing.T, events eventStoreReader) []modulecore.EventEnvelope {
	t.Helper()
	stored, err := events.ListByComponent(context.Background(), domaintrace.APIArtifactPublicationComponentID, 100)
	if err != nil {
		t.Fatalf("list canonical supersession facts: %v", err)
	}
	filtered := make([]modulecore.EventEnvelope, 0, len(stored))
	for _, event := range stored {
		if event.EventType == domaintrace.APIArtifactSupersededEventType {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

// eventStoreReader is the read side the helpers above need. The real canonical store
// satisfies it, so no test double pretends to be the event owner here.
type eventStoreReader interface {
	ListByComponent(ctx context.Context, componentID string, limit int) ([]modulecore.EventEnvelope, error)
}

func TestNewAPIArtifactSupersessionFactMintsAFreshCanonicalSupersessionFact(t *testing.T) {
	predecessor, successor := supersessionFixturePair(t)
	occurredAt := predecessor.CreatedAt.Add(90 * time.Second)

	fact, err := NewAPIArtifactSupersessionFact(predecessor, successor, modulecore.NewTraceID(), occurredAt)
	if err != nil {
		t.Fatalf("mint supersession fact: %v", err)
	}
	if fact.EventSeq != 0 {
		t.Errorf("minted supersession fact %s carries event_seq %d: the canonical event store owns sequences", fact.EventID, fact.EventSeq)
	}
	if err := fact.EventID.Validate(); err != nil {
		t.Errorf("minted supersession event id: %v", err)
	}
	if fact.EventType != domaintrace.APIArtifactSupersededEventType || fact.ComponentID != domaintrace.APIArtifactPublicationComponentID {
		t.Errorf("minted supersession fact uses type %q component %q, want %q and %q",
			fact.EventType, fact.ComponentID, domaintrace.APIArtifactSupersededEventType, domaintrace.APIArtifactPublicationComponentID)
	}
	if fact.ArtifactID != predecessor.ArtifactID || fact.TaskID != predecessor.TaskID || fact.RunID != predecessor.RunID ||
		fact.ActorID != predecessor.ActorID || fact.WorkstreamID != modulecore.WorkstreamID(predecessor.WorkstreamID) {
		t.Errorf("minted supersession fact %s does not carry the superseded artifact identity: %+v", fact.EventID, fact)
	}
	if fact.ActorKind != domaintrace.APIArtifactPublicationActorKind {
		t.Errorf("minted supersession fact actor_kind is %q, want %q", fact.ActorKind, domaintrace.APIArtifactPublicationActorKind)
	}
	if !fact.OccurredAt.Equal(occurredAt.UTC()) {
		t.Errorf("minted supersession fact occurred_at = %v, want the mint time %v", fact.OccurredAt, occurredAt.UTC())
	}
	if len(fact.Payload) != 3 {
		t.Errorf("minted supersession fact payload has %d fields, want the three supersession fields: %v", len(fact.Payload), fact.Payload)
	}
	if fact.Payload[domaintrace.APIArtifactSupersessionPayloadSupersededBy] != string(successor.ArtifactID) {
		t.Errorf("payload superseded_by = %v, want %s", fact.Payload[domaintrace.APIArtifactSupersessionPayloadSupersededBy], successor.ArtifactID)
	}
	if fact.Payload[domaintrace.APIArtifactSupersessionPayloadPredecessorContentHash] != predecessor.ContentHash {
		t.Errorf("payload predecessor_content_hash = %v, want the superseded digest %s",
			fact.Payload[domaintrace.APIArtifactSupersessionPayloadPredecessorContentHash], predecessor.ContentHash)
	}
	if fact.Payload[domaintrace.APIArtifactSupersessionPayloadSuccessorContentHash] != successor.ContentHash {
		t.Errorf("payload successor_content_hash = %v, want the successor digest %s",
			fact.Payload[domaintrace.APIArtifactSupersessionPayloadSuccessorContentHash], successor.ContentHash)
	}

	// Repeating a supersede request mints a new fact instead of recovering an old event id:
	// the route has no request-id idempotency contract, so nothing may dedup here.
	second, err := NewAPIArtifactSupersessionFact(predecessor, successor, fact.TraceID, occurredAt.Add(time.Second))
	if err != nil {
		t.Fatalf("second mint: %v", err)
	}
	if second.EventID == fact.EventID {
		t.Errorf("two mints of the same edge both produced event id %s", second.EventID)
	}

	// An artifact that already moved cannot be superseded a second time, and a successor
	// from another workstream is not the same artifact line.
	edged := predecessor
	edged.SupersededBy = modulecore.NewArtifactID()
	if _, err := NewAPIArtifactSupersessionFact(edged, successor, modulecore.NewTraceID(), occurredAt); err == nil {
		t.Errorf("minted a supersession fact for artifact %s that already carries superseded_by %s", edged.ArtifactID, edged.SupersededBy)
	}
	foreign := successor
	foreign.WorkstreamID = string(modulecore.NewWorkstreamID())
	if _, err := NewAPIArtifactSupersessionFact(predecessor, foreign, modulecore.NewTraceID(), occurredAt); err == nil {
		t.Errorf("minted a supersession fact joining %s to successor %s in another workstream", predecessor.ArtifactID, foreign.ArtifactID)
	}
}

func TestPublishAPIArtifactSupersessionFactsAppendsOnceAndRerunsConfirm(t *testing.T) {
	ctx := context.Background()
	stores := newPublicationStores(t)
	_, _, fact := createSupersessionEdge(t, stores)

	report, err := PublishAPIArtifactSupersessionFacts(ctx, stores.events, fact)
	if err != nil {
		t.Fatalf("publish supersession fact: %v (report %+v)", err, report)
	}
	if report.Scanned != 1 || report.Published != 1 || report.Confirmed != 0 || report.Failed != 0 {
		t.Errorf("first publication report = %+v, want 1 scanned and 1 published", report)
	}
	stored, found, err := stores.events.GetByID(ctx, fact.EventID)
	if err != nil || !found {
		t.Fatalf("supersession fact %s not readable from the canonical event store: found=%v err=%v", fact.EventID, found, err)
	}
	if stored.EventSeq <= 0 {
		t.Errorf("supersession fact %s stored with event_seq %d; the canonical owner must assign a live sequence", fact.EventID, stored.EventSeq)
	}
	if err := compareAPISupersessionFact(fact, stored); err != nil {
		t.Errorf("stored supersession fact %s differs from the durable fact: %v", fact.EventID, err)
	}

	// A rerun, and a restart that reopens the very same files, reach the same decision from
	// the canonical store's own answer: the fact is delivered, so nothing is appended twice.
	rerun, err := PublishPendingAPIArtifactSupersessionFacts(ctx, stores.artifacts, stores.events)
	if err != nil {
		t.Fatalf("rerun recovery: %v (report %+v)", err, rerun)
	}
	if rerun.Scanned != 1 || rerun.Published != 0 || rerun.Confirmed != 1 || rerun.Failed != 0 {
		t.Errorf("rerun recovery report = %+v, want 1 scanned, 1 confirmed, 0 published", rerun)
	}

	reopenedArtifacts := mustOpenArtifactStore(t, stores.artifactDir)
	defer reopenedArtifacts.Close()
	reopenedEvents := mustOpenEventStore(t, stores.eventPath)
	defer reopenedEvents.Close()
	restarted, err := PublishPendingAPIArtifactSupersessionFacts(ctx, reopenedArtifacts, reopenedEvents)
	if err != nil {
		t.Fatalf("restart recovery: %v (report %+v)", err, restarted)
	}
	if restarted.Scanned != 1 || restarted.Published != 0 || restarted.Confirmed != 1 || restarted.Failed != 0 {
		t.Errorf("restart recovery report = %+v, want 1 scanned, 1 confirmed, 0 published", restarted)
	}
	if held := publishedSupersessionFacts(t, stores.events); len(held) != 1 {
		t.Errorf("canonical event store holds %d browsertrace supersession facts after two recovery passes, want 1: %v",
			len(held), eventIDList(held))
	}
}

func TestPublishAPIArtifactSupersessionFactsReportsConflictAndWritesNothing(t *testing.T) {
	ctx := context.Background()
	stores := newPublicationStores(t)
	predecessor, _, fact := createSupersessionEdge(t, stores)

	// Another writer already used this event id for a different fact. That event, not the
	// supersession fact, is what the canonical store holds, and neither side may move.
	foreign := fact
	foreign.EventType = domaintrace.APIArtifactCreatedEventType
	foreign.Payload = map[string]any{
		domaintrace.APIArtifactPublicationPayloadArtifactKind: string(predecessor.Kind),
		domaintrace.APIArtifactPublicationPayloadArtifactType: predecessor.Type,
		domaintrace.APIArtifactPublicationPayloadContentHash:  modulecore.ContentHashOf([]byte("competing body")),
	}
	appended, err := stores.events.AppendSequenced(ctx, foreign)
	if err != nil {
		t.Fatalf("append competing event under id %s: %v", fact.EventID, err)
	}

	report, err := PublishAPIArtifactSupersessionFacts(ctx, stores.events, fact)
	if !errors.Is(err, ErrAPIArtifactPublicationConflict) {
		t.Fatalf("publish with a competing event id did not report ErrAPIArtifactPublicationConflict: %v", err)
	}
	if report.Scanned != 1 || report.Published != 0 || report.Confirmed != 0 || report.Failed != 1 {
		t.Errorf("conflict report = %+v, want 1 scanned and 1 failed", report)
	}
	stored, found, err := stores.events.GetByID(ctx, fact.EventID)
	if err != nil || !found {
		t.Fatalf("read back competing event: found=%v err=%v", found, err)
	}
	if stored.EventSeq != appended.EventSeq || stored.EventType != appended.EventType {
		t.Errorf("conflicting publish changed the canonical event to type %q seq %d, want the competing type %q seq %d",
			stored.EventType, stored.EventSeq, appended.EventType, appended.EventSeq)
	}

	// The undelivered fact is still durable in the artifact owner, so a later pass still sees
	// the edge it describes and can retry it.
	pending, err := stores.artifacts.ListAPIArtifactSupersessionFacts(ctx, "", 10)
	if err != nil || len(pending) != 1 {
		t.Errorf("artifact store supersession facts after conflict = %d (err %v); an undelivered fact has to stay durable", len(pending), err)
	}
}

func TestPublishPendingAPIArtifactSupersessionFactsDrainsEveryPage(t *testing.T) {
	ctx := context.Background()
	stores := newPublicationStores(t)
	edges := make([]modulecore.EventEnvelope, 0, 5)
	for range 5 {
		_, _, fact := createSupersessionEdge(t, stores)
		edges = append(edges, fact)
	}

	// The drain asks the store for its own default page. This wrapper lowers that page to two
	// facts so the walk across page boundaries is observed instead of assumed; it changes no
	// production bound and no source of truth.
	source := &pagedSupersessionSource{inner: stores.artifacts, pageSize: 2}
	report, err := PublishPendingAPIArtifactSupersessionFacts(ctx, source, stores.events)
	if err != nil {
		t.Fatalf("drain every supersession fact: %v (report %+v)", err, report)
	}
	if report.Scanned != 5 || report.Published != 5 || report.Failed != 0 {
		t.Errorf("drain report = %+v, want 5 scanned and 5 published", report)
	}
	if source.pages < 4 {
		t.Errorf("drain visited %d pages of 2 facts covering 5 facts, want at least 4 so the walk past the first page is proven", source.pages)
	}
	if source.largestPage > 2 {
		t.Errorf("drain asked for a page of %d, above the bounded page this wrapper enforces", source.largestPage)
	}

	held := publishedSupersessionFacts(t, stores.events)
	if len(held) != 5 {
		t.Errorf("canonical event store holds %d of the 5 supersession facts: %v", len(held), eventIDList(held))
	}
	for _, fact := range edges {
		if _, found, err := stores.events.GetByID(ctx, fact.EventID); err != nil || !found {
			t.Errorf("supersession fact %s missing after drain: found=%v err=%v", fact.EventID, found, err)
		}
	}
}

// pagedSupersessionSource bounds the page the supersession drain sees and counts the pages it
// had to walk. It is a test-side observation point, not a production bound.
type pagedSupersessionSource struct {
	inner       APIArtifactSupersessionSource
	pageSize    int
	pages       int
	largestPage int
}

func (s *pagedSupersessionSource) ListAPIArtifactSupersessionFacts(ctx context.Context, after modulecore.ArtifactID, limit int) ([]modulecore.EventEnvelope, error) {
	asked := s.pageSize
	if limit > 0 && limit < asked {
		asked = limit
	}
	if asked > s.largestPage {
		s.largestPage = asked
	}
	s.pages++
	return s.inner.ListAPIArtifactSupersessionFacts(ctx, after, asked)
}

func TestPublishAPIArtifactSupersessionFactsKeepsUndeliveredFactsRetryable(t *testing.T) {
	ctx := context.Background()
	stores := newPublicationStores(t)
	_, _, fact := createSupersessionEdge(t, stores)

	failing := &failingPublicationPublisher{inner: stores.events, appendErr: errors.New("canonical event store unavailable")}
	report, err := PublishAPIArtifactSupersessionFacts(ctx, failing, fact)
	if err == nil {
		t.Fatalf("publish through a failing canonical store reported no error, although it published %d of 1 facts", report.Published)
	}
	if report.Published != 0 || report.Failed != 1 {
		t.Errorf("failed publish report = %+v, want 1 failed and 0 published", report)
	}
	if _, found, err := stores.events.GetByID(ctx, fact.EventID); err != nil || found {
		t.Errorf("a failed append left an event under id %s in the canonical store: found=%v err=%v", fact.EventID, found, err)
	}
	if _, err := stores.artifacts.ListAPIArtifactSupersessionFacts(ctx, "", 10); err != nil {
		t.Fatalf("artifact store supersession facts unreadable after a failed delivery: %v", err)
	}

	retried, err := PublishPendingAPIArtifactSupersessionFacts(ctx, stores.artifacts, stores.events)
	if err != nil {
		t.Fatalf("retry after delivery failure: %v (report %+v)", err, retried)
	}
	if retried.Published != 1 || retried.Failed != 0 {
		t.Errorf("retry report = %+v, want 1 published and 0 failed", retried)
	}
	confirmed, err := PublishPendingAPIArtifactSupersessionFacts(ctx, stores.artifacts, stores.events)
	if err != nil {
		t.Fatalf("confirm after retry: %v (report %+v)", err, confirmed)
	}
	if confirmed.Published != 0 || confirmed.Confirmed != 1 {
		t.Errorf("confirm report = %+v, want 1 confirmed and 0 published", confirmed)
	}
	if held := publishedSupersessionFacts(t, stores.events); len(held) != 1 {
		t.Errorf("canonical event store holds %d events after one failure and two retries, want 1: %v",
			len(held), eventIDList(held))
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled, err := PublishPendingAPIArtifactSupersessionFacts(canceled, stores.artifacts, stores.events)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("drain with a canceled context did not report context.Canceled: %v", err)
	}
	if cancelled.Published != 0 || cancelled.Failed != 0 {
		t.Errorf("canceled drain reported %+v before any delivery", cancelled)
	}
}

func TestPublishAPIArtifactSupersessionFactsTreatsAWonRaceAsConfirmation(t *testing.T) {
	ctx := context.Background()
	stores := newPublicationStores(t)
	_, _, fact := createSupersessionEdge(t, stores)

	persisted, err := stores.events.AppendSequenced(ctx, fact)
	if err != nil {
		t.Fatalf("append the winning fact directly: %v", err)
	}

	// The wrapper hides the stored event from the first lookup and then refuses the append the
	// way a competing writer does, so the decision has to come from reading the canonical
	// store again: that is a confirmation, not a failure and not a second append.
	racing := &racingPublicationPublisher{inner: stores.events, persisted: persisted}
	report, err := PublishAPIArtifactSupersessionFacts(ctx, racing, fact)
	if err != nil {
		t.Fatalf("publish against a won race: %v (report %+v)", err, report)
	}
	if report.Published != 0 || report.Confirmed != 1 || report.Failed != 0 {
		t.Errorf("won-race report = %+v, want 1 confirmed, 0 published, 0 failed", report)
	}
	if racing.appendCalls != 1 {
		t.Errorf("won-race publish made %d append calls, want exactly 1", racing.appendCalls)
	}
	if stored, found, err := stores.events.GetByID(ctx, fact.EventID); err != nil || !found || stored.EventSeq != persisted.EventSeq {
		t.Errorf("canonical event changed under a won race: found=%v seq=%d err=%v", found, stored.EventSeq, err)
	}
}

// TestPublishPendingAPIArtifactSupersessionFactsDrainsTheJSONLOwner runs the same drain
// against the other accepted artifact owner, so the publication path is proven against both
// durable stores and not only the one it was written next to.
func TestPublishPendingAPIArtifactSupersessionFactsDrainsTheJSONLOwner(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	artifacts, err := artifactstore.NewJSONLStore(root)
	if err != nil {
		t.Fatalf("open jsonl artifact store in %s: %v", root, err)
	}
	// The JSONL owner holds no open file handle: the jsonlbatch store opens and
	// locks its files per operation, so there is nothing to close here.
	events := mustOpenEventStore(t, t.TempDir()+"/canonical_events.db")
	defer events.Close()

	edges := make([]modulecore.EventEnvelope, 0, 3)
	for range 3 {
		predecessor, successor := supersessionFixturePair(t)
		for _, item := range []domaintrace.APIArtifact{predecessor, successor} {
			intent, err := NewAPIArtifactCreationIntent(item, modulecore.NewTraceID(), item.CreatedAt.Add(time.Second))
			if err != nil {
				t.Fatalf("mint creation fact for %s: %v", item.ArtifactID, err)
			}
			if err := artifacts.CreateAPIArtifactWithPublicationIntent(ctx, item, intent); err != nil {
				t.Fatalf("create artifact %s with creation fact %s: %v", item.ArtifactID, intent.EventID, err)
			}
		}
		fact, err := NewAPIArtifactSupersessionFact(predecessor, successor, modulecore.NewTraceID(), predecessor.CreatedAt.Add(2*time.Second))
		if err != nil {
			t.Fatalf("mint supersession fact: %v", err)
		}
		if err := artifacts.SupersedeAPIArtifactWithPublicationIntent(ctx, predecessor.ArtifactID, successor.ArtifactID, fact); err != nil {
			t.Fatalf("supersede %s with fact %s in the jsonl owner: %v", predecessor.ArtifactID, fact.EventID, err)
		}
		edges = append(edges, fact)
	}

	source := &pagedSupersessionSource{inner: artifacts, pageSize: 2}
	report, err := PublishPendingAPIArtifactSupersessionFacts(ctx, source, events)
	if err != nil {
		t.Fatalf("drain the jsonl owner: %v (report %+v)", err, report)
	}
	if report.Scanned != 3 || report.Published != 3 || report.Failed != 0 {
		t.Errorf("jsonl drain report = %+v, want 3 scanned and 3 published", report)
	}
	if held := publishedSupersessionFacts(t, events); len(held) != 3 {
		t.Errorf("canonical event store holds %d of the 3 jsonl supersession facts: %v", len(held), eventIDList(held))
	}

	// The same decision after a restart of the artifact owner: every fact is already delivered.
	reopened, err := artifactstore.NewJSONLStore(root)
	if err != nil {
		t.Fatalf("reopen jsonl artifact store in %s: %v", root, err)
	}
	restarted, err := PublishPendingAPIArtifactSupersessionFacts(ctx, reopened, events)
	if err != nil {
		t.Fatalf("jsonl restart recovery: %v (report %+v)", err, restarted)
	}
	if restarted.Scanned != 3 || restarted.Published != 0 || restarted.Confirmed != 3 {
		t.Errorf("jsonl restart recovery report = %+v, want 3 scanned and 3 confirmed", restarted)
	}
}
