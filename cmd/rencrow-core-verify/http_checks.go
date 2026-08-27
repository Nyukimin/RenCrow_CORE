package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func runHealth(ctx context.Context, options verifierOptions, check manifestCheck, deps verifierDependencies) verifierOutcome {
	deps = normalizeVerifierDependencies(deps)
	return runCoreJSONGet(ctx, options, check, deps, "/health", func(body map[string]any) error {
		service, _ := body["service"].(string)
		if service != "rencrow-core" {
			return errors.New("CORE health service identity is invalid")
		}
		if ok, present := body["ok"].(bool); present && !ok {
			return errors.New("CORE health reported ok=false")
		}
		status, _ := body["status"].(string)
		if strings.EqualFold(status, "down") || strings.EqualFold(status, "unavailable") {
			return errors.New("CORE health reported unavailable")
		}
		return nil
	})
}

func runReadiness(ctx context.Context, options verifierOptions, check manifestCheck, deps verifierDependencies) verifierOutcome {
	deps = normalizeVerifierDependencies(deps)
	return runCoreJSONGet(ctx, options, check, deps, "/health/ready", func(body map[string]any) error {
		service, _ := body["service"].(string)
		if service != "rencrow-core" {
			return errors.New("CORE readiness service identity is invalid")
		}
		ready, ok := body["ready"].(bool)
		if !ok || !ready {
			return errors.New("CORE readiness reported ready=false")
		}
		return nil
	})
}

func runL1LightweightQuery(ctx context.Context, options verifierOptions, check manifestCheck, deps verifierDependencies) verifierOutcome {
	deps = normalizeVerifierDependencies(deps)
	return runCoreJSONGet(ctx, options, check, deps, "/viewer/memory/layers?include_l2=false&limit=1", func(body map[string]any) error {
		for _, field := range []string{"l0", "l1", "l3"} {
			if _, ok := body[field]; !ok {
				return fmt.Errorf("L1 lightweight response is missing %s", field)
			}
		}
		return nil
	})
}

func runCoreJSONGet(ctx context.Context, options verifierOptions, _ manifestCheck, deps verifierDependencies, route string, validate func(map[string]any) error) verifierOutcome {
	deps = normalizeVerifierDependencies(deps)
	baseURL, err := validateVerifierCoreURL(options.CoreURL)
	if err != nil {
		return verifierOutcome{Status: "blocked", FailureBoundary: "canonical CORE URL is not an allowed loopback endpoint"}
	}
	endpoint := joinVerifierRoute(baseURL, route)
	response := verifierHTTPJSON(ctx, deps.HTTPClient, http.MethodGet, endpoint, map[string]string{"Accept": "application/json"}, nil)
	evidence := map[string]any{
		"route":          route,
		"endpoint_host":  baseURL.Host,
		"http_status":    response.StatusCode,
		"response_bytes": len(response.Body),
	}
	if response.Err != nil {
		status := "failed"
		boundary := "canonical CORE route returned invalid JSON"
		if verifierHTTPStatusKind(response) == "unavailable" {
			status = "blocked"
			boundary = "canonical CORE route unavailable"
		}
		return verifierOutcome{Status: status, FailureBoundary: boundary, Evidence: evidence}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		status := "failed"
		boundary := "canonical CORE route returned an unexpected HTTP status"
		if verifierHTTPStatusKind(response) == "unavailable" {
			status = "blocked"
			boundary = "canonical CORE route unavailable"
		}
		return verifierOutcome{Status: status, FailureBoundary: boundary, Evidence: evidence}
	}
	if response.JSON == nil {
		return verifierOutcome{Status: "failed", FailureBoundary: "canonical CORE route returned no JSON object", Evidence: evidence}
	}
	if err := validate(response.JSON); err != nil {
		return verifierOutcome{Status: "failed", FailureBoundary: truncateVerifierMessage(err.Error()), Evidence: evidence}
	}
	evidence["response_fields"] = sortedJSONKeys(response.JSON)
	return verifierOutcome{Status: "passed", Evidence: evidence}
}

func sortedJSONKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	// A tiny insertion sort avoids importing another helper solely for this
	// bounded diagnostic list and keeps evidence deterministic.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func coreEndpoint(base *url.URL, route string) string {
	return joinVerifierRoute(base, route)
}
