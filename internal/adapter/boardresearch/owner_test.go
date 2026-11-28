package boardresearch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	appresearch "github.com/Nyukimin/RenCrow_CORE/internal/application/research"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

func TestResearchBoardOwnerCommitsPinnedDestinationAndVerifiesReadback(t *testing.T) {
	const report = "# Report\n\nVerified finding."
	const correlation = "research-board-correlation"
	posts := 0
	var firstPayload []byte
	receiptLookupFails := false
	canonicalReceipt := boardcommunication.SendReceipt{
		MessageID: "message-001", ThreadID: "thread-001",
		StoredAt: time.Date(2026, 11, 28, 8, 0, 2, 0, time.UTC), Status: "stored",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Correlation-ID", correlation)
		switch r.URL.Path {
		case "/api/v1/connection":
			_, _ = io.WriteString(w, `{"principal_id":"ren-core","journal_instance_id":"journal_0123456789abcdef0123456789abcdef","correlation_id":"`+correlation+`"}`)
		case "/api/v1/threads":
			payloadBytes, readErr := io.ReadAll(r.Body)
			var payload struct {
				ChannelID      string   `json:"channel_id"`
				Title          string   `json:"title"`
				Body           string   `json:"body"`
				Recipients     []string `json:"recipients"`
				Kind           string   `json:"kind"`
				IdempotencyKey string   `json:"idempotency_key"`
			}
			unmarshalErr := json.Unmarshal(payloadBytes, &payload)
			if r.Method != http.MethodPost || readErr != nil || unmarshalErr != nil {
				t.Errorf("invalid prepared BOARD request: read=%v decode=%v", readErr, unmarshalErr)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			posts++
			if payload.Kind != "" && payload.Kind != "reply" && payload.Kind != "request" {
				t.Errorf("fake BOARD received noncanonical kind %q", payload.Kind)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if payload.Kind != "" {
				t.Errorf("Research informational report kind = %q, want empty", payload.Kind)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if payload.ChannelID != "research" || payload.Title != "Research" || payload.Body != report || !reflect.DeepEqual(payload.Recipients, []string{"peer-a"}) || payload.IdempotencyKey != r.Header.Get("Idempotency-Key") {
				t.Errorf("unexpected prepared BOARD send: %#v", payload)
			}
			if posts == 1 {
				firstPayload = append([]byte(nil), payloadBytes...)
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, `{"receipt":`)
				return
			}
			if !bytes.Equal(payloadBytes, firstPayload) {
				t.Errorf("retry changed exact BOARD request bytes")
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"receipt":{"message_id":"message-001","thread_id":"thread-001","stored_at":"2026-11-28T08:00:02Z","status":"stored"},"correlation_id":"`+correlation+`"}`)
		case "/api/v1/messages/message-001/receipt":
			if r.Method != http.MethodGet {
				t.Errorf("canonical message receipt used %s", r.Method)
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if receiptLookupFails {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"error":"receipt unavailable"}`)
				return
			}
			_ = json.NewEncoder(w).Encode(struct {
				Receipt       boardcommunication.SendReceipt `json:"receipt"`
				CorrelationID string                         `json:"correlation_id"`
			}{Receipt: canonicalReceipt, CorrelationID: correlation})
		case "/api/v1/threads/thread-001":
			_, _ = io.WriteString(w, `{"thread":{"id":"thread-001","space":"research","title":"Research","summary":"","status":"open","decision":"","participants":["ren-core","peer-a"],"created_at":"2026-11-28T08:00:01Z","updated_at":"2026-11-28T08:00:02.000017Z","messages":[{"id":"message-001","seq":1,"by":"ren-core","at":"2026-11-28T08:00:02.000017Z","body":"# Report\n\nVerified finding.","mentions":[],"recipients":["peer-a"],"channel_id":"research","thread_id":"thread-001"}]},"next_cursor":"","correlation_id":"`+correlation+`"}`)
		default:
			t.Errorf("unexpected BOARD route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := boardcommunication.NewClient(boardcommunication.Options{
		URL:                server.URL,
		TokenSource:        func(context.Context) ([]byte, error) { return []byte("synthetic-token"), nil },
		ExpectedConnection: boardcommunication.ConnectionIdentity{PrincipalID: "ren-core", JournalInstanceID: "journal_0123456789abcdef0123456789abcdef"},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()
	now := time.Date(2026, 11, 28, 8, 0, 0, 0, time.UTC)
	pin := eventstore.BoardConnectionPin{OwnerID: "ren", PrincipalID: "ren-core", JournalInstanceID: "journal_0123456789abcdef0123456789abcdef", CutoverAt: now.Add(-time.Hour)}
	destination := appresearch.BoardDestination{Ref: "research-main", OwnerID: "ren", PrincipalID: "ren-core", ChannelID: "research", NewThread: true, Recipients: []string{"peer-a"}, DataScope: "user", Title: "Research"}
	legacyDestination := destination
	legacyDestination.Ref = "research-legacy"
	queue, err := persistencequeue.NewPersistenceQueue(8, 1<<20)
	if err != nil {
		t.Fatalf("NewPersistenceQueue: %v", err)
	}
	writeQueue, err := eventstore.NewPersistenceQueueWriteQueue(queue)
	if err != nil {
		t.Fatalf("NewPersistenceQueueWriteQueue: %v", err)
	}
	store, err := eventstore.NewSQLiteStoreWithWriteQueue(filepath.Join(t.TempDir(), "research-board.sqlite"), writeQueue)
	if err != nil {
		t.Fatalf("NewSQLiteStoreWithWriteQueue: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := queue.Shutdown(shutdownCtx); err != nil {
			t.Errorf("queue shutdown: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Errorf("store close: %v", err)
		}
	})
	if _, err := store.PinBoardConnection(context.Background(), pin); err != nil {
		t.Fatalf("PinBoardConnection: %v", err)
	}
	owner, err := NewOwner(client, store, pin, []appresearch.BoardDestination{destination, legacyDestination}, func() time.Time { return now.Add(3 * time.Second) })
	if err != nil {
		t.Fatalf("NewOwner: %v", err)
	}
	digest := sha256.Sum256([]byte(report))
	request := engine.CommitRequest{Envelope: c.Envelope{RunID: "run-1", Actor: "mio", OwnerID: "ren", DataScope: "user"}, ResearchRunID: "research-1", Target: c.SaveTarget{Target: "board", DestinationRef: destination.Ref}, Report: report, Digest: hex.EncodeToString(digest[:])}
	request.IdempotencyKey = appresearch.IdempotencyKey(request.ResearchRunID, request.Target.Target, request.Target.DestinationRef, request.Digest)

	scope := appresearch.BoardPublicationScope{OwnerID: pin.OwnerID, PrincipalID: pin.PrincipalID, JournalInstanceID: pin.JournalInstanceID}
	operationID := appresearch.BoardOperationID(scope, destination.Ref, destination.DataScope, destination.ChannelID, destination.ThreadID, destination.NewThread, destination.Title, request.Digest, destination.Recipients)
	legacyOperationID := appresearch.BoardOperationID(scope, legacyDestination.Ref, legacyDestination.DataScope, legacyDestination.ChannelID, legacyDestination.ThreadID, legacyDestination.NewThread, legacyDestination.Title, request.Digest, legacyDestination.Recipients)
	legacyPrepared, err := client.PrepareSend(boardcommunication.SendRequest{
		ChannelID: legacyDestination.ChannelID, ThreadID: legacyDestination.ThreadID, Title: legacyDestination.Title,
		Body: request.Report, Recipients: legacyDestination.Recipients, Kind: "research_report", IdempotencyKey: legacyOperationID,
	})
	if err != nil {
		t.Fatalf("PrepareSend(legacy pending payload): %v", err)
	}
	legacyPayloadHash := legacyPrepared.SHA256()
	legacyLease, claimed, err := store.ClaimResearchBoardPublication(context.Background(), appresearch.BoardPublicationClaimInput{
		Scope: scope, OperationID: legacyOperationID, DestinationRef: legacyDestination.Ref, DataScope: legacyDestination.DataScope,
		ChannelID: legacyDestination.ChannelID, ThreadID: legacyDestination.ThreadID, NewThread: legacyDestination.NewThread,
		Title: legacyDestination.Title, Recipients: append([]string(nil), legacyDestination.Recipients...), ContentDigest: request.Digest,
		IdempotencyKey: legacyOperationID, PreparedBytes: legacyPrepared.JSONBytes(), PayloadSHA256: hex.EncodeToString(legacyPayloadHash[:]),
		Now: now, LeaseDuration: boardPublicationLeaseDuration,
	})
	if err != nil || !claimed {
		t.Fatalf("ClaimResearchBoardPublication(legacy pending payload) = claimed %t, err %v", claimed, err)
	}
	if released, err := store.ReleaseResearchBoardPublication(context.Background(), legacyLease, now.Add(time.Second)); err != nil || !released {
		t.Fatalf("ReleaseResearchBoardPublication(legacy pending payload) = released %t, err %v", released, err)
	}
	legacyRequest := request
	legacyRequest.Target.DestinationRef = legacyDestination.Ref
	legacyRequest.IdempotencyKey = appresearch.IdempotencyKey(legacyRequest.ResearchRunID, legacyRequest.Target.Target, legacyRequest.Target.DestinationRef, legacyRequest.Digest)
	if _, err := owner.Commit(context.Background(), legacyRequest); err == nil {
		t.Fatal("owner rewrote a pending noncanonical payload under the same BOARD operation ID")
	}
	if _, err := owner.Reconcile(context.Background(), legacyRequest); err == nil {
		t.Fatal("Reconcile rewrote a pending noncanonical payload under the same BOARD operation ID")
	}
	if posts != 0 {
		t.Fatalf("BOARD send count after rejecting pending noncanonical payload = %d, want 0", posts)
	}

	if _, err := owner.Commit(context.Background(), request); err == nil {
		t.Fatal("Commit treated an ambiguous BOARD response as success")
	}
	receiptLookupFails = true
	if _, err := owner.Reconcile(context.Background(), request); err == nil {
		t.Fatal("Reconcile treated an unavailable canonical BOARD receipt as success")
	}
	if _, found, err := store.LoadResearchBoardPublication(context.Background(), scope, operationID); err != nil || found {
		t.Fatalf("canonical receipt lookup failure persisted an outcome: found %t, err %v", found, err)
	}
	receiptLookupFails = false
	reconciled, err := owner.Reconcile(context.Background(), request)
	if err != nil || reconciled.Receipt.DestinationRef != destination.Ref || reconciled.Receipt.CanonicalID != "message-001" || !reconciled.Receipt.VerifiedAt.IsZero() {
		t.Fatalf("Reconcile = %#v, err %v", reconciled, err)
	}
	reconciledAgain, err := owner.Reconcile(context.Background(), request)
	if err != nil || !reflect.DeepEqual(reconciledAgain.Receipt, reconciled.Receipt) || reconciledAgain.RetrySafe {
		t.Fatalf("repeated Reconcile = %#v, err %v; want same committed receipt", reconciledAgain, err)
	}
	verified, err := owner.Verify(context.Background(), request.Envelope, reconciled.Receipt)
	if err != nil || verified.VerifiedAt.IsZero() || verified.DestinationRef != destination.Ref {
		t.Fatalf("Verify = %#v, err %v", verified, err)
	}
	canonicalReceiptCases := []struct {
		name   string
		mutate func(*boardcommunication.SendReceipt)
	}{
		{name: "message id", mutate: func(receipt *boardcommunication.SendReceipt) { receipt.MessageID = "message-other" }},
		{name: "thread id", mutate: func(receipt *boardcommunication.SendReceipt) { receipt.ThreadID = "thread-other" }},
		{name: "status", mutate: func(receipt *boardcommunication.SendReceipt) { receipt.Status = "pending" }},
		{name: "stored at", mutate: func(receipt *boardcommunication.SendReceipt) { receipt.StoredAt = receipt.StoredAt.Add(time.Second) }},
	}
	for _, testCase := range canonicalReceiptCases {
		t.Run("canonical receipt mismatch/"+testCase.name, func(t *testing.T) {
			original := canonicalReceipt
			testCase.mutate(&canonicalReceipt)
			defer func() { canonicalReceipt = original }()
			if _, err := owner.Verify(context.Background(), request.Envelope, reconciled.Receipt); err == nil {
				t.Fatal("Verify accepted a canonical receipt that differed from the original send receipt")
			}
		})
	}
	duplicate, err := owner.Commit(context.Background(), request)
	if err != nil || duplicate.CanonicalID != reconciled.Receipt.CanonicalID || duplicate.DestinationRef != reconciled.Receipt.DestinationRef {
		t.Fatalf("exact duplicate Commit = %#v, err %v", duplicate, err)
	}
	if posts != 3 {
		t.Fatalf("BOARD send count = %d, want one unknown attempt and two exact idempotent replays", posts)
	}
	wrongDigest := request
	wrongDigestHash := sha256.Sum256([]byte(report + " changed"))
	wrongDigest.Digest = hex.EncodeToString(wrongDigestHash[:])
	wrongDigest.IdempotencyKey = appresearch.IdempotencyKey(wrongDigest.ResearchRunID, wrongDigest.Target.Target, wrongDigest.Target.DestinationRef, wrongDigest.Digest)
	if _, err := owner.Commit(context.Background(), wrongDigest); err == nil {
		t.Fatal("owner accepted a report whose bytes do not match its digest")
	}
	if _, err := owner.Reconcile(context.Background(), wrongDigest); err == nil {
		t.Fatal("Reconcile accepted a report whose bytes do not match its digest")
	}
	wrongScope := request
	wrongScope.Envelope.OwnerID = "other-user"
	if _, err := owner.Commit(context.Background(), wrongScope); err == nil {
		t.Fatal("owner accepted another user's scope")
	}
	if _, err := owner.Reconcile(context.Background(), wrongScope); err == nil {
		t.Fatal("Reconcile accepted another user's scope")
	}
	if posts != 3 {
		t.Fatalf("BOARD send count after digest/scope rejection = %d, want 3", posts)
	}
}

func TestResearchBoardOwnerRejectsUnboundConfigurations(t *testing.T) {
	pin := eventstore.BoardConnectionPin{OwnerID: "ren", PrincipalID: "ren-core", JournalInstanceID: "journal_0123456789abcdef0123456789abcdef", CutoverAt: time.Date(2026, 11, 28, 8, 0, 0, 0, time.UTC)}
	destination := appresearch.BoardDestination{Ref: "research-main", OwnerID: "ren", PrincipalID: "ren-core", ChannelID: "research", NewThread: true, Recipients: []string{"peer-a"}, DataScope: "user", Title: "Research"}
	if _, err := NewOwner(nil, nil, pin, []appresearch.BoardDestination{destination}, nil); err == nil {
		t.Fatal("owner accepted missing pinned BOARD client")
	}
	client := &boardcommunication.Client{}
	public := destination
	public.DataScope = "public"
	if _, err := NewOwner(client, nil, pin, []appresearch.BoardDestination{public}, nil); err == nil {
		t.Fatal("owner accepted public destination without authenticated user binding")
	}
	foreign := destination
	foreign.PrincipalID = "another-principal"
	if _, err := NewOwner(client, nil, pin, []appresearch.BoardDestination{foreign}, nil); err == nil {
		t.Fatal("owner accepted a different BOARD principal")
	}
}
