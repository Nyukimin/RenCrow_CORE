package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	runnerSchemaVersion  = 1
	ownerManifestVersion = 2
	ownerReceiptSchema   = "rencrow.check-receipt.v1"
	defaultCoreURL       = "http://127.0.0.1:18790"
	defaultPlanner       = "rencrow-check-plan"
	maxManifestBytes     = 1 << 20
	maxPlanBytes         = 1 << 20
	maxResponseBytes     = 64 << 10
	maxReceiptMessage    = 256
	maxManifestChecks    = 128
)

var stableCommandIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

func defaultManifestPath() string {
	if configured := strings.TrimSpace(os.Getenv("RENCROW_CORE_CHECK_MANIFEST")); configured != "" {
		return configured
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join("config", "checks", "core.json")
	}
	return filepath.Join(home, ".local", "share", "rencrow", "checks", "core.json")
}

type runnerOptions struct {
	ManifestPath string
	CoreURL      string
	Phase        string
	PlannerPath  string
	SnapshotDir  string
	Now          time.Time
	HTTPClient   *http.Client

	// Planner is injected by focused tests. Production always uses the fixed
	// rencrow-check-plan subprocess below; CORE does not copy planner logic.
	Planner func(context.Context, []string) ([]byte, error)
}

type checkRequest struct {
	SchemaVersion int             `json:"schema_version"`
	Purpose       string          `json:"purpose"`
	Phase         string          `json:"phase"`
	Checks        []manifestCheck `json:"checks"`
}

// ownerManifestEnvelope is the on-disk CORE-owned source representation. v2
// adds metadata used by full-system composition; only its planner-compatible
// Check fields are projected into checkRequest before invoking the strict v1
// planner.
type ownerManifestEnvelope struct {
	SchemaVersion int               `json:"schema_version"`
	Purpose       string            `json:"purpose"`
	Phase         string            `json:"phase"`
	Checks        []json.RawMessage `json:"checks"`
}

type ownerManifest struct {
	SchemaVersion int
	Purpose       string
	Phase         string
	Checks        []manifestCheck
}

type ownerExecutor struct {
	Kind      string `json:"kind"`
	CommandID string `json:"command_id"`
}

type manifestCheck struct {
	CheckID            string    `json:"check_id"`
	GuaranteeID        string    `json:"guarantee_id"`
	Owner              string    `json:"owner"`
	Purpose            string    `json:"purpose"`
	Target             string    `json:"target"`
	Phase              string    `json:"phase"`
	Consumer           string    `json:"consumer,omitempty"`
	FailureAction      string    `json:"failure_action,omitempty"`
	Cost               string    `json:"cost"`
	SafetyGate         bool      `json:"safety_gate"`
	ReplacementCheckID string    `json:"replacement_check_id,omitempty"`
	Evidence           *evidence `json:"evidence,omitempty"`
}

type evidence struct {
	Status     string    `json:"status"`
	VerifiedAt time.Time `json:"verified_at"`
	TTLSeconds int64     `json:"ttl_seconds"`
	ReceiptRef string    `json:"receipt_ref"`
}

type checkPlan struct {
	SchemaVersion int        `json:"schema_version"`
	Status        string     `json:"status"`
	Purpose       string     `json:"purpose"`
	Phase         string     `json:"phase"`
	EvaluatedAt   time.Time  `json:"evaluated_at"`
	PlanRevision  string     `json:"plan_revision"`
	Included      []planItem `json:"included"`
	Excluded      []planItem `json:"excluded"`
	Deferred      []planItem `json:"deferred"`
	Errors        []string   `json:"errors"`
}

type planItem struct {
	CheckID               string   `json:"check_id"`
	Classifications       []string `json:"classifications,omitempty"`
	Reason                string   `json:"reason"`
	ReplacementCheckID    string   `json:"replacement_check_id,omitempty"`
	ReplacementReceiptRef string   `json:"replacement_receipt_ref,omitempty"`
}

type checkResult struct {
	CheckID    string `json:"check_id"`
	Status     string `json:"status"`
	Target     string `json:"target"`
	ObservedAt string `json:"observed_at"`
	DurationMS int64  `json:"duration_ms"`
	HTTPStatus int    `json:"http_status,omitempty"`
	Message    string `json:"message,omitempty"`
}

type runnerReceipt struct {
	SchemaVersion int           `json:"schema_version"`
	Status        string        `json:"status"`
	Purpose       string        `json:"purpose,omitempty"`
	Phase         string        `json:"phase,omitempty"`
	EvaluatedAt   string        `json:"evaluated_at,omitempty"`
	PlanRevision  string        `json:"plan_revision,omitempty"`
	Results       []checkResult `json:"results"`
	Excluded      []planItem    `json:"excluded,omitempty"`
	Deferred      []planItem    `json:"deferred,omitempty"`
	Error         string        `json:"error,omitempty"`
}

func runRunner(ctx context.Context, options runnerOptions, out io.Writer) (runnerReceipt, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	if strings.TrimSpace(options.Phase) == "" {
		options.Phase = "runtime"
	}
	if strings.TrimSpace(options.ManifestPath) == "" {
		options.ManifestPath = defaultManifestPath()
	}
	if strings.TrimSpace(options.CoreURL) == "" {
		options.CoreURL = defaultCoreURL
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}

	receipt := runnerReceipt{SchemaVersion: runnerSchemaVersion, Status: "blocked", Results: []checkResult{}}
	manifest, err := loadCheckManifest(options.ManifestPath)
	if err != nil {
		return finishRunner(out, receipt, fmt.Errorf("load check manifest: %w", err))
	}
	request, err := projectCheckRequest(manifest, options.Phase)
	if err != nil {
		return finishRunner(out, receipt, fmt.Errorf("project check manifest: %w", err))
	}
	receipt.Purpose = request.Purpose
	receipt.Phase = request.Phase
	receipt.EvaluatedAt = now.Format(time.RFC3339)

	plannerInputPath, cleanupPlannerInput, err := writePlannerInput(request)
	if err != nil {
		return finishRunner(out, receipt, fmt.Errorf("prepare planner input: %w", err))
	}
	defer cleanupPlannerInput()
	plannerArgs := []string{"plan", "--input", plannerInputPath, "--now", now.Format(time.RFC3339)}
	plannerOutput, plannerErr := invokePlanner(ctx, options, plannerArgs)
	if len(plannerOutput) == 0 {
		if plannerErr == nil {
			plannerErr = errors.New("planner returned empty output")
		}
		return finishRunner(out, receipt, fmt.Errorf("invoke check planner: %w", plannerErr))
	}
	if len(plannerOutput) > maxPlanBytes {
		return finishRunner(out, receipt, errors.New("planner output exceeds bounded size"))
	}
	plan, err := decodePlan(plannerOutput)
	if err != nil {
		return finishRunner(out, receipt, fmt.Errorf("decode planner output: %w", err))
	}
	receipt.PlanRevision = plan.PlanRevision
	if err := validatePlan(request, plan, now); err != nil {
		return finishRunner(out, receipt, err)
	}
	if plannerErr != nil {
		return finishRunner(out, receipt, fmt.Errorf("planner failed after returning a plan: %w", plannerErr))
	}
	if plan.Status != "ready" {
		return finishRunner(out, receipt, fmt.Errorf("check plan is %s", plan.Status))
	}

	baseURL, err := validateCoreURL(options.CoreURL)
	if err != nil {
		return finishRunner(out, receipt, fmt.Errorf("core URL rejected: %w", err))
	}
	allowlist := coreCheckAllowlist()
	for _, item := range plan.Included {
		if _, ok := allowlist[item.CheckID]; !ok {
			return finishRunner(out, receipt, fmt.Errorf("included check %q is not allowlisted", item.CheckID))
		}
	}
	for _, item := range plan.Included {
		executor := allowlist[item.CheckID]
		result := executor(ctx, baseURL, options.HTTPClient, options)
		result.CheckID = item.CheckID
		if result.Status == "" {
			result.Status = "failed"
		}
		if len(result.Message) > maxReceiptMessage {
			result.Message = result.Message[:maxReceiptMessage]
		}
		receipt.Results = append(receipt.Results, result)
	}
	receipt.Excluded = append([]planItem(nil), plan.Excluded...)
	receipt.Deferred = append([]planItem(nil), plan.Deferred...)
	receipt.Status = "passed"
	for _, result := range receipt.Results {
		if result.Status != "passed" {
			receipt.Status = "failed"
			break
		}
	}
	if receipt.Status != "passed" {
		return finishRunner(out, receipt, errors.New("one or more included checks failed"))
	}
	return finishRunner(out, receipt, nil)
}

type checkExecutor func(context.Context, *url.URL, *http.Client, runnerOptions) checkResult

func coreCheckAllowlist() map[string]checkExecutor {
	return map[string]checkExecutor{
		"core_health":                runHealthCheck,
		"core_readiness":             runReadinessCheck,
		"core_l1_lightweight_query":  runL1LightweightQuery,
		"core_l1_snapshot_integrity": runSnapshotIntegrityCheck,
	}
}

func runHealthCheck(ctx context.Context, baseURL *url.URL, client *http.Client, _ runnerOptions) checkResult {
	return runJSONGetCheck(ctx, baseURL, client, "/health", func(body map[string]any) error {
		if ok, present := body["ok"].(bool); present && !ok {
			return errors.New("CORE health reported ok=false")
		}
		if status, _ := body["status"].(string); strings.EqualFold(status, "down") {
			return errors.New("CORE health reported down")
		}
		return nil
	})
}

func runReadinessCheck(ctx context.Context, baseURL *url.URL, client *http.Client, _ runnerOptions) checkResult {
	return runJSONGetCheck(ctx, baseURL, client, "/health/ready", func(body map[string]any) error {
		ready, ok := body["ready"].(bool)
		if !ok || !ready {
			return errors.New("CORE readiness reported ready=false")
		}
		return nil
	})
}

func runL1LightweightQuery(ctx context.Context, baseURL *url.URL, client *http.Client, _ runnerOptions) checkResult {
	return runJSONGetCheck(ctx, baseURL, client, "/viewer/memory/layers?include_l2=false&limit=1", func(body map[string]any) error {
		for _, field := range []string{"l0", "l1", "l3"} {
			if _, ok := body[field]; !ok {
				return fmt.Errorf("L1 lightweight response is missing %s", field)
			}
		}
		return nil
	})
}

func runSnapshotIntegrityCheck(ctx context.Context, _ *url.URL, _ *http.Client, options runnerOptions) checkResult {
	started := time.Now()
	result := checkResult{Target: "conversation_l1 snapshot", ObservedAt: started.UTC().Format(time.RFC3339)}
	if strings.TrimSpace(options.SnapshotDir) == "" {
		result.Status = "failed"
		result.Message = "backup phase requires --snapshot-dir"
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	checker, err := exec.LookPath("rencrow-storage-restore-check")
	if err != nil {
		result.Status = "failed"
		result.Message = "rencrow-storage-restore-check is unavailable"
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	command := exec.CommandContext(ctx, checker, options.SnapshotDir) // fixed owner checker; no arbitrary command args
	output, err := command.Output()
	if err != nil {
		result.Status = "failed"
		result.Message = fmt.Sprintf("snapshot restore check failed: %v", err)
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	result.Status = "passed"
	result.Message = strings.TrimSpace(string(output))
	result.DurationMS = time.Since(started).Milliseconds()
	return result
}

func runJSONGetCheck(ctx context.Context, baseURL *url.URL, client *http.Client, path string, validate func(map[string]any) error) checkResult {
	started := time.Now()
	result := checkResult{Target: path, ObservedAt: started.UTC().Format(time.RFC3339)}
	requestURL := *baseURL
	parsedPath, err := url.Parse(path)
	if err != nil {
		result.Status = "failed"
		result.Message = fmt.Sprintf("invalid fixed check path: %v", err)
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	requestURL.Path = parsedPath.Path
	requestURL.RawPath = ""
	requestURL.RawQuery = parsedPath.RawQuery
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		result.Status = "failed"
		result.Message = fmt.Sprintf("create request: %v", err)
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	resp, err := client.Do(req)
	if err != nil {
		result.Status = "failed"
		result.Message = fmt.Sprintf("request failed: %v", err)
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	defer resp.Body.Close()
	result.HTTPStatus = resp.StatusCode
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		result.Status = "failed"
		result.Message = fmt.Sprintf("read response: %v", err)
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	if len(body) > maxResponseBytes {
		result.Status = "failed"
		result.Message = "response exceeds bounded size"
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		result.Status = "failed"
		result.Message = fmt.Sprintf("unexpected HTTP status %d", resp.StatusCode)
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	var decoded map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if err := decoder.Decode(&decoded); err != nil {
		result.Status = "failed"
		result.Message = fmt.Sprintf("decode JSON response: %v", err)
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	if err := ensureJSONEOF(decoder); err != nil {
		result.Status = "failed"
		result.Message = fmt.Sprintf("decode JSON response: %v", err)
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	if decoded == nil {
		result.Status = "failed"
		result.Message = "JSON response must be an object"
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	if err := ensureJSONEOF(decoder); err != nil {
		result.Status = "failed"
		result.Message = fmt.Sprintf("response contains trailing JSON: %v", err)
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	if err := validate(decoded); err != nil {
		result.Status = "failed"
		result.Message = err.Error()
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	result.Status = "passed"
	result.DurationMS = time.Since(started).Milliseconds()
	return result
}

// loadCheckRequest preserves the old helper contract for callers that want a
// request using the manifest's declared phase. runRunner deliberately calls
// projectCheckRequest with its explicit requested phase instead.
func loadCheckRequest(path string) (checkRequest, error) {
	manifest, err := loadCheckManifest(path)
	if err != nil {
		return checkRequest{}, err
	}
	return projectCheckRequest(manifest, manifest.Phase)
}

func loadCheckManifest(path string) (ownerManifest, error) {
	data, err := readManifestFile(path)
	if err != nil {
		return ownerManifest{}, err
	}
	var envelope ownerManifestEnvelope
	if err := decodeStrictJSON(data, &envelope); err != nil {
		return ownerManifest{}, err
	}
	if strings.TrimSpace(envelope.Purpose) == "" || strings.TrimSpace(envelope.Phase) == "" {
		return ownerManifest{}, errors.New("manifest purpose and phase are required")
	}
	if len(envelope.Checks) == 0 || len(envelope.Checks) > maxManifestChecks {
		return ownerManifest{}, fmt.Errorf("manifest checks must contain 1..%d entries", maxManifestChecks)
	}
	if envelope.SchemaVersion != runnerSchemaVersion && envelope.SchemaVersion != ownerManifestVersion {
		return ownerManifest{}, fmt.Errorf("manifest schema_version must be %d or %d", runnerSchemaVersion, ownerManifestVersion)
	}
	if envelope.SchemaVersion == ownerManifestVersion && !validManifestPhase(envelope.Phase) {
		return ownerManifest{}, fmt.Errorf("manifest has invalid phase %q", envelope.Phase)
	}

	checks := make([]manifestCheck, 0, len(envelope.Checks))
	seen := make(map[string]struct{}, len(envelope.Checks))
	for index, rawCheck := range envelope.Checks {
		check, err := decodeManifestCheck(rawCheck, envelope.SchemaVersion)
		if err != nil {
			return ownerManifest{}, fmt.Errorf("manifest checks[%d]: %w", index, err)
		}
		if _, exists := seen[check.CheckID]; exists {
			return ownerManifest{}, fmt.Errorf("manifest contains duplicate check %q", check.CheckID)
		}
		seen[check.CheckID] = struct{}{}
		checks = append(checks, check)
	}
	return ownerManifest{
		SchemaVersion: envelope.SchemaVersion,
		Purpose:       envelope.Purpose,
		Phase:         envelope.Phase,
		Checks:        checks,
	}, nil
}

// projectCheckRequest strips v2-only metadata and sets the planner phase from
// the caller. The manifest's phase is descriptive/default metadata; it must
// not constrain an explicit phase requested by the runner.
func projectCheckRequest(manifest ownerManifest, requestedPhase string) (checkRequest, error) {
	requestedPhase = strings.TrimSpace(requestedPhase)
	if requestedPhase == "" {
		return checkRequest{}, errors.New("requested phase is required")
	}
	return checkRequest{
		SchemaVersion: runnerSchemaVersion,
		Purpose:       manifest.Purpose,
		Phase:         requestedPhase,
		Checks:        append([]manifestCheck(nil), manifest.Checks...),
	}, nil
}

var plannerManifestFields = map[string]struct{}{
	"check_id": {}, "guarantee_id": {}, "owner": {}, "purpose": {}, "target": {}, "phase": {},
	"consumer": {}, "failure_action": {}, "cost": {}, "safety_gate": {}, "replacement_check_id": {}, "evidence": {},
}

var v2ManifestFields = map[string]struct{}{
	"check_id": {}, "guarantee_id": {}, "owner": {}, "purpose": {}, "target": {}, "phase": {},
	"consumer": {}, "failure_action": {}, "cost": {}, "safety_gate": {}, "replacement_check_id": {}, "evidence": {},
	"coverage": {}, "executor": {}, "receipt_schema": {}, "surfaces": {},
}

func decodeManifestCheck(raw json.RawMessage, schemaVersion int) (manifestCheck, error) {
	var fields map[string]json.RawMessage
	if err := decodeJSON(raw, &fields); err != nil {
		return manifestCheck{}, fmt.Errorf("check must be an object: %w", err)
	}
	if fields == nil {
		return manifestCheck{}, errors.New("check must be an object")
	}
	allowed := plannerManifestFields
	if schemaVersion == ownerManifestVersion {
		allowed = v2ManifestFields
	}
	for key := range fields {
		if _, ok := allowed[key]; !ok {
			return manifestCheck{}, fmt.Errorf("unknown field %q", key)
		}
	}

	var check manifestCheck
	if err := decodeJSON(raw, &check); err != nil {
		return manifestCheck{}, fmt.Errorf("decode planner fields: %w", err)
	}
	if evidenceRaw, ok := fields["evidence"]; ok {
		var value evidence
		if err := decodeStrictJSON(evidenceRaw, &value); err != nil {
			return manifestCheck{}, fmt.Errorf("decode evidence: %w", err)
		}
	}
	if strings.TrimSpace(check.CheckID) == "" {
		return manifestCheck{}, errors.New("check_id is required")
	}
	if schemaVersion != ownerManifestVersion {
		return check, nil
	}

	if _, err := validateStringList(fields, "coverage", true); err != nil {
		return manifestCheck{}, err
	}
	if _, err := decodeOwnerExecutor(fields); err != nil {
		return manifestCheck{}, err
	}
	receiptSchema, err := decodeRequiredString(fields, "receipt_schema")
	if err != nil {
		return manifestCheck{}, err
	}
	if receiptSchema != ownerReceiptSchema {
		return manifestCheck{}, fmt.Errorf("receipt_schema must be %q, got %q", ownerReceiptSchema, receiptSchema)
	}
	if _, err := validateStringList(fields, "surfaces", false); err != nil {
		return manifestCheck{}, err
	}
	return check, nil
}

func decodeOwnerExecutor(fields map[string]json.RawMessage) (ownerExecutor, error) {
	raw, ok := fields["executor"]
	if !ok {
		return ownerExecutor{}, errors.New("executor is required")
	}
	var object map[string]json.RawMessage
	if err := decodeJSON(raw, &object); err != nil {
		return ownerExecutor{}, fmt.Errorf("executor must be an object: %w", err)
	}
	if object == nil {
		return ownerExecutor{}, errors.New("executor must be an object")
	}
	if len(object) != 2 {
		return ownerExecutor{}, errors.New("executor must contain exactly kind and command_id")
	}
	if _, ok := object["kind"]; !ok {
		return ownerExecutor{}, errors.New("executor.kind is required")
	}
	if _, ok := object["command_id"]; !ok {
		return ownerExecutor{}, errors.New("executor.command_id is required")
	}
	var executor ownerExecutor
	if err := decodeStrictJSON(raw, &executor); err != nil {
		return ownerExecutor{}, fmt.Errorf("decode executor: %w", err)
	}
	if executor.Kind != "owner_cli" {
		return ownerExecutor{}, fmt.Errorf("executor.kind must be owner_cli, got %q", executor.Kind)
	}
	if strings.TrimSpace(executor.CommandID) == "" || !stableCommandIDPattern.MatchString(executor.CommandID) {
		return ownerExecutor{}, fmt.Errorf("executor.command_id must be a non-empty stable id: %q", executor.CommandID)
	}
	return executor, nil
}

func decodeRequiredString(fields map[string]json.RawMessage, key string) (string, error) {
	raw, ok := fields[key]
	if !ok {
		return "", fmt.Errorf("%s is required", key)
	}
	var value string
	if err := decodeJSON(raw, &value); err != nil {
		return "", fmt.Errorf("%s must be a string: %w", key, err)
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s must be non-empty", key)
	}
	return value, nil
}

func validateStringList(fields map[string]json.RawMessage, key string, required bool) ([]string, error) {
	raw, ok := fields[key]
	if !ok {
		if required {
			return nil, fmt.Errorf("%s is required", key)
		}
		return nil, nil
	}
	var values []string
	if err := decodeJSON(raw, &values); err != nil {
		return nil, fmt.Errorf("%s must be an array of strings: %w", key, err)
	}
	if len(values) == 0 {
		if required {
			return nil, fmt.Errorf("%s must be non-empty", key)
		}
		return nil, fmt.Errorf("%s must be non-empty when present", key)
	}
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("%s[%d] must be non-empty", key, index)
		}
		if _, exists := seen[value]; exists {
			return nil, fmt.Errorf("%s contains duplicate value %q", key, value)
		}
		seen[value] = struct{}{}
	}
	return values, nil
}

func validManifestPhase(phase string) bool {
	switch phase {
	case "any", "startup", "runtime", "deploy", "backup", "diagnostic":
		return true
	default:
		return false
	}
}

func readManifestFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxManifestBytes {
		return nil, fmt.Errorf("manifest exceeds %d bytes", maxManifestBytes)
	}
	return data, nil
}

func writePlannerInput(request checkRequest) (string, func(), error) {
	data, err := json.Marshal(request)
	if err != nil {
		return "", func() {}, err
	}
	if len(data) > maxManifestBytes {
		return "", func() {}, errors.New("projected planner input exceeds bounded size")
	}
	file, err := os.CreateTemp("", "rencrow-check-plan-runner-*.json")
	if err != nil {
		return "", func() {}, err
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

func decodeJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func decodeStrictJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return ensureJSONEOF(decoder)
}

func decodePlan(data []byte) (checkPlan, error) {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var plan checkPlan
	if err := decoder.Decode(&plan); err != nil {
		return checkPlan{}, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return checkPlan{}, err
	}
	return plan, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("input must contain exactly one JSON value")
		}
		return err
	}
	return nil
}

func validatePlan(request checkRequest, plan checkPlan, now time.Time) error {
	if plan.SchemaVersion != runnerSchemaVersion {
		return fmt.Errorf("plan schema_version must be %d", runnerSchemaVersion)
	}
	if plan.Purpose != request.Purpose || plan.Phase != request.Phase {
		return fmt.Errorf("plan purpose/phase does not match manifest")
	}
	if !plan.EvaluatedAt.Equal(now.UTC()) {
		return fmt.Errorf("plan evaluated_at does not match requested UTC")
	}
	if strings.TrimSpace(plan.PlanRevision) == "" {
		return errors.New("plan revision is required")
	}
	if plan.Status == "blocked" {
		return fmt.Errorf("check plan is blocked: %s", strings.Join(plan.Errors, "; "))
	}
	if plan.Status != "ready" {
		return fmt.Errorf("unknown check plan status %q", plan.Status)
	}
	if len(plan.Errors) != 0 {
		return errors.New("ready check plan contains errors")
	}
	known := make(map[string]struct{}, len(request.Checks))
	for _, check := range request.Checks {
		if _, exists := known[check.CheckID]; exists {
			return fmt.Errorf("manifest contains duplicate check %q", check.CheckID)
		}
		known[check.CheckID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(request.Checks))
	for _, group := range []struct {
		name  string
		items []planItem
	}{
		{name: "included", items: plan.Included},
		{name: "excluded", items: plan.Excluded},
		{name: "deferred", items: plan.Deferred},
	} {
		for _, item := range group.items {
			if _, ok := known[item.CheckID]; !ok {
				if group.name == "included" {
					return fmt.Errorf("included check %q is not allowlisted", item.CheckID)
				}
				return fmt.Errorf("%s check %q is not declared by the manifest", group.name, item.CheckID)
			}
			if _, duplicate := seen[item.CheckID]; duplicate {
				return fmt.Errorf("check %q appears more than once in plan", item.CheckID)
			}
			seen[item.CheckID] = struct{}{}
		}
	}
	if len(seen) != len(known) {
		return errors.New("plan does not classify every manifest check")
	}
	return nil
}

func invokePlanner(ctx context.Context, options runnerOptions, args []string) ([]byte, error) {
	if options.Planner != nil {
		return options.Planner(ctx, args)
	}
	planner := strings.TrimSpace(options.PlannerPath)
	if planner == "" {
		planner = defaultPlanner
	}
	if filepath.Base(planner) != defaultPlanner && filepath.Base(planner) != defaultPlanner+".exe" {
		return nil, fmt.Errorf("planner executable %q is not the fixed rencrow-check-plan command", planner)
	}
	command := exec.CommandContext(ctx, planner, args...)
	output, err := command.Output()
	if err != nil {
		return output, err
	}
	return output, nil
}

func validateCoreURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("only http/https loopback URLs are allowed")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("core URL must not contain userinfo, query, or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, errors.New("core URL must not contain a path prefix")
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		parsed.Path = ""
		return parsed, nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("core URL host must be localhost or a loopback IP")
	}
	parsed.Path = ""
	return parsed, nil
}

func finishRunner(out io.Writer, receipt runnerReceipt, err error) (runnerReceipt, error) {
	if err != nil {
		receipt.Error = truncateMessage(err.Error())
		if receipt.Status == "" {
			receipt.Status = "blocked"
		}
	}
	if out != nil {
		encoder := json.NewEncoder(out)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(receipt)
	}
	return receipt, err
}

func truncateMessage(message string) string {
	if len(message) <= maxReceiptMessage {
		return message
	}
	return message[:maxReceiptMessage]
}

// sortedCheckIDs is used by tests and diagnostics to make allowlist review
// stable without exposing arbitrary executor registration.
func sortedCheckIDs() []string {
	ids := make([]string, 0, len(coreCheckAllowlist()))
	for id := range coreCheckAllowlist() {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
