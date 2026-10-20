package speecheval

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestBuildPackKeepsOnlyBlindPlaybackFieldsInPublicManifest(t *testing.T) {
	rendered := renderedCasesForTest()
	manifest, answer, err := BuildPack(strings.Repeat("c", 32), 1, rendered)
	if err != nil {
		t.Fatalf("BuildPack() error = %v", err)
	}
	if manifest.SchemaVersion != "rencrow.speech-eval/1" || len(manifest.Items) != 20 {
		t.Fatalf("public manifest header/items = %#v", manifest)
	}
	if answer.SchemaVersion != "rencrow.speech-eval-answer/1" || len(answer.Items) != 20 {
		t.Fatalf("private answer header/items = %#v", answer)
	}
	wantCueHints := []string{
		"",
		"冒頭に笑う",
		"冒頭に息をのむ、終わりに疑問の声",
		"",
		"終わりにため息をつく",
		"",
		"終わりに笑う",
		"終わりにため息をつく",
		"",
		"冒頭に息をのむ",
		"",
		"",
		"冒頭に叫ぶ、終わりに笑う",
		"",
		"終わりに笑う",
		"",
		"終わりに息を吸う",
		"冒頭に同意の声",
		"",
		"",
	}
	for i, item := range manifest.Items {
		if item.CueHint != wantCueHints[i] {
			t.Errorf("case[%s] cue_hint = %q, want %q", item.ID, item.CueHint, wantCueHints[i])
		}
		if item.CueApplicable != (wantCueHints[i] != "") {
			t.Errorf("case[%s] cue_applicable=%t but cue_hint=%q", item.ID, item.CueApplicable, item.CueHint)
		}
	}
	publicJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	publicText := string(publicJSON)
	for _, privateField := range []string{"neutral", "expressive", "seed", "profile_revision", "sha256", "synthesis_ms", "session_id", "turn_id"} {
		if strings.Contains(publicText, privateField) {
			t.Errorf("public manifest leaks private field or value %q: %s", privateField, publicText)
		}
	}
	seenOrderings := map[string]bool{}
	for i, mapping := range answer.Mapping {
		if mapping.ID != fmt.Sprintf("%02d", i+1) {
			t.Errorf("private mapping[%d].id = %q", i, mapping.ID)
		}
		seenOrderings[mapping.A+"/"+mapping.B] = true
	}
	if len(seenOrderings) != 2 {
		t.Errorf("private per-case mapping did not contain both A/B orderings: %#v", answer.Mapping)
	}
	for _, item := range manifest.Items {
		if !strings.HasPrefix(item.AURL, "/viewer/tts/audio?path=speech-eval/") || !strings.HasPrefix(item.BURL, "/viewer/tts/audio?path=speech-eval/") {
			t.Errorf("item[%s] URLs are outside the safe audio route: %#v", item.ID, item)
		}
	}
}

func TestBuildPackRejectsUnsafeNamesAndMismatchedPairMetadata(t *testing.T) {
	base := renderedCasesForTest()
	base[0].A.Filename = "../../answer.json"
	if _, _, err := BuildPack(strings.Repeat("c", 32), 1, base); err == nil {
		t.Fatal("BuildPack accepted a path-like WAV filename")
	}
	base = renderedCasesForTest()
	base[0].B.Seed++
	if _, _, err := BuildPack(strings.Repeat("c", 32), 1, base); err == nil {
		t.Fatal("BuildPack accepted a different A/B seed")
	}
	base = renderedCasesForTest()
	base[0].A.ProfileRevision++
	if _, _, err := BuildPack(strings.Repeat("c", 32), 1, base); err == nil {
		t.Fatal("BuildPack accepted an unapproved profile revision")
	}
	base = renderedCasesForTest()
	base[0].BVariant = "neutral"
	if _, _, err := BuildPack(strings.Repeat("c", 32), 1, base); err == nil {
		t.Fatal("BuildPack accepted a pair with duplicate variant assignments")
	}
}

func renderedCasesForTest() []RenderedCase {
	rendered := make([]RenderedCase, 0, 20)
	for i, item := range AllCases() {
		seed := uint64(len(rendered) + 100)
		aVariant, bVariant := "neutral", "expressive"
		if i%2 == 1 {
			aVariant, bVariant = bVariant, aVariant
		}
		rendered = append(rendered, RenderedCase{
			Case:      item,
			SessionID: "session-" + item.ID,
			TurnID:    "turn-" + item.ID,
			AVariant:  aVariant,
			BVariant:  bVariant,
			A:         AudioArtifact{Filename: opaqueWAVName('a', item.ID), ProfileRevision: 1, Seed: seed, SHA256: strings.Repeat("a", 64), SynthesisMS: 101},
			B:         AudioArtifact{Filename: opaqueWAVName('b', item.ID), ProfileRevision: 1, Seed: seed, SHA256: strings.Repeat("b", 64), SynthesisMS: 102},
		})
	}
	return rendered
}

func opaqueWAVName(prefix byte, id string) string {
	return strings.Repeat(string(prefix), 32-len(id)) + id + ".wav"
}
