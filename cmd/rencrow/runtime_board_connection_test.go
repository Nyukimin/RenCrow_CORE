package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
)

const boardConnectionTestBearer = "synthetic-board-token"

var boardConnectionTestIdentity = boardcommunication.ConnectionIdentity{
	PrincipalID:       "principal-alpha",
	JournalInstanceID: "journal_0123456789abcdef0123456789abcdef",
}

func boardConnectionTestPin() eventstore.BoardConnectionPin {
	return eventstore.BoardConnectionPin{
		OwnerID:           "owner-alpha",
		JournalInstanceID: boardConnectionTestIdentity.JournalInstanceID,
		PrincipalID:       boardConnectionTestIdentity.PrincipalID,
		CutoverAt:         time.Date(2026, 11, 5, 3, 4, 5, 0, time.UTC),
	}
}

func newBoardConnectionTestStore(t *testing.T) *eventstore.SQLiteStore {
	t.Helper()
	store, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "event-store.sqlite"))
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})
	return store
}

func boardConnectionTestSettings(serverURL string) boardConnectionSettings {
	return boardConnectionSettings{
		OwnerID:             "owner-alpha",
		ExpectedPrincipalID: boardConnectionTestIdentity.PrincipalID,
		AllowedChannelIDs:   []string{"channel-alpha", "channel-beta"},
		CutoverAt:           boardConnectionTestPin().CutoverAt,
		Transport: boardcommunication.Options{
			URL: serverURL,
			TokenSource: func(context.Context) ([]byte, error) {
				return []byte(boardConnectionTestBearer), nil
			},
		},
	}
}

func writeBoardConnectionIdentity(w http.ResponseWriter, identity boardcommunication.ConnectionIdentity, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Correlation-ID", "correlation-synthetic")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"principal_id":        identity.PrincipalID,
		"journal_instance_id": identity.JournalInstanceID,
		"correlation_id":      "correlation-synthetic",
	})
}

func writeBoardConnectionContexts(w http.ResponseWriter, ids []string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Correlation-ID", "correlation-synthetic")
	w.WriteHeader(status)
	contexts := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		contexts = append(contexts, map[string]string{"id": id, "label": id, "sub": "synthetic"})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"contexts": contexts, "correlation_id": "correlation-synthetic"})
}

func assertBoardConnectionRequest(t *testing.T, r *http.Request, wantBound bool) {
	t.Helper()
	if r.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", r.Method)
	}
	if r.Header.Get("Authorization") != "Bearer "+boardConnectionTestBearer {
		t.Errorf("%s %s omitted synthetic bearer authorization", r.Method, r.URL.Path)
	}
	wantPrincipal, wantJournal := "", ""
	if wantBound {
		wantPrincipal = boardConnectionTestIdentity.PrincipalID
		wantJournal = boardConnectionTestIdentity.JournalInstanceID
	}
	if r.Header.Get("X-BOARD-Principal-ID") != wantPrincipal || r.Header.Get("X-BOARD-Journal-Instance-ID") != wantJournal {
		t.Errorf("%s %s pin headers = (%q, %q), want (%q, %q)", r.Method, r.URL.Path,
			r.Header.Get("X-BOARD-Principal-ID"), r.Header.Get("X-BOARD-Journal-Instance-ID"), wantPrincipal, wantJournal)
	}
}

func TestOpenBoardCommunicationConnectionDiscoversOnceThenPinsAfterBoundContextRead(t *testing.T) {
	store := newBoardConnectionTestStore(t)
	var mu sync.Mutex
	var sequence []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/connection":
			bound := r.Header.Get("X-BOARD-Principal-ID") != ""
			assertBoardConnectionRequest(t, r, bound)
			mu.Lock()
			if bound {
				sequence = append(sequence, "bound_readback")
			} else {
				sequence = append(sequence, "unbound_discovery")
			}
			mu.Unlock()
			writeBoardConnectionIdentity(w, boardConnectionTestIdentity, http.StatusOK)
		case "/api/v1/contexts":
			assertBoardConnectionRequest(t, r, true)
			if pin, found, err := store.ReadBoardConnectionPin(r.Context()); err != nil || found {
				t.Errorf("pin became durable before context verification: %#v, %t, %v", pin, found, err)
			}
			mu.Lock()
			sequence = append(sequence, "bound_contexts")
			mu.Unlock()
			writeBoardConnectionContexts(w, []string{"channel-alpha", "channel-beta"}, http.StatusOK)
		default:
			t.Errorf("unexpected synthetic BOARD route %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, pin, err := openBoardCommunicationConnection(context.Background(), store, boardConnectionTestSettings(server.URL))
	if err != nil {
		t.Fatalf("openBoardCommunicationConnection(first connection): %v", err)
	}
	if client == nil {
		t.Fatal("openBoardCommunicationConnection returned a nil client")
	}
	defer client.Close()
	if pin != boardConnectionTestPin() {
		t.Fatalf("returned pin = %#v, want configured owner and discovered identity", pin)
	}
	got, found, err := store.ReadBoardConnectionPin(context.Background())
	if err != nil || !found || got != pin {
		t.Fatalf("ReadBoardConnectionPin() = %#v, %t, %v; want durable returned pin", got, found, err)
	}
	mu.Lock()
	gotSequence := append([]string(nil), sequence...)
	mu.Unlock()
	wantSequenceWithoutReadback := []string{"unbound_discovery", "bound_contexts"}
	wantSequenceWithReadback := []string{"unbound_discovery", "bound_readback", "bound_contexts"}
	if !reflect.DeepEqual(gotSequence, wantSequenceWithoutReadback) && !reflect.DeepEqual(gotSequence, wantSequenceWithReadback) {
		t.Fatalf("BOARD request sequence = %#v; want one unbound discover followed by bound verification", gotSequence)
	}
}

func TestOpenBoardCommunicationConnectionDoesNotPinMismatchedOrUnverifiedIdentity(t *testing.T) {
	t.Run("discovered principal mismatch", func(t *testing.T) {
		store := newBoardConnectionTestStore(t)
		var requests int
		other := boardConnectionTestIdentity
		other.PrincipalID = "principal-other"
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			if r.URL.Path != "/api/v1/connection" {
				t.Errorf("principal mismatch made unexpected request to %s", r.URL.Path)
			}
			assertBoardConnectionRequest(t, r, false)
			writeBoardConnectionIdentity(w, other, http.StatusOK)
		}))
		defer server.Close()

		client, _, err := openBoardCommunicationConnection(context.Background(), store, boardConnectionTestSettings(server.URL))
		if err == nil || client != nil {
			t.Fatalf("mismatched principal returned client=%v error=%v; want closed failure", client != nil, err)
		}
		if requests != 1 {
			t.Fatalf("requests = %d; want only one unbound discovery", requests)
		}
		if _, found, readErr := store.ReadBoardConnectionPin(context.Background()); readErr != nil || found {
			t.Fatalf("mismatched discovery left pin found=%t error=%v", found, readErr)
		}
	})

	for _, scenario := range []struct {
		name   string
		status int
		ids    []string
	}{
		{name: "context read unavailable", status: http.StatusServiceUnavailable, ids: []string{"channel-alpha", "channel-beta"}},
		{name: "configured context missing", status: http.StatusOK, ids: []string{"channel-alpha"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			store := newBoardConnectionTestStore(t)
			var sequence []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/connection":
					assertBoardConnectionRequest(t, r, false)
					sequence = append(sequence, "unbound_discovery")
					writeBoardConnectionIdentity(w, boardConnectionTestIdentity, http.StatusOK)
				case "/api/v1/contexts":
					assertBoardConnectionRequest(t, r, true)
					sequence = append(sequence, "bound_contexts")
					writeBoardConnectionContexts(w, scenario.ids, scenario.status)
				default:
					t.Errorf("unexpected synthetic BOARD route %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			client, _, err := openBoardCommunicationConnection(context.Background(), store, boardConnectionTestSettings(server.URL))
			if err == nil || client != nil {
				t.Fatalf("unverified contexts returned client=%v error=%v; want closed failure", client != nil, err)
			}
			if !reflect.DeepEqual(sequence, []string{"unbound_discovery", "bound_contexts"}) {
				t.Fatalf("BOARD request sequence = %#v; want discovery followed by bound context check", sequence)
			}
			if _, found, readErr := store.ReadBoardConnectionPin(context.Background()); readErr != nil || found {
				t.Fatalf("failed context verification left pin found=%t error=%v", found, readErr)
			}
		})
	}
}

func TestOpenBoardCommunicationConnectionUsesOnlyDurablePinAfterRestart(t *testing.T) {
	store := newBoardConnectionTestStore(t)
	want := boardConnectionTestPin()
	if _, err := store.PinBoardConnection(context.Background(), want); err != nil {
		t.Fatalf("seed durable test pin: %v", err)
	}
	var sequence []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/connection":
			assertBoardConnectionRequest(t, r, true)
			sequence = append(sequence, "bound_connection")
			writeBoardConnectionIdentity(w, boardConnectionTestIdentity, http.StatusOK)
		case "/api/v1/contexts":
			assertBoardConnectionRequest(t, r, true)
			sequence = append(sequence, "bound_contexts")
			writeBoardConnectionContexts(w, []string{"channel-alpha", "channel-beta"}, http.StatusOK)
		default:
			t.Errorf("existing pin used unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, got, err := openBoardCommunicationConnection(context.Background(), store, boardConnectionTestSettings(server.URL))
	if err != nil {
		t.Fatalf("openBoardCommunicationConnection(existing pin): %v", err)
	}
	if client == nil {
		t.Fatal("existing pin returned a nil client")
	}
	defer client.Close()
	if got != want || !reflect.DeepEqual(sequence, []string{"bound_connection", "bound_contexts"}) {
		t.Fatalf("existing pin result=%#v requests=%#v; want exact pin and bound-only reads", got, sequence)
	}
}

func TestOpenBoardCommunicationConnectionRejectsStoredPinConfigurationMismatchBeforeNetwork(t *testing.T) {
	for _, change := range []struct {
		name string
		edit func(*boardConnectionSettings)
	}{
		{name: "owner", edit: func(settings *boardConnectionSettings) { settings.OwnerID = "owner-other" }},
		{name: "principal", edit: func(settings *boardConnectionSettings) { settings.ExpectedPrincipalID = "principal-other" }},
		{name: "cutover", edit: func(settings *boardConnectionSettings) { settings.CutoverAt = settings.CutoverAt.Add(time.Second) }},
	} {
		t.Run(change.name, func(t *testing.T) {
			store := newBoardConnectionTestStore(t)
			want := boardConnectionTestPin()
			if _, err := store.PinBoardConnection(context.Background(), want); err != nil {
				t.Fatalf("seed durable test pin: %v", err)
			}
			var requests int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				http.Error(w, "unexpected request", http.StatusInternalServerError)
			}))
			defer server.Close()
			settings := boardConnectionTestSettings(server.URL)
			change.edit(&settings)
			client, _, err := openBoardCommunicationConnection(context.Background(), store, settings)
			if err == nil || client != nil || requests != 0 {
				t.Fatalf("configuration mismatch returned client=%v error=%v requests=%d; want pre-network failure", client != nil, err, requests)
			}
			if err.Error() == "" {
				t.Fatal("configuration mismatch did not explain the failure")
			}
			if got, found, readErr := store.ReadBoardConnectionPin(context.Background()); readErr != nil || !found || got != want {
				t.Fatalf("configuration mismatch changed durable pin: %#v %t %v", got, found, readErr)
			}
		})
	}
}

func TestOpenBoardCommunicationConnectionUsesPrincipalScopedPinsOnlyWhenExplicit(t *testing.T) {
	store := newBoardConnectionTestStore(t)
	identities := map[string]boardcommunication.ConnectionIdentity{
		"shiro-token":  {PrincipalID: "shiro", JournalInstanceID: "journal_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		"midori-token": {PrincipalID: "midori", JournalInstanceID: "journal_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		"kuro-token":   {PrincipalID: "kuro", JournalInstanceID: "journal_cccccccccccccccccccccccccccccccc"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		identity, ok := identities[token]
		if !ok {
			t.Errorf("unexpected responder token source %q", r.Header.Get("Authorization"))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-BOARD-Principal-ID") != "" &&
			(r.Header.Get("X-BOARD-Principal-ID") != identity.PrincipalID || r.Header.Get("X-BOARD-Journal-Instance-ID") != identity.JournalInstanceID) {
			t.Errorf("responder request used another principal's pin headers")
			http.Error(w, "wrong pin", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/api/v1/connection":
			writeBoardConnectionIdentity(w, identity, http.StatusOK)
		case "/api/v1/contexts":
			if r.Header.Get("X-BOARD-Principal-ID") != identity.PrincipalID || r.Header.Get("X-BOARD-Journal-Instance-ID") != identity.JournalInstanceID {
				t.Errorf("responder context verification omitted its own bound pin")
				http.Error(w, "unbound", http.StatusForbidden)
				return
			}
			writeBoardConnectionContexts(w, []string{"channel-alpha"}, http.StatusOK)
		default:
			t.Errorf("unexpected responder connection route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	for _, actor := range []string{"shiro", "midori", "kuro"} {
		actor := actor
		settings := boardConnectionTestSettings(server.URL)
		settings.ExpectedPrincipalID = actor
		settings.AllowedChannelIDs = []string{"channel-alpha"}
		settings.UsePrincipalScopedPins = true
		settings.Transport.TokenSource = func(context.Context) ([]byte, error) { return []byte(actor + "-token"), nil }
		client, pin, err := openBoardCommunicationConnection(context.Background(), store, settings)
		if err != nil {
			t.Fatalf("openBoardCommunicationConnection(%s): %v", actor, err)
		}
		client.Close()
		if pin.PrincipalID != actor {
			t.Fatalf("responder %s received principal pin %q", actor, pin.PrincipalID)
		}
	}

	for actor, journal := range map[string]string{
		"shiro": "journal_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "midori": "journal_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"kuro": "journal_cccccccccccccccccccccccccccccccc",
	} {
		pin, found, err := store.ReadBoardConnectionPinForPrincipal(context.Background(), actor)
		if err != nil || !found || pin.PrincipalID != actor || pin.JournalInstanceID != journal {
			t.Fatalf("ReadBoardConnectionPinForPrincipal(%s) = %#v, %t, %v", actor, pin, found, err)
		}
	}
}

func TestOpenBoardCommunicationConnectionDoesNotRepinAfterBoundReadbackAuthenticationFailure(t *testing.T) {
	store := newBoardConnectionTestStore(t)
	want := boardConnectionTestPin()
	if _, err := store.PinBoardConnection(context.Background(), want); err != nil {
		t.Fatalf("seed durable test pin: %v", err)
	}
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/v1/connection" {
			t.Errorf("failed connection readback made unexpected request to %s", r.URL.Path)
		}
		assertBoardConnectionRequest(t, r, true)
		http.Error(w, "synthetic unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()

	client, _, err := openBoardCommunicationConnection(context.Background(), store, boardConnectionTestSettings(server.URL))
	if err == nil || client != nil || requests != 1 {
		t.Fatalf("failed readback returned client=%v error=%v requests=%d; want fail closed", client != nil, err, requests)
	}
	if got, found, readErr := store.ReadBoardConnectionPin(context.Background()); readErr != nil || !found || got != want {
		t.Fatalf("failed readback changed durable pin: %#v %t %v", got, found, readErr)
	}
}
