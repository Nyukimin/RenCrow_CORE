package boardcommunication

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestInboxExternalMessageFieldsKeepStrictCompatibility(t *testing.T) {
	tests := []struct {
		name          string
		response      string
		wantKind      string
		wantReplyTo   string
		wantCausation string
		wantHopCount  int
		wantError     bool
	}{
		{
			name: "current BOARD inbox fields",
			response: `{
				"items": [{
					"item_id": "item-alpha",
					"message_id": "message-alpha",
					"channel_id": "channel-alpha",
					"thread_id": "thread-alpha",
					"sender_id": "peer-alpha",
					"created_at": "2026-11-06T03:04:05Z",
					"body": "synthetic inbox body",
					"mentions": ["ren"],
					"recipients": ["ren"],
					"ack_receipt": "synthetic-private-ack-receipt",
					"kind": "request",
					"reply_to": "message-parent",
					"causation_id": "message-origin",
					"hop_count": 2
				}],
				"next_cursor": "item-alpha",
				"correlation_id": "synthetic"
			}`,
			wantKind:      "request",
			wantReplyTo:   "message-parent",
			wantCausation: "message-origin",
			wantHopCount:  2,
		},
		{
			name: "older BOARD response omits added fields",
			response: `{
				"items": [{
					"item_id": "item-alpha",
					"message_id": "message-alpha",
					"channel_id": "channel-alpha",
					"thread_id": "thread-alpha",
					"sender_id": "peer-alpha",
					"created_at": "2026-11-06T03:04:05Z",
					"body": "synthetic inbox body",
					"mentions": ["ren"],
					"recipients": ["ren"],
					"ack_receipt": "synthetic-private-ack-receipt"
				}],
				"next_cursor": "item-alpha",
				"correlation_id": "synthetic"
			}`,
		},
		{
			name: "unknown inbox field remains rejected",
			response: `{
				"items": [{
					"item_id": "item-alpha",
					"message_id": "message-alpha",
					"channel_id": "channel-alpha",
					"thread_id": "thread-alpha",
					"sender_id": "peer-alpha",
					"created_at": "2026-11-06T03:04:05Z",
					"body": "synthetic inbox body",
					"mentions": ["ren"],
					"recipients": ["ren"],
					"ack_receipt": "synthetic-private-ack-receipt",
					"future_wire_field": "unrecognized"
				}],
				"next_cursor": "item-alpha",
				"correlation_id": "synthetic"
			}`,
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _ := newFakeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/inbox" {
					t.Errorf("Inbox used an unexpected route")
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.response)
			}))

			page, err := client.Inbox(context.Background(), PageRequest{Limit: 4})
			if test.wantError {
				if !errors.Is(err, ErrProtocol) {
					t.Fatalf("Inbox error = %v, want %v", err, ErrProtocol)
				}
				return
			}
			if err != nil {
				t.Fatalf("Inbox returned an error: %v", err)
			}
			if len(page.Items) != 1 {
				t.Fatalf("Inbox items = %d, want 1", len(page.Items))
			}
			item := page.Items[0]
			if item.Kind != test.wantKind || item.ReplyTo != test.wantReplyTo || item.CausationID != test.wantCausation ||
				item.HopCount != test.wantHopCount {
				t.Fatalf("Inbox external fields = (%q, %q, %q, %d), want (%q, %q, %q, %d)",
					item.Kind, item.ReplyTo, item.CausationID, item.HopCount,
					test.wantKind, test.wantReplyTo, test.wantCausation, test.wantHopCount)
			}
		})
	}
}

func TestMessageStatusNotificationDeliveriesKeepStrictCompatibility(t *testing.T) {
	const currentDelivery = `{
		"delivery_id": "del-alpha",
		"event_id": "evt-alpha",
		"subscription_id": "sub-alpha",
		"provider": "grok",
		"status": "terminal",
		"attempt": 8,
		"attempted_at": "2026-11-06T03:05:01Z",
		"last_result_at": "2026-11-06T03:05:02Z",
		"error_code": "provider_outcome_unknown",
		"outcome_unknown": true
	}`
	const unknownFieldDelivery = `{
		"delivery_id": "del-alpha",
		"event_id": "evt-alpha",
		"subscription_id": "sub-alpha",
		"provider": "grok",
		"status": "terminal",
		"attempt": 8,
		"outcome_unknown": true,
		"future_wire_field": "unrecognized"
	}`

	tests := []struct {
		name         string
		delivery     string
		wantDelivery bool
		wantError    bool
	}{
		{name: "current notification delivery fields", delivery: currentDelivery, wantDelivery: true},
		{name: "older BOARD response omits added field", wantDelivery: false},
		{name: "unknown delivery field remains rejected", delivery: unknownFieldDelivery, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _ := newFakeClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/messages/message-alpha/status" {
					t.Errorf("MessageStatus used an unexpected route")
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, messageStatusResponseWithDelivery(test.delivery))
			}))

			statuses, err := client.MessageStatus(context.Background(), "message-alpha")
			if test.wantError {
				if !errors.Is(err, ErrProtocol) {
					t.Fatalf("MessageStatus error = %v, want %v", err, ErrProtocol)
				}
				return
			}
			if err != nil {
				t.Fatalf("MessageStatus returned an error: %v", err)
			}
			if len(statuses) != 1 {
				t.Fatalf("MessageStatus recipients = %d, want 1", len(statuses))
			}
			deliveries := statuses[0].NotificationDeliveries
			if !test.wantDelivery {
				if len(deliveries) != 0 {
					t.Fatalf("NotificationDeliveries = %d, want 0 for an older response", len(deliveries))
				}
				return
			}
			if len(deliveries) != 1 {
				t.Fatalf("NotificationDeliveries = %d, want 1", len(deliveries))
			}
			delivery := deliveries[0]
			wantAttemptedAt := time.Date(2026, time.November, 6, 3, 5, 1, 0, time.UTC)
			wantResultAt := time.Date(2026, time.November, 6, 3, 5, 2, 0, time.UTC)
			if delivery.DeliveryID != "del-alpha" || delivery.EventID != "evt-alpha" || delivery.SubscriptionID != "sub-alpha" ||
				delivery.Provider != "grok" || delivery.Status != "terminal" || delivery.Attempt != 8 ||
				delivery.AttemptedAt == nil || !delivery.AttemptedAt.Equal(wantAttemptedAt) || delivery.AcceptedAt != nil ||
				delivery.LastStatusCode != 0 || delivery.LastResultAt == nil ||
				!delivery.LastResultAt.Equal(wantResultAt) || delivery.ErrorCode != "provider_outcome_unknown" || !delivery.OutcomeUnknown {
				t.Fatalf("MessageStatus did not preserve all notification delivery fields: %+v", delivery)
			}
		})
	}
}

func messageStatusResponseWithDelivery(delivery string) string {
	deliveryField := ""
	if delivery != "" {
		deliveryField = `,"notification_deliveries":[` + delivery + `]`
	}
	return `{
		"message_id": "message-alpha",
		"recipients": [{
			"message_id": "message-alpha",
			"principal_id": "peer-alpha",
			"stored_at": "2026-11-06T03:04:05Z",
			"notification_attempted_at": null,
			"notification_accepted_at": null,
			"body_read_at": null,
			"processing_started_at": null,
			"reply_observed_at": null` + deliveryField + `
		}],
		"correlation_id": "synthetic"
	}`
}
