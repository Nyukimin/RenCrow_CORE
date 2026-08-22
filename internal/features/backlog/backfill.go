package backlog

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"reflect"
	"sort"
	"strings"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
)

const (
	CanonicalBackfillDatasetID = "rencrow-atlas-initial-backfill"
	CanonicalBackfillSHA256    = "7fd72429f88ff6eeb4be9e08619087a6c7c7b3038ed1b81a7e0e519dab2fb4af"
	CanonicalBackfillRevision  = 1
)

// The restored package is compiled into CORE. Runtime code never reads the
// staging directory or accepts a filesystem path from an HTTP request.
var (
	//go:embed backfill/atlas_backfill_v1.json
	canonicalBackfillJSON []byte
	//go:embed backfill/atlas_seed_v1.json
	backfillSeedJSON []byte
	//go:embed backfill/specification_artifacts.json
	backfillArtifactJSON []byte
	//go:embed backfill/specifications/*.md
	backfillSpecificationFiles embed.FS
)

type BackfillSourceRef struct {
	Type     string `json:"type"`
	Locator  string `json:"locator"`
	Strength string `json:"strength"`
}

type BackfillItem struct {
	SchemaVersion       int                 `json:"schema_version"`
	FeatureID           string              `json:"feature_id"`
	Category            string              `json:"category"`
	Title               string              `json:"title"`
	Purpose             string              `json:"purpose"`
	Problem             string              `json:"problem"`
	Idea                string              `json:"idea"`
	Background          string              `json:"background"`
	ExpectedEffect      []string            `json:"expected_effect"`
	RelationRefs        []string            `json:"relation_refs"`
	ConceptState        string              `json:"concept_state"`
	DeliveryState       string              `json:"delivery_state"`
	ReconstructionBasis string              `json:"reconstruction_basis"`
	SourceRefs          []BackfillSourceRef `json:"source_refs"`
	SpecificationRefs   []string            `json:"specification_refs"`
	MigrationStatus     string              `json:"migration_status"`
	CreatedAt           string              `json:"created_at"`
	UpdatedAt           string              `json:"updated_at"`
	OriginAtlas         []string            `json:"origin_atlas"`
}

type SeedFeature struct {
	FeatureID   string   `json:"feature_id"`
	Category    string   `json:"category"`
	DisplayName string   `json:"display_name"`
	Purpose     string   `json:"purpose"`
	Summary     string   `json:"summary"`
	Relations   []string `json:"relations"`
	OriginAtlas []string `json:"origin_atlas"`
}

type backfillDocument struct {
	SchemaVersion          int                                   `json:"schema_version"`
	GeneratedAt            string                                `json:"generated_at"`
	Items                  []BackfillItem                        `json:"items"`
	SpecificationArtifacts []domainbacklog.SpecificationArtifact `json:"specification_artifacts"`
}

type seedDocument struct {
	SchemaVersion int           `json:"schema_version"`
	GeneratedAt   string        `json:"generated_at"`
	Features      []SeedFeature `json:"features"`
}

type BackfillPackage struct {
	DatasetID              string
	PackageSHA256          string
	Revision               int
	GeneratedAt            string
	Items                  []BackfillItem
	SeedFeatures           []SeedFeature
	SpecificationArtifacts []domainbacklog.SpecificationArtifact
}

func LoadBackfillPackage() (BackfillPackage, error) {
	hash := sha256.Sum256(canonicalBackfillJSON)
	packageSHA := hex.EncodeToString(hash[:])
	if packageSHA != CanonicalBackfillSHA256 {
		return BackfillPackage{}, fmt.Errorf("Atlas backfill package hash mismatch: %s", packageSHA)
	}
	var document backfillDocument
	if err := json.Unmarshal(canonicalBackfillJSON, &document); err != nil {
		return BackfillPackage{}, fmt.Errorf("decode Atlas backfill package: %w", err)
	}
	var seed seedDocument
	if err := json.Unmarshal(backfillSeedJSON, &seed); err != nil {
		return BackfillPackage{}, fmt.Errorf("decode Atlas seed: %w", err)
	}
	var artifactList []domainbacklog.SpecificationArtifact
	if err := json.Unmarshal(backfillArtifactJSON, &artifactList); err != nil {
		return BackfillPackage{}, fmt.Errorf("decode specification artifact metadata: %w", err)
	}
	if err := validateBackfillDocument(document, seed, artifactList); err != nil {
		return BackfillPackage{}, err
	}
	artifacts := append([]domainbacklog.SpecificationArtifact(nil), document.SpecificationArtifacts...)
	for i := range artifacts {
		if err := loadEmbeddedSpecification(&artifacts[i]); err != nil {
			return BackfillPackage{}, err
		}
	}
	return BackfillPackage{
		DatasetID:              CanonicalBackfillDatasetID,
		PackageSHA256:          packageSHA,
		Revision:               CanonicalBackfillRevision,
		GeneratedAt:            document.GeneratedAt,
		Items:                  append([]BackfillItem(nil), document.Items...),
		SeedFeatures:           append([]SeedFeature(nil), seed.Features...),
		SpecificationArtifacts: artifacts,
	}, nil
}

func validateBackfillDocument(document backfillDocument, seed seedDocument, metadata []domainbacklog.SpecificationArtifact) error {
	if document.SchemaVersion != 1 || len(document.Items) != 114 {
		return fmt.Errorf("Atlas backfill item dataset is incomplete: schema=%d count=%d", document.SchemaVersion, len(document.Items))
	}
	if seed.SchemaVersion != 1 || len(seed.Features) != len(document.Items) {
		return fmt.Errorf("Atlas seed count mismatch: schema=%d count=%d", seed.SchemaVersion, len(seed.Features))
	}
	if !reflect.DeepEqual(document.SpecificationArtifacts, metadata) {
		return errors.New("specification artifact metadata mismatch")
	}
	specIDs := make(map[string]struct{}, len(metadata))
	for i := range metadata {
		if err := domainbacklog.ValidateSpecificationArtifact(metadata[i]); err != nil {
			return err
		}
		if _, exists := specIDs[metadata[i].SpecID]; exists {
			return fmt.Errorf("duplicate specification ID %q", metadata[i].SpecID)
		}
		specIDs[metadata[i].SpecID] = struct{}{}
	}
	items := make(map[string]struct{}, len(document.Items))
	for _, item := range document.Items {
		if item.SchemaVersion != domainbacklog.SchemaVersion2 || strings.TrimSpace(item.FeatureID) == "" || strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.Purpose) == "" || strings.TrimSpace(item.Problem) == "" || strings.TrimSpace(item.Idea) == "" || strings.TrimSpace(item.Background) == "" {
			return fmt.Errorf("invalid Atlas item %q", item.FeatureID)
		}
		if _, exists := items[item.FeatureID]; exists {
			return fmt.Errorf("duplicate Atlas feature_id %q", item.FeatureID)
		}
		switch strings.ToUpper(strings.TrimSpace(item.ConceptState)) {
		case domainbacklog.ConceptRadar, domainbacklog.ConceptCandidate, domainbacklog.ConceptAdopted, domainbacklog.ConceptDeferred, domainbacklog.ConceptRejected:
		default:
			return fmt.Errorf("invalid source concept state %q on %q", item.ConceptState, item.FeatureID)
		}
		switch strings.ToUpper(strings.TrimSpace(item.DeliveryState)) {
		case domainbacklog.DeliveryNone, domainbacklog.DeliverySpec, domainbacklog.DeliveryLiveVerified, "PARTIAL":
		default:
			return fmt.Errorf("invalid source delivery state %q on %q", item.DeliveryState, item.FeatureID)
		}
		items[item.FeatureID] = struct{}{}
		for _, ref := range item.SourceRefs {
			if strings.TrimSpace(ref.Type) == "" || strings.TrimSpace(ref.Locator) == "" || strings.TrimSpace(ref.Strength) == "" {
				return fmt.Errorf("invalid source reference on %q", item.FeatureID)
			}
		}
		for _, specID := range item.SpecificationRefs {
			if _, ok := specIDs[specID]; !ok {
				return fmt.Errorf("Atlas item %q references unknown specification %q", item.FeatureID, specID)
			}
		}
	}
	seedIDs := make(map[string]struct{}, len(seed.Features))
	for _, feature := range seed.Features {
		if strings.TrimSpace(feature.FeatureID) == "" {
			return errors.New("Atlas seed feature_id is required")
		}
		seedIDs[feature.FeatureID] = struct{}{}
	}
	if len(seedIDs) != len(items) {
		return errors.New("Atlas seed feature IDs are not one-to-one with backfill items")
	}
	for id := range items {
		if _, ok := seedIDs[id]; !ok {
			return fmt.Errorf("Atlas seed is missing feature_id %q", id)
		}
	}
	return nil
}

func loadEmbeddedSpecification(artifact *domainbacklog.SpecificationArtifact) error {
	if artifact == nil || strings.TrimSpace(artifact.ContentPath) == "" {
		if artifact != nil {
			artifact.BodyAvailable = false
			artifact.Content = ""
		}
		return nil
	}
	relative := path.Clean(strings.TrimSpace(artifact.ContentPath))
	if relative == "." || strings.HasPrefix(relative, "../") || relative == ".." || !strings.HasPrefix(relative, "specifications/") {
		return fmt.Errorf("unsafe specification content path for %q", artifact.SpecID)
	}
	content, err := backfillSpecificationFiles.ReadFile("backfill/" + relative)
	if err != nil {
		return fmt.Errorf("read embedded specification %q: %w", artifact.SpecID, err)
	}
	hash := sha256.Sum256(content)
	if !strings.EqualFold(hex.EncodeToString(hash[:]), strings.TrimSpace(artifact.ContentSHA256)) {
		return fmt.Errorf("embedded specification hash mismatch for %q", artifact.SpecID)
	}
	artifact.Content = string(content)
	artifact.BodyAvailable = true
	return nil
}

func (p BackfillPackage) Specification(specID string) (domainbacklog.SpecificationArtifact, bool) {
	for _, artifact := range p.SpecificationArtifacts {
		if artifact.SpecID == strings.TrimSpace(specID) {
			return artifact, true
		}
	}
	return domainbacklog.SpecificationArtifact{}, false
}

func (p BackfillPackage) FeatureMaps() []map[string]any {
	features := make([]map[string]any, 0, len(p.SeedFeatures))
	for _, feature := range p.SeedFeatures {
		features = append(features, map[string]any{
			"feature_id": feature.FeatureID, "category": feature.Category,
			"display_name": feature.DisplayName, "purpose": feature.Purpose,
			"summary": feature.Summary, "relations": append([]string(nil), feature.Relations...),
			"origin_atlas": append([]string(nil), feature.OriginAtlas...),
		})
	}
	sort.SliceStable(features, func(i, j int) bool {
		return features[i]["feature_id"].(string) < features[j]["feature_id"].(string)
	})
	return features
}
