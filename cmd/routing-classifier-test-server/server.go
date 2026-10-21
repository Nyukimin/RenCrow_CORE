package main

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing/classifierlab"
)

const (
	httpWaitTimeoutMS = 3000
	maxRequestIDBytes = 256
)

type candidateBackend interface {
	Readiness() (bool, string, bool)
	Classify(classifierlab.CandidateInput) (classifierlab.CandidateResult, error)
}

type classificationServer struct {
	config       loadedConfig
	backend      candidateBackend
	startupError string
	model        classifierlab.CandidateModel
	policySHA256 string
	routes       []classifierlab.RouteLabel
	busy         chan struct{}
	httpTimeout  time.Duration
}

type errorResponse struct {
	ReasonCode string `json:"reason_code"`
}

type readinessResponse struct {
	Status                       string `json:"status"`
	OfflineConfigurationVerified bool   `json:"offline_configuration_verified"`
	NetworkIsolationStatus       string `json:"network_isolation_status"`
	ReasonCode                   string `json:"reason_code,omitempty"`
}

func newClassificationServer(config loadedConfig, backend candidateBackend, startupError string, artifacts *classifierlab.PreparedArtifacts) *classificationServer {
	server := &classificationServer{
		config:       config,
		backend:      backend,
		startupError: startupError,
		busy:         make(chan struct{}, 1),
		httpTimeout:  httpWaitTimeoutMS * time.Millisecond,
	}
	if artifacts != nil {
		server.model = classifierlab.CandidateModel{
			Family:     artifacts.Manifest.Model.Family,
			SourceID:   artifacts.Manifest.Model.SourceID,
			Revision:   artifacts.Manifest.Model.Revision,
			SDKVersion: artifacts.Manifest.SDK.Version,
			Device:     artifacts.Manifest.Model.Device,
			DType:      artifacts.Manifest.Model.DType,
		}
		server.policySHA256 = artifacts.PolicySHA256
		server.routes = make([]classifierlab.RouteLabel, len(artifacts.Policy.Routes))
		for index, route := range artifacts.Policy.Routes {
			server.routes[index] = classifierlab.RouteLabel(route)
		}
	}
	return server
}

func (s *classificationServer) Handler() http.Handler {
	return http.HandlerFunc(s.serveHTTP)
}

func (s *classificationServer) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/healthz":
		if request.Method != http.MethodGet {
			writeError(writer, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
			return
		}
		writeJSON(writer, http.StatusOK, map[string]string{"status": "alive"})
	case "/readyz":
		if request.Method != http.MethodGet {
			writeError(writer, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
			return
		}
		if ready, reason := s.ready(); ready {
			writeJSON(writer, http.StatusOK, readinessResponse{
				Status: "ready", OfflineConfigurationVerified: true, NetworkIsolationStatus: "UNVERIFIED",
			})
		} else {
			writeJSON(writer, http.StatusServiceUnavailable, readinessResponse{
				Status: "not_ready", OfflineConfigurationVerified: false,
				NetworkIsolationStatus: "UNVERIFIED", ReasonCode: reason,
			})
		}
	case "/v1/classify":
		if request.Method != http.MethodPost {
			writeError(writer, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
			return
		}
		s.handleClassify(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (s *classificationServer) ready() (bool, string) {
	if s.startupError != "" {
		return false, s.startupError
	}
	if s.config.Token == "" {
		return false, "AUTH_NOT_CONFIGURED"
	}
	if s.backend == nil {
		return false, "MODEL_NOT_READY"
	}
	ready, reason, offlineConfigurationVerified := s.backend.Readiness()
	if !ready {
		return false, reason
	}
	if !offlineConfigurationVerified {
		return false, "MODEL_NOT_READY"
	}
	return true, ""
}

func (s *classificationServer) handleClassify(writer http.ResponseWriter, request *http.Request) {
	if s.config.Token == "" {
		writeError(writer, http.StatusServiceUnavailable, "AUTH_NOT_CONFIGURED")
		return
	}
	if !validBearerToken(request.Header.Get("Authorization"), s.config.Token) {
		writeError(writer, http.StatusUnauthorized, "UNAUTHORIZED")
		return
	}
	if ready, reason := s.ready(); !ready {
		writeError(writer, http.StatusServiceUnavailable, reason)
		return
	}
	if s.backend == nil {
		writeError(writer, http.StatusServiceUnavailable, "MODEL_NOT_READY")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(writer, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, classifierlab.MaxHTTPBodyBytes+1))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	if len(body) > classifierlab.MaxHTTPBodyBytes {
		writeError(writer, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE")
		return
	}
	input, err := decodeCandidateInput(body)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	if err := validateCandidateInput(input); err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_REQUEST")
		return
	}
	if input.PolicySHA256 != s.policySHA256 || input.PolicySHA256 != s.config.Value.Candidate.ExpectedPolicySHA256 {
		writeError(writer, http.StatusConflict, "POLICY_MISMATCH")
		return
	}
	select {
	case s.busy <- struct{}{}:
	default:
		writeError(writer, http.StatusTooManyRequests, "WORKER_BUSY")
		return
	}
	resultChannel := make(chan classifyOutcome, 1)
	go func() {
		defer func() { <-s.busy }()
		result, classifyErr := s.backend.Classify(input)
		resultChannel <- classifyOutcome{result: result, err: classifyErr}
	}()

	timer := time.NewTimer(s.httpTimeout)
	defer timer.Stop()
	select {
	case outcome := <-resultChannel:
		if outcome.err != nil {
			writeError(writer, httpStatusForWorkerError(outcome.err), reasonCodeForError(outcome.err))
			return
		}
		if err := validateCandidateResult(input, outcome.result, s.model, s.policySHA256, s.routes); err != nil {
			writeError(writer, http.StatusInternalServerError, "INVALID_MODEL_OUTPUT")
			return
		}
		writeJSON(writer, http.StatusOK, outcome.result)
	case <-timer.C:
		writeError(writer, http.StatusGatewayTimeout, "INFERENCE_TIMEOUT")
	case <-request.Context().Done():
		return
	}
}

type classifyOutcome struct {
	result classifierlab.CandidateResult
	err    error
}

func validBearerToken(header, expected string) bool {
	if len(expected) == 0 || len(header) != len("Bearer ")+len(expected) || !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	provided := header[len("Bearer "):]
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func decodeCandidateInput(body []byte) (classifierlab.CandidateInput, error) {
	var input classifierlab.CandidateInput
	if len(body) == 0 || len(body) > classifierlab.MaxHTTPBodyBytes {
		return input, errors.New("candidate body size is invalid")
	}
	if err := rejectDuplicateFields(body); err != nil {
		return input, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return input, errors.New("candidate body contains multiple JSON values")
		}
		return input, err
	}
	return input, nil
}

func validateCandidateInput(input classifierlab.CandidateInput) error {
	if input.SchemaVersion != classifierlab.SchemaVersion || input.RequestID == "" ||
		len(input.RequestID) > maxRequestIDBytes || strings.TrimSpace(input.RequestID) != input.RequestID ||
		strings.TrimSpace(input.Message) == "" {
		return errors.New("candidate input identity, schema, or message is invalid")
	}
	for _, character := range input.RequestID {
		if unicode.IsControl(character) {
			return errors.New("candidate request_id contains control characters")
		}
	}
	return nil
}

func validateCandidateResult(input classifierlab.CandidateInput, result classifierlab.CandidateResult, expectedModel classifierlab.CandidateModel, expectedPolicySHA string, routes []classifierlab.RouteLabel) error {
	if result.SchemaVersion != classifierlab.SchemaVersion || result.RequestID != input.RequestID || result.PolicySHA256 != expectedPolicySHA {
		return errors.New("child response schema, request id, or policy hash mismatch")
	}
	if result.Model != expectedModel {
		return errors.New("child response model metadata mismatch")
	}
	switch result.Status {
	case classifierlab.CandidateAbstain:
		if result.Route != nil || len(result.Probabilities) != 0 || (result.ReasonCode != "INPUT_TOO_LONG" && result.ReasonCode != "POLICY_TOO_LONG") {
			return errors.New("child returned an invalid abstention")
		}
		return nil
	case classifierlab.CandidateClassified:
	default:
		return errors.New("child returned an unknown candidate status")
	}
	if result.Route == nil || !validServerRoute(*result.Route, routes) || len(result.Probabilities) != len(routes) {
		return errors.New("child returned an invalid route or probability set")
	}
	var total float64
	top, second := -1.0, -1.0
	for _, route := range routes {
		probability, exists := result.Probabilities[route]
		if !exists || !finiteUnit(probability) {
			return fmt.Errorf("child probability for %q is invalid", route)
		}
		total += probability
		if probability > top {
			second, top = top, probability
		} else if probability > second {
			second = probability
		}
	}
	if math.Abs(total-1) > 1e-4 || !finiteUnit(result.SelectedProbability) ||
		math.Abs(result.SelectedProbability-result.Probabilities[*result.Route]) > 1e-4 ||
		!finiteUnit(result.TopTwoMargin) || math.Abs(result.TopTwoMargin-(top-second)) > 1e-4 {
		return errors.New("child probability summary is inconsistent")
	}
	if result.NativeConfidence != nil && !finiteUnit(*result.NativeConfidence) {
		return errors.New("child native confidence is invalid")
	}
	if result.ProbabilityStatus != classifierlab.ProbabilityStatusUnvalidated || result.ReasonCode != "OK" ||
		result.Input.EffectiveSequenceTokens < 1 || result.Input.Truncated ||
		!finiteNonnegative(result.Timing.Prepare) || !finiteNonnegative(result.Timing.Inference) ||
		!finiteNonnegative(result.Timing.Postprocess) || !finiteNonnegative(result.Timing.ServerTotal) {
		return errors.New("child result metadata is invalid")
	}
	return nil
}

func validServerRoute(route classifierlab.RouteLabel, routes []classifierlab.RouteLabel) bool {
	for _, expected := range routes {
		if route == expected {
			return true
		}
	}
	return false
}

func finiteUnit(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func finiteNonnegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func reasonCodeForError(err error) string {
	var workerErr *workerCallError
	if errors.As(err, &workerErr) {
		return workerErr.reasonCode
	}
	return "SDK_ERROR"
}

func writeError(writer http.ResponseWriter, status int, reason string) {
	writeJSON(writer, status, errorResponse{ReasonCode: reason})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil || len(body)+1 > classifierlab.MaxHTTPBodyBytes {
		status = http.StatusInternalServerError
		body = []byte(`{"reason_code":"SDK_ERROR"}`)
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_, _ = writer.Write(append(body, '\n'))
}
