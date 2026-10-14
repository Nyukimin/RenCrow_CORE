package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/transportmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	transportpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/transport"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type sttFactoryOwners struct {
	tasks      *taskmanager.Manager
	actions    *actionmanager.Manager
	transports *transportmanager.Manager
}

func newSTTFactoryOwners(t *testing.T) sttFactoryOwners {
	t.Helper()
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	actionStore, err := actionpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "actions"))
	if err != nil {
		t.Fatal(err)
	}
	actions := actionmanager.New(actionStore)
	transportStore, err := transportpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "transport"))
	if err != nil {
		t.Fatal(err)
	}
	return sttFactoryOwners{
		tasks:      taskmanager.New(taskStore, taskmanager.DefaultParallelLimits()),
		actions:    actions,
		transports: transportmanager.New(transportStore, actions),
	}
}

// The production wiring of /stt: buildSTTRuntime from the real config shape,
// the route registered as in production, and a stand-in for RenCrow_STT's
// stream. It pins what CORE sends to RenCrow_STT (session.start, the fixed
// Origin, audio and control as their own frame kinds), that the stand-in's
// HTTP transcription route is never called for an utterance (the stand-in fails
// the test if it is), and that the evidence of the recognition is recorded.
func TestBuildSTTRuntimeRelaysAnUtteranceThroughTheRenCrowSTTStream(t *testing.T) {
	fake := startFakeSTTServer(t)
	cfg := &config.Config{}
	cfg.STT.Enabled = true
	cfg.STT.GatewayBaseURL = fake.HTTPBase()
	cfg.STT.TimeoutMS = 5000
	cfg.STT.BusyPolicy = "queue_latest"
	owners := newSTTFactoryOwners(t)
	runtime := buildSTTRuntime(cfg, owners.tasks, owners.actions, owners.transports)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := runtime.CloseRecorder(ctx); err != nil {
			t.Errorf("CloseRecorder() = %v", err)
		}
	})

	mux := http.NewServeMux()
	registerSTTRuntimeRoutes(mux, runtime)
	server := httptest.NewServer(mux)
	defer server.Close()

	client := dialRelay(t, "ws"+strings.TrimPrefix(server.URL, "http")+"/stt")
	conn := fake.Accept()

	start := fake.SessionStarts()[0]
	requestID, _ := start["request_id"].(string)
	if start["type"] != "session.start" || start["protocol"] != "stt.stream.v1" || start["format"] != "pcm_s16le" ||
		start["sample_rate"] != float64(16000) || start["channels"] != float64(1) || !strings.HasPrefix(requestID, "stt-") {
		t.Fatalf("session.start = %#v", start)
	}
	if _, has := start["options"]; has {
		t.Fatalf("CORE sends no options: %#v", start)
	}
	if origins := fake.Origins(); len(origins) != 1 || origins[0] != "http://rencrow-core.invalid" {
		t.Fatalf("Origin = %v, want the fixed Origin of the contract", origins)
	}

	client.ExpectReady()
	client.Control(map[string]any{"type": "start", "utterance_id": "v1-1", "sample_rate": 16000, "channels": 1, "format": "pcm_s16le"})
	const frames = 10 // one second of Viewer-sized 100 ms frames
	for i := 0; i < frames; i++ {
		client.Audio(rawPCM16Chunk())
	}
	client.Control(map[string]any{"type": "stop"})

	if msg := conn.NextControl(t); msg["type"] != "utterance.start" || msg["utterance_id"] != "v1-1" {
		t.Fatalf("start = %#v", msg)
	}
	audioBytes := 0
	for i := 0; i < frames; i++ {
		frame := conn.Next(t)
		audioBytes += len(frame.Data)
	}
	if audioBytes != frames*len(rawPCM16Chunk()) {
		t.Fatalf("audio reached STT as %d bytes, want %d: CORE must not cut, drop or add audio", audioBytes, frames*len(rawPCM16Chunk()))
	}
	// The Viewer's stop is id-less and stays so: STT applies it to the utterance
	// it announced that is in progress (contract 3.7 j.).
	if msg := conn.NextControl(t); msg["type"] != "utterance.stop" || msg["utterance_id"] != nil {
		t.Fatalf("stop = %#v", msg)
	}

	conn.Send(`{"type":"speech_start","utterance_id":"v1-1","offset_ms":0}`)
	conn.Send(`{"type":"final","utterance_id":"v1-1","text":"音声経路OK","end_reason":"stop","audio_ms":1000,"queue_ms":1,"processing_ms":120,"target_called":true}`)
	client.Expect("speech_start")
	final := client.Expect("final")
	if final["text"] != "音声経路OK" || final["utterance_id"] != "v1-1" {
		t.Fatalf("final = %#v", final)
	}

	// The record is made after the final was relayed.
	var recorded []domainaction.Action
	waitUntil(t, "the Action of the utterance", func() bool {
		actions, err := owners.actions.ListActions(context.Background(), domainaction.Filter{Kind: domainaction.KindSTT})
		recorded = actions
		return err == nil && len(recorded) == 1 && domainaction.IsTerminal(recorded[0].Status)
	})
	if want := "stt.stream:" + requestID + ":v1-1"; recorded[0].Name != want || recorded[0].Status != domainaction.StatusSucceeded {
		t.Fatalf("Action = %+v, want name %q succeeded", recorded[0], want)
	}
	tasks, err := owners.tasks.List(context.Background(), domaintask.Filter{Limit: 100})
	if err != nil || len(tasks) != 1 || tasks[0].Status != domaintask.StatusSucceeded {
		t.Fatalf("Tasks=%v err=%v: one short-lived Task per utterance, closed", tasks, err)
	}
	// A transport receipt needs a Run and an Action bound before the request, and
	// nothing precedes an utterance; the Action carries the correlation instead.
	requests, err := owners.transports.ListRequests(context.Background(), domaintransport.RequestFilter{})
	if err != nil || len(requests) != 0 {
		t.Fatalf("the stream must not make transport receipts: requests=%v err=%v", requests, err)
	}
}

func TestBuildSTTRuntimeReportsTheStreamThroughItsOwnHealthProvider(t *testing.T) {
	fake := startFakeSTTServer(t)
	cfg := &config.Config{}
	cfg.STT.Enabled = true
	cfg.STT.GatewayBaseURL = fake.HTTPBase()
	owners := newSTTFactoryOwners(t)
	runtime := buildSTTRuntime(cfg, owners.tasks, owners.actions, owners.transports)
	t.Cleanup(func() { _ = runtime.CloseRecorder(context.Background()) })

	report := runtime.StreamHealth.Health(context.Background())
	if report.Status != modulecore.HealthReady || !report.Ready {
		t.Fatalf("report = %#v", report)
	}
	if _, ok := report.Metadata["recorder_dropped"]; !ok {
		t.Fatalf("a lost record must be visible next to the stream: %#v", report.Metadata)
	}
}

func TestBuildSTTRuntimeWithSTTDisabledServesNoStream(t *testing.T) {
	cfg := &config.Config{}
	cfg.STT.Enabled = false
	owners := newSTTFactoryOwners(t)
	runtime := buildSTTRuntime(cfg, owners.tasks, owners.actions, owners.transports)
	if runtime.Recorder != nil {
		t.Fatal("a disabled STT records nothing")
	}
	if err := runtime.CloseRecorder(context.Background()); err != nil {
		t.Fatalf("CloseRecorder() on a disabled runtime = %v", err)
	}
	mux := http.NewServeMux()
	registerSTTRuntimeRoutes(mux, runtime)
	server := httptest.NewServer(mux)
	defer server.Close()

	client := dialRelay(t, "ws"+strings.TrimPrefix(server.URL, "http")+"/stt")
	event := client.Expect("error")
	if event["error"] != "stt provider is not configured" {
		t.Fatalf("error = %#v", event)
	}
	if report := runtime.StreamHealth.Health(context.Background()); report.Ready {
		t.Fatalf("a disabled STT has no relayable stream: %#v", report)
	}
}

func TestSTTRelayConfigFollowsTheGatewayBaseURL(t *testing.T) {
	cfg := &config.Config{}
	cfg.STT.GatewayBaseURL = "https://stt.example.invalid:8766/"
	relay := sttRelayConfigFromAppConfig(cfg, true, nil)
	if !relay.Enabled || relay.Open == nil || relay.Recorder != nil {
		t.Fatalf("relay config = %+v", relay)
	}
}
