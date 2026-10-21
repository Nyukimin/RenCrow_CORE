package classifierlab

import (
	"context"
	"errors"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

const (
	SchemaVersion                = "routing-classifier-test/v1"
	MaxHTTPBodyBytes             = 64 << 10
	MaxProbabilityLabels         = 11
	ProbabilityStatusUnvalidated = "upstream_default_not_validated_on_rencrow"
)

var (
	ErrInvalidCandidateOutput = errors.New("invalid candidate output")
	ErrCandidateBodyTooLarge  = errors.New("candidate HTTP body exceeds 64 KiB")
	ErrShadowClosed           = errors.New("shadow classifier is closed")
)

type Mode string

const (
	ModeOff    Mode = "off"
	ModeShadow Mode = "shadow"
)

type RouteLabel string

const (
	LabelCHAT     RouteLabel = "CHAT"
	LabelPLAN     RouteLabel = "PLAN"
	LabelANALYZE  RouteLabel = "ANALYZE"
	LabelOPS      RouteLabel = "OPS"
	LabelRESEARCH RouteLabel = "RESEARCH"
	LabelCODE     RouteLabel = "CODE"
	LabelCODE1    RouteLabel = "CODE1"
	LabelCODE2    RouteLabel = "CODE2"
	LabelCODE3    RouteLabel = "CODE3"
	LabelCODE4    RouteLabel = "CODE4"
	LabelWILD     RouteLabel = "WILD"
)

var routeLabels = [...]RouteLabel{
	LabelCHAT, LabelPLAN, LabelANALYZE, LabelOPS, LabelRESEARCH,
	LabelCODE, LabelCODE1, LabelCODE2, LabelCODE3, LabelCODE4, LabelWILD,
}

func validRouteLabel(route RouteLabel) bool {
	for _, allowed := range routeLabels {
		if route == allowed {
			return true
		}
	}
	return false
}

type CandidateStatus string

const (
	CandidateClassified CandidateStatus = "classified"
	CandidateAbstain    CandidateStatus = "abstain"
)

// CandidateInput and CandidateResult are the experiment-only wire contract.
// They intentionally do not use routing.Decision or routing.Route.
type CandidateInput struct {
	SchemaVersion string `json:"schema_version"`
	RequestID     string `json:"request_id"`
	PolicySHA256  string `json:"policy_sha256"`
	Message       string `json:"message"`
}

type CandidateResult struct {
	SchemaVersion       string                 `json:"schema_version"`
	RequestID           string                 `json:"request_id"`
	Status              CandidateStatus        `json:"status"`
	Route               *RouteLabel            `json:"route"`
	Probabilities       map[RouteLabel]float64 `json:"probabilities,omitempty"`
	SelectedProbability float64                `json:"selected_probability,omitempty"`
	TopTwoMargin        float64                `json:"top_two_margin,omitempty"`
	NativeConfidence    *float64               `json:"native_confidence"`
	ProbabilityStatus   string                 `json:"probability_status,omitempty"`
	Model               CandidateModel         `json:"model,omitempty"`
	PolicySHA256        string                 `json:"policy_sha256"`
	Input               CandidateInputMetadata `json:"input,omitempty"`
	Timing              CandidateTiming        `json:"timing_ms,omitempty"`
	ReasonCode          string                 `json:"reason_code"`
}

type CandidateModel struct {
	Family     string `json:"family"`
	SourceID   string `json:"source_id"`
	Revision   string `json:"revision"`
	SDKVersion string `json:"sdk_version"`
	Device     string `json:"device"`
	DType      string `json:"dtype"`
}

type CandidateInputMetadata struct {
	EffectiveSequenceTokens int  `json:"effective_sequence_tokens"`
	Truncated               bool `json:"truncated"`
}

type CandidateTiming struct {
	Prepare     float64 `json:"prepare"`
	Inference   float64 `json:"inference"`
	Postprocess float64 `json:"postprocess"`
	ServerTotal float64 `json:"server_total"`
}

type CandidateClassifier interface {
	ClassifyCandidate(context.Context, CandidateInput) (CandidateResult, error)
}

type CandidateFactory func(Config) (CandidateClassifier, error)

type PrimaryDecisionSnapshot struct {
	Route      routing.Route
	Confidence float64
	Reason     string
	Evidence   []routing.DecisionEvidence
}

type ComparisonRecord struct {
	RequestID            string
	CorrelationID        string
	Message              string
	PrimaryDecision      PrimaryDecisionSnapshot
	PrimaryError         string
	PrimaryDurationNanos int64
	CandidateResult      *CandidateResult
	CandidateError       string
	CandidateDurationNS  int64
}

// ComparisonRecorder must enqueue or drop synchronously without waiting for
// storage or external I/O. Dropped reports records rejected by the recorder.
type ComparisonRecorder interface {
	TryRecord(ComparisonRecord) bool
	Dropped() uint64
}

type RecordSink func(context.Context, ComparisonRecord) error

type PrimaryClassifier = agent.Classifier

// keep the canonical TurnInput type visible in this package's documentation.
type TurnInput = conversation.TurnInput
