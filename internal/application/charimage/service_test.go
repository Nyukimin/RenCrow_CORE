package charimage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/charimage"
	domainimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/imagegeneration"
)

func TestGenerateChecksProfileCallsImageOnceAndSavesMetadata(t *testing.T) {
	catalog, err := charimage.LoadEmbeddedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	gateway := &fakeImageGateway{includeProfile: true}
	store := &fakeHistoryStore{}
	service := NewService(catalog, gateway, store)
	width, height, steps := 832, 896, 24
	seed, cfgScale := int64(7), 4.5
	faceDetailer, loraStrength := true, 0.7
	outcome, err := service.Generate(context.Background(), GenerateInput{
		ComposeInput: charimage.ComposeInput{CharacterID: "01_sera", RenderMode: charimage.RenderMode3D, SceneVariantText: "A custom night market."},
		Seed:         &seed, Width: &width, Height: &height, Steps: &steps, CFGScale: &cfgScale,
		FaceDetailer: &faceDetailer, LoRAStrength: &loraStrength,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if gateway.optionsCalls != 1 || gateway.generateCalls != 1 || store.pingCalls != 1 || store.saveCalls != 1 {
		t.Fatalf("calls options/generate/ping/save = %d/%d/%d/%d", gateway.optionsCalls, gateway.generateCalls, store.pingCalls, store.saveCalls)
	}
	request := gateway.generateRequest
	if request.Model == nil || *request.Model != charimage.ModelProfileID || request.NegativePrompt != "" ||
		request.Width == nil || *request.Width != width || request.Height == nil || *request.Height != height ||
		request.Steps == nil || *request.Steps != steps || request.CFGScale == nil || *request.CFGScale != cfgScale ||
		request.FaceDetailer == nil || !*request.FaceDetailer || request.LoRAStrength == nil || *request.LoRAStrength != loraStrength {
		t.Fatalf("Image request = %#v", request)
	}
	if outcome.Partial || outcome.Image.ID != "img_charimage_1" || outcome.Composition.SceneVariantPrompt != "A custom night market." {
		t.Fatalf("Generate() outcome = %#v", outcome)
	}
	record := store.saved
	if record.CharacterID != "01_sera" || record.RenderMode != charimage.RenderMode3D ||
		record.SceneVariantText != "A custom night market." || record.SceneVariantEffective != "A custom night market." ||
		record.FinalPrompt != outcome.Composition.FinalPrompt || record.Profile != charimage.ModelProfileID ||
		record.ImageSHA256 != strings.Repeat("a", 64) || record.ImageSizeBytes != 4321 ||
		record.CatalogRevision != catalog.Revision || !record.CreatedAt.Equal(time.Unix(outcome.Image.Created, 0).UTC()) {
		t.Fatalf("saved history = %#v", record)
	}
}

func TestGenerateReturnsPartialImageWhenHistorySaveFails(t *testing.T) {
	catalog, err := charimage.LoadEmbeddedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	gateway := &fakeImageGateway{includeProfile: true}
	store := &fakeHistoryStore{saveErr: errors.New("disk full")}
	service := NewService(catalog, gateway, store)
	outcome, err := service.Generate(context.Background(), GenerateInput{
		ComposeInput: charimage.ComposeInput{CharacterID: "01_sera", RenderMode: charimage.RenderMode2D, SceneVariantText: ""},
	})
	if !errors.Is(err, ErrHistorySaveFailed) || !outcome.Partial || outcome.Image.ID != "img_charimage_1" || gateway.generateCalls != 1 {
		t.Fatalf("Generate() outcome=%#v err=%v generateCalls=%d", outcome, err, gateway.generateCalls)
	}
}

func TestGenerateRejectsImageMissingIntegrityMetadataForHistory(t *testing.T) {
	catalog, err := charimage.LoadEmbeddedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	gateway := &fakeImageGateway{includeProfile: true, omitIntegrity: true}
	store := &fakeHistoryStore{}
	service := NewService(catalog, gateway, store)
	outcome, err := service.Generate(context.Background(), GenerateInput{
		ComposeInput: charimage.ComposeInput{CharacterID: "01_sera", RenderMode: charimage.RenderMode2D},
	})
	if !errors.Is(err, ErrGenerationFailed) || !outcome.Partial || outcome.Image.ID != "img_charimage_1" ||
		gateway.generateCalls != 1 || store.saveCalls != 0 {
		t.Fatalf("Generate() outcome=%#v err=%v generateCalls=%d saveCalls=%d", outcome, err, gateway.generateCalls, store.saveCalls)
	}
}

func TestGeneratePreflightsHistoryAndProfileBeforeImageSideEffect(t *testing.T) {
	catalog, err := charimage.LoadEmbeddedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("history unavailable", func(t *testing.T) {
		gateway := &fakeImageGateway{includeProfile: true}
		store := &fakeHistoryStore{pingErr: errors.New("unavailable")}
		service := NewService(catalog, gateway, store)
		if _, err := service.Generate(context.Background(), GenerateInput{ComposeInput: charimage.ComposeInput{CharacterID: "01_sera", RenderMode: charimage.RenderMode2D}}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Generate() error = %v", err)
		}
		if gateway.optionsCalls != 0 || gateway.generateCalls != 0 {
			t.Fatalf("image calls = options:%d generate:%d", gateway.optionsCalls, gateway.generateCalls)
		}
	})
	t.Run("profile unavailable", func(t *testing.T) {
		gateway := &fakeImageGateway{}
		store := &fakeHistoryStore{}
		service := NewService(catalog, gateway, store)
		if _, err := service.Generate(context.Background(), GenerateInput{ComposeInput: charimage.ComposeInput{CharacterID: "01_sera", RenderMode: charimage.RenderMode2D}}); !errors.Is(err, ErrProfileUnavailable) {
			t.Fatalf("Generate() error = %v", err)
		}
		if gateway.generateCalls != 0 {
			t.Fatalf("Generate() called Image %d times", gateway.generateCalls)
		}
	})
}

type fakeImageGateway struct {
	includeProfile  bool
	omitIntegrity   bool
	optionsCalls    int
	generateCalls   int
	generateRequest domainimage.GenerateRequest
}

func (g *fakeImageGateway) Options(context.Context) (domainimage.OptionsResponse, error) {
	g.optionsCalls++
	options := domainimage.OptionsResponse{OK: true}
	if g.includeProfile {
		options.Models = []domainimage.ImageOptionModel{{ID: charimage.ModelProfileID}}
	}
	return options, nil
}

func (g *fakeImageGateway) Generate(_ context.Context, request domainimage.GenerateRequest) (domainimage.GenerateResult, error) {
	g.generateCalls++
	g.generateRequest = request
	image := domainimage.ImageResult{ID: "img_charimage_1", ContentType: "image/png", Width: 768, Height: 768}
	if !g.omitIntegrity {
		image.SHA256 = strings.Repeat("a", 64)
		image.SizeBytes = 4321
	}
	return domainimage.GenerateResult{
		OK: true, ID: "img_charimage_1", Created: 1790000000, Profile: charimage.ModelProfileID, Prompt: request.Prompt,
		Image: image,
	}, nil
}

type fakeHistoryStore struct {
	pingCalls int
	saveCalls int
	saved     charimage.HistoryEntry
	pingErr   error
	saveErr   error
	items     []charimage.HistoryEntry
}

func (s *fakeHistoryStore) Ping(context.Context) error {
	s.pingCalls++
	return s.pingErr
}

func (s *fakeHistoryStore) Save(_ context.Context, entry charimage.HistoryEntry) error {
	s.saveCalls++
	s.saved = entry
	return s.saveErr
}

func (s *fakeHistoryStore) List(_ context.Context, limit int) ([]charimage.HistoryEntry, error) {
	if limit > len(s.items) {
		limit = len(s.items)
	}
	return append([]charimage.HistoryEntry(nil), s.items[:limit]...), nil
}
