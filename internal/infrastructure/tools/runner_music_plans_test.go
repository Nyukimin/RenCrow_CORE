package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type musicPlanGatewayStub struct {
	actorID     string
	created     int
	got         int
	projectID   string
	planID      string
	input       MusicProductionPlanInput
	idempotency string
}

func (s *musicPlanGatewayStub) ActorID() string { return s.actorID }
func (s *musicPlanGatewayStub) CreateProductionPlan(_ context.Context, projectID string, input MusicProductionPlanInput, key string) (MusicProductionPlan, error) {
	s.created++
	s.projectID = projectID
	s.input = input
	s.idempotency = key
	return MusicProductionPlan{
		PlanID: "plan_opaque", ProjectID: projectID, MusicProductionPlanInput: input,
		IntendedUse: "personal_noncommercial", PlanHash: "sha256:" + strings.Repeat("a", 64),
		Executable: false, ExecutionReason: "MUSIC_BACKEND_UNAVAILABLE",
	}, nil
}
func (s *musicPlanGatewayStub) GetProductionPlan(_ context.Context, id string) (MusicProductionPlan, error) {
	s.got++
	s.planID = id
	return MusicProductionPlan{
		PlanID: id, ProjectID: "prj_opaque", MusicProductionPlanInput: MusicProductionPlanInput{
			Kind: "production", SchemaVersion: "1.0", BriefText: "quiet piano", Language: "ja", SongKind: "instrumental",
		},
		IntendedUse: "personal_noncommercial", PlanHash: "sha256:" + strings.Repeat("b", 64),
		ExecutionReason: "MUSIC_BACKEND_UNAVAILABLE",
	}, nil
}

func TestMusicPlanToolsRegisterOnlyWhenPlanGatewayIsConfigured(t *testing.T) {
	without := NewToolRunner(ToolRunnerConfig{DisableToolHarness: true})
	metadata, err := without.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range metadata {
		if strings.HasPrefix(item.ToolID, "music.plan.") {
			t.Fatalf("Music plan Tool registered without plan readiness: %q", item.ToolID)
		}
	}

	runner := NewToolRunner(ToolRunnerConfig{MusicPlans: &musicPlanGatewayStub{actorID: "ren"}, DisableToolHarness: true})
	byID := map[string]tool.ToolMetadata{}
	metadata, err = runner.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range metadata {
		byID[item.ToolID] = item
	}
	for _, name := range []string{"music.plan.create", "music.plan.get"} {
		if _, ok := byID[name]; !ok {
			t.Errorf("configured Music plan Tool %q is missing from Worker Runner metadata", name)
		}
	}
	if _, ok := byID["music.plan.generate"]; ok {
		t.Fatal("production-plan storage must not register music generation")
	}
	create := byID["music.plan.create"]
	if create.Category != "mutation" || create.Origin != tool.OriginCoreRuntime || create.Parameters["additionalProperties"] != false {
		t.Fatalf("plan create metadata = %#v, want strict CORE mutation Tool", create)
	}
	properties, ok := create.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("plan create properties = %T, want schema object", create.Parameters["properties"])
	}
	for _, forbidden := range []string{"actor_id", "source_request_id", "kind", "schema_version", "intended_use", "budget_policy_id", "plan_hash", "executable", "execution_reason"} {
		if _, exists := properties[forbidden]; exists {
			t.Errorf("plan create schema exposes server-owned field %q", forbidden)
		}
	}
}

func TestMusicPlanCreateDerivesIdentityAndPreservesTypedSemanticFields(t *testing.T) {
	gateway := &musicPlanGatewayStub{actorID: "ren"}
	runner := NewToolRunner(ToolRunnerConfig{MusicPlans: gateway, DisableToolHarness: true})
	ctx := delegatedMusicUserContext(t, "root-plan-request-42", "ren", []string{tool.DataScopePublic, tool.DataScopeUser})
	lyricsLock := false
	response, err := runner.ExecuteV2(ctx, "music.plan.create", map[string]any{
		"project_id":         "prj_opaque",
		"brief_text":         "  雨の日に聴く静かな曲  ",
		"language":           "ja",
		"song_kind":          "vocal",
		"mood":               "静か",
		"lyrics":             []any{map[string]any{"section": "verse", "text": "  雨の音が続く  "}},
		"lyrics_lock":        lyricsLock,
		"structure":          []any{"verse", "chorus"},
		"constraints":        []any{"explicit lyrics remain unchanged"},
		"protected_features": []any{"雨音のイメージ"},
		"assumptions":        []any{"テンポは中速"},
		"duration_target_s":  180,
		"candidate_count":    2,
	})
	if err != nil || response == nil || response.Error != nil {
		t.Fatalf("authorized plan create = (%#v, %v), want success", response, err)
	}
	if gateway.created != 1 || gateway.projectID != "prj_opaque" {
		t.Fatalf("gateway create count/project = %d/%q", gateway.created, gateway.projectID)
	}
	if gateway.input.Kind != "production" || gateway.input.SchemaVersion != "1.0" || gateway.input.SourceRequestID != "root-plan-request-42" {
		t.Fatalf("CORE-derived plan identity = %#v", gateway.input)
	}
	if gateway.input.BriefText != "  雨の日に聴く静かな曲  " || len(gateway.input.Lyrics) != 1 || gateway.input.Lyrics[0].Text != "  雨の音が続く  " || gateway.input.LyricsLock == nil || *gateway.input.LyricsLock {
		t.Fatalf("source material or explicit lyrics lock was not preserved: %#v", gateway.input)
	}
	if len(gateway.input.Constraints) != 1 || gateway.input.Constraints[0] != "explicit lyrics remain unchanged" || gateway.input.Assumptions[0] != "テンポは中速" || gateway.input.ProtectedFeatures[0] != "雨音のイメージ" {
		t.Fatalf("typed constraints/assumptions/protected features were not preserved: %#v", gateway.input)
	}
	if !strings.HasPrefix(gateway.idempotency, "rencrow-core-plan-") || len(gateway.idempotency) > 200 {
		t.Fatalf("plan idempotency key = %q, want bounded proof-derived key", gateway.idempotency)
	}
}

func TestMusicPlanToolsRequireDelegatedProofAndRejectRightsOrSourceOverrides(t *testing.T) {
	gateway := &musicPlanGatewayStub{actorID: "ren"}
	runner := NewToolRunner(ToolRunnerConfig{MusicPlans: gateway, DisableToolHarness: true})
	args := map[string]any{"project_id": "prj_opaque", "brief_text": "quiet piano", "song_kind": "instrumental"}
	noScope, err := runner.ExecuteV2(context.Background(), "music.plan.create", args)
	if err != nil || noScope == nil || noScope.Error == nil || noScope.Error.Code != tool.ErrToolExecutionScopeMissing {
		t.Fatalf("plan create without trusted proof = (%#v, %v), want missing-scope denial", noScope, err)
	}
	if gateway.created != 0 {
		t.Fatalf("unauthorized plan create reached gateway %d times", gateway.created)
	}

	ctx := delegatedMusicUserContext(t, "root-plan-request-43", "ren", []string{tool.DataScopePublic, tool.DataScopeUser})
	for _, key := range []string{"source_request_id", "kind", "schema_version", "intended_use", "budget_policy_id", "actor_id", "idempotency_key"} {
		forged := map[string]any{"project_id": "prj_opaque", "brief_text": "quiet piano", "song_kind": "instrumental", key: "attacker-value"}
		response, err := runner.ExecuteV2(ctx, "music.plan.create", forged)
		if err != nil || response == nil || response.Error == nil || response.Error.Code != tool.ErrValidationFailed {
			t.Errorf("forged plan field %q = (%#v, %v), want validation failure", key, response, err)
		}
	}
	if gateway.created != 0 {
		t.Fatalf("forged plan fields reached gateway, create count = %d", gateway.created)
	}
}

func TestMusicPlanGetRequiresDelegatedProofAndOpaqueID(t *testing.T) {
	gateway := &musicPlanGatewayStub{actorID: "ren"}
	runner := NewToolRunner(ToolRunnerConfig{MusicPlans: gateway, DisableToolHarness: true})
	ctx := delegatedMusicUserContext(t, "root-plan-read-1", "ren", []string{tool.DataScopePublic, tool.DataScopeUser})
	response, err := runner.ExecuteV2(ctx, "music.plan.get", map[string]any{"plan_id": "plan_opaque"})
	if err != nil || response == nil || response.Error != nil || gateway.got != 1 || gateway.planID != "plan_opaque" {
		t.Fatalf("authorized plan get = (%#v, %v), gateway=%#v", response, err, gateway)
	}
	for _, planID := range []string{"../other", "plan/other", "plan%2Fother", ""} {
		bad, err := runner.ExecuteV2(ctx, "music.plan.get", map[string]any{"plan_id": planID})
		if err != nil || bad == nil || bad.Error == nil || bad.Error.Code != tool.ErrValidationFailed {
			t.Errorf("unsafe plan ID %q = (%#v, %v), want validation failure", planID, bad, err)
		}
	}
	if gateway.got != 1 {
		t.Fatalf("invalid plan IDs reached gateway, get count = %d", gateway.got)
	}
}
