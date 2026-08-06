package policybundle

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policybundle"
	"gopkg.in/yaml.v3"
)

var requiredPolicyPaths = []string{
	"authorizations.yaml",
	"capabilities.yaml",
	"data-handling.yaml",
	"deployment/development.yaml",
	"deployment/production.yaml",
	"external-actions.yaml",
	"global.yaml",
}

type loadResult struct {
	status   domainpolicy.Status
	snapshot domainpolicy.Snapshot
}

func LoadWorkspace(workspaceDir string) domainpolicy.Status {
	result, status, _ := loadWorkspace(workspaceDir)
	if result.status.State != "" {
		return result.status
	}
	return status
}

func loadWorkspace(workspaceDir string) (loadResult, domainpolicy.Status, error) {
	root := filepath.Join(workspaceDir, "policies")
	status := domainpolicy.Status{
		State:                domainpolicy.StateMissing,
		PolicyRoot:           root,
		ContractRevision:     domainpolicy.ContractRevision,
		DeploymentProfile:    "production",
		DisabledCapabilities: []string{},
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			status.LastReloadState = domainpolicy.StateMissing
			return loadResult{}, status, fmt.Errorf("policy bundle is missing")
		}
		status.State = domainpolicy.StateInvalid
		status.Error = fmt.Sprintf("cannot inspect policy root: %v", err)
		status.LastReloadState = domainpolicy.StateInvalid
		return loadResult{}, status, fmt.Errorf("%s", status.Error)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		status.State = domainpolicy.StateInvalid
		status.Error = "policy root must be a real directory, not a symlink"
		status.LastReloadState = domainpolicy.StateInvalid
		return loadResult{}, status, fmt.Errorf("%s", status.Error)
	}

	loaded, err := load(root)
	if err != nil {
		status.State = domainpolicy.StateInvalid
		status.Error = err.Error()
		status.LastReloadState = domainpolicy.StateInvalid
		return loadResult{}, status, err
	}
	loaded.status.LastReloadState = domainpolicy.StateActive
	return loaded, loaded.status, nil
}

func load(root string) (loadResult, error) {
	manifestPath := filepath.Join(root, "manifest.yaml")
	manifestBytes, err := readRegularPolicyFile(root, "manifest.yaml")
	if err != nil {
		return loadResult{}, err
	}
	var manifest domainpolicy.Manifest
	if err := decodeStrict(manifestPath, manifestBytes, &manifest); err != nil {
		return loadResult{}, err
	}
	if err := validateManifest(manifest); err != nil {
		return loadResult{}, err
	}

	entries := make(map[string]domainpolicy.FileEntry, len(manifest.Files))
	for _, entry := range manifest.Files {
		if err := validateRelativePath(entry.Path); err != nil {
			return loadResult{}, fmt.Errorf("manifest file path %q: %w", entry.Path, err)
		}
		if _, exists := entries[entry.Path]; exists {
			return loadResult{}, fmt.Errorf("duplicate manifest file path: %s", entry.Path)
		}
		entries[entry.Path] = entry
	}
	if err := validateRequiredPaths(entries); err != nil {
		return loadResult{}, err
	}
	if got := contentHash(manifest.Files); got != strings.ToLower(manifest.ContentSHA256) {
		return loadResult{}, fmt.Errorf("bundle content hash mismatch")
	}

	contents := make(map[string][]byte, len(entries))
	for path, entry := range entries {
		data, err := readRegularPolicyFile(root, path)
		if err != nil {
			return loadResult{}, err
		}
		got := fmt.Sprintf("%x", sha256.Sum256(data))
		if got != strings.ToLower(entry.SHA256) {
			return loadResult{}, fmt.Errorf("policy file hash mismatch: %s", path)
		}
		contents[path] = data
	}

	policyIDs := map[string]string{}
	registerID := func(path, id string) error {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("%s policy_id must be non-empty", path)
		}
		if previous, exists := policyIDs[id]; exists {
			return fmt.Errorf("duplicate policy_id %q in %s and %s", id, previous, path)
		}
		policyIDs[id] = path
		return nil
	}

	var global domainpolicy.GlobalPolicy
	if err := decodePolicy(contents, "global.yaml", &global); err != nil {
		return loadResult{}, err
	}
	if global.DefaultSideEffect != "blocked" {
		return loadResult{}, fmt.Errorf("global.yaml default_side_effect must be blocked")
	}
	if err := registerID("global.yaml", global.PolicyID); err != nil {
		return loadResult{}, err
	}

	var capabilities domainpolicy.CapabilityPolicy
	if err := decodePolicy(contents, "capabilities.yaml", &capabilities); err != nil {
		return loadResult{}, err
	}
	if err := registerID("capabilities.yaml", capabilities.PolicyID); err != nil {
		return loadResult{}, err
	}

	var authorizations domainpolicy.AuthorizationPolicy
	if err := decodePolicy(contents, "authorizations.yaml", &authorizations); err != nil {
		return loadResult{}, err
	}
	if err := registerID("authorizations.yaml", authorizations.PolicyID); err != nil {
		return loadResult{}, err
	}
	if err := validateAuthorizations(authorizations.Authorizations); err != nil {
		return loadResult{}, err
	}

	var dataHandling domainpolicy.DataHandlingPolicy
	if err := decodePolicy(contents, "data-handling.yaml", &dataHandling); err != nil {
		return loadResult{}, err
	}
	if err := registerID("data-handling.yaml", dataHandling.PolicyID); err != nil {
		return loadResult{}, err
	}

	var external domainpolicy.ExternalActionPolicy
	if err := decodePolicy(contents, "external-actions.yaml", &external); err != nil {
		return loadResult{}, err
	}
	if err := registerID("external-actions.yaml", external.PolicyID); err != nil {
		return loadResult{}, err
	}
	for action, decision := range external.Actions {
		if strings.TrimSpace(action) == "" || (decision != "blocked" && decision != "explicit_authorization") {
			return loadResult{}, fmt.Errorf("external-actions.yaml action %q has invalid decision", action)
		}
	}

	deployments := map[string]domainpolicy.DeploymentPolicy{}
	for _, profile := range []string{"development", "production"} {
		path := "deployment/" + profile + ".yaml"
		var deployment domainpolicy.DeploymentPolicy
		if err := decodePolicy(contents, path, &deployment); err != nil {
			return loadResult{}, err
		}
		if deployment.Profile != profile {
			return loadResult{}, fmt.Errorf("%s profile must be %s", path, profile)
		}
		if err := registerID(path, deployment.PolicyID); err != nil {
			return loadResult{}, err
		}
		deployments[profile] = deployment
	}

	disabled := map[string]struct{}{}
	for capability, allowed := range capabilities.Capabilities {
		if strings.TrimSpace(capability) == "" {
			return loadResult{}, fmt.Errorf("capabilities.yaml has empty capability")
		}
		if !allowed {
			disabled[capability] = struct{}{}
		}
	}
	for _, capability := range deployments["production"].DisabledCapabilities {
		if strings.TrimSpace(capability) == "" {
			return loadResult{}, fmt.Errorf("production disabled capability must be non-empty")
		}
		disabled[capability] = struct{}{}
	}
	disabledList := make([]string, 0, len(disabled))
	for capability := range disabled {
		disabledList = append(disabledList, capability)
	}
	sort.Strings(disabledList)

	status := domainpolicy.Status{
		State:                domainpolicy.StateActive,
		PolicyRoot:           root,
		ContractRevision:     domainpolicy.ContractRevision,
		BundleID:             manifest.BundleID,
		BundleRevision:       manifest.Revision,
		ContentSHA256:        strings.ToLower(manifest.ContentSHA256),
		MinimumCoreContract:  manifest.MinimumCoreContract,
		DeploymentProfile:    "production",
		DisabledCapabilities: disabledList,
	}
	return loadResult{
		status: status,
		snapshot: domainpolicy.Snapshot{
			BundleID:           manifest.BundleID,
			BundleRevision:     manifest.Revision,
			ContentSHA256:      strings.ToLower(manifest.ContentSHA256),
			Capabilities:       cloneBoolMap(capabilities.Capabilities),
			ExternalActions:    cloneStringMap(external.Actions),
			ProductionDisabled: boolSet(deployments["production"].DisabledCapabilities),
		},
	}, nil
}

func cloneBoolMap(source map[string]bool) map[string]bool {
	cloned := make(map[string]bool, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func cloneStringMap(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func boolSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func validateManifest(manifest domainpolicy.Manifest) error {
	if manifest.SchemaVersion != domainpolicy.SchemaVersion {
		return fmt.Errorf("manifest schema_version must be %d", domainpolicy.SchemaVersion)
	}
	if strings.TrimSpace(manifest.BundleID) == "" || strings.TrimSpace(manifest.Revision) == "" {
		return fmt.Errorf("manifest bundle_id and revision must be non-empty")
	}
	if _, err := time.Parse(time.RFC3339, manifest.CreatedAt); err != nil {
		return fmt.Errorf("manifest created_at must be RFC3339")
	}
	if manifest.MinimumCoreContract != domainpolicy.ContractRevision {
		return fmt.Errorf("minimum_core_contract %q is incompatible with %s", manifest.MinimumCoreContract, domainpolicy.ContractRevision)
	}
	if !isSHA256(manifest.ContentSHA256) {
		return fmt.Errorf("manifest content_sha256 must be SHA-256")
	}
	if len(manifest.Files) == 0 {
		return fmt.Errorf("manifest files must be non-empty")
	}
	for _, entry := range manifest.Files {
		if !isSHA256(entry.SHA256) {
			return fmt.Errorf("manifest sha256 is invalid for %s", entry.Path)
		}
	}
	return nil
}

func decodePolicy(contents map[string][]byte, path string, target any) error {
	if err := decodeStrict(path, contents[path], target); err != nil {
		return err
	}
	value := reflect.Indirect(reflect.ValueOf(target))
	field := value.FieldByName("SchemaVersion")
	if !field.IsValid() || int(field.Int()) != domainpolicy.SchemaVersion {
		return fmt.Errorf("%s schema_version must be %d", path, domainpolicy.SchemaVersion)
	}
	return nil
}

func decodeStrict(path string, data []byte, target any) error {
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid policy YAML %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("invalid policy YAML %s: multiple documents", path)
		}
		return fmt.Errorf("invalid policy YAML %s: %w", path, err)
	}
	return nil
}

func validateAuthorizations(items []domainpolicy.Authorization) error {
	seen := map[string]struct{}{}
	for _, item := range items {
		if strings.TrimSpace(item.ID) == "" {
			return fmt.Errorf("authorization id must be non-empty")
		}
		if _, exists := seen[item.ID]; exists {
			return fmt.Errorf("duplicate authorization id: %s", item.ID)
		}
		seen[item.ID] = struct{}{}
		if len(item.Capabilities) == 0 {
			return fmt.Errorf("authorization %s capabilities must be non-empty", item.ID)
		}
	}
	return nil
}

func validateRequiredPaths(entries map[string]domainpolicy.FileEntry) error {
	if len(entries) != len(requiredPolicyPaths) {
		return fmt.Errorf("manifest must contain exactly %d policy files", len(requiredPolicyPaths))
	}
	for _, path := range requiredPolicyPaths {
		if _, exists := entries[path]; !exists {
			return fmt.Errorf("manifest is missing required policy file: %s", path)
		}
	}
	return nil
}

func contentHash(entries []domainpolicy.FileEntry) string {
	ordered := append([]domainpolicy.FileEntry(nil), entries...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Path < ordered[j].Path })
	var input strings.Builder
	for _, entry := range ordered {
		input.WriteString(entry.Path)
		input.WriteByte('\n')
		input.WriteString(strings.ToLower(entry.SHA256))
		input.WriteByte('\n')
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(input.String())))
}

func validateRelativePath(path string) error {
	if path == "" || filepath.IsAbs(path) || filepath.ToSlash(path) != path {
		return fmt.Errorf("must be a portable relative path")
	}
	if filepath.Clean(path) != path || strings.HasPrefix(path, "../") || path == ".." {
		return fmt.Errorf("must not escape policy root")
	}
	return nil
}

func readRegularPolicyFile(root, relative string) ([]byte, error) {
	if err := validateRelativePath(relative); err != nil {
		return nil, fmt.Errorf("invalid policy path %q: %w", relative, err)
	}
	if err := ensureNoSymlinkSegments(root, relative); err != nil {
		return nil, err
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("cannot inspect policy file %s: %w", relative, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("policy file must not be a symlink: %s", relative)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("policy file must be regular: %s", relative)
	}
	if hasMultipleLinks(info) {
		return nil, fmt.Errorf("policy file must not be a hard link: %s", relative)
	}
	return os.ReadFile(path)
}

func ensureNoSymlinkSegments(root, relative string) error {
	current := root
	for _, part := range strings.Split(filepath.FromSlash(relative), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("cannot inspect policy path %s: %w", relative, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("policy path must not contain a symlink: %s", relative)
		}
	}
	return nil
}

func hasMultipleLinks(info os.FileInfo) bool {
	value := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return false
	}
	field := value.FieldByName("Nlink")
	if !field.IsValid() {
		return false
	}
	switch field.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return field.Uint() > 1
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return field.Int() > 1
	default:
		return false
	}
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, char := range strings.ToLower(value) {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}
