package llm

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type fakeRoleProvider struct {
	name string
}

func (p fakeRoleProvider) Name() string {
	return p.name
}

func (p fakeRoleProvider) Health(context.Context) core.HealthReport {
	return core.HealthReport{Module: "llm", Status: core.HealthLive, Ready: true}
}

func (p fakeRoleProvider) Generate(context.Context, GenerateRequest) (GenerateResponse, error) {
	return GenerateResponse{Content: "ok"}, nil
}

func TestBuildRoleProviderMap(t *testing.T) {
	got := BuildRoleProviderMap(RoleProviders{
		Chat:   fakeRoleProvider{name: "chat"},
		Worker: fakeRoleProvider{name: "worker"},
		Heavy:  fakeRoleProvider{name: "heavy"},
		Wild:   fakeRoleProvider{name: "wild"},
	})

	for _, role := range []string{RoleChat, RoleWorker, RoleHeavy, RoleWild} {
		if got[role] == nil {
			t.Fatalf("role provider missing: %s in %+v", role, got)
		}
	}
	if len(got) != 4 {
		t.Fatalf("unexpected provider count: %+v", got)
	}
}

func TestNormalizeRoleName(t *testing.T) {
	if got := NormalizeRoleName(" Worker "); got != RoleWorker {
		t.Fatalf("NormalizeRoleName() = %q", got)
	}
}

func TestBuildGenerateResponse(t *testing.T) {
	got := BuildGenerateResponse(GenerateOutput{
		Content:      "generated",
		TokensUsed:   12,
		FinishReason: "stop",
		ResponseID:   "resp-1",
	})

	if got.Content != "generated" || got.TokensUsed != 12 || got.FinishReason != "stop" || got.ResponseID != "resp-1" {
		t.Fatalf("generate response was not mapped: %+v", got)
	}
}
