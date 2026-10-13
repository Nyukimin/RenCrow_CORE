package action

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func dirListing(t *testing.T, root string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = info.Size()
	}
	return out
}

func TestVerifyAgreesOnAGoodStoreAndChangesNothing(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "actions")
	store := openFull(t, root)
	actions, attempts := seedPairs(t, store, 7)
	closedAction, closedAttempt := closePair(t, actions[0], attempts[0], supportBase.Add(time.Hour), domainaction.StatusFailed, domainaction.AttemptStatusFailed)
	if err := savePair(ctx, store, closedAction, closedAttempt); err != nil {
		t.Fatal(err)
	}
	before := dirListing(t, root)

	report, err := VerifyActionStore(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK || report.Mismatches != 0 || len(report.Problems) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if report.Actions != 7 || report.Attempts != 7 {
		t.Fatalf("report counts %d Actions and %d Attempts, want 7 and 7", report.Actions, report.Attempts)
	}
	if report.ActionBytes != fileSize(t, store.actionPath) || report.AttemptBytes != fileSize(t, store.attemptPath) {
		t.Fatalf("report covers %d and %d bytes", report.ActionBytes, report.AttemptBytes)
	}
	after := dirListing(t, root)
	if len(before) != len(after) {
		t.Fatalf("verify changed the directory: %v -> %v", before, after)
	}
	for name, size := range before {
		if after[name] != size {
			t.Fatalf("verify changed %s (%d -> %d bytes)", name, size, after[name])
		}
	}
	// It held no lock afterwards.
	action, attempt := newOpenPair(modulecore.NewTaskID(), modulecore.NewRunID(), domainaction.KindTool, "after", supportBase.Add(2*time.Hour))
	if err := savePair(ctx, store, action, attempt); err != nil {
		t.Fatalf("write after verify: %v", err)
	}
}

func TestVerifyReportsAnInvalidLogInsteadOfPassingIt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "actions")
	store := openFull(t, root)
	seedPairs(t, store, 3)
	f, err := os.OpenFile(store.attemptPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{not json}\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	report, err := VerifyActionStore(context.Background(), root)
	if err == nil && report.OK {
		t.Fatalf("verify passed a log with an undecodable record: %+v", report)
	}
}

func TestVerifyOfAMissingStoreIsAnError(t *testing.T) {
	if _, err := VerifyActionStore(context.Background(), filepath.Join(t.TempDir(), "nothing")); err == nil {
		t.Fatal("verify of a directory that does not exist succeeded")
	}
}

func TestVerifyDoesNotCreateAStore(t *testing.T) {
	root := t.TempDir()
	if _, err := VerifyActionStore(context.Background(), root); err == nil {
		t.Fatal("verify of a directory without Action logs succeeded")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == actionFilename || e.Name() == attemptFilename {
			t.Fatalf("verify created %s", e.Name())
		}
	}
}

func TestVerifyAsksAboutEveryRunTheActionsNameOnce(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "actions")
	store := openFull(t, root)
	// 6 Actions over 3 Runs.
	seedPairs(t, store, 6)

	var asked []modulecore.RunID
	check := func(_ context.Context, runs []modulecore.RunID) (int, error) {
		asked = append(asked, runs...)
		return 1, nil
	}
	report, err := VerifyActionStoreWithOptions(ctx, root, VerifyOptions{CheckRuns: check})
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 3 || report.RunsChecked != 3 || report.RunsMissing != 1 {
		t.Fatalf("asked about %d Runs, report %+v; want 3 distinct Runs checked and 1 missing", len(asked), report)
	}
	if report.OK {
		t.Fatalf("a store that names a Run nobody owns passed: %+v", report)
	}
	seen := map[modulecore.RunID]bool{}
	for _, id := range asked {
		if seen[id] {
			t.Fatalf("Run %s was asked about twice", id)
		}
		seen[id] = true
	}
	for _, problem := range report.Problems {
		if strings.Contains(problem, "run_") {
			t.Fatalf("a problem line carries an identifier: %q", problem)
		}
	}

	none, err := VerifyActionStoreWithOptions(ctx, root, VerifyOptions{CheckRuns: func(context.Context, []modulecore.RunID) (int, error) { return 0, nil }})
	if err != nil || !none.OK || none.RunsChecked != 3 || none.RunsMissing != 0 {
		t.Fatalf("report %+v, err %v; want ok with 3 Runs checked", none, err)
	}
	plain, err := VerifyActionStore(ctx, root)
	if err != nil || !plain.OK || plain.RunsChecked != 0 {
		t.Fatalf("a verify without the Run check = %+v, %v; it must not report Runs it did not check", plain, err)
	}
}

func TestVerifyReportsAFailureOfTheRunCheckAsAnError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "actions")
	seedPairs(t, openFull(t, root), 2)
	wantErr := errors.New("task store unavailable")
	_, err := VerifyActionStoreWithOptions(context.Background(), root, VerifyOptions{CheckRuns: func(context.Context, []modulecore.RunID) (int, error) { return 0, wantErr }})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want the Run check's own error", err)
	}
}

// A line of a verify report never names a record: the identifiers an error carries
// are replaced, whatever their prefix.
func TestVerifyReportLinesCarryNoIdentifiers(t *testing.T) {
	ids := []string{string(modulecore.NewActionID()), string(modulecore.NewAttemptID()), string(modulecore.NewTaskID()), string(modulecore.NewRunID())}
	for _, id := range ids {
		err := errors.New("record " + id + " is not valid\nsecond line " + id)
		line := firstLine(err)
		if strings.Contains(line, id) || strings.Contains(line, "\n") || !strings.Contains(line, "<id>") {
			t.Fatalf("firstLine(%q) = %q; want the identifier replaced and one line", id, line)
		}
	}
}
