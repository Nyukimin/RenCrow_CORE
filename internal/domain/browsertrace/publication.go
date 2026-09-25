package browsertrace

import (
	"fmt"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// APIArtifactCreatedEventType is the canonical event type of the fact that a
// browsertrace APIArtifact was created. The publication intent that a store keeps for
// a new artifact is the envelope of exactly this fact, so the name is declared once
// here and not restated per store or per caller.
const APIArtifactCreatedEventType = "artifact.created"

// APIArtifactPublicationComponentID is the stable, documented component that owns
// browsertrace APIArtifact creation facts. An intent naming another component is a
// different owner's fact and is not this store's to publish.
const APIArtifactPublicationComponentID = "browsertrace"

// APIArtifactPublicationActorKind is the actor provenance of a browsertrace APIArtifact
// creation fact: an authenticated CORE agent, never an LLM-supplied name and never an
// unattributed envelope. The caller owns the verified ActorID; this owner only refuses a
// creation intent that does not claim the agent provenance the contract declares.
const APIArtifactPublicationActorKind = "agent"

// Payload keys of an APIArtifact creation intent. The digest and content role are
// carried so a replay can prove that the event describes the bytes that were stored,
// without the intent becoming a second copy of the body.
const (
	APIArtifactPublicationPayloadArtifactKind = "artifact_kind"
	APIArtifactPublicationPayloadArtifactType = "artifact_type"
	APIArtifactPublicationPayloadContentHash  = "content_hash"
)

// apiArtifactPublicationPayloadFieldCount is the number of fields a creation intent
// payload declares. Anything else is a payload this owner did not write.
const apiArtifactPublicationPayloadFieldCount = 3

// apiArtifactPublicationIntentFields decodes the three declared creation fields of a
// stored intent once, into typed values, so every validator that compares them shares a
// single reading of the payload instead of restating the key names and the shape check.
func apiArtifactPublicationIntentFields(intent modulecore.EventEnvelope) (string, modulecore.ArtifactKind, string, error) {
	if len(intent.Payload) != apiArtifactPublicationPayloadFieldCount {
		return "", "", "", fmt.Errorf("publication intent %s payload carries %d keys, want the %d declared creation fields", intent.EventID, len(intent.Payload), apiArtifactPublicationPayloadFieldCount)
	}
	for _, key := range []string{
		APIArtifactPublicationPayloadArtifactKind,
		APIArtifactPublicationPayloadArtifactType,
		APIArtifactPublicationPayloadContentHash,
	} {
		value, found := intent.Payload[key]
		if !found {
			return "", "", "", fmt.Errorf("publication intent %s payload is missing %s", intent.EventID, key)
		}
		if _, ok := value.(string); !ok {
			return "", "", "", fmt.Errorf("publication intent %s payload %s must be a string, got %T", intent.EventID, key, value)
		}
	}
	artifactType, _ := intent.Payload[APIArtifactPublicationPayloadArtifactType].(string)
	kind := modulecore.ArtifactKind(intent.Payload[APIArtifactPublicationPayloadArtifactKind].(string))
	contentHash, _ := intent.Payload[APIArtifactPublicationPayloadContentHash].(string)
	return artifactType, kind, contentHash, nil
}

// ValidatePersistedAPIArtifactPublicationIntent reports whether a stored intent is a
// self-contained browsertrace APIArtifact creation envelope: the canonical envelope
// contract, the sequence the canonical event store still has to assign, this owner's
// event type and component, a bound artifact id, the task, run and actor identity that
// the artifact domain already owns, a canonical workstream, the agent actor provenance,
// and a payload whose three declared fields name a real content role, the kind that role
// stores, and a canonical content digest.
//
// A reader that only holds the stored envelope uses this, because a creation intent is
// immutable history: a later legitimate body update or supersession changes the artifact
// row's digest and edge, so re-checking the stored intent against that mutable row would
// turn an accepted update into a startup failure. The value-level binding to a row is
// ValidateAPIArtifactPublicationIntentRow's job.
func ValidatePersistedAPIArtifactPublicationIntent(intent modulecore.EventEnvelope) error {
	if err := modulecore.ValidateEventEnvelope(intent); err != nil {
		return fmt.Errorf("publication intent %s: %w", intent.EventID, err)
	}
	if intent.EventSeq != 0 {
		return fmt.Errorf("publication intent %s has event_seq %d: the canonical event store assigns the sequence", intent.EventID, intent.EventSeq)
	}
	if intent.EventType != APIArtifactCreatedEventType {
		return fmt.Errorf("publication intent %s has event_type %q, want %q", intent.EventID, intent.EventType, APIArtifactCreatedEventType)
	}
	if intent.ComponentID != APIArtifactPublicationComponentID {
		return fmt.Errorf("publication intent %s has component_id %q, want %q", intent.EventID, intent.ComponentID, APIArtifactPublicationComponentID)
	}
	if err := intent.ArtifactID.Validate(); err != nil {
		return fmt.Errorf("publication intent %s artifact_id: %w", intent.EventID, err)
	}
	// The identity a creation intent claims is checked by the same domain rule an
	// artifact row is checked against, so an intent cannot carry an unformulated task or
	// run, or an actor that is not a CORE agent, and still be treated as deliverable.
	if err := validateProjectionIdentity(intent.TaskID, intent.RunID, intent.ActorID); err != nil {
		return fmt.Errorf("publication intent %s: %w", intent.EventID, err)
	}
	if err := intent.WorkstreamID.Validate(); err != nil {
		return fmt.Errorf("publication intent %s workstream_id: %w", intent.EventID, err)
	}
	if intent.ActorKind != APIArtifactPublicationActorKind {
		return fmt.Errorf("publication intent %s has actor_kind %q, want %q for an authenticated CORE agent", intent.EventID, intent.ActorKind, APIArtifactPublicationActorKind)
	}
	artifactType, kind, contentHash, err := apiArtifactPublicationIntentFields(intent)
	if err != nil {
		return err
	}
	// The payload has to describe a real browsertrace content role together with the kind
	// that role stores, and a digest in canonical form, because replay uses this payload as
	// the evidence of the bytes that were created.
	if err := ValidateAPIArtifactKindForType(artifactType, kind); err != nil {
		return fmt.Errorf("publication intent %s payload: %w", intent.EventID, err)
	}
	if err := modulecore.ValidateContentHash(contentHash, "publication intent payload content_hash"); err != nil {
		return fmt.Errorf("publication intent %s: %w", intent.EventID, err)
	}
	return nil
}

// ValidateAPIArtifactPublicationIntentRow checks the parts of a stored intent that cannot
// change over the life of the artifact it belongs to: the artifact id it is bound to and
// the task, run, actor, workstream, content role and kind that row carries.
//
// It says nothing about the row's current body digest or its supersession edge, because
// those are exactly what a legitimate in-place update and the owner Supersede operation
// change after creation: comparing them would report an accepted update as corrupt intent
// history and would stop a superseded artifact's intent from loading at all. Create uses
// this shared check and then adds the create-only checks, so the reader and the writer
// agree on the immutable references without duplicating the rules.
func ValidateAPIArtifactPublicationIntentRow(intent modulecore.EventEnvelope, item APIArtifact) error {
	artifactType, kind, _, err := apiArtifactPublicationIntentFields(intent)
	if err != nil {
		return err
	}
	if intent.ArtifactID != item.ArtifactID {
		return fmt.Errorf("publication intent %s binds artifact_id %s, want %s", intent.EventID, intent.ArtifactID, item.ArtifactID)
	}
	if intent.TaskID != item.TaskID {
		return fmt.Errorf("publication intent %s has task_id %s, want %s", intent.EventID, intent.TaskID, item.TaskID)
	}
	if intent.RunID != item.RunID {
		return fmt.Errorf("publication intent %s has run_id %s, want %s", intent.EventID, intent.RunID, item.RunID)
	}
	if intent.ActorID != item.ActorID {
		return fmt.Errorf("publication intent %s has actor_id %q, want %q", intent.EventID, intent.ActorID, item.ActorID)
	}
	if intent.WorkstreamID != modulecore.WorkstreamID(item.WorkstreamID) {
		return fmt.Errorf("publication intent %s has workstream_id %q, want %q", intent.EventID, intent.WorkstreamID, item.WorkstreamID)
	}
	if artifactType != item.Type {
		return fmt.Errorf("publication intent %s payload artifact_type is %q, want the row's content role %q", intent.EventID, artifactType, item.Type)
	}
	if kind != item.Kind {
		return fmt.Errorf("publication intent %s payload artifact_kind is %q, want the row's kind %q", intent.EventID, kind, item.Kind)
	}
	return nil
}

// ValidateAPIArtifactPublicationIntent reports whether intent is the creation fact of
// exactly this artifact: the persisted intent contract, the immutable references the
// shared row check owns, the digest of the bytes being created, and no supersession edge
// yet, because an edge is established later by the owner Supersede operation after both
// rows were read and checked.
//
// The envelope's own occurred_at is validated only as a timestamp: an event occurrence
// time and the creation metadata stored on the artifact row (which may be captured or
// imported metadata) are different facts and are not silently equated here. This is a
// comparison of two values the caller already holds, not authentication: the caller owns
// canonical v7 issuance, the trace and the verified actor, and a store may not infer
// provenance from a field it was handed.
func ValidateAPIArtifactPublicationIntent(item APIArtifact, intent modulecore.EventEnvelope) error {
	if err := ValidatePersistedAPIArtifactPublicationIntent(intent); err != nil {
		return err
	}
	if err := ValidateAPIArtifactPublicationIntentRow(intent, item); err != nil {
		return err
	}
	_, _, contentHash, err := apiArtifactPublicationIntentFields(intent)
	if err != nil {
		return err
	}
	if contentHash != item.ContentHash {
		return fmt.Errorf("publication intent %s payload content_hash is %q, want the created digest %q", intent.EventID, contentHash, item.ContentHash)
	}
	if item.SupersededBy != "" {
		return fmt.Errorf("artifact %s is created with superseded_by %s: only SupersedeAPIArtifact may establish an edge", item.ArtifactID, item.SupersededBy)
	}
	return nil
}
