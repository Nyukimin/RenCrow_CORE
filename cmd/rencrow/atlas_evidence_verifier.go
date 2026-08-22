package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	backlogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	backlogfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/backlog"
	executionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/execution"
)

const (
	atlasDeploymentReceiptRelativePath = ".rencrow/receipts/binary-redeployment.jsonl"
	atlasMaxDeploymentReceiptLine      = 1024 * 1024
	atlasProbeTimeout                  = 3 * time.Second
	atlasProbeMaxBody                  = 512 * 1024
	atlasReadinessEvidenceRef          = "core:/ready"
	atlasSmokeEvidenceRef              = "core:/viewer/atlas/items"
)

// atlasExecutionReportReader is the smallest existing owner-store contract
// needed by the verifier.  Keeping the interface here lets focused tests use
// a fixture owner store without introducing another production persistence
// route.
type atlasExecutionReportReader interface {
	GetByJobID(context.Context, string) (execution.ExecutionReport, error)
}

// atlasEvidenceVerifier is the production CORE owner of Atlas evidence
// verification.  It deliberately has no request-provided filesystem or
// network inputs.  Specifications come from the embedded backfill package,
// execution reports come from the already configured CORE report store, and
// deployment receipts come from the fixed operator receipt log.
type atlasEvidenceVerifier struct {
	reportStore    atlasExecutionReportReader
	receiptPath    string
	specs          map[string]domainbacklog.SpecificationArtifact
	ownerBaseURL   string
	executablePath string
	httpClient     *http.Client
	buildInfo      func(string) (*buildinfo.BuildInfo, error)
}

type atlasEvidenceVerifierOptions struct {
	ReportStore    atlasExecutionReportReader
	ReceiptPath    string
	Specifications []domainbacklog.SpecificationArtifact
	OwnerBaseURL   string
	ExecutablePath string
	HTTPClient     *http.Client
	BuildInfo      func(string) (*buildinfo.BuildInfo, error)
}

// newAtlasEvidenceVerifier constructs the production verifier.  It does not
// create or probe an alternate evidence store.  A missing report store keeps
// execution_report evidence unavailable while still allowing embedded spec
// evidence to be verified; the relevant evidence kind then fails closed at
// verification time.
func newAtlasEvidenceVerifier(cfg *config.Config, reportStore *executionpersistence.JSONLReportStore) (*atlasEvidenceVerifier, error) {
	pkg, err := backlogfeature.LoadBackfillPackage()
	if err != nil {
		return nil, fmt.Errorf("load embedded Atlas specification package: %w", err)
	}
	if cfg == nil || cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		return nil, errors.New("resolve Atlas owner base URL: configured server port is invalid")
	}
	executablePath, err := os.Executable()
	if err != nil || strings.TrimSpace(executablePath) == "" {
		if err == nil {
			err = errors.New("executable path is empty")
		}
		return nil, fmt.Errorf("resolve Atlas owner executable: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		if err == nil {
			err = errors.New("home directory is empty")
		}
		return nil, fmt.Errorf("resolve deployment receipt home: %w", err)
	}
	return newAtlasEvidenceVerifierWithOptions(atlasEvidenceVerifierOptions{
		ReportStore:    reportStore,
		ReceiptPath:    filepath.Join(home, atlasDeploymentReceiptRelativePath),
		Specifications: pkg.SpecificationArtifacts,
		OwnerBaseURL:   fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port),
		ExecutablePath: executablePath,
	})
}

// newAtlasEvidenceVerifierWithOptions is intentionally kept in cmd/rencrow so
// tests can provide a bounded fixture receipt log.  Production wiring uses
// newAtlasEvidenceVerifier and the fixed home receipt path above.
func newAtlasEvidenceVerifierWithOptions(options atlasEvidenceVerifierOptions) (*atlasEvidenceVerifier, error) {
	if strings.TrimSpace(options.ReceiptPath) == "" {
		return nil, errors.New("Atlas deployment receipt path is required")
	}
	ownerBaseURL, err := normalizeAtlasOwnerBaseURL(options.OwnerBaseURL)
	if err != nil {
		return nil, err
	}
	executablePath := strings.TrimSpace(options.ExecutablePath)
	if executablePath != "" && !filepath.IsAbs(executablePath) {
		return nil, errors.New("Atlas owner executable path must be absolute")
	}
	verifier := &atlasEvidenceVerifier{
		reportStore:    options.ReportStore,
		receiptPath:    filepath.Clean(options.ReceiptPath),
		specs:          make(map[string]domainbacklog.SpecificationArtifact, len(options.Specifications)),
		ownerBaseURL:   ownerBaseURL,
		executablePath: executablePath,
		httpClient:     newAtlasProbeHTTPClient(options.HTTPClient),
		buildInfo:      options.BuildInfo,
	}
	for _, artifact := range options.Specifications {
		id := strings.TrimSpace(artifact.SpecID)
		if id == "" {
			return nil, errors.New("embedded Atlas specification has empty spec_id")
		}
		// The backfill package also retains metadata-only external
		// specifications.  They are intentionally not addressable by this
		// verifier: only artifacts with an embedded body may pass the spec
		// evidence gate.
		if !artifact.BodyAvailable {
			continue
		}
		if _, exists := verifier.specs[id]; exists {
			return nil, fmt.Errorf("duplicate embedded Atlas specification %q", id)
		}
		if err := validateEmbeddedSpecification(artifact); err != nil {
			return nil, err
		}
		verifier.specs[id] = artifact
	}
	if len(verifier.specs) == 0 {
		return nil, errors.New("embedded Atlas specification package is empty")
	}
	return verifier, nil
}

var _ backlogapp.EvidenceVerifier = (*atlasEvidenceVerifier)(nil)

func (v *atlasEvidenceVerifier) Verify(ctx context.Context, request backlogapp.EvidenceVerificationRequest) (bool, error) {
	if v == nil {
		return false, errors.New("Atlas evidence verifier is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	ref := request.Ref
	target := strings.ToUpper(strings.TrimSpace(request.TargetDeliveryState))
	unitID := strings.TrimSpace(request.ImplementationUnitID)
	if strings.TrimSpace(request.ItemID) == "" || unitID == "" || request.ImplementationRevision < 1 || target == "" {
		return false, errors.New("authoritative Atlas evidence context is incomplete")
	}
	if stage := strings.TrimSpace(ref.Stage); stage != "" && !strings.EqualFold(stage, target) {
		return false, fmt.Errorf("evidence stage %q does not match authoritative target %q", ref.Stage, target)
	}
	kind := strings.ToLower(strings.TrimSpace(ref.Kind))
	switch kind {
	case "spec", "specification":
		return v.verifySpecification(ref)
	case "execution_report":
		return v.verifyExecutionReport(ctx, request)
	case "deploy_receipt", "deployment_receipt":
		return v.verifyDeploymentReceipt(ctx, request)
	case "readiness":
		return v.verifyReadiness(ctx, request)
	case "production_smoke", "live_verified":
		return v.verifyProductionSmoke(ctx, request)
	case "smoke":
		return false, errors.New("Atlas evidence kind \"smoke\" is unsupported; use production_smoke")
	default:
		return false, fmt.Errorf("unsupported Atlas evidence kind %q", kind)
	}
}

func (v *atlasEvidenceVerifier) verifySpecification(ref domainbacklog.EvidenceRef) (bool, error) {
	id, err := exactEvidenceID(ref.Ref, "spec:", "specification:")
	if err != nil {
		return false, fmt.Errorf("invalid specification evidence ref: %w", err)
	}
	artifact, ok := v.specs[id]
	if !ok {
		return false, fmt.Errorf("embedded specification %q not found", id)
	}
	if err := validateEmbeddedSpecification(artifact); err != nil {
		return false, err
	}
	contentHash := strings.ToLower(strings.TrimSpace(artifact.ContentSHA256))
	if supplied := strings.TrimSpace(ref.SHA256); supplied != "" {
		if !validSHA256(supplied) || !strings.EqualFold(supplied, contentHash) {
			return false, fmt.Errorf("specification %q content hash mismatch", id)
		}
	}
	if supplied := strings.TrimSpace(ref.Revision); supplied != "" && supplied != strconv.Itoa(artifact.Revision) {
		return false, fmt.Errorf("specification %q revision mismatch", id)
	}
	if supplied := strings.TrimSpace(ref.ObservedAt); supplied != "" {
		if !timestampsEqual(supplied, artifact.CapturedAt) {
			return false, fmt.Errorf("specification %q is stale or observed_at does not match", id)
		}
	}
	return true, nil
}

func (v *atlasEvidenceVerifier) verifyExecutionReport(ctx context.Context, request backlogapp.EvidenceVerificationRequest) (bool, error) {
	ref := request.Ref
	id, err := exactEvidenceID(ref.Ref, "execution_report:")
	if err != nil {
		return false, fmt.Errorf("invalid execution report evidence ref: %w", err)
	}
	if v.reportStore == nil {
		return false, errors.New("configured execution report store is unavailable")
	}
	stage := strings.ToUpper(strings.TrimSpace(request.TargetDeliveryState))
	unitID := strings.TrimSpace(request.ImplementationUnitID)
	if stage == "" || unitID == "" || request.ImplementationRevision < 1 {
		return false, errors.New("execution report evidence requires authoritative stage, unit, and revision")
	}
	if !executionReportStageAllowed(stage) {
		return false, fmt.Errorf("execution report cannot prove Atlas target stage %q", stage)
	}
	if ref.Repository != domainbacklog.LifecycleOwnerModule {
		return false, fmt.Errorf("execution report %q requires repository %q", id, domainbacklog.LifecycleOwnerModule)
	}
	if stage == domainbacklog.DeliveryBuild {
		if ref.SHA256 == "" || ref.SHA256 != strings.TrimSpace(ref.SHA256) || !validSHA256(ref.SHA256) {
			return false, fmt.Errorf("execution report %q BUILD evidence requires a full artifact SHA-256", id)
		}
	} else if ref.SHA256 != "" {
		return false, fmt.Errorf("execution report %q non-BUILD evidence cannot carry SHA-256", id)
	}
	report, err := v.reportStore.GetByJobID(ctx, id)
	if err != nil {
		return false, fmt.Errorf("execution report %q not found: %w", id, err)
	}
	if err := report.Validate(); err != nil {
		return false, fmt.Errorf("execution report %q is invalid: %w", id, err)
	}
	if report.JobID != id {
		return false, fmt.Errorf("execution report %q returned a different job ID %q", id, report.JobID)
	}
	switch strings.ToLower(strings.TrimSpace(report.Status)) {
	case "passed", "success", "succeeded":
	default:
		return false, fmt.Errorf("execution report %q status %q is not successful", id, report.Status)
	}
	if report.FinishedAt.IsZero() {
		return false, fmt.Errorf("execution report %q has no finished_at", id)
	}
	implementationRevision := strconv.Itoa(request.ImplementationRevision)
	if !executionReportHasAtlasMarker(report, "atlas.item", strings.TrimSpace(request.ItemID)) ||
		!executionReportHasAtlasMarker(report, "atlas.unit", unitID) ||
		!executionReportHasAtlasMarker(report, "atlas.implementation_revision", implementationRevision) ||
		!executionReportHasAtlasMarker(report, "atlas.stage", stage) {
		return false, fmt.Errorf("execution report %q lacks exact Atlas item/unit/implementation revision/stage markers", id)
	}
	sourceRevision, ok := executionReportAtlasMarker(report, "atlas.source_revision")
	if !ok || !validAtlasSourceRevision(sourceRevision) {
		return false, fmt.Errorf("execution report %q lacks a full 40-hex atlas.source_revision marker", id)
	}
	if ref.Revision == "" || ref.Revision != strings.TrimSpace(ref.Revision) || !validAtlasSourceRevision(ref.Revision) || !strings.EqualFold(ref.Revision, sourceRevision) {
		return false, fmt.Errorf("execution report %q source revision mismatch", id)
	}
	if stage == domainbacklog.DeliveryTDDRed && !executionReportHasAtlasMarker(report, "atlas.red_observed", "true") {
		return false, fmt.Errorf("execution report %q lacks atlas.red_observed=true", id)
	}
	if stage == domainbacklog.DeliveryBuild && !executionReportHasAtlasMarker(report, "atlas.artifact.sha256", strings.TrimSpace(ref.SHA256)) {
		return false, fmt.Errorf("execution report %q artifact SHA-256 marker mismatch", id)
	}
	if supplied := strings.TrimSpace(ref.ObservedAt); supplied != "" && !timestampEqualToTime(supplied, report.FinishedAt) {
		return false, fmt.Errorf("execution report %q is stale or observed_at does not match", id)
	}
	return true, nil
}

func (v *atlasEvidenceVerifier) verifyDeploymentReceipt(ctx context.Context, request backlogapp.EvidenceVerificationRequest) (bool, error) {
	ref := request.Ref
	id, err := exactEvidenceID(ref.Ref, "deploy_receipt:", "deployment_receipt:")
	if err != nil {
		return false, fmt.Errorf("invalid deployment receipt ref: %w", err)
	}
	receipt, err := readAtlasDeploymentReceipt(ctx, v.receiptPath, id)
	if err != nil {
		return false, err
	}
	if !strings.EqualFold(strings.TrimSpace(receipt.Component), "core") {
		return false, fmt.Errorf("deployment receipt %q is not for component core", id)
	}
	target := strings.ToUpper(strings.TrimSpace(request.TargetDeliveryState))
	switch target {
	case domainbacklog.DeliveryDeploy:
		// A completed core deployment receipt is sufficient for DEPLOY.
	case domainbacklog.DeliveryRestart:
		if !hasExactAtlasRunningUnit(receipt.RunningUnits, "rencrow.service") {
			return false, fmt.Errorf("deployment receipt %q does not prove rencrow.service was running", id)
		}
	default:
		return false, fmt.Errorf("deployment receipt %q only proves DEPLOY or RESTART, not %s", id, target)
	}
	if strings.ToLower(strings.TrimSpace(receipt.Outcome)) != "success" || strings.ToLower(strings.TrimSpace(receipt.Phase)) != "complete" {
		return false, fmt.Errorf("deployment receipt %q is not a completed success", id)
	}
	targetRevision := strings.TrimSpace(receipt.TargetRevision)
	if !validAtlasRevision(targetRevision) {
		return false, fmt.Errorf("deployment receipt %q target_revision is not a full SHA", id)
	}
	suppliedRevision := strings.TrimSpace(ref.Revision)
	if !validAtlasRevision(suppliedRevision) {
		return false, fmt.Errorf("deployment receipt %q evidence requires a full revision SHA", id)
	}
	if !strings.EqualFold(suppliedRevision, targetRevision) {
		return false, fmt.Errorf("deployment receipt %q target_revision mismatch", id)
	}
	if strings.TrimSpace(receipt.FinishedAt) == "" || !timestampPresent(receipt.FinishedAt) {
		return false, fmt.Errorf("deployment receipt %q has no valid finished_at", id)
	}
	if supplied := strings.TrimSpace(ref.ObservedAt); supplied != "" && !timestampsEqual(supplied, receipt.FinishedAt) {
		return false, fmt.Errorf("deployment receipt %q is stale or observed_at does not match", id)
	}
	if err := validateInstalledAtlasBinary(receipt, strings.TrimSpace(ref.SHA256)); err != nil {
		return false, fmt.Errorf("deployment receipt %q installed binary check failed: %w", id, err)
	}
	return true, nil
}

type atlasReadinessProbeResponse struct {
	Ready  *bool  `json:"ready"`
	Status string `json:"status"`
}

type atlasSmokeProbeResponse struct {
	Item                   domainbacklog.Item                     `json:"item"`
	ResolvedSpecifications *[]domainbacklog.SpecificationArtifact `json:"resolved_specifications"`
}

func (v *atlasEvidenceVerifier) verifyReadiness(ctx context.Context, request backlogapp.EvidenceVerificationRequest) (bool, error) {
	if strings.ToUpper(strings.TrimSpace(request.TargetDeliveryState)) != domainbacklog.DeliveryPostDeployVerify {
		return false, fmt.Errorf("readiness evidence requires target %s", domainbacklog.DeliveryPostDeployVerify)
	}
	if request.Ref.Ref != atlasReadinessEvidenceRef {
		return false, fmt.Errorf("unsupported readiness evidence ref %q", request.Ref.Ref)
	}
	if err := validateAtlasProbeRevision(request.Ref); err != nil {
		return false, err
	}
	if err := v.verifyExecutableRevision(request.Ref.Revision); err != nil {
		return false, err
	}
	var payload atlasReadinessProbeResponse
	if err := v.getAtlasProbe(ctx, "/ready", &payload); err != nil {
		return false, fmt.Errorf("CORE readiness probe failed: %w", err)
	}
	if payload.Ready == nil || !*payload.Ready || !strings.EqualFold(strings.TrimSpace(payload.Status), "ready") {
		return false, errors.New("CORE readiness response is not ready")
	}
	return true, nil
}

func (v *atlasEvidenceVerifier) verifyProductionSmoke(ctx context.Context, request backlogapp.EvidenceVerificationRequest) (bool, error) {
	if strings.ToUpper(strings.TrimSpace(request.TargetDeliveryState)) != domainbacklog.DeliveryLiveVerified {
		return false, fmt.Errorf("production smoke evidence requires target %s", domainbacklog.DeliveryLiveVerified)
	}
	if request.Ref.Ref != atlasSmokeEvidenceRef {
		return false, fmt.Errorf("unsupported production smoke evidence ref %q", request.Ref.Ref)
	}
	if err := validateAtlasProbeRevision(request.Ref); err != nil {
		return false, err
	}
	if err := v.verifyExecutableRevision(request.Ref.Revision); err != nil {
		return false, err
	}
	itemID := strings.TrimSpace(request.ItemID)
	if itemID == "" {
		return false, errors.New("production smoke evidence requires authoritative item ID")
	}
	endpointPath := "/viewer/atlas/items/" + url.PathEscape(itemID)
	var payload atlasSmokeProbeResponse
	if err := v.getAtlasProbe(ctx, endpointPath, &payload); err != nil {
		return false, fmt.Errorf("CORE Atlas item probe failed: %w", err)
	}
	if err := validateAtlasSmokeItem(payload, request); err != nil {
		return false, err
	}
	return true, nil
}

func (v *atlasEvidenceVerifier) getAtlasProbe(ctx context.Context, path string, target any) error {
	if v == nil || strings.TrimSpace(v.ownerBaseURL) == "" {
		return errors.New("fixed loopback CORE probe is unavailable")
	}
	if path == "" || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#") {
		return errors.New("invalid fixed CORE probe path")
	}
	probeCtx, cancel := context.WithTimeout(ctx, atlasProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, v.ownerBaseURL+path, nil)
	if err != nil {
		return fmt.Errorf("create fixed CORE probe request: %w", err)
	}
	client := v.httpClient
	if client == nil {
		client = newAtlasProbeHTTPClient(nil)
	}
	resp, err := client.Do(req)
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("request fixed CORE probe: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fixed CORE probe returned HTTP %d", resp.StatusCode)
	}
	if contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type"))); contentType != "" && !strings.Contains(contentType, "application/json") {
		return fmt.Errorf("fixed CORE probe returned non-JSON content type %q", contentType)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, atlasProbeMaxBody+1))
	if err != nil {
		return fmt.Errorf("read fixed CORE probe response: %w", err)
	}
	if len(body) > atlasProbeMaxBody {
		return errors.New("fixed CORE probe response is too large")
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode fixed CORE probe response: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("fixed CORE probe response contains trailing JSON")
		}
		return fmt.Errorf("decode fixed CORE probe trailing data: %w", err)
	}
	return nil
}

func validateAtlasProbeRevision(ref domainbacklog.EvidenceRef) error {
	if strings.TrimSpace(ref.Revision) == "" || !validAtlasRevision(ref.Revision) {
		return errors.New("readiness/smoke evidence requires a full executable revision SHA")
	}
	if strings.TrimSpace(ref.SHA256) != "" {
		return errors.New("readiness/smoke evidence cannot prove a requested content hash")
	}
	if strings.TrimSpace(ref.ObservedAt) != "" {
		return errors.New("readiness/smoke response has no authoritative observed_at timestamp")
	}
	return nil
}

func validAtlasRevision(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (v *atlasEvidenceVerifier) verifyExecutableRevision(expected string) error {
	reader := buildinfo.ReadFile
	if v != nil && v.buildInfo != nil {
		reader = v.buildInfo
	}
	return verifyAtlasExecutableRevisionWithReader(expected, v.executablePath, reader)
}

func verifyAtlasExecutableRevisionWithReader(expected, executablePath string, reader func(string) (*buildinfo.BuildInfo, error)) error {
	expected = strings.TrimSpace(expected)
	if !validAtlasRevision(expected) {
		return errors.New("requested executable revision is not a full SHA")
	}
	executablePath = strings.TrimSpace(executablePath)
	if executablePath == "" || !filepath.IsAbs(executablePath) {
		return errors.New("current CORE executable path is unavailable")
	}
	info, err := os.Stat(executablePath)
	if err != nil {
		return fmt.Errorf("stat CORE executable: %w", err)
	}
	if info.IsDir() {
		return errors.New("CORE executable path is a directory")
	}
	if reader == nil {
		return errors.New("CORE executable build stamp reader is unavailable")
	}
	build, err := reader(executablePath)
	if err != nil {
		return fmt.Errorf("read CORE executable build stamp: %w", err)
	}
	settings := make(map[string]string, len(build.Settings))
	for _, setting := range build.Settings {
		settings[setting.Key] = setting.Value
	}
	actual := strings.TrimSpace(settings["vcs.revision"])
	if !validAtlasRevision(actual) || !strings.EqualFold(actual, expected) {
		return fmt.Errorf("CORE executable vcs.revision %q does not match requested %q", actual, expected)
	}
	if !strings.EqualFold(strings.TrimSpace(settings["vcs.modified"]), "false") {
		return errors.New("CORE executable build is not stamped clean")
	}
	return nil
}

func validateAtlasSmokeItem(payload atlasSmokeProbeResponse, request backlogapp.EvidenceVerificationRequest) error {
	item := payload.Item
	if strings.TrimSpace(item.ItemID) != strings.TrimSpace(request.ItemID) {
		return fmt.Errorf("Atlas smoke item ID %q does not match authoritative %q", item.ItemID, request.ItemID)
	}
	if strings.TrimSpace(item.ImplementationUnit) != strings.TrimSpace(request.ImplementationUnitID) {
		return fmt.Errorf("Atlas smoke implementation unit %q does not match authoritative %q", item.ImplementationUnit, request.ImplementationUnitID)
	}
	if item.ImplementationRevision != request.ImplementationRevision {
		return fmt.Errorf("Atlas smoke implementation revision %d does not match authoritative %d", item.ImplementationRevision, request.ImplementationRevision)
	}
	for field, value := range map[string]string{"purpose": item.Purpose, "problem": item.Problem, "idea": item.Idea} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("Atlas smoke item has empty %s", field)
		}
	}
	if len(item.SourceRefs) == 0 {
		return errors.New("Atlas smoke item has no source_refs")
	}
	for _, source := range item.SourceRefs {
		if err := domainbacklog.ValidateSourceRef(source); err != nil {
			return fmt.Errorf("Atlas smoke source_refs invalid: %w", err)
		}
	}
	if len(item.SpecificationRefs) == 0 || payload.ResolvedSpecifications == nil || len(*payload.ResolvedSpecifications) == 0 {
		return errors.New("Atlas smoke item lacks specification_refs or resolved specifications")
	}
	resolved := make(map[string]domainbacklog.SpecificationArtifact, len(*payload.ResolvedSpecifications))
	for _, artifact := range *payload.ResolvedSpecifications {
		id := strings.TrimSpace(artifact.SpecID)
		if id == "" {
			return errors.New("Atlas smoke resolved specification has empty spec_id")
		}
		if err := validateEmbeddedSpecification(artifact); err != nil {
			return fmt.Errorf("Atlas smoke resolved specification %q invalid: %w", id, err)
		}
		resolved[id] = artifact
	}
	for _, specID := range item.SpecificationRefs {
		id := strings.TrimSpace(specID)
		if id == "" {
			return errors.New("Atlas smoke item has empty specification ref")
		}
		if _, ok := resolved[id]; !ok {
			return fmt.Errorf("Atlas smoke specification %q is not resolved", id)
		}
	}
	return nil
}

func normalizeAtlasOwnerBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("Atlas owner probe base URL must be a pathless HTTP loopback URL")
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("Atlas owner probe base URL host %q is not loopback", host)
	}
	port := parsed.Port()
	if port == "" {
		return "", errors.New("Atlas owner probe base URL must include a port")
	}
	portNumber, portErr := strconv.Atoi(port)
	if portErr != nil || portNumber < 1 || portNumber > 65535 {
		return "", errors.New("Atlas owner probe base URL port is invalid")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func newAtlasProbeHTTPClient(input *http.Client) *http.Client {
	client := http.Client{Timeout: atlasProbeTimeout}
	if input != nil {
		client = *input
		if client.Timeout <= 0 || client.Timeout > atlasProbeTimeout {
			client.Timeout = atlasProbeTimeout
		}
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &client
}

type atlasDeploymentReceipt struct {
	ReceiptID      string
	Component      string
	TargetRevision string
	Phase          string
	Outcome        string
	FinishedAt     string
	BinaryPath     string
	RunningUnits   []string
	Raw            map[string]json.RawMessage
}

func readAtlasDeploymentReceipt(ctx context.Context, path, receiptID string) (atlasDeploymentReceipt, error) {
	if strings.TrimSpace(path) == "" {
		return atlasDeploymentReceipt{}, errors.New("deployment receipt log is unavailable")
	}
	file, err := os.Open(path)
	if err != nil {
		return atlasDeploymentReceipt{}, fmt.Errorf("open deployment receipt log: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), atlasMaxDeploymentReceiptLine)
	var found atlasDeploymentReceipt
	foundCount := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return atlasDeploymentReceipt{}, err
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			continue
		}
		id, ok := rawString(raw, "receipt_id")
		if !ok || id != receiptID {
			continue
		}
		foundCount++
		runningUnits, _ := rawStringArray(raw, "running_units")
		found = atlasDeploymentReceipt{
			ReceiptID: id, Component: rawStringDefault(raw, "component"),
			TargetRevision: rawStringDefault(raw, "target_revision"), Phase: rawStringDefault(raw, "phase"),
			Outcome: rawStringDefault(raw, "outcome"), FinishedAt: rawStringDefault(raw, "finished_at"),
			BinaryPath: rawStringDefault(raw, "binary_path"), RunningUnits: runningUnits, Raw: raw,
		}
	}
	if err := scanner.Err(); err != nil {
		return atlasDeploymentReceipt{}, fmt.Errorf("scan deployment receipt log: %w", err)
	}
	if foundCount == 0 {
		return atlasDeploymentReceipt{}, fmt.Errorf("deployment receipt %q not found", receiptID)
	}
	if foundCount != 1 {
		return atlasDeploymentReceipt{}, fmt.Errorf("deployment receipt %q is ambiguous", receiptID)
	}
	return found, nil
}

func validateInstalledAtlasBinary(receipt atlasDeploymentReceipt, requestedHash string) error {
	expectedHash := strings.TrimSpace(requestedHash)
	if expectedHash != "" && !validSHA256(expectedHash) {
		return errors.New("requested binary hash is not SHA-256")
	}
	for _, key := range []string{"installed_sha256", "installed_hash", "binary_sha256", "artifact_sha256", "sha256"} {
		if value := strings.TrimSpace(rawStringDefault(receipt.Raw, key)); value != "" {
			if !validSHA256(value) {
				return fmt.Errorf("receipt field %s is not SHA-256", key)
			}
			if expectedHash != "" && !strings.EqualFold(expectedHash, value) {
				return fmt.Errorf("requested hash does not match receipt field %s", key)
			}
			if expectedHash == "" {
				expectedHash = value
			}
			break
		}
	}
	binaryPath := strings.TrimSpace(receipt.BinaryPath)
	if expectedHash != "" {
		if binaryPath == "" || !filepath.IsAbs(binaryPath) {
			return errors.New("binary path is unavailable for requested hash verification")
		}
		actual, err := sha256File(binaryPath)
		if err != nil {
			return err
		}
		if !strings.EqualFold(actual, expectedHash) {
			return errors.New("installed binary hash mismatch")
		}
	}
	if binaryPath == "" || !filepath.IsAbs(binaryPath) {
		return nil
	}
	info, err := os.Stat(binaryPath)
	if err != nil {
		// A receipt without a requested/recorded hash may still be valid on a
		// host where the old binary was removed after the receipt was written.
		// No unavailable stamp is inferred as a match.
		return nil
	}
	if info.IsDir() {
		return errors.New("installed binary path is a directory")
	}
	build, err := buildinfo.ReadFile(binaryPath)
	if err != nil {
		return nil
	}
	settings := make(map[string]string, len(build.Settings))
	for _, setting := range build.Settings {
		settings[setting.Key] = setting.Value
	}
	if revision := strings.TrimSpace(settings["vcs.revision"]); revision != "" && revision != strings.TrimSpace(receipt.TargetRevision) {
		return fmt.Errorf("installed vcs.revision %q does not match target_revision", revision)
	}
	if strings.EqualFold(strings.TrimSpace(settings["vcs.modified"]), "true") {
		return errors.New("installed binary is stamped dirty")
	}
	return nil
}

func validateEmbeddedSpecification(artifact domainbacklog.SpecificationArtifact) error {
	if err := domainbacklog.ValidateSpecificationArtifact(artifact); err != nil {
		return fmt.Errorf("embedded specification %q is invalid: %w", artifact.SpecID, err)
	}
	if !artifact.BodyAvailable || strings.TrimSpace(artifact.Content) == "" {
		return fmt.Errorf("specification %q has no embedded body", artifact.SpecID)
	}
	if artifact.Revision < 1 || strings.TrimSpace(artifact.ContentSHA256) == "" || !validSHA256(artifact.ContentSHA256) {
		return fmt.Errorf("specification %q lacks verifiable revision/hash metadata", artifact.SpecID)
	}
	hash := sha256.Sum256([]byte(artifact.Content))
	if !strings.EqualFold(hex.EncodeToString(hash[:]), strings.TrimSpace(artifact.ContentSHA256)) {
		return fmt.Errorf("embedded specification %q content hash mismatch", artifact.SpecID)
	}
	if strings.TrimSpace(artifact.CapturedAt) == "" || !timestampPresent(artifact.CapturedAt) {
		return fmt.Errorf("specification %q lacks valid captured_at metadata", artifact.SpecID)
	}
	return nil
}

func executionReportHasAtlasMarker(report execution.ExecutionReport, key, expected string) bool {
	value, ok := executionReportAtlasMarker(report, key)
	return ok && value == strings.TrimSpace(expected)
}

func executionReportAtlasMarker(report execution.ExecutionReport, key string) (string, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", false
	}
	entries := make([]string, 0, len(report.Verification)+len(report.Artifacts)+len(report.Acceptance)+len(report.Steps)+1)
	entries = append(entries, report.Verification...)
	entries = append(entries, report.Artifacts...)
	entries = append(entries, report.Acceptance...)
	entries = append(entries, report.Steps...)
	entries = append(entries, report.Goal)
	prefix := key + "="
	var value string
	found := false
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if !strings.HasPrefix(entry, prefix) {
			continue
		}
		candidate := strings.TrimPrefix(entry, prefix)
		if candidate == "" || (found && candidate != value) {
			return "", false
		}
		value = candidate
		found = true
	}
	return value, found
}

func executionReportStageAllowed(stage string) bool {
	switch stage {
	case domainbacklog.DeliveryTDDRed, domainbacklog.DeliveryTDDGreen,
		domainbacklog.DeliveryRefactor, domainbacklog.DeliveryE2EPredeploy,
		domainbacklog.DeliveryBuild:
		return true
	default:
		return false
	}
}

func validAtlasSourceRevision(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 40 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func exactEvidenceID(raw string, prefixes ...string) (string, error) {
	id := strings.TrimSpace(raw)
	for _, prefix := range prefixes {
		if strings.HasPrefix(strings.ToLower(id), strings.ToLower(prefix)) {
			id = strings.TrimSpace(id[len(prefix):])
			break
		}
	}
	if id == "" || strings.ContainsRune(id, '\x00') || strings.ContainsAny(id, `/\\`) {
		return "", errors.New("evidence ID is empty or contains a path separator")
	}
	return id, nil
}

func rawString(raw map[string]json.RawMessage, key string) (string, bool) {
	value, ok := raw[key]
	if !ok {
		return "", false
	}
	var out string
	if json.Unmarshal(value, &out) != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
}

func rawStringDefault(raw map[string]json.RawMessage, key string) string {
	value, _ := rawString(raw, key)
	return value
}

func rawStringArray(raw map[string]json.RawMessage, key string) ([]string, bool) {
	value, ok := raw[key]
	if !ok {
		return nil, false
	}
	var values []string
	if err := json.Unmarshal(value, &values); err != nil {
		return nil, false
	}
	for _, item := range values {
		if strings.TrimSpace(item) == "" {
			return nil, false
		}
	}
	return values, true
}

func hasExactAtlasRunningUnit(units []string, expected string) bool {
	for _, unit := range units {
		if unit == expected {
			return true
		}
	}
	return false
}

func validSHA256(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func timestampPresent(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	return err == nil
}

func timestampsEqual(left, right string) bool {
	leftTime, leftErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(left))
	rightTime, rightErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(right))
	return leftErr == nil && rightErr == nil && leftTime.Equal(rightTime)
}

func timestampEqualToTime(value string, target time.Time) bool {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	return err == nil && !target.IsZero() && parsed.Equal(target)
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open installed binary: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash installed binary: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
