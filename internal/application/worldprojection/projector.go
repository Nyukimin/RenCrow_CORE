package worldprojection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	domainprojection "github.com/Nyukimin/RenCrow_CORE/internal/domain/worldprojection"
)

const (
	DefaultProjectionValidFor      = 15 * time.Minute
	DefaultProjectionRefreshBefore = 5 * time.Minute
)

// SourceRevisionStore owns CORE's durable monotonic revision per
// destination/kind/epoch. It must reject an epoch change; that path requires an
// explicit reconcile and is never inferred by the projector.
type SourceRevisionStore interface {
	NextSourceRevision(context.Context, domainprojection.Target, string, string, time.Time) (uint64, error)
}

// OutboxHistory reads the newest durable outbox record for a destination. The
// projector uses it to avoid spending a revision when the full WorkSet content
// and validity horizon are already current.
type OutboxHistory interface {
	Latest(context.Context, domainprojection.Target, string) (OutboxRecord, bool, error)
}

// ProjectionActor is one configured CORE-to-WORLD actor mapping. ActorID is
// WORLD's canonical ID; CoreAssignee is the exact CORE Task.assignee value and
// may differ in case. Neither value is normalized or aliased.
type ProjectionActor struct {
	ActorID          string
	ActorInstanceID  string
	ActorTransportID string
	CoreAssignee     string
}

// Projector publishes a full WorkSet snapshot per configured Actor. It is
// state-based: it reads one complete Task/Run view, compares it with the last
// durable outbox record, and only then allocates a new source revision. A
// crash between a Task update and this call is recovered by the next snapshot.
type Projector struct {
	Source           TaskRunSource
	LifePolicySource LifePolicySource
	Outbox           Outbox
	History          OutboxHistory
	Revisions        SourceRevisionStore
	Actors           []ProjectionActor
	WorldID          string
	TimelineID       string
	TimelineEpoch    string
	SourceEpoch      string
	ValidFor         time.Duration
	RefreshBefore    time.Duration
	Now              func() time.Time
}

// SyncResult reports one full projection pass. Enqueued/Skipped count WorkSet
// records only; LifePolicy records are reported separately so a caller cannot
// mistake one projection kind for the other.
type SyncResult struct {
	Actors               int
	Enqueued             int
	Skipped              int
	LifePoliciesEnqueued int
	LifePoliciesSkipped  int
}

// Sync reads and projects every configured Actor. It never mutates Task/Run
// state and never starts or stops CORE work.
func (p *Projector) Sync(ctx context.Context) (SyncResult, error) {
	var result SyncResult
	if p == nil || p.Source == nil || p.Outbox == nil || p.Revisions == nil {
		return result, errors.New("world projection: source, outbox, and revision store are required")
	}
	if ctx == nil {
		return result, errors.New("world projection: context is nil")
	}
	if len(p.Actors) == 0 {
		return result, errors.New("world projection: at least one actor is required")
	}
	if p.WorldID == "" || p.TimelineID == "" || p.TimelineEpoch == "" || p.SourceEpoch == "" {
		return result, errors.New("world projection: destination and source epoch are required")
	}
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	validFor := p.ValidFor
	if validFor <= 0 {
		validFor = DefaultProjectionValidFor
	}
	refreshBefore := p.RefreshBefore
	if refreshBefore <= 0 {
		refreshBefore = DefaultProjectionRefreshBefore
	}
	validUntil := now.Add(validFor)

	result.Actors = len(p.Actors)
	for _, actor := range p.Actors {
		target, err := p.target(actor)
		if err != nil {
			return result, err
		}
		assignee := actor.CoreAssignee
		if assignee == "" {
			assignee = actor.ActorID
		}
		candidate, err := BuildFullWorkSet(ctx, p.Source, BuildWorkSetInput{
			WorldID:        p.WorldID,
			ActorID:        actor.ActorID,
			Assignee:       assignee,
			SourceEpoch:    p.SourceEpoch,
			SourceRevision: 1,
			ValidUntil:     validUntil,
		})
		if err != nil {
			return result, fmt.Errorf("world projection: build %s work set: %w", actor.ActorID, err)
		}
		contentDigest, err := domainprojection.WorkSetContentDigest(candidate)
		if err != nil {
			return result, err
		}

		skip, err := p.isCurrent(ctx, target, contentDigest, now, refreshBefore)
		if err != nil {
			return result, err
		}
		if skip {
			result.Skipped++
		} else {
			revision, err := p.Revisions.NextSourceRevision(ctx, target, domainprojection.KindWorkSet, p.SourceEpoch, now)
			if err != nil {
				return result, fmt.Errorf("world projection: allocate %s revision: %w", actor.ActorID, err)
			}
			workSet, err := domainprojection.NewWorkSet(p.WorldID, actor.ActorID, p.SourceEpoch,
				revision, validUntil, candidate.ActiveWork)
			if err != nil {
				return result, err
			}
			envelope, err := domainprojection.NewEnvelope(target, domainprojection.KindWorkSet, workSet)
			if err != nil {
				return result, err
			}
			raw, err := envelope.Marshal()
			if err != nil {
				return result, err
			}
			record := OutboxRecord{
				ID:             projectionRecordID(target, domainprojection.KindWorkSet, revision),
				Target:         target,
				Kind:           domainprojection.KindWorkSet,
				Envelope:       raw,
				Digest:         envelope.Digest,
				SourceEpoch:    p.SourceEpoch,
				SourceRevision: revision,
				CreatedAt:      now,
			}
			if err := p.Outbox.Enqueue(ctx, record, now); err != nil {
				return result, fmt.Errorf("world projection: enqueue %s revision %d: %w", actor.ActorID, revision, err)
			}
			result.Enqueued++
		}

		if p.LifePolicySource != nil {
			enqueued, err := p.syncLifePolicy(ctx, target, actor, now, validUntil, refreshBefore)
			if err != nil {
				return result, err
			}
			if enqueued {
				result.LifePoliciesEnqueued++
			} else {
				result.LifePoliciesSkipped++
			}
		}
	}
	return result, nil
}

func (p *Projector) syncLifePolicy(ctx context.Context, target domainprojection.Target, actor ProjectionActor, now, validUntil time.Time, refreshBefore time.Duration) (bool, error) {
	snapshot, err := p.LifePolicySource.Snapshot(ctx, actor)
	if err != nil {
		return false, fmt.Errorf("world projection: build %s life policy: %w", actor.ActorID, err)
	}
	candidate, err := domainprojection.NewLifePolicy(p.WorldID, actor.ActorID, p.SourceEpoch, 1, validUntil,
		snapshot.PersonaRevision, snapshot.ConditionProfileRef,
		snapshot.AllowedNonCognitiveActivityIDs, snapshot.ExpiredFallbackActivityIDs)
	if err != nil {
		return false, fmt.Errorf("world projection: validate %s life policy: %w", actor.ActorID, err)
	}
	contentDigest, err := domainprojection.LifePolicyContentDigest(candidate)
	if err != nil {
		return false, err
	}
	skip, err := p.isCurrentLifePolicy(ctx, target, contentDigest, now, refreshBefore)
	if err != nil {
		return false, err
	}
	if skip {
		return false, nil
	}
	revision, err := p.Revisions.NextSourceRevision(ctx, target, domainprojection.KindLifePolicy, p.SourceEpoch, now)
	if err != nil {
		return false, fmt.Errorf("world projection: allocate %s life policy revision: %w", actor.ActorID, err)
	}
	policy, err := domainprojection.NewLifePolicy(p.WorldID, actor.ActorID, p.SourceEpoch, revision, validUntil,
		snapshot.PersonaRevision, snapshot.ConditionProfileRef,
		snapshot.AllowedNonCognitiveActivityIDs, snapshot.ExpiredFallbackActivityIDs)
	if err != nil {
		return false, err
	}
	envelope, err := domainprojection.NewEnvelope(target, domainprojection.KindLifePolicy, policy)
	if err != nil {
		return false, err
	}
	raw, err := envelope.Marshal()
	if err != nil {
		return false, err
	}
	record := OutboxRecord{
		ID:             projectionRecordID(target, domainprojection.KindLifePolicy, revision),
		Target:         target,
		Kind:           domainprojection.KindLifePolicy,
		Envelope:       raw,
		Digest:         envelope.Digest,
		SourceEpoch:    p.SourceEpoch,
		SourceRevision: revision,
		CreatedAt:      now,
	}
	if err := p.Outbox.Enqueue(ctx, record, now); err != nil {
		return false, fmt.Errorf("world projection: enqueue %s life policy revision %d: %w", actor.ActorID, revision, err)
	}
	return true, nil
}

func (p *Projector) target(actor ProjectionActor) (domainprojection.Target, error) {
	if actor.ActorID == "" {
		return domainprojection.Target{}, errors.New("world projection: actor id is required")
	}
	if actor.ActorInstanceID == "" {
		return domainprojection.Target{}, fmt.Errorf("world projection: actor %s instance id is required", actor.ActorID)
	}
	transportID := actor.ActorTransportID
	if transportID == "" {
		transportID = actor.ActorID
	}
	target := domainprojection.Target{
		WorldID:          p.WorldID,
		TimelineID:       p.TimelineID,
		TimelineEpoch:    p.TimelineEpoch,
		ActorID:          actor.ActorID,
		ActorInstanceID:  actor.ActorInstanceID,
		ActorTransportID: transportID,
	}
	if err := target.Validate(); err != nil {
		return domainprojection.Target{}, err
	}
	return target, nil
}

func (p *Projector) isCurrent(ctx context.Context, target domainprojection.Target, contentDigest string, now time.Time, refreshBefore time.Duration) (bool, error) {
	if p.History == nil {
		return false, nil
	}
	latest, ok, err := p.History.Latest(ctx, target, domainprojection.KindWorkSet)
	if err != nil {
		return false, fmt.Errorf("world projection: read %s outbox head: %w", target.ActorID, err)
	}
	if !ok {
		return false, nil
	}
	switch latest.Status {
	case OutboxPending, OutboxInFlight:
		// An exact-content delivery is already queued; allocating another
		// revision would only create avoidable work.
		return latestWorkSetContentDigest(latest) == contentDigest, nil
	case OutboxSent:
		workSet, err := decodeWorkSetRecord(latest)
		if err != nil {
			return false, fmt.Errorf("world projection: decode sent %s record: %w", target.ActorID, err)
		}
		if latest.SourceEpoch != p.SourceEpoch {
			return false, nil
		}
		digest, err := domainprojection.WorkSetContentDigest(workSet)
		if err != nil {
			return false, err
		}
		if digest != contentDigest {
			return false, nil
		}
		return workSet.ValidUntil.After(now.Add(refreshBefore)), nil
	default:
		// failed and superseded snapshots are not current.
		return false, nil
	}
}

func (p *Projector) isCurrentLifePolicy(ctx context.Context, target domainprojection.Target, contentDigest string, now time.Time, refreshBefore time.Duration) (bool, error) {
	if p.History == nil {
		return false, nil
	}
	latest, ok, err := p.History.Latest(ctx, target, domainprojection.KindLifePolicy)
	if err != nil {
		return false, fmt.Errorf("world projection: read %s life policy outbox head: %w", target.ActorID, err)
	}
	if !ok {
		return false, nil
	}
	switch latest.Status {
	case OutboxPending, OutboxInFlight:
		return latestLifePolicyContentDigest(latest) == contentDigest, nil
	case OutboxSent:
		policy, err := decodeLifePolicyRecord(latest)
		if err != nil {
			return false, fmt.Errorf("world projection: decode sent %s life policy: %w", target.ActorID, err)
		}
		if latest.SourceEpoch != p.SourceEpoch {
			return false, nil
		}
		digest, err := domainprojection.LifePolicyContentDigest(policy)
		if err != nil {
			return false, err
		}
		if digest != contentDigest {
			return false, nil
		}
		return policy.ValidUntil.After(now.Add(refreshBefore)), nil
	default:
		return false, nil
	}
}

func latestWorkSetContentDigest(record OutboxRecord) string {
	workSet, err := decodeWorkSetRecord(record)
	if err != nil {
		return ""
	}
	digest, err := domainprojection.WorkSetContentDigest(workSet)
	if err != nil {
		return ""
	}
	return digest
}

func latestLifePolicyContentDigest(record OutboxRecord) string {
	policy, err := decodeLifePolicyRecord(record)
	if err != nil {
		return ""
	}
	digest, err := domainprojection.LifePolicyContentDigest(policy)
	if err != nil {
		return ""
	}
	return digest
}

func decodeWorkSetRecord(record OutboxRecord) (domainprojection.WorkSet, error) {
	if record.Kind != domainprojection.KindWorkSet {
		return domainprojection.WorkSet{}, errors.New("world projection: record is not a work set")
	}
	var wire struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(record.Envelope, &wire); err != nil {
		return domainprojection.WorkSet{}, fmt.Errorf("world projection: envelope is invalid: %w", err)
	}
	var workSet domainprojection.WorkSet
	if err := json.Unmarshal(wire.Payload, &workSet); err != nil {
		return domainprojection.WorkSet{}, fmt.Errorf("world projection: payload is invalid: %w", err)
	}
	if err := workSet.Validate(); err != nil {
		return domainprojection.WorkSet{}, err
	}
	return workSet, nil
}

func decodeLifePolicyRecord(record OutboxRecord) (domainprojection.LifePolicy, error) {
	if record.Kind != domainprojection.KindLifePolicy {
		return domainprojection.LifePolicy{}, errors.New("world projection: record is not a life policy")
	}
	var wire struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(record.Envelope, &wire); err != nil {
		return domainprojection.LifePolicy{}, fmt.Errorf("world projection: envelope is invalid: %w", err)
	}
	var policy domainprojection.LifePolicy
	if err := json.Unmarshal(wire.Payload, &policy); err != nil {
		return domainprojection.LifePolicy{}, fmt.Errorf("world projection: payload is invalid: %w", err)
	}
	if err := policy.Validate(); err != nil {
		return domainprojection.LifePolicy{}, err
	}
	return policy, nil
}

func projectionRecordID(target domainprojection.Target, kind string, revision uint64) string {
	return target.WorldID + "/" + target.TimelineID + "/" + target.TimelineEpoch + "/" +
		target.ActorID + "/" + kind + "/" + strconv.FormatUint(revision, 10)
}
