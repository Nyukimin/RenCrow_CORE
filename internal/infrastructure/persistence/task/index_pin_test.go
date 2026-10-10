package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// The criteria revision pinned by a Task's first save and its native OPS Resume
// claims are checked by foldTaskRecords (and transactionStore.appendTask) on the
// full-fold path. These tests show the indexed store applies the same rules:
// when it opens a log, when it dry-runs a write, and when a Task is saved.

// uidFunc returns a canonical UUID-shaped string for a label.
type uidFunc func(label string) string

func seededUID(seed string) uidFunc {
	n := 0
	return func(label string) string {
		n++
		return testUUID(seed+"/"+label, n)
	}
}

// pinTestRevision is a valid criteria revision derived from n.
func pinTestRevision(n int) string { return fmt.Sprintf("%064x", n+1) }

// acceptedOPSTestTask turns base into a Task that carries the accepted OPS claim
// Resume claims require.
func acceptedOPSTestTask(base domaintask.Task, uid uidFunc, revision string) domaintask.Task {
	base.Route = domaintask.RouteOperations
	base.OwnerID = domaintask.AcceptedOPSAgentAssignee
	base.Assignee = domaintask.AcceptedOPSAgentAssignee
	base.OriginSessionID = modulecore.SessionID("ses_" + uid("session"))
	base.OriginThreadID = modulecore.ThreadID("thr_" + uid("thread"))
	base.OriginTurnID = modulecore.TurnID("turn_" + uid("turn"))
	base.OriginMessageID = modulecore.MessageID("msg_" + uid("message"))
	base.AcceptedOPSClaim = &domaintask.AcceptedOPSClaim{
		ReceiptRef: domainconversation.AcceptedOPSInputReference{
			OwnerID: "owner-1", RequestID: "request-" + uid("request"), PayloadSHA256: strings.Repeat("c", 64),
		},
		BackendSelection: domainconversation.BackendShiroNativeCodingV1,
	}
	base.ExpectedCriteriaRevision = revision
	return base
}

func resumeTestClaim(task domaintask.Task, uid uidFunc, at time.Time) domaintask.NativeOPSResumeClaim {
	return domaintask.NativeOPSResumeClaim{
		RequestID: "resume-" + uid("resume"), OwnerUserID: task.AcceptedOPSClaim.ReceiptRef.OwnerID,
		PayloadSHA256:     strings.Repeat("d", 64),
		ExpectedCoreRunID: modulecore.RunID("run_" + uid("expected-run")),
		NewCoreRunID:      modulecore.RunID("run_" + uid("new-run")),
		TraceID:           modulecore.TraceID("trc_" + uid("trace")),
		Source: domaintask.NativeOPSResumeSource{
			ActionID: modulecore.ActionID("act_" + uid("action")), AttemptID: modulecore.AttemptID("att_" + uid("attempt")),
			HarnessTaskID: "harness-task", ThreadID: "harness-thread", RunID: "harness-run", ReceiptID: "harness-receipt",
			RunResultSHA256: strings.Repeat("e", 64),
		},
		WriterGeneration: 1, CreatedAt: at,
	}
}

func withClaims(task domaintask.Task, claims ...domaintask.NativeOPSResumeClaim) domaintask.Task {
	task.NativeResumeClaims = append([]domaintask.NativeOPSResumeClaim(nil), claims...)
	return task
}

func bumped(task domaintask.Task) domaintask.Task {
	task.UpdatedAt = task.UpdatedAt.Add(time.Second)
	return task
}

func TestTestFixturesAreValidTasks(t *testing.T) {
	uid := seededUID("fixtures")
	task := acceptedOPSTestTask(indexedTestTask(1, indexTestNow), uid, pinTestRevision(1))
	task = withClaims(task, resumeTestClaim(task, uid, indexTestNow))
	if err := task.Validate(); err != nil {
		t.Fatalf("fixture Task with a Resume claim is invalid: %v", err)
	}
}

func TestOpenFailsClosedOnPinViolationsInTheLog(t *testing.T) {
	uid := seededUID("open")
	now := indexTestNow
	plain := indexedTestTask(1, now)
	pinned := plain
	pinned.TaskID = indexedTestTask(2, now).TaskID
	pinned.ExpectedCriteriaRevision = pinTestRevision(1)
	changed := bumped(pinned)
	changed.ExpectedCriteriaRevision = pinTestRevision(2)
	deleted := bumped(pinned)
	deleted.ExpectedCriteriaRevision = ""
	backfilled := bumped(plain)
	backfilled.ExpectedCriteriaRevision = pinTestRevision(3)

	ops := acceptedOPSTestTask(indexedTestTask(3, now), uid, pinTestRevision(4))
	first := resumeTestClaim(ops, uid, now)
	second := resumeTestClaim(ops, uid, now.Add(time.Second))
	withFirst := bumped(withClaims(ops, first))
	rewritten := first
	rewritten.PayloadSHA256 = strings.Repeat("f", 64)
	rewrittenTask := bumped(withClaims(ops, rewritten))
	dropped := bumped(bumped(ops))
	reordered := bumped(bumped(withClaims(ops, first, second)))
	reordered.NativeResumeClaims = []domaintask.NativeOPSResumeClaim{second, first}

	lines := func(values ...domaintask.Task) []byte {
		var out []byte
		for _, value := range values {
			out = append(out, marshalLine(t, value)...)
		}
		return out
	}
	cases := []struct {
		name  string
		tasks []domaintask.Task
		want  error
	}{
		{name: "criteria revision changed", tasks: []domaintask.Task{pinned, changed}, want: ErrExpectedCriteriaRevisionImmutable},
		{name: "criteria revision deleted", tasks: []domaintask.Task{pinned, deleted}, want: ErrExpectedCriteriaRevisionImmutable},
		{name: "criteria revision backfilled", tasks: []domaintask.Task{plain, backfilled}, want: ErrExpectedCriteriaRevisionImmutable},
		{name: "claim rewritten", tasks: []domaintask.Task{ops, withFirst, rewrittenTask}, want: ErrNativeOPSResumeClaimImmutable},
		{name: "claim dropped", tasks: []domaintask.Task{ops, withFirst, dropped}, want: ErrNativeOPSResumeClaimImmutable},
		{name: "claims reordered", tasks: []domaintask.Task{ops, withFirst, bumped(withClaims(ops, first, second)), reordered}, want: ErrNativeOPSResumeClaimImmutable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, stateFilename), lines(tc.tasks...), 0o644); err != nil {
				t.Fatal(err)
			}
			store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true})
			if err == nil {
				_ = store.Close()
				t.Fatal("indexed store opened over a log that rewrites a pinned field")
			}
			if !errors.Is(err, ErrIndexInconsistent) || !errors.Is(err, tc.want) {
				t.Fatalf("open error = %v, want ErrIndexInconsistent wrapping %v", err, tc.want)
			}
		})
	}

	t.Run("consistent history opens", func(t *testing.T) {
		root := t.TempDir()
		history := lines(plain, bumped(plain), pinned, bumped(pinned), ops, withFirst, bumped(bumped(withClaims(ops, first, second))))
		if err := os.WriteFile(filepath.Join(root, stateFilename), history, 0o644); err != nil {
			t.Fatal(err)
		}
		store := openIndexed(t, root)
		got, err := store.GetTask(context.Background(), ops.TaskID)
		if err != nil || len(got.NativeResumeClaims) != 2 {
			t.Fatalf("GetTask = %d claims, %v", len(got.NativeResumeClaims), err)
		}
	})
}

// A record the index would refuse must not reach the log (the dry run of
// writeBatch), including a pin violation that bypassed the Save checks.
func TestDryRunRefusesPinViolationBeforeCommit(t *testing.T) {
	root := t.TempDir()
	store := openIndexed(t, root)
	ctx := context.Background()
	pinned := indexedTestTask(1, indexTestNow)
	pinned.ExpectedCriteriaRevision = pinTestRevision(1)
	if err := store.SaveTask(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	sizesBefore := scanLog(t, root).bytes
	changed := bumped(pinned)
	changed.ExpectedCriteriaRevision = pinTestRevision(2)
	err := store.writeBatch(ctx, func() (map[string][]byte, error) {
		return map[string][]byte{stateFilename: marshalLine(t, changed)}, nil
	})
	if !errors.Is(err, ErrIndexInconsistent) || !errors.Is(err, ErrExpectedCriteriaRevisionImmutable) {
		t.Fatalf("writeBatch with a changed criteria revision = %v", err)
	}
	for kind, size := range sizesBefore {
		if after := scanLog(t, root).bytes[kind]; after != size {
			t.Fatalf("%s grew from %d to %d although the write was refused", kindFilename[kind], size, after)
		}
	}
	if err := store.SaveTask(ctx, bumped(pinned)); err != nil {
		t.Fatalf("the store stopped working after a refused write: %v", err)
	}
}

func TestIndexedSaveTaskKeepsPinsAndClaimsAcrossTransactionsAndRestarts(t *testing.T) {
	root := t.TempDir()
	store := openIndexed(t, root)
	ctx := context.Background()
	uid := seededUID("save")
	now := indexTestNow

	legacy := indexedTestTask(1, now)
	pinned := indexedTestTask(2, now)
	pinned.ExpectedCriteriaRevision = pinTestRevision(1)
	ops := acceptedOPSTestTask(indexedTestTask(3, now), uid, pinTestRevision(2))
	first := resumeTestClaim(ops, uid, now)
	second := resumeTestClaim(ops, uid, now.Add(time.Second))
	rewritten := first
	rewritten.RequestID = "resume-rewritten"

	mustSave := func(label string, task domaintask.Task) {
		t.Helper()
		if err := store.SaveTask(ctx, task); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
	}
	mustRefuse := func(label string, task domaintask.Task, want error) {
		t.Helper()
		if err := store.SaveTask(ctx, task); !errors.Is(err, want) {
			t.Fatalf("%s = %v, want %v", label, err, want)
		}
	}
	checkAll := func(phase string) {
		t.Helper()
		changed := bumped(pinned)
		changed.ExpectedCriteriaRevision = pinTestRevision(9)
		cleared := bumped(pinned)
		cleared.ExpectedCriteriaRevision = ""
		backfilled := bumped(legacy)
		backfilled.ExpectedCriteriaRevision = pinTestRevision(1)
		mustRefuse(phase+": change pin", changed, ErrExpectedCriteriaRevisionImmutable)
		mustRefuse(phase+": clear pin", cleared, ErrExpectedCriteriaRevisionImmutable)
		mustRefuse(phase+": backfill pin", backfilled, ErrExpectedCriteriaRevisionImmutable)
		mustRefuse(phase+": rewrite claim", bumped(withClaims(ops, rewritten)), ErrNativeOPSResumeClaimImmutable)
		mustRefuse(phase+": drop claim", bumped(ops), ErrNativeOPSResumeClaimImmutable)
		mustRefuse(phase+": reorder claims", bumped(withClaims(ops, second, first)), ErrNativeOPSResumeClaimImmutable)
	}

	mustSave("legacy Task", legacy)
	mustSave("pinned Task", pinned)
	mustSave("accepted OPS Task", ops)
	firstSaved := withClaims(ops, first)
	firstSaved.UpdatedAt = ops.UpdatedAt.Add(time.Second)
	mustSave("first Resume claim", firstSaved)
	mustSave("unchanged legacy update", bumped(legacy))
	mustSave("unchanged pinned update", bumped(pinned))
	mustRefuse("a new Task cannot start with claims", withClaims(acceptedOPSTestTask(indexedTestTask(4, now), uid, pinTestRevision(3)), first), ErrNativeOPSResumeClaimImmutable)
	checkAll("live")

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = openIndexed(t, root)
	checkAll("reopened")
	secondSaved := withClaims(ops, first, second)
	secondSaved.UpdatedAt = ops.UpdatedAt.Add(2 * time.Second)
	mustSave("second Resume claim after reopen", secondSaved)
	got, err := store.GetTask(ctx, ops.TaskID)
	if err != nil || len(got.NativeResumeClaims) != 2 || got.ExpectedCriteriaRevision != ops.ExpectedCriteriaRevision {
		t.Fatalf("GetTask = %d claims pin %q: %v", len(got.NativeResumeClaims), got.ExpectedCriteriaRevision, err)
	}
}

// Within one transaction the pending Task is the previous version: the first
// pin wins, and a claim appended earlier in the same transaction cannot be
// rewritten later in it.
func TestIndexedSaveTaskChecksAgainstPendingRecordsOfTheSameTransaction(t *testing.T) {
	store := openIndexed(t, t.TempDir())
	ctx := context.Background()
	uid := seededUID("pending")
	now := indexTestNow
	ops := acceptedOPSTestTask(indexedTestTask(1, now), uid, pinTestRevision(1))
	first := resumeTestClaim(ops, uid, now)
	rewritten := first
	rewritten.PayloadSHA256 = strings.Repeat("a", 64)
	other := bumped(ops)
	other.ExpectedCriteriaRevision = pinTestRevision(2)

	err := store.Transaction(ctx, func(tx domaintask.Store) error {
		if err := tx.SaveTask(ctx, ops); err != nil {
			return fmt.Errorf("first save: %w", err)
		}
		if err := tx.SaveTask(ctx, other); !errors.Is(err, ErrExpectedCriteriaRevisionImmutable) {
			return fmt.Errorf("pin change inside the transaction = %v", err)
		}
		if err := tx.SaveTask(ctx, bumped(withClaims(ops, first))); err != nil {
			return fmt.Errorf("claim append: %w", err)
		}
		if err := tx.SaveTask(ctx, bumped(bumped(withClaims(ops, rewritten)))); !errors.Is(err, ErrNativeOPSResumeClaimImmutable) {
			return fmt.Errorf("claim rewrite inside the transaction = %v", err)
		}
		got, err := tx.GetTask(ctx, ops.TaskID)
		if err != nil || len(got.NativeResumeClaims) != 1 {
			return fmt.Errorf("pending Task = %d claims, %v", len(got.NativeResumeClaims), err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTask(ctx, ops.TaskID)
	if err != nil || got.ExpectedCriteriaRevision != ops.ExpectedCriteriaRevision || len(got.NativeResumeClaims) != 1 {
		t.Fatalf("committed Task = pin %q, %d claims, %v", got.ExpectedCriteriaRevision, len(got.NativeResumeClaims), err)
	}
	requireIndexCoversLog(t, store, store.root)
}

// TestPinRulesMatchFullFoldOnCraftedLogs feeds the same hand-made log to the
// full-fold store and to the indexed store, including logs the API itself would
// not write (a first record that already carries Resume claims), and requires
// the same outcome: the same logs read the same, the same logs are refused with
// the same sentinel, and the same follow-up saves are accepted or refused.
func TestPinRulesMatchFullFoldOnCraftedLogs(t *testing.T) {
	uid := seededUID("crafted")
	now := indexTestNow
	ops := acceptedOPSTestTask(indexedTestTask(1, now), uid, pinTestRevision(1))
	first := resumeTestClaim(ops, uid, now)
	second := resumeTestClaim(ops, uid, now.Add(time.Second))
	rewritten := first
	rewritten.PayloadSHA256 = strings.Repeat("9", 64)

	firstWithClaim := withClaims(ops, first)                       // a first record that already has a claim
	extended := bumped(withClaims(ops, first, second))             // append-only extension
	rewrittenVersion := bumped(withClaims(ops, rewritten, second)) // rewrites the stored claim
	dropped := bumped(ops)                                         // drops the stored claim
	repinned := bumped(firstWithClaim)
	repinned.ExpectedCriteriaRevision = pinTestRevision(7)

	cases := []struct {
		name       string
		records    []domaintask.Task
		wantRead   error // nil: the log reads; otherwise the sentinel both stores must report
		followUps  []domaintask.Task
		followWant []error // per follow-up save: nil accepted, else the sentinel
	}{
		{
			name:    "first record with claims is read, extension accepted, rewrite refused",
			records: []domaintask.Task{firstWithClaim}, wantRead: nil,
			followUps:  []domaintask.Task{bumped(bumped(withClaims(ops, first, second))), bumped(withClaims(ops, rewritten)), bumped(ops), bumped(repinned)},
			followWant: []error{nil, ErrNativeOPSResumeClaimImmutable, ErrNativeOPSResumeClaimImmutable, ErrExpectedCriteriaRevisionImmutable},
		},
		{
			name: "first record with claims then extension reads", records: []domaintask.Task{firstWithClaim, extended}, wantRead: nil,
			followUps:  []domaintask.Task{bumped(bumped(extended)), bumped(withClaims(ops, second, first))},
			followWant: []error{nil, ErrNativeOPSResumeClaimImmutable},
		},
		{name: "first record with claims then rewrite is refused", records: []domaintask.Task{firstWithClaim, rewrittenVersion}, wantRead: ErrNativeOPSResumeClaimImmutable},
		{name: "first record with claims then drop is refused", records: []domaintask.Task{firstWithClaim, dropped}, wantRead: ErrNativeOPSResumeClaimImmutable},
		{name: "first record with claims then re-pin is refused", records: []domaintask.Task{firstWithClaim, repinned}, wantRead: ErrExpectedCriteriaRevisionImmutable},
		{
			name: "claim-free history reads and takes the first claim", records: []domaintask.Task{ops, bumped(ops)}, wantRead: nil,
			followUps:  []domaintask.Task{bumped(bumped(withClaims(ops, first))), bumped(bumped(bumped(withClaims(ops, first))))},
			followWant: []error{nil, nil}, // saving the same claim again is an unchanged prefix
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var log []byte
			for _, record := range tc.records {
				log = append(log, marshalLine(t, record)...)
			}
			type outcome struct {
				readErr   error
				followErr []error
			}
			run := func(opts OpenOptions) outcome {
				root := t.TempDir()
				if err := os.WriteFile(filepath.Join(root, stateFilename), log, 0o644); err != nil {
					t.Fatal(err)
				}
				store, err := NewJSONLStoreWithOptions(root, opts)
				if err != nil {
					return outcome{readErr: err}
				}
				defer store.Close()
				ctx := context.Background()
				if _, err := store.GetTask(ctx, ops.TaskID); err != nil {
					return outcome{readErr: err}
				}
				var out outcome
				for _, value := range tc.followUps {
					out.followErr = append(out.followErr, store.SaveTask(ctx, value))
				}
				return out
			}
			legacy, indexed := run(OpenOptions{}), run(OpenOptions{Index: true})
			if tc.wantRead != nil {
				if !errors.Is(legacy.readErr, tc.wantRead) || !errors.Is(indexed.readErr, tc.wantRead) {
					t.Fatalf("read of a log that breaks the pin rules: legacy=%v indexed=%v, want both to wrap %v", legacy.readErr, indexed.readErr, tc.wantRead)
				}
				return
			}
			if legacy.readErr != nil || indexed.readErr != nil {
				t.Fatalf("read of a consistent log: legacy=%v indexed=%v", legacy.readErr, indexed.readErr)
			}
			for i, want := range tc.followWant {
				l, x := legacy.followErr[i], indexed.followErr[i]
				if want == nil && (l != nil || x != nil) || want != nil && (!errors.Is(l, want) || !errors.Is(x, want)) {
					t.Errorf("follow-up save %d: legacy=%v indexed=%v, want %v", i, l, x, want)
				}
			}
		})
	}
}
