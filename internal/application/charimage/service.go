package charimage

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/charimage"
	domainimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/imagegeneration"
)

const HistoryLimit = 100

var (
	ErrUnavailable        = errors.New("CharImage is unavailable")
	ErrInvalidRequest     = errors.New("CharImage request is invalid")
	ErrProfileUnavailable = errors.New("CharImage profile is unavailable")
	ErrGenerationFailed   = errors.New("CharImage generation failed")
	ErrHistorySaveFailed  = errors.New("CharImage history could not be saved")
)

type ImageGateway interface {
	Options(context.Context) (domainimage.OptionsResponse, error)
	Generate(context.Context, domainimage.GenerateRequest) (domainimage.GenerateResult, error)
}

type HistoryStore interface {
	Ping(context.Context) error
	Save(context.Context, charimage.HistoryEntry) error
	List(context.Context, int) ([]charimage.HistoryEntry, error)
}

type GenerateInput struct {
	ComposeInput charimage.ComposeInput
	Seed         *int64
	Width        *int
	Height       *int
	Steps        *int
	CFGScale     *float64
	FaceDetailer *bool
	LoRAStrength *float64
}

type GenerateOutcome struct {
	CharacterID      string
	RenderMode       charimage.RenderMode
	SceneVariantText string
	Composition      charimage.ComposeOutput
	Image            domainimage.GenerateResult
	Partial          bool
}

type Service struct {
	catalog charimage.Catalog
	gateway ImageGateway
	store   HistoryStore
	clock   func() time.Time
}

func NewService(catalog charimage.Catalog, gateway ImageGateway, store HistoryStore) *Service {
	return &Service{catalog: catalog, gateway: gateway, store: store, clock: time.Now}
}

func (s *Service) Characters() []charimage.Character {
	if s == nil {
		return nil
	}
	return append([]charimage.Character(nil), s.catalog.Characters...)
}

func (s *Service) Model() string {
	if s == nil {
		return charimage.ModelProfileID
	}
	return s.catalog.Model
}

func (s *Service) Compose(input charimage.ComposeInput) (charimage.ComposeOutput, error) {
	if s == nil {
		return charimage.ComposeOutput{}, ErrUnavailable
	}
	composed, err := s.catalog.Compose(input)
	if err != nil {
		return charimage.ComposeOutput{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	return composed, nil
}

func (s *Service) Generate(ctx context.Context, input GenerateInput) (GenerateOutcome, error) {
	if s == nil || s.gateway == nil || s.store == nil {
		return GenerateOutcome{}, ErrUnavailable
	}
	composition, err := s.Compose(input.ComposeInput)
	if err != nil {
		return GenerateOutcome{}, err
	}
	if err := s.store.Ping(ctx); err != nil {
		return GenerateOutcome{}, ErrUnavailable
	}
	options, err := s.gateway.Options(ctx)
	if err != nil || !options.OK || !containsProfile(options.Models, charimage.ModelProfileID) {
		return GenerateOutcome{}, ErrProfileUnavailable
	}
	profile := charimage.ModelProfileID
	request := domainimage.GenerateRequest{
		Prompt:       composition.FinalPrompt,
		Model:        &profile,
		Seed:         input.Seed,
		Width:        input.Width,
		Height:       input.Height,
		Steps:        input.Steps,
		CFGScale:     input.CFGScale,
		FaceDetailer: input.FaceDetailer,
		LoRAStrength: input.LoRAStrength,
	}
	imageResult, err := s.gateway.Generate(ctx, request)
	if err != nil {
		return GenerateOutcome{}, ErrGenerationFailed
	}
	outcome := GenerateOutcome{
		CharacterID:      input.ComposeInput.CharacterID,
		RenderMode:       input.ComposeInput.RenderMode,
		SceneVariantText: input.ComposeInput.SceneVariantText,
		Composition:      composition,
		Image:            imageResult,
	}
	if !validGeneratedImage(imageResult, profile, composition.FinalPrompt) {
		outcome.Partial = true
		return outcome, ErrGenerationFailed
	}
	createdAt := time.Unix(imageResult.Created, 0).UTC()
	if imageResult.Created <= 0 {
		createdAt = s.clock().UTC()
	}
	record := charimage.HistoryEntry{
		ID:                    imageResult.ID,
		CreatedAt:             createdAt,
		CharacterID:           input.ComposeInput.CharacterID,
		RenderMode:            input.ComposeInput.RenderMode,
		SceneVariantText:      input.ComposeInput.SceneVariantText,
		SceneVariantEffective: composition.SceneVariantPrompt,
		FinalPrompt:           composition.FinalPrompt,
		Profile:               profile,
		ImageID:               imageResult.Image.ID,
		ImageSHA256:           imageResult.Image.SHA256,
		ImageSizeBytes:        imageResult.Image.SizeBytes,
		ImageContentType:      imageResult.Image.ContentType,
		ImageWidth:            imageResult.Image.Width,
		ImageHeight:           imageResult.Image.Height,
		CatalogRevision:       s.catalog.Revision,
	}
	if err := s.store.Save(ctx, record); err != nil {
		outcome.Partial = true
		return outcome, ErrHistorySaveFailed
	}
	return outcome, nil
}

func (s *Service) History(ctx context.Context) ([]charimage.HistoryEntry, error) {
	if s == nil || s.store == nil {
		return nil, ErrUnavailable
	}
	items, err := s.store.List(ctx, HistoryLimit)
	if err != nil {
		return nil, ErrUnavailable
	}
	return items, nil
}

func containsProfile(models []domainimage.ImageOptionModel, profile string) bool {
	for _, model := range models {
		if model.ID == profile {
			return true
		}
	}
	return false
}

func validGeneratedImage(result domainimage.GenerateResult, profile, prompt string) bool {
	digest, err := hex.DecodeString(result.Image.SHA256)
	return result.OK && result.ID != "" && result.ID == result.Image.ID && result.Profile == profile &&
		result.Prompt == prompt && result.Image.ContentType == "image/png" &&
		result.Image.Width > 0 && result.Image.Height > 0 && result.Image.SizeBytes > 0 &&
		err == nil && len(digest) == 32 && hex.EncodeToString(digest) == result.Image.SHA256
}
