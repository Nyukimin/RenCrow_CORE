package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type worldPocReaderStub struct {
	snapshot WorldPocSnapshot
	events   []WorldPocSnapshot
	err      error
}

func (s worldPocReaderStub) ReadSnapshot(context.Context) (WorldPocSnapshot, error) {
	return s.snapshot, s.err
}

func (s worldPocReaderStub) ReadEvents(context.Context, uint64) ([]WorldPocSnapshot, error) {
	return s.events, s.err
}

func TestWorldPocSnapshotHandlerReturnsScopedContract(t *testing.T) {
	fixture := worldPocFixture(1)
	bridge := NewWorldPocBridge(worldPocReaderStub{snapshot: fixture}, WorldPocViewScope{ActorIDs: []string{"mio"}})
	request := httptest.NewRequest(http.MethodGet, "/viewer/world/snapshot", nil)
	response := httptest.NewRecorder()
	bridge.SnapshotHandler()(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var got WorldPocSnapshot
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.SchemaVersion != worldPocSchemaVersion || got.Source != "mock" || got.WorldID != "r04-house" || got.WorldRevision != 1 {
		t.Fatalf("unexpected snapshot contract: %+v", got)
	}
	if got.CapturedAt != fixture.CapturedAt || got.SpatialRevision != "unverified" || got.SpatialReady {
		t.Fatalf("unexpected snapshot metadata: %+v", got)
	}
	if len(got.Actors) != 1 || got.Actors[0].ActorID != "mio" || got.Actors[0].Presence != "present" || got.Actors[0].FloorID != "1f" || got.Actors[0].RoomID != "r04-room-1f" || got.Actors[0].Position != nil || got.Actors[0].ActivityID != "idle" {
		t.Fatalf("unexpected actor fixture: %+v", got.Actors)
	}
}

func TestWorldPocSnapshotHandlerFiltersActorsByExplicitScope(t *testing.T) {
	bridge := NewWorldPocBridge(worldPocReaderStub{snapshot: worldPocFixture(1)}, WorldPocViewScope{})
	request := httptest.NewRequest(http.MethodGet, "/viewer/world/snapshot", nil)
	response := httptest.NewRecorder()
	bridge.SnapshotHandler()(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var got WorldPocSnapshot
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Actors == nil || len(got.Actors) != 0 || strings.Contains(response.Body.String(), "activity_id") {
		t.Fatalf("out-of-scope actor leaked: %s", response.Body.String())
	}
}

func TestWorldPocSnapshotHandlerRejectsMethodAndQuery(t *testing.T) {
	bridge := NewWorldPocBridge(worldPocReaderStub{snapshot: worldPocFixture(1)}, WorldPocViewScope{ActorIDs: []string{"mio"}})
	tests := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{name: "method", method: http.MethodPost, path: "/viewer/world/snapshot", want: http.StatusMethodNotAllowed},
		{name: "query", method: http.MethodGet, path: "/viewer/world/snapshot?extra=1", want: http.StatusBadRequest},
		{name: "path", method: http.MethodGet, path: "/viewer/world/other", want: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			response := httptest.NewRecorder()
			bridge.SnapshotHandler()(response, request)
			if response.Code != test.want {
				t.Fatalf("expected %d, got %d: %s", test.want, response.Code, response.Body.String())
			}
		})
	}
}

func TestNewMockWorldPocReaderReturnsStableFixtureWithoutAdvancing(t *testing.T) {
	reader := NewMockWorldPocReader()
	first, err := reader.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatalf("read first snapshot: %v", err)
	}
	second, err := reader.ReadSnapshot(context.Background())
	if err != nil {
		t.Fatalf("read second snapshot: %v", err)
	}
	if !reflect.DeepEqual(first, second) || first.WorldRevision != 1 {
		t.Fatalf("GET read changed fixture: first=%+v second=%+v", first, second)
	}
	firstEvents, err := reader.ReadEvents(context.Background(), 0)
	if err != nil {
		t.Fatalf("read first event batch: %v", err)
	}
	secondEvents, err := reader.ReadEvents(context.Background(), 0)
	if err != nil {
		t.Fatalf("read second event batch: %v", err)
	}
	if !reflect.DeepEqual(firstEvents, secondEvents) || len(firstEvents) != 1 || firstEvents[0].WorldRevision != 1 {
		t.Fatalf("GET stream changed fixture: first=%+v second=%+v", firstEvents, secondEvents)
	}
	resumed, err := reader.ReadEvents(context.Background(), 1)
	if err != nil || len(resumed) != 0 {
		t.Fatalf("expected no events after revision 1, got %+v, %v", resumed, err)
	}
}

func TestWorldPocStreamHandlerEmitsScopedSnapshotAndCursorOnlyForHiddenActor(t *testing.T) {
	t.Run("visible actor", func(t *testing.T) {
		fixture := worldPocFixture(1)
		bridge := NewWorldPocBridge(worldPocReaderStub{snapshot: fixture, events: []WorldPocSnapshot{fixture}}, WorldPocViewScope{ActorIDs: []string{"mio"}})
		request := httptest.NewRequest(http.MethodGet, "/viewer/world/stream?after_revision=0", nil)
		response := httptest.NewRecorder()
		bridge.StreamHandler()(response, request)
		body := response.Body.String()
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" {
			t.Fatalf("expected SSE 200, got %d %q: %s", response.Code, response.Header().Get("Content-Type"), body)
		}
		if !strings.Contains(body, "id: 1\nevent: snapshot\ndata: ") || !strings.Contains(body, `"actor_id":"mio"`) {
			t.Fatalf("missing scoped snapshot event: %s", body)
		}
	})

	t.Run("hidden actor", func(t *testing.T) {
		fixture := worldPocFixture(1)
		bridge := NewWorldPocBridge(worldPocReaderStub{snapshot: fixture, events: []WorldPocSnapshot{fixture}}, WorldPocViewScope{})
		request := httptest.NewRequest(http.MethodGet, "/viewer/world/stream?after_revision=0", nil)
		response := httptest.NewRecorder()
		bridge.StreamHandler()(response, request)
		body := response.Body.String()
		if response.Code != http.StatusOK || !strings.Contains(body, "id: 1\nevent: cursor\n") {
			t.Fatalf("expected leak-free cursor advance, got %d: %s", response.Code, body)
		}
		if strings.Contains(body, "actor_id") || strings.Contains(body, "activity_id") || strings.Contains(body, "present") {
			t.Fatalf("hidden actor payload leaked: %s", body)
		}
	})
}

func TestWorldPocStreamHandlerEndsCatchUpWithCaughtUp(t *testing.T) {
	caughtUpEvent := func(revision uint64) string {
		return "id: " + strconv.FormatUint(revision, 10) + "\nevent: caught_up\ndata: {\"schema_version\":\"r04.mock.v1\",\"source\":\"mock\",\"world_id\":\"r04-house\",\"world_revision\":" + strconv.FormatUint(revision, 10) + "}\n\n"
	}

	t.Run("normal EOF after catch-up", func(t *testing.T) {
		first := worldPocFixture(1)
		second := worldPocFixture(2)
		bridge := NewWorldPocBridge(worldPocReaderStub{snapshot: second, events: []WorldPocSnapshot{first, second}}, WorldPocViewScope{ActorIDs: []string{"mio"}})
		request := httptest.NewRequest(http.MethodGet, "/viewer/world/stream?after_revision=0", nil)
		response := httptest.NewRecorder()
		bridge.StreamHandler()(response, request)
		body := response.Body.String()
		if response.Code != http.StatusOK || !strings.HasSuffix(body, caughtUpEvent(2)) {
			t.Fatalf("expected completed catch-up SSE, got %d: %s", response.Code, body)
		}
		firstIndex := strings.Index(body, "id: 1\nevent: snapshot")
		secondIndex := strings.Index(body, "id: 2\nevent: snapshot")
		caughtUpIndex := strings.LastIndex(body, "event: caught_up\n")
		if strings.Count(body, "event: caught_up\n") != 1 || firstIndex < 0 || secondIndex <= firstIndex || caughtUpIndex <= secondIndex {
			t.Fatalf("caught_up must follow all revision events exactly once: %s", body)
		}
	})

	t.Run("empty batch at current revision", func(t *testing.T) {
		fixture := worldPocFixture(1)
		bridge := NewWorldPocBridge(worldPocReaderStub{snapshot: fixture}, WorldPocViewScope{ActorIDs: []string{"mio"}})
		request := httptest.NewRequest(http.MethodGet, "/viewer/world/stream?after_revision=1", nil)
		response := httptest.NewRecorder()
		bridge.StreamHandler()(response, request)
		if response.Code != http.StatusOK || response.Body.String() != caughtUpEvent(1) {
			t.Fatalf("empty catch-up must emit only caught_up: %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("hidden actor payload remains absent", func(t *testing.T) {
		fixture := worldPocFixture(1)
		bridge := NewWorldPocBridge(worldPocReaderStub{snapshot: fixture, events: []WorldPocSnapshot{fixture}}, WorldPocViewScope{})
		request := httptest.NewRequest(http.MethodGet, "/viewer/world/stream?after_revision=0", nil)
		response := httptest.NewRecorder()
		bridge.StreamHandler()(response, request)
		body := response.Body.String()
		if response.Code != http.StatusOK || !strings.Contains(body, "id: 1\nevent: cursor\ndata: {\"schema_version\":\"r04.mock.v1\",\"source\":\"mock\",\"world_id\":\"r04-house\",\"world_revision\":1}\n\n"+caughtUpEvent(1)) {
			t.Fatalf("hidden actor should advance through minimal cursor events: %d %s", response.Code, body)
		}
		if strings.Contains(body, "actor_id") || strings.Contains(body, "activity_id") || strings.Contains(body, "present") {
			t.Fatalf("hidden actor payload leaked: %s", body)
		}
	})
}

func TestWorldPocStreamHandlerRejectsMethodAndPath(t *testing.T) {
	bridge := NewWorldPocBridge(worldPocReaderStub{snapshot: worldPocFixture(1)}, WorldPocViewScope{ActorIDs: []string{"mio"}})
	for _, test := range []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{name: "method", method: http.MethodPost, path: "/viewer/world/stream?after_revision=0", want: http.StatusMethodNotAllowed},
		{name: "path", method: http.MethodGet, path: "/viewer/world/other?after_revision=0", want: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			response := httptest.NewRecorder()
			bridge.StreamHandler()(response, request)
			if response.Code != test.want {
				t.Fatalf("expected %d, got %d: %s", test.want, response.Code, response.Body.String())
			}
		})
	}
}

func TestWorldPocStreamHandlerRejectsInvalidCursorAndEventGap(t *testing.T) {
	fixture := worldPocFixture(1)
	bridge := NewWorldPocBridge(worldPocReaderStub{snapshot: fixture}, WorldPocViewScope{ActorIDs: []string{"mio"}})
	for _, test := range []struct {
		name string
		path string
		want int
	}{
		{name: "missing", path: "/viewer/world/stream", want: http.StatusBadRequest},
		{name: "malformed", path: "/viewer/world/stream?after_revision=1x", want: http.StatusBadRequest},
		{name: "noncanonical", path: "/viewer/world/stream?after_revision=01", want: http.StatusBadRequest},
		{name: "duplicate", path: "/viewer/world/stream?after_revision=0&after_revision=0", want: http.StatusBadRequest},
		{name: "unknown query", path: "/viewer/world/stream?after_revision=0&extra=1", want: http.StatusBadRequest},
		{name: "ahead", path: "/viewer/world/stream?after_revision=2", want: http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			bridge.StreamHandler()(response, request)
			if response.Code != test.want {
				t.Fatalf("expected %d, got %d: %s", test.want, response.Code, response.Body.String())
			}
		})
	}

	gap := NewWorldPocBridge(worldPocReaderStub{snapshot: fixture}, WorldPocViewScope{ActorIDs: []string{"mio"}})
	request := httptest.NewRequest(http.MethodGet, "/viewer/world/stream?after_revision=0", nil)
	response := httptest.NewRecorder()
	gap.StreamHandler()(response, request)
	if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), "event: ") {
		t.Fatalf("event gap must fail closed before emitting events: %d %s", response.Code, response.Body.String())
	}
}

func TestWorldPocStreamHandlerRejectsUnknownVersionWorldAndRevision(t *testing.T) {
	fixture := worldPocFixture(1)
	tests := []struct {
		name   string
		mutate func(*WorldPocSnapshot)
	}{
		{name: "version", mutate: func(event *WorldPocSnapshot) { event.SchemaVersion = "unknown" }},
		{name: "world", mutate: func(event *WorldPocSnapshot) { event.WorldID = "other-world" }},
		{name: "revision", mutate: func(event *WorldPocSnapshot) { event.WorldRevision = 2 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := fixture
			test.mutate(&event)
			bridge := NewWorldPocBridge(worldPocReaderStub{snapshot: fixture, events: []WorldPocSnapshot{event}}, WorldPocViewScope{ActorIDs: []string{"mio"}})
			request := httptest.NewRequest(http.MethodGet, "/viewer/world/stream?after_revision=0", nil)
			response := httptest.NewRecorder()
			bridge.StreamHandler()(response, request)
			want := http.StatusBadGateway
			if test.name == "revision" {
				want = http.StatusConflict
			}
			if response.Code != want || strings.Contains(response.Body.String(), "event: ") {
				t.Fatalf("expected fail-closed status %d, got %d %s", want, response.Code, response.Body.String())
			}
		})
	}
}

func TestWorldPocSnapshotHandlerRejectsInvalidReaderContract(t *testing.T) {
	fixture := worldPocFixture(1)
	fixture.SchemaVersion = "unknown"
	bridge := NewWorldPocBridge(worldPocReaderStub{snapshot: fixture}, WorldPocViewScope{ActorIDs: []string{"mio"}})
	request := httptest.NewRequest(http.MethodGet, "/viewer/world/snapshot", nil)
	response := httptest.NewRecorder()
	bridge.SnapshotHandler()(response, request)
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "actor_id") {
		t.Fatalf("invalid reader data must fail closed: %d %s", response.Code, response.Body.String())
	}
}

func TestWorldPocSnapshotHandlerRejectsCapturedAtTooFarInFuture(t *testing.T) {
	fixture := worldPocFixture(1)
	fixture.CapturedAt = time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	bridge := NewWorldPocBridge(worldPocReaderStub{snapshot: fixture}, WorldPocViewScope{ActorIDs: []string{"mio"}})
	request := httptest.NewRequest(http.MethodGet, "/viewer/world/snapshot", nil)
	response := httptest.NewRecorder()
	bridge.SnapshotHandler()(response, request)
	if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "actor_id") {
		t.Fatalf("future reader timestamp must fail closed: %d %s", response.Code, response.Body.String())
	}
}

func worldPocFixture(revision uint64) WorldPocSnapshot {
	return WorldPocSnapshot{
		SchemaVersion:   worldPocSchemaVersion,
		Source:          "mock",
		WorldID:         "r04-house",
		WorldRevision:   revision,
		CapturedAt:      "2026-10-15T00:00:00Z",
		SpatialRevision: "unverified",
		SpatialReady:    false,
		Actors: []WorldPocActor{{
			ActorID:    "mio",
			Presence:   "present",
			FloorID:    "1f",
			RoomID:     "r04-room-1f",
			ActivityID: "idle",
		}},
	}
}
