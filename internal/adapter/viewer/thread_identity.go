package viewer

import (
	"fmt"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// validateViewerThreadTuple validates the identity tuple at the Viewer
// boundary. L1 events may intentionally carry the exact empty tuple when they
// are not associated with a thread; every non-empty tuple must be canonical.
func validateViewerThreadTuple(threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, threadKind modulecore.ThreadKind, allowEmpty bool) error {
	if allowEmpty && threadID == "" && threadSeq == 0 && threadKind == "" {
		return nil
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
	return nil
}
