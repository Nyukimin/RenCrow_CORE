package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var verifierRevisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type catalogCoreIdentity struct {
	Repository    string
	WorkspacePath string
	Version       string
}

type goBuildStamp struct {
	MainPackage string
	Module      string
	Revision    string
	Modified    *bool
}

func runDeployIdentityChain(ctx context.Context, options verifierOptions, _ manifestCheck, deps verifierDependencies) verifierOutcome {
	deps = normalizeVerifierDependencies(deps)
	catalogPath := strings.TrimSpace(options.CatalogPath)
	workspacePath := strings.TrimSpace(options.WorkspacePath)
	artifactPath := strings.TrimSpace(options.InstalledArtifact)
	if catalogPath == "" {
		workingDirectory, err := os.Getwd()
		if err == nil {
			catalogPath = filepath.Join(workingDirectory, "ecosystem.yaml")
		}
	}
	catalog, err := loadCatalogCoreIdentity(catalogPath)
	if err != nil {
		return verifierOutcome{Status: "blocked", FailureBoundary: "ecosystem catalog evidence is unavailable"}
	}
	if workspacePath == "" {
		workspacePath = resolveCatalogWorkspace(catalogPath, catalog.WorkspacePath)
	}
	if artifactPath == "" {
		snapshot, outcome := readSystemdService(ctx, options, deps)
		if outcome.Status != "" {
			return verifierOutcome{Status: "blocked", FailureBoundary: "installed CORE artifact evidence is unavailable"}
		}
		artifactPath = snapshot.ExecPath
	}
	workspaceInfo, err := os.Lstat(workspacePath)
	if err != nil || workspaceInfo.Mode()&os.ModeSymlink != 0 || !workspaceInfo.IsDir() {
		return verifierOutcome{Status: "blocked", FailureBoundary: "CORE workspace evidence is unavailable"}
	}
	if !sameCleanPath(resolveCatalogWorkspace(catalogPath, catalog.WorkspacePath), workspacePath) {
		return verifierOutcome{Status: "failed", FailureBoundary: "supplied CORE workspace does not match catalog workspace identity"}
	}
	artifactInfo, err := os.Lstat(artifactPath)
	if err != nil || artifactInfo.Mode()&os.ModeSymlink != 0 || !artifactInfo.Mode().IsRegular() {
		return verifierOutcome{Status: "blocked", FailureBoundary: "installed CORE artifact evidence is unavailable"}
	}
	gitRevision, sourceOutcome := readSourceIdentity(ctx, workspacePath, catalog.Version, deps)
	if sourceOutcome.Status != "" {
		return sourceOutcome
	}
	dirtyOutcome := readSourceCleanliness(ctx, workspacePath, deps)
	if dirtyOutcome.Status != "" {
		return dirtyOutcome
	}
	artifactHash, artifactSize, err := sha256File(artifactPath)
	if err != nil {
		return verifierOutcome{Status: "blocked", FailureBoundary: "installed CORE artifact cannot be hashed"}
	}
	stampResult := deps.RunCommand(ctx, "go", []string{"version", "-m", artifactPath})
	if stampResult.ExitCode < 0 {
		return verifierOutcome{Status: "blocked", FailureBoundary: "Go build stamp observation is unavailable", Evidence: map[string]any{
			"catalog_repository":  catalog.Repository,
			"source_revision":     gitRevision,
			"artifact_sha256":     artifactHash,
			"artifact_size_bytes": artifactSize,
		}}
	}
	if stampResult.ExitCode != 0 {
		return verifierOutcome{Status: "unverified", FailureBoundary: "installed CORE artifact has no readable Go build stamp", Evidence: map[string]any{
			"catalog_repository":  catalog.Repository,
			"source_revision":     gitRevision,
			"artifact_sha256":     artifactHash,
			"artifact_size_bytes": artifactSize,
		}}
	}
	stamp, stamped := parseGoBuildStamp(stampResult.Stdout)
	evidence := map[string]any{
		"catalog_repository":     catalog.Repository,
		"catalog_workspace_path": catalog.WorkspacePath,
		"source_revision":        gitRevision,
		"artifact_name":          safeBase(artifactPath),
		"artifact_sha256":        artifactHash,
		"artifact_size_bytes":    artifactSize,
		"stamp_main_package":     stamp.MainPackage,
		"stamp_module":           stamp.Module,
		"stamp_revision":         stamp.Revision,
	}
	if !stamped {
		return verifierOutcome{Status: "unverified", FailureBoundary: "installed CORE artifact has no complete Go build stamp", Evidence: evidence}
	}
	if stamp.Module == "" || !sameModulePath(stamp.Module, "github.com/Nyukimin/RenCrow_CORE") {
		return verifierOutcome{Status: "failed", FailureBoundary: "installed CORE artifact module stamp is not RenCrow_CORE", Evidence: evidence}
	}
	if stamp.Revision != catalog.Version || stamp.Revision != gitRevision {
		return verifierOutcome{Status: "failed", FailureBoundary: "source, artifact, and catalog revisions do not match", Evidence: evidence}
	}
	if stamp.Modified == nil {
		return verifierOutcome{Status: "unverified", FailureBoundary: "installed CORE artifact dirty-state stamp is missing", Evidence: evidence}
	}
	if *stamp.Modified {
		return verifierOutcome{Status: "failed", FailureBoundary: "installed CORE artifact was built from modified source", Evidence: evidence}
	}
	checkerOptions := options
	checkerOptions.CatalogPath = catalogPath
	checkerOptions.WorkspacePath = filepath.Dir(catalogPath)
	checkerOptions.InstalledArtifact = artifactPath
	if strings.TrimSpace(checkerOptions.StampedChecker) == "" {
		checkerOptions.StampedChecker = discoverStampedChecker(catalogPath)
	}
	if checkerOptions.StampedChecker != "" {
		checkerOutcome := runStampedDeploymentChecker(ctx, checkerOptions, deps)
		if checkerOutcome.Status != "passed" {
			return checkerOutcome
		}
		evidence = mergeEvidence(evidence, checkerOutcome.Evidence)
	}
	return verifierOutcome{Status: "passed", Evidence: evidence}
}

func discoverStampedChecker(catalogPath string) string {
	candidate := filepath.Join(filepath.Dir(strings.TrimSpace(catalogPath)), "scripts", "check_deployed_binaries.py")
	info, err := os.Lstat(candidate)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ""
	}
	return candidate
}

func loadCatalogCoreIdentity(path string) (catalogCoreIdentity, error) {
	raw, err := readRegularFile(path, maxVerifierFileBytes)
	if err != nil {
		return catalogCoreIdentity{}, err
	}
	var document map[string]any
	if err := decodeStrictJSON(raw, &document); err != nil || document == nil {
		return catalogCoreIdentity{}, errors.New("ecosystem catalog must be a JSON object")
	}
	components, ok := document["components"].(map[string]any)
	if !ok {
		return catalogCoreIdentity{}, errors.New("ecosystem catalog components are missing")
	}
	core, ok := components["core"].(map[string]any)
	if !ok {
		return catalogCoreIdentity{}, errors.New("ecosystem catalog core component is missing")
	}
	readString := func(key string) (string, error) {
		raw, ok := core[key]
		value, stringOK := raw.(string)
		value = strings.TrimSpace(value)
		if !ok || !stringOK || value == "" {
			return "", fmt.Errorf("catalog core.%s is required", key)
		}
		return value, nil
	}
	repository, err := readString("repository")
	if err != nil {
		return catalogCoreIdentity{}, err
	}
	workspacePath, err := readString("workspace_path")
	if err != nil {
		return catalogCoreIdentity{}, err
	}
	version, err := readString("version")
	if err != nil {
		return catalogCoreIdentity{}, err
	}
	if repository != "Nyukimin/RenCrow_CORE" {
		return catalogCoreIdentity{}, errors.New("catalog core.repository is not RenCrow_CORE")
	}
	if !verifierRevisionPattern.MatchString(version) {
		return catalogCoreIdentity{}, errors.New("catalog core.version must be a full lowercase Git SHA")
	}
	return catalogCoreIdentity{Repository: repository, WorkspacePath: workspacePath, Version: version}, nil
}

func resolveCatalogWorkspace(catalogPath, workspacePath string) string {
	workspacePath = strings.TrimSpace(workspacePath)
	if filepath.IsAbs(workspacePath) {
		return filepath.Clean(workspacePath)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(catalogPath), workspacePath))
}

func readSourceIdentity(ctx context.Context, workspace, expected string, deps verifierDependencies) (string, verifierOutcome) {
	result := deps.RunCommand(ctx, "git", []string{"-C", workspace, "rev-parse", "HEAD"})
	if commandUnavailable(result) {
		return "", verifierOutcome{Status: "blocked", FailureBoundary: "CORE source revision observation is unavailable"}
	}
	revision := strings.TrimSpace(result.Stdout)
	if !verifierRevisionPattern.MatchString(revision) {
		return "", verifierOutcome{Status: "blocked", FailureBoundary: "CORE source revision is not a full Git SHA"}
	}
	if revision != expected {
		return revision, verifierOutcome{Status: "failed", FailureBoundary: "CORE workspace revision does not match catalog pin", Evidence: map[string]any{"source_revision": revision}}
	}
	return revision, verifierOutcome{}
}

func readSourceCleanliness(ctx context.Context, workspace string, deps verifierDependencies) verifierOutcome {
	result := deps.RunCommand(ctx, "git", []string{"-C", workspace, "status", "--porcelain", "--untracked-files=all"})
	if commandUnavailable(result) {
		return verifierOutcome{Status: "blocked", FailureBoundary: "CORE source cleanliness observation is unavailable"}
	}
	if strings.TrimSpace(result.Stdout) != "" {
		return verifierOutcome{Status: "failed", FailureBoundary: "CORE workspace has uncommitted or untracked changes"}
	}
	return verifierOutcome{}
}

func parseGoBuildStamp(output string) (goBuildStamp, bool) {
	stamp := goBuildStamp{}
	hasRevision := false
	hasModified := false
	for _, line := range strings.Split(output, "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		key := strings.TrimSpace(parts[1])
		value := strings.TrimSpace(parts[2])
		switch key {
		case "path":
			stamp.MainPackage = value
		case "mod":
			fields := strings.Fields(value)
			if len(fields) > 0 {
				stamp.Module = fields[0]
			}
		case "build":
			switch {
			case strings.HasPrefix(value, "vcs.revision="):
				stamp.Revision = strings.TrimSpace(strings.TrimPrefix(value, "vcs.revision="))
				hasRevision = stamp.Revision != ""
			case strings.HasPrefix(value, "vcs.modified="):
				raw := strings.TrimSpace(strings.TrimPrefix(value, "vcs.modified="))
				modified := raw == "true"
				if raw == "true" || raw == "false" {
					stamp.Modified = &modified
					hasModified = true
				}
			}
		}
	}
	return stamp, hasRevision && hasModified
}

func sameModulePath(module, root string) bool {
	module = strings.TrimSpace(module)
	root = strings.TrimSpace(root)
	return module == root || strings.HasPrefix(module, root+"/")
}

func runStampedDeploymentChecker(ctx context.Context, options verifierOptions, deps verifierDependencies) verifierOutcome {
	checker := strings.TrimSpace(options.StampedChecker)
	if !isStampedCheckerPath(checker) {
		return verifierOutcome{Status: "blocked", FailureBoundary: "stamped deployment checker must be check_deployed_binaries.py"}
	}
	info, err := os.Lstat(checker)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return verifierOutcome{Status: "blocked", FailureBoundary: "stamped deployment checker is unavailable"}
	}
	python := strings.TrimSpace(options.Python)
	if python == "" {
		for _, candidate := range []string{"python3", "python"} {
			if resolved, lookErr := deps.LookPath(candidate); lookErr == nil && strings.TrimSpace(resolved) != "" {
				python = resolved
				break
			}
		}
	} else if !isPythonCommand(python) {
		return verifierOutcome{Status: "blocked", FailureBoundary: "stamped deployment checker requires the fixed Python executable"}
	}
	if python == "" {
		return verifierOutcome{Status: "blocked", FailureBoundary: "Python is unavailable for the stamped deployment checker"}
	}
	result := deps.RunCommand(ctx, python, []string{checker, options.CatalogPath, "--workspace", options.WorkspacePath, "--json", "--only", "core"})
	if result.Err != nil && strings.TrimSpace(result.Stdout) == "" {
		return verifierOutcome{Status: "blocked", FailureBoundary: "stamped deployment checker could not run"}
	}
	var rows []map[string]any
	if err := decodeStrictJSON([]byte(result.Stdout), &rows); err != nil || len(rows) == 0 {
		return verifierOutcome{Status: "blocked", FailureBoundary: "stamped deployment checker returned invalid evidence"}
	}
	for _, row := range rows {
		component, _ := row["component"].(string)
		if component != "core" {
			continue
		}
		if _, hasObservedAt := row["observed_at"]; hasObservedAt {
			if raw, ok := row["observed_at"].(string); !ok {
				return verifierOutcome{Status: "blocked", FailureBoundary: "stamped deployment evidence observed_at is invalid"}
			} else if observedAt, parseErr := parseVerifierObservedAt(raw); parseErr != nil {
				return verifierOutcome{Status: "blocked", FailureBoundary: "stamped deployment evidence observed_at is invalid"}
			} else if freshnessErr := validateVerifierEvidenceTime(observedAt, options.ObservedAt); freshnessErr != nil {
				return verifierOutcome{Status: "blocked", FailureBoundary: freshnessErr.Error()}
			}
		}
		status, _ := row["status"].(string)
		evidence := map[string]any{"stamped_checker": safeBase(checker), "stamped_status": status}
		switch status {
		case "MATCH":
			// The checker reports every deployed component and its process exit
			// status is aggregate. A non-zero exit can therefore be caused by an
			// unrelated component even when CORE itself matches its pin. The
			// component row is the authoritative result for this CORE check.
			return verifierOutcome{Status: "passed", Evidence: evidence}
		case "DIRTY", "MISMATCH":
			return verifierOutcome{Status: "failed", FailureBoundary: "stamped deployment checker reported publication drift", Evidence: evidence}
		case "UNSTAMPED":
			return verifierOutcome{Status: "unverified", FailureBoundary: "stamped deployment checker reported an unstamped artifact", Evidence: evidence}
		default:
			return verifierOutcome{Status: "blocked", FailureBoundary: "stamped deployment checker returned an unsupported status", Evidence: evidence}
		}
	}
	return verifierOutcome{Status: "blocked", FailureBoundary: "stamped deployment checker returned no CORE row"}
}

func isStampedCheckerPath(path string) bool {
	return filepath.Base(strings.TrimSpace(path)) == "check_deployed_binaries.py"
}

func isPythonCommand(path string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(path)))
	return base == "python" || base == "python3" || base == "python.exe" || base == "python3.exe"
}
