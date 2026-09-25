package browsertrace

import (
	"strings"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Publication intent fixtures for the persisted-intent contract. The workstream id is
// minted canonically because a creation envelope has to carry the same canonical
// workstream reference the artifact row belongs to, and both fixture values are
// prevalidated here so a malformed fixture cannot be mistaken for a contract failure.
const publicationIntentFixtureContent = "openapi: 3.1.0\ninfo:\n  title: publication intent fixture\n"

func publicationIntentFixtureArtifact(t *testing.T) APIArtifact {
	t.Helper()
	workstreamID := modulecore.NewWorkstreamID()
	if err := workstreamID.Validate(); err != nil {
		t.Fatalf("fixture workstream id %s is invalid: %v", workstreamID, err)
	}
	item := APIArtifact{
		ArtifactID:   modulecore.NewArtifactID(),
		Kind:         modulecore.ArtifactKindSpecification,
		TaskID:       modulecore.TaskID("tsk_00000000-0000-5000-8000-000000000001"),
		RunID:        modulecore.RunID("run_00000000-0000-5000-8000-000000000002"),
		ActorID:      "mio",
		WorkstreamID: string(workstreamID),
		Type:         APIArtifactTypeObservedOpenAPI,
		Title:        "Observed OpenAPI Draft",
		Status:       "generated",
		Content:      publicationIntentFixtureContent,
		ContentHash:  modulecore.ContentHashOf([]byte(publicationIntentFixtureContent)),
		CreatedAt:    time.Date(2026, 9, 22, 1, 0, 0, 0, time.UTC),
	}
	if err := ValidateAPIArtifact(item); err != nil {
		t.Fatalf("fixture artifact %s is invalid before any assertion: %v", item.ArtifactID, err)
	}
	return item
}

func publicationIntentFixtureEnvelope(t *testing.T, item APIArtifact) modulecore.EventEnvelope {
	t.Helper()
	envelope := modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion,
		EventID:       modulecore.NewEventID(),
		EventSeq:      0,
		TraceID:       modulecore.NewTraceID(),
		EventType:     APIArtifactCreatedEventType,
		ComponentID:   APIArtifactPublicationComponentID,
		OccurredAt:    time.Date(2026, 9, 22, 1, 0, 0, 0, time.UTC),
		WorkstreamID:  modulecore.WorkstreamID(item.WorkstreamID),
		TaskID:        item.TaskID,
		RunID:         item.RunID,
		ActorID:       item.ActorID,
		ActorKind:     APIArtifactPublicationActorKind,
		ArtifactID:    item.ArtifactID,
		Payload: map[string]any{
			APIArtifactPublicationPayloadArtifactKind: string(item.Kind),
			APIArtifactPublicationPayloadArtifactType: item.Type,
			APIArtifactPublicationPayloadContentHash:  item.ContentHash,
		},
	}
	if err := modulecore.ValidateEventEnvelope(envelope); err != nil {
		t.Fatalf("fixture envelope %s is invalid before any assertion: %v", envelope.EventID, err)
	}
	if err := ValidatePersistedAPIArtifactPublicationIntent(envelope); err != nil {
		t.Fatalf("fixture envelope %s is not a valid persisted intent before any assertion: %v", envelope.EventID, err)
	}
	return envelope
}

// TestValidatePersistedAPIArtifactPublicationIntentRejectsUnusableIdentity pins that a
// stored intent cannot claim an identity that no artifact row could carry: the canonical
// envelope validation skips optional ids that are simply absent, so the identity rules
// the artifact domain owns have to reject them here instead of an intent silently being
// kept as deliverable evidence.
func TestValidatePersistedAPIArtifactPublicationIntentRejectsUnusableIdentity(t *testing.T) {
	artifact := publicationIntentFixtureArtifact(t)
	tests := []struct {
		name    string
		mutate  func(*modulecore.EventEnvelope)
		wantErr string
	}{
		{
			name:    "missing task id",
			mutate:  func(e *modulecore.EventEnvelope) { e.TaskID = "" },
			wantErr: "task_id is invalid",
		},
		{
			name:    "malformed task id",
			mutate:  func(e *modulecore.EventEnvelope) { e.TaskID = modulecore.TaskID("task-1") },
			wantErr: "task_id",
		},
		{
			name:    "missing run id",
			mutate:  func(e *modulecore.EventEnvelope) { e.RunID = "" },
			wantErr: "run_id is invalid",
		},
		{
			name:    "malformed run id",
			mutate:  func(e *modulecore.EventEnvelope) { e.RunID = modulecore.RunID("run_1") },
			wantErr: "run_id",
		},
		{
			name:    "missing actor id",
			mutate:  func(e *modulecore.EventEnvelope) { e.ActorID = "" },
			wantErr: "actor_id must be one of mio, shiro, midori, kuro",
		},
		{
			name:    "actor id that is not a core agent",
			mutate:  func(e *modulecore.EventEnvelope) { e.ActorID = "mallory" },
			wantErr: "actor_id must be one of mio, shiro, midori, kuro",
		},
		{
			name:    "missing workstream id",
			mutate:  func(e *modulecore.EventEnvelope) { e.WorkstreamID = "" },
			wantErr: "workstream_id",
		},
		{
			name:    "non-canonical workstream id",
			mutate:  func(e *modulecore.EventEnvelope) { e.WorkstreamID = modulecore.WorkstreamID("ws_1") },
			wantErr: "workstream_id",
		},
		{
			name:    "actor kind that is not an agent",
			mutate:  func(e *modulecore.EventEnvelope) { e.ActorKind = "tool" },
			wantErr: "actor_kind",
		},
		{
			name:    "missing actor kind",
			mutate:  func(e *modulecore.EventEnvelope) { e.ActorKind = "" },
			wantErr: "actor_kind",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			envelope := publicationIntentFixtureEnvelope(t, artifact)
			tc.mutate(&envelope)
			err := ValidatePersistedAPIArtifactPublicationIntent(envelope)
			if err == nil {
				t.Fatalf("ValidatePersistedAPIArtifactPublicationIntent() = nil, want rejection: %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ValidatePersistedAPIArtifactPublicationIntent() error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestValidatePersistedAPIArtifactPublicationIntentRejectsUnusablePayload pins that the
// three payload fields are validated as the fields they are, not merely as strings: a
// content role this owner does not have, a kind that contradicts its role (two roles can
// share one kind, so the role/kind pair is what carries meaning) and a digest outside the
// canonical form have to fail closed.
func TestValidatePersistedAPIArtifactPublicationIntentRejectsUnusablePayload(t *testing.T) {
	artifact := publicationIntentFixtureArtifact(t)
	validHash := artifact.ContentHash
	tests := []struct {
		name    string
		mutate  func(*modulecore.EventEnvelope)
		wantErr string
	}{
		{
			name: "unknown content role",
			mutate: func(e *modulecore.EventEnvelope) {
				e.Payload[APIArtifactPublicationPayloadArtifactType] = "coverage_mystery"
			},
			wantErr: "unknown browsertrace artifact type",
		},
		{
			name: "kind that contradicts its role",
			mutate: func(e *modulecore.EventEnvelope) {
				e.Payload[APIArtifactPublicationPayloadArtifactType] = "coverage_report"
				e.Payload[APIArtifactPublicationPayloadArtifactKind] = string(modulecore.ArtifactKindSpecification)
			},
			wantErr: "does not match artifact_type",
		},
		{
			name: "missing kind",
			mutate: func(e *modulecore.EventEnvelope) {
				e.Payload[APIArtifactPublicationPayloadArtifactKind] = ""
			},
			wantErr: "artifact_kind is required",
		},
		{
			name: "content hash without the canonical prefix",
			mutate: func(e *modulecore.EventEnvelope) {
				e.Payload[APIArtifactPublicationPayloadContentHash] = strings.TrimPrefix(validHash, modulecore.ContentHashPrefix)
			},
			wantErr: "content_hash",
		},
		{
			name: "empty content hash",
			mutate: func(e *modulecore.EventEnvelope) {
				e.Payload[APIArtifactPublicationPayloadContentHash] = ""
			},
			wantErr: "content_hash",
		},
		{
			name: "content hash that is not lowercase hex",
			mutate: func(e *modulecore.EventEnvelope) {
				e.Payload[APIArtifactPublicationPayloadContentHash] = modulecore.ContentHashPrefix + strings.ToUpper(strings.TrimPrefix(validHash, modulecore.ContentHashPrefix))
			},
			wantErr: "content_hash",
		},
		{
			name: "content hash with a truncated digest",
			mutate: func(e *modulecore.EventEnvelope) {
				e.Payload[APIArtifactPublicationPayloadContentHash] = validHash[:len(validHash)-8]
			},
			wantErr: "content_hash",
		},
		{
			name: "missing payload field",
			mutate: func(e *modulecore.EventEnvelope) {
				delete(e.Payload, APIArtifactPublicationPayloadContentHash)
			},
			wantErr: "keys, want the 3 declared creation fields",
		},
		{
			name: "non-string payload field",
			mutate: func(e *modulecore.EventEnvelope) {
				e.Payload[APIArtifactPublicationPayloadArtifactKind] = 1
			},
			wantErr: "must be a string",
		},
		{
			name: "undeclared extra payload field",
			mutate: func(e *modulecore.EventEnvelope) {
				e.Payload["body"] = publicationIntentFixtureContent
			},
			wantErr: "keys, want the 3 declared creation fields",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			envelope := publicationIntentFixtureEnvelope(t, artifact)
			tc.mutate(&envelope)
			err := ValidatePersistedAPIArtifactPublicationIntent(envelope)
			if err == nil {
				t.Fatalf("ValidatePersistedAPIArtifactPublicationIntent() = nil, want rejection: %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ValidatePersistedAPIArtifactPublicationIntent() error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestValidateAPIArtifactPublicationIntentRowTracksOnlyImmutableReferences pins the
// reader boundary: the original creation intent stays valid against a row whose body,
// digest and supersession edge were legitimately changed afterwards, while a row that
// moved to another task, run, actor, workstream, content role or kind contradicts the
// intent that is supposed to describe its creation.
func TestValidateAPIArtifactPublicationIntentRowTracksOnlyImmutableReferences(t *testing.T) {
	artifact := publicationIntentFixtureArtifact(t)
	envelope := publicationIntentFixtureEnvelope(t, artifact)
	if err := ValidateAPIArtifactPublicationIntentRow(envelope, artifact); err != nil {
		t.Fatalf("ValidateAPIArtifactPublicationIntentRow() error = %v for the untouched creation pair", err)
	}

	updated := artifact
	updated.Content = "openapi: 3.1.0\ninfo:\n  title: rewritten body\n"
	updated.ContentHash = modulecore.ContentHashOf([]byte(updated.Content))
	updated.Title = "Observed OpenAPI Revised"
	updated.Status = "draft"
	updated.SupersededBy = modulecore.NewArtifactID()
	if err := ValidateAPIArtifact(updated); err != nil {
		t.Fatalf("updated fixture artifact %s is invalid before any assertion: %v", updated.ArtifactID, err)
	}
	if err := ValidatePersistedAPIArtifactPublicationIntent(envelope); err != nil {
		t.Errorf("ValidatePersistedAPIArtifactPublicationIntent() after a legitimate row update = %v, want the stored creation intent to stay valid", err)
	}
	if err := ValidateAPIArtifactPublicationIntentRow(envelope, updated); err != nil {
		t.Errorf("ValidateAPIArtifactPublicationIntentRow() after a legitimate row update = %v, want only the immutable references checked", err)
	}

	// The create-time validator is the one that does compare the created digest and the
	// absence of an edge, so it is the place where the same updated row is not a creation.
	if err := ValidateAPIArtifactPublicationIntent(updated, envelope); err == nil {
		t.Errorf("ValidateAPIArtifactPublicationIntent() = nil for a row that was updated and superseded after creation, want rejection")
	}

	moved := map[string]func(*APIArtifact){
		"task":         func(a *APIArtifact) { a.TaskID = modulecore.TaskID("tsk_00000000-0000-5000-8000-000000000009") },
		"run":          func(a *APIArtifact) { a.RunID = modulecore.RunID("run_00000000-0000-5000-8000-000000000009") },
		"actor":        func(a *APIArtifact) { a.ActorID = "shiro" },
		"workstream":   func(a *APIArtifact) { a.WorkstreamID = string(modulecore.NewWorkstreamID()) },
		"content role": func(a *APIArtifact) { a.Type = APIArtifactTypeCoverageReport },
		"kind":         func(a *APIArtifact) { a.Kind = modulecore.ArtifactKindDocument },
	}
	for name, mutate := range moved {
		t.Run("row moved to another "+name, func(t *testing.T) {
			movedRow := artifact
			mutate(&movedRow)
			err := ValidateAPIArtifactPublicationIntentRow(envelope, movedRow)
			if err == nil {
				t.Fatalf("ValidateAPIArtifactPublicationIntentRow() = nil after the row's %s moved, want rejection", name)
			}
			if !strings.Contains(err.Error(), string(envelope.EventID)) {
				t.Errorf("ValidateAPIArtifactPublicationIntentRow() error = %v, want it to name intent %s", err, envelope.EventID)
			}
		})
	}
}

// TestValidateAPIArtifactPublicationIntentChecksCreatedDigest pins the create-time
// binding that the shared row check leaves out on purpose: the intent has to carry the
// digest of the bytes being created and the row must not already claim an edge.
func TestValidateAPIArtifactPublicationIntentChecksCreatedDigest(t *testing.T) {
	artifact := publicationIntentFixtureArtifact(t)
	envelope := publicationIntentFixtureEnvelope(t, artifact)
	if err := ValidateAPIArtifactPublicationIntent(artifact, envelope); err != nil {
		t.Fatalf("ValidateAPIArtifactPublicationIntent() error = %v for the exact creation pair", err)
	}

	wrongDigest := publicationIntentFixtureEnvelope(t, artifact)
	wrongDigest.Payload[APIArtifactPublicationPayloadContentHash] = modulecore.ContentHashOf([]byte("other bytes"))
	if err := ValidateAPIArtifactPublicationIntent(artifact, wrongDigest); err == nil ||
		!strings.Contains(err.Error(), "want the created digest") {
		t.Errorf("ValidateAPIArtifactPublicationIntent() error = %v, want the created digest mismatch", err)
	}

	alreadySuperseded := artifact
	alreadySuperseded.SupersededBy = modulecore.NewArtifactID()
	if err := ValidateAPIArtifactPublicationIntent(alreadySuperseded, envelope); err == nil ||
		!strings.Contains(err.Error(), "only SupersedeAPIArtifact may establish an edge") {
		t.Errorf("ValidateAPIArtifactPublicationIntent() error = %v, want the creation-with-edge rejection", err)
	}
}
