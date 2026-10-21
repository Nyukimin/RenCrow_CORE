package classifierlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHTTPCandidateClassifierValidatesRequestAndResponseContract(t *testing.T) {
	config := validShadowConfig()
	var request CandidateInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/classify" {
			t.Errorf("request = %s %s, want POST /v1/classify", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(validCandidateResult(config))
	}))
	defer server.Close()
	config.Candidate.Endpoint = server.URL

	client, err := NewHTTPCandidateClassifier(config, "test-token")
	if err != nil {
		t.Fatalf("NewHTTPCandidateClassifier() error = %v", err)
	}
	input := CandidateInput{
		SchemaVersion: SchemaVersion,
		RequestID:     "turn-123",
		PolicySHA256:  config.Candidate.ExpectedPolicySHA256,
		Message:       "今日は相談したい。",
	}
	got, err := client.ClassifyCandidate(context.Background(), input)
	if err != nil {
		t.Fatalf("ClassifyCandidate() error = %v", err)
	}
	if request != input {
		t.Fatalf("wire request = %+v, want %+v", request, input)
	}
	if got.Status != CandidateClassified || got.Route == nil || *got.Route != LabelCHAT {
		t.Fatalf("candidate result = %+v", got)
	}
}

func TestHTTPCandidateClassifierRejectsPolicyAndProbabilityMismatches(t *testing.T) {
	config := validShadowConfig()
	for _, test := range []struct {
		name   string
		mutate func(*CandidateResult)
	}{
		{
			name: "policy hash mismatch",
			mutate: func(result *CandidateResult) {
				result.PolicySHA256 = strings.Repeat("b", 64)
			},
		},
		{
			name: "probability sum mismatch",
			mutate: func(result *CandidateResult) {
				result.Probabilities[LabelCHAT] = 0.5
			},
		},
		{
			name: "unknown route label",
			mutate: func(result *CandidateResult) {
				delete(result.Probabilities, LabelWILD)
				result.Probabilities[RouteLabel("WILDLY")] = 0.02
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				result := validCandidateResult(config)
				test.mutate(&result)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(result)
			}))
			defer server.Close()
			config.Candidate.Endpoint = server.URL
			client, err := NewHTTPCandidateClassifier(config, "test-token")
			if err != nil {
				t.Fatalf("NewHTTPCandidateClassifier() error = %v", err)
			}
			_, err = client.ClassifyCandidate(context.Background(), validCandidateInput(config))
			if !errors.Is(err, ErrInvalidCandidateOutput) {
				t.Fatalf("ClassifyCandidate() error = %v, want ErrInvalidCandidateOutput", err)
			}
		})
	}
}

func TestHTTPCandidateClassifierRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", MaxHTTPBodyBytes+1)))
	}))
	defer server.Close()
	config := validShadowConfig()
	config.Candidate.Endpoint = server.URL
	client, err := NewHTTPCandidateClassifier(config, "test-token")
	if err != nil {
		t.Fatalf("NewHTTPCandidateClassifier() error = %v", err)
	}
	_, err = client.ClassifyCandidate(context.Background(), validCandidateInput(config))
	if !errors.Is(err, ErrCandidateBodyTooLarge) {
		t.Fatalf("ClassifyCandidate() error = %v, want ErrCandidateBodyTooLarge", err)
	}
}

func TestHTTPCandidateClassifierNeverForwardsRequestsAcrossRedirects(t *testing.T) {
	for _, statusCode := range []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			var redirectRequests atomic.Int32
			redirectBodies := make(chan []byte, 1)
			redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				redirectRequests.Add(1)
				body, _ := io.ReadAll(r.Body)
				redirectBodies <- body
				w.WriteHeader(http.StatusOK)
			}))
			defer redirectTarget.Close()

			originalBodies := make(chan []byte, 1)
			original := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				originalBodies <- body
				w.Header().Set("Location", redirectTarget.URL+"/collect")
				w.WriteHeader(statusCode)
			}))
			defer original.Close()

			config := validShadowConfig()
			config.Candidate.Endpoint = original.URL
			client, err := NewHTTPCandidateClassifier(config, "test-token")
			if err != nil {
				t.Fatalf("NewHTTPCandidateClassifier() error = %v", err)
			}
			input := validCandidateInput(config)
			input.Message = "sensitive conversation body must stay at configured endpoint"
			_, err = client.ClassifyCandidate(context.Background(), input)
			var responseError *CandidateHTTPError
			if !errors.As(err, &responseError) || responseError.StatusCode != statusCode {
				t.Fatalf("ClassifyCandidate() error = %v, want HTTP %d", err, statusCode)
			}
			originalBody := <-originalBodies
			if !bytes.Contains(originalBody, []byte(input.Message)) {
				t.Fatalf("configured endpoint did not receive the expected request body")
			}
			if got := redirectRequests.Load(); got != 0 {
				t.Fatalf("redirect target received %d request(s); body=%q", got, <-redirectBodies)
			}
		})
	}
}

func TestNewHTTPCandidateClassifierRequiresCanonicalLoopbackEndpoint(t *testing.T) {
	config := validShadowConfig()
	config.Candidate.Endpoint = "https://classifier.example:18190"
	if _, err := NewHTTPCandidateClassifier(config, "test-token"); err == nil {
		t.Fatal("candidate client accepted a remote HTTPS endpoint")
	}
}

func validShadowConfig() Config {
	config := DefaultConfig()
	config.Mode = ModeShadow
	config.ExperimentID = "test-experiment"
	config.Candidate.Endpoint = "http://127.0.0.1:18190"
	config.Candidate.AuthTokenEnv = "RENCROW_CLASSIFIER_TEST_TOKEN"
	config.Candidate.ExpectedFamily = "laya-multilingual"
	config.Candidate.ExpectedModelRevision = strings.Repeat("b", 40)
	config.Candidate.ExpectedPolicySHA256 = strings.Repeat("a", 64)
	config.ReferenceWorker.PythonExecutable = "/tmp/classifierlab/python"
	config.ReferenceWorker.ModelDirectory = "/tmp/classifierlab/model"
	config.Recording.Directory = tTempDirectoryPlaceholder
	return config
}

const tTempDirectoryPlaceholder = "/tmp/classifierlab-private"

func validCandidateInput(config Config) CandidateInput {
	return CandidateInput{
		SchemaVersion: SchemaVersion,
		RequestID:     "turn-123",
		PolicySHA256:  config.Candidate.ExpectedPolicySHA256,
		Message:       "hello",
	}
}

func validCandidateResult(config Config) CandidateResult {
	probabilities := map[RouteLabel]float64{
		LabelCHAT: 0.7, LabelPLAN: 0.08, LabelANALYZE: 0.04, LabelOPS: 0.02,
		LabelRESEARCH: 0.02, LabelCODE: 0.03, LabelCODE1: 0.02, LabelCODE2: 0.03,
		LabelCODE3: 0.02, LabelCODE4: 0.02, LabelWILD: 0.02,
	}
	route := LabelCHAT
	return CandidateResult{
		SchemaVersion:       SchemaVersion,
		RequestID:           "turn-123",
		Status:              CandidateClassified,
		Route:               &route,
		Probabilities:       probabilities,
		SelectedProbability: 0.7,
		TopTwoMargin:        0.62,
		ProbabilityStatus:   "upstream_default_not_validated_on_rencrow",
		Model: CandidateModel{
			Family: config.Candidate.ExpectedFamily, SourceID: "source-1",
			Revision: config.Candidate.ExpectedModelRevision, SDKVersion: "1.0.0",
			Device: "cpu", DType: "float32",
		},
		PolicySHA256: config.Candidate.ExpectedPolicySHA256,
		Input:        CandidateInputMetadata{EffectiveSequenceTokens: 120},
		Timing:       CandidateTiming{Prepare: 3, Inference: 42, Postprocess: 1, ServerTotal: 48},
		ReasonCode:   "OK",
	}
}
