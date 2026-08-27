package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func runL1SnapshotIntegrity(ctx context.Context, options verifierOptions, _ manifestCheck, deps verifierDependencies) verifierOutcome {
	deps = normalizeVerifierDependencies(deps)
	snapshot := strings.TrimSpace(options.SnapshotDir)
	if snapshot == "" {
		return verifierOutcome{Status: "blocked", FailureBoundary: "snapshot input is required for the backup check"}
	}
	info, err := os.Lstat(snapshot)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return verifierOutcome{Status: "blocked", FailureBoundary: "explicit snapshot directory is unavailable"}
	}
	snapshotAt, freshness := validateSnapshotFreshness(snapshot, options.ObservedAt)
	if freshness.Status != "" {
		return freshness
	}
	checker := strings.TrimSpace(options.RestoreCheck)
	if checker == "" {
		name := "rencrow-storage-restore-check"
		if deps.Platform() == "windows" {
			name += ".exe"
		}
		checker, err = deps.LookPath(name)
		if err != nil || strings.TrimSpace(checker) == "" {
			return verifierOutcome{Status: "blocked", FailureBoundary: "rencrow-storage-restore-check is unavailable"}
		}
	}
	if !isRestoreCheckerPath(checker) {
		return verifierOutcome{Status: "blocked", FailureBoundary: "restore checker must be the fixed owner utility"}
	}
	checkerInfo, err := os.Lstat(checker)
	if err != nil || checkerInfo.Mode()&os.ModeSymlink != 0 || !checkerInfo.Mode().IsRegular() {
		return verifierOutcome{Status: "blocked", FailureBoundary: "restore checker is unavailable"}
	}
	result := deps.RunCommand(ctx, checker, []string{snapshot})
	evidence := map[string]any{
		"checker_name":         safeBase(checker),
		"snapshot_name":        safeBase(snapshot),
		"snapshot_observed_at": snapshotAt.Format(time.RFC3339Nano),
		"exit_code":            result.ExitCode,
	}
	if commandUnavailable(result) {
		return verifierOutcome{Status: "failed", FailureBoundary: "snapshot restore integrity check failed", Evidence: evidence}
	}
	evidence["checker_output_bytes"] = len(result.Stdout)
	if strings.TrimSpace(result.Stdout) == "" {
		return verifierOutcome{Status: "failed", FailureBoundary: "snapshot restore checker returned no success evidence", Evidence: evidence}
	}
	return verifierOutcome{Status: "passed", Evidence: evidence}
}

// validateSnapshotFreshness reads the canonical backup manifest timestamp.
// Older/manual snapshots may not carry created_at_jst; in that case the
// manifest file mtime is the only meaningful capture-time signal and is
// checked instead. Either signal must be within the inclusive five-minute
// evidence window.
func validateSnapshotFreshness(snapshot string, observedAt time.Time) (time.Time, verifierOutcome) {
	manifestPath := filepath.Join(snapshot, "manifest.txt")
	info, err := os.Lstat(manifestPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return time.Time{}, verifierOutcome{Status: "blocked", FailureBoundary: "snapshot manifest evidence is unavailable"}
	}
	raw, err := readRegularFile(manifestPath, maxVerifierFileBytes)
	if err != nil {
		return time.Time{}, verifierOutcome{Status: "blocked", FailureBoundary: "snapshot manifest evidence is unavailable"}
	}
	values := make(map[string]string)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if key == "" {
			continue
		}
		if _, exists := values[key]; exists {
			return time.Time{}, verifierOutcome{Status: "blocked", FailureBoundary: "snapshot manifest contains duplicate metadata"}
		}
		values[key] = value
	}

	var snapshotAt time.Time
	if value := values["created_at_jst"]; value != "" {
		jst := time.FixedZone("JST", 9*60*60)
		snapshotAt, err = time.ParseInLocation("20060102-150405", value, jst)
		if err != nil {
			return time.Time{}, verifierOutcome{Status: "blocked", FailureBoundary: fmt.Sprintf("snapshot created_at_jst is invalid: %v", err)}
		}
	} else {
		for _, key := range []string{"observed_at", "observedAt", "created_at_utc", "createdAtUTC", "created_at", "createdAt"} {
			if value := values[key]; value != "" {
				snapshotAt, err = parseVerifierObservedAt(value)
				if err != nil {
					return time.Time{}, verifierOutcome{Status: "blocked", FailureBoundary: fmt.Sprintf("snapshot %s is invalid: %v", key, err)}
				}
				break
			}
		}
		if snapshotAt.IsZero() {
			snapshotAt = info.ModTime().UTC()
		}
	}
	if err := validateVerifierEvidenceTime(snapshotAt, observedAt); err != nil {
		return time.Time{}, verifierOutcome{Status: "blocked", FailureBoundary: err.Error()}
	}
	return snapshotAt.UTC(), verifierOutcome{}
}

func isRestoreCheckerPath(path string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(path)))
	return base == "rencrow-storage-restore-check" || base == "rencrow-storage-restore-check.exe"
}
