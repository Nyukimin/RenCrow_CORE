package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type musicProjectGatewayStub struct {
	actorID     string
	created     int
	got         int
	createInput MusicProjectCreateInput
	createKey   string
	projectID   string
}

func (s *musicProjectGatewayStub) ActorID() string { return s.actorID }
func (s *musicProjectGatewayStub) ProbeCapabilities(context.Context) (MusicCapabilities, error) {
	return MusicCapabilities{}, nil
}
func (s *musicProjectGatewayStub) CreateProject(_ context.Context, input MusicProjectCreateInput, key string) (MusicProject, error) {
	s.created++
	s.createInput = input
	s.createKey = key
	return MusicProject{ID: "prj_opaque", Brief: input.Brief, IntendedUse: "personal_noncommercial", Mode: "interactive", State: "draft"}, nil
}
func (s *musicProjectGatewayStub) GetProject(_ context.Context, id string) (MusicProject, error) {
	s.got++
	s.projectID = id
	return MusicProject{ID: id, Brief: "quiet piano", State: "draft"}, nil
}

func TestMusicProjectToolsRegisterOnlyWhenGatewayIsConfigured(t *testing.T) {
	without := NewToolRunner(ToolRunnerConfig{DisableToolHarness: true})
	withoutMetadata, err := without.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, metadata := range withoutMetadata {
		if strings.HasPrefix(metadata.ToolID, "music.project.") {
			t.Fatalf("Music project Tool registered without gateway: %q", metadata.ToolID)
		}
	}

	with := NewToolRunner(ToolRunnerConfig{MusicProjects: &musicProjectGatewayStub{actorID: "ren"}, DisableToolHarness: true})
	metadata, err := with.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]tool.ToolMetadata{}
	for _, item := range metadata {
		seen[item.ToolID] = item
	}
	for _, name := range []string{"music.project.create", "music.project.get"} {
		if _, ok := seen[name]; !ok {
			t.Errorf("configured Music project Tool %q is missing from production Runner metadata", name)
		}
	}
	if _, ok := seen["music.project.generate"]; ok {
		t.Fatal("music generation must not be registered from project storage capability")
	}
	create := seen["music.project.create"]
	if create.Category != "mutation" || create.Origin != tool.OriginCoreRuntime {
		t.Fatalf("create metadata = %#v, want CORE mutation Tool", create)
	}
	if create.Parameters["additionalProperties"] != false {
		t.Fatalf("create schema must reject untrusted extra actor/key fields: %#v", create.Parameters)
	}
}

func TestMusicProjectToolsRequireDelegatedUserProofAndUseRequestIdentity(t *testing.T) {
	gateway := &musicProjectGatewayStub{actorID: "ren"}
	runner := NewToolRunner(ToolRunnerConfig{MusicProjects: gateway, DisableToolHarness: true})

	noScope, err := runner.ExecuteV2(context.Background(), "music.project.create", map[string]any{"brief": "quiet piano"})
	if err != nil || noScope == nil || noScope.Error == nil || noScope.Error.Code != tool.ErrToolExecutionScopeMissing {
		t.Fatalf("create without trusted HTTP proof = (%#v, %v), want missing-scope denial", noScope, err)
	}
	if gateway.created != 0 {
		t.Fatalf("unauthorized create reached gateway %d times", gateway.created)
	}

	ctx := delegatedMusicUserContext(t, "root-request-42", "ren", []string{tool.DataScopePublic, tool.DataScopeUser})
	response, err := runner.ExecuteV2(ctx, "music.project.create", map[string]any{
		"title": "雨上がり", "brief": "夜の雨上がりに聴く静かなピアノ曲。",
	})
	if err != nil || response == nil || response.Error != nil {
		t.Fatalf("authorized create = (%#v, %v), want success", response, err)
	}
	if gateway.created != 1 || gateway.createInput.Brief != "夜の雨上がりに聴く静かなピアノ曲。" {
		t.Fatalf("gateway create count/input = %d/%#v", gateway.created, gateway.createInput)
	}
	if !strings.HasPrefix(gateway.createKey, "rencrow-core-") || len(gateway.createKey) > 200 {
		t.Fatalf("create key = %q, want bounded request-derived key", gateway.createKey)
	}

	forged := map[string]any{"brief": "quiet piano", "actor_id": "ren", "idempotency_key": "attacker-choice"}
	bad, err := runner.ExecuteV2(ctx, "music.project.create", forged)
	if err != nil || bad == nil || bad.Error == nil || bad.Error.Code != tool.ErrValidationFailed {
		t.Fatalf("forged create arguments = (%#v, %v), want validation failure", bad, err)
	}
	if gateway.created != 1 {
		t.Fatalf("forged arguments reached gateway, create count = %d", gateway.created)
	}
}

func TestMusicProjectGetUsesOpaqueIDAndValidatedScope(t *testing.T) {
	gateway := &musicProjectGatewayStub{actorID: "ren"}
	runner := NewToolRunner(ToolRunnerConfig{MusicProjects: gateway, DisableToolHarness: true})
	ctx := delegatedMusicUserContext(t, "get-request-1", "ren", []string{tool.DataScopePublic, tool.DataScopeUser})
	response, err := runner.ExecuteV2(ctx, "music.project.get", map[string]any{"project_id": "prj_opaque"})
	if err != nil || response == nil || response.Error != nil {
		t.Fatalf("project get = (%#v, %v), want success", response, err)
	}
	if gateway.got != 1 || gateway.projectID != "prj_opaque" {
		t.Fatalf("get count/id = %d/%q", gateway.got, gateway.projectID)
	}
	bad, err := runner.ExecuteV2(ctx, "music.project.get", map[string]any{"project_id": "prj_opaque", "actor_id": "ren"})
	if err != nil || bad == nil || bad.Error == nil || bad.Error.Code != tool.ErrValidationFailed {
		t.Fatalf("get with actor argument = (%#v, %v), want validation failure", bad, err)
	}
	if gateway.got != 1 {
		t.Fatalf("unsupported get argument reached gateway, count = %d", gateway.got)
	}
}

func delegatedMusicUserContext(t *testing.T, requestID, userID string, scopes []string) context.Context {
	t.Helper()
	user, err := tool.NewToolExecutionScope(requestID, tool.ActorKindUser, userID, userID, scopes, tool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatal(err)
	}
	parent := tool.WithToolExecutionScope(context.Background(), user)
	derived, err := tool.DeriveAgentToolExecutionScope(parent, requestID, "worker-agent", "worker", "music project", false)
	if err != nil {
		t.Fatal(err)
	}
	return derived
}
