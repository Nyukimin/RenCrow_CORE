package jsonlbatch

// SyncDirectory flushes the metadata of a directory so that entries created or
// renamed in it survive a crash. It is the helper New uses for its own files,
// exported for derived files that live beside the log and replace one another
// with rename (the Task index sidecar). On Windows it is a no-op for the
// reasons given in directory_sync_windows.go.
func SyncDirectory(path string) error { return syncDirectory(path) }
