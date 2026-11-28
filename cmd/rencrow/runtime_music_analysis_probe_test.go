package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

const musicAnalysisProbeToken = "music-analysis-probe-token-0123456789abcdef"

func TestAudioStudioProfileRegistersOnlyReadyOperationsAndRetainsImportStatus(t *testing.T) {
	t.Setenv("REN_CROW_MUSIC_ANALYSIS_TEST_TOKEN", musicAnalysisProbeToken)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/music/capabilities" {
			t.Errorf("capability probe request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+musicAnalysisProbeToken || r.Header.Get("X-RenCrow-Music-Expected-Actor") != "ren" {
			t.Error("Music capability probe omitted its fixed auth assertion")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"actor_id": "ren", "profile": "audio-studio", "capabilities": map[string]any{
				"project_storage":         readyMusicProbeCapability(),
				"music_analysis_beats":    readyMusicProbeCapability(),
				"music_analysis_captions": unavailableMusicProbeCapability(),
				"music_source_search":     readyMusicProbeCapability(),
				"audio_import":            unavailableMusicProbeCapability(),
			},
		})
	}))
	defer server.Close()
	worker := &tools.ToolRunnerConfig{}
	if err := configureRuntimeMusicTools(context.Background(), config.MusicConfig{
		Enabled: true, Endpoint: server.URL, ActorID: "ren", TokenEnv: "REN_CROW_MUSIC_ANALYSIS_TEST_TOKEN",
	}, worker); err != nil {
		t.Fatalf("configureRuntimeMusicTools() error = %v", err)
	}
	if worker.MusicAnalysisBeats == nil || worker.MusicAnalysisCaptions != nil || worker.MusicSourceSearch == nil || worker.MusicAudioImport == nil || worker.MusicAudioImportReady {
		t.Fatalf("Audio Studio capability wiring = beats:%t captions:%t search:%t import-status:%t import-ready:%t",
			worker.MusicAnalysisBeats != nil, worker.MusicAnalysisCaptions != nil, worker.MusicSourceSearch != nil, worker.MusicAudioImport != nil, worker.MusicAudioImportReady)
	}
	runner := tools.NewToolRunner(*worker)
	metadata, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatalf("Worker ListTools() error = %v", err)
	}
	snapshot := capdomain.Normalize(buildRuntimeCapabilitySnapshotWithSkills(metadata, nil, nil, nil))
	for toolID, want := range map[string]bool{
		"music.analysis.create": true, "music.analysis.get": true, "music.source.search": true,
		"music.audio.import": false, "music.audio.import.status": true,
	} {
		if got := hasToolMetadata(metadata, toolID); got != want {
			t.Errorf("Tool %q registered=%v want=%v", toolID, got, want)
		}
		entry, found := findRuntimeCapability(snapshot, capdomain.CapabilityKindTool, toolID)
		if got := found && entry.Status == capdomain.CapabilityStatusAvailable; got != want {
			t.Errorf("capability snapshot Tool %q available=%v want=%v entry=%+v", toolID, got, want, entry)
		}
	}
}

func TestMusicAnalysisReaderSurvivesBlockedCreationCapabilitiesInRuntimeSnapshot(t *testing.T) {
	for _, test := range []struct {
		name         string
		analysisCaps map[string]any
	}{
		{name: "blocked", analysisCaps: map[string]any{
			"music_analysis_beats": blockedMusicProbeCapability(), "music_analysis_captions": blockedMusicProbeCapability(),
		}},
		{name: "absent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("REN_CROW_MUSIC_ANALYSIS_TEST_TOKEN", musicAnalysisProbeToken)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/music/capabilities" {
					t.Errorf("capability probe request = %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer "+musicAnalysisProbeToken || r.Header.Get("X-RenCrow-Music-Expected-Actor") != "ren" {
					t.Error("Music capability probe omitted its fixed auth assertion")
				}
				capabilities := map[string]any{
					"project_storage":     readyMusicProbeCapability(),
					"music_source_search": unavailableMusicProbeCapability(),
					"audio_import":        unavailableMusicProbeCapability(),
				}
				for name, status := range test.analysisCaps {
					capabilities[name] = status
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"actor_id": "ren", "profile": "audio-studio", "capabilities": capabilities})
			}))
			defer server.Close()
			worker := &tools.ToolRunnerConfig{}
			if err := configureRuntimeMusicTools(context.Background(), config.MusicConfig{
				Enabled: true, Endpoint: server.URL, ActorID: "ren", TokenEnv: "REN_CROW_MUSIC_ANALYSIS_TEST_TOKEN",
			}, worker); err != nil {
				t.Fatalf("configureRuntimeMusicTools() error = %v", err)
			}
			if worker.MusicAnalysisReader == nil || worker.MusicAnalysisBeats != nil || worker.MusicAnalysisCaptions != nil {
				t.Fatalf("analysis wiring = reader:%t beats:%t captions:%t",
					worker.MusicAnalysisReader != nil, worker.MusicAnalysisBeats != nil, worker.MusicAnalysisCaptions != nil)
			}
			runner := tools.NewToolRunner(*worker)
			metadata, err := runner.ListTools(context.Background())
			if err != nil {
				t.Fatalf("Worker ListTools() error = %v", err)
			}
			if !hasToolMetadata(metadata, "music.analysis.get") || hasToolMetadata(metadata, "music.analysis.create") {
				t.Fatalf("analysis capability tool registration = get:%v create:%v", hasToolMetadata(metadata, "music.analysis.get"), hasToolMetadata(metadata, "music.analysis.create"))
			}
			snapshot := capdomain.Normalize(buildRuntimeCapabilitySnapshotWithSkills(metadata, nil, nil, nil))
			readerEntry, readerFound := findRuntimeCapability(snapshot, capdomain.CapabilityKindTool, "music.analysis.get")
			if !readerFound || readerEntry.Status != capdomain.CapabilityStatusAvailable {
				t.Fatalf("analysis reader snapshot entry=%+v found=%v", readerEntry, readerFound)
			}
			if _, createFound := findRuntimeCapability(snapshot, capdomain.CapabilityKindTool, "music.analysis.create"); createFound {
				t.Fatal("blocked or absent analysis creation unexpectedly appeared in the runtime snapshot")
			}
		})
	}
}

func TestMusicCapabilityProbeRejectsUndocumentedProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"actor_id":"ren","profile":"other-profile","capabilities":{}}`))
	}))
	defer server.Close()
	client, err := tools.NewMusicProjectGatewayClient(tools.MusicGatewayClientConfig{
		Endpoint: server.URL, ActorID: "ren", BearerToken: musicAnalysisProbeToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ProbeCapabilities(context.Background()); err == nil {
		t.Fatal("Music capability probe accepted an undocumented profile")
	}
}

func readyMusicProbeCapability() map[string]any {
	return map[string]any{"requirement_ids": []string{"AR02"}, "support_state": "implemented", "verification_state": "not_run", "availability": "ready", "evidence_scope": "test"}
}

func unavailableMusicProbeCapability() map[string]any {
	return map[string]any{"requirement_ids": []string{"AR02"}, "support_state": "implemented", "verification_state": "not_run", "availability": "unavailable", "reason": "not_configured", "evidence_scope": "test"}
}

func blockedMusicProbeCapability() map[string]any {
	return map[string]any{"requirement_ids": []string{"AR02"}, "support_state": "implemented", "verification_state": "not_run", "availability": "blocked", "reason": "durable_gate_needs_reconciliation", "evidence_scope": "test"}
}
