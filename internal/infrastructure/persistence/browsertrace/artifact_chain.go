package browsertrace

import (
	"context"
	"fmt"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// apiArtifactRowLoader reads one APIArtifact row by id from whichever store the
// caller already holds: the SQLite connection of a reserved transaction, or the
// latest-state map that the JSONL store loaded under the batch write lock. It
// exists so the supersession chain rules below are declared once for both stores
// instead of being copied per backend, and so each store keeps its own read path,
// locking and fail-closed row checks. A loader returns an error for a missing,
// undecodable or invalid row rather than reporting a miss.
type apiArtifactRowLoader func(ctx context.Context, id modulecore.ArtifactID) (domaintrace.APIArtifact, error)

// verifyAPIArtifactSuccessorChain walks the supersession chain that starts at the
// proposed successor and keeps it a chain: every row it points at has to exist,
// decode, match its own digest, stay in the same scope as the row above it, and
// never lead back to the artifact being superseded. The visited set both detects a
// chain that already loops and bounds the walk, so a corrupt row cannot spin here
// forever. What "read a row" means, including the lock or transaction that makes
// the read stable, belongs to the loader and not to this function.
func verifyAPIArtifactSuccessorChain(ctx context.Context, load apiArtifactRowLoader, rootID modulecore.ArtifactID, from domaintrace.APIArtifact) error {
	if load == nil {
		return fmt.Errorf("supersession chain of artifact %s: no artifact row loader was given", rootID)
	}
	visited := map[modulecore.ArtifactID]bool{rootID: true, from.ArtifactID: true}
	node := from
	for node.SupersededBy != "" {
		if err := contextErr(ctx); err != nil {
			return err
		}
		next, err := load(ctx, node.SupersededBy)
		if err != nil {
			return fmt.Errorf("supersession chain of artifact %s: %w", node.ArtifactID, err)
		}
		if next.ArtifactID == rootID {
			return fmt.Errorf("artifact %s cannot supersede %s: artifact %s is already superseded by it, so the edge would close a cycle", rootID, from.ArtifactID, node.ArtifactID)
		}
		if visited[next.ArtifactID] {
			return fmt.Errorf("supersession chain of artifact %s revisits artifact %s", rootID, next.ArtifactID)
		}
		if err := domaintrace.ValidateAPIArtifactSupersessionPair(node, next); err != nil {
			return err
		}
		visited[next.ArtifactID] = true
		node = next
	}
	return nil
}
