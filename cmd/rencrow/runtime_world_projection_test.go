package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

func TestStartWorldProjectionRunsProjectorAndDrain(t *testing.T) {
	t.Setenv("RENCROW_TEST_WORLD_TOKEN", "test-token")
	requests := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		requests <- r.URL.Path + " " + string(body)
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
		WorkspaceDir: workspace,
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
	select {
	case request := <-requests:
		if request == "" {
			t.Fatal("empty projection request")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("projection request was not sent")
	}
	outbox, err := worldoutbox.NewSQLiteStore(outboxPath)
	if err != nil {
		t.Fatalf("open observer outbox: %v", err)
	}
	defer outbox.Close()
	target := domainprojection.Target{
		WorldID: "rencrow-home", TimelineID: "timeline-1", TimelineEpoch: "epoch-1",
		ActorID: "mio", ActorInstanceID: "core-instance-mio", ActorTransportID: "mio",
	}
	var record appprojection.OutboxRecord
	var ok bool
	deadline := time.Now().Add(5 * time.Second)
	for {
		record, ok, err = outbox.Latest(t.Context(), target, domainprojection.KindWorkSet)
		if err != nil {
			t.Fatalf("Latest() error = %v", err)
		}
		if ok && record.Status == appprojection.OutboxSent {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("latest outbox did not reach sent: %+v", record)
		}
		time.Sleep(10 * time.Millisecond)
	}
	deps.Shutdown()
	if record.AckStatus != "accepted" {
		t.Fatalf("latest outbox = %+v", record)
	}
}
