package workstream

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	tool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMeetingOccurrenceTwoHandlesCreateOnceCASAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workstream.db")
	first, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ctx, access, _, key, initial := meetingStoreFixture(t)

	var wg sync.WaitGroup
	results := make(chan MeetingOccurrenceRecord, 2)
	errs := make(chan error, 2)
	for _, store := range []*SQLiteStore{first, second} {
		wg.Add(1)
		go func(store *SQLiteStore) {
			defer wg.Done()
			result, err := store.LoadOrCreateMeetingOccurrence(ctx, access, key, initial)
			results <- result
			errs <- err
		}(store)
	}
	wg.Wait()
	close(results)
	close(errs)
	created := 0
	var current MeetingOccurrenceRecord
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for result := range results {
		if result.Created {
			created++
		}
		current = result
	}
	if created != 1 {
		t.Fatalf("created occurrences = %d, want 1", created)
	}
	var committedPayload string
	if err := first.db.QueryRow(`SELECT payload FROM artifact WHERE json_extract(payload, '$.artifact_type') = ?`, taskMeetingOccurrenceArtifactType).Scan(&committedPayload); err != nil {
		t.Fatal(err)
	}
	var committedArtifact domainworkstream.Artifact
	if err := json.Unmarshal([]byte(committedPayload), &committedArtifact); err != nil {
		t.Fatal(err)
	}
	if err := modulecore.WorkstreamID(committedArtifact.WorkstreamID).Validate(); err != nil {
		t.Fatalf("artifact WorkstreamID is not canonical: %v", err)
	}
	if committedArtifact.WorkstreamID == string(key.ScheduleID) || modulecore.WorkstreamID(string(key.ScheduleID)).Validate() == nil {
		t.Fatal("schedule identity was incorrectly used as native WorkstreamID")
	}
	var ownerWorkstreamPayload string
	if err := first.db.QueryRow(`SELECT payload FROM workstream WHERE workstream_id = ?`, committedArtifact.WorkstreamID).Scan(&ownerWorkstreamPayload); err != nil {
		t.Fatal(err)
	}
	var ownerWorkstream domainworkstream.Workstream
	if err := json.Unmarshal([]byte(ownerWorkstreamPayload), &ownerWorkstream); err != nil {
		t.Fatal(err)
	}
	if ownerWorkstream.TaskCenterScope == nil || *ownerWorkstream.TaskCenterScope != (domainworkstream.TaskCenterWorkstreamScope{DataScope: domainworkstream.TaskPlanDataScopeUser, UserID: access.OwnerUserID}) {
		t.Fatalf("meeting artifact did not reuse the authenticated owner's Task Center Workstream: %+v", ownerWorkstream.TaskCenterScope)
	}
	otherAccess := access
	otherAccess.OwnerUserID = "owner-2"
	otherAccess.AuthorityEvidence = modulecore.NewEvidenceID()
	otherCtx := meetingStoreContext(t, otherAccess)
	otherTx, err := first.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	otherWorkstreamID, err := findOrCreateTaskCenterWorkstream(ctx, otherTx, domainworkstream.TaskPlanDataScopeUser, otherAccess.OwnerUserID, initial.ScheduledAt)
	if err != nil {
		_ = otherTx.Rollback()
		t.Fatal(err)
	}
	if err := otherTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := first.LoadOrCreateMeetingOccurrence(otherCtx, otherAccess, key, initial); !errors.Is(err, domainmeeting.ErrConflict) {
		t.Fatalf("same occurrence requested from another owner error = %v, want ErrConflict", err)
	}
	corruptID := string(modulecore.NewArtifactID())
	if _, err := first.db.Exec(`INSERT INTO artifact (artifact_id, workstream_id, created_at, payload) VALUES (?, ?, CURRENT_TIMESTAMP, ?)`, corruptID, otherWorkstreamID, `{"artifact_type":"task_meeting_occurrence",`); err != nil {
		t.Fatal(err)
	}
	replayed, err := first.LoadOrCreateMeetingOccurrence(ctx, access, key, initial)
	if err != nil || replayed.Created {
		t.Fatalf("corrupt foreign row affected scoped replay: created=%v err=%v", replayed.Created, err)
	}

	before := current.Meeting
	next, err := before.BeginGathering(before.Guard(), before.ScheduledAt)
	if err != nil {
		t.Fatal(err)
	}
	for name, tampered := range map[string]domainmeeting.Meeting{
		"rooms":             func() domainmeeting.Meeting { value := next; value.Rooms.Dining = "changed:dining"; return value }(),
		"schedule revision": func() domainmeeting.Meeting { value := next; value.ScheduleRevision++; return value }(),
		"world binding": func() domainmeeting.Meeting {
			value := next
			value.WorldActorBindings = append([]domainmeeting.WorldActorBinding(nil), next.WorldActorBindings...)
			value.WorldActorBindings[0].WorldActorID = "world-rebound"
			return value
		}(),
	} {
		if _, err := first.SaveMeetingOccurrence(ctx, access, key, before.Guard(), current.PayloadHash, tampered); !errors.Is(err, domainmeeting.ErrConflict) {
			t.Errorf("tampered %s CAS error = %v, want ErrConflict", name, err)
		}
	}
	winner, err := first.SaveMeetingOccurrence(ctx, access, key, before.Guard(), current.PayloadHash, next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.SaveMeetingOccurrence(ctx, access, key, before.Guard(), current.PayloadHash, next); !errors.Is(err, domainmeeting.ErrStale) {
		t.Fatalf("second stale CAS error = %v, want ErrStale", err)
	}

	changed := access
	changed.CoordinatorActorID = "shiro"
	changedCtx := meetingStoreContext(t, changed)
	if _, err := first.LoadOrCreateMeetingOccurrence(changedCtx, changed, key, initial); !errors.Is(err, domainmeeting.ErrConflict) {
		t.Fatalf("changed current authority error = %v, want ErrConflict", err)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reloaded, err := reopened.LoadOrCreateMeetingOccurrence(ctx, access, key, initial)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Created || reloaded.Meeting.Guard() != winner.Meeting.Guard() || reloaded.Meeting.Status != domainmeeting.StatusGathering {
		t.Fatalf("restart did not retain winner: created=%v status=%q guard=%+v", reloaded.Created, reloaded.Meeting.Status, reloaded.Meeting.Guard())
	}
}

func TestFindMeetingOccurrenceIsReadOnlyAndOwnerScoped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meeting-find.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, access, _, key, initial := meetingStoreFixture(t)
	var beforeWorkstreams, beforeArtifacts int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM workstream`).Scan(&beforeWorkstreams); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM artifact`).Scan(&beforeArtifacts); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.FindMeetingOccurrence(ctx, access, key); err != nil || found {
		t.Fatalf("missing occurrence lookup: found=%t err=%v", found, err)
	}
	var afterWorkstreams, afterArtifacts int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM workstream`).Scan(&afterWorkstreams); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM artifact`).Scan(&afterArtifacts); err != nil {
		t.Fatal(err)
	}
	if afterWorkstreams != beforeWorkstreams || afterArtifacts != beforeArtifacts {
		t.Fatalf("read lookup mutated storage: workstreams %d->%d artifacts %d->%d", beforeWorkstreams, afterWorkstreams, beforeArtifacts, afterArtifacts)
	}
	created, err := store.LoadOrCreateMeetingOccurrence(ctx, access, key, initial)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := store.FindMeetingOccurrence(ctx, access, key)
	if err != nil || !found || got.Guard() != created.Meeting.Guard() || got.Key != key {
		t.Fatalf("exact owner occurrence lookup: found=%t meeting=%+v err=%v", found, got, err)
	}
	otherAccess := access
	otherAccess.OwnerUserID = "owner-other"
	otherAccess.AuthorityEvidence = modulecore.NewEvidenceID()
	otherCtx := meetingStoreContext(t, otherAccess)
	if _, found, err := store.FindMeetingOccurrence(otherCtx, otherAccess, key); err != nil || found {
		t.Fatalf("foreign owner's lookup: found=%t err=%v", found, err)
	}
}

func TestMeetingOccurrenceSQLiteRoundTripPreservesCanonicalTimingAndNoopReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meeting-roundtrip.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, access, _, key, initial := meetingStoreFixture(t)
	created, err := store.LoadOrCreateMeetingOccurrence(ctx, access, key, initial)
	if err != nil {
		t.Fatal(err)
	}
	before := created.Meeting
	next, err := before.BeginGathering(before.Guard(), before.ScheduledAt)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.SaveMeetingOccurrence(ctx, access, key, before.Guard(), created.PayloadHash, next)
	if err != nil {
		t.Fatalf("SaveMeetingOccurrence after SQLite round trip: %v", err)
	}
	loaded, err := store.LoadOrCreateMeetingOccurrence(ctx, access, key, initial)
	if err != nil {
		t.Fatal(err)
	}
	threadID := modulecore.NewThreadID()
	observedAt := loaded.Meeting.ScheduledAt.Add(time.Minute)
	snapshot := domainmeeting.WorldSnapshotEvidence{WorldID: "world-home", TimelineID: "timeline-main", TimelineEpoch: "epoch-1",
		StateSeq: 11, StateHash: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", OwnerProofID: modulecore.NewEvidenceID()}
	scope := domainmeeting.WorldScopeEvidence{EvidenceID: modulecore.NewEvidenceID(), Key: key, Generation: loaded.Meeting.Generation, Epoch: loaded.Meeting.Epoch,
		Snapshot: snapshot, ThreadID: threadID, LivingRoomRef: loaded.Meeting.Rooms.Living, DiningRoomRef: loaded.Meeting.Rooms.Dining,
		Audience: append([]string(nil), loaded.Meeting.Participants...), ObservedAt: observedAt, ValidUntil: loaded.Meeting.DeadlineAt.Add(-time.Second)}
	withScope, err := loaded.Meeting.ConfirmWorldScope(loaded.Meeting.Guard(), scope)
	if err != nil {
		t.Fatal(err)
	}
	storedScope, err := store.SaveMeetingOccurrence(ctx, access, key, loaded.Meeting.Guard(), loaded.PayloadHash, withScope)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := storedScope.Meeting.ConfirmWorldScope(storedScope.Meeting.Guard(), scope)
	if err != nil {
		t.Fatalf("exact persisted World receipt replay: %v", err)
	}
	if !domainmeeting.SamePersistedSnapshot(storedScope.Meeting, replay) || storedScope.Meeting.Guard() != replay.Guard() {
		t.Fatal("exact receipt replay changed the canonical persisted snapshot or guard")
	}
	if stored.PayloadHash == "" || storedScope.PayloadHash == "" {
		t.Fatal("SQLite round-trip save did not return persisted payload hashes")
	}
}

func TestMeetingOccurrenceRejectsUntrustedExecutionScope(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "workstream.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, access, _, key, initial := meetingStoreFixture(t)
	if _, err := store.LoadOrCreateMeetingOccurrence(context.Background(), access, key, initial); !errors.Is(err, domainmeeting.ErrConflict) {
		t.Fatalf("untrusted context error = %v, want ErrConflict", err)
	}
}

func TestMeetingOccurrenceWorldRegistrationProjectionIsImmutable(t *testing.T) {
	_, _, _, _, base := meetingStoreFixture(t)
	base.WorldBaseURL = "https://world.example.test"
	base.WorldCredentialEnv = "REN_WORLD_TOKEN"
	base.WorldID = "world-home"
	base.TimelineID = "timeline-main"
	base.TimelineEpoch = "epoch-7"
	if err := base.Validate(); err != nil {
		t.Fatalf("valid frozen World registration projection: %v", err)
	}
	if err := validateMeetingCASProjection(base, base); err != nil {
		t.Fatalf("unchanged frozen World projection: %v", err)
	}
	mutations := map[string]func(*domainmeeting.Meeting){
		"base URL":       func(value *domainmeeting.Meeting) { value.WorldBaseURL = "https://other.example.test" },
		"credential env": func(value *domainmeeting.Meeting) { value.WorldCredentialEnv = "OTHER_WORLD_TOKEN" },
		"World ID":       func(value *domainmeeting.Meeting) { value.WorldID = "world-other" },
		"timeline ID":    func(value *domainmeeting.Meeting) { value.TimelineID = "timeline-other" },
		"timeline epoch": func(value *domainmeeting.Meeting) { value.TimelineEpoch = "epoch-8" },
	}
	for name, mutate := range mutations {
		next := base
		mutate(&next)
		if err := validateMeetingCASProjection(base, next); !errors.Is(err, domainmeeting.ErrConflict) {
			t.Errorf("mutated %s projection error = %v, want conflict", name, err)
		}
	}
}

func meetingStoreFixture(t *testing.T) (context.Context, domainmeeting.StoreAccess, domainmeeting.ConfigSnapshot, domainmeeting.OccurrenceKey, domainmeeting.Meeting) {
	t.Helper()
	actors := []string{"mio", "shiro", "aka", "ao", "kin", "gin", "midori", "kuro"}
	config := domainmeeting.ConfigSnapshot{
		ScheduleID: modulecore.NewScheduleID(), Revision: 1,
		Timing:           modulecore.TaskTiming{Timezone: domainmeeting.TimezoneJST, Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceDaily, Times: []string{"08:00", "12:00", "17:00"}}},
		ConfiguredActors: actors, WorldActorBindings: meetingTestWorldBindings(actors), RegisteredActors: append([]string(nil), actors...),
		RegistrationEvidenceID: modulecore.NewEvidenceID(), Rooms: domainmeeting.RoomRefs{Living: "living", Dining: "dining"},
		MeetingDeadlineAfterSecs: 3600,
	}
	key := domainmeeting.OccurrenceKey{ScheduleID: config.ScheduleID, LocalDate: "2026-11-17", Slot: "08:00", Timezone: domainmeeting.TimezoneJST}
	meeting, err := domainmeeting.NewOccurrence(config, key)
	if err != nil {
		t.Fatal(err)
	}
	access := domainmeeting.StoreAccess{CoordinatorActorID: "mio", OwnerUserID: "owner-1", Audience: actors, AuthorityEvidence: modulecore.NewEvidenceID()}
	return meetingStoreContext(t, access), access, config, key, meeting
}

func meetingTestWorldBindings(actors []string) []domainmeeting.WorldActorBinding {
	out := make([]domainmeeting.WorldActorBinding, 0, len(actors))
	for _, actor := range actors {
		out = append(out, domainmeeting.WorldActorBinding{CoreActorID: actor, WorldActorID: "world-" + actor})
	}
	return out
}

func meetingStoreContext(t *testing.T, access domainmeeting.StoreAccess) context.Context {
	t.Helper()
	scope, err := tool.NewToolExecutionScope("meeting-test", tool.ActorKindAgent, access.CoordinatorActorID, access.OwnerUserID, []string{tool.DataScopeUser}, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	return tool.WithToolExecutionScope(context.Background(), scope)
}
