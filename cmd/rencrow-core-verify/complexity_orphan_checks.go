package main

import (
	"context"
	"strings"

	complexitypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/complexity"
)

const complexityOrphanCommandID = "core-complexity-identity-orphan"

// runComplexityIdentityOrphan is the owner_cli executor for the Step 14
// guarantee "complexity identity orphan zero". It opens only the explicitly
// supplied complexity database under PRAGMA query_only and counts rows whose
// identity index column is empty, disagrees with its payload, or references a
// parent row that does not exist. There is no live-database discovery and no
// fallback path: an absent owner input is `blocked`, not a pass.
func runComplexityIdentityOrphan(ctx context.Context, options verifierOptions, _ manifestCheck, deps verifierDependencies) verifierOutcome {
	deps = normalizeVerifierDependencies(deps)
	if err := ctx.Err(); err != nil {
		return verifierOutcome{Status: "blocked", FailureBoundary: "complexity orphan observation was cancelled"}
	}
	dbPath := strings.TrimSpace(options.ComplexityDBPath)
	if dbPath == "" {
		return verifierOutcome{Status: "blocked", FailureBoundary: "explicit complexity database path is required"}
	}
	store, err := complexitypersistence.OpenSQLiteStoreReadOnly(ctx, dbPath)
	if err != nil {
		return verifierOutcome{Status: "blocked", FailureBoundary: "complexity database is unavailable for read-only observation"}
	}
	defer store.Close()
	counts, err := store.CountComplexityIdentityOrphans(ctx)
	if err != nil {
		return verifierOutcome{Status: "blocked", FailureBoundary: "complexity identity orphan count is unavailable"}
	}
	orphans := counts.HotspotsMissingScan + counts.HotspotsInvalidIndex +
		counts.EvidenceMissingHotspot + counts.EvidenceInvalidIndex +
		counts.ArtifactsMissingScan + counts.ArtifactsInvalidIndex
	evidence := map[string]any{
		"command_id":                    complexityOrphanCommandID,
		"db_name":                       safeBase(dbPath),
		"scan_events":                   counts.ScanEvents,
		"hotspots":                      counts.Hotspots,
		"evidence":                      counts.Evidence,
		"artifacts":                     counts.Artifacts,
		"hotspots_missing_scan":         counts.HotspotsMissingScan,
		"hotspots_invalid_index":        counts.HotspotsInvalidIndex,
		"evidence_missing_hotspot":      counts.EvidenceMissingHotspot,
		"evidence_invalid_index":        counts.EvidenceInvalidIndex,
		"artifacts_missing_scan":        counts.ArtifactsMissingScan,
		"artifacts_invalid_index":       counts.ArtifactsInvalidIndex,
		"identity_orphans":              orphans,
		"read_only_observation":         true,
		"live_database_write_performed": false,
	}
	if orphans != 0 {
		evidence["failure_shape"] = "identity_orphans_non_zero"
		return verifierOutcome{
			Status:          "failed",
			FailureBoundary: "complexity identity orphan rows are non-zero",
			Evidence:        evidence,
		}
	}
	if counts.ScanEvents+counts.Hotspots+counts.Evidence+counts.Artifacts == 0 {
		evidence["empty_complexity_store"] = true
	}
	return verifierOutcome{Status: "passed", Evidence: evidence}
}
