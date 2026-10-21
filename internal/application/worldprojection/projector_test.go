package worldprojection

import (
	"context"
	"errors"
	"testing"
	"time"

	domainprojection "github.com/Nyukimin/RenCrow_CORE/internal/domain/worldprojection"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type fakeProjectorOutbox struct {
	enqueued     []OutboxRecord
	latest       OutboxRecord
	hasLatest    bool
	latestByKind map[string]OutboxRecord
}

func (f *fakeProjectorOutbox) Enqueue(_ context.Context, record OutboxRecord, _ time.Time) error {
	record.Status = OutboxPending
	f.enqueued = append(f.enqueued, record)
	return nil
}

func (f *fakeProjectorOutbox) Claim(context.Context, time.Time, int) ([]OutboxRecord, error) {
	return nil, nil
}

func (f *fakeProjectorOutbox) Complete(context.Context, string, PublishResult, time.Time) error {
	return nil
}

func (f *fakeProjectorOutbox) Fail(context.Context, string, time.Time, bool, string, time.Time) error {
	return nil
}

func (f *fakeProjectorOutbox) Latest(_ context.Context, _ domainprojection.Target, kind string) (OutboxRecord, bool, error) {
	if record, ok := f.latestByKind[kind]; ok {
		return record, true, nil
	}
	return f.latest, f.hasLatest, nil
}

type fakeRevisions struct {
	next  uint64
	calls int
}

func (f *fakeRevisions) NextSourceRevision(context.Context, domainprojection.Target, string, string, time.Time) (uint64, error) {
	f.calls++
	f.next++
	return f.next, nil
}

func newTestProjector(t *testing.T, source *fakeTaskRunSource) (*Projector, *fakeProjectorOutbox, *fakeRevisions) {
	t.Helper()
	outbox := &fakeProjectorOutbox{}
	revisions := &fakeRevisions{}
	projector := &Projector{
		Source:        source,
		Outbox:        outbox,
		History:       outbox,
		Revisions:     revisions,
		WorldID:       "world-house",
		TimelineID:    "timeline-main",
		TimelineEpoch: "timeline-epoch",
		SourceEpoch:   "core-epoch-1",
		Actors: []ProjectionActor{{
			ActorID: "mio", ActorInstanceID: "instance-mio", ActorTransportID: "mio",
		}},
		ValidFor: time.Hour,
		Now:      func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) },
	}
	return projector, outbox, revisions
}

func TestProjectorEnqueuesFullWorkSetAndSkipsCurrentContent(t *testing.T) {
	taskID := modulecore.NewTaskID()
	source := &fakeTaskRunSource{tasks: []domaintask.Task{activeTask(taskID, domaintask.StatusQueued)}}
	projector, outbox, revisions := newTestProjector(t, source)

	result, err := projector.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if result.Enqueued != 1 || result.Skipped != 0 || len(outbox.enqueued) != 1 || revisions.calls != 1 {
		t.Fatalf("result = %+v, enqueued = %d, revisions = %+v", result, len(outbox.enqueued), revisions)
	}
	record := outbox.enqueued[0]
	if record.SourceRevision != 1 || record.Target.ActorID != "mio" ||
		record.Target.ActorInstanceID != "instance-mio" || record.Kind != domainprojection.KindWorkSet {
		t.Fatalf("record = %+v", record)
	}
	workSet, err := decodeWorkSetRecord(record)
	if err != nil {
		t.Fatalf("decodeWorkSetRecord() error = %v", err)
	}
	if len(workSet.ActiveWork) != 1 || workSet.ActiveWork[0].TaskID != string(taskID) {
		t.Fatalf("work set = %+v", workSet)
	}

	record.Status = OutboxSent
	outbox.latest = record
	outbox.hasLatest = true
	result, err = projector.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync(second) error = %v", err)
	}
	if result.Enqueued != 0 || result.Skipped != 1 || len(outbox.enqueued) != 1 || revisions.calls != 1 {
		t.Fatalf("second result = %+v, enqueued = %d, revisions = %+v", result, len(outbox.enqueued), revisions)
	}
}

func TestProjectorEnqueuesNewRevisionWhenActiveWorkChanges(t *testing.T) {
	firstID := modulecore.NewTaskID()
	source := &fakeTaskRunSource{tasks: []domaintask.Task{activeTask(firstID, domaintask.StatusQueued)}}
	projector, outbox, revisions := newTestProjector(t, source)
	if _, err := projector.Sync(context.Background()); err != nil {
		t.Fatalf("Sync(first) error = %v", err)
	}
	outbox.latest = outbox.enqueued[0]
	outbox.latest.Status = OutboxSent
	outbox.hasLatest = true

	source.tasks = append(source.tasks, activeTask(modulecore.NewTaskID(), domaintask.StatusBlocked))
	result, err := projector.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync(second) error = %v", err)
	}
	if result.Enqueued != 1 || len(outbox.enqueued) != 2 || revisions.calls != 2 {
		t.Fatalf("result = %+v, enqueued = %d, revisions = %+v", result, len(outbox.enqueued), revisions)
	}
	if outbox.enqueued[1].SourceRevision != 2 {
		t.Fatalf("second revision = %d", outbox.enqueued[1].SourceRevision)
	}
	workSet, err := decodeWorkSetRecord(outbox.enqueued[1])
	if err != nil {
		t.Fatalf("decode second record: %v", err)
	}
	if len(workSet.ActiveWork) != 2 {
		t.Fatalf("second active work = %+v", workSet.ActiveWork)
	}
}

func TestProjectorRefreshesValidityWithoutChangingContent(t *testing.T) {
	taskID := modulecore.NewTaskID()
	source := &fakeTaskRunSource{tasks: []domaintask.Task{activeTask(taskID, domaintask.StatusQueued)}}
	projector, outbox, revisions := newTestProjector(t, source)
	now := projector.Now()
	if _, err := projector.Sync(context.Background()); err != nil {
		t.Fatalf("Sync(first) error = %v", err)
	}
	first, err := decodeWorkSetRecord(outbox.enqueued[0])
	if err != nil {
		t.Fatalf("decode first record: %v", err)
	}
	stale, err := domainprojection.NewWorkSet(first.WorldID, first.ActorID, first.SourceEpoch,
		first.SourceRevision, now.Add(time.Minute), first.ActiveWork)
	if err != nil {
		t.Fatalf("NewWorkSet(stale) error = %v", err)
	}
	envelope, err := domainprojection.NewEnvelope(outbox.enqueued[0].Target, domainprojection.KindWorkSet, stale)
	if err != nil {
		t.Fatalf("NewEnvelope(stale) error = %v", err)
	}
	raw, err := envelope.Marshal()
	if err != nil {
		t.Fatalf("Marshal(stale) error = %v", err)
	}
	outbox.latest = outbox.enqueued[0]
	outbox.latest.Envelope = raw
	outbox.latest.Digest = envelope.Digest
	outbox.latest.Status = OutboxSent
	outbox.latest.SourceRevision = stale.SourceRevision
	outbox.hasLatest = true

	result, err := projector.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync(refresh) error = %v", err)
	}
	if result.Enqueued != 1 || len(outbox.enqueued) != 2 || revisions.calls != 2 {
		t.Fatalf("result = %+v, enqueued = %d, revisions = %+v", result, len(outbox.enqueued), revisions)
	}
}

func TestProjectorSkipsPendingDeliveryOfSameContent(t *testing.T) {
	taskID := modulecore.NewTaskID()
	source := &fakeTaskRunSource{tasks: []domaintask.Task{activeTask(taskID, domaintask.StatusQueued)}}
	projector, outbox, revisions := newTestProjector(t, source)
	if _, err := projector.Sync(context.Background()); err != nil {
		t.Fatalf("Sync(first) error = %v", err)
	}
	outbox.latest = outbox.enqueued[0]
	outbox.hasLatest = true
	result, err := projector.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync(second) error = %v", err)
	}
	if result.Skipped != 1 || result.Enqueued != 0 || revisions.calls != 1 {
		t.Fatalf("result = %+v, revisions = %+v", result, revisions)
	}
}

func TestProjectorSeparatesWorldActorFromCoreAssignee(t *testing.T) {
	task := activeTask(modulecore.NewTaskID(), domaintask.StatusQueued)
	task.Assignee = "Mio"
	source := &fakeTaskRunSource{tasks: []domaintask.Task{task}}
	projector, outbox, _ := newTestProjector(t, source)
	projector.Actors[0].CoreAssignee = "Mio"
	if _, err := projector.Sync(context.Background()); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	workSet, err := decodeWorkSetRecord(outbox.enqueued[0])
	if err != nil {
		t.Fatalf("decodeWorkSetRecord() error = %v", err)
	}
	if workSet.ActorID != "mio" || len(workSet.ActiveWork) != 1 {
		t.Fatalf("work set = %+v", workSet)
	}
}

func TestProjectorRejectsIncompleteConfiguration(t *testing.T) {
	source := &fakeTaskRunSource{}
	for name, mutate := range map[string]func(*Projector){
		"no actors":       func(p *Projector) { p.Actors = nil },
		"no instance":     func(p *Projector) { p.Actors[0].ActorInstanceID = "" },
		"no timeline":     func(p *Projector) { p.TimelineID = "" },
		"no source epoch": func(p *Projector) { p.SourceEpoch = "" },
	} {
		t.Run(name, func(t *testing.T) {
			projector, _, _ := newTestProjector(t, source)
			mutate(projector)
			if _, err := projector.Sync(context.Background()); err == nil {
				t.Fatal("Sync() error = nil")
			}
		})
	}
}

func TestProjectorValidatesLatestEnvelope(t *testing.T) {
	source := &fakeTaskRunSource{}
	projector, outbox, _ := newTestProjector(t, source)
	if _, err := projector.Sync(context.Background()); err != nil {
		t.Fatalf("Sync(first) error = %v", err)
	}
	outbox.latest = outbox.enqueued[0]
	outbox.latest.Status = OutboxSent
	outbox.latest.Envelope = []byte(`{"payload":{}}`)
	outbox.hasLatest = true
	if _, err := projector.Sync(context.Background()); !errors.Is(err, domainprojection.ErrInvalidProjection) {
		t.Fatalf("Sync() error = %v, want ErrInvalidProjection", err)
	}
}

func TestProjectorEnqueuesDerivedLifePolicyAndSkipsCurrentContent(t *testing.T) {
	source := &fakeTaskRunSource{}
	projector, outbox, revisions := newTestProjector(t, source)
	projector.LifePolicySource = &fakeLifePolicySource{snapshot: LifePolicySnapshot{
		PersonaRevision:                "persona-sha256:one",
		ConditionProfileRef:            "agent-profile-sha256:one",
		AllowedNonCognitiveActivityIDs: []string{"idle", "rest", "walk"},
		ExpiredFallbackActivityIDs:     []string{"idle", "rest"},
	}}
	result, err := projector.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync(first) error = %v", err)
	}
	if result.Enqueued != 1 || result.LifePoliciesEnqueued != 1 || len(outbox.enqueued) != 2 || revisions.calls != 2 {
		t.Fatalf("result = %+v, enqueued = %d, revisions = %+v", result, len(outbox.enqueued), revisions)
	}
	policyRecord := findEnqueued(t, outbox, domainprojection.KindLifePolicy)
	policy, err := decodeLifePolicyRecord(policyRecord)
	if err != nil {
		t.Fatalf("decodeLifePolicyRecord() error = %v", err)
	}
	if policy.PersonaRevision != "persona-sha256:one" ||
		len(policy.AllowedNonCognitiveActivityIDs) != 3 ||
		policy.ActorID != "mio" {
		t.Fatalf("policy = %+v", policy)
	}

	outbox.latestByKind = map[string]OutboxRecord{
		domainprojection.KindWorkSet:    findEnqueued(t, outbox, domainprojection.KindWorkSet),
		domainprojection.KindLifePolicy: policyRecord,
	}
	for kind, record := range outbox.latestByKind {
		record.Status = OutboxSent
		outbox.latestByKind[kind] = record
	}
	result, err = projector.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync(second) error = %v", err)
	}
	if result.Enqueued != 0 || result.Skipped != 1 || result.LifePoliciesEnqueued != 0 ||
		result.LifePoliciesSkipped != 1 || revisions.calls != 2 {
		t.Fatalf("second result = %+v, revisions = %+v", result, revisions)
	}
}

func TestProjectorEnqueuesNewLifePolicyRevisionWhenPermissionsChange(t *testing.T) {
	source := &fakeTaskRunSource{}
	projector, outbox, revisions := newTestProjector(t, source)
	lifeSource := &fakeLifePolicySource{snapshot: LifePolicySnapshot{
		PersonaRevision:                "persona-sha256:one",
		ConditionProfileRef:            "agent-profile-sha256:one",
		AllowedNonCognitiveActivityIDs: []string{"idle", "rest", "walk"},
		ExpiredFallbackActivityIDs:     []string{"idle", "rest"},
	}}
	projector.LifePolicySource = lifeSource
	if _, err := projector.Sync(context.Background()); err != nil {
		t.Fatalf("Sync(first) error = %v", err)
	}
	first := findEnqueued(t, outbox, domainprojection.KindLifePolicy)
	first.Status = OutboxSent
	outbox.latestByKind = map[string]OutboxRecord{
		domainprojection.KindWorkSet:    sentRecord(findEnqueued(t, outbox, domainprojection.KindWorkSet)),
		domainprojection.KindLifePolicy: first,
	}

	lifeSource.snapshot.AllowedNonCognitiveActivityIDs = []string{"idle", "rest", "sit", "walk"}
	result, err := projector.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync(second) error = %v", err)
	}
	if result.Enqueued != 0 || result.Skipped != 1 || result.LifePoliciesEnqueued != 1 || revisions.calls != 3 {
		t.Fatalf("result = %+v, revisions = %+v", result, revisions)
	}
	second := findEnqueued(t, outbox, domainprojection.KindLifePolicy, 2)
	policy, err := decodeLifePolicyRecord(second)
	if err != nil {
		t.Fatalf("decodeLifePolicyRecord(second) error = %v", err)
	}
	if len(policy.AllowedNonCognitiveActivityIDs) != 4 {
		t.Fatalf("second policy = %+v", policy)
	}
}

type fakeLifePolicySource struct {
	snapshot LifePolicySnapshot
	err      error
}

func (f *fakeLifePolicySource) Snapshot(context.Context, ProjectionActor) (LifePolicySnapshot, error) {
	return f.snapshot, f.err
}

func findEnqueued(t *testing.T, outbox *fakeProjectorOutbox, kind string, occurrence ...int) OutboxRecord {
	t.Helper()
	want := 1
	if len(occurrence) > 0 {
		want = occurrence[0]
	}
	seen := 0
	for _, record := range outbox.enqueued {
		if record.Kind != kind {
			continue
		}
		seen++
		if seen == want {
			return record
		}
	}
	t.Fatalf("no %s record #%d in %d enqueued records", kind, want, len(outbox.enqueued))
	return OutboxRecord{}
}

func sentRecord(record OutboxRecord) OutboxRecord {
	record.Status = OutboxSent
	return record
}
