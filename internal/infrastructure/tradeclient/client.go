package tradeclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	toolcontext "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

const maxResponseBytes = 1 << 20

var ownerPathIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}$`)
var shadowOutcomeEventIDPattern = regexp.MustCompile(`^shadow-event/sha256:[0-9a-f]{64}$`)

type ownerCallContract struct {
	purpose   string
	dataScope string
	read      bool
}

var (
	ownerRiskPreviewCall         = ownerCallContract{purpose: "portfolio_memory_read", dataScope: toolcontext.DataScopeInternal, read: true}
	ownerSimulationCommitCall    = ownerCallContract{purpose: "portfolio_memory_write", dataScope: toolcontext.DataScopeInternal}
	ownerShadowObservationCall   = ownerCallContract{purpose: "ledger_memory_write", dataScope: toolcontext.DataScopeInternal}
	ownerShadowOutcomeCall       = ownerCallContract{purpose: "ledger_memory_write", dataScope: toolcontext.DataScopeInternal}
	ownerShadowReviewCall        = ownerCallContract{purpose: "ledger_memory_write", dataScope: toolcontext.DataScopeInternal}
	ownerShadowOutcomeReportCall = ownerCallContract{purpose: "ledger_memory_read", dataScope: toolcontext.DataScopeInternal, read: true}
	ownerShadowReviewReportCall  = ownerCallContract{purpose: "ledger_memory_read", dataScope: toolcontext.DataScopeInternal, read: true}
	ownerSourceRecordCall        = ownerCallContract{purpose: "source_memory_read", dataScope: toolcontext.DataScopeInternal, read: true}
	ownerSourceCollectCall       = ownerCallContract{purpose: "source_memory_write", dataScope: toolcontext.DataScopeInternal}
	ownerLearningCandidateCall   = ownerCallContract{purpose: "learning_memory_read", dataScope: toolcontext.DataScopeInternal, read: true}
	ownerLearningImportCall      = ownerCallContract{purpose: "learning_memory_write", dataScope: toolcontext.DataScopeInternal}
	ownerMarketSnapshotCall      = ownerCallContract{purpose: "market_memory_read", dataScope: toolcontext.DataScopeInternal, read: true}
	ownerMarketImportCall        = ownerCallContract{purpose: "market_memory_write", dataScope: toolcontext.DataScopeInternal}
	ownerReplayDecisionCall      = ownerCallContract{purpose: "replay_memory_read", dataScope: toolcontext.DataScopeInternal, read: true}
	ownerReplayRecordCall        = ownerCallContract{purpose: "replay_memory_write", dataScope: toolcontext.DataScopeInternal}
	ownerPortfolioSnapshotCall   = ownerCallContract{purpose: "portfolio_memory_read", dataScope: toolcontext.DataScopeInternal, read: true}
	ownerPortfolioEnsureCall     = ownerCallContract{purpose: "portfolio_memory_write", dataScope: toolcontext.DataScopeInternal}
)

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

type ServiceError struct {
	StatusCode int
}

func (err *ServiceError) Error() string {
	return fmt.Sprintf("TRADE private API returned HTTP %d", err.StatusCode)
}

func (err *ServiceError) HTTPStatus() int { return err.StatusCode }

func ownerScopeFor(ctx context.Context, call ownerCallContract) (toolcontext.ToolExecutionScope, error) {
	scope, found := toolcontext.ToolExecutionScopeFromContext(ctx)
	if !found {
		return toolcontext.ToolExecutionScope{}, fmt.Errorf("TRADE owner scope is required")
	}
	if err := scope.Validate(); err != nil {
		return toolcontext.ToolExecutionScope{}, fmt.Errorf("validate TRADE owner scope: %w", err)
	}
	if scope.ActorKind != toolcontext.ActorKindAgent || !scope.Allows(toolcontext.DataScopeInternal) {
		return toolcontext.ToolExecutionScope{}, fmt.Errorf("TRADE owner scope must be an internal Agent scope")
	}
	switch {
	case scope.ActorID == "shiro" && scope.AgentRole == "worker":
	case scope.ActorID == "kuro" && scope.AgentRole == "heavy":
	default:
		return toolcontext.ToolExecutionScope{}, fmt.Errorf("TRADE owner Agent and role are not permitted")
	}
	if scope.Purpose != call.purpose {
		return toolcontext.ToolExecutionScope{}, fmt.Errorf("TRADE owner scope purpose is invalid")
	}
	return scope, nil
}

func ownerBodyScope(ctx context.Context, correlationID, requestID string, call ownerCallContract) (toolcontext.ToolExecutionScope, error) {
	scope, err := ownerScopeFor(ctx, call)
	if err != nil {
		return toolcontext.ToolExecutionScope{}, err
	}
	if scope.RequestID != requestID || scope.RequestID != correlationID {
		return toolcontext.ToolExecutionScope{}, fmt.Errorf("TRADE owner request and correlation IDs must match the trusted scope")
	}
	return scope, nil
}

func ownerReportScope(ctx context.Context, correlationID string, call ownerCallContract) (toolcontext.ToolExecutionScope, error) {
	scope, err := ownerScopeFor(ctx, call)
	if err != nil {
		return toolcontext.ToolExecutionScope{}, err
	}
	if scope.RequestID != correlationID {
		return toolcontext.ToolExecutionScope{}, fmt.Errorf("TRADE owner report correlation ID must match the trusted scope")
	}
	return scope, nil
}

func setOwnerHeaders(request *http.Request, scope toolcontext.ToolExecutionScope, call ownerCallContract) {
	request.Header.Set("X-RenCrow-Agent-ID", scope.ActorID)
	request.Header.Set("X-RenCrow-Agent-Role", scope.AgentRole)
	request.Header.Set("X-RenCrow-Request-Purpose", call.purpose)
	request.Header.Set("X-RenCrow-Data-Scope", call.dataScope)
	request.Header.Set("X-Request-ID", scope.RequestID)
	request.Header.Set("X-RenCrow-Request-Time", time.Now().UTC().Format(time.RFC3339Nano))
	if call.read {
		request.Header.Set("X-RenCrow-Result-Limit", "1")
	} else {
		request.Header.Del("X-RenCrow-Result-Limit")
	}
}

func validateOwnerEvidenceIdentity(evidence moduletrade.OwnerEvidence, scope toolcontext.ToolExecutionScope, call ownerCallContract) error {
	if evidence.AgentID != scope.ActorID || evidence.Role != scope.AgentRole || evidence.Purpose != scope.Purpose || evidence.DataScope != call.dataScope {
		return fmt.Errorf("TRADE owner evidence identity does not match the trusted scope")
	}
	return nil
}

func validateOwnerReceiptIdentity(receipt moduletrade.OwnerReceipt, scope toolcontext.ToolExecutionScope, call ownerCallContract) error {
	if receipt.AgentID != scope.ActorID || receipt.Role != scope.AgentRole || receipt.Purpose != scope.Purpose || receipt.DataScope != call.dataScope {
		return fmt.Errorf("TRADE owner receipt identity does not match the trusted scope")
	}
	return nil
}

func (client *Client) newOwnerRequest(ctx context.Context, method, path string, scope toolcontext.ToolExecutionScope, call ownerCallContract, payload []byte) (*http.Request, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+client.token)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("X-Correlation-ID", scope.RequestID)
	setOwnerHeaders(request, scope, call)
	return request, nil
}

func decodeStrictOwnerResponse(response *http.Response, operation string, target any) error {
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return &ServiceError{StatusCode: response.StatusCode}
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read TRADE %s response: %w", operation, err)
	}
	if len(payload) > maxResponseBytes {
		return fmt.Errorf("TRADE %s response exceeds %d bytes", operation, maxResponseBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode TRADE %s response: %w", operation, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("decode TRADE %s response: trailing JSON value", operation)
	}
	return nil
}

func encodeOwnerRequest(input any, operation string) ([]byte, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("encode TRADE %s request: %w", operation, err)
	}
	return payload, nil
}

func validateOwnerPathID(value, name string) error {
	if !ownerPathIDPattern.MatchString(value) || strings.ContainsAny(value, `/\\`) {
		return fmt.Errorf("TRADE %s is invalid", name)
	}
	return nil
}

func validateShadowOutcomeReportQuery(queryRef string) (bool, error) {
	if shadowOutcomeEventIDPattern.MatchString(queryRef) {
		return true, nil
	}
	if err := validateOwnerPathID(queryRef, "Shadow outcome report query"); err != nil {
		return false, err
	}
	return false, nil
}

func NewClient(baseURL, tokenFile string, timeout time.Duration) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || parsed.Scheme != "http" {
		return nil, fmt.Errorf("TRADE base URL must be an absolute HTTP URL")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("TRADE timeout must be positive")
	}
	token, err := readTokenFile(tokenFile)
	if err != nil {
		return nil, fmt.Errorf("read TRADE control token: %w", err)
	}
	return &Client{baseURL: baseURL, token: token, httpClient: &http.Client{Timeout: timeout}}, nil
}

func (client *Client) Status(ctx context.Context, correlationID string) (moduletrade.PrivateStatus, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+"/v1/status", nil)
	if err != nil {
		return moduletrade.PrivateStatus{}, fmt.Errorf("create TRADE status request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+client.token)
	if strings.TrimSpace(correlationID) != "" {
		request.Header.Set("X-Correlation-ID", correlationID)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return moduletrade.PrivateStatus{}, fmt.Errorf("TRADE status request failed: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return moduletrade.PrivateStatus{}, fmt.Errorf("read TRADE status response: %w", err)
	}
	if len(payload) > maxResponseBytes {
		return moduletrade.PrivateStatus{}, fmt.Errorf("TRADE status response exceeds %d bytes", maxResponseBytes)
	}
	if response.StatusCode != http.StatusOK {
		return moduletrade.PrivateStatus{}, &ServiceError{StatusCode: response.StatusCode}
	}
	var status moduletrade.PrivateStatus
	if err := json.Unmarshal(payload, &status); err != nil {
		return moduletrade.PrivateStatus{}, fmt.Errorf("decode TRADE status response: %w", err)
	}
	if err := status.ValidateDisabledFoundation(); err != nil {
		return moduletrade.PrivateStatus{}, fmt.Errorf("validate TRADE status response: %w", err)
	}
	return status, nil
}

func (client *Client) Evaluate(ctx context.Context, correlationID string, input moduletrade.PolicyEvaluationRequest) (moduletrade.PrivatePolicyEvaluation, error) {
	if err := input.Validate(); err != nil {
		return moduletrade.PrivatePolicyEvaluation{}, fmt.Errorf("validate TRADE policy evaluation request: %w", err)
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return moduletrade.PrivatePolicyEvaluation{}, fmt.Errorf("encode TRADE policy evaluation request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/v1/policy/evaluate", bytes.NewReader(payload))
	if err != nil {
		return moduletrade.PrivatePolicyEvaluation{}, fmt.Errorf("create TRADE policy evaluation request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+client.token)
	if strings.TrimSpace(correlationID) != "" {
		request.Header.Set("X-Correlation-ID", correlationID)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return moduletrade.PrivatePolicyEvaluation{}, fmt.Errorf("TRADE policy evaluation request failed: %w", err)
	}
	defer response.Body.Close()
	responsePayload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return moduletrade.PrivatePolicyEvaluation{}, fmt.Errorf("read TRADE policy evaluation response: %w", err)
	}
	if len(responsePayload) > maxResponseBytes {
		return moduletrade.PrivatePolicyEvaluation{}, fmt.Errorf("TRADE policy evaluation response exceeds %d bytes", maxResponseBytes)
	}
	if response.StatusCode != http.StatusOK {
		return moduletrade.PrivatePolicyEvaluation{}, &ServiceError{StatusCode: response.StatusCode}
	}
	var result moduletrade.PrivatePolicyEvaluation
	if err := json.Unmarshal(responsePayload, &result); err != nil {
		return moduletrade.PrivatePolicyEvaluation{}, fmt.Errorf("decode TRADE policy evaluation response: %w", err)
	}
	if err := result.Validate(input); err != nil {
		return moduletrade.PrivatePolicyEvaluation{}, fmt.Errorf("validate TRADE policy evaluation response: %w", err)
	}
	return result, nil
}

func (client *Client) PreviewRisk(ctx context.Context, correlationID string, input moduletrade.RiskPreviewRequest) (moduletrade.PrivateRiskPreview, error) {
	scope, err := ownerBodyScope(ctx, correlationID, input.RequestID, ownerRiskPreviewCall)
	if err != nil {
		return moduletrade.PrivateRiskPreview{}, err
	}
	if err := input.Validate(); err != nil {
		return moduletrade.PrivateRiskPreview{}, fmt.Errorf("validate TRADE risk preview request: %w", err)
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return moduletrade.PrivateRiskPreview{}, fmt.Errorf("encode TRADE risk preview request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/v1/portfolio/risk-preview", bytes.NewReader(payload))
	if err != nil {
		return moduletrade.PrivateRiskPreview{}, fmt.Errorf("create TRADE risk preview request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("X-Correlation-ID", correlationID)
	setOwnerHeaders(request, scope, ownerRiskPreviewCall)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return moduletrade.PrivateRiskPreview{}, fmt.Errorf("TRADE risk preview request failed: %w", err)
	}
	defer response.Body.Close()
	responsePayload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return moduletrade.PrivateRiskPreview{}, fmt.Errorf("read TRADE risk preview response: %w", err)
	}
	if len(responsePayload) > maxResponseBytes {
		return moduletrade.PrivateRiskPreview{}, fmt.Errorf("TRADE risk preview response exceeds %d bytes", maxResponseBytes)
	}
	if response.StatusCode != http.StatusOK {
		return moduletrade.PrivateRiskPreview{}, &ServiceError{StatusCode: response.StatusCode}
	}
	decoder := json.NewDecoder(bytes.NewReader(responsePayload))
	decoder.DisallowUnknownFields()
	var result moduletrade.PrivateRiskPreview
	if err := decoder.Decode(&result); err != nil {
		return moduletrade.PrivateRiskPreview{}, fmt.Errorf("decode TRADE risk preview response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return moduletrade.PrivateRiskPreview{}, fmt.Errorf("decode TRADE risk preview response: trailing JSON value")
	}
	if err := result.Validate(input); err != nil {
		return moduletrade.PrivateRiskPreview{}, fmt.Errorf("validate TRADE risk preview response: %w", err)
	}
	if err := validateOwnerEvidenceIdentity(result.OwnerEvidence, scope, ownerRiskPreviewCall); err != nil {
		return moduletrade.PrivateRiskPreview{}, err
	}
	return result, nil
}

func (client *Client) CommitSimulation(ctx context.Context, correlationID string, input moduletrade.SimulationCommitRequest) (moduletrade.PrivateSimulationCommit, error) {
	scope, err := ownerBodyScope(ctx, correlationID, input.RequestID, ownerSimulationCommitCall)
	if err != nil {
		return moduletrade.PrivateSimulationCommit{}, err
	}
	if err := input.Validate(); err != nil {
		return moduletrade.PrivateSimulationCommit{}, fmt.Errorf("validate TRADE simulation commit request: %w", err)
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return moduletrade.PrivateSimulationCommit{}, fmt.Errorf("encode TRADE simulation commit request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/v1/portfolio/simulation-commit", bytes.NewReader(payload))
	if err != nil {
		return moduletrade.PrivateSimulationCommit{}, fmt.Errorf("create TRADE simulation commit request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("X-Correlation-ID", correlationID)
	setOwnerHeaders(request, scope, ownerSimulationCommitCall)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return moduletrade.PrivateSimulationCommit{}, fmt.Errorf("TRADE simulation commit request failed: %w", err)
	}
	defer response.Body.Close()
	responsePayload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(responsePayload) > maxResponseBytes {
		return moduletrade.PrivateSimulationCommit{}, fmt.Errorf("read TRADE simulation commit response")
	}
	if response.StatusCode != http.StatusOK {
		return moduletrade.PrivateSimulationCommit{}, &ServiceError{StatusCode: response.StatusCode}
	}
	decoder := json.NewDecoder(bytes.NewReader(responsePayload))
	decoder.DisallowUnknownFields()
	var result moduletrade.PrivateSimulationCommit
	if err := decoder.Decode(&result); err != nil {
		return moduletrade.PrivateSimulationCommit{}, fmt.Errorf("decode TRADE simulation commit response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return moduletrade.PrivateSimulationCommit{}, fmt.Errorf("decode TRADE simulation commit response: trailing JSON value")
	}
	if err := result.Validate(input); err != nil {
		return moduletrade.PrivateSimulationCommit{}, fmt.Errorf("validate TRADE simulation commit response: %w", err)
	}
	if err := validateOwnerReceiptIdentity(result.OwnerReceipt, scope, ownerSimulationCommitCall); err != nil {
		return moduletrade.PrivateSimulationCommit{}, err
	}
	return result, nil
}

func (client *Client) RecordShadowObservation(ctx context.Context, correlationID string, input moduletrade.ShadowObservationRequest) (moduletrade.PrivateShadowObservation, error) {
	scope, err := ownerBodyScope(ctx, correlationID, input.RequestID, ownerShadowObservationCall)
	if err != nil {
		return moduletrade.PrivateShadowObservation{}, err
	}
	if err := input.Validate(); err != nil {
		return moduletrade.PrivateShadowObservation{}, fmt.Errorf("validate TRADE Shadow observation request: %w", err)
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return moduletrade.PrivateShadowObservation{}, fmt.Errorf("encode TRADE Shadow observation request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/v1/shadow/observations", bytes.NewReader(payload))
	if err != nil {
		return moduletrade.PrivateShadowObservation{}, fmt.Errorf("create TRADE Shadow observation request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("X-Correlation-ID", correlationID)
	setOwnerHeaders(request, scope, ownerShadowObservationCall)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return moduletrade.PrivateShadowObservation{}, fmt.Errorf("TRADE Shadow observation request failed: %w", err)
	}
	defer response.Body.Close()
	responsePayload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(responsePayload) > maxResponseBytes {
		return moduletrade.PrivateShadowObservation{}, fmt.Errorf("read TRADE Shadow observation response")
	}
	if response.StatusCode != http.StatusOK {
		return moduletrade.PrivateShadowObservation{}, &ServiceError{StatusCode: response.StatusCode}
	}
	decoder := json.NewDecoder(bytes.NewReader(responsePayload))
	decoder.DisallowUnknownFields()
	var result moduletrade.PrivateShadowObservation
	if err := decoder.Decode(&result); err != nil {
		return moduletrade.PrivateShadowObservation{}, fmt.Errorf("decode TRADE Shadow observation response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return moduletrade.PrivateShadowObservation{}, fmt.Errorf("decode TRADE Shadow observation response: trailing JSON value")
	}
	if err := result.Validate(input); err != nil {
		return moduletrade.PrivateShadowObservation{}, fmt.Errorf("validate TRADE Shadow observation response: %w", err)
	}
	if err := validateOwnerReceiptIdentity(result.OwnerReceipt, scope, ownerShadowObservationCall); err != nil {
		return moduletrade.PrivateShadowObservation{}, err
	}
	return result, nil
}

func (client *Client) RecordShadowOutcome(ctx context.Context, correlationID string, input moduletrade.ShadowOutcomeRequest) (moduletrade.PrivateShadowOutcome, error) {
	scope, err := ownerBodyScope(ctx, correlationID, input.RequestID, ownerShadowOutcomeCall)
	if err != nil {
		return moduletrade.PrivateShadowOutcome{}, err
	}
	if err := input.Validate(); err != nil {
		return moduletrade.PrivateShadowOutcome{}, fmt.Errorf("validate TRADE Shadow outcome request: %w", err)
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return moduletrade.PrivateShadowOutcome{}, fmt.Errorf("encode TRADE Shadow outcome request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/v1/shadow/outcomes", bytes.NewReader(payload))
	if err != nil {
		return moduletrade.PrivateShadowOutcome{}, fmt.Errorf("create TRADE Shadow outcome request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("X-Correlation-ID", correlationID)
	setOwnerHeaders(request, scope, ownerShadowOutcomeCall)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return moduletrade.PrivateShadowOutcome{}, fmt.Errorf("TRADE Shadow outcome request failed: %w", err)
	}
	defer response.Body.Close()
	responsePayload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(responsePayload) > maxResponseBytes {
		return moduletrade.PrivateShadowOutcome{}, fmt.Errorf("read TRADE Shadow outcome response")
	}
	if response.StatusCode != http.StatusOK {
		return moduletrade.PrivateShadowOutcome{}, &ServiceError{StatusCode: response.StatusCode}
	}
	decoder := json.NewDecoder(bytes.NewReader(responsePayload))
	decoder.DisallowUnknownFields()
	var result moduletrade.PrivateShadowOutcome
	if err := decoder.Decode(&result); err != nil {
		return moduletrade.PrivateShadowOutcome{}, fmt.Errorf("decode TRADE Shadow outcome response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return moduletrade.PrivateShadowOutcome{}, fmt.Errorf("decode TRADE Shadow outcome response: trailing JSON value")
	}
	if err := result.Validate(input); err != nil {
		return moduletrade.PrivateShadowOutcome{}, fmt.Errorf("validate TRADE Shadow outcome response: %w", err)
	}
	if err := validateOwnerReceiptIdentity(result.OwnerReceipt, scope, ownerShadowOutcomeCall); err != nil {
		return moduletrade.PrivateShadowOutcome{}, err
	}
	return result, nil
}

func (client *Client) RecordShadowReview(ctx context.Context, correlationID string, input moduletrade.ShadowReviewRequest) (moduletrade.PrivateShadowReview, error) {
	scope, err := ownerBodyScope(ctx, correlationID, input.RequestID, ownerShadowReviewCall)
	if err != nil {
		return moduletrade.PrivateShadowReview{}, err
	}
	if err := input.Validate(); err != nil {
		return moduletrade.PrivateShadowReview{}, fmt.Errorf("validate TRADE Shadow review request: %w", err)
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return moduletrade.PrivateShadowReview{}, fmt.Errorf("encode TRADE Shadow review request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+"/v1/shadow/outcomes/reviews", bytes.NewReader(payload))
	if err != nil {
		return moduletrade.PrivateShadowReview{}, fmt.Errorf("create TRADE Shadow review request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("X-Correlation-ID", correlationID)
	setOwnerHeaders(request, scope, ownerShadowReviewCall)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return moduletrade.PrivateShadowReview{}, fmt.Errorf("TRADE Shadow review request failed: %w", err)
	}
	defer response.Body.Close()
	responsePayload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(responsePayload) > maxResponseBytes {
		return moduletrade.PrivateShadowReview{}, fmt.Errorf("read TRADE Shadow review response")
	}
	if response.StatusCode != http.StatusOK {
		return moduletrade.PrivateShadowReview{}, &ServiceError{StatusCode: response.StatusCode}
	}
	decoder := json.NewDecoder(bytes.NewReader(responsePayload))
	decoder.DisallowUnknownFields()
	var result moduletrade.PrivateShadowReview
	if err := decoder.Decode(&result); err != nil {
		return moduletrade.PrivateShadowReview{}, fmt.Errorf("decode TRADE Shadow review response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return moduletrade.PrivateShadowReview{}, fmt.Errorf("decode TRADE Shadow review response: trailing JSON value")
	}
	if err := result.Validate(input); err != nil {
		return moduletrade.PrivateShadowReview{}, fmt.Errorf("validate TRADE Shadow review response: %w", err)
	}
	if err := validateOwnerReceiptIdentity(result.OwnerReceipt, scope, ownerShadowReviewCall); err != nil {
		return moduletrade.PrivateShadowReview{}, err
	}
	return result, nil
}

func (client *Client) ShadowOutcomeReport(ctx context.Context, correlationID, queryRef string) (moduletrade.PrivateShadowOutcomeReport, error) {
	scope, err := ownerReportScope(ctx, correlationID, ownerShadowOutcomeReportCall)
	if err != nil {
		return moduletrade.PrivateShadowOutcomeReport{}, err
	}
	isEventQuery, err := validateShadowOutcomeReportQuery(queryRef)
	if err != nil {
		return moduletrade.PrivateShadowOutcomeReport{}, err
	}
	queryName := "study_id"
	if isEventQuery {
		queryName = "event_id"
	}
	requestURL := client.baseURL + "/v1/shadow/outcomes/report?" + queryName + "=" + url.QueryEscape(queryRef)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return moduletrade.PrivateShadowOutcomeReport{}, fmt.Errorf("create TRADE Shadow outcome report request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("X-Correlation-ID", correlationID)
	setOwnerHeaders(request, scope, ownerShadowOutcomeReportCall)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return moduletrade.PrivateShadowOutcomeReport{}, fmt.Errorf("TRADE Shadow outcome report request failed: %w", err)
	}
	defer response.Body.Close()
	responsePayload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(responsePayload) > maxResponseBytes {
		return moduletrade.PrivateShadowOutcomeReport{}, fmt.Errorf("read TRADE Shadow outcome report response")
	}
	if response.StatusCode != http.StatusOK {
		return moduletrade.PrivateShadowOutcomeReport{}, &ServiceError{StatusCode: response.StatusCode}
	}
	decoder := json.NewDecoder(bytes.NewReader(responsePayload))
	decoder.DisallowUnknownFields()
	var result moduletrade.PrivateShadowOutcomeReport
	if err := decoder.Decode(&result); err != nil {
		return moduletrade.PrivateShadowOutcomeReport{}, fmt.Errorf("decode TRADE Shadow outcome report response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return moduletrade.PrivateShadowOutcomeReport{}, fmt.Errorf("decode TRADE Shadow outcome report response: trailing JSON value")
	}
	validationStudyID := queryRef
	if isEventQuery {
		if err := validateOwnerPathID(result.Report.StudyID, "Shadow outcome report study ID"); err != nil {
			return moduletrade.PrivateShadowOutcomeReport{}, err
		}
		validationStudyID = result.Report.StudyID
	}
	if err := result.Validate(validationStudyID, scope.RequestID); err != nil {
		return moduletrade.PrivateShadowOutcomeReport{}, fmt.Errorf("validate TRADE Shadow outcome report response: %w", err)
	}
	if isEventQuery && result.OwnerEvidence.ProvenanceRef != queryRef {
		return moduletrade.PrivateShadowOutcomeReport{}, fmt.Errorf("TRADE Shadow outcome report provenance does not match the exact event query")
	}
	if err := validateOwnerEvidenceIdentity(result.OwnerEvidence, scope, ownerShadowOutcomeReportCall); err != nil {
		return moduletrade.PrivateShadowOutcomeReport{}, err
	}
	return result, nil
}

func (client *Client) ShadowReviewReport(ctx context.Context, correlationID, studyID string) (moduletrade.PrivateShadowReviewReport, error) {
	scope, err := ownerReportScope(ctx, correlationID, ownerShadowReviewReportCall)
	if err != nil {
		return moduletrade.PrivateShadowReviewReport{}, err
	}
	if strings.TrimSpace(studyID) == "" {
		return moduletrade.PrivateShadowReviewReport{}, fmt.Errorf("TRADE Shadow review report study ID is required")
	}
	requestURL := client.baseURL + "/v1/shadow/outcomes/reviews/report?study_id=" + url.QueryEscape(studyID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return moduletrade.PrivateShadowReviewReport{}, fmt.Errorf("create TRADE Shadow review report request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+client.token)
	request.Header.Set("X-Correlation-ID", correlationID)
	setOwnerHeaders(request, scope, ownerShadowReviewReportCall)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return moduletrade.PrivateShadowReviewReport{}, fmt.Errorf("TRADE Shadow review report request failed: %w", err)
	}
	defer response.Body.Close()
	responsePayload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(responsePayload) > maxResponseBytes {
		return moduletrade.PrivateShadowReviewReport{}, fmt.Errorf("read TRADE Shadow review report response")
	}
	if response.StatusCode != http.StatusOK {
		return moduletrade.PrivateShadowReviewReport{}, &ServiceError{StatusCode: response.StatusCode}
	}
	decoder := json.NewDecoder(bytes.NewReader(responsePayload))
	decoder.DisallowUnknownFields()
	var result moduletrade.PrivateShadowReviewReport
	if err := decoder.Decode(&result); err != nil {
		return moduletrade.PrivateShadowReviewReport{}, fmt.Errorf("decode TRADE Shadow review report response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return moduletrade.PrivateShadowReviewReport{}, fmt.Errorf("decode TRADE Shadow review report response: trailing JSON value")
	}
	if err := result.Validate(studyID, scope.RequestID); err != nil {
		return moduletrade.PrivateShadowReviewReport{}, fmt.Errorf("validate TRADE Shadow review report response: %w", err)
	}
	if err := validateOwnerEvidenceIdentity(result.OwnerEvidence, scope, ownerShadowReviewReportCall); err != nil {
		return moduletrade.PrivateShadowReviewReport{}, err
	}
	return result, nil
}

// ReadPortfolioSnapshot reads the single TRADE-owned simulation portfolio
// snapshot for the authenticated Agent scope.
func (client *Client) ReadPortfolioSnapshot(ctx context.Context) (moduletrade.PortfolioSnapshotReadResponse, error) {
	scope, err := ownerScopeFor(ctx, ownerPortfolioSnapshotCall)
	if err != nil {
		return moduletrade.PortfolioSnapshotReadResponse{}, err
	}
	request, err := client.newOwnerRequest(ctx, http.MethodGet, "/v1/memory/portfolio/snapshot", scope, ownerPortfolioSnapshotCall, nil)
	if err != nil {
		return moduletrade.PortfolioSnapshotReadResponse{}, fmt.Errorf("create TRADE portfolio snapshot request: %w", err)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return moduletrade.PortfolioSnapshotReadResponse{}, fmt.Errorf("TRADE portfolio snapshot request failed: %w", err)
	}
	var result moduletrade.PortfolioSnapshotReadResponse
	if err := decodeStrictOwnerResponse(response, "portfolio snapshot", &result); err != nil {
		return moduletrade.PortfolioSnapshotReadResponse{}, err
	}
	if err := result.Validate(scope.RequestID, scope.RequestID); err != nil {
		return moduletrade.PortfolioSnapshotReadResponse{}, fmt.Errorf("validate TRADE portfolio snapshot response: %w", err)
	}
	if err := validateOwnerEvidenceIdentity(result.OwnerEvidence, scope, ownerPortfolioSnapshotCall); err != nil {
		return moduletrade.PortfolioSnapshotReadResponse{}, err
	}
	return result, nil
}

// EnsurePortfolioInitialized asks TRADE to idempotently initialize its
// simulation portfolio. The request identity comes only from the trusted
// ToolExecutionScope.
func (client *Client) EnsurePortfolioInitialized(ctx context.Context) (moduletrade.PortfolioSnapshotWriteResponse, error) {
	scope, err := ownerScopeFor(ctx, ownerPortfolioEnsureCall)
	if err != nil {
		return moduletrade.PortfolioSnapshotWriteResponse{}, err
	}
	input := moduletrade.EnsurePortfolioInitializedRequest{
		ContractVersion: moduletrade.MemoryOwnerContractVersion,
		RequestID:       scope.RequestID,
	}
	if err := input.Validate(); err != nil {
		return moduletrade.PortfolioSnapshotWriteResponse{}, fmt.Errorf("validate TRADE portfolio initialization request: %w", err)
	}
	payload, err := encodeOwnerRequest(input, "portfolio initialization")
	if err != nil {
		return moduletrade.PortfolioSnapshotWriteResponse{}, err
	}
	request, err := client.newOwnerRequest(ctx, http.MethodPost, "/v1/memory/portfolio/ensure-initialized", scope, ownerPortfolioEnsureCall, payload)
	if err != nil {
		return moduletrade.PortfolioSnapshotWriteResponse{}, fmt.Errorf("create TRADE portfolio initialization request: %w", err)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return moduletrade.PortfolioSnapshotWriteResponse{}, fmt.Errorf("TRADE portfolio initialization request failed: %w", err)
	}
	var result moduletrade.PortfolioSnapshotWriteResponse
	if err := decodeStrictOwnerResponse(response, "portfolio initialization", &result); err != nil {
		return moduletrade.PortfolioSnapshotWriteResponse{}, err
	}
	if err := result.Validate(scope.RequestID); err != nil {
		return moduletrade.PortfolioSnapshotWriteResponse{}, fmt.Errorf("validate TRADE portfolio initialization response: %w", err)
	}
	if err := validateOwnerReceiptIdentity(result.OwnerReceipt, scope, ownerPortfolioEnsureCall); err != nil {
		return moduletrade.PortfolioSnapshotWriteResponse{}, err
	}
	return result, nil
}

// ReadSourceRecord reads exactly one bounded source projection from TRADE.
func (client *Client) ReadSourceRecord(ctx context.Context, recordID string) (moduletrade.SourceRecordReadResponse, error) {
	scope, err := ownerScopeFor(ctx, ownerSourceRecordCall)
	if err != nil {
		return moduletrade.SourceRecordReadResponse{}, err
	}
	if err := validateOwnerPathID(recordID, "source record ID"); err != nil {
		return moduletrade.SourceRecordReadResponse{}, err
	}
	request, err := client.newOwnerRequest(ctx, http.MethodGet, "/v1/memory/source/records/"+url.PathEscape(recordID), scope, ownerSourceRecordCall, nil)
	if err != nil {
		return moduletrade.SourceRecordReadResponse{}, fmt.Errorf("create TRADE source record request: %w", err)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return moduletrade.SourceRecordReadResponse{}, fmt.Errorf("TRADE source record request failed: %w", err)
	}
	var result moduletrade.SourceRecordReadResponse
	if err := decodeStrictOwnerResponse(response, "source record", &result); err != nil {
		return moduletrade.SourceRecordReadResponse{}, err
	}
	if err := result.Validate(scope.RequestID, scope.RequestID); err != nil {
		return moduletrade.SourceRecordReadResponse{}, fmt.Errorf("validate TRADE source record response: %w", err)
	}
	if result.Record.SourceRecordID != recordID {
		return moduletrade.SourceRecordReadResponse{}, fmt.Errorf("TRADE source record response ID does not match the requested ID")
	}
	if err := validateOwnerEvidenceIdentity(result.OwnerEvidence, scope, ownerSourceRecordCall); err != nil {
		return moduletrade.SourceRecordReadResponse{}, err
	}
	return result, nil
}

// CollectSource invokes TRADE's validated source collection workflow. The
// request ID is always taken from the trusted execution scope.
func (client *Client) CollectSource(ctx context.Context, sourceDefinitionID string) (moduletrade.SourceRecordWriteResponse, error) {
	scope, err := ownerScopeFor(ctx, ownerSourceCollectCall)
	if err != nil {
		return moduletrade.SourceRecordWriteResponse{}, err
	}
	input := moduletrade.CollectSourceRequest{
		ContractVersion:    moduletrade.MemoryOwnerContractVersion,
		RequestID:          scope.RequestID,
		SourceDefinitionID: sourceDefinitionID,
	}
	if err := input.Validate(); err != nil {
		return moduletrade.SourceRecordWriteResponse{}, fmt.Errorf("validate TRADE source collect request: %w", err)
	}
	payload, err := encodeOwnerRequest(input, "source collect")
	if err != nil {
		return moduletrade.SourceRecordWriteResponse{}, err
	}
	request, err := client.newOwnerRequest(ctx, http.MethodPost, "/v1/memory/source/collect", scope, ownerSourceCollectCall, payload)
	if err != nil {
		return moduletrade.SourceRecordWriteResponse{}, fmt.Errorf("create TRADE source collect request: %w", err)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return moduletrade.SourceRecordWriteResponse{}, fmt.Errorf("TRADE source collect request failed: %w", err)
	}
	var result moduletrade.SourceRecordWriteResponse
	if err := decodeStrictOwnerResponse(response, "source collect", &result); err != nil {
		return moduletrade.SourceRecordWriteResponse{}, err
	}
	if err := result.Validate(scope.RequestID); err != nil {
		return moduletrade.SourceRecordWriteResponse{}, fmt.Errorf("validate TRADE source collect response: %w", err)
	}
	if err := validateOwnerReceiptIdentity(result.OwnerReceipt, scope, ownerSourceCollectCall); err != nil {
		return moduletrade.SourceRecordWriteResponse{}, err
	}
	return result, nil
}

// ReadLearningCandidate reads exactly one bounded learning candidate.
func (client *Client) ReadLearningCandidate(ctx context.Context, candidateRecordID string) (moduletrade.LearningCandidateReadResponse, error) {
	scope, err := ownerScopeFor(ctx, ownerLearningCandidateCall)
	if err != nil {
		return moduletrade.LearningCandidateReadResponse{}, err
	}
	if err := validateOwnerPathID(candidateRecordID, "learning candidate ID"); err != nil {
		return moduletrade.LearningCandidateReadResponse{}, err
	}
	request, err := client.newOwnerRequest(ctx, http.MethodGet, "/v1/memory/learning/candidates/"+url.PathEscape(candidateRecordID), scope, ownerLearningCandidateCall, nil)
	if err != nil {
		return moduletrade.LearningCandidateReadResponse{}, fmt.Errorf("create TRADE learning candidate request: %w", err)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return moduletrade.LearningCandidateReadResponse{}, fmt.Errorf("TRADE learning candidate request failed: %w", err)
	}
	var result moduletrade.LearningCandidateReadResponse
	if err := decodeStrictOwnerResponse(response, "learning candidate", &result); err != nil {
		return moduletrade.LearningCandidateReadResponse{}, err
	}
	if err := result.Validate(scope.RequestID, scope.RequestID); err != nil {
		return moduletrade.LearningCandidateReadResponse{}, fmt.Errorf("validate TRADE learning candidate response: %w", err)
	}
	if result.Record.CandidateRecordID != candidateRecordID {
		return moduletrade.LearningCandidateReadResponse{}, fmt.Errorf("TRADE learning candidate response ID does not match the requested ID")
	}
	if err := validateOwnerEvidenceIdentity(result.OwnerEvidence, scope, ownerLearningCandidateCall); err != nil {
		return moduletrade.LearningCandidateReadResponse{}, err
	}
	return result, nil
}

// ImportLearningCandidate invokes TRADE's validated candidate import
// workflow. The request ID is always taken from the trusted execution scope.
func (client *Client) ImportLearningCandidate(ctx context.Context, candidateDefinitionID string) (moduletrade.LearningCandidateWriteResponse, error) {
	scope, err := ownerScopeFor(ctx, ownerLearningImportCall)
	if err != nil {
		return moduletrade.LearningCandidateWriteResponse{}, err
	}
	input := moduletrade.ImportLearningCandidateRequest{
		ContractVersion:       moduletrade.MemoryOwnerContractVersion,
		RequestID:             scope.RequestID,
		CandidateDefinitionID: candidateDefinitionID,
	}
	if err := input.Validate(); err != nil {
		return moduletrade.LearningCandidateWriteResponse{}, fmt.Errorf("validate TRADE learning candidate import request: %w", err)
	}
	payload, err := encodeOwnerRequest(input, "learning candidate import")
	if err != nil {
		return moduletrade.LearningCandidateWriteResponse{}, err
	}
	request, err := client.newOwnerRequest(ctx, http.MethodPost, "/v1/memory/learning/import-candidate", scope, ownerLearningImportCall, payload)
	if err != nil {
		return moduletrade.LearningCandidateWriteResponse{}, fmt.Errorf("create TRADE learning candidate import request: %w", err)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return moduletrade.LearningCandidateWriteResponse{}, fmt.Errorf("TRADE learning candidate import request failed: %w", err)
	}
	var result moduletrade.LearningCandidateWriteResponse
	if err := decodeStrictOwnerResponse(response, "learning candidate import", &result); err != nil {
		return moduletrade.LearningCandidateWriteResponse{}, err
	}
	if err := result.Validate(scope.RequestID); err != nil {
		return moduletrade.LearningCandidateWriteResponse{}, fmt.Errorf("validate TRADE learning candidate import response: %w", err)
	}
	if err := validateOwnerReceiptIdentity(result.OwnerReceipt, scope, ownerLearningImportCall); err != nil {
		return moduletrade.LearningCandidateWriteResponse{}, err
	}
	return result, nil
}

// ReadMarketSnapshot reads exactly one bounded market snapshot.
func (client *Client) ReadMarketSnapshot(ctx context.Context, snapshotID string) (moduletrade.MarketSnapshotReadResponse, error) {
	scope, err := ownerScopeFor(ctx, ownerMarketSnapshotCall)
	if err != nil {
		return moduletrade.MarketSnapshotReadResponse{}, err
	}
	if err := validateOwnerPathID(snapshotID, "market snapshot ID"); err != nil {
		return moduletrade.MarketSnapshotReadResponse{}, err
	}
	request, err := client.newOwnerRequest(ctx, http.MethodGet, "/v1/memory/market/snapshots/"+url.PathEscape(snapshotID), scope, ownerMarketSnapshotCall, nil)
	if err != nil {
		return moduletrade.MarketSnapshotReadResponse{}, fmt.Errorf("create TRADE market snapshot request: %w", err)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return moduletrade.MarketSnapshotReadResponse{}, fmt.Errorf("TRADE market snapshot request failed: %w", err)
	}
	var result moduletrade.MarketSnapshotReadResponse
	if err := decodeStrictOwnerResponse(response, "market snapshot", &result); err != nil {
		return moduletrade.MarketSnapshotReadResponse{}, err
	}
	if err := result.Validate(scope.RequestID, scope.RequestID); err != nil {
		return moduletrade.MarketSnapshotReadResponse{}, fmt.Errorf("validate TRADE market snapshot response: %w", err)
	}
	if result.Record.SnapshotID != snapshotID {
		return moduletrade.MarketSnapshotReadResponse{}, fmt.Errorf("TRADE market snapshot response ID does not match the requested ID")
	}
	if err := validateOwnerEvidenceIdentity(result.OwnerEvidence, scope, ownerMarketSnapshotCall); err != nil {
		return moduletrade.MarketSnapshotReadResponse{}, err
	}
	return result, nil
}

// ImportMarketSnapshot invokes TRADE's validated market snapshot workflow.
func (client *Client) ImportMarketSnapshot(ctx context.Context, runID, instrumentID, tradeDate string) (moduletrade.MarketSnapshotWriteResponse, error) {
	scope, err := ownerScopeFor(ctx, ownerMarketImportCall)
	if err != nil {
		return moduletrade.MarketSnapshotWriteResponse{}, err
	}
	input := moduletrade.ImportMarketSnapshotRequest{
		ContractVersion: moduletrade.MemoryOwnerContractVersion,
		RequestID:       scope.RequestID,
		RunID:           runID,
		InstrumentID:    instrumentID,
		TradeDate:       tradeDate,
	}
	if err := input.Validate(); err != nil {
		return moduletrade.MarketSnapshotWriteResponse{}, fmt.Errorf("validate TRADE market snapshot import request: %w", err)
	}
	payload, err := encodeOwnerRequest(input, "market snapshot import")
	if err != nil {
		return moduletrade.MarketSnapshotWriteResponse{}, err
	}
	request, err := client.newOwnerRequest(ctx, http.MethodPost, "/v1/memory/market/import-snapshot", scope, ownerMarketImportCall, payload)
	if err != nil {
		return moduletrade.MarketSnapshotWriteResponse{}, fmt.Errorf("create TRADE market snapshot import request: %w", err)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return moduletrade.MarketSnapshotWriteResponse{}, fmt.Errorf("TRADE market snapshot import request failed: %w", err)
	}
	var result moduletrade.MarketSnapshotWriteResponse
	if err := decodeStrictOwnerResponse(response, "market snapshot import", &result); err != nil {
		return moduletrade.MarketSnapshotWriteResponse{}, err
	}
	if err := result.Validate(scope.RequestID); err != nil {
		return moduletrade.MarketSnapshotWriteResponse{}, fmt.Errorf("validate TRADE market snapshot import response: %w", err)
	}
	if err := validateOwnerReceiptIdentity(result.OwnerReceipt, scope, ownerMarketImportCall); err != nil {
		return moduletrade.MarketSnapshotWriteResponse{}, err
	}
	return result, nil
}

// ReadReplayDecision reads exactly one bounded replay decision.
func (client *Client) ReadReplayDecision(ctx context.Context, decisionID string) (moduletrade.ReplayDecisionReadResponse, error) {
	scope, err := ownerScopeFor(ctx, ownerReplayDecisionCall)
	if err != nil {
		return moduletrade.ReplayDecisionReadResponse{}, err
	}
	if err := validateOwnerPathID(decisionID, "replay decision ID"); err != nil {
		return moduletrade.ReplayDecisionReadResponse{}, err
	}
	request, err := client.newOwnerRequest(ctx, http.MethodGet, "/v1/memory/replay/decisions/"+url.PathEscape(decisionID), scope, ownerReplayDecisionCall, nil)
	if err != nil {
		return moduletrade.ReplayDecisionReadResponse{}, fmt.Errorf("create TRADE replay decision request: %w", err)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return moduletrade.ReplayDecisionReadResponse{}, fmt.Errorf("TRADE replay decision request failed: %w", err)
	}
	var result moduletrade.ReplayDecisionReadResponse
	if err := decodeStrictOwnerResponse(response, "replay decision", &result); err != nil {
		return moduletrade.ReplayDecisionReadResponse{}, err
	}
	if err := result.Validate(scope.RequestID, scope.RequestID); err != nil {
		return moduletrade.ReplayDecisionReadResponse{}, fmt.Errorf("validate TRADE replay decision response: %w", err)
	}
	if result.Record.DecisionID != decisionID {
		return moduletrade.ReplayDecisionReadResponse{}, fmt.Errorf("TRADE replay decision response ID does not match the requested ID")
	}
	if err := validateOwnerEvidenceIdentity(result.OwnerEvidence, scope, ownerReplayDecisionCall); err != nil {
		return moduletrade.ReplayDecisionReadResponse{}, err
	}
	return result, nil
}

// RecordReplayDecision invokes TRADE's bounded observe/select/avoid workflow.
func (client *Client) RecordReplayDecision(ctx context.Context, runID, instrumentID, tradeDate, action string) (moduletrade.ReplayDecisionWriteResponse, error) {
	scope, err := ownerScopeFor(ctx, ownerReplayRecordCall)
	if err != nil {
		return moduletrade.ReplayDecisionWriteResponse{}, err
	}
	input := moduletrade.RecordReplayDecisionRequest{
		ContractVersion: moduletrade.MemoryOwnerContractVersion,
		RequestID:       scope.RequestID,
		RunID:           runID,
		InstrumentID:    instrumentID,
		TradeDate:       tradeDate,
		Action:          action,
	}
	if err := input.Validate(); err != nil {
		return moduletrade.ReplayDecisionWriteResponse{}, fmt.Errorf("validate TRADE replay decision request: %w", err)
	}
	payload, err := encodeOwnerRequest(input, "replay decision record")
	if err != nil {
		return moduletrade.ReplayDecisionWriteResponse{}, err
	}
	request, err := client.newOwnerRequest(ctx, http.MethodPost, "/v1/memory/replay/record-decision", scope, ownerReplayRecordCall, payload)
	if err != nil {
		return moduletrade.ReplayDecisionWriteResponse{}, fmt.Errorf("create TRADE replay decision request: %w", err)
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return moduletrade.ReplayDecisionWriteResponse{}, fmt.Errorf("TRADE replay decision request failed: %w", err)
	}
	var result moduletrade.ReplayDecisionWriteResponse
	if err := decodeStrictOwnerResponse(response, "replay decision record", &result); err != nil {
		return moduletrade.ReplayDecisionWriteResponse{}, err
	}
	if err := result.Validate(scope.RequestID); err != nil {
		return moduletrade.ReplayDecisionWriteResponse{}, fmt.Errorf("validate TRADE replay decision response: %w", err)
	}
	if err := validateOwnerReceiptIdentity(result.OwnerReceipt, scope, ownerReplayRecordCall); err != nil {
		return moduletrade.ReplayDecisionWriteResponse{}, err
	}
	return result, nil
}

func readTokenFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("token path must reference a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("token file must be owner-only")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(payload))
	if len(token) < 32 || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("token must be a single value of at least 32 bytes")
	}
	return token, nil
}
