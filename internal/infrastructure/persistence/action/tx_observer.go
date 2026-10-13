package action

import (
	"context"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/txobserve"
)

// The Action store's transaction timing observation is the shared txobserve
// package, the same one the Task store uses; this file supplies only what is the
// Action store's own: the log prefix, the two hot files, and the op labels.
const (
	// txLogPrefix tags every line this observation writes.
	txLogPrefix = "[ActionStore]"
	// The hot file names the summary line reports sizes under
	// (action_state_bytes, action_attempt_bytes).
	txHotActionName  = "action_state"
	txHotAttemptName = "action_attempt"

	// txOpOpen labels the validation read that opens the store.
	txOpOpen = "Open"
	// txOpRotate and txOpMigrate label the read that makes the snapshot current
	// before a rotation closes a segment, or before the move to segments.
	txOpRotate  = "Rotate"
	txOpMigrate = "Migrate"
	// The entry points that open a batch transaction. The single-record methods
	// are labelled separately from the generic Transaction / ReadTransaction so
	// that a slow line says which kind of call was slow.
	txOpTransaction     = "Transaction"
	txOpReadTransaction = "ReadTransaction"
	txOpSaveAction      = "SaveAction"
	txOpGetAction       = "GetAction"
	txOpListActions     = "ListActions"
	txOpSaveAttempt     = "SaveAttempt"
	txOpGetAttempt      = "GetAttempt"
	txOpListAttempts    = "ListAttempts"
)

// withTxLabel attaches the op (the store method name) that the observer reads
// back from the context jsonlbatch hands it. The Action store labels the op only:
// the Action and Attempt ids are record identifiers a log line does not need.
func withTxLabel(ctx context.Context, op string) context.Context {
	return txobserve.WithLabel(ctx, txobserve.Label{Op: op})
}

// txObserverOptions are the options the Action store's observer is built from:
// it sizes the two files every transaction decodes, which are those of the
// current segment (hot says which they are now).
func txObserverOptions(hot func() [fileCount]string) txobserve.Options {
	return txobserve.Options{
		Prefix: txLogPrefix,
		Hot: func() txobserve.HotSizes {
			paths := hot()
			return txobserve.SizeFiles(
				txobserve.HotPath{Name: txHotActionName, Path: paths[fileAction]},
				txobserve.HotPath{Name: txHotAttemptName, Path: paths[fileAttempt]},
			)
		},
	}
}
