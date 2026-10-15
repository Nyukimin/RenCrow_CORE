package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	worldPocSchemaVersion = "r04.mock.v1"
	worldPocSource        = "mock"
	worldPocWorldID       = "r04-house"
	worldPocMaxFutureSkew = 5 * time.Minute
)

type WorldPocActor struct {
	ActorID    string `json:"actor_id"`
	Presence   string `json:"presence"`
	FloorID    string `json:"floor_id"`
	RoomID     string `json:"room_id"`
	Position   any    `json:"position"`
	ActivityID string `json:"activity_id"`
}

type WorldPocSnapshot struct {
	SchemaVersion   string          `json:"schema_version"`
	Source          string          `json:"source"`
	WorldID         string          `json:"world_id"`
	WorldRevision   uint64          `json:"world_revision"`
	CapturedAt      string          `json:"captured_at"`
	SpatialRevision string          `json:"spatial_revision"`
	SpatialReady    bool            `json:"spatial_ready"`
	Actors          []WorldPocActor `json:"actors"`
}

type WorldPocReader interface {
	ReadSnapshot(context.Context) (WorldPocSnapshot, error)
	ReadEvents(context.Context, uint64) ([]WorldPocSnapshot, error)
}

type WorldPocViewScope struct {
	ActorIDs []string
}

type WorldPocBridge struct {
	reader   WorldPocReader
	actorIDs map[string]struct{}
}

type mockWorldPocReader struct {
	snapshot WorldPocSnapshot
	events   []WorldPocSnapshot
}

var (
	errWorldPocReaderContract = errors.New("invalid mock world reader contract")
	errWorldPocEventGap       = errors.New("mock world event gap")
)

func NewMockWorldPocReader() WorldPocReader {
	snapshot := WorldPocSnapshot{
		SchemaVersion:   worldPocSchemaVersion,
		Source:          worldPocSource,
		WorldID:         worldPocWorldID,
		WorldRevision:   1,
		CapturedAt:      time.Now().UTC().Format(time.RFC3339),
		SpatialRevision: "unverified",
		SpatialReady:    false,
		Actors: []WorldPocActor{{
			ActorID:    "mio",
			Presence:   "present",
			FloorID:    "1f",
			RoomID:     "r04-room-1f",
			Position:   nil,
			ActivityID: "idle",
		}},
	}
	return &mockWorldPocReader{snapshot: snapshot, events: []WorldPocSnapshot{snapshot}}
}

func (r *mockWorldPocReader) ReadSnapshot(ctx context.Context) (WorldPocSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return WorldPocSnapshot{}, err
	}
	return cloneWorldPocSnapshot(r.snapshot), nil
}

func (r *mockWorldPocReader) ReadEvents(ctx context.Context, afterRevision uint64) ([]WorldPocSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	events := make([]WorldPocSnapshot, 0, len(r.events))
	for _, event := range r.events {
		if event.WorldRevision > afterRevision {
			events = append(events, cloneWorldPocSnapshot(event))
		}
	}
	return events, nil
}

func NewWorldPocBridge(reader WorldPocReader, scope WorldPocViewScope) *WorldPocBridge {
	actorIDs := make(map[string]struct{}, len(scope.ActorIDs))
	for _, actorID := range scope.ActorIDs {
		if actorID != "" {
			actorIDs[actorID] = struct{}{}
		}
	}
	return &WorldPocBridge{reader: reader, actorIDs: actorIDs}
}

func (b *WorldPocBridge) SnapshotHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validateWorldPocRequest(w, r, "/viewer/world/snapshot") {
			return
		}
		if b == nil || b.reader == nil {
			http.Error(w, "world snapshot unavailable", http.StatusServiceUnavailable)
			return
		}
		snapshot, err := b.reader.ReadSnapshot(r.Context())
		if err != nil {
			http.Error(w, "world snapshot unavailable", http.StatusServiceUnavailable)
			return
		}
		if validateWorldPocSnapshot(snapshot) != nil {
			http.Error(w, "world snapshot contract invalid", http.StatusBadGateway)
			return
		}
		writeWorldPocJSON(w, b.filterSnapshot(snapshot))
	}
}

func (b *WorldPocBridge) StreamHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/viewer/world/stream" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		afterRevision, err := parseWorldPocCursor(r.URL.RawQuery)
		if err != nil {
			http.Error(w, "invalid after_revision", http.StatusBadRequest)
			return
		}
		if b == nil || b.reader == nil {
			http.Error(w, "world stream unavailable", http.StatusServiceUnavailable)
			return
		}
		snapshot, err := b.reader.ReadSnapshot(r.Context())
		if err != nil {
			http.Error(w, "world stream unavailable", http.StatusServiceUnavailable)
			return
		}
		if validateWorldPocSnapshot(snapshot) != nil {
			http.Error(w, "world stream contract invalid", http.StatusBadGateway)
			return
		}
		if afterRevision > snapshot.WorldRevision {
			http.Error(w, "after_revision is ahead of world", http.StatusConflict)
			return
		}
		events, err := b.reader.ReadEvents(r.Context(), afterRevision)
		if err != nil {
			http.Error(w, "world stream unavailable", http.StatusServiceUnavailable)
			return
		}
		body, err := b.streamBody(snapshot, afterRevision, events)
		if err != nil {
			if errors.Is(err, errWorldPocEventGap) {
				http.Error(w, "world event gap", http.StatusConflict)
				return
			}
			http.Error(w, "world stream contract invalid", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}

func validateWorldPocRequest(w http.ResponseWriter, r *http.Request, path string) bool {
	if r.URL.Path != path {
		http.NotFound(w, r)
		return false
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 0 {
		http.Error(w, "unexpected query", http.StatusBadRequest)
		return false
	}
	return true
}

func parseWorldPocCursor(rawQuery string) (uint64, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil || len(query) != 1 {
		return 0, errors.New("invalid query")
	}
	values, ok := query["after_revision"]
	if !ok || len(values) != 1 || values[0] == "" {
		return 0, errors.New("invalid cursor")
	}
	cursor, err := strconv.ParseUint(values[0], 10, 64)
	if err != nil || strconv.FormatUint(cursor, 10) != values[0] {
		return 0, errors.New("invalid cursor")
	}
	return cursor, nil
}

func validateWorldPocSnapshot(snapshot WorldPocSnapshot) error {
	if snapshot.SchemaVersion != worldPocSchemaVersion || snapshot.Source != worldPocSource || snapshot.WorldID != worldPocWorldID || snapshot.WorldRevision == 0 {
		return errWorldPocReaderContract
	}
	capturedAt, err := time.Parse(time.RFC3339, snapshot.CapturedAt)
	if err != nil || capturedAt.After(time.Now().UTC().Add(worldPocMaxFutureSkew)) {
		return errWorldPocReaderContract
	}
	if snapshot.SpatialRevision != "unverified" || snapshot.SpatialReady || len(snapshot.Actors) != 1 {
		return errWorldPocReaderContract
	}
	actor := snapshot.Actors[0]
	if actor.ActorID != "mio" || actor.Presence != "present" || actor.FloorID != "1f" || actor.RoomID != "r04-room-1f" || actor.Position != nil || actor.ActivityID != "idle" {
		return errWorldPocReaderContract
	}
	return nil
}

func (b *WorldPocBridge) filterSnapshot(snapshot WorldPocSnapshot) WorldPocSnapshot {
	filtered := cloneWorldPocSnapshot(snapshot)
	filtered.Actors = make([]WorldPocActor, 0, len(snapshot.Actors))
	for _, actor := range snapshot.Actors {
		if _, allowed := b.actorIDs[actor.ActorID]; allowed {
			filtered.Actors = append(filtered.Actors, actor)
		}
	}
	return filtered
}

func (b *WorldPocBridge) streamBody(snapshot WorldPocSnapshot, afterRevision uint64, events []WorldPocSnapshot) ([]byte, error) {
	if uint64(len(events)) != snapshot.WorldRevision-afterRevision {
		return nil, errWorldPocEventGap
	}
	var body bytes.Buffer
	expectedRevision := afterRevision + 1
	for _, event := range events {
		if err := validateWorldPocSnapshot(event); err != nil {
			return nil, err
		}
		if event.WorldRevision != expectedRevision || event.WorldRevision > snapshot.WorldRevision {
			return nil, errWorldPocEventGap
		}
		expectedRevision++
		filtered := b.filterSnapshot(event)
		if len(filtered.Actors) == 0 {
			cursor := worldPocCursor{
				SchemaVersion: worldPocSchemaVersion,
				Source:        worldPocSource,
				WorldID:       worldPocWorldID,
				WorldRevision: event.WorldRevision,
			}
			if err := writeWorldPocEvent(&body, event.WorldRevision, "cursor", cursor); err != nil {
				return nil, err
			}
			continue
		}
		if err := writeWorldPocEvent(&body, event.WorldRevision, "snapshot", filtered); err != nil {
			return nil, err
		}
	}
	caughtUp := worldPocCursor{
		SchemaVersion: worldPocSchemaVersion,
		Source:        worldPocSource,
		WorldID:       worldPocWorldID,
		WorldRevision: snapshot.WorldRevision,
	}
	if err := writeWorldPocEvent(&body, snapshot.WorldRevision, "caught_up", caughtUp); err != nil {
		return nil, err
	}
	return body.Bytes(), nil
}

type worldPocCursor struct {
	SchemaVersion string `json:"schema_version"`
	Source        string `json:"source"`
	WorldID       string `json:"world_id"`
	WorldRevision uint64 `json:"world_revision"`
}

func writeWorldPocEvent(body *bytes.Buffer, revision uint64, eventType string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(body, "id: %d\nevent: %s\ndata: %s\n\n", revision, eventType, data); err != nil {
		return err
	}
	return nil
}

func writeWorldPocJSON(w http.ResponseWriter, snapshot WorldPocSnapshot) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(snapshot)
}

func cloneWorldPocSnapshot(snapshot WorldPocSnapshot) WorldPocSnapshot {
	snapshot.Actors = append([]WorldPocActor(nil), snapshot.Actors...)
	return snapshot
}
