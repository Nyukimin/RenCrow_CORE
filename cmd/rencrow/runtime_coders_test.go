package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	moduleworker "github.com/Nyukimin/RenCrow_CORE/modules/worker"
	"gopkg.in/yaml.v3"
)

func TestCoderQuadConfigOverlayEnablesNamedSlotsAndAllowsBundlePersonas(t *testing.T) {
	content, err := os.ReadFile("../../config/coder-quad.yaml.example")
	if err != nil {
		t.Fatalf("read coder quad overlay: %v", err)
	}
	var cfg config.Config
	if err := yaml.Unmarshal(content, &cfg); err != nil {
		t.Fatalf("decode coder quad overlay: %v", err)
	}

	text := strings.ToLower(string(content))
	if !strings.Contains(text, "partial overlay") || strings.Contains(text, "gpu:") || strings.Contains(text, "model:") {
		t.Fatalf("overlay must stay partial and leave model/GPU routing outside CORE: %s", content)
	}
	cfg.Prompts = &config.LoadedPrompts{CharacterPrompts: map[string]string{
		"aka": "Aka bundle persona",
		"ao":  "Ao bundle persona",
		"kin": "Kin bundle persona",
		"gin": "Gin bundle persona",
	}}
	for _, tc := range []struct {
		slot  string
		name  string
		coder config.CoderConfig
	}{
		{slot: "coder1", name: "aka", coder: cfg.Coder1},
		{slot: "coder2", name: "ao", coder: cfg.Coder2},
		{slot: "coder3", name: "kin", coder: cfg.Coder3},
		{slot: "coder4", name: "gin", coder: cfg.Coder4},
	} {
		if !tc.coder.Enabled || tc.coder.Name != tc.name || tc.coder.PersonaFile != "" || tc.coder.Personality != "" || tc.coder.LightMemory.Enabled {
			t.Errorf("%s overlay config = %#v", tc.slot, tc.coder)
		}
		personality, source := resolveCoderPersonality(&cfg, tc.coder)
		if personality != strings.ToUpper(tc.name[:1])+tc.name[1:]+" bundle persona" || source != "character bundle: "+tc.name {
			t.Errorf("%s personality/source = %q / %q", tc.slot, personality, source)
		}
	}
	setupPlans := moduleworker.BuildCoderSetupPlans(coderSlotConfigsFromRuntime(&cfg))
	if len(setupPlans) != 4 {
		t.Fatalf("runtime setup plan count = %d, want 4", len(setupPlans))
	}
	for _, plan := range setupPlans {
		if plan.UseLightMemory {
			t.Errorf("%s unexpectedly uses shared LightMemory", plan.Name)
		}
	}
}

func TestLogCoderPromptIdentityWritesSourceAndHashesOnly(t *testing.T) {
	var output bytes.Buffer
	oldWriter := log.Writer()
	oldFlags := log.Flags()
	oldPrefix := log.Prefix()
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
	})

	personality := "persona text must not appear in logs"
	logCoderPromptIdentity("coder1", "赤", "character bundle: aka", personality)
	line := output.String()
	personalityHash := fmt.Sprintf("%x", sha256.Sum256([]byte(personality)))
	contractHash := fmt.Sprintf("%x", sha256.Sum256([]byte(agent.CommonCoderContract)))
	for _, want := range []string{"persona_source=\"character bundle: aka\"", "personality_sha256=" + personalityHash, "common_contract_sha256=" + contractHash} {
		if !strings.Contains(line, want) {
			t.Errorf("log %q missing %q", line, want)
		}
	}
	if strings.Contains(line, personality) || strings.Contains(line, agent.CommonCoderContract) {
		t.Fatalf("log exposed prompt content: %q", line)
	}
}
