package audiorouter

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHTTPDownloaderDownload_Non2xxIncludesResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "audio object expired", http.StatusGone)
	}))
	defer server.Close()

	downloader := NewHTTPDownloader(time.Second)
	_, err := downloader.Download(context.Background(), server.URL+"/audio.wav")
	if err == nil {
		t.Fatal("Download() error = nil, want non-2xx error")
	}
	if !strings.Contains(err.Error(), "bad status: 410") || !strings.Contains(err.Error(), "audio object expired") {
		t.Fatalf("Download() error = %q, want status and response body", err.Error())
	}
}

func TestSSEClientRun_NonOKIncludesResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "audio stream unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := NewSSEClient(SSEClientConfig{
		URL:            server.URL,
		ConnectTimeout: time.Second,
		RetryDelay:     time.Hour,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got error
	client.cfg.OnDisconnect = func(err error) {
		got = err
		cancel()
	}
	_ = client.Run(ctx, func(id int64, ev Event) error { return nil })
	if got == nil {
		t.Fatal("OnDisconnect error = nil, want non-OK error")
	}
	if !strings.Contains(got.Error(), "unexpected status: 503") || !strings.Contains(got.Error(), "audio stream unavailable") {
		t.Fatalf("OnDisconnect error = %q, want status and response body", got.Error())
	}
}

func TestSSEClientRun_ReconnectsWithLastEventID(t *testing.T) {
	var (
		mu          sync.Mutex
		requests    []string
		firstServed bool
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("missing flusher")
		}
		mu.Lock()
		requests = append(requests, r.Header.Get("Last-Event-ID"))
		lastID := r.Header.Get("Last-Event-ID")
		servedFirst := firstServed
		if !firstServed {
			firstServed = true
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		if !servedFirst && lastID == "" {
			fmt.Fprint(w, "id: 1\n")
			fmt.Fprint(w, "data: {\"session_id\":\"s1\",\"chunk_index\":0,\"character_id\":\"mio\",\"audio_url\":\"http://example/1.wav\"}\n\n")
			flusher.Flush()
			return
		}
		if lastID == "1" {
			fmt.Fprint(w, "id: 2\n")
			fmt.Fprint(w, "data: {\"session_id\":\"s2\",\"chunk_index\":0,\"character_id\":\"shiro\",\"audio_url\":\"http://example/2.wav\"}\n\n")
			flusher.Flush()
			return
		}
		http.Error(w, "unexpected Last-Event-ID", http.StatusBadRequest)
	}))
	defer server.Close()

	client := NewSSEClient(SSEClientConfig{
		URL:            server.URL,
		ConnectTimeout: time.Second,
		RetryDelay:     10 * time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	var got []int64
	err := client.Run(ctx, func(id int64, ev Event) error {
		got = append(got, id)
		if len(got) == 2 {
			cancel()
		}
		if ev.AudioURL == "" {
			t.Fatalf("expected audio_url in event %+v", ev)
		}
		return nil
	})
	if err != context.Canceled && err != context.DeadlineExceeded {
		t.Fatalf("Run error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d", len(got))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(requests))
	}
	if requests[1] != "1" {
		t.Fatalf("expected reconnect with Last-Event-ID=1, got %q", requests[1])
	}
}
