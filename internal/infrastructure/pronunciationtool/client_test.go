package pronunciationtool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientStartsAndWaitsForNewDailyReport(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/daily/run":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"started": true})
		case r.Method == http.MethodGet && r.URL.Path == "/api/daily/latest":
			if polls.Add(1) < 3 {
				_ = json.NewEncoder(w).Encode(map[string]any{"started_at": "2026-07-18T04:30:00Z", "running": true})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"date": "2026-07-19", "started_at": "2026-07-19T04:30:00Z", "running": false,
				"total": 30, "passed": 29, "failed": 1,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, &http.Client{Timeout: time.Second}, time.Millisecond)
	report, err := client.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.Total != 30 || report.Passed != 29 || polls.Load() < 3 {
		t.Fatalf("report=%+v polls=%d", report, polls.Load())
	}
}

func TestClientReadsRemoteGPUSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/gpu/status" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"index": 0, "name": "NVIDIA GeForce RTX 5060 Ti", "free_mb": 1134,
			"utilization_percent": 0,
		})
	}))
	defer server.Close()
	client := NewClient(server.URL, &http.Client{Timeout: time.Second}, time.Millisecond)
	snapshot, err := client.Snapshot(context.Background(), "RTX 5060 Ti")
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.FreeMB != 1134 || snapshot.UtilizationPercent != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}
