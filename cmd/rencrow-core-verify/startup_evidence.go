package main

import (
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// loadStartupRequestEvidence reads a prior canonical request receipt without
// treating its body as a new request.  Startup tracing needs proof that the
// ready service handled a real request, but it must not replay that request or
// copy user-visible output into the audit evidence.
func loadStartupRequestEvidence(path string, observedAt time.Time) (map[string]any, verifierOutcome) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, verifierOutcome{Status: "blocked", FailureBoundary: "startup request evidence is required"}
	}
	raw, err := readRegularFile(path, maxVerifierFileBytes)
	if err != nil {
		return nil, verifierOutcome{Status: "blocked", FailureBoundary: "startup request evidence is unavailable"}
	}
	var fields map[string]any
	if err := decodeStrictJSON(raw, &fields); err != nil || fields == nil {
		return nil, verifierOutcome{Status: "blocked", FailureBoundary: "startup request evidence is not a JSON object"}
	}
	if err := validateVerifierEvidenceFreshness(fields, observedAt); err != nil {
		return nil, verifierOutcome{Status: "blocked", FailureBoundary: err.Error()}
	}
	status, statusPresent := evidenceString(fields, "status")
	if statusPresent && !successfulEvidenceStatus(status) {
		return nil, verifierOutcome{Status: "failed", FailureBoundary: "startup request evidence is not successful"}
	}
	requestID, ok := evidenceString(fields, "request_id")
	if !ok {
		return nil, verifierOutcome{Status: "blocked", FailureBoundary: "startup request evidence has no request id"}
	}
	traceID, traceOK := evidenceString(fields, "trace_id")
	if !traceOK || modulecore.TraceID(traceID).Validate() != nil {
		return nil, verifierOutcome{Status: "blocked", FailureBoundary: "startup request evidence has no canonical trace id"}
	}
	result := map[string]any{
		"request_id_sha256": sha256Text(requestID),
		"trace_id_sha256":   sha256Text(traceID),
	}
	if statusPresent {
		result["request_status"] = status
	}
	if route, routeOK := firstEvidenceString(fields, "route", "route_or_target", "path"); routeOK {
		result["request_route"] = route
	}
	return result, verifierOutcome{}
}

func evidenceString(fields map[string]any, key string) (string, bool) {
	value, ok := fields[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	text = strings.TrimSpace(text)
	if text == "" || len([]byte(text)) > 128 || strings.IndexFunc(text, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", false
	}
	return text, true
}

func firstEvidenceString(fields map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := evidenceString(fields, key); ok {
			return value, true
		}
	}
	return "", false
}

func successfulEvidenceStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "passed", "pass", "ok", "success", "succeeded", "completed", "complete":
		return true
	default:
		return false
	}
}
