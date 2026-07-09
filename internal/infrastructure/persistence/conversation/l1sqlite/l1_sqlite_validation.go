package l1sqlite

import (
	"fmt"
	"strings"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	_ "github.com/mattn/go-sqlite3"
)

func validateMemoryState(memoryState string) error {
	switch memoryState {
	case MemoryStateObserved, MemoryStateCandidate, MemoryStateConfirmed, MemoryStatePinned:
		return nil
	default:
		return fmt.Errorf("invalid l1 memory state: %s", memoryState)
	}
}

func validateL1StagingKind(kind string) error {
	switch kind {
	case L1StagingKindExternalFetch, L1StagingKindMemoryCandidate, L1StagingKindSearchResult:
		return nil
	default:
		return fmt.Errorf("invalid l1 staging kind: %s", kind)
	}
}

func validateL1StagingStatus(status string) error {
	switch status {
	case L1StagingStatusPending, L1StagingStatusValidated, L1StagingStatusRejected:
		return nil
	default:
		return fmt.Errorf("invalid l1 staging validation status: %s", status)
	}
}

func validateL1SourceKind(kind string) error {
	switch kind {
	case L1SourceKindRSS, L1SourceKindAtom, L1SourceKindOfficialAPI, L1SourceKindGitHub,
		L1SourceKindHuggingFace, L1SourceKindPyPI, L1SourceKindMediaWiki, L1SourceKindSearchFallback,
		L1SourceKindWebGather:
		return nil
	default:
		return fmt.Errorf("invalid l1 source registry kind: %s", kind)
	}
}

func validateL1SourceFetchStatus(status string) error {
	switch status {
	case L1SourceFetchStatusOK, L1SourceFetchStatusError:
		return nil
	default:
		return fmt.Errorf("invalid l1 source registry fetch status: %s", status)
	}
}

func validateL1MemoryEvent(item L1MemoryEvent) error {
	if strings.TrimSpace(item.ID) == "" {
		return fmt.Errorf("l1 memory event id is required")
	}
	if err := ValidateL1Namespace(item.Namespace); err != nil {
		return err
	}
	if strings.TrimSpace(item.Message) == "" {
		return fmt.Errorf("l1 memory event message is required")
	}
	if err := validateMemoryState(item.MemoryState); err != nil {
		return err
	}
	if strings.TrimSpace(item.Layer) == "" {
		return fmt.Errorf("l1 memory event layer is required")
	}
	if strings.TrimSpace(item.Source) == "" {
		return fmt.Errorf("l1 memory event source is required")
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("l1 memory event created_at is required")
	}
	if item.UpdatedAt.IsZero() {
		return fmt.Errorf("l1 memory event updated_at is required")
	}
	return nil
}

func validateL1MessageSaveInput(sessionID string, threadID int64, msg domconv.Message) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("l1 memory event session_id is required")
	}
	if threadID <= 0 {
		return fmt.Errorf("l1 memory event thread_id must be > 0")
	}
	if strings.TrimSpace(string(msg.Speaker)) == "" {
		return fmt.Errorf("l1 memory event speaker is required")
	}
	if strings.TrimSpace(msg.Msg) == "" {
		return fmt.Errorf("l1 memory event message is required")
	}
	return nil
}
