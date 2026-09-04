package archivesqlite

import (
	"fmt"
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// validateArchiveThreadTuple enforces the one Thread identity representation
// accepted by ArchiveSQLite. Summary rows require a canonical tuple; L1
// archive rows additionally permit the exact empty tuple for events that are
// not attached to a conversation thread.
func validateArchiveThreadTuple(threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, threadKind modulecore.ThreadKind, allowEmpty bool) error {
	if threadID == "" {
		if allowEmpty && threadSeq == 0 && threadKind == "" {
			return nil
		}
		return fmt.Errorf("thread identity tuple is incomplete")
	}
	if err := threadID.Validate(); err != nil {
		return fmt.Errorf("invalid thread_id: %w", err)
	}
	if err := threadSeq.Validate(); err != nil {
		return fmt.Errorf("invalid thread_seq: %w", err)
	}
	if err := threadKind.Validate(); err != nil {
		return fmt.Errorf("invalid thread_kind: %w", err)
	}
	if strings.TrimSpace(string(threadID)) != string(threadID) {
		return fmt.Errorf("thread_id must not contain surrounding whitespace")
	}
	return nil
}
