package worldread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestNewValidatesExactScopeAndDefaults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()

	valid := func() Config {
		return Config{
			BaseURL:       server.URL,
			Credential:    "read-token",
			WorldID:       "rencrow-home",
			TimelineID:    "timeline-1",
			TimelineEpoch: "epoch-1",
			ActorIDs:      []string{"mio"},
		}
	}

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "empty base", mutate: func(c *Config) { c.BaseURL = "" }},
		{name: "bad scheme", mutate: func(c *Config) { c.BaseURL = "ftp://example.test" }},
		{name: "userinfo", mutate: func(c *Config) { c.BaseURL = "http://user:pass@example.test" }},
		{name: "query", mutate: func(c *Config) { c.BaseURL = server.URL + "?x=1" }},
		{name: "fragment", mutate: func(c *Config) { c.BaseURL = server.URL + "#fragment" }},
		{name: "empty credential", mutate: func(c *Config) { c.Credential = "" }},
		{name: "padded credential", mutate: func(c *Config) { c.Credential = " read-token " }},
		{name: "newline credential", mutate: func(c *Config) { c.Credential = "read\ntoken" }},
		{name: "empty world", mutate: func(c *Config) { c.WorldID = "" }},
		{name: "control world", mutate: func(c *Config) { c.WorldID = "world\x00" }},
		{name: "empty timeline", mutate: func(c *Config) { c.TimelineID = "" }},
		{name: "empty epoch", mutate: func(c *Config) { c.TimelineEpoch = "" }},
		{name: "no actors", mutate: func(c *Config) { c.ActorIDs = nil }},
		{name: "empty actor", mutate: func(c *Config) { c.ActorIDs = []string{""} }},
		{name: "duplicate actor", mutate: func(c *Config) { c.ActorIDs = []string{"mio", "mio"} }},
		{name: "control actor", mutate: func(c *Config) { c.ActorIDs = []string{"mio\x00"} }},
		{name: "negative timeout", mutate: func(c *Config) { c.Timeout = -time.Second }},
		{name: "too many events", mutate: func(c *Config) { c.MaxEvents = 501 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := valid()
			test.mutate(&cfg)
			client, err := New(cfg)
			if err == nil || client != nil {
				t.Fatalf("New() = %+v, %v; want an invalid-config error", client, err)
			}
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("New() error = %v; want ErrInvalidConfig", err)
			}
		})
	}

	client, err := New(valid())
	if err != nil {
		t.Fatalf("New(valid) error = %v", err)
	}
	if client.timeout != DefaultTimeout || client.maxEvents != DefaultMaxEvents {
		t.Fatalf("defaults = %v, %d; want %v, %d", client.timeout, client.maxEvents, DefaultTimeout, DefaultMaxEvents)
	}
}

func TestSnapshotUsesAuthenticatedTransportAndFiltersActors(t *testing.T) {
	var gotPath, gotRawQuery, gotAuth, gotAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotRawQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		body := validSnapshotBody()
		body["world_id"] = "世界"
		body["timeline_id"] = "timeline 1"
		body["timeline_epoch"] = "epoch-1"
		writeJSON(w, http.StatusOK, body)
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL:       server.URL,
		Credential:    "read-token",
		WorldID:       "世界",
		TimelineID:    "timeline 1",
		TimelineEpoch: "epoch-1",
		ActorIDs:      []string{"mio", "kuro"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	snapshot, err := client.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}

	if gotPath != "/v1/worlds/~5LiW55WM/timelines/~dGltZWxpbmUgMQ/snapshot" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotRawQuery != "" || gotAuth != "Bearer read-token" || gotAccept != "application/json" {
		t.Fatalf("request metadata query=%q auth=%q accept=%q", gotRawQuery, gotAuth, gotAccept)
	}
	if snapshot.SchemaVersion != 1 || snapshot.WorldID != "世界" || snapshot.TimelineID != "timeline 1" ||
		snapshot.TimelineEpoch != "epoch-1" || snapshot.StateSeq != 4 || snapshot.StateHash != testHash {
		t.Fatalf("snapshot metadata = %+v", snapshot)
	}
	if len(snapshot.Actors) != 2 {
		t.Fatalf("actors = %+v", snapshot.Actors)
	}
	if snapshot.Actors[0].ActorID != "kuro" || !snapshot.Actors[0].Present || snapshot.Actors[0].RoomID != "1f-kitchen" {
		t.Fatalf("first actor = %+v", snapshot.Actors[0])
	}
	if snapshot.Actors[1].ActorID != "mio" || !snapshot.Actors[1].Present ||
		snapshot.Actors[1].RoomID != "1f-living" || snapshot.Actors[1].ActivityID != "read" {
		t.Fatalf("second actor = %+v", snapshot.Actors[1])
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if strings.Contains(string(raw), "shiro") || strings.Contains(string(raw), "2f-room") {
		t.Fatalf("hidden actor leaked: %s", raw)
	}
}

func TestSnapshotReportsAllowedActorsThatAreAbsent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := validSnapshotBody()
		body["state"] = map[string]any{"actors": map[string]any{}}
		writeJSON(w, http.StatusOK, body)
	}))
	defer server.Close()

	client := newClient(t, server.URL, "mio")
	snapshot, err := client.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snapshot.Actors) != 1 || snapshot.Actors[0].ActorID != "mio" || snapshot.Actors[0].Present {
		t.Fatalf("actors = %+v", snapshot.Actors)
	}
}

func TestSnapshotRejectsContractViolations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "schema", mutate: func(b map[string]any) { b["schema_version"] = 2 }},
		{name: "world", mutate: func(b map[string]any) { b["world_id"] = "other" }},
		{name: "timeline", mutate: func(b map[string]any) { b["timeline_id"] = "other" }},
		{name: "epoch", mutate: func(b map[string]any) { b["timeline_epoch"] = "other" }},
		{name: "missing hash", mutate: func(b map[string]any) { b["state_hash"] = "" }},
		{name: "uppercase hash", mutate: func(b map[string]any) { b["state_hash"] = strings.Repeat("A", 64) }},
		{name: "short hash", mutate: func(b map[string]any) { b["state_hash"] = "abc" }},
		{name: "state seq above json max", mutate: func(b map[string]any) { b["state_seq"] = uint64(maxJSONSequence + 1) }},
		{name: "missing state", mutate: func(b map[string]any) { delete(b, "state") }},
		{name: "nil actors", mutate: func(b map[string]any) { b["state"] = map[string]any{"actors": nil} }},
		{name: "state not object", mutate: func(b map[string]any) { b["state"] = "invalid" }},
		{name: "visible actor not object", mutate: func(b map[string]any) {
			actors(b)["mio"] = "invalid"
		}},
		{name: "visible room control", mutate: func(b map[string]any) {
			actors(b)["mio"] = map[string]any{"room_id": "bad\x00"}
		}},
		{name: "visible activity control", mutate: func(b map[string]any) {
			actors(b)["mio"] = map[string]any{"activity_id": "bad\x00"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				body := validSnapshotBody()
				test.mutate(body)
				writeJSON(w, http.StatusOK, body)
			}))
			defer server.Close()

			_, err := newClient(t, server.URL, "mio").Snapshot(context.Background())
			if !errors.Is(err, ErrContract) {
				t.Fatalf("Snapshot() error = %v; want ErrContract", err)
			}
		})
	}
}

func TestSnapshotDoesNotValidateHiddenActorFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := validSnapshotBody()
		actors(body)["shiro"] = map[string]any{"room_id": "bad\x00"}
		writeJSON(w, http.StatusOK, body)
	}))
	defer server.Close()

	snapshot, err := newClient(t, server.URL, "mio").Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snapshot.Actors) != 1 || snapshot.Actors[0].ActorID != "mio" {
		t.Fatalf("actors = %+v", snapshot.Actors)
	}
}

func TestHTTPFailuresAreClassified(t *testing.T) {
	tests := []struct {
		status    int
		want      error
		retryable bool
	}{
		{status: http.StatusUnauthorized, want: ErrUnauthorized},
		{status: http.StatusForbidden, want: ErrForbidden},
		{status: http.StatusNotFound, want: ErrNotFound},
		{status: http.StatusBadRequest, want: ErrContract},
		{status: http.StatusConflict, want: ErrContract},
		{status: http.StatusInternalServerError, want: ErrUnavailable, retryable: true},
		{status: http.StatusServiceUnavailable, want: ErrUnavailable, retryable: true},
		{status: http.StatusTooManyRequests, want: ErrUnavailable, retryable: true},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("status_%d", test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, `{"error":{"code":"test"}}`)
			}))
			defer server.Close()

			_, err := newClient(t, server.URL, "mio").Snapshot(context.Background())
			if !errors.Is(err, test.want) {
				t.Fatalf("Snapshot() error = %v; want %v", err, test.want)
			}
			var readErr *ReadError
			if !errors.As(err, &readErr) {
				t.Fatalf("error = %T; want *ReadError", err)
			}
			if readErr.Status != test.status || readErr.Retryable != test.retryable {
				t.Fatalf("ReadError = %+v", readErr)
			}
		})
	}
}

func TestSnapshotDoesNotFollowRedirects(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		writeJSON(w, http.StatusOK, validSnapshotBody())
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/snapshot", http.StatusFound)
	}))
	defer redirect.Close()

	_, err := newClient(t, redirect.URL, "mio").Snapshot(context.Background())
	if !errors.Is(err, ErrContract) {
		t.Fatalf("Snapshot() error = %v; want ErrContract", err)
	}
	if targetHits.Load() != 0 {
		t.Fatalf("redirect target was called %d times", targetHits.Load())
	}
}

func TestSnapshotRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Repeat("a", maxResponseBytes+1)))
	}))
	defer server.Close()

	_, err := newClient(t, server.URL, "mio").Snapshot(context.Background())
	if !errors.Is(err, ErrContract) {
		t.Fatalf("Snapshot() error = %v; want ErrContract", err)
	}
}

func TestSnapshotHonorsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newClient(t, server.URL, "mio").Snapshot(ctx)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Snapshot() error = %v; want ErrUnavailable", err)
	}
	var readErr *ReadError
	if !errors.As(err, &readErr) || !readErr.Retryable {
		t.Fatalf("ReadError = %+v", readErr)
	}
}

func TestEventsUsesAuthenticatedPageAndHidesOutOfScopeActorPayloads(t *testing.T) {
	var gotPath, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		gotQuery = r.URL.RawQuery
		writeJSON(w, http.StatusOK, map[string]any{
			"schema_version": 1,
			"events": []map[string]any{
				eventBody(1, "txn-1", "evt-1", "debug.noop", map[string]any{"actor_id": "mio", "n": 1}),
				eventBody(2, "txn-2", "evt-2", "debug.noop", map[string]any{"actor_id": "shiro", "n": 2}),
				eventBody(3, "txn-3", "evt-3", "debug.noop", map[string]any{"note": "global"}),
			},
			"next_seq": 3,
			"done":     true,
		})
	}))
	defer server.Close()

	batch, err := newClient(t, server.URL, "mio").Events(context.Background(), 0)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if gotPath != "/v1/worlds/rencrow-home/timelines/timeline-1/events" ||
		gotQuery != "after_seq=0&limit=500" {
		t.Fatalf("request path=%q query=%q", gotPath, gotQuery)
	}
	if len(batch.Events) != 2 || batch.Skipped != 1 || batch.NextSeq != 3 || !batch.Done {
		t.Fatalf("batch = %+v", batch)
	}
	if batch.Events[0].Seq != 1 || batch.Events[0].TransactionID != "txn-1" ||
		batch.Events[0].EventID != "evt-1" || batch.Events[0].Kind != "debug.noop" ||
		string(batch.Events[0].Payload) != `{"actor_id":"mio","n":1}` {
		t.Fatalf("first event = %+v", batch.Events[0])
	}
	if batch.Events[1].Seq != 3 || string(batch.Events[1].Payload) != `{"note":"global"}` {
		t.Fatalf("second event = %+v", batch.Events[1])
	}
	for _, event := range batch.Events {
		if strings.Contains(string(event.Payload), "shiro") {
			t.Fatalf("hidden actor leaked: %s", event.Payload)
		}
	}
	if !batch.Events[0].RecordedAt.Equal(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("recorded_at = %v", batch.Events[0].RecordedAt)
	}
}

func TestEventsAcceptsGlobalAndRejectsMalformedActorPayloads(t *testing.T) {
	t.Run("global payload", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{
				"schema_version": 1,
				"events": []map[string]any{
					eventBody(1, "txn-1", "evt-1", "debug.noop", map[string]any{"note": "global"}),
				},
				"next_seq": 1,
				"done":     true,
			})
		}))
		defer server.Close()
		batch, err := newClient(t, server.URL, "mio").Events(context.Background(), 0)
		if err != nil || len(batch.Events) != 1 || batch.Skipped != 0 {
			t.Fatalf("Events() = %+v, %v", batch, err)
		}
	})

	tests := []struct {
		name    string
		payload any
	}{
		{name: "null actor id", payload: map[string]any{"actor_id": nil}},
		{name: "numeric actor id", payload: map[string]any{"actor_id": 5}},
		{name: "array payload", payload: []string{"mio"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, map[string]any{
					"schema_version": 1,
					"events": []map[string]any{
						eventBody(1, "txn-1", "evt-1", "debug.noop", test.payload),
					},
					"next_seq": 1,
					"done":     true,
				})
			}))
			defer server.Close()
			_, err := newClient(t, server.URL, "mio").Events(context.Background(), 0)
			if !errors.Is(err, ErrContract) {
				t.Fatalf("Events() error = %v; want ErrContract", err)
			}
		})
	}
}

func TestEventsRejectsGapsAndMalformedPages(t *testing.T) {
	tests := []struct {
		name   string
		after  uint64
		mutate func(map[string]any)
		want   error
	}{
		{name: "first seq gap", after: 0, mutate: func(b map[string]any) {
			b["events"] = []map[string]any{eventBody(2, "txn-2", "evt-2", "debug.noop", map[string]any{})}
			b["next_seq"] = 2
		}, want: ErrResyncRequired},
		{name: "out of order", after: 0, mutate: func(b map[string]any) {
			b["events"] = []map[string]any{
				eventBody(1, "txn-1", "evt-1", "debug.noop", map[string]any{}),
				eventBody(3, "txn-3", "evt-3", "debug.noop", map[string]any{}),
			}
			b["next_seq"] = 3
		}, want: ErrContract},
		{name: "duplicate seq", after: 0, mutate: func(b map[string]any) {
			b["events"] = []map[string]any{
				eventBody(1, "txn-1", "evt-1", "debug.noop", map[string]any{}),
				eventBody(1, "txn-1", "evt-2", "debug.noop", map[string]any{}),
			}
			b["next_seq"] = 1
		}, want: ErrContract},
		{name: "next seq mismatch", after: 0, mutate: func(b map[string]any) {
			b["events"] = []map[string]any{eventBody(1, "txn-1", "evt-1", "debug.noop", map[string]any{})}
			b["next_seq"] = 2
		}, want: ErrContract},
		{name: "next seq behind", after: 0, mutate: func(b map[string]any) {
			b["events"] = []map[string]any{eventBody(1, "txn-1", "evt-1", "debug.noop", map[string]any{})}
			b["next_seq"] = 0
		}, want: ErrContract},
		{name: "empty page next seq", after: 0, mutate: func(b map[string]any) {
			b["events"] = []map[string]any{}
			b["next_seq"] = 1
			b["done"] = true
		}, want: ErrContract},
		{name: "empty page not done", after: 0, mutate: func(b map[string]any) {
			b["events"] = []map[string]any{}
			b["next_seq"] = 0
			b["done"] = false
		}, want: ErrContract},
		{name: "schema", after: 0, mutate: func(b map[string]any) { b["schema_version"] = 2 }, want: ErrContract},
		{name: "missing events", after: 0, mutate: func(b map[string]any) { delete(b, "events") }, want: ErrContract},
		{name: "nil events", after: 0, mutate: func(b map[string]any) { b["events"] = nil }, want: ErrContract},
		{name: "next seq above json max", after: 0, mutate: func(b map[string]any) {
			b["next_seq"] = uint64(maxJSONSequence + 1)
		}, want: ErrContract},
		{name: "event seq above json max", after: 0, mutate: func(b map[string]any) {
			b["events"] = []map[string]any{eventBody(uint64(maxJSONSequence+1), "txn-1", "evt-1", "debug.noop", map[string]any{})}
			b["next_seq"] = uint64(maxJSONSequence + 1)
		}, want: ErrContract},
		{name: "empty event id", after: 0, mutate: func(b map[string]any) {
			b["events"] = []map[string]any{eventBody(1, "txn-1", "", "debug.noop", map[string]any{})}
			b["next_seq"] = 1
		}, want: ErrContract},
		{name: "empty transaction id", after: 0, mutate: func(b map[string]any) {
			b["events"] = []map[string]any{eventBody(1, "", "evt-1", "debug.noop", map[string]any{})}
			b["next_seq"] = 1
		}, want: ErrContract},
		{name: "empty kind", after: 0, mutate: func(b map[string]any) {
			b["events"] = []map[string]any{eventBody(1, "txn-1", "evt-1", "", map[string]any{})}
			b["next_seq"] = 1
		}, want: ErrContract},
		{name: "missing recorded at", after: 0, mutate: func(b map[string]any) {
			event := eventBody(1, "txn-1", "evt-1", "debug.noop", map[string]any{})
			delete(event, "recorded_at")
			b["events"] = []map[string]any{event}
			b["next_seq"] = 1
		}, want: ErrContract},
		{name: "missing payload", after: 0, mutate: func(b map[string]any) {
			event := eventBody(1, "txn-1", "evt-1", "debug.noop", map[string]any{})
			delete(event, "payload")
			b["events"] = []map[string]any{event}
			b["next_seq"] = 1
		}, want: ErrContract},
		{name: "transaction reappears", after: 0, mutate: func(b map[string]any) {
			b["events"] = []map[string]any{
				eventBody(1, "txn-a", "evt-1", "debug.noop", map[string]any{}),
				eventBody(2, "txn-b", "evt-2", "debug.noop", map[string]any{}),
				eventBody(3, "txn-a", "evt-3", "debug.noop", map[string]any{}),
			}
			b["next_seq"] = 3
		}, want: ErrContract},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				body := validEventsBody()
				test.mutate(body)
				writeJSON(w, http.StatusOK, body)
			}))
			defer server.Close()
			_, err := newClient(t, server.URL, "mio").Events(context.Background(), test.after)
			if !errors.Is(err, test.want) {
				t.Fatalf("Events() error = %v; want %v", err, test.want)
			}
		})
	}
}

func TestEventsRejectsMalformedPayloadJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"schema_version":1,"events":[{"seq":1,"transaction_id":"txn-1","event_id":"evt-1","kind":"debug.noop","payload":not-json,"recorded_at":"2026-10-21T12:00:00Z"}],"next_seq":1,"done":true}`)
	}))
	defer server.Close()

	_, err := newClient(t, server.URL, "mio").Events(context.Background(), 0)
	if !errors.Is(err, ErrContract) {
		t.Fatalf("Events() error = %v; want ErrContract", err)
	}
}

func TestReadFeedScopesActorsAndCursor(t *testing.T) {
	var eventCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/worlds/rencrow-home/timelines/timeline-1/snapshot":
			body := validSnapshotBody()
			body["state_seq"] = 3
			writeJSON(w, http.StatusOK, body)
		case "/v1/worlds/rencrow-home/timelines/timeline-1/events":
			eventCalls.Add(1)
			after := r.URL.Query().Get("after_seq")
			switch after {
			case "0":
				writeJSON(w, http.StatusOK, eventsBody(
					eventBody(1, "txn-1", "evt-1", "debug.noop", map[string]any{"actor_id": "mio"}),
					eventBody(2, "txn-2", "evt-2", "debug.noop", map[string]any{"actor_id": "shiro"}),
					eventBody(3, "txn-3", "evt-3", "debug.noop", map[string]any{"note": "global"}),
				))
			case "1":
				writeJSON(w, http.StatusOK, eventsBody(
					eventBody(2, "txn-2", "evt-2", "debug.noop", map[string]any{"actor_id": "shiro"}),
					eventBody(3, "txn-3", "evt-3", "debug.noop", map[string]any{"note": "global"}),
				))
			case "3":
				writeJSON(w, http.StatusOK, map[string]any{
					"schema_version": 1, "events": []map[string]any{}, "next_seq": 3, "done": true,
				})
			default:
				t.Fatalf("unexpected after_seq %q", after)
			}
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()
	client := newClient(t, server.URL, "mio")

	feed, err := client.ReadFeed(context.Background(), 0)
	if err != nil {
		t.Fatalf("ReadFeed(0) error = %v", err)
	}
	if feed.Cursor != 3 || feed.Skipped != 1 || len(feed.Events) != 2 {
		t.Fatalf("feed = %+v", feed)
	}
	if len(feed.Snapshot.Actors) != 1 || feed.Snapshot.Actors[0].ActorID != "mio" || !feed.Snapshot.Actors[0].Present {
		t.Fatalf("snapshot actors = %+v", feed.Snapshot.Actors)
	}

	feed, err = client.ReadFeed(context.Background(), 3)
	if err != nil || feed.Cursor != 3 || len(feed.Events) != 0 || feed.Skipped != 0 {
		t.Fatalf("ReadFeed(3) = %+v, %v", feed, err)
	}

	before := eventCalls.Load()
	_, err = client.ReadFeed(context.Background(), 4)
	if !errors.Is(err, ErrResyncRequired) {
		t.Fatalf("ReadFeed(4) error = %v; want ErrResyncRequired", err)
	}
	if eventCalls.Load() != before {
		t.Fatal("cursor-ahead feed must not request events")
	}

	feed, err = client.ReadFeed(context.Background(), 1)
	if err != nil || feed.Cursor != 3 || len(feed.Events) != 1 || feed.Skipped != 1 {
		t.Fatalf("ReadFeed(1) = %+v, %v", feed, err)
	}
	if feed.Events[0].Seq != 3 {
		t.Fatalf("ReadFeed(1) events = %+v", feed.Events)
	}
}

func TestReadFeedConsumesEveryPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/worlds/rencrow-home/timelines/timeline-1/snapshot":
			body := validSnapshotBody()
			body["state_seq"] = 3
			writeJSON(w, http.StatusOK, body)
		case "/v1/worlds/rencrow-home/timelines/timeline-1/events":
			switch r.URL.Query().Get("after_seq") {
			case "0":
				body := eventsBody(eventBody(1, "txn-1", "evt-1", "debug.noop", map[string]any{"actor_id": "mio"}))
				body["done"] = false
				writeJSON(w, http.StatusOK, body)
			case "1":
				writeJSON(w, http.StatusOK, eventsBody(
					eventBody(2, "txn-2", "evt-2", "debug.noop", map[string]any{"actor_id": "shiro"}),
					eventBody(3, "txn-3", "evt-3", "debug.noop", map[string]any{"note": "global"}),
				))
			default:
				t.Fatalf("unexpected after_seq")
			}
		default:
			t.Fatalf("unexpected path")
		}
	}))
	defer server.Close()

	feed, err := newClient(t, server.URL, "mio").ReadFeed(context.Background(), 0)
	if err != nil {
		t.Fatalf("ReadFeed() error = %v", err)
	}
	if len(feed.Events) != 2 || feed.Skipped != 1 || feed.Cursor != 3 {
		t.Fatalf("feed = %+v", feed)
	}
}

func TestReadFeedRequiresResyncWhenHistoryIsMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/worlds/rencrow-home/timelines/timeline-1/snapshot":
			body := validSnapshotBody()
			body["state_seq"] = 5
			writeJSON(w, http.StatusOK, body)
		case "/v1/worlds/rencrow-home/timelines/timeline-1/events":
			writeJSON(w, http.StatusOK, map[string]any{
				"schema_version": 1, "events": []map[string]any{}, "next_seq": 0, "done": true,
			})
		default:
			t.Fatalf("unexpected path")
		}
	}))
	defer server.Close()

	_, err := newClient(t, server.URL, "mio").ReadFeed(context.Background(), 0)
	if !errors.Is(err, ErrResyncRequired) {
		t.Fatalf("ReadFeed() error = %v; want ErrResyncRequired", err)
	}
}

func newClient(t *testing.T, baseURL string, actorIDs ...string) *Client {
	t.Helper()
	client, err := New(Config{
		BaseURL:       baseURL,
		Credential:    "read-token",
		WorldID:       "rencrow-home",
		TimelineID:    "timeline-1",
		TimelineEpoch: "epoch-1",
		ActorIDs:      actorIDs,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

func validSnapshotBody() map[string]any {
	return map[string]any{
		"schema_version": 1,
		"world_id":       "rencrow-home",
		"timeline_id":    "timeline-1",
		"timeline_epoch": "epoch-1",
		"state_seq":      4,
		"state_hash":     testHash,
		"state": map[string]any{
			"actors": map[string]any{
				"mio":   map[string]any{"room_id": "1f-living", "activity_id": "read"},
				"shiro": map[string]any{"room_id": "2f-room"},
				"kuro":  map[string]any{"room_id": "1f-kitchen"},
			},
			"activities":   map[string]any{},
			"resources":    map[string]any{},
			"reservations": []any{},
		},
	}
}

func validEventsBody() map[string]any {
	return map[string]any{
		"schema_version": 1,
		"events": []map[string]any{
			eventBody(1, "txn-1", "evt-1", "debug.noop", map[string]any{"actor_id": "mio"}),
		},
		"next_seq": 1,
		"done":     true,
	}
}

func eventsBody(events ...map[string]any) map[string]any {
	if events == nil {
		events = []map[string]any{}
	}
	var next uint64
	if len(events) > 0 {
		next = events[len(events)-1]["seq"].(uint64)
	}
	return map[string]any{
		"schema_version": 1,
		"events":         events,
		"next_seq":       next,
		"done":           true,
	}
}

func eventBody(seq uint64, transactionID, eventID, kind string, payload any) map[string]any {
	return map[string]any{
		"seq":            seq,
		"transaction_id": transactionID,
		"event_id":       eventID,
		"kind":           kind,
		"payload":        payload,
		"recorded_at":    "2026-10-21T12:00:00Z",
	}
}

func actors(body map[string]any) map[string]any {
	state := body["state"].(map[string]any)
	return state["actors"].(map[string]any)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	raw, err := json.Marshal(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
