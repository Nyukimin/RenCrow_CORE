package browsertrace

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
)

type ValidationPolicy struct {
	RequireTermsReview      bool
	RequireOfficialAPICheck bool
	ReadOnlyOnly            bool
	RequireLivePolicyCheck  bool
	DenySensitiveFlows      []string
}

func DefaultValidationPolicy() ValidationPolicy {
	return ValidationPolicy{
		RequireTermsReview:      true,
		RequireOfficialAPICheck: true,
		ReadOnlyOnly:            true,
		RequireLivePolicyCheck:  true,
	}
}

func ValidateAPICandidates(candidates []domaintrace.APICandidate, policy ValidationPolicy, now time.Time) []domaintrace.APICandidateValidationResult {
	results := make([]domaintrace.APICandidateValidationResult, 0, len(candidates))
	for _, candidate := range candidates {
		issues := apiCandidateValidationIssues(candidate, policy)
		passed := len(issues) == 0
		status := "validated"
		if !passed {
			status = "needs_review"
		}
		results = append(results, domaintrace.APICandidateValidationResult{
			ValidationID: "api_val_" + validationHash(candidate.TraceRunID+"|"+candidate.CandidateID),
			CandidateID:  candidate.CandidateID,
			TraceRunID:   candidate.TraceRunID,
			Passed:       passed,
			Status:       status,
			Issues:       issues,
			CreatedAt:    now,
		})
	}
	return results
}

type LivePolicyCheck struct {
	RobotsChecked       bool
	RobotsAllowed       bool
	RobotsStatus        int
	RateLimitChecked    bool
	RateLimitConfirmed  bool
	RateLimitHeaderName string
	RateLimitStatus     int
	Error               string
}

type LivePolicyChecker interface {
	Check(ctx context.Context, candidate domaintrace.APICandidate) LivePolicyCheck
}

type HTTPPolicyChecker struct {
	Client *http.Client
}

func ValidateAPICandidatesWithLivePolicy(ctx context.Context, candidates []domaintrace.APICandidate, policy ValidationPolicy, now time.Time, checker LivePolicyChecker) []domaintrace.APICandidateValidationResult {
	if !policy.RequireLivePolicyCheck || checker == nil {
		return ValidateAPICandidates(candidates, policy, now)
	}
	basePolicy := policy
	basePolicy.RequireTermsReview = false
	results := ValidateAPICandidates(candidates, basePolicy, now)
	for i := range results {
		check := checker.Check(ctx, candidates[i])
		results[i].Issues = append(results[i].Issues, livePolicyIssues(check)...)
		results[i].Passed = len(results[i].Issues) == 0
		results[i].Status = "validated"
		if !results[i].Passed {
			results[i].Status = "needs_review"
		}
	}
	return results
}

func (c HTTPPolicyChecker) Check(ctx context.Context, candidate domaintrace.APICandidate) LivePolicyCheck {
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	u, err := url.Parse(strings.TrimSpace(candidate.ObservedURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return LivePolicyCheck{Error: "invalid observed URL"}
	}
	check := LivePolicyCheck{}
	robotsURL := (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/robots.txt"}).String()
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil); err == nil {
		if resp, err := client.Do(req); err == nil {
			check.RobotsChecked = true
			check.RobotsStatus = resp.StatusCode
			check.RobotsAllowed = resp.StatusCode >= 200 && resp.StatusCode < 400
			_ = resp.Body.Close()
		} else {
			check.Error = err.Error()
		}
	}
	if req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.String(), nil); err == nil {
		if resp, err := client.Do(req); err == nil {
			check.RateLimitChecked = true
			check.RateLimitStatus = resp.StatusCode
			for _, name := range []string{"RateLimit-Limit", "X-RateLimit-Limit", "Retry-After"} {
				if strings.TrimSpace(resp.Header.Get(name)) != "" {
					check.RateLimitConfirmed = true
					check.RateLimitHeaderName = name
					break
				}
			}
			_ = resp.Body.Close()
		} else if check.Error == "" {
			check.Error = err.Error()
		}
	}
	return check
}

func livePolicyIssues(check LivePolicyCheck) []domaintrace.APIValidationIssue {
	var issues []domaintrace.APIValidationIssue
	if check.Error != "" {
		issues = append(issues, domaintrace.APIValidationIssue{
			Code:     "live_policy_check_failed",
			Message:  "robots or rate-limit check failed: " + check.Error,
			Severity: "medium",
		})
	}
	if !check.RobotsChecked || !check.RobotsAllowed {
		issues = append(issues, domaintrace.APIValidationIssue{
			Code:     "robots_review_required",
			Message:  fmt.Sprintf("robots.txt was not confirmed safe (status=%d)", check.RobotsStatus),
			Severity: "high",
		})
	}
	if !check.RateLimitChecked || !check.RateLimitConfirmed {
		issues = append(issues, domaintrace.APIValidationIssue{
			Code:     "rate_limit_review_required",
			Message:  fmt.Sprintf("rate limit policy was not confirmed from response headers (status=%d)", check.RateLimitStatus),
			Severity: "high",
		})
	}
	return issues
}

func apiCandidateValidationIssues(candidate domaintrace.APICandidate, policy ValidationPolicy) []domaintrace.APIValidationIssue {
	issues := []domaintrace.APIValidationIssue{}
	method := strings.ToUpper(strings.TrimSpace(candidate.Method))
	if policy.ReadOnlyOnly && method != "GET" && method != "HEAD" && method != "OPTIONS" {
		issues = append(issues, domaintrace.APIValidationIssue{
			Code:     "read_only_review_required",
			Message:  "candidate method is not read-only; fetcher promotion requires explicit review",
			Severity: "high",
		})
	}
	if policy.RequireTermsReview {
		issues = append(issues, domaintrace.APIValidationIssue{
			Code:     "terms_review_required",
			Message:  "terms, robots, API policy, and rate limit are not confirmed",
			Severity: "high",
		})
	}
	if policy.RequireOfficialAPICheck {
		issues = append(issues, domaintrace.APIValidationIssue{
			Code:     "official_api_check_required",
			Message:  "official API, RSS, Atom, or public feed alternative is not confirmed",
			Severity: "medium",
		})
	}
	if candidate.AuthRequired {
		issues = append(issues, domaintrace.APIValidationIssue{
			Code:     "auth_review_required",
			Message:  "candidate was observed with authentication headers",
			Severity: "high",
		})
	}
	personal := strings.ToLower(strings.TrimSpace(candidate.ContainsPersonalData))
	if personal == "" || personal == "unknown" || personal == "personal" || personal == "true" {
		issues = append(issues, domaintrace.APIValidationIssue{
			Code:     "pii_review_required",
			Message:  "personal data presence is not confirmed safe",
			Severity: "high",
		})
	}
	risk := strings.ToLower(strings.TrimSpace(candidate.RiskLevel))
	if risk != "" && risk != "low" {
		issues = append(issues, domaintrace.APIValidationIssue{
			Code:     "risk_review_required",
			Message:  "candidate risk level requires human review before promotion",
			Severity: "medium",
		})
	}
	if flow, ok := matchedSensitiveFlow(candidate, policy.DenySensitiveFlows); ok {
		issues = append(issues, domaintrace.APIValidationIssue{
			Code:     "sensitive_flow_review_required",
			Message:  "candidate appears to involve a denied sensitive flow: " + flow,
			Severity: "high",
		})
	}
	return issues
}

func matchedSensitiveFlow(candidate domaintrace.APICandidate, denyFlows []string) (string, bool) {
	if len(denyFlows) == 0 {
		return "", false
	}
	haystack := strings.ToLower(strings.Join([]string{
		candidate.ObservedURL,
		candidate.TemplatedURL,
		candidate.PathTemplate,
	}, " "))
	for _, flow := range denyFlows {
		flow = strings.ToLower(strings.TrimSpace(flow))
		if flow == "" {
			continue
		}
		if strings.Contains(haystack, flow) || strings.Contains(haystack, strings.ReplaceAll(flow, "_", "-")) {
			return flow, true
		}
	}
	return "", false
}

func validationHash(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}
