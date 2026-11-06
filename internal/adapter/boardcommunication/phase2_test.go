package boardcommunication

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const syntheticCorrelation = "correlation-synthetic"

func TestPhase2CanonicalOperationRoutes(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+syntheticBearer || r.Header.Get("Idempotency-Key") != "" {
			t.Errorf("canonical read or inbox operation used unexpected request credentials")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Correlation-ID", syntheticCorrelation)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/threads":
			if r.URL.Query().Get("channel_id") != "channel-alpha" || r.URL.Query().Get("limit") != "3" || r.URL.Query().Get("cursor") != "thread-before" {
				t.Errorf("ListThreads did not use the bounded canonical query")
			}
			_, _ = io.WriteString(w, `{"threads":[{"thread_id":"thread-alpha","channel_id":"channel-alpha","title":"Alpha","status":"open","updated_at":"2026-11-05T03:05:05Z"}],"next_cursor":"","correlation_id":"correlation-synthetic"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/inbox/item-alpha/receipt":
			_, _ = io.WriteString(w, `{"item_id":"item-alpha","message_id":"message-alpha","ack_receipt":"synthetic-private-ack-receipt","correlation_id":"correlation-synthetic"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/inbox/item-alpha/processing-claim":
			var request ClaimRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Receipt != "synthetic-private-ack-receipt" ||
				request.ClaimID != "claim-alpha" || request.LeaseSeconds != 75 || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("ClaimProcessing did not send the explicit canonical request")
			}
			_, _ = io.WriteString(w, `{"message_id":"message-alpha","claim_id":"claim-alpha","claim_receipt":"synthetic-private-claim-receipt","status":"claimed","lease_until":"2026-11-05T03:06:15Z","can_process":true,"correlation_id":"correlation-synthetic"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/inbox/item-alpha/ack":
			var request ackRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Receipt != "synthetic-private-ack-receipt" ||
				r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("AckInbox did not send the explicit canonical request")
			}
			_, _ = io.WriteString(w, `{"item_id":"item-alpha","status":"acked","correlation_id":"correlation-synthetic"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/messages/message-alpha/receipt":
			_, _ = io.WriteString(w, `{"receipt":{"message_id":"message-alpha","thread_id":"thread-alpha","stored_at":"2026-11-05T03:04:05Z","status":"stored"},"correlation_id":"correlation-synthetic"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/messages/message-alpha/status":
			_, _ = io.WriteString(w, `{"message_id":"message-alpha","recipients":[{"message_id":"message-alpha","principal_id":"peer-alpha","stored_at":"2026-11-05T03:04:05Z","notification_attempted_at":null,"notification_accepted_at":null,"body_read_at":null,"processing_started_at":null,"reply_observed_at":null}],"correlation_id":"correlation-synthetic"}`)
		default:
			t.Errorf("unexpected canonical operation route")
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := newPinnedClient(Options{
		URL:         server.URL,
		TokenSource: func(context.Context) ([]byte, error) { return []byte(syntheticBearer), nil },
	})
	if err != nil {
		t.Fatal("NewClient rejected the synthetic loopback URL")
	}
	defer client.Close()

	threads, err := client.ListThreads(context.Background(), "channel-alpha", PageRequest{Limit: 3, Cursor: "thread-before"})
	requireAvailable(t, "ListThreads", err)
	if len(threads.Threads) != 1 || threads.Threads[0].ThreadID != "thread-alpha" {
		t.Fatal("ListThreads did not decode the canonical BOARD summary")
	}
	ack, err := client.InboxReceipt(context.Background(), "item-alpha")
	requireAvailable(t, "InboxReceipt", err)
	if ack.ItemID != "item-alpha" || ack.MessageID != "message-alpha" || ack.AckReceipt == "" {
		t.Fatal("InboxReceipt did not preserve the explicit private ACK receipt")
	}
	claim, err := client.ClaimProcessing(context.Background(), "item-alpha", ClaimRequest{
		Receipt: "synthetic-private-ack-receipt", ClaimID: "claim-alpha", LeaseSeconds: 75,
	})
	requireAvailable(t, "ClaimProcessing", err)
	if claim.MessageID != "message-alpha" || claim.ClaimID != "claim-alpha" || claim.ClaimReceipt == "" || !claim.CanProcess {
		t.Fatal("ClaimProcessing did not preserve the canonical claim result")
	}
	if err := client.AckInbox(context.Background(), "item-alpha", "synthetic-private-ack-receipt"); err != nil {
		t.Fatal("AckInbox did not accept the canonical ACK response")
	}
	receipt, err := client.MessageReceipt(context.Background(), "message-alpha")
	requireAvailable(t, "MessageReceipt", err)
	if receipt.MessageID != "message-alpha" || receipt.ThreadID != "thread-alpha" || receipt.Status != "stored" {
		t.Fatal("MessageReceipt did not preserve the canonical stored receipt")
	}
	statuses, err := client.MessageStatus(context.Background(), "message-alpha")
	requireAvailable(t, "MessageStatus", err)
	if len(statuses) != 1 || statuses[0].PrincipalID != "peer-alpha" || statuses[0].MessageID != "message-alpha" {
		t.Fatal("MessageStatus did not decode the canonical recipient view")
	}
	if requests.Load() != 6 {
		t.Fatal("explicit BOARD operations issued an unexpected number of HTTP requests")
	}
}

func TestClaimProcessingAcceptsAlreadyProcessedExistingClaim(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/inbox/item-alpha/processing-claim" {
			t.Errorf("ClaimProcessing did not use the canonical route")
		}
		var request ClaimRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Receipt != "synthetic-private-ack-receipt" || request.ClaimID != "claim-current" {
			t.Errorf("ClaimProcessing did not send the explicit caller-owned claim")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Correlation-ID", syntheticCorrelation)
		_, _ = io.WriteString(w, `{"message_id":"message-alpha","claim_id":"claim-existing","status":"already_processed","completed_at":"2026-11-05T03:06:15Z","correlation_id":"correlation-synthetic"}`)
	}))
	defer server.Close()
	client, err := newPinnedClient(Options{
		URL:         server.URL,
		TokenSource: func(context.Context) ([]byte, error) { return []byte(syntheticBearer), nil },
	})
	if err != nil {
		t.Fatal("NewClient rejected the synthetic loopback URL")
	}
	defer client.Close()

	result, err := client.ClaimProcessing(context.Background(), "item-alpha", ClaimRequest{
		Receipt: "synthetic-private-ack-receipt", ClaimID: "claim-current", LeaseSeconds: 75,
	})
	requireAvailable(t, "ClaimProcessing already_processed", err)
	if result.MessageID != "message-alpha" || result.ClaimID != "claim-existing" || result.Status != "already_processed" ||
		result.CompletedAt == nil || result.CompletedAt.IsZero() || result.ClaimReceipt != "" || result.LeaseUntil != nil {
		t.Fatal("ClaimProcessing did not preserve the canonical completed claim result")
	}
	if requests.Load() != 1 {
		t.Fatal("one explicit claim request caused an unexpected number of HTTP requests")
	}
}

func TestPhase2RejectsInvalidPagesAndMismatchedReadbacks(t *testing.T) {
	var requests atomic.Int32
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Correlation-ID", syntheticCorrelation)
		switch r.URL.Path {
		case "/api/v1/threads/thread-alpha":
			_, _ = io.WriteString(w, `{"thread":{"id":"thread-other","space":"channel-alpha","title":"Alpha","summary":"summary","status":"open","decision":"","participants":[],"created_at":"2026-11-05T03:04:05Z","updated_at":"2026-11-05T03:05:05Z","messages":[]},"next_cursor":"","correlation_id":"correlation-synthetic"}`)
		case "/api/v1/inbox/item-alpha/receipt":
			_, _ = io.WriteString(w, `{"item_id":"item-other","message_id":"message-alpha","ack_receipt":"synthetic-private-ack-receipt","correlation_id":"correlation-synthetic"}`)
		case "/api/v1/messages/message-alpha/receipt":
			_, _ = io.WriteString(w, `{"receipt":{"message_id":"message-other","thread_id":"thread-alpha","stored_at":"2026-11-05T03:04:05Z","status":"stored"},"correlation_id":"correlation-synthetic"}`)
		case "/api/v1/messages/message-alpha/status":
			_, _ = io.WriteString(w, `{"message_id":"message-other","recipients":[],"correlation_id":"correlation-synthetic"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := newPinnedClient(Options{
		URL: server.URL,
		TokenSource: func(context.Context) ([]byte, error) {
			tokenCalls.Add(1)
			return []byte(syntheticBearer), nil
		},
	})
	if err != nil {
		t.Fatal("NewClient rejected the synthetic loopback URL")
	}
	defer client.Close()
	for _, page := range []PageRequest{{Limit: 0}, {Limit: 101}, {Limit: 2, Cursor: "../thread-alpha"}} {
		_, err := client.Inbox(context.Background(), page)
		requireCategory(t, err, ErrInvalidRequest)
	}
	if _, err := client.ReadThread(context.Background(), "thread-alpha", PageRequest{Limit: 1}); !errors.Is(err, ErrProtocol) {
		t.Fatal("ReadThread accepted a different thread than requested")
	}
	if _, err := client.InboxReceipt(context.Background(), "item-alpha"); !errors.Is(err, ErrProtocol) {
		t.Fatal("InboxReceipt accepted a different item than requested")
	}
	if _, err := client.MessageReceipt(context.Background(), "message-alpha"); !errors.Is(err, ErrProtocol) {
		t.Fatal("MessageReceipt accepted a different message than requested")
	}
	if _, err := client.MessageStatus(context.Background(), "message-alpha"); !errors.Is(err, ErrProtocol) {
		t.Fatal("MessageStatus accepted a different message than requested")
	}
	if requests.Load() != 4 || tokenCalls.Load() != 4 {
		t.Fatal("invalid page inputs reached BOARD or a readback caused an extra operation")
	}
}

func TestPhase2PostFailuresAreUnknownAndRedactedWithoutRetry(t *testing.T) {
	for _, route := range []string{
		"/api/v1/inbox/item-alpha/processing-claim",
		"/api/v1/inbox/item-alpha/ack",
	} {
		t.Run(route, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != route || r.Method != http.MethodPost {
					t.Errorf("POST used an unexpected canonical route")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"error":"synthetic raw detail synthetic-private-ack-receipt"}`)
			}))
			defer server.Close()
			client, err := newPinnedClient(Options{
				URL:         server.URL,
				TokenSource: func(context.Context) ([]byte, error) { return []byte(syntheticBearer), nil },
			})
			if err != nil {
				t.Fatal("NewClient rejected the synthetic loopback URL")
			}
			defer client.Close()
			var operationErr error
			if strings.HasSuffix(route, "/ack") {
				operationErr = client.AckInbox(context.Background(), "item-alpha", "synthetic-private-ack-receipt")
			} else {
				_, operationErr = client.ClaimProcessing(context.Background(), "item-alpha", ClaimRequest{Receipt: "synthetic-private-ack-receipt"})
			}
			requireCategory(t, operationErr, ErrUnknownOutcome)
			if strings.Contains(operationErr.Error(), "synthetic") || requests.Load() != 1 {
				t.Fatal("POST exposed wire detail or retried an unknown outcome")
			}
		})
	}
}

func TestPhase2MalformedMutationSuccessIsUnknown(t *testing.T) {
	tests := []struct {
		name   string
		route  string
		invoke func(*Client) error
		body   string
	}{
		{
			name:  "ACK item mismatch",
			route: "/api/v1/inbox/item-alpha/ack",
			invoke: func(client *Client) error {
				return client.AckInbox(context.Background(), "item-alpha", "synthetic-private-ack-receipt")
			},
			body: `{"item_id":"item-other","status":"acked","correlation_id":"correlation-synthetic"}`,
		},
		{
			name:  "claim status mismatch",
			route: "/api/v1/inbox/item-alpha/processing-claim",
			invoke: func(client *Client) error {
				_, err := client.ClaimProcessing(context.Background(), "item-alpha", ClaimRequest{Receipt: "synthetic-private-ack-receipt", ClaimID: "claim-alpha"})
				return err
			},
			body: `{"message_id":"message-alpha","claim_id":"claim-alpha","claim_receipt":"synthetic-private-claim-receipt","status":"unknown","lease_until":"2026-11-05T03:06:15Z","can_process":true,"correlation_id":"correlation-synthetic"}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Correlation-ID", syntheticCorrelation)
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			client, err := newPinnedClient(Options{
				URL:         server.URL,
				TokenSource: func(context.Context) ([]byte, error) { return []byte(syntheticBearer), nil },
			})
			if err != nil {
				t.Fatal("NewClient rejected the synthetic loopback URL")
			}
			defer client.Close()
			err = test.invoke(client)
			requireCategory(t, err, ErrUnknownOutcome)
			if requests.Load() != 1 || strings.Contains(err.Error(), "synthetic") {
				t.Fatal("malformed mutation success was retried or exposed wire detail")
			}
		})
	}
}

func TestPhase2ReadbackRejectsCorrelationHeaderMismatch(t *testing.T) {
	client, _ := newFakeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Correlation-ID", "correlation-other")
		_, _ = io.WriteString(w, `{"item_id":"item-alpha","message_id":"message-alpha","ack_receipt":"synthetic-private-ack-receipt","correlation_id":"correlation-synthetic"}`)
	}))
	defer client.Close()
	_, err := client.InboxReceipt(context.Background(), "item-alpha")
	requireCategory(t, err, ErrProtocol)
}

func TestOpenEventsSkipsKeepaliveAndClientCloseReleasesStream(t *testing.T) {
	t.Run("keepalive is activity only", func(t *testing.T) {
		client, _ := newFakeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, ": keepalive\n\nid: 8\nevent: board\ndata: {\"seq\":8,\"type\":\"message_posted\",\"mentions\":[],\"recipients\":[],\"time\":\"2026-11-05T03:05:00Z\"}\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
		}))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stream, err := client.OpenEvents(ctx, 7)
		requireAvailable(t, "OpenEvents", err)
		defer stream.Close()
		event, err := stream.Next()
		requireAvailable(t, "EventStream.Next", err)
		if event.Seq != 8 || event.Type != "message_posted" {
			t.Fatal("SSE keepalive was returned as an event or event sequence was lost")
		}
	})

	t.Run("client close interrupts the stream", func(t *testing.T) {
		entered := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			close(entered)
			<-r.Context().Done()
		}))
		defer server.Close()
		client, err := newPinnedClient(Options{
			URL:         server.URL,
			TokenSource: func(context.Context) ([]byte, error) { return []byte(syntheticBearer), nil },
		})
		if err != nil {
			t.Fatal("NewClient rejected the synthetic loopback URL")
		}
		stream, err := client.OpenEvents(context.Background(), 0)
		requireAvailable(t, "OpenEvents", err)
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("synthetic BOARD event handler did not receive the request")
		}
		client.Close()
		if _, err := stream.Next(); !errors.Is(err, ErrClosed) {
			t.Fatal("Client.Close did not close the caller-owned event stream")
		}
		if err := stream.Close(); err != nil {
			t.Fatal("EventStream.Close was not idempotent")
		}
	})
}

func TestOpenEventsRejectsMalformedAndOverdeepFrames(t *testing.T) {
	deepValue := strings.Repeat("[", maxCanonicalJSONDepth+1) + "0" + strings.Repeat("]", maxCanonicalJSONDepth+1)
	frames := []struct {
		name string
		body string
	}{
		{
			name: "unexpected event kind",
			body: "id: 1\nevent: other\ndata: {\"seq\":1,\"type\":\"message_posted\",\"mentions\":[],\"recipients\":[],\"time\":\"2026-11-05T03:05:00Z\"}\n\n",
		},
		{
			name: "duplicate event field",
			body: "id: 1\nevent: board\nevent: board\ndata: {\"seq\":1,\"type\":\"message_posted\",\"mentions\":[],\"recipients\":[],\"time\":\"2026-11-05T03:05:00Z\"}\n\n",
		},
		{
			name: "overdeep JSON payload",
			body: "id: 1\nevent: board\ndata: {\"seq\":1,\"type\":\"message_posted\",\"mentions\":[],\"recipients\":[],\"time\":\"2026-11-05T03:05:00Z\",\"extra\":" + deepValue + "}\n\n",
		},
	}
	for _, frame := range frames {
		t.Run(frame.name, func(t *testing.T) {
			client, _ := newFakeClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, frame.body)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}))
			stream, err := client.OpenEvents(context.Background(), 0)
			if errors.Is(err, ErrProtocol) {
				return
			}
			requireAvailable(t, "OpenEvents", err)
			defer stream.Close()
			_, err = stream.Next()
			requireCategory(t, err, ErrProtocol)
		})
	}
}
