//go:build taskindexdefault

package task

// This file exists only for test builds made with -tags taskindexdefault. It
// makes NewJSONLStore open every store with the in-memory index, so the existing
// consumer tests (taskmanager, heartbeat, ...) can run against the index without
// being edited. The sidecar is on too, so those tests also exercise restore and
// checkpoint. A normal build has no such switch: the index is requested only
// through OpenOptions.
func init() {
	defaultIndexOption = true
	defaultPersistOption = true
}
