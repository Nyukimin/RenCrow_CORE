package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var _ modulecore.SequencedEventAppender = (*SQLiteStore)(nil)

func TestAppendAndReadRootChildParallelJoin(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	traceID := modulecore.NewTraceID()
	root := eventFixture(traceID, "task.created", "orchestrator")
	left := eventFixture(traceID, "attempt.started", "superagent")
	left.CausationEventID = root.EventID
	right := eventFixture(traceID, "attempt.started", "ai_workflow")
	right.CausationEventID = root.EventID
	join := eventFixture(traceID, "verification.completed", "orchestrator")
	join.CausationEventID = left.EventID
	join.DependencyEventIDs = []modulecore.EventID{right.EventID}

	events := []modulecore.EventEnvelope{root, left, right, join}
	for index, event := range events {
		stored, err := store.AppendSequenced(ctx, event)
		if err != nil {
			t.Fatalf("AppendSequenced(%q) error = %v", event.EventID, err)
		}
		events[index] = stored
	}

	gotJoin, found, err := store.GetByID(ctx, join.EventID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !found {
		t.Fatal("GetByID() found = false, want true")
	}
	if !reflect.DeepEqual(gotJoin, events[3]) {
		t.Fatalf("GetByID() = %#v, want %#v", gotJoin, events[3])
	}

	got, err := store.ListByComponent(ctx, "orchestrator", 10)
	if err != nil {
		t.Fatalf("ListByComponent() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListByComponent() length = %d, want 2", len(got))
	}
	if got[0].EventID != join.EventID || got[1].EventID != root.EventID {
		t.Fatalf("ListByComponent() IDs = %q, %q, want %q, %q", got[0].EventID, got[1].EventID, join.EventID, root.EventID)
	}
}

func TestNewSQLiteStoreRejectsUnconfiguredPath(t *testing.T) {
	if _, err := NewSQLiteStore(""); err == nil {
		t.Fatal("NewSQLiteStore(\"\") error = nil")
	}
}

func TestNewSQLiteStoreAllowsOnlyOneLiveWriterPerDatabasePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "会話-event-store.sqlite")
	first, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(first): %v", err)
	}
	if second, err := NewSQLiteStore(path); err == nil {
		_ = second.Close()
		t.Fatal("NewSQLiteStore(second) succeeded for a path with a live writer")
	}

	stored, err := first.AppendSequenced(context.Background(), eventFixture(modulecore.NewTraceID(), "writer.locked", "dci"))
	if err != nil {
		t.Fatalf("AppendSequenced after rejected second writer: %v", err)
	}
	if stored.EventSeq != 1 {
		t.Fatalf("EventSeq = %d, want 1", stored.EventSeq)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(first): %v", err)
	}

	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore after Close: %v", err)
	}
	defer reopened.Close()
	next, err := reopened.AppendSequenced(context.Background(), eventFixture(stored.TraceID, "writer.reopened", "dci"))
	if err != nil {
		t.Fatalf("AppendSequenced after reopen: %v", err)
	}
	if next.EventSeq != 2 {
		t.Fatalf("reopened EventSeq = %d, want 2", next.EventSeq)
	}
}

func TestNewSQLiteStoreRejectsEquivalentSQLiteURIWhileWriterIsOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "会話 uri.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(path): %v", err)
	}
	defer store.Close()

	uri := "file:" + filepath.ToSlash(path) + "?mode=rwc"
	second, err := NewSQLiteStore(uri)
	if err == nil {
		_ = second.Close()
		t.Fatalf("NewSQLiteStore(%q) succeeded while its file path already has a live writer", uri)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd(): %v", err)
	}
	relativePath, err := filepath.Rel(workingDirectory, path)
	if err != nil {
		t.Fatalf("filepath.Rel(): %v", err)
	}
	if second, err := NewSQLiteStore(relativePath); err == nil {
		_ = second.Close()
		t.Fatalf("NewSQLiteStore(%q) succeeded while its absolute path already has a live writer", relativePath)
	}
	escapedURI := "file:" + strings.ReplaceAll(filepath.ToSlash(relativePath), " ", "%20") + "?mode=rwc"
	if second, err := NewSQLiteStore(escapedURI); err == nil {
		_ = second.Close()
		t.Fatalf("NewSQLiteStore(%q) succeeded while its decoded file path already has a live writer", escapedURI)
	}
}

func TestNewSQLiteStoreRejectsSymlinkAliasWhileWriterIsOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event-store.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	defer store.Close()
	alias := filepath.Join(filepath.Dir(path), "event-store-alias.sqlite")
	if err := os.Symlink(path, alias); err != nil {
		t.Skipf("filesystem does not support symlinks: %v", err)
	}
	if second, err := NewSQLiteStore(alias); err == nil {
		_ = second.Close()
		t.Fatal("NewSQLiteStore(symlink alias) succeeded while the resolved database path already has a live writer")
	}
	danglingTarget := filepath.Join(filepath.Dir(path), "not-created.sqlite")
	danglingAlias := filepath.Join(filepath.Dir(path), "dangling-event-store.sqlite")
	if err := os.Symlink(danglingTarget, danglingAlias); err != nil {
		t.Skipf("filesystem does not support dangling symlinks: %v", err)
	}
	if dangling, err := NewSQLiteStore(danglingAlias); err == nil {
		_ = dangling.Close()
		t.Fatal("NewSQLiteStore(dangling symlink) succeeded despite ambiguous database identity")
	}
}

func TestEventStoreSiblingLockIsHeldUntilClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event-store-file-lock.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	lockPath := path + ".writer.lock"
	if _, err := acquireEventStoreFileLock(lockPath); !errors.Is(err, errEventStoreWriterBusy) {
		t.Fatalf("acquireEventStoreFileLock while store is open: %v, want busy", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	lock, err := acquireEventStoreFileLock(lockPath)
	if err != nil {
		t.Fatalf("acquireEventStoreFileLock after Close: %v", err)
	}
	if err := releaseEventStoreFileLock(lock); err != nil {
		t.Fatalf("releaseEventStoreFileLock(): %v", err)
	}
}

func TestEventStorePathLockKeysHandleURIAndMemoryPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "会話-event-store.sqlite")
	plainKey, err := eventStorePathLockKey(path)
	if err != nil {
		t.Fatalf("eventStorePathLockKey(path): %v", err)
	}
	uriKey, err := eventStorePathLockKey("file:" + filepath.ToSlash(path) + "?mode=rwc")
	if err != nil {
		t.Fatalf("eventStorePathLockKey(file URI): %v", err)
	}
	if uriKey != plainKey {
		t.Fatalf("SQLite URI key = %q, plain path key = %q; want same identity", uriKey, plainKey)
	}
	privateFirst, err := eventStorePathLockKey(":memory:")
	if err != nil {
		t.Fatalf("eventStorePathLockKey(:memory:): %v", err)
	}
	privateSecond, err := eventStorePathLockKey(":memory:")
	if err != nil {
		t.Fatalf("eventStorePathLockKey(second :memory:): %v", err)
	}
	if privateFirst == privateSecond {
		t.Fatal(":memory: paths share a writer identity despite SQLite creating a private database per connection")
	}
	sharedFirst, err := eventStorePathLockKey("file:shared_events?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("eventStorePathLockKey(shared memory): %v", err)
	}
	sharedSecond, err := eventStorePathLockKey("file:shared_events?cache=shared&mode=memory")
	if err != nil {
		t.Fatalf("eventStorePathLockKey(reordered shared memory): %v", err)
	}
	if sharedFirst != sharedSecond {
		t.Fatalf("shared-memory keys differ for reordered URI query: %q vs %q", sharedFirst, sharedSecond)
	}
}

func TestNewSQLiteStoreRejectsHardlinkedDatabasePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event-store.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	alias := filepath.Join(filepath.Dir(path), "event-store-hardlink.sqlite")
	if err := os.Link(path, alias); err != nil {
		t.Skipf("filesystem does not support hard links: %v", err)
	}
	if _, err := NewSQLiteStore(path); err == nil || !strings.Contains(strings.ToLower(err.Error()), "hard-link") {
		t.Fatalf("NewSQLiteStore(hardlinked file) error = %v, want ambiguous hard-link rejection", err)
	}
}

func TestNewSQLiteStoreHoldsPathLockUntilCloseDrainsAndReleasesAfterConstructorFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event-store-lock-lifecycle.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx(): %v", err)
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- store.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before the active transaction drained: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if second, err := NewSQLiteStore(path); err == nil {
		_ = second.Close()
		t.Fatal("NewSQLiteStore succeeded while Close was still draining the old writer")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback(): %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close(): %v", err)
	}

	failedPath := filepath.Join(t.TempDir(), "event-store-constructor-failure.sqlite")
	legacy, err := sql.Open("sqlite", failedPath)
	if err != nil {
		t.Fatalf("sql.Open(legacy): %v", err)
	}
	if _, err := legacy.Exec(`CREATE TABLE event_envelope (event_id TEXT PRIMARY KEY NOT NULL)`); err != nil {
		_ = legacy.Close()
		t.Fatalf("create incompatible schema: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("Close(legacy): %v", err)
	}
	if _, err := NewSQLiteStore(failedPath); err == nil || !strings.Contains(err.Error(), "Step09 migration required") {
		t.Fatalf("NewSQLiteStore(incompatible schema) error = %v, want migration-required error", err)
	}
	legacy, err = sql.Open("sqlite", failedPath)
	if err != nil {
		t.Fatalf("sql.Open(repair): %v", err)
	}
	if _, err := legacy.Exec(`DROP TABLE event_envelope`); err != nil {
		_ = legacy.Close()
		t.Fatalf("drop incompatible schema: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("Close(repair): %v", err)
	}
	recovered, err := NewSQLiteStore(failedPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore after constructor failure: %v", err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatalf("Close(recovered): %v", err)
	}
}

func TestEventSeqIsSharedAcrossConcurrentAppendAndLiveAcceptanceAndSeedsAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event-store-mixed-sequences.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	const writesPerPath = 12
	sequences := make(chan modulecore.EventSeq, writesPerPath*2)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for index := 0; index < writesPerPath; index++ {
		index := index
		workers.Add(2)
		go func() {
			defer workers.Done()
			<-start
			stored, err := store.AppendSequenced(context.Background(), eventFixture(modulecore.NewTraceID(), fmt.Sprintf("mixed.append.%d", index), "dci"))
			if err != nil {
				t.Errorf("AppendSequenced(%d): %v", index, err)
				return
			}
			sequences <- stored.EventSeq
		}()
		go func() {
			defer workers.Done()
			<-start
			sessionID, turnID := modulecore.NewSessionID(), modulecore.NewTurnID()
			event := liveInputEvent(sessionID, turnID, fmt.Sprintf("mixed live %d", index))
			accepted, err := store.AcceptTurn(context.Background(), AcceptInput{
				OperationID: fmt.Sprintf("mixed-live-op-%d", index), OwnerID: "user-1", RuntimeVersion: "v2",
				ClientSubmissionID: fmt.Sprintf("mixed-live-submission-%d", index), ActorID: "user-1", TriggerKind: "user_message",
				GenerationEpoch: 1, CreateNewSession: true, InputEvent: event,
				InputMessage: MessageIndex{MessageID: event.MessageID, EventID: event.EventID, SessionID: sessionID, TurnID: turnID,
					FromActor: "user-1", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public"},
			})
			if err != nil {
				t.Errorf("AcceptTurn(%d): %v", index, err)
				return
			}
			sequences <- accepted.AdmissionSeq
		}()
	}
	close(start)
	workers.Wait()
	close(sequences)
	got := make([]int, 0, writesPerPath*2)
	for sequence := range sequences {
		got = append(got, int(sequence))
	}
	if len(got) != writesPerPath*2 {
		t.Fatalf("successful write count = %d, want %d", len(got), writesPerPath*2)
	}
	sort.Ints(got)
	for index, sequence := range got {
		if sequence != index+1 {
			t.Fatalf("sorted mixed EventSeq[%d] = %d, want %d", index, sequence, index+1)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(reopen): %v", err)
	}
	defer reopened.Close()
	next, err := reopened.AppendSequenced(context.Background(), eventFixture(modulecore.NewTraceID(), "mixed.after-restart", "dci"))
	if err != nil {
		t.Fatalf("AppendSequenced(after restart): %v", err)
	}
	if next.EventSeq != writesPerPath*2+1 {
		t.Fatalf("after-restart EventSeq = %d, want %d", next.EventSeq, writesPerPath*2+1)
	}
}

func TestSQLiteStoreConfiguresWALPragmasForEveryConnection(t *testing.T) {
	store := newTestStore(t)
	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}
	store.db.SetMaxIdleConns(0)
	for connection := 0; connection < 2; connection++ {
		var walAutoCheckpoint int
		if err := store.db.QueryRow(`PRAGMA wal_autocheckpoint`).Scan(&walAutoCheckpoint); err != nil {
			t.Fatalf("connection %d wal_autocheckpoint: %v", connection, err)
		}
		if walAutoCheckpoint != 0 {
			t.Fatalf("connection %d wal_autocheckpoint = %d, want 0 to keep checkpoints off the append path", connection, walAutoCheckpoint)
		}
		var journalMode string
		if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
			t.Fatalf("connection %d journal_mode: %v", connection, err)
		}
		if !strings.EqualFold(journalMode, "wal") {
			t.Fatalf("connection %d journal_mode = %q, want wal", connection, journalMode)
		}
		var synchronous int
		if err := store.db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
			t.Fatalf("connection %d synchronous: %v", connection, err)
		}
		if synchronous != 1 {
			t.Fatalf("connection %d synchronous = %d, want NORMAL (1)", connection, synchronous)
		}
		var foreignKeys int
		if err := store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
			t.Fatalf("connection %d foreign_keys: %v", connection, err)
		}
		if foreignKeys != 1 {
			t.Fatalf("connection %d foreign_keys = %d, want 1", connection, foreignKeys)
		}
	}
}

func TestFreshSchemaContainsEventSeqColumnAndIndex(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	rows, err := store.db.QueryContext(ctx, `PRAGMA table_info(event_envelope)`)
	if err != nil {
		t.Fatalf("event_envelope table_info: %v", err)
	}
	defer rows.Close()
	var foundEventSeq bool
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("event_envelope table_info scan: %v", err)
		}
		if name == "event_seq" {
			foundEventSeq = true
			if columnType != "INTEGER" || notNull != 1 {
				t.Fatalf("event_seq schema = type %q notnull=%d, want INTEGER NOT NULL", columnType, notNull)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("event_envelope table_info rows: %v", err)
	}
	if !foundEventSeq {
		t.Fatal("event_envelope.event_seq column is missing")
	}

	var indexCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_index_list('event_envelope') WHERE name = 'event_envelope_seq_idx'`).Scan(&indexCount); err != nil {
		t.Fatalf("event sequence index lookup: %v", err)
	}
	if indexCount != 1 {
		t.Fatal("event_envelope_seq_idx is missing")
	}
}

func TestAppendSequencedAssignsMonotonicSequenceAndAppendDelegates(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	first, err := store.AppendSequenced(ctx, eventFixture(modulecore.NewTraceID(), "sequence.first", "dci"))
	if err != nil {
		t.Fatalf("AppendSequenced(first): %v", err)
	}
	if first.EventSeq != 1 {
		t.Fatalf("first EventSeq = %d, want 1", first.EventSeq)
	}
	second, err := store.AppendSequenced(ctx, eventFixture(first.TraceID, "sequence.second", "dci"))
	if err != nil {
		t.Fatalf("AppendSequenced(second): %v", err)
	}
	if second.EventSeq != 2 {
		t.Fatalf("second EventSeq = %d, want 2", second.EventSeq)
	}

	third := eventFixture(first.TraceID, "sequence.third", "dci")
	if err := store.Append(ctx, third); err != nil {
		t.Fatalf("Append(third): %v", err)
	}
	got, found, err := store.GetByID(ctx, third.EventID)
	if err != nil || !found {
		t.Fatalf("GetByID(third): found=%t err=%v", found, err)
	}
	if got.EventSeq != 3 {
		t.Fatalf("delegated Append EventSeq = %d, want 3", got.EventSeq)
	}
}

func TestAppendSequencedRejectsPreassignedSequence(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	preassigned := eventFixture(modulecore.NewTraceID(), "sequence.preassigned", "dci")
	preassigned.EventSeq = 7
	_, err := store.AppendSequenced(ctx, preassigned)
	assertAppendErrorContains(t, err, "zero for live append")
	assertEventAbsent(t, store, preassigned.EventID)

	auto := eventFixture(preassigned.TraceID, "sequence.after-preassigned", "dci")
	stored, err := store.AppendSequenced(ctx, auto)
	if err != nil {
		t.Fatalf("AppendSequenced(auto): %v", err)
	}
	if stored.EventSeq != 1 {
		t.Fatalf("auto EventSeq = %d, want 1", stored.EventSeq)
	}

	negative := eventFixture(preassigned.TraceID, "sequence.negative", "dci")
	negative.EventSeq = -1
	_, err = store.AppendSequenced(ctx, negative)
	assertAppendErrorContains(t, err, "event_seq")
	assertEventAbsent(t, store, negative.EventID)
}

func TestAppendSequencedContinuesAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event-store.sqlite")
	ctx := context.Background()
	firstStore, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(first): %v", err)
	}
	first, err := firstStore.AppendSequenced(ctx, eventFixture(modulecore.NewTraceID(), "sequence.before-restart", "dci"))
	if err != nil {
		t.Fatalf("AppendSequenced(first): %v", err)
	}
	if first.EventSeq != 1 {
		t.Fatalf("first EventSeq = %d, want 1", first.EventSeq)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatalf("Close(first): %v", err)
	}
	closedStatus := firstStore.CheckpointStatus()
	if !closedStatus.CheckpointAttempted || closedStatus.LastSuccessAt.IsZero() {
		t.Fatalf("Close(first) status = %+v, want final checkpoint success", closedStatus)
	}

	secondStore, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(second): %v", err)
	}
	defer func() { _ = secondStore.Close() }()
	second, err := secondStore.AppendSequenced(ctx, eventFixture(first.TraceID, "sequence.after-restart", "dci"))
	if err != nil {
		t.Fatalf("AppendSequenced(second): %v", err)
	}
	if second.EventSeq != 2 {
		t.Fatalf("second EventSeq = %d, want 2", second.EventSeq)
	}
}

func TestCloseFinalCheckpointIncludesLastCommittedAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event-store-close-final.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	event := eventFixture(modulecore.NewTraceID(), "close.final-checkpoint", "dci")
	stored, err := store.AppendSequenced(context.Background(), event)
	if err != nil {
		t.Fatalf("AppendSequenced: %v", err)
	}
	committedAt := time.Now().UTC()
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	status := store.CheckpointStatus()
	if !status.CheckpointAttempted || status.LastSuccessAt.IsZero() || status.LastSuccessAt.Before(committedAt) {
		t.Fatalf("final checkpoint status = %+v, want success after commit at %s", status, committedAt)
	}
	if status.Busy != 0 || (status.LogFrames != status.CheckpointedFrames && !(status.LogFrames == -1 && status.CheckpointedFrames == -1)) {
		t.Fatalf("final checkpoint status = %+v, want fully checkpointed WAL", status)
	}

	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore after close: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	reloaded, found, err := reopened.GetByID(context.Background(), stored.EventID)
	if err != nil || !found || reloaded.EventSeq != stored.EventSeq {
		t.Fatalf("reopened last committed event found=%v seq=%d err=%v; want found with seq %d", found, reloaded.EventSeq, err, stored.EventSeq)
	}
}

func TestCloseWaitsForActiveWriterTransactionBeforeFinalCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "event-store-close-drain.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	if _, err := store.db.Exec(`CREATE TABLE close_drain_test (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create close drain table: %v", err)
	}
	tx, err := store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO close_drain_test (id) VALUES (1)`); err != nil {
		_ = tx.Rollback()
		t.Fatalf("insert in-flight row: %v", err)
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- store.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before the active writer transaction completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if status := store.CheckpointStatus(); status.CheckpointAttempted {
		t.Fatalf("checkpoint ran before the active writer committed: %+v", status)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit active writer transaction: %v", err)
	}
	commitAt := time.Now().UTC()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not finish after the active writer transaction committed")
	}
	status := store.CheckpointStatus()
	if !status.CheckpointAttempted || status.LastSuccessAt.IsZero() || status.LastSuccessAt.Before(commitAt) {
		t.Fatalf("final checkpoint status = %+v, want success after commit at %s", status, commitAt)
	}
	if status.Busy != 0 || (status.LogFrames != status.CheckpointedFrames && !(status.LogFrames == -1 && status.CheckpointedFrames == -1)) {
		t.Fatalf("final checkpoint status = %+v, want fully checkpointed WAL", status)
	}

	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore after close: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	var rows int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM close_drain_test WHERE id = 1`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("persisted close-racing transaction rows = %d, err=%v; want 1", rows, err)
	}
}

func TestAppendSequencedConcurrentWithWALCheckpoint(t *testing.T) {
	store := newTestStore(t)
	const writes = 24
	errCh := make(chan error, writes+4)
	var wg sync.WaitGroup
	for i := 0; i < writes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.AppendSequenced(context.Background(), eventFixture(modulecore.NewTraceID(), "checkpoint.concurrent", "dci")); err != nil {
				errCh <- err
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.checkpointWorker.Checkpoint(context.Background()); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent append/checkpoint error: %v", err)
	}
	items, err := store.ListByComponent(context.Background(), "dci", writes)
	if err != nil || len(items) != writes {
		t.Fatalf("concurrent append count = %d, err=%v; want %d", len(items), err, writes)
	}
}

func TestListByComponentOrdersByEventSeq(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	first := eventFixture(modulecore.NewTraceID(), "sequence.component.first", "dci")
	first.OccurredAt = first.OccurredAt.Add(10 * time.Minute)
	second := eventFixture(first.TraceID, "sequence.component.second", "dci")
	second.OccurredAt = second.OccurredAt.Add(-10 * time.Minute)
	if _, err := store.AppendSequenced(ctx, first); err != nil {
		t.Fatalf("AppendSequenced(first): %v", err)
	}
	if _, err := store.AppendSequenced(ctx, second); err != nil {
		t.Fatalf("AppendSequenced(second): %v", err)
	}
	got, err := store.ListByComponent(ctx, "dci", 10)
	if err != nil {
		t.Fatalf("ListByComponent(): %v", err)
	}
	if len(got) != 2 || got[0].EventID != second.EventID || got[1].EventID != first.EventID {
		t.Fatalf("ListByComponent() order = %#v, want newest event sequence first", got)
	}
}

func TestAppendBatchAssignsZeroSequencesPreservesPositiveAndRejectsDuplicates(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	traceID := modulecore.NewTraceID()
	first := eventFixture(traceID, "batch.first", "dci")
	preserved := eventFixture(traceID, "batch.preserved", "dci")
	preserved.EventSeq = 5
	last := eventFixture(traceID, "batch.last", "dci")
	if err := store.AppendBatch(ctx, []modulecore.EventEnvelope{first, preserved, last}); err != nil {
		t.Fatalf("AppendBatch(): %v", err)
	}
	for eventID, want := range map[modulecore.EventID]modulecore.EventSeq{
		first.EventID:     1,
		preserved.EventID: 5,
		last.EventID:      2,
	} {
		got, found, err := store.GetByID(ctx, eventID)
		if err != nil || !found {
			t.Fatalf("GetByID(%q): found=%t err=%v", eventID, found, err)
		}
		if got.EventSeq != want {
			t.Fatalf("event %q EventSeq = %d, want %d", eventID, got.EventSeq, want)
		}
	}

	duplicateA := eventFixture(traceID, "batch.duplicate.a", "dci")
	duplicateA.EventSeq = 9
	duplicateB := eventFixture(traceID, "batch.duplicate.b", "dci")
	duplicateB.EventSeq = 9
	assertAppendErrorContains(t, store.AppendBatch(ctx, []modulecore.EventEnvelope{duplicateA, duplicateB}), "duplicate event_seq")
	assertEventAbsent(t, store, duplicateA.EventID)
	assertEventAbsent(t, store, duplicateB.EventID)
}

func TestAppendBatchSkipsExplicitSequenceImmediatelyAfterPersistedMaximum(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	traceID := modulecore.NewTraceID()
	for index := 0; index < 4; index++ {
		if _, err := store.AppendSequenced(ctx, eventFixture(traceID, fmt.Sprintf("seed.%d", index), "dci")); err != nil {
			t.Fatalf("seed append %d: %v", index, err)
		}
	}
	zero := eventFixture(traceID, "batch.zero", "dci")
	reserved := eventFixture(traceID, "batch.reserved", "dci")
	reserved.EventSeq = 5
	if err := store.AppendBatch(ctx, []modulecore.EventEnvelope{zero, reserved}); err != nil {
		t.Fatalf("AppendBatch(): %v", err)
	}
	storedZero, found, err := store.GetByID(ctx, zero.EventID)
	if err != nil || !found {
		t.Fatalf("GetByID(zero): found=%t err=%v", found, err)
	}
	if storedZero.EventSeq != 6 {
		t.Fatalf("zero EventSeq = %d, want 6 after reserved 5", storedZero.EventSeq)
	}
}

func TestAppendBatchRejectsExplicitSequenceBehindLiveAllocator(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	traceID := modulecore.NewTraceID()
	first := eventFixture(traceID, "sequence.order.first", "dci")
	if stored, err := store.AppendSequenced(ctx, first); err != nil || stored.EventSeq != 1 {
		t.Fatalf("AppendSequenced(first) = (%d, %v), want (1, nil)", stored.EventSeq, err)
	}
	if _, err := store.AppendSequenced(ctx, first); err == nil {
		t.Fatal("AppendSequenced(duplicate) error = nil, want duplicate event rejection")
	}
	third, err := store.AppendSequenced(ctx, eventFixture(traceID, "sequence.order.third", "dci"))
	if err != nil || third.EventSeq != 3 {
		t.Fatalf("AppendSequenced(third) = (%d, %v), want (3, nil)", third.EventSeq, err)
	}
	lateGap := eventFixture(traceID, "sequence.order.late-gap", "dci")
	lateGap.EventSeq = 2
	if err := store.AppendBatch(ctx, []modulecore.EventEnvelope{lateGap}); err == nil || !strings.Contains(err.Error(), "allocator") {
		t.Fatalf("AppendBatch(sequence 2) error = %v, want allocator-order rejection", err)
	}
	assertEventAbsent(t, store, lateGap.EventID)
}

func TestNewSQLiteStoreRejectsLegacySchemaWithoutEventSeq(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-event-store.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(legacy): %v", err)
	}
	_, err = db.Exec(`CREATE TABLE event_envelope (
		event_id TEXT PRIMARY KEY NOT NULL,
		trace_id TEXT NOT NULL,
		schema_version TEXT NOT NULL,
		event_type TEXT NOT NULL,
		component_id TEXT NOT NULL,
		occurred_at TEXT NOT NULL,
		envelope_json TEXT NOT NULL
	)`)
	if err != nil {
		db.Close()
		t.Fatalf("create legacy event_envelope: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close(legacy): %v", err)
	}
	if _, err := NewSQLiteStore(path); err == nil {
		t.Fatal("NewSQLiteStore(legacy) error = nil, want Step09 migration-required error")
	} else if !strings.Contains(strings.ToLower(err.Error()), "step09 migration required") {
		t.Fatalf("legacy schema error = %q, want explicit Step09 migration-required error", err)
	}
}

func TestNewSQLiteStoreRejectsIncompleteEventSeqSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "incomplete-event-store.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open(incomplete): %v", err)
	}
	_, err = db.Exec(`CREATE TABLE event_envelope (
		event_id TEXT PRIMARY KEY NOT NULL,
		event_seq INTEGER NOT NULL,
		trace_id TEXT NOT NULL,
		schema_version TEXT NOT NULL,
		event_type TEXT NOT NULL,
		component_id TEXT NOT NULL,
		occurred_at TEXT NOT NULL,
		envelope_json TEXT NOT NULL
	)`)
	if err != nil {
		db.Close()
		t.Fatalf("create incomplete event_envelope: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close(incomplete): %v", err)
	}
	if _, err := NewSQLiteStore(path); err == nil {
		t.Fatal("NewSQLiteStore(incomplete) error = nil, want Step09 migration-required error")
	} else if !strings.Contains(strings.ToLower(err.Error()), "step09 migration required") {
		t.Fatalf("incomplete schema error = %q, want explicit Step09 migration-required error", err)
	}
}

func TestReadRejectsEventSeqColumnJSONMismatch(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	event, err := store.AppendSequenced(ctx, eventFixture(modulecore.NewTraceID(), "sequence.drift", "dci"))
	if err != nil {
		t.Fatalf("AppendSequenced(): %v", err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER event_envelope_append_only_update`); err != nil {
		t.Fatalf("drop append-only trigger: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE event_envelope SET event_seq = event_seq + 1 WHERE event_id = ?`, event.EventID); err != nil {
		t.Fatalf("mutate event sequence column: %v", err)
	}
	if _, _, err := store.GetByID(ctx, event.EventID); err == nil {
		t.Fatal("GetByID() accepted event sequence column/JSON mismatch")
	}
}

func TestReadRejectsPersistedUnassignedEventSeq(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	event, err := store.AppendSequenced(ctx, eventFixture(modulecore.NewTraceID(), "sequence.unassigned", "dci"))
	if err != nil {
		t.Fatalf("AppendSequenced(): %v", err)
	}
	var payload string
	if err := store.db.QueryRowContext(ctx, `SELECT envelope_json FROM event_envelope WHERE event_id = ?`, event.EventID).Scan(&payload); err != nil {
		t.Fatalf("read envelope JSON: %v", err)
	}
	var tampered modulecore.EventEnvelope
	if err := json.Unmarshal([]byte(payload), &tampered); err != nil {
		t.Fatalf("decode envelope JSON: %v", err)
	}
	tampered.EventSeq = 0
	tamperedPayload, err := json.Marshal(tampered)
	if err != nil {
		t.Fatalf("encode envelope JSON: %v", err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER event_envelope_append_only_update`); err != nil {
		t.Fatalf("drop append-only trigger: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE event_envelope SET envelope_json = ? WHERE event_id = ?`, string(tamperedPayload), event.EventID); err != nil {
		t.Fatalf("mutate envelope JSON: %v", err)
	}
	if _, _, err := store.GetByID(ctx, event.EventID); err == nil {
		t.Fatal("GetByID() accepted persisted unassigned event sequence")
	} else if !strings.Contains(strings.ToLower(err.Error()), "event sequence must be positive") {
		t.Fatalf("GetByID() error = %q, want positive event sequence failure", err)
	}
}

func TestAppendRejectsMissingCausationAndDependency(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	traceID := modulecore.NewTraceID()

	missingCausation := eventFixture(traceID, "run.started", "superagent")
	missingCausation.CausationEventID = modulecore.NewEventID()
	assertAppendErrorContains(t, store.Append(ctx, missingCausation), "causation")
	assertEventAbsent(t, store, missingCausation.EventID)

	missingDependency := eventFixture(traceID, "verification.started", "superagent")
	missingDependency.DependencyEventIDs = []modulecore.EventID{modulecore.NewEventID()}
	assertAppendErrorContains(t, store.Append(ctx, missingDependency), "dependency")
	assertEventAbsent(t, store, missingDependency.EventID)
}

func TestAppendRejectsCrossTraceReference(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	root := eventFixture(modulecore.NewTraceID(), "task.created", "orchestrator")
	if err := store.Append(ctx, root); err != nil {
		t.Fatalf("Append(root) error = %v", err)
	}
	child := eventFixture(modulecore.NewTraceID(), "run.started", "superagent")
	child.CausationEventID = root.EventID

	assertAppendErrorContains(t, store.Append(ctx, child), "trace")
	assertEventAbsent(t, store, child.EventID)
}

func TestAppendRejectsDuplicateEventID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	event := eventFixture(modulecore.NewTraceID(), "task.created", "orchestrator")
	if err := store.Append(ctx, event); err != nil {
		t.Fatalf("Append(event) error = %v", err)
	}

	assertAppendErrorContains(t, store.Append(ctx, event), "duplicate")
	got, err := store.ListByComponent(ctx, event.ComponentID, 10)
	if err != nil {
		t.Fatalf("ListByComponent() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListByComponent() length = %d, want 1", len(got))
	}
}

func TestAppendRollbackLeavesNoPartialEnvelopeOrDependencies(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	traceID := modulecore.NewTraceID()
	left := eventFixture(traceID, "branch.left", "orchestrator")
	right := eventFixture(traceID, "branch.right", "orchestrator")
	for _, event := range []modulecore.EventEnvelope{left, right} {
		if err := store.Append(ctx, event); err != nil {
			t.Fatalf("Append(%q) error = %v", event.EventID, err)
		}
	}

	_, err := store.db.Exec(`
		CREATE TRIGGER eventstore_test_abort_dependency
		BEFORE INSERT ON event_dependency
		WHEN NEW.relation_type = 'dependency'
		BEGIN
			SELECT RAISE(ABORT, 'forced dependency failure');
		END`)
	if err != nil {
		t.Fatalf("create rollback trigger: %v", err)
	}
	defer func() { _, _ = store.db.Exec(`DROP TRIGGER eventstore_test_abort_dependency`) }()

	join := eventFixture(traceID, "branch.join", "orchestrator")
	join.CausationEventID = left.EventID
	join.DependencyEventIDs = []modulecore.EventID{right.EventID}
	assertAppendErrorContains(t, store.Append(ctx, join), "forced dependency failure")
	assertEventAbsent(t, store, join.EventID)

	var envelopeCount, dependencyCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM event_envelope`).Scan(&envelopeCount); err != nil {
		t.Fatalf("count envelopes: %v", err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM event_dependency`).Scan(&dependencyCount); err != nil {
		t.Fatalf("count dependencies: %v", err)
	}
	if envelopeCount != 2 || dependencyCount != 0 {
		t.Fatalf("after rollback envelopes = %d, dependencies = %d, want 2, 0", envelopeCount, dependencyCount)
	}
}

func TestAppendJSONRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	event := eventFixture(modulecore.NewTraceID(), "payload.recorded", "orchestrator")
	event.OccurredAt = time.Date(2026, 8, 29, 23, 45, 12, 123456789, time.FixedZone("JST", 9*60*60))
	event.Payload = map[string]any{
		"message": "round trip",
		"nested":  map[string]any{"ok": true, "score": 1.5},
		"items":   []any{"one", "two"},
	}
	stored, err := store.AppendSequenced(ctx, event)
	if err != nil {
		t.Fatalf("AppendSequenced(event) error = %v", err)
	}

	got, found, err := store.GetByID(ctx, stored.EventID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if !found {
		t.Fatal("GetByID() found = false, want true")
	}
	wantJSON, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("json.Marshal(want): %v", err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("json.Marshal(got): %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("JSON round trip = %s, want %s", gotJSON, wantJSON)
	}
}

func TestAppendCannotCreateCycleBecauseReferencesMustPreexist(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	traceID := modulecore.NewTraceID()
	first := eventFixture(traceID, "cycle.first", "orchestrator")
	second := eventFixture(traceID, "cycle.second", "orchestrator")
	first.CausationEventID = second.EventID
	assertAppendErrorContains(t, store.Append(ctx, first), "causation")

	second.CausationEventID = first.EventID
	assertAppendErrorContains(t, store.Append(ctx, second), "causation")
	assertEventAbsent(t, store, first.EventID)
	assertEventAbsent(t, store, second.EventID)
}

func TestAppendBatchIsOrderIndependentAndAtomic(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	traceID := modulecore.NewTraceID()
	root := eventFixture(traceID, "migration.root", "ai_workflow")
	child := eventFixture(traceID, "migration.child", "ai_workflow")
	child.CausationEventID = root.EventID

	if err := store.AppendBatch(ctx, []modulecore.EventEnvelope{child, root}); err != nil {
		t.Fatalf("AppendBatch() error = %v", err)
	}
	if _, found, err := store.GetByID(ctx, child.EventID); err != nil || !found {
		t.Fatalf("child found=%t err=%v", found, err)
	}

	valid := eventFixture(modulecore.NewTraceID(), "migration.valid", "superagent")
	invalid := eventFixture(modulecore.NewTraceID(), "migration.invalid", "superagent")
	invalid.CausationEventID = modulecore.NewEventID()
	if err := store.AppendBatch(ctx, []modulecore.EventEnvelope{valid, invalid}); err == nil {
		t.Fatal("AppendBatch() error = nil, want closed-graph rejection")
	}
	assertEventAbsent(t, store, valid.EventID)
	assertEventAbsent(t, store, invalid.EventID)
}

func TestAppendEnablesForeignKeysAndReadIsBounded(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	var foreignKeys int
	if err := store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("PRAGMA foreign_keys = %d, want 1", foreignKeys)
	}

	traceID := modulecore.NewTraceID()
	for i := 0; i < 3; i++ {
		event := eventFixture(traceID, "bounded.event", "orchestrator")
		event.OccurredAt = event.OccurredAt.Add(time.Duration(i) * time.Second)
		if err := store.Append(ctx, event); err != nil {
			t.Fatalf("Append(%q) error = %v", event.EventID, err)
		}
	}
	got, err := store.ListByComponent(ctx, "orchestrator", 2)
	if err != nil {
		t.Fatalf("ListByComponent() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListByComponent() length = %d, want 2", len(got))
	}
}

func TestListByTraceIDIsExactBoundedAndIndexed(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	traceID := modulecore.NewTraceID()
	first := eventFixture(traceID, "trace.first", "dci")
	first.OccurredAt = first.OccurredAt.Add(2 * time.Second)
	second := eventFixture(traceID, "trace.second", "dci")
	second.OccurredAt = second.OccurredAt.Add(time.Second)
	third := eventFixture(traceID, "trace.third", "dci")
	for _, event := range []modulecore.EventEnvelope{first, second, third} {
		if err := store.Append(ctx, event); err != nil {
			t.Fatalf("Append(%q): %v", event.EventID, err)
		}
	}
	got, err := store.ListByTraceID(ctx, traceID, 3)
	if err != nil {
		t.Fatalf("ListByTraceID(): %v", err)
	}
	if len(got) != 3 || got[0].EventID != first.EventID || got[1].EventID != second.EventID || got[2].EventID != third.EventID {
		t.Fatalf("ListByTraceID() order = %#v, want event sequence order", got)
	}
	if _, err := store.ListByTraceID(ctx, traceID, 2); err == nil {
		t.Fatal("ListByTraceID() over-bound query returned nil error")
	}
	if got, err := store.ListByTraceID(ctx, modulecore.NewTraceID(), 3); err != nil || len(got) != 0 {
		t.Fatalf("ListByTraceID() missing trace = %#v, %v; want empty success", got, err)
	}
	for _, invalid := range []struct {
		trace modulecore.TraceID
		max   int
	}{
		{trace: modulecore.TraceID(""), max: 1},
		{trace: traceID, max: 0},
	} {
		if _, err := store.ListByTraceID(ctx, invalid.trace, invalid.max); err == nil {
			t.Fatalf("ListByTraceID(%q,%d) returned nil error", invalid.trace, invalid.max)
		}
	}
	if _, err := store.ListByTraceID(ctx, traceID, maxListLimit+1); err == nil {
		t.Fatal("ListByTraceID() accepted a maximum above the hard bound")
	}
	var indexExists int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_index_list('event_envelope') WHERE name = 'event_envelope_trace_idx'`).Scan(&indexExists); err != nil {
		t.Fatalf("trace index lookup: %v", err)
	}
	if indexExists != 1 {
		t.Fatal("event_envelope_trace_idx is missing")
	}
	var planID, planParent, planNotUsed int
	var queryPlan string
	if err := store.db.QueryRowContext(ctx, `EXPLAIN QUERY PLAN SELECT event_id FROM event_envelope WHERE trace_id = ? ORDER BY event_seq ASC LIMIT 4`, traceID).Scan(&planID, &planParent, &planNotUsed, &queryPlan); err != nil {
		t.Fatalf("trace query plan: %v", err)
	}
	if !strings.Contains(strings.ToUpper(queryPlan), "EVENT_ENVELOPE_TRACE_IDX") {
		t.Fatalf("exact trace lookup did not use trace index: %q", queryPlan)
	}
}

func TestListByTraceIDRejectsEnvelopeColumnDrift(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	event := eventFixture(modulecore.NewTraceID(), "trace.drift", "dci")
	if err := store.Append(ctx, event); err != nil {
		t.Fatalf("Append(): %v", err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER event_envelope_append_only_update`); err != nil {
		t.Fatalf("drop append-only trigger: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE event_envelope SET component_id = 'other' WHERE event_id = ?`, event.EventID); err != nil {
		t.Fatalf("mutate canonical column: %v", err)
	}
	if _, err := store.ListByTraceID(ctx, event.TraceID, 1); err == nil {
		t.Fatal("ListByTraceID() accepted column/envelope mismatch")
	}
}

func TestHasPublicConversationFinalResponseFindsLateEventWithoutTraceLimit(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	traceID := modulecore.NewTraceID()
	turnID := modulecore.NewTurnID()
	sessionID := modulecore.NewSessionID()
	events := make([]modulecore.EventEnvelope, 1001)
	for index := range events {
		events[index] = eventFixture(traceID, "agent.thinking", "orchestrator")
		events[index].TurnID = turnID
		events[index].SessionID = sessionID
	}
	if err := store.AppendBatch(ctx, events); err != nil {
		t.Fatalf("AppendBatch noise events: %v", err)
	}
	wrongRecipient := eventFixture(traceID, "agent.response", "orchestrator")
	wrongRecipient.TurnID = turnID
	wrongRecipient.SessionID = sessionID
	wrongRecipient.Payload = map[string]any{"to": "agent"}
	if err := store.Append(ctx, wrongRecipient); err != nil {
		t.Fatalf("Append wrong-recipient response: %v", err)
	}
	wrongTurn := eventFixture(traceID, "agent.response", "orchestrator")
	wrongTurn.TurnID = modulecore.NewTurnID()
	wrongTurn.SessionID = sessionID
	wrongTurn.Payload = map[string]any{"to": "user"}
	if err := store.Append(ctx, wrongTurn); err != nil {
		t.Fatalf("Append wrong-turn response: %v", err)
	}
	legacyFinal := eventFixture(traceID, "agent.response", "orchestrator")
	legacyFinal.SessionID = sessionID
	legacyFinal.Payload = map[string]any{"to": "user"}
	if err := store.Append(ctx, legacyFinal); err != nil {
		t.Fatalf("Append legacy response without turn ID: %v", err)
	}
	if found, err := store.HasPublicConversationFinalResponse(ctx, traceID, turnID, sessionID); err != nil || found {
		t.Fatalf("HasPublicConversationFinalResponse after legacy response without turn ID = %v, %v; want false", found, err)
	}
	final := eventFixture(traceID, "agent.response", "orchestrator")
	final.TurnID = turnID
	final.SessionID = sessionID
	final.Payload = map[string]any{"to": " User "}
	if err := store.Append(ctx, final); err != nil {
		t.Fatalf("Append exact final: %v", err)
	}
	if found, err := store.HasPublicConversationFinalResponse(ctx, traceID, turnID, sessionID); err != nil || !found {
		t.Fatalf("HasPublicConversationFinalResponse exact final = %v, %v; want true", found, err)
	}
	if found, err := store.HasPublicConversationFinalResponse(ctx, modulecore.NewTraceID(), turnID, sessionID); err != nil || found {
		t.Fatalf("HasPublicConversationFinalResponse wrong trace = %v, %v; want false", found, err)
	}
	if found, err := store.HasPublicConversationFinalResponse(ctx, traceID, turnID, modulecore.NewSessionID()); err != nil || found {
		t.Fatalf("HasPublicConversationFinalResponse wrong session = %v, %v; want false", found, err)
	}
}

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "event-store.sqlite"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store
}

func eventFixture(traceID modulecore.TraceID, eventType, componentID string) modulecore.EventEnvelope {
	return modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion,
		EventID:       modulecore.NewEventID(),
		TraceID:       traceID,
		EventType:     eventType,
		ComponentID:   componentID,
		OccurredAt:    time.Date(2026, 8, 29, 22, 30, 0, 0, time.UTC),
		Payload:       map[string]any{"source": componentID},
	}
}

func assertAppendErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Append() error = nil, want text containing %q", want)
	}
	if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
		t.Fatalf("Append() error = %q, want text containing %q", err, want)
	}
}

func assertEventAbsent(t *testing.T, store *SQLiteStore, eventID modulecore.EventID) {
	t.Helper()
	_, found, err := store.GetByID(context.Background(), eventID)
	if err != nil {
		t.Fatalf("GetByID(%q) error = %v", eventID, err)
	}
	if found {
		t.Fatalf("GetByID(%q) found = true, want false", eventID)
	}
}
