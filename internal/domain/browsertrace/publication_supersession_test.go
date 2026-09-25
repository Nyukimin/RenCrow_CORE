package browsertrace

import (
	"reflect"
	"strings"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Supersession fact fixtures. The predecessor reuses the publication-intent fixture so the
// two facts describe one artifact's life, and the successor is a distinct artifact id that
// shares the scope the supersession scope rule owns. Both rows and the fact are prevalidated
// here so a malformed fixture cannot be mistaken for a contract failure.
const supersessionFixtureSuccessorContent = "openapi: 3.1.0\ninfo:\n  title: supersession fixture successor\n"

func supersessionFixturePair(t *testing.T) (APIArtifact, APIArtifact) {
	t.Helper()
	predecessor := publicationIntentFixtureArtifact(t)
	successor := predecessor
	successor.ArtifactID = modulecore.NewArtifactID()
	successor.Content = supersessionFixtureSuccessorContent
	successor.ContentHash = modulecore.ContentHashOf([]byte(supersessionFixtureSuccessorContent))
	successor.Title = "Supersession Fixture Successor"
	successor.CreatedAt = predecessor.CreatedAt.Add(time.Minute)
	for _, item := range []APIArtifact{predecessor, successor} {
		if err := ValidateAPIArtifact(item); err != nil {
			t.Fatalf("fixture artifact %s is invalid before any assertion: %v", item.ArtifactID, err)
		}
	}
	if err := ValidateAPIArtifactScope(predecessor, successor); err != nil {
		t.Fatalf("fixture pair is not one supersession scope: %v", err)
	}
	return predecessor, successor
}

func supersessionFixtureFact(t *testing.T, predecessor, successor APIArtifact) modulecore.EventEnvelope {
	t.Helper()
	fact := modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion,
		EventID:       modulecore.NewEventID(),
		EventSeq:      0,
		TraceID:       modulecore.NewTraceID(),
		EventType:     APIArtifactSupersededEventType,
		ComponentID:   APIArtifactPublicationComponentID,
		OccurredAt:    time.Date(2026, 9, 22, 2, 0, 0, 0, time.UTC),
		WorkstreamID:  modulecore.WorkstreamID(predecessor.WorkstreamID),
		TaskID:        predecessor.TaskID,
		RunID:         predecessor.RunID,
		ActorID:       predecessor.ActorID,
		ActorKind:     APIArtifactPublicationActorKind,
		ArtifactID:    predecessor.ArtifactID,
		Payload: map[string]any{
			APIArtifactSupersessionPayloadSupersededBy:           string(successor.ArtifactID),
			APIArtifactSupersessionPayloadPredecessorContentHash: predecessor.ContentHash,
			APIArtifactSupersessionPayloadSuccessorContentHash:   successor.ContentHash,
		},
	}
	if err := modulecore.ValidateEventEnvelope(fact); err != nil {
		t.Fatalf("fixture supersession fact %s is invalid before any assertion: %v", fact.EventID, err)
	}
	if err := ValidatePersistedAPIArtifactSupersessionFact(fact); err != nil {
		t.Fatalf("fixture supersession fact %s is not a valid persisted fact before any assertion: %v", fact.EventID, err)
	}
	return fact
}

// copySupersessionFixtureFact snapshots an envelope so a case that mutates the payload in
// place can still be checked against what it started as: an envelope carries maps and
// slices, so comparing the mutated value to the pre-mutation value by identity would report
// every in-place payload change as "changed nothing".
func copySupersessionFixtureFact(fact modulecore.EventEnvelope) modulecore.EventEnvelope {
	out := fact
	if fact.Payload != nil {
		out.Payload = make(map[string]any, len(fact.Payload))
		for key, value := range fact.Payload {
			out.Payload[key] = value
		}
	}
	if fact.DependencyEventIDs != nil {
		out.DependencyEventIDs = append([]modulecore.EventID(nil), fact.DependencyEventIDs...)
	}
	return out
}

// TestValidatePersistedAPIArtifactSupersessionFactRejectsUnusableIdentity pins that a stored
// supersession fact cannot claim an identity no artifact row could carry, and cannot carry a
// payload that is not the three declared supersession fields. The canonical envelope
// validation skips optional ids that are simply absent, so the identity rules the artifact
// domain owns have to reject them here rather than a fact being kept as deliverable evidence.
func TestValidatePersistedAPIArtifactSupersessionFactRejectsUnusableIdentity(t *testing.T) {
	predecessor, successor := supersessionFixturePair(t)
	type mutate func(modulecore.EventEnvelope) modulecore.EventEnvelope
	cases := []struct {
		name   string
		mutate mutate
		want   string
	}{
		{"sequence assigned", func(f modulecore.EventEnvelope) modulecore.EventEnvelope {
			f.EventSeq = 7
			return f
		}, "event_seq"},
		{"creation event type", func(f modulecore.EventEnvelope) modulecore.EventEnvelope {
			f.EventType = APIArtifactCreatedEventType
			return f
		}, APIArtifactCreatedEventType},
		{"other owner component", func(f modulecore.EventEnvelope) modulecore.EventEnvelope {
			f.ComponentID = "superagent"
			return f
		}, "component_id"},
		{"unformulated task", func(f modulecore.EventEnvelope) modulecore.EventEnvelope {
			f.TaskID = modulecore.TaskID("tsk_not_formed")
			return f
		}, "task_id"},
		{"unformulated run", func(f modulecore.EventEnvelope) modulecore.EventEnvelope {
			f.RunID = modulecore.RunID("run_not_formed")
			return f
		}, "run_id"},
		{"actor that is not a core agent", func(f modulecore.EventEnvelope) modulecore.EventEnvelope {
			f.ActorID = "someone_else"
			return f
		}, "actor_id"},
		{"unformulated workstream", func(f modulecore.EventEnvelope) modulecore.EventEnvelope {
			f.WorkstreamID = modulecore.WorkstreamID("wks_not_formed")
			return f
		}, "workstream_id"},
		{"missing actor kind", func(f modulecore.EventEnvelope) modulecore.EventEnvelope {
			f.ActorKind = ""
			return f
		}, "actor_kind"},
		{"creation payload", func(f modulecore.EventEnvelope) modulecore.EventEnvelope {
			f.Payload = map[string]any{
				APIArtifactPublicationPayloadArtifactKind: string(successor.Kind),
				APIArtifactPublicationPayloadArtifactType: successor.Type,
				APIArtifactPublicationPayloadContentHash:  successor.ContentHash,
			}
			return f
		}, APIArtifactSupersessionPayloadSupersededBy},
		{"extra payload field", func(f modulecore.EventEnvelope) modulecore.EventEnvelope {
			f.Payload = map[string]any{
				APIArtifactSupersessionPayloadSupersededBy:           string(successor.ArtifactID),
				APIArtifactSupersessionPayloadPredecessorContentHash: predecessor.ContentHash,
				APIArtifactSupersessionPayloadSuccessorContentHash:   successor.ContentHash,
				APIArtifactPublicationPayloadArtifactType:            successor.Type,
			}
			return f
		}, "keys"},
		{"payload field is not a string", func(f modulecore.EventEnvelope) modulecore.EventEnvelope {
			f.Payload[APIArtifactSupersessionPayloadSuccessorContentHash] = 7
			return f
		}, APIArtifactSupersessionPayloadSuccessorContentHash},
		{"self edge", func(f modulecore.EventEnvelope) modulecore.EventEnvelope {
			f.Payload[APIArtifactSupersessionPayloadSupersededBy] = string(predecessor.ArtifactID)
			return f
		}, "itself"},
		{"unformulated successor", func(f modulecore.EventEnvelope) modulecore.EventEnvelope {
			f.Payload[APIArtifactSupersessionPayloadSupersededBy] = "art_not_formed"
			return f
		}, "superseded_by"},
		{"predecessor digest is not canonical", func(f modulecore.EventEnvelope) modulecore.EventEnvelope {
			f.Payload[APIArtifactSupersessionPayloadPredecessorContentHash] = "sha256:short"
			return f
		}, "predecessor_content_hash"},
		{"successor digest is not canonical", func(f modulecore.EventEnvelope) modulecore.EventEnvelope {
			f.Payload[APIArtifactSupersessionPayloadSuccessorContentHash] = strings.Repeat("z", 64)
			return f
		}, "successor_content_hash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := supersessionFixtureFact(t, predecessor, successor)
			before := copySupersessionFixtureFact(base)
			fact := tc.mutate(base)
			if reflect.DeepEqual(fact, before) {
				t.Fatalf("mutation %q changed nothing, so the case asserts nothing", tc.name)
			}
			err := ValidatePersistedAPIArtifactSupersessionFact(fact)
			if err == nil {
				t.Fatalf("fact with %s accepted: %v", tc.name, fact.Payload)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

// TestValidateAPIArtifactSupersessionFactRowTracksOnlyImmutableReferences pins both halves of
// the row check: a fact stays valid when the only thing that moved is a body digest that a
// legitimate update replaced, and it fails closed when the edge the fact established, or a
// reference that cannot move, is not what the fact names.
func TestValidateAPIArtifactSupersessionFactRowTracksOnlyImmutableReferences(t *testing.T) {
	predecessor, successor := supersessionFixturePair(t)
	edge := predecessor
	edge.SupersededBy = successor.ArtifactID
	fact := supersessionFixtureFact(t, predecessor, successor)

	if err := ValidateAPIArtifactSupersessionFactRow(fact, edge, successor); err != nil {
		t.Fatalf("valid fact rejected against its own edge: %v", err)
	}

	// A later legitimate body update on either row replaces the digest this fact records as
	// history, so it must not turn accepted history into a load failure.
	updatedPredecessor := edge
	updatedPredecessor.Content = "openapi: 3.1.0\ninfo:\n  title: updated after supersession\n"
	updatedPredecessor.ContentHash = modulecore.ContentHashOf([]byte(updatedPredecessor.Content))
	if err := ValidateAPIArtifact(updatedPredecessor); err != nil {
		t.Fatalf("updated predecessor fixture is invalid: %v", err)
	}
	if err := ValidateAPIArtifactSupersessionFactRow(fact, updatedPredecessor, successor); err != nil {
		t.Errorf("fact rejected after a legitimate predecessor body update: %v", err)
	}
	updatedSuccessor := successor
	updatedSuccessor.Content = supersessionFixtureSuccessorContent + "# revised\n"
	updatedSuccessor.ContentHash = modulecore.ContentHashOf([]byte(updatedSuccessor.Content))
	if err := ValidateAPIArtifact(updatedSuccessor); err != nil {
		t.Fatalf("updated successor fixture is invalid: %v", err)
	}
	if err := ValidateAPIArtifactSupersessionFactRow(fact, edge, updatedSuccessor); err != nil {
		t.Errorf("fact rejected after a legitimate successor body update: %v", err)
	}

	withEdge := func(mutate func(APIArtifact) APIArtifact) APIArtifact {
		moved := mutate(edge)
		if err := ValidateAPIArtifact(moved); err != nil {
			t.Fatalf("case predecessor fixture %s is invalid: %v", moved.ArtifactID, err)
		}
		return moved
	}
	otherSuccessor := successor
	otherSuccessor.ArtifactID = modulecore.NewArtifactID()
	type rowCase struct {
		name        string
		fact        modulecore.EventEnvelope
		predecessor APIArtifact
		successor   APIArtifact
		want        string
	}
	// The fact still names the successor it was minted for, while the row now carries a
	// different successor: the edge the fact established is no longer the stored edge.
	movedEdge := withEdge(func(p APIArtifact) APIArtifact { p.SupersededBy = otherSuccessor.ArtifactID; return p })
	if err := ValidateAPIArtifact(otherSuccessor); err != nil {
		t.Fatalf("other successor fixture is invalid: %v", err)
	}
	boundElsewhere := fact
	boundElsewhere.ArtifactID = modulecore.NewArtifactID()
	for _, tc := range []rowCase{
		{"edge cleared", fact, predecessor, successor, "superseded_by"},
		{"edge moved to another successor", fact, movedEdge, otherSuccessor, "superseded_by"},
		{"predecessor workstream moved", fact, withEdge(func(p APIArtifact) APIArtifact {
			p.WorkstreamID = string(modulecore.NewWorkstreamID())
			return p
		}), successor, "workstream_id"},
		{"predecessor task moved", fact, withEdge(func(p APIArtifact) APIArtifact {
			p.TaskID = modulecore.TaskID("tsk_00000000-0000-5000-8000-000000000009")
			return p
		}), successor, "task_id"},
		{"predecessor run moved", fact, withEdge(func(p APIArtifact) APIArtifact {
			p.RunID = modulecore.RunID("run_00000000-0000-5000-8000-000000000008")
			return p
		}), successor, "run_id"},
		{"predecessor actor moved", fact, withEdge(func(p APIArtifact) APIArtifact { p.ActorID = "shiro"; return p }), successor, "actor_id"},
		{"fact bound to another artifact", boundElsewhere, edge, successor, "artifact_id"},
		{"predecessor content role moved", fact, withEdge(func(p APIArtifact) APIArtifact {
			p.Type = APIArtifactTypeRiskAssessment
			p.Kind = modulecore.ArtifactKindReport
			return p
		}), successor, "content role"},
		{"successor left the scope", fact, edge, func() APIArtifact {
			s := successor
			s.WorkstreamID = string(modulecore.NewWorkstreamID())
			if err := ValidateAPIArtifact(s); err != nil {
				t.Fatalf("out of scope successor fixture is invalid: %v", err)
			}
			return s
		}(), "workstream_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAPIArtifactSupersessionFactRow(tc.fact, tc.predecessor, tc.successor)
			if err == nil {
				t.Fatalf("row check accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

// TestValidateAPIArtifactSupersessionFactChecksSupersededDigests pins the create-only half:
// the digests must be the bytes the two rows actually hold, the successor must be the row the
// fact names, and a predecessor that already carries an edge cannot have a new fact minted
// over it by this call.
func TestValidateAPIArtifactSupersessionFactChecksSupersededDigests(t *testing.T) {
	predecessor, successor := supersessionFixturePair(t)
	fact := supersessionFixtureFact(t, predecessor, successor)
	if err := ValidateAPIArtifactSupersessionFact(predecessor, successor, fact); err != nil {
		t.Fatalf("valid supersession fact rejected: %v", err)
	}

	// A predecessor that already carries an edge means this call did not establish it.
	edged := predecessor
	edged.SupersededBy = successor.ArtifactID
	if err := ValidateAPIArtifactSupersessionFact(edged, successor, fact); err == nil {
		t.Error("fact accepted for a predecessor that already carries the edge")
	} else if !strings.Contains(err.Error(), "superseded_by") {
		t.Errorf("error %q does not name the stored edge", err)
	}

	stalePredecessorDigest := fact
	stalePredecessorDigest.Payload = map[string]any{
		APIArtifactSupersessionPayloadSupersededBy:           string(successor.ArtifactID),
		APIArtifactSupersessionPayloadPredecessorContentHash: modulecore.ContentHashOf([]byte("another body\n")),
		APIArtifactSupersessionPayloadSuccessorContentHash:   successor.ContentHash,
	}
	if err := ValidateAPIArtifactSupersessionFact(predecessor, successor, stalePredecessorDigest); err == nil {
		t.Error("fact accepted while its predecessor digest is not the superseded bytes")
	} else if !strings.Contains(err.Error(), "predecessor_content_hash") {
		t.Errorf("error %q does not name the predecessor digest", err)
	}

	staleSuccessorDigest := fact
	staleSuccessorDigest.Payload = map[string]any{
		APIArtifactSupersessionPayloadSupersededBy:           string(successor.ArtifactID),
		APIArtifactSupersessionPayloadPredecessorContentHash: predecessor.ContentHash,
		APIArtifactSupersessionPayloadSuccessorContentHash:   modulecore.ContentHashOf([]byte("another body\n")),
	}
	if err := ValidateAPIArtifactSupersessionFact(predecessor, successor, staleSuccessorDigest); err == nil {
		t.Error("fact accepted while its successor digest is not the successor bytes")
	} else if !strings.Contains(err.Error(), "successor_content_hash") {
		t.Errorf("error %q does not name the successor digest", err)
	}

	another := successor
	another.ArtifactID = modulecore.NewArtifactID()
	if err := ValidateAPIArtifactSupersessionFact(predecessor, another, fact); err == nil {
		t.Error("fact accepted against a successor it does not name")
	} else if !strings.Contains(err.Error(), "successor") {
		t.Errorf("error %q does not name the successor", err)
	}

	// The create-only check is the persisted contract plus the create-only additions, so it
	// cannot be satisfied by a fact the persisted contract already refuses.
	assignedSeq := fact
	assignedSeq.EventSeq = 3
	if err := ValidateAPIArtifactSupersessionFact(predecessor, successor, assignedSeq); err == nil {
		t.Error("fact accepted with an event sequence this owner may not assign")
	}
}
