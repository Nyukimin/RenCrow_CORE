package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing/classifierlab"
)

const maxFixtureLineBytes = 1 << 20

type smokeFixture struct {
	ID          string `json:"id"`
	Language    string `json:"language"`
	Message     string `json:"message"`
	RepeatText  string `json:"repeat_text,omitempty"`
	RepeatCount int    `json:"repeat_count,omitempty"`
	Suffix      string `json:"suffix,omitempty"`
	Boundary    string `json:"boundary"`
}

type blockedStatus struct {
	SchemaVersion              string   `json:"schema_version"`
	Status                     string   `json:"status"`
	ReasonCode                 string   `json:"reason_code"`
	Reasons                    []string `json:"reasons"`
	ManifestID                 string   `json:"manifest_id,omitempty"`
	ExperimentID               string   `json:"experiment_id,omitempty"`
	OnlineAcquisitionAttempted bool     `json:"online_acquisition_attempted,omitempty"`
	ModelInvoked               bool     `json:"model_invoked"`
	CaseCount                  int      `json:"case_count,omitempty"`
	DatasetSHA256              string   `json:"dataset_sha256,omitempty"`
	Split                      string   `json:"split,omitempty"`
	Variants                   []string `json:"variants,omitempty"`
	Seed                       int64    `json:"seed,omitempty"`
	IsolatedRequested          bool     `json:"isolated_requested,omitempty"`
	IsolationVerified          bool     `json:"isolation_verified,omitempty"`
	SideEffectCount            *int     `json:"side_effect_count,omitempty"`
}

type runManifest struct {
	SchemaVersion             string                         `json:"schema_version"`
	ManifestID                string                         `json:"manifest_id"`
	Status                    string                         `json:"status"`
	ReasonCode                string                         `json:"reason_code"`
	ExperimentID              string                         `json:"experiment_id"`
	DatasetSHA256             string                         `json:"dataset_sha256"`
	Split                     string                         `json:"split"`
	CaseCount                 int                            `json:"case_count"`
	Variants                  []string                       `json:"variants"`
	VariantStatuses           map[string]variantReplayStatus `json:"variant_statuses,omitempty"`
	Seed                      int64                          `json:"seed"`
	PolicyStatus              string                         `json:"policy_status"`
	ModelInvoked              bool                           `json:"model_invoked"`
	BootstrapResamples        int                            `json:"bootstrap_resamples"`
	NoninferiorityMargin      float64                        `json:"noninferiority_margin_macro_f1"`
	CriticalRegressionAllowed int                            `json:"critical_regression_allowed_count"`
}

type experimentManifest struct {
	SchemaVersion                  string                         `json:"schema_version"`
	ManifestID                     string                         `json:"manifest_id"`
	ExperimentID                   string                         `json:"experiment_id"`
	RunStatus                      string                         `json:"run_status"`
	CoreCommit                     string                         `json:"core_commit"`
	WorkingTreeDirty               bool                           `json:"working_tree_dirty"`
	WorkingTreeDiffSHA256          string                         `json:"working_tree_diff_sha256"`
	WorkingTreeStatusSHA256        string                         `json:"working_tree_status_sha256"`
	UntrackedSourceSHA256          map[string]string              `json:"untracked_source_sha256"`
	SourceSHA256                   map[string]string              `json:"source_sha256"`
	RepositoryPromptSHA256         string                         `json:"repository_prompt_sha256"`
	LoadedPromptSHA256             *string                        `json:"loaded_prompt_sha256"`
	LegacyModelUnknown             bool                           `json:"legacy_model_unknown"`
	PolicySHA256                   string                         `json:"policy_sha256"`
	PolicyStatus                   string                         `json:"policy_status"`
	ModelFamily                    string                         `json:"model_family"`
	ModelRevision                  string                         `json:"model_revision"`
	Device                         string                         `json:"device"`
	DType                          string                         `json:"dtype"`
	SDKVersion                     *string                        `json:"sdk_version"`
	SDKLockSHA256                  *string                        `json:"sdk_lock_sha256"`
	PreparedManifestSHA256         string                         `json:"prepared_manifest_sha256,omitempty"`
	ModelFilesSHA256               string                         `json:"model_files_sha256,omitempty"`
	DatasetSHA256                  string                         `json:"dataset_sha256,omitempty"`
	Split                          string                         `json:"split,omitempty"`
	Variants                       []string                       `json:"variants,omitempty"`
	VariantStatuses                map[string]variantReplayStatus `json:"variant_statuses,omitempty"`
	Seed                           int64                          `json:"seed"`
	BootstrapResamples             int                            `json:"bootstrap_resamples"`
	NoninferiorityMargin           float64                        `json:"noninferiority_margin_macro_f1"`
	CriticalRegressionAllowed      int                            `json:"critical_regression_allowed_count"`
	SuppliedLegacyPromptPath       string                         `json:"supplied_legacy_prompt_path,omitempty"`
	SuppliedLegacyPromptSHA256     string                         `json:"supplied_legacy_prompt_sha256,omitempty"`
	LegacyPromptRuntimeObservation string                         `json:"legacy_prompt_runtime_observation,omitempty"`
}

type adapterContract struct {
	Status       string          `json:"status"`
	Distribution string          `json:"distribution"`
	SourceURL    string          `json:"source_url"`
	SDKVersion   *string         `json:"sdk_version"`
	LockSHA256   *string         `json:"lock_sha256"`
	Model        string          `json:"model"`
	PredictAPI   string          `json:"predict_api"`
	MaxLen       int             `json:"max_len"`
	HeadMaxLen   int             `json:"head_max_len"`
	Checks       map[string]bool `json:"checks"`
	Reason       string          `json:"reason"`
}

func writeRunEvidence(directory string, config classifierlab.Config, inspection repositoryInspection, datasetSHA, split string, variants []string) error {
	for _, artifact := range []struct {
		name  string
		value any
	}{
		{name: "baseline_manifest.json", value: inspection.Baseline},
		{name: classifierlab.PreparedPolicyFileName, value: inspection.Policy},
		{name: "policy_review.json", value: inspection.Review},
	} {
		if err := writeJSONExclusive(filepath.Join(directory, artifact.name), artifact.value); err != nil {
			return err
		}
	}
	manifest, err := buildExperimentManifest(config, inspection, datasetSHA, split, variants)
	if err != nil {
		return err
	}
	if err := writeJSONExclusive(filepath.Join(directory, "experiment_manifest.json"), manifest); err != nil {
		return err
	}
	contract := adapterContract{
		Status:       "UNVERIFIED",
		Distribution: "laya",
		SourceURL:    "https://github.com/NandhaKishorM/laya",
		SDKVersion:   nil,
		LockSHA256:   nil,
		Model:        "multilingual",
		PredictAPI:   `Router.predict(message, questions, model="multilingual", max_len=..., head_max_len=...)`,
		MaxLen:       config.ReferenceWorker.MaxLen,
		HeadMaxLen:   config.ReferenceWorker.HeadMaxLen,
		Checks: map[string]bool{
			"pinned_sdk_version":              false,
			"router_predict_signature":        false,
			"multilingual_checkpoint_fixed":   false,
			"choice_output_and_probabilities": false,
			"token_truncation_metadata":       false,
			"offline_configuration_verified":  false,
			"network_isolation_verified":      false,
		},
		Reason: "No exact SDK lock or prepared isolated environment exists; no SDK or model was invoked.",
	}
	return writeJSONExclusive(filepath.Join(directory, "adapter_contract.json"), contract)
}

func buildExperimentManifest(config classifierlab.Config, inspection repositoryInspection, datasetSHA, split string, variants []string) (experimentManifest, error) {
	manifest := experimentManifest{
		SchemaVersion: classifierlab.SchemaVersion, ExperimentID: config.ExperimentID,
		RunStatus: "BLOCKED", CoreCommit: inspection.Baseline.CoreCommit,
		WorkingTreeDirty:          inspection.Baseline.WorkingTreeDirty,
		WorkingTreeDiffSHA256:     inspection.Baseline.WorkingTreeDiffSHA256,
		WorkingTreeStatusSHA256:   inspection.Baseline.WorkingTreeStatusSHA256,
		UntrackedSourceSHA256:     inspection.Baseline.UntrackedSourceSHA256,
		SourceSHA256:              inspection.Baseline.SourceSHA256,
		RepositoryPromptSHA256:    inspection.Baseline.RepositoryPromptSHA256,
		LoadedPromptSHA256:        inspection.Baseline.LoadedPromptSHA256,
		LegacyModelUnknown:        inspection.Baseline.LegacyModelUnknown,
		PolicySHA256:              inspection.Review.PolicySHA256,
		PolicyStatus:              inspection.Review.Status,
		ModelFamily:               config.Candidate.ExpectedFamily,
		ModelRevision:             config.Candidate.ExpectedModelRevision,
		Device:                    config.ReferenceWorker.Device,
		DType:                     config.ReferenceWorker.DType,
		SDKVersion:                nil,
		SDKLockSHA256:             nil,
		DatasetSHA256:             datasetSHA,
		Split:                     split,
		Variants:                  append([]string(nil), variants...),
		Seed:                      config.Evaluation.RandomSeed,
		BootstrapResamples:        config.Evaluation.BootstrapResamples,
		NoninferiorityMargin:      config.Evaluation.NoninferiorityMarginMacroF1,
		CriticalRegressionAllowed: config.Evaluation.CriticalRegressionAllowedCount,
	}
	manifestID, err := experimentManifestID(manifest)
	if err != nil {
		return experimentManifest{}, err
	}
	manifest.ManifestID = manifestID
	return manifest, nil
}

func loadSmokeFixtures(name string) ([]smokeFixture, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open smoke fixtures: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), maxFixtureLineBytes)
	fixtures := make([]smokeFixture, 0)
	seen := make(map[string]struct{})
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := scanner.Bytes()
		if len(line) == 0 {
			return nil, fmt.Errorf("smoke fixture line %d is empty", lineNumber)
		}
		var fixture smokeFixture
		if err := decodeStrictJSON(line, &fixture); err != nil {
			return nil, fmt.Errorf("decode smoke fixture line %d: %w", lineNumber, err)
		}
		if !smokeFixtureIDPattern.MatchString(fixture.ID) || strings.TrimSpace(fixture.Message) == "" {
			return nil, fmt.Errorf("smoke fixture line %d requires id and message", lineNumber)
		}
		if fixture.Language != "ja" && fixture.Language != "en" && fixture.Language != "mixed" {
			return nil, fmt.Errorf("smoke fixture line %d has unsupported language", lineNumber)
		}
		switch fixture.Boundary {
		case "normal", "long_input", "policy_long":
		default:
			return nil, fmt.Errorf("smoke fixture line %d has unsupported boundary", lineNumber)
		}
		if fixture.Boundary == "long_input" {
			if fixture.RepeatCount < 8500 || fixture.RepeatCount > 12000 || fixture.RepeatText == "" || len(fixture.RepeatText) > 32 {
				return nil, fmt.Errorf("smoke fixture line %d long_input requires bounded repeated text with 8500 to 12000 repetitions", lineNumber)
			}
		} else if fixture.RepeatCount != 0 || fixture.RepeatText != "" || fixture.Suffix != "" {
			return nil, fmt.Errorf("smoke fixture line %d repeat fields are only allowed for long_input", lineNumber)
		}
		if _, err := smokeFixtureMessage(fixture); err != nil {
			return nil, fmt.Errorf("smoke fixture line %d: %w", lineNumber, err)
		}
		if _, exists := seen[fixture.ID]; exists {
			return nil, fmt.Errorf("duplicate smoke fixture id %q", fixture.ID)
		}
		seen[fixture.ID] = struct{}{}
		fixtures = append(fixtures, fixture)
		if len(fixtures) > 1000 {
			return nil, errors.New("smoke fixture set exceeds 1000 cases")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read smoke fixtures: %w", err)
	}
	if len(fixtures) == 0 {
		return nil, errors.New("smoke fixture set is empty")
	}
	return fixtures, nil
}

func runReplay(configPath, datasetPath, split, variantsValue, outPath, repoRoot string) error {
	variants, err := parseVariants(variantsValue)
	if err != nil {
		return err
	}
	return runReplayCommand(configPath, datasetPath, split, variants, outPath, repoRoot)
}

func parseVariants(value string) ([]string, error) {
	allowed := map[string]bool{"legacy": true, "candidate": true, "hybrid-replay": true, "rules-only": true}
	variants := strings.Split(value, ",")
	seen := make(map[string]struct{}, len(variants))
	for index, variant := range variants {
		variant = strings.TrimSpace(variant)
		if !allowed[variant] {
			return nil, fmt.Errorf("unsupported replay variant %q", variant)
		}
		if _, exists := seen[variant]; exists {
			return nil, fmt.Errorf("duplicate replay variant %q", variant)
		}
		seen[variant] = struct{}{}
		variants[index] = variant
	}
	if len(variants) == 0 {
		return nil, errors.New("at least one replay variant is required")
	}
	return variants, nil
}

func runE2E(configPath, datasetPath, outPath string, isolated bool, repoRoot string) error {
	config, err := loadExperimentConfig(configPath)
	if err != nil {
		return err
	}
	cases, err := classifierlab.LoadReplayDataset(datasetPath)
	if err != nil {
		return err
	}
	inspection, err := inspectRepository(repoRoot)
	if err != nil {
		return err
	}
	datasetSHA, err := fileSHA256(datasetPath)
	if err != nil {
		return err
	}
	directory, err := createPrivateOutputDirectory(outPath)
	if err != nil {
		return err
	}
	status := blockedStatus{
		SchemaVersion: classifierlab.SchemaVersion, Status: "BLOCKED", ReasonCode: "ISOLATION_ADAPTER_UNAVAILABLE",
		Reasons: []string{
			"real E2E needs the observed legacy provider and loaded prompt plus a verified prepared candidate model; the in-process fake-provider harness is test-only",
		},
		ModelInvoked: false, CaseCount: len(cases), DatasetSHA256: datasetSHA,
		ExperimentID: config.ExperimentID, IsolatedRequested: isolated,
	}
	if err := writeRunEvidence(directory, config, inspection, datasetSHA, "e2e", nil); err != nil {
		return err
	}
	e2eManifest, err := readExperimentManifest(directory)
	if err != nil {
		return fmt.Errorf("read generated E2E provenance: %w", err)
	}
	status.ManifestID = e2eManifest.ManifestID
	if !isolated {
		status.ReasonCode = "ISOLATION_REQUIRED"
		status.Reasons = []string{"--isolated is required for E2E; no runtime services were started"}
	}
	if err := writeJSONExclusive(filepath.Join(directory, "e2e_status.json"), status); err != nil {
		return err
	}
	return commandError{Code: status.ReasonCode, Err: errors.New(status.Reasons[0])}
}

type reportMetrics struct {
	SchemaVersion                 string                                  `json:"schema_version"`
	ManifestID                    string                                  `json:"manifest_id,omitempty"`
	Status                        string                                  `json:"status"`
	ModelDecision                 string                                  `json:"model_decision"`
	Variants                      map[string]classifierlab.VariantMetrics `json:"variants"`
	Bootstrap                     *classifierlab.PairedBootstrapInterval  `json:"paired_bootstrap,omitempty"`
	E2EStatus                     string                                  `json:"e2e_status"`
	E2ESideEffectCount            *int                                    `json:"e2e_side_effect_count,omitempty"`
	AdapterContractStatus         string                                  `json:"adapter_contract_status"`
	PerformanceEvidence           reportPerformanceEvidence               `json:"performance_evidence"`
	LegacyModelUnknown            bool                                    `json:"legacy_model_unknown"`
	LoadedPromptObserved          bool                                    `json:"loaded_prompt_observed"`
	VerifiedRealConversationCases map[string]int                          `json:"verified_real_conversation_cases"`
	MissingVerifiedRouteSupport   map[string][]string                     `json:"missing_verified_route_support"`
	Issues                        []string                                `json:"issues"`
}

type reportPerformanceEvidence struct {
	TTFTStatus                string `json:"ttft_status"`
	FinalResponseStatus       string `json:"final_response_status"`
	SharedResourceStatus      string `json:"shared_resource_contention_status"`
	RepeatedMeasurementStatus string `json:"repeated_measurement_status"`
}

func unavailablePerformanceEvidence() reportPerformanceEvidence {
	return reportPerformanceEvidence{
		TTFTStatus: "UNAVAILABLE", FinalResponseStatus: "UNAVAILABLE",
		SharedResourceStatus: "UNAVAILABLE", RepeatedMeasurementStatus: "UNAVAILABLE",
	}
}

func runReport(runPath, e2ePath, outPath string) error {
	directory, err := createPrivateOutputDirectory(outPath)
	if err != nil {
		return err
	}
	result := reportMetrics{
		SchemaVersion: classifierlab.SchemaVersion, Status: "INCONCLUSIVE",
		ModelDecision: "INCONCLUSIVE", Variants: make(map[string]classifierlab.VariantMetrics),
		E2EStatus: "NOT_RUN", AdapterContractStatus: "UNVERIFIED", LegacyModelUnknown: true,
		PerformanceEvidence:           unavailablePerformanceEvidence(),
		VerifiedRealConversationCases: make(map[string]int), MissingVerifiedRouteSupport: make(map[string][]string),
		Issues: make([]string, 0),
	}
	if err := copyRunProvenance(runPath, directory); err != nil {
		result.Issues = append(result.Issues, "run provenance is incomplete")
	}
	experiment, experimentErr := readExperimentManifest(runPath)
	predictionPath := filepath.Join(runPath, "predictions.jsonl")
	records, loadErr := classifierlab.LoadReplayPredictions(predictionPath)
	if loadErr != nil {
		if errors.Is(loadErr, os.ErrNotExist) {
			result.Status = "NOT_RUN"
			result.Issues = append(result.Issues, "predictions.jsonl is absent; no replay predictions exist")
		} else {
			result.Status = "INVALID"
			result.Issues = append(result.Issues, "predictions.jsonl failed strict validation")
		}
	} else {
		result.VerifiedRealConversationCases, result.MissingVerifiedRouteSupport = verifiedRealConversationSupport(records)
		if experimentErr != nil {
			result.Status = "INVALID"
			result.Issues = append(result.Issues, "experiment manifest is missing or invalid")
		} else {
			result.ManifestID = experiment.ManifestID
			for _, record := range records {
				if record.ManifestID != experiment.ManifestID {
					result.Status = "INVALID"
					result.Issues = append(result.Issues, "prediction manifest ID does not match experiment manifest")
					break
				}
			}
		}
		run, runErr := readRunManifest(runPath)
		if runErr != nil || experiment.ManifestID != run.ManifestID || experiment.ExperimentID != run.ExperimentID ||
			experiment.RunStatus != run.Status || experiment.DatasetSHA256 != run.DatasetSHA256 ||
			experiment.Split != run.Split || !sameStrings(experiment.Variants, run.Variants) ||
			experiment.PolicyStatus != run.PolicyStatus || experiment.Seed != run.Seed ||
			experiment.BootstrapResamples != run.BootstrapResamples ||
			experiment.NoninferiorityMargin != run.NoninferiorityMargin ||
			experiment.CriticalRegressionAllowed != run.CriticalRegressionAllowed || !sameVariantStatuses(experiment.VariantStatuses, run.VariantStatuses) {
			result.Status = "INVALID"
			result.Issues = append(result.Issues, "run manifest does not match experiment provenance")
		} else if run.Status != "COMPLETED" {
			result.Issues = append(result.Issues, "replay run is partial or blocked; unavailable variants remain unmeasured")
		}
		variantNames := make(map[string]struct{})
		for _, record := range records {
			variantNames[record.Variant] = struct{}{}
		}
		orderedVariants := make([]string, 0, len(variantNames))
		for variant := range variantNames {
			orderedVariants = append(orderedVariants, variant)
		}
		sort.Strings(orderedVariants)
		for _, variant := range orderedVariants {
			metrics, err := classifierlab.ComputeVariantMetrics(records, variant)
			if err != nil {
				result.Status = "INVALID"
				result.Issues = append(result.Issues, "metrics invalid for variant "+variant)
				continue
			}
			result.Variants[variant] = metrics
		}
		expectedVariants := completedVariantNames(run.VariantStatuses)
		if len(run.VariantStatuses) == 0 {
			expectedVariants = run.Variants
		}
		if len(variantNames) != len(expectedVariants) {
			result.Status = "INVALID"
			result.Issues = append(result.Issues, "prediction variants do not match the run manifest")
		} else {
			for _, variant := range expectedVariants {
				if _, exists := variantNames[variant]; !exists {
					result.Status = "INVALID"
					result.Issues = append(result.Issues, "prediction variants do not match the run manifest")
					break
				}
			}
		}
		for _, variant := range run.Variants {
			status := run.VariantStatuses[variant]
			if status.Status != "COMPLETED" && status.Status != "COMPLETED_WITH_ERRORS" {
				result.Issues = append(result.Issues, variant+" replay was not completed: "+status.ReasonCode)
			}
		}
		if _, hasCandidate := variantNames["candidate"]; hasCandidate {
			resamples, seed := loadBootstrapSettings(runPath)
			bootstrap, err := classifierlab.BootstrapPairedMacroF1(records, resamples, seed)
			result.Bootstrap = &bootstrap
			if err != nil {
				result.Issues = append(result.Issues, "paired bootstrap is inconclusive: "+safeReason(err))
			}
		}
	}
	if experimentErr == nil {
		result.LegacyModelUnknown = experiment.LegacyModelUnknown
		result.LoadedPromptObserved = experiment.LoadedPromptSHA256 != nil
		if experiment.LegacyModelUnknown || experiment.LoadedPromptSHA256 == nil {
			result.Issues = append(result.Issues, "legacy model or runtime-loaded prompt identity is unknown")
		}
		if contract, contractErr := readAdapterContract(runPath, experiment); contractErr == nil {
			result.AdapterContractStatus = contract.Status
		} else {
			result.Issues = append(result.Issues, "adapter contract evidence is missing or invalid")
		}
		if experiment.PreparedManifestSHA256 != "" {
			if err := validatePreparedReplayEvidence(runPath, experiment); err != nil {
				result.Status = "INVALID"
				result.Issues = append(result.Issues, "prepared candidate artifacts do not match experiment provenance")
			}
		}
	}
	if result.AdapterContractStatus != "VERIFIED" {
		result.Issues = append(result.Issues, "adapter contract is not VERIFIED by real SDK smoke")
	}
	for _, variant := range []string{"candidate", "legacy"} {
		if result.VerifiedRealConversationCases[variant] == 0 {
			result.Issues = append(result.Issues, variant+" has no verified real-conversation cases")
		}
		missing := result.MissingVerifiedRouteSupport[variant]
		if len(missing) > 0 {
			result.Issues = append(result.Issues, variant+" lacks verified real-conversation support for all route labels")
		}
	}
	if status, err := readE2EStatus(e2ePath, experiment, experimentErr); err == nil {
		result.E2EStatus = status.Status
		result.E2ESideEffectCount = status.SideEffectCount
	} else {
		result.Issues = append(result.Issues, "isolated E2E evidence is unavailable")
	}
	result.Issues = append(result.Issues, missingPerformanceEvidenceIssues(result.PerformanceEvidence)...)
	margin, allowance := loadDecisionThresholds(runPath)
	if result.Status == "INCONCLUSIVE" && reportCanPass(result, margin, allowance) {
		result.Status = "COMPUTED"
		result.ModelDecision = "PASS"
	} else {
		result.ModelDecision = "INCONCLUSIVE"
	}
	if err := writeJSONExclusive(filepath.Join(directory, "metrics.json"), result); err != nil {
		return err
	}
	if err := writeTextExclusive(filepath.Join(directory, "report.md"), renderReport(result)); err != nil {
		return err
	}
	if result.Status == "INVALID" {
		return commandError{Code: "PREDICTIONS_INVALID", Err: errors.New("strict prediction validation failed")}
	}
	if result.Status == "NOT_RUN" {
		return commandError{Code: "NO_PREDICTIONS", Err: errors.New("report has no replay predictions")}
	}
	return nil
}

func copyRunProvenance(runPath, reportPath string) error {
	files := []string{"baseline_manifest.json", classifierlab.PreparedPolicyFileName, "policy_review.json", "experiment_manifest.json", "adapter_contract.json"}
	if experiment, err := readExperimentManifest(runPath); err == nil && experiment.PreparedManifestSHA256 != "" {
		files = append(files, classifierlab.PreparedManifestFileName, "resolved-config.yaml")
		var prepared classifierlab.PreparedManifest
		manifestBytes, readErr := readBoundedFile(filepath.Join(runPath, classifierlab.PreparedManifestFileName), classifierlab.MaxPreparedArtifactBytes)
		if readErr == nil && decodeStrictJSON(manifestBytes, &prepared) == nil {
			if _, pathErr := preparedEvidencePath(runPath, prepared.SDK.LockFile); pathErr == nil {
				files = append(files, filepath.FromSlash(prepared.SDK.LockFile))
			}
		}
	}
	var missing []string
	for _, name := range files {
		data, err := readBoundedFile(filepath.Join(runPath, name), classifierlab.MaxPreparedArtifactBytes)
		if err != nil {
			missing = append(missing, name)
			continue
		}
		destination := filepath.Join(reportPath, name)
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return fmt.Errorf("create report provenance directory: %w", err)
		}
		if err := writeBytesExclusive(destination, data); err != nil {
			return err
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf("missing run provenance artifacts: %s", strings.Join(missing, ", "))
	}
	return nil
}

func readAdapterContract(runPath string, experiment experimentManifest) (adapterContract, error) {
	data, err := readBoundedFile(filepath.Join(runPath, "adapter_contract.json"), 64<<10)
	if err != nil {
		return adapterContract{}, err
	}
	var contract adapterContract
	if err := decodeStrictJSON(data, &contract); err != nil {
		return adapterContract{}, err
	}
	if contract.Status != "VERIFIED" && contract.Status != "UNVERIFIED" {
		return adapterContract{}, errors.New("adapter contract status is invalid")
	}
	if contract.Status == "VERIFIED" {
		if err := validateVerifiedAdapterContract(runPath, contract, experiment); err != nil {
			return adapterContract{}, fmt.Errorf("verified adapter contract evidence is incomplete: %w", err)
		}
	}
	return contract, nil
}

func validateVerifiedAdapterContract(runPath string, contract adapterContract, experiment experimentManifest) error {
	if experiment.PolicyStatus != "verified" || !validSHA256(experiment.PolicySHA256) ||
		experiment.ModelFamily != "laya-multilingual" || experiment.ModelRevision == "" ||
		experiment.SDKVersion == nil || experiment.SDKLockSHA256 == nil {
		return errors.New("experiment does not contain verified policy, model, and SDK pins")
	}
	requiredChecks := []string{
		"pinned_sdk_version", "router_predict_signature", "multilingual_checkpoint_fixed",
		"choice_output_and_probabilities", "token_truncation_metadata",
		"offline_configuration_verified", "network_isolation_verified",
	}
	for _, check := range requiredChecks {
		if passed, exists := contract.Checks[check]; !exists || !passed {
			return fmt.Errorf("required adapter check %q is missing or false", check)
		}
	}
	if contract.Distribution != "laya" || contract.SourceURL != layaSourceURL ||
		contract.Model != experiment.ModelFamily ||
		contract.PredictAPI != `Router.predict(message, questions, model="multilingual", max_len=..., head_max_len=...)` ||
		contract.SDKVersion == nil ||
		*contract.SDKVersion != *experiment.SDKVersion || contract.LockSHA256 == nil ||
		*contract.LockSHA256 != *experiment.SDKLockSHA256 || contract.MaxLen < 1 || contract.HeadMaxLen < 1 {
		return errors.New("adapter SDK or model pins do not match the experiment")
	}
	if err := validatePreparedReplayEvidence(runPath, experiment); err != nil {
		return fmt.Errorf("prepared SDK, model, or policy evidence is invalid: %w", err)
	}
	resolved, err := classifierlab.LoadConfig(filepath.Join(runPath, "resolved-config.yaml"))
	if err != nil || resolved.Mode != classifierlab.ModeOff || !resolved.ReferenceWorker.OfflineRequired ||
		resolved.ReferenceWorker.MaxLen != contract.MaxLen || resolved.ReferenceWorker.HeadMaxLen != contract.HeadMaxLen ||
		resolved.Candidate.ExpectedFamily != experiment.ModelFamily ||
		resolved.Candidate.ExpectedModelRevision != experiment.ModelRevision ||
		resolved.Candidate.ExpectedPolicySHA256 != experiment.PolicySHA256 {
		return errors.New("resolved config does not match adapter and experiment pins")
	}
	var smoke smokeStatus
	smokeBytes, err := readBoundedFile(filepath.Join(runPath, "smoke_status.json"), 64<<10)
	if err != nil || decodeStrictJSON(smokeBytes, &smoke) != nil {
		return errors.New("matching smoke status evidence is unavailable or invalid")
	}
	if smoke.SchemaVersion != classifierlab.SchemaVersion || smoke.ManifestID != experiment.ManifestID || smoke.ExperimentID != experiment.ExperimentID ||
		(smoke.Status != "PASS" && smoke.Status != "INCOMPLETE") || smoke.AdapterContractStatus != "VERIFIED" || !smoke.ServerReady ||
		!smoke.AuthenticationGuardVerified || !smoke.OfflineConfigurationVerified ||
		smoke.NetworkIsolationStatus != "VERIFIED" || !smoke.ModelInvoked || smoke.PolicyStatus != "verified" ||
		smoke.PolicySHA256 != experiment.PolicySHA256 || smoke.Model == nil ||
		smoke.Model.Family != experiment.ModelFamily || smoke.Model.SourceID != layaModelSource ||
		smoke.Model.Revision != experiment.ModelRevision || smoke.Model.Device != experiment.Device ||
		smoke.Model.DType != experiment.DType || experiment.SDKVersion == nil ||
		smoke.Model.SDKVersion != *experiment.SDKVersion || smoke.ModelFilesSHA256 != experiment.ModelFilesSHA256 ||
		smoke.SDKLockSHA256 != *experiment.SDKLockSHA256 {
		return errors.New("smoke evidence does not prove the same prepared SDK, model, policy, and isolated runtime")
	}
	if _, err := validateSmokeEndpoint(smoke.Endpoint); err != nil {
		return errors.New("smoke evidence endpoint is not the fixed loopback HTTP origin")
	}
	for _, check := range requiredChecks {
		if passed, exists := smoke.Checks[check]; !exists || !passed {
			return fmt.Errorf("smoke evidence check %q is missing or false", check)
		}
	}
	return nil
}

func validatePreparedReplayEvidence(runPath string, experiment experimentManifest) error {
	if !validSHA256(experiment.PreparedManifestSHA256) || !validSHA256(experiment.ModelFilesSHA256) ||
		experiment.SDKVersion == nil || experiment.SDKLockSHA256 == nil || !validSHA256(*experiment.SDKLockSHA256) {
		return errors.New("prepared replay pins are incomplete")
	}
	manifestBytes, err := readBoundedFile(filepath.Join(runPath, classifierlab.PreparedManifestFileName), classifierlab.MaxPreparedArtifactBytes)
	if err != nil || sha256Hex(manifestBytes) != experiment.PreparedManifestSHA256 {
		return errors.New("prepared manifest hash mismatch")
	}
	var prepared classifierlab.PreparedManifest
	if err := decodeStrictJSON(manifestBytes, &prepared); err != nil {
		return err
	}
	if prepared.SchemaVersion != classifierlab.PreparedManifestSchemaVersion || prepared.PolicySHA256 != experiment.PolicySHA256 ||
		prepared.Model.Family != experiment.ModelFamily || prepared.Model.Revision != experiment.ModelRevision ||
		prepared.Model.SourceID != "convaiinnovations/laya-multilingual" || prepared.Model.Device != experiment.Device ||
		prepared.Model.DType != experiment.DType || prepared.Model.FilesSHA256 != experiment.ModelFilesSHA256 ||
		prepared.SDK.Distribution != "laya" || prepared.SDK.SourceURL != "https://github.com/NandhaKishorM/laya" ||
		prepared.SDK.Version != *experiment.SDKVersion || prepared.SDK.LockSHA256 != *experiment.SDKLockSHA256 {
		return errors.New("prepared artifact pins do not match experiment manifest")
	}
	policyBytes, err := readBoundedFile(filepath.Join(runPath, classifierlab.PreparedPolicyFileName), classifierlab.MaxPreparedArtifactBytes)
	if err != nil {
		return errors.New("prepared route policy evidence is missing")
	}
	var policy classifierlab.RoutePolicy
	if err := decodeStrictJSON(policyBytes, &policy); err != nil || policy.SemanticReviewStatus != "verified" {
		return errors.New("prepared route policy is invalid or unverified")
	}
	canonicalPolicy, err := json.Marshal(policy)
	if err != nil || sha256Hex(canonicalPolicy) != experiment.PolicySHA256 {
		return errors.New("prepared route policy hash does not match experiment manifest")
	}
	lockPath, err := preparedEvidencePath(runPath, prepared.SDK.LockFile)
	if err != nil {
		return errors.New("prepared SDK lock path is invalid")
	}
	lockBytes, err := readBoundedFile(lockPath, classifierlab.MaxPreparedArtifactBytes)
	if err != nil || sha256Hex(lockBytes) != *experiment.SDKLockSHA256 {
		return errors.New("prepared SDK lock evidence hash mismatch")
	}
	return nil
}

func preparedEvidencePath(root, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) || strings.Contains(relative, "\\") {
		return "", errors.New("prepared evidence path must be a relative slash path")
	}
	for _, component := range strings.Split(relative, "/") {
		if component == "" || component == "." || component == ".." {
			return "", errors.New("prepared evidence path contains an unsafe component")
		}
	}
	joined := filepath.Join(root, filepath.FromSlash(relative))
	rel, err := filepath.Rel(root, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("prepared evidence path escapes its root")
	}
	return joined, nil
}

func completedVariantNames(statuses map[string]variantReplayStatus) []string {
	variants := make([]string, 0, len(statuses))
	for variant, status := range statuses {
		if status.Status == "COMPLETED" || status.Status == "COMPLETED_WITH_ERRORS" {
			variants = append(variants, variant)
		}
	}
	sort.Strings(variants)
	return variants
}

func sameVariantStatuses(left, right map[string]variantReplayStatus) bool {
	if len(left) != len(right) {
		return false
	}
	for variant, status := range left {
		if other, exists := right[variant]; !exists || other != status {
			return false
		}
	}
	return true
}

func verifiedRealConversationSupport(records []classifierlab.ReplayPrediction) (map[string]int, map[string][]string) {
	casesByVariant := map[string]map[string]struct{}{"candidate": {}, "legacy": {}}
	support := map[string]map[string]int{"candidate": {}, "legacy": {}}
	for _, record := range records {
		if record.Repetition != 0 || (record.Variant != "candidate" && record.Variant != "legacy") ||
			record.Origin != "real_conversation" || record.LabelStatus != "verified" || record.NeedsContext ||
			record.ClassifierReached == nil || !*record.ClassifierReached || len(record.ExpectedRoutes) != 1 {
			continue
		}
		casesByVariant[record.Variant][record.CaseID] = struct{}{}
		support[record.Variant][record.ExpectedRoutes[0]]++
	}
	cases := make(map[string]int, len(casesByVariant))
	missing := make(map[string][]string, len(casesByVariant))
	for _, variant := range []string{"candidate", "legacy"} {
		cases[variant] = len(casesByVariant[variant])
		for _, route := range classifierlab.ReplayRouteNames() {
			if support[variant][route] == 0 {
				missing[variant] = append(missing[variant], route)
			}
		}
	}
	return cases, missing
}

func readE2EStatus(directory string, replay experimentManifest, replayErr error) (blockedStatus, error) {
	if replayErr != nil {
		return blockedStatus{}, errors.New("replay experiment provenance is unavailable")
	}
	e2eManifest, err := readExperimentManifest(directory)
	if err != nil {
		return blockedStatus{}, fmt.Errorf("read E2E experiment manifest: %w", err)
	}
	data, err := readBoundedFile(filepath.Join(directory, "e2e_status.json"), 64<<10)
	if err != nil {
		return blockedStatus{}, err
	}
	var status blockedStatus
	if err := decodeStrictJSON(data, &status); err != nil {
		return blockedStatus{}, err
	}
	if status.SchemaVersion != classifierlab.SchemaVersion || status.ManifestID != e2eManifest.ManifestID ||
		status.ExperimentID != e2eManifest.ExperimentID || status.DatasetSHA256 != e2eManifest.DatasetSHA256 {
		return blockedStatus{}, errors.New("E2E status does not match its experiment manifest")
	}
	if !sameExperimentProvenance(replay, e2eManifest) {
		return blockedStatus{}, errors.New("E2E provenance does not match the replay experiment or dataset")
	}
	if e2eManifest.RunStatus != "COMPLETED" || e2eManifest.PolicyStatus != "verified" {
		status.Status = "INCONCLUSIVE"
		return status, nil
	}
	if status.Status != "PASS" || !status.IsolatedRequested || !status.IsolationVerified || status.SideEffectCount == nil || *status.SideEffectCount != 0 {
		status.Status = "INCONCLUSIVE"
	}
	return status, nil
}

func sameExperimentProvenance(left, right experimentManifest) bool {
	return left.ExperimentID == right.ExperimentID && left.DatasetSHA256 == right.DatasetSHA256 &&
		left.CoreCommit == right.CoreCommit && left.WorkingTreeDirty == right.WorkingTreeDirty &&
		left.WorkingTreeDiffSHA256 == right.WorkingTreeDiffSHA256 &&
		left.WorkingTreeStatusSHA256 == right.WorkingTreeStatusSHA256 &&
		sameStringMap(left.SourceSHA256, right.SourceSHA256) &&
		sameStringMap(left.UntrackedSourceSHA256, right.UntrackedSourceSHA256) &&
		left.RepositoryPromptSHA256 == right.RepositoryPromptSHA256 &&
		optionalStringEqual(left.LoadedPromptSHA256, right.LoadedPromptSHA256) &&
		left.PolicySHA256 == right.PolicySHA256 && left.PolicyStatus == right.PolicyStatus &&
		left.ModelFamily == right.ModelFamily && left.ModelRevision == right.ModelRevision &&
		left.Device == right.Device && left.DType == right.DType &&
		optionalStringEqual(left.SDKVersion, right.SDKVersion) && optionalStringEqual(left.SDKLockSHA256, right.SDKLockSHA256) &&
		left.PreparedManifestSHA256 == right.PreparedManifestSHA256 && left.ModelFilesSHA256 == right.ModelFilesSHA256 &&
		left.SuppliedLegacyPromptPath == right.SuppliedLegacyPromptPath &&
		left.SuppliedLegacyPromptSHA256 == right.SuppliedLegacyPromptSHA256 &&
		left.LegacyPromptRuntimeObservation == right.LegacyPromptRuntimeObservation &&
		left.Seed == right.Seed && left.BootstrapResamples == right.BootstrapResamples &&
		left.NoninferiorityMargin == right.NoninferiorityMargin &&
		left.CriticalRegressionAllowed == right.CriticalRegressionAllowed
}

func sameStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func optionalStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func reportCanPass(result reportMetrics, margin float64, criticalAllowance int) bool {
	legacy, legacyOK := result.Variants["legacy"]
	candidate, candidateOK := result.Variants["candidate"]
	if !legacyOK || !candidateOK || result.AdapterContractStatus != "VERIFIED" || result.LegacyModelUnknown || !result.LoadedPromptObserved ||
		result.VerifiedRealConversationCases["legacy"] == 0 || result.VerifiedRealConversationCases["candidate"] == 0 ||
		len(result.MissingVerifiedRouteSupport["legacy"]) != 0 || len(result.MissingVerifiedRouteSupport["candidate"]) != 0 ||
		!performanceEvidenceVerified(result.PerformanceEvidence) ||
		result.E2EStatus != "PASS" || result.E2ESideEffectCount == nil || *result.E2ESideEffectCount != 0 ||
		result.Bootstrap == nil || result.Bootstrap.Status != "COMPUTED" || result.Bootstrap.Lower95 == nil ||
		candidate.MacroF1 == nil || legacy.MacroF1 == nil || candidate.CriticalNewErrors > criticalAllowance {
		return false
	}
	if *result.Bootstrap.Lower95 < -margin {
		return false
	}
	candidateLatency := candidate.Latencies["candidate_call_wall_ms"].P95MS
	legacyLatency := legacy.Latencies["legacy_classify_wall_ms"].P95MS
	if candidateLatency == nil || legacyLatency == nil || math.IsNaN(*candidateLatency) || math.IsNaN(*legacyLatency) || *candidateLatency >= *legacyLatency {
		return false
	}
	return len(result.Issues) == 0
}

func performanceEvidenceVerified(evidence reportPerformanceEvidence) bool {
	return evidence.TTFTStatus == "VERIFIED" && evidence.FinalResponseStatus == "VERIFIED" &&
		evidence.SharedResourceStatus == "VERIFIED" && evidence.RepeatedMeasurementStatus == "VERIFIED"
}

func missingPerformanceEvidenceIssues(evidence reportPerformanceEvidence) []string {
	var issues []string
	if evidence.TTFTStatus != "VERIFIED" {
		issues = append(issues, "paired TTFT evidence is unavailable or unverified")
	}
	if evidence.FinalResponseStatus != "VERIFIED" {
		issues = append(issues, "paired final-response latency evidence is unavailable or unverified")
	}
	if evidence.SharedResourceStatus != "VERIFIED" {
		issues = append(issues, "shared-resource contention measurements are unavailable or unverified")
	}
	if evidence.RepeatedMeasurementStatus != "VERIFIED" {
		issues = append(issues, "paired remeasurement evidence is unavailable or unverified")
	}
	return issues
}

func loadBootstrapSettings(runPath string) (int, int64) {
	manifest, err := readRunManifest(runPath)
	if err != nil || manifest.BootstrapResamples < 1 {
		return 0, 0
	}
	return manifest.BootstrapResamples, manifest.Seed
}

func loadDecisionThresholds(runPath string) (float64, int) {
	manifest, err := readRunManifest(runPath)
	if err != nil || manifest.NoninferiorityMargin < 0 || manifest.NoninferiorityMargin > 1 || manifest.CriticalRegressionAllowed < 0 {
		return 0, 0
	}
	return manifest.NoninferiorityMargin, manifest.CriticalRegressionAllowed
}

func readRunManifest(runPath string) (runManifest, error) {
	data, err := readBoundedFile(filepath.Join(runPath, "run_manifest.json"), 64<<10)
	if err != nil {
		return runManifest{}, err
	}
	var manifest runManifest
	if err := decodeStrictJSON(data, &manifest); err != nil {
		return runManifest{}, err
	}
	if manifest.SchemaVersion != classifierlab.SchemaVersion || !validSHA256(manifest.ManifestID) ||
		(manifest.Status != "BLOCKED" && manifest.Status != "PARTIAL" && manifest.Status != "COMPLETED" && manifest.Status != "NOT_RUN") ||
		manifest.ExperimentID == "" || !validSHA256(manifest.DatasetSHA256) ||
		manifest.Split == "" || manifest.CaseCount < 1 || len(manifest.Variants) == 0 ||
		(manifest.PolicyStatus != "POLICY_UNDEFINED" && manifest.PolicyStatus != "SOURCE_ALIGNED_REVIEW_REQUIRED" && manifest.PolicyStatus != "verified") ||
		manifest.BootstrapResamples < 1 || math.IsNaN(manifest.NoninferiorityMargin) ||
		math.IsInf(manifest.NoninferiorityMargin, 0) || manifest.NoninferiorityMargin < 0 ||
		manifest.NoninferiorityMargin > 1 || manifest.CriticalRegressionAllowed < 0 {
		return runManifest{}, errors.New("run manifest fields are invalid")
	}
	seenVariants := make(map[string]struct{}, len(manifest.Variants))
	for _, variant := range manifest.Variants {
		switch variant {
		case "legacy", "candidate", "hybrid-replay", "rules-only":
		default:
			return runManifest{}, errors.New("run manifest contains an unsupported variant")
		}
		if _, exists := seenVariants[variant]; exists {
			return runManifest{}, errors.New("run manifest contains duplicate variants")
		}
		seenVariants[variant] = struct{}{}
	}
	if len(manifest.VariantStatuses) > 0 {
		if len(manifest.VariantStatuses) != len(manifest.Variants) {
			return runManifest{}, errors.New("run manifest variant statuses do not match variants")
		}
		for variant, status := range manifest.VariantStatuses {
			if _, exists := seenVariants[variant]; !exists || !validReplayVariantStatus(status.Status) {
				return runManifest{}, errors.New("run manifest contains an invalid variant status")
			}
		}
		if manifest.Status == "COMPLETED" && !replayVariantsCompleted(manifest.VariantStatuses) {
			return runManifest{}, errors.New("completed run contains an incomplete variant")
		}
		if manifest.Status == "COMPLETED" && manifest.PolicyStatus != "verified" && !onlyRulesOnlyCompleted(manifest.VariantStatuses) {
			return runManifest{}, errors.New("unverified policy cannot complete model replay")
		}
	} else if manifest.Status == "COMPLETED" && manifest.PolicyStatus != "verified" {
		return runManifest{}, errors.New("completed run lacks verified policy provenance")
	}
	return manifest, nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]int, len(left))
	for _, value := range left {
		seen[value]++
	}
	for _, value := range right {
		seen[value]--
		if seen[value] < 0 {
			return false
		}
	}
	for _, remaining := range seen {
		if remaining != 0 {
			return false
		}
	}
	return true
}

func validReplayVariantStatus(status string) bool {
	switch status {
	case "COMPLETED", "COMPLETED_WITH_ERRORS", "BLOCKED", "UNAVAILABLE":
		return true
	default:
		return false
	}
}

func replayVariantsCompleted(statuses map[string]variantReplayStatus) bool {
	if len(statuses) == 0 {
		return false
	}
	for _, status := range statuses {
		if status.Status != "COMPLETED" && status.Status != "COMPLETED_WITH_ERRORS" {
			return false
		}
	}
	return true
}

func onlyRulesOnlyCompleted(statuses map[string]variantReplayStatus) bool {
	status, exists := statuses["rules-only"]
	return exists && len(statuses) == 1 && status.Status == "COMPLETED"
}

func readExperimentManifest(runPath string) (experimentManifest, error) {
	data, err := readBoundedFile(filepath.Join(runPath, "experiment_manifest.json"), classifierlab.MaxPreparedArtifactBytes)
	if err != nil {
		return experimentManifest{}, err
	}
	var manifest experimentManifest
	if err := decodeStrictJSON(data, &manifest); err != nil {
		return experimentManifest{}, err
	}
	if manifest.SchemaVersion != classifierlab.SchemaVersion || !validSHA256(manifest.ManifestID) ||
		manifest.ExperimentID == "" || manifest.CoreCommit == "" || !validSHA256(manifest.PolicySHA256) ||
		manifest.PolicyStatus != "POLICY_UNDEFINED" && manifest.PolicyStatus != "SOURCE_ALIGNED_REVIEW_REQUIRED" && manifest.PolicyStatus != "verified" ||
		manifest.RunStatus != "BLOCKED" && manifest.RunStatus != "PARTIAL" && manifest.RunStatus != "COMPLETED" && manifest.RunStatus != "NOT_RUN" ||
		manifest.DatasetSHA256 != "" && !validSHA256(manifest.DatasetSHA256) {
		return experimentManifest{}, errors.New("experiment manifest fields are invalid")
	}
	if len(manifest.VariantStatuses) > 0 {
		if len(manifest.VariantStatuses) != len(manifest.Variants) {
			return experimentManifest{}, errors.New("experiment manifest variant statuses do not match variants")
		}
		for _, status := range manifest.VariantStatuses {
			if !validReplayVariantStatus(status.Status) {
				return experimentManifest{}, errors.New("experiment manifest contains an invalid variant status")
			}
		}
		if candidate, exists := manifest.VariantStatuses["candidate"]; exists &&
			(candidate.Status == "COMPLETED" || candidate.Status == "COMPLETED_WITH_ERRORS") && manifest.PreparedManifestSHA256 == "" {
			return experimentManifest{}, errors.New("candidate replay completed without prepared artifact provenance")
		}
		if manifest.RunStatus == "COMPLETED" && !replayVariantsCompleted(manifest.VariantStatuses) {
			return experimentManifest{}, errors.New("completed experiment contains an incomplete variant")
		}
		if manifest.RunStatus == "COMPLETED" && manifest.PolicyStatus != "verified" && !onlyRulesOnlyCompleted(manifest.VariantStatuses) {
			return experimentManifest{}, errors.New("unverified policy cannot complete model replay")
		}
	} else if manifest.RunStatus == "COMPLETED" && manifest.PolicyStatus != "verified" {
		return experimentManifest{}, errors.New("completed experiment lacks verified policy provenance")
	}
	if manifest.PreparedManifestSHA256 == "" && manifest.ModelFilesSHA256 != "" {
		return experimentManifest{}, errors.New("model files hash lacks prepared manifest provenance")
	}
	if hasPromptProvenance := manifest.SuppliedLegacyPromptPath != "" || manifest.SuppliedLegacyPromptSHA256 != "" || manifest.LegacyPromptRuntimeObservation != ""; hasPromptProvenance {
		if manifest.SuppliedLegacyPromptPath == "" || !validSHA256(manifest.SuppliedLegacyPromptSHA256) ||
			manifest.LegacyPromptRuntimeObservation != "NOT_OBSERVED" || manifest.LoadedPromptSHA256 != nil {
			return experimentManifest{}, errors.New("experiment manifest supplied prompt provenance is invalid or claims runtime observation")
		}
	}
	expectedID, err := experimentManifestID(manifest)
	if err != nil {
		return experimentManifest{}, err
	}
	if expectedID != manifest.ManifestID {
		return experimentManifest{}, errors.New("experiment manifest ID does not match its contents")
	}
	if manifest.PreparedManifestSHA256 != "" {
		if err := validatePreparedReplayEvidence(runPath, manifest); err != nil {
			return experimentManifest{}, fmt.Errorf("prepared candidate provenance is invalid: %w", err)
		}
	}
	return manifest, nil
}

func experimentManifestID(manifest experimentManifest) (string, error) {
	manifest.ManifestID = ""
	identity, err := canonicalJSON(manifest)
	if err != nil {
		return "", err
	}
	return sha256Hex(identity), nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func renderReport(result reportMetrics) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "# Routing classifier evaluation\n\nStatus: **%s**  \nModel decision: **%s**  \nIsolated E2E: **%s**\n", result.Status, result.ModelDecision, result.E2EStatus)
	fmt.Fprintf(&builder, "\n## Adoption measurement evidence\n\n- Paired TTFT: %s\n- Paired final-response latency: %s\n- Shared-resource contention: %s\n- Paired remeasurement: %s\n",
		result.PerformanceEvidence.TTFTStatus, result.PerformanceEvidence.FinalResponseStatus,
		result.PerformanceEvidence.SharedResourceStatus, result.PerformanceEvidence.RepeatedMeasurementStatus)
	if len(result.Issues) > 0 {
		builder.WriteString("\n## Issues\n\n")
		for _, issue := range result.Issues {
			fmt.Fprintf(&builder, "- %s\n", issue)
		}
	}
	if result.Bootstrap != nil {
		fmt.Fprintf(&builder, "\nPaired macro-F1 bootstrap status: %s (%d groups, %d resamples).\n", result.Bootstrap.Status, result.Bootstrap.Groups, result.Bootstrap.Resamples)
	}
	return builder.String()
}

func createPrivateOutputDirectory(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", errors.New("--out is required")
	}
	absPath, err := filepath.Abs(name)
	if err != nil {
		return "", fmt.Errorf("resolve output directory: %w", err)
	}
	if err := os.Mkdir(absPath, 0o700); err != nil {
		return "", fmt.Errorf("create new private output directory: %w", err)
	}
	if err := os.Chmod(absPath, 0o700); err != nil {
		_ = os.Remove(absPath)
		return "", fmt.Errorf("restrict output directory permissions: %w", err)
	}
	return absPath, nil
}

func writeJSONExclusive(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode output %s: %w", filepath.Base(path), err)
	}
	return writeBytesExclusive(path, append(data, '\n'))
}

func writeTextExclusive(path, value string) error {
	return writeBytesExclusive(path, []byte(value))
}

func writeBytesExclusive(path string, value []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create output %s: %w", filepath.Base(path), err)
	}
	if _, err := file.Write(value); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write output %s: %w", filepath.Base(path), err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close output %s: %w", filepath.Base(path), err)
	}
	return nil
}

func readBoundedFile(path string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("input file exceeds size limit")
	}
	return data, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open input file for hash: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	limit := int64(classifierlab.MaxReplayCases * classifierlab.MaxReplayLineBytes)
	count, err := io.Copy(hash, io.LimitReader(file, limit+1))
	if err != nil {
		return "", fmt.Errorf("hash input file: %w", err)
	}
	if count > limit {
		return "", errors.New("input file exceeds size limit")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func decodeStrictJSON(data []byte, target any) error {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var visit func() error
	visit = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("JSON object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate JSON field %q", key)
				}
				seen[key] = struct{}{}
				if err := visit(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := visit(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("unexpected JSON delimiter")
		}
	}
	if err := visit(); err != nil {
		return err
	}
	return nil
}

func safeReason(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}
