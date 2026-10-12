package jsonlbatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// CheckSettled is how the Task store asks, without taking a lock or touching a
// file, whether a closed segment's WAL is clean.
func TestCheckSettledSeesWhatRecoverWouldRepair(t *testing.T) {
	names := []string{"state.jsonl", "run.jsonl"}
	root := t.TempDir()
	store, err := New(root, names)
	if err != nil {
		t.Fatal(err)
	}

	if err := CheckSettled(root, names); err != nil {
		t.Fatalf("a new directory is settled: %v", err)
	}
	if err := store.Write(context.Background(), func() (map[string][]byte, error) {
		return map[string][]byte{"state.jsonl": []byte("one\n")}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := CheckSettled(root, names); err != nil {
		t.Fatalf("a committed transaction is settled: %v", err)
	}
	if err := CheckSettled(t.TempDir(), names); err != nil {
		t.Fatalf("a directory without a WAL is settled: %v", err)
	}

	walPath := filepath.Join(root, journalFilename)
	statePath := filepath.Join(root, "state.jsonl")
	before, err := os.ReadFile(walPath)
	if err != nil {
		t.Fatal(err)
	}

	// A prepare with no end: pending.
	offset := int64(len("one\n"))
	prepare := journalRecord{Version: journalVersion, Kind: journalPrepare, TxID: "check-pending", Files: []journalFile{
		{Name: "state.jsonl", Offset: offset, AppendLength: 4, PrefixSHA256: mustHashPrefix(t, statePath, offset), PayloadSHA256: hashBytes([]byte("two\n"))},
	}}
	if err := appendJournalRecord(walPath, prepare); err != nil {
		t.Fatal(err)
	}
	if err := CheckSettled(root, names); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("a pending prepare: %v, want ErrRecoveryRequired", err)
	}
	if err := store.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := CheckSettled(root, names); err != nil {
		t.Fatalf("after Recover: %v", err)
	}

	// A torn last record.
	after, _ := os.ReadFile(walPath)
	torn := append(append([]byte(nil), after...), []byte(`{"version":1,"kind":"prepare"`)...)
	if err := os.WriteFile(walPath, torn, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckSettled(root, names); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("a torn record: %v, want ErrRecoveryRequired", err)
	}

	// It changed nothing, and it needs no lock: it works on a read-only WAL.
	if err := os.WriteFile(walPath, after, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(walPath, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(walPath, 0o600) })
	if err := CheckSettled(root, names); err != nil {
		t.Fatalf("a read-only settled WAL: %v", err)
	}
	if got, _ := os.ReadFile(walPath); string(got) != string(after) {
		t.Fatal("CheckSettled changed the WAL")
	}
	_ = before
}
