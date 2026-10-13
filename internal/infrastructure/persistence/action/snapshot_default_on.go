//go:build actionindexdefault

package action

// A build made with -tags actionindexdefault opens every store the plain
// constructor returns with the snapshot, so the tests of every consumer of the
// Action store (the Manager, the recoveries, the Viewer handlers) run against it
// without changing a line. The flag is off in every other build.
func init() { defaultIndexOption = true }
