package eventmigration

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func apply(ctx context.Context, path string, events []modulecore.EventEnvelope) (string, error) {
	store, err := eventstore.NewSQLiteStore(path)
	if err != nil {
		return "", newCodedError("target_store", "open canonical event store: %w", err)
	}
	action, applyErr := reconcileTarget(ctx, store, events)
	closeErr := store.Close()
	if applyErr != nil {
		return "", applyErr
	}
	if closeErr != nil {
		return "", newCodedError("target_store", "close canonical event store: %w", closeErr)
	}
	return action, nil
}

func reconcileTarget(ctx context.Context, store *eventstore.SQLiteStore, events []modulecore.EventEnvelope) (string, error) {
	present := 0
	var mismatch error
	for _, expected := range events {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		actual, found, err := store.GetByID(ctx, expected.EventID)
		if err != nil {
			return "", newCodedError("target_store", "read canonical event store: %w", err)
		}
		if !found {
			continue
		}
		present++
		// EventSeq is assigned by the canonical store. Legacy migration plans
		// intentionally carry zero, so compare every planned field after binding
		// that one storage-owned value to the persisted envelope.
		if expected.EventSeq == 0 {
			expected.EventSeq = actual.EventSeq
		}
		expectedJSON, err := json.Marshal(expected)
		if err != nil {
			return "", newCodedError("target_mismatch", "encode expected canonical event")
		}
		actualJSON, err := json.Marshal(actual)
		if err != nil {
			return "", newCodedError("target_mismatch", "encode stored canonical event")
		}
		if !bytes.Equal(expectedJSON, actualJSON) && mismatch == nil {
			mismatch = newCodedError("target_mismatch", "canonical event already exists with different JSON")
		}
	}
	if mismatch != nil {
		return "", mismatch
	}
	if present == 0 {
		if err := store.AppendBatch(ctx, events); err != nil {
			return "", newCodedError("target_store", "append canonical event batch: %w", err)
		}
		return StatusApplied, nil
	}
	if present == len(events) {
		return StatusNoop, nil
	}
	return "", newCodedError("target_partial", "canonical event store contains a partial migration set")
}
