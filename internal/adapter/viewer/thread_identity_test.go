package viewer

import (
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func canonicalViewerTestThreadID(t *testing.T, value string) modulecore.ThreadID {
	t.Helper()
	return mustCanonicalViewerTestThreadID(value)
}

func mustCanonicalViewerTestThreadID(value string) modulecore.ThreadID {
	raw, err := modulecore.NewMigrationID(modulecore.CanonicalThreadID, "viewer_test", "fixture", value)
	if err != nil {
		panic(err)
	}
	return modulecore.ThreadID(raw)
}
