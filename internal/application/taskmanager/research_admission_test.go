package taskmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
)

func researchAdmissionDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

type researchAdmissionReadOnlyTaskStore struct {
	Store
	reader researchAdmissionReader
}

func (store *researchAdmissionReadOnlyTaskStore) ExecuteIdempotentGlobalTaskOperation(context.Context, string, string, func(Store) (json.RawMessage, error)) (json.RawMessage, error) {
	return nil, errors.New("read-only test store rejects global writes")
}

func (store *researchAdmissionReadOnlyTaskStore) LookupGlobalTaskOperation(ctx context.Context, operationID, requestHash string) (json.RawMessage, bool, error) {
	return store.reader.LookupGlobalTaskOperation(ctx, operationID, requestHash)
}

func TestEnsureResearchAdmissionReplaysExactReceiptAcrossPolicyRevision(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	ctx := context.Background()
	requestBody := `{"mode":"collect","objective":"private objective https://private.invalid/path"}`
	digest := researchAdmissionDigest(requestBody)
	first, created, err := manager.EnsureResearchAdmission(ctx, "local-owner-1", "opaque-client-key-01", digest, "policy.v1", "collect")
	if err != nil || !created {
		t.Fatalf("first admission=(%+v,%t,%v)", first, created, err)
	}
	manager.store = &researchAdmissionReadOnlyTaskStore{Store: manager.store, reader: manager.store.(researchAdmissionReader)}
	second, created, err := manager.EnsureResearchAdmission(ctx, "local-owner-1", "opaque-client-key-01", digest, "policy.v2", "collect")
	if err != nil || created || second != first {
		t.Fatalf("replay=(%+v,%t,%v), want exact original receipt", second, created, err)
	}
	if err := first.TaskID.Validate(); err != nil {
		t.Fatalf("task ID: %v", err)
	}
	if err := first.InitialRunID.Validate(); err != nil {
		t.Fatalf("run ID: %v", err)
	}
	if err := first.TraceID.Validate(); err != nil {
		t.Fatalf("trace ID: %v", err)
	}
	if err := first.RequestID.Validate(); err != nil {
		t.Fatalf("request ID: %v", err)
	}
	if first.PolicyRevision != "policy.v1" || first.RequestDigest != digest || first.CreatedAt.IsZero() {
		t.Fatalf("receipt=%+v", first)
	}
	task, err := manager.Get(ctx, first.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := manager.Context(ctx, first.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := manager.GetRun(ctx, first.InitialRunID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Route != domaintask.RouteResearch || task.OwnerID != "local-owner-1" || task.Assignee != "local-owner-1" || task.ResearchBinding == nil || task.ResearchBinding.Validate(task) != nil || run.TaskID != task.TaskID || run.Assignee != "local-owner-1" || run.TraceID != first.TraceID {
		t.Fatalf("Task/Run binding mismatch: Task=%+v Run=%+v", task, run)
	}
	storedTaskState, err := json.Marshal(struct {
		Task   domaintask.Task              `json:"task"`
		Shared domaintask.SharedRoleContext `json:"shared"`
	}{task, shared})
	if err != nil {
		t.Fatal(err)
	}
	for _, privateValue := range []string{requestBody, "private objective", "private.invalid/path", digest, "opaque-client-key-01"} {
		if strings.Contains(string(storedTaskState), privateValue) {
			t.Fatalf("private request material %q leaked into Task/context: %s", privateValue, storedTaskState)
		}
	}
	if task.Title != researchAdmissionTitle {
		t.Fatalf("private request data leaked into Task/context: Task=%+v Shared=%+v", task, shared)
	}
	assertStoreCounts(t, manager, 1, 1)
}

func TestResearchIndexAdmissionLookupIsReadOnlyAcrossManagerRestart(t *testing.T) {
	physicalTempDir, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatalf("resolve test temp directory: %v", err)
	}
	t.Setenv("TMPDIR", physicalTempDir)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "tasks")
	store, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	manager := New(store, DefaultParallelLimits())
	key, digest := "opaque-index-key-01", researchAdmissionDigest("canonical-index-request")
	first, created, err := manager.EnsureResearchAdmission(ctx, "local-owner-1", key, digest, "policy.v1", "index_build")
	if err != nil || !created {
		t.Fatalf("index admission=(%+v,%t,%v)", first, created, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted := New(reopened, DefaultParallelLimits())
	second, found, err := restarted.LookupResearchAdmission(ctx, "local-owner-1", key, digest, "index_build")
	if err != nil || !found || second != first {
		t.Fatalf("restarted index lookup=(%+v,%t,%v), want original receipt", second, found, err)
	}
	if _, _, err := restarted.EnsureResearchAdmission(ctx, "local-owner-1", key, researchAdmissionDigest("different-index-request"), "policy.v2", "index_build"); !errors.Is(err, taskpersistence.ErrTaskOperationConflict) {
		t.Fatalf("changed request reused index key: %v", err)
	}
	if _, _, err := restarted.LookupResearchAdmission(ctx, "local-owner-1", key, digest, "collect"); !errors.Is(err, taskpersistence.ErrTaskOperationConflict) {
		t.Fatalf("index operation key crossed admission kind: %v", err)
	}
}

func TestResearchAdmissionSameKeyDifferentBodyConflictsAndDifferentOwnerIsolated(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	ctx := context.Background()
	key := "opaque-client-key-02"
	first, _, err := manager.EnsureResearchAdmission(ctx, "local-owner-1", key, researchAdmissionDigest("body-one"), "policy.v1", "collect")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.EnsureResearchAdmission(ctx, "local-owner-1", key, researchAdmissionDigest("body-two"), "policy.v1", "collect"); !errors.Is(err, taskpersistence.ErrTaskOperationConflict) {
		t.Fatalf("changed body error=%v, want operation conflict", err)
	}
	if _, _, err := manager.EnsureResearchAdmission(ctx, "local-owner-1", key, researchAdmissionDigest("body-one"), "policy.v1", "investigate"); !errors.Is(err, taskpersistence.ErrTaskOperationConflict) {
		t.Fatalf("changed mode error=%v, want operation conflict", err)
	}
	if _, err := manager.Succeed(ctx, first.TaskID, "finished"); err != nil {
		t.Fatal(err)
	}
	other, created, err := manager.EnsureResearchAdmission(ctx, "local-owner-2", key, researchAdmissionDigest("body-one"), "policy.v1", "collect")
	if err != nil || !created || other.TaskID == first.TaskID {
		t.Fatalf("other owner admission=(%+v,%t,%v)", other, created, err)
	}
	assertStoreCounts(t, manager, 2, 2)
}

func TestLookupResearchAdmissionIsReadOnlyAndValidatesIdentity(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	ctx := context.Background()
	key, digest := "opaque-client-key-03", researchAdmissionDigest("body")
	if got, found, err := manager.LookupResearchAdmission(ctx, "local-owner-1", key, digest, "collect"); err != nil || found || got.TaskID != "" {
		t.Fatalf("absent lookup=(%+v,%t,%v)", got, found, err)
	}
	first, _, err := manager.EnsureResearchAdmission(ctx, "local-owner-1", key, digest, "policy.v1", "collect")
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := manager.LookupResearchAdmission(ctx, "local-owner-1", key, digest, "collect")
	if err != nil || !found || got != first {
		t.Fatalf("lookup=(%+v,%t,%v), want receipt %+v", got, found, err, first)
	}
	if _, _, err := manager.LookupResearchAdmission(ctx, "local-owner-1", key, researchAdmissionDigest("changed"), "collect"); !errors.Is(err, taskpersistence.ErrTaskOperationConflict) {
		t.Fatalf("changed digest lookup error=%v, want conflict", err)
	}
	assertStoreCounts(t, manager, 1, 1)
}

func TestResearchAdmissionRollbackLeavesNoTaskRunOrReceipt(t *testing.T) {
	limits := DefaultParallelLimits()
	limits.Global = 1
	manager := newCreateAndStartManager(t, limits)
	ctx := context.Background()
	if _, _, err := manager.EnsureResearchAdmission(ctx, "local-owner-1", "opaque-client-key-04a", researchAdmissionDigest("one"), "policy.v1", "collect"); err != nil {
		t.Fatal(err)
	}
	key, digest := "opaque-client-key-04b", researchAdmissionDigest("blocked")
	if _, _, err := manager.EnsureResearchAdmission(ctx, "local-owner-2", key, digest, "policy.v1", "collect"); !errors.Is(err, ErrParallelLimit) {
		t.Fatalf("capacity refusal=%v, want ErrParallelLimit", err)
	}
	if _, found, err := manager.LookupResearchAdmission(ctx, "local-owner-2", key, digest, "collect"); err != nil || found {
		t.Fatalf("failed admission left receipt: found=%t err=%v", found, err)
	}
	assertStoreCounts(t, manager, 1, 1)
}

func TestResearchAdmissionConcurrentAndDurableReplayCreatesOnce(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "tasks")
	store, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	manager := New(store, DefaultParallelLimits())
	digest, key := researchAdmissionDigest("stable request"), "opaque-client-key-05"
	const workers = 12
	type result struct {
		receipt ResearchAdmissionReceipt
		created bool
		err     error
	}
	results := make(chan result, workers)
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			receipt, created, err := manager.EnsureResearchAdmission(ctx, "local-owner-1", key, digest, "policy.v1", "investigate")
			results <- result{receipt, created, err}
		}()
	}
	group.Wait()
	close(results)
	var expected ResearchAdmissionReceipt
	createdCount := 0
	for got := range results {
		if got.err != nil {
			t.Fatal(got.err)
		}
		if expected.TaskID == "" {
			expected = got.receipt
		}
		if got.receipt != expected {
			t.Fatalf("concurrent receipt mismatch: %+v != %+v", got.receipt, expected)
		}
		if got.created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("created count=%d, want 1", createdCount)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	manager = New(store, DefaultParallelLimits())
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	replayed, created, err := manager.EnsureResearchAdmission(ctx, "local-owner-1", key, digest, "policy.v2", "investigate")
	if err != nil || created || replayed != expected {
		t.Fatalf("durable replay=(%+v,%t,%v), want %+v", replayed, created, err, expected)
	}
}

func TestResearchAdmissionRejectsMalformedInputsWithoutWrites(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	validDigest := researchAdmissionDigest("body")
	cases := []struct{ name, owner, key, digest, revision, mode string }{
		{"empty owner", "", "valid-client-key-06", validDigest, "policy.v1", "collect"},
		{"untrimmed owner", " local-owner-1 ", "valid-client-key-06", validDigest, "policy.v1", "collect"},
		{"empty key", "local-owner-1", "", validDigest, "policy.v1", "collect"},
		{"digest invalid", "local-owner-1", "valid-client-key-06", "ABC", "policy.v1", "collect"},
		{"revision invalid", "local-owner-1", "valid-client-key-06", validDigest, "", "collect"},
		{"mode open", "local-owner-1", "valid-client-key-06", validDigest, "policy.v1", "arbitrary"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := manager.EnsureResearchAdmission(context.Background(), tc.owner, tc.key, tc.digest, tc.revision, tc.mode); err == nil {
				t.Fatal("malformed input accepted")
			}
		})
	}
	if _, _, err := manager.EnsureResearchAdmission(nil, "local-owner-1", "valid-client-key-06", validDigest, "policy.v1", "collect"); err == nil {
		t.Fatal("nil context accepted")
	}
	assertStoreCounts(t, manager, 0, 0)
}

func TestResearchAdmissionReceiptJSONDoesNotContainClientKey(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	key := "secret-correlation-material-07"
	receipt, _, err := manager.EnsureResearchAdmission(context.Background(), "local-owner-1", key, researchAdmissionDigest("body"), "policy.v1", "search_only")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), key) {
		t.Fatal("opaque client key was persisted in receipt")
	}
}

func TestResearchAdmissionAcceptsRefreshMode(t *testing.T) {
	manager := newCreateAndStartManager(t, DefaultParallelLimits())
	if _, created, err := manager.EnsureResearchAdmission(context.Background(), "local-owner-1", "opaque-client-key-refresh", researchAdmissionDigest("refresh"), "policy.v1", "refresh"); err != nil || !created {
		t.Fatalf("refresh admission created=%t err=%v", created, err)
	}
}
