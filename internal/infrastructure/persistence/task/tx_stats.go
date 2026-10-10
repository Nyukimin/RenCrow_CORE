package task

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
)

// This file holds the I/O-free part of the task store's transaction timing
// observation: the sample type, a bounded ring, percentile arithmetic, the slow
// and "summary due" decisions, and the two log-line formats. The thresholds are
// code constants on purpose; they are not configuration.
const (
	// txSlowExecCommit marks a transaction slow when exec + commit exceeds it.
	txSlowExecCommit = 5 * time.Second
	// txSlowLockWait marks a transaction slow when the flock wait exceeds it.
	txSlowLockWait = 10 * time.Second
	// txSummaryInterval is the minimum gap between two summary lines.
	txSummaryInterval = 10 * time.Minute
	// txRingCapacity is how many recent transactions the summary covers.
	txRingCapacity = 512

	// txLogPrefix tags every line this observation writes.
	txLogPrefix = "[TaskStore]"
	// txUnlabeledOp is the op of a transaction whose caller attached no label.
	txUnlabeledOp = "unlabeled"
)

// txSample is one finished transaction reduced to what the summary needs.
type txSample struct {
	At    time.Time
	Write bool
	// Acquired is false when the call ended before it held the lock.
	Acquired bool
	// Appended is true for a write that committed at least one file, the only
	// population in which Commit is a real measurement.
	Appended bool
	// Failed is true when the call returned an error.
	Failed   bool
	LockWait time.Duration
	Settle   time.Duration
	Exec     time.Duration
	Commit   time.Duration
}

func newTxSample(tx jsonlbatch.TxObservation, at time.Time) txSample {
	write := tx.Kind == jsonlbatch.TxKindWrite
	return txSample{
		At:       at,
		Write:    write,
		Acquired: tx.Acquired,
		Appended: write && len(tx.Files) > 0,
		Failed:   tx.Err != nil,
		LockWait: tx.LockWait,
		Settle:   tx.Settle,
		Exec:     tx.Exec,
		Commit:   tx.Commit,
	}
}

func (s txSample) kind() string {
	if s.Write {
		return string(jsonlbatch.TxKindWrite)
	}
	return string(jsonlbatch.TxKindRead)
}

// result classifies how the call ended without carrying any error text.
func (s txSample) result() string {
	switch {
	case !s.Acquired:
		return "lock_failed"
	case s.Failed:
		return "error"
	default:
		return "ok"
	}
}

// isSlowTx applies the slow rule: exec + commit above execCommit, or lock wait
// above lockWait. Settle is deliberately not part of the rule.
func isSlowTx(s txSample, execCommit, lockWait time.Duration) bool {
	return s.Exec+s.Commit > execCommit || s.LockWait > lockWait
}

// txSummaryDue reports whether a summary should be written now. A clock that
// moved backwards is never due.
func txSummaryDue(last, now time.Time, interval time.Duration) bool {
	return now.Sub(last) >= interval
}

// txRing keeps the most recent samples in a fixed-size buffer. It is not safe
// for concurrent use; txObserver serialises access.
type txRing struct {
	buf   []txSample
	next  int
	count int
}

func newTxRing(capacity int) *txRing {
	if capacity < 1 {
		capacity = 1
	}
	return &txRing{buf: make([]txSample, capacity)}
}

func (r *txRing) add(s txSample) {
	r.buf[r.next] = s
	r.next = (r.next + 1) % len(r.buf)
	if r.count < len(r.buf) {
		r.count++
	}
}

// samples returns a copy of the held samples, oldest first.
func (r *txRing) samples() []txSample {
	out := make([]txSample, r.count)
	start := (r.next - r.count + len(r.buf)) % len(r.buf)
	for i := range out {
		out[i] = r.buf[(start+i)%len(r.buf)]
	}
	return out
}

// durationStat is the p50 / p95 of one population. N == 0 means no data.
type durationStat struct {
	N        int
	P50, P95 time.Duration
}

// percentile is the nearest-rank percentile of an ascending slice: the value at
// rank ceil(p/100 * n). It returns 0 for an empty slice.
func percentile(sorted []time.Duration, p int) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	rank := (p*n + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return sorted[rank-1]
}

// statOf computes p50 / p95 over a copy of values; the input order is kept.
func statOf(values []time.Duration) durationStat {
	if len(values) == 0 {
		return durationStat{}
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return durationStat{N: len(sorted), P50: percentile(sorted, 50), P95: percentile(sorted, 95)}
}

// txKindSummary summarises one kind (read or write). Populations differ:
// LockWait covers every transaction of the kind, Settle and Exec only those that
// held the lock, and Commit only writes that committed something.
type txKindSummary struct {
	N        int
	LockWait durationStat
	Settle   durationStat
	Exec     durationStat
	Commit   durationStat
}

type txSummary struct {
	// Window is the age of the oldest sample the summary covers.
	Window time.Duration
	Write  txKindSummary
	Read   txKindSummary
}

func summarizeTx(samples []txSample, now time.Time) txSummary {
	var sum txSummary
	if len(samples) == 0 {
		return sum
	}
	sum.Window = now.Sub(samples[0].At)
	if sum.Window < 0 {
		sum.Window = 0
	}
	sum.Write = summarizeTxKind(samples, true)
	sum.Read = summarizeTxKind(samples, false)
	return sum
}

func summarizeTxKind(samples []txSample, write bool) txKindSummary {
	var lockWait, settle, exec, commit []time.Duration
	var result txKindSummary
	for _, s := range samples {
		if s.Write != write {
			continue
		}
		result.N++
		lockWait = append(lockWait, s.LockWait)
		if s.Acquired {
			settle = append(settle, s.Settle)
			exec = append(exec, s.Exec)
		}
		if s.Appended {
			commit = append(commit, s.Commit)
		}
	}
	result.LockWait = statOf(lockWait)
	result.Settle = statOf(settle)
	result.Exec = statOf(exec)
	result.Commit = statOf(commit)
	return result
}

// hotSizes are the byte sizes of the two files every transaction decodes.
type hotSizes struct {
	state, run int64
	ok         bool
}

func fmtTxDuration(d time.Duration) string {
	return d.Round(time.Millisecond).String()
}

func fmtTxStat(d time.Duration, present bool) string {
	if !present {
		return "-"
	}
	return fmtTxDuration(d)
}

// formatTxSlowLine renders the one-line slow-transaction record. It carries the
// op, task id and file names only, never record bodies or error text.
func formatTxSlowLine(s txSample, label txLabel, files []string, hot hotSizes) string {
	op := label.op
	if op == "" {
		op = txUnlabeledOp
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s tx slow kind=%s op=%s", txLogPrefix, s.kind(), op)
	if label.taskID != "" {
		fmt.Fprintf(&b, " task_id=%s", label.taskID)
	}
	fmt.Fprintf(&b, " result=%s lock_wait=%s settle=%s exec=%s commit=%s",
		s.result(), fmtTxDuration(s.LockWait), fmtTxDuration(s.Settle), fmtTxDuration(s.Exec), fmtTxDuration(s.Commit))
	if len(files) > 0 {
		fmt.Fprintf(&b, " files=%s", strings.Join(files, "+"))
	}
	fmt.Fprintf(&b, " hot_bytes=%s", fmtHotBytes(hot))
	return b.String()
}

func fmtHotBytes(hot hotSizes) string {
	if !hot.ok {
		return "unknown"
	}
	return strconv.FormatInt(hot.state+hot.run, 10)
}

func writeTxStatPair(b *strings.Builder, prefix, name string, stat durationStat) {
	present := stat.N > 0
	fmt.Fprintf(b, " %s_%s_p50=%s %s_%s_p95=%s", prefix, name, fmtTxStat(stat.P50, present), prefix, name, fmtTxStat(stat.P95, present))
}

// formatTxSummaryLine renders the periodic one-line summary. Read has no commit
// phase, so its commit keys are omitted.
func formatTxSummaryLine(sum txSummary, hot hotSizes) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s tx summary window=%s", txLogPrefix, sum.Window.Round(time.Second))
	fmt.Fprintf(&b, " write_n=%d", sum.Write.N)
	writeTxStatPair(&b, "write", "lock_wait", sum.Write.LockWait)
	writeTxStatPair(&b, "write", "settle", sum.Write.Settle)
	writeTxStatPair(&b, "write", "exec", sum.Write.Exec)
	writeTxStatPair(&b, "write", "commit", sum.Write.Commit)
	fmt.Fprintf(&b, " read_n=%d", sum.Read.N)
	writeTxStatPair(&b, "read", "lock_wait", sum.Read.LockWait)
	writeTxStatPair(&b, "read", "settle", sum.Read.Settle)
	writeTxStatPair(&b, "read", "exec", sum.Read.Exec)
	fmt.Fprintf(&b, " hot_bytes=%s", fmtHotBytes(hot))
	if hot.ok {
		fmt.Fprintf(&b, " task_state_bytes=%d task_run_bytes=%d", hot.state, hot.run)
	}
	return b.String()
}
