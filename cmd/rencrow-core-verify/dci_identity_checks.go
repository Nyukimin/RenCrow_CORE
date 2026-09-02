package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	dcipersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/dci"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/dcimigration"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	dciIdentityFixedQuery            = "RenCrow identity architecture"
	dciIdentityOperation             = "dci_identity_acceptance"
	dciIdentityResponseSchema        = "rencrow.agent-ops.dci-identity-acceptance/v1"
	dciIdentityPreEvidenceSchema     = "rencrow.core-verify.dci-identity-pre-restart/v1"
	dciIdentityPostEvidenceSchema    = "rencrow.core-verify.dci-identity-post-restart/v1"
	dciIdentityFinalEvidenceSchema   = "rencrow.identity.dci-final/v1"
	dciIdentityPreCommandID          = "core-dci-identity-pre-restart"
	dciIdentityPostCommandID         = "core-dci-identity-post-restart"
	dciIdentityFinalCommandID        = "core-dci-identity-final"
	dciIdentityPrePhase              = "pre_restart"
	dciIdentityPostPhase             = "post_restart"
	dciIdentityFinalPhase            = "final"
	dciIdentityEvidenceMaxBytes      = 64 << 10
	dciIdentityMaxServicePID         = int64(^uint32(0) >> 1)
	dciIdentityRoute                 = "/v1/agent/ops"
	dciIdentityUnavailableBoundary   = "canonical DCI identity route is unavailable"
	dciIdentityResponseBoundary      = "canonical DCI identity response is invalid"
	dciIdentityReplayBoundary        = "canonical DCI identity replay contract is invalid"
	dciIdentityPriorBoundary         = "canonical DCI pre-restart evidence is invalid"
	dciIdentityGenerationBoundary    = "canonical CORE service generation did not change"
	dciIdentityOldGenerationBoundary = "canonical CORE old service generation is still present"
)

var (
	dciIdentityResponseKeys = []string{
		"schema_version", "status", "request_id", "agent_id", "role", "operation",
		"action_id", "trace_id", "first_write_replay", "second_write_replay",
		"event_count", "step_count", "evidence_count", "current_projection_count",
		"archive_projection_count", "event_graph_sha256",
	}
	dciIdentityPreEvidenceKeys = []string{
		"schema_version", "receipt_schema", "check_id", "status", "observed_at", "failure_boundary",
		"evidence_schema", "command_id", "phase",
		"response_schema_version", "response_status", "request_id", "agent_id", "role", "operation",
		"action_id", "trace_id", "first_write_replay", "second_write_replay", "event_count", "step_count",
		"evidence_count", "current_projection_count", "archive_projection_count", "event_graph_sha256",
		"fixture_sha256", "service_main_pid", "service_generation_sha256", "artifact_sha256", "config_sha256",
		"listener_ready", "readiness_ready", "response_facts_sha256",
	}
	dciIdentityPostEvidenceKeys = []string{
		"schema_version", "receipt_schema", "check_id", "status", "observed_at", "failure_boundary",
		"evidence_schema", "command_id", "phase",
		"response_schema_version", "response_status", "request_id", "agent_id", "role", "operation",
		"action_id", "trace_id", "first_write_replay", "second_write_replay", "event_count", "step_count",
		"evidence_count", "current_projection_count", "archive_projection_count", "event_graph_sha256",
		"fixture_sha256", "service_generation_sha256", "artifact_sha256", "config_sha256",
		"listener_ready", "readiness_ready", "response_facts_sha256", "old_generation_absent",
		"pre_restart_evidence_sha256",
	}
)

type dciIdentityResponse struct {
	SchemaVersion          string `json:"schema_version"`
	Status                 string `json:"status"`
	RequestID              string `json:"request_id"`
	AgentID                string `json:"agent_id"`
	Role                   string `json:"role"`
	Operation              string `json:"operation"`
	ActionID               string `json:"action_id"`
	TraceID                string `json:"trace_id"`
	FirstWriteReplay       bool   `json:"first_write_replay"`
	SecondWriteReplay      bool   `json:"second_write_replay"`
	EventCount             int    `json:"event_count"`
	StepCount              int    `json:"step_count"`
	EvidenceCount          int    `json:"evidence_count"`
	CurrentProjectionCount int    `json:"current_projection_count"`
	ArchiveProjectionCount int    `json:"archive_projection_count"`
	EventGraphSHA256       string `json:"event_graph_sha256"`
}

type dciIdentityEvidence struct {
	SchemaVersion            int    `json:"schema_version"`
	ReceiptSchema            string `json:"receipt_schema"`
	CheckID                  string `json:"check_id"`
	Status                   string `json:"status"`
	ObservedAt               string `json:"observed_at"`
	FailureBoundary          string `json:"failure_boundary"`
	EvidenceSchema           string `json:"evidence_schema"`
	CommandID                string `json:"command_id"`
	Phase                    string `json:"phase"`
	ResponseSchemaVersion    string `json:"response_schema_version"`
	ResponseStatus           string `json:"response_status"`
	RequestID                string `json:"request_id"`
	AgentID                  string `json:"agent_id"`
	Role                     string `json:"role"`
	Operation                string `json:"operation"`
	ActionID                 string `json:"action_id"`
	TraceID                  string `json:"trace_id"`
	FirstWriteReplay         bool   `json:"first_write_replay"`
	SecondWriteReplay        bool   `json:"second_write_replay"`
	EventCount               int    `json:"event_count"`
	StepCount                int    `json:"step_count"`
	EvidenceCount            int    `json:"evidence_count"`
	CurrentProjectionCount   int    `json:"current_projection_count"`
	ArchiveProjectionCount   int    `json:"archive_projection_count"`
	EventGraphSHA256         string `json:"event_graph_sha256"`
	FixtureSHA256            string `json:"fixture_sha256"`
	ServiceMainPID           int64  `json:"service_main_pid"`
	ServiceGenerationSHA256  string `json:"service_generation_sha256"`
	ArtifactSHA256           string `json:"artifact_sha256"`
	ConfigSHA256             string `json:"config_sha256"`
	ListenerReady            bool   `json:"listener_ready"`
	ReadinessReady           bool   `json:"readiness_ready"`
	ResponseFactsSHA256      string `json:"response_facts_sha256"`
	OldGenerationAbsent      bool   `json:"old_generation_absent,omitempty"`
	PreRestartEvidenceSHA256 string `json:"pre_restart_evidence_sha256,omitempty"`
}

type dciIdentityRuntimeObservation struct {
	ServiceMainPID          int64
	ServiceGenerationSHA256 string
	ArtifactSHA256          string
	ConfigSHA256            string
	ConfigPath              string
}

func runDCIIdentityPreRestart(ctx context.Context, options verifierOptions, check manifestCheck, deps verifierDependencies) verifierOutcome {
	deps = normalizeVerifierDependencies(deps)
	runtimeObservation, outcome := observeDCIIdentityRuntime(ctx, options, check, deps)
	if outcome.Status != "" {
		return outcome
	}
	requestID, ok := dciIdentityRequestID(options.RequestID, options.ObservedAt, true)
	if !ok {
		return verifierOutcome{Status: "blocked", FailureBoundary: "canonical DCI identity request id is invalid"}
	}
	response, responseFactsSHA256, outcome := performDCIIdentityRequest(ctx, options, runtimeObservation.ConfigPath, requestID, deps)
	if outcome.Status != "" {
		return outcome
	}
	if response.FirstWriteReplay || !response.SecondWriteReplay {
		return verifierOutcome{Status: "failed", FailureBoundary: dciIdentityReplayBoundary}
	}
	return verifierOutcome{Status: "passed", Evidence: dciIdentityEvidenceValue(
		response, responseFactsSHA256, runtimeObservation, dciIdentityPreEvidenceSchema,
		dciIdentityPreCommandID, dciIdentityPrePhase, false, "",
	)}
}

func runDCIIdentityPostRestart(ctx context.Context, options verifierOptions, check manifestCheck, deps verifierDependencies) verifierOutcome {
	deps = normalizeVerifierDependencies(deps)
	prior, priorEvidenceSHA256, outcome := loadDCIIdentityPreEvidence(options.DCIPreRestartEvidence, options.ObservedAt, deps)
	if outcome.Status != "" {
		return outcome
	}
	if suppliedRequestID := options.RequestID; strings.TrimSpace(suppliedRequestID) != "" {
		if suppliedRequestID != strings.TrimSpace(suppliedRequestID) || !verifierActorRequestIDPattern.MatchString(suppliedRequestID) || len([]byte(suppliedRequestID)) > verifierMaxActorRequestIDBytes || suppliedRequestID != prior.RequestID {
			return verifierOutcome{Status: "failed", FailureBoundary: "canonical DCI identity request id does not match pre-restart evidence"}
		}
	}
	runtimeObservation, outcome := observeDCIIdentityRuntime(ctx, options, check, deps)
	if outcome.Status != "" {
		return outcome
	}
	if runtimeObservation.ServiceMainPID == prior.ServiceMainPID || runtimeObservation.ServiceGenerationSHA256 == prior.ServiceGenerationSHA256 {
		return verifierOutcome{Status: "failed", FailureBoundary: dciIdentityGenerationBoundary}
	}
	if runtimeObservation.ArtifactSHA256 != prior.ArtifactSHA256 || runtimeObservation.ConfigSHA256 != prior.ConfigSHA256 {
		return verifierOutcome{Status: "failed", FailureBoundary: "canonical CORE artifact or config changed across restart"}
	}
	if outcome = requireDCIIdentityOldGenerationAbsent(prior.ServiceMainPID, deps); outcome.Status != "" {
		return outcome
	}
	response, responseFactsSHA256, outcome := performDCIIdentityRequest(ctx, options, runtimeObservation.ConfigPath, prior.RequestID, deps)
	if outcome.Status != "" {
		return outcome
	}
	if !response.FirstWriteReplay || !response.SecondWriteReplay {
		return verifierOutcome{Status: "failed", FailureBoundary: dciIdentityReplayBoundary}
	}
	if !sameDCIIdentityStableFacts(prior.response(), response) {
		return verifierOutcome{Status: "failed", FailureBoundary: "canonical DCI identity facts changed across restart"}
	}
	return verifierOutcome{Status: "passed", Evidence: dciIdentityEvidenceValue(
		response, responseFactsSHA256, runtimeObservation, dciIdentityPostEvidenceSchema,
		dciIdentityPostCommandID, dciIdentityPostPhase, true, priorEvidenceSHA256,
	)}
}

type dciDeployReceipt struct {
	SchemaVersion   int      `json:"schema_version"`
	ReceiptID       string   `json:"receipt_id"`
	Component       string   `json:"component"`
	BinaryPath      string   `json:"binary_path"`
	SourcePath      string   `json:"source_path,omitempty"`
	FromRevision    string   `json:"from_revision"`
	TargetRevision  string   `json:"target_revision"`
	Phase           string   `json:"phase"`
	Outcome         string   `json:"outcome"`
	RollbackOutcome string   `json:"rollback_outcome"`
	RunningUnits    []string `json:"running_units"`
	BackupPath      string   `json:"backup_path"`
	StartedAt       string   `json:"started_at"`
	FinishedAt      string   `json:"finished_at"`
}

func runDCIIdentityFinal(ctx context.Context, options verifierOptions, check manifestCheck, deps verifierDependencies) verifierOutcome {
	deps = normalizeVerifierDependencies(deps)
	post, postRaw, outcome := loadDCIIdentityPostEvidence(options.DCIPostRestartEvidence, deps)
	if outcome.Status != "" {
		return outcome
	}
	postAt, err := time.Parse(time.RFC3339Nano, post.ObservedAt)
	if err != nil || postAt.After(options.ObservedAt) || postAt.Before(options.ObservedAt.Add(-24*time.Hour)) {
		return verifierOutcome{Status: "blocked", FailureBoundary: "canonical DCI post-restart evidence is stale or invalid"}
	}
	pre, preRaw, outcome := loadDCIIdentityPreEvidenceAt(options.DCIPreRestartEvidence, postAt, deps)
	if outcome.Status != "" {
		return outcome
	}
	if post.PreRestartEvidenceSHA256 != sha256Text(string(preRaw)) || !sameDCIIdentityStableFacts(pre.response(), post.response()) ||
		post.ArtifactSHA256 != pre.ArtifactSHA256 || post.ConfigSHA256 != pre.ConfigSHA256 || post.ServiceGenerationSHA256 == pre.ServiceGenerationSHA256 ||
		!post.FirstWriteReplay || !post.SecondWriteReplay || !post.OldGenerationAbsent {
		return verifierOutcome{Status: "failed", FailureBoundary: "canonical DCI pre/post evidence chain is invalid"}
	}
	service, serviceSHA, err := loadServiceCutoverReceipt(options.DCIServiceCutoverReceipt)
	if err != nil {
		return verifierOutcome{Status: "blocked", FailureBoundary: "canonical DCI service-cutover receipt is invalid"}
	}
	cutover, cutoverSHA, err := loadCutoverReceipt(options.DCICutoverReceipt)
	if err != nil {
		return verifierOutcome{Status: "blocked", FailureBoundary: "canonical DCI cutover receipt is invalid"}
	}
	if service.Status != dcimigration.CutoverStatusApplied || service.CutoverSubreceiptStatus != dcimigration.CutoverStatusApplied ||
		service.CutoverTerminalStatus != dcimigration.CutoverStatusApplied || service.CutoverSubreceiptSHA256 != cutoverSHA ||
		service.NewRuntimeSHA256 != service.FinalRunning.RuntimeSHA256 || service.BuildReceiptSHA256 != cutover.BuildReceiptSHA256 ||
		cutover.Status != dcimigration.CutoverStatusApplied || cutover.QuickCheckOK != 1 || cutover.ForeignKeyViolations != 0 ||
		cutover.SidecarZero != 1 || cutover.LegacyKeyMarkers != 0 || cutover.OrphanActionRefs != 0 {
		return verifierOutcome{Status: "failed", FailureBoundary: "canonical DCI migration receipt chain is invalid"}
	}
	deployRevision, deploySHA, err := loadDCIDeployRevision(options.DCIDeployReceiptLog, deps)
	if err != nil {
		return verifierOutcome{Status: "blocked", FailureBoundary: "canonical DCI deployment receipt chain is invalid"}
	}
	runtimeObservation, outcome := observeDCIIdentityRuntime(ctx, options, check, deps)
	if outcome.Status != "" {
		return outcome
	}
	if runtimeObservation.ArtifactSHA256 != post.ArtifactSHA256 || runtimeObservation.ConfigSHA256 != post.ConfigSHA256 {
		return verifierOutcome{Status: "failed", FailureBoundary: "canonical CORE runtime changed after DCI post-restart evidence"}
	}
	journal := deps.RunCommand(ctx, "journalctl", []string{"--user", "-u", canonicalCoreUnit, "--since", formatJournalctlTime(postAt), "--until", formatJournalctlTime(options.ObservedAt), "--priority=warning", "--no-pager", "--output=cat"})
	if commandUnavailable(journal) || journal.ExitCode != 0 || journal.Err != nil {
		return verifierOutcome{Status: "blocked", FailureBoundary: "canonical CORE journal review is unavailable"}
	}
	if strings.TrimSpace(journal.Stdout) != "" {
		return verifierOutcome{Status: "failed", FailureBoundary: "canonical CORE warning or error log was observed"}
	}
	return verifierOutcome{Status: "passed", Evidence: map[string]any{
		"evidence_schema": dciIdentityFinalEvidenceSchema, "command_id": dciIdentityFinalCommandID, "phase": dciIdentityFinalPhase,
		"pre_evidence_sha256": sha256Text(string(preRaw)), "post_evidence_sha256": sha256Text(string(postRaw)),
		"service_cutover_receipt_sha256": serviceSHA, "cutover_receipt_sha256": cutoverSHA, "deploy_receipt_log_sha256": deploySHA,
		"deploy_revision": deployRevision, "request_id": post.RequestID, "agent_id": post.AgentID, "action_id": post.ActionID, "trace_id": post.TraceID,
		"event_graph_sha256": post.EventGraphSHA256, "event_count": post.EventCount, "step_count": post.StepCount, "evidence_count": post.EvidenceCount,
		"current_projection_count": post.CurrentProjectionCount, "archive_projection_count": post.ArchiveProjectionCount,
		"artifact_sha256": post.ArtifactSHA256, "config_sha256": post.ConfigSHA256, "service_generation_sha256": runtimeObservation.ServiceGenerationSHA256,
		"listener_ready": true, "readiness_ready": true, "old_generation_absent": true, "migration_integrity": true, "deploy_pair_verified": true, "journal_clean": true,
	}}
}

func loadDCIIdentityPreEvidenceAt(path string, at time.Time, deps verifierDependencies) (dciIdentityEvidence, []byte, verifierOutcome) {
	evidence, _, outcome := loadDCIIdentityPreEvidence(path, at, deps)
	if outcome.Status != "" {
		return dciIdentityEvidence{}, nil, outcome
	}
	raw, err := readRegularFile(path, dciIdentityEvidenceMaxBytes)
	if err != nil {
		return dciIdentityEvidence{}, nil, verifierOutcome{Status: "blocked", FailureBoundary: dciIdentityPriorBoundary}
	}
	return evidence, raw, verifierOutcome{}
}

func loadDCIIdentityPostEvidence(path string, deps verifierDependencies) (dciIdentityEvidence, []byte, verifierOutcome) {
	raw, err := readOwnerOnlyEvidence(path, dciIdentityEvidenceMaxBytes, deps)
	if err != nil {
		return dciIdentityEvidence{}, nil, verifierOutcome{Status: "blocked", FailureBoundary: "canonical DCI post-restart evidence is unavailable"}
	}
	var evidence dciIdentityEvidence
	values, err := decodeDCIIdentityObject(raw, &evidence, dciIdentityPostEvidenceKeys)
	if err != nil || validateDCIIdentityPostEvidenceTypes(values) != nil {
		return dciIdentityEvidence{}, nil, verifierOutcome{Status: "blocked", FailureBoundary: "canonical DCI post-restart evidence is invalid"}
	}
	if evidence.SchemaVersion != verifierSchemaVersion || evidence.ReceiptSchema != verifierReceiptSchema || evidence.CheckID != "core_dci_identity_post_restart" || evidence.Status != "passed" || evidence.FailureBoundary != "" || evidence.EvidenceSchema != dciIdentityPostEvidenceSchema || evidence.CommandID != dciIdentityPostCommandID || evidence.Phase != dciIdentityPostPhase || !evidence.ListenerReady || !evidence.ReadinessReady || !evidence.OldGenerationAbsent || !isLowercaseSHA256(evidence.PreRestartEvidenceSHA256) || !isLowercaseSHA256(evidence.ServiceGenerationSHA256) || !isLowercaseSHA256(evidence.ArtifactSHA256) || !isLowercaseSHA256(evidence.ConfigSHA256) {
		return dciIdentityEvidence{}, nil, verifierOutcome{Status: "blocked", FailureBoundary: "canonical DCI post-restart evidence is invalid"}
	}
	facts, err := validateDCIIdentityResponseFacts(evidence.response(), evidence.RequestID)
	if err != nil || facts != evidence.ResponseFactsSHA256 || !evidence.FirstWriteReplay || !evidence.SecondWriteReplay {
		return dciIdentityEvidence{}, nil, verifierOutcome{Status: "blocked", FailureBoundary: "canonical DCI post-restart evidence is invalid"}
	}
	return evidence, raw, verifierOutcome{}
}

func validateDCIIdentityPostEvidenceTypes(fields map[string]json.RawMessage) error {
	for _, key := range []string{"receipt_schema", "check_id", "status", "observed_at", "failure_boundary", "evidence_schema", "command_id", "phase", "response_schema_version", "response_status", "request_id", "agent_id", "role", "operation", "action_id", "trace_id", "event_graph_sha256", "fixture_sha256", "service_generation_sha256", "artifact_sha256", "config_sha256", "response_facts_sha256", "pre_restart_evidence_sha256"} {
		if err := unmarshalDCIIdentityNonNull(fields[key], new(string)); err != nil {
			return err
		}
	}
	for _, key := range []string{"first_write_replay", "second_write_replay", "listener_ready", "readiness_ready", "old_generation_absent"} {
		if err := unmarshalDCIIdentityNonNull(fields[key], new(bool)); err != nil {
			return err
		}
	}
	for _, key := range []string{"schema_version", "event_count", "step_count", "evidence_count", "current_projection_count", "archive_projection_count"} {
		if err := unmarshalDCIIdentityNonNull(fields[key], new(int64)); err != nil {
			return err
		}
	}
	return nil
}

func readOwnerOnlyEvidence(path string, limit int, deps verifierDependencies) ([]byte, error) {
	deps = normalizeVerifierDependencies(deps)
	path = strings.TrimSpace(path)
	info, err := deps.Lstat(path)
	if err != nil || info == nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || (deps.Platform() != "windows" && info.Mode().Perm() != 0o600) {
		return nil, errors.New("owner evidence unavailable")
	}
	return readRegularFile(path, limit)
}

func loadServiceCutoverReceipt(path string) (dcimigration.ServiceCutoverReceipt, string, error) {
	before, _, err := sha256File(path)
	if err != nil {
		return dcimigration.ServiceCutoverReceipt{}, "", err
	}
	receipt, err := dcimigration.ReadValidatedServiceCutoverReceipt(path)
	if err != nil {
		return receipt, "", err
	}
	after, _, err := sha256File(path)
	if err != nil || before != after {
		return receipt, "", errors.New("service cutover receipt changed during validation")
	}
	return receipt, after, nil
}
func loadCutoverReceipt(path string) (dcimigration.CutoverReceipt, string, error) {
	before, _, err := sha256File(path)
	if err != nil {
		return dcimigration.CutoverReceipt{}, "", err
	}
	receipt, err := dcimigration.ReadValidatedCutoverReceipt(path)
	if err != nil {
		return receipt, "", err
	}
	after, _, err := sha256File(path)
	if err != nil || before != after {
		return receipt, "", errors.New("cutover receipt changed during validation")
	}
	return receipt, after, nil
}

func loadDCIDeployRevision(path string, deps verifierDependencies) (string, string, error) {
	data, err := readOwnerOnlyEvidence(path, maxVerifierFileBytes, deps)
	if err != nil {
		return "", "", err
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	pairs := map[string]map[string]bool{}
	order := []string{}
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var receipt dciDeployReceipt
		if err := decodeStrictJSON(line, &receipt); err != nil {
			return "", "", err
		}
		if receipt.SchemaVersion != 1 || receipt.Component != "core" || receipt.Outcome != "success" || receipt.Phase != "complete" || !isFullGitSHA(receipt.TargetRevision) {
			continue
		}
		name := filepath.Base(receipt.BinaryPath)
		if name != "rencrow" && name != "rencrow-core-verify" {
			continue
		}
		if pairs[receipt.TargetRevision] == nil {
			pairs[receipt.TargetRevision] = map[string]bool{}
			order = append(order, receipt.TargetRevision)
		}
		pairs[receipt.TargetRevision][name] = true
	}
	if err := scanner.Err(); err != nil {
		return "", "", err
	}
	for i := len(order) - 1; i >= 0; i-- {
		rev := order[i]
		if pairs[rev]["rencrow"] && pairs[rev]["rencrow-core-verify"] {
			return rev, sha256Text(string(data)), nil
		}
	}
	return "", "", errors.New("paired deploy revision unavailable")
}

func isFullGitSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func observeDCIIdentityRuntime(ctx context.Context, options verifierOptions, check manifestCheck, deps verifierDependencies) (dciIdentityRuntimeObservation, verifierOutcome) {
	if outcome := requireLinuxSystemd(options, deps); outcome.Status != "" {
		return dciIdentityRuntimeObservation{}, outcome
	}
	snapshot, outcome := readSystemdService(ctx, options, deps)
	if outcome.Status != "" {
		return dciIdentityRuntimeObservation{}, outcome
	}
	if snapshot.MainPID <= 0 || snapshot.MainPID > dciIdentityMaxServicePID {
		return dciIdentityRuntimeObservation{}, verifierOutcome{Status: "blocked", FailureBoundary: "canonical CORE systemd MainPID is unavailable"}
	}
	if outcome = validateServiceState(snapshot); outcome.Status != "" {
		return dciIdentityRuntimeObservation{}, outcome
	}
	if outcome = validateCanonicalUnitText(snapshot.UnitText); outcome.Status != "" {
		return dciIdentityRuntimeObservation{}, outcome
	}
	if outcome = validateProcessIdentity(snapshot, options, deps); outcome.Status != "" && outcome.Status != "passed" {
		return dciIdentityRuntimeObservation{}, outcome
	}
	if outcome = validateConfigIdentity(snapshot, options); outcome.Status != "" && outcome.Status != "passed" {
		return dciIdentityRuntimeObservation{}, outcome
	}
	if outcome = observeCoreListener(ctx, snapshot, deps); outcome.Status != "passed" {
		return dciIdentityRuntimeObservation{}, outcome
	}
	readiness := runReadiness(ctx, options, check, deps)
	if readiness.Status != "passed" {
		return dciIdentityRuntimeObservation{}, verifierOutcome{Status: readiness.Status, FailureBoundary: readiness.FailureBoundary}
	}
	artifactPath := strings.TrimSpace(options.InstalledArtifact)
	if artifactPath == "" {
		artifactPath = snapshot.ExecPath
	}
	artifactPath = expandHomePlaceholder(artifactPath)
	configPath := strings.TrimSpace(options.ConfigPath)
	if configPath == "" {
		configPath = snapshot.ConfigPath
	}
	configPath = expandHomePlaceholder(configPath)
	artifactSHA256, _, err := sha256File(artifactPath)
	if err != nil || !isLowercaseSHA256(artifactSHA256) {
		return dciIdentityRuntimeObservation{}, verifierOutcome{Status: "blocked", FailureBoundary: "canonical CORE artifact hash is unavailable"}
	}
	configSHA256, _, err := sha256File(configPath)
	if err != nil || !isLowercaseSHA256(configSHA256) {
		return dciIdentityRuntimeObservation{}, verifierOutcome{Status: "blocked", FailureBoundary: "canonical CORE config hash is unavailable"}
	}
	generation := dciIdentityServiceGenerationSHA256(snapshot.MainPID, artifactSHA256, configSHA256)
	return dciIdentityRuntimeObservation{
		ServiceMainPID: snapshot.MainPID, ServiceGenerationSHA256: generation,
		ArtifactSHA256: artifactSHA256, ConfigSHA256: configSHA256, ConfigPath: configPath,
	}, verifierOutcome{}
}

func dciIdentityServiceGenerationSHA256(mainPID int64, artifactSHA256, configSHA256 string) string {
	return sha256Text(fmt.Sprintf("unit=%s\nmain_pid=%d\nartifact_sha256=%s\nconfig_sha256=%s", canonicalCoreUnit, mainPID, artifactSHA256, configSHA256))
}

func dciIdentityRequestID(raw string, observedAt time.Time, deriveIfEmpty bool) (string, bool) {
	requestID := raw
	if strings.TrimSpace(requestID) == "" {
		if !deriveIfEmpty {
			return "", false
		}
		requestID = "core-dci-" + observedAt.UTC().Format("20060102T150405.000000000Z")
	} else if requestID != strings.TrimSpace(requestID) {
		return "", false
	}
	return requestID, verifierActorRequestIDPattern.MatchString(requestID) && len([]byte(requestID)) <= verifierMaxActorRequestIDBytes
}

func performDCIIdentityRequest(ctx context.Context, options verifierOptions, configPath, requestID string, deps verifierDependencies) (dciIdentityResponse, string, verifierOutcome) {
	deps = normalizeVerifierDependencies(deps)
	tokenPath := strings.TrimSpace(options.ActorTokenFile)
	if tokenPath == "" {
		var err error
		tokenPath, err = readCanonicalActorTokenPath(configPath)
		if err != nil {
			return dciIdentityResponse{}, "", verifierOutcome{Status: "blocked", FailureBoundary: "canonical Agent credentials are unavailable"}
		}
	}
	token, err := readVerifierActorToken(tokenPath, deps)
	if err != nil {
		return dciIdentityResponse{}, "", verifierOutcome{Status: "blocked", FailureBoundary: "canonical Agent credentials are unavailable"}
	}
	baseURL, err := validateVerifierCoreURL(options.CoreURL)
	if err != nil {
		return dciIdentityResponse{}, "", verifierOutcome{Status: "blocked", FailureBoundary: "canonical CORE URL is not an allowed loopback endpoint"}
	}
	body, err := json.Marshal(struct {
		Operation string `json:"operation"`
		Query     string `json:"query"`
	}{Operation: dciIdentityOperation, Query: dciIdentityFixedQuery})
	if err != nil {
		return dciIdentityResponse{}, "", verifierOutcome{Status: "blocked", FailureBoundary: "canonical DCI identity request could not be encoded"}
	}
	actorClient := verifierActorHTTPClient(deps.HTTPClient)
	response := verifierHTTPJSON(ctx, &actorClient, http.MethodPost, coreEndpoint(baseURL, dciIdentityRoute), map[string]string{
		"Accept":                        "application/json",
		"Content-Type":                  "application/json",
		"Authorization":                 "Bearer " + token,
		"X-Request-ID":                  requestID,
		"X-RenCrow-Client":              "RenCrow_CMD",
		"X-RenCrow-Interaction-Profile": "agent-ops",
	}, body)
	failureEvidence := map[string]any{"route": dciIdentityRoute, "http_status": response.StatusCode}
	if response.Err != nil && response.StatusCode == 0 {
		return dciIdentityResponse{}, "", verifierOutcome{Status: "blocked", FailureBoundary: dciIdentityUnavailableBoundary, Evidence: failureEvidence}
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return dciIdentityResponse{}, "", verifierOutcome{Status: "blocked", FailureBoundary: dciIdentityUnavailableBoundary, Evidence: failureEvidence}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return dciIdentityResponse{}, "", verifierOutcome{Status: "failed", FailureBoundary: "canonical DCI identity route returned an unexpected HTTP status", Evidence: failureEvidence}
	}
	if response.Err != nil {
		return dciIdentityResponse{}, "", verifierOutcome{Status: "failed", FailureBoundary: dciIdentityResponseBoundary, Evidence: failureEvidence}
	}
	fields, factsSHA256, err := decodeDCIIdentityResponse(response.Body, requestID)
	if err != nil {
		return dciIdentityResponse{}, "", verifierOutcome{Status: "failed", FailureBoundary: dciIdentityResponseBoundary, Evidence: failureEvidence}
	}
	return fields, factsSHA256, verifierOutcome{}
}

func decodeDCIIdentityResponse(raw []byte, requestID string) (dciIdentityResponse, string, error) {
	var fields dciIdentityResponse
	values, err := decodeDCIIdentityObject(raw, &fields, dciIdentityResponseKeys)
	if err != nil {
		return dciIdentityResponse{}, "", err
	}
	if err := validateDCIIdentityResponseTypes(values); err != nil {
		return dciIdentityResponse{}, "", err
	}
	facts, err := validateDCIIdentityResponseFacts(fields, requestID)
	if err != nil {
		return dciIdentityResponse{}, "", err
	}
	return fields, facts, nil
}

func validateDCIIdentityResponseTypes(fields map[string]json.RawMessage) error {
	for _, key := range []string{
		"schema_version", "status", "request_id", "agent_id", "role", "operation", "action_id", "trace_id", "event_graph_sha256",
	} {
		if err := unmarshalDCIIdentityNonNull(fields[key], new(string)); err != nil {
			return err
		}
	}
	for _, key := range []string{"first_write_replay", "second_write_replay"} {
		if err := unmarshalDCIIdentityNonNull(fields[key], new(bool)); err != nil {
			return err
		}
	}
	for _, key := range []string{"event_count", "step_count", "evidence_count", "current_projection_count", "archive_projection_count"} {
		if err := unmarshalDCIIdentityNonNull(fields[key], new(int)); err != nil {
			return err
		}
	}
	return nil
}

func unmarshalDCIIdentityNonNull(raw json.RawMessage, destination any) error {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("null is not a valid DCI identity field")
	}
	return json.Unmarshal(raw, destination)
}

func decodeDCIIdentityObject(raw []byte, destination any, expectedKeys []string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := first.(json.Delim)
	if !ok || delim != '{' {
		return nil, errors.New("object required")
	}
	fields := make(map[string]json.RawMessage, len(expectedKeys))
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("object key required")
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, errors.New("duplicate field")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	last, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	lastDelim, ok := last.(json.Delim)
	if !ok || lastDelim != '}' || !hasExactJSONKeys(fields, expectedKeys) {
		return nil, errors.New("fields are not exact")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("trailing value")
		}
		return nil, err
	}
	if err := decodeStrictJSON(raw, destination); err != nil {
		return nil, err
	}
	return fields, nil
}

func hasExactJSONKeys(fields map[string]json.RawMessage, expected []string) bool {
	if len(fields) != len(expected) {
		return false
	}
	for _, key := range expected {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	return true
}

func dciIdentityEvidenceValue(response dciIdentityResponse, responseFactsSHA256 string, runtimeObservation dciIdentityRuntimeObservation, evidenceSchema, commandID, phase string, oldGenerationAbsent bool, priorEvidenceSHA256 string) map[string]any {
	evidence := map[string]any{
		"evidence_schema":           evidenceSchema,
		"command_id":                commandID,
		"phase":                     phase,
		"response_schema_version":   response.SchemaVersion,
		"response_status":           response.Status,
		"request_id":                response.RequestID,
		"agent_id":                  response.AgentID,
		"role":                      response.Role,
		"operation":                 response.Operation,
		"action_id":                 response.ActionID,
		"trace_id":                  response.TraceID,
		"first_write_replay":        response.FirstWriteReplay,
		"second_write_replay":       response.SecondWriteReplay,
		"event_count":               response.EventCount,
		"step_count":                response.StepCount,
		"evidence_count":            response.EvidenceCount,
		"current_projection_count":  response.CurrentProjectionCount,
		"archive_projection_count":  response.ArchiveProjectionCount,
		"event_graph_sha256":        response.EventGraphSHA256,
		"fixture_sha256":            sha256Text(dciIdentityFixedQuery),
		"service_generation_sha256": runtimeObservation.ServiceGenerationSHA256,
		"artifact_sha256":           runtimeObservation.ArtifactSHA256,
		"config_sha256":             runtimeObservation.ConfigSHA256,
		"listener_ready":            true,
		"readiness_ready":           true,
		"response_facts_sha256":     responseFactsSHA256,
	}
	if oldGenerationAbsent {
		evidence["old_generation_absent"] = true
		evidence["pre_restart_evidence_sha256"] = priorEvidenceSHA256
	} else {
		evidence["service_main_pid"] = runtimeObservation.ServiceMainPID
	}
	return evidence
}

func (e dciIdentityEvidence) response() dciIdentityResponse {
	return dciIdentityResponse{
		SchemaVersion: e.ResponseSchemaVersion, Status: e.ResponseStatus, RequestID: e.RequestID,
		AgentID: e.AgentID, Role: e.Role, Operation: e.Operation, ActionID: e.ActionID, TraceID: e.TraceID,
		FirstWriteReplay: e.FirstWriteReplay, SecondWriteReplay: e.SecondWriteReplay,
		EventCount: e.EventCount, StepCount: e.StepCount, EvidenceCount: e.EvidenceCount,
		CurrentProjectionCount: e.CurrentProjectionCount, ArchiveProjectionCount: e.ArchiveProjectionCount,
		EventGraphSHA256: e.EventGraphSHA256,
	}
}

func sameDCIIdentityStableFacts(left, right dciIdentityResponse) bool {
	return left.SchemaVersion == right.SchemaVersion && left.Status == right.Status && left.RequestID == right.RequestID &&
		left.AgentID == right.AgentID && left.Role == right.Role && left.Operation == right.Operation &&
		left.ActionID == right.ActionID && left.TraceID == right.TraceID && left.EventCount == right.EventCount &&
		left.StepCount == right.StepCount && left.EvidenceCount == right.EvidenceCount &&
		left.CurrentProjectionCount == right.CurrentProjectionCount && left.ArchiveProjectionCount == right.ArchiveProjectionCount &&
		left.EventGraphSHA256 == right.EventGraphSHA256
}

func loadDCIIdentityPreEvidence(path string, observedAt time.Time, deps verifierDependencies) (dciIdentityEvidence, string, verifierOutcome) {
	deps = normalizeVerifierDependencies(deps)
	path = strings.TrimSpace(path)
	if path == "" {
		return dciIdentityEvidence{}, "", verifierOutcome{Status: "blocked", FailureBoundary: "canonical DCI pre-restart evidence is required"}
	}
	info, err := deps.Lstat(path)
	if err != nil || info == nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return dciIdentityEvidence{}, "", verifierOutcome{Status: "blocked", FailureBoundary: "canonical DCI pre-restart evidence is unavailable"}
	}
	if deps.Platform() != "windows" && info.Mode().Perm() != 0o600 {
		return dciIdentityEvidence{}, "", verifierOutcome{Status: "blocked", FailureBoundary: "canonical DCI pre-restart evidence is not owner-only"}
	}
	raw, err := readRegularFile(path, dciIdentityEvidenceMaxBytes)
	if err != nil {
		return dciIdentityEvidence{}, "", verifierOutcome{Status: "blocked", FailureBoundary: "canonical DCI pre-restart evidence is unavailable"}
	}
	var evidence dciIdentityEvidence
	values, err := decodeDCIIdentityObject(raw, &evidence, dciIdentityPreEvidenceKeys)
	if err != nil {
		return dciIdentityEvidence{}, "", verifierOutcome{Status: "blocked", FailureBoundary: dciIdentityPriorBoundary}
	}
	if err := validateDCIIdentityPreEvidenceTypes(values); err != nil {
		return dciIdentityEvidence{}, "", verifierOutcome{Status: "blocked", FailureBoundary: dciIdentityPriorBoundary}
	}
	if outcome := validateDCIIdentityPreEvidence(evidence, observedAt); outcome.Status != "" {
		return dciIdentityEvidence{}, "", outcome
	}
	return evidence, sha256Text(string(raw)), verifierOutcome{}
}

func validateDCIIdentityPreEvidenceTypes(fields map[string]json.RawMessage) error {
	for _, key := range []string{
		"receipt_schema", "check_id", "status", "observed_at", "failure_boundary", "evidence_schema", "command_id", "phase",
		"response_schema_version", "response_status", "request_id", "agent_id", "role", "operation", "action_id", "trace_id",
		"event_graph_sha256", "fixture_sha256", "service_generation_sha256", "artifact_sha256", "config_sha256", "response_facts_sha256",
	} {
		if err := unmarshalDCIIdentityNonNull(fields[key], new(string)); err != nil {
			return err
		}
	}
	for _, key := range []string{"first_write_replay", "second_write_replay", "listener_ready", "readiness_ready"} {
		if err := unmarshalDCIIdentityNonNull(fields[key], new(bool)); err != nil {
			return err
		}
	}
	for _, key := range []string{"schema_version", "event_count", "step_count", "evidence_count", "current_projection_count", "archive_projection_count", "service_main_pid"} {
		if err := unmarshalDCIIdentityNonNull(fields[key], new(int64)); err != nil {
			return err
		}
	}
	return nil
}

func validateDCIIdentityPreEvidence(evidence dciIdentityEvidence, observedAt time.Time) verifierOutcome {
	if evidence.SchemaVersion != verifierSchemaVersion || evidence.ReceiptSchema != verifierReceiptSchema || evidence.CheckID != "core_dci_identity_pre_restart" || evidence.Status != "passed" || evidence.FailureBoundary != "" || evidence.EvidenceSchema != dciIdentityPreEvidenceSchema || evidence.CommandID != dciIdentityPreCommandID || evidence.Phase != dciIdentityPrePhase {
		return verifierOutcome{Status: "blocked", FailureBoundary: dciIdentityPriorBoundary}
	}
	if err := validateVerifierEvidenceFreshness(map[string]any{"observed_at": evidence.ObservedAt}, observedAt); err != nil {
		return verifierOutcome{Status: "blocked", FailureBoundary: dciIdentityPriorBoundary}
	}
	requestID, ok := dciIdentityRequestID(evidence.RequestID, time.Time{}, false)
	if !ok || requestID != evidence.RequestID || evidence.FixtureSHA256 != sha256Text(dciIdentityFixedQuery) || !evidence.ListenerReady || !evidence.ReadinessReady || evidence.ServiceMainPID <= 0 || evidence.ServiceMainPID > dciIdentityMaxServicePID || !isLowercaseSHA256(evidence.ServiceGenerationSHA256) || !isLowercaseSHA256(evidence.ArtifactSHA256) || !isLowercaseSHA256(evidence.ConfigSHA256) || evidence.ServiceGenerationSHA256 != dciIdentityServiceGenerationSHA256(evidence.ServiceMainPID, evidence.ArtifactSHA256, evidence.ConfigSHA256) {
		return verifierOutcome{Status: "blocked", FailureBoundary: dciIdentityPriorBoundary}
	}
	response := evidence.response()
	facts, err := validateDCIIdentityResponseFacts(response, evidence.RequestID)
	if err != nil {
		return verifierOutcome{Status: "blocked", FailureBoundary: dciIdentityPriorBoundary}
	}
	if response.FirstWriteReplay || !response.SecondWriteReplay || evidence.ResponseFactsSHA256 != facts {
		return verifierOutcome{Status: "blocked", FailureBoundary: dciIdentityPriorBoundary}
	}
	return verifierOutcome{}
}

func validateDCIIdentityResponseFacts(response dciIdentityResponse, requestID string) (string, error) {
	if response.SchemaVersion != dciIdentityResponseSchema || response.Status != "passed" || response.RequestID != requestID || response.AgentID != "shiro" || response.Role != "worker" || response.Operation != dciIdentityOperation {
		return "", errors.New("invalid response facts")
	}
	evidence := dcipersistence.IdentityEvidence{
		SchemaVersion: dcipersistence.IdentityEvidenceSchemaVersion, Status: response.Status,
		ActionID: modulecore.ActionID(response.ActionID), TraceID: modulecore.TraceID(response.TraceID),
		ActorKind: "agent", ActorID: response.AgentID, SearchStatus: "completed",
		EventCount: response.EventCount, StepCount: response.StepCount, EvidenceCount: response.EvidenceCount,
		CurrentProjectionCount: response.CurrentProjectionCount, ArchiveProjectionCount: response.ArchiveProjectionCount,
		EventGraphSHA256: response.EventGraphSHA256,
	}
	if err := dcipersistence.ValidateIdentityEvidence(evidence); err != nil {
		return "", err
	}
	facts, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return sha256Text(string(facts)), nil
}

func requireDCIIdentityOldGenerationAbsent(mainPID int64, deps verifierDependencies) verifierOutcome {
	deps = normalizeVerifierDependencies(deps)
	if mainPID <= 0 {
		return verifierOutcome{Status: "blocked", FailureBoundary: dciIdentityPriorBoundary}
	}
	path := filepath.Join(string(filepath.Separator)+"proc", strconv.FormatInt(mainPID, 10))
	_, err := deps.Lstat(path)
	if err == nil {
		return verifierOutcome{Status: "failed", FailureBoundary: dciIdentityOldGenerationBoundary}
	}
	if errors.Is(err, os.ErrNotExist) {
		return verifierOutcome{}
	}
	return verifierOutcome{Status: "blocked", FailureBoundary: "canonical CORE old generation absence is unavailable"}
}

func isLowercaseSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}
