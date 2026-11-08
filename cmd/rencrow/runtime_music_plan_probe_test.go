package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

const musicPlanProbeTestToken = "music-plan-probe-test-token-0123456789abcdef"

func TestConfigureRuntimeMusicToolsSeparatesProjectAndPlanReadiness(t *testing.T) {
	t.Setenv("REN_CROW_MUSIC_PLAN_TEST_TOKEN", musicPlanProbeTestToken)

	tests := []struct {
		name        string
		capability  map[string]map[string]string
		wantProject bool
		wantPlan    bool
		wantControl bool
	}{
		{
			name: "both ready",
			capability: map[string]map[string]string{
				"project_storage":         {"support_state": "implemented", "availability": "ready"},
				"production_plan_storage": {"support_state": "implemented", "availability": "ready"},
				"production_control":      {"support_state": "implemented", "availability": "ready"},
			},
			wantProject: true, wantPlan: true, wantControl: true,
		},
		{
			name: "older project-only gateway",
			capability: map[string]map[string]string{
				"project_storage": {"support_state": "implemented", "availability": "ready"},
			},
			wantProject: true, wantPlan: false,
		},
		{
			name: "control readiness is independent of storage capabilities",
			capability: map[string]map[string]string{
				"production_control": {"support_state": "implemented", "availability": "ready"},
			},
			wantControl: true,
		},
		{
			name: "unready control is not registered",
			capability: map[string]map[string]string{
				"production_control": {"support_state": "implemented", "availability": "unavailable"},
			},
		},
		{
			name: "plan readiness independent",
			capability: map[string]map[string]string{
				"project_storage":         {"support_state": "implemented", "availability": "unavailable"},
				"production_plan_storage": {"support_state": "implemented", "availability": "ready"},
			},
			wantProject: false, wantPlan: true,
		},
		{
			name: "planned capability is not registered",
			capability: map[string]map[string]string{
				"project_storage":         {"support_state": "implemented", "availability": "ready"},
				"production_plan_storage": {"support_state": "planned", "availability": "unavailable"},
			},
			wantProject: true, wantPlan: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != "/v1/music/capabilities" {
					t.Errorf("probe request = %s %s, want authenticated capabilities GET", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer "+musicPlanProbeTestToken || r.Header.Get("X-RenCrow-Music-Expected-Actor") != "ren" {
					t.Errorf("probe authentication/actor assertion headers are missing")
				}
				caps := map[string]any{"actor_id": "ren", "profile": "s0-local", "capabilities": map[string]any{}}
				projected := caps["capabilities"].(map[string]any)
				for name, status := range tt.capability {
					projected[name] = map[string]any{
						"support_state": status["support_state"], "availability": status["availability"],
						"verification_state": "not_run", "requirement_ids": []string{},
					}
				}
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				if err := json.NewEncoder(w).Encode(caps); err != nil {
					t.Errorf("encode probe response: %v", err)
				}
			}))
			defer server.Close()

			worker := tools.ToolRunnerConfig{}
			if err := configureRuntimeMusicTools(context.Background(), config.MusicConfig{
				Enabled: true, Endpoint: server.URL, ActorID: "ren", TokenEnv: "REN_CROW_MUSIC_PLAN_TEST_TOKEN",
			}, &worker); err != nil {
				t.Fatalf("configureRuntimeMusicTools() error = %v", err)
			}
			if got := worker.MusicProjects != nil; got != tt.wantProject {
				t.Errorf("project gateway configured = %t, want %t", got, tt.wantProject)
			}
			if got := worker.MusicPlans != nil; got != tt.wantPlan {
				t.Errorf("plan gateway configured = %t, want %t", got, tt.wantPlan)
			}
			if got := worker.MusicControl != nil; got != tt.wantControl {
				t.Errorf("production-control gateway configured = %t, want %t", got, tt.wantControl)
			}
			if calls != 1 {
				t.Fatalf("capability probe count = %d, want one", calls)
			}
		})
	}
}
