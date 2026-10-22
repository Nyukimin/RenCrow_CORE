package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainprojection "github.com/Nyukimin/RenCrow_CORE/internal/domain/worldprojection"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/worldoutbox"
)

const (
	epochSwitchProjectionEnv = "RENCROW_TEST_EPOCH_SWITCH_PROJECTION_TOKEN"
	epochSwitchReconcileEnv  = "RENCROW_TEST_EPOCH_SWITCH_RECONCILE_TOKEN"
	epochSwitchProjectionTok = "projection-token"
	epochSwitchReconcileTok  = "reconcile-token"
)

type epochSwitchRequest struct {
	authorization string
	query         string
}

type epochSwitchServer struct {
	mu       sync.Mutex
	requests []epochSwitchRequest
	received chan epochSwitchRequest
}

func (s *epochSwitchServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.NotFound(w, r)
		return
	}
	_, _ = io.ReadAll(r.Body)
	req := epochSwitchRequest{authorization: r.Header.Get("Authorization"), query: r.URL.RawQuery}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	select {
	case s.received <- req:
	default:
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"schema_version": 1,
		"projection": map[string]any{
			"status":      "accepted",
			"accepted_at": time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC),
		},
	})
}

func (s *epochSwitchServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func epochSwitchSettings(baseURL, outboxPath, sourceEpoch string, reconcileEnabled bool, reconcileEnv string) config.WorldProjectionConfig {
	disabled := false
	return config.WorldProjectionConfig{
		Enabled:              true,
		LifePolicyEnabled:    &disabled,
		BaseURL:              baseURL,
		TokenEnv:             epochSwitchProjectionEnv,
		OutboxPath:           outboxPath,
		WorldID:              "rencrow-home",
		TimelineID:           "timeline-1",
		TimelineEpoch:        "epoch-1",
		SourceEpoch:          sourceEpoch,
		IntervalSeconds:      3600,
		ValidForSeconds:      900,
		RefreshBeforeSeconds: 300,
		TimeoutSeconds:       5,
		ReconcileEnabled:     reconcileEnabled,
		ReconcileTokenEnv:    reconcileEnv,
		Actors: []config.WorldProjectionActorConfig{{
			ActorID: "mio", ActorInstanceID: "core-instance-mio", CoreAssignee: "mio",
		}},
	}
}

func epochSwitchTaskStore(t *testing.T, workspace string) *taskpersistence.JSONLStore {
	t.Helper()
	store, err := taskpersistence.NewJSONLStoreWithOptions(filepath.Join(workspace, "tasks"), taskpersistence.OpenOptions{})
	if err != nil {
		t.Fatalf("open task store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	task := domaintask.Task{Title: "project me", Route: domaintask.RouteGeneral, Assignee: "mio"}
	task.ApplyDefaults(time.Now().UTC())
	if err := store.SaveTask(t.Context(), task); err != nil {
		t.Fatalf("SaveTask() error = %v", err)
	}
	return store
}

// D-06: switching CORE's source epoch is an operator-authorized action. When
// enabled it must reach WORLD through the real projector -> outbox -> worker
// route as revision 1 of the new epoch, carrying the separate reconcile
// credential instead of the projection credential.
func TestStartWorldProjectionSwitchesSourceEpochWithReconcileCredential(t *testing.T) {
	t.Setenv(epochSwitchProjectionEnv, epochSwitchProjectionTok)
	t.Setenv(epochSwitchReconcileEnv, epochSwitchReconcileTok)

	workspace := t.TempDir()
	outboxPath := filepath.Join(workspace, "outbox.sqlite")
	store := epochSwitchTaskStore(t, workspace)

	handler := &epochSwitchServer{received: make(chan epochSwitchRequest, 16)}
	server := httptest.NewServer(handler)
	defer server.Close()

	start := func(sourceEpoch string, reconcileEnabled bool, reconcileEnv string) *Dependencies {
		settings := epochSwitchSettings(server.URL, outboxPath, sourceEpoch, reconcileEnabled, reconcileEnv)
		deps := &Dependencies{taskStore: store}
		if err := startWorldProjection(&config.Config{WorkspaceDir: workspace, WorldProjection: settings}, deps); err != nil {
			t.Fatalf("startWorldProjection(%s) error = %v", sourceEpoch, err)
		}
		return deps
	}

	deps := start("core-epoch-1", false, "")
	select {
	case first := <-handler.received:
		if first.query != "" || first.authorization != "Bearer "+epochSwitchProjectionTok {
			t.Fatalf("first projection = %+v", first)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("initial projection never reached WORLD")
	}
	deps.Shutdown()

	deps = start("core-epoch-2", true, epochSwitchReconcileEnv)
	select {
	case switched := <-handler.received:
		if switched.query != "reconcile=true" {
			t.Fatalf("epoch switch request = %+v, want reconcile=true", switched)
		}
		if switched.authorization != "Bearer "+epochSwitchReconcileTok {
			t.Fatalf("epoch switch Authorization = %q, want the reconcile credential", switched.authorization)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("source epoch switch never reached WORLD")
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
	record := waitForSentOutbox(t, outbox, target, domainprojection.KindWorkSet, 1)
	if record.SourceEpoch != "core-epoch-2" || record.SourceRevision != 1 || !record.Reconcile || record.AckStatus != "accepted" {
		t.Fatalf("epoch switch record = %+v, want core-epoch-2 revision 1 reconcile accepted", record)
	}
	deps.Shutdown()
	if got := handler.count(); got != 2 {
		t.Fatalf("WORLD requests = %d, want 2", got)
	}
}

// Enabling the epoch switch without a distinct credential is a configuration
// error. CORE must not start the loop or silently reuse the projection token.
func TestStartWorldProjectionReconcileFailsClosedWithoutDistinctCredential(t *testing.T) {
	t.Setenv(epochSwitchProjectionEnv, epochSwitchProjectionTok)
	workspace := t.TempDir()
	writeTestCharacter(t, workspace, "mio")

	for name, envName := range map[string]string{
		"missing env name": "",
		"reused env name":  epochSwitchProjectionEnv,
		"blank env name":   "   ",
		"unset env name":   "RENCROW_TEST_EPOCH_SWITCH_UNSET_TOKEN",
		"empty env value":  "RENCROW_TEST_EPOCH_SWITCH_EMPTY_TOKEN",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("RENCROW_TEST_EPOCH_SWITCH_EMPTY_TOKEN", "")
			if envName == "   " || envName == epochSwitchProjectionEnv {
				t.Setenv(epochSwitchReconcileEnv, epochSwitchReconcileTok)
			}
			deps := &Dependencies{taskStore: epochSwitchTaskStore(t, workspace)}
			settings := epochSwitchSettings("http://127.0.0.1:1", filepath.Join(workspace, "outbox.sqlite"), "core-epoch-1", true, envName)
			err := startWorldProjection(&config.Config{WorkspaceDir: workspace, WorldProjection: settings}, deps)
			if err == nil {
				deps.Shutdown()
				t.Fatal("startWorldProjection() error = nil")
			}
			if !strings.Contains(err.Error(), "reconcile") {
				t.Fatalf("startWorldProjection() error = %v, want a reconcile configuration error", err)
			}
			if deps.worldProjectionCancel != nil || deps.worldProjectionClose != nil {
				t.Fatal("rejected reconcile configuration initialized resources")
			}
		})
	}
}

// With reconcile disabled, a configured source epoch that differs from the
// durable epoch must hold the projection instead of re-baselining it.
func TestStartWorldProjectionHoldsWhenSourceEpochDiffersAndReconcileDisabled(t *testing.T) {
	t.Setenv(epochSwitchProjectionEnv, epochSwitchProjectionTok)
	workspace := t.TempDir()
	outboxPath := filepath.Join(workspace, "outbox.sqlite")
	store := epochSwitchTaskStore(t, workspace)

	handler := &epochSwitchServer{received: make(chan epochSwitchRequest, 16)}
	server := httptest.NewServer(handler)
	defer server.Close()

	settings := epochSwitchSettings(server.URL, outboxPath, "core-epoch-1", false, "")
	deps := &Dependencies{taskStore: store}
	if err := startWorldProjection(&config.Config{WorkspaceDir: workspace, WorldProjection: settings}, deps); err != nil {
		t.Fatalf("startWorldProjection() error = %v", err)
	}
	select {
	case <-handler.received:
	case <-time.After(5 * time.Second):
		deps.Shutdown()
		t.Fatal("initial projection never reached WORLD")
	}
	deps.Shutdown()

	settings.SourceEpoch = "core-epoch-2"
	second := &Dependencies{taskStore: store}
	if err := startWorldProjection(&config.Config{WorkspaceDir: workspace, WorldProjection: settings}, second); err != nil {
		t.Fatalf("startWorldProjection() after epoch change error = %v", err)
	}
	defer second.Shutdown()
	time.Sleep(500 * time.Millisecond)
	if got := handler.count(); got != 1 {
		t.Fatalf("WORLD requests = %d, want the unauthorized epoch switch to hold at 1", got)
	}
}
