package jsonlutil

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAppendBatchCombinesConcurrentRowsWithoutChangingBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "records.jsonl")
	var mu sync.Mutex
	writes := 0
	coordinator := newAppendBatchCoordinator(20*time.Millisecond, 64, 1<<20, 8, func(path string, lines [][]byte) error {
		mu.Lock()
		writes++
		mu.Unlock()
		return writeAppendLines(path, lines)
	})
	const count = 100
	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errCh <- coordinator.submitLine(path, []byte(fmt.Sprintf("{\"id\":%d}\n", i)), false)
		}(i)
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("append row: %v", err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := make(map[string]int, count)
	for i := 0; i < count; i++ {
		want[fmt.Sprintf("{\"id\":%d}\n", i)]++
	}
	got := make(map[string]int, count)
	for _, line := range splitNonEmptyLines(data) {
		got[string(append(line, '\n'))]++
	}
	if len(got) != count {
		t.Fatalf("persisted unique rows=%d, want %d", len(got), count)
	}
	for line, occurrences := range want {
		if got[line] != occurrences {
			t.Fatalf("row %q occurred %d times, want %d", line, got[line], occurrences)
		}
	}
	mu.Lock()
	if writes >= count {
		mu.Unlock()
		t.Fatalf("underlying append calls=%d, want fewer than %d", writes, count)
	}
	batchedWrites := writes
	mu.Unlock()

	legacyPath := filepath.Join(t.TempDir(), "legacy-immediate.jsonl")
	var legacyWrites atomic.Int32
	legacy := newAppendBatchCoordinator(20*time.Millisecond, 64, 1<<20, 8, func(path string, lines [][]byte) error {
		legacyWrites.Add(1)
		return writeAppendLines(path, lines)
	})
	start = make(chan struct{})
	errCh = make(chan error, count)
	wg = sync.WaitGroup{}
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errCh <- legacy.submitLine(legacyPath, []byte(fmt.Sprintf("{\"id\":%d}\n", i)), true)
		}(i)
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("legacy immediate append: %v", err)
		}
	}
	legacyData, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyGot := make(map[string]int, count)
	for _, line := range splitNonEmptyLines(legacyData) {
		legacyGot[string(append(line, '\n'))]++
	}
	if !reflect.DeepEqual(legacyGot, got) {
		t.Fatalf("batched and immediate workloads persisted different record sets")
	}
	if got := legacyWrites.Load(); got != count {
		t.Fatalf("legacy immediate underlying appends=%d, want %d", got, count)
	}
	t.Logf("fixed workload records=%d batched_file_appends=%d immediate_file_appends=%d", count, batchedWrites, legacyWrites.Load())
}

func TestAppendBatchFilesProgressIndependently(t *testing.T) {
	root := t.TempDir()
	pathA := filepath.Join(root, "a.jsonl")
	pathB := filepath.Join(root, "b.jsonl")
	startedA := make(chan struct{})
	releaseA := make(chan struct{})
	coordinator := newAppendBatchCoordinator(10*time.Millisecond, 64, 1<<20, 4, func(path string, lines [][]byte) error {
		if path == pathA {
			close(startedA)
			<-releaseA
		}
		return writeAppendLines(path, lines)
	})
	aDone := make(chan error, 1)
	go func() { aDone <- coordinator.submitLine(pathA, []byte("a\n"), false) }()
	select {
	case <-startedA:
	case <-time.After(time.Second):
		t.Fatal("first file did not reach its writer")
	}
	bDone := make(chan error, 1)
	go func() { bDone <- coordinator.submitLine(pathB, []byte("b\n"), false) }()
	select {
	case err := <-bDone:
		if err != nil {
			t.Fatalf("second file append: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second file was blocked by the first file writer")
	}
	close(releaseA)
	if err := <-aDone; err != nil {
		t.Fatalf("first file append: %v", err)
	}
}

func TestAppendBatchEnforcesRecordAndByteCapsAndAllowsOneOversize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bounded.jsonl")
	var mu sync.Mutex
	var batches [][]int
	coordinator := newAppendBatchCoordinator(10*time.Millisecond, 3, 15, 2, func(path string, lines [][]byte) error {
		lengths := make([]int, len(lines))
		total := 0
		for i, line := range lines {
			lengths[i] = len(line)
			total += len(line)
		}
		if len(lines) > 3 || total > 15 && len(lines) != 1 {
			return fmt.Errorf("batch violated caps: records=%d bytes=%d", len(lines), total)
		}
		mu.Lock()
		batches = append(batches, lengths)
		mu.Unlock()
		return writeAppendLines(path, lines)
	})
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := coordinator.submitLine(path, []byte("12345\n"), false); err != nil {
				t.Errorf("append: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if err := coordinator.submitLine(path, []byte("oversized-record\n"), false); err != nil {
		t.Fatalf("oversized singleton append: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(batches) < 3 {
		t.Fatalf("writer batches=%d, expected capped batches plus oversized singleton", len(batches))
	}
	last := batches[len(batches)-1]
	if len(last) != 1 || last[0] <= 15 {
		t.Fatalf("last batch lengths=%v, want one oversized record", last)
	}
	lines, err := ReadAllLines(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(lines[len(lines)-1]); got != "oversized-record" {
		t.Fatalf("oversized persisted record=%q, want exact bytes", got)
	}
}

func TestAppendBatchCallerWaitsForWriteAndCloseResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "barrier.jsonl")
	started, release := make(chan struct{}), make(chan struct{})
	closeErr := errors.New("injected close failure")
	coordinator := newAppendBatchCoordinator(5*time.Millisecond, 64, 1<<20, 2, func(string, [][]byte) error {
		close(started)
		<-release
		return &AppendError{Stage: AppendStageClose, Err: closeErr}
	})
	done := make(chan error, 1)
	go func() { done <- coordinator.submitLine(path, []byte("one\n"), false) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("writer did not start")
	}
	select {
	case err := <-done:
		t.Fatalf("caller returned before writer close: %v", err)
	default:
	}
	close(release)
	if err := <-done; !errors.Is(err, closeErr) {
		t.Fatalf("append error=%v, want close failure", err)
	}
}

func TestAppendBatchFailureFansOutWithoutRetry(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "write", err: fmt.Errorf("injected write failure")},
		{name: "short write", err: &AppendError{Stage: AppendStageWrite, Err: io.ErrShortWrite}},
		{name: "close", err: &AppendError{Stage: AppendStageClose, Err: errors.New("injected close failure")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "failure.jsonl")
			var calls atomic.Int32
			coordinator := newAppendBatchCoordinator(40*time.Millisecond, 64, 1<<20, 2, func(string, [][]byte) error {
				calls.Add(1)
				return test.err
			})
			const count = 5
			start := make(chan struct{})
			errCh := make(chan error, count)
			var wg sync.WaitGroup
			for i := 0; i < count; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					errCh <- coordinator.submitLine(path, []byte(fmt.Sprintf("%d\n", i)), false)
				}(i)
			}
			close(start)
			wg.Wait()
			close(errCh)
			for err := range errCh {
				if err == nil {
					t.Fatal("failed batch member returned nil")
				}
				if test.name == "short write" && !errors.Is(err, io.ErrShortWrite) {
					t.Fatalf("short write error=%v, want io.ErrShortWrite", err)
				}
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("underlying write calls=%d, want one failure without retry", got)
			}
		})
	}
}

func TestAppendBatchEnforcesCadenceAndImmediateFence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "order.jsonl")
	firstStarted, releaseFirst := make(chan time.Time, 1), make(chan struct{})
	var mu sync.Mutex
	var starts []time.Time
	coordinator := newAppendBatchCoordinator(25*time.Millisecond, 64, 1<<20, 2, func(path string, lines [][]byte) error {
		started := time.Now()
		mu.Lock()
		starts = append(starts, started)
		index := len(starts)
		mu.Unlock()
		if index == 1 {
			firstStarted <- started
			<-releaseFirst
		}
		return writeAppendLines(path, lines)
	})
	firstDone := make(chan error, 1)
	go func() { firstDone <- coordinator.submitLine(path, []byte("first\n"), false) }()
	firstStart := <-firstStarted
	secondDone := make(chan error, 1)
	go func() { secondDone <- coordinator.submitLine(path, []byte("second\n"), false) }()
	deadline := time.Now().Add(time.Second)
	for {
		coordinator.mu.Lock()
		state := coordinator.states[path]
		pending := 0
		if state != nil {
			state.mu.Lock()
			pending = len(state.queue)
			state.mu.Unlock()
		}
		coordinator.mu.Unlock()
		if pending > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second append was not admitted")
		}
		time.Sleep(time.Millisecond)
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 2 {
		t.Fatalf("underlying append starts=%d, want 2", len(starts))
	}
	if elapsed := starts[1].Sub(firstStart); elapsed < 20*time.Millisecond {
		t.Fatalf("normal batch start spacing=%s, want at least 20ms", elapsed)
	}
}

func TestAppendBatchImmediateFlushesPrefixAndLeavesNoWorker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fenced.jsonl")
	var mu sync.Mutex
	var writes [][]string
	coordinator := newAppendBatchCoordinator(50*time.Millisecond, 64, 1<<20, 2, func(path string, lines [][]byte) error {
		batch := make([]string, len(lines))
		for i := range lines {
			batch[i] = string(lines[i])
		}
		mu.Lock()
		writes = append(writes, batch)
		mu.Unlock()
		return writeAppendLines(path, lines)
	})
	firstDone := make(chan error, 1)
	go func() { firstDone <- coordinator.submitLine(path, []byte("prefix\n"), false) }()
	deadline := time.Now().Add(time.Second)
	for {
		coordinator.mu.Lock()
		state := coordinator.states[path]
		pending := 0
		if state != nil {
			state.mu.Lock()
			pending = len(state.queue)
			state.mu.Unlock()
		}
		coordinator.mu.Unlock()
		if pending != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("prefix was not admitted")
		}
		time.Sleep(time.Millisecond)
	}
	if err := coordinator.submitLine(path, []byte("immediate\n"), true); err != nil {
		t.Fatalf("immediate append: %v", err)
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("prefix append: %v", err)
	}
	mu.Lock()
	if len(writes) != 2 || len(writes[0]) != 1 || writes[0][0] != "prefix\n" || len(writes[1]) != 1 || writes[1][0] != "immediate\n" {
		t.Fatalf("ordered writes=%q, want prefix then immediate", writes)
	}
	mu.Unlock()
	deadline = time.Now().Add(time.Second)
	for {
		coordinator.mu.Lock()
		active := len(coordinator.states)
		slots := len(coordinator.fileSlots)
		coordinator.mu.Unlock()
		if active == 0 && slots == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("idle coordinator retained state: active=%d slots=%d", active, slots)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAppendBatchBlockedImmediateRequestsFlushAndBroadcastsToProducers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backpressure.jsonl")
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan time.Time, 1)
	var writeMu sync.Mutex
	writes := 0
	coordinator := newAppendBatchCoordinator(150*time.Millisecond, 1, 1<<20, 2, func(path string, lines [][]byte) error {
		writeMu.Lock()
		writes++
		index := writes
		writeMu.Unlock()
		if index == 1 {
			close(firstStarted)
			<-releaseFirst
		} else if index == 2 {
			secondStarted <- time.Now()
		}
		return writeAppendLines(path, lines)
	})
	firstDone := make(chan error, 1)
	go func() { firstDone <- coordinator.submitLine(path, []byte("first\n"), false) }()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first append did not start")
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- coordinator.submitLine(path, []byte("second\n"), false) }()
	deadline := time.Now().Add(time.Second)
	for {
		coordinator.mu.Lock()
		state := coordinator.states[path]
		pending := 0
		if state != nil {
			state.mu.Lock()
			pending = len(state.queue)
			state.mu.Unlock()
		}
		coordinator.mu.Unlock()
		if pending == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second append did not fill the pending queue")
		}
		time.Sleep(time.Millisecond)
	}
	producerCount := 4
	producerDone := make(chan error, producerCount)
	for i := 0; i < producerCount; i++ {
		go func(i int) {
			producerDone <- coordinator.submitLine(path, []byte(fmt.Sprintf("waiter-%d\n", i)), false)
		}(i)
	}
	immediateDone := make(chan error, 1)
	go func() { immediateDone <- coordinator.submitLine(path, []byte("immediate\n"), true) }()
	deadline = time.Now().Add(time.Second)
	for {
		coordinator.mu.Lock()
		state := coordinator.states[path]
		waiters := 0
		if state != nil {
			state.mu.Lock()
			waiters = state.immediateWaiters
			state.mu.Unlock()
		}
		coordinator.mu.Unlock()
		if waiters == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("immediate producer did not request a prefix flush")
		}
		time.Sleep(time.Millisecond)
	}
	releasedAt := time.Now()
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first append: %v", err)
	}
	select {
	case startedAt := <-secondStarted:
		if elapsed := startedAt.Sub(releasedAt); elapsed >= 100*time.Millisecond {
			t.Fatalf("queued prefix waited %s after blocked immediate requested flush", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("queued prefix did not start after releasing first writer")
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second append: %v", err)
	}
	if err := <-immediateDone; err != nil {
		t.Fatalf("immediate append: %v", err)
	}
	for i := 0; i < producerCount; i++ {
		select {
		case err := <-producerDone:
			if err != nil {
				t.Fatalf("backpressured producer: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("producer %d remained blocked after queue space broadcast", i)
		}
	}
	deadline = time.Now().Add(time.Second)
	for {
		coordinator.mu.Lock()
		active, slots := len(coordinator.states), len(coordinator.fileSlots)
		coordinator.mu.Unlock()
		if active == 0 && slots == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("blocked immediate path leaked worker: active=%d slots=%d", active, slots)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAppendBatchStatsReflectProductionWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stats.jsonl")
	before := AppendBatchStatsSnapshot()
	const count = 100
	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errCh <- Append(path, testRecord{ID: i})
		}(i)
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	after := AppendBatchStatsSnapshot()
	if got := after.NormalRecordsWritten - before.NormalRecordsWritten; got != count {
		t.Fatalf("normal records stat=%d, want %d", got, count)
	}
	if got := after.NormalFlushSuccesses - before.NormalFlushSuccesses; got >= count {
		t.Fatalf("normal flushes=%d, want fewer than %d", got, count)
	}
	if got, want := after.NormalFlushAttempts-before.NormalFlushAttempts, after.NormalFlushSuccesses-before.NormalFlushSuccesses; got != want {
		t.Fatalf("normal attempts=%d successes=%d, want no failures", got, want)
	}
	t.Logf("production Append workload records=%d normal_flushes=%d bytes=%d", after.NormalRecordsWritten-before.NormalRecordsWritten, after.NormalFlushSuccesses-before.NormalFlushSuccesses, after.NormalBytesWritten-before.NormalBytesWritten)
	lines, err := ReadAllLines(path)
	if err != nil || len(lines) != count {
		t.Fatalf("persisted lines=%d err=%v, want %d", len(lines), err, count)
	}
}

func TestAppendBoundedFencesAndCompactsPriorNormalAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fenced-bounded.jsonl")
	appendDone := make(chan error, 1)
	go func() { appendDone <- Append(path, testRecord{ID: 1}) }()
	deadline := time.Now().Add(time.Second)
	for {
		appendBatches.mu.Lock()
		state := appendBatches.states[path]
		pending := 0
		if state != nil {
			state.mu.Lock()
			pending = len(state.queue)
			state.mu.Unlock()
		}
		appendBatches.mu.Unlock()
		if pending > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("normal append was not accepted before bounded fence")
		}
		time.Sleep(time.Millisecond)
	}
	if err := AppendBounded(path, testRecord{ID: 2}, BoundOptions{MaxRecords: 1, MaxBytes: 1}); err != nil {
		t.Fatalf("bounded append: %v", err)
	}
	if err := <-appendDone; err != nil {
		t.Fatalf("prior normal append: %v", err)
	}
	lines, err := ReadAllLines(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || string(lines[0]) != `{"id":2}` {
		t.Fatalf("compacted lines=%q, want only bounded record", lines)
	}
	archives, err := filepath.Glob(filepath.Join(filepath.Dir(path), "archive", "fenced-bounded.compacted.*.jsonl.gz"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("archive count=%d err=%v, want one archive for prior accepted row", len(archives), err)
	}
	archived := readJSONLGzipLines(t, archives[0])
	if len(archived) != 1 || archived[0] != `{"id":1}` {
		t.Fatalf("archived records=%q, want prior accepted row", archived)
	}
}
