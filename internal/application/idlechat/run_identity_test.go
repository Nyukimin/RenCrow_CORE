package idlechat

import (
	taskmanager "github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	"testing"
)

func newTestIdleChatRunIssuer(t *testing.T) *taskmanager.Manager {
	t.Helper()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	return m
}
func testIdleChatRunIdentityPair() (modulecore.TaskID, modulecore.RunID) {
	return modulecore.NewTaskID(), modulecore.NewRunID()
}
