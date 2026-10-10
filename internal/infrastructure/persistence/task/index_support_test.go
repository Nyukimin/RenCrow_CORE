package task

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// logFacts is what the log files say, read without the index.
type logFacts struct {
	lines map[fileKind]int64
	bytes map[fileKind]int64
	tasks map[modulecore.TaskID]struct{}
	runs  map[modulecore.RunID]struct{}
	ctxs  map[modulecore.TaskID]struct{}
}

func scanLog(t testing.TB, root string) logFacts {
	t.Helper()
	facts := logFacts{
		lines: map[fileKind]int64{}, bytes: map[fileKind]int64{},
		tasks: map[modulecore.TaskID]struct{}{}, runs: map[modulecore.RunID]struct{}{}, ctxs: map[modulecore.TaskID]struct{}{},
	}
	for kind := fileKind(0); kind < kindCount; kind++ {
		data, err := os.ReadFile(filepath.Join(root, kindFilename[kind]))
		if err != nil {
			t.Fatalf("read %s: %v", kindFilename[kind], err)
		}
		facts.bytes[kind] = int64(len(data))
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 0, 64*1024), 32<<20)
		for scanner.Scan() {
			facts.lines[kind]++
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			switch kind {
			case kindState:
				var value struct {
					TaskID modulecore.TaskID `json:"task_id"`
				}
				mustUnmarshal(t, line, &value)
				facts.tasks[value.TaskID] = struct{}{}
			case kindRun:
				var value struct {
					RunID modulecore.RunID `json:"run_id"`
				}
				mustUnmarshal(t, line, &value)
				facts.runs[value.RunID] = struct{}{}
			case kindContext:
				var value struct {
					TaskID modulecore.TaskID `json:"task_id"`
				}
				mustUnmarshal(t, line, &value)
				facts.ctxs[value.TaskID] = struct{}{}
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatalf("scan %s: %v", kindFilename[kind], err)
		}
	}
	return facts
}

func mustUnmarshal(t testing.TB, data []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("decode log line: %v", err)
	}
}

// requireIndexCoversLog is INV-2 (index contains the log): every line is
// counted, every ID in the log can be fetched, and the indexed prefix of every
// file is the whole file.
func requireIndexCoversLog(t testing.TB, store *JSONLStore, root string) {
	t.Helper()
	facts := scanLog(t, root)
	stats, ok := store.IndexStats()
	if !ok {
		t.Fatal("store has no index")
	}
	for kind := fileKind(0); kind < kindCount; kind++ {
		if stats.Lines[kind] != facts.lines[kind] {
			t.Errorf("%s: index counts %d lines, log has %d", kindFilename[kind], stats.Lines[kind], facts.lines[kind])
		}
		if stats.AppliedBytes[kind] != facts.bytes[kind] {
			t.Errorf("%s: index applied %d bytes, file has %d", kindFilename[kind], stats.AppliedBytes[kind], facts.bytes[kind])
		}
	}
	if stats.Tasks != len(facts.tasks) {
		t.Errorf("index has %d tasks, log has %d distinct", stats.Tasks, len(facts.tasks))
	}
	if stats.Runs != len(facts.runs) {
		t.Errorf("index has %d runs, log has %d distinct", stats.Runs, len(facts.runs))
	}
	ctx := context.Background()
	for id := range facts.tasks {
		if _, err := store.GetTask(ctx, id); err != nil {
			t.Fatalf("GetTask(%s) for an ID in the log: %v", id, err)
		}
	}
	for id := range facts.runs {
		if _, err := store.GetRun(ctx, id); err != nil {
			t.Fatalf("GetRun(%s) for an ID in the log: %v", id, err)
		}
	}
	for id := range facts.ctxs {
		if _, err := store.GetContext(ctx, id); err != nil {
			t.Fatalf("GetContext(%s) for an ID in the log: %v", id, err)
		}
	}
}

// indexedTestTask builds a valid Task with a deterministic ID.
func indexedTestTask(n int, now time.Time) domaintask.Task {
	id := modulecore.TaskID(fmt.Sprintf("tsk_%s", testUUID("task", n)))
	return domaintask.Task{
		TaskID: id, Title: fmt.Sprintf("indexed task %d", n), Route: domaintask.RouteGeneral, Status: domaintask.StatusQueued,
		Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now,
	}
}

func indexedTestRun(task domaintask.Task, n int, generation uint64, now time.Time) domaintask.Run {
	return domaintask.Run{
		WriterGeneration: generation, RunID: modulecore.RunID("run_" + testUUID("run", n)), TaskID: task.TaskID,
		StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now,
	}
}

// testUUID derives a canonical UUIDv5-shaped string from a label and number.
func testUUID(label string, n int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", label, n)))
	b := sum[:16]
	b[6] = b[6]&0x0f | 0x50
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func openIndexed(t testing.TB, root string) *JSONLStore {
	t.Helper()
	store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true})
	if err != nil {
		t.Fatalf("open indexed store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// craftWAL appends WAL records for a transaction the way jsonlbatch would, so a
// test can leave the files in the exact shape a crash or an uncertain commit
// leaves behind. With commit=false the transaction stays pending.
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
	prepare := record{Version: 1, Kind: "prepare", TxID: fmt.Sprintf("crafted-%d", time.Now().UnixNano())}
	for _, name := range sortedKeys(appends) {
		payload := appends[name]
		path := filepath.Join(root, name)
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		prepare.Files = append(prepare.Files, journalFile{
			Name: name, Offset: int64(len(before)), AppendLength: int64(len(payload)),
			PrefixSHA256: hash(before), PayloadSHA256: hash(payload),
		})
	}
	wal, err := os.OpenFile(filepath.Join(root, ".jsonlbatch.wal"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer wal.Close()
	writeRecord := func(r record) {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := wal.Write(append(line, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	writeRecord(prepare)
	for _, name := range sortedKeys(appends) {
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
		writeRecord(record{Version: 1, Kind: "commit", TxID: prepare.TxID})
	}
	if err := wal.Sync(); err != nil {
		t.Fatal(err)
	}
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func marshalLine(t testing.TB, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}
