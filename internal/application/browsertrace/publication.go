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

// ErrAPIArtifactPublicationConflict reports that the canonical event store already holds
// an event under the EventID a persisted creation intent names, and that event is not the
// creation fact this owner stored. It is a conflict, not a duplicate to be ignored: the
// two records claim one EventID for different facts, so no writer may overwrite either.
var ErrAPIArtifactPublicationConflict = errors.New("browser trace api artifact creation fact conflicts with the canonical event store")

// APIArtifactCreationPublisher is the one canonical event store that a browsertrace
// creation fact is delivered to. Only the two operations a replay needs are declared:
// the direct lookup of the persisted EventID, and the sequence-assigning append that the
// canonical owner owns. ListByComponent is deliberately absent, because a display bound
// cannot decide whether an older source event is observable.
type APIArtifactCreationPublisher interface {
	GetByID(ctx context.Context, eventID modulecore.EventID) (modulecore.EventEnvelope, bool, error)
	AppendSequenced(ctx context.Context, event modulecore.EventEnvelope) (modulecore.EventEnvelope, error)
}

// APIArtifactPublicationSource is the artifact owner's side of the same contract: the
// durable intents an artifact store kept next to the rows it created. It is paged by
// artifact id so a caller can drain every intent, not just the newest page.
type APIArtifactPublicationSource interface {
	ListAPIArtifactPublicationIntents(ctx context.Context, after modulecore.ArtifactID, limit int) ([]modulecore.EventEnvelope, error)
}

// NewAPIArtifactCreationIntent mints the canonical creation fact of one newly created
// APIArtifact: a fresh v7 EventID, the caller's trace, the browsertrace component and
// content-role payload the domain declares, and the verified task, run, actor and
// workstream of the artifact itself.
//
// The caller owns issuance and provenance, and this function adds neither: it mints no
// ArtifactID, derives no EventID from content, and authenticates nobody. The occurrence
// time is the time the fact was minted, which the domain deliberately does not equate with
// the artifact's stored creation metadata. The result keeps EventSeq zero because the
// canonical event store, and only the canonical event store, assigns sequences.
func NewAPIArtifactCreationIntent(item domaintrace.APIArtifact, traceID modulecore.TraceID, occurredAt time.Time) (modulecore.EventEnvelope, error) {
	if err := domaintrace.ValidateAPIArtifact(item); err != nil {
		return modulecore.EventEnvelope{}, err
	}
	intent := modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion,
		EventID:       modulecore.NewEventID(),
		EventSeq:      0,
		TraceID:       traceID,
		EventType:     domaintrace.APIArtifactCreatedEventType,
		ComponentID:   domaintrace.APIArtifactPublicationComponentID,
		OccurredAt:    occurredAt.UTC(),
		WorkstreamID:  modulecore.WorkstreamID(item.WorkstreamID),
		TaskID:        item.TaskID,
		RunID:         item.RunID,
		ActorKind:     domaintrace.APIArtifactPublicationActorKind,
		ActorID:       item.ActorID,
		ArtifactID:    item.ArtifactID,
		Payload: map[string]any{
			domaintrace.APIArtifactPublicationPayloadArtifactKind: string(item.Kind),
			domaintrace.APIArtifactPublicationPayloadArtifactType: item.Type,
			domaintrace.APIArtifactPublicationPayloadContentHash:  item.ContentHash,
		},
	}
	if err := modulecore.ValidateEventEnvelope(intent); err != nil {
		return modulecore.EventEnvelope{}, fmt.Errorf("mint creation fact of artifact %s: %w", item.ArtifactID, err)
	}
	if err := domaintrace.ValidateAPIArtifactPublicationIntent(item, intent); err != nil {
		return modulecore.EventEnvelope{}, err
	}
	return intent, nil
}

// APIArtifactPublicationReport counts one publication pass. Published means this pass
// appended the fact, Confirmed means the canonical store already held exactly the fact
// the intent describes, and Failed means the intent stays durable in the artifact store
// for a later retry. A pass with any Failed is not a success, and the report never
// claims a delivery that only a successful readback produced.
type APIArtifactPublicationReport struct {
	Scanned   int
	Published int
	Confirmed int
	Failed    int
}

func (r APIArtifactPublicationReport) errDetails(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("browser trace api artifact publication %d published, %d confirmed, %d failed of %d intents: %w",
		r.Published, r.Confirmed, r.Failed, r.Scanned, errors.Join(errs...))
}

// PublishAPIArtifactCreationIntents delivers the given creation facts to the canonical
// event store, in the given order, and reports what it delivered. It is the pass a
// request path uses right after it durably stored a new pair, so a successful response
// cannot rest on an artifact whose creation fact was never written anywhere else.
func PublishAPIArtifactCreationIntents(ctx context.Context, events APIArtifactCreationPublisher, intents ...modulecore.EventEnvelope) (APIArtifactPublicationReport, error) {
	var report APIArtifactPublicationReport
	var failures []error
	if events == nil {
		return report, errors.New("publish browser trace api artifact creation facts: canonical event store unavailable")
	}
	for _, intent := range intents {
		if err := ctx.Err(); err != nil {
			report.Failed += len(intents) - report.Scanned
			failures = append(failures, fmt.Errorf("publish browser trace api artifact creation facts: %w", err))
			return report, report.errDetails(failures)
		}
		report.Scanned++
		outcome, err := publishAPIArtifactCreationIntent(ctx, events, intent)
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

// PublishPendingAPIArtifactCreationFacts drains every creation intent the artifact store
// still holds and delivers it to the canonical event store. It walks the artifact_id
// keyset forward, feeding the last id of a page back in until a page comes back empty, so
// no total limit can leave an older artifact with an unpublished creation fact and no
// caller to retry it: a restart reaches the same state without waiting for a request to
// be resent. Per-page size is left to the store's own default, which is the single owner
// of that bound.
func PublishPendingAPIArtifactCreationFacts(ctx context.Context, source APIArtifactPublicationSource, events APIArtifactCreationPublisher) (APIArtifactPublicationReport, error) {
	var report APIArtifactPublicationReport
	var failures []error
	if source == nil || events == nil {
		return report, errors.New("recover browser trace api artifact creation facts: artifact store or canonical event store unavailable")
	}
	after := modulecore.ArtifactID("")
	for {
		if err := ctx.Err(); err != nil {
			return report, report.errDetails(append(failures, fmt.Errorf("recover browser trace api artifact creation facts: %w", err)))
		}
		page, err := source.ListAPIArtifactPublicationIntents(ctx, after, 0)
		if err != nil {
			return report, report.errDetails(append(failures, err))
		}
		if len(page) == 0 {
			return report, report.errDetails(failures)
		}
		passed, err := PublishAPIArtifactCreationIntents(ctx, events, page...)
		report.Scanned += passed.Scanned
		report.Published += passed.Published
		report.Confirmed += passed.Confirmed
		report.Failed += passed.Failed
		last := page[len(page)-1].ArtifactID
		if err != nil {
			failures = append(failures, err)
		}
		if last <= after {
			return report, report.errDetails(append(failures, fmt.Errorf("publication cursor %q did not advance past %q", last, after)))
		}
		after = last
	}
}

type publicationOutcome int

const (
	publicationOutcomeConfirmed publicationOutcome = iota
	publicationOutcomePublished
)

// publishAPIArtifactCreationIntent delivers one persisted intent. The decision is always
// made against the canonical store's own answer, never against a local guess:
//
//   - the EventID is already there, and the stored event is the fact this intent
//     describes: confirmed, and nothing is appended.
//   - the EventID is already there with different fields: a conflict. The sequence the
//     canonical owner assigned is the only field a match does not require, and the intent
//     keeps EventSeq zero by contract, so this comparison cannot be satisfied by an
//     unrelated event that happens to share an id.
//   - the EventID is not there: append through the canonical sequence owner. If that
//     append reports a failure, the intent is only called delivered when a readback finds
//     the very fact this store persisted — a competing writer that won the race is a
//     confirmation, an unconfirmed append is not.
func publishAPIArtifactCreationIntent(ctx context.Context, events APIArtifactCreationPublisher, intent modulecore.EventEnvelope) (publicationOutcome, error) {
	if err := domaintrace.ValidatePersistedAPIArtifactPublicationIntent(intent); err != nil {
		return publicationOutcomeConfirmed, err
	}
	stored, found, err := events.GetByID(ctx, intent.EventID)
	if err != nil {
		return publicationOutcomeConfirmed, fmt.Errorf("read creation fact %s of artifact %s from the canonical event store: %w", intent.EventID, intent.ArtifactID, err)
	}
	if found {
		if err := compareAPICreationFact(intent, stored); err != nil {
			return publicationOutcomeConfirmed, err
		}
		return publicationOutcomeConfirmed, nil
	}
	persisted, err := events.AppendSequenced(ctx, intent)
	if err != nil {
		reread, found, readErr := events.GetByID(ctx, intent.EventID)
		if readErr == nil && found {
			if cmpErr := compareAPICreationFact(intent, reread); cmpErr != nil {
				return publicationOutcomeConfirmed, cmpErr
			}
			return publicationOutcomeConfirmed, nil
		}
		return publicationOutcomeConfirmed, fmt.Errorf("append creation fact %s of artifact %s: %w", intent.EventID, intent.ArtifactID, err)
	}
	if err := compareAPICreationFact(intent, persisted); err != nil {
		return publicationOutcomePublished, err
	}
	return publicationOutcomePublished, nil
}

// compareAPICreationFact requires the stored event to be the fact the intent describes.
// EventSeq is the one excluded field: the canonical event store assigns it, while the
// persisted intent is required to hold zero, so comparing it would report every correctly
// published fact as a conflict.
func compareAPICreationFact(intent, stored modulecore.EventEnvelope) error {
	if !reflect.DeepEqual(withoutEventSeq(intent), withoutEventSeq(stored)) {
		return fmt.Errorf("%w: canonical event store holds event_id %s for artifact %s with different fields than the persisted creation intent",
			ErrAPIArtifactPublicationConflict, stored.EventID, intent.ArtifactID)
	}
	return nil
}

func withoutEventSeq(event modulecore.EventEnvelope) modulecore.EventEnvelope {
	out := event
	out.EventSeq = 0
	return out
}
