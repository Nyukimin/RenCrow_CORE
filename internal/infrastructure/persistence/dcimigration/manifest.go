package dcimigration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	requiredDatabaseLogicalHashKeys = []string{"source_dci", "source_event_store", "source_l1", "source_archive"}
	requiredSchemaHashKeys          = []string{"source_dci", "source_event_store", "source_l1", "source_archive"}
	requiredClassificationHashKeys  = []string{"source_dci", "source_dci_jsonl", "source_event_store", "source_l1", "source_archive"}
	requiredFileHashKeys            = []string{"source_dci_jsonl"}
	requiredNonDCILogicalHashKeys   = []string{"source_event_store", "source_l1", "source_archive"}
)

func writeManifest(path string, manifest Manifest) error {
	if err := validateManifest(manifest); err != nil {
		return newCodedError("manifest_write", "validate DCI migration manifest: %v", err)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return newCodedError("manifest_write", "encode DCI migration manifest: %v", err)
	}
	if int64(len(encoded))+1 > maxManifestBytes {
		return newCodedError("manifest_write", "DCI migration manifest exceeds the size bound")
	}
	encoded = append(encoded, '\n')
	info, err := os.Lstat(path)
	if err == nil {
		_ = info
		return newCodedError("manifest_write", "manifest path already exists")
	}
	if !os.IsNotExist(err) {
		return newCodedError("manifest_write", "inspect manifest path: %v", err)
	}
	parent := filepath.Dir(path)
	temporary, err := os.CreateTemp(parent, ".rencrow-dci-migrate-*.tmp")
	if err != nil {
		return newCodedError("manifest_write", "create temporary manifest: %v", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return newCodedError("manifest_write", "set manifest permissions: %v", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return newCodedError("manifest_write", "write manifest: %v", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return newCodedError("manifest_write", "sync manifest: %v", err)
	}
	if err := temporary.Close(); err != nil {
		return newCodedError("manifest_write", "close manifest: %v", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return newCodedError("manifest_write", "install manifest: %v", err)
	}
	return nil
}

func validateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != ManifestSchemaVersion || manifest.Mode != ModeDryRun {
		return fmt.Errorf("manifest header is invalid")
	}
	if manifest.Status != StatusReady && manifest.Status != StatusBlocked {
		return fmt.Errorf("manifest status is invalid")
	}
	if manifest.LogicalHashAlgorithm != LogicalHashAlgorithm {
		return fmt.Errorf("manifest logical hash algorithm is invalid")
	}
	if manifest.TextNormalizationAlgorithm != TextNormalizationAlgorithm {
		return fmt.Errorf("manifest text normalization algorithm is invalid")
	}
	if err := validateExpectedCounts(manifest.ExpectedCounts); err != nil {
		return err
	}
	if err := validateManifestCounts(manifest); err != nil {
		return err
	}
	if len(manifest.ExclusionReasonCounts) > maxJSONLRecords || len(manifest.LegacyActorLabelCounts) > maxActorLabels {
		return fmt.Errorf("manifest bounded maps exceed limits")
	}
	for reason, count := range manifest.ExclusionReasonCounts {
		if reason != "legacy_limit_projection" {
			return fmt.Errorf("manifest contains unknown exclusion reason")
		}
		if count < 0 {
			return fmt.Errorf("manifest exclusion counts must be non-negative")
		}
	}
	for label, count := range manifest.LegacyActorLabelCounts {
		if label == "" || len(label) > maxActorLabel || count < 0 {
			return fmt.Errorf("manifest actor label counts are invalid")
		}
	}
	exact := manifest.Status == StatusReady
	if err := validateHashMap(manifest.SourceDatabaseLogicalSHA256, requiredDatabaseLogicalHashKeys, exact, "source_database_logical_sha256"); err != nil {
		return err
	}
	if err := validateHashMap(manifest.SourceSchemaSHA256, requiredSchemaHashKeys, exact, "source_schema_sha256"); err != nil {
		return err
	}
	if err := validateHashMap(manifest.SourceDCIClassificationSHA256, requiredClassificationHashKeys, exact, "source_dci_classification_sha256"); err != nil {
		return err
	}
	if err := validateHashMap(manifest.SourceFileSHA256, requiredFileHashKeys, exact, "source_file_sha256"); err != nil {
		return err
	}
	if err := validateHashMap(manifest.SourceNonDCILogicalSHA256, requiredNonDCILogicalHashKeys, exact, "source_non_dci_logical_sha256"); err != nil {
		return err
	}
	for name, hash := range map[string]string{
		"mapping_sha256":         manifest.MappingSHA256,
		"action_id_set_sha256":   manifest.ActionSetSHA256,
		"trace_id_set_sha256":    manifest.TraceSetSHA256,
		"evidence_id_set_sha256": manifest.EvidenceSetSHA256,
		"event_id_set_sha256":    manifest.EventSetSHA256,
		"event_plan_sha256":      manifest.EventPlanSHA256,
	} {
		if manifest.Status == StatusReady && !isLowerHexSHA256(hash) {
			return fmt.Errorf("manifest %s must be a lowercase SHA-256", name)
		}
		if manifest.Status == StatusBlocked && hash != "" && !isLowerHexSHA256(hash) {
			return fmt.Errorf("manifest %s must be a lowercase SHA-256 when present", name)
		}
	}
	if manifest.PlannedZeroCounters.LegacyKeyZero != 0 || manifest.PlannedZeroCounters.OrphanZero != 0 {
		return fmt.Errorf("manifest planned zero counters must be exactly zero")
	}
	if manifest.Status == StatusReady {
		if manifest.ErrorCode != "" {
			return fmt.Errorf("ready manifest must not contain error_code")
		}
	} else if !validErrorCode(manifest.ErrorCode) {
		return fmt.Errorf("blocked manifest error_code is missing or invalid")
	}
	return nil
}

func validateManifestCounts(manifest Manifest) error {
	source := manifest.SourceCounts
	if source.DCITraces < 0 || source.DCISteps < 0 || source.DCIEvidence < 0 || source.DCIQueryTerms < 0 || source.JSONLTraces < 0 || source.JSONLSteps < 0 || source.CurrentStaging < 0 || source.CurrentDCIStaging < 0 || source.CurrentRegistry < 0 || source.ArchiveStaging < 0 || source.ArchiveDCIStaging < 0 || source.EventStore < 0 {
		return fmt.Errorf("manifest source counts must be non-negative")
	}
	actual := manifest.ActualCounts
	if actual.Searches < 0 || actual.ReadEvents < 0 || actual.EvidenceEvents < 0 || actual.TotalEvents < 0 || actual.LegacyLimitSteps < 0 || actual.NormalizedTextValues < 0 || actual.InvalidUTF8Bytes < 0 {
		return fmt.Errorf("manifest actual counts must be non-negative")
	}
	dedupe := manifest.DedupeCounts
	if dedupe.SearchesRemoved < 0 || dedupe.StepsRemoved < 0 || dedupe.EvidenceRemoved < 0 || dedupe.StagingDuplicates < 0 {
		return fmt.Errorf("manifest dedupe counts must be non-negative")
	}
	if manifest.ActorClassification.AuthenticatedAgent < 0 || manifest.ActorClassification.LegacyUnattributed < 0 {
		return fmt.Errorf("manifest actor counts must be non-negative")
	}
	if manifest.PlannedZeroCounters.LegacyKeyZero < 0 || manifest.PlannedZeroCounters.OrphanZero < 0 {
		return fmt.Errorf("manifest zero counters must be non-negative")
	}
	return nil
}

func validateHashMap(hashes map[string]string, required []string, exact bool, field string) error {
	allowed := make(map[string]struct{}, len(required))
	for _, key := range required {
		allowed[key] = struct{}{}
	}
	if exact && len(hashes) != len(required) {
		return fmt.Errorf("ready manifest must contain exactly %d %s entries", len(required), field)
	}
	for key, hash := range hashes {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("manifest contains unknown %s key", field)
		}
		if !isLowerHexSHA256(hash) {
			return fmt.Errorf("manifest %s values must be lowercase SHA-256", field)
		}
	}
	if exact {
		for _, key := range required {
			if _, ok := hashes[key]; !ok {
				return fmt.Errorf("ready manifest is missing %s key %s", field, key)
			}
		}
	}
	return nil
}

func isLowerHexSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func validErrorCode(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for index, char := range value {
		if index == 0 && (char < 'a' || char > 'z') {
			return false
		}
		if index > 0 && !((char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_') {
			return false
		}
	}
	return !strings.ContainsAny(value, "\r\n")
}
