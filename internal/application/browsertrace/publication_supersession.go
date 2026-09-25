package browsertrace

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// APIArtifactSupersessionSource is the artifact owner's side of the supersession contract:
// the durable supersession facts an artifact store kept next to the edge it established.
// It is paged by predecessor artifact id so a caller can drain every fact, not just the
// newest page, and it resolves each fact against both rows it names before returning it.
type APIArtifactSupersessionSource interface {
	ListAPIArtifactSupersessionFacts(ctx context.Context, after modulecore.ArtifactID, limit int) ([]modulecore.EventEnvelope, error)
}

// NewAPIArtifactSupersessionFact mints the canonical supersession fact of one edge about
// to be established: a fresh v7 EventID, the caller's trace, this owner's supersession
// event type and component, the task, run, actor and workstream of the predecessor row the
// fact is about, and a payload naming the successor together with the two content digests
// the rows actually hold at this moment.
//
// The caller owns issuance and provenance and this function adds neither: it mints no
// ArtifactID, derives no EventID from content, and authenticates nobody. The occurrence
// time is the time the fact was minted, which the domain deliberately does not equate with
// the creation metadata stored on either artifact row. The result keeps EventSeq zero
// because the canonical event store, and only the canonical event store, assigns sequences.
func NewAPIArtifactSupersessionFact(predecessor, successor domaintrace.APIArtifact, traceID modulecore.TraceID, occurredAt time.Time) (modulecore.EventEnvelope, error) {
	if err := domaintrace.ValidateAPIArtifactSupersessionPair(predecessor, successor); err != nil {
		return modulecore.EventEnvelope{}, err
	}
	fact := modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion,
		EventID:       modulecore.NewEventID(),
		EventSeq:      0,
		TraceID:       traceID,
		EventType:     domaintrace.APIArtifactSupersededEventType,
		ComponentID:   domaintrace.APIArtifactPublicationComponentID,
		OccurredAt:    occurredAt.UTC(),
		WorkstreamID:  modulecore.WorkstreamID(predecessor.WorkstreamID),
		TaskID:        predecessor.TaskID,
		RunID:         predecessor.RunID,
		ActorKind:     domaintrace.APIArtifactPublicationActorKind,
		ActorID:       predecessor.ActorID,
		ArtifactID:    predecessor.ArtifactID,
		Payload: map[string]any{
			domaintrace.APIArtifactSupersessionPayloadSupersededBy:           string(successor.ArtifactID),
			domaintrace.APIArtifactSupersessionPayloadPredecessorContentHash: predecessor.ContentHash,
			domaintrace.APIArtifactSupersessionPayloadSuccessorContentHash:   successor.ContentHash,
		},
	}
	if err := modulecore.ValidateEventEnvelope(fact); err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("mint supersession fact of artifact %s: %w", predecessor.ArtifactID, err)
	}
	if err := domaintrace.ValidateAPIArtifactSupersessionFact(predecessor, successor, fact); err != nil {
		return modulecore.EventEnvelope{}, err
	}
	return fact, nil
}

// PublishAPIArtifactSupersessionFacts delivers the given supersession facts to the one
// canonical event store, in the given order, and reports what it delivered. It is the pass
// a supersede path uses right after it durably stored the edge and its fact, so a
// successful response cannot rest on an edge whose event was never written anywhere else.
func PublishAPIArtifactSupersessionFacts(ctx context.Context, events APIArtifactCreationPublisher, facts ...modulecore.EventEnvelope) (APIArtifactPublicationReport, error) {
	var report APIArtifactPublicationReport
	var failures []error
	if events == nil {
		return report, errors.New("publish browser trace api artifact supersession facts: canonical event store unavailable")
	}
	for _, fact := range facts {
		if err := ctx.Err(); err != nil {
			report.Failed += len(facts) - report.Scanned
			failures = append(failures, fmt.Errorf("publish browser trace api artifact supersession facts: %w", err))
			return report, report.errDetails(failures)
		}
		report.Scanned++
		outcome, err := publishAPIArtifactSupersessionFact(ctx, events, fact)
		if err != nil {
			report.Failed++
			failures = append(failures, err)
			continue
		}
		if outcome == publicationOutcomePublished {
			report.Published++
			continue
		}
		report.Confirmed++
	}
	return report, report.errDetails(failures)
}

// PublishPendingAPIArtifactSupersessionFacts drains every supersession fact the artifact
// store still holds and delivers it to the canonical event store. It walks the predecessor
// artifact_id keyset forward, feeding the last id of a page back in until a page comes back
// empty, so no total limit can leave an older edge with an unpublished fact and no caller to
// retry it: a restart reaches the same state without waiting for a request to be resent.
// Per-page size is left to the store's own default, which is the single owner of that bound.
func PublishPendingAPIArtifactSupersessionFacts(ctx context.Context, source APIArtifactSupersessionSource, events APIArtifactCreationPublisher) (APIArtifactPublicationReport, error) {
	var report APIArtifactPublicationReport
	var failures []error
	if source == nil || events == nil {
		return report, errors.New("recover browser trace api artifact supersession facts: artifact store or canonical event store unavailable")
	}
	after := modulecore.ArtifactID("")
	for {
		if err := ctx.Err(); err != nil {
			return report, report.errDetails(append(failures, fmt.Errorf("recover browser trace api artifact supersession facts: %w", err)))
		}
		page, err := source.ListAPIArtifactSupersessionFacts(ctx, after, 0)
		if err != nil {
			return report, report.errDetails(append(failures, err))
		}
		if len(page) == 0 {
			return report, report.errDetails(failures)
		}
		passed, err := PublishAPIArtifactSupersessionFacts(ctx, events, page...)
		report.Scanned += passed.Scanned
		report.Published += passed.Published
		report.Confirmed += passed.Confirmed
		report.Failed += passed.Failed
		last := page[len(page)-1].ArtifactID
		if err != nil {
			failures = append(failures, err)
		}
		if last <= after {
			return report, report.errDetails(append(failures, fmt.Errorf("supersession fact cursor %q did not advance past %q", last, after)))
		}
		after = last
	}
}

// publishAPIArtifactSupersessionFact delivers one persisted fact through the same canonical
// contract the creation fact uses: the direct lookup of the persisted EventID and the
// sequence-assigning append. The decision is always made against the canonical store's own
// answer, never against a local guess. An EventID already held with different fields is a
// conflict, and an append that reported a failure only counts as delivered when a readback
// finds the very fact this store persisted.
func publishAPIArtifactSupersessionFact(ctx context.Context, events APIArtifactCreationPublisher, fact modulecore.EventEnvelope) (publicationOutcome, error) {
	if err := domaintrace.ValidatePersistedAPIArtifactSupersessionFact(fact); err != nil {
		return publicationOutcomeConfirmed, err
	}
	stored, found, err := events.GetByID(ctx, fact.EventID)
	if err != nil {
		return publicationOutcomeConfirmed, fmt.Errorf("read supersession fact %s of artifact %s from the canonical event store: %w", fact.EventID, fact.ArtifactID, err)
	}
	if found {
		if err := compareAPISupersessionFact(fact, stored); err != nil {
			return publicationOutcomeConfirmed, err
		}
		return publicationOutcomeConfirmed, nil
	}
	persisted, err := events.AppendSequenced(ctx, fact)
	if err != nil {
		reread, found, readErr := events.GetByID(ctx, fact.EventID)
		if readErr == nil && found {
			if cmpErr := compareAPISupersessionFact(fact, reread); cmpErr != nil {
				return publicationOutcomeConfirmed, cmpErr
			}
			return publicationOutcomeConfirmed, nil
		}
		return publicationOutcomeConfirmed, fmt.Errorf("append supersession fact %s of artifact %s: %w", fact.EventID, fact.ArtifactID, err)
	}
	if err := compareAPISupersessionFact(fact, persisted); err != nil {
		return publicationOutcomePublished, err
	}
	return publicationOutcomePublished, nil
}

// compareAPISupersessionFact requires the stored event to be the fact the intent describes.
// EventSeq is the one excluded field: the canonical event store assigns it, while a
// persisted supersession fact is required to hold zero, so comparing it would report every
// correctly published fact as a conflict.
func compareAPISupersessionFact(intent, stored modulecore.EventEnvelope) error {
	if !reflect.DeepEqual(withoutEventSeq(intent), withoutEventSeq(stored)) {
		return fmt.Errorf("%w: canonical event store holds event_id %s for artifact %s with different fields than the persisted supersession fact",
			ErrAPIArtifactPublicationConflict, stored.EventID, intent.ArtifactID)
	}
	return nil
}
