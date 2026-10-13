// Package txobserve turns jsonlbatch transaction observations into the slow-
// transaction line and the periodic summary line that the JSONL stores log. The
// log contract (key names, thresholds, summary cadence) lives here once; a store
// supplies only its log prefix, the names and sizes of its hot files, and an
// optional label for the entry point that started the transaction.
package txobserve

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
)

// This file holds the I/O-free part of the transaction timing observation: the
// sample type, a bounded ring, percentile arithmetic, the slow and "summary due"
// decisions, and the two log-line formats. The thresholds are code constants on
// purpose; they are not configuration.
const (
	// slowExecCommit marks a transaction slow when exec + commit exceeds it.
	slowExecCommit = 5 * time.Second
	// slowLockWait marks a transaction slow when the flock wait exceeds it.
	slowLockWait = 10 * time.Second
	// summaryInterval is the minimum gap between two summary lines.
	summaryInterval = 10 * time.Minute
	// ringCapacity is how many recent transactions the summary covers.
	ringCapacity = 512

	// unlabeledOp is the op of a transaction whose caller attached no label.
	unlabeledOp = "unlabeled"
)

// sample is one finished transaction reduced to what the summary needs.
type sample struct {
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

func newSample(tx jsonlbatch.TxObservation, at time.Time) sample {
	write := tx.Kind == jsonlbatch.TxKindWrite
	return sample{
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

func (s sample) kind() string {
	if s.Write {
		return string(jsonlbatch.TxKindWrite)
	}
	return string(jsonlbatch.TxKindRead)
}

// result classifies how the call ended without carrying any error text.
func (s sample) result() string {
	switch {
	case !s.Acquired:
		return "lock_failed"
	case s.Failed:
		return "error"
	default:
		return "ok"
	}
}

// isSlow applies the slow rule: exec + commit above execCommit, or lock wait
// above lockWait. Settle is deliberately not part of the rule.
func isSlow(s sample, execCommit, lockWait time.Duration) bool {
	return s.Exec+s.Commit > execCommit || s.LockWait > lockWait
}

// summaryDue reports whether a summary should be written now. A clock that
// moved backwards is never due.
func summaryDue(last, now time.Time, interval time.Duration) bool {
	return now.Sub(last) >= interval
}

// ring keeps the most recent samples in a fixed-size buffer. It is not safe
// for concurrent use; Observer serialises access.
type ring struct {
	buf   []sample
	next  int
	count int
}

func newRing(capacity int) *ring {
	if capacity < 1 {
		capacity = 1
	}
	return &ring{buf: make([]sample, capacity)}
}

func (r *ring) add(s sample) {
	r.buf[r.next] = s
	r.next = (r.next + 1) % len(r.buf)
	if r.count < len(r.buf) {
		r.count++
	}
}

// samples returns a copy of the held samples, oldest first.
func (r *ring) samples() []sample {
	out := make([]sample, r.count)
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

// kindSummary summarises one kind (read or write). Populations differ:
// LockWait covers every transaction of the kind, Settle and Exec only those that
// held the lock, and Commit only writes that committed something.
type kindSummary struct {
	N        int
	LockWait durationStat
	Settle   durationStat
	Exec     durationStat
	Commit   durationStat
}

type summary struct {
	// Window is the age of the oldest sample the summary covers.
	Window time.Duration
	Write  kindSummary
	Read   kindSummary
}

func summarize(samples []sample, now time.Time) summary {
	var sum summary
	if len(samples) == 0 {
		return sum
	}
	sum.Window = now.Sub(samples[0].At)
	if sum.Window < 0 {
		sum.Window = 0
	}
	sum.Write = summarizeKind(samples, true)
	sum.Read = summarizeKind(samples, false)
	return sum
}

func summarizeKind(samples []sample, write bool) kindSummary {
	var lockWait, settle, exec, commit []time.Duration
	var result kindSummary
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

// HotFile is the byte size of one file every transaction decodes. Name is the
// key stem the summary line reports it under: Name "task_state" is printed as
// task_state_bytes=N.
type HotFile struct {
	Name  string
	Bytes int64
}

// HotSizes are the byte sizes of the files every transaction decodes. OK is false
// when any of them could not be sized; the lines then say hot_bytes=unknown and
// carry no per-file keys.
type HotSizes struct {
	Files []HotFile
	OK    bool
}

// HotPath is a hot file to size: Name is the key stem (see HotFile), Path where
// the file is.
type HotPath struct {
	Name string
	Path string
}

// SizeFiles stats each file and never decodes it. The result is OK only when every
// file was sized; one failing stat makes the whole answer unknown, because a
// total over some of the files would be misleading.
func SizeFiles(files ...HotPath) HotSizes {
	sized := make([]HotFile, 0, len(files))
	for _, f := range files {
		info, err := os.Stat(f.Path)
		if err != nil {
			return HotSizes{}
		}
		sized = append(sized, HotFile{Name: f.Name, Bytes: info.Size()})
	}
	return HotSizes{Files: sized, OK: true}
}

func (h HotSizes) total() int64 {
	var sum int64
	for _, f := range h.Files {
		sum += f.Bytes
	}
	return sum
}

func fmtDuration(d time.Duration) string {
	return d.Round(time.Millisecond).String()
}

func fmtStat(d time.Duration, present bool) string {
	if !present {
		return "-"
	}
	return fmtDuration(d)
}

// formatSlowLine renders the one-line slow-transaction record. It carries the
// op, the label's scope id and file names only, never record bodies or error text.
func formatSlowLine(prefix string, s sample, label Label, files []string, hot HotSizes) string {
	op := label.Op
	if op == "" {
		op = unlabeledOp
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s tx slow kind=%s op=%s", prefix, s.kind(), op)
	if label.ScopeKey != "" && label.ScopeValue != "" {
		fmt.Fprintf(&b, " %s=%s", label.ScopeKey, label.ScopeValue)
	}
	fmt.Fprintf(&b, " result=%s lock_wait=%s settle=%s exec=%s commit=%s",
		s.result(), fmtDuration(s.LockWait), fmtDuration(s.Settle), fmtDuration(s.Exec), fmtDuration(s.Commit))
	if len(files) > 0 {
		fmt.Fprintf(&b, " files=%s", strings.Join(files, "+"))
	}
	fmt.Fprintf(&b, " hot_bytes=%s", fmtHotBytes(hot))
	return b.String()
}

func fmtHotBytes(hot HotSizes) string {
	if !hot.OK {
		return "unknown"
	}
	return strconv.FormatInt(hot.total(), 10)
}

func writeStatPair(b *strings.Builder, prefix, name string, stat durationStat) {
	present := stat.N > 0
	fmt.Fprintf(b, " %s_%s_p50=%s %s_%s_p95=%s", prefix, name, fmtStat(stat.P50, present), prefix, name, fmtStat(stat.P95, present))
}

// formatSummaryLine renders the periodic one-line summary. Read has no commit
// phase, so its commit keys are omitted.
func formatSummaryLine(prefix string, sum summary, hot HotSizes) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s tx summary window=%s", prefix, sum.Window.Round(time.Second))
	fmt.Fprintf(&b, " write_n=%d", sum.Write.N)
	writeStatPair(&b, "write", "lock_wait", sum.Write.LockWait)
	writeStatPair(&b, "write", "settle", sum.Write.Settle)
	writeStatPair(&b, "write", "exec", sum.Write.Exec)
	writeStatPair(&b, "write", "commit", sum.Write.Commit)
	fmt.Fprintf(&b, " read_n=%d", sum.Read.N)
	writeStatPair(&b, "read", "lock_wait", sum.Read.LockWait)
	writeStatPair(&b, "read", "settle", sum.Read.Settle)
	writeStatPair(&b, "read", "exec", sum.Read.Exec)
	fmt.Fprintf(&b, " hot_bytes=%s", fmtHotBytes(hot))
	if hot.OK {
		for _, f := range hot.Files {
			fmt.Fprintf(&b, " %s_bytes=%d", f.Name, f.Bytes)
		}
	}
	return b.String()
}
