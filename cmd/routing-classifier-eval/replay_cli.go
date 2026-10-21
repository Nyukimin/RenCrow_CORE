package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainrouting "github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	infrarouting "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing/classifierlab"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type variantReplayStatus struct {
	Status                       string `json:"status"`
	ReasonCode                   string `json:"reason_code,omitempty"`
	Reason                       string `json:"reason,omitempty"`
	CaseCount                    int    `json:"case_count"`
	ClassifierReachableCases     int    `json:"classifier_reachable_cases"`
	PredictionRows               int    `json:"prediction_rows"`
	Calls                        int    `json:"calls"`
	WarmupCalls                  int    `json:"warmup_calls"`
	Errors                       int    `json:"errors"`
	Abstentions                  int    `json:"abstentions"`
	ModelInvoked                 bool   `json:"model_invoked"`
	OfflineConfigurationVerified bool   `json:"offline_configuration_verified"`
	NetworkIsolationStatus       string `json:"network_isolation_status"`
}

type replayStatus struct {
	SchemaVersion  string                         `json:"schema_version"`
	Status         string                         `json:"status"`
	ReasonCode     string                         `json:"reason_code"`
	ManifestID     string                         `json:"manifest_id"`
	ExperimentID   string                         `json:"experiment_id"`
	DatasetSHA256  string                         `json:"dataset_sha256"`
	Split          string                         `json:"split"`
	CaseCount      int                            `json:"case_count"`
	Variants       []string                       `json:"variants"`
	VariantResults map[string]variantReplayStatus `json:"variant_results"`
	Seed           int64                          `json:"seed"`
	ModelInvoked   bool                           `json:"model_invoked"`
	Reasons        []string                       `json:"reasons"`
}

type replayReachabilityClassifier struct{ reached bool }

func (classifier *replayReachabilityClassifier) Classify(context.Context, conversation.TurnInput) (domainrouting.Decision, error) {
	classifier.reached = true
	// An empty, low-confidence observation leaves Mio's existing safe fallback
	// unchanged while recording whether the normal route path reached this stage.
	return domainrouting.Decision{}, nil
}

type mioReplayObservation struct {
	RuleMatched       *bool
	ClassifierReached bool
	FinalRoute        string
	DecisionWallMS    float64
}

func replayTurnInput(item classifierlab.ReplayCase) (conversation.TurnInput, error) {
	address, err := conversation.NewChannelAddress("replay", "case")
	if err != nil {
		return conversation.TurnInput{}, errors.New("construct replay channel address")
	}
	turn, err := conversation.NewTurnInput(modulecore.NewTaskID(), item.Message, address)
	if err != nil {
		return conversation.TurnInput{}, errors.New("construct replay turn input")
	}
	fixture, err := classifierlab.ParseReplayTurnFixture(item.TurnFixture)
	if err != nil {
		return conversation.TurnInput{}, errors.New("decode allowlisted replay turn metadata")
	}
	if fixture.SessionID != "" {
		turn = turn.WithSessionID(fixture.SessionID)
	}
	if fixture.ViewerRecipient != "" {
		turn = turn.WithViewerRecipient(fixture.ViewerRecipient)
	}
	return turn, nil
}

func observeMioReplay(item classifierlab.ReplayCase) (mioReplayObservation, error) {
	turn, err := replayTurnInput(item)
	if err != nil {
		return mioReplayObservation{}, err
	}
	observer := &replayReachabilityClassifier{}
	mio := domainagent.NewMioAgent(nil, observer, infrarouting.NewRuleDictionary(), nil, nil, nil)
	started := time.Now()
	decision, err := mio.DecideAction(context.Background(), turn)
	if err != nil || decision.Route == "" {
		return mioReplayObservation{}, errors.New("Mio returned no replay route")
	}
	ruleEvaluated, ruleMatched := false, false
	for _, evidence := range decision.Evidence {
		if evidence.Source == domainrouting.EvidenceSourceRuleDictionary {
			ruleEvaluated = true
			ruleMatched = ruleMatched || evidence.Matched
		}
	}
	var ruleValue *bool
	if ruleEvaluated {
		ruleValue = boolValue(ruleMatched)
	}
	return mioReplayObservation{
		RuleMatched: ruleValue, ClassifierReached: observer.reached,
		FinalRoute: decision.Route.String(), DecisionWallMS: float64(time.Since(started)) / float64(time.Millisecond),
	}, nil
}

func runReplayCommand(configPath, datasetPath, split string, variants []string, outPath, repoRoot string) error {
	config, err := loadExperimentConfig(configPath)
	if err != nil {
		return err
	}
	cases, err := classifierlab.LoadReplayDataset(datasetPath)
	if err != nil {
		return err
	}
	ordered, err := classifierlab.ReplayOrder(cases, split, config.Evaluation.RandomSeed)
	if err != nil {
		return err
	}
	if len(ordered) > classifierlab.MaxReplayPredictions {
		return commandError{Code: "REPLAY_LIMIT_EXCEEDED", Err: errors.New("selected split exceeds the prediction output bound")}
	}
	for _, variant := range variants {
		if variant == "candidate" && len(ordered)*maxInt(1, config.Evaluation.LatencyRepetitions) > classifierlab.MaxReplayPredictions {
			return commandError{Code: "REPLAY_LIMIT_EXCEEDED", Err: errors.New("selected split and candidate repetitions exceed the prediction output bound")}
		}
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
	if err := writeRunEvidence(directory, config, inspection, datasetSHA, split, variants); err != nil {
		return err
	}

	statuses := make(map[string]variantReplayStatus, len(variants))
	var predictions []classifierlab.ReplayPrediction
	var preparedArtifacts *classifierlab.PreparedArtifacts
	for _, variant := range variants {
		switch variant {
		case "rules-only":
			rows, status := replayRulesOnly(config.ExperimentID, ordered)
			predictions = append(predictions, rows...)
			statuses[variant] = status
		case "candidate":
			rows, status, artifacts := replayCandidate(configPath, config, inspection, ordered)
			predictions = append(predictions, rows...)
			statuses[variant] = status
			preparedArtifacts = artifacts
		case "legacy":
			statuses[variant] = variantReplayStatus{
				Status: "UNAVAILABLE", ReasonCode: "LEGACY_BASELINE_UNAVAILABLE",
				Reason:    "live provider, loaded prompt, model identity, middleware, and isolated legacy baseline are not observed",
				CaseCount: len(ordered),
			}
		case "hybrid-replay":
			statuses[variant] = variantReplayStatus{
				Status: "UNAVAILABLE", ReasonCode: "HYBRID_ADAPTER_UNAVAILABLE",
				Reason:    "a verified legacy adapter and calibrated candidate acceptance predicate are required before hybrid calls",
				CaseCount: len(ordered),
			}
		}
	}

	runStatus := "COMPLETED"
	failedVariants := 0
	completedVariants := 0
	for _, status := range statuses {
		if status.Status == "COMPLETED" || status.Status == "COMPLETED_WITH_ERRORS" {
			completedVariants++
		} else {
			failedVariants++
		}
	}
	if failedVariants > 0 {
		runStatus = "PARTIAL"
	}
	if completedVariants == 0 {
		runStatus = "BLOCKED"
	}

	experiment, err := readExperimentManifest(directory)
	if err != nil {
		return err
	}
	experiment.RunStatus = runStatus
	experiment.VariantStatuses = statuses
	if preparedArtifacts != nil {
		if err := writePreparedReplayEvidence(directory, configPath, config, *preparedArtifacts, &experiment); err != nil {
			return err
		}
	}
	experiment.ManifestID = ""
	experiment.ManifestID, err = experimentManifestID(experiment)
	if err != nil {
		return err
	}
	if err := replaceJSON(filepath.Join(directory, "experiment_manifest.json"), experiment); err != nil {
		return err
	}
	for index := range predictions {
		predictions[index].ManifestID = experiment.ManifestID
	}
	if len(predictions) > 0 {
		if err := writeJSONLinesExclusive(filepath.Join(directory, "predictions.jsonl"), predictions); err != nil {
			return err
		}
	}
	manifest := runManifest{
		SchemaVersion: classifierlab.SchemaVersion, ManifestID: experiment.ManifestID,
		Status: runStatus, ReasonCode: "REPLAY_" + runStatus,
		ExperimentID: config.ExperimentID, DatasetSHA256: datasetSHA, Split: split,
		CaseCount: len(ordered), Variants: variants, VariantStatuses: statuses,
		Seed: config.Evaluation.RandomSeed, PolicyStatus: experiment.PolicyStatus,
		BootstrapResamples:        config.Evaluation.BootstrapResamples,
		NoninferiorityMargin:      config.Evaluation.NoninferiorityMarginMacroF1,
		CriticalRegressionAllowed: config.Evaluation.CriticalRegressionAllowedCount,
	}
	for _, status := range statuses {
		manifest.ModelInvoked = manifest.ModelInvoked || status.ModelInvoked
	}
	if err := writeJSONExclusive(filepath.Join(directory, "run_manifest.json"), manifest); err != nil {
		return err
	}
	reasons := make([]string, 0, len(statuses))
	reasonCode := "REPLAY_COMPLETED"
	for _, variant := range variants {
		status := statuses[variant]
		if status.Status != "COMPLETED" && status.Status != "COMPLETED_WITH_ERRORS" {
			if reasonCode == "REPLAY_COMPLETED" || variant == "candidate" && status.Status == "BLOCKED" {
				reasonCode = status.ReasonCode
			}
			reasons = append(reasons, variant+": "+status.Reason)
		}
	}
	if reasons == nil {
		reasons = []string{}
	}
	replay := replayStatus{
		SchemaVersion: classifierlab.SchemaVersion, Status: runStatus, ReasonCode: reasonCode,
		ManifestID: experiment.ManifestID, ExperimentID: config.ExperimentID,
		DatasetSHA256: datasetSHA, Split: split, CaseCount: len(ordered), Variants: variants,
		VariantResults: statuses, Seed: config.Evaluation.RandomSeed, ModelInvoked: manifest.ModelInvoked,
		Reasons: reasons,
	}
	if err := writeJSONExclusive(filepath.Join(directory, "replay_status.json"), replay); err != nil {
		return err
	}
	if runStatus != "COMPLETED" {
		if reasonCode == "REPLAY_COMPLETED" {
			reasonCode = "REPLAY_PARTIAL"
		}
		return commandError{Code: reasonCode, Err: errors.New(strings.Join(reasons, "; "))}
	}
	return nil
}

func replayRulesOnly(experimentID string, cases []classifierlab.ReplayCase) ([]classifierlab.ReplayPrediction, variantReplayStatus) {
	rows := make([]classifierlab.ReplayPrediction, 0, len(cases))
	status := variantReplayStatus{Status: "COMPLETED", CaseCount: len(cases)}
	for _, item := range cases {
		observation, err := observeMioReplay(item)
		if err != nil {
			status.Status, status.ReasonCode, status.Reason = "BLOCKED", "MIO_DECISION_FAILED", "Mio could not reconstruct or decide the replay turn"
			return nil, status
		}
		if observation.ClassifierReached {
			status.ClassifierReachableCases++
		}
		rows = append(rows, classifierlab.ReplayPrediction{
			CaseID: item.CaseID, GroupID: item.GroupID, Origin: item.Origin, Split: item.Split,
			Variant: "rules-only", Repetition: 0, RiskTags: append([]string(nil), item.RiskTags...),
			RuleMatched: observation.RuleMatched, ClassifierReached: boolValue(observation.ClassifierReached),
			ExpectedRoutes: append([]string(nil), item.ExpectedRoutes...), LabelStatus: item.LabelStatus,
			NeedsContext: item.NeedsContext, FinalRoute: &observation.FinalRoute, DecisionWallMS: floatValue(observation.DecisionWallMS),
		})
	}
	status.PredictionRows = len(rows)
	return rows, status
}

func replayCandidate(configPath string, config classifierlab.Config, inspection repositoryInspection, cases []classifierlab.ReplayCase) ([]classifierlab.ReplayPrediction, variantReplayStatus, *classifierlab.PreparedArtifacts) {
	status := variantReplayStatus{Status: "BLOCKED", CaseCount: len(cases), NetworkIsolationStatus: "UNVERIFIED"}
	observations := make([]mioReplayObservation, len(cases))
	for index, item := range cases {
		observation, err := observeMioReplay(item)
		if err != nil {
			status.ReasonCode, status.Reason = "MIO_DECISION_FAILED", "Mio could not reconstruct or decide a replay turn"
			return nil, status, nil
		}
		observations[index] = observation
		if observation.ClassifierReached {
			status.ClassifierReachableCases++
		}
	}
	artifacts, token, code, reason := validateSmokePreflight(configPath, config, inspection)
	if code != "" {
		status.ReasonCode, status.Reason = code, reason
		return nil, status, nil
	}
	prepared := &artifacts
	if !allowCandidateReplayWithoutNetworkIsolation(&status) {
		return nil, status, prepared
	}
	endpoint, err := validateSmokeEndpoint(config.Candidate.Endpoint)
	if err != nil {
		status.ReasonCode, status.Reason = "LOCAL_ENDPOINT_REQUIRED", "candidate endpoint must be the configured 127.0.0.1 origin"
		return nil, status, prepared
	}
	client, err := newSmokeHTTPClient(endpoint, time.Duration(config.Candidate.TimeoutMS)*time.Millisecond)
	if err != nil {
		status.ReasonCode, status.Reason = "LOCAL_ENDPOINT_REQUIRED", "candidate replay could not create a loopback-only HTTP client"
		return nil, status, prepared
	}
	defer client.CloseIdleConnections()
	ctx := context.Background()
	ready, _, err := smokeGETReady(ctx, client, endpoint+"/readyz")
	if err != nil || ready.Status != "ready" {
		status.ReasonCode, status.Reason = "SERVER_NOT_READY", "candidate ready endpoint did not attest ready status"
		return nil, status, prepared
	}
	if validateSmokeReadyResponse(ready) != nil {
		status.ReasonCode, status.Reason = "SERVER_NOT_READY", "candidate ready endpoint did not attest the required offline configuration schema"
		return nil, status, prepared
	}
	status.OfflineConfigurationVerified = ready.OfflineConfigurationVerified
	status.NetworkIsolationStatus = ready.NetworkIsolationStatus
	policySHA := artifacts.PolicySHA256
	expectedModel := classifierlab.CandidateModel{
		Family: artifacts.Manifest.Model.Family, SourceID: artifacts.Manifest.Model.SourceID,
		Revision: artifacts.Manifest.Model.Revision, SDKVersion: artifacts.Manifest.SDK.Version,
		Device: artifacts.Manifest.Model.Device, DType: artifacts.Manifest.Model.DType,
	}
	routes := append([]string(nil), artifacts.Policy.Routes...)
	repetitions := config.Evaluation.LatencyRepetitions
	if repetitions < 1 || len(cases)*repetitions > classifierlab.MaxReplayPredictions {
		status.ReasonCode, status.Reason = "REPLAY_LIMIT_EXCEEDED", "candidate repetition count exceeds the prediction output bound"
		return nil, status, prepared
	}
	reachable := make([]int, 0, status.ClassifierReachableCases)
	for index, observation := range observations {
		if observation.ClassifierReached {
			reachable = append(reachable, index)
		}
	}
	for warmup := 0; warmup < config.Evaluation.WarmupRequests && len(reachable) > 0; warmup++ {
		item := cases[reachable[warmup%len(reachable)]]
		input := classifierlab.CandidateInput{
			SchemaVersion: classifierlab.SchemaVersion,
			RequestID:     classifierlab.ReplayRequestID(config.ExperimentID, item.CaseID, "candidate-warmup", warmup),
			PolicySHA256:  policySHA, Message: item.Message,
		}
		result, _, requestErr := smokePOSTClassify(ctx, client, endpoint+"/v1/classify", input, token)
		status.WarmupCalls++
		if requestErr == nil {
			requestErr = validateSmokeResult(input, result, expectedModel, policySHA, routes)
			if result.Status == classifierlab.CandidateClassified {
				status.ModelInvoked = true
			}
		}
		if requestErr != nil {
			status.ReasonCode, status.Reason = "CANDIDATE_WARMUP_FAILED", "candidate warmup did not satisfy the prepared model and policy contract"
			return nil, status, prepared
		}
	}

	rows := make([]classifierlab.ReplayPrediction, 0, len(cases)*repetitions)
	for caseIndex, item := range cases {
		for repetition := 0; repetition < repetitions; repetition++ {
			observation := observations[caseIndex]
			row := classifierlab.ReplayPrediction{
				CaseID: item.CaseID, GroupID: item.GroupID, Origin: item.Origin, Split: item.Split,
				Variant: "candidate", Repetition: repetition, RiskTags: append([]string(nil), item.RiskTags...),
				ExpectedRoutes: append([]string(nil), item.ExpectedRoutes...), LabelStatus: item.LabelStatus,
				NeedsContext: item.NeedsContext, RuleMatched: observation.RuleMatched,
				ClassifierReached: boolValue(observation.ClassifierReached),
				DecisionWallMS:    floatValue(observation.DecisionWallMS),
			}
			if !observation.ClassifierReached {
				rows = append(rows, row)
				continue
			}
			input := classifierlab.CandidateInput{
				SchemaVersion: classifierlab.SchemaVersion,
				RequestID:     classifierlab.ReplayRequestID(config.ExperimentID, item.CaseID, "candidate", repetition),
				PolicySHA256:  policySHA, Message: item.Message,
			}
			started := time.Now()
			result, _, requestErr := smokePOSTClassify(ctx, client, endpoint+"/v1/classify", input, token)
			wallMS := float64(time.Since(started)) / float64(time.Millisecond)
			status.Calls++
			row.CandidateCallWallMS = floatValue(wallMS)
			if requestErr == nil {
				requestErr = validateSmokeResult(input, result, expectedModel, policySHA, routes)
			}
			if requestErr != nil {
				row.CandidateErrorCode = stringValue(smokeErrorCode(requestErr))
				status.Errors++
			} else {
				candidateStatus := string(result.Status)
				row.CandidateStatus = &candidateStatus
				if result.ReasonCode != "" {
					row.CandidateReasonCode = stringValue(result.ReasonCode)
				}
				if result.Status == classifierlab.CandidateAbstain {
					status.Abstentions++
				} else if result.Route != nil {
					selected := string(*result.Route)
					row.CandidateSelectedRoute = &selected
					row.CandidateSelectedProb = floatValue(result.SelectedProbability)
					row.CandidateProbabilities = make(map[string]float64, len(result.Probabilities))
					for route, probability := range result.Probabilities {
						row.CandidateProbabilities[string(route)] = probability
					}
					row.CandidateServerMS = floatValue(result.Timing.ServerTotal)
					status.ModelInvoked = true
				}
			}
			rows = append(rows, row)
		}
	}
	status.PredictionRows = len(rows)
	status.Status = "COMPLETED"
	if status.Errors > 0 {
		status.Status = "COMPLETED_WITH_ERRORS"
		status.ReasonCode = "CANDIDATE_ERRORS"
		status.Reason = "some candidate responses failed bounded schema or policy validation"
	}
	return rows, status, prepared
}

func allowCandidateReplayWithoutNetworkIsolation(status *variantReplayStatus) bool {
	if status == nil {
		return false
	}
	// No independent egress-isolation evidence is part of the current replay
	// contract. The child's offline configuration attestation is insufficient.
	status.Status = "BLOCKED"
	status.ReasonCode = "OFFLINE_ISOLATION_UNVERIFIED"
	status.Reason = "candidate inference is blocked until external network isolation has independent evidence"
	status.OfflineConfigurationVerified = false
	status.NetworkIsolationStatus = "UNVERIFIED"
	return false
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func writePreparedReplayEvidence(directory, configPath string, config classifierlab.Config,
	artifacts classifierlab.PreparedArtifacts, experiment *experimentManifest) error {
	if experiment == nil || artifacts.Policy.SemanticReviewStatus != "verified" ||
		artifacts.PolicySHA256 == "" || artifacts.Manifest.Model.FilesSHA256 == "" || artifacts.SDKLockSHA256 == "" {
		return errors.New("prepared candidate provenance is incomplete")
	}
	preparedManifest, err := readBoundedFile(filepath.Join(artifacts.ArtifactDirectory, classifierlab.PreparedManifestFileName), classifierlab.MaxPreparedArtifactBytes)
	if err != nil {
		return fmt.Errorf("read prepared candidate manifest: %w", err)
	}
	lockRelative := filepath.FromSlash(artifacts.Manifest.SDK.LockFile)
	lockPath := filepath.Join(artifacts.ArtifactDirectory, lockRelative)
	lockBytes, err := readBoundedFile(lockPath, classifierlab.MaxPreparedArtifactBytes)
	if err != nil || sha256Hex(lockBytes) != artifacts.SDKLockSHA256 {
		return errors.New("prepared candidate SDK lock does not match validated pin")
	}
	resolvedConfig, err := readBoundedFile(configPath, 1<<20)
	if err != nil {
		return fmt.Errorf("read resolved candidate config: %w", err)
	}
	if err := writeBytesExclusive(filepath.Join(directory, classifierlab.PreparedManifestFileName), preparedManifest); err != nil {
		return err
	}
	if err := writeBytesExclusive(filepath.Join(directory, "resolved-config.yaml"), resolvedConfig); err != nil {
		return err
	}
	lockOutput := filepath.Join(directory, lockRelative)
	if err := os.MkdirAll(filepath.Dir(lockOutput), 0o700); err != nil {
		return fmt.Errorf("create SDK lock evidence directory: %w", err)
	}
	if err := writeBytesExclusive(lockOutput, lockBytes); err != nil {
		return err
	}
	if err := replaceJSON(filepath.Join(directory, classifierlab.PreparedPolicyFileName), artifacts.Policy); err != nil {
		return err
	}
	review := policyReview{
		Status: "verified", Conflict: "", ConflictSourcePaths: []string{},
		PolicySHA256: artifacts.PolicySHA256,
		Usage:        "Prepared candidate replay validated this canonical policy; this does not observe a runtime-loaded legacy prompt.",
	}
	if err := replaceJSON(filepath.Join(directory, "policy_review.json"), review); err != nil {
		return err
	}
	sdkVersion, lockSHA := artifacts.Manifest.SDK.Version, artifacts.SDKLockSHA256
	experiment.PolicySHA256 = artifacts.PolicySHA256
	experiment.PolicyStatus = "verified"
	experiment.ModelFamily = artifacts.Manifest.Model.Family
	experiment.ModelRevision = artifacts.Manifest.Model.Revision
	experiment.Device = artifacts.Manifest.Model.Device
	experiment.DType = artifacts.Manifest.Model.DType
	experiment.SDKVersion = &sdkVersion
	experiment.SDKLockSHA256 = &lockSHA
	experiment.PreparedManifestSHA256 = sha256Hex(preparedManifest)
	experiment.ModelFilesSHA256 = artifacts.ModelFilesSHA256
	contract := adapterContract{
		Status: "UNVERIFIED", Distribution: artifacts.Manifest.SDK.Distribution,
		SourceURL: artifacts.Manifest.SDK.SourceURL, SDKVersion: &sdkVersion, LockSHA256: &lockSHA,
		Model:      artifacts.Manifest.Model.Family,
		PredictAPI: `Router.predict(message, questions, model="multilingual", max_len=..., head_max_len=...)`,
		MaxLen:     config.ReferenceWorker.MaxLen, HeadMaxLen: config.ReferenceWorker.HeadMaxLen,
		Checks: map[string]bool{
			"pinned_sdk_version": false, "router_predict_signature": false,
			"multilingual_checkpoint_fixed": false, "choice_output_and_probabilities": false,
			"token_truncation_metadata": false, "offline_configuration_verified": false,
			"network_isolation_verified": false,
		},
		Reason: "Prepared SDK and model pins are recorded; runtime adapter contract and external network isolation remain unverified.",
	}
	return replaceJSON(filepath.Join(directory, "adapter_contract.json"), contract)
}

func boolValue(value bool) *bool        { return &value }
func floatValue(value float64) *float64 { return &value }
func stringValue(value string) *string  { return &value }

func writeJSONLinesExclusive(path string, rows []classifierlab.ReplayPrediction) error {
	if len(rows) > classifierlab.MaxReplayPredictions {
		return fmt.Errorf("prediction rows exceed %d", classifierlab.MaxReplayPredictions)
	}
	var data strings.Builder
	for _, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			return errors.New("encode replay prediction")
		}
		data.Write(encoded)
		data.WriteByte('\n')
	}
	return writeBytesExclusive(path, []byte(data.String()))
}
