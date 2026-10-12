package taskmanager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Failure knowledge: docs/調査/*Run無しqueued_Taskの滞留*_FailureKnowledge.md.
// These tests run the reaper against real Task stores, with and without the ID
// index, and check what it reads, writes and refuses.

// reapWorld is a store seeded like a production store that holds stale queued
// Tasks: one candidate of every allowlisted kind plus one Task for each way a
// queued Task is not a candidate.
type reapWorld struct {
	root string
	// candidates are the 12 allowlisted Tasks, by kind.
	candidates map[ReapKind]modulecore.TaskID
	userOrigin modulecore.TaskID
	// afterCutoff was created after the 2026-10-12T02:39Z cutoff; young is 12h old.
	afterCutoff modulecore.TaskID
	young       modulecore.TaskID
	hasRun      modulecore.TaskID
	hasParent   modulecore.TaskID
	referenced  modulecore.TaskID
	// unrelated holds the Tasks no rule matches (other, root, child).
	unrelated []modulecore.TaskID
}

const reapWorldQueuedPool = 21

func openReapStore(t *testing.T, root string, indexed bool) *taskpersistence.JSONLStore {
	t.Helper()
	store, err := taskpersistence.NewJSONLStoreWithOptions(root, taskpersistence.OpenOptions{Index: indexed, Persist: indexed})
	if err != nil {
		t.Fatalf("open task store: %v", err)
	}
	return store
}

func newReapManager(store Store) *Manager {
	manager := New(store, DefaultParallelLimits())
	manager.now = func() time.Time { return reapTestNow }
	return manager
}

func seedReapWorld(t *testing.T, indexed bool) reapWorld {
	t.Helper()
	root := filepath.Join(t.TempDir(), "tasks")
	store := openReapStore(t, root, indexed)
	manager := newReapManager(store)
	ctx := context.Background()
	world := reapWorld{root: root, candidates: map[ReapKind]modulecore.TaskID{}}
	create := func(task domaintask.Task) modulecore.TaskID {
		t.Helper()
		created, err := manager.Create(ctx, task, domaintask.SharedRoleContext{CurrentPlan: "plan text that must never be printed"})
		if err != nil {
			t.Fatalf("seed %q: %v", task.Title, err)
		}
		return created.TaskID
	}
	shaped := func(shape reapShape, createdAt time.Time) domaintask.Task {
		task := reapTaskOf(shape, createdAt)
		task.TaskID = "" // the owner allocates it
		return task
	}
	for i, shape := range reapShapes {
		world.candidates[shape.kind] = create(shaped(shape, reapTestOld.Add(time.Duration(i)*time.Minute)))
	}
	idle := reapShapes[0]
	user := shaped(idle, reapTestOld)
	user.OriginSessionID = modulecore.NewSessionID()
	world.userOrigin = create(user)
	world.afterCutoff = create(shaped(idle, time.Date(2026, 10, 12, 5, 0, 0, 0, time.UTC)))
	world.young = create(shaped(idle, reapTestNow.Add(-12*time.Hour)))
	world.hasRun = create(shaped(idle, reapTestOld))
	attachClosedRun(t, store, world.hasRun)

	root1 := create(domaintask.Task{Title: "root work", Route: domaintask.RouteCode, CreatedAt: reapTestOld})
	withParent := shaped(idle, reapTestOld)
	withParent.ParentTaskID = root1
	world.hasParent = create(withParent)
	world.referenced = create(shaped(idle, reapTestOld))
	child := create(domaintask.Task{Title: "child work", Route: domaintask.RouteCode, ParentTaskID: world.referenced, CreatedAt: reapTestOld})
	other := create(domaintask.Task{Title: "other work", Route: domaintask.RouteGeneral, CreatedAt: reapTestOld})
	world.unrelated = []modulecore.TaskID{root1, child, other}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return world
}

// attachClosedRun gives a queued Task a closed Run, as a Run-bearing queued
// Task would have.
func attachClosedRun(t *testing.T, store *taskpersistence.JSONLStore, taskID modulecore.TaskID) {
	t.Helper()
	generation, err := store.WriterGeneration()
	if err != nil {
		t.Fatal(err)
	}
	completed := reapTestOld.Add(time.Minute)
	err = store.SaveRun(context.Background(), domaintask.Run{
		WriterGeneration: generation, RunID: modulecore.NewRunID(), TaskID: taskID, StartReason: domaintask.RunStartReasonFirst,
		Status: domaintask.RunStatusInterrupted, StartedAt: reapTestOld, CompletedAt: &completed,
	})
	if err != nil {
		t.Fatalf("attach run: %v", err)
	}
}

// snapshotTree hashes every regular file under root (relative path -> digest),
// so a test can show that nothing was created, changed or removed.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		sum := sha256.Sum256(data)
		files[filepath.ToSlash(relative)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func requireSameTree(t *testing.T, before, after map[string]string) {
	t.Helper()
	var diffs []string
	for name, digest := range before {
		if got, ok := after[name]; !ok {
			diffs = append(diffs, "removed "+name)
		} else if got != digest {
			diffs = append(diffs, "changed "+name)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			diffs = append(diffs, "created "+name)
		}
	}
	sort.Strings(diffs)
	if len(diffs) > 0 {
		t.Fatalf("the store changed: %s", strings.Join(diffs, ", "))
	}
}

func eachReapStoreMode(t *testing.T, run func(t *testing.T, indexed bool)) {
	t.Helper()
	for _, mode := range []struct {
		name    string
		indexed bool
	}{{"plain store", false}, {"indexed store", true}} {
		t.Run(mode.name, func(t *testing.T) { run(t, mode.indexed) })
	}
}

func reapRequest(apply bool) ReapRequest {
	return ReapRequest{CreatedBefore: reapTestCutoff, MinAge: DefaultReapMinAge, BatchSize: 5, Apply: apply}
}

func TestReapDryRunReportsCandidatesByKindAndExclusionsByReasonAndWritesNothing(t *testing.T) {
	eachReapStoreMode(t, func(t *testing.T, indexed bool) {
		world := seedReapWorld(t, indexed)
		before := snapshotTree(t, world.root)

		var reader *taskpersistence.JSONLStore
		var err error
		if indexed {
			reader, err = taskpersistence.NewIndexedJSONLReader(world.root)
		} else {
			reader, err = taskpersistence.NewJSONLReader(world.root)
		}
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		report, err := newReapManager(reader).ReapStaleQueued(context.Background(), reapRequest(false))
		if err != nil {
			t.Fatalf("dry-run: %v", err)
		}
		if report.Mode != "dry_run" || report.Candidates != 12 || report.QueuedPool != reapWorldQueuedPool || len(report.Batches) != 0 || report.Applied != 0 {
			t.Fatalf("report = %+v", report)
		}
		for _, shape := range reapShapes {
			if report.CandidatesByKind[shape.kind] != 1 {
				t.Errorf("candidates of %s = %d, want 1", shape.kind, report.CandidatesByKind[shape.kind])
			}
		}
		want := map[ReapExclusion]int{
			ExcludedUserOrigin: 1, ExcludedKindNotAllowlisted: 3, ExcludedCreatedAfterCutoff: 2,
			ExcludedYoungerThanMinAge: 0, ExcludedHasRelations: 1, ExcludedReferenced: 1, ExcludedHasRun: 1,
		}
		for reason, count := range want {
			if report.ExcludedByReason[reason] != count {
				t.Errorf("excluded %s = %d, want %d", reason, report.ExcludedByReason[reason], count)
			}
		}
		if report.Excluded != 9 || report.Candidates+report.Excluded != report.QueuedPool {
			t.Errorf("excluded = %d, candidates+excluded must equal the pool %d", report.Excluded, report.QueuedPool)
		}
		if len(report.CandidateSetSHA256) != 64 {
			t.Errorf("candidate set digest = %q", report.CandidateSetSHA256)
		}
		after := snapshotTree(t, world.root)
		requireSameTree(t, before, after)
	})
}

func TestReapMinimumAgeExcludesYoungTasksWhenTheCutoffAllowsThem(t *testing.T) {
	world := seedReapWorld(t, true)
	reader, err := taskpersistence.NewIndexedJSONLReader(world.root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	request := reapRequest(false)
	request.CreatedBefore = reapTestNow.Add(time.Hour)
	report, err := newReapManager(reader).ReapStaleQueued(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	// The 12-hour-old Task is now blocked by the minimum age only; the one made
	// after the original cutoff (8 days old) is a candidate.
	if report.ExcludedByReason[ExcludedYoungerThanMinAge] != 1 || report.ExcludedByReason[ExcludedCreatedAfterCutoff] != 0 || report.Candidates != 13 {
		t.Fatalf("report = %+v", report)
	}
}

func TestReapApplyCancelsOnlyCandidatesWithAFixedReasonAndCreatesNoNotification(t *testing.T) {
	eachReapStoreMode(t, func(t *testing.T, indexed bool) {
		world := seedReapWorld(t, indexed)
		store := openReapStore(t, world.root, indexed)
		defer store.Close()
		manager := newReapManager(store)
		ctx := context.Background()
		notificationsBefore, err := store.ListNotifications(ctx, 0, false)
		if err != nil {
			t.Fatal(err)
		}

		report, err := manager.ReapStaleQueued(ctx, reapRequest(true))
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		// 12 candidates in batches of 5: 5 + 5 + 2.
		if report.Applied != 12 || report.Skipped != 0 || len(report.Batches) != 3 {
			t.Fatalf("report = %+v", report)
		}
		sizes := []int{report.Batches[0].Tasks, report.Batches[1].Tasks, report.Batches[2].Tasks}
		if sizes[0] != 5 || sizes[1] != 5 || sizes[2] != 2 {
			t.Fatalf("batch sizes = %v", sizes)
		}
		seenOperations := map[string]bool{}
		for _, batch := range report.Batches {
			if batch.Cancelled != batch.Tasks || batch.Replayed || seenOperations[batch.OperationID] {
				t.Fatalf("batch = %+v", batch)
			}
			seenOperations[batch.OperationID] = true
			receipt, err := store.LookupTaskOperation(ctx, batch.OperationID)
			if err != nil || receipt.Scope != taskpersistence.TaskOperationScopeGlobal {
				t.Fatalf("receipt of %s = %+v, %v", batch.OperationID, receipt, err)
			}
		}

		for kind, taskID := range world.candidates {
			task, err := store.GetTask(ctx, taskID)
			if err != nil {
				t.Fatal(err)
			}
			if task.Status != domaintask.StatusCancelled || task.Summary != reapReason(kind) {
				t.Errorf("candidate %s: status=%s summary=%q", kind, task.Status, task.Summary)
			}
			if task.FinishedAt == nil || !task.FinishedAt.Equal(reapTestNow) || !task.UpdatedAt.Equal(reapTestNow) {
				t.Errorf("candidate %s: finished_at=%v updated_at=%v", kind, task.FinishedAt, task.UpdatedAt)
			}
			if domaintask.ShouldNotify(task) {
				t.Errorf("a cancelled reaped Task (%s) must not notify", kind)
			}
			runs, err := store.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID})
			if err != nil || len(runs) != 0 {
				t.Errorf("runs of %s = %d, %v", kind, len(runs), err)
			}
		}
		untouched := append([]modulecore.TaskID{world.userOrigin, world.afterCutoff, world.young, world.hasRun, world.hasParent, world.referenced}, world.unrelated...)
		for _, taskID := range untouched {
			task, err := store.GetTask(ctx, taskID)
			if err != nil {
				t.Fatal(err)
			}
			if task.Status != domaintask.StatusQueued || task.Summary != "" || task.FinishedAt != nil {
				t.Errorf("a Task that is not a candidate changed: status=%s summary=%q", task.Status, task.Summary)
			}
		}
		notificationsAfter, err := store.ListNotifications(ctx, 0, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(notificationsAfter) != len(notificationsBefore) {
			t.Fatalf("notifications %d -> %d: cancelling must not notify", len(notificationsBefore), len(notificationsAfter))
		}
		for _, taskID := range world.candidates {
			shared, err := store.GetContext(ctx, taskID)
			if err != nil || shared.CurrentPlan != "plan text that must never be printed" {
				t.Fatalf("context of a cancelled Task changed: %+v, %v", shared, err)
			}
		}
	})
}

func TestReapReportNeverCarriesRecordContent(t *testing.T) {
	world := seedReapWorld(t, true)
	store := openReapStore(t, world.root, true)
	defer store.Close()
	report, err := newReapManager(store).ReapStaleQueued(context.Background(), reapRequest(true))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, secret := range []string{"IdleChat", "Memory Promotion", "plan text", "root work", "child work", "other work", "actor-a", "user-a"} {
		if strings.Contains(text, secret) {
			t.Fatalf("the report contains record content %q: %s", secret, text)
		}
	}
	for _, taskID := range world.candidates {
		if strings.Contains(text, string(taskID)) {
			t.Fatalf("the report names a Task ID: %s", text)
		}
	}
}

func TestReapRunTwiceCancelsEachTaskExactlyOnce(t *testing.T) {
	eachReapStoreMode(t, func(t *testing.T, indexed bool) {
		world := seedReapWorld(t, indexed)
		store := openReapStore(t, world.root, indexed)
		defer store.Close()
		manager := newReapManager(store)
		ctx := context.Background()
		first, err := manager.ReapStaleQueued(ctx, reapRequest(true))
		if err != nil || first.Applied != 12 {
			t.Fatalf("first apply = %+v, %v", first, err)
		}
		stateBefore := readFileBytes(t, filepath.Join(world.root, "task_state.jsonl"))
		second, err := manager.ReapStaleQueued(ctx, reapRequest(true))
		if err != nil {
			t.Fatal(err)
		}
		if second.Candidates != 0 || second.Applied != 0 || len(second.Batches) != 0 {
			t.Fatalf("second apply = %+v, want zero candidates", second)
		}
		if !bytes.Equal(stateBefore, readFileBytes(t, filepath.Join(world.root, "task_state.jsonl"))) {
			t.Fatal("the second run appended to the log")
		}
		requireOneCancelledLineEach(t, world.root, world.candidates)
	})
}

func readFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// requireOneCancelledLineEach counts the stored state lines of every Task: a
// Task cancelled exactly once has exactly one cancelled line.
func requireOneCancelledLineEach(t *testing.T, root string, tasks map[ReapKind]modulecore.TaskID) {
	t.Helper()
	data := readFileBytes(t, filepath.Join(root, "task_state.jsonl"))
	for kind, taskID := range tasks {
		cancelled := 0
		for _, line := range bytes.Split(data, []byte("\n")) {
			if bytes.Contains(line, []byte(`"task_id":"`+string(taskID)+`"`)) && bytes.Contains(line, []byte(`"status":"cancelled"`)) {
				cancelled++
			}
		}
		if cancelled != 1 {
			t.Errorf("%s has %d cancelled lines, want exactly 1", kind, cancelled)
		}
	}
}

func TestReapRevalidatesEveryTaskInsideTheBatchTransaction(t *testing.T) {
	eachReapStoreMode(t, func(t *testing.T, indexed bool) {
		world := seedReapWorld(t, indexed)
		store := openReapStore(t, world.root, indexed)
		defer store.Close()
		manager := newReapManager(store)
		ctx := context.Background()
		request := reapRequest(true)
		plan, err := manager.planReap(ctx, request.policy())
		if err != nil || len(plan.candidates) != 12 {
			t.Fatalf("plan: %d candidates, %v", len(plan.candidates), err)
		}

		// The state moves after the plan (the dry-run): one candidate starts,
		// one gains a Run, one becomes the parent of a new Task.
		started := world.candidates[ReapKindMemoryPromotion]
		if _, err := manager.Start(ctx, started); err != nil {
			t.Fatalf("start after plan: %v", err)
		}
		withRun := world.candidates[ReapKindIdleChatWordTopic]
		attachClosedRun(t, store, withRun)
		parent := world.candidates[ReapKindDCISearch]
		if _, err := manager.Create(ctx, domaintask.Task{Title: "late child", Route: domaintask.RouteCode, ParentTaskID: parent, CreatedAt: reapTestOld}, domaintask.SharedRoleContext{}); err != nil {
			t.Fatal(err)
		}

		report := newReapReport(request, plan, 0)
		report, err = manager.executeReapPlan(ctx, request, plan, report)
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		if report.Applied != 9 || report.Skipped != 3 {
			t.Fatalf("applied=%d skipped=%d, want 9 and 3: %+v", report.Applied, report.Skipped, report.Batches)
		}
		skipped := map[ReapExclusion]int{}
		for _, batch := range report.Batches {
			for reason, count := range batch.SkippedByReason {
				skipped[reason] += count
			}
		}
		if skipped[ExcludedNotQueued] != 1 || skipped[ExcludedHasRun] != 1 || skipped[ExcludedReferenced] != 1 {
			t.Fatalf("skipped by reason = %v", skipped)
		}
		for name, taskID := range map[string]modulecore.TaskID{"started": started, "with run": withRun, "parent": parent} {
			task, err := store.GetTask(ctx, taskID)
			if err != nil {
				t.Fatal(err)
			}
			if task.Status == domaintask.StatusCancelled {
				t.Errorf("the %s Task was cancelled although it changed after the plan", name)
			}
		}
	})
}

func TestReapReplaysAStoredReceiptWithoutRunningTheBatchAgain(t *testing.T) {
	world := seedReapWorld(t, true)
	store := openReapStore(t, world.root, true)
	defer store.Close()
	manager := newReapManager(store)
	ctx := context.Background()
	request := reapRequest(true)
	plan, err := manager.planReap(ctx, request.policy())
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.executeReapPlan(ctx, request, plan, newReapReport(request, plan, 0))
	if err != nil || first.Applied != 12 {
		t.Fatalf("first execute = %+v, %v", first, err)
	}
	logBefore := snapshotTree(t, world.root)
	second, err := manager.executeReapPlan(ctx, request, plan, newReapReport(request, plan, 0))
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	if second.Applied != 0 || second.AlreadyApplied != 12 || len(second.Batches) != 3 {
		t.Fatalf("second execute = %+v", second)
	}
	for _, batch := range second.Batches {
		if !batch.Replayed {
			t.Fatalf("batch %d ran again", batch.Ordinal)
		}
	}
	// The receipt answered: no record was appended.
	after := snapshotTree(t, world.root)
	for _, name := range []string{"task_state.jsonl", "task_run.jsonl", "task_context.jsonl", "task_notifications.jsonl", "task_operation_receipts.jsonl"} {
		if logBefore[name] != after[name] {
			t.Errorf("%s changed on replay", name)
		}
	}
}

// uncertainOwner makes the failing batch end with ErrCommitUncertain, either
// after the real commit (the caller cannot know) or without any commit.
type uncertainOwner struct {
	*taskpersistence.JSONLStore
	failOnCall      int
	commitBeforeErr bool
	calls           int
}

func (o *uncertainOwner) ExecuteIdempotentGlobalTaskOperation(ctx context.Context, operationID, requestHash string, fn func(domaintask.Store) (json.RawMessage, error)) (json.RawMessage, error) {
	o.calls++
	if o.calls != o.failOnCall {
		return o.JSONLStore.ExecuteIdempotentGlobalTaskOperation(ctx, operationID, requestHash, fn)
	}
	if o.commitBeforeErr {
		if _, err := o.JSONLStore.ExecuteIdempotentGlobalTaskOperation(ctx, operationID, requestHash, fn); err != nil {
			return nil, err
		}
	}
	return nil, errors.Join(jsonlbatch.ErrCommitUncertain, jsonlbatch.ErrRecoveryRequired)
}

func TestReapRerunAfterAnUncertainCommitCancelsEachTaskExactlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name            string
		commitBeforeErr bool
	}{
		{"the uncertain batch was committed", true},
		{"the uncertain batch was not committed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			world := seedReapWorld(t, true)
			store := openReapStore(t, world.root, true)
			defer store.Close()
			ctx := context.Background()
			flaky := &uncertainOwner{JSONLStore: store, failOnCall: 2, commitBeforeErr: tc.commitBeforeErr}
			first, err := newReapManager(flaky).ReapStaleQueued(ctx, reapRequest(true))
			if !errors.Is(err, jsonlbatch.ErrCommitUncertain) {
				t.Fatalf("first run error = %v, want ErrCommitUncertain", err)
			}
			if len(first.Batches) != 1 || first.Applied != 5 {
				t.Fatalf("first run reached %+v", first)
			}

			rerun, err := newReapManager(store).ReapStaleQueued(ctx, reapRequest(true))
			if err != nil {
				t.Fatalf("re-run: %v", err)
			}
			// The batch whose outcome was uncertain is counted by neither run: the
			// first could not know, and the second must not see it again.
			unknown := 0
			if tc.commitBeforeErr {
				unknown = 5
			}
			if first.Applied+unknown+rerun.Applied != 12 {
				t.Fatalf("applied %d, %d uncertain, then %d: want 12 in all", first.Applied, unknown, rerun.Applied)
			}
			if tc.commitBeforeErr && rerun.Candidates != 2 {
				t.Fatalf("the committed uncertain batch must not be a candidate again: %d candidates", rerun.Candidates)
			}
			if !tc.commitBeforeErr && rerun.Candidates != 7 {
				t.Fatalf("the uncommitted batch must be a candidate again: %d candidates", rerun.Candidates)
			}
			requireOneCancelledLineEach(t, world.root, world.candidates)
			final, err := newReapManager(store).ReapStaleQueued(ctx, reapRequest(true))
			if err != nil || final.Candidates != 0 {
				t.Fatalf("third run = %+v, %v", final, err)
			}
		})
	}
}

func TestReapOperationIdentity(t *testing.T) {
	policy := reapTestPolicy()
	ids := []reapCandidate{{TaskID: "tsk_b"}, {TaskID: "tsk_a"}, {TaskID: "tsk_c"}}
	reordered := []reapCandidate{ids[2], ids[0], ids[1]}
	id1, hash1 := reapOperationIdentity(policy, ids)
	id2, hash2 := reapOperationIdentity(policy, reordered)
	if id1 != id2 || hash1 != hash2 {
		t.Fatal("the order of a batch must not change its identity")
	}
	other := policy
	other.CreatedBefore = policy.CreatedBefore.Add(time.Hour)
	id3, hash3 := reapOperationIdentity(other, ids)
	if id3 != id1 || hash3 == hash1 {
		t.Fatalf("another cutoff must keep the operation ID and change the request hash (conflict, not replay)")
	}
	id4, _ := reapOperationIdentity(policy, ids[:2])
	if id4 == id1 {
		t.Fatal("another Task set must be another operation")
	}
	if !strings.HasPrefix(id1, "reap-queued-") || len(id1) > 128 || len(hash1) != 64 {
		t.Fatalf("id=%q hash=%q", id1, hash1)
	}
}

func TestReapRequestValidation(t *testing.T) {
	world := seedReapWorld(t, false)
	store := openReapStore(t, world.root, false)
	defer store.Close()
	manager := newReapManager(store)
	for name, request := range map[string]ReapRequest{
		"no cutoff":          {MinAge: time.Hour, BatchSize: 5},
		"negative min age":   {CreatedBefore: reapTestCutoff, MinAge: -time.Second, BatchSize: 5},
		"zero batch size":    {CreatedBefore: reapTestCutoff, BatchSize: 0},
		"batch size too big": {CreatedBefore: reapTestCutoff, BatchSize: MaxReapBatchSize + 1},
	} {
		if _, err := manager.ReapStaleQueued(context.Background(), request); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestReapApplyNeedsAStoreWithGlobalOperations(t *testing.T) {
	world := seedReapWorld(t, false)
	store := openReapStore(t, world.root, false)
	defer store.Close()
	// Only the Store contract is visible: no ExecuteIdempotentGlobalTaskOperation.
	manager := newReapManager(struct{ Store }{store})
	dry, err := manager.ReapStaleQueued(context.Background(), reapRequest(false))
	if err != nil || dry.Candidates != 12 {
		t.Fatalf("dry-run needs no owner capability: %+v, %v", dry, err)
	}
	if _, err := manager.ReapStaleQueued(context.Background(), reapRequest(true)); !errors.Is(err, ErrReapUnsupportedStore) {
		t.Fatalf("apply error = %v, want ErrReapUnsupportedStore", err)
	}
}

func TestReapStopsOnACancelledContextAndWritesNothing(t *testing.T) {
	world := seedReapWorld(t, true)
	store := openReapStore(t, world.root, true)
	defer store.Close()
	before := snapshotTree(t, world.root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newReapManager(store).ReapStaleQueued(ctx, reapRequest(true)); err == nil {
		t.Fatal("a cancelled context must stop the reaper")
	}
	// The writer holds its lock; only the log files are compared.
	after := snapshotTree(t, world.root)
	for _, name := range []string{"task_state.jsonl", "task_run.jsonl", "task_context.jsonl", "task_notifications.jsonl", "task_operation_receipts.jsonl"} {
		if before[name] != after[name] {
			t.Errorf("%s changed", name)
		}
	}
}
