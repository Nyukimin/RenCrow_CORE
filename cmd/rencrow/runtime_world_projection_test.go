package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	appprojection "github.com/Nyukimin/RenCrow_CORE/internal/application/worldprojection"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainprojection "github.com/Nyukimin/RenCrow_CORE/internal/domain/worldprojection"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/worldoutbox"
)

func TestStartWorldProjectionIsDisabledByDefault(t *testing.T) {
	deps := &Dependencies{}
	if err := startWorldProjection(&config.Config{}, deps); err != nil {
		t.Fatalf("startWorldProjection() error = %v", err)
	}
	if deps.worldProjectionCancel != nil || deps.worldProjectionClose != nil {
		t.Fatal("disabled projection loop initialized resources")
	}
}

func TestStartWorldProjectionFailsClosedWithoutCredential(t *testing.T) {
	t.Setenv("RENCROW_TEST_WORLD_TOKEN", "")
	deps := &Dependencies{}
	cfg := &config.Config{WorldProjection: config.WorldProjectionConfig{
		Enabled: true, BaseURL: "http://127.0.0.1:1", TokenEnv: "RENCROW_TEST_WORLD_TOKEN",
	}}
	if err := startWorldProjection(cfg, deps); err == nil {
		t.Fatal("startWorldProjection() error = nil")
	}
}

func TestStartWorldProjectionFailsClosedWithoutLifePolicyCharacters(t *testing.T) {
	t.Setenv("RENCROW_TEST_WORLD_TOKEN", "test-token")
	workspace := t.TempDir()
	store, err := taskpersistence.NewJSONLStoreWithOptions(filepath.Join(workspace, "tasks"), taskpersistence.OpenOptions{})
	if err != nil {
		t.Fatalf("open task store: %v", err)
	}
	deps := &Dependencies{taskStore: store}
	cfg := &config.Config{
		WorkspaceDir:        workspace,
		PersonaArchitecture: config.PersonaArchitectureConfig{CharacterRoot: workspace},
		WorldProjection: config.WorldProjectionConfig{
			Enabled: true, BaseURL: "http://127.0.0.1:1", TokenEnv: "RENCROW_TEST_WORLD_TOKEN",
			OutboxPath: filepath.Join(workspace, "outbox.sqlite"),
			WorldID:    "rencrow-home", TimelineID: "timeline-1", TimelineEpoch: "epoch-1",
			SourceEpoch: "core-epoch-1",
			Actors: []config.WorldProjectionActorConfig{{
				ActorID: "mio", ActorInstanceID: "core-instance-mio", CoreAssignee: "mio",
			}},
		},
	}
	err = startWorldProjection(cfg, deps)
	if err == nil {
		deps.Shutdown()
		t.Fatal("startWorldProjection() error = nil")
	}
	if !strings.Contains(err.Error(), "character bundles") {
		t.Fatalf("startWorldProjection() error = %v", err)
	}
}

func TestStartWorldProjectionRunsProjectorAndDrain(t *testing.T) {
	t.Setenv("RENCROW_TEST_WORLD_TOKEN", "test-token")
	requests := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.NotFound(w, r)
			return
		}
		_, _ = io.ReadAll(r.Body)
		requests <- r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"schema_version": 1,
			"projection": map[string]any{
				"status":      "accepted",
				"accepted_at": time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC),
			},
		})
	}))
	defer server.Close()

	workspace := t.TempDir()
	writeTestCharacter(t, workspace, "mio")
	store, err := taskpersistence.NewJSONLStoreWithOptions(filepath.Join(workspace, "tasks"), taskpersistence.OpenOptions{})
	if err != nil {
		t.Fatalf("open task store: %v", err)
	}
	task := domaintask.Task{Title: "project me", Route: domaintask.RouteGeneral, Assignee: "mio"}
	task.ApplyDefaults(time.Now().UTC())
	if err := store.SaveTask(t.Context(), task); err != nil {
		t.Fatalf("SaveTask() error = %v", err)
	}
	deps := &Dependencies{taskStore: store}
	outboxPath := filepath.Join(workspace, "outbox.sqlite")
	cfg := &config.Config{
		WorkspaceDir:        workspace,
		PersonaArchitecture: config.PersonaArchitectureConfig{CharacterRoot: workspace},
		WorldProjection: config.WorldProjectionConfig{
			Enabled: true, BaseURL: server.URL, TokenEnv: "RENCROW_TEST_WORLD_TOKEN",
			OutboxPath: outboxPath,
			WorldID:    "rencrow-home", TimelineID: "timeline-1", TimelineEpoch: "epoch-1",
			SourceEpoch: "core-epoch-1", IntervalSeconds: 3600,
			ValidForSeconds: 900, RefreshBeforeSeconds: 300, TimeoutSeconds: 5,
			Actors: []config.WorldProjectionActorConfig{{
				ActorID: "mio", ActorInstanceID: "core-instance-mio", CoreAssignee: "mio",
			}},
		},
	}
	if err := startWorldProjection(cfg, deps); err != nil {
		t.Fatalf("startWorldProjection() error = %v", err)
	}
	waitForProjectionKinds(t, requests, "full_actor_work_set", "life_policy")
	outbox, err := worldoutbox.NewSQLiteStore(outboxPath)
	if err != nil {
		t.Fatalf("open observer outbox: %v", err)
	}
	defer outbox.Close()
	target := domainprojection.Target{
		WorldID: "rencrow-home", TimelineID: "timeline-1", TimelineEpoch: "epoch-1",
		ActorID: "mio", ActorInstanceID: "core-instance-mio", ActorTransportID: "mio",
	}
	record := waitForSentOutbox(t, outbox, target, domainprojection.KindWorkSet, 1)
	if record.AckStatus != "accepted" {
		t.Fatalf("latest outbox = %+v", record)
	}
	policy := waitForSentOutbox(t, outbox, target, domainprojection.KindLifePolicy, 1)
	if policy.AckStatus != "accepted" {
		t.Fatalf("life policy outbox = %+v", policy)
	}

	// A committed Task write must wake the loop immediately; the configured
	// interval is one hour, so a second delivery within the test deadline proves
	// the commit observer is connected.
	second := domaintask.Task{Title: "project me too", Route: domaintask.RouteGeneral, Assignee: "mio"}
	second.ApplyDefaults(time.Now().UTC())
	if err := store.SaveTask(t.Context(), second); err != nil {
		t.Fatalf("SaveTask(second) error = %v", err)
	}
	waitForProjectionKinds(t, requests, "full_actor_work_set")
	record = waitForSentOutbox(t, outbox, target, domainprojection.KindWorkSet, 2)
	deps.Shutdown()
	if record.AckStatus != "accepted" {
		t.Fatalf("second outbox = %+v", record)
	}
	policy, ok, err := outbox.Latest(t.Context(), target, domainprojection.KindLifePolicy)
	if err != nil || !ok || policy.SourceRevision != 1 {
		t.Fatalf("life policy after Task commit = %+v, ok = %v, err = %v", policy, ok, err)
	}
}

func writeTestCharacter(t *testing.T, root, actorID string) {
	t.Helper()
	dir := filepath.Join(root, "characters", actorID, "persona")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "self.md"), []byte("test persona for "+actorID), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", actorID, err)
	}
}

func waitForProjectionKinds(t *testing.T, requests <-chan string, want ...string) {
	t.Helper()
	needed := make(map[string]bool, len(want))
	for _, kind := range want {
		needed[kind] = true
	}
	deadline := time.After(5 * time.Second)
	for len(needed) > 0 {
		select {
		case path := <-requests:
			for kind := range needed {
				if strings.HasSuffix(path, "/"+kind) {
					delete(needed, kind)
				}
			}
		case <-deadline:
			t.Fatalf("projection requests not received: %v", needed)
		}
	}
}

func waitForSentOutbox(t *testing.T, outbox *worldoutbox.SQLiteStore, target domainprojection.Target, kind string, wantRevision uint64) appprojection.OutboxRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		record, ok, err := outbox.Latest(t.Context(), target, kind)
		if err != nil {
			t.Fatalf("Latest(%s) error = %v", kind, err)
		}
		if ok && record.Status == appprojection.OutboxSent && record.SourceRevision == wantRevision {
			return record
		}
		if time.Now().After(deadline) {
			t.Fatalf("latest %s outbox did not reach sent revision %d: %+v", kind, wantRevision, record)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
