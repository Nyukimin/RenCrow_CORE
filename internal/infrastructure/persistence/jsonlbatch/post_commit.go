package jsonlbatch

import "context"

// CommittedAppend describes the bytes one file received from a committed Write.
type CommittedAppend struct {
	// Name is the data file the bytes were appended to.
	Name string
	// Offset is the size of the file before the append, so the bytes occupy
	// [Offset, Offset+len(Payload)).
	Offset int64
	// Payload is the appended bytes. It is read-only and valid only for the
	// duration of the hook call; a hook that needs it later must copy it.
	Payload []byte
}

// PostCommitHook is invoked by Write after the commit record is durable and
// before the OS lock is released, so a derived structure (an in-memory index)
// can follow the log under the same exclusion as the writer. It receives every
// appended file of that transaction, sorted by file name.
//
// The commit has already happened: a hook cannot veto or undo it and has no
// error to return. A hook that fails to absorb the transaction must remember
// that and resynchronize from the files on its next use. The hook is not called
// for a Write whose callback failed, that appended nothing, that was rolled
// back, or whose commit is uncertain (ErrCommitUncertain): in the last case the
// outcome is decided by the WAL shape the next writer repairs, so the caller
// learns the log's contents only by reading it.
//
// The hook runs on the calling goroutine and extends the lock hold time, so it
// must be short and must not call back into the Store.
type PostCommitHook func(ctx context.Context, appends []CommittedAppend)

// SetPostCommitHook installs hook, or removes it when hook is nil. It is safe to
// call while other goroutines use the Store. A Store without a hook does no
// extra work.
func (s *Store) SetPostCommitHook(hook PostCommitHook) {
	if hook == nil {
		s.postCommit.Store(nil)
		return
	}
	s.postCommit.Store(&hook)
}

// notifyCommitted delivers the committed transaction to the installed hook.
func (s *Store) notifyCommitted(ctx context.Context, prepared []preparedFile) {
	hook := s.postCommit.Load()
	if hook == nil {
		return
	}
	appends := make([]CommittedAppend, len(prepared))
	for i := range prepared {
		appends[i] = CommittedAppend{
			Name:    prepared[i].journal.Name,
			Offset:  prepared[i].journal.Offset,
			Payload: prepared[i].payload,
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	(*hook)(ctx, appends)
}
