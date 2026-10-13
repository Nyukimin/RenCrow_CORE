package action

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// The snapshot tests compare a store opened with OpenOptions.Index against one
// opened without it, the full read that has always been the definition of what
// the logs mean. Both run the same transactions; the full read is the oracle.

var supportBase = time.Date(2026, 10, 13, 1, 0, 0, 0, time.UTC)

func openFull(t testing.TB, root string) *JSONLStore {
	t.Helper()
	store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: false})
	if err != nil {
		t.Fatalf("open store without the snapshot: %v", err)
	}
	return store
}

func openIndexed(t testing.TB, root string) *JSONLStore {
	t.Helper()
	store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true})
	if err != nil {
		t.Fatalf("open store with the snapshot: %v", err)
	}
	return store
}

// requireIndexed fails unless the store keeps a snapshot, and returns it.
func requireIndexed(t testing.TB, store *JSONLStore) *snapshot {
	t.Helper()
	if store.idx == nil {
		t.Fatal("the store was opened with Index but keeps no snapshot")
	}
	return store.idx
}

// newOpenPair is a first Action and its running first Attempt, the shape
// Manager.CreateAction saves.
func newOpenPair(taskID modulecore.TaskID, runID modulecore.RunID, kind domainaction.Kind, name string, at time.Time) (domainaction.Action, domainaction.Attempt) {
	attemptID := modulecore.NewAttemptID()
	action := domainaction.Action{
		ActionID: modulecore.NewActionID(), TaskID: taskID, RunID: runID, Kind: kind, Name: name,
		Status: domainaction.StatusOpen, CurrentAttemptID: attemptID, CreatedAt: at, UpdatedAt: at,
	}
	attempt := domainaction.Attempt{
		AttemptID: attemptID, ActionID: action.ActionID, StartReason: domainaction.AttemptStartReasonFirst,
		Status: domainaction.AttemptStatusRunning, StartedAt: at,
	}
	return action, attempt
}

// closePair returns the terminal versions of an open pair.
func closePair(t testing.TB, action domainaction.Action, attempt domainaction.Attempt, at time.Time, actionStatus domainaction.Status, attemptStatus domainaction.AttemptStatus) (domainaction.Action, domainaction.Attempt) {
	t.Helper()
	closedAttempt, err := attempt.Close(attemptStatus, at, "done")
	if err != nil {
		t.Fatal(err)
	}
	closedAction, err := action.Close(actionStatus, at, "done")
	if err != nil {
		t.Fatal(err)
	}
	return closedAction, closedAttempt
}

func savePair(ctx context.Context, store *JSONLStore, action domainaction.Action, attempt domainaction.Attempt) error {
	return store.Transaction(ctx, func(tx domainaction.Store) error {
		if err := tx.SaveAction(ctx, action); err != nil {
			return err
		}
		return tx.SaveAttempt(ctx, attempt)
	})
}

// seedPairs commits n open pairs, one transaction each, spread over a few Tasks
// and Runs so that the per-Run and per-Task lookups have more than one hit.
func seedPairs(t testing.TB, store *JSONLStore, n int) ([]domainaction.Action, []domainaction.Attempt) {
	t.Helper()
	ctx := context.Background()
	tasks := []modulecore.TaskID{modulecore.NewTaskID(), modulecore.NewTaskID()}
	runs := []modulecore.RunID{modulecore.NewRunID(), modulecore.NewRunID(), modulecore.NewRunID()}
	var actions []domainaction.Action
	var attempts []domainaction.Attempt
	for i := 0; i < n; i++ {
		action, attempt := newOpenPair(tasks[i%len(tasks)], runs[i%len(runs)], domainaction.KindTool, fmt.Sprintf("n%d", i%4), supportBase.Add(time.Duration(i)*time.Second))
		if err := savePair(ctx, store, action, attempt); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, action)
		attempts = append(attempts, attempt)
	}
	return actions, attempts
}

// probe is everything a caller can ask a store, answered as one comparable value.
type probe struct {
	Actions  map[string]string
	Attempts map[string]string
}

func describe(v any, err error) string {
	if err != nil {
		if errors.Is(err, domainaction.ErrNotFound) {
			return "not-found"
		}
		return "error"
	}
	data, marshalErr := json.Marshal(v)
	if marshalErr != nil {
		return "unmarshalable"
	}
	return string(data)
}

// observe asks the store every question the owner API allows about the records
// in ids: lookups by ID, the chronological lists under every filter field alone
// and combined, and the Limit forms.
func observe(ctx context.Context, store domainaction.Store, actionIDs []modulecore.ActionID, attemptIDs []modulecore.AttemptID, tasks []modulecore.TaskID, runs []modulecore.RunID, names []string) probe {
	out := probe{Actions: map[string]string{}, Attempts: map[string]string{}}
	for _, id := range actionIDs {
		v, err := store.GetAction(ctx, id)
		out.Actions["get "+string(id)] = describe(v, err)
		list, err := store.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: id})
		out.Attempts["of "+string(id)] = describe(list, err)
		for _, status := range []domainaction.AttemptStatus{domainaction.AttemptStatusRunning, domainaction.AttemptStatusSucceeded, domainaction.AttemptStatusFailed, domainaction.AttemptStatusCancelled, domainaction.AttemptStatusTimedOut} {
			list, err := store.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: id, Status: status})
			out.Attempts[fmt.Sprintf("of %s %s", id, status)] = describe(list, err)
		}
	}
	for _, id := range attemptIDs {
		v, err := store.GetAttempt(ctx, id)
		out.Attempts["get "+string(id)] = describe(v, err)
	}
	statuses := []domainaction.Status{"", domainaction.StatusOpen, domainaction.StatusSucceeded, domainaction.StatusFailed, domainaction.StatusCancelled, domainaction.StatusSuperseded}
	kinds := []domainaction.Kind{"", domainaction.KindTool, domainaction.KindLLM, domainaction.KindPlayback, domainaction.KindMemoryPromotion, domainaction.KindDelegation}
	list := func(label string, filter domainaction.Filter) {
		v, err := store.ListActions(ctx, filter)
		out.Actions[label] = describe(v, err)
	}
	list("all", domainaction.Filter{})
	list("limit1", domainaction.Filter{Limit: 1})
	list("limit3", domainaction.Filter{Limit: 3})
	for _, status := range statuses {
		list("status "+string(status), domainaction.Filter{Status: status})
		list("status+limit "+string(status), domainaction.Filter{Status: status, Limit: 2})
	}
	for _, kind := range kinds {
		list("kind "+string(kind), domainaction.Filter{Kind: kind})
		for _, name := range names {
			list(fmt.Sprintf("kind+name %s %s", kind, name), domainaction.Filter{Kind: kind, Name: name})
		}
	}
	for _, name := range names {
		list("name "+name, domainaction.Filter{Name: name})
	}
	for _, task := range tasks {
		list("task "+string(task), domainaction.Filter{TaskID: task})
		for _, run := range runs {
			list(fmt.Sprintf("task+run %s %s", task, run), domainaction.Filter{TaskID: task, RunID: run})
			list(fmt.Sprintf("task+run+open %s %s", task, run), domainaction.Filter{TaskID: task, RunID: run, Status: domainaction.StatusOpen})
		}
	}
	for _, run := range runs {
		list("run "+string(run), domainaction.Filter{RunID: run})
		list("run+limit "+string(run), domainaction.Filter{RunID: run, Limit: 1})
		for _, status := range statuses {
			list(fmt.Sprintf("run+status %s %s", run, status), domainaction.Filter{RunID: run, Status: status})
		}
		list("run+kind "+string(run), domainaction.Filter{RunID: run, Kind: domainaction.KindTool})
	}
	for _, status := range []domainaction.AttemptStatus{"", domainaction.AttemptStatusRunning, domainaction.AttemptStatusSucceeded, domainaction.AttemptStatusFailed} {
		v, err := store.ListAttempts(ctx, domainaction.AttemptFilter{Status: status})
		out.Attempts["all "+string(status)] = describe(v, err)
		v, err = store.ListAttempts(ctx, domainaction.AttemptFilter{Status: status, Limit: 2})
		out.Attempts["all+limit "+string(status)] = describe(v, err)
	}
	return out
}

// knownIDs reads the IDs a full store holds, plus a few that it does not.
func knownIDs(t testing.TB, store *JSONLStore) (actions []modulecore.ActionID, attempts []modulecore.AttemptID, tasks []modulecore.TaskID, runs []modulecore.RunID, names []string) {
	t.Helper()
	ctx := context.Background()
	all, err := store.ListActions(ctx, domainaction.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	seenTask := map[modulecore.TaskID]bool{}
	seenRun := map[modulecore.RunID]bool{}
	seenName := map[string]bool{}
	for _, a := range all {
		actions = append(actions, a.ActionID)
		if !seenTask[a.TaskID] {
			seenTask[a.TaskID] = true
			tasks = append(tasks, a.TaskID)
		}
		if !seenRun[a.RunID] {
			seenRun[a.RunID] = true
			runs = append(runs, a.RunID)
		}
		if !seenName[a.Name] {
			seenName[a.Name] = true
			names = append(names, a.Name)
		}
	}
	allAttempts, err := store.ListAttempts(ctx, domainaction.AttemptFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range allAttempts {
		attempts = append(attempts, a.AttemptID)
	}
	actions = append(actions, modulecore.NewActionID())
	attempts = append(attempts, modulecore.NewAttemptID())
	tasks = append(tasks, modulecore.NewTaskID())
	runs = append(runs, modulecore.NewRunID())
	names = append(names, "no-such-name")
	sort.Strings(names)
	return
}

// requireSameAnswers asks both stores everything and fails on the first answer
// that differs. The oracle is expected to be the store opened without the
// snapshot. The battery runs inside one read transaction per store, so the
// oracle reads its logs once for it; a few direct calls then check that the
// single-call methods agree as well.
func requireSameAnswers(t testing.TB, oracle, subject *JSONLStore) {
	t.Helper()
	actions, attempts, tasks, runs, names := knownIDs(t, oracle)
	ctx := context.Background()
	battery := func(store *JSONLStore) probe {
		var out probe
		if err := store.ReadTransaction(ctx, func(tx domainaction.Store) error {
			out = observe(ctx, tx, actions, attempts, tasks, runs, names)
			return nil
		}); err != nil {
			t.Fatalf("read transaction: %v", err)
		}
		return out
	}
	want, got := battery(oracle), battery(subject)
	for key, w := range want.Actions {
		if g := got.Actions[key]; g != w {
			t.Fatalf("answer %q differs\n  full read: %.300s\n  snapshot:  %.300s", key, w, g)
		}
	}
	for key, w := range want.Attempts {
		if g := got.Attempts[key]; g != w {
			t.Fatalf("answer %q differs\n  full read: %.300s\n  snapshot:  %.300s", key, w, g)
		}
	}
	direct := func(store *JSONLStore) []string {
		var out []string
		for i := 0; i < len(actions) && i < 3; i++ {
			v, err := store.GetAction(ctx, actions[i])
			out = append(out, describe(v, err))
			list, err := store.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: actions[i]})
			out = append(out, describe(list, err))
		}
		all, err := store.ListActions(ctx, domainaction.Filter{Limit: 4})
		out = append(out, describe(all, err))
		open, err := store.ListActions(ctx, domainaction.Filter{Status: domainaction.StatusOpen})
		out = append(out, describe(open, err))
		return out
	}
	wantDirect, gotDirect := direct(oracle), direct(subject)
	for i := range wantDirect {
		if wantDirect[i] != gotDirect[i] {
			t.Fatalf("direct answer %d differs\n  full read: %.300s\n  snapshot:  %.300s", i, wantDirect[i], gotDirect[i])
		}
	}
}

// requireSnapshotEqualsLog fails unless the snapshot holds exactly what a full
// read of the logs yields, every field, and every derived lookup agrees with the
// records they were derived from.
func requireSnapshotEqualsLog(t testing.TB, store *JSONLStore) {
	t.Helper()
	ix := requireIndexed(t, store)
	actions, err := store.loadActions(context.Background())
	if err != nil {
		t.Fatalf("full read of the Action log: %v", err)
	}
	attempts, err := store.loadAttempts(context.Background())
	if err != nil {
		t.Fatalf("full read of the Attempt log: %v", err)
	}
	if problems := ix.checkAgainst(actions, attempts); len(problems) > 0 {
		t.Fatalf("snapshot differs from the log: %s", strings.Join(problems, "; "))
	}
}

// requireSameFiles fails unless both stores hold byte-identical logs.
func requireSameFiles(t testing.TB, a, b *JSONLStore) {
	t.Helper()
	for _, pair := range [][2]string{{a.actionPath, b.actionPath}, {a.attemptPath, b.attemptPath}} {
		left, err := os.ReadFile(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		right, err := os.ReadFile(pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if string(left) != string(right) {
			t.Fatalf("log %s differs between the stores (%d and %d bytes)", filepath.Base(pair[0]), len(left), len(right))
		}
	}
}

func fileSize(t testing.TB, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func mustJSONLine(t testing.TB, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func deepEqual(a, b any) bool { return reflect.DeepEqual(a, b) }

// craftWAL leaves behind what a writer that stopped inside jsonlbatch.Write
// leaves: a prepare record, the appended bytes, and (when commit is set) the
// commit record. It writes the WAL by hand because the stop points are inside
// another package.
func craftWAL(t testing.TB, root string, appends map[string][]byte, commit bool) {
	t.Helper()
	type journalFile struct {
		Name          string `json:"name"`
		Offset        int64  `json:"offset"`
		AppendLength  int64  `json:"append_length"`
		PrefixSHA256  string `json:"prefix_sha256"`
		PayloadSHA256 string `json:"payload_sha256"`
	}
	type record struct {
		Version int           `json:"version"`
		Kind    string        `json:"kind"`
		TxID    string        `json:"tx_id"`
		Files   []journalFile `json:"files,omitempty"`
	}
	hash := func(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
	names := make([]string, 0, len(appends))
	for name := range appends {
		names = append(names, name)
	}
	sort.Strings(names)
	prepare := record{Version: 1, Kind: "prepare", TxID: fmt.Sprintf("crafted-%d", time.Now().UnixNano())}
	for _, name := range names {
		before, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		prepare.Files = append(prepare.Files, journalFile{
			Name: name, Offset: int64(len(before)), AppendLength: int64(len(appends[name])),
			PrefixSHA256: hash(before), PayloadSHA256: hash(appends[name]),
		})
	}
	wal, err := os.OpenFile(filepath.Join(root, ".jsonlbatch.wal"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()
	write := func(r record) {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := wal.Write(append(line, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	write(prepare)
	for _, name := range names {
		file, err := os.OpenFile(filepath.Join(root, name), os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(appends[name]); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if commit {
		write(record{Version: 1, Kind: "commit", TxID: prepare.TxID})
	}
	if err := wal.Sync(); err != nil {
		t.Fatal(err)
	}
}

// copyStoreDir copies the log files, WAL and lock of a store, which is what a
// process that stopped leaves behind.
func copyStoreDir(t testing.TB, src, dst string) {
	t.Helper()
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, entry.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func lines(data []byte) []string {
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}
