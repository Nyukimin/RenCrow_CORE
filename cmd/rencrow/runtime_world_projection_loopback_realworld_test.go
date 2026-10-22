//go:build realworld

package main

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

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainprojection "github.com/Nyukimin/RenCrow_CORE/internal/domain/worldprojection"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/worldoutbox"
)

// TestWorldProjectionLoopbackIsolatedConnectionWithRealWorld is the D-14
// isolated connection trial: the real CORE projection loop is started from an
// explicit loopback config (the port comes only from config, never from the
// contract) against the real WORLD binary, and the result is read back
// through the URL/version/auth/health contract on that ephemeral port.
// It adopts nothing about a production route, port, or TLS.
func TestWorldProjectionLoopbackIsolatedConnectionWithRealWorld(t *testing.T) {
	bin := strings.TrimSpace(os.Getenv("RENCROW_WORLD_BIN"))
	if bin == "" {
		t.Skip("RENCROW_WORLD_BIN is not set")
	}
	baseURL, projectionToken, readToken, stop := startLoopbackRealWorld(t, bin)
	defer stop()

	assessmentPath := "/v1/worlds/rencrow-home/timelines/timeline-1/actors/mio/assessment"

	// health semantics stay separated: live is process liveness, ready reports
	// staged readiness, and spatial readiness remains false without measurement.
	live := getLoopbackJSON(t, baseURL+"/health/live", "", http.StatusOK)
	if live["status"] != "live" || live["live"] != true {
		t.Fatalf("live = %v", live)
	}
	ready := getLoopbackJSON(t, baseURL+"/health/ready", readToken, http.StatusOK)
	if ready["store_ready"] != true || ready["logical_ready"] != true ||
		ready["reconciled"] != true || ready["spatial_ready"] != false {
		t.Fatalf("ready stages = %v", ready)
	}

	// The contract version and auth boundary hold on the ephemeral loopback
	// port; a missing head is an explicit missing state, not a success.
	getLoopbackJSON(t, baseURL+assessmentPath, "wrong-token", http.StatusUnauthorized)
	getLoopbackJSON(t, baseURL+assessmentPath, projectionToken, http.StatusForbidden)
	before := getLoopbackJSON(t, baseURL+assessmentPath, readToken, http.StatusOK)
	if before["work_set_state"] != "missing" {
		t.Fatalf("assessment before projection = %v", before)
	}

	// Start CORE's real runtime loop from an explicit config. The port lives
	// only in the config; the interval is one hour so only the commit observer
	// can wake the loop within the test deadline.
	t.Setenv("RENCROW_TEST_D14_WORLD_TOKEN", projectionToken)
	workspace := t.TempDir()
	writeTestCharacter(t, workspace, "mio")
	store, err := taskpersistence.NewJSONLStoreWithOptions(filepath.Join(workspace, "tasks"), taskpersistence.OpenOptions{})
	if err != nil {
		t.Fatalf("open task store: %v", err)
	}
	deps := &Dependencies{taskStore: store}
	outboxPath := filepath.Join(workspace, "outbox.sqlite")
	cfg := &config.Config{
		WorkspaceDir:        workspace,
		PersonaArchitecture: config.PersonaArchitectureConfig{CharacterRoot: workspace},
		WorldProjection: config.WorldProjectionConfig{
			Enabled: true, BaseURL: baseURL, TokenEnv: "RENCROW_TEST_D14_WORLD_TOKEN",
			OutboxPath: outboxPath,
			WorldID:    "rencrow-home", TimelineID: "timeline-1", TimelineEpoch: "epoch-e2e",
			SourceEpoch: "core-epoch-d14", IntervalSeconds: 3600,
			ValidForSeconds: 900, RefreshBeforeSeconds: 300, TimeoutSeconds: 5,
			Actors: []config.WorldProjectionActorConfig{{
				ActorID: "mio", ActorInstanceID: "core-instance-mio", ActorTransportID: "mio", CoreAssignee: "mio",
			}},
		},
	}
	if err := startWorldProjection(cfg, deps); err != nil {
		t.Fatalf("startWorldProjection() error = %v", err)
	}
	defer deps.Shutdown()

	task := domaintask.Task{Title: "d14 loopback task", Route: domaintask.RouteGeneral, Assignee: "mio", Status: domaintask.StatusQueued}
	task.ApplyDefaults(time.Now().UTC())
	if err := store.SaveTask(t.Context(), task); err != nil {
		t.Fatalf("SaveTask() error = %v", err)
	}

	// The real read client on the same loopback contract must observe the
	// queued Task committed through CORE's canonical Task store.
	client, err := worldread.New(worldread.Config{
		BaseURL: baseURL, Credential: readToken,
		WorldID: "rencrow-home", TimelineID: "timeline-1", TimelineEpoch: "epoch-e2e",
		ActorIDs: []string{"mio"},
	})
	if err != nil {
		t.Fatalf("worldread.New() error = %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		got, err := client.Assessment(context.Background(), "mio")
		if err != nil {
			t.Fatalf("Assessment() error = %v", err)
		}
		if got.WorkSetState == "assigned" && containsLoopback(got.WorkPresentation.QueuedTaskIDs, string(task.TaskID)) {
			if got.WorkPresentation.Kind != "queued" || got.WorkSuccess {
				t.Fatalf("presentation = %+v", got.WorkPresentation)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("assessment never consumed the queued task: %+v", got)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// CORE keeps its own receipt: the outbox record for the same target was
	// acknowledged by WORLD.
	outbox, err := worldoutbox.NewSQLiteStore(outboxPath)
	if err != nil {
		t.Fatalf("open outbox: %v", err)
	}
	defer outbox.Close()
	target := domainprojection.Target{
		WorldID: "rencrow-home", TimelineID: "timeline-1", TimelineEpoch: "epoch-e2e",
		ActorID: "mio", ActorInstanceID: "core-instance-mio", ActorTransportID: "mio",
	}
	record, ok, err := outbox.Latest(t.Context(), target, domainprojection.KindWorkSet)
	if err != nil || !ok {
		t.Fatalf("outbox latest = %+v, ok = %v, err = %v", record, ok, err)
	}
	if record.AckStatus != "accepted" {
		t.Fatalf("outbox record = %+v", record)
	}
}

func startLoopbackRealWorld(t *testing.T, bin string) (string, string, string, func()) {
	t.Helper()
	dir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	projectionToken := "d14-projection-token"
	readToken := "d14-read-token"
	sum := func(token string) string {
		digest := sha256.Sum256([]byte(token))
		return hex.EncodeToString(digest[:])
	}
	config := fmt.Sprintf(`{
  "listen": "127.0.0.1:%d",
  "store_path": %q,
  "scope": {"world_id": "rencrow-home", "timeline_id": "timeline-1", "timeline_epoch": "epoch-e2e"},
  "tokens": [
    {"id": "core", "publisher_id": "core-publisher", "sha256": %q, "scopes": ["projection", "projection_reconcile"]},
    {"id": "core-read", "publisher_id": "core-publisher", "sha256": %q, "scopes": ["read"]}
  ],
  "core_projection_actors": [{"actor_id": "mio", "actor_instance_id": "core-instance-mio"}],
  "page": {"max_events": 500, "max_bytes": 2097152},
  "sse": {"heartbeat_seconds": 2, "poll_millis": 50},
  "initial_state": {"actors": {"mio": {"room_id": "1f-living"}}, "resources": {"chair": 1}}
}`, port, filepath.Join(dir, "world.db"), sum(projectionToken), sum(readToken))
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
			t.Fatalf("WORLD never became live on %s", baseURL)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return baseURL, projectionToken, readToken, func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}
}

func getLoopbackJSON(t *testing.T, url, token string, wantStatus int) map[string]any {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest(%s) error = %v", url, err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("GET(%s) error = %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body(%s) error = %v", url, err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("GET(%s) status = %d, want %d, body = %s", url, resp.StatusCode, wantStatus, body)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal(%s) error = %v, body = %s", url, err, body)
	}
	return payload
}

func containsLoopback(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
