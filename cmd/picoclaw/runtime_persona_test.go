package main

import (
	"testing"

	domainpersona "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/persona"
)

func TestBuildPersonaRuntimeTriggerDefinitionsFromCharacterProfiles(t *testing.T) {
	defs := buildPersonaRuntimeTriggerDefinitions(map[string]domainpersona.CharacterProfile{
		"mio": {
			CharacterID: "mio",
			Persona: map[string]string{
				"triggers/tiredness": "- 疲れた、作業が進まない\n- 迷っている",
				"speech":             "- unrelated",
			},
		},
	})
	if len(defs) != 1 {
		t.Fatalf("defs = %#v", defs)
	}
	def := defs[0]
	if def.CharacterID != "mio" || def.Category != "tiredness" || def.TriggerID != "mio:triggers/tiredness" {
		t.Fatalf("def = %#v", def)
	}
	if len(def.Keywords) != 3 || def.Keywords[0] != "疲れた" || def.Keywords[1] != "作業が進まない" || def.Keywords[2] != "迷っている" {
		t.Fatalf("keywords = %#v", def.Keywords)
	}
}

func TestBuildPersonaRuntimeCanonicalResponsesFromCharacterProfiles(t *testing.T) {
	defs := buildPersonaRuntimeCanonicalResponses(map[string]domainpersona.CharacterProfile{
		"kuro": {
			CharacterID: "kuro",
			Persona: map[string]string{
				"canonical_responses/danger": "# Danger\n\nその操作は止めます。\n理由を確認します。\n\n## Notes\nignored",
				"triggers/danger":            "- 削除",
			},
		},
	})
	if len(defs) != 1 {
		t.Fatalf("defs = %#v", defs)
	}
	def := defs[0]
	if def.CharacterID != "kuro" || def.Category != "danger" || def.ResponseID != "kuro:canonical_responses/danger" {
		t.Fatalf("def = %#v", def)
	}
	if def.Response != "その操作は止めます。\n理由を確認します。" {
		t.Fatalf("response = %q", def.Response)
	}
	if len(def.RequiredContexts) != 1 || def.RequiredContexts[0] != "danger" {
		t.Fatalf("contexts = %#v", def.RequiredContexts)
	}
	if def.CooldownTurns != 5 || def.MaxPerSession != 3 {
		t.Fatalf("policy = %#v", def)
	}
}

func TestBuildPersonaRuntimeDefinitionsUseConfiguredPathsAndCanonicalPolicy(t *testing.T) {
	opts := personaRuntimeDefinitionOptions{
		triggerCategoryPath:            "trigger_categories",
		canonicalResponsePath:          "canonical",
		canonicalResponseCooldownTurns: 9,
		canonicalResponseMaxPerSession: 1,
	}
	characters := map[string]domainpersona.CharacterProfile{
		"mio": {
			CharacterID: "mio",
			Persona: map[string]string{
				"trigger_categories/tiredness": "- 疲れた",
				"canonical/tiredness":          "今日は一手だけに分けます。",
				"triggers/ignored":             "- ignored",
				"canonical_responses/ignored":  "ignored",
			},
		},
	}

	triggers := buildPersonaRuntimeTriggerDefinitionsWithOptions(characters, opts)
	if len(triggers) != 1 || triggers[0].Category != "tiredness" || triggers[0].TriggerID != "mio:trigger_categories/tiredness" {
		t.Fatalf("triggers = %#v", triggers)
	}

	canonicals := buildPersonaRuntimeCanonicalResponsesWithOptions(characters, opts)
	if len(canonicals) != 1 {
		t.Fatalf("canonicals = %#v", canonicals)
	}
	def := canonicals[0]
	if def.Category != "tiredness" || def.ResponseID != "mio:canonical/tiredness" {
		t.Fatalf("canonical = %#v", def)
	}
	if def.CooldownTurns != 9 || def.MaxPerSession != 1 {
		t.Fatalf("policy = %#v", def)
	}
}
