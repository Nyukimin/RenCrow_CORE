package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	verifierSchemaVersion           = 1
	verifierReceiptSchema           = "rencrow.check-receipt.v1"
	verifierOwner                   = "RenCrow_CORE"
	verifierManifestV2              = 2
	defaultVerifierURL              = "http://127.0.0.1:18790"
	canonicalCoreUnit               = "rencrow.service"
	canonicalCorePort               = 18790
	maxVerifierFileBytes            = 4 << 20
	maxVerifierBodyBytes            = 64 << 10
	maxVerifierMessage              = 256
	verifierEvidenceFreshnessWindow = 5 * time.Minute
	verifierExitPassed              = 0
	verifierExitFailed              = 10
	verifierExitBlocked             = 20
	verifierExitUnverified          = 30
	verifierExitCLIError            = 2
)

var stableVerifierID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// verifierReceipt is the cross-module receipt-v1 contract. Keep this type
// deliberately small: command output is an audit handoff, not a diagnostic
// dump or a place for credentials and raw user data.
type verifierReceipt struct {
	SchemaVersion   int      `json:"schema_version"`
	ReceiptSchema   string   `json:"receipt_schema"`
	CheckID         string   `json:"check_id"`
	GuaranteeID     string   `json:"guarantee_id"`
	Owner           string   `json:"owner"`
	Status          string   `json:"status"`
	ObservedAt      string   `json:"observed_at"`
	RouteOrTarget   string   `json:"route_or_target"`
	EvidenceRefs    []string `json:"evidence_refs"`
	FailureBoundary string   `json:"failure_boundary"`
}

type verifierOutcome struct {
	Status          string
	FailureBoundary string
	Evidence        map[string]any
}

type ownerManifest struct {
	SchemaVersion int             `json:"schema_version"`
	Purpose       string          `json:"purpose"`
	Phase         string          `json:"phase"`
	Checks        []manifestCheck `json:"checks"`
}

type manifestCheck struct {
	CheckID       string           `json:"check_id"`
	GuaranteeID   string           `json:"guarantee_id"`
	Owner         string           `json:"owner"`
	Purpose       string           `json:"purpose"`
	Target        string           `json:"target"`
	Phase         string           `json:"phase"`
	Consumer      string           `json:"consumer,omitempty"`
	FailureAction string           `json:"failure_action,omitempty"`
	Cost          string           `json:"cost"`
	SafetyGate    bool             `json:"safety_gate"`
	Coverage      []string         `json:"coverage"`
	Executor      manifestExecutor `json:"executor"`
	ReceiptSchema string           `json:"receipt_schema"`
	Surfaces      []string         `json:"surfaces,omitempty"`
	ReplacementID string           `json:"replacement_check_id,omitempty"`
	Evidence      *json.RawMessage `json:"evidence,omitempty"`
}

type manifestExecutor struct {
	Kind      string `json:"kind"`
	CommandID string `json:"command_id"`
}

type verifierOptions struct {
	ManifestPath      string
	CheckID           string
	ObservedAt        time.Time
	EvidenceDir       string
	CoreURL           string
	SnapshotDir       string
	RestoreCheck      string
	CatalogPath       string
	WorkspacePath     string
	InstalledArtifact string
	StampedChecker    string
	Python            string
	Unit              string
	ConfigPath        string
	RequestEvidence   string
	JournalSince      time.Time
	ActorTokenFile    string
	RequestID         string
	ActorMessage      string
}

type verifierCommandResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Err      error
}

type verifierDependencies struct {
	HTTPClient *http.Client
	RunCommand func(context.Context, string, []string) verifierCommandResult
	LookPath   func(string) (string, error)
	Readlink   func(string) (string, error)
	Platform   func() string
}

func defaultVerifierDependencies() verifierDependencies {
	return verifierDependencies{
		HTTPClient: &http.Client{Timeout: 8 * time.Second},
		RunCommand: runVerifierCommand,
		LookPath:   exec.LookPath,
		Readlink:   os.Readlink,
		Platform:   func() string { return runtime.GOOS },
	}
}

func normalizeVerifierDependencies(deps verifierDependencies) verifierDependencies {
	defaults := defaultVerifierDependencies()
	if deps.HTTPClient == nil {
		deps.HTTPClient = defaults.HTTPClient
	}
	if deps.RunCommand == nil {
		deps.RunCommand = defaults.RunCommand
	}
	if deps.LookPath == nil {
		deps.LookPath = defaults.LookPath
	}
	if deps.Readlink == nil {
		deps.Readlink = defaults.Readlink
	}
	if deps.Platform == nil {
		deps.Platform = defaults.Platform
	}
	return deps
}

func runVerifierCommand(ctx context.Context, name string, args []string) verifierCommandResult {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
	}
	command := exec.CommandContext(ctx, name, args...)
	var stdout, stderr boundedVerifierBuffer
	stdout.limit = maxVerifierFileBytes
	stderr.limit = maxVerifierFileBytes
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	result := verifierCommandResult{Stdout: stdout.String(), Stderr: stderr.String(), Err: err}
	if stdout.overflow || stderr.overflow {
		result.Err = errors.New("owner command output exceeds bounded size")
		result.ExitCode = -1
		return result
	}
	if err == nil {
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	} else {
		result.ExitCode = -1
	}
	return result
}

type boundedVerifierBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (buffer *boundedVerifierBuffer) Write(value []byte) (int, error) {
	remaining := buffer.limit - buffer.Len()
	if remaining <= 0 {
		buffer.overflow = true
		return len(value), nil
	}
	if len(value) > remaining {
		_, _ = buffer.Buffer.Write(value[:remaining])
		buffer.overflow = true
		return len(value), nil
	}
	return buffer.Buffer.Write(value)
}

var fixedVerifierCommands = map[string]string{
	"core-startup-phase-trace":                 "core_startup_phase_trace",
	"core-health":                              "core_health",
	"core-readiness":                           "core_readiness",
	"core-l1-lightweight-query":                "core_l1_lightweight_query",
	"core-l1-snapshot-integrity":               "core_l1_snapshot_integrity",
	"core-deploy-identity-chain":               "core_deploy_identity_chain",
	"core-runtime-identity-lifecycle-security": "core_runtime_identity_lifecycle_security",
	"core-canonical-actor-e2e":                 "core_canonical_actor_e2e",
}

func checkIDForCommand(commandID string) (string, bool) {
	checkID, ok := fixedVerifierCommands[commandID]
	return checkID, ok
}

type verifierHandler func(context.Context, verifierOptions, manifestCheck, verifierDependencies) verifierOutcome

func verifierForCommand(commandID string) (verifierHandler, bool) {
	switch commandID {
	case "core-startup-phase-trace":
		return runStartupPhaseTrace, true
	case "core-health":
		return runHealth, true
	case "core-readiness":
		return runReadiness, true
	case "core-l1-lightweight-query":
		return runL1LightweightQuery, true
	case "core-l1-snapshot-integrity":
		return runL1SnapshotIntegrity, true
	case "core-deploy-identity-chain":
		return runDeployIdentityChain, true
	case "core-runtime-identity-lifecycle-security":
		return runRuntimeIdentityLifecycleSecurity, true
	case "core-canonical-actor-e2e":
		return runCanonicalActorE2E, true
	default:
		return nil, false
	}
}

func loadOwnerManifest(path string) (ownerManifest, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return ownerManifest{}, errors.New("manifest path is required")
	}
	data, err := readRegularFile(path, maxVerifierFileBytes)
	if err != nil {
		return ownerManifest{}, fmt.Errorf("read manifest: %w", err)
	}
	var manifest ownerManifest
	if err := decodeStrictJSON(data, &manifest); err != nil {
		return ownerManifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if manifest.SchemaVersion != verifierManifestV2 {
		return ownerManifest{}, fmt.Errorf("manifest schema_version must be %d", verifierManifestV2)
	}
	if strings.TrimSpace(manifest.Purpose) == "" || strings.TrimSpace(manifest.Phase) == "" {
		return ownerManifest{}, errors.New("manifest purpose and phase are required")
	}
	if len(manifest.Checks) == 0 || len(manifest.Checks) > 128 {
		return ownerManifest{}, errors.New("manifest checks must contain 1..128 entries")
	}
	seenChecks := make(map[string]struct{}, len(manifest.Checks))
	seenCommands := make(map[string]struct{}, len(manifest.Checks))
	for index, check := range manifest.Checks {
		if err := validateManifestCheck(check); err != nil {
			return ownerManifest{}, fmt.Errorf("manifest checks[%d]: %w", index, err)
		}
		if _, exists := seenChecks[check.CheckID]; exists {
			return ownerManifest{}, fmt.Errorf("manifest contains duplicate check_id %q", check.CheckID)
		}
		seenChecks[check.CheckID] = struct{}{}
		if _, exists := seenCommands[check.Executor.CommandID]; exists {
			return ownerManifest{}, fmt.Errorf("manifest contains duplicate command_id %q", check.Executor.CommandID)
		}
		seenCommands[check.Executor.CommandID] = struct{}{}
		allowlistedCheck, ok := checkIDForCommand(check.Executor.CommandID)
		if !ok {
			return ownerManifest{}, fmt.Errorf("executor.command_id %q is not allowlisted", check.Executor.CommandID)
		}
		if allowlistedCheck != check.CheckID {
			return ownerManifest{}, fmt.Errorf("executor.command_id %q does not own check_id %q", check.Executor.CommandID, check.CheckID)
		}
	}
	return manifest, nil
}

func validateManifestCheck(check manifestCheck) error {
	for name, value := range map[string]string{
		"check_id": check.CheckID, "guarantee_id": check.GuaranteeID, "owner": check.Owner,
		"purpose": check.Purpose, "target": check.Target, "phase": check.Phase, "cost": check.Cost,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if check.Owner != verifierOwner {
		return fmt.Errorf("owner must be %q, got %q", verifierOwner, check.Owner)
	}
	if !stableVerifierID.MatchString(check.CheckID) || !stableVerifierID.MatchString(check.GuaranteeID) {
		return errors.New("check and guarantee ids must be stable identifiers")
	}
	if check.Executor.Kind != "owner_cli" {
		return fmt.Errorf("executor.kind must be owner_cli, got %q", check.Executor.Kind)
	}
	if !stableVerifierID.MatchString(check.Executor.CommandID) {
		return errors.New("executor.command_id must be a stable identifier")
	}
	if check.ReceiptSchema != verifierReceiptSchema {
		return fmt.Errorf("receipt_schema must be %q", verifierReceiptSchema)
	}
	if len(check.Coverage) == 0 {
		return errors.New("coverage is required and must be non-empty")
	}
	for index, value := range check.Coverage {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("coverage[%d] must be non-empty", index)
		}
	}
	for _, coverage := range check.Coverage {
		if strings.TrimSpace(coverage) == "security_exposure" && !check.SafetyGate {
			return errors.New("security_exposure requires safety_gate=true")
		}
	}
	return nil
}

func decodeStrictJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("input must contain exactly one JSON value")
		}
		return err
	}
	return nil
}

func readRegularFile(path string, limit int) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("path must be a regular non-symlink file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return data, nil
}

func parseVerifierObservedAt(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, errors.New("observed-at is required")
	}
	if !strings.HasSuffix(raw, "Z") {
		return time.Time{}, errors.New("observed-at must be RFC3339 UTC ending in Z")
	}
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("observed-at must be RFC3339 UTC: %w", err)
	}
	if _, offset := value.Zone(); offset != 0 {
		return time.Time{}, errors.New("observed-at must use UTC")
	}
	return value.UTC(), nil
}

// validateVerifierEvidenceFreshness binds file-backed evidence to the frozen
// observation time supplied to this invocation. Live HTTP/systemd checks do
// not use this helper because their state is observed during the invocation;
// captured evidence must prove that it was observed no more than five minutes
// before the receipt and never after it.
func validateVerifierEvidenceFreshness(fields map[string]any, observedAt time.Time) error {
	if fields == nil {
		return errors.New("evidence observed_at is required")
	}
	for _, key := range []string{"observed_at", "observedAt"} {
		raw, exists := fields[key]
		if !exists {
			continue
		}
		text, ok := raw.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return errors.New("evidence observed_at is invalid")
		}
		evidenceAt, err := parseVerifierObservedAt(text)
		if err != nil {
			return fmt.Errorf("evidence observed_at: %w", err)
		}
		return validateVerifierEvidenceTime(evidenceAt, observedAt)
	}
	return errors.New("evidence observed_at is required")
}

func validateVerifierEvidenceTime(evidenceAt, observedAt time.Time) error {
	observedAt = observedAt.UTC()
	if observedAt.IsZero() {
		return errors.New("requested observed_at is required")
	}
	evidenceAt = evidenceAt.UTC()
	if evidenceAt.Before(observedAt.Add(-verifierEvidenceFreshnessWindow)) {
		return fmt.Errorf("evidence is stale: observed_at=%s requested=%s", evidenceAt.Format(time.RFC3339Nano), observedAt.Format(time.RFC3339Nano))
	}
	if evidenceAt.After(observedAt) {
		return fmt.Errorf("evidence is from the future: observed_at=%s requested=%s", evidenceAt.Format(time.RFC3339Nano), observedAt.Format(time.RFC3339Nano))
	}
	return nil
}

func runVerifierCLI(ctx context.Context, args []string, out, errOut io.Writer, deps verifierDependencies) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(args) == 0 || args[0] != "run" {
		if errOut != nil {
			fmt.Fprintln(errOut, "usage: rencrow-core-verify run --manifest PATH --check-id ID --observed-at RFC3339-UTC --evidence-dir DIR")
		}
		return verifierExitCLIError
	}
	flags := flag.NewFlagSet("rencrow-core-verify run", flag.ContinueOnError)
	flags.SetOutput(errOut)
	manifestPath := flags.String("manifest", "", "CORE owner manifest")
	checkID := flags.String("check-id", "", "declared CORE check id")
	observedAtRaw := flags.String("observed-at", "", "frozen RFC3339 UTC observation time")
	evidenceDir := flags.String("evidence-dir", "", "bounded evidence directory")
	coreURL := flags.String("core-url", defaultVerifierURL, "canonical loopback CORE URL")
	snapshotDir := flags.String("snapshot-dir", "", "explicit offline snapshot directory")
	snapshotAlias := flags.String("snapshot", "", "alias for --snapshot-dir")
	restoreCheck := flags.String("restore-check", "", "fixed rencrow-storage-restore-check path")
	restoreAlias := flags.String("restore-checker", "", "alias for --restore-check")
	catalogPath := flags.String("catalog", "", "explicit ecosystem catalog")
	catalogAlias := flags.String("catalog-manifest", "", "alias for --catalog")
	workspacePath := flags.String("workspace", "", "explicit catalog workspace root")
	workspaceAlias := flags.String("workspace-root", "", "alias for --workspace")
	installedArtifact := flags.String("installed-artifact", "", "explicit installed CORE artifact")
	artifactAlias := flags.String("artifact", "", "alias for --installed-artifact")
	stampedChecker := flags.String("stamped-checker", "", "optional existing stamped deployment checker")
	checkerAlias := flags.String("deployed-checker", "", "alias for --stamped-checker")
	python := flags.String("python", "", "Python executable for the optional stamped checker")
	unit := flags.String("unit", canonicalCoreUnit, "canonical CORE user systemd unit")
	configPath := flags.String("config", "", "expected active CORE config path")
	configAlias := flags.String("config-path", "", "alias for --config")
	requestEvidence := flags.String("request-evidence", "", "prior canonical request receipt for startup trace")
	startupEvidenceAlias := flags.String("startup-request-evidence", "", "alias for --request-evidence")
	requestReceiptAlias := flags.String("request-receipt", "", "alias for --request-evidence")
	journalSinceRaw := flags.String("journal-since", "", "RFC3339 UTC lower bound for startup journal")
	actorTokenFile := flags.String("actor-token-file", "", "owner-only bearer token file for canonical Agent route")
	authTokenAlias := flags.String("auth-token-file", "", "alias for --actor-token-file")
	tokenAlias := flags.String("token-file", "", "alias for --actor-token-file")
	requestID := flags.String("request-id", "", "authenticated canonical request id")
	actorRequestIDAlias := flags.String("actor-request-id", "", "alias for --request-id")
	actorMessage := flags.String("actor-message", "", "bounded canonical Agent diagnostic message")
	messageAlias := flags.String("message", "", "alias for --actor-message")
	if err := flags.Parse(args[1:]); err != nil {
		return verifierExitCLIError
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(errOut, "positional arguments are not accepted")
		return verifierExitCLIError
	}
	for name, value := range map[string]string{
		"--manifest": *manifestPath, "--check-id": *checkID,
		"--observed-at": *observedAtRaw, "--evidence-dir": *evidenceDir,
	} {
		if strings.TrimSpace(value) == "" {
			fmt.Fprintf(errOut, "%s is required\n", name)
			return verifierExitCLIError
		}
	}
	observedAt, err := parseVerifierObservedAt(*observedAtRaw)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return verifierExitCLIError
	}
	manifest, err := loadOwnerManifest(*manifestPath)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return verifierExitCLIError
	}
	var check manifestCheck
	found := false
	for _, candidate := range manifest.Checks {
		if candidate.CheckID == strings.TrimSpace(*checkID) {
			check = candidate
			found = true
			break
		}
	}
	if !found {
		fmt.Fprintf(errOut, "check-id %q is not declared by the manifest\n", *checkID)
		return verifierExitCLIError
	}
	handler, ok := verifierForCommand(check.Executor.CommandID)
	if !ok {
		fmt.Fprintf(errOut, "executor.command_id %q is not implemented\n", check.Executor.CommandID)
		return verifierExitCLIError
	}

	if strings.TrimSpace(*snapshotDir) == "" {
		*snapshotDir = strings.TrimSpace(*snapshotAlias)
	}
	if strings.TrimSpace(*restoreCheck) == "" {
		*restoreCheck = strings.TrimSpace(*restoreAlias)
	}
	if strings.TrimSpace(*catalogPath) == "" {
		*catalogPath = strings.TrimSpace(*catalogAlias)
	}
	if strings.TrimSpace(*workspacePath) == "" {
		*workspacePath = strings.TrimSpace(*workspaceAlias)
	}
	if strings.TrimSpace(*installedArtifact) == "" {
		*installedArtifact = strings.TrimSpace(*artifactAlias)
	}
	if strings.TrimSpace(*stampedChecker) == "" {
		*stampedChecker = strings.TrimSpace(*checkerAlias)
	}
	if strings.TrimSpace(*configPath) == "" {
		*configPath = strings.TrimSpace(*configAlias)
	}
	if strings.TrimSpace(*requestEvidence) == "" {
		*requestEvidence = strings.TrimSpace(*startupEvidenceAlias)
	}
	if strings.TrimSpace(*requestEvidence) == "" {
		*requestEvidence = strings.TrimSpace(*requestReceiptAlias)
	}
	if strings.TrimSpace(*actorTokenFile) == "" {
		*actorTokenFile = strings.TrimSpace(*authTokenAlias)
	}
	if strings.TrimSpace(*actorTokenFile) == "" {
		*actorTokenFile = strings.TrimSpace(*tokenAlias)
	}
	if strings.TrimSpace(*requestID) == "" {
		*requestID = strings.TrimSpace(*actorRequestIDAlias)
	}
	if strings.TrimSpace(*actorMessage) == "" {
		*actorMessage = strings.TrimSpace(*messageAlias)
	}
	var journalSince time.Time
	if strings.TrimSpace(*journalSinceRaw) != "" {
		journalSince, err = parseVerifierObservedAt(*journalSinceRaw)
		if err != nil {
			fmt.Fprintln(errOut, err)
			return verifierExitCLIError
		}
	}
	options := verifierOptions{
		ManifestPath: *manifestPath, CheckID: *checkID, ObservedAt: observedAt,
		EvidenceDir: *evidenceDir, CoreURL: *coreURL, SnapshotDir: *snapshotDir,
		RestoreCheck: *restoreCheck, CatalogPath: *catalogPath, WorkspacePath: *workspacePath,
		InstalledArtifact: *installedArtifact, StampedChecker: *stampedChecker,
		Python: *python, Unit: *unit, ConfigPath: *configPath,
		RequestEvidence: *requestEvidence, JournalSince: journalSince,
		ActorTokenFile: *actorTokenFile, RequestID: *requestID, ActorMessage: *actorMessage,
	}
	deps = normalizeVerifierDependencies(deps)
	outcome := handler(ctx, options, check, deps)
	if outcome.Status == "" {
		outcome.Status = "blocked"
		outcome.FailureBoundary = "verifier returned no status"
	}
	if !validVerifierStatus(outcome.Status) {
		outcome.Status = "blocked"
		outcome.FailureBoundary = "verifier returned an invalid status"
	}
	outcome.FailureBoundary = truncateVerifierMessage(outcome.FailureBoundary)
	receipt := verifierReceipt{
		SchemaVersion: verifierSchemaVersion, ReceiptSchema: verifierReceiptSchema,
		CheckID: check.CheckID, GuaranteeID: check.GuaranteeID, Owner: check.Owner,
		Status: outcome.Status, ObservedAt: observedAt.Format(time.RFC3339Nano),
		RouteOrTarget: check.Target, EvidenceRefs: []string{}, FailureBoundary: outcome.FailureBoundary,
	}
	if receipt.ObservedAt == "" {
		receipt.ObservedAt = observedAt.Format(time.RFC3339)
	}
	evidence := outcome.Evidence
	if evidence == nil {
		evidence = map[string]any{}
	}
	evidence["schema_version"] = verifierSchemaVersion
	evidence["receipt_schema"] = verifierReceiptSchema
	evidence["check_id"] = check.CheckID
	evidence["status"] = outcome.Status
	evidence["observed_at"] = receipt.ObservedAt
	evidence["failure_boundary"] = outcome.FailureBoundary
	if ref, writeErr := writeVerifierEvidence(*evidenceDir, check.CheckID, observedAt, evidence); writeErr == nil {
		receipt.EvidenceRefs = []string{"relative:" + ref}
	} else if outcome.Status == "passed" || outcome.Status == "not_applicable" {
		receipt.Status = "blocked"
		receipt.FailureBoundary = truncateVerifierMessage("evidence output unavailable")
	}
	if err := writeVerifierReceipt(out, receipt); err != nil {
		fmt.Fprintln(errOut, err)
		return verifierExitCLIError
	}
	return verifierExitCode(receipt.Status)
}

func validVerifierStatus(status string) bool {
	switch status {
	case "passed", "failed", "blocked", "unverified", "not_applicable":
		return true
	default:
		return false
	}
}

func verifierExitCode(status string) int {
	switch status {
	case "passed", "not_applicable":
		return verifierExitPassed
	case "failed":
		return verifierExitFailed
	case "unverified":
		return verifierExitUnverified
	case "blocked":
		return verifierExitBlocked
	default:
		return verifierExitCLIError
	}
}

func truncateVerifierMessage(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > maxVerifierMessage {
		return value[:maxVerifierMessage]
	}
	return value
}

func writeVerifierReceipt(out io.Writer, receipt verifierReceipt) error {
	if out == nil {
		return errors.New("receipt output is unavailable")
	}
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(receipt)
}

func writeVerifierEvidence(dir, checkID string, observedAt time.Time, value map[string]any) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", errors.New("evidence directory is required")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("evidence path must be a regular directory")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(data) > maxVerifierFileBytes {
		return "", errors.New("evidence exceeds bounded size")
	}
	stamp := observedAt.UTC().Format("20060102T150405.000000000Z")
	base := fmt.Sprintf("core-%s-%s", strings.ReplaceAll(checkID, "_", "-"), stamp)
	for attempt := 0; attempt < 10; attempt++ {
		name := base + ".json"
		if attempt > 0 {
			name = fmt.Sprintf("%s-%d.json", base, attempt)
		}
		path := filepath.Join(dir, name)
		file, openErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if openErr != nil {
			if errors.Is(openErr, os.ErrExist) {
				continue
			}
			return "", openErr
		}
		_, writeErr := file.Write(data)
		if writeErr == nil {
			writeErr = file.Sync()
		}
		closeErr := file.Close()
		if writeErr != nil {
			return "", writeErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		return name, nil
	}
	return "", errors.New("evidence filename collision limit exceeded")
}

func validateVerifierCoreURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("CORE URL must use http or https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("CORE URL must not contain userinfo, query, or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, errors.New("CORE URL must not contain a path prefix")
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		parsed.Path, parsed.RawPath = "", ""
		return parsed, nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("CORE URL host must be localhost or a loopback IP")
	}
	parsed.Path, parsed.RawPath = "", ""
	return parsed, nil
}

func joinVerifierRoute(base *url.URL, route string) string {
	copyURL := *base
	parsed, _ := url.Parse(route)
	copyURL.Path = parsed.Path
	copyURL.RawPath = ""
	copyURL.RawQuery = parsed.RawQuery
	return copyURL.String()
}

type verifierHTTPResponse struct {
	StatusCode int
	Body       []byte
	JSON       map[string]any
	Err        error
}

func verifierHTTPJSON(ctx context.Context, client *http.Client, method, endpoint string, headers map[string]string, body []byte) verifierHTTPResponse {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return verifierHTTPResponse{Err: err}
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return verifierHTTPResponse{Err: err}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxVerifierBodyBytes+1))
	if err != nil {
		return verifierHTTPResponse{StatusCode: response.StatusCode, Err: err}
	}
	result := verifierHTTPResponse{StatusCode: response.StatusCode, Body: raw}
	if len(raw) > maxVerifierBodyBytes {
		result.Err = errors.New("HTTP response exceeds bounded size")
		return result
	}
	if len(raw) == 0 {
		return result
	}
	if err := decodeStrictJSON(raw, &result.JSON); err != nil {
		result.Err = err
	}
	return result
}

func verifierHTTPStatusKind(response verifierHTTPResponse) string {
	if response.Err != nil && response.StatusCode == 0 {
		return "unavailable"
	}
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed ||
		response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return "unavailable"
	}
	return "failed"
}

func sha256Text(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func sha256File(path string) (string, int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", 0, errors.New("path must be a regular non-symlink file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	digest := sha256.New()
	count, err := io.Copy(digest, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(digest.Sum(nil)), count, nil
}

func safeBase(path string) string {
	path = filepath.Base(filepath.Clean(path))
	if path == "." || path == string(filepath.Separator) || path == "" {
		return ""
	}
	return path
}

func parseKeyValueOutput(output string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return values
}

func parsePositivePID(raw string) (int64, error) {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("systemd MainPID is unavailable")
	}
	return value, nil
}
