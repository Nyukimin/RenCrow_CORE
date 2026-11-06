package boardcommunication

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const syntheticBearer = "synthetic-board-token"

func newPinnedClient(options Options) (*Client, error) {
	options.ExpectedConnection = syntheticConnectionIdentity
	return NewClient(options)
}

func newFakeClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := newPinnedClient(Options{
		URL: server.URL,
		TokenSource: func(context.Context) ([]byte, error) {
			return []byte(syntheticBearer), nil
		},
	})
	if err != nil {
		t.Fatal("NewClient rejected a valid synthetic test URL")
	}
	t.Cleanup(client.Close)
	return client, server
}

func requireAvailable(t *testing.T, operation string, err error) {
	t.Helper()
	if errors.Is(err, ErrUnavailable) {
		t.Fatalf("%s returned unavailable", operation)
	}
	if err != nil {
		t.Fatalf("%s returned an error", operation)
	}
}

func TestListContextsUsesCanonicalV1Response(t *testing.T) {
	var called atomic.Bool
	client, _ := newFakeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/contexts" {
			t.Errorf("unexpected canonical contexts request")
		}
		if r.Header.Get("Authorization") != "Bearer "+syntheticBearer {
			t.Errorf("canonical contexts request omitted synthetic bearer auth")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"contexts":[{"id":"channel-alpha","label":"Alpha","sub":"Test channel"}]}`)
	}))

	contexts, err := client.ListContexts(context.Background())
	requireAvailable(t, "ListContexts", err)
	if !called.Load() {
		t.Fatal("ListContexts did not use the fake canonical endpoint")
	}
	if len(contexts) != 1 || contexts[0] != (BoardContext{ID: "channel-alpha", Label: "Alpha", Sub: "Test channel"}) {
		t.Fatal("ListContexts did not decode the canonical BOARD context")
	}
}

func TestListContextsRejectsSingletonCaseAliases(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "contexts envelope alias",
			body: `{"CONTEXTS":[{"id":"channel-alpha","label":"Alpha","sub":"Test channel"}]}`,
		},
		{
			name: "context id alias",
			body: `{"contexts":[{"ID":"channel-alpha","label":"Alpha","sub":"Test channel"}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _ := newFakeClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.body)
			}))
			defer client.Close()
			_, err := client.ListContexts(context.Background())
			requireCategory(t, err, ErrProtocol)
		})
	}
}

func TestReadThreadAndInboxCanonicalDTOs(t *testing.T) {
	t.Run("thread page", func(t *testing.T) {
		client, _ := newFakeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/api/v1/threads/thread-alpha" ||
				r.URL.Query().Get("limit") != "3" || r.URL.Query().Get("cursor") != "message-before" {
				t.Errorf("ReadThread did not use the canonical page route")
			}
			if r.Header.Get("Authorization") != "Bearer "+syntheticBearer {
				t.Errorf("ReadThread omitted synthetic bearer auth")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"thread":{"id":"thread-alpha","space":"channel-alpha","title":"Alpha","summary":"summary","status":"open","decision":"","participants":["ren","peer-alpha"],"created_at":"2026-11-05T03:04:05Z","updated_at":"2026-11-05T03:05:05Z","messages":[{"id":"message-alpha","seq":41,"by":"peer-alpha","at":"2026-11-05T03:05:00Z","body":"synthetic thread message","kind":"chat","mentions":["ren"],"recipients":["ren"],"channel_id":"channel-alpha","thread_id":"thread-alpha","reply_to":"message-before","causation_id":"cause-alpha","hop_count":1}]},"next_cursor":"message-alpha","correlation_id":"synthetic"}`)
		}))
		defer client.Close()
		page, err := client.ReadThread(context.Background(), "thread-alpha", PageRequest{Limit: 3, Cursor: "message-before"})
		requireAvailable(t, "ReadThread", err)
		if page.Thread.ID != "thread-alpha" || page.Thread.Space != "channel-alpha" ||
			page.NextCursor != "message-alpha" || len(page.Thread.Messages) != 1 {
			t.Fatal("ReadThread did not decode the canonical BOARD thread page")
		}
		message := page.Thread.Messages[0]
		if message.ID != "message-alpha" || message.Seq != 41 || message.By != "peer-alpha" ||
			message.ReplyTo != "message-before" || message.CausationID != "cause-alpha" {
			t.Fatal("ReadThread did not preserve BOARD message fields and string IDs")
		}
	})

	t.Run("flat inbox page", func(t *testing.T) {
		client, _ := newFakeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/api/v1/inbox" ||
				r.URL.Query().Get("limit") != "4" || r.URL.Query().Get("cursor") != "item-before" {
				t.Errorf("Inbox did not use the canonical page route")
			}
			if r.Header.Get("Authorization") != "Bearer "+syntheticBearer {
				t.Errorf("Inbox omitted synthetic bearer auth")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"items":[{"item_id":"item-alpha","message_id":"message-alpha","channel_id":"channel-alpha","thread_id":"thread-alpha","sender_id":"peer-alpha","created_at":"2026-11-05T03:06:00Z","body":"synthetic inbox body","mentions":["ren"],"recipients":["ren"],"ack_receipt":"synthetic-private-ack-receipt"}],"next_cursor":"item-alpha","correlation_id":"synthetic"}`)
		}))
		defer client.Close()
		page, err := client.Inbox(context.Background(), PageRequest{Limit: 4, Cursor: "item-before"})
		requireAvailable(t, "Inbox", err)
		if len(page.Items) != 1 || page.NextCursor != "item-alpha" {
			t.Fatal("Inbox did not decode the canonical flat BOARD inbox page")
		}
		item := page.Items[0]
		if item.ItemID != "item-alpha" || item.MessageID != "message-alpha" || item.SenderID != "peer-alpha" ||
			item.Body != "synthetic inbox body" || item.AckReceipt != "synthetic-private-ack-receipt" || item.CreatedAt.IsZero() {
			t.Fatal("Inbox did not preserve canonical inbox row fields")
		}
	})
}

func TestOpenEventsReturnsCanonicalSequenceHint(t *testing.T) {
	client, _ := newFakeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/events" || r.Header.Get("Last-Event-ID") != "40" {
			t.Errorf("OpenEvents did not use the canonical SSE cursor request")
		}
		if r.Header.Get("Authorization") != "Bearer "+syntheticBearer || !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			t.Errorf("OpenEvents omitted canonical authenticated SSE headers")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "id: 41\nevent: board\ndata: {\"seq\":41,\"type\":\"message_posted\",\"topic_id\":\"thread-alpha\",\"space_id\":\"channel-alpha\",\"message_id\":\"message-alpha\",\"sender\":\"peer-alpha\",\"mentions\":[\"ren\"],\"recipients\":[\"ren\"],\"time\":\"2026-11-05T03:05:00Z\"}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.OpenEvents(ctx, 40)
	if stream != nil {
		defer stream.Close()
	}
	requireAvailable(t, "OpenEvents", err)
	event, err := stream.Next()
	requireAvailable(t, "EventStream.Next", err)
	if event.Seq != 41 || event.Type != "message_posted" || event.TopicID != "thread-alpha" ||
		event.SpaceID != "channel-alpha" || event.MessageID != "message-alpha" || event.Sender != "peer-alpha" {
		t.Fatal("OpenEvents did not return the canonical BOARD sequence hint")
	}

	t.Run("rejects event id sequence mismatch", func(t *testing.T) {
		mismatchedClient, _ := newFakeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "id: 42\nevent: board\ndata: {\"seq\":41,\"type\":\"message_posted\",\"mentions\":[],\"recipients\":[],\"time\":\"2026-11-05T03:05:00Z\"}\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
		}))
		defer mismatchedClient.Close()
		mismatchCtx, mismatchCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer mismatchCancel()
		mismatchedStream, openErr := mismatchedClient.OpenEvents(mismatchCtx, 40)
		if mismatchedStream != nil {
			defer mismatchedStream.Close()
		}
		if errors.Is(openErr, ErrProtocol) {
			return
		}
		requireAvailable(t, "OpenEvents", openErr)
		_, eventErr := mismatchedStream.Next()
		requireCategory(t, eventErr, ErrProtocol)
	})
}

func TestPreparedSendImmutableExactReplayContract(t *testing.T) {
	request := SendRequest{
		ChannelID:              "channel-alpha",
		ThreadID:               "thread-alpha",
		Body:                   "synthetic message",
		Recipients:             []string{"peer-alpha"},
		IdempotencyKey:         "logical-key-alpha",
		ProcessingClaimID:      "claim-alpha",
		ProcessingClaimReceipt: "synthetic-private-claim-receipt",
	}

	t.Run("preparation", func(t *testing.T) {
		client, _ := newFakeClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Error("PrepareSend unexpectedly performed network I/O")
		}))
		prepared, err := client.PrepareSend(request)
		requireAvailable(t, "PrepareSend", err)
		if prepared.Path() != "/api/v1/threads/thread-alpha/messages" || prepared.IdempotencyKey() != request.IdempotencyKey {
			t.Fatal("PrepareSend did not freeze the canonical path and logical key")
		}
		payload := prepared.JSONBytes()
		if len(payload) == 0 || prepared.SHA256() != sha256.Sum256(payload) {
			t.Fatal("PrepareSend did not freeze matching payload bytes and SHA-256")
		}
		payload[0] ^= 0xff
		if bytes.Equal(payload, prepared.JSONBytes()) {
			t.Fatal("PreparedSend exposed mutable internal payload bytes")
		}
		var wire struct {
			IdempotencyKey         string `json:"idempotency_key"`
			ProcessingClaimReceipt string `json:"processing_claim_receipt"`
		}
		if err := json.Unmarshal(prepared.JSONBytes(), &wire); err != nil {
			t.Fatal("PrepareSend produced invalid JSON")
		}
		if wire.IdempotencyKey != request.IdempotencyKey || wire.ProcessingClaimReceipt != request.ProcessingClaimReceipt {
			t.Fatal("PrepareSend did not preserve the logical key and private claim receipt")
		}
	})

	t.Run("exact replay", func(t *testing.T) {
		const payload = `{"channel_id":"channel-alpha","thread_id":"thread-alpha","body":"synthetic message","recipients":["peer-alpha"],"idempotency_key":"logical-key-alpha","processing_claim_id":"claim-alpha","processing_claim_receipt":"synthetic-private-claim-receipt"}`
		wantBytes := []byte(payload)
		prepared := PreparedSend{
			path:           "/api/v1/threads/thread-alpha/messages",
			idempotencyKey: "logical-key-alpha",
			payload:        append([]byte(nil), wantBytes...),
			payloadSHA256:  sha256.Sum256(wantBytes),
		}
		var calls atomic.Int32
		client, _ := newFakeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.Method != http.MethodPost || r.URL.Path != prepared.Path() {
				t.Errorf("SendPrepared used a noncanonical endpoint")
			}
			if r.Header.Get("Authorization") != "Bearer "+syntheticBearer {
				t.Errorf("SendPrepared omitted synthetic bearer auth")
			}
			if r.Header.Get("Idempotency-Key") != prepared.IdempotencyKey() {
				t.Errorf("SendPrepared changed the logical idempotency key")
			}
			got, err := io.ReadAll(r.Body)
			if err != nil || !bytes.Equal(got, wantBytes) {
				t.Errorf("SendPrepared changed the exact prepared bytes")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"receipt":{"message_id":"message-alpha","thread_id":"thread-alpha","stored_at":"2026-11-05T03:04:05Z","status":"stored"}}`)
		}))

		var first SendReceipt
		for attempt := 0; attempt < 2; attempt++ {
			receipt, err := client.SendPrepared(context.Background(), prepared)
			requireAvailable(t, "SendPrepared", err)
			if receipt.MessageID != "message-alpha" || receipt.ThreadID != "thread-alpha" || receipt.Status != "stored" {
				t.Fatal("SendPrepared did not decode the typed BOARD receipt")
			}
			if attempt == 0 {
				first = receipt
			} else if receipt != first {
				t.Fatal("exact replay returned a different BOARD receipt")
			}
		}
		if calls.Load() != 2 {
			t.Fatal("SendPrepared did not issue exactly one request per caller-owned attempt")
		}
	})
}

func requireCategory(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("expected fixed error category %q", want.Error())
	}
}

func TestNewClientRejectsUnsafeConnectionOptionsWithoutNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	tokenSource := TokenSource(func(context.Context) ([]byte, error) { return []byte(syntheticBearer), nil })
	absoluteSocket := filepath.Join(t.TempDir(), "board.sock")
	tests := []struct {
		name    string
		options Options
	}{
		{name: "no endpoint", options: Options{TokenSource: tokenSource}},
		{name: "both endpoint modes", options: Options{URL: server.URL, UnixSocket: absoluteSocket, TokenSource: tokenSource}},
		{name: "missing token source", options: Options{URL: server.URL}},
		{name: "non-loopback HTTP", options: Options{URL: "http://203.0.113.7:18792", TokenSource: tokenSource}},
		{name: "extra base path", options: Options{URL: "http://127.0.0.1/api", TokenSource: tokenSource}},
		{name: "userinfo", options: Options{URL: "http://user@127.0.0.1", TokenSource: tokenSource}},
		{name: "query", options: Options{URL: "http://127.0.0.1?token=synthetic", TokenSource: tokenSource}},
		{name: "fragment", options: Options{URL: "http://127.0.0.1#fragment", TokenSource: tokenSource}},
		{name: "unsupported scheme", options: Options{URL: "ftp://127.0.0.1", TokenSource: tokenSource}},
		{name: "malformed port", options: Options{URL: "http://127.0.0.1:abc", TokenSource: tokenSource}},
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		tests = append(tests, struct {
			name    string
			options Options
		}{name: "relative Unix socket", options: Options{UnixSocket: "board.sock", TokenSource: tokenSource}})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.options.ExpectedConnection = syntheticConnectionIdentity
			client, err := NewClient(test.options)
			if client != nil {
				client.Close()
			}
			requireCategory(t, err, ErrInvalidConfiguration)
		})
	}
	httpsClient, err := newPinnedClient(Options{
		URL:         "https://board.example",
		TokenSource: tokenSource,
	})
	if err != nil {
		t.Fatal("NewClient rejected a verified HTTPS root URL")
	}
	httpsClient.Close()
	if requests.Load() != 0 {
		t.Fatal("invalid connection options performed network I/O")
	}
}

func TestClientCloseAndNilCallsFailClosed(t *testing.T) {
	var tokenCalls atomic.Int32
	client, err := newPinnedClient(Options{
		URL: "http://127.0.0.1:1",
		TokenSource: func(context.Context) ([]byte, error) {
			tokenCalls.Add(1)
			return []byte(syntheticBearer), nil
		},
	})
	if err != nil {
		t.Fatal("NewClient rejected a synthetic loopback URL")
	}
	prepared, err := client.PrepareSend(SendRequest{
		ChannelID:      "channel-alpha",
		ThreadID:       "thread-alpha",
		Body:           "synthetic message",
		Recipients:     []string{},
		IdempotencyKey: "logical-key-alpha",
	})
	requireAvailable(t, "PrepareSend", err)
	client.Close()
	_, err = client.ListContexts(context.Background())
	requireCategory(t, err, ErrClosed)
	_, err = client.ListContexts(nil)
	requireCategory(t, err, ErrInvalidRequest)
	_, err = (*Client)(nil).ListContexts(context.Background())
	requireCategory(t, err, ErrInvalidRequest)
	_, err = client.SendPrepared(nil, prepared)
	requireCategory(t, err, ErrInvalidRequest)
	if tokenCalls.Load() != 0 {
		t.Fatal("closed client accessed its token source")
	}
}

func TestListContextsIgnoresProxyAndRejectsRedirect(t *testing.T) {
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		proxyCalls.Add(1)
	}))
	defer proxy.Close()
	var redirectCalls atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectCalls.Add(1)
	}))
	defer redirectTarget.Close()
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+syntheticBearer {
			t.Errorf("request omitted synthetic bearer auth")
		}
		http.Redirect(w, r, redirectTarget.URL, http.StatusFound)
	}))
	defer target.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	client, err := newPinnedClient(Options{
		URL:         target.URL,
		TokenSource: func(context.Context) ([]byte, error) { return []byte(syntheticBearer), nil },
	})
	if err != nil {
		t.Fatal("NewClient rejected the synthetic loopback URL")
	}
	defer client.Close()
	_, err = client.ListContexts(context.Background())
	requireCategory(t, err, ErrProtocol)
	if targetCalls.Load() != 1 || redirectCalls.Load() != 0 || proxyCalls.Load() != 0 {
		t.Fatal("client followed a redirect or used an environment proxy")
	}
}

func TestTokenSourceRotatesAndFailuresAreRedacted(t *testing.T) {
	var tokenCalls atomic.Int32
	var requestCalls atomic.Int32
	seenAuth := make(chan string, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCalls.Add(1)
		seenAuth <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"contexts":[] ,"correlation_id":"synthetic"}`)
	}))
	defer server.Close()
	client, err := newPinnedClient(Options{
		URL: server.URL,
		TokenSource: func(context.Context) ([]byte, error) {
			switch tokenCalls.Add(1) {
			case 1:
				return []byte("synthetic-token-one"), nil
			case 2:
				return []byte("synthetic-token-two"), nil
			default:
				return []byte("synthetic-secret-material"), errors.New("synthetic raw token source detail")
			}
		},
	})
	if err != nil {
		t.Fatal("NewClient rejected the synthetic loopback URL")
	}
	defer client.Close()
	for range 2 {
		_, err := client.ListContexts(context.Background())
		requireAvailable(t, "ListContexts", err)
	}
	if first, second := <-seenAuth, <-seenAuth; first != "Bearer synthetic-token-one" || second != "Bearer synthetic-token-two" {
		t.Fatal("client did not request a fresh token for each operation")
	}
	_, err = client.ListContexts(context.Background())
	requireCategory(t, err, ErrAuthentication)
	if strings.Contains(err.Error(), "synthetic raw token source detail") || strings.Contains(err.Error(), "synthetic-secret-material") {
		t.Fatal("client error exposed token-source material")
	}
	if requestCalls.Load() != 2 || tokenCalls.Load() != 3 {
		t.Fatal("token-source failure reached the BOARD endpoint")
	}

	var unsafeRequests atomic.Int32
	unsafeServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		unsafeRequests.Add(1)
	}))
	defer unsafeServer.Close()
	unsafeClient, err := newPinnedClient(Options{
		URL: unsafeServer.URL,
		TokenSource: func(context.Context) ([]byte, error) {
			return []byte("synthetic-token\r\nX-Injected: yes"), nil
		},
	})
	if err != nil {
		t.Fatal("NewClient rejected the synthetic loopback URL")
	}
	defer unsafeClient.Close()
	_, err = unsafeClient.ListContexts(context.Background())
	requireCategory(t, err, ErrAuthentication)
	if unsafeRequests.Load() != 0 {
		t.Fatal("unsafe bearer bytes were sent")
	}
}

func TestPrepareAndRestorePreserveCanonicalPayload(t *testing.T) {
	client, _ := newFakeClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("send preparation unexpectedly performed network I/O")
	}))
	defer client.Close()
	body := strings.Repeat("x", 7400) + "  exact tail  "
	prepared, err := client.PrepareSend(SendRequest{
		ChannelID:      "channel-alpha",
		Title:          "  exact title  ",
		Body:           body,
		Recipients:     []string{},
		IdempotencyKey: "logical-key-new-thread",
	})
	requireAvailable(t, "PrepareSend", err)
	if prepared.Path() != "/api/v1/threads" {
		t.Fatal("new-thread send used a noncanonical endpoint")
	}
	var wire sendWire
	if err := json.Unmarshal(prepared.JSONBytes(), &wire); err != nil || wire.Title != "  exact title  " || wire.Body != body || wire.Recipients == nil {
		t.Fatal("send preparation trimmed, truncated, or changed canonical fields")
	}

	exact := []byte("{ \n\"channel_id\":\"channel-alpha\",\"thread_id\":\"thread-alpha\",\"body\":\" synthetic body \" ,\"recipients\":[],\"reply_to\":\"\",\"kind\":\"\",\"idempotency_key\":\"logical-key-restored\",\"processing_claim_id\":\"\",\"processing_claim_receipt\":\"\"}\n")
	hash := sha256.Sum256(exact)
	restored, err := RestorePreparedSend(exact, hash)
	requireAvailable(t, "RestorePreparedSend", err)
	if !bytes.Equal(restored.JSONBytes(), exact) || restored.SHA256() != hash || restored.Path() != "/api/v1/threads/thread-alpha/messages" || restored.IdempotencyKey() != "logical-key-restored" {
		t.Fatal("restore did not preserve exact bytes while deriving its route and key")
	}
	badHash := hash
	badHash[0] ^= 1
	_, err = RestorePreparedSend(exact, badHash)
	requireCategory(t, err, ErrInvalidPreparedSend)

	var calls atomic.Int32
	validationClient, _ := newFakeClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer validationClient.Close()
	badKey := restored
	badKey.idempotencyKey = "different-logical-key"
	_, err = validationClient.SendPrepared(context.Background(), badKey)
	requireCategory(t, err, ErrInvalidPreparedSend)
	badRoute := restored
	badRoute.path = "/api/v1/threads/../contexts"
	_, err = validationClient.SendPrepared(context.Background(), badRoute)
	requireCategory(t, err, ErrInvalidPreparedSend)
	if calls.Load() != 0 {
		t.Fatal("invalid prepared data reached the BOARD endpoint")
	}

	invalidRequests := []SendRequest{
		{ChannelID: "channel-alpha", Body: "body", Recipients: []string{}, IdempotencyKey: "key"},
		{ChannelID: "channel-alpha", ThreadID: "thread-alpha", Title: "not allowed", Body: "body", Recipients: []string{}, IdempotencyKey: "key"},
		{ChannelID: "channel-alpha", ThreadID: "../thread", Body: "body", Recipients: []string{}, IdempotencyKey: "key"},
		{ChannelID: "channel-alpha", ThreadID: "thread-alpha", Body: "body", IdempotencyKey: "key"},
	}
	for _, invalid := range invalidRequests {
		_, err := client.PrepareSend(invalid)
		requireCategory(t, err, ErrInvalidRequest)
	}
	_, err = client.PrepareSend(SendRequest{
		ChannelID:      "channel-alpha",
		ThreadID:       "thread-alpha",
		Body:           strings.Repeat("x", maxRequestBodyBytes+1),
		Recipients:     []string{},
		IdempotencyKey: "oversized-synthetic-key",
	})
	requireCategory(t, err, ErrInvalidRequest)
}

func TestSendPreparedUnknownOutcomesAndSpecificFailures(t *testing.T) {
	request := SendRequest{
		ChannelID:      "channel-alpha",
		ThreadID:       "thread-alpha",
		Body:           "synthetic message",
		Recipients:     []string{"peer-alpha"},
		IdempotencyKey: "logical-key-alpha",
	}
	prepared, err := (*Client)(nil).PrepareSend(request)
	requireAvailable(t, "PrepareSend", err)
	validReceipt := `{"receipt":{"message_id":"message-alpha","thread_id":"thread-alpha","stored_at":"2026-11-05T03:04:05Z","status":"stored"},"correlation_id":"synthetic"}`
	tests := []struct {
		name       string
		status     int
		body       string
		want       error
		bodyLength int
	}{
		{name: "malformed 201", status: http.StatusCreated, body: `{`, want: ErrUnknownOutcome},
		{name: "wrong success status", status: http.StatusOK, body: validReceipt, want: ErrUnknownOutcome},
		{name: "malformed receipt", status: http.StatusCreated, body: `{"receipt":{"status":"stored"}}`, want: ErrUnknownOutcome},
		{name: "specific idempotency conflict", status: http.StatusConflict, body: `{"error":"idempotency_conflict"}`, want: ErrConflict},
		{name: "idempotency conflict with correlation id", status: http.StatusConflict, body: `{"error":"idempotency_conflict","correlation_id":"synthetic"}`, want: ErrConflict},
		{name: "specific claim fence", status: http.StatusBadRequest, body: `{"error":"processing_claim_required_or_expired"}`, want: ErrClaimFence},
		{name: "claim fence with correlation id", status: http.StatusBadRequest, body: `{"error":"processing_claim_required_or_expired","correlation_id":"synthetic"}`, want: ErrClaimFence},
		{name: "specific error with unknown field", status: http.StatusBadRequest, body: `{"error":"processing_claim_required_or_expired","extra":"synthetic"}`, want: ErrUnknownOutcome},
		{name: "unrecognized wire error redacted", status: http.StatusConflict, body: `{"error":"synthetic-secret-wire-detail"}`, want: ErrUnknownOutcome},
		{name: "oversized success", status: http.StatusCreated, bodyLength: maxResponseBodyBytes + 1, want: ErrUnknownOutcome},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _ := newFakeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.bodyLength > 0 {
					w.WriteHeader(test.status)
					_, _ = io.WriteString(w, strings.Repeat("x", test.bodyLength))
					return
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer client.Close()
			_, err := client.SendPrepared(context.Background(), prepared)
			requireCategory(t, err, test.want)
			if strings.Contains(err.Error(), "synthetic-secret-wire-detail") {
				t.Fatal("send error exposed BOARD response text")
			}
		})
	}
}

func TestSendPreparedDuplicateErrorEnvelopeKeysAreUnknown(t *testing.T) {
	prepared, err := (*Client)(nil).PrepareSend(SendRequest{
		ChannelID:      "channel-alpha",
		ThreadID:       "thread-alpha",
		Body:           "synthetic message",
		Recipients:     []string{},
		IdempotencyKey: "logical-key-alpha",
	})
	requireAvailable(t, "PrepareSend", err)
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{
			name:   "duplicate claim error with recognized value last",
			status: http.StatusBadRequest,
			body:   `{"error":"other","error":"processing_claim_required_or_expired"}`,
		},
		{
			name:   "duplicate claim error with repeated recognized value",
			status: http.StatusBadRequest,
			body:   `{"error":"processing_claim_required_or_expired","error":"processing_claim_required_or_expired"}`,
		},
		{
			name:   "case alias claim error with recognized value last",
			status: http.StatusBadRequest,
			body:   `{"error":"other","ERROR":"processing_claim_required_or_expired"}`,
		},
		{
			name:   "single claim error alias",
			status: http.StatusBadRequest,
			body:   `{"ERROR":"processing_claim_required_or_expired"}`,
		},
		{
			name:   "duplicate conflict error with recognized value last",
			status: http.StatusConflict,
			body:   `{"error":"other","error":"idempotency_conflict"}`,
		},
		{
			name:   "duplicate conflict error with repeated recognized value",
			status: http.StatusConflict,
			body:   `{"error":"idempotency_conflict","error":"idempotency_conflict"}`,
		},
		{
			name:   "case alias conflict error with recognized value last",
			status: http.StatusConflict,
			body:   `{"error":"other","ERROR":"idempotency_conflict"}`,
		},
		{
			name:   "single conflict error alias",
			status: http.StatusConflict,
			body:   `{"ERROR":"idempotency_conflict"}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _ := newFakeClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer client.Close()
			_, err := client.SendPrepared(context.Background(), prepared)
			requireCategory(t, err, ErrUnknownOutcome)
		})
	}
}

func TestSendPreparedNestedReceiptDuplicateKeysAreUnknown(t *testing.T) {
	prepared, err := (*Client)(nil).PrepareSend(SendRequest{
		ChannelID:      "channel-alpha",
		ThreadID:       "thread-alpha",
		Body:           "synthetic message",
		Recipients:     []string{},
		IdempotencyKey: "logical-key-alpha",
	})
	requireAvailable(t, "PrepareSend", err)
	tests := []struct {
		name string
		body string
	}{
		{
			name: "duplicate nested message id",
			body: `{"receipt":{"message_id":"message-other","message_id":"message-alpha","thread_id":"thread-alpha","stored_at":"2026-11-05T03:04:05Z","status":"stored"}}`,
		},
		{
			name: "duplicate nested status",
			body: `{"receipt":{"message_id":"message-alpha","thread_id":"thread-alpha","stored_at":"2026-11-05T03:04:05Z","status":"failed","status":"stored"}}`,
		},
		{
			name: "nested case alias message id",
			body: `{"receipt":{"message_id":"message-other","MESSAGE_ID":"message-alpha","thread_id":"thread-alpha","stored_at":"2026-11-05T03:04:05Z","status":"stored"}}`,
		},
		{
			name: "single receipt envelope alias",
			body: `{"RECEIPT":{"message_id":"message-alpha","thread_id":"thread-alpha","stored_at":"2026-11-05T03:04:05Z","status":"stored"}}`,
		},
		{
			name: "single message id alias",
			body: `{"receipt":{"MESSAGE_ID":"message-alpha","thread_id":"thread-alpha","stored_at":"2026-11-05T03:04:05Z","status":"stored"}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _ := newFakeClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, test.body)
			}))
			defer client.Close()
			_, err := client.SendPrepared(context.Background(), prepared)
			requireCategory(t, err, ErrUnknownOutcome)
		})
	}

	t.Run("valid receipt remains accepted without correlation id", func(t *testing.T) {
		client, _ := newFakeClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"receipt":{"message_id":"message-alpha","thread_id":"thread-alpha","stored_at":"2026-11-05T03:04:05Z","status":"stored"}}`)
		}))
		defer client.Close()
		receipt, err := client.SendPrepared(context.Background(), prepared)
		requireAvailable(t, "SendPrepared", err)
		if receipt.MessageID != "message-alpha" || receipt.ThreadID != "thread-alpha" || receipt.Status != "stored" {
			t.Fatal("SendPrepared did not accept the canonical receipt")
		}
	})
}

func TestSendPreparedCancellationIsUnknownAndClosesRequest(t *testing.T) {
	prepared, err := (*Client)(nil).PrepareSend(SendRequest{
		ChannelID:      "channel-alpha",
		ThreadID:       "thread-alpha",
		Body:           "synthetic message",
		Recipients:     []string{"peer-alpha"},
		IdempotencyKey: "logical-key-alpha",
	})
	requireAvailable(t, "PrepareSend", err)
	entered := make(chan struct{})
	cancelObserved := make(chan struct{})
	releaseHandler := make(chan struct{})
	var cancelObservedOnce sync.Once
	var releaseHandlerOnce sync.Once
	var enteredOnce sync.Once
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("could not drain synthetic request body")
		}
		if err := r.Body.Close(); err != nil {
			t.Errorf("could not close synthetic request body")
		}
		enteredOnce.Do(func() { close(entered) })
		handlerDeadline := time.NewTimer(5 * time.Second)
		defer handlerDeadline.Stop()
		select {
		case <-r.Context().Done():
			cancelObservedOnce.Do(func() { close(cancelObserved) })
		case <-releaseHandler:
		case <-handlerDeadline.C:
			t.Errorf("synthetic BOARD handler did not observe request cancellation")
		}
	}))
	defer func() {
		releaseHandlerOnce.Do(func() { close(releaseHandler) })
		server.Close()
	}()
	client, err := newPinnedClient(Options{
		URL:         server.URL,
		TokenSource: func(context.Context) ([]byte, error) { return []byte(syntheticBearer), nil },
	})
	if err != nil {
		t.Fatal("NewClient rejected the synthetic loopback URL")
	}
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.SendPrepared(ctx, prepared)
		result <- err
	}()
	select {
	case <-entered:
		cancel()
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("synthetic BOARD handler did not receive the request")
	}
	select {
	case err := <-result:
		requireCategory(t, err, ErrUnknownOutcome)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled send did not release its request")
	}
	if requests.Load() != 1 {
		t.Fatal("one caller-owned send attempt produced more than one HTTP request")
	}
	select {
	case <-cancelObserved:
	case <-time.After(5 * time.Second):
		t.Fatal("synthetic BOARD handler did not observe request cancellation")
	}
}

func TestClientCloseCancelsInFlightRead(t *testing.T) {
	entered := make(chan struct{})
	var enteredOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		enteredOnce.Do(func() { close(entered) })
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
	defer client.Close()
	result := make(chan error, 1)
	go func() {
		_, err := client.ListContexts(context.Background())
		result <- err
	}()
	select {
	case <-entered:
		client.Close()
	case <-time.After(5 * time.Second):
		client.Close()
		t.Fatal("synthetic BOARD handler did not receive the request")
	}
	select {
	case err := <-result:
		requireCategory(t, err, ErrUnavailable)
	case <-time.After(5 * time.Second):
		t.Fatal("client close did not release the in-flight read")
	}
}

func TestUnixSocketUsesOnlyPrivateFixedHost(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		path := filepath.Join(t.TempDir(), "board.sock")
		_, err := newPinnedClient(Options{UnixSocket: path, TokenSource: func(context.Context) ([]byte, error) { return []byte(syntheticBearer), nil }})
		requireCategory(t, err, ErrUnsupported)
		return
	}

	directory := resolvedPrivateTempDir(t)
	socketPath := filepath.Join(directory, "s")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal("could not create synthetic Unix socket")
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.Chmod(socketPath, 0o600); err != nil {
		t.Fatal("could not protect synthetic Unix socket fixture")
	}
	var tokenCalls atomic.Int32
	client, err := newPinnedClient(Options{
		UnixSocket: socketPath,
		TokenSource: func(context.Context) ([]byte, error) {
			tokenCalls.Add(1)
			return []byte(syntheticBearer), nil
		},
	})
	if err != nil {
		t.Fatal("NewClient rejected a private synthetic Unix socket")
	}
	defer client.Close()
	if client.base.Scheme != "http" || client.base.Host != "127.0.0.1:18792" {
		t.Fatal("Unix socket client did not use the fixed logical HTTP host")
	}
	connection, err := client.transport.DialContext(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("client transport did not connect through the synthetic Unix socket")
	}
	defer connection.Close()
	accepted, err := listener.Accept()
	if err != nil {
		t.Fatal("synthetic Unix listener did not accept the client transport")
	}
	defer accepted.Close()
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal("could not make synthetic socket parent unsafe")
	}
	_, err = client.ListContexts(context.Background())
	requireCategory(t, err, ErrInvalidConfiguration)
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal("could not restore synthetic socket parent permissions")
	}
	if err := os.Chmod(socketPath, 0o644); err != nil {
		t.Fatal("could not make synthetic socket fixture unsafe")
	}
	_, err = client.ListContexts(context.Background())
	requireCategory(t, err, ErrInvalidConfiguration)
	if tokenCalls.Load() != 0 {
		t.Fatal("Unix socket dial or unsafe path caused token material to be read")
	}
}

func TestUnixSocketRejectsMissingAndSymlinkPathsBeforeTokenRead(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Unix sockets are unsupported on this operating system")
	}
	directory := resolvedPrivateTempDir(t)
	missingPath := filepath.Join(directory, "m")
	var tokenCalls atomic.Int32
	tokenSource := func(context.Context) ([]byte, error) {
		tokenCalls.Add(1)
		return []byte(syntheticBearer), nil
	}
	missingClient, err := newPinnedClient(Options{UnixSocket: missingPath, TokenSource: tokenSource})
	if err != nil {
		t.Fatal("NewClient rejected an absolute synthetic socket path")
	}
	_, err = missingClient.ListContexts(context.Background())
	requireCategory(t, err, ErrUnavailable)
	missingClient.Close()
	if tokenCalls.Load() != 0 {
		t.Fatal("missing socket caused token material to be read")
	}

	alias := filepath.Join(directory, "alias")
	if err := os.Symlink(directory, alias); err != nil {
		t.Skip("symlink creation is unavailable in this local test environment")
	}
	linkClient, err := newPinnedClient(Options{UnixSocket: filepath.Join(alias, "m"), TokenSource: tokenSource})
	if err != nil {
		t.Fatal("NewClient rejected an absolute synthetic socket path")
	}
	_, err = linkClient.ListContexts(context.Background())
	requireCategory(t, err, ErrInvalidConfiguration)
	linkClient.Close()
	if tokenCalls.Load() != 0 {
		t.Fatal("symlinked socket route caused token material to be read")
	}
}

func resolvedPrivateTempDir(t *testing.T) string {
	t.Helper()
	moduleRoot := nearestModuleRoot(t)
	root := os.Getenv("RENCROW_TEST_SOCKET_ROOT")
	if root == "" {
		root = filepath.Join(moduleRoot, "Tmp", "test-runtime")
	} else if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		t.Fatal("RENCROW_TEST_SOCKET_ROOT must be an absolute clean path")
	}
	if !isIntendedSocketRoot(root, moduleRoot) {
		t.Fatal("RENCROW_TEST_SOCKET_ROOT is not the owner workspace Tmp/test-runtime path")
	}
	if err := rejectSymlinkComponents(root); err != nil {
		t.Fatal("Unix socket fixture root must not contain symlinks")
	}
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal("owner workspace test-runtime directory is unavailable")
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("owner workspace test-runtime path is not a real directory")
	}
	directory, err := os.MkdirTemp(root, "u")
	if err != nil {
		t.Fatal("could not create private synthetic socket fixture")
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("could not remove owned synthetic socket fixture")
		}
	})
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal("could not protect synthetic temporary directory")
	}
	info, err = os.Stat(directory)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatal("synthetic temporary directory is not owner-only")
	}
	return directory
}

func nearestModuleRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal("could not locate current module")
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		t.Fatal("could not resolve current module path")
	}
	for {
		info, statErr := os.Stat(filepath.Join(directory, "go.mod"))
		if statErr == nil && !info.IsDir() {
			return filepath.Clean(directory)
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("could not locate current module go.mod")
		}
		directory = parent
	}
}

func isIntendedSocketRoot(root, moduleRoot string) bool {
	expected := filepath.Join(moduleRoot, "Tmp", "test-runtime")
	if filepath.Clean(root) == expected {
		return true
	}
	return filepath.Base(root) == "test-runtime" &&
		filepath.Base(filepath.Dir(root)) == "Tmp" &&
		pathContains(root, moduleRoot)
}

func pathContains(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(relative) || relative == ".." {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func rejectSymlinkComponents(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return os.ErrInvalid
	}
	volume := filepath.VolumeName(path)
	current := volume + string(os.PathSeparator)
	remaining := strings.TrimLeft(strings.TrimPrefix(path, volume), string(os.PathSeparator))
	for _, component := range strings.Split(remaining, string(os.PathSeparator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return os.ErrInvalid
		}
	}
	return nil
}
