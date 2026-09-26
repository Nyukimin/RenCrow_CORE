package llmops

import "time"

// FitLevel is llmfit's fit classification for a model on a node. The reported
// string is kept as-is; values outside the documented set are preserved so the
// viewer can still display them.
type FitLevel string

// Documented llmfit fit levels.
const (
	FitLevelPerfect     FitLevel = "perfect"
	FitLevelGood        FitLevel = "good"
	FitLevelMarginal    FitLevel = "marginal"
	FitLevelTooTight    FitLevel = "too_tight"
	FitLevelUnsupported FitLevel = "unsupported"
)

// EstimateConfidence describes how an estimate was obtained. Unknown values are
// preserved as reported so the viewer can decide how to render them.
type EstimateConfidence string

// Documented estimate confidence levels (feature spec "推定値の信頼度").
const (
	EstimateConfidenceMeasuredLocal     EstimateConfidence = "measured_local"
	EstimateConfidenceMeasuredCommunity EstimateConfidence = "measured_community"
	EstimateConfidenceCalibrated        EstimateConfidence = "calibrated"
	EstimateConfidenceEstimated         EstimateConfidence = "estimated"
	EstimateConfidenceUnsupported       EstimateConfidence = "unsupported"
)

// IsKnown reports whether the confidence is one of the documented levels.
func (c EstimateConfidence) IsKnown() bool {
	switch c {
	case EstimateConfidenceMeasuredLocal,
		EstimateConfidenceMeasuredCommunity,
		EstimateConfidenceCalibrated,
		EstimateConfidenceEstimated,
		EstimateConfidenceUnsupported:
		return true
	default:
		return false
	}
}

// IsMeasured reports whether the confidence is backed by a measurement rather
// than a pure estimate.
func (c EstimateConfidence) IsMeasured() bool {
	return c == EstimateConfidenceMeasuredLocal || c == EstimateConfidenceMeasuredCommunity
}

// ModelFitAssessment is llmfit's fit assessment of one model on one node.
// Pointer fields distinguish "not reported" (nil) from a reported zero value;
// RenCrow never substitutes a computed value for a nil.
type ModelFitAssessment struct {
	NodeID   string
	ModelID  string
	Provider string

	ParameterCount string
	ParamsB        float64
	IsMoE          bool

	FitLevel FitLevel
	Score    float64

	QualityScore float64
	SpeedScore   float64
	FitScore     float64
	ContextScore float64

	Runtime string
	RunMode string

	BestQuant *string

	ContextLength          int
	UsableContext          int
	EffectiveContextLength int

	MemoryRequiredGB  float64
	MemoryAvailableGB float64
	UtilizationPct    float64

	EstimatedTPS *float64
	MeasuredTPS  *float64

	PrefillTPS *float64
	TTFTMs     *float64

	EstimateConfidence EstimateConfidence

	Installed    bool
	DiskSizeGB   *float64
	Capabilities []string
	License      string

	Notes []string

	CollectedAt time.Time
}
