package task

import (
	"context"
	"log"
	"os"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// txLabel names the store entry point (and the Task, when scoped to one) that
// started a batch transaction. The store knows nothing about the Manager-level
// operation (Create / Start / Succeed), so op is the store method name.
type txLabel struct {
	op     string
	taskID modulecore.TaskID
}

type txLabelKey struct{}

// withTxLabel attaches the label that txObserver reads back from the context
// jsonlbatch hands it. A nil context is returned unchanged (the transaction is
// then reported as unlabeled).
func withTxLabel(ctx context.Context, op string, taskID modulecore.TaskID) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, txLabelKey{}, txLabel{op: op, taskID: taskID})
}

func txLabelFrom(ctx context.Context) txLabel {
	if ctx == nil {
		return txLabel{}
	}
	label, _ := ctx.Value(txLabelKey{}).(txLabel)
	return label
}

// txObserverDeps are the effects txObserver needs; tests replace them.
type txObserverDeps struct {
	now      func() time.Time
	logf     func(format string, args ...any)
	statSize func(path string) (int64, error)
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// txObserver turns jsonlbatch observations into the slow-transaction line and
// the periodic summary line. It starts no goroutine and no timer: both lines are
// written from observe, on the goroutine of the transaction that just finished.
type txObserver struct {
	statePath, runPath string
	now                func() time.Time
	logf               func(format string, args ...any)
	statSize           func(path string) (int64, error)

	// Thresholds default to the package constants. They are fields only so tests
	// can force the slow path; they are not configuration.
	slowExecCommit  time.Duration
	slowLockWait    time.Duration
	summaryInterval time.Duration

	mu          sync.Mutex
	ring        *txRing
	lastSummary time.Time
}

// newTxObserver builds the production observer for the two hot files.
func newTxObserver(statePath, runPath string) *txObserver {
	return newTxObserverWith(statePath, runPath, txObserverDeps{})
}

func newTxObserverWith(statePath, runPath string, deps txObserverDeps) *txObserver {
	if deps.now == nil {
		deps.now = time.Now
	}
	if deps.logf == nil {
		deps.logf = log.Printf
	}
	if deps.statSize == nil {
		deps.statSize = fileSize
	}
	return &txObserver{
		statePath:       statePath,
		runPath:         runPath,
		now:             deps.now,
		logf:            deps.logf,
		statSize:        deps.statSize,
		slowExecCommit:  txSlowExecCommit,
		slowLockWait:    txSlowLockWait,
		summaryInterval: txSummaryInterval,
		ring:            newTxRing(txRingCapacity),
		// The first summary comes one interval after the store opened, so it
		// never covers a handful of start-up transactions.
		lastSummary: deps.now(),
	}
}

// observe is the jsonlbatch.TxObserver. jsonlbatch calls it after releasing the
// OS lock, so the stat and log calls here never lengthen the lock hold time.
func (o *txObserver) observe(ctx context.Context, tx jsonlbatch.TxObservation) {
	now := o.now()
	sample := newTxSample(tx, now)
	slow := isSlowTx(sample, o.slowExecCommit, o.slowLockWait)

	var summarySamples []txSample
	o.mu.Lock()
	o.ring.add(sample)
	due := txSummaryDue(o.lastSummary, now, o.summaryInterval)
	if due {
		// Claim the summary under the mutex so concurrent finishers print it once.
		o.lastSummary = now
		summarySamples = o.ring.samples()
	}
	o.mu.Unlock()

	if !slow && !due {
		return
	}
	hot := o.hotSizes()
	if slow {
		o.logf("%s", formatTxSlowLine(sample, txLabelFrom(ctx), tx.Files, hot))
	}
	if due {
		o.logf("%s", formatTxSummaryLine(summarizeTx(summarySamples, now), hot))
	}
}

// hotSizes stats the two hot files. It never decodes them.
func (o *txObserver) hotSizes() hotSizes {
	state, stateErr := o.statSize(o.statePath)
	run, runErr := o.statSize(o.runPath)
	if stateErr != nil || runErr != nil {
		return hotSizes{}
	}
	return hotSizes{state: state, run: run, ok: true}
}
