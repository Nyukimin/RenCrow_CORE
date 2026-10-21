package worldprojection_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	appprojection "github.com/Nyukimin/RenCrow_CORE/internal/application/worldprojection"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainprojection "github.com/Nyukimin/RenCrow_CORE/internal/domain/worldprojection"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/worldoutbox"
)

// The full-set projector is state-based: the Task store transaction is the
// source of truth and the outbox is a derived delivery snapshot. A crash after
// the Task commit and before delivery must therefore be repaired by the next
// projector pass, including the removal of a Task that became terminal.
func TestProjectorReconcilesTaskCommitsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	tasks, err := taskpersistence.NewJSONLStoreWithOptions(filepath.Join(root, "tasks"), taskpersistence.OpenOptions{})
	if err != nil {
		t.Fatalf("open task store: %v", err)
	}
	defer tasks.Close()
	outbox, err := worldoutbox.NewSQLiteStore(filepath.Join(root, "outbox.sqlite"))
	if err != nil {
		t.Fatalf("open outbox: %v", err)
	}
	defer outbox.Close()

	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	task := domaintask.Task{Title: "committed before projection", Route: domaintask.RouteGeneral, Assignee: "mio"}
	task.ApplyDefaults(now)
	if err := tasks.SaveTask(ctx, task); err != nil {
		t.Fatalf("SaveTask() error = %v", err)
	}

	projector := realStoreProjector(tasks, outbox, now)
	result, err := projector.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync(first) error = %v", err)
	}
	if result.Enqueued != 1 {
		t.Fatalf("first Sync() = %+v", result)
	}
	target := domainprojection.Target{
		WorldID: "world-house", TimelineID: "timeline-main", TimelineEpoch: "timeline-epoch",
		ActorID: "mio", ActorInstanceID: "instance-mio", ActorTransportID: "mio",
	}
	first, ok, err := outbox.Latest(ctx, target, domainprojection.KindWorkSet)
	if err != nil || !ok || first.SourceRevision != 1 {
		t.Fatalf("first latest = %+v, ok = %v, err = %v", first, ok, err)
	}
	if err := outbox.Complete(ctx, first.ID, appprojection.PublishResult{Status: "accepted", AcceptedAt: now}, now); err != nil {
		t.Fatalf("Complete(first) error = %v", err)
	}

	// Commit a terminal state without running the projector, as a process crash
	// between the Task transaction and the projection pass would.
	task.Status = domaintask.StatusSucceeded
	task.UpdatedAt = now.Add(time.Minute)
	if err := tasks.SaveTask(ctx, task); err != nil {
		t.Fatalf("SaveTask(terminal) error = %v", err)
	}

	// A new projector has no in-memory state; only the committed Task store and
	// the durable outbox are available.
	restarted := realStoreProjector(tasks, outbox, now.Add(2*time.Minute))
	result, err = restarted.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync(restart) error = %v", err)
	}
	if result.Enqueued != 1 {
		t.Fatalf("restart Sync() = %+v", result)
	}
	second, ok, err := outbox.Latest(ctx, target, domainprojection.KindWorkSet)
	if err != nil || !ok || second.SourceRevision != 2 {
		t.Fatalf("second latest = %+v, ok = %v, err = %v", second, ok, err)
	}
	workSet, err := decodeRecoveryWorkSet(second)
	if err != nil {
		t.Fatalf("decode second WorkSet: %v", err)
	}
	if len(workSet.ActiveWork) != 0 {
		t.Fatalf("terminal Task remained in the restarted WorkSet: %+v", workSet.ActiveWork)
	}
}

func realStoreProjector(tasks domaintask.Store, outbox *worldoutbox.SQLiteStore, now time.Time) *appprojection.Projector {
	return &appprojection.Projector{
		Source: tasks, Outbox: outbox, History: outbox, Revisions: outbox,
		WorldID: "world-house", TimelineID: "timeline-main", TimelineEpoch: "timeline-epoch",
		SourceEpoch: "core-epoch-1",
		Actors:      []appprojection.ProjectionActor{{ActorID: "mio", ActorInstanceID: "instance-mio", ActorTransportID: "mio"}},
		ValidFor:    time.Hour,
		Now:         func() time.Time { return now },
	}
}

func decodeRecoveryWorkSet(record appprojection.OutboxRecord) (domainprojection.WorkSet, error) {
	var wire struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(record.Envelope, &wire); err != nil {
		return domainprojection.WorkSet{}, err
	}
	var workSet domainprojection.WorkSet
	if err := json.Unmarshal(wire.Payload, &workSet); err != nil {
		return domainprojection.WorkSet{}, err
	}
	return workSet, nil
}
