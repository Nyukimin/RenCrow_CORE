package viewer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCollectAudioSnapshotProbesSTTAndTTSConcurrently(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"ok":true,"status":"ready","ready":{"model_loaded":true},"provider":{"model_loaded":true}}`))
			return
		}
		_, _ = w.Write([]byte(r.URL.Path))
	}))
	defer server.Close()

	started := time.Now()
	snapshot := collectAudioSnapshot(DebugSystemOptions{
		STTBaseURL: server.URL,
		TTSBaseURL: server.URL,
	})
	elapsed := time.Since(started)

	if !snapshot.STTOK || !snapshot.TTSLiveOK || !snapshot.TTSReadyOK {
		t.Fatalf("snapshot=%#v, want all probes ok", snapshot)
	}
	if !strings.Contains(snapshot.STTHealth, `"model_loaded":true`) || snapshot.TTSLive != "/health/live" || snapshot.TTSReady != "/health/ready" {
		t.Fatalf("snapshot bodies=%#v", snapshot)
	}
	if elapsed > 350*time.Millisecond {
		t.Fatalf("audio snapshot took %s, want concurrent probes", elapsed)
	}
}

func TestCollectAudioSnapshotRequiresSTTReadyModelLoaded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"status":"warming","ready":{"model_loaded":false}}`))
	}))
	defer server.Close()

	snapshot := collectAudioSnapshot(DebugSystemOptions{STTBaseURL: server.URL})

	if snapshot.STTOK {
		t.Fatalf("snapshot=%#v, want stt not ok until status ready and model_loaded true", snapshot)
	}
	if !strings.Contains(snapshot.STTHealth, `"model_loaded":false`) {
		t.Fatalf("snapshot=%#v, want health body preserved", snapshot)
	}
}

func TestCollectAudioSnapshotRecordsTimeoutAsBlockedSignal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	started := time.Now()
	snapshot := collectAudioSnapshot(DebugSystemOptions{
		STTBaseURL: server.URL,
		TTSBaseURL: server.URL,
	})
	elapsed := time.Since(started)

	if snapshot.STTOK || snapshot.TTSLiveOK || snapshot.TTSReadyOK {
		t.Fatalf("snapshot=%#v, want timed out probes not ok", snapshot)
	}
	if snapshot.LastError == "" {
		t.Fatalf("snapshot=%#v, want timeout error detail", snapshot)
	}
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("audio snapshot took %s, want bounded timeout", elapsed)
	}
}
