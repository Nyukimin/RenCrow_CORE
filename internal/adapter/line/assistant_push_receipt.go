package line

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ErrAssistantPushUncertain means a push may have reached LINE and must not be
// automatically repeated with the same delivery ID.
var ErrAssistantPushUncertain = errors.New("LINE push delivery state is uncertain")

type assistantPushReceipt struct {
	DeliveryID string    `json:"delivery_id"`
	TraceID    string    `json:"trace_id"`
	Status     string    `json:"status"`
	RecordedAt time.Time `json:"recorded_at"`
}

// AssistantPushReceiptStore persists transport receipts used to suppress a
// duplicate LINE push after process restarts. ASSISTANT remains the owner of
// the user-facing Delivery state.
type AssistantPushReceiptStore struct {
	path string
	mu   sync.Mutex
}

// NewAssistantPushReceiptStore creates a private append-only receipt store.
func NewAssistantPushReceiptStore(path string) *AssistantPushReceiptStore {
	return &AssistantPushReceiptStore{path: strings.TrimSpace(path)}
}

// SendOnce serializes a single delivery ID across the send and its receipt.
// An interrupted or ambiguous send is kept uncertain and is never retried
// automatically with the same delivery ID.
func (s *AssistantPushReceiptStore) SendOnce(
	ctx context.Context,
	deliveryID string,
	traceID string,
	now time.Time,
	send func(context.Context) error,
) (bool, error) {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return false, fmt.Errorf("assistant LINE push receipt path is empty")
	}
	if send == nil {
		return false, fmt.Errorf("assistant LINE push sender is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	latest, found, err := s.latest(deliveryID)
	if err != nil {
		return false, err
	}
	if found {
		if latest.TraceID != traceID {
			return false, fmt.Errorf("delivery_id is already associated with another trace_id")
		}
		switch latest.Status {
		case "sent":
			return true, nil
		case "sending", "uncertain":
			return false, ErrAssistantPushUncertain
		default:
			return false, fmt.Errorf("unsupported assistant LINE push receipt status %q", latest.Status)
		}
	}

	if err := s.append(assistantPushReceipt{
		DeliveryID: deliveryID,
		TraceID:    traceID,
		Status:     "sending",
		RecordedAt: now.UTC(),
	}); err != nil {
		return false, err
	}
	if err := send(ctx); err != nil {
		if receiptErr := s.append(assistantPushReceipt{
			DeliveryID: deliveryID,
			TraceID:    traceID,
			Status:     "uncertain",
			RecordedAt: now.UTC(),
		}); receiptErr != nil {
			return false, fmt.Errorf("LINE push failed: %v; persist uncertain receipt: %w", err, receiptErr)
		}
		return false, fmt.Errorf("LINE push result is uncertain: %w", err)
	}
	if err := s.append(assistantPushReceipt{
		DeliveryID: deliveryID,
		TraceID:    traceID,
		Status:     "sent",
		RecordedAt: now.UTC(),
	}); err != nil {
		return false, fmt.Errorf("%w: persist sent receipt: %v", ErrAssistantPushUncertain, err)
	}
	return false, nil
}

func (s *AssistantPushReceiptStore) latest(deliveryID string) (assistantPushReceipt, bool, error) {
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return assistantPushReceipt{}, false, nil
	}
	if err != nil {
		return assistantPushReceipt{}, false, fmt.Errorf("open assistant LINE push receipts: %w", err)
	}
	defer file.Close()

	var latest assistantPushReceipt
	found := false
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	for scanner.Scan() {
		var item assistantPushReceipt
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return assistantPushReceipt{}, false, fmt.Errorf("decode assistant LINE push receipt: %w", err)
		}
		if item.DeliveryID == deliveryID {
			latest = item
			found = true
		}
	}
	if err := scanner.Err(); err != nil {
		return assistantPushReceipt{}, false, fmt.Errorf("read assistant LINE push receipts: %w", err)
	}
	return latest, found, nil
}

func (s *AssistantPushReceiptStore) append(receipt assistantPushReceipt) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create assistant LINE push receipt directory: %w", err)
	}
	file, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("open assistant LINE push receipt file: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat assistant LINE push receipt file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("assistant LINE push receipt file must be regular with 0600 permissions")
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode assistant LINE push receipt: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("append assistant LINE push receipt: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync assistant LINE push receipt: %w", err)
	}
	return nil
}
