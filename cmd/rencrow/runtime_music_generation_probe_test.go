package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

const musicGenerationProbeTestToken = "music-generation-probe-test-token-0123456789abcdef"

func TestRuntimeMusicWorkflowCapabilitiesRegisterOnlyObservedWorkerTools(t *testing.T) {
	t.Setenv("REN_CROW_MUSIC_WORKFLOW_TEST_TOKEN", musicGenerationProbeTestToken)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/music/capabilities" {
			t.Errorf("capability probe request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+musicGenerationProbeTestToken || r.Header.Get("X-RenCrow-Music-Expected-Actor") != "ren" {
			t.Error("capability probe omitted its configured bearer or fixed actor assertion")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"actor_id": "ren", "profile": "s0-local", "capabilities": map[string]any{
				"project_storage":         map[string]string{"support_state": "implemented", "availability": "ready"},
				"production_plan_storage": map[string]string{"support_state": "implemented", "availability": "ready"},
				"music_generation":        map[string]string{"support_state": "implemented", "availability": "ready"},
				"artifact_playback":       map[string]string{"support_state": "implemented", "availability": "ready"},
				"music_remix":             map[string]string{"support_state": "implemented", "availability": "ready"},
			},
		})
	}))
	defer server.Close()

	cfg := testCapabilityRuntimeConfig(t)
	cfg.Music = config.MusicConfig{Enabled: true, Endpoint: server.URL, ActorID: "ren", TokenEnv: "REN_CROW_MUSIC_WORKFLOW_TEST_TOKEN"}
	runtime := buildToolRuntimeWithCapabilities(nil, cfg, nil, nil, nil, nil, nil, testCanonicalMediationStore(t))
	metadata, err := runtime.WorkerRuntimeRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatalf("production Worker ListTools: %v", err)
	}
	snapshot := capdomain.Normalize(buildRuntimeCapabilitySnapshotWithSkills(metadata, nil, nil, nil))
	for _, toolID := range []string{"music.generation.submit", "music.generation.status", "music.remix.create"} {
		if !runtimeHasMusicWorkflowTool(metadata, toolID) {
			t.Errorf("observed ready Music workflow capability is missing Worker Tool %q", toolID)
		}
		entry, ok := findRuntimeCapability(snapshot, capdomain.CapabilityKindTool, toolID)
		if !ok || entry.Status != capdomain.CapabilityStatusAvailable {
			t.Errorf("available Music Tool %q is absent from Worker capability snapshot: %+v", toolID, snapshot.Entries)
		}
	}
	if runtimeHasMusicWorkflowTool(metadata, "music.generation.cancel") || runtimeHasMusicWorkflowTool(metadata, "music.studio.invoke") {
		t.Fatal("Music workflow must not expose unrequested cancel or direct Studio tools")
	}
}

func TestConfigureRuntimeMusicWorkflowReadinessIsIndependent(t *testing.T) {
	t.Setenv("REN_CROW_MUSIC_WORKFLOW_TEST_TOKEN", musicGenerationProbeTestToken)
	tests := []struct {
		name           string
		capabilities   map[string]map[string]string
		wantGeneration bool
		wantPlayback   bool
		wantRemix      bool
	}{
		{
			name: "generation only",
			capabilities: map[string]map[string]string{
				"music_generation": {"support_state": "implemented", "availability": "ready"},
			},
			wantGeneration: true,
		},
		{
			name: "playback only",
			capabilities: map[string]map[string]string{
				"artifact_playback": {"support_state": "implemented", "availability": "ready"},
			},
			wantPlayback: true,
		},
		{
			name: "remix only",
			capabilities: map[string]map[string]string{
				"music_remix": {"support_state": "implemented", "availability": "ready"},
			},
			wantRemix: true,
		},
		{
			name: "planned generation remains hidden",
			capabilities: map[string]map[string]string{
				"music_generation":  {"support_state": "planned", "availability": "unavailable"},
				"artifact_playback": {"support_state": "implemented", "availability": "ready"},
				"music_remix":       {"support_state": "implemented", "availability": "ready"},
			},
			wantPlayback: true, wantRemix: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/music/capabilities" {
					t.Errorf("capability probe request = %s %s", r.Method, r.URL.Path)
				}
				capabilities := map[string]any{}
				for name, status := range tt.capabilities {
					capabilities[name] = map[string]any{"support_state": status["support_state"], "availability": status["availability"]}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"actor_id": "ren", "profile": "s0-local", "capabilities": capabilities})
			}))
			defer server.Close()

			worker := tools.ToolRunnerConfig{}
			if err := configureRuntimeMusicTools(context.Background(), config.MusicConfig{
				Enabled: true, Endpoint: server.URL, ActorID: "ren", TokenEnv: "REN_CROW_MUSIC_WORKFLOW_TEST_TOKEN",
			}, &worker); err != nil {
				t.Fatalf("configureRuntimeMusicTools() error = %v", err)
			}
			if got := worker.MusicGeneration != nil; got != tt.wantGeneration {
				t.Errorf("Music generation gateway ready=%v, want %v", got, tt.wantGeneration)
			}
			if got := worker.MusicArtifactPlayback != nil; got != tt.wantPlayback {
				t.Errorf("artifact playback gateway ready=%v, want %v", got, tt.wantPlayback)
			}
			if got := worker.MusicRemix != nil; got != tt.wantRemix {
				t.Errorf("Music remix gateway ready=%v, want %v", got, tt.wantRemix)
			}
		})
	}
}

func runtimeHasMusicWorkflowTool(metadata []domaintool.ToolMetadata, toolID string) bool {
	for _, item := range metadata {
		if item.ToolID == toolID {
			return true
		}
	}
	return false
}
