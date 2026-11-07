package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const boardOwnerHTTPTestToken = "0123456789abcdef0123456789abcdef"

func TestBoardOwnerHTTPRejectsRemoteAndUnauthorizedBeforeCallback(t *testing.T) {
	calls := 0
	handler := newBoardOwnerHTTPTestHandler(t, boardOwnerHTTPCallbacks{
		Accept: func(context.Context, string, boardOwnerHTTPRequest) (boardOwnerHTTPAcceptedReceipt, error) {
			calls++
			return boardOwnerHTTPTestAcceptedReceipt(), nil
		},
		Status: func(context.Context, string, modulecore.TurnID) (boardOwnerHTTPStatusReceipt, bool, error) {
			calls++
			return boardOwnerHTTPTestStatusReceipt("accepted"), true, nil
		},
	})

	remote := boardOwnerHTTPTestRequest(http.MethodPost, boardOwnerRequestsPath, boardOwnerHTTPValidBody())
	remote.RemoteAddr = "203.0.113.8:41000"
	if got := boardOwnerHTTPDo(handler, remote).Code; got != http.StatusNotFound {
		t.Fatalf("remote request status = %d, want 404", got)
	}

	proxied := boardOwnerHTTPTestRequest(http.MethodPost, boardOwnerRequestsPath, boardOwnerHTTPValidBody())
	proxied.Header.Set("X-Forwarded-For", "203.0.113.8")
	if got := boardOwnerHTTPDo(handler, proxied).Code; got != http.StatusNotFound {
		t.Fatalf("proxied request status = %d, want 404", got)
	}

	unauthorized := boardOwnerHTTPTestRequest(http.MethodPost, boardOwnerRequestsPath, "not-json")
	unauthorized.Header.Del("Authorization")
	if got := boardOwnerHTTPDo(handler, unauthorized).Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthorized request status = %d, want 401", got)
	}

	duplicateAuth := boardOwnerHTTPTestRequest(http.MethodGet, boardOwnerRequestsPath+"/"+string(modulecore.NewTurnID()), "")
	duplicateAuth.Header.Add("Authorization", "Bearer "+boardOwnerHTTPTestToken)
	if got := boardOwnerHTTPDo(handler, duplicateAuth).Code; got != http.StatusUnauthorized {
		t.Fatalf("duplicate Authorization status = %d, want 401", got)
	}
	if calls != 0 {
		t.Fatalf("callback calls = %d, want 0 before direct-local authenticated request", calls)
	}
}

func TestBoardOwnerHTTPRejectsNonCanonicalPOSTJSONBeforeCallback(t *testing.T) {
	calls := 0
	handler := newBoardOwnerHTTPTestHandler(t, boardOwnerHTTPCallbacks{
		Accept: func(context.Context, string, boardOwnerHTTPRequest) (boardOwnerHTTPAcceptedReceipt, error) {
			calls++
			return boardOwnerHTTPTestAcceptedReceipt(), nil
		},
	})
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "caller owner", body: `{"owner_id":"owner-7","channel_id":"channel-1","peer_id":"peer-2","title":"Title","instruction":"Do work","idempotency_key":"key-1"}`},
		{name: "caller actor", body: `{"actor_id":"trusted","channel_id":"channel-1","peer_id":"peer-2","title":"Title","instruction":"Do work","idempotency_key":"key-1"}`},
		{name: "caller Mio result", body: `{"mio_result":"trusted","channel_id":"channel-1","peer_id":"peer-2","title":"Title","instruction":"Do work","idempotency_key":"key-1"}`},
		{name: "unknown field", body: `{"channel_id":"channel-1","peer_id":"peer-2","title":"Title","instruction":"Do work","idempotency_key":"key-1","extra":"x"}`},
		{name: "case alias", body: `{"Channel_ID":"channel-1","peer_id":"peer-2","title":"Title","instruction":"Do work","idempotency_key":"key-1"}`},
		{name: "duplicate key", body: `{"channel_id":"channel-1","channel_id":"channel-2","peer_id":"peer-2","title":"Title","instruction":"Do work","idempotency_key":"key-1"}`},
		{name: "null value", body: `{"channel_id":null,"peer_id":"peer-2","title":"Title","instruction":"Do work","idempotency_key":"key-1"}`},
		{name: "trailing JSON", body: boardOwnerHTTPValidBody() + ` {}`},
		{name: "invalid JSON", body: `{"channel_id":`},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := boardOwnerHTTPTestRequest(http.MethodPost, boardOwnerRequestsPath, test.body)
			response := boardOwnerHTTPDo(handler, req)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body=%s, want 400", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "channel-1") || strings.Contains(response.Body.String(), "peer-2") {
				t.Fatalf("response echoed request data: %s", response.Body.String())
			}
		})
	}

	tooLarge := `{"channel_id":"channel-1","peer_id":"peer-2","title":"Title","instruction":"` + strings.Repeat("x", conversation.MaxUserMessageBytes+1) + `","idempotency_key":"key-1"}`
	response := boardOwnerHTTPDo(handler, boardOwnerHTTPTestRequest(http.MethodPost, boardOwnerRequestsPath, tooLarge))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversized instruction status = %d, body=%s, want 400", response.Code, response.Body.String())
	}
	if calls != 0 {
		t.Fatalf("callback calls = %d, want 0 for malformed or oversized input", calls)
	}
}

func TestBoardOwnerHTTPIssues202OnlyForExactDurableAcceptance(t *testing.T) {
	var calls int
	var seenOwners []string
	var seenRequest boardOwnerHTTPRequest
	receipt := boardOwnerHTTPTestAcceptedReceipt()
	marker := "private-publication-receipt-marker"
	handler := newBoardOwnerHTTPTestHandler(t, boardOwnerHTTPCallbacks{
		Accept: func(_ context.Context, ownerID string, request boardOwnerHTTPRequest) (boardOwnerHTTPAcceptedReceipt, error) {
			calls++
			seenOwners = append(seenOwners, ownerID)
			seenRequest = request
			return receipt, nil
		},
	})
	body := `{"channel_id":"channel-1","peer_id":"peer-2","title":"Title","instruction":"keep  two spaces","idempotency_key":"key-1"}`
	firstRequest := boardOwnerHTTPTestRequest(http.MethodPost, boardOwnerRequestsPath, body)
	firstRequest.Header.Set("X-RenCrow-Actor-ID", "caller-controlled-actor")
	first := boardOwnerHTTPDo(handler, firstRequest)
	second := boardOwnerHTTPDo(handler, boardOwnerHTTPTestRequest(http.MethodPost, boardOwnerRequestsPath, body))
	if first.Code != http.StatusAccepted || second.Code != http.StatusAccepted {
		t.Fatalf("replay statuses = %d/%d, want 202/202", first.Code, second.Code)
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("same durable request returned different acceptance bodies: %s / %s", first.Body.String(), second.Body.String())
	}
	if len(seenOwners) != 2 || seenOwners[0] != "owner-7" || seenOwners[1] != "owner-7" ||
		seenRequest.ChannelID != "channel-1" || seenRequest.PeerID != "peer-2" ||
		seenRequest.Title != "Title" || seenRequest.Instruction != "keep  two spaces" || seenRequest.IdempotencyKey != "key-1" || calls != 2 {
		t.Fatalf("callback owners/request/calls = %q/%+v/%d", seenOwners, seenRequest, calls)
	}
	var accepted map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode acceptance body: %v", err)
	}
	wantAcceptedKeys := []string{"owner_id", "session_id", "turn_id", "trace_id", "input_message_id", "stage", "accepted_at"}
	if !boardOwnerHTTPExactKeys(accepted, wantAcceptedKeys...) {
		t.Fatalf("acceptance keys = %v, want only %v", boardOwnerHTTPKeys(accepted), wantAcceptedKeys)
	}
	if accepted["owner_id"] != receipt.OwnerID || accepted["session_id"] != string(receipt.SessionID) ||
		accepted["turn_id"] != string(receipt.TurnID) || accepted["trace_id"] != string(receipt.TraceID) ||
		accepted["input_message_id"] != string(receipt.InputMessageID) || accepted["stage"] != "accepted" ||
		accepted["accepted_at"] != receipt.AcceptedAt.Format(time.RFC3339Nano) {
		t.Fatalf("acceptance response does not match durable receipt: %#v", accepted)
	}
	if strings.Contains(first.Body.String(), marker) || strings.Contains(first.Body.String(), "keep  two spaces") {
		t.Fatalf("acceptance response exposed private data: %s", first.Body.String())
	}

	for _, test := range []struct {
		name       string
		err        error
		receipt    boardOwnerHTTPAcceptedReceipt
		wantStatus int
		wantCode   string
	}{
		{name: "conflict", err: fmt.Errorf("%s: %w", marker, errBoardOwnerHTTPConflict), wantStatus: http.StatusConflict, wantCode: "idempotency_conflict"},
		{name: "invalid", err: fmt.Errorf("%s: %w", marker, errBoardOwnerHTTPInvalid), wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "unknown commit", err: errors.New(marker), wantStatus: http.StatusServiceUnavailable, wantCode: "request_outcome_unknown"},
		{name: "receipt not durable", receipt: func() boardOwnerHTTPAcceptedReceipt {
			value := boardOwnerHTTPTestAcceptedReceipt()
			value.Durable = false
			return value
		}(), wantStatus: http.StatusServiceUnavailable, wantCode: "request_outcome_unknown"},
		{name: "wrong owner receipt", receipt: func() boardOwnerHTTPAcceptedReceipt {
			value := boardOwnerHTTPTestAcceptedReceipt()
			value.OwnerID = "other-owner"
			return value
		}(), wantStatus: http.StatusServiceUnavailable, wantCode: "request_outcome_unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := test.receipt
			if candidate.OwnerID == "" && candidate.TurnID == "" {
				candidate = boardOwnerHTTPTestAcceptedReceipt()
			}
			failureHandler := newBoardOwnerHTTPTestHandler(t, boardOwnerHTTPCallbacks{
				Accept: func(context.Context, string, boardOwnerHTTPRequest) (boardOwnerHTTPAcceptedReceipt, error) {
					return candidate, test.err
				},
			})
			response := boardOwnerHTTPDo(failureHandler, boardOwnerHTTPTestRequest(http.MethodPost, boardOwnerRequestsPath, body))
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", response.Code, response.Body.String(), test.wantStatus)
			}
			var payload map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload["error"] != test.wantCode {
				t.Fatalf("error payload = %s, want %q (decode error %v)", response.Body.String(), test.wantCode, err)
			}
			if strings.Contains(response.Body.String(), marker) {
				t.Fatalf("error response leaked internal details: %s", response.Body.String())
			}
		})
	}
}

func TestBoardOwnerHTTPGETRequiresCanonicalOwnerScopedStatus(t *testing.T) {
	turnID := modulecore.NewTurnID()
	calls := 0
	var seenOwner string
	var seenTurn modulecore.TurnID
	statusReceipt := boardOwnerHTTPTestStatusReceipt("publication_pending")
	statusReceipt.TurnID = turnID
	handler := newBoardOwnerHTTPTestHandler(t, boardOwnerHTTPCallbacks{
		Status: func(_ context.Context, ownerID string, canonicalTurnID modulecore.TurnID) (boardOwnerHTTPStatusReceipt, bool, error) {
			calls++
			seenOwner, seenTurn = ownerID, canonicalTurnID
			return statusReceipt, true, nil
		},
	})
	response := boardOwnerHTTPDo(handler, boardOwnerHTTPTestRequest(http.MethodGet, boardOwnerRequestsPath+"/"+string(turnID), ""))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", response.Code, response.Body.String())
	}
	if calls != 1 || seenOwner != "owner-7" || seenTurn != turnID {
		t.Fatalf("status callback scope = calls %d owner %q turn %q", calls, seenOwner, seenTurn)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode status body: %v", err)
	}
	wantKeys := []string{"owner_id", "session_id", "turn_id", "trace_id", "input_message_id", "stage", "accepted_at", "updated_at"}
	if !boardOwnerHTTPExactKeys(payload, wantKeys...) {
		t.Fatalf("status keys = %v, want only %v", boardOwnerHTTPKeys(payload), wantKeys)
	}
	if payload["owner_id"] != "owner-7" || payload["session_id"] != string(statusReceipt.SessionID) ||
		payload["turn_id"] != string(turnID) || payload["trace_id"] != string(statusReceipt.TraceID) ||
		payload["input_message_id"] != string(statusReceipt.InputMessageID) || payload["stage"] != "publication_pending" ||
		payload["accepted_at"] != statusReceipt.AcceptedAt.Format(time.RFC3339Nano) ||
		payload["updated_at"] != statusReceipt.UpdatedAt.Format(time.RFC3339Nano) {
		t.Fatalf("status response does not match scoped receipt: %#v", payload)
	}

	for _, path := range []string{
		boardOwnerRequestsPath + "/not-a-canonical-turn",
		boardOwnerRequestsPath + "/" + string(turnID) + "?owner_id=other-owner",
	} {
		invalid := boardOwnerHTTPDo(handler, boardOwnerHTTPTestRequest(http.MethodGet, path, ""))
		if invalid.Code != http.StatusBadRequest {
			t.Fatalf("GET %q status = %d body=%s, want 400", path, invalid.Code, invalid.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("malformed/query-scoped GET reached callback: calls=%d", calls)
	}

	for _, test := range []struct {
		name       string
		receipt    boardOwnerHTTPStatusReceipt
		found      bool
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "not found", found: false, wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "wrong owner", receipt: func() boardOwnerHTTPStatusReceipt {
			value := boardOwnerHTTPTestStatusReceipt("accepted")
			value.OwnerID = "other-owner"
			return value
		}(), found: true, wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "wrong turn", receipt: func() boardOwnerHTTPStatusReceipt {
			value := boardOwnerHTTPTestStatusReceipt("accepted")
			value.TurnID = modulecore.NewTurnID()
			return value
		}(), found: true, wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "unknown stage", receipt: boardOwnerHTTPTestStatusReceipt("future_stage"), found: true, wantStatus: http.StatusServiceUnavailable, wantCode: "request_outcome_unknown"},
		{name: "callback failure", err: errors.New("private publication receipt marker"), wantStatus: http.StatusServiceUnavailable, wantCode: "request_outcome_unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := test.receipt
			if candidate.OwnerID == "" && candidate.TurnID == "" {
				candidate = boardOwnerHTTPTestStatusReceipt("accepted")
			}
			if test.name != "wrong turn" {
				candidate.TurnID = turnID
			}
			failureHandler := newBoardOwnerHTTPTestHandler(t, boardOwnerHTTPCallbacks{
				Status: func(context.Context, string, modulecore.TurnID) (boardOwnerHTTPStatusReceipt, bool, error) {
					return candidate, test.found, test.err
				},
			})
			failure := boardOwnerHTTPDo(failureHandler, boardOwnerHTTPTestRequest(http.MethodGet, boardOwnerRequestsPath+"/"+string(turnID), ""))
			if failure.Code != test.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", failure.Code, failure.Body.String(), test.wantStatus)
			}
			var body map[string]string
			if err := json.Unmarshal(failure.Body.Bytes(), &body); err != nil || body["error"] != test.wantCode {
				t.Fatalf("error body = %s, want %q (decode error %v)", failure.Body.String(), test.wantCode, err)
			}
			if strings.Contains(failure.Body.String(), "private publication receipt marker") {
				t.Fatalf("status error leaked callback details: %s", failure.Body.String())
			}
		})
	}
}

func newBoardOwnerHTTPTestHandler(t *testing.T, callbacks boardOwnerHTTPCallbacks) http.Handler {
	t.Helper()
	handler, err := newBoardOwnerHTTPHandler("owner-7", []byte(boardOwnerHTTPTestToken), callbacks)
	if err != nil {
		t.Fatalf("newBoardOwnerHTTPHandler: %v", err)
	}
	return handler
}

func boardOwnerHTTPTestRequest(method, target, body string) *http.Request {
	url := "http://127.0.0.1:8097" + target
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:49152"
	req.Header.Set("Authorization", "Bearer "+boardOwnerHTTPTestToken)
	return req
}

func boardOwnerHTTPDo(handler http.Handler, req *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func boardOwnerHTTPValidBody() string {
	return `{"channel_id":"channel-1","peer_id":"peer-2","title":"Title","instruction":"Do work","idempotency_key":"key-1"}`
}

func boardOwnerHTTPTestAcceptedReceipt() boardOwnerHTTPAcceptedReceipt {
	return boardOwnerHTTPAcceptedReceipt{
		OwnerID: "owner-7", SessionID: modulecore.NewSessionID(), TurnID: modulecore.NewTurnID(),
		TraceID: modulecore.NewTraceID(), InputMessageID: modulecore.NewMessageID(),
		AcceptedAt: time.Date(2026, 11, 5, 5, 4, 3, 0, time.UTC), Durable: true,
	}
}

func boardOwnerHTTPTestStatusReceipt(stage string) boardOwnerHTTPStatusReceipt {
	accepted := boardOwnerHTTPTestAcceptedReceipt()
	return boardOwnerHTTPStatusReceipt{
		OwnerID: accepted.OwnerID, SessionID: accepted.SessionID, TurnID: accepted.TurnID, TraceID: accepted.TraceID,
		InputMessageID: accepted.InputMessageID, Stage: stage, AcceptedAt: accepted.AcceptedAt,
		UpdatedAt: accepted.AcceptedAt.Add(time.Minute), Durable: true,
	}
}

func boardOwnerHTTPExactKeys(value map[string]any, expected ...string) bool {
	if len(value) != len(expected) {
		return false
	}
	for _, key := range expected {
		if _, ok := value[key]; !ok {
			return false
		}
	}
	return true
}

func boardOwnerHTTPKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	return keys
}
