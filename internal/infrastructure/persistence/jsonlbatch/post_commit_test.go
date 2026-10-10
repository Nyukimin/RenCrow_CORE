package jsonlbatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type recordedCommit struct {
	appends []CommittedAppend
	// settled reports whether the journal had no pending transaction when the
	// hook ran, i.e. the commit record was already durable.
	settled bool
	// dataSeen is the size of each appended file when the hook ran.
	dataSeen map[string]int64
}

func newHookRecorder(t *testing.T, store *Store) *[]recordedCommit {
	t.Helper()
	var calls []recordedCommit
	store.SetPostCommitHook(func(_ context.Context, appends []CommittedAppend) {
		call := recordedCommit{dataSeen: map[string]int64{}}
		for _, item := range appends {
			// The payload is only valid during the hook call; keep a copy.
			item.Payload = append([]byte(nil), item.Payload...)
			call.appends = append(call.appends, item)
			if info, err := os.Stat(filepath.Join(store.root, item.Name)); err == nil {
				call.dataSeen[item.Name] = info.Size()
			}
		}
		data, err := store.readJournalData()
		if err == nil {
			state, parseErr := parseJournal(data, store.allowed)
			call.settled = parseErr == nil && !state.torn && state.pending == nil
		}
		calls = append(calls, call)
	})
	return &calls
}

func TestPostCommitHookReceivesEveryCommittedAppendAfterCommit(t *testing.T) {
	store, err := New(t.TempDir(), []string{"state.jsonl", "run.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	calls := newHookRecorder(t, store)

	first := map[string][]byte{
		"state.jsonl": []byte("s1\ns2\n"),
		"run.jsonl":   []byte("r1\n"),
	}
	if err := store.Write(context.Background(), func() (map[string][]byte, error) { return first, nil }); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	second := map[string][]byte{"state.jsonl": []byte("s3\n")}
	if err := store.Write(context.Background(), func() (map[string][]byte, error) { return second, nil }); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if len(*calls) != 2 {
		t.Fatalf("hook calls = %d, want 2", len(*calls))
	}
	got := (*calls)[0]
	if !got.settled {
		t.Fatal("hook ran before the commit record was durable")
	}
	if len(got.appends) != 2 || got.appends[0].Name != "run.jsonl" || got.appends[1].Name != "state.jsonl" {
		t.Fatalf("first hook appends = %#v, want run.jsonl then state.jsonl", got.appends)
	}
	if got.appends[0].Offset != 0 || string(got.appends[0].Payload) != "r1\n" {
		t.Fatalf("run append = %#v", got.appends[0])
	}
	if got.appends[1].Offset != 0 || string(got.appends[1].Payload) != "s1\ns2\n" {
		t.Fatalf("state append = %#v", got.appends[1])
	}
	for name, payload := range first {
		if got.dataSeen[name] != int64(len(payload)) {
			t.Fatalf("%s size at hook = %d, want %d (data must already be appended)", name, got.dataSeen[name], len(payload))
		}
	}
	next := (*calls)[1]
	if len(next.appends) != 1 || next.appends[0].Name != "state.jsonl" || next.appends[0].Offset != int64(len("s1\ns2\n")) || string(next.appends[0].Payload) != "s3\n" {
		t.Fatalf("second hook appends = %#v", next.appends)
	}
}

func TestPostCommitHookIsNotCalledWithoutDurableCommit(t *testing.T) {
	root := t.TempDir()
	store, err := New(root, []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	calls := newHookRecorder(t, store)

	callbackErr := errors.New("callback failed")
	if err := store.Write(context.Background(), func() (map[string][]byte, error) { return nil, callbackErr }); !errors.Is(err, callbackErr) {
		t.Fatalf("Write() error = %v, want callback error", err)
	}
	if err := store.Write(context.Background(), func() (map[string][]byte, error) { return nil, nil }); err != nil {
		t.Fatalf("Write() empty payload error = %v", err)
	}
	if err := store.Write(context.Background(), func() (map[string][]byte, error) {
		return map[string][]byte{"state.jsonl": []byte("no-newline")}, nil
	}); err == nil {
		t.Fatal("Write() accepted an unterminated payload")
	}
	if len(*calls) != 0 {
		t.Fatalf("hook ran %d times for writes that committed nothing", len(*calls))
	}

	injected := errors.New("injected commit sync failure")
	var journalSyncs int
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
		return map[string][]byte{"state.jsonl": []byte("uncertain\n")}, nil
	})
	if !errors.Is(err, ErrCommitUncertain) {
		t.Fatalf("Write() error = %v, want ErrCommitUncertain", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("hook ran %d times for an uncertain commit; the caller must resync from the log instead", len(*calls))
	}
}

func TestPostCommitHookIsNotCalledForACancelledWrite(t *testing.T) {
	store, err := New(t.TempDir(), []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	calls := newHookRecorder(t, store)
	if err := store.Write(context.Background(), func() (map[string][]byte, error) {
		return map[string][]byte{"state.jsonl": []byte("committed\n")}, nil
	}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("hook calls = %d, want 1 for a committed write", len(*calls))
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Write(cancelled, func() (map[string][]byte, error) {
		return map[string][]byte{"state.jsonl": []byte("never\n")}, nil
	}); err == nil {
		t.Fatal("Write() with a cancelled context succeeded")
	}
	if len(*calls) != 1 {
		t.Fatalf("hook calls = %d after a cancelled write, want 1", len(*calls))
	}
}

func TestPostCommitHookCanBeRemovedAndDefaultsToNone(t *testing.T) {
	store, err := New(t.TempDir(), []string{"state.jsonl"})
	if err != nil {
		t.Fatal(err)
	}
	// No hook installed: the write path must behave exactly as before.
	if err := store.Write(context.Background(), func() (map[string][]byte, error) {
		return map[string][]byte{"state.jsonl": []byte("plain\n")}, nil
	}); err != nil {
		t.Fatalf("Write() without hook error = %v", err)
	}
	calls := newHookRecorder(t, store)
	store.SetPostCommitHook(nil)
	if err := store.Write(context.Background(), func() (map[string][]byte, error) {
		return map[string][]byte{"state.jsonl": []byte("removed\n")}, nil
	}); err != nil {
		t.Fatalf("Write() after removing the hook error = %v", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("removed hook ran %d times", len(*calls))
	}
}
