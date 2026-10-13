package txobserve

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
)

// Label names the store entry point (and the record scope, when there is one)
// that started a batch transaction. The store knows nothing about the Manager-
// level operation, so Op is the store method name. ScopeKey and ScopeValue are
// printed as key=value after the op when both are set (the task store uses
// task_id).
type Label struct {
	Op         string
	ScopeKey   string
	ScopeValue string
}

type labelKey struct{}

// WithLabel attaches the label that Observer reads back from the context
// jsonlbatch hands it. A nil context is returned unchanged (the transaction is
// then reported as unlabeled).
func WithLabel(ctx context.Context, label Label) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, labelKey{}, label)
}

// LabelFrom returns the label WithLabel attached, or the zero Label.
func LabelFrom(ctx context.Context) Label {
	if ctx == nil {
		return Label{}
	}
	label, _ := ctx.Value(labelKey{}).(Label)
	return label
}

// Options configures an Observer.
type Options struct {
	// Prefix tags every line, for example "[TaskStore]". Required.
	Prefix string
	// Hot sizes the files every transaction decodes. It is called only when a
	// line is about to be written, never for a fast transaction, and outside the
	// Observer's lock, so it may stat files. Nil reports the sizes as unknown.
	Hot func() HotSizes
	// Now and Logf default to time.Now and log.Printf. Logf must be safe for
	// concurrent use.
	Now  func() time.Time
	Logf func(format string, args ...any)

	// The thresholds default (when zero) to the package constants. They are fields
	// only so tests can force the slow path (a negative value makes every
	// transaction slow); they are not configuration.
	SlowExecCommit  time.Duration
	SlowLockWait    time.Duration
	SummaryInterval time.Duration
}

// Observer turns jsonlbatch observations into the slow-transaction line and the
// periodic summary line. It starts no goroutine and no timer: both lines are
// written from Observe, on the goroutine of the transaction that just finished.
type Observer struct {
	prefix string
	hot    func() HotSizes
	now    func() time.Time
	logf   func(format string, args ...any)

	slowExecCommit  time.Duration
	slowLockWait    time.Duration
	summaryInterval time.Duration

	mu          sync.Mutex
	ring        *ring
	lastSummary time.Time
}

// New builds an Observer. The first summary comes one interval after New, so it
// never covers a handful of start-up transactions.
func New(opts Options) *Observer {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	if opts.Hot == nil {
		opts.Hot = func() HotSizes { return HotSizes{} }
	}
	if opts.SlowExecCommit == 0 {
		opts.SlowExecCommit = slowExecCommit
	}
	if opts.SlowLockWait == 0 {
		opts.SlowLockWait = slowLockWait
	}
	if opts.SummaryInterval == 0 {
		opts.SummaryInterval = summaryInterval
	}
	return &Observer{
		prefix:          opts.Prefix,
		hot:             opts.Hot,
		now:             opts.Now,
		logf:            opts.Logf,
		slowExecCommit:  opts.SlowExecCommit,
		slowLockWait:    opts.SlowLockWait,
		summaryInterval: opts.SummaryInterval,
		ring:            newRing(ringCapacity),
		lastSummary:     opts.Now(),
	}
}

// Observe is the jsonlbatch.TxObserver. jsonlbatch calls it after releasing the
// OS lock, so the stat and log calls here never lengthen the lock hold time. The
// mutex covers only the ring and the summary claim; sizing and logging happen
// outside it.
func (o *Observer) Observe(ctx context.Context, tx jsonlbatch.TxObservation) {
	now := o.now()
	s := newSample(tx, now)
	slow := isSlow(s, o.slowExecCommit, o.slowLockWait)

	var summarySamples []sample
	o.mu.Lock()
	o.ring.add(s)
	due := summaryDue(o.lastSummary, now, o.summaryInterval)
	if due {
		// Claim the summary under the mutex so concurrent finishers print it once.
		o.lastSummary = now
		summarySamples = o.ring.samples()
	}
	o.mu.Unlock()

	if !slow && !due {
		return
	}
	hot := o.hot()
	if slow {
		o.logf("%s", formatSlowLine(o.prefix, s, LabelFrom(ctx), tx.Files, hot))
	}
	if due {
		o.logf("%s", formatSummaryLine(o.prefix, summarize(summarySamples, now), hot))
	}
}
