package txobserve

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

// testPrefix is the task store's prefix: the format tests pin the exact lines the
// task store has always written, so moving the code here changed none of them.
const testPrefix = "[TaskStore]"

func TestPercentileUsesNearestRank(t *testing.T) {
	ten := make([]time.Duration, 10)
	for i := range ten {
		ten[i] = ms(i + 1)
	}
	big := make([]time.Duration, 512)
	for i := range big {
		big[i] = ms(i + 1)
	}
	tests := []struct {
		name   string
		values []time.Duration
		p      int
		want   time.Duration
	}{
		{"p50 of ten", ten, 50, ms(5)},
		{"p95 of ten", ten, 95, ms(10)},
		{"p50 of one", []time.Duration{ms(7)}, 50, ms(7)},
		{"p95 of one", []time.Duration{ms(7)}, 95, ms(7)},
		{"p50 of 512", big, 50, ms(256)},
		{"p95 of 512", big, 95, ms(487)},
		{"p95 of two", []time.Duration{ms(1), ms(2)}, 95, ms(2)},
		{"p50 of two", []time.Duration{ms(1), ms(2)}, 50, ms(1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := percentile(tt.values, tt.p); got != tt.want {
				t.Fatalf("percentile(p=%d) = %v, want %v", tt.p, got, tt.want)
			}
		})
	}
}

func TestStatOfSortsACopyAndReportsEmpty(t *testing.T) {
	input := []time.Duration{ms(30), ms(10), ms(20)}
	got := statOf(input)
	if got.N != 3 || got.P50 != ms(20) || got.P95 != ms(30) {
		t.Fatalf("statOf = %+v, want n=3 p50=20ms p95=30ms", got)
	}
	if input[0] != ms(30) || input[1] != ms(10) || input[2] != ms(20) {
		t.Fatalf("statOf reordered its input: %v", input)
	}
	if empty := statOf(nil); empty.N != 0 || empty.P50 != 0 || empty.P95 != 0 {
		t.Fatalf("statOf(nil) = %+v, want zero value", empty)
	}
}

func TestRingKeepsTheLatestSamplesOldestFirst(t *testing.T) {
	ring := newRing(3)
	if got := ring.samples(); len(got) != 0 {
		t.Fatalf("empty ring samples = %v", got)
	}
	base := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 5; i++ {
		ring.add(sample{At: base.Add(time.Duration(i) * time.Second), Exec: ms(i)})
	}
	got := ring.samples()
	if len(got) != 3 || got[0].Exec != ms(3) || got[1].Exec != ms(4) || got[2].Exec != ms(5) {
		t.Fatalf("ring samples = %+v, want the last three oldest first", got)
	}
	ring.add(sample{Exec: ms(6)})
	if got := ring.samples(); len(got) != 3 || got[0].Exec != ms(4) || got[2].Exec != ms(6) {
		t.Fatalf("ring samples after wrap = %+v", got)
	}
}

func TestRingCapacityBoundsMemory(t *testing.T) {
	ring := newRing(ringCapacity)
	for i := 0; i < ringCapacity*3+7; i++ {
		ring.add(sample{Exec: ms(i)})
	}
	if got := len(ring.samples()); got != ringCapacity {
		t.Fatalf("ring holds %d samples, want %d", got, ringCapacity)
	}
}

func TestNewSampleClassifiesPopulations(t *testing.T) {
	at := time.Date(2026, 10, 10, 1, 2, 3, 0, time.UTC)
	cases := []struct {
		name         string
		tx           jsonlbatch.TxObservation
		wantWrite    bool
		wantAcquired bool
		wantAppended bool
		wantResult   string
	}{
		{"committed write", jsonlbatch.TxObservation{Kind: jsonlbatch.TxKindWrite, Acquired: true, Files: []string{"task_state.jsonl"}}, true, true, true, "ok"},
		{"write that appended nothing", jsonlbatch.TxObservation{Kind: jsonlbatch.TxKindWrite, Acquired: true}, true, true, false, "ok"},
		{"write with callback error", jsonlbatch.TxObservation{Kind: jsonlbatch.TxKindWrite, Acquired: true, Err: errors.New("x")}, true, true, false, "error"},
		{"write that never got the lock", jsonlbatch.TxObservation{Kind: jsonlbatch.TxKindWrite, Err: errors.New("deadline")}, true, false, false, "lock_failed"},
		{"read", jsonlbatch.TxObservation{Kind: jsonlbatch.TxKindRead, Acquired: true}, false, true, false, "ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := newSample(tc.tx, at)
			if got.Write != tc.wantWrite || got.Acquired != tc.wantAcquired || got.Appended != tc.wantAppended || got.result() != tc.wantResult || !got.At.Equal(at) {
				t.Fatalf("newSample = %+v result=%s, want write=%v acquired=%v appended=%v result=%s",
					got, got.result(), tc.wantWrite, tc.wantAcquired, tc.wantAppended, tc.wantResult)
			}
		})
	}
}

func TestSummarizeSeparatesKindsAndPopulations(t *testing.T) {
	now := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	samples := []sample{
		{At: now.Add(-9 * time.Minute), Write: true, Acquired: true, Appended: true, LockWait: ms(10), Settle: ms(1), Exec: ms(100), Commit: ms(20)},
		{At: now.Add(-8 * time.Minute), Write: true, Acquired: true, Appended: true, LockWait: ms(30), Settle: ms(3), Exec: ms(300), Commit: ms(40)},
		// A write whose callback failed: it held the lock and decoded, but never committed.
		{At: now.Add(-7 * time.Minute), Write: true, Acquired: true, LockWait: ms(20), Settle: ms(2), Exec: ms(200)},
		// A write that gave up waiting: only lock_wait is a real measurement.
		{At: now.Add(-6 * time.Minute), Write: true, LockWait: ms(50000)},
		{At: now.Add(-5 * time.Minute), Acquired: true, LockWait: ms(5), Settle: ms(1), Exec: ms(70)},
		{At: now.Add(-1 * time.Minute), Acquired: true, LockWait: ms(15), Settle: ms(2), Exec: ms(90)},
	}
	sum := summarize(samples, now)
	if sum.Window != 9*time.Minute {
		t.Fatalf("Window = %v, want the age of the oldest sample", sum.Window)
	}
	w := sum.Write
	if w.N != 4 {
		t.Fatalf("write N = %d, want 4", w.N)
	}
	if w.LockWait.N != 4 || w.LockWait.P50 != ms(20) || w.LockWait.P95 != ms(50000) {
		t.Fatalf("write lock_wait = %+v, want every sample", w.LockWait)
	}
	if w.Settle.N != 3 || w.Exec.N != 3 || w.Exec.P50 != ms(200) || w.Exec.P95 != ms(300) {
		t.Fatalf("write settle/exec = %+v / %+v, want acquired samples only", w.Settle, w.Exec)
	}
	if w.Commit.N != 2 || w.Commit.P50 != ms(20) || w.Commit.P95 != ms(40) {
		t.Fatalf("write commit = %+v, want appended writes only", w.Commit)
	}
	r := sum.Read
	if r.N != 2 || r.LockWait.P50 != ms(5) || r.LockWait.P95 != ms(15) || r.Exec.P50 != ms(70) || r.Exec.P95 != ms(90) {
		t.Fatalf("read summary = %+v", r)
	}
	if r.Commit.N != 0 {
		t.Fatalf("read commit = %+v, want no population", r.Commit)
	}
}

func TestSummarizeOfNothingIsZero(t *testing.T) {
	if sum := summarize(nil, time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)); sum.Write.N != 0 || sum.Read.N != 0 || sum.Window != 0 {
		t.Fatalf("summary of no samples = %+v, want zero", sum)
	}
}

func TestIsSlowThresholds(t *testing.T) {
	const execCommit, lockWait = 5 * time.Second, 10 * time.Second
	tests := []struct {
		name   string
		sample sample
		want   bool
	}{
		{"exec plus commit at the limit", sample{Exec: 3 * time.Second, Commit: 2 * time.Second}, false},
		{"exec plus commit over the limit", sample{Exec: 3 * time.Second, Commit: 2*time.Second + 1}, true},
		{"exec alone over the limit", sample{Exec: 5*time.Second + 1}, true},
		{"lock wait at the limit", sample{LockWait: lockWait}, false},
		{"lock wait over the limit", sample{LockWait: lockWait + 1}, true},
		{"settle is not part of the rule", sample{Settle: time.Minute}, false},
		{"unacquired lock waited long", sample{LockWait: 11 * time.Second}, true},
		{"fast", sample{LockWait: ms(1), Exec: ms(10), Commit: ms(5)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSlow(tt.sample, execCommit, lockWait); got != tt.want {
				t.Fatalf("isSlow(%+v) = %v, want %v", tt.sample, got, tt.want)
			}
		})
	}
}

func TestConstantsMatchTheSpecifiedThresholds(t *testing.T) {
	if slowExecCommit != 5*time.Second || slowLockWait != 10*time.Second || summaryInterval != 10*time.Minute || ringCapacity != 512 {
		t.Fatalf("thresholds = %v/%v/%v/%d, want 5s/10s/10m/512", slowExecCommit, slowLockWait, summaryInterval, ringCapacity)
	}
}

func TestSummaryDue(t *testing.T) {
	last := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		now  time.Time
		want bool
	}{
		{"just after", last.Add(time.Second), false},
		{"one second early", last.Add(10*time.Minute - time.Second), false},
		{"exactly the interval", last.Add(10 * time.Minute), true},
		{"well past", last.Add(3 * time.Hour), true},
		{"clock moved backwards", last.Add(-time.Hour), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summaryDue(last, tt.now, summaryInterval); got != tt.want {
				t.Fatalf("summaryDue(now=%v) = %v, want %v", tt.now.Sub(last), got, tt.want)
			}
		})
	}
}

func TestFormatSlowLine(t *testing.T) {
	slow := sample{Write: true, Acquired: true, Appended: true, LockWait: 1234 * time.Millisecond, Settle: 3 * time.Millisecond, Exec: 5600 * time.Millisecond, Commit: 1200 * time.Millisecond}
	hot := HotSizes{Files: []HotFile{{Name: "task_state", Bytes: 66200000}, {Name: "task_run", Bytes: 29000000}}, OK: true}
	got := formatSlowLine(testPrefix, slow, Label{Op: "TaskTransaction", ScopeKey: "task_id", ScopeValue: "tsk_1"}, []string{"task_run.jsonl", "task_state.jsonl"}, hot)
	want := "[TaskStore] tx slow kind=write op=TaskTransaction task_id=tsk_1 result=ok lock_wait=1.234s settle=3ms exec=5.6s commit=1.2s files=task_run.jsonl+task_state.jsonl hot_bytes=95200000"
	if got != want {
		t.Fatalf("slow line =\n%s\nwant\n%s", got, want)
	}

	read := sample{Acquired: true, LockWait: ms(1), Exec: 6 * time.Second}
	got = formatSlowLine(testPrefix, read, Label{}, nil, HotSizes{})
	want = "[TaskStore] tx slow kind=read op=unlabeled result=ok lock_wait=1ms settle=0s exec=6s commit=0s hot_bytes=unknown"
	if got != want {
		t.Fatalf("slow line without label/task/files/hot =\n%s\nwant\n%s", got, want)
	}

	failed := sample{Write: true, LockWait: 11 * time.Second}
	got = formatSlowLine(testPrefix, failed, Label{Op: "Transaction"}, nil, hot)
	want = "[TaskStore] tx slow kind=write op=Transaction result=lock_failed lock_wait=11s settle=0s exec=0s commit=0s hot_bytes=95200000"
	if got != want {
		t.Fatalf("slow line for an unacquired lock =\n%s\nwant\n%s", got, want)
	}
}

func TestFormatSummaryLine(t *testing.T) {
	stat := func(p50, p95 int) durationStat {
		return durationStat{N: 3, P50: ms(p50), P95: ms(p95)}
	}
	sum := summary{
		Window: 9*time.Minute + 58*time.Second + 400*time.Millisecond,
		Write:  kindSummary{N: 312, LockWait: stat(1, 2), Settle: stat(3, 4), Exec: stat(5, 6), Commit: stat(7, 8)},
		Read:   kindSummary{N: 40, LockWait: stat(9, 10), Settle: stat(11, 12), Exec: stat(13, 14)},
	}
	got := formatSummaryLine(testPrefix, sum, HotSizes{Files: []HotFile{{Name: "task_state", Bytes: 66200000}, {Name: "task_run", Bytes: 29000000}}, OK: true})
	want := "[TaskStore] tx summary window=9m58s" +
		" write_n=312 write_lock_wait_p50=1ms write_lock_wait_p95=2ms write_settle_p50=3ms write_settle_p95=4ms" +
		" write_exec_p50=5ms write_exec_p95=6ms write_commit_p50=7ms write_commit_p95=8ms" +
		" read_n=40 read_lock_wait_p50=9ms read_lock_wait_p95=10ms read_settle_p50=11ms read_settle_p95=12ms" +
		" read_exec_p50=13ms read_exec_p95=14ms" +
		" hot_bytes=95200000 task_state_bytes=66200000 task_run_bytes=29000000"
	if got != want {
		t.Fatalf("summary line =\n%s\nwant\n%s", got, want)
	}

	empty := formatSummaryLine(testPrefix, summary{Window: time.Minute, Write: kindSummary{N: 1, LockWait: durationStat{N: 1, P50: ms(1), P95: ms(1)}}}, HotSizes{})
	for _, token := range []string{" write_exec_p50=-", " write_commit_p95=-", " read_n=0", " read_exec_p50=-", " hot_bytes=unknown"} {
		if !strings.Contains(empty, token) {
			t.Fatalf("summary line %q lacks %q", empty, token)
		}
	}
	if strings.Contains(empty, "task_state_bytes") {
		t.Fatalf("summary line %q reports per-file sizes although the stat failed", empty)
	}
}
