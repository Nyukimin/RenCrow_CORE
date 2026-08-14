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
	"runtime"
	"strings"
	"time"

	toolcontext "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

const maxResponseBytes = 1 << 20

type ownerCallContract struct {
	purpose   string
	dataScope string
}

var (
	ownerRiskPreviewCall         = ownerCallContract{purpose: "portfolio_memory_read", dataScope: toolcontext.DataScopeInternal}
	ownerSimulationCommitCall    = ownerCallContract{purpose: "portfolio_memory_write", dataScope: toolcontext.DataScopeInternal}
	ownerShadowObservationCall   = ownerCallContract{purpose: "ledger_memory_write", dataScope: toolcontext.DataScopeInternal}
	ownerShadowOutcomeCall       = ownerCallContract{purpose: "ledger_memory_write", dataScope: toolcontext.DataScopeInternal}
	ownerShadowReviewCall        = ownerCallContract{purpose: "ledger_memory_write", dataScope: toolcontext.DataScopeInternal}
	ownerShadowOutcomeReportCall = ownerCallContract{purpose: "ledger_memory_read", dataScope: toolcontext.DataScopeInternal}
	ownerShadowReviewReportCall  = ownerCallContract{purpose: "ledger_memory_read", dataScope: toolcontext.DataScopeInternal}
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

func (client *Client) ShadowOutcomeReport(ctx context.Context, correlationID, studyID string) (moduletrade.PrivateShadowOutcomeReport, error) {
	scope, err := ownerReportScope(ctx, correlationID, ownerShadowOutcomeReportCall)
	if err != nil {
		return moduletrade.PrivateShadowOutcomeReport{}, err
	}
	if strings.TrimSpace(studyID) == "" {
		return moduletrade.PrivateShadowOutcomeReport{}, fmt.Errorf("TRADE Shadow outcome report study ID is required")
	}
	requestURL := client.baseURL + "/v1/shadow/outcomes/report?study_id=" + url.QueryEscape(studyID)
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
	if err := result.Validate(studyID, scope.RequestID); err != nil {
		return moduletrade.PrivateShadowOutcomeReport{}, fmt.Errorf("validate TRADE Shadow outcome report response: %w", err)
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
