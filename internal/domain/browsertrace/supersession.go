package browsertrace

import (
	"fmt"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// ValidateAPIArtifactScope reports whether two artifacts sit in one supersession
// scope: same task, run, actor and workstream, and the same content role together
// with its stored kind. The two values are not required to be one artifact — a
// predecessor and its successor are distinct ArtifactIDs that have to agree on these
// fields — and an in-place update of a single row agrees with itself by definition.
//
// Both axes are compared on purpose. Kind follows the content role, so two
// different roles can share one kind (coverage_report and risk_assessment are both
// reports) and a kind-only comparison would silently join two artifacts, while a
// stored kind that contradicts its own role marks a row that cannot be trusted as
// a supersession partner.
//
// It is a pure comparison of two values the caller already holds and says nothing
// about the artifact IDs themselves, because an ordinary in-place update of one row
// keeps its ID and must not be turned into a self-supersession rejection. Whether two
// distinct artifacts may be joined by a supersession edge is the decision of
// ValidateAPIArtifactSupersessionPair below.
func ValidateAPIArtifactScope(previous, next APIArtifact) error {
	if previous.TaskID != next.TaskID {
		return fmt.Errorf("artifact %s task_id %s does not match artifact %s task_id %s", previous.ArtifactID, previous.TaskID, next.ArtifactID, next.TaskID)
	}
	if previous.RunID != next.RunID {
		return fmt.Errorf("artifact %s run_id %s does not match artifact %s run_id %s", previous.ArtifactID, previous.RunID, next.ArtifactID, next.RunID)
	}
	if previous.ActorID != next.ActorID {
		return fmt.Errorf("artifact %s actor_id %q does not match artifact %s actor_id %q", previous.ArtifactID, previous.ActorID, next.ArtifactID, next.ActorID)
	}
	if previous.WorkstreamID != next.WorkstreamID {
		return fmt.Errorf("artifact %s workstream_id %q does not match artifact %s workstream_id %q", previous.ArtifactID, previous.WorkstreamID, next.ArtifactID, next.WorkstreamID)
	}
	if previous.Type != next.Type {
		return fmt.Errorf("artifact %s content role %q does not match artifact %s content role %q", previous.ArtifactID, previous.Type, next.ArtifactID, next.Type)
	}
	if previous.Kind != next.Kind {
		return fmt.Errorf("artifact %s kind %q does not match artifact %s kind %q", previous.ArtifactID, previous.Kind, next.ArtifactID, next.Kind)
	}
	return nil
}

// ValidateAPIArtifactSupersessionPair reports whether an edge claiming that previous
// was replaced by next is a legal edge: the two artifact IDs must be canonical and
// distinct, and both rows must still describe one artifact.
//
// It does not claim that either row exists, that a longer chain is intact, or that
// the edge is written atomically: the browsertrace store operation that owns supersede
// covers those. ID form and the self-reference rule are not restated here, they are
// delegated to the shared modules/core validator that owns them.
func ValidateAPIArtifactSupersessionPair(previous, next APIArtifact) error {
	if err := modulecore.ValidateArtifactSupersession(previous.ArtifactID, next.ArtifactID); err != nil {
		return fmt.Errorf("supersession pair %s -> %s: %w", previous.ArtifactID, next.ArtifactID, err)
	}
	return ValidateAPIArtifactScope(previous, next)
}
