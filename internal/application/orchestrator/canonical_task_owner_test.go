package orchestrator

import "testing"

type canonicalTestTaskOwnerSetter interface {
	SetTaskLifecycleManager(TaskLifecycleManager)
}

// attachCanonicalTestTaskOwner gives ProcessMessage tests the same owner-issued
// Task/Run boundary as production construction. The recording manager creates
// the Run only from its Start transition; callers never inject or derive one.
func attachCanonicalTestTaskOwner(t *testing.T, orchestrator canonicalTestTaskOwnerSetter) {
	t.Helper()
	orchestrator.SetTaskLifecycleManager(newRecordingTaskLifecycleManager())
}
