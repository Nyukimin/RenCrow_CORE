//go:build taskindexdefault

package task

// This file exists only for test builds made with -tags taskindexdefault. It
// makes NewJSONLStore open every store with the in-memory index, so the existing
// consumer tests (taskmanager, heartbeat, ...) can run against the index without
// being edited. A normal build has no such switch: the index is requested only
// through OpenOptions.
func init() { defaultIndexOption = true }
