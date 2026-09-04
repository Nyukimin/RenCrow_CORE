package l1sqlite

import (
	"fmt"
	"strings"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	_ "modernc.org/sqlite"
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
	if err := validateL1SessionThreadTuple(item.SessionID, item.ThreadID, item.ThreadSeq, item.ThreadKind); err != nil {
		return fmt.Errorf("invalid l1 memory event thread identity: %w", err)
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

func validateL1ThreadTuple(threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, threadKind modulecore.ThreadKind) error {
	if threadID == "" {
		if threadSeq != 0 || threadKind != "" {
			return fmt.Errorf("empty thread identity requires thread_seq=0 and thread_kind empty")
		}
		return nil
	}
	if err := threadID.Validate(); err != nil {
		return err
	}
	if err := threadSeq.Validate(); err != nil {
		return err
	}
	if err := threadKind.Validate(); err != nil {
		return err
	}
	return nil
}

// validateL1SessionThreadTuple enforces the parent/child identity contract at
// every L1 boundary.  An entirely unbound event is valid for operational
// namespaces, but a bound Thread tuple must carry a canonical SessionID.
func validateL1SessionThreadTuple(sessionID string, threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, threadKind modulecore.ThreadKind) error {
	if err := validateL1ThreadTuple(threadID, threadSeq, threadKind); err != nil {
		return err
	}
	if sessionID == "" {
		if !isEmptyL1ThreadTuple(threadID, threadSeq, threadKind) {
			return fmt.Errorf("session_id is required when thread identity is bound")
		}
		return nil
	}
	if err := modulecore.SessionID(sessionID).Validate(); err != nil {
		return fmt.Errorf("invalid l1 session identity: %w", err)
	}
	return nil
}

func validateL1BoundThreadTuple(threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, threadKind modulecore.ThreadKind) error {
	if threadID == "" {
		return fmt.Errorf("thread_id is required")
	}
	if err := validateL1ThreadTuple(threadID, threadSeq, threadKind); err != nil {
		return err
	}
	return nil
}

func isEmptyL1ThreadTuple(threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, threadKind modulecore.ThreadKind) bool {
	return string(threadID) == "" && threadSeq == 0 && threadKind == ""
}

func validateL1MessageSaveInput(sessionID string, threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, threadKind modulecore.ThreadKind, msg domconv.Message) error {
	if err := validateL1SessionThreadTuple(sessionID, threadID, threadSeq, threadKind); err != nil {
		return fmt.Errorf("l1 memory event thread identity is invalid: %w", err)
	}
	if isEmptyL1ThreadTuple(threadID, threadSeq, threadKind) {
		return fmt.Errorf("l1 memory event thread identity is invalid: thread_id is required")
	}
	if strings.TrimSpace(string(msg.Speaker)) == "" {
		return fmt.Errorf("l1 memory event speaker is required")
	}
	if strings.TrimSpace(msg.Msg) == "" {
		return fmt.Errorf("l1 memory event message is required")
	}
	return nil
}
