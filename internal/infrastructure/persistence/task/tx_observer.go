package task

import (
	"context"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/txobserve"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// The task store's transaction timing observation is the shared txobserve
// package; this file supplies only what is the task store's own: the log prefix,
// the task_id label scope, and the two hot files (which move on rotation).
const (
	// txLogPrefix tags every line this observation writes.
	txLogPrefix = "[TaskStore]"
	// txScopeTaskID is the label key the Task scope is printed under.
	txScopeTaskID = "task_id"
	// The hot file names the summary line reports sizes under
	// (task_state_bytes, task_run_bytes).
	txHotStateName = "task_state"
	txHotRunName   = "task_run"
)

// withTxLabel attaches the label that the observer reads back from the context
// jsonlbatch hands it. The store knows nothing about the Manager-level operation
// (Create / Start / Succeed), so op is the store method name. A nil context is
// returned unchanged (the transaction is then reported as unlabeled).
func withTxLabel(ctx context.Context, op string, taskID modulecore.TaskID) context.Context {
	return txobserve.WithLabel(ctx, txobserve.Label{Op: op, ScopeKey: txScopeTaskID, ScopeValue: string(taskID)})
}

// txHotPaths are the two files every task transaction decodes. A rotation points
// it at the files of the new segment; the observer may size them from the
// goroutine of any transaction that finishes, so access is guarded.
type txHotPaths struct {
	mu    sync.RWMutex
	state string
	run   string
}

func newTxHotPaths(statePath, runPath string) *txHotPaths {
	return &txHotPaths{state: statePath, run: runPath}
}

// set points the sizing at the two hot files of another segment.
func (p *txHotPaths) set(statePath, runPath string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state, p.run = statePath, runPath
}

// sizes stats the two hot files. It never decodes them.
func (p *txHotPaths) sizes() txobserve.HotSizes {
	p.mu.RLock()
	statePath, runPath := p.state, p.run
	p.mu.RUnlock()
	return txobserve.SizeFiles(
		txobserve.HotPath{Name: txHotStateName, Path: statePath},
		txobserve.HotPath{Name: txHotRunName, Path: runPath},
	)
}

// txObserverOptions are the options the task store's observer is built from.
// Tests change a copy to capture the log or force the slow path.
func txObserverOptions(hot *txHotPaths) txobserve.Options {
	return txobserve.Options{Prefix: txLogPrefix, Hot: hot.sizes}
}

// newTxObserver builds the production observer for the two hot files.
func newTxObserver(hot *txHotPaths) *txobserve.Observer {
	return txobserve.New(txObserverOptions(hot))
}
