package l1sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

// ReadPreparedRecallUserMemories returns a bounded owner-scoped L1 snapshot
// for the startup prepared-recall projection. The caller supplies the owner
// proven by its authenticated boundary; this query never infers one from
// configuration. Source versions remain owned by the runtime projection.
func (s *L1SQLiteStore) ReadPreparedRecallUserMemories(ctx context.Context, authenticatedOwnerID string) ([]domainmemory.UserMemory, string, error) {
	if s == nil || s.readDB == nil {
		return nil, "", fmt.Errorf("%w: L1 read handle is unavailable", domainmemory.ErrUserMemoryOwnerUnavailable)
	}
	if authenticatedOwnerID == "" || strings.TrimSpace(authenticatedOwnerID) != authenticatedOwnerID {
		return nil, "", domainmemory.ErrUserMemoryOwnerInvalid
	}
	namespace, err := BuildL1Namespace(NamespaceKindUser, authenticatedOwnerID)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", domainmemory.ErrUserMemoryOwnerInvalid, err)
	}

	tx, err := s.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, "", fmt.Errorf("%w: failed to begin prepared recall snapshot: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	defer tx.Rollback()

	limit := domainconversation.PreparedRecallMaxRecordsPerSource + 1
	rows, err := tx.QueryContext(ctx, `
SELECT id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json,
       memory_state, layer, source, created_at, updated_at
FROM l1_memory_event
WHERE namespace = ? AND speaker = ? AND layer = ? AND memory_state IN (?, ?)
ORDER BY created_at DESC, id ASC
LIMIT ?`,
		namespace, string(domainconversation.SpeakerMemory), MemoryLayerL1,
		domainmemory.MemoryStateConfirmed, domainmemory.MemoryStatePinned, limit)
	if err != nil {
		return nil, "", fmt.Errorf("%w: failed to read prepared recall snapshot: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	events, scanErr := scanL1Events(rows)
	closeErr := rows.Close()
	if scanErr != nil {
		return nil, "", fmt.Errorf("%w: invalid prepared recall source row: %v", domainmemory.ErrUserMemoryOwnerUnavailable, scanErr)
	}
	if closeErr != nil {
		return nil, "", fmt.Errorf("%w: failed to close prepared recall snapshot rows: %v", domainmemory.ErrUserMemoryOwnerUnavailable, closeErr)
	}

	coverage := domainconversation.PreparedRecallCoverageFull
	if len(events) > domainconversation.PreparedRecallMaxRecordsPerSource {
		coverage = domainconversation.PreparedRecallCoveragePartial
		events = events[:domainconversation.PreparedRecallMaxRecordsPerSource]
	}

	memories := make([]domainmemory.UserMemory, 0, len(events))
	for _, event := range events {
		memory, err := strictUserMemoryFromEvent(event)
		if err != nil {
			return nil, "", fmt.Errorf("%w: invalid prepared recall user memory: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
		}
		if memory.UserID != authenticatedOwnerID || memory.Namespace != namespace || event.Namespace != namespace {
			return nil, "", fmt.Errorf("%w: prepared recall row owner does not match authenticated owner", domainmemory.ErrUserMemoryOwnerUnavailable)
		}
		if err := validatePreparedRecallUserMemoryMetadata(event, *memory); err != nil {
			return nil, "", fmt.Errorf("%w: invalid prepared recall user memory metadata: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
		}

		decisions := domainmemory.RankUserMemoriesForRecall("", []domainmemory.UserMemory{*memory}, 1)
		if len(decisions) != 1 {
			return nil, "", fmt.Errorf("%w: prepared recall eligibility returned an invalid result", domainmemory.ErrUserMemoryOwnerUnavailable)
		}
		switch decisions[0].Status {
		case domainmemory.UserMemoryRecallStatusInjected:
			clone := *memory
			clone.EvidenceEventIDs = append([]string(nil), memory.EvidenceEventIDs...)
			memories = append(memories, clone)
		case domainmemory.UserMemoryRecallStatusFilteredStatus, domainmemory.UserMemoryRecallStatusFilteredSensitivity:
			// Valid inactive, superseded, decayed, or sensitive records are
			// outside the initial prompt projection.
			continue
		case domainmemory.UserMemoryRecallStatusFilteredScope:
			return nil, "", fmt.Errorf("%w: user memory scope is not recognized", domainmemory.ErrUserMemoryOwnerUnavailable)
		default:
			return nil, "", fmt.Errorf("%w: unexpected prepared recall eligibility status %q", domainmemory.ErrUserMemoryOwnerUnavailable, decisions[0].Status)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, "", fmt.Errorf("%w: failed to finish prepared recall snapshot: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	return memories, coverage, nil
}

func validatePreparedRecallUserMemoryMetadata(event L1MemoryEvent, memory domainmemory.UserMemory) error {
	if event.Meta == nil {
		return fmt.Errorf("metadata is missing")
	}
	value, ok := event.Meta["active"]
	if !ok {
		return fmt.Errorf("active is missing")
	}
	if _, ok := value.(bool); !ok {
		return fmt.Errorf("active must be a boolean")
	}
	if value, ok := event.Meta["scope"]; !ok {
		return fmt.Errorf("scope is missing")
	} else if scope, ok := value.(string); !ok || strings.TrimSpace(scope) == "" {
		return fmt.Errorf("scope must be a non-empty string")
	}
	sensitivityValue, ok := event.Meta["sensitivity"]
	if !ok {
		return fmt.Errorf("sensitivity is missing")
	}
	sensitivity, ok := sensitivityValue.(string)
	if !ok {
		return fmt.Errorf("sensitivity must be a string")
	}
	switch strings.ToLower(strings.TrimSpace(sensitivity)) {
	case "normal", "sensitive":
	default:
		return fmt.Errorf("sensitivity is not recognized")
	}
	if raw, ok := event.Meta["confidence"]; ok {
		confidence, ok := raw.(float64)
		if !ok || math.IsNaN(confidence) || math.IsInf(confidence, 0) || confidence <= 0 || confidence > 1 {
			return fmt.Errorf("confidence is invalid")
		}
	}
	if len(memory.EvidenceEventIDs) == 0 || len(memory.EvidenceEventIDs) > domainconversation.PreparedRecallMaxIndexKeysPerRecord {
		return fmt.Errorf("evidence event IDs are missing or exceed the record bound")
	}
	for i, evidenceID := range memory.EvidenceEventIDs {
		if strings.TrimSpace(evidenceID) == "" {
			return fmt.Errorf("evidence event ID %d is empty", i)
		}
	}
	if strings.TrimSpace(event.Source) == "" {
		return fmt.Errorf("source provenance is missing")
	}
	return nil
}
