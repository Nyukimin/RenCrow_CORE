package boardcommunication

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// SendRequest describes one BOARD send. Exactly one of ThreadID (existing
// thread) or an empty ThreadID with Title (new thread) is expected.
type SendRequest struct {
	ChannelID              string
	ThreadID               string
	Title                  string
	Body                   string
	Recipients             []string
	Mentions               []string
	ReplyTo                string
	Kind                   string
	IdempotencyKey         string
	ProcessingClaimID      string
	ProcessingClaimReceipt string
}

// PreparedSend is an immutable snapshot of one exact BOARD wire attempt.
// Accessors return copies of byte data so callers cannot mutate the snapshot.
type PreparedSend struct {
	path           string
	idempotencyKey string
	payload        []byte
	payloadSHA256  [32]byte
}

// Path returns the fixed canonical v1 endpoint for this send.
func (p PreparedSend) Path() string { return p.path }

// IdempotencyKey returns the logical BOARD idempotency key.
func (p PreparedSend) IdempotencyKey() string { return p.idempotencyKey }

// JSONBytes returns a copy of the exact prepared request bytes.
func (p PreparedSend) JSONBytes() []byte { return append([]byte(nil), p.payload...) }

// Request decodes the prepared payload into its public request representation.
func (p PreparedSend) Request() (SendRequest, error) {
	wire, _, err := validatePreparedSend(p)
	if err != nil {
		return SendRequest{}, ErrInvalidPreparedSend
	}
	return SendRequest{
		ChannelID:              wire.ChannelID,
		ThreadID:               wire.ThreadID,
		Title:                  wire.Title,
		Body:                   wire.Body,
		Recipients:             cloneStrings(wire.Recipients),
		Mentions:               cloneStrings(wire.Mentions),
		ReplyTo:                wire.ReplyTo,
		Kind:                   wire.Kind,
		IdempotencyKey:         wire.IdempotencyKey,
		ProcessingClaimID:      wire.ProcessingClaimID,
		ProcessingClaimReceipt: wire.ProcessingClaimReceipt,
	}, nil
}

// SHA256 returns the hash of the exact prepared request bytes.
func (p PreparedSend) SHA256() [32]byte { return p.payloadSHA256 }

type sendWire struct {
	ChannelID              string   `json:"channel_id"`
	ThreadID               string   `json:"thread_id,omitempty"`
	Title                  string   `json:"title,omitempty"`
	Body                   string   `json:"body"`
	Recipients             []string `json:"recipients"`
	Mentions               []string `json:"mentions,omitempty"`
	ReplyTo                string   `json:"reply_to,omitempty"`
	Kind                   string   `json:"kind,omitempty"`
	IdempotencyKey         string   `json:"idempotency_key"`
	ProcessingClaimID      string   `json:"processing_claim_id,omitempty"`
	ProcessingClaimReceipt string   `json:"processing_claim_receipt,omitempty"`
}

// PrepareSend freezes the canonical endpoint, idempotency key, exact JSON
// payload, and payload hash before publication is attempted.
func (c *Client) PrepareSend(request SendRequest) (PreparedSend, error) {
	if len(request.Recipients) > 32 || len(request.Mentions) > 32 {
		return PreparedSend{}, ErrInvalidRequest
	}
	wire := sendWire{
		ChannelID:              request.ChannelID,
		ThreadID:               request.ThreadID,
		Title:                  request.Title,
		Body:                   request.Body,
		Recipients:             cloneStrings(request.Recipients),
		Mentions:               cloneStrings(request.Mentions),
		ReplyTo:                request.ReplyTo,
		Kind:                   request.Kind,
		IdempotencyKey:         request.IdempotencyKey,
		ProcessingClaimID:      request.ProcessingClaimID,
		ProcessingClaimReceipt: request.ProcessingClaimReceipt,
	}
	path, err := validateSendWire(wire)
	if err != nil {
		return PreparedSend{}, err
	}
	payload, err := json.Marshal(wire)
	if err != nil || len(payload) > maxRequestBodyBytes {
		return PreparedSend{}, ErrInvalidRequest
	}
	return PreparedSend{
		path:           path,
		idempotencyKey: wire.IdempotencyKey,
		payload:        append([]byte(nil), payload...),
		payloadSHA256:  sha256.Sum256(payload),
	}, nil
}

// RestorePreparedSend restores an immutable send attempt from the exact
// persisted bytes and hash. It derives the endpoint and logical key from the
// payload without re-marshalling it.
func RestorePreparedSend(payload []byte, expectedSHA256 [32]byte) (PreparedSend, error) {
	if len(payload) == 0 || len(payload) > maxRequestBodyBytes || sha256.Sum256(payload) != expectedSHA256 {
		return PreparedSend{}, ErrInvalidPreparedSend
	}
	wire, err := decodeSendWire(payload)
	if err != nil {
		return PreparedSend{}, ErrInvalidPreparedSend
	}
	path, err := validateSendWire(wire)
	if err != nil {
		return PreparedSend{}, ErrInvalidPreparedSend
	}
	return PreparedSend{
		path:           path,
		idempotencyKey: wire.IdempotencyKey,
		payload:        append([]byte(nil), payload...),
		payloadSHA256:  expectedSHA256,
	}, nil
}

// SendReceipt is the typed receipt returned by BOARD after a send is stored.
type SendReceipt struct {
	MessageID string    `json:"message_id"`
	ThreadID  string    `json:"thread_id"`
	StoredAt  time.Time `json:"stored_at"`
	Status    string    `json:"status"`
}

// SendPrepared submits one already-prepared exact payload. It never retries or
// regenerates an idempotency key.
func (c *Client) SendPrepared(ctx context.Context, prepared PreparedSend) (SendReceipt, error) {
	wire, path, err := validatePreparedSend(prepared)
	if err != nil {
		return SendReceipt{}, err
	}
	response, release, err := c.do(ctx, http.MethodPost, path, prepared.payload, prepared.idempotencyKey)
	if err != nil {
		return SendReceipt{}, err
	}
	defer release()
	defer response.Body.Close()
	body, err := readBounded(response.Body, maxResponseBodyBytes)
	if err != nil {
		return SendReceipt{}, ErrUnknownOutcome
	}
	if response.StatusCode != http.StatusCreated {
		return SendReceipt{}, sendResponseError(response.StatusCode, body, response.Header)
	}
	var envelope struct {
		Receipt       SendReceipt     `json:"receipt"`
		Thread        json.RawMessage `json:"thread,omitempty"`
		CorrelationID string          `json:"correlation_id"`
	}
	if err := decodeStrictJSON(body, &envelope); err != nil {
		return SendReceipt{}, ErrUnknownOutcome
	}
	receipt := envelope.Receipt
	if !validBoardID(receipt.MessageID) || !validBoardID(receipt.ThreadID) || receipt.StoredAt.IsZero() || receipt.Status != "stored" {
		return SendReceipt{}, ErrUnknownOutcome
	}
	if wire.ThreadID != "" && receipt.ThreadID != wire.ThreadID {
		return SendReceipt{}, ErrUnknownOutcome
	}
	return receipt, nil
}

func validatePreparedSend(prepared PreparedSend) (sendWire, string, error) {
	if len(prepared.payload) == 0 || len(prepared.payload) > maxRequestBodyBytes ||
		sha256.Sum256(prepared.payload) != prepared.payloadSHA256 || !validHeaderValue(prepared.idempotencyKey) {
		return sendWire{}, "", ErrInvalidPreparedSend
	}
	wire, err := decodeSendWire(prepared.payload)
	if err != nil {
		return sendWire{}, "", ErrInvalidPreparedSend
	}
	path, err := validateSendWire(wire)
	if err != nil || path != prepared.path || wire.IdempotencyKey != prepared.idempotencyKey {
		return sendWire{}, "", ErrInvalidPreparedSend
	}
	return wire, path, nil
}

func decodeSendWire(payload []byte) (sendWire, error) {
	var wire sendWire
	if err := decodeStrictJSON(payload, &wire); err != nil {
		return sendWire{}, err
	}
	return wire, nil
}

func validateSendWire(wire sendWire) (string, error) {
	if !validBoardID(wire.ChannelID) || wire.Body == "" || wire.Recipients == nil ||
		len(wire.Recipients) > 32 || len(wire.Mentions) > 32 || !withinRawPayloadLimit(wire) ||
		len(wire.IdempotencyKey) > 128 || !validHeaderValue(wire.IdempotencyKey) {
		return "", ErrInvalidRequest
	}
	if strings.TrimSpace(wire.IdempotencyKey) == "" {
		return "", ErrInvalidRequest
	}
	if wire.ThreadID == "" {
		if strings.TrimSpace(wire.Title) == "" {
			return "", ErrInvalidRequest
		}
		return "/api/v1/threads", nil
	}
	if !validBoardID(wire.ThreadID) || wire.Title != "" {
		return "", ErrInvalidRequest
	}
	return "/api/v1/threads/" + wire.ThreadID + "/messages", nil
}

func withinRawPayloadLimit(wire sendWire) bool {
	remaining := maxRequestBodyBytes
	consume := func(value string) bool {
		if len(value) > remaining {
			return false
		}
		remaining -= len(value)
		return true
	}
	for _, value := range []string{
		wire.ChannelID, wire.ThreadID, wire.Title, wire.Body, wire.ReplyTo, wire.Kind,
		wire.IdempotencyKey, wire.ProcessingClaimID, wire.ProcessingClaimReceipt,
	} {
		if !consume(value) {
			return false
		}
	}
	for _, value := range wire.Recipients {
		if !consume(value) {
			return false
		}
	}
	for _, value := range wire.Mentions {
		if !consume(value) {
			return false
		}
	}
	return true
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}

func sendResponseError(status int, body []byte, header http.Header) error {
	errorCode, valid := decodeConnectionIdentityError(body, header)
	if !valid {
		return ErrUnknownOutcome
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return ErrAuthentication
	}
	if status == http.StatusConflict && errorCode == "connection_identity_mismatch" {
		return ErrConnectionIdentityMismatch
	}
	if status == http.StatusConflict && errorCode == "idempotency_conflict" {
		return ErrConflict
	}
	if status == http.StatusBadRequest && errorCode == "processing_claim_required_or_expired" {
		return ErrClaimFence
	}
	return ErrUnknownOutcome
}
