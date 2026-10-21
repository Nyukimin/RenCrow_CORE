package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing/classifierlab"
	"gopkg.in/yaml.v3"
)

const (
	layaDistribution = "laya"
	layaSourceURL    = "https://github.com/NandhaKishorM/laya"
	layaModelSource  = "convaiinnovations/laya-multilingual"
	pypiIndexURL     = "https://pypi.org/simple"
	maxPolicyBytes   = 4 << 20
	maxPipReport     = 16 << 20
	prepareTimeout   = 90 * time.Minute
)

var (
	modelRevisionPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	sdkVersionPattern    = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)+(?:a[0-9]+|b[0-9]+|rc[0-9]+|\.post[0-9]+|\.dev[0-9]+)?(?:\+[a-z0-9]+(?:[.-][a-z0-9]+)*)?$`)
	sha256Pattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type prepareBackend interface {
	CreateVenv(context.Context, string, string) (pythonExecutable, pythonVersion string, err error)
	InstallSDK(context.Context, string, string, string) (map[string]string, []byte, error)
	DownloadModel(context.Context, string, string, string) error
}

type prepareSourceArgs struct {
	PolicyPath   string
	LegacyPrompt string
	SDKVersion   string
	BasePython   string
}

type prepareStatus struct {
	SchemaVersion              string   `json:"schema_version"`
	Status                     string   `json:"status"`
	ReasonCode                 string   `json:"reason_code"`
	Reasons                    []string `json:"reasons"`
	OnlineAcquisitionAttempted bool     `json:"online_acquisition_attempted"`
	ModelInvoked               bool     `json:"model_invoked"`
	PythonVersion              string   `json:"python_version,omitempty"`
	SDKVersion                 string   `json:"sdk_version,omitempty"`
	ModelRevision              string   `json:"model_revision,omitempty"`
	AdapterContractStatus      string   `json:"adapter_contract_status,omitempty"`
}

type verifiedPrepareInputs struct {
	Policy        classifierlab.RoutePolicy
	PolicySHA256  string
	PromptPath    string
	PromptSHA256  string
	SDKVersion    string
	ModelRevision string
}

func runPrepare(configPath, outPath, repoRoot, policyPath, legacyPrompt, sdkVersion, basePython string) error {
	return runPrepareWithBackend(configPath, outPath, repoRoot, prepareSourceArgs{
		PolicyPath: policyPath, LegacyPrompt: legacyPrompt, SDKVersion: sdkVersion, BasePython: basePython,
	}, systemPrepareBackend{})
}

func runPrepareWithBackend(configPath, outPath, repoRoot string, args prepareSourceArgs, backend prepareBackend) error {
	config, err := loadExperimentConfig(configPath)
	if err != nil {
		return err
	}
	inspection, err := inspectRepository(repoRoot)
	if err != nil {
		return err
	}
	output, err := createPrivateOutputDirectory(outPath)
	if err != nil {
		return err
	}

	inputs, blockCode, blockReason := validatePrepareInputs(config, inspection, args)
	if blockCode != "" {
		if err := writeRunEvidence(output, config, inspection, "", "", nil); err != nil {
			return err
		}
		status := prepareStatus{
			SchemaVersion: classifierlab.SchemaVersion, Status: "BLOCKED", ReasonCode: blockCode,
			Reasons: []string{blockReason}, OnlineAcquisitionAttempted: false, ModelInvoked: false,
		}
		if err := writeJSONExclusive(filepath.Join(output, "prepare_status.json"), status); err != nil {
			return err
		}
		return commandError{Code: blockCode, Err: errors.New(blockReason)}
	}
	if backend == nil {
		return commandError{Code: "PREPARE_BACKEND_UNAVAILABLE", Err: errors.New("isolated acquisition backend is unavailable")}
	}

	modelDirectory := filepath.Join(output, "model")
	venvDirectory := filepath.Join(output, "venv")
	if err := os.Mkdir(modelDirectory, 0o700); err != nil {
		return fmt.Errorf("create private model directory: %w", err)
	}
	if err := os.Chmod(modelDirectory, 0o700); err != nil {
		return fmt.Errorf("restrict model directory permissions: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), prepareTimeout)
	defer cancel()
	pythonExecutable, pythonVersion, err := backend.CreateVenv(ctx, args.BasePython, venvDirectory)
	if err != nil {
		return writePrepareFailure(output, config, inspection, inputs, "VENV_CREATE_FAILED", "isolated Python environment creation failed", false, pythonVersion)
	}
	packages, lock, err := backend.InstallSDK(ctx, pythonExecutable, output, inputs.SDKVersion)
	if err != nil {
		return writePrepareFailure(output, config, inspection, inputs, "SDK_INSTALL_FAILED", "pinned Laya SDK installation failed", true, pythonVersion)
	}
	if err := validateResolvedPackages(packages, inputs.SDKVersion); err != nil {
		return writePrepareFailure(output, config, inspection, inputs, "SDK_LOCK_INVALID", err.Error(), true, pythonVersion)
	}
	if err := validateRequirementsLock(packages, lock); err != nil {
		return writePrepareFailure(output, config, inspection, inputs, "SDK_LOCK_INVALID", err.Error(), true, pythonVersion)
	}
	if err := writeBytesExclusive(filepath.Join(output, "requirements.lock"), lock); err != nil {
		return err
	}
	if err := backend.DownloadModel(ctx, pythonExecutable, modelDirectory, inputs.ModelRevision); err != nil {
		return writePrepareFailure(output, config, inspection, inputs, "MODEL_DOWNLOAD_FAILED", "pinned multilingual checkpoint download failed", true, pythonVersion)
	}
	currentPromptSHA, err := fileSHA256(inputs.PromptPath)
	if err != nil || currentPromptSHA != inputs.PromptSHA256 {
		return writePrepareFailure(output, config, inspection, inputs, "LEGACY_PROMPT_CHANGED", "supplied prompt file changed during preparation", true, pythonVersion)
	}
	if err := cleanupPrepareTemporaryFiles(output); err != nil {
		return writePrepareFailure(output, config, inspection, inputs, "PREPARE_TEMP_CLEANUP_FAILED", "private acquisition temporary data could not be removed", true, pythonVersion)
	}

	postInspection, err := inspectRepository(repoRoot)
	if err != nil {
		return writePrepareFailure(output, config, inspection, inputs, "SOURCE_RECHECK_FAILED", "cannot recheck policy source after preparation", true, pythonVersion)
	}
	if !samePolicySourceHashes(inspection.Baseline.SourceSHA256, postInspection.Baseline.SourceSHA256) || postInspection.Review.Status != "SOURCE_ALIGNED_REVIEW_REQUIRED" {
		return writePrepareFailure(output, config, inspection, inputs, "SOURCE_CHANGED_DURING_PREPARE", "policy source changed or became inconsistent during preparation", true, pythonVersion)
	}

	resolved := config
	resolved.Mode = classifierlab.ModeOff
	resolved.Candidate.ExpectedFamily = "laya-multilingual"
	resolved.Candidate.ExpectedModelRevision = inputs.ModelRevision
	resolved.Candidate.ExpectedPolicySHA256 = inputs.PolicySHA256
	resolved.ReferenceWorker.PythonExecutable = pythonExecutable
	resolved.ReferenceWorker.ModelDirectory = modelDirectory
	resolved.ReferenceWorker.OfflineRequired = true

	files, filesSHA, err := hashPreparedModelTree(modelDirectory)
	if err != nil {
		return writePrepareFailure(output, config, inspection, inputs, "MODEL_TREE_INVALID", "downloaded model tree is empty or contains unsupported entries", true, pythonVersion)
	}
	lockSHA := sha256Hex(lock)
	manifest := classifierlab.PreparedManifest{
		SchemaVersion: classifierlab.PreparedManifestSchemaVersion,
		PolicySHA256:  inputs.PolicySHA256,
		Model: classifierlab.PreparedModelManifest{
			Family: resolved.Candidate.ExpectedFamily, SourceID: layaModelSource,
			Revision: inputs.ModelRevision, Device: resolved.ReferenceWorker.Device, DType: resolved.ReferenceWorker.DType,
			Files: files, FilesSHA256: filesSHA,
		},
		SDK: classifierlab.PreparedSDKManifest{
			Distribution: layaDistribution, Version: inputs.SDKVersion, SourceURL: layaSourceURL,
			LockFile: "requirements.lock", LockSHA256: lockSHA, Packages: packages,
		},
	}
	if err := writeJSONExclusive(filepath.Join(output, classifierlab.PreparedManifestFileName), manifest); err != nil {
		return err
	}
	resolvedBytes, err := yaml.Marshal(resolved)
	if err != nil {
		return fmt.Errorf("encode resolved config: %w", err)
	}
	if err := writeBytesExclusive(filepath.Join(output, "resolved-config.yaml"), resolvedBytes); err != nil {
		return err
	}
	if err := writePrepareEvidence(output, resolved, postInspection, inputs, lockSHA); err != nil {
		return err
	}
	if _, err := classifierlab.LoadPreparedArtifacts(
		output, modelDirectory, resolved.Candidate.ExpectedFamily, resolved.Candidate.ExpectedModelRevision,
		resolved.ReferenceWorker.Device, resolved.ReferenceWorker.DType, resolved.Candidate.ExpectedPolicySHA256,
	); err != nil {
		return writePrepareFailure(output, resolved, postInspection, inputs, "PREPARED_ARTIFACTS_INVALID", "generated prepared artifacts failed integrity validation", true, pythonVersion)
	}
	status := prepareStatus{
		SchemaVersion: classifierlab.SchemaVersion, Status: "PREPARED", ReasonCode: "PREPARED_UNVERIFIED_ADAPTER",
		Reasons:                    []string{"exact SDK and model pins were acquired and artifact hashes validated; adapter contract remains unverified until isolated SDK smoke"},
		OnlineAcquisitionAttempted: true, ModelInvoked: false, PythonVersion: pythonVersion,
		SDKVersion: inputs.SDKVersion, ModelRevision: inputs.ModelRevision, AdapterContractStatus: "UNVERIFIED",
	}
	if err := writeJSONExclusive(filepath.Join(output, "prepare_status.json"), status); err != nil {
		return err
	}
	return nil
}

func validatePrepareInputs(config classifierlab.Config, inspection repositoryInspection, args prepareSourceArgs) (verifiedPrepareInputs, string, string) {
	if inspection.Review.Status == "POLICY_UNDEFINED" {
		return verifiedPrepareInputs{}, "POLICY_UNDEFINED", inspection.Review.Conflict
	}
	if inspection.Review.Status != "SOURCE_ALIGNED_REVIEW_REQUIRED" {
		return verifiedPrepareInputs{}, "POLICY_SOURCE_UNVERIFIED", "policy sources are not in a recognized reviewable state"
	}
	if strings.TrimSpace(args.PolicyPath) == "" || strings.TrimSpace(args.LegacyPrompt) == "" || strings.TrimSpace(args.SDKVersion) == "" {
		return verifiedPrepareInputs{}, "PREPARE_PINS_REQUIRED", "prepare requires --policy, --legacy-prompt, and --sdk-version"
	}
	if !sdkVersionPattern.MatchString(args.SDKVersion) || args.SDKVersion == "latest" || args.SDKVersion == "main" {
		return verifiedPrepareInputs{}, "SDK_VERSION_INVALID", "--sdk-version must be an exact numeric package version"
	}
	revision := config.Candidate.ExpectedModelRevision
	if !modelRevisionPattern.MatchString(revision) {
		return verifiedPrepareInputs{}, "MODEL_REVISION_INVALID", "candidate.expected_model_revision must be a pinned lowercase Hub commit hash"
	}
	if config.Candidate.ExpectedFamily != "" && !isPlaceholder(config.Candidate.ExpectedFamily) && config.Candidate.ExpectedFamily != "laya-multilingual" {
		return verifiedPrepareInputs{}, "MODEL_FAMILY_MISMATCH", "candidate.expected_family must be empty, a placeholder, or laya-multilingual"
	}
	if !config.ReferenceWorker.OfflineRequired {
		return verifiedPrepareInputs{}, "OFFLINE_REQUIRED", "reference_worker.offline_required must be true"
	}
	policyData, err := readBoundedFile(args.PolicyPath, maxPolicyBytes)
	if err != nil {
		return verifiedPrepareInputs{}, "POLICY_INPUT_INVALID", "cannot read bounded --policy file"
	}
	var policy classifierlab.RoutePolicy
	if err := decodeStrictJSON(policyData, &policy); err != nil {
		return verifiedPrepareInputs{}, "POLICY_INPUT_INVALID", "--policy must be strict RoutePolicy JSON"
	}
	if policy.SemanticReviewStatus != "verified" {
		return verifiedPrepareInputs{}, "POLICY_NOT_VERIFIED", "route_policy.json must set semantic_review_status to verified"
	}
	if policy.PolicyVersion != inspection.Policy.PolicyVersion || !sameStringSequence(policy.Routes, inspection.Policy.Routes) || !sameStringMap(policy.Criteria, inspection.Policy.Criteria) {
		return verifiedPrepareInputs{}, "POLICY_SOURCE_MISMATCH", "verified policy route order or criteria do not match current canonical policy sources"
	}
	if policy.SourcePaths == nil || !sameStrings(policy.SourcePaths, inspection.Policy.SourcePaths) {
		return verifiedPrepareInputs{}, "POLICY_SOURCE_MISMATCH", "verified policy source_paths must exactly name current canonical policy sources"
	}
	if strings.TrimSpace(policy.Instructions) == "" {
		return verifiedPrepareInputs{}, "POLICY_INPUT_INVALID", "verified policy instructions are empty"
	}
	canonicalPolicy, err := canonicalJSON(policy)
	if err != nil {
		return verifiedPrepareInputs{}, "POLICY_INPUT_INVALID", "verified policy cannot be canonicalized"
	}
	policySHA := sha256Hex(canonicalPolicy)
	if config.Candidate.ExpectedPolicySHA256 != "" && !isPlaceholder(config.Candidate.ExpectedPolicySHA256) && config.Candidate.ExpectedPolicySHA256 != policySHA {
		return verifiedPrepareInputs{}, "POLICY_HASH_MISMATCH", "candidate.expected_policy_sha256 differs from the verified policy"
	}
	promptData, err := readBoundedFile(args.LegacyPrompt, maxPolicyBytes)
	if err != nil {
		return verifiedPrepareInputs{}, "LEGACY_PROMPT_INVALID", "cannot read bounded --legacy-prompt file"
	}
	promptSHA := sha256Hex(promptData)
	if !sha256Pattern.MatchString(policy.LegacyPromptSHA256) || promptSHA != policy.LegacyPromptSHA256 {
		return verifiedPrepareInputs{}, "LEGACY_PROMPT_MISMATCH", "--legacy-prompt bytes must match route_policy.legacy_prompt_sha256"
	}
	promptAbs, err := filepath.Abs(args.LegacyPrompt)
	if err != nil {
		return verifiedPrepareInputs{}, "LEGACY_PROMPT_INVALID", "cannot resolve --legacy-prompt provenance path"
	}
	return verifiedPrepareInputs{
		Policy: policy, PolicySHA256: policySHA, PromptPath: promptAbs, PromptSHA256: promptSHA,
		SDKVersion: args.SDKVersion, ModelRevision: revision,
	}, "", ""
}

func writePrepareEvidence(directory string, config classifierlab.Config, inspection repositoryInspection, inputs verifiedPrepareInputs, lockSHA string) error {
	if err := writeJSONExclusive(filepath.Join(directory, "baseline_manifest.json"), inspection.Baseline); err != nil {
		return err
	}
	review := inspection.Review
	review.Status = "verified"
	review.PolicySHA256 = inputs.PolicySHA256
	review.Usage = "verified external route policy accepted after current source consistency and prompt provenance checks; live runtime prompt remains unobserved"
	if err := writeJSONExclusive(filepath.Join(directory, "policy_review.json"), review); err != nil {
		return err
	}
	version, lock := inputs.SDKVersion, lockSHA
	manifest := experimentManifest{
		SchemaVersion: classifierlab.SchemaVersion, ExperimentID: config.ExperimentID,
		RunStatus: "NOT_RUN", CoreCommit: inspection.Baseline.CoreCommit,
		WorkingTreeDirty:        inspection.Baseline.WorkingTreeDirty,
		WorkingTreeDiffSHA256:   inspection.Baseline.WorkingTreeDiffSHA256,
		WorkingTreeStatusSHA256: inspection.Baseline.WorkingTreeStatusSHA256,
		UntrackedSourceSHA256:   inspection.Baseline.UntrackedSourceSHA256,
		SourceSHA256:            inspection.Baseline.SourceSHA256,
		RepositoryPromptSHA256:  inspection.Baseline.RepositoryPromptSHA256,
		LoadedPromptSHA256:      inspection.Baseline.LoadedPromptSHA256,
		LegacyModelUnknown:      inspection.Baseline.LegacyModelUnknown,
		PolicySHA256:            inputs.PolicySHA256, PolicyStatus: "verified",
		ModelFamily: "laya-multilingual", ModelRevision: inputs.ModelRevision,
		Device: config.ReferenceWorker.Device, DType: config.ReferenceWorker.DType,
		SDKVersion: &version, SDKLockSHA256: &lock,
		Seed: config.Evaluation.RandomSeed, BootstrapResamples: config.Evaluation.BootstrapResamples,
		NoninferiorityMargin:           config.Evaluation.NoninferiorityMarginMacroF1,
		CriticalRegressionAllowed:      config.Evaluation.CriticalRegressionAllowedCount,
		SuppliedLegacyPromptPath:       filepath.Clean(inputs.PromptPath),
		SuppliedLegacyPromptSHA256:     inputs.PromptSHA256,
		LegacyPromptRuntimeObservation: "NOT_OBSERVED",
	}
	manifestID, err := experimentManifestID(manifest)
	if err != nil {
		return err
	}
	manifest.ManifestID = manifestID
	contract := adapterContract{
		Status: "UNVERIFIED", Distribution: layaDistribution, SourceURL: layaSourceURL,
		SDKVersion: &version, LockSHA256: &lock, Model: "multilingual",
		PredictAPI: `Router.predict(message, questions, model="multilingual", max_len=..., head_max_len=...)`,
		MaxLen:     config.ReferenceWorker.MaxLen, HeadMaxLen: config.ReferenceWorker.HeadMaxLen,
		Checks: map[string]bool{
			"pinned_sdk_version": false, "router_predict_signature": false,
			"multilingual_checkpoint_fixed": false, "choice_output_and_probabilities": false,
			"token_truncation_metadata": false, "offline_configuration_verified": false,
			"network_isolation_verified": false,
		},
		Reason: "Exact package and model pins are recorded; SDK output, signature, truncation, offline configuration, and network isolation remain unverified until smoke and independent isolation evidence.",
	}
	if err := writeJSONExclusive(filepath.Join(directory, classifierlab.PreparedPolicyFileName), inputs.Policy); err != nil {
		return err
	}
	if err := writeJSONExclusive(filepath.Join(directory, "experiment_manifest.json"), manifest); err != nil {
		return err
	}
	return writeJSONExclusive(filepath.Join(directory, "adapter_contract.json"), contract)
}

func writePrepareFailure(directory string, config classifierlab.Config, inspection repositoryInspection, inputs verifiedPrepareInputs, code, reason string, attempted bool, pythonVersion string) error {
	if _, err := os.Stat(filepath.Join(directory, "experiment_manifest.json")); errors.Is(err, os.ErrNotExist) {
		lock := ""
		if data, readErr := os.ReadFile(filepath.Join(directory, "requirements.lock")); readErr == nil {
			lock = sha256Hex(data)
		}
		if inputs.PolicySHA256 != "" {
			if err := writePrepareEvidence(directory, config, inspection, inputs, lock); err != nil {
				return err
			}
		} else if err := writeRunEvidence(directory, config, inspection, "", "", nil); err != nil {
			return err
		}
	}
	status := prepareStatus{
		SchemaVersion: classifierlab.SchemaVersion, Status: "FAILED", ReasonCode: code,
		Reasons: []string{reason}, OnlineAcquisitionAttempted: attempted, ModelInvoked: false,
		PythonVersion: pythonVersion, SDKVersion: inputs.SDKVersion, ModelRevision: inputs.ModelRevision,
		AdapterContractStatus: "UNVERIFIED",
	}
	if err := writeJSONExclusive(filepath.Join(directory, "prepare_status.json"), status); err != nil {
		return err
	}
	return commandError{Code: code, Err: errors.New(reason)}
}

func validateResolvedPackages(packages map[string]string, sdkVersion string) error {
	if packages["laya"] != sdkVersion {
		return errors.New("resolved package set does not contain the exact requested laya version")
	}
	if _, ok := packages["huggingface-hub"]; !ok {
		return errors.New("resolved package set does not include huggingface-hub")
	}
	for name, version := range packages {
		if name == "" || normalizeDistributionName(name) != name || !sdkVersionPattern.MatchString(version) {
			return fmt.Errorf("resolved package %q has an invalid name or exact version", name)
		}
	}
	return nil
}

func validateRequirementsLock(packages map[string]string, lock []byte) error {
	if len(lock) == 0 {
		return errors.New("requirements lock is empty")
	}
	lines := strings.Split(strings.TrimSuffix(string(lock), "\n"), "\n")
	if len(lines) != len(packages) {
		return errors.New("requirements lock package count does not match the resolved package set")
	}
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		parts := strings.Split(line, " --hash=sha256:")
		if len(parts) != 2 || !sha256Pattern.MatchString(parts[1]) {
			return errors.New("requirements lock must pin every package with a SHA-256 artifact hash")
		}
		name, version, found := strings.Cut(parts[0], "==")
		if !found || name == "" || normalizeDistributionName(name) != name || !sdkVersionPattern.MatchString(version) {
			return errors.New("requirements lock contains an invalid exact package pin")
		}
		if _, duplicate := seen[name]; duplicate || packages[name] != version {
			return errors.New("requirements lock does not match the unique resolved package set")
		}
		seen[name] = struct{}{}
	}
	return nil
}

func cleanupPrepareTemporaryFiles(output string) error {
	for _, name := range []string{"cache", "tmp", "home"} {
		if err := os.RemoveAll(filepath.Join(output, name)); err != nil {
			return err
		}
	}
	return nil
}

func samePolicySourceHashes(before, after map[string]string) bool {
	for _, path := range []string{"internal/domain/routing/route.go", "rules/routing-policy.md", "prompts/classifier.md"} {
		if before[path] == "" || before[path] != after[path] {
			return false
		}
	}
	return true
}

func sameStringSequence(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func isPlaceholder(value string) bool {
	return value == "" || strings.HasPrefix(strings.TrimSpace(value), "REPLACE_")
}

func hashPreparedModelTree(root string) ([]classifierlab.PreparedModelFile, string, error) {
	files := make([]classifierlab.PreparedModelFile, 0, 32)
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == root || entry.IsDir() {
			return nil
		}
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("model entry %q is not a regular file", name)
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		if len(files) >= classifierlab.MaxPreparedModelFiles {
			return errors.New("model tree exceeds the prepared-file count limit")
		}
		digest, err := fileSHA256(name)
		if err != nil {
			return err
		}
		files = append(files, classifierlab.PreparedModelFile{Path: filepath.ToSlash(relative), SHA256: digest})
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	if len(files) == 0 {
		return nil, "", errors.New("model tree contains no files")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	canonical, err := json.Marshal(files)
	if err != nil {
		return nil, "", err
	}
	return files, sha256Hex(canonical), nil
}

type systemPrepareBackend struct{}

func (systemPrepareBackend) CreateVenv(ctx context.Context, basePython, envDirectory string) (string, string, error) {
	if strings.TrimSpace(basePython) == "" {
		basePython = "python3"
		if runtime.GOOS == "windows" {
			basePython = "python"
		}
	}
	resolved, err := exec.LookPath(basePython)
	if err != nil {
		return "", "", errors.New("base Python executable was not found")
	}
	if err := os.Mkdir(envDirectory, 0o700); err != nil {
		return "", "", err
	}
	privateRoot := filepath.Dir(envDirectory)
	if err := runPrivateCommand(ctx, resolved, []string{"-I", "-m", "venv", envDirectory}, privateRoot); err != nil {
		return "", "", errors.New("venv subprocess failed")
	}
	python := venvPythonPath(envDirectory)
	var versionBuffer boundedBuffer
	if err := runPrivateCommandWithWriter(ctx, python, []string{"-I", "-c", "import platform; print(platform.python_version())"}, privateRoot, &versionBuffer); err != nil {
		return "", "", errors.New("venv Python version query failed")
	}
	version := strings.TrimSpace(versionBuffer.String())
	if !sdkVersionPattern.MatchString(version) || !supportsLayaPython(version) {
		return "", version, errors.New("venv Python must be an exact version at or above 3.10")
	}
	return python, version, nil
}

func supportsLayaPython(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	return majorErr == nil && minorErr == nil && (major > 3 || major == 3 && minor >= 10)
}

func (systemPrepareBackend) InstallSDK(ctx context.Context, python, outputDirectory, version string) (map[string]string, []byte, error) {
	reportPath := filepath.Join(outputDirectory, "pip-install-report.json")
	lockPath := filepath.Join(outputDirectory, ".prepare-requirements.lock")
	resolveArguments := []string{
		"-I", "-m", "pip", "install", "--dry-run", "--ignore-installed", "--only-binary=:all:", "--disable-pip-version-check", "--no-cache-dir", "--no-input",
		"--index-url", pypiIndexURL, "--report", reportPath, layaDistribution + "==" + version,
	}
	if err := runPrivateCommand(ctx, python, resolveArguments, outputDirectory); err != nil {
		_ = os.Remove(reportPath)
		return nil, nil, errors.New("pinned SDK dependency resolution failed")
	}
	defer func() {
		_ = os.Remove(reportPath)
		_ = os.Remove(lockPath)
	}()
	report, err := readBoundedFile(reportPath, maxPipReport)
	if err != nil {
		return nil, nil, errors.New("pip did not produce a bounded install report")
	}
	packages, hashes, err := parsePipInstallReport(report)
	if err != nil {
		return nil, nil, err
	}
	if packages[layaDistribution] != version {
		return nil, nil, errors.New("official PyPI resolution did not select the exact requested laya version")
	}
	lock := renderRequirementsLock(packages, hashes)
	if err := validateRequirementsLock(packages, lock); err != nil {
		return nil, nil, err
	}
	lockFile, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, nil, errors.New("cannot create private temporary SDK lock")
	}
	if _, err := lockFile.Write(lock); err != nil {
		_ = lockFile.Close()
		return nil, nil, errors.New("cannot write private temporary SDK lock")
	}
	if err := lockFile.Close(); err != nil {
		return nil, nil, errors.New("cannot close private temporary SDK lock")
	}
	installArguments := []string{
		"-I", "-m", "pip", "install", "--require-hashes", "--no-deps", "--disable-pip-version-check",
		"--only-binary=:all:", "--no-cache-dir", "--no-input", "--index-url", pypiIndexURL, "-r", lockPath,
	}
	if err := runPrivateCommand(ctx, python, installArguments, outputDirectory); err != nil {
		return nil, nil, errors.New("hash-locked SDK installation failed")
	}
	var listBuffer boundedBuffer
	if err := runPrivateCommandWithWriter(ctx, python, []string{"-I", "-m", "pip", "list", "--format=json"}, outputDirectory, &listBuffer); err != nil {
		return nil, nil, errors.New("installed package inventory failed")
	}
	installed, err := parsePipList(listBuffer.Bytes())
	if err != nil {
		return nil, nil, errors.New("installed package inventory is invalid")
	}
	for name, version := range packages {
		if installed[name] != version {
			return nil, nil, fmt.Errorf("resolved SDK package %q does not match the installed inventory", name)
		}
	}
	for name := range installed {
		if _, expected := packages[name]; !expected && name != "pip" && name != "setuptools" && name != "wheel" {
			return nil, nil, fmt.Errorf("venv contains unrecorded package %q", name)
		}
	}
	return packages, lock, nil
}

func (systemPrepareBackend) DownloadModel(ctx context.Context, python, modelDirectory, revision string) error {
	if !modelRevisionPattern.MatchString(revision) {
		return errors.New("model revision is not an exact Hub commit hash")
	}
	const script = "import sys; from huggingface_hub import snapshot_download; snapshot_download(repo_id=sys.argv[1], revision=sys.argv[2], local_dir=sys.argv[3], token=False)"
	if err := runPrivateCommand(ctx, python, []string{"-I", "-c", script, layaModelSource, revision, modelDirectory}, filepath.Dir(modelDirectory)); err != nil {
		return errors.New("pinned Hub model download subprocess failed")
	}
	return nil
}

type pipReport struct {
	Install []struct {
		Metadata struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"metadata"`
		DownloadInfo struct {
			URL         string `json:"url"`
			ArchiveInfo struct {
				Hashes map[string]string `json:"hashes"`
			} `json:"archive_info"`
		} `json:"download_info"`
	} `json:"install"`
}

func parsePipInstallReport(data []byte) (map[string]string, map[string]string, error) {
	var report pipReport
	if err := json.Unmarshal(data, &report); err != nil || len(report.Install) == 0 {
		return nil, nil, errors.New("pip install report is malformed or empty")
	}
	packages, hashes := make(map[string]string, len(report.Install)), make(map[string]string, len(report.Install))
	for _, entry := range report.Install {
		name := normalizeDistributionName(entry.Metadata.Name)
		version := entry.Metadata.Version
		digest := entry.DownloadInfo.ArchiveInfo.Hashes["sha256"]
		parsedURL, err := url.Parse(entry.DownloadInfo.URL)
		if name == "" || !sdkVersionPattern.MatchString(version) || !sha256Pattern.MatchString(digest) || err != nil ||
			parsedURL.Scheme != "https" || parsedURL.Host != "files.pythonhosted.org" || parsedURL.User != nil {
			return nil, nil, errors.New("pip report contains a package without an exact version, official PyPI artifact URL, or SHA-256")
		}
		if _, exists := packages[name]; exists {
			return nil, nil, fmt.Errorf("pip report repeats package %q", name)
		}
		packages[name], hashes[name] = version, digest
	}
	return packages, hashes, nil
}

func parsePipList(data []byte) (map[string]string, error) {
	var entries []struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &entries); err != nil || len(entries) == 0 {
		return nil, errors.New("pip list is malformed or empty")
	}
	packages := make(map[string]string, len(entries))
	for _, entry := range entries {
		name := normalizeDistributionName(entry.Name)
		if name == "" || !sdkVersionPattern.MatchString(entry.Version) {
			return nil, errors.New("pip list contains an invalid package")
		}
		if _, exists := packages[name]; exists {
			return nil, fmt.Errorf("pip list repeats package %q", name)
		}
		packages[name] = entry.Version
	}
	return packages, nil
}

func renderRequirementsLock(packages, hashes map[string]string) []byte {
	names := make([]string, 0, len(packages))
	for name := range packages {
		names = append(names, name)
	}
	sort.Strings(names)
	var builder strings.Builder
	for _, name := range names {
		fmt.Fprintf(&builder, "%s==%s --hash=sha256:%s\n", name, packages[name], hashes[name])
	}
	return []byte(builder.String())
}

func runPrivateCommand(ctx context.Context, executable string, arguments []string, privateRoot string) error {
	return runPrivateCommandWithWriter(ctx, executable, arguments, privateRoot, io.Discard)
}

func runPrivateCommandWithWriter(ctx context.Context, executable string, arguments []string, privateRoot string, output io.Writer) error {
	commandCtx, cancel := context.WithTimeout(ctx, prepareTimeout)
	defer cancel()
	command := exec.CommandContext(commandCtx, executable, arguments...)
	command.Env = isolatedPythonEnvironment(privateRoot)
	command.Stdout = output
	command.Stderr = output
	if err := command.Run(); err != nil {
		return errors.New("private preparation subprocess failed")
	}
	return nil
}

func isolatedPythonEnvironment(privateRoot string) []string {
	allowed := make(map[string]string)
	for _, pair := range os.Environ() {
		key, value, found := strings.Cut(pair, "=")
		if !found {
			continue
		}
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "PYTHON") || strings.HasPrefix(upper, "PIP_") || strings.HasPrefix(upper, "HF_") ||
			strings.Contains(upper, "HUGGINGFACE") || upper == "TRANSFORMERS_CACHE" || upper == "XDG_CACHE_HOME" ||
			upper == "HOME" || upper == "USERPROFILE" || upper == "APPDATA" || upper == "LOCALAPPDATA" ||
			upper == "TMPDIR" || upper == "TEMP" || upper == "TMP" {
			continue
		}
		allowed[key] = value
	}
	cache := filepath.Join(privateRoot, "cache")
	privateTemp := filepath.Join(privateRoot, "tmp")
	for _, directory := range []string{cache, filepath.Join(privateRoot, "home"), privateTemp} {
		_ = os.MkdirAll(directory, 0o700)
		_ = os.Chmod(directory, 0o700)
	}
	allowed["HOME"] = filepath.Join(privateRoot, "home")
	allowed["USERPROFILE"] = filepath.Join(privateRoot, "home")
	allowed["TMPDIR"] = privateTemp
	allowed["TEMP"] = privateTemp
	allowed["TMP"] = privateTemp
	allowed["PYTHONNOUSERSITE"] = "1"
	allowed["PIP_CONFIG_FILE"] = os.DevNull
	allowed["PIP_REQUIRE_VIRTUALENV"] = "1"
	allowed["PIP_NO_INPUT"] = "1"
	allowed["PIP_DISABLE_PIP_VERSION_CHECK"] = "1"
	allowed["HF_HOME"] = filepath.Join(cache, "huggingface")
	allowed["HF_HUB_DISABLE_TELEMETRY"] = "1"
	allowed["HF_HUB_DISABLE_IMPLICIT_TOKEN"] = "1"
	allowed["TOKENIZERS_PARALLELISM"] = "false"
	keys := make([]string, 0, len(allowed))
	for key := range allowed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		environment = append(environment, key+"="+allowed[key])
	}
	return environment
}

func venvPythonPath(directory string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(directory, "Scripts", "python.exe")
	}
	return filepath.Join(directory, "bin", "python")
}

type boundedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	limit  int
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	original := len(data)
	if buffer.limit == 0 {
		buffer.limit = 64 << 10
	}
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = buffer.buffer.Write(data)
	}
	return original, nil
}

func (buffer *boundedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func (buffer *boundedBuffer) Bytes() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return append([]byte(nil), buffer.buffer.Bytes()...)
}

func normalizeDistributionName(value string) string {
	var builder strings.Builder
	separator := false
	for _, character := range strings.ToLower(value) {
		if character == '-' || character == '_' || character == '.' {
			if !separator {
				builder.WriteByte('-')
			}
			separator = true
			continue
		}
		separator = false
		builder.WriteRune(character)
	}
	return builder.String()
}
