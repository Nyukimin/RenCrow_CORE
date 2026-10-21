//go:build realworld

package worldread

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
)

func TestRealWorldReadFeedScopesActorsAndCursor(t *testing.T) {
	bin := strings.TrimSpace(os.Getenv("RENCROW_WORLD_BIN"))
	if bin == "" {
		t.Skip("RENCROW_WORLD_BIN is not set")
	}
	baseURL, token, stop := startRealWorldRead(t, bin)
	defer stop()

	client, err := New(Config{
		BaseURL:       baseURL,
		Credential:    token,
		WorldID:       "rencrow-home",
		TimelineID:    "timeline-1",
		TimelineEpoch: "epoch-e2e",
		ActorIDs:      []string{"mio"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	feed, err := client.ReadFeed(context.Background(), 0)
	if err != nil {
		t.Fatalf("ReadFeed(initial) error = %v", err)
	}
	if feed.Snapshot.SchemaVersion != 1 || feed.Snapshot.WorldID != "rencrow-home" ||
		feed.Snapshot.TimelineID != "timeline-1" || feed.Snapshot.TimelineEpoch != "epoch-e2e" {
		t.Fatalf("snapshot = %+v", feed.Snapshot)
	}
	if feed.Snapshot.StateSeq != 0 || len(feed.Snapshot.StateHash) != 64 {
		t.Fatalf("initial snapshot revision/hash = %d %q", feed.Snapshot.StateSeq, feed.Snapshot.StateHash)
	}
	if len(feed.Snapshot.Actors) != 1 || feed.Snapshot.Actors[0].ActorID != "mio" ||
		!feed.Snapshot.Actors[0].Present || feed.Snapshot.Actors[0].RoomID != "1f-living" {
		t.Fatalf("initial actors = %+v", feed.Snapshot.Actors)
	}
	if len(feed.Events) != 0 || feed.Cursor != 0 {
		t.Fatalf("initial feed = %+v", feed)
	}
	raw, err := json.Marshal(feed.Snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if strings.Contains(string(raw), "shiro") || strings.Contains(string(raw), "2f-room") {
		t.Fatalf("hidden actor leaked: %s", raw)
	}

	postDebug(t, baseURL, token, "read-e2e-1", map[string]any{"actor_id": "mio", "n": 1})
	postDebug(t, baseURL, token, "read-e2e-2", map[string]any{"actor_id": "shiro", "n": 2})
	postDebug(t, baseURL, token, "read-e2e-3", map[string]any{"note": "global"})

	feed, err = client.ReadFeed(context.Background(), 0)
	if err != nil {
		t.Fatalf("ReadFeed(catch-up) error = %v", err)
	}
	if feed.Snapshot.StateSeq != 3 || feed.Cursor != 3 || feed.Skipped != 1 || len(feed.Events) != 2 {
		t.Fatalf("catch-up feed = %+v", feed)
	}
	if feed.Events[0].Seq != 1 || string(feed.Events[0].Payload) != `{"actor_id":"mio","n":1}` {
		t.Fatalf("first visible event = %+v", feed.Events[0])
	}
	if feed.Events[1].Seq != 3 || string(feed.Events[1].Payload) != `{"note":"global"}` {
		t.Fatalf("second visible event = %+v", feed.Events[1])
	}
	for _, event := range feed.Events {
		if strings.Contains(string(event.Payload), "shiro") {
			t.Fatalf("hidden actor event leaked: %s", event.Payload)
		}
	}

	feed, err = client.ReadFeed(context.Background(), 3)
	if err != nil || feed.Cursor != 3 || len(feed.Events) != 0 || feed.Skipped != 0 {
		t.Fatalf("ReadFeed(current) = %+v, %v", feed, err)
	}

	feed, err = client.ReadFeed(context.Background(), 1)
	if err != nil || feed.Cursor != 3 || len(feed.Events) != 1 || feed.Skipped != 1 {
		t.Fatalf("ReadFeed(after 1) = %+v, %v", feed, err)
	}
	if feed.Events[0].Seq != 3 {
		t.Fatalf("ReadFeed(after 1) events = %+v", feed.Events)
	}

	_, err = client.ReadFeed(context.Background(), 4)
	if !errors.Is(err, ErrResyncRequired) {
		t.Fatalf("ReadFeed(ahead) error = %v; want ErrResyncRequired", err)
	}

	wrongTimeline, err := New(Config{
		BaseURL: baseURL, Credential: token,
		WorldID: "rencrow-home", TimelineID: "timeline-2", TimelineEpoch: "epoch-e2e",
		ActorIDs: []string{"mio"},
	})
	if err != nil {
		t.Fatalf("New(wrong timeline) error = %v", err)
	}
	_, err = wrongTimeline.ReadFeed(context.Background(), 0)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("wrong timeline error = %v; want ErrForbidden", err)
	}

	wrongToken, err := New(Config{
		BaseURL: baseURL, Credential: "wrong-token",
		WorldID: "rencrow-home", TimelineID: "timeline-1", TimelineEpoch: "epoch-e2e",
		ActorIDs: []string{"mio"},
	})
	if err != nil {
		t.Fatalf("New(wrong token) error = %v", err)
	}
	_, err = wrongToken.ReadFeed(context.Background(), 0)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong token error = %v; want ErrUnauthorized", err)
	}
}

func postDebug(t *testing.T, baseURL, token, idempotencyKey string, payload map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"action":         "debug.noop",
		"timeline_epoch": "epoch-e2e",
		"payload":        payload,
	})
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	request, err := http.NewRequest(http.MethodPost,
		baseURL+"/v1/worlds/rencrow-home/timelines/timeline-1/commands", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build command: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", idempotencyKey)
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("post command: %v", err)
	}
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("post command status = %d: %s", response.StatusCode, responseBody)
	}
}

func startRealWorldRead(t *testing.T, bin string) (string, string, func()) {
	t.Helper()
	dir := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	token := "core-read-e2e-token"
	sum := sha256.Sum256([]byte(token))
	// The scope override exists only to run the E2E under a controlled
	// authorization fault. The default is the real read/write/stream set.
	scopes := `["read", "write", "stream"]`
	if override := strings.TrimSpace(os.Getenv("RENCROW_WORLD_READ_E2E_SCOPES")); override != "" {
		scopes = override
	}
	config := fmt.Sprintf(`{
  "listen": "127.0.0.1:%d",
  "store_path": %q,
  "scope": {"world_id": "rencrow-home", "timeline_id": "timeline-1", "timeline_epoch": "epoch-e2e"},
  "tokens": [{"id": "core-reader", "sha256": %q, "scopes": %s}],
  "page": {"max_events": 500, "max_bytes": 2097152},
  "sse": {"heartbeat_seconds": 2, "poll_millis": 50},
  "allow_probe_action": true,
  "initial_state": {"actors": {"mio": {"room_id": "1f-living"}, "shiro": {"room_id": "2f-room"}}, "resources": {"chair": 1}}
}`, port, filepath.Join(dir, "world.db"), hex.EncodeToString(sum[:]), scopes)
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
		response, err := client.Get(baseURL + "/health/live")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
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
	return baseURL, token, func() {
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
