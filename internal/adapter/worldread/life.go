package worldread

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var (
	ErrCommandRejected = errors.New("world life command was rejected")
	ErrCommandRetry    = errors.New("world life command requires retry")
)

const (
	HomeActionMove     = "life.home.move"
	HomeActionSleep    = "life.home.sleep"
	HomeActionWake     = "life.home.wake"
	HomeActionExercise = "life.home.exercise"
	HomeActionMirror   = "life.home.mirror_pose"
	HomeActionFinish   = "life.home.finish"
	HomeActionStop     = "life.home.stop"
	maxIdempotencyKey  = 128
)

// LifeConfig scopes the action client to one timeline and an exact actor
// allowlist. Its credential is a separate life-scoped bearer, never a reader
// token supplied by the same Client instance.
type LifeConfig struct {
	BaseURL       string
	Credential    string
	WorldID       string
	TimelineID    string
	TimelineEpoch string
	ActorIDs      []string
	Timeout       time.Duration
}

// HomeActionPayload is an exact typed payload for one WORLD life.home action.
// The method validates that only fields legal for that action are populated.
type HomeActionPayload struct {
	ActorID    string `json:"actor_id"`
	RoomID     string `json:"room_id,omitempty"`
	SlotID     string `json:"slot_id,omitempty"`
	ActivityID string `json:"activity_id,omitempty"`
	PoseID     string `json:"pose_id,omitempty"`
}

type CommandReceipt struct {
	ReceiptID     string
	Status        string
	Reason        string
	TransactionID string
	EventIDs      []string
	FirstSeq      uint64
	LastSeq       uint64
	StateSeq      uint64
	RecordedAt    time.Time
}

type CommandResult struct {
	SchemaVersion int
	Receipt       CommandReceipt
	Duplicate     bool
}

// LifeClient sends owner-scoped home lifecycle commands to the existing R07
// commands route. It never follows redirects and never exposes its bearer.
type LifeClient struct {
	client     *Client
	credential string
}

func NewLife(cfg LifeConfig) (*LifeClient, error) {
	client, err := New(Config{
		BaseURL: cfg.BaseURL, Credential: cfg.Credential, WorldID: cfg.WorldID,
		TimelineID: cfg.TimelineID, TimelineEpoch: cfg.TimelineEpoch,
		ActorIDs: cfg.ActorIDs, Timeout: cfg.Timeout,
	})
	if err != nil {
		return nil, err
	}
	return &LifeClient{client: client, credential: cfg.Credential}, nil
}

// Command posts one exact home action with a caller-owned stable idempotency
// key. A 409 handler rejection returns its validated receipt together with
// ErrCommandRejected; other 409 bodies without a receipt are contract errors.
func (c *LifeClient) Command(ctx context.Context, key, action string, payload HomeActionPayload) (CommandResult, error) {
	if c == nil || c.client == nil || c.client.httpClient == nil || ctx == nil {
		return CommandResult{}, &ReadError{Op: "command", Err: ErrInvalidConfig}
	}
	if !validIdempotencyKey(key) {
		return CommandResult{}, &ReadError{Op: "command", Err: fmt.Errorf("%w: idempotency key is invalid", ErrInvalidConfig)}
	}
	if err := c.validatePayload(action, payload); err != nil {
		return CommandResult{}, &ReadError{Op: "command", Err: err}
	}
	requestBody, err := json.Marshal(struct {
		Action        string            `json:"action"`
		TimelineEpoch string            `json:"timeline_epoch"`
		Payload       HomeActionPayload `json:"payload"`
	}{Action: action, TimelineEpoch: c.client.epoch, Payload: payload})
	if err != nil {
		return CommandResult{}, &ReadError{Op: "command", Err: fmt.Errorf("%w: encode request", ErrContract)}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.client.urlFor("commands", nil).String(), bytes.NewReader(requestBody))
	if err != nil {
		return CommandResult{}, &ReadError{Op: "command", Err: fmt.Errorf("%w: build request", ErrContract)}
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.credential)
	request.Header.Set("Idempotency-Key", key)
	response, err := c.client.httpClient.Do(request)
	if err != nil {
		return CommandResult{}, &ReadError{Op: "command", Retryable: true, Err: fmt.Errorf("%w: %v", ErrUnavailable, err)}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusConflict && response.StatusCode != http.StatusUnprocessableEntity {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return CommandResult{}, classifyStatus("command", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return CommandResult{}, &ReadError{Op: "command", Status: response.StatusCode, Retryable: true, Err: fmt.Errorf("%w: read response", ErrUnavailable)}
	}
	if len(body) > maxResponseBytes {
		return CommandResult{}, &ReadError{Op: "command", Status: response.StatusCode, Err: fmt.Errorf("%w: response is too large", ErrContract)}
	}
	var wire commandResultDTO
	if err := json.Unmarshal(body, &wire); err != nil {
		return CommandResult{}, &ReadError{Op: "command", Status: response.StatusCode, Err: fmt.Errorf("%w: decode response", ErrContract)}
	}
	result := CommandResult{
		SchemaVersion: wire.SchemaVersion,
		Receipt: CommandReceipt{
			ReceiptID: wire.Receipt.ReceiptID, Status: wire.Receipt.Status, Reason: wire.Receipt.Reason,
			TransactionID: wire.Receipt.TransactionID, EventIDs: append([]string(nil), wire.Receipt.EventIDs...),
			FirstSeq: wire.Receipt.FirstSeq, LastSeq: wire.Receipt.LastSeq, StateSeq: wire.Receipt.StateSeq,
			RecordedAt: wire.Receipt.RecordedAt,
		},
		Duplicate: wire.Duplicate,
	}
	if err := validateCommandResult(response.StatusCode, wire); err != nil {
		return CommandResult{}, &ReadError{Op: "command", Status: response.StatusCode, Err: err}
	}
	switch result.Receipt.Status {
	case "rejected":
		return result, &ReadError{Op: "command", Status: response.StatusCode, Err: ErrCommandRejected}
	case "retry":
		return result, &ReadError{Op: "command", Status: response.StatusCode, Retryable: true, Err: ErrCommandRetry}
	default:
		return result, nil
	}
}

func (c *LifeClient) validatePayload(action string, payload HomeActionPayload) error {
	if _, allowed := c.client.actors[payload.ActorID]; !allowed {
		return fmt.Errorf("%w: actor is outside the configured life scope", ErrInvalidConfig)
	}
	fields := []string{payload.RoomID, payload.SlotID, payload.ActivityID, payload.PoseID}
	validOptional := func(values ...string) bool {
		for _, value := range values {
			if value != "" && !validID(value) {
				return false
			}
		}
		return true
	}
	if !validID(payload.ActorID) || !validOptional(fields...) {
		return fmt.Errorf("%w: home action payload contains an invalid identifier", ErrInvalidConfig)
	}
	valid := false
	switch action {
	case HomeActionMove:
		valid = payload.RoomID != "" && payload.ActivityID == "" && payload.PoseID == ""
	case HomeActionSleep, HomeActionExercise:
		valid = payload.RoomID == "" && payload.SlotID == "" && payload.ActivityID == "" && payload.PoseID == ""
	case HomeActionMirror:
		valid = payload.RoomID == "" && payload.SlotID == "" && payload.ActivityID == "" && payload.PoseID != ""
	case HomeActionWake, HomeActionFinish, HomeActionStop:
		valid = payload.RoomID == "" && payload.SlotID == "" && payload.ActivityID != "" && payload.PoseID == ""
	default:
		return fmt.Errorf("%w: action is not an allowed life.home command", ErrInvalidConfig)
	}
	if !valid {
		return fmt.Errorf("%w: payload fields do not match action %q", ErrInvalidConfig, action)
	}
	return nil
}

type commandResultDTO struct {
	SchemaVersion int               `json:"schema_version"`
	Receipt       commandReceiptDTO `json:"receipt"`
	Duplicate     bool              `json:"duplicate,omitempty"`
}

type commandReceiptDTO struct {
	ReceiptID     string    `json:"receipt_id"`
	Status        string    `json:"status"`
	Reason        string    `json:"reason,omitempty"`
	TransactionID string    `json:"transaction_id,omitempty"`
	EventIDs      []string  `json:"event_ids"`
	FirstSeq      uint64    `json:"first_seq"`
	LastSeq       uint64    `json:"last_seq"`
	StateSeq      uint64    `json:"state_seq"`
	RecordedAt    time.Time `json:"recorded_at"`
}

func validateCommandResult(status int, dto commandResultDTO) error {
	if dto.SchemaVersion != schemaVersion || !validID(dto.Receipt.ReceiptID) || dto.Receipt.RecordedAt.IsZero() ||
		dto.Receipt.StateSeq > maxJSONSequence || dto.Receipt.FirstSeq > maxJSONSequence || dto.Receipt.LastSeq > maxJSONSequence {
		return fmt.Errorf("%w: command receipt identity or schema is invalid", ErrContract)
	}
	switch dto.Receipt.Status {
	case "committed":
		if status != http.StatusOK || !validID(dto.Receipt.TransactionID) || len(dto.Receipt.EventIDs) == 0 ||
			dto.Receipt.FirstSeq == 0 || dto.Receipt.LastSeq < dto.Receipt.FirstSeq || dto.Receipt.StateSeq < dto.Receipt.LastSeq {
			return fmt.Errorf("%w: committed command receipt is incomplete", ErrContract)
		}
		seen := make(map[string]struct{}, len(dto.Receipt.EventIDs))
		for _, eventID := range dto.Receipt.EventIDs {
			if !validID(eventID) {
				return fmt.Errorf("%w: command receipt contains an invalid EventID", ErrContract)
			}
			if _, duplicate := seen[eventID]; duplicate {
				return fmt.Errorf("%w: command receipt contains duplicate EventIDs", ErrContract)
			}
			seen[eventID] = struct{}{}
		}
	case "retry":
		if status != http.StatusOK || (dto.Receipt.TransactionID != "" && !validID(dto.Receipt.TransactionID)) {
			return fmt.Errorf("%w: retry command receipt has an invalid status or transaction", ErrContract)
		}
	case "rejected":
		if (status != http.StatusConflict && status != http.StatusUnprocessableEntity) || dto.Receipt.TransactionID != "" || len(dto.Receipt.EventIDs) != 0 ||
			dto.Receipt.FirstSeq != 0 || dto.Receipt.LastSeq != 0 || dto.Receipt.Reason == "" {
			return fmt.Errorf("%w: rejected command receipt is incomplete", ErrContract)
		}
	default:
		return fmt.Errorf("%w: command receipt status is unknown", ErrContract)
	}
	if dto.Receipt.Reason != "" && !validID(dto.Receipt.Reason) {
		return fmt.Errorf("%w: command receipt reason is invalid", ErrContract)
	}
	return nil
}

func validIdempotencyKey(value string) bool {
	return len(value) > 0 && len(value) <= maxIdempotencyKey && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}
