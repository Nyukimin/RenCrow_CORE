package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
)

func TestValidateListenAddressAcceptsLoopbackAndRejectsOtherHosts(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "[::1]:18794"} {
		if err := validateListenAddress(address); err != nil {
			t.Errorf("validateListenAddress(%q) error = %v", address, err)
		}
	}

	for _, address := range []string{
		"0.0.0.0:18794",
		"192.0.2.1:18794",
		"[2001:db8::1]:18794",
		"localhost:18794",
		"127.0.0.1",
	} {
		if err := validateListenAddress(address); err == nil {
			t.Errorf("validateListenAddress(%q) unexpectedly succeeded", address)
		}
	}
}

func TestHandlerServesOnlyScopedSnapshotAndFiniteBridgeStream(t *testing.T) {
	handler := newHandler()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := server.Client()

	snapshotResponse, err := client.Get(server.URL + "/viewer/world/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	defer snapshotResponse.Body.Close()
	if snapshotResponse.StatusCode != http.StatusOK {
		t.Fatalf("snapshot status = %d", snapshotResponse.StatusCode)
	}
	var snapshot viewer.WorldPocSnapshot
	if err := json.NewDecoder(snapshotResponse.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion != "r04.mock.v1" || snapshot.Source != "mock" || snapshot.WorldID != "r04-house" || snapshot.WorldRevision != 1 {
		t.Fatalf("snapshot contract = %+v", snapshot)
	}
	if len(snapshot.Actors) != 1 || snapshot.Actors[0].ActorID != "mio" {
		t.Fatalf("snapshot actors = %+v", snapshot.Actors)
	}

	streamResponse, err := client.Get(server.URL + "/viewer/world/stream?after_revision=0")
	if err != nil {
		t.Fatal(err)
	}
	defer streamResponse.Body.Close()
	if streamResponse.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", streamResponse.StatusCode)
	}
	if got := streamResponse.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("stream content type = %q", got)
	}
	streamBody, err := io.ReadAll(streamResponse.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"id: 1\nevent: snapshot", `"actor_id":"mio"`, "event: caught_up"} {
		if !strings.Contains(string(streamBody), expected) {
			t.Errorf("stream body missing %q: %s", expected, streamBody)
		}
	}

	for _, path := range []string{"/tasks", "/events", "/viewer/tasks", "/viewer/events", "/viewer/world/snapshot/extra"} {
		response, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want %d", path, response.StatusCode, http.StatusNotFound)
		}
	}

	postRequest, err := http.NewRequest(http.MethodPost, server.URL+"/viewer/world/snapshot", nil)
	if err != nil {
		t.Fatal(err)
	}
	postResponse, err := client.Do(postRequest)
	if err != nil {
		t.Fatal(err)
	}
	postResponse.Body.Close()
	if postResponse.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST snapshot status = %d, want %d", postResponse.StatusCode, http.StatusMethodNotAllowed)
	}
}

func TestServeShutsDownWhenContextIsCancelled(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		finished <- serve(ctx, listener, http.NotFoundHandler())
	}()
	cancel()

	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("serve returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not stop after context cancellation")
	}
}
