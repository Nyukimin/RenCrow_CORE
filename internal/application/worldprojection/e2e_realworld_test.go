//go:build realworld

package worldprojection_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appagentprofile "github.com/Nyukimin/RenCrow_CORE/internal/application/agentprofile"
	appprojection "github.com/Nyukimin/RenCrow_CORE/internal/application/worldprojection"
	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainprojection "github.com/Nyukimin/RenCrow_CORE/internal/domain/worldprojection"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/worldoutbox"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRealWorldEpochUnchangedRequiresMatchingHead(t *testing.T) {
	bin := strings.TrimSpace(os.Getenv("RENCROW_WORLD_BIN"))
	if bin == "" {
		t.Skip("RENCROW_WORLD_BIN is not set")
	}
	baseURL, stop := startRealWorld(t, bin)
	defer stop()

	target := domainprojection.Target{
		WorldID: "rencrow-home", TimelineID: "timeline-1", TimelineEpoch: "epoch-e2e",
		ActorID: "mio", ActorInstanceID: "core-instance-mio", ActorTransportID: "mio",
	}
	token := "core-e2e-token"
	// epoch_unchanged is a reconcile write, so CORE needs the separate reconcile
	// credential the harness grants; the projection credential alone fails closed.
	publisher, err := appprojection.NewHTTPPublisherWithReconcile(baseURL, token, "core-e2e-reconcile-token", 5*time.Second)
	if err != nil {
		t.Fatalf("NewHTTPPublisher() error = %v", err)
	}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	validUntil := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)

	revision1 := buildEnvelope(t, target, validUntil, 1, []domainprojection.WorkItem{})
	accepted := drainOne(t, publisher, target, "accepted", revision1, false, now)
	if accepted.Status != appprojection.OutboxSent || accepted.AckStatus != "accepted" {
		t.Fatalf("accepted record = %+v", accepted)
	}

	unchanged := drainOne(t, publisher, target, "unchanged", revision1, true, now)
	if unchanged.Status != appprojection.OutboxSent || unchanged.AckStatus != "epoch_unchanged" {
		t.Fatalf("epoch_unchanged record = %+v", unchanged)
	}

	revision2 := buildEnvelope(t, target, validUntil, 2, []domainprojection.WorkItem{{
		TaskID: "task-2", ExecutionState: domainprojection.ExecutionQueued,
		WorkClass: domainprojection.WorkClassAssignedWork, ClassificationReasonCode: "assigned",
	}})
	mismatch := drainOne(t, publisher, target, "mismatch", revision2, true, now)
	if mismatch.Status != appprojection.OutboxFailed || !strings.Contains(mismatch.LastError, "head verification failed") {
		t.Fatalf("mismatched epoch_unchanged record = %+v", mismatch)
	}
}

type realWorldSource struct {
	tasks []domaintask.Task
	runs  []domaintask.Run
}

func (s *realWorldSource) ListTasks(_ context.Context, filter domaintask.Filter) ([]domaintask.Task, error) {
	var result []domaintask.Task
	for _, task := range s.tasks {
		if (filter.Status == "" || task.Status == filter.Status) &&
			(filter.Assignee == "" || task.Assignee == filter.Assignee) {
			result = append(result, task)
		}
	}
	return result, nil
}

func (s *realWorldSource) ListRuns(_ context.Context, filter domaintask.RunFilter) ([]domaintask.Run, error) {
	var result []domaintask.Run
	for _, run := range s.runs {
		if filter.Status == "" || run.Status == filter.Status {
			result = append(result, run)
		}
	}
	return result, nil
}

func realWorldTask(assignee string) domaintask.Task {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	return domaintask.Task{
		TaskID: modulecore.NewTaskID(), Title: "projected work", Route: domaintask.RouteGeneral,
		Assignee: assignee, Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal,
		InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now,
	}
}

func TestRealWorldProjectorEnqueuesAndRefreshesFullWorkSet(t *testing.T) {
	bin := strings.TrimSpace(os.Getenv("RENCROW_WORLD_BIN"))
	if bin == "" {
		t.Skip("RENCROW_WORLD_BIN is not set")
	}
	baseURL, stop := startRealWorld(t, bin)
	defer stop()

	source := &realWorldSource{tasks: []domaintask.Task{realWorldTask("mio")}}
	store, err := worldoutbox.NewSQLiteStore(filepath.Join(t.TempDir(), "projector.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	publisher, err := appprojection.NewHTTPPublisher(baseURL, "core-e2e-token", 5*time.Second)
	if err != nil {
		t.Fatalf("NewHTTPPublisher() error = %v", err)
	}
	projector := &appprojection.Projector{
		Source: source, Outbox: store, History: store, Revisions: store,
		WorldID: "rencrow-home", TimelineID: "timeline-1", TimelineEpoch: "epoch-e2e",
		SourceEpoch: "core-epoch-e2e",
		Actors: []appprojection.ProjectionActor{{
			ActorID: "mio", ActorInstanceID: "core-instance-mio", ActorTransportID: "mio",
		}},
		ValidFor: time.Hour,
		Now:      func() time.Time { return now },
	}
	result, err := projector.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync(first) error = %v", err)
	}
	if result.Enqueued != 1 {
		t.Fatalf("first Sync() = %+v", result)
	}
	worker := &appprojection.Worker{Outbox: store, Publisher: publisher, HeadReader: publisher, Now: func() time.Time { return now }}
	if drained, err := worker.Drain(context.Background()); err != nil || drained.Sent != 1 {
		t.Fatalf("Drain(first) = %+v, err = %v", drained, err)
	}
	first, ok, err := store.Latest(context.Background(), domainprojection.Target{
		WorldID: "rencrow-home", TimelineID: "timeline-1", TimelineEpoch: "epoch-e2e",
		ActorID: "mio", ActorInstanceID: "core-instance-mio", ActorTransportID: "mio",
	}, domainprojection.KindWorkSet)
	if err != nil || !ok || first.Status != appprojection.OutboxSent || first.SourceRevision != 1 {
		t.Fatalf("first latest = %+v, ok = %v, err = %v", first, ok, err)
	}
	head, err := publisher.ReadHead(context.Background(), first.Target, domainprojection.KindWorkSet)
	if err != nil {
		t.Fatalf("ReadHead(first) error = %v", err)
	}
	if head.SourceRevision != first.SourceRevision || head.Digest != first.Digest {
		t.Fatalf("head = %+v, record = %+v", head, first)
	}

	result, err = projector.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync(second) error = %v", err)
	}
	if result.Enqueued != 0 || result.Skipped != 1 {
		t.Fatalf("second Sync() = %+v", result)
	}

	source.tasks = append(source.tasks, realWorldTask("mio"))
	result, err = projector.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync(third) error = %v", err)
	}
	if result.Enqueued != 1 {
		t.Fatalf("third Sync() = %+v", result)
	}
	if drained, err := worker.Drain(context.Background()); err != nil || drained.Sent != 1 {
		t.Fatalf("Drain(second) = %+v, err = %v", drained, err)
	}
	second, ok, err := store.Latest(context.Background(), first.Target, domainprojection.KindWorkSet)
	if err != nil || !ok || second.Status != appprojection.OutboxSent || second.SourceRevision != 2 {
		t.Fatalf("second latest = %+v, ok = %v, err = %v", second, ok, err)
	}
}

func TestRealWorldProjectorProjectsDerivedLifePolicy(t *testing.T) {
	bin := strings.TrimSpace(os.Getenv("RENCROW_WORLD_BIN"))
	if bin == "" {
		t.Skip("RENCROW_WORLD_BIN is not set")
	}
	baseURL, stop := startRealWorld(t, bin)
	defer stop()

	source := &realWorldSource{tasks: []domaintask.Task{realWorldTask("mio")}}
	store, err := worldoutbox.NewSQLiteStore(filepath.Join(t.TempDir(), "life-policy.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	publisher, err := appprojection.NewHTTPPublisher(baseURL, "core-e2e-token", 5*time.Second)
	if err != nil {
		t.Fatalf("NewHTTPPublisher() error = %v", err)
	}
	personas := map[string]domainpersona.CharacterProfile{
		"mio": {CharacterID: "mio", Persona: map[string]string{"self": "first persona"}},
	}
	projector := &appprojection.Projector{
		Source: source, Outbox: store, History: store, Revisions: store,
		LifePolicySource: &appprojection.CatalogLifePolicySource{
			Profiles: appagentprofile.NewStaticCatalog(),
			Personas: personas,
		},
		WorldID: "rencrow-home", TimelineID: "timeline-1", TimelineEpoch: "epoch-e2e",
		SourceEpoch: "core-epoch-e2e",
		Actors: []appprojection.ProjectionActor{{
			ActorID: "mio", ActorInstanceID: "core-instance-mio", ActorTransportID: "mio",
		}},
		ValidFor: time.Hour,
		Now:      func() time.Time { return now },
	}
	target := domainprojection.Target{
		WorldID: "rencrow-home", TimelineID: "timeline-1", TimelineEpoch: "epoch-e2e",
		ActorID: "mio", ActorInstanceID: "core-instance-mio", ActorTransportID: "mio",
	}
	result, err := projector.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync(first) error = %v", err)
	}
	if result.Enqueued != 1 || result.LifePoliciesEnqueued != 1 {
		t.Fatalf("first Sync() = %+v", result)
	}
	worker := &appprojection.Worker{Outbox: store, Publisher: publisher, HeadReader: publisher, Now: func() time.Time { return now }}
	if drained, err := worker.Drain(context.Background()); err != nil || drained.Sent != 2 {
		t.Fatalf("Drain(first) = %+v, err = %v", drained, err)
	}
	first, ok, err := store.Latest(context.Background(), target, domainprojection.KindLifePolicy)
	if err != nil || !ok || first.Status != appprojection.OutboxSent || first.SourceRevision != 1 {
		t.Fatalf("first life policy = %+v, ok = %v, err = %v", first, ok, err)
	}
	head, err := publisher.ReadHead(context.Background(), target, domainprojection.KindLifePolicy)
	if err != nil {
		t.Fatalf("ReadHead(life policy) error = %v", err)
	}
	if head.SourceRevision != first.SourceRevision || head.Digest != first.Digest {
		t.Fatalf("life policy head = %+v, record = %+v", head, first)
	}
	policy := decodeLifePolicyPayload(t, first)
	if policy.ActorID != "mio" || len(policy.AllowedNonCognitiveActivityIDs) != 8 ||
		len(policy.ExpiredFallbackActivityIDs) != 3 ||
		!strings.HasPrefix(policy.PersonaRevision, "persona-sha256:") ||
		!strings.HasPrefix(policy.ConditionProfileRef, "agent-profile-sha256:") {
		t.Fatalf("life policy payload = %+v", policy)
	}

	result, err = projector.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync(second) error = %v", err)
	}
	if result.Enqueued != 0 || result.Skipped != 1 || result.LifePoliciesEnqueued != 0 || result.LifePoliciesSkipped != 1 {
		t.Fatalf("second Sync() = %+v", result)
	}

	personas["mio"] = domainpersona.CharacterProfile{CharacterID: "mio", Persona: map[string]string{"self": "second persona"}}
	result, err = projector.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync(third) error = %v", err)
	}
	if result.Enqueued != 0 || result.Skipped != 1 || result.LifePoliciesEnqueued != 1 {
		t.Fatalf("third Sync() = %+v", result)
	}
	if drained, err := worker.Drain(context.Background()); err != nil || drained.Sent != 1 {
		t.Fatalf("Drain(second) = %+v, err = %v", drained, err)
	}
	second, ok, err := store.Latest(context.Background(), target, domainprojection.KindLifePolicy)
	if err != nil || !ok || second.Status != appprojection.OutboxSent || second.SourceRevision != 2 {
		t.Fatalf("second life policy = %+v, ok = %v, err = %v", second, ok, err)
	}
	secondPayload := decodeLifePolicyPayload(t, second)
	if secondPayload.PersonaRevision == policy.PersonaRevision {
		t.Fatal("persona revision did not change after persona content change")
	}
	secondHead, err := publisher.ReadHead(context.Background(), target, domainprojection.KindLifePolicy)
	if err != nil {
		t.Fatalf("ReadHead(second life policy) error = %v", err)
	}
	if secondHead.SourceRevision != 2 || secondHead.Digest != second.Digest {
		t.Fatalf("second life policy head = %+v, record = %+v", secondHead, second)
	}
}

func decodeLifePolicyPayload(t *testing.T, record appprojection.OutboxRecord) domainprojection.LifePolicy {
	t.Helper()
	var wire struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(record.Envelope, &wire); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	var policy domainprojection.LifePolicy
	if err := json.Unmarshal(wire.Payload, &policy); err != nil {
		t.Fatalf("decode life policy payload: %v", err)
	}
	return policy
}

func buildEnvelope(t *testing.T, target domainprojection.Target, validUntil time.Time, revision uint64, work []domainprojection.WorkItem) domainprojection.Envelope {
	t.Helper()
	workSet, err := domainprojection.NewWorkSet(target.WorldID, target.ActorID, "core-epoch-e2e", revision, validUntil, work)
	if err != nil {
		t.Fatalf("NewWorkSet() error = %v", err)
	}
	envelope, err := domainprojection.NewEnvelope(target, domainprojection.KindWorkSet, workSet)
	if err != nil {
		t.Fatalf("NewEnvelope() error = %v", err)
	}
	return envelope
}

func drainOne(t *testing.T, publisher *appprojection.HTTPPublisher, target domainprojection.Target, id string, envelope domainprojection.Envelope, reconcile bool, now time.Time) appprojection.OutboxRecord {
	t.Helper()
	raw, err := envelope.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	store, err := worldoutbox.NewSQLiteStore(filepath.Join(t.TempDir(), "outbox.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Enqueue(context.Background(), appprojection.OutboxRecord{
		ID: id, Target: target, Kind: domainprojection.KindWorkSet,
		Envelope: raw, Digest: envelope.Digest,
		SourceEpoch: "core-epoch-e2e", SourceRevision: envelopeRevision(t, envelope),
		Reconcile: reconcile, CreatedAt: now,
	}, now); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	worker := &appprojection.Worker{Outbox: store, Publisher: publisher, HeadReader: publisher, Now: func() time.Time { return now }, Limit: 4}
	result, err := worker.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Claimed != 1 {
		t.Fatalf("Drain() result = %+v", result)
	}
	record, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	return record
}

func envelopeRevision(t *testing.T, envelope domainprojection.Envelope) uint64 {
	t.Helper()
	var payload struct {
		SourceRevision uint64 `json:"source_revision"`
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return payload.SourceRevision
}

func startRealWorld(t *testing.T, bin string) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	token := "core-e2e-token"
	reconcileToken := "core-e2e-reconcile-token"
	sum := sha256.Sum256([]byte(token))
	reconcileSum := sha256.Sum256([]byte(reconcileToken))
	config := fmt.Sprintf(`{
  "listen": "127.0.0.1:%d",
  "store_path": %q,
  "scope": {"world_id": "rencrow-home", "timeline_id": "timeline-1", "timeline_epoch": "epoch-e2e"},
  "tokens": [{"id": "core", "publisher_id": "core-publisher", "sha256": %q, "scopes": ["projection", "projection_reconcile"]}, {"id": "core-reconcile", "publisher_id": "core-publisher", "sha256": %q, "scopes": ["projection_reconcile"]}],
  "core_projection_actors": [{"actor_id": "mio", "actor_instance_id": "core-instance-mio"}],
  "page": {"max_events": 500, "max_bytes": 2097152},
  "sse": {"heartbeat_seconds": 2, "poll_millis": 50},
  "allow_probe_action": true,
  "initial_state": {"actors": {"mio": {"room_id": "1f-living"}}, "resources": {"chair": 1}}
}`, port, filepath.Join(dir, "world.db"), hex.EncodeToString(sum[:]), hex.EncodeToString(reconcileSum[:]))
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	command := exec.Command(bin, "serve", "--config", configPath)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		t.Fatalf("start WORLD: %v", err)
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := client.Get(baseURL + "/health/live")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatal("WORLD did not become live")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return baseURL, func() {
		_ = command.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = command.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
	}
}
