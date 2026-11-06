package boardcommunication

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var syntheticConnectionIdentity = ConnectionIdentity{
	PrincipalID:       "principal-alpha",
	JournalInstanceID: "journal_0123456789abcdef0123456789abcdef",
}

func TestDiscoverConnectionUsesOneShotUnboundProbe(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/connection" || r.URL.RawQuery != "" {
			t.Errorf("DiscoverConnection did not use the canonical one-shot route")
		}
		if r.Header.Get("Authorization") != "Bearer "+syntheticBearer {
			t.Errorf("DiscoverConnection omitted the caller's bearer token")
		}
		if r.Header.Get("X-BOARD-Principal-ID") != "" || r.Header.Get("X-BOARD-Journal-Instance-ID") != "" {
			t.Errorf("unbound identity discovery sent identity pin headers")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Correlation-ID", syntheticCorrelation)
		_, _ = io.WriteString(w, `{"principal_id":"principal-alpha","journal_instance_id":"journal_0123456789abcdef0123456789abcdef","correlation_id":"correlation-synthetic"}`)
	}))
	defer server.Close()

	identity, err := DiscoverConnection(context.Background(), Options{
		URL: server.URL,
		TokenSource: func(context.Context) ([]byte, error) {
			return []byte(syntheticBearer), nil
		},
	})
	if err != nil {
		t.Fatalf("DiscoverConnection did not decode the canonical identity: %v", err)
	}
	if identity != syntheticConnectionIdentity || requests.Load() != 1 {
		t.Fatal("DiscoverConnection did not return one canonical identity from one request")
	}
}

func TestBoundConnectionIdentityHeadersAndRequiredPin(t *testing.T) {
	t.Run("unpinned client is refused", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		defer server.Close()
		_, err := NewClient(Options{
			URL: server.URL,
			TokenSource: func(context.Context) ([]byte, error) {
				return []byte(syntheticBearer), nil
			},
		})
		if !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("NewClient did not reject an unpinned normal connection: %v", err)
		}
	})

	t.Run("read send and event requests carry the pin", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			if r.Header.Get("X-BOARD-Principal-ID") != syntheticConnectionIdentity.PrincipalID ||
				r.Header.Get("X-BOARD-Journal-Instance-ID") != syntheticConnectionIdentity.JournalInstanceID {
				t.Errorf("%s %s omitted the pinned BOARD identity headers", r.Method, r.URL.Path)
			}
			w.Header().Set("X-Correlation-ID", syntheticCorrelation)
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/api/v1/contexts":
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"contexts":[{"id":"channel-alpha","label":"Alpha","sub":""}],"correlation_id":"correlation-synthetic"}`)
			case r.Method == http.MethodPost && r.URL.Path == "/api/v1/threads/thread-alpha/messages":
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, `{"receipt":{"message_id":"message-alpha","thread_id":"thread-alpha","stored_at":"2026-11-05T03:05:00Z","status":"stored"},"correlation_id":"correlation-synthetic"}`)
			case r.Method == http.MethodGet && r.URL.Path == "/api/events":
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "id: 41\nevent: board\ndata: {\"seq\":41,\"type\":\"message_posted\",\"topic_id\":\"thread-alpha\",\"space_id\":\"channel-alpha\",\"message_id\":\"message-alpha\",\"sender\":\"peer-alpha\",\"mentions\":[],\"recipients\":[],\"time\":\"2026-11-05T03:05:00Z\"}\n\n")
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				<-r.Context().Done()
			default:
				t.Errorf("unexpected bound identity request %s %s", r.Method, r.URL.Path)
				http.NotFound(w, r)
			}
		}))
		defer server.Close()
		client, err := NewClient(Options{
			URL:                server.URL,
			ExpectedConnection: syntheticConnectionIdentity,
			TokenSource: func(context.Context) ([]byte, error) {
				return []byte(syntheticBearer), nil
			},
		})
		if err != nil {
			t.Fatalf("NewClient rejected a valid pinned connection: %v", err)
		}
		defer client.Close()

		if _, err := client.ListContexts(context.Background()); err != nil {
			t.Fatalf("bound context read failed: %v", err)
		}
		prepared, err := client.PrepareSend(SendRequest{
			ChannelID: "channel-alpha", ThreadID: "thread-alpha", Body: "synthetic body",
			Recipients: []string{"peer-alpha"}, IdempotencyKey: "identity-send-1",
		})
		if err != nil {
			t.Fatalf("PrepareSend failed: %v", err)
		}
		if _, err := client.SendPrepared(context.Background(), prepared); err != nil {
			t.Fatalf("bound send failed: %v", err)
		}
		streamCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stream, err := client.OpenEvents(streamCtx, 40)
		if err != nil {
			t.Fatalf("bound event stream failed: %v", err)
		}
		defer stream.Close()
		if _, err := stream.Next(); err != nil {
			t.Fatalf("bound event read failed: %v", err)
		}
		if requests.Load() != 3 {
			t.Fatal("bound connection issued an unexpected number of requests")
		}
	})
}

func TestReadConnectionRequiresExactPinnedTupleWithoutRepinning(t *testing.T) {
	tests := []struct {
		name          string
		identity      string
		wantErr       error
		checkNoRebind bool
	}{
		{
			name:     "same tuple",
			identity: `{"principal_id":"principal-alpha","journal_instance_id":"journal_0123456789abcdef0123456789abcdef","correlation_id":"correlation-synthetic"}`,
		},
		{
			name:          "different tuple",
			identity:      `{"principal_id":"principal-other","journal_instance_id":"journal_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","correlation_id":"correlation-synthetic"}`,
			wantErr:       ErrConnectionIdentityMismatch,
			checkNoRebind: true,
		},
		{
			name:     "malformed tuple",
			identity: `{"principal_id":"Principal-alpha","journal_instance_id":"journal_0123456789abcdef0123456789abcdef","correlation_id":"correlation-synthetic"}`,
			wantErr:  ErrProtocol,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("X-BOARD-Principal-ID") != syntheticConnectionIdentity.PrincipalID ||
					r.Header.Get("X-BOARD-Journal-Instance-ID") != syntheticConnectionIdentity.JournalInstanceID {
					t.Errorf("request did not retain the originally pinned tuple")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Correlation-ID", syntheticCorrelation)
				switch r.URL.Path {
				case "/api/v1/connection":
					_, _ = io.WriteString(w, test.identity)
				case "/api/v1/contexts":
					_, _ = io.WriteString(w, `{"contexts":[],"correlation_id":"correlation-synthetic"}`)
				default:
					t.Errorf("unexpected identity verification route")
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client, err := newPinnedClient(Options{
				URL: server.URL,
				TokenSource: func(context.Context) ([]byte, error) {
					return []byte(syntheticBearer), nil
				},
			})
			if err != nil {
				t.Fatalf("NewClient rejected a valid pinned tuple: %v", err)
			}
			defer client.Close()

			identity, err := client.ReadConnection(context.Background())
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("ReadConnection returned %v, want fixed category %v", err, test.wantErr)
				}
				if strings.Contains(err.Error(), "principal-other") || strings.Contains(err.Error(), "journal_aaaaaaaa") {
					t.Fatal("ReadConnection exposed an untrusted identity tuple in its error")
				}
			} else if err != nil || identity != syntheticConnectionIdentity {
				t.Fatalf("ReadConnection did not return the matching tuple: %v", err)
			}
			if test.checkNoRebind {
				if _, err := client.ListContexts(context.Background()); err != nil {
					t.Fatalf("original pin was not retained after mismatch: %v", err)
				}
				if requests.Load() != 2 {
					t.Fatal("identity mismatch caused an unexpected request or repin attempt")
				}
			} else if requests.Load() != 1 {
				t.Fatal("identity read caused an unexpected extra request")
			}
		})
	}
}

func TestNewClientValidatesAndPreservesConnectionPin(t *testing.T) {
	pin := ConnectionIdentity{
		PrincipalID:       "principal-alpha",
		JournalInstanceID: "journal_0123456789abcdef0123456789abcdef",
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("X-BOARD-Principal-ID") != pin.PrincipalID ||
			r.Header.Get("X-BOARD-Journal-Instance-ID") != pin.JournalInstanceID {
			t.Errorf("client changed the exact pinned identity header values")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"contexts":[],"correlation_id":"correlation-synthetic"}`)
	}))
	defer server.Close()
	tokenSource := func(context.Context) ([]byte, error) { return []byte(syntheticBearer), nil }
	client, err := NewClient(Options{URL: server.URL, TokenSource: tokenSource, ExpectedConnection: pin})
	if err != nil {
		t.Fatalf("NewClient rejected canonical lowercase journal suffix: %v", err)
	}
	if _, err := client.ListContexts(context.Background()); err != nil {
		t.Fatalf("pinned request failed: %v", err)
	}
	client.Close()
	if requests.Load() != 1 {
		t.Fatal("pin validation made an unexpected network request")
	}

	invalid := []ConnectionIdentity{
		{PrincipalID: "", JournalInstanceID: pin.JournalInstanceID},
		{PrincipalID: "Principal-alpha", JournalInstanceID: pin.JournalInstanceID},
		{PrincipalID: "_principal", JournalInstanceID: pin.JournalInstanceID},
		{PrincipalID: strings.Repeat("a", 65), JournalInstanceID: pin.JournalInstanceID},
		{PrincipalID: pin.PrincipalID, JournalInstanceID: "Journal_0123456789abcdef0123456789abcdef"},
		{PrincipalID: pin.PrincipalID, JournalInstanceID: "journal_0123456789abcdef0123456789abcdeg"},
		{PrincipalID: pin.PrincipalID, JournalInstanceID: "journal_0123456789abcdef0123456789abcde"},
	}
	for _, identity := range invalid {
		_, err := NewClient(Options{URL: server.URL, TokenSource: tokenSource, ExpectedConnection: identity})
		if !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatal("NewClient accepted a malformed pinned connection identity")
		}
	}
	if requests.Load() != 1 {
		t.Fatal("invalid pin validation caused network I/O")
	}
}

func TestUppercaseJournalSuffixIsRejectedByPinAndDiscovery(t *testing.T) {
	uppercaseID := "journal_0123456789ABCDEF0123456789ABCDEF"
	t.Run("constructor pin", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		defer server.Close()
		_, err := NewClient(Options{
			URL:                server.URL,
			TokenSource:        func(context.Context) ([]byte, error) { return []byte(syntheticBearer), nil },
			ExpectedConnection: ConnectionIdentity{PrincipalID: "principal-alpha", JournalInstanceID: uppercaseID},
		})
		if !errors.Is(err, ErrInvalidConfiguration) {
			t.Fatalf("NewClient did not reject uppercase journal suffix: %v", err)
		}
	})

	t.Run("discovered identity", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Correlation-ID", syntheticCorrelation)
			_, _ = io.WriteString(w, `{"principal_id":"principal-alpha","journal_instance_id":"`+uppercaseID+`","correlation_id":"correlation-synthetic"}`)
		}))
		defer server.Close()
		_, err := DiscoverConnection(context.Background(), Options{
			URL:         server.URL,
			TokenSource: func(context.Context) ([]byte, error) { return []byte(syntheticBearer), nil },
		})
		if !errors.Is(err, ErrProtocol) || requests.Load() != 1 {
			t.Fatalf("discovery did not reject uppercase journal suffix after one probe: %v", err)
		}
	})
}

func TestIdentityMismatchResponsesHaveDedicatedCategory(t *testing.T) {
	for _, operation := range []string{"context read", "send", "events"} {
		t.Run(operation, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Correlation-ID", syntheticCorrelation)
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"error":"connection_identity_mismatch","correlation_id":"correlation-synthetic"}`)
			}))
			defer server.Close()
			client, err := newPinnedClient(Options{
				URL: server.URL,
				TokenSource: func(context.Context) ([]byte, error) {
					return []byte(syntheticBearer), nil
				},
			})
			if err != nil {
				t.Fatalf("NewClient rejected a valid pinned tuple: %v", err)
			}
			defer client.Close()

			var operationErr error
			switch operation {
			case "context read":
				_, operationErr = client.ListContexts(context.Background())
			case "send":
				prepared, prepareErr := client.PrepareSend(SendRequest{
					ChannelID: "channel-alpha", ThreadID: "thread-alpha", Body: "synthetic body",
					Recipients: []string{"peer-alpha"}, IdempotencyKey: "identity-conflict-1",
				})
				if prepareErr != nil {
					t.Fatalf("PrepareSend failed: %v", prepareErr)
				}
				_, operationErr = client.SendPrepared(context.Background(), prepared)
			case "events":
				_, operationErr = client.OpenEvents(context.Background(), 0)
			}
			if !errors.Is(operationErr, ErrConnectionIdentityMismatch) || requests.Load() != 1 {
				t.Fatalf("identity fence did not return the dedicated fixed category: %v", operationErr)
			}
			if operationErr.Error() != string(ErrorCodeConnectionIdentityMismatch) {
				t.Fatal("identity fence exposed response details instead of the fixed error category")
			}
		})
	}
}

func TestIdentityMismatchErrorEnvelopeMustBeCanonical(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "duplicate error field", body: `{"error":"other","error":"connection_identity_mismatch"}`},
		{name: "correlation mismatch", body: `{"error":"connection_identity_mismatch","correlation_id":"correlation-other"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Correlation-ID", syntheticCorrelation)
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			client, err := newPinnedClient(Options{
				URL: server.URL,
				TokenSource: func(context.Context) ([]byte, error) {
					return []byte(syntheticBearer), nil
				},
			})
			if err != nil {
				t.Fatalf("NewClient rejected a valid pinned tuple: %v", err)
			}
			defer client.Close()
			_, err = client.ListContexts(context.Background())
			if !errors.Is(err, ErrProtocol) || errors.Is(err, ErrConnectionIdentityMismatch) {
				t.Fatalf("ambiguous identity error envelope got the wrong category: %v", err)
			}
		})
	}
}
