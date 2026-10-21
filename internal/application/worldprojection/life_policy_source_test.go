package worldprojection

import (
	"context"
	"strings"
	"testing"

	appagentprofile "github.com/Nyukimin/RenCrow_CORE/internal/application/agentprofile"
	domainagentprofile "github.com/Nyukimin/RenCrow_CORE/internal/domain/agentprofile"
	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
)

func TestCatalogLifePolicySourceDerivesRevisionAndActivitySets(t *testing.T) {
	source := &CatalogLifePolicySource{
		Profiles: appagentprofile.NewStaticCatalog(),
		Personas: map[string]domainpersona.CharacterProfile{
			"mio": {CharacterID: "mio", Persona: map[string]string{"self": "Mio self"}},
		},
	}
	got, err := source.Snapshot(context.Background(), ProjectionActor{ActorID: "mio", CoreAssignee: "mio"})
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if !strings.HasPrefix(got.PersonaRevision, "persona-sha256:") ||
		!strings.HasPrefix(got.ConditionProfileRef, "agent-profile-sha256:") {
		t.Fatalf("snapshot = %+v", got)
	}
	if len(got.AllowedNonCognitiveActivityIDs) != 8 || len(got.ExpiredFallbackActivityIDs) != 3 {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.ExpiredFallbackActivityIDs[0] != "idle" {
		t.Fatalf("fallback = %#v", got.ExpiredFallbackActivityIDs)
	}
}

func TestCatalogLifePolicySourceUsesCoreAssigneeSeparatelyFromActorID(t *testing.T) {
	source := &CatalogLifePolicySource{
		Profiles: appagentprofile.NewStaticCatalog(),
		Personas: map[string]domainpersona.CharacterProfile{
			"mio": {CharacterID: "mio", Persona: map[string]string{"self": "Mio self"}},
		},
	}
	got, err := source.Snapshot(context.Background(), ProjectionActor{ActorID: "world-mio", CoreAssignee: "mio"})
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if got.PersonaRevision == "" {
		t.Fatal("persona revision is empty")
	}
}

func TestCatalogLifePolicySourceFailsClosed(t *testing.T) {
	validProfile := appagentprofile.NewStaticCatalog()
	tests := map[string]*CatalogLifePolicySource{
		"missing profile": {
			Profiles: validProfile,
			Personas: map[string]domainpersona.CharacterProfile{"unknown": {CharacterID: "unknown"}},
		},
		"missing persona": {
			Profiles: validProfile,
			Personas: map[string]domainpersona.CharacterProfile{},
		},
		"invalid envelope": {
			Profiles: fakeAgentProfiles{"mio": {ID: "mio"}},
			Personas: map[string]domainpersona.CharacterProfile{"mio": {CharacterID: "mio"}},
		},
	}
	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := source.Snapshot(context.Background(), ProjectionActor{ActorID: "mio", CoreAssignee: "mio"}); err == nil {
				t.Fatal("Snapshot() error = nil")
			}
		})
	}
}

func TestCatalogLifePolicySourceRevisionTracksPersonaAndProfileContent(t *testing.T) {
	personas := map[string]domainpersona.CharacterProfile{
		"mio": {CharacterID: "mio", Persona: map[string]string{"self": "first"}},
	}
	source := &CatalogLifePolicySource{Profiles: appagentprofile.NewStaticCatalog(), Personas: personas}
	first, err := source.Snapshot(context.Background(), ProjectionActor{ActorID: "mio", CoreAssignee: "mio"})
	if err != nil {
		t.Fatalf("Snapshot(first) error = %v", err)
	}

	personas["mio"] = domainpersona.CharacterProfile{CharacterID: "mio", Persona: map[string]string{"self": "second"}}
	second, err := source.Snapshot(context.Background(), ProjectionActor{ActorID: "mio", CoreAssignee: "mio"})
	if err != nil {
		t.Fatalf("Snapshot(second) error = %v", err)
	}
	if first.PersonaRevision == second.PersonaRevision {
		t.Fatal("persona revision did not change with persona content")
	}

	profile, _ := appagentprofile.NewStaticCatalog().Get("mio")
	profile.NonCognitive.Allowed = append(profile.NonCognitive.Allowed, "stand")
	changed := &CatalogLifePolicySource{
		Profiles: fakeAgentProfiles{"mio": profile},
		Personas: personaMap(personas),
	}
	third, err := changed.Snapshot(context.Background(), ProjectionActor{ActorID: "mio", CoreAssignee: "mio"})
	if err != nil {
		t.Fatalf("Snapshot(third) error = %v", err)
	}
	if second.ConditionProfileRef == third.ConditionProfileRef {
		t.Fatal("condition profile ref did not change with AgentProfile content")
	}
}

type fakeAgentProfiles map[string]domainagentprofile.Profile

func (f fakeAgentProfiles) Get(id string) (domainagentprofile.Profile, bool) {
	profile, ok := f[strings.ToLower(strings.TrimSpace(id))]
	return profile, ok
}

func personaMap(source map[string]domainpersona.CharacterProfile) map[string]domainpersona.CharacterProfile {
	out := make(map[string]domainpersona.CharacterProfile, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}
