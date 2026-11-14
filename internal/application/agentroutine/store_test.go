package agentroutine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestStoreSaveUsesActorRevisionCASAndPersistsSchedule(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "routine.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx, "Mio")
	if err != nil || state.Revision != 0 {
		t.Fatalf("initial Load = %+v, %v", state, err)
	}
	sleepAt, wakeAt, err := ScheduleFor("Mio", "2026-11-14")
	if err != nil {
		t.Fatal(err)
	}
	state.ActivatedOn = "2026-11-13"
	state.NextDiaryDate = "2026-11-13"
	state.ScheduleDate = "2026-11-14"
	state.PlannedSleep = sleepAt
	state.PlannedWake = wakeAt
	saved, err := store.Save(ctx, state, 0)
	if err != nil || saved.Revision != 1 {
		t.Fatalf("Save = %+v, %v", saved, err)
	}
	if _, err := store.Save(ctx, state, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Save error = %v, want ErrConflict", err)
	}
	other, err := store.Load(ctx, "Shiro")
	if err != nil || other.Revision != 0 || other.ActorID != "Shiro" {
		t.Fatalf("actor isolation Load = %+v, %v", other, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reloaded, err := store.Load(ctx, "Mio")
	if err != nil || reloaded.Revision != 1 || reloaded.ScheduleDate != "2026-11-14" || !reloaded.PlannedWake.Equal(wakeAt) {
		t.Fatalf("reloaded state = %+v, %v", reloaded, err)
	}
}

func TestOpenCreatesPrivateSQLitePathAndRejectsExistingBroadFileWithoutChangingIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file permission bits do not represent inherited ACLs")
	}
	privateDir := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(privateDir, "routine.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(privateDir)
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0600 || dirInfo.Mode().Perm() != 0700 {
		t.Fatalf("new database permissions = file %04o, directory %04o; want 0600 and 0700", fileInfo.Mode().Perm(), dirInfo.Mode().Perm())
	}

	broadPath := filepath.Join(t.TempDir(), "existing.db")
	if err := os.WriteFile(broadPath, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(broadPath, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(broadPath); !errors.Is(err, ErrInsecurePath) {
		t.Fatalf("broad existing file error = %v, want ErrInsecurePath", err)
	}
	after, err := os.Stat(broadPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != 0644 {
		t.Fatalf("existing file changed during rejection: mode=%04o", after.Mode().Perm())
	}
}

func TestStoreEnsureScheduleIsStableForActorAndDate(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "routine.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := store.EnsureSchedule(ctx, "Mio", "2026-11-14")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.EnsureSchedule(ctx, "Mio", "2026-11-14")
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != second.Revision || !first.PlannedSleep.Equal(second.PlannedSleep) || !first.PlannedWake.Equal(second.PlannedWake) {
		t.Fatalf("same date schedule changed: first=%+v second=%+v", first, second)
	}
	third, err := store.EnsureSchedule(ctx, "Mio", "2026-11-15")
	if err != nil || third.ScheduleDate != "2026-11-15" || third.Revision != second.Revision+1 {
		t.Fatalf("next date schedule = %+v, %v", third, err)
	}
}

func TestStoreIntentTaskLinkChoiceCommitCheckpointAndFinishAreRetrySafe(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "routine.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	intent := Intent{ID: "intent-1", ActorID: "Mio", Date: "2026-11-14", Kind: ActivityMorningChat, ChoiceEligible: true, Participants: []string{"Shiro"}}
	state, err := store.DraftIntent(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DraftIntent(ctx, Intent{ID: "intent-2", ActorID: "Mio", Date: intent.Date, Kind: ActivityHobby}); !errors.Is(err, ErrPendingIntent) {
		t.Fatalf("second pending intent error = %v, want ErrPendingIntent", err)
	}
	state, err = store.LinkTask(ctx, "Mio", intent.ID, "canonical-task-1", state.Revision)
	if err != nil || state.Pending == nil || state.Pending.TaskID != "canonical-task-1" {
		t.Fatalf("LinkTask = %+v, %v", state, err)
	}
	beforeStart, err := store.Load(ctx, "Mio")
	if err != nil || beforeStart.Pending.TaskID != "canonical-task-1" {
		t.Fatalf("TaskID was not persisted before run creation: %+v, %v", beforeStart, err)
	}
	state, err = store.CommitInitiatorSelection(ctx, "Mio", intent.ID, state.Revision, true)
	if err != nil || state.ChoiceIndex != 1 || !state.Pending.ChoiceCommitted {
		t.Fatalf("CommitInitiatorSelection = %+v, %v", state, err)
	}
	retried, err := store.CommitInitiatorSelection(ctx, "Mio", intent.ID, 0, true)
	if err != nil || retried.ChoiceIndex != 1 {
		t.Fatalf("idempotent selection retry = %+v, %v", retried, err)
	}
	if _, err := store.CommitInitiatorSelection(ctx, "Mio", intent.ID, retried.Revision, false); err != nil {
		t.Fatalf("already committed intent retry should be harmless: %v", err)
	}
	state, err = store.Checkpoint(ctx, "Mio", intent.ID, "read", "checkpoint output", retried.Revision)
	if err != nil || state.Pending.Step != "read" || state.Pending.Output != "checkpoint output" {
		t.Fatalf("Checkpoint = %+v, %v", state, err)
	}
	retryAt := jst(2026, time.November, 14, 9, 0)
	state, err = store.FinishIntent(ctx, "Mio", intent.ID, state.Revision, retryAt)
	if err != nil || state.Pending != nil || !state.CooldownUntil.Equal(retryAt) {
		t.Fatalf("FinishIntent = %+v, %v", state, err)
	}
	finishedAgain, err := store.FinishIntent(ctx, "Mio", intent.ID, 0, retryAt)
	if err != nil || finishedAgain.ChoiceIndex != 1 || finishedAgain.Pending != nil {
		t.Fatalf("idempotent FinishIntent retry = %+v, %v", finishedAgain, err)
	}
}

func TestStoreChoiceCommitRequiresBothChoicesAndEligibleIntent(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "routine.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.DraftIntent(ctx, Intent{ID: "fallback", ActorID: "Mio", Date: "2026-11-14", Kind: ActivityHobby})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitInitiatorSelection(ctx, "Mio", "fallback", state.Revision, true); !errors.Is(err, ErrChoiceNotEligible) {
		t.Fatalf("ineligible choice commit error = %v", err)
	}
	state, err = store.FinishIntent(ctx, "Mio", "fallback", state.Revision, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	state, err = store.DraftIntent(ctx, Intent{ID: "choice", ActorID: "Mio", Date: "2026-11-14", Kind: ActivityHobby, ChoiceEligible: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitInitiatorSelection(ctx, "Mio", "choice", state.Revision, false); !errors.Is(err, ErrChoiceNotEligible) {
		t.Fatalf("invalid candidate commit error = %v", err)
	}
	unchanged, err := store.Load(ctx, "Mio")
	if err != nil || unchanged.ChoiceIndex != 0 {
		t.Fatalf("invalid choice consumed position: %+v, %v", unchanged, err)
	}
}

func TestStoreFactsAreActorDayPartitionedAndIdempotentWithoutInventingTime(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "routine.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fact := Activity{ActorID: "Mio", Date: "2026-11-14", Key: "task-1:conversation", Kind: ActivityKind("conversation"), Source: "canonical_task", SourceID: "task-1", Confirmed: true, Summary: "completed conversation"}
	saved, err := store.SaveFact(ctx, fact)
	if err != nil || !saved.At.IsZero() {
		t.Fatalf("SaveFact = %+v, %v; missing time must remain zero", saved, err)
	}
	if _, err := store.SaveFact(ctx, fact); err != nil {
		t.Fatalf("idempotent SaveFact retry: %v", err)
	}
	facts, err := store.ListFacts(ctx, "Mio", "2026-11-14")
	if err != nil || len(facts) != 1 || facts[0].SourceID != "task-1" || !facts[0].Confirmed {
		t.Fatalf("same-day facts = %+v, %v", facts, err)
	}
	otherDay, err := store.ListFacts(ctx, "Mio", "2026-11-13")
	if err != nil || len(otherDay) != 0 {
		t.Fatalf("other-day facts = %+v, %v", otherDay, err)
	}
	otherActor, err := store.ListFacts(ctx, "Shiro", "2026-11-14")
	if err != nil || len(otherActor) != 0 {
		t.Fatalf("other-actor facts = %+v, %v", otherActor, err)
	}
	conflict := fact
	conflict.Summary = "different result under same key"
	if _, err := store.SaveFact(ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting fact retry error = %v, want ErrConflict", err)
	}
	missingSource := fact
	missingSource.Key = "missing-source"
	missingSource.Source = ""
	if _, err := store.SaveFact(ctx, missingSource); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing source error = %v, want ErrInvalid", err)
	}
}

func TestStoreDiaryDraftRevisionsAndReceiptRetries(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "routine.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := store.SaveDiary(ctx, Diary{ActorID: "Mio", Date: "2026-11-14", Body: "body one", Reflection: "reflection one"})
	if err != nil || first.Revision != 1 || first.PostingKey == "" {
		t.Fatalf("first SaveDiary = %+v, %v", first, err)
	}
	retry, err := store.SaveDiary(ctx, Diary{ActorID: "Mio", Date: first.Date, Body: first.Body, Reflection: first.Reflection, Revision: first.Revision})
	if err != nil || retry.Revision != first.Revision || retry.PostingKey != first.PostingKey {
		t.Fatalf("same draft retry = %+v, %v", retry, err)
	}
	ack, err := store.SaveDiary(ctx, Diary{ActorID: "Mio", Date: first.Date, Body: first.Body, Reflection: first.Reflection, Revision: first.Revision, PostingKey: first.PostingKey, Receipt: "board-receipt", ReceiptRevision: "board-r7"})
	if err != nil || ack.Revision != first.Revision || ack.Receipt != "board-receipt" || ack.ReceiptRevision != "board-r7" || ack.PostingKey != first.PostingKey {
		t.Fatalf("receipt save = %+v, %v", ack, err)
	}
	updated, err := store.SaveDiary(ctx, Diary{ActorID: "Mio", Date: first.Date, Body: "body two", Reflection: "reflection two", Revision: 2})
	if err != nil || updated.Revision != 2 || updated.PostingKey == first.PostingKey || updated.Receipt != "" {
		t.Fatalf("new diary revision = %+v, %v", updated, err)
	}
	if _, err := store.SaveDiary(ctx, Diary{ActorID: "Mio", Date: first.Date, Body: "stale content", Reflection: "reflection", Revision: 2}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale diary update error = %v, want ErrConflict", err)
	}
	loaded, err := store.GetDiary(ctx, "Mio", first.Date)
	if err != nil || loaded.Revision != 2 || loaded.Body != "body two" {
		t.Fatalf("GetDiary = %+v, %v", loaded, err)
	}
}
