package jsonlbatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteAndReadKeepsMultipleFilesTogether(t *testing.T) {
	store, err := New(t.TempDir(), []string{"state.jsonl", "run.jsonl"})
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Write(context.Background(), func() (map[string][]byte, error) {
		return map[string][]byte{
			"state.jsonl": []byte(`{"id":"task-1","state":"running"}` + "\n"),
			"run.jsonl":   []byte(`{"id":"run-1","task":"task-1"}` + "\n"),
		}, nil
	}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	var seenState, seenRun []byte
	if err := store.Read(context.Background(), func() error {
		var err error
		seenState, err = os.ReadFile(filepath.Join(store.root, "state.jsonl"))
		if err != nil {
			return err
		}
		seenRun, err = os.ReadFile(filepath.Join(store.root, "run.jsonl"))
		return err
	}); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if string(seenState) != `{"id":"task-1","state":"running"}`+"\n" {
		t.Fatalf("state = %q", seenState)
	}
	if string(seenRun) != `{"id":"run-1","task":"task-1"}`+"\n" {
		t.Fatalf("run = %q", seenRun)
	}
}

func TestOpenReaderDoesNotCreateDataOrWAL(t *testing.T) {
	root := t.TempDir()
	dataPath := filepath.Join(root, "state.jsonl")
	if err := os.WriteFile(dataPath, []byte("existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReader(root, []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, journalFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenReader() WAL error = %v, want not exist", err)
	}
	if err := reader.Read(context.Background(), func() error {
		data, readErr := os.ReadFile(dataPath)
		if readErr != nil {
			return readErr
		}
		if string(data) != "existing\n" {
			return errors.New("reader observed unexpected data")
		}
		return nil
	}); err != nil {
		t.Fatalf("OpenReader().Read() error = %v", err)
	}
	if err := reader.Write(context.Background(), func() (map[string][]byte, error) {
		return map[string][]byte{"state.jsonl": []byte("write\n")}, nil
	}); err == nil {
		t.Fatal("OpenReader().Write() unexpectedly succeeded")
	}
}

func TestWriteCallbackFailureLeavesDataAndJournalUntouched(t *testing.T) {
	root := t.TempDir()
	store, err := New(root, []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	beforeData, err := os.ReadFile(filepath.Join(root, "state.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	beforeJournal, err := os.ReadFile(filepath.Join(root, journalFilename))
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("validation failed")
	if err := store.Write(context.Background(), func() (map[string][]byte, error) {
		return nil, want
	}); !errors.Is(err, want) {
		t.Fatalf("Write() error = %v, want %v", err, want)
	}
	afterData, err := os.ReadFile(filepath.Join(root, "state.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	afterJournal, err := os.ReadFile(filepath.Join(root, journalFilename))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterData) != string(beforeData) || string(afterJournal) != string(beforeJournal) {
		t.Fatalf("callback failure mutated data=%q journal=%q", afterData, afterJournal)
	}
}

func TestCommitSyncFailureLeavesCommittedShapeForRecovery(t *testing.T) {
	root := t.TempDir()
	store, err := New(root, []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	var journalSyncs int
	injected := errors.New("injected commit sync failure")
	store.syncFn = func(file *os.File) error {
		if filepath.Base(file.Name()) == journalFilename {
			journalSyncs++
			if journalSyncs == 2 {
				return injected
			}
		}
		return file.Sync()
	}
	err = store.Write(context.Background(), func() (map[string][]byte, error) {
		return map[string][]byte{"state.jsonl": []byte("committed\n")}, nil
	})
	if !errors.Is(err, injected) || !errors.Is(err, ErrCommitUncertain) || !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("Write() error = %v, want injected commit uncertainty", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "state.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "committed\n" {
		t.Fatalf("data = %q, want committed append retained", data)
	}
	if err := store.Read(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("Read() after complete commit record error = %v", err)
	}
}

func TestCommitCloseFailureLeavesCommittedShapeForRecovery(t *testing.T) {
	root := t.TempDir()
	store, err := New(root, []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	var journalCloses int
	injected := errors.New("injected commit close failure")
	store.closeFn = func(file *os.File) error {
		closeErr := file.Close()
		if filepath.Base(file.Name()) == journalFilename {
			journalCloses++
			if journalCloses == 2 {
				return errors.Join(closeErr, injected)
			}
		}
		return closeErr
	}
	err = store.Write(context.Background(), func() (map[string][]byte, error) {
		return map[string][]byte{"state.jsonl": []byte("committed-close\n")}, nil
	})
	if !errors.Is(err, injected) || !errors.Is(err, ErrCommitUncertain) || !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("Write() error = %v, want injected commit uncertainty", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "state.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "committed-close\n" {
		t.Fatalf("data = %q, want committed append retained", data)
	}
	if err := store.Read(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("Read() after complete commit record error = %v", err)
	}
}

func TestPendingPrepareIsHiddenFromReaderAndRecoveredByWriter(t *testing.T) {
	root := t.TempDir()
	_, err := New(root, []string{"state.jsonl", "run.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "state.jsonl")
	runPath := filepath.Join(root, "run.jsonl")
	if err := os.WriteFile(statePath, []byte("before-state\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runPath, []byte("before-run\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stateOffset := int64(len("before-state\n"))
	runOffset := int64(len("before-run\n"))
	if err := os.WriteFile(statePath, []byte("before-state\npartial-state"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runPath, []byte("before-run\npartial-run"), 0o644); err != nil {
		t.Fatal(err)
	}
	prepare := journalRecord{
		Version: journalVersion,
		Kind:    journalPrepare,
		TxID:    "pending-test",
		Files: []journalFile{
			{Name: "state.jsonl", Offset: stateOffset, AppendLength: int64(len("partial-state")), PrefixSHA256: mustHashPrefix(t, statePath, stateOffset), PayloadSHA256: hashBytes([]byte("partial-state"))},
			{Name: "run.jsonl", Offset: runOffset, AppendLength: int64(len("partial-run")), PrefixSHA256: mustHashPrefix(t, runPath, runOffset), PayloadSHA256: hashBytes([]byte("partial-run"))},
		},
	}
	if err := appendJournalRecord(filepath.Join(root, journalFilename), prepare); err != nil {
		t.Fatal(err)
	}

	reopened, err := New(root, []string{"state.jsonl", "run.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Read(context.Background(), func() error { return nil }); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("Read() error = %v, want ErrRecoveryRequired", err)
	}
	if err := reopened.Recover(context.Background()); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if err := reopened.Read(context.Background(), func() error {
		state, readErr := os.ReadFile(statePath)
		if readErr != nil {
			return readErr
		}
		if string(state) != "before-state\n" {
			return errors.New("state was not rolled back")
		}
		run, readErr := os.ReadFile(runPath)
		if readErr != nil {
			return readErr
		}
		if string(run) != "before-run\n" {
			return errors.New("run was not rolled back")
		}
		return nil
	}); err != nil {
		t.Fatalf("Read() after recovery error = %v", err)
	}
}

func TestTornJournalIsReaderErrorAndWriterRepairsIt(t *testing.T) {
	root := t.TempDir()
	store, err := New(root, []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(root, journalFilename)
	if err := os.WriteFile(journalPath, []byte(`{"version":1,"kind":"prepare"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Read(context.Background(), func() error { return nil }); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("Read() error = %v, want ErrRecoveryRequired", err)
	}
	if err := store.Recover(context.Background()); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	data, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Fatalf("repaired journal = %q, want empty", data)
	}
}

func TestTornCommitRollsBackPendingDataOnWriterRecovery(t *testing.T) {
	root := t.TempDir()
	store, err := New(root, []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "state.jsonl")
	if err := os.WriteFile(statePath, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	payload := []byte("pending\n")
	if file, openErr := os.OpenFile(statePath, os.O_WRONLY|os.O_APPEND, 0); openErr != nil {
		t.Fatal(openErr)
	} else {
		if _, writeErr := file.Write(payload); writeErr != nil {
			_ = file.Close()
			t.Fatal(writeErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	prepare := journalRecord{
		Version: journalVersion,
		Kind:    journalPrepare,
		TxID:    "torn-commit",
		Files: []journalFile{{
			Name:          "state.jsonl",
			Offset:        int64(len("before\n")),
			AppendLength:  int64(len(payload)),
			PrefixSHA256:  hashBytes([]byte("before\n")),
			PayloadSHA256: hashBytes(payload),
		}},
	}
	if err := appendJournalRecord(filepath.Join(root, journalFilename), prepare); err != nil {
		t.Fatal(err)
	}
	journal, err := os.OpenFile(filepath.Join(root, journalFilename), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Write([]byte(`{"version":1,"kind":"commit"`)); err != nil {
		_ = journal.Close()
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Read(context.Background(), func() error { return nil }); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("Read() error = %v, want recovery required", err)
	}
	if err := store.Recover(context.Background()); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "before\n" {
		t.Fatalf("recovered data = %q, want pending append removed", data)
	}
}

func TestConcurrentInstancesSerializeCallbacks(t *testing.T) {
	root := t.TempDir()
	first, err := New(root, []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(root, []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.Read(context.Background(), func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	secondErr := second.Write(ctx, func() (map[string][]byte, error) {
		return map[string][]byte{"state.jsonl": []byte("blocked\n")}, nil
	})
	if !errors.Is(secondErr, context.DeadlineExceeded) {
		t.Fatalf("second Write() error = %v, want deadline exceeded", secondErr)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Read() error = %v", err)
	}
}

func TestRejectsPathEscapeAndSymlink(t *testing.T) {
	for _, filename := range []string{"../state.jsonl", `nested/state.jsonl`, `nested\\state.jsonl`, ".jsonlbatch.wal"} {
		if _, err := New(t.TempDir(), []string{filename}); err == nil {
			t.Fatalf("New(%q) unexpectedly succeeded", filename)
		}
	}

	root := t.TempDir()
	target := filepath.Join(root, "outside.jsonl")
	if err := os.WriteFile(target, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "state.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := New(root, []string{"state.jsonl"}); err == nil {
		t.Fatal("New() accepted a symlink data path")
	}
}

func TestRejectsUnterminatedPayload(t *testing.T) {
	store, err := New(t.TempDir(), []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Write(context.Background(), func() (map[string][]byte, error) {
		return map[string][]byte{"state.jsonl": []byte(`{"id":1}`)}, nil
	}); err == nil {
		t.Fatal("Write() accepted unterminated JSONL payload")
	}
}

func mustHashPrefix(t *testing.T, path string, size int64) string {
	t.Helper()
	hash, err := hashFileRange(path, 0, size)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestCompleteJournalCorruptionFailsClosed(t *testing.T) {
	root := t.TempDir()
	store, err := New(root, []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(root, journalFilename)
	if err := os.WriteFile(journalPath, []byte(`{"version":1,"kind":"commit","tx_id":"orphan"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Read(context.Background(), func() error { return nil }); !errors.Is(err, ErrJournalCorrupt) || !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("Read() error = %v, want both journal corruption and recovery required", err)
	}
	if err := store.Recover(context.Background()); !errors.Is(err, ErrJournalCorrupt) || !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("Recover() error = %v, want both journal corruption and recovery required", err)
	}
	data, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("corrupt journal was unexpectedly mutated")
	}
}

func TestMultiplePendingPreparesFailClosed(t *testing.T) {
	root := t.TempDir()
	store, err := New(root, []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	emptyHash := hashBytes(nil)
	for _, record := range []journalRecord{
		{Version: journalVersion, Kind: journalPrepare, TxID: "pending-a", Files: []journalFile{{Name: "state.jsonl", AppendLength: 2, PrefixSHA256: emptyHash, PayloadSHA256: hashBytes([]byte("a\n"))}}},
		{Version: journalVersion, Kind: journalPrepare, TxID: "pending-b", Files: []journalFile{{Name: "state.jsonl", AppendLength: 2, PrefixSHA256: emptyHash, PayloadSHA256: hashBytes([]byte("b\n"))}}},
	} {
		if err := appendJournalRecord(filepath.Join(root, journalFilename), record); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Read(context.Background(), func() error { return nil }); !errors.Is(err, ErrJournalCorrupt) || !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("Read() error = %v, want both journal corruption and recovery required", err)
	}
	if err := store.Recover(context.Background()); !errors.Is(err, ErrJournalCorrupt) || !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("Recover() error = %v, want both journal corruption and recovery required", err)
	}
}
