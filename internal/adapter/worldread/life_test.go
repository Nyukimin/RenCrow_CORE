package worldread

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLifeCommandUsesSeparateBearerAndExistingR07Envelope(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotKey string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.EscapedPath()
		gotAuth, gotKey = r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode command request: %v", err)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"schema_version": 1,
			"receipt": map[string]any{
				"receipt_id": "receipt-1", "status": "committed", "transaction_id": "txn-1",
				"event_ids": []string{"event-1"}, "first_seq": 5, "last_seq": 5, "state_seq": 5,
				"recorded_at": "2026-11-14T01:02:03Z",
			},
		})
	}))
	defer server.Close()

	client, err := NewLife(LifeConfig{
		BaseURL: server.URL, Credential: "life-token", WorldID: "rencrow-home", TimelineID: "timeline-1",
		TimelineEpoch: "epoch-1", ActorIDs: []string{"mio"},
	})
	if err != nil {
		t.Fatalf("NewLife: %v", err)
	}
	result, err := client.Command(context.Background(), "routine-world-key-1", HomeActionMove, HomeActionPayload{
		ActorID: "mio", RoomID: "living", SlotID: "bed-mio",
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/worlds/rencrow-home/timelines/timeline-1/commands" ||
		gotAuth != "Bearer life-token" || gotKey != "routine-world-key-1" {
		t.Fatalf("request method=%q path=%q auth=%q key=%q", gotMethod, gotPath, gotAuth, gotKey)
	}
	if gotBody["action"] != HomeActionMove || gotBody["timeline_epoch"] != "epoch-1" {
		t.Fatalf("request envelope = %#v", gotBody)
	}
	payload, ok := gotBody["payload"].(map[string]any)
	if !ok || payload["actor_id"] != "mio" || payload["room_id"] != "living" || payload["slot_id"] != "bed-mio" || len(payload) != 3 {
		t.Fatalf("request payload = %#v", gotBody["payload"])
	}
	if result.SchemaVersion != 1 || result.Receipt.ReceiptID != "receipt-1" || result.Receipt.Status != "committed" ||
		result.Receipt.TransactionID != "txn-1" || len(result.Receipt.EventIDs) != 1 || result.Receipt.StateSeq != 5 || result.Receipt.RecordedAt.IsZero() {
		t.Fatalf("command result = %+v", result)
	}
}

func TestLifeCommandRejectsOutOfScopeActorAndMismatchedPayloadBeforePost(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	client, err := NewLife(LifeConfig{
		BaseURL: server.URL, Credential: "life-token", WorldID: "world", TimelineID: "timeline",
		TimelineEpoch: "epoch-1", ActorIDs: []string{"mio"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		action  string
		payload HomeActionPayload
	}{
		{name: "out of scope actor", action: HomeActionSleep, payload: HomeActionPayload{ActorID: "shiro"}},
		{name: "move payload has activity", action: HomeActionMove, payload: HomeActionPayload{ActorID: "mio", RoomID: "living", ActivityID: "home:mio:1"}},
		{name: "unknown action", action: "life.home.teleport", payload: HomeActionPayload{ActorID: "mio"}},
		{name: "wake requires instance", action: HomeActionWake, payload: HomeActionPayload{ActorID: "mio"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := client.Command(context.Background(), "routine-world-key", test.action, test.payload); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Command error = %v, want ErrInvalidConfig", err)
			}
		})
	}
	if requests != 0 {
		t.Fatalf("invalid commands sent %d HTTP requests", requests)
	}
}

func TestLifeCommandClassifiesHandlerRejectionAndRetryReceipt(t *testing.T) {
	status := http.StatusConflict
	receiptStatus := "rejected"
	reason := "handler_error"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, status, map[string]any{"schema_version": 1, "receipt": map[string]any{
			"receipt_id": "receipt-1", "status": receiptStatus, "reason": reason,
			"event_ids": []string{}, "first_seq": 0, "last_seq": 0, "state_seq": 4,
			"recorded_at": "2026-11-14T01:02:03Z",
		}})
	}))
	defer server.Close()
	client, err := NewLife(LifeConfig{
		BaseURL: server.URL, Credential: "life-token", WorldID: "world", TimelineID: "timeline",
		TimelineEpoch: "epoch-1", ActorIDs: []string{"mio"}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Command(context.Background(), "routine-world-key", HomeActionSleep, HomeActionPayload{ActorID: "mio"})
	if !errors.Is(err, ErrCommandRejected) || result.Receipt.Status != "rejected" {
		t.Fatalf("rejected command = %+v, %v", result, err)
	}
	status, receiptStatus, reason = http.StatusOK, "retry", "temporarily_unavailable"
	result, err = client.Command(context.Background(), "routine-world-key-retry", HomeActionSleep, HomeActionPayload{ActorID: "mio"})
	if !errors.Is(err, ErrCommandRetry) || result.Receipt.Status != "retry" {
		t.Fatalf("retry command = %+v, %v", result, err)
	}
}

func TestLifeCommandAcceptsValidated422HandlerReceipt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"schema_version": 1, "receipt": map[string]any{
			"receipt_id": "receipt-rejected", "status": "rejected", "reason": "layout_mismatch",
			"event_ids": []string{}, "first_seq": 0, "last_seq": 0, "state_seq": 4,
			"recorded_at": "2026-11-14T01:02:03Z",
		}})
	}))
	defer server.Close()
	client, err := NewLife(LifeConfig{
		BaseURL: server.URL, Credential: "life-token", WorldID: "world", TimelineID: "timeline",
		TimelineEpoch: "epoch-1", ActorIDs: []string{"mio"}, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Command(context.Background(), "routine-world-key", HomeActionSleep, HomeActionPayload{ActorID: "mio"})
	if !errors.Is(err, ErrCommandRejected) || result.Receipt.ReceiptID != "receipt-rejected" || result.Receipt.Reason != "layout_mismatch" {
		t.Fatalf("422 rejected command result=%+v error=%v", result, err)
	}
}
