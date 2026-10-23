package charimage

import (
	"strings"
	"testing"
)

func TestEmbeddedCatalogHasOneValidated22CharacterSource(t *testing.T) {
	catalog, err := LoadEmbeddedCatalog()
	if err != nil {
		t.Fatalf("LoadEmbeddedCatalog() error = %v", err)
	}
	if len(catalog.Characters) != CatalogSize || !strings.HasPrefix(catalog.Revision, "sha256:") {
		t.Fatalf("catalog size/revision = %d/%q", len(catalog.Characters), catalog.Revision)
	}
	if catalog.Model != ModelProfileID {
		t.Fatalf("catalog model = %q", catalog.Model)
	}
}

func TestComposeAllCharactersInBothModesUsesFixedFieldsAndScene(t *testing.T) {
	catalog, err := LoadEmbeddedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, character := range catalog.Characters {
		for _, mode := range []RenderMode{RenderMode2D, RenderMode3D} {
			for _, sceneInput := range []string{"", "  Custom scene variant.  "} {
				t.Run(character.ID+"/"+string(mode)+"/"+sceneInput, func(t *testing.T) {
					composed, err := catalog.Compose(ComposeInput{CharacterID: character.ID, RenderMode: mode, SceneVariantText: sceneInput})
					if err != nil {
						t.Fatalf("Compose() error = %v", err)
					}
					wantScene := strings.TrimSpace(sceneInput)
					if wantScene == "" {
						wantScene = strings.TrimSpace(character.DefaultSceneEN)
					}
					wantProfile := character.Fixed.Render2DEN
					wantTail := render2DTail
					if mode == RenderMode3D {
						wantProfile = character.Fixed.Render3DEN
						wantTail = render3DTail
					}
					want := strings.Join([]string{character.Fixed.CharacterCoreEN, wantScene, wantProfile, wantTail}, " ")
					if composed.CharacterCorePrompt != character.Fixed.CharacterCoreEN || composed.RenderProfilePrompt != wantProfile ||
						composed.SceneVariantPrompt != wantScene || composed.FinalPrompt != want || composed.RenderMode != mode {
						t.Fatalf("Compose() = %#v, want core/profile/scene/mode with deterministic final prompt", composed)
					}
				})
			}
		}
	}
}

func TestValidateCatalogRejectsDuplicateIDsStatusDriftAndMissingFixedFields(t *testing.T) {
	catalog, err := LoadEmbeddedCatalog()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("duplicate id", func(t *testing.T) {
		invalid := catalog
		invalid.Characters = append([]Character(nil), catalog.Characters...)
		invalid.Characters[1].ID = invalid.Characters[0].ID
		if err := ValidateCatalog(invalid); err == nil {
			t.Fatal("ValidateCatalog() accepted duplicate ids")
		}
	})
	t.Run("status drift", func(t *testing.T) {
		invalid := catalog
		invalid.Characters = append([]Character(nil), catalog.Characters...)
		invalid.Characters[0].Status = StatusCandidateUnapproved
		if err := ValidateCatalog(invalid); err == nil {
			t.Fatal("ValidateCatalog() accepted status drift")
		}
	})
	t.Run("missing fixed field", func(t *testing.T) {
		invalid := catalog
		invalid.Characters = append([]Character(nil), catalog.Characters...)
		invalid.Characters[0].Fixed.Render2DEN = " "
		if err := ValidateCatalog(invalid); err == nil {
			t.Fatal("ValidateCatalog() accepted a missing fixed render profile")
		}
	})
}

func TestComposeRejectsInvalidModeUnknownCharacterAndOversizedScene(t *testing.T) {
	catalog, err := LoadEmbeddedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]ComposeInput{
		"unknown character": {CharacterID: "missing", RenderMode: RenderMode2D},
		"invalid mode":      {CharacterID: catalog.Characters[0].ID, RenderMode: "sideways"},
		"oversized scene":   {CharacterID: catalog.Characters[0].ID, RenderMode: RenderMode2D, SceneVariantText: strings.Repeat("x", MaxSceneBytes+1)},
		"control character": {CharacterID: catalog.Characters[0].ID, RenderMode: RenderMode2D, SceneVariantText: "scene\x00variant"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := catalog.Compose(input); err == nil {
				t.Fatal("Compose() accepted invalid input")
			}
		})
	}
}
