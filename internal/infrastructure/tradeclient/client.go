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

	moduletrade "github.com/Nyukimin/RenCrow_CORE/modules/trade"
)

const maxResponseBytes = 1 << 20

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
	if strings.TrimSpace(correlationID) != "" {
		request.Header.Set("X-Correlation-ID", correlationID)
	}
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
	return result, nil
}

func (client *Client) CommitSimulation(ctx context.Context, correlationID string, input moduletrade.SimulationCommitRequest) (moduletrade.PrivateSimulationCommit, error) {
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
	if strings.TrimSpace(correlationID) != "" {
		request.Header.Set("X-Correlation-ID", correlationID)
	}
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
	return result, nil
}

func (client *Client) RecordShadowObservation(ctx context.Context, correlationID string, input moduletrade.ShadowObservationRequest) (moduletrade.PrivateShadowObservation, error) {
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
	if strings.TrimSpace(correlationID) != "" {
		request.Header.Set("X-Correlation-ID", correlationID)
	}
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
	return result, nil
}

func (client *Client) RecordShadowOutcome(ctx context.Context, correlationID string, input moduletrade.ShadowOutcomeRequest) (moduletrade.PrivateShadowOutcome, error) {
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
	if strings.TrimSpace(correlationID) != "" {
		request.Header.Set("X-Correlation-ID", correlationID)
	}
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
	return result, nil
}

func (client *Client) RecordShadowReview(ctx context.Context, correlationID string, input moduletrade.ShadowReviewRequest) (moduletrade.PrivateShadowReview, error) {
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
	if strings.TrimSpace(correlationID) != "" {
		request.Header.Set("X-Correlation-ID", correlationID)
	}
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
	return result, nil
}

func (client *Client) ShadowOutcomeReport(ctx context.Context, correlationID, studyID string) (moduletrade.PrivateShadowOutcomeReport, error) {
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
	if strings.TrimSpace(correlationID) != "" {
		request.Header.Set("X-Correlation-ID", correlationID)
	}
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
	if err := result.Validate(studyID); err != nil {
		return moduletrade.PrivateShadowOutcomeReport{}, fmt.Errorf("validate TRADE Shadow outcome report response: %w", err)
	}
	return result, nil
}

func (client *Client) ShadowReviewReport(ctx context.Context, correlationID, studyID string) (moduletrade.PrivateShadowReviewReport, error) {
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
	if strings.TrimSpace(correlationID) != "" {
		request.Header.Set("X-Correlation-ID", correlationID)
	}
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
	if err := result.Validate(studyID); err != nil {
		return moduletrade.PrivateShadowReviewReport{}, fmt.Errorf("validate TRADE Shadow review report response: %w", err)
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
