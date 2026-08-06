package trade

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"
)

const PrivateContractVersion = "trade-private/v1"
const PolicyEvaluationContractVersion = "trade-policy-evaluation/v1"
const RiskPreviewRequestContractVersion = "trade-risk-preview/v1"
const RiskPreviewPlanContractVersion = "risk-preview-plan/v1"
const SimulationCommitContractVersion = "trade-simulation-commit/v1"
const ShadowObservationContractVersion = "shadow-observation/v1"

type PolicyStatus struct {
	SchemaVersion          int             `json:"schema_version"`
	PolicyID               string          `json:"policy_id"`
	CoreContractRevision   string          `json:"core_contract_revision"`
	ModulePolicyRevision   string          `json:"module_policy_revision"`
	BinaryContractRevision string          `json:"binary_contract_revision"`
	ExecutionMode          string          `json:"execution_mode"`
	BrokerAdapter          string          `json:"broker_adapter"`
	KillSwitch             string          `json:"kill_switch"`
	Capabilities           map[string]bool `json:"capabilities"`
	BlockedCapabilities    []string        `json:"blocked_capabilities"`
}

type DependencyStatuses struct {
	Broker     string `json:"broker"`
	Ledger     string `json:"ledger"`
	MarketData string `json:"market_data"`
}

type PrivateStatus struct {
	ContractVersion string              `json:"contract_version"`
	ServiceStatus   string              `json:"service_status"`
	CorrelationID   string              `json:"correlation_id"`
	ExecutionMode   string              `json:"execution_mode"`
	LearningMode    string              `json:"learning_mode"`
	Ready           bool                `json:"ready"`
	KillSwitch      string              `json:"kill_switch"`
	Dependencies    DependencyStatuses  `json:"dependencies"`
	Policy          PolicyStatus        `json:"policy"`
	Portfolio       PortfolioProjection `json:"portfolio"`
}

type PortfolioProjection struct {
	Status   string             `json:"status"`
	Snapshot *PortfolioSnapshot `json:"snapshot,omitempty"`
}

type PortfolioSnapshot struct {
	SchemaVersion    int                 `json:"schema_version"`
	PortfolioID      string              `json:"portfolio_id"`
	Mode             string              `json:"mode"`
	Guaranteed       bool                `json:"guaranteed"`
	InitialCashJPY   int64               `json:"initial_cash_jpy"`
	CashJPY          int64               `json:"cash_jpy"`
	RealizedPnLJPY   int64               `json:"realized_pnl_jpy"`
	UnrealizedPnLJPY *int64              `json:"unrealized_pnl_jpy,omitempty"`
	NAVJPY           *int64              `json:"nav_jpy,omitempty"`
	ValuationStatus  string              `json:"valuation_status"`
	Positions        []PortfolioPosition `json:"positions"`
	EventCount       int64               `json:"event_count"`
	LatestEventHash  string              `json:"latest_event_hash"`
}

type PortfolioPosition struct {
	InstrumentID     string `json:"instrument_id"`
	Quantity         int64  `json:"quantity"`
	CostBasisJPY     int64  `json:"cost_basis_jpy"`
	MarkPriceJPY     *int64 `json:"mark_price_jpy,omitempty"`
	MarketValueJPY   *int64 `json:"market_value_jpy,omitempty"`
	UnrealizedPnLJPY *int64 `json:"unrealized_pnl_jpy,omitempty"`
}

type GlobalPolicyInput struct {
	ContractRevision string `json:"contract_revision"`
	BundleRevision   string `json:"bundle_revision"`
	ContentSHA256    string `json:"content_sha256"`
	Allowed          bool   `json:"allowed"`
}

type PolicyLayerInput struct {
	Revision string `json:"revision"`
	Allowed  bool   `json:"allowed"`
}

type PolicyEvaluationRequest struct {
	ContractVersion string            `json:"contract_version"`
	RequestID       string            `json:"request_id"`
	Capability      string            `json:"capability"`
	GlobalPolicy    GlobalPolicyInput `json:"global_policy"`
	Deployment      PolicyLayerInput  `json:"deployment"`
	RequestScope    PolicyLayerInput  `json:"request_scope"`
}

type PolicyDecision struct {
	Capability             string `json:"capability"`
	Status                 string `json:"status"`
	ReasonCode             string `json:"reason_code"`
	Reason                 string `json:"reason"`
	BinaryContractRevision string `json:"binary_contract_revision"`
	ModulePolicyRevision   string `json:"module_policy_revision"`
	PolicyID               string `json:"policy_id"`
	GlobalBundleRevision   string `json:"global_bundle_revision"`
	DeploymentRevision     string `json:"deployment_revision"`
	RequestScopeRevision   string `json:"request_scope_revision"`
}

type PrivatePolicyEvaluation struct {
	ContractVersion string         `json:"contract_version"`
	ServiceStatus   string         `json:"service_status"`
	CorrelationID   string         `json:"correlation_id"`
	ExecutionMode   string         `json:"execution_mode"`
	LearningMode    string         `json:"learning_mode"`
	Decision        PolicyDecision `json:"decision"`
}

type RiskPreviewRequest struct {
	ContractVersion string          `json:"contract_version"`
	RequestID       string          `json:"request_id"`
	Plan            RiskPreviewPlan `json:"plan"`
}

type RiskPreviewPlan struct {
	ContractVersion string                  `json:"contract_version"`
	PlanID          string                  `json:"plan_id"`
	PolicyRevision  string                  `json:"policy_revision"`
	AsOf            string                  `json:"as_of"`
	Selection       RiskPreviewSelection    `json:"selection"`
	Proposal        RiskPreviewBuyProposal  `json:"proposal"`
	ExitContract    RiskPreviewExitContract `json:"exit_contract"`
	Limits          RiskPreviewLimits       `json:"limits"`
}

type RiskPreviewSelection struct {
	InstrumentID     string   `json:"instrument_id"`
	Status           string   `json:"status"`
	EvidenceRefs     []string `json:"evidence_refs"`
	KnownMissingData []string `json:"known_missing_data"`
}

type RiskPreviewBuyProposal struct {
	InstrumentID         string `json:"instrument_id"`
	Side                 string `json:"side"`
	Quantity             int64  `json:"quantity"`
	EntryPriceJPY        int64  `json:"entry_price_jpy"`
	GrossJPY             int64  `json:"gross_jpy"`
	EntryFeesJPY         int64  `json:"entry_fees_jpy"`
	EstimatedExitFeesJPY int64  `json:"estimated_exit_fees_jpy"`
}

type RiskPreviewExitContract struct {
	ContractID                   string   `json:"exit_contract_id"`
	Revision                     string   `json:"revision"`
	InstrumentID                 string   `json:"instrument_id"`
	StopTriggerPriceJPY          int64    `json:"stop_trigger_price_jpy"`
	WorstCaseExitPriceJPY        int64    `json:"worst_case_exit_price_jpy"`
	GapSlippageBufferJPY         int64    `json:"gap_slippage_buffer_jpy"`
	TimeDeadline                 string   `json:"time_deadline"`
	ThesisInvalidationConditions []string `json:"thesis_invalidation_conditions"`
	EventConditions              []string `json:"event_conditions"`
	LiquidityConditions          []string `json:"liquidity_conditions"`
	PortfolioConditions          []string `json:"portfolio_conditions"`
	DataConditions               []string `json:"data_conditions"`
	OperationalConditions        []string `json:"operational_conditions"`
}

type RiskPreviewLimits struct {
	MaxLossJPY     int64 `json:"max_loss_jpy"`
	MaxPositionBPS int64 `json:"max_position_bps"`
	MaxInvestedBPS int64 `json:"max_invested_bps"`
	MinCashBPS     int64 `json:"min_cash_bps"`
	MaxPositions   int64 `json:"max_positions"`
}

type RiskPreviewMetrics struct {
	PreTradeNAVJPY         int64 `json:"pre_trade_nav_jpy"`
	PostTradeCashJPY       int64 `json:"post_trade_cash_jpy"`
	ProspectivePositionJPY int64 `json:"prospective_position_jpy"`
	ProspectiveInvestedJPY int64 `json:"prospective_invested_jpy"`
	ConservativeLossJPY    int64 `json:"conservative_loss_jpy"`
	PositionBPS            int64 `json:"position_bps"`
	InvestedBPS            int64 `json:"invested_bps"`
	CashBPS                int64 `json:"cash_bps"`
	PositionCount          int64 `json:"position_count"`
}

type RiskPreviewDecision struct {
	ContractVersion     string             `json:"contract_version"`
	PlanID              string             `json:"plan_id"`
	PolicyRevision      string             `json:"policy_revision"`
	AsOf                string             `json:"as_of"`
	InstrumentID        string             `json:"instrument_id"`
	Status              string             `json:"status"`
	ReasonCodes         []string           `json:"reason_codes"`
	Metrics             RiskPreviewMetrics `json:"metrics"`
	InputSnapshotSHA256 string             `json:"input_snapshot_sha256"`
	AuthorizesExecution bool               `json:"authorizes_execution"`
	MutatesPortfolio    bool               `json:"mutates_portfolio"`
}

type PrivateRiskPreview struct {
	ContractVersion          string              `json:"contract_version"`
	ServiceStatus            string              `json:"service_status"`
	CorrelationID            string              `json:"correlation_id"`
	ExecutionMode            string              `json:"execution_mode"`
	LearningMode             string              `json:"learning_mode"`
	RequestID                string              `json:"request_id"`
	PortfolioID              string              `json:"portfolio_id"`
	PortfolioEventCount      int64               `json:"portfolio_event_count"`
	PortfolioLatestEventHash string              `json:"portfolio_latest_event_hash"`
	Decision                 RiskPreviewDecision `json:"decision"`
}

type SimulationCommitRequest struct {
	ContractVersion                  string                  `json:"contract_version"`
	RequestID                        string                  `json:"request_id"`
	IdempotencyKey                   string                  `json:"idempotency_key"`
	ExpectedPortfolioEventCount      int64                   `json:"expected_portfolio_event_count"`
	ExpectedPortfolioLatestEventHash string                  `json:"expected_portfolio_latest_event_hash"`
	ExpectedInputSnapshotSHA256      string                  `json:"expected_input_snapshot_sha256"`
	Plan                             RiskPreviewPlan         `json:"plan"`
	Policy                           PolicyEvaluationRequest `json:"policy"`
}

type PrivateSimulationCommit struct {
	ContractVersion             string               `json:"contract_version"`
	ServiceStatus               string               `json:"service_status"`
	CorrelationID               string               `json:"correlation_id"`
	ExecutionMode               string               `json:"execution_mode"`
	LearningMode                string               `json:"learning_mode"`
	RequestID                   string               `json:"request_id"`
	PortfolioID                 string               `json:"portfolio_id"`
	Mode                        string               `json:"mode"`
	AuthorizesExternalExecution bool                 `json:"authorizes_external_execution"`
	PortfolioMutated            bool                 `json:"portfolio_mutated"`
	IdempotentReplay            bool                 `json:"idempotent_replay"`
	PreviousPortfolioEventCount int64                `json:"previous_portfolio_event_count"`
	PreviousPortfolioLatestHash string               `json:"previous_portfolio_latest_event_hash"`
	PolicyDecision              PolicyDecision       `json:"policy_decision"`
	RiskDecision                *RiskPreviewDecision `json:"risk_decision,omitempty"`
	Snapshot                    PortfolioSnapshot    `json:"snapshot"`
}

type ShadowObservationInput struct {
	IdempotencyKey             string   `json:"idempotency_key"`
	StudyID                    string   `json:"study_id"`
	DecisionID                 string   `json:"decision_id"`
	ActorID                    string   `json:"actor_id"`
	InstrumentID               string   `json:"instrument_id"`
	DecisionKind               string   `json:"decision_kind"`
	MarketObservedAt           string   `json:"market_observed_at"`
	ContextSnapshotSHA256      string   `json:"context_snapshot_sha256"`
	OutcomeLabelContractSHA256 string   `json:"outcome_label_contract_sha256"`
	ReasonCodes                []string `json:"reason_codes"`
	EvidenceRefs               []string `json:"evidence_refs"`
}

type ShadowObservationRequest struct {
	ContractVersion string                  `json:"contract_version"`
	RequestID       string                  `json:"request_id"`
	Observation     ShadowObservationInput  `json:"observation"`
	Policy          PolicyEvaluationRequest `json:"policy"`
}

type ShadowObservationEvent struct {
	EventVersion int    `json:"event_version"`
	EventID      string `json:"event_id"`
	Sequence     int64  `json:"sequence"`
	RecordedAt   string `json:"recorded_at"`
	Type         string `json:"type"`
	ShadowObservationInput
	PreviousHash string `json:"previous_hash"`
	EventHash    string `json:"event_hash"`
}

type PrivateShadowObservation struct {
	ContractVersion             string                 `json:"contract_version"`
	ServiceStatus               string                 `json:"service_status"`
	CorrelationID               string                 `json:"correlation_id"`
	ExecutionMode               string                 `json:"execution_mode"`
	LearningMode                string                 `json:"learning_mode"`
	RequestID                   string                 `json:"request_id"`
	Environment                 string                 `json:"environment"`
	AuthorizesExternalExecution bool                   `json:"authorizes_external_execution"`
	PortfolioMutated            bool                   `json:"portfolio_mutated"`
	KnowledgePromoted           bool                   `json:"knowledge_promoted"`
	IdempotentReplay            bool                   `json:"idempotent_replay"`
	PolicyDecision              PolicyDecision         `json:"policy_decision"`
	Event                       ShadowObservationEvent `json:"event"`
}

var policySHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var eventSHA256Pattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func (status PrivateStatus) ValidateDisabledFoundation() error {
	if status.ContractVersion != PrivateContractVersion {
		return fmt.Errorf("unsupported TRADE contract version %q", status.ContractVersion)
	}
	if strings.TrimSpace(status.CorrelationID) == "" || strings.TrimSpace(status.ServiceStatus) == "" {
		return fmt.Errorf("TRADE status envelope is incomplete")
	}
	if status.ExecutionMode != "DISABLED" || status.Policy.ExecutionMode != "DISABLED" {
		return fmt.Errorf("TRADE execution mode is not authorized by the disabled foundation")
	}
	if status.KillSwitch != "ON" || status.Policy.KillSwitch != "ON" {
		return fmt.Errorf("TRADE kill switch is not ON")
	}
	if status.Policy.BrokerAdapter != "none" || status.Dependencies.Broker != "disabled" {
		return fmt.Errorf("TRADE broker boundary is not disabled")
	}
	if status.Policy.Capabilities["broker_network"] || status.Policy.Capabilities["paper_order"] || status.Policy.Capabilities["live_order"] {
		return fmt.Errorf("TRADE reports an unauthorized external trading capability")
	}
	if strings.TrimSpace(status.Policy.ModulePolicyRevision) == "" || strings.TrimSpace(status.Policy.BinaryContractRevision) == "" {
		return fmt.Errorf("TRADE policy revisions are incomplete")
	}
	switch status.Portfolio.Status {
	case "unconfigured", "not_initialized", "unavailable":
		if status.Portfolio.Snapshot != nil {
			return fmt.Errorf("TRADE portfolio snapshot is present for status %q", status.Portfolio.Status)
		}
	case "ready":
		if status.Portfolio.Snapshot == nil || status.Portfolio.Snapshot.Mode != "SIMULATION" || status.Portfolio.Snapshot.InitialCashJPY != 1_000_000 || status.Portfolio.Snapshot.Guaranteed {
			return fmt.Errorf("TRADE portfolio snapshot violates the 1000000 JPY simulation contract")
		}
	default:
		return fmt.Errorf("TRADE portfolio status is invalid")
	}
	return nil
}

func (request PolicyEvaluationRequest) Validate() error {
	if request.ContractVersion != PolicyEvaluationContractVersion {
		return fmt.Errorf("unsupported TRADE policy evaluation contract version %q", request.ContractVersion)
	}
	for name, value := range map[string]string{
		"request_id":                    request.RequestID,
		"capability":                    request.Capability,
		"global_policy.bundle_revision": request.GlobalPolicy.BundleRevision,
		"deployment.revision":           request.Deployment.Revision,
		"request_scope.revision":        request.RequestScope.Revision,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if len(request.RequestID) > 128 {
		return fmt.Errorf("request_id must not exceed 128 bytes")
	}
	if request.GlobalPolicy.ContractRevision != "global-policy/v1" {
		return fmt.Errorf("unsupported Global Policy contract revision %q", request.GlobalPolicy.ContractRevision)
	}
	if !policySHA256Pattern.MatchString(request.GlobalPolicy.ContentSHA256) {
		return fmt.Errorf("global_policy.content_sha256 must be lowercase SHA-256")
	}
	return nil
}

func (response PrivatePolicyEvaluation) Validate(request PolicyEvaluationRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if response.ContractVersion != PrivateContractVersion {
		return fmt.Errorf("unsupported TRADE contract version %q", response.ContractVersion)
	}
	if strings.TrimSpace(response.ServiceStatus) == "" || strings.TrimSpace(response.CorrelationID) == "" {
		return fmt.Errorf("TRADE policy evaluation envelope is incomplete")
	}
	if response.ExecutionMode != "DISABLED" {
		return fmt.Errorf("TRADE execution mode is not DISABLED")
	}
	decision := response.Decision
	if decision.Capability != request.Capability || decision.GlobalBundleRevision != request.GlobalPolicy.BundleRevision || decision.DeploymentRevision != request.Deployment.Revision || decision.RequestScopeRevision != request.RequestScope.Revision {
		return fmt.Errorf("TRADE policy evaluation revisions do not match the request")
	}
	for name, value := range map[string]string{
		"reason_code":              decision.ReasonCode,
		"reason":                   decision.Reason,
		"binary_contract_revision": decision.BinaryContractRevision,
		"module_policy_revision":   decision.ModulePolicyRevision,
		"policy_id":                decision.PolicyID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("TRADE policy decision %s is required", name)
		}
	}
	if decision.Status != "allowed" && decision.Status != "blocked" {
		return fmt.Errorf("TRADE policy decision status is invalid")
	}
	validReasonCode := map[string]bool{
		"UNKNOWN_CAPABILITY":        true,
		"BINARY_HARD_LIMIT_BLOCKED": true,
		"MODULE_POLICY_BLOCKED":     true,
		"GLOBAL_POLICY_BLOCKED":     true,
		"DEPLOYMENT_BLOCKED":        true,
		"REQUEST_SCOPE_BLOCKED":     true,
		"ALL_POLICY_LAYERS_ALLOWED": true,
	}
	if !validReasonCode[decision.ReasonCode] {
		return fmt.Errorf("TRADE policy decision reason code is invalid")
	}
	if (decision.Status == "allowed") != (decision.ReasonCode == "ALL_POLICY_LAYERS_ALLOWED") {
		return fmt.Errorf("TRADE policy decision status and reason code are inconsistent")
	}
	if decision.Status == "allowed" {
		switch decision.Capability {
		case "broker_network", "knowledge_auto_promotion", "live_order", "paper_order":
			return fmt.Errorf("TRADE binary-blocked capability was reported allowed")
		}
	} else {
		switch decision.Capability {
		case "broker_network", "knowledge_auto_promotion", "live_order", "paper_order":
			if decision.ReasonCode != "BINARY_HARD_LIMIT_BLOCKED" {
				return fmt.Errorf("TRADE binary hard-limit reason is missing")
			}
		}
	}
	return nil
}

func (request RiskPreviewRequest) Validate() error {
	if request.ContractVersion != RiskPreviewRequestContractVersion {
		return fmt.Errorf("unsupported TRADE risk preview request contract version %q", request.ContractVersion)
	}
	if strings.TrimSpace(request.RequestID) == "" || len(request.RequestID) > 128 {
		return fmt.Errorf("risk preview request_id is invalid")
	}
	plan := request.Plan
	if plan.ContractVersion != RiskPreviewPlanContractVersion {
		return fmt.Errorf("unsupported risk preview plan contract version %q", plan.ContractVersion)
	}
	for name, value := range map[string]string{
		"plan_id": plan.PlanID, "policy_revision": plan.PolicyRevision, "as_of": plan.AsOf,
		"selection.instrument_id":        plan.Selection.InstrumentID,
		"proposal.instrument_id":         plan.Proposal.InstrumentID,
		"exit_contract.exit_contract_id": plan.ExitContract.ContractID,
		"exit_contract.instrument_id":    plan.ExitContract.InstrumentID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("risk preview %s is required", name)
		}
	}
	if plan.Selection.InstrumentID != plan.Proposal.InstrumentID || plan.ExitContract.InstrumentID != plan.Proposal.InstrumentID {
		return fmt.Errorf("risk preview instrument IDs do not match")
	}
	return nil
}

func (response PrivateRiskPreview) Validate(request RiskPreviewRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if response.ContractVersion != PrivateContractVersion || strings.TrimSpace(response.ServiceStatus) == "" || strings.TrimSpace(response.CorrelationID) == "" {
		return fmt.Errorf("TRADE risk preview envelope is invalid")
	}
	if response.ExecutionMode != "DISABLED" || response.RequestID != request.RequestID || strings.TrimSpace(response.PortfolioID) == "" || response.PortfolioEventCount < 1 || !eventSHA256Pattern.MatchString(response.PortfolioLatestEventHash) {
		return fmt.Errorf("TRADE risk preview portfolio evidence is invalid")
	}
	decision := response.Decision
	if decision.ContractVersion != RiskPreviewPlanContractVersion || decision.PlanID != request.Plan.PlanID || decision.PolicyRevision != request.Plan.PolicyRevision || decision.AsOf != request.Plan.AsOf || decision.InstrumentID != request.Plan.Proposal.InstrumentID {
		return fmt.Errorf("TRADE risk preview decision does not match request")
	}
	if decision.AuthorizesExecution || decision.MutatesPortfolio || !policySHA256Pattern.MatchString(decision.InputSnapshotSHA256) {
		return fmt.Errorf("TRADE risk preview safety contract is invalid")
	}
	if decision.Status != "pass" && decision.Status != "block" {
		return fmt.Errorf("TRADE risk preview status is invalid")
	}
	if (decision.Status == "pass") != (len(decision.ReasonCodes) == 0) {
		return fmt.Errorf("TRADE risk preview status and reason codes are inconsistent")
	}
	return nil
}

func (request SimulationCommitRequest) Validate() error {
	if request.ContractVersion != SimulationCommitContractVersion || strings.TrimSpace(request.IdempotencyKey) == "" || request.ExpectedPortfolioEventCount < 1 ||
		!eventSHA256Pattern.MatchString(request.ExpectedPortfolioLatestEventHash) || !policySHA256Pattern.MatchString(request.ExpectedInputSnapshotSHA256) {
		return fmt.Errorf("TRADE simulation commit envelope is invalid")
	}
	preview := RiskPreviewRequest{ContractVersion: RiskPreviewRequestContractVersion, RequestID: request.RequestID, Plan: request.Plan}
	if err := preview.Validate(); err != nil {
		return err
	}
	if err := request.Policy.Validate(); err != nil {
		return err
	}
	if request.Policy.RequestID != request.RequestID || request.Policy.Capability != "portfolio_simulation_commit" ||
		request.Policy.GlobalPolicy.BundleRevision != request.Plan.PolicyRevision || request.Policy.RequestScope.Revision != "simulation-commit/sha256:"+request.ExpectedInputSnapshotSHA256 {
		return fmt.Errorf("TRADE simulation commit policy binding is invalid")
	}
	return nil
}

func (response PrivateSimulationCommit) Validate(request SimulationCommitRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if response.ContractVersion != PrivateContractVersion || response.ExecutionMode != "DISABLED" || response.RequestID != request.RequestID ||
		response.PortfolioID == "" || response.Mode != "SIMULATION" || response.AuthorizesExternalExecution || response.PreviousPortfolioEventCount != request.ExpectedPortfolioEventCount ||
		response.PreviousPortfolioLatestHash != request.ExpectedPortfolioLatestEventHash || response.Snapshot.PortfolioID != response.PortfolioID || response.Snapshot.Mode != "SIMULATION" || response.Snapshot.Guaranteed {
		return fmt.Errorf("TRADE simulation commit response is invalid")
	}
	if response.IdempotentReplay == response.PortfolioMutated || (!response.IdempotentReplay && response.RiskDecision == nil) {
		return fmt.Errorf("TRADE simulation commit mutation evidence is invalid")
	}
	if response.PolicyDecision.Capability != "portfolio_simulation_commit" || response.PolicyDecision.Status != "allowed" {
		return fmt.Errorf("TRADE simulation commit policy decision is invalid")
	}
	return nil
}

func (input ShadowObservationInput) Validate() error {
	for name, value := range map[string]string{
		"idempotency_key": input.IdempotencyKey, "study_id": input.StudyID, "decision_id": input.DecisionID,
		"actor_id": input.ActorID, "instrument_id": input.InstrumentID,
	} {
		if strings.TrimSpace(value) == "" || len(value) > 128 {
			return fmt.Errorf("shadow observation %s is invalid", name)
		}
	}
	switch input.DecisionKind {
	case "select", "exclude", "abstain", "hold", "exit":
	default:
		return fmt.Errorf("shadow observation decision_kind is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, input.MarketObservedAt); err != nil {
		return fmt.Errorf("shadow observation market_observed_at is invalid")
	}
	if !policySHA256Pattern.MatchString(input.ContextSnapshotSHA256) || !policySHA256Pattern.MatchString(input.OutcomeLabelContractSHA256) {
		return fmt.Errorf("shadow observation hashes are invalid")
	}
	if len(input.ReasonCodes) == 0 || len(input.EvidenceRefs) == 0 {
		return fmt.Errorf("shadow observation reasons and evidence are required")
	}
	return nil
}

func (request ShadowObservationRequest) Validate() error {
	if request.ContractVersion != ShadowObservationContractVersion || strings.TrimSpace(request.RequestID) == "" {
		return fmt.Errorf("TRADE shadow observation envelope is invalid")
	}
	if err := request.Observation.Validate(); err != nil {
		return err
	}
	if err := request.Policy.Validate(); err != nil {
		return err
	}
	if request.Policy.RequestID != request.RequestID || request.Policy.Capability != "shadow_observation_record" ||
		request.Policy.RequestScope.Revision != "shadow-observation/sha256:"+request.Observation.ContextSnapshotSHA256 {
		return fmt.Errorf("TRADE shadow observation policy binding is invalid")
	}
	return nil
}

func (response PrivateShadowObservation) Validate(request ShadowObservationRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if response.ContractVersion != PrivateContractVersion || response.ExecutionMode != "DISABLED" || response.RequestID != request.RequestID ||
		response.Environment != "SHADOW" || response.AuthorizesExternalExecution || response.PortfolioMutated || response.KnowledgePromoted {
		return fmt.Errorf("TRADE shadow observation safety envelope is invalid")
	}
	if response.PolicyDecision.Capability != "shadow_observation_record" || response.PolicyDecision.Status != "allowed" {
		return fmt.Errorf("TRADE shadow observation policy decision is invalid")
	}
	event := response.Event
	if event.EventVersion != 1 || event.Sequence < 1 || event.Type != "shadow_observation_recorded" || !reflect.DeepEqual(event.ShadowObservationInput, request.Observation) ||
		!eventSHA256Pattern.MatchString(event.EventHash) || event.EventID != "shadow-event/"+event.EventHash {
		return fmt.Errorf("TRADE shadow observation event is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, event.RecordedAt); err != nil {
		return fmt.Errorf("TRADE shadow observation recorded_at is invalid")
	}
	return nil
}
