package classifierlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"
)

const probabilityTolerance = 1e-4

type CandidateHTTPError struct {
	StatusCode int
}

func (e *CandidateHTTPError) Error() string {
	return fmt.Sprintf("candidate HTTP endpoint returned status %d", e.StatusCode)
}

type HTTPCandidateClassifier struct {
	client         *http.Client
	endpoint       string
	token          string
	expectedFamily string
	expectedRev    string
	expectedPolicy string
}

func NewHTTPCandidateClassifier(cfg Config, token string) (*HTTPCandidateClassifier, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate classifierlab config: %w", err)
	}
	if cfg.Mode != ModeShadow {
		return nil, errors.New("candidate HTTP client requires shadow mode")
	}
	if token == "" || strings.TrimSpace(token) != token || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("candidate auth token is empty or malformed")
	}
	endpoint := strings.TrimRight(cfg.Candidate.Endpoint, "/") + "/v1/classify"
	return &HTTPCandidateClassifier{
		client: &http.Client{
			Timeout: time.Duration(cfg.Candidate.TimeoutMS) * time.Millisecond,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		endpoint:       endpoint,
		token:          token,
		expectedFamily: cfg.Candidate.ExpectedFamily,
		expectedRev:    cfg.Candidate.ExpectedModelRevision,
		expectedPolicy: cfg.Candidate.ExpectedPolicySHA256,
	}, nil
}

func NewHTTPCandidateClassifierFromEnv(cfg Config) (*HTTPCandidateClassifier, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate classifierlab config: %w", err)
	}
	token, ok := os.LookupEnv(cfg.Candidate.AuthTokenEnv)
	if !ok {
		return nil, fmt.Errorf("candidate auth token environment variable %q is not set", cfg.Candidate.AuthTokenEnv)
	}
	return NewHTTPCandidateClassifier(cfg, token)
}

func (c *HTTPCandidateClassifier) ClassifyCandidate(ctx context.Context, input CandidateInput) (CandidateResult, error) {
	if c == nil || c.client == nil {
		return CandidateResult{}, errors.New("candidate HTTP client is not initialized")
	}
	if ctx == nil {
		return CandidateResult{}, errors.New("candidate context is nil")
	}
	if input.SchemaVersion != SchemaVersion || strings.TrimSpace(input.RequestID) == "" ||
		input.PolicySHA256 != c.expectedPolicy || strings.TrimSpace(input.Message) == "" {
		return CandidateResult{}, fmt.Errorf("%w: candidate request identity, schema, policy, or message is invalid", ErrInvalidCandidateOutput)
	}
	body, err := json.Marshal(input)
	if err != nil {
		return CandidateResult{}, fmt.Errorf("encode candidate request: %w", err)
	}
	if len(body) > MaxHTTPBodyBytes {
		return CandidateResult{}, ErrCandidateBodyTooLarge
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return CandidateResult{}, fmt.Errorf("build candidate request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.token)

	response, err := c.client.Do(request)
	if err != nil {
		return CandidateResult{}, fmt.Errorf("call candidate endpoint: %w", err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, MaxHTTPBodyBytes+1))
	if err != nil {
		return CandidateResult{}, fmt.Errorf("read candidate response: %w", err)
	}
	if len(responseBody) > MaxHTTPBodyBytes {
		return CandidateResult{}, ErrCandidateBodyTooLarge
	}
	if response.StatusCode != http.StatusOK {
		return CandidateResult{}, &CandidateHTTPError{StatusCode: response.StatusCode}
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return CandidateResult{}, invalidCandidateOutput("response Content-Type must be application/json")
	}
	if err := rejectDuplicateJSONFields(responseBody); err != nil {
		return CandidateResult{}, invalidCandidateOutput("%v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	decoder.DisallowUnknownFields()
	var result CandidateResult
	if err := decoder.Decode(&result); err != nil {
		return CandidateResult{}, invalidCandidateOutput("decode response: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return CandidateResult{}, invalidCandidateOutput("response contains multiple JSON values")
		}
		return CandidateResult{}, invalidCandidateOutput("decode trailing response data: %v", err)
	}
	if err := c.validateResult(input, result); err != nil {
		return CandidateResult{}, err
	}
	return cloneCandidateResult(result), nil
}

func (c *HTTPCandidateClassifier) validateResult(input CandidateInput, result CandidateResult) error {
	if result.SchemaVersion != SchemaVersion || result.RequestID != input.RequestID || result.PolicySHA256 != c.expectedPolicy {
		return invalidCandidateOutput("response schema, request_id, or policy_sha256 does not match the request")
	}
	switch result.Status {
	case CandidateAbstain:
		if result.Route != nil || strings.TrimSpace(result.ReasonCode) == "" || len(result.Probabilities) != 0 {
			return invalidCandidateOutput("abstain response must have null route, no probabilities, and a reason_code")
		}
		return nil
	case CandidateClassified:
	default:
		return invalidCandidateOutput("unknown candidate status %q", result.Status)
	}
	if result.Route == nil || !validRouteLabel(*result.Route) {
		return invalidCandidateOutput("classified response has an unknown or missing route")
	}
	if len(result.Probabilities) != MaxProbabilityLabels {
		return invalidCandidateOutput("classified response must contain exactly %d route probabilities", MaxProbabilityLabels)
	}
	var total float64
	top, second := -1.0, -1.0
	for _, label := range routeLabels {
		value, ok := result.Probabilities[label]
		if !ok || !finiteUnit(value) {
			return invalidCandidateOutput("probability for %q is missing or outside [0,1]", label)
		}
		total += value
		if value > top {
			second = top
			top = value
		} else if value > second {
			second = value
		}
	}
	if math.Abs(total-1) > probabilityTolerance {
		return invalidCandidateOutput("probability sum %.8f differs from 1", total)
	}
	selected := result.Probabilities[*result.Route]
	if !finiteUnit(result.SelectedProbability) || math.Abs(result.SelectedProbability-selected) > probabilityTolerance {
		return invalidCandidateOutput("selected_probability does not match the selected route probability")
	}
	margin := top - second
	if !finiteUnit(result.TopTwoMargin) || math.Abs(result.TopTwoMargin-margin) > probabilityTolerance {
		return invalidCandidateOutput("top_two_margin does not match the route probabilities")
	}
	if result.NativeConfidence != nil && !finiteUnit(*result.NativeConfidence) {
		return invalidCandidateOutput("native_confidence is outside [0,1]")
	}
	if result.ProbabilityStatus != ProbabilityStatusUnvalidated {
		return invalidCandidateOutput("probability_status is not the unvalidated upstream status")
	}
	if result.Model.Family != c.expectedFamily || result.Model.Revision != c.expectedRev ||
		strings.TrimSpace(result.Model.SourceID) == "" || strings.TrimSpace(result.Model.SDKVersion) == "" ||
		strings.TrimSpace(result.Model.Device) == "" || strings.TrimSpace(result.Model.DType) == "" {
		return invalidCandidateOutput("model metadata does not match the configured candidate")
	}
	if result.Input.EffectiveSequenceTokens < 1 || result.Input.Truncated {
		return invalidCandidateOutput("classified response has invalid or truncated input metadata")
	}
	if !finiteNonnegative(result.Timing.Prepare) || !finiteNonnegative(result.Timing.Inference) ||
		!finiteNonnegative(result.Timing.Postprocess) || !finiteNonnegative(result.Timing.ServerTotal) {
		return invalidCandidateOutput("timing_ms contains a negative or non-finite value")
	}
	if result.ReasonCode != "OK" {
		return invalidCandidateOutput("classified response reason_code must be OK")
	}
	return nil
}

func invalidCandidateOutput(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidCandidateOutput, fmt.Sprintf(format, args...))
}

func finiteUnit(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func finiteNonnegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func rejectDuplicateJSONFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	first, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := first.(json.Delim)
	if !ok || delimiter != '{' {
		return errors.New("response root must be an object")
	}
	if err := consumeJSONContainer(decoder, delimiter); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("response contains trailing JSON data")
		}
		return err
	}
	return nil
}

func consumeJSONContainer(decoder *json.Decoder, delimiter json.Delim) error {
	if delimiter == '{' {
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON field %q", key)
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return errors.New("malformed JSON object")
		}
		return nil
	}
	if delimiter == '[' {
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return errors.New("malformed JSON array")
		}
		return nil
	}
	return errors.New("unexpected JSON container")
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delimiter != '{' && delimiter != '[' {
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return consumeJSONContainer(decoder, delimiter)
}

func cloneCandidateResult(result CandidateResult) CandidateResult {
	if result.Route != nil {
		route := *result.Route
		result.Route = &route
	}
	if result.NativeConfidence != nil {
		confidence := *result.NativeConfidence
		result.NativeConfidence = &confidence
	}
	if result.Probabilities != nil {
		probabilities := make(map[RouteLabel]float64, len(result.Probabilities))
		for route, probability := range result.Probabilities {
			probabilities[route] = probability
		}
		result.Probabilities = probabilities
	}
	return result
}
