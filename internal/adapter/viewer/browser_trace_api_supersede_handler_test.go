package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	browsertraceapp "github.com/Nyukimin/RenCrow_CORE/internal/application/browsertrace"
	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	artifactstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/browsertrace"
	eventstore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Supersede route fixtures. The artifact owner is the real JSONL store on a real temporary
// root and the event owner is the real sequence-assigning canonical event store, so the
// route is exercised against the owners it actually drives. This is a handler-boundary
// integration check, not a deployed-runtime route observation.

const (
	supersedeRoutePredecessorContent = "openapi: 3.1.0\ninfo:\n  title: supersede route predecessor\n"
	supersedeRouteSuccessorContent   = "openapi: 3.1.0\ninfo:\n  title: supersede route successor revised\n"
	supersedeRouteRivalContent       = "openapi: 3.1.0\ninfo:\n  title: supersede route rival successor\n"
)

type supersedeRouteFixture struct {
	predecessor domaintrace.APIArtifact
	successor   domaintrace.APIArtifact
}

func newSupersedeRouteStore(t *testing.T) *artifactstore.JSONLStore {
	t.Helper()
	store, err := artifactstore.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatalf("open jsonl artifact store: %v", err)
	}
	return store
}

func newSupersedeRouteEventStore(t *testing.T) *eventstore.SQLiteStore {
	t.Helper()
	events, err := eventstore.NewSQLiteStore(t.TempDir() + "/canonical_events.db")
	if err != nil {
		t.Fatalf("open canonical event store: %v", err)
	}
	t.Cleanup(func() {
		if err := events.Close(); err != nil {
			t.Errorf("close canonical event store: %v", err)
		}
	})
	return events
}

// newSupersedeRouteArtifact mints one valid artifact of the observed-openapi content role.
func newSupersedeRouteArtifact(t *testing.T, content string, createdAt time.Time) domaintrace.APIArtifact {
	t.Helper()
	kind, err := domaintrace.ArtifactKindForAPIArtifactType(domaintrace.APIArtifactTypeObservedOpenAPI)
	if err != nil {
		t.Fatalf("content role of %s: %v", domaintrace.APIArtifactTypeObservedOpenAPI, err)
	}
	item := domaintrace.APIArtifact{
		ArtifactID:   modulecore.NewArtifactID(),
		Kind:         kind,
		TaskID:       modulecore.NewTaskID(),
		RunID:        modulecore.NewRunID(),
		ActorID:      "mio",
		WorkstreamID: string(modulecore.NewWorkstreamID()),
		Type:         domaintrace.APIArtifactTypeObservedOpenAPI,
		Title:        "Supersede Route Fixture",
		Status:       "generated",
		Content:      content,
		ContentHash:  modulecore.ContentHashOf([]byte(content)),
		CreatedAt:    createdAt,
	}
	if err := domaintrace.ValidateAPIArtifact(item); err != nil {
		t.Fatalf("supersede route fixture %s is invalid before any operation: %v", item.ArtifactID, err)
	}
	return item
}

// storeSupersedeRouteArtifact stores the row the way the accepted create path stores it:
// together with its own creation fact.
func storeSupersedeRouteArtifact(t *testing.T, store *artifactstore.JSONLStore, item domaintrace.APIArtifact) {
	t.Helper()
	intent, err := browsertraceapp.NewAPIArtifactCreationIntent(item, modulecore.NewTraceID(), item.CreatedAt.Add(time.Second))
	if err != nil {
		t.Fatalf("mint creation fact for %s: %v", item.ArtifactID, err)
	}
	if err := store.CreateAPIArtifactWithPublicationIntent(context.Background(), item, intent); err != nil {
		t.Fatalf("store artifact %s with its creation fact: %v", item.ArtifactID, err)
	}
}

// newSupersedeRoutePair stores the pair the supersede route joins. The supersede contract
// requires one scope, so both rows share task, run, actor, workstream, content role and kind
// while each keeps the digest of its own body.
func newSupersedeRoutePair(t *testing.T, store *artifactstore.JSONLStore) supersedeRouteFixture {
	t.Helper()
	now := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	fixture := supersedeRouteFixture{
		predecessor: newSupersedeRouteArtifact(t, supersedeRoutePredecessorContent, now),
		successor:   newSupersedeRouteArtifact(t, supersedeRouteSuccessorContent, now.Add(time.Minute)),
	}
	fixture.successor.TaskID = fixture.predecessor.TaskID
	fixture.successor.RunID = fixture.predecessor.RunID
	fixture.successor.ActorID = fixture.predecessor.ActorID
	fixture.successor.WorkstreamID = fixture.predecessor.WorkstreamID
	fixture.successor.Kind = fixture.predecessor.Kind
	fixture.successor.Type = fixture.predecessor.Type
	for _, item := range []domaintrace.APIArtifact{fixture.predecessor, fixture.successor} {
		if err := domaintrace.ValidateAPIArtifact(item); err != nil {
			t.Fatalf("scoped supersede route fixture %s is invalid: %v", item.ArtifactID, err)
		}
	}
	storeSupersedeRouteArtifact(t, store, fixture.predecessor)
	storeSupersedeRouteArtifact(t, store, fixture.successor)
	return fixture
}

func (f supersedeRouteFixture) request() BrowserTraceAPIArtifactSupersedeRequest {
	return BrowserTraceAPIArtifactSupersedeRequest{
		PredecessorArtifactID: string(f.predecessor.ArtifactID),
		SuccessorArtifactID:   string(f.successor.ArtifactID),
		TaskID:                f.predecessor.TaskID,
		RunID:                 f.predecessor.RunID,
		ActorID:               f.predecessor.ActorID,
	}
}

func supersedeRouteBody(t *testing.T, req BrowserTraceAPIArtifactSupersedeRequest) *bytes.Reader {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("encode supersede request: %v", err)
	}
	return bytes.NewReader(raw)
}

func supersedeRoutePost(t *testing.T, body *bytes.Reader) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/supersede", body)
}

// storedSupersedeRouteArtifact reads one artifact back out of the owner by its identifier, so
// an assertion can name the whole stored record instead of one field of it.
func storedSupersedeRouteArtifact(t *testing.T, store *artifactstore.JSONLStore, id modulecore.ArtifactID) domaintrace.APIArtifact {
	t.Helper()
	current, err := store.ListAPIArtifacts(context.Background(), 200)
	if err != nil {
		t.Fatalf("list artifacts: %v", err)
	}
	for _, item := range current {
		if item.ArtifactID == id {
			return item
		}
	}
	t.Fatalf("artifact %s is not stored: %d rows", id, len(current))
	return domaintrace.APIArtifact{}
}

// firstAPISupersessionFactForEvent reads one supersession fact back out of the artifact owner
// by the canonical event id it was published under. It walks the predecessor artifact_id
// keyset until a page comes back empty, so the assertion can reach an older fact instead of
// only the newest page the store happens to return.
func firstAPISupersessionFactForEvent(ctx context.Context, store *artifactstore.JSONLStore, eventID modulecore.EventID) (modulecore.EventEnvelope, bool, error) {
	after := modulecore.ArtifactID("")
	for {
		if err := ctx.Err(); err != nil {
			return modulecore.EventEnvelope{}, false, err
		}
		page, err := store.ListAPIArtifactSupersessionFacts(ctx, after, 0)
		if err != nil {
			return modulecore.EventEnvelope{}, false, err
		}
		if len(page) == 0 {
			return modulecore.EventEnvelope{}, false, nil
		}
		for _, fact := range page {
			if fact.EventID == eventID {
				return fact, true, nil
			}
		}
		last := page[len(page)-1].ArtifactID
		if last <= after {
			return modulecore.EventEnvelope{}, false, fmt.Errorf("supersession fact cursor %q did not advance past %q", last, after)
		}
		after = last
	}
}

func TestHandleBrowserTraceAPIArtifactSupersedeSupersedesAndPublishesTheSupersessionFact(t *testing.T) {
	ctx := context.Background()
	store := newSupersedeRouteStore(t)
	fixture := newSupersedeRoutePair(t, store)
	events := newSupersedeRouteEventStore(t)
	handler := HandleBrowserTraceAPIArtifactSupersede(store, stubBrowserTraceRunVerifier{assignee: fixture.predecessor.ActorID}, events)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, supersedeRoutePost(t, supersedeRouteBody(t, fixture.request())))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Artifact          domaintrace.APIArtifact  `json:"api_artifact"`
		SupersessionEvent modulecore.EventEnvelope `json:"supersession_event"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode supersede response %s: %v", rec.Body.String(), err)
	}
	if got.Artifact.ArtifactID != fixture.predecessor.ArtifactID {
		t.Fatalf("response names artifact %s, want the predecessor %s", got.Artifact.ArtifactID, fixture.predecessor.ArtifactID)
	}
	if got.Artifact.SupersededBy != fixture.successor.ArtifactID {
		t.Errorf("response artifact %s carries superseded_by %q, want %s",
			got.Artifact.ArtifactID, got.Artifact.SupersededBy, fixture.successor.ArtifactID)
	}
	if got.SupersessionEvent.EventSeq == 0 {
		t.Errorf("response supersession event %s carries no sequence from the canonical owner", got.SupersessionEvent.EventID)
	}

	// Only the predecessor edge changes: the successor keeps its whole stored record.
	if stored := storedSupersedeRouteArtifact(t, store, fixture.successor.ArtifactID); !reflect.DeepEqual(stored, fixture.successor) {
		t.Errorf("successor %s changed while it was being made the successor", fixture.successor.ArtifactID)
	}
	wantPredecessor := fixture.predecessor
	wantPredecessor.SupersededBy = fixture.successor.ArtifactID
	if stored := storedSupersedeRouteArtifact(t, store, fixture.predecessor.ArtifactID); !reflect.DeepEqual(stored, wantPredecessor) {
		t.Errorf("stored predecessor %s changed beyond its supersession edge", fixture.predecessor.ArtifactID)
	}

	published, found, err := events.GetByID(ctx, got.SupersessionEvent.EventID)
	if err != nil || !found {
		t.Fatalf("supersession fact %s is not in the canonical event store: found=%v err=%v",
			got.SupersessionEvent.EventID, found, err)
	}
	// The artifact owner keeps the fact with no sequence and the canonical event store is the
	// only owner that assigns one, so the accepted fact shape is validated on the owner's record
	// and the canonical readback is compared to it with that single assigned field excluded.
	// Comparing the sequence would report every correctly published fact as a conflict.
	persisted, found, err := firstAPISupersessionFactForEvent(ctx, store, published.EventID)
	if err != nil {
		t.Fatalf("read the supersession fact the artifact owner kept: %v", err)
	}
	if !found {
		t.Fatalf("artifact owner holds no supersession fact for canonical event %s", published.EventID)
	}
	if err := domaintrace.ValidatePersistedAPIArtifactSupersessionFact(persisted); err != nil {
		t.Errorf("persisted supersession fact is not the accepted fact shape: %v", err)
	}
	unsequenced := published
	unsequenced.EventSeq = 0
	if !reflect.DeepEqual(unsequenced, persisted) {
		t.Errorf("canonical event %s differs from the supersession fact the artifact owner persisted", published.EventID)
	}
	if published.ArtifactID != fixture.predecessor.ArtifactID {
		t.Errorf("published supersession fact names artifact %s, want predecessor %s",
			published.ArtifactID, fixture.predecessor.ArtifactID)
	}
}

func TestHandleBrowserTraceAPIArtifactSupersedeRetriesTheSameEdgeWithoutASecondFact(t *testing.T) {
	ctx := context.Background()
	store := newSupersedeRouteStore(t)
	fixture := newSupersedeRoutePair(t, store)
	events := newSupersedeRouteEventStore(t)
	handler := HandleBrowserTraceAPIArtifactSupersede(store, stubBrowserTraceRunVerifier{assignee: fixture.predecessor.ActorID}, events)

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, supersedeRoutePost(t, supersedeRouteBody(t, fixture.request())))
	if first.Code != http.StatusOK {
		t.Fatalf("first supersede status=%d body=%s", first.Code, first.Body.String())
	}
	facts, err := store.ListAPIArtifactSupersessionFacts(ctx, "", 100)
	if err != nil {
		t.Fatalf("list supersession facts after the first supersede: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("store holds %d supersession facts after one supersede, want 1", len(facts))
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, supersedeRoutePost(t, supersedeRouteBody(t, fixture.request())))
	if second.Code != http.StatusOK {
		t.Fatalf("retry status=%d body=%s", second.Code, second.Body.String())
	}
	retried, err := store.ListAPIArtifactSupersessionFacts(ctx, "", 100)
	if err != nil {
		t.Fatalf("list supersession facts after the retry: %v", err)
	}
	if len(retried) != 1 || !reflect.DeepEqual(retried[0], facts[0]) {
		t.Fatalf("retry wrote %d supersession facts, want the one fact %s unchanged", len(retried), facts[0].EventID)
	}
	stored := storedSupersedeRouteArtifact(t, store, fixture.predecessor.ArtifactID)
	if stored.SupersededBy != fixture.successor.ArtifactID {
		t.Errorf("retry left superseded_by %q on %s, want %s", stored.SupersededBy, stored.ArtifactID, fixture.successor.ArtifactID)
	}
}

func TestHandleBrowserTraceAPIArtifactSupersedeRefusesAConflictingSuccessorWithoutWrites(t *testing.T) {
	ctx := context.Background()
	store := newSupersedeRouteStore(t)
	fixture := newSupersedeRoutePair(t, store)
	events := newSupersedeRouteEventStore(t)
	handler := HandleBrowserTraceAPIArtifactSupersede(store, stubBrowserTraceRunVerifier{assignee: fixture.predecessor.ActorID}, events)

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, supersedeRoutePost(t, supersedeRouteBody(t, fixture.request())))
	if first.Code != http.StatusOK {
		t.Fatalf("first supersede status=%d body=%s", first.Code, first.Body.String())
	}

	// A rival successor in the same scope that the predecessor never chose.
	rival := newSupersedeRouteArtifact(t, supersedeRouteRivalContent, time.Now().UTC())
	rival.TaskID = fixture.predecessor.TaskID
	rival.RunID = fixture.predecessor.RunID
	rival.ActorID = fixture.predecessor.ActorID
	rival.WorkstreamID = fixture.predecessor.WorkstreamID
	rival.Kind = fixture.predecessor.Kind
	rival.Type = fixture.predecessor.Type
	if err := domaintrace.ValidateAPIArtifact(rival); err != nil {
		t.Fatalf("rival successor is invalid: %v", err)
	}
	storeSupersedeRouteArtifact(t, store, rival)

	conflict := fixture.request()
	conflict.SuccessorArtifactID = string(rival.ArtifactID)
	before, err := store.ListAPIArtifactSupersessionFacts(ctx, "", 100)
	if err != nil {
		t.Fatalf("list supersession facts before the conflict: %v", err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, supersedeRoutePost(t, supersedeRouteBody(t, conflict)))

	if rec.Code != http.StatusConflict {
		t.Fatalf("conflicting supersede status=%d, want %d body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), string(fixture.successor.ArtifactID)) {
		t.Errorf("conflict response does not name the established successor %s: %s", fixture.successor.ArtifactID, rec.Body.String())
	}
	after, err := store.ListAPIArtifactSupersessionFacts(ctx, "", 100)
	if err != nil {
		t.Fatalf("list supersession facts after the conflict: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("conflicting supersede holds %d supersession facts, want the %d it held before", len(after), len(before))
	}
	stored := storedSupersedeRouteArtifact(t, store, fixture.predecessor.ArtifactID)
	if stored.SupersededBy != fixture.successor.ArtifactID {
		t.Errorf("conflicting supersede moved the edge of %s to %q, want the established %s",
			stored.ArtifactID, stored.SupersededBy, fixture.successor.ArtifactID)
	}
}

func TestHandleBrowserTraceAPIArtifactSupersedeRefusesUnverifiedTaskRunOwnership(t *testing.T) {
	ctx := context.Background()
	store := newSupersedeRouteStore(t)
	fixture := newSupersedeRoutePair(t, store)
	events := newSupersedeRouteEventStore(t)
	ownerErr := errors.New("run verification failed: no such run")

	rec := httptest.NewRecorder()
	HandleBrowserTraceAPIArtifactSupersede(store, stubBrowserTraceRunVerifier{err: ownerErr}, events).
		ServeHTTP(rec, supersedeRoutePost(t, supersedeRouteBody(t, fixture.request())))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("unverified provenance status=%d, want %d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	stored := storedSupersedeRouteArtifact(t, store, fixture.predecessor.ArtifactID)
	if stored.SupersededBy != "" {
		t.Errorf("unverified supersede still wrote an edge: %q", stored.SupersededBy)
	}
	facts, err := store.ListAPIArtifactSupersessionFacts(ctx, "", 100)
	if err != nil {
		t.Fatalf("list supersession facts after the refusal: %v", err)
	}
	if len(facts) != 0 {
		t.Errorf("unverified supersede stored %d supersession facts, want none", len(facts))
	}
}

func TestHandleBrowserTraceAPIArtifactSupersedeRefusesAPairOwnedByAnotherTask(t *testing.T) {
	store := newSupersedeRouteStore(t)
	fixture := newSupersedeRoutePair(t, store)
	events := newSupersedeRouteEventStore(t)

	// The caller proves a task and run it owns, but the named pair belongs elsewhere.
	req := fixture.request()
	req.TaskID = modulecore.NewTaskID()
	req.RunID = modulecore.NewRunID()

	rec := httptest.NewRecorder()
	HandleBrowserTraceAPIArtifactSupersede(store, stubBrowserTraceRunVerifier{assignee: fixture.predecessor.ActorID}, events).
		ServeHTTP(rec, supersedeRoutePost(t, supersedeRouteBody(t, req)))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign pair status=%d, want %d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	stored := storedSupersedeRouteArtifact(t, store, fixture.predecessor.ArtifactID)
	if stored.SupersededBy != "" {
		t.Errorf("foreign supersede still wrote an edge: %q", stored.SupersededBy)
	}
}

func TestHandleBrowserTraceAPIArtifactSupersedeRefusesNonCanonicalArtifactIDsBeforeWrites(t *testing.T) {
	store := newSupersedeRouteStore(t)
	fixture := newSupersedeRoutePair(t, store)
	events := &stubBrowserTraceCanonicalEvents{}

	cases := []struct {
		name   string
		mutate func(*BrowserTraceAPIArtifactSupersedeRequest)
	}{
		{"opaque predecessor", func(r *BrowserTraceAPIArtifactSupersedeRequest) {
			r.PredecessorArtifactID = "artifact_1"
		}},
		{"opaque successor", func(r *BrowserTraceAPIArtifactSupersedeRequest) {
			r.SuccessorArtifactID = "art_uuidv4_substitute"
		}},
		{"missing predecessor", func(r *BrowserTraceAPIArtifactSupersedeRequest) {
			r.PredecessorArtifactID = ""
		}},
		{"self edge", func(r *BrowserTraceAPIArtifactSupersedeRequest) {
			r.SuccessorArtifactID = r.PredecessorArtifactID
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := fixture.request()
			tc.mutate(&req)
			rec := httptest.NewRecorder()
			HandleBrowserTraceAPIArtifactSupersede(store, stubBrowserTraceRunVerifier{assignee: fixture.predecessor.ActorID}, events).
				ServeHTTP(rec, supersedeRoutePost(t, supersedeRouteBody(t, req)))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			stored := storedSupersedeRouteArtifact(t, store, fixture.predecessor.ArtifactID)
			if stored.SupersededBy != "" {
				t.Errorf("refused request %q wrote an edge: %q", tc.name, stored.SupersededBy)
			}
			if events.appendCalls != 0 {
				t.Errorf("refused request %q appended %d canonical events, want none", tc.name, events.appendCalls)
			}
		})
	}
}

func TestHandleBrowserTraceAPIArtifactSupersedeKeepsTheFactDurableWhenPublicationFails(t *testing.T) {
	ctx := context.Background()
	store := newSupersedeRouteStore(t)
	fixture := newSupersedeRoutePair(t, store)
	events := &stubBrowserTraceCanonicalEvents{appendErr: errors.New("canonical event store is unavailable")}

	rec := httptest.NewRecorder()
	HandleBrowserTraceAPIArtifactSupersede(store, stubBrowserTraceRunVerifier{assignee: fixture.predecessor.ActorID}, events).
		ServeHTTP(rec, supersedeRoutePost(t, supersedeRouteBody(t, fixture.request())))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("undelivered fact status=%d, want %d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	stored := storedSupersedeRouteArtifact(t, store, fixture.predecessor.ArtifactID)
	if stored.SupersededBy != fixture.successor.ArtifactID {
		t.Errorf("edge of %s = %q, want the durable %s", stored.ArtifactID, stored.SupersededBy, fixture.successor.ArtifactID)
	}
	facts, err := store.ListAPIArtifactSupersessionFacts(ctx, "", 100)
	if err != nil {
		t.Fatalf("list supersession facts after the failed delivery: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("store holds %d supersession facts after the failed delivery, want the one fact to stay durable", len(facts))
	}

	// The pass the restart recovery performs: a still-failing owner reports it, and a working
	// owner delivers exactly that kept fact once.
	report, err := browsertraceapp.PublishPendingAPIArtifactSupersessionFacts(ctx, store, events)
	if err == nil {
		t.Fatalf("retry with a failing canonical store reported no error: report=%+v", report)
	}
	if report.Failed != 1 {
		t.Errorf("retry report = %+v, want 1 failed delivery", report)
	}
	working := newSupersedeRouteEventStore(t)
	recovered, err := browsertraceapp.PublishPendingAPIArtifactSupersessionFacts(ctx, store, working)
	if err != nil {
		t.Fatalf("recover the undelivered supersession fact: %v (report %+v)", err, recovered)
	}
	if recovered.Scanned != 1 || recovered.Published != 1 || recovered.Failed != 0 {
		t.Errorf("recovery report = %+v, want 1 scanned and 1 published", recovered)
	}
}
