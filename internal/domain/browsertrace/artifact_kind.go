package browsertrace

import (
	"errors"
	"fmt"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// BrowserTrace artifact types are the content roles emitted by the browsertrace
// builders. They stay on APIArtifact.Type as the content role; ArtifactKind is
// the canonical Artifact classification owned by modules/core.
const (
	APIArtifactTypeObservedOpenAPI   = "observed_openapi"
	APIArtifactTypeCoverageReport    = "coverage_report"
	APIArtifactTypeRiskAssessment    = "risk_assessment"
	APIArtifactTypeEndpointInventory = "endpoint_inventory"
	APIArtifactTypeFetcherPlan       = "fetcher_plan"
	APIArtifactTypeFetcherProposal   = "fetcher_proposal"
	APIArtifactTypeClientDraft       = "client_draft"
)

// apiArtifactKindByType is the single declaration of the browsertrace
// content-role to ArtifactKind correspondence, so the mapping is not hand copied
// into the builders, the stores or the client.
var apiArtifactKindByType = map[string]modulecore.ArtifactKind{
	APIArtifactTypeObservedOpenAPI:   modulecore.ArtifactKindSpecification,
	APIArtifactTypeCoverageReport:    modulecore.ArtifactKindReport,
	APIArtifactTypeRiskAssessment:    modulecore.ArtifactKindReport,
	APIArtifactTypeEndpointInventory: modulecore.ArtifactKindDocument,
	APIArtifactTypeFetcherPlan:       modulecore.ArtifactKindDraft,
	APIArtifactTypeFetcherProposal:   modulecore.ArtifactKindDraft,
	APIArtifactTypeClientDraft:       modulecore.ArtifactKindDraft,
}

// ArtifactKindForAPIArtifactType returns the canonical kind for a browsertrace
// content role. There is no fallback: an unknown type is an error, and a kind is
// never reminted into an artifact at read time.
func ArtifactKindForAPIArtifactType(artifactType string) (modulecore.ArtifactKind, error) {
	kind, ok := apiArtifactKindByType[artifactType]
	if !ok {
		return "", fmt.Errorf("unknown browsertrace artifact type %q", artifactType)
	}
	return kind, nil
}

// ValidateAPIArtifactKindForType checks the persisted kind against the content
// role instead of overwriting it.
func ValidateAPIArtifactKindForType(artifactType string, kind modulecore.ArtifactKind) error {
	expected, err := ArtifactKindForAPIArtifactType(artifactType)
	if err != nil {
		return err
	}
	if err := kind.Validate(); err != nil {
		if kind == "" {
			return errors.New("artifact_kind is required")
		}
		return fmt.Errorf("artifact_kind is invalid: %w", err)
	}
	if kind != expected {
		return fmt.Errorf("artifact_kind %q does not match artifact_type %q, want %q", kind, artifactType, expected)
	}
	return nil
}
