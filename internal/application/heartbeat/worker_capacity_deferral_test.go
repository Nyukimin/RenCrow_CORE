package heartbeat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	appbacklog "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	gmailapp "github.com/Nyukimin/RenCrow_CORE/internal/application/gmailintake"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// A refusal for lack of execution capacity is not a failure: nothing was
// attempted, nothing was persisted, and the next scheduled tick tries again.
// The scheduler's own period is the (bounded) backoff; no loop retries here.

func eventTypes(listener *recordingEventListener) map[string]int {
	counts := map[string]int{}
	for _, event := range listener.events {
		counts[event.Type]++
	}
	return counts
}

func TestHeartbeatTickDefersInsteadOfFailingWhenExecutionCapacityIsUnavailable(t *testing.T) {
	saturated := taskmanagertest.NewSaturated(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("check"), 0600); err != nil {
		t.Fatal(err)
	}
	worker := &mockWorkerAgent{response: "HEARTBEAT_OK"}
	listener := &recordingEventListener{}
	svc := NewHeartbeatService(worker, &mockSender{}, dir, 30).WithTaskOwner(saturated.Manager, "shiro").WithEventListener(listener)

	if err := svc.tick(context.Background()); err != nil {
		t.Fatalf("a capacity refusal must be deferred, not returned as a failure: %v", err)
	}
	counts := eventTypes(listener)
	if counts["heartbeat.error"] != 0 || counts["heartbeat.skip"] != 1 {
		t.Fatalf("events=%v, want one heartbeat.skip and no heartbeat.error", counts)
	}
	if worker.called {
		t.Fatal("the worker ran although admission was refused")
	}
	saturated.AssertNothingPersisted(t)
}

func TestBacklogRunnerDefersWhenAtlasAdmissionIsRefusedForCapacity(t *testing.T) {
	backlogStore := &memoryBacklogStore{items: []domainbacklog.Item{{
		SchemaVersion:      domainbacklog.SchemaVersion2,
		BacklogItemID:      "runner-deferred",
		ImplementationUnit: "unit-runner-deferred",
		WorkstreamID:       "ws-runner-deferred",
		Title:              "deferred",
		ConceptState:       domainbacklog.ConceptAdopted,
		DeliveryState:      domainbacklog.DeliverySpec,
		Status:             "implementing",
	}}}
	// The Atlas owner wraps the Task owner's refusal with %w.
	atlas := &revision2AtlasRunnerFake{acquireErr: fmt.Errorf("start Atlas run: %w", taskmanager.ErrParallelLimit)}
	listener := &recordingEventListener{}
	worker := &mockWorkerAgent{response: "unused"}
	svc := NewHeartbeatService(worker, &mockSender{}, t.TempDir(), 30).
		WithBacklogStore(backlogStore).WithAtlasService(atlas).WithEventListener(listener)
	svc = withHeartbeatTestTaskOwner(t, svc)

	report, err := svc.RunBacklogRunner(context.Background(), time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("a capacity refusal must be deferred, not returned as a failure: %v", err)
	}
	if report.Failed != 0 || report.Started != 0 || report.Skipped != 1 {
		t.Fatalf("report=%+v, want skipped=1 failed=0", report)
	}
	if counts := eventTypes(listener); counts["backlog.runner.error"] != 0 {
		t.Fatalf("events=%v, a deferral must not emit backlog.runner.error", counts)
	}
	if worker.called {
		t.Fatal("the worker ran although Atlas admission was refused")
	}
}

func TestBacklogRunnerDefersWhenWorkerAdmissionIsRefusedForCapacity(t *testing.T) {
	saturated := taskmanagertest.NewSaturated(t)
	item := domainbacklog.Item{
		SchemaVersion:      domainbacklog.SchemaVersion2,
		BacklogItemID:      "runner-worker-deferred",
		ImplementationUnit: "unit-runner-worker-deferred",
		WorkstreamID:       "ws-runner-worker-deferred",
		Title:              "worker deferred",
		ConceptState:       domainbacklog.ConceptAdopted,
		DeliveryState:      domainbacklog.DeliverySpec,
		Status:             "implementing",
	}
	backlogStore := &memoryBacklogStore{items: []domainbacklog.Item{item}}
	atlas := &revision2AtlasRunnerFake{result: appbacklog.AcquireRunnableResult{Item: item, Acquired: true}}
	listener := &recordingEventListener{}
	worker := &mockWorkerAgent{response: "unused"}
	svc := NewHeartbeatService(worker, &mockSender{}, t.TempDir(), 30).
		WithBacklogStore(backlogStore).WithAtlasService(atlas).WithEventListener(listener).
		WithTaskOwner(saturated.Manager, "shiro")

	report, err := svc.RunBacklogRunner(context.Background(), time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("a capacity refusal must be deferred, not returned as a failure: %v", err)
	}
	if report.Failed != 0 || report.Started != 0 {
		t.Fatalf("report=%+v, want failed=0 started=0", report)
	}
	if counts := eventTypes(listener); counts["backlog.runner.error"] != 0 {
		t.Fatalf("events=%v, a deferral must not emit backlog.runner.error", counts)
	}
	if worker.called || len(atlas.reviseCalls) != 0 {
		t.Fatalf("worker=%t revise=%d, want neither after a refused admission", worker.called, len(atlas.reviseCalls))
	}
	saturated.AssertNothingPersisted(t)
}

func TestWorkstreamHeartbeatSweepDefersWhenExecutionCapacityIsUnavailable(t *testing.T) {
	saturated := taskmanagertest.NewSaturated(t)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	store := &memoryWorkstreamHeartbeatStore{
		schedules: []domainworkstream.HeartbeatSchedule{{
			ScheduleID:   modulecore.ScheduleID("sch_b9bd908d-fe51-5109-8877-58900d2d1cc8"),
			WorkstreamID: "ws_1",
			ScheduleText: "daily 08:00",
			Task:         "due",
			Status:       domainworkstream.StatusActive,
			NextRunAt:    now.Add(-time.Minute),
			CreatedAt:    now,
		}},
	}
	listener := &recordingEventListener{}
	worker := &mockWorkerAgent{response: "unused"}
	svc := NewHeartbeatService(worker, &mockSender{}, t.TempDir(), 30).
		WithWorkstreamStore(store).WithEventListener(listener).WithTaskOwner(saturated.Manager, "shiro")

	report, err := svc.RunDueWorkstreamHeartbeats(context.Background(), now)
	if err != nil {
		t.Fatalf("a capacity refusal must be deferred, not returned as a failure: %v", err)
	}
	if report.Failed != 0 || report.Run != 0 || report.Skipped != 1 {
		t.Fatalf("report=%+v, want skipped=1 failed=0 run=0", report)
	}
	if counts := eventTypes(listener); counts["workstream.heartbeat.error"] != 0 {
		t.Fatalf("events=%v, a deferral must not emit workstream.heartbeat.error", counts)
	}
	if worker.called {
		t.Fatal("the worker ran although admission was refused")
	}
	saturated.AssertNothingPersisted(t)
}

func TestGmailIntakeDefersWhenExecutionCapacityIsUnavailable(t *testing.T) {
	saturated := taskmanagertest.NewSaturated(t)
	listener := &recordingEventListener{}
	called := false
	intake := gmailIntakeFunc(func(context.Context) (gmailapp.GmailRunReport, error) {
		called = true
		return gmailapp.GmailRunReport{}, nil
	})
	svc := NewHeartbeatService(nil, nil, t.TempDir(), 30).
		WithTaskOwner(saturated.Manager, "shiro").WithEventListener(listener).
		WithGmailIntake(intake, "ren", time.Hour, time.Minute, false)

	if !svc.startGmailIntake() {
		t.Fatal("collection should start")
	}
	svc.waitForGmailIntake()

	counts := eventTypes(listener)
	if counts["heartbeat.gmail.deferred"] != 1 || counts["heartbeat.gmail.error"] != 0 {
		t.Fatalf("events=%v, want one deferred and no error", counts)
	}
	if called {
		t.Fatal("the intake ran although admission was refused")
	}
	saturated.AssertNothingPersisted(t)
}

func TestXBookmarkCollectionDefersWhenExecutionCapacityIsUnavailable(t *testing.T) {
	saturated := taskmanagertest.NewSaturated(t)
	listener := &recordingEventListener{}
	collector := &blockingXBookmarkCollector{started: make(chan struct{}, 1), release: make(chan struct{}), canceled: make(chan struct{}, 1)}
	svc := NewHeartbeatService(&mockWorkerAgent{response: "HEARTBEAT_OK"}, nil, t.TempDir(), 30).
		WithTaskOwner(saturated.Manager, "shiro").WithEventListener(listener).
		WithXBookmarkCollection(collector, time.Hour, time.Minute, false)

	if !svc.startXBookmarkCollection() {
		t.Fatal("collection should start")
	}
	svc.waitForXBookmarkCollection()

	counts := eventTypes(listener)
	if counts["heartbeat.x_bookmarks.deferred"] != 1 || counts["heartbeat.x_bookmarks.error"] != 0 {
		t.Fatalf("events=%v, want one deferred and no error", counts)
	}
	if collector.calls.Load() != 0 {
		t.Fatal("the collector ran although admission was refused")
	}
	saturated.AssertNothingPersisted(t)
}
