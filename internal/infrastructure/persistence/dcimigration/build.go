package dcimigration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"time"
)

// maxBuildReceiptBytes is deliberately the same one MiB bound used by the
// dry-run manifest.  A build receipt is evidence only; it never carries a
// query, payload, path, or identity value.
const maxBuildReceiptBytes = int64(1 << 20)

// buildReceiptWriter is a narrow durability seam.  The production value is
// writeBuildReceipt; tests may inject a bounded write failure without adding a
// second output route.
var buildReceiptWriter = writeBuildReceipt

// Build binds one captured snapshot and one ready dry-run manifest, then
// materializes the four fixed offline databases through their owner helpers.
// It never writes production state and never re-plans the retained migration
// graph.  A blocked result is returned together with a bounded generic error.
func Build(ctx context.Context, options BuildOptions) (BuildReceipt, error) {
	started := time.Now().UTC()
	receipt := newBaseBuildReceipt(started)
	if ctx == nil {
		return finishBlockedBuild(receipt, "invalid_options", nil)
	}
	if err := ctx.Err(); err != nil {
		return finishBlockedBuild(receipt, "context_canceled", err)
	}

	prepared, err := prepareBuild(ctx, buildOptions{
		SnapshotDir:    options.SnapshotDir,
		BuildDir:       options.BuildDir,
		CaptureReceipt: options.CaptureReceipt,
		DryRunManifest: options.DryRunManifest,
		AgentIDs:       append([]string(nil), options.AgentIDs...),
	})
	if err != nil {
		return finishBlockedBuild(receipt, errorCode(err, "build_prepare"), err)
	}
	receipt = buildReceiptFromPrepared(receipt, prepared)
	if err := ctx.Err(); err != nil {
		return finishBlockedBuildAtRoot(receipt, prepared.paths.buildDir, "context_canceled", err)
	}

	evidence, err := materializeBuildOutputs(ctx, prepared)
	if err != nil {
		return finishBlockedBuildAtRoot(receipt, prepared.paths.buildDir, errorCode(err, "build_outputs"), err)
	}
	receipt = buildReceiptFromOutputEvidence(receipt, evidence)
	receipt.Status = StatusReady
	receipt.ErrorCode = ""
	receipt.CompletedAt = time.Now().UTC()
	if err := validateBuildReceipt(receipt); err != nil {
		return finishBlockedBuildAtRoot(receipt, prepared.paths.buildDir, "receipt_validation", err)
	}
	if err := ctx.Err(); err != nil {
		return finishBlockedBuildAtRoot(receipt, prepared.paths.buildDir, "context_canceled", err)
	}

	receiptPath := filepath.Join(prepared.paths.buildDir, BuildReceiptFilename)
	if err := buildReceiptWriter(receiptPath, receipt); err != nil {
		return finishBlockedBuildAtRoot(receipt, prepared.paths.buildDir, "receipt_write", err)
	}
	if err := verifyBuiltOutput(ctx, prepared, receipt); err != nil {
		return finishBlockedBuildAtRoot(receipt, prepared.paths.buildDir, "build_verify", err)
	}
	return receipt, nil
}

func newBaseBuildReceipt(started time.Time) BuildReceipt {
	return BuildReceipt{
		SchemaVersion:                 BuildSchemaVersion,
		Mode:                          ModeBuild,
		Status:                        StatusBlocked,
		StartedAt:                     started,
		ExpectedCounts:                ExpectedCounts{},
		ExclusionReasonCounts:         make(map[string]int),
		LegacyActorLabelCounts:        make(map[string]int),
		LogicalHashAlgorithm:          LogicalHashAlgorithm,
		SourceDatabaseLogicalSHA256:   make(map[string]string),
		SourceSchemaSHA256:            make(map[string]string),
		SourceDCIClassificationSHA256: make(map[string]string),
		SourceFileSHA256:              make(map[string]string),
		SourceNonDCILogicalSHA256:     make(map[string]string),
		TextNormalizationAlgorithm:    TextNormalizationAlgorithm,
	}
}

func buildReceiptFromPrepared(receipt BuildReceipt, prepared preparedBuild) BuildReceipt {
	manifest := prepared.dryRunManifest
	receipt.ExpectedCounts = manifest.ExpectedCounts
	receipt.SourceCounts = manifest.SourceCounts
	receipt.ActualCounts = manifest.ActualCounts
	receipt.DedupeCounts = manifest.DedupeCounts
	receipt.ExclusionReasonCounts = cloneBuildIntMap(manifest.ExclusionReasonCounts)
	receipt.ActorClassification = manifest.ActorClassification
	receipt.LegacyActorLabelCounts = cloneBuildIntMap(manifest.LegacyActorLabelCounts)
	receipt.LogicalHashAlgorithm = manifest.LogicalHashAlgorithm
	receipt.SourceDatabaseLogicalSHA256 = cloneBuildStringMap(manifest.SourceDatabaseLogicalSHA256)
	receipt.SourceSchemaSHA256 = cloneBuildStringMap(manifest.SourceSchemaSHA256)
	receipt.SourceDCIClassificationSHA256 = cloneBuildStringMap(manifest.SourceDCIClassificationSHA256)
	receipt.SourceFileSHA256 = cloneBuildStringMap(manifest.SourceFileSHA256)
	receipt.SourceNonDCILogicalSHA256 = cloneBuildStringMap(manifest.SourceNonDCILogicalSHA256)
	receipt.MappingSHA256 = manifest.MappingSHA256
	receipt.ActionSetSHA256 = manifest.ActionSetSHA256
	receipt.TraceSetSHA256 = manifest.TraceSetSHA256
	receipt.EvidenceSetSHA256 = manifest.EvidenceSetSHA256
	receipt.EventSetSHA256 = manifest.EventSetSHA256
	receipt.EventPlanSHA256 = manifest.EventPlanSHA256
	receipt.TextNormalizationAlgorithm = manifest.TextNormalizationAlgorithm
	receipt.CaptureReceiptSHA256 = prepared.captureReceiptSHA256
	receipt.DryRunManifestSHA256 = prepared.dryRunManifestSHA256
	receipt.CaptureArtifactSetSHA256 = prepared.artifactSetSHA256
	receipt.PlannedZeroCounters = manifest.PlannedZeroCounters
	return receipt
}

func buildReceiptFromOutputEvidence(receipt BuildReceipt, evidence buildOutputsEvidence) BuildReceipt {
	receipt.OutputArtifacts = map[string]BuildOutputArtifact{
		buildOutputDCIRole: {
			FileSHA256:           evidence.TargetDCISHA256,
			Bytes:                evidence.TargetDCIBytes,
			OutputSchemaSHA256:   evidence.DCI.OutputSchemaSHA256,
			OutputLogicalSHA256:  evidence.DCI.OutputLogicalSHA256,
			QuickCheckOK:         evidence.DCI.QuickCheckOK,
			ForeignKeyViolations: evidence.DCI.ForeignKeyViolations,
			SidecarZero:          evidence.DCI.SidecarZero,
		},
		buildOutputEventStoreRole: {
			FileSHA256:           evidence.TargetEventStoreSHA256,
			Bytes:                evidence.TargetEventStoreBytes,
			OutputSchemaSHA256:   evidence.EventStore.OutputSchemaSHA256,
			OutputLogicalSHA256:  evidence.EventStore.OutputLogicalSHA256,
			OutputNonDCISHA256:   evidence.EventStore.OutputNonDCISHA256,
			QuickCheckOK:         evidence.EventStore.QuickCheckOK,
			ForeignKeyViolations: evidence.EventStore.ForeignKeyViolations,
			SidecarZero:          evidence.EventStore.SidecarZero,
		},
		buildOutputL1Role: {
			FileSHA256:           evidence.TargetL1SHA256,
			Bytes:                evidence.TargetL1Bytes,
			OutputSchemaSHA256:   evidence.L1.OutputSchemaSHA256,
			OutputNonDCISHA256:   evidence.L1.OutputNonDCISHA256,
			QuickCheckOK:         evidence.L1.QuickCheckOK,
			ForeignKeyViolations: evidence.L1.ForeignKeyViolations,
			SidecarZero:          evidence.L1.SidecarZero,
		},
		buildOutputArchiveRole: {
			FileSHA256:           evidence.TargetArchiveSHA256,
			Bytes:                evidence.TargetArchiveBytes,
			OutputSchemaSHA256:   evidence.Archive.OutputSchemaSHA256,
			OutputNonDCISHA256:   evidence.Archive.OutputNonDCISHA256,
			QuickCheckOK:         evidence.Archive.QuickCheckOK,
			ForeignKeyViolations: evidence.Archive.ForeignKeyViolations,
			SidecarZero:          evidence.Archive.SidecarZero,
		},
	}
	receipt.OutputArtifactSetSHA256 = evidence.ArtifactSetSHA256
	receipt.BuildRootModeOK = evidence.BuildRootModeOK
	receipt.SidecarZero = evidence.SidecarZero
	receipt.SourceInputsStable = evidence.SourceInputsStable
	receipt.DCI = buildDCICheck(evidence.DCI)
	receipt.EventStore = buildEventStoreCheck(evidence.EventStore)
	receipt.L1 = buildL1Check(evidence.L1)
	receipt.Archive = buildL1Check(evidence.Archive)
	return receipt
}

func buildDCICheck(evidence buildDCIEvidence) BuildDCICheck {
	return BuildDCICheck{
		OutputSchemaSHA256: evidence.OutputSchemaSHA256, OutputLogicalSHA256: evidence.OutputLogicalSHA256,
		TraceRows: evidence.TraceRows, StepRows: evidence.StepRows, EvidenceRows: evidence.EvidenceRows,
		QueryTermRows: evidence.QueryTermRows, AuthenticatedTraces: evidence.AuthenticatedTraces,
		LegacyUnattributedTraces: evidence.LegacyUnattributedTraces, DistinctActionIDs: evidence.DistinctActionIDs,
		DistinctTraceIDs: evidence.DistinctTraceIDs, DistinctStepEventIDs: evidence.DistinctStepEventIDs,
		DistinctEvidenceIDs: evidence.DistinctEvidenceIDs, DistinctCreatedEventIDs: evidence.DistinctCreatedEventIDs,
		LegacyKeyMarkers: evidence.LegacyKeyMarkers, OrphanActionRefs: evidence.OrphanActionRefs,
		ForeignKeyViolations: evidence.ForeignKeyViolations, QuickCheckOK: evidence.QuickCheckOK,
		SidecarZero: evidence.SidecarZero,
	}
}

func buildEventStoreCheck(evidence buildEventStoreEvidence) BuildEventStoreCheck {
	return BuildEventStoreCheck{
		SourceSchemaSHA256: evidence.SourceSchemaSHA256, OutputSchemaSHA256: evidence.OutputSchemaSHA256,
		SourceNonDCISHA256: evidence.SourceNonDCISHA256, OutputNonDCISHA256: evidence.OutputNonDCISHA256,
		OutputLogicalSHA256: evidence.OutputLogicalSHA256, SourceEnvelopeCount: evidence.SourceEnvelopeCount,
		PlannedEnvelopeCount: evidence.PlannedEnvelopeCount, OutputEnvelopeCount: evidence.OutputEnvelopeCount,
		SourceDependencyCount: evidence.SourceDependencyCount, PlannedDependencyCount: evidence.PlannedDependencyCount,
		OutputDependencyCount: evidence.OutputDependencyCount, PlannedDCIEventCount: evidence.PlannedDCIEventCount,
		OutputDCIEventCount: evidence.OutputDCIEventCount, ForeignKeyViolations: evidence.ForeignKeyViolations,
		QuickCheckOK: evidence.QuickCheckOK, SidecarZero: evidence.SidecarZero,
	}
}

func buildL1Check(evidence l1ProjectionEvidence) BuildL1Check {
	return BuildL1Check{
		DCIStagingRows: evidence.DCIStagingRows, RegistryRows: evidence.RegistryRows,
		CanonicalStagingRows: evidence.CanonicalStagingRows, CanonicalRegistryRows: evidence.CanonicalRegistryRows,
		OldStagingRowsRemaining: evidence.OldStagingRowsRemaining, RawTextHashMismatches: evidence.RawTextHashMismatches,
		RawHashMismatches: evidence.RawHashMismatches, PromotedReferences: evidence.PromotedReferences,
		OrphanRows: evidence.OrphanRows, ForeignKeyViolations: evidence.ForeignKeyViolations,
		QuickCheckOK: evidence.QuickCheckOK, SidecarZero: evidence.SidecarZero,
		SourceSchemaSHA256: evidence.SourceSchemaSHA256, OutputSchemaSHA256: evidence.OutputSchemaSHA256,
		SourceNonDCISHA256: evidence.SourceNonDCISHA256, OutputNonDCISHA256: evidence.OutputNonDCISHA256,
	}
}

func cloneBuildIntMap(input map[string]int) map[string]int {
	if input == nil {
		return make(map[string]int)
	}
	output := make(map[string]int, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneBuildStringMap(input map[string]string) map[string]string {
	if input == nil {
		return make(map[string]string)
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func finishBlockedBuild(receipt BuildReceipt, code string, cause error) (BuildReceipt, error) {
	return finishBlockedBuildAtRoot(receipt, "", code, cause)
}

func finishBlockedBuildAtRoot(receipt BuildReceipt, root, code string, cause error) (BuildReceipt, error) {
	if code == "" || !validErrorCode(code) {
		code = "build_blocked"
	}
	receipt.Status = StatusBlocked
	receipt.ErrorCode = code
	receipt.CompletedAt = time.Now().UTC()
	receipt.OutputArtifacts = nil
	receipt.OutputArtifactSetSHA256 = ""
	receipt.BuildRootModeOK = 0
	receipt.SidecarZero = 0
	receipt.SourceInputsStable = 0
	receipt.DCI = BuildDCICheck{}
	receipt.EventStore = BuildEventStoreCheck{}
	receipt.L1 = BuildL1Check{}
	receipt.Archive = BuildL1Check{}

	if root != "" && safeBuildRootForReceipt(root) {
		cleanupBuildOutputRoot(root)
		receiptPath := filepath.Join(root, BuildReceiptFilename)
		if writeErr := buildReceiptWriter(receiptPath, receipt); writeErr != nil || !verifyBlockedBuildReceipt(root, receipt) {
			// A failed blocked-receipt write must not leave a ready-looking
			// artifact or stale sidecar behind.  The root remains only when it
			// is still a safe, empty directory.
			cleanupBuildOutputRoot(root)
			receipt.ErrorCode = "receipt_write"
			return receipt, buildError("receipt_write")
		}
	}
	if errors.Is(cause, context.Canceled) {
		return receipt, context.Canceled
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return receipt, context.DeadlineExceeded
	}
	return receipt, buildError(code)
}

func verifyBlockedBuildReceipt(root string, want BuildReceipt) bool {
	if !safeBuildRootForReceipt(root) {
		return false
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != BuildReceiptFilename {
		return false
	}
	data, err := readBuildInputBytes(filepath.Join(root, BuildReceiptFilename), maxBuildReceiptBytes)
	if err != nil {
		return false
	}
	var got BuildReceipt
	if err := decodeOneBuildInputObject(data, &got); err != nil || !reflect.DeepEqual(got, want) {
		return false
	}
	return validateBuildReceipt(got) == nil
}

func buildError(code string) error {
	if code == "" || !validErrorCode(code) {
		code = "build_blocked"
	}
	return newCodedError(code, "offline DCI build failed")
}

func validateBuildReceipt(receipt BuildReceipt) error {
	if receipt.SchemaVersion != BuildSchemaVersion || receipt.Mode != ModeBuild {
		return errors.New("build receipt header is invalid")
	}
	if receipt.StartedAt.IsZero() || receipt.CompletedAt.IsZero() || receipt.CompletedAt.Before(receipt.StartedAt) {
		return errors.New("build receipt timestamps are invalid")
	}
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion, Mode: ModeDryRun, Status: receipt.Status,
		ExpectedCounts: receipt.ExpectedCounts, SourceCounts: receipt.SourceCounts, ActualCounts: receipt.ActualCounts,
		DedupeCounts: receipt.DedupeCounts, ExclusionReasonCounts: receipt.ExclusionReasonCounts,
		ActorClassification: receipt.ActorClassification, LegacyActorLabelCounts: receipt.LegacyActorLabelCounts,
		LogicalHashAlgorithm: receipt.LogicalHashAlgorithm, SourceDatabaseLogicalSHA256: receipt.SourceDatabaseLogicalSHA256,
		SourceSchemaSHA256: receipt.SourceSchemaSHA256, SourceDCIClassificationSHA256: receipt.SourceDCIClassificationSHA256,
		SourceFileSHA256: receipt.SourceFileSHA256, SourceNonDCILogicalSHA256: receipt.SourceNonDCILogicalSHA256,
		MappingSHA256: receipt.MappingSHA256, ActionSetSHA256: receipt.ActionSetSHA256, TraceSetSHA256: receipt.TraceSetSHA256,
		EvidenceSetSHA256: receipt.EvidenceSetSHA256, EventSetSHA256: receipt.EventSetSHA256, EventPlanSHA256: receipt.EventPlanSHA256,
		TextNormalizationAlgorithm: receipt.TextNormalizationAlgorithm, PlannedZeroCounters: receipt.PlannedZeroCounters,
		ErrorCode: receipt.ErrorCode,
	}
	if err := validateManifest(manifest); err != nil {
		return errors.New("build receipt manifest binding is invalid")
	}
	if receipt.Status != StatusReady && receipt.Status != StatusBlocked {
		return errors.New("build receipt status is invalid")
	}
	if receipt.CaptureReceiptSHA256 != "" && !isLowerHexSHA256(receipt.CaptureReceiptSHA256) {
		return errors.New("build capture receipt hash is invalid")
	}
	if receipt.DryRunManifestSHA256 != "" && !isLowerHexSHA256(receipt.DryRunManifestSHA256) {
		return errors.New("build dry-run manifest hash is invalid")
	}
	if receipt.CaptureArtifactSetSHA256 != "" && !isLowerHexSHA256(receipt.CaptureArtifactSetSHA256) {
		return errors.New("build capture artifact set hash is invalid")
	}
	if receipt.Status == StatusBlocked {
		if !validErrorCode(receipt.ErrorCode) || len(receipt.OutputArtifacts) != 0 || receipt.OutputArtifactSetSHA256 != "" || receipt.BuildRootModeOK != 0 || receipt.SidecarZero != 0 || receipt.SourceInputsStable != 0 || receipt.DCI != (BuildDCICheck{}) || receipt.EventStore != (BuildEventStoreCheck{}) || receipt.L1 != (BuildL1Check{}) || receipt.Archive != (BuildL1Check{}) {
			return errors.New("blocked build receipt output claim is invalid")
		}
		return nil
	}
	if receipt.ErrorCode != "" || !isLowerHexSHA256(receipt.CaptureReceiptSHA256) || !isLowerHexSHA256(receipt.DryRunManifestSHA256) || !isLowerHexSHA256(receipt.CaptureArtifactSetSHA256) {
		return errors.New("ready build receipt input binding is incomplete")
	}
	if receipt.BuildRootModeOK != 1 || receipt.SidecarZero != 1 || receipt.SourceInputsStable != 1 {
		return errors.New("ready build receipt health is incomplete")
	}
	if !isLowerHexSHA256(receipt.OutputArtifactSetSHA256) {
		return errors.New("ready build output artifact set hash is invalid")
	}
	if err := validateBuildOutputArtifacts(receipt); err != nil {
		return err
	}
	if err := validateBuildOwnerChecks(receipt); err != nil {
		return err
	}
	return nil
}

func validateBuildOutputArtifacts(receipt BuildReceipt) error {
	expected := map[string]struct{}{
		buildOutputDCIRole: {}, buildOutputEventStoreRole: {}, buildOutputL1Role: {}, buildOutputArchiveRole: {},
	}
	if len(receipt.OutputArtifacts) != len(expected) {
		return errors.New("ready build output artifact set is incomplete")
	}
	artifactFiles := make(map[string]buildOutputFile, len(receipt.OutputArtifacts))
	for role, artifact := range receipt.OutputArtifacts {
		if _, ok := expected[role]; !ok || !isLowerHexSHA256(artifact.FileSHA256) || artifact.Bytes < 0 || artifact.QuickCheckOK != 1 || artifact.ForeignKeyViolations != 0 || artifact.SidecarZero != 1 {
			return errors.New("ready build output artifact is invalid")
		}
		if !isLowerHexSHA256(artifact.OutputSchemaSHA256) {
			return errors.New("ready build output schema hash is invalid")
		}
		switch role {
		case buildOutputDCIRole:
			if !isLowerHexSHA256(artifact.OutputLogicalSHA256) || artifact.OutputNonDCISHA256 != "" {
				return errors.New("ready DCI output hashes are invalid")
			}
		case buildOutputEventStoreRole:
			if !isLowerHexSHA256(artifact.OutputLogicalSHA256) || !isLowerHexSHA256(artifact.OutputNonDCISHA256) {
				return errors.New("ready Event Store output hashes are invalid")
			}
		case buildOutputL1Role, buildOutputArchiveRole:
			if artifact.OutputLogicalSHA256 != "" || !isLowerHexSHA256(artifact.OutputNonDCISHA256) {
				return errors.New("ready L1 output hashes are invalid")
			}
		}
		artifactFiles[role] = buildOutputFile{
			role: role, sha256: artifact.FileSHA256, bytes: artifact.Bytes,
			quickCheckOK: artifact.QuickCheckOK, sidecarZero: artifact.SidecarZero,
		}
	}
	if buildOutputArtifactSetSHA256(artifactFiles) != receipt.OutputArtifactSetSHA256 {
		return errors.New("ready build output artifact set hash does not match")
	}
	return nil
}

func validateBuildOwnerChecks(receipt BuildReceipt) error {
	if err := validateBuildDCICheck(receipt); err != nil {
		return err
	}
	if err := validateBuildEventStoreCheck(receipt); err != nil {
		return err
	}
	if err := validateBuildL1Check(receipt.L1, receipt.SourceSchemaSHA256, receipt.SourceNonDCILogicalSHA256, false); err != nil {
		return err
	}
	if err := validateBuildL1Check(receipt.Archive, receipt.SourceSchemaSHA256, receipt.SourceNonDCILogicalSHA256, true); err != nil {
		return err
	}
	if dci := receipt.OutputArtifacts[buildOutputDCIRole]; dci.OutputSchemaSHA256 != receipt.DCI.OutputSchemaSHA256 || dci.OutputLogicalSHA256 != receipt.DCI.OutputLogicalSHA256 || dci.OutputNonDCISHA256 != "" || dci.QuickCheckOK != receipt.DCI.QuickCheckOK || dci.ForeignKeyViolations != receipt.DCI.ForeignKeyViolations || dci.SidecarZero != receipt.DCI.SidecarZero {
		return errors.New("ready DCI output and owner evidence differ")
	}
	if eventStore := receipt.OutputArtifacts[buildOutputEventStoreRole]; eventStore.OutputSchemaSHA256 != receipt.EventStore.OutputSchemaSHA256 || eventStore.OutputLogicalSHA256 != receipt.EventStore.OutputLogicalSHA256 || eventStore.OutputNonDCISHA256 != receipt.EventStore.OutputNonDCISHA256 || eventStore.QuickCheckOK != receipt.EventStore.QuickCheckOK || eventStore.ForeignKeyViolations != receipt.EventStore.ForeignKeyViolations || eventStore.SidecarZero != receipt.EventStore.SidecarZero {
		return errors.New("ready Event Store output and owner evidence differ")
	}
	if l1 := receipt.OutputArtifacts[buildOutputL1Role]; l1.OutputSchemaSHA256 != receipt.L1.OutputSchemaSHA256 || l1.OutputLogicalSHA256 != "" || l1.OutputNonDCISHA256 != receipt.L1.OutputNonDCISHA256 || l1.QuickCheckOK != receipt.L1.QuickCheckOK || l1.ForeignKeyViolations != receipt.L1.ForeignKeyViolations || l1.SidecarZero != receipt.L1.SidecarZero {
		return errors.New("ready L1 output and owner evidence differ")
	}
	if archive := receipt.OutputArtifacts[buildOutputArchiveRole]; archive.OutputSchemaSHA256 != receipt.Archive.OutputSchemaSHA256 || archive.OutputLogicalSHA256 != "" || archive.OutputNonDCISHA256 != receipt.Archive.OutputNonDCISHA256 || archive.QuickCheckOK != receipt.Archive.QuickCheckOK || archive.ForeignKeyViolations != receipt.Archive.ForeignKeyViolations || archive.SidecarZero != receipt.Archive.SidecarZero {
		return errors.New("ready archive output and owner evidence differ")
	}
	return nil
}

func validateBuildDCICheck(receipt BuildReceipt) error {
	check := receipt.DCI
	if !isLowerHexSHA256(check.OutputSchemaSHA256) || !isLowerHexSHA256(check.OutputLogicalSHA256) {
		return errors.New("ready DCI owner hashes are invalid")
	}
	if check.TraceRows != receipt.ActualCounts.Searches || check.StepRows != receipt.ActualCounts.ReadEvents || check.EvidenceRows != receipt.ActualCounts.EvidenceEvents || check.AuthenticatedTraces != receipt.ActorClassification.AuthenticatedAgent || check.LegacyUnattributedTraces != receipt.ActorClassification.LegacyUnattributed {
		return errors.New("ready DCI owner counts do not bind the plan")
	}
	for _, count := range []int{check.TraceRows, check.StepRows, check.EvidenceRows, check.QueryTermRows, check.AuthenticatedTraces, check.LegacyUnattributedTraces, check.DistinctActionIDs, check.DistinctTraceIDs, check.DistinctStepEventIDs, check.DistinctEvidenceIDs, check.DistinctCreatedEventIDs, check.LegacyKeyMarkers, check.OrphanActionRefs, check.ForeignKeyViolations, check.QuickCheckOK, check.SidecarZero} {
		if count < 0 {
			return errors.New("ready DCI owner count is negative")
		}
	}
	if check.DistinctActionIDs != check.TraceRows || check.DistinctTraceIDs != check.TraceRows || check.DistinctStepEventIDs != check.StepRows || check.DistinctEvidenceIDs != check.EvidenceRows || check.DistinctCreatedEventIDs != check.EvidenceRows || check.LegacyKeyMarkers != 0 || check.OrphanActionRefs != 0 || check.ForeignKeyViolations != 0 || check.QuickCheckOK != 1 || check.SidecarZero != 1 {
		return errors.New("ready DCI owner zero or distinctness checks failed")
	}
	return nil
}

func validateBuildEventStoreCheck(receipt BuildReceipt) error {
	check := receipt.EventStore
	if !isLowerHexSHA256(check.SourceSchemaSHA256) || !isLowerHexSHA256(check.OutputSchemaSHA256) || !isLowerHexSHA256(check.SourceNonDCISHA256) || !isLowerHexSHA256(check.OutputNonDCISHA256) || !isLowerHexSHA256(check.OutputLogicalSHA256) {
		return errors.New("ready Event Store owner hashes are invalid")
	}
	if check.SourceSchemaSHA256 != receipt.SourceSchemaSHA256["source_event_store"] || check.SourceNonDCISHA256 != receipt.SourceNonDCILogicalSHA256["source_event_store"] || check.PlannedEnvelopeCount != receipt.ActualCounts.TotalEvents || check.OutputEnvelopeCount != check.SourceEnvelopeCount+check.PlannedEnvelopeCount || check.OutputDependencyCount != check.SourceDependencyCount+check.PlannedDependencyCount || check.PlannedDCIEventCount != check.PlannedEnvelopeCount || check.OutputDCIEventCount != check.PlannedEnvelopeCount {
		return errors.New("ready Event Store owner counts do not bind the plan")
	}
	for _, count := range []int{check.SourceEnvelopeCount, check.PlannedEnvelopeCount, check.OutputEnvelopeCount, check.SourceDependencyCount, check.PlannedDependencyCount, check.OutputDependencyCount, check.PlannedDCIEventCount, check.OutputDCIEventCount, check.ForeignKeyViolations, check.QuickCheckOK, check.SidecarZero} {
		if count < 0 {
			return errors.New("ready Event Store owner count is negative")
		}
	}
	if check.ForeignKeyViolations != 0 || check.QuickCheckOK != 1 || check.SidecarZero != 1 {
		return errors.New("ready Event Store owner health is invalid")
	}
	return nil
}

func validateBuildL1Check(check BuildL1Check, sourceSchemas, sourceNonDCI map[string]string, archive bool) error {
	if !isLowerHexSHA256(check.SourceSchemaSHA256) || !isLowerHexSHA256(check.OutputSchemaSHA256) || !isLowerHexSHA256(check.SourceNonDCISHA256) || !isLowerHexSHA256(check.OutputNonDCISHA256) {
		return errors.New("ready L1 owner hashes are invalid")
	}
	key := "source_l1"
	if archive {
		key = "source_archive"
	}
	if check.SourceSchemaSHA256 != sourceSchemas[key] || check.SourceNonDCISHA256 != sourceNonDCI[key] {
		return errors.New("ready L1 owner source hashes do not bind the snapshot")
	}
	for _, count := range []int{check.DCIStagingRows, check.RegistryRows, check.CanonicalStagingRows, check.CanonicalRegistryRows, check.OldStagingRowsRemaining, check.RawTextHashMismatches, check.RawHashMismatches, check.PromotedReferences, check.OrphanRows, check.ForeignKeyViolations, check.QuickCheckOK, check.SidecarZero} {
		if count < 0 {
			return errors.New("ready L1 owner count is negative")
		}
	}
	if check.CanonicalStagingRows != check.DCIStagingRows || check.CanonicalRegistryRows != check.RegistryRows || check.OldStagingRowsRemaining != 0 || check.RawTextHashMismatches != 0 || check.RawHashMismatches != 0 || check.PromotedReferences != 0 || check.OrphanRows != 0 || check.ForeignKeyViolations != 0 || check.QuickCheckOK != 1 || check.SidecarZero != 1 {
		return errors.New("ready L1 owner zero or row checks failed")
	}
	if archive && (check.RegistryRows != 0 || check.CanonicalRegistryRows != 0) {
		return errors.New("ready archive owner registry rows are invalid")
	}
	return nil
}

func writeBuildReceipt(path string, receipt BuildReceipt) error {
	if err := validateBuildReceipt(receipt); err != nil {
		return buildError("receipt_write")
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || int64(len(encoded))+1 > maxBuildReceiptBytes {
		return buildError("receipt_write")
	}
	encoded = append(encoded, '\n')

	absolute, err := absolutePath(path)
	if err != nil || filepath.Base(absolute) != BuildReceiptFilename {
		return buildError("receipt_write")
	}
	root := filepath.Dir(absolute)
	if !safeBuildRootForReceipt(root) {
		return buildError("receipt_write")
	}
	if _, err := os.Lstat(absolute); err == nil || !errors.Is(err, os.ErrNotExist) {
		return buildError("receipt_write")
	}
	temporary, err := os.CreateTemp(root, ".rencrow-build-receipt-*.tmp")
	if err != nil {
		return buildError("receipt_write")
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	writeErr := func() error {
		if err := temporary.Chmod(0o600); err != nil {
			return err
		}
		if _, err := temporary.Write(encoded); err != nil {
			return err
		}
		return temporary.Sync()
	}()
	if writeErr != nil {
		_ = temporary.Close()
		return buildError("receipt_write")
	}
	if err := temporary.Close(); err != nil {
		return buildError("receipt_write")
	}
	if err := os.Rename(temporaryName, absolute); err != nil {
		return buildError("receipt_write")
	}
	if err := syncBuildOutputDirectories(root); err != nil {
		return buildError("receipt_write")
	}
	info, err := os.Lstat(absolute)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		return buildError("receipt_write")
	}
	return nil
}

func safeBuildRootForReceipt(root string) bool {
	absolute, err := absolutePath(root)
	if err != nil {
		return false
	}
	info, err := os.Lstat(absolute)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		return false
	}
	realRoot, err := filepath.EvalSymlinks(absolute)
	if err != nil || !samePath(absolute, filepath.Clean(realRoot)) {
		return false
	}
	parent := filepath.Dir(absolute)
	parentInfo, err := os.Lstat(parent)
	if err != nil || parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return false
	}
	realParent, err := filepath.EvalSymlinks(parent)
	return err == nil && samePath(parent, filepath.Clean(realParent))
}

func verifyBuiltOutput(ctx context.Context, prepared preparedBuild, receipt BuildReceipt) error {
	if ctx == nil {
		return errors.New("build verification context is invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root := prepared.paths.buildDir
	if !safeBuildRootForReceipt(root) {
		return errors.New("build root is invalid")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 5 {
		return errors.New("build root entry set is invalid")
	}
	wantNames := map[string]struct{}{
		BuildReceiptFilename: {}, buildOutputDCIFilename: {}, buildOutputEventStoreFilename: {}, buildOutputL1Filename: {}, buildOutputArchiveFilename: {},
	}
	for _, entry := range entries {
		if _, ok := wantNames[entry.Name()]; !ok {
			return errors.New("build root contains an unexpected entry")
		}
	}
	targets := buildOutputTargets(root)
	files := make(map[string]buildOutputFile, len(targets))
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := os.Lstat(target.path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
			return errors.New("build output is invalid")
		}
		if err := rejectCapturedSQLiteSidecars(target.path); err != nil {
			return errors.New("build output sidecar is invalid")
		}
		target.sha256, target.bytes, err = hashBuildFile(target.path)
		if err != nil {
			return errors.New("build output hash is invalid")
		}
		artifact, ok := receipt.OutputArtifacts[target.role]
		if !ok || artifact.FileSHA256 != target.sha256 || artifact.Bytes != target.bytes {
			return errors.New("build output hash differs from receipt")
		}
		target.quickCheckOK = artifact.QuickCheckOK
		target.sidecarZero = artifact.SidecarZero
		files[target.role] = target
	}
	if buildOutputArtifactSetSHA256(files) != receipt.OutputArtifactSetSHA256 {
		return errors.New("build output artifact set differs from receipt")
	}
	receiptData, err := readBuildInputBytes(filepath.Join(root, BuildReceiptFilename), maxBuildReceiptBytes)
	if err != nil {
		return errors.New("build receipt cannot be read")
	}
	var diskReceipt BuildReceipt
	if err := decodeOneBuildInputObject(receiptData, &diskReceipt); err != nil || !reflect.DeepEqual(diskReceipt, receipt) {
		return errors.New("build receipt differs from result")
	}
	if err := verifyBuildOutputInputs(ctx, prepared); err != nil {
		return err
	}
	return ctx.Err()
}
