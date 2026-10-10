package heartbeat

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
)

// A refused Heartbeat worker admission must not leave a Run-less queued Task
// behind, and the worker must not run.
func TestHeartbeatWorkerAdmissionRefusalLeavesNothingPersisted(t *testing.T) {
	saturated := taskmanagertest.NewSaturated(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("check"), 0600); err != nil {
		t.Fatal(err)
	}
	worker := &mockWorkerAgent{response: "HEARTBEAT_OK"}
	svc := NewHeartbeatService(worker, &mockSender{}, dir, 30).WithTaskOwner(saturated.Manager, "shiro")

	_ = svc.tick(context.Background())

	if worker.called {
		t.Fatal("the worker ran although admission was refused")
	}
	saturated.AssertNothingPersisted(t)
}
