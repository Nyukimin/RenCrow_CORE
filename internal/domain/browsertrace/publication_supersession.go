package browsertrace

import (
	"fmt"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// APIArtifactSupersededEventType is the canonical event type of the fact that a
// browsertrace APIArtifact was superseded by an existing successor. The envelope a store
// keeps next to the edge is the envelope of exactly this fact, so the name is declared
// once here and not restated per store or per caller. It is a different fact from
// APIArtifactCreatedEventType: the creation fact says which bytes came into existence,
// this fact says which existing artifact was replaced by which other existing artifact.
const APIArtifactSupersededEventType = "artifact.superseded"

// Payload keys of a supersession fact. The successor id is carried because the fact is
// about the predecessor row and an envelope has exactly one ArtifactID field, which
// holds the predecessor; the two digests are carried so a replay can prove which bytes
// were replaced by which bytes, without the fact becoming a second copy of either body.
const (
	APIArtifactSupersessionPayloadSupersededBy           = "superseded_by"
	APIArtifactSupersessionPayloadPredecessorContentHash = "predecessor_content_hash"
	APIArtifactSupersessionPayloadSuccessorContentHash   = "successor_content_hash"
)

// apiArtifactSupersessionPayloadFieldCount is the number of fields a supersession fact
// payload declares. It is deliberately its own count rather than the creation intent's:
// the two facts describe different things, so a payload carrying the three creation keys
// is not a supersession fact and must not be accepted by this contract.
const apiArtifactSupersessionPayloadFieldCount = 3

// apiArtifactSupersessionFactFields decodes the three declared supersession fields once,
// into typed values, so every validator that compares them shares a single reading of the
// payload instead of restating the key names and the shape check.
func apiArtifactSupersessionFactFields(fact modulecore.EventEnvelope) (modulecore.ArtifactID, string, string, error) {
	if len(fact.Payload) != apiArtifactSupersessionPayloadFieldCount {
		return "", "", "", fmt.Errorf("supersession fact %s payload carries %d keys, want the %d declared supersession fields", fact.EventID, len(fact.Payload), apiArtifactSupersessionPayloadFieldCount)
	}
	for _, key := range []string{
		APIArtifactSupersessionPayloadSupersededBy,
		APIArtifactSupersessionPayloadPredecessorContentHash,
		APIArtifactSupersessionPayloadSuccessorContentHash,
	} {
		value, found := fact.Payload[key]
		if !found {
			return "", "", "", fmt.Errorf("supersession fact %s payload is missing %s", fact.EventID, key)
		}
		if _, ok := value.(string); !ok {
			return "", "", "", fmt.Errorf("supersession fact %s payload %s must be a string, got %T", fact.EventID, key, value)
		}
	}
	successorID := modulecore.ArtifactID(fact.Payload[APIArtifactSupersessionPayloadSupersededBy].(string))
	predecessorHash := fact.Payload[APIArtifactSupersessionPayloadPredecessorContentHash].(string)
	successorHash := fact.Payload[APIArtifactSupersessionPayloadSuccessorContentHash].(string)
	return successorID, predecessorHash, successorHash, nil
}

// ValidatePersistedAPIArtifactSupersessionFact reports whether a stored fact is a
// self-contained browsertrace supersession envelope: the canonical envelope contract, the
// sequence the canonical event store still has to assign, this owner's supersession event
// type and component, the predecessor artifact id it is about, the task, run and actor
// identity the artifact domain already owns, a canonical workstream, the agent actor
// provenance, and a payload naming a canonical successor distinct from that predecessor
// together with two canonical content digests.
//
// A reader that only holds the stored envelope uses this. It says nothing about the
// current rows, because a supersession fact is immutable history: a later legitimate body
// update replaces a digest the payload records, so re-checking the stored digests against
// a mutable row would report accepted history as corrupt. Binding the fact to rows is
// ValidateAPIArtifactSupersessionFactRow's job.
func ValidatePersistedAPIArtifactSupersessionFact(fact modulecore.EventEnvelope) error {
	if err := modulecore.ValidateEventEnvelope(fact); err != nil {
		return fmt.Errorf("supersession fact %s: %w", fact.EventID, err)
	}
	if fact.EventSeq != 0 {
		return fmt.Errorf("supersession fact %s has event_seq %d: the canonical event store assigns the sequence", fact.EventID, fact.EventSeq)
	}
	if fact.EventType != APIArtifactSupersededEventType {
		return fmt.Errorf("supersession fact %s has event_type %q, want %q", fact.EventID, fact.EventType, APIArtifactSupersededEventType)
	}
	if fact.ComponentID != APIArtifactPublicationComponentID {
		return fmt.Errorf("supersession fact %s has component_id %q, want %q", fact.EventID, fact.ComponentID, APIArtifactPublicationComponentID)
	}
	if err := fact.ArtifactID.Validate(); err != nil {
		return fmt.Errorf("supersession fact %s artifact_id: %w", fact.EventID, err)
	}
	// The identity a fact claims is checked by the same domain rule an artifact row is
	// checked against, so a fact cannot carry an unformulated task or run, or an actor
	// that is not a CORE agent, and still be treated as deliverable.
	if err := validateProjectionIdentity(fact.TaskID, fact.RunID, fact.ActorID); err != nil {
		return fmt.Errorf("supersession fact %s: %w", fact.EventID, err)
	}
	if err := fact.WorkstreamID.Validate(); err != nil {
		return fmt.Errorf("supersession fact %s workstream_id: %w", fact.EventID, err)
	}
	if fact.ActorKind != APIArtifactPublicationActorKind {
		return fmt.Errorf("supersession fact %s has actor_kind %q, want %q for an authenticated CORE agent", fact.EventID, fact.ActorKind, APIArtifactPublicationActorKind)
	}
	successorID, predecessorHash, successorHash, err := apiArtifactSupersessionFactFields(fact)
	if err != nil {
		return err
	}
	// The edge the payload names has to be a legal edge in itself: a canonical successor
	// id that is not the predecessor. The shared core validator owns id form and the
	// self-reference rule, so this path does not restate them.
	if err := modulecore.ValidateArtifactSupersession(fact.ArtifactID, successorID); err != nil {
		return fmt.Errorf("supersession fact %s payload %s: %w", fact.EventID, APIArtifactSupersessionPayloadSupersededBy, err)
	}
	if err := modulecore.ValidateContentHash(predecessorHash, "supersession fact payload predecessor_content_hash"); err != nil {
		return fmt.Errorf("supersession fact %s: %w", fact.EventID, err)
	}
	if err := modulecore.ValidateContentHash(successorHash, "supersession fact payload successor_content_hash"); err != nil {
		return fmt.Errorf("supersession fact %s: %w", fact.EventID, err)
	}
	return nil
}

// ValidateAPIArtifactSupersessionFactRow checks the parts of a stored fact that a
// supersession could not legitimately have changed by the time a later reader sees it: the
// predecessor row it is about still carries the task, run, actor and workstream the
// envelope names, the predecessor's stored edge is exactly the successor the payload
// names, and that successor row still exists in the same scope as its predecessor.
//
// What is deliberately not compared is either row's current body digest: a legitimate
// in-place update replaces the body and its digest after the edge was established, and
// those digests are exactly what the payload records as history. Comparing them would turn
// an accepted update into a load failure and would stop a superseded artifact's fact from
// being drained at all. Supersede uses the shared create check below and this check shares
// the immutable-reference rules with it, so reader and writer cannot drift apart.
func ValidateAPIArtifactSupersessionFactRow(fact modulecore.EventEnvelope, predecessor, successor APIArtifact) error {
	successorID, _, _, err := apiArtifactSupersessionFactFields(fact)
	if err != nil {
		return err
	}
	if err := validateAPIArtifactSupersessionFactRefs(fact, predecessor); err != nil {
		return err
	}
	if predecessor.SupersededBy != successorID {
		return fmt.Errorf("supersession fact %s names successor %s, but artifact %s stores superseded_by %q", fact.EventID, successorID, predecessor.ArtifactID, predecessor.SupersededBy)
	}
	if err := ValidateAPIArtifactScope(predecessor, successor); err != nil {
		return fmt.Errorf("supersession fact %s successor %s: %w", fact.EventID, successor.ArtifactID, err)
	}
	return nil
}

// validateAPIArtifactSupersessionFactRefs compares the references of a stored fact to the
// predecessor row it is bound to. Those references cannot move over the life of an
// artifact: the supersede operation rewrites only SupersededBy, and an ordinary Save is
// refused a change to task, run, actor, workstream, content role or kind of a stored row.
func validateAPIArtifactSupersessionFactRefs(fact modulecore.EventEnvelope, predecessor APIArtifact) error {
	if fact.ArtifactID != predecessor.ArtifactID {
		return fmt.Errorf("supersession fact %s binds artifact_id %s, want %s", fact.EventID, fact.ArtifactID, predecessor.ArtifactID)
	}
	if fact.TaskID != predecessor.TaskID {
		return fmt.Errorf("supersession fact %s has task_id %s, want %s", fact.EventID, fact.TaskID, predecessor.TaskID)
	}
	if fact.RunID != predecessor.RunID {
		return fmt.Errorf("supersession fact %s has run_id %s, want %s", fact.EventID, fact.RunID, predecessor.RunID)
	}
	if fact.ActorID != predecessor.ActorID {
		return fmt.Errorf("supersession fact %s has actor_id %q, want %q", fact.EventID, fact.ActorID, predecessor.ActorID)
	}
	if fact.WorkstreamID != modulecore.WorkstreamID(predecessor.WorkstreamID) {
		return fmt.Errorf("supersession fact %s has workstream_id %q, want %q", fact.EventID, fact.WorkstreamID, predecessor.WorkstreamID)
	}
	return nil
}

// ValidateAPIArtifactSupersessionFact reports whether fact is the supersession fact of
// exactly this edge, at the moment the owner Supersede operation is about to establish it:
// the persisted-fact contract, the immutable references the shared row check owns, the two
// digests the rows actually hold, a successor that shares the predecessor's scope, and no
// edge on the predecessor yet, because the edge is what this operation establishes.
//
// The envelope's own occurred_at is validated only as a timestamp: an event occurrence time
// and the creation metadata stored on either artifact row are different facts and are not
// silently equated here. This is a comparison of values the caller already holds, not
// authentication: the caller owns canonical v7 issuance, the trace and the verified actor,
// and a store may not infer provenance from a field it was handed.
func ValidateAPIArtifactSupersessionFact(predecessor, successor APIArtifact, fact modulecore.EventEnvelope) error {
	if err := ValidatePersistedAPIArtifactSupersessionFact(fact); err != nil {
		return err
	}
	if err := validateAPIArtifactSupersessionFactRefs(fact, predecessor); err != nil {
		return err
	}
	successorID, predecessorHash, successorHash, err := apiArtifactSupersessionFactFields(fact)
	if err != nil {
		return err
	}
	if successorID != successor.ArtifactID {
		return fmt.Errorf("supersession fact %s names successor %s, want %s", fact.EventID, successorID, successor.ArtifactID)
	}
	if err := ValidateAPIArtifactScope(predecessor, successor); err != nil {
		return fmt.Errorf("supersession fact %s successor %s: %w", fact.EventID, successor.ArtifactID, err)
	}
	if predecessorHash != predecessor.ContentHash {
		return fmt.Errorf("supersession fact %s payload predecessor_content_hash is %q, want the superseded digest %q", fact.EventID, predecessorHash, predecessor.ContentHash)
	}
	if successorHash != successor.ContentHash {
		return fmt.Errorf("supersession fact %s payload successor_content_hash is %q, want the successor digest %q", fact.EventID, successorHash, successor.ContentHash)
	}
	if predecessor.SupersededBy != "" {
		return fmt.Errorf("artifact %s already carries superseded_by %s, so supersession fact %s would describe an edge this call did not establish", predecessor.ArtifactID, predecessor.SupersededBy, fact.EventID)
	}
	return nil
}
