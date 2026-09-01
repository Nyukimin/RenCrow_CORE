package dcimigration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxCutoverReceiptBytes       = int64(64 << 10)
	cutoverJSONLRetiredStageName = ".rencrow-identity-step03-jsonl-retired.stage"
)

// preparedCutoverPreflight contains only already-bound private values.  It is
// an in-memory hand-off for the later mutating unit; preflight itself never
// creates, removes, renames, or writes a filesystem entry.
type preparedCutoverPreflight struct {
	staged       preparedCutoverStage
	active       preparedCutoverActiveCohort
	retiredJSONL string
	seed         CutoverReceipt
}

// cutoverApplyError is the path-free error boundary for this unit.
func cutoverApplyError(code string) error {
	if code == "" || !validErrorCode(code) {
		code = "cutover_apply"
	}
	return newCodedError(code, "coordinated DCI cutover is not available")
}

// preflightStagedCutover verifies the immutable D2b bundle and all
// prospective paths without performing any filesystem mutation.
func preflightStagedCutover(ctx context.Context, bundle preparedCutoverStage) (preparedCutoverPreflight, CutoverReceipt, error) {
	if ctx == nil {
		return preparedCutoverPreflight{}, CutoverReceipt{}, cutoverApplyError("invalid_context")
	}
	if err := ctx.Err(); err != nil {
		return preparedCutoverPreflight{}, CutoverReceipt{}, err
	}
	if err := validatePreparedCutoverStageShape(bundle); err != nil {
		return preparedCutoverPreflight{}, CutoverReceipt{}, cutoverApplyError("invalid_input")
	}
	active, err := revalidateCutoverStageCohort(ctx, bundle.active, true)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return preparedCutoverPreflight{}, CutoverReceipt{}, err
		}
		return preparedCutoverPreflight{}, CutoverReceipt{}, cutoverApplyError("source_changed")
	}
	if !sameCutoverActiveCohort(bundle.active, active) {
		return preparedCutoverPreflight{}, CutoverReceipt{}, cutoverApplyError("source_changed")
	}
	if err := verifyCutoverRollbackRoot(active.build.paths.rollbackDir, bundle.rollbackFiles); err != nil {
		return preparedCutoverPreflight{}, CutoverReceipt{}, cutoverApplyError("rollback_verify")
	}
	if err := verifyCutoverReplacementStages(bundle.stageFiles); err != nil {
		return preparedCutoverPreflight{}, CutoverReceipt{}, cutoverApplyError("stage_verify")
	}
	if err := validateCutoverStageTargetAliases(active, bundle.rollbackFiles, bundle.stageFiles); err != nil {
		return preparedCutoverPreflight{}, CutoverReceipt{}, cutoverApplyError("unsafe_path")
	}
	if err := validateCutoverApplyStageSources(active, bundle.rollbackFiles, bundle.stageFiles); err != nil {
		return preparedCutoverPreflight{}, CutoverReceipt{}, cutoverApplyError("stage_verify")
	}
	if bundle.rollback != active.build.paths.rollbackDir || !samePath(bundle.rollback, active.build.paths.rollbackDir) {
		return preparedCutoverPreflight{}, CutoverReceipt{}, cutoverApplyError("rollback_verify")
	}
	if bundle.evidence.RollbackFileCount != 7 || bundle.evidence.ReplacementFileCount != 5 ||
		bundle.evidence.RollbackRootModeOK != 1 || bundle.evidence.ReplacementModeOK != 1 ||
		bundle.evidence.SourceInputsStable != 1 || bundle.evidence.SidecarZero != 1 ||
		bundle.evidence.NonAlias != 1 || bundle.evidence.SyncOK != 1 {
		return preparedCutoverPreflight{}, CutoverReceipt{}, cutoverApplyError("stage_evidence")
	}
	if !isLowerHexSHA256(bundle.evidence.RollbackArtifactSetSHA256) ||
		bundle.evidence.RollbackArtifactSetSHA256 != cutoverStageArtifactSetSHA256(bundle.rollbackFiles, "rollback") ||
		!isLowerHexSHA256(bundle.evidence.ReplacementArtifactSetSHA256) ||
		bundle.evidence.ReplacementArtifactSetSHA256 != cutoverStageArtifactSetSHA256(bundle.stageFiles, "replacement") {
		return preparedCutoverPreflight{}, CutoverReceipt{}, cutoverApplyError("stage_evidence")
	}
	if err := verifyCutoverActiveBindings(active.files); err != nil {
		return preparedCutoverPreflight{}, CutoverReceipt{}, cutoverApplyError("source_changed")
	}
	freshReceipt, err := resolveCutoverFreshPath(active.build.paths.cutoverReceipt)
	if err != nil || !samePath(freshReceipt, active.build.paths.cutoverReceipt) {
		return preparedCutoverPreflight{}, CutoverReceipt{}, cutoverApplyError("unsafe_path")
	}
	if err := ctx.Err(); err != nil {
		return preparedCutoverPreflight{}, CutoverReceipt{}, err
	}
	retiredJSONL, err := resolveCutoverFreshPath(filepath.Join(filepath.Dir(active.paths.dciJSONL), cutoverJSONLRetiredStageName))
	if err != nil || !samePath(filepath.Dir(retiredJSONL), filepath.Dir(active.paths.dciJSONL)) {
		return preparedCutoverPreflight{}, CutoverReceipt{}, cutoverApplyError("unsafe_path")
	}
	if err := validateCutoverApplyProspectiveAliases(active, retiredJSONL); err != nil {
		return preparedCutoverPreflight{}, CutoverReceipt{}, cutoverApplyError("unsafe_path")
	}
	if err := ctx.Err(); err != nil {
		return preparedCutoverPreflight{}, CutoverReceipt{}, err
	}
	seed := newCutoverReceiptSeed(active)
	if err := validateCutoverReceipt(seed); err != nil {
		return preparedCutoverPreflight{}, CutoverReceipt{}, cutoverApplyError("receipt_validation")
	}
	return preparedCutoverPreflight{staged: bundle, active: active, retiredJSONL: retiredJSONL, seed: seed}, seed, nil
}

func applyStagedCutover(ctx context.Context, bundle preparedCutoverStage) (CutoverReceipt, error) {
	result, err := applyStagedCutoverOperation(ctx, bundle)
	return result.receipt, err
}

func validatePreparedCutoverStageShape(bundle preparedCutoverStage) error {
	if len(bundle.rollbackFiles) != 7 || len(bundle.stageFiles) != 5 {
		return errors.New("prepared cutover stage shape is invalid")
	}
	if strings.TrimSpace(bundle.rollback) == "" || strings.TrimSpace(bundle.active.paths.dci) == "" || strings.TrimSpace(bundle.active.paths.dciJSONL) == "" || strings.TrimSpace(bundle.active.paths.eventStore) == "" || strings.TrimSpace(bundle.active.paths.l1) == "" || strings.TrimSpace(bundle.active.paths.archive) == "" {
		return errors.New("prepared cutover stage paths are incomplete")
	}
	return validatePreparedCutoverActiveShape(bundle.active)
}

func validateCutoverApplyStageSources(active preparedCutoverActiveCohort, rollback, stages []cutoverStageBinding) error {
	rollbackExpected := []struct {
		role string
		path string
	}{
		{role: "active_dci", path: active.paths.dci},
		{role: "active_dci_jsonl", path: active.paths.dciJSONL},
		{role: "active_event_store", path: active.paths.eventStore},
		{role: "active_l1", path: active.paths.l1},
		{role: "active_archive", path: active.paths.archive},
		{role: "installed_runtime", path: active.build.paths.installedRuntime},
		{role: "staged_runtime", path: active.build.paths.stagedRuntime},
	}
	for _, expected := range rollbackExpected {
		binding, ok := findCutoverStageRole(rollback, expected.role)
		want, wantOK := findCutoverBoundFile(active.files, expected.path)
		if !ok || !wantOK || !sameCutoverBoundFile(binding.source, want) {
			if expected.role == "installed_runtime" || expected.role == "staged_runtime" {
				want, wantOK = findCutoverBoundFile(active.build.files, expected.path)
				if !ok || !wantOK || !sameCutoverBoundFile(binding.source, want) {
					return errors.New("rollback source binding is invalid")
				}
			} else {
				return errors.New("rollback source binding is invalid")
			}
		}
	}
	stageExpected := []struct {
		role string
		path string
	}{
		{role: "replacement_dci", path: filepath.Join(active.build.paths.buildRoot, buildOutputDCIFilename)},
		{role: "replacement_event_store", path: filepath.Join(active.build.paths.buildRoot, buildOutputEventStoreFilename)},
		{role: "replacement_l1", path: filepath.Join(active.build.paths.buildRoot, buildOutputL1Filename)},
		{role: "replacement_archive", path: filepath.Join(active.build.paths.buildRoot, buildOutputArchiveFilename)},
		{role: "replacement_runtime", path: active.build.paths.stagedRuntime},
	}
	for _, expected := range stageExpected {
		binding, ok := findCutoverStageRole(stages, expected.role)
		want, wantOK := findCutoverBoundFile(active.build.files, expected.path)
		if !ok || !wantOK || !sameCutoverBoundFile(binding.source, want) {
			return errors.New("replacement source binding is invalid")
		}
	}
	return nil
}

func findCutoverStageRole(files []cutoverStageBinding, role string) (cutoverStageBinding, bool) {
	for _, file := range files {
		if file.role == role {
			return file, true
		}
	}
	return cutoverStageBinding{}, false
}

func validateCutoverApplyProspectiveAliases(active preparedCutoverActiveCohort, retired string) error {
	paths := []string{retired, active.build.paths.rollbackDir, active.build.paths.cutoverReceipt, active.paths.dci, active.paths.dciJSONL, active.paths.eventStore, active.paths.l1, active.paths.archive, active.build.paths.installedRuntime, active.build.paths.stagedRuntime}
	for _, file := range active.build.files {
		paths = append(paths, file.path)
	}
	for _, file := range active.files {
		paths = append(paths, file.path)
	}
	for _, file := range active.build.outputFiles {
		paths = append(paths, file.path)
	}
	for left := 0; left < len(paths); left++ {
		for right := left + 1; right < len(paths); right++ {
			if samePath(paths[left], paths[right]) || pathWithinOrRoot(paths[left], paths[right]) || pathWithinOrRoot(paths[right], paths[left]) {
				if samePath(paths[left], retired) || samePath(paths[right], retired) {
					return errors.New("retired JSONL stage aliases a cohort path")
				}
			}
		}
	}
	return nil
}

func newCutoverReceiptSeed(active preparedCutoverActiveCohort) CutoverReceipt {
	build := active.build.buildReceipt
	seed := CutoverReceipt{
		SchemaVersion:                 CutoverSchemaVersion,
		Mode:                          ModeCutover,
		Status:                        CutoverStatusBlocked,
		ErrorCode:                     "cutover_preflight",
		StartedAt:                     time.Now().UTC(),
		CompletedAt:                   time.Now().UTC(),
		BuildReceiptSHA256:            active.build.buildReceiptSHA256,
		CaptureReceiptSHA256:          build.CaptureReceiptSHA256,
		DryRunManifestSHA256:          build.DryRunManifestSHA256,
		CaptureArtifactSetSHA256:      build.CaptureArtifactSetSHA256,
		ExpectedCounts:                build.ExpectedCounts,
		SourceCounts:                  build.SourceCounts,
		ActualCounts:                  build.ActualCounts,
		DedupeCounts:                  build.DedupeCounts,
		ExclusionReasonCounts:         cloneBuildIntMap(build.ExclusionReasonCounts),
		ActorClassification:           build.ActorClassification,
		LegacyActorLabelCounts:        cloneBuildIntMap(build.LegacyActorLabelCounts),
		LogicalHashAlgorithm:          build.LogicalHashAlgorithm,
		SourceDatabaseLogicalSHA256:   cloneBuildStringMap(build.SourceDatabaseLogicalSHA256),
		SourceSchemaSHA256:            cloneBuildStringMap(build.SourceSchemaSHA256),
		SourceDCIClassificationSHA256: cloneBuildStringMap(build.SourceDCIClassificationSHA256),
		SourceFileSHA256:              cloneBuildStringMap(build.SourceFileSHA256),
		SourceNonDCILogicalSHA256:     cloneBuildStringMap(build.SourceNonDCILogicalSHA256),
		MappingSHA256:                 build.MappingSHA256,
		ActionSetSHA256:               build.ActionSetSHA256,
		TraceSetSHA256:                build.TraceSetSHA256,
		EvidenceSetSHA256:             build.EvidenceSetSHA256,
		EventSetSHA256:                build.EventSetSHA256,
		EventPlanSHA256:               build.EventPlanSHA256,
		TextNormalizationAlgorithm:    build.TextNormalizationAlgorithm,
		PlannedZeroCounters:           build.PlannedZeroCounters,
	}
	clearCutoverPostMutationClaims(&seed)
	return seed
}

func clearCutoverPostMutationClaims(receipt *CutoverReceipt) {
	if receipt == nil {
		return
	}
	receipt.OutputArtifacts = nil
	receipt.OutputArtifactSetSHA256 = ""
	receipt.RollbackArtifactSetSHA256 = ""
	receipt.ReplacementArtifactSetSHA256 = ""
	receipt.ActiveBeforeArtifactSetSHA256 = ""
	receipt.ActiveAfterArtifactSetSHA256 = ""
	receipt.RestoredArtifactSetSHA256 = ""
	receipt.OldRuntimeSHA256 = ""
	receipt.NewRuntimeSHA256 = ""
	receipt.RollbackFileCount = 0
	receipt.ReplacementFileCount = 0
	receipt.ActiveFileCount = 0
	receipt.JSONLRetired = 0
	receipt.JSONLRestored = 0
	receipt.QuickCheckOK = 0
	receipt.ForeignKeyViolations = 0
	receipt.SidecarZero = 0
	receipt.LegacyKeyMarkers = 0
	receipt.OrphanActionRefs = 0
	receipt.SourceInputsStable = 0
	receipt.DCI = BuildDCICheck{}
	receipt.EventStore = BuildEventStoreCheck{}
	receipt.L1 = BuildL1Check{}
	receipt.Archive = BuildL1Check{}
}

func validateCutoverReceipt(receipt CutoverReceipt) error {
	if receipt.SchemaVersion != CutoverSchemaVersion || receipt.Mode != ModeCutover {
		return errors.New("cutover receipt header is invalid")
	}
	if receipt.StartedAt.IsZero() || receipt.CompletedAt.IsZero() || receipt.StartedAt.Location() != time.UTC || receipt.CompletedAt.Location() != time.UTC || receipt.CompletedAt.Before(receipt.StartedAt) {
		return errors.New("cutover receipt timestamps are invalid")
	}
	if receipt.Status != CutoverStatusApplied && receipt.Status != CutoverStatusBlocked && receipt.Status != CutoverStatusRolledBack && receipt.Status != CutoverStatusRollbackFailed {
		return errors.New("cutover receipt status is invalid")
	}
	if receipt.ErrorCode != "" && !validErrorCode(receipt.ErrorCode) {
		return errors.New("cutover receipt error code is invalid")
	}
	if receipt.Status == CutoverStatusApplied && receipt.ErrorCode != "" {
		return errors.New("applied cutover receipt has an error")
	}
	if receipt.Status == CutoverStatusRollbackFailed && receipt.ErrorCode != CutoverStatusRollbackFailed {
		return errors.New("rollback-failed cutover receipt error is invalid")
	}
	if receipt.Status != CutoverStatusApplied && !validErrorCode(receipt.ErrorCode) {
		return errors.New("non-applied cutover receipt error is invalid")
	}
	if err := validateCutoverReceiptCounts(receipt); err != nil {
		return err
	}
	if err := validateCutoverReceiptMaps(receipt); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"build_receipt_sha256":              receipt.BuildReceiptSHA256,
		"capture_receipt_sha256":            receipt.CaptureReceiptSHA256,
		"dry_run_manifest_sha256":           receipt.DryRunManifestSHA256,
		"capture_artifact_set_sha256":       receipt.CaptureArtifactSetSHA256,
		"mapping_sha256":                    receipt.MappingSHA256,
		"action_set_sha256":                 receipt.ActionSetSHA256,
		"trace_set_sha256":                  receipt.TraceSetSHA256,
		"evidence_set_sha256":               receipt.EvidenceSetSHA256,
		"event_set_sha256":                  receipt.EventSetSHA256,
		"event_plan_sha256":                 receipt.EventPlanSHA256,
		"output_artifact_set_sha256":        receipt.OutputArtifactSetSHA256,
		"rollback_artifact_set_sha256":      receipt.RollbackArtifactSetSHA256,
		"replacement_artifact_set_sha256":   receipt.ReplacementArtifactSetSHA256,
		"active_before_artifact_set_sha256": receipt.ActiveBeforeArtifactSetSHA256,
		"active_after_artifact_set_sha256":  receipt.ActiveAfterArtifactSetSHA256,
		"restored_artifact_set_sha256":      receipt.RestoredArtifactSetSHA256,
		"old_runtime_sha256":                receipt.OldRuntimeSHA256,
		"new_runtime_sha256":                receipt.NewRuntimeSHA256,
	} {
		if value != "" && !isLowerHexSHA256(value) {
			return fmt.Errorf("cutover receipt %s is invalid", name)
		}
	}
	if receipt.LogicalHashAlgorithm != "" && receipt.LogicalHashAlgorithm != LogicalHashAlgorithm {
		return errors.New("cutover receipt logical hash algorithm is invalid")
	}
	if receipt.TextNormalizationAlgorithm != "" && receipt.TextNormalizationAlgorithm != TextNormalizationAlgorithm {
		return errors.New("cutover receipt text normalization algorithm is invalid")
	}
	if receipt.Status == CutoverStatusBlocked {
		if receipt.BuildReceiptSHA256 != "" && !isLowerHexSHA256(receipt.BuildReceiptSHA256) {
			return errors.New("blocked cutover build receipt hash is invalid")
		}
		if !cutoverReceiptHasNoMutationClaims(receipt) {
			return errors.New("blocked cutover receipt claims post-mutation state")
		}
		return nil
	}
	if receipt.BuildReceiptSHA256 == "" || !isLowerHexSHA256(receipt.BuildReceiptSHA256) ||
		receipt.CaptureReceiptSHA256 == "" || !isLowerHexSHA256(receipt.CaptureReceiptSHA256) ||
		receipt.DryRunManifestSHA256 == "" || !isLowerHexSHA256(receipt.DryRunManifestSHA256) ||
		receipt.CaptureArtifactSetSHA256 == "" || !isLowerHexSHA256(receipt.CaptureArtifactSetSHA256) {
		return errors.New("cutover receipt input binding is incomplete")
	}
	if err := validateCutoverManifestProjection(receipt); err != nil {
		return err
	}
	if receipt.Status == CutoverStatusRollbackFailed {
		if receipt.RestoredArtifactSetSHA256 != "" || receipt.JSONLRestored != 0 {
			return errors.New("rollback-failed receipt claims restored state")
		}
		if len(receipt.OutputArtifacts) != 0 || receipt.OutputArtifactSetSHA256 != "" || receipt.DCI != (BuildDCICheck{}) || receipt.EventStore != (BuildEventStoreCheck{}) || receipt.L1 != (BuildL1Check{}) || receipt.Archive != (BuildL1Check{}) {
			if err := validateCutoverBuildProjection(receipt); err != nil {
				return err
			}
		}
		return nil
	}
	if err := validateCutoverBuildProjection(receipt); err != nil {
		return err
	}
	if receipt.RollbackFileCount != 7 || receipt.ReplacementFileCount != 5 || receipt.ActiveFileCount != 5 ||
		!isLowerHexSHA256(receipt.RollbackArtifactSetSHA256) || !isLowerHexSHA256(receipt.ReplacementArtifactSetSHA256) ||
		receipt.QuickCheckOK != 1 || receipt.ForeignKeyViolations != 0 || receipt.SidecarZero != 1 ||
		receipt.LegacyKeyMarkers != 0 || receipt.OrphanActionRefs != 0 || receipt.SourceInputsStable != 1 ||
		!isLowerHexSHA256(receipt.OutputArtifactSetSHA256) {
		return errors.New("cutover receipt health or artifact claims are incomplete")
	}
	if receipt.Status == CutoverStatusApplied {
		if !isLowerHexSHA256(receipt.ActiveBeforeArtifactSetSHA256) || !isLowerHexSHA256(receipt.ActiveAfterArtifactSetSHA256) || receipt.RestoredArtifactSetSHA256 != "" ||
			receipt.JSONLRetired != 1 || receipt.JSONLRestored != 0 || !isLowerHexSHA256(receipt.OldRuntimeSHA256) || !isLowerHexSHA256(receipt.NewRuntimeSHA256) {
			return errors.New("applied cutover receipt claims are incomplete")
		}
		return nil
	}
	if receipt.Status == CutoverStatusRolledBack {
		if !isLowerHexSHA256(receipt.ActiveBeforeArtifactSetSHA256) || receipt.ActiveAfterArtifactSetSHA256 != "" || !isLowerHexSHA256(receipt.RestoredArtifactSetSHA256) ||
			receipt.RestoredArtifactSetSHA256 != receipt.ActiveBeforeArtifactSetSHA256 || receipt.JSONLRestored != 1 || !isLowerHexSHA256(receipt.OldRuntimeSHA256) || !isLowerHexSHA256(receipt.NewRuntimeSHA256) {
			return errors.New("rolled-back cutover receipt claims are incomplete")
		}
		return nil
	}
	return nil
}

func validateCutoverReceiptCounts(receipt CutoverReceipt) error {
	if err := validateExpectedCounts(receipt.ExpectedCounts); err != nil {
		return err
	}
	manifest := Manifest{SourceCounts: receipt.SourceCounts, ActualCounts: receipt.ActualCounts, DedupeCounts: receipt.DedupeCounts, ActorClassification: receipt.ActorClassification, LegacyActorLabelCounts: receipt.LegacyActorLabelCounts, PlannedZeroCounters: receipt.PlannedZeroCounters}
	if err := validateManifestCounts(manifest); err != nil {
		return err
	}
	if err := validateCutoverOwnerCountBounds(receipt); err != nil {
		return err
	}
	for name, value := range map[string]int{
		"rollback_file_count": receipt.RollbackFileCount, "replacement_file_count": receipt.ReplacementFileCount, "active_file_count": receipt.ActiveFileCount,
		"jsonl_retired": receipt.JSONLRetired, "jsonl_restored": receipt.JSONLRestored, "quick_check_ok": receipt.QuickCheckOK,
		"foreign_key_violations": receipt.ForeignKeyViolations, "sidecar_zero": receipt.SidecarZero, "legacy_key_markers": receipt.LegacyKeyMarkers,
		"orphan_action_refs": receipt.OrphanActionRefs, "source_inputs_stable": receipt.SourceInputsStable,
	} {
		if value < 0 || value > maxLogicalRows {
			return fmt.Errorf("cutover receipt %s is out of bounds", name)
		}
	}
	if receipt.JSONLRetired > 1 || receipt.JSONLRestored > 1 || receipt.QuickCheckOK > 1 || receipt.SidecarZero > 1 || receipt.SourceInputsStable > 1 {
		return errors.New("cutover receipt boolean counters are invalid")
	}
	return nil
}

func validateCutoverOwnerCountBounds(receipt CutoverReceipt) error {
	values := []int{
		receipt.ExpectedCounts.Searches, receipt.ExpectedCounts.ReadEvents, receipt.ExpectedCounts.EvidenceEvents,
		receipt.ExpectedCounts.TotalEvents, receipt.ExpectedCounts.LegacyLimitSteps,
		receipt.ExpectedCounts.NormalizedTextValues, receipt.ExpectedCounts.InvalidUTF8Bytes,
		receipt.SourceCounts.DCITraces, receipt.SourceCounts.DCISteps, receipt.SourceCounts.DCIEvidence,
		receipt.SourceCounts.DCIQueryTerms, receipt.SourceCounts.JSONLTraces, receipt.SourceCounts.JSONLSteps,
		receipt.SourceCounts.CurrentStaging, receipt.SourceCounts.CurrentDCIStaging, receipt.SourceCounts.CurrentRegistry,
		receipt.SourceCounts.ArchiveStaging, receipt.SourceCounts.ArchiveDCIStaging, receipt.SourceCounts.EventStore,
		receipt.ActualCounts.Searches, receipt.ActualCounts.ReadEvents, receipt.ActualCounts.EvidenceEvents,
		receipt.ActualCounts.TotalEvents, receipt.ActualCounts.LegacyLimitSteps, receipt.ActualCounts.NormalizedTextValues,
		receipt.ActualCounts.InvalidUTF8Bytes, receipt.DedupeCounts.SearchesRemoved, receipt.DedupeCounts.StepsRemoved,
		receipt.DedupeCounts.EvidenceRemoved, receipt.DedupeCounts.StagingDuplicates,
		receipt.ActorClassification.AuthenticatedAgent, receipt.ActorClassification.LegacyUnattributed,
		receipt.PlannedZeroCounters.LegacyKeyZero, receipt.PlannedZeroCounters.OrphanZero,
		receipt.DCI.TraceRows, receipt.DCI.StepRows, receipt.DCI.EvidenceRows, receipt.DCI.QueryTermRows,
		receipt.DCI.AuthenticatedTraces, receipt.DCI.LegacyUnattributedTraces, receipt.DCI.DistinctActionIDs,
		receipt.DCI.DistinctTraceIDs, receipt.DCI.DistinctStepEventIDs, receipt.DCI.DistinctEvidenceIDs,
		receipt.DCI.DistinctCreatedEventIDs, receipt.DCI.LegacyKeyMarkers, receipt.DCI.OrphanActionRefs,
		receipt.DCI.ForeignKeyViolations, receipt.DCI.QuickCheckOK, receipt.DCI.SidecarZero,
		receipt.EventStore.SourceEnvelopeCount, receipt.EventStore.PlannedEnvelopeCount, receipt.EventStore.OutputEnvelopeCount,
		receipt.EventStore.SourceDependencyCount, receipt.EventStore.PlannedDependencyCount, receipt.EventStore.OutputDependencyCount,
		receipt.EventStore.PlannedDCIEventCount, receipt.EventStore.OutputDCIEventCount, receipt.EventStore.ForeignKeyViolations,
		receipt.EventStore.QuickCheckOK, receipt.EventStore.SidecarZero,
		receipt.L1.DCIStagingRows, receipt.L1.RegistryRows, receipt.L1.CanonicalStagingRows, receipt.L1.CanonicalRegistryRows,
		receipt.L1.OldStagingRowsRemaining, receipt.L1.RawTextHashMismatches, receipt.L1.RawHashMismatches,
		receipt.L1.PromotedReferences, receipt.L1.OrphanRows, receipt.L1.ForeignKeyViolations, receipt.L1.QuickCheckOK,
		receipt.L1.SidecarZero, receipt.Archive.DCIStagingRows, receipt.Archive.RegistryRows,
		receipt.Archive.CanonicalStagingRows, receipt.Archive.CanonicalRegistryRows, receipt.Archive.OldStagingRowsRemaining,
		receipt.Archive.RawTextHashMismatches, receipt.Archive.RawHashMismatches, receipt.Archive.PromotedReferences,
		receipt.Archive.OrphanRows, receipt.Archive.ForeignKeyViolations, receipt.Archive.QuickCheckOK, receipt.Archive.SidecarZero,
	}
	for _, value := range values {
		if value < 0 || value > maxLogicalRows {
			return errors.New("cutover receipt owner count is out of bounds")
		}
	}
	return nil
}

func validateCutoverReceiptMaps(receipt CutoverReceipt) error {
	if err := validateCutoverLegacyActorLabelCounts(receipt.LegacyActorLabelCounts); err != nil {
		return err
	}
	if len(receipt.ExclusionReasonCounts) > maxJSONLRecords {
		return errors.New("cutover receipt exclusion map is too large")
	}
	for key, value := range receipt.ExclusionReasonCounts {
		if key != "legacy_limit_projection" || value < 0 || value > maxLogicalRows {
			return errors.New("cutover receipt exclusion map is invalid")
		}
	}
	mapChecks := []struct {
		name     string
		values   map[string]string
		required []string
	}{
		{"source_database_logical_sha256", receipt.SourceDatabaseLogicalSHA256, requiredDatabaseLogicalHashKeys},
		{"source_schema_sha256", receipt.SourceSchemaSHA256, requiredSchemaHashKeys},
		{"source_dci_classification_sha256", receipt.SourceDCIClassificationSHA256, requiredClassificationHashKeys},
		{"source_file_sha256", receipt.SourceFileSHA256, requiredFileHashKeys},
		{"source_non_dci_logical_sha256", receipt.SourceNonDCILogicalSHA256, requiredNonDCILogicalHashKeys},
	}
	for _, check := range mapChecks {
		if len(check.values) == 0 {
			continue
		}
		if err := validateHashMap(check.values, check.required, true, check.name); err != nil {
			return err
		}
	}
	return nil
}

func validateCutoverLegacyActorLabelCounts(labels map[string]int) error {
	manifest := Manifest{
		SchemaVersion:              ManifestSchemaVersion,
		Mode:                       ModeDryRun,
		Status:                     StatusBlocked,
		LegacyActorLabelCounts:     labels,
		LogicalHashAlgorithm:       LogicalHashAlgorithm,
		TextNormalizationAlgorithm: TextNormalizationAlgorithm,
		ErrorCode:                  "cutover_blocked",
	}
	if err := validateManifest(manifest); err != nil {
		return errors.New("cutover receipt legacy actor labels are invalid")
	}
	for _, count := range labels {
		if count > maxLogicalRows {
			return errors.New("cutover receipt legacy actor label count is out of bounds")
		}
	}
	return nil
}

func cutoverReceiptHasNoMutationClaims(receipt CutoverReceipt) bool {
	return len(receipt.OutputArtifacts) == 0 && receipt.OutputArtifactSetSHA256 == "" && receipt.RollbackArtifactSetSHA256 == "" && receipt.ReplacementArtifactSetSHA256 == "" && receipt.ActiveBeforeArtifactSetSHA256 == "" && receipt.ActiveAfterArtifactSetSHA256 == "" && receipt.RestoredArtifactSetSHA256 == "" && receipt.OldRuntimeSHA256 == "" && receipt.NewRuntimeSHA256 == "" && receipt.RollbackFileCount == 0 && receipt.ReplacementFileCount == 0 && receipt.ActiveFileCount == 0 && receipt.JSONLRetired == 0 && receipt.JSONLRestored == 0 && receipt.QuickCheckOK == 0 && receipt.ForeignKeyViolations == 0 && receipt.SidecarZero == 0 && receipt.LegacyKeyMarkers == 0 && receipt.OrphanActionRefs == 0 && receipt.SourceInputsStable == 0 && receipt.DCI == (BuildDCICheck{}) && receipt.EventStore == (BuildEventStoreCheck{}) && receipt.L1 == (BuildL1Check{}) && receipt.Archive == (BuildL1Check{})
}

func validateCutoverBuildProjection(receipt CutoverReceipt) error {
	if err := validateBuildOutputArtifacts(BuildReceipt{OutputArtifacts: receipt.OutputArtifacts, OutputArtifactSetSHA256: receipt.OutputArtifactSetSHA256}); err != nil {
		return err
	}
	projection := BuildReceipt{
		ExpectedCounts: receipt.ExpectedCounts, SourceCounts: receipt.SourceCounts, ActualCounts: receipt.ActualCounts, ActorClassification: receipt.ActorClassification,
		SourceDatabaseLogicalSHA256: receipt.SourceDatabaseLogicalSHA256, SourceSchemaSHA256: receipt.SourceSchemaSHA256, SourceNonDCILogicalSHA256: receipt.SourceNonDCILogicalSHA256,
		OutputArtifacts: receipt.OutputArtifacts, DCI: receipt.DCI, EventStore: receipt.EventStore, L1: receipt.L1, Archive: receipt.Archive,
	}
	return validateBuildOwnerChecks(projection)
}

func validateCutoverManifestProjection(receipt CutoverReceipt) error {
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion, Mode: ModeDryRun, Status: StatusReady,
		ExpectedCounts: receipt.ExpectedCounts, SourceCounts: receipt.SourceCounts, ActualCounts: receipt.ActualCounts,
		DedupeCounts: receipt.DedupeCounts, ExclusionReasonCounts: receipt.ExclusionReasonCounts,
		ActorClassification:           receipt.ActorClassification,
		LegacyActorLabelCounts:        receipt.LegacyActorLabelCounts,
		LogicalHashAlgorithm:          receipt.LogicalHashAlgorithm,
		SourceDatabaseLogicalSHA256:   receipt.SourceDatabaseLogicalSHA256,
		SourceSchemaSHA256:            receipt.SourceSchemaSHA256,
		SourceDCIClassificationSHA256: receipt.SourceDCIClassificationSHA256,
		SourceFileSHA256:              receipt.SourceFileSHA256,
		SourceNonDCILogicalSHA256:     receipt.SourceNonDCILogicalSHA256,
		MappingSHA256:                 receipt.MappingSHA256, ActionSetSHA256: receipt.ActionSetSHA256,
		TraceSetSHA256: receipt.TraceSetSHA256, EvidenceSetSHA256: receipt.EvidenceSetSHA256,
		EventSetSHA256: receipt.EventSetSHA256, EventPlanSHA256: receipt.EventPlanSHA256,
		TextNormalizationAlgorithm: receipt.TextNormalizationAlgorithm,
		PlannedZeroCounters:        receipt.PlannedZeroCounters,
	}
	return validateManifest(manifest)
}

func marshalCutoverReceipt(receipt CutoverReceipt) ([]byte, error) {
	if err := validateCutoverReceipt(receipt); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || int64(len(encoded))+1 > maxCutoverReceiptBytes {
		return nil, errors.New("cutover receipt exceeds size bound")
	}
	return append(encoded, '\n'), nil
}

func readCutoverReceipt(path string) (CutoverReceipt, error) {
	absolute, err := absolutePath(path)
	if err != nil {
		return CutoverReceipt{}, cutoverApplyError("receipt_read")
	}
	resolved, err := resolveCutoverExistingPath(absolute)
	if err != nil || !samePath(resolved, absolute) {
		return CutoverReceipt{}, cutoverApplyError("receipt_read")
	}
	info, err := inspectCutoverFile(resolved, true, false)
	if err != nil {
		return CutoverReceipt{}, cutoverApplyError("receipt_read")
	}
	if info.Size() > maxCutoverReceiptBytes {
		return CutoverReceipt{}, cutoverApplyError("receipt_read")
	}
	data, err := readBuildInputBytes(resolved, maxCutoverReceiptBytes)
	if err != nil || rejectDuplicateJSONKeys(data) != nil {
		return CutoverReceipt{}, cutoverApplyError("receipt_read")
	}
	var receipt CutoverReceipt
	if err := decodeOneBuildInputObject(data, &receipt); err != nil || validateCutoverReceipt(receipt) != nil {
		return CutoverReceipt{}, cutoverApplyError("receipt_read")
	}
	return receipt, nil
}
