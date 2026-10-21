package classifierlab

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	PreparedManifestSchemaVersion = "routing-classifier-prepared/v1"
	PreparedPolicyFileName        = "route_policy.json"
	PreparedManifestFileName      = "prepared_manifest.json"
	MaxPreparedArtifactBytes      = 4 << 20
	MaxPreparedModelFiles         = 100000
)

var (
	preparedSHA256Pattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	preparedRevisionPattern      = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	ErrPreparedPolicyUnavailable = errors.New("prepared route policy is unavailable or unverified")
)

// RoutePolicy is the canonical, machine-readable Laya classification policy.
// Its SHA-256 is computed from json.Marshal(RoutePolicy), not file whitespace.
type RoutePolicy struct {
	PolicyVersion        string            `json:"policy_version"`
	Routes               []string          `json:"routes"`
	Instructions         string            `json:"instructions"`
	Criteria             map[string]string `json:"criteria"`
	SourcePaths          []string          `json:"source_paths"`
	LegacyPromptSHA256   string            `json:"legacy_prompt_sha256"`
	SemanticReviewStatus string            `json:"semantic_review_status"`
}

// PreparedManifest describes the exact local model files and isolated Python
// environment expected by the test server. A prepare command should write this
// file beside resolved-config.yaml and route_policy.json.
//
// Example shape:
//
//	{
//	  "schema_version":"routing-classifier-prepared/v1",
//	  "policy_sha256":"<canonical route policy sha256>",
//	  "model":{
//	    "family":"laya-multilingual",
//	    "source_id":"convaiinnovations/laya-multilingual",
//	    "revision":"<40 lowercase hex commit>",
//	    "device":"cpu","dtype":"float32",
//	    "files":[{"path":"config.json","sha256":"<64 lowercase hex>"}],
//	    "files_sha256":"<sha256 of canonical files array>"
//	  },
//	  "sdk":{
//	    "distribution":"laya","version":"<exact version>",
//	    "source_url":"https://github.com/NandhaKishorM/laya",
//	    "lock_file":"requirements.lock",
//	    "lock_sha256":"<64 lowercase hex>",
//	    "packages":{"laya":"<same exact version>","torch":"<exact version>"}
//	  }
//	}
//
// Every regular file under the configured model directory must appear exactly
// once in files, in slash-normalized lexical order. SDK packages contains the
// normalized distribution name and exact version of each installed package in
// the lock. The server hashes the lock file and model tree before starting the
// worker. The worker checks installed package versions before importing Laya.
type PreparedManifest struct {
	SchemaVersion string                `json:"schema_version"`
	PolicySHA256  string                `json:"policy_sha256"`
	Model         PreparedModelManifest `json:"model"`
	SDK           PreparedSDKManifest   `json:"sdk"`
}

type PreparedModelManifest struct {
	Family      string              `json:"family"`
	SourceID    string              `json:"source_id"`
	Revision    string              `json:"revision"`
	Device      string              `json:"device"`
	DType       string              `json:"dtype"`
	Files       []PreparedModelFile `json:"files"`
	FilesSHA256 string              `json:"files_sha256"`
}

type PreparedModelFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type PreparedSDKManifest struct {
	Distribution string            `json:"distribution"`
	Version      string            `json:"version"`
	SourceURL    string            `json:"source_url"`
	LockFile     string            `json:"lock_file"`
	LockSHA256   string            `json:"lock_sha256"`
	Packages     map[string]string `json:"packages"`
}

type PreparedArtifacts struct {
	Policy            RoutePolicy
	Manifest          PreparedManifest
	PolicySHA256      string
	ModelFilesSHA256  string
	SDKLockSHA256     string
	ModelDirectory    string
	ArtifactDirectory string
}

// LoadPreparedArtifacts validates the two sibling prepared artifacts against
// the resolved configuration and the current local model directory.
func LoadPreparedArtifacts(artifactDirectory, modelDirectory, expectedFamily, expectedRevision, expectedDevice, expectedDType, expectedPolicySHA256 string) (PreparedArtifacts, error) {
	if strings.TrimSpace(artifactDirectory) == "" || strings.TrimSpace(modelDirectory) == "" {
		return PreparedArtifacts{}, errors.New("prepared artifact and model directories are required")
	}
	artifactRoot, err := filepath.Abs(artifactDirectory)
	if err != nil {
		return PreparedArtifacts{}, fmt.Errorf("resolve prepared artifact directory: %w", err)
	}
	modelRoot, err := filepath.Abs(modelDirectory)
	if err != nil {
		return PreparedArtifacts{}, fmt.Errorf("resolve model directory: %w", err)
	}
	modelRoot, err = filepath.EvalSymlinks(modelRoot)
	if err != nil {
		return PreparedArtifacts{}, fmt.Errorf("resolve model directory symlinks: %w", err)
	}
	modelRoot, err = filepath.Abs(modelRoot)
	if err != nil {
		return PreparedArtifacts{}, fmt.Errorf("normalize model directory: %w", err)
	}

	policyData, err := readPreparedFile(filepath.Join(artifactRoot, PreparedPolicyFileName), MaxPreparedArtifactBytes)
	if err != nil {
		return PreparedArtifacts{}, fmt.Errorf("%w: read route policy: %v", ErrPreparedPolicyUnavailable, err)
	}
	policy, err := decodePreparedJSON[RoutePolicy](policyData)
	if err != nil {
		return PreparedArtifacts{}, fmt.Errorf("%w: decode route policy: %v", ErrPreparedPolicyUnavailable, err)
	}
	if err := validateRoutePolicy(policy); err != nil {
		return PreparedArtifacts{}, fmt.Errorf("%w: validate route policy: %v", ErrPreparedPolicyUnavailable, err)
	}
	canonicalPolicy, err := json.Marshal(policy)
	if err != nil {
		return PreparedArtifacts{}, fmt.Errorf("canonicalize prepared route policy: %w", err)
	}
	policySHA := sha256Hex(canonicalPolicy)
	if !preparedSHA256Pattern.MatchString(expectedPolicySHA256) || policySHA != expectedPolicySHA256 {
		return PreparedArtifacts{}, fmt.Errorf("%w: canonical route policy SHA-256 does not match resolved config", ErrPreparedPolicyUnavailable)
	}
	if policy.SemanticReviewStatus != "verified" {
		return PreparedArtifacts{}, fmt.Errorf("%w: semantic_review_status is %q, want verified", ErrPreparedPolicyUnavailable, policy.SemanticReviewStatus)
	}

	manifestData, err := readPreparedFile(filepath.Join(artifactRoot, PreparedManifestFileName), MaxPreparedArtifactBytes)
	if err != nil {
		return PreparedArtifacts{}, fmt.Errorf("read prepared manifest: %w", err)
	}
	manifest, err := decodePreparedJSON[PreparedManifest](manifestData)
	if err != nil {
		return PreparedArtifacts{}, fmt.Errorf("decode prepared manifest: %w", err)
	}
	if err := validateManifestShape(manifest); err != nil {
		return PreparedArtifacts{}, fmt.Errorf("validate prepared manifest: %w", err)
	}
	if manifest.PolicySHA256 != policySHA {
		return PreparedArtifacts{}, errors.New("prepared manifest policy SHA-256 does not match canonical route policy")
	}
	if manifest.Model.Family != expectedFamily || manifest.Model.Revision != expectedRevision {
		return PreparedArtifacts{}, errors.New("prepared manifest model family or revision does not match resolved config")
	}
	if manifest.Model.Family != "laya-multilingual" || manifest.Model.SourceID != "convaiinnovations/laya-multilingual" {
		return PreparedArtifacts{}, errors.New("prepared manifest must pin the official laya-multilingual checkpoint")
	}
	if manifest.Model.Device != expectedDevice || manifest.Model.DType != expectedDType {
		return PreparedArtifacts{}, errors.New("prepared manifest model device or dtype does not match resolved config")
	}
	if !validPreparedDevice(manifest.Model.Device) {
		return PreparedArtifacts{}, fmt.Errorf("prepared manifest model device %q is unsupported", manifest.Model.Device)
	}
	if manifest.Model.DType != "float32" && manifest.Model.DType != "float16" && manifest.Model.DType != "bfloat16" {
		return PreparedArtifacts{}, fmt.Errorf("prepared manifest model dtype %q is unsupported", manifest.Model.DType)
	}
	if manifest.SDK.Distribution != "laya" || manifest.SDK.SourceURL != "https://github.com/NandhaKishorM/laya" {
		return PreparedArtifacts{}, errors.New("prepared manifest SDK must come from the official Laya distribution")
	}

	actualFiles, filesSHA, err := hashPreparedModelTree(modelRoot)
	if err != nil {
		return PreparedArtifacts{}, fmt.Errorf("hash prepared model tree: %w", err)
	}
	if !equalPreparedFiles(actualFiles, manifest.Model.Files) || filesSHA != manifest.Model.FilesSHA256 {
		return PreparedArtifacts{}, errors.New("local model file set or SHA-256 does not match prepared manifest")
	}
	lockPath, err := preparedChildPath(artifactRoot, manifest.SDK.LockFile)
	if err != nil {
		return PreparedArtifacts{}, fmt.Errorf("resolve SDK lock file: %w", err)
	}
	lockData, err := readPreparedFile(lockPath, MaxPreparedArtifactBytes)
	if err != nil {
		return PreparedArtifacts{}, fmt.Errorf("read SDK lock file: %w", err)
	}
	lockSHA := sha256Hex(lockData)
	if lockSHA != manifest.SDK.LockSHA256 {
		return PreparedArtifacts{}, errors.New("SDK lock file SHA-256 does not match prepared manifest")
	}

	return PreparedArtifacts{
		Policy: policy, Manifest: manifest, PolicySHA256: policySHA,
		ModelFilesSHA256: filesSHA, SDKLockSHA256: lockSHA,
		ModelDirectory: modelRoot, ArtifactDirectory: artifactRoot,
	}, nil
}

func validateRoutePolicy(policy RoutePolicy) error {
	if policy.PolicyVersion != "rencrow-route-policy-v1" || strings.TrimSpace(policy.Instructions) == "" {
		return errors.New("policy_version or instructions are missing")
	}
	if len(policy.Routes) != len(routeLabels) || len(policy.Criteria) != len(routeLabels) {
		return fmt.Errorf("policy must contain exactly %d routes and criteria", len(routeLabels))
	}
	for index, label := range routeLabels {
		if policy.Routes[index] != string(label) || strings.TrimSpace(policy.Criteria[string(label)]) == "" {
			return fmt.Errorf("policy route and criteria order must match the fixed route contract at %q", label)
		}
	}
	if len(policy.SourcePaths) == 0 {
		return errors.New("source_paths is empty")
	}
	for _, source := range policy.SourcePaths {
		if strings.TrimSpace(source) == "" {
			return errors.New("source_paths contains an empty path")
		}
	}
	if !preparedSHA256Pattern.MatchString(policy.LegacyPromptSHA256) {
		return errors.New("legacy_prompt_sha256 is invalid")
	}
	if strings.TrimSpace(policy.SemanticReviewStatus) == "" {
		return errors.New("semantic_review_status is empty")
	}
	return nil
}

func validateManifestShape(manifest PreparedManifest) error {
	if manifest.SchemaVersion != PreparedManifestSchemaVersion {
		return fmt.Errorf("unsupported schema_version %q", manifest.SchemaVersion)
	}
	if !preparedSHA256Pattern.MatchString(manifest.PolicySHA256) {
		return errors.New("policy_sha256 is invalid")
	}
	if !preparedRevisionPattern.MatchString(manifest.Model.Revision) {
		return errors.New("model revision must be a pinned lowercase commit hash")
	}
	if strings.TrimSpace(manifest.Model.Family) == "" || strings.TrimSpace(manifest.Model.SourceID) == "" {
		return errors.New("model family or source_id is empty")
	}
	if !validPreparedDevice(manifest.Model.Device) {
		return fmt.Errorf("model device %q is unsupported", manifest.Model.Device)
	}
	if manifest.Model.DType != "float32" && manifest.Model.DType != "float16" && manifest.Model.DType != "bfloat16" {
		return fmt.Errorf("model dtype %q is unsupported", manifest.Model.DType)
	}
	if len(manifest.Model.Files) == 0 || len(manifest.Model.Files) > MaxPreparedModelFiles {
		return fmt.Errorf("model files must contain 1 to %d entries", MaxPreparedModelFiles)
	}
	for index, file := range manifest.Model.Files {
		if err := validatePreparedModelFile(file); err != nil {
			return fmt.Errorf("model files[%d]: %w", index, err)
		}
		if index > 0 && manifest.Model.Files[index-1].Path >= file.Path {
			return errors.New("model files must be unique and sorted by path")
		}
	}
	if !preparedSHA256Pattern.MatchString(manifest.Model.FilesSHA256) {
		return errors.New("model files_sha256 is invalid")
	}
	if !validPreparedVersion(manifest.SDK.Version) || manifest.SDK.Version == "main" || manifest.SDK.Version == "latest" {
		return errors.New("SDK version must be an exact pinned version")
	}
	if strings.TrimSpace(manifest.SDK.LockFile) == "" || filepath.IsAbs(manifest.SDK.LockFile) || filepath.VolumeName(manifest.SDK.LockFile) != "" {
		return errors.New("SDK lock_file must be a relative path")
	}
	if !preparedSHA256Pattern.MatchString(manifest.SDK.LockSHA256) {
		return errors.New("SDK lock_sha256 is invalid")
	}
	if len(manifest.SDK.Packages) == 0 {
		return errors.New("SDK packages list is empty")
	}
	for name, version := range manifest.SDK.Packages {
		normalized := normalizePackageName(name)
		if name != normalized || !validPreparedVersion(version) {
			return fmt.Errorf("SDK package %q has a non-normalized name or unpinned version", name)
		}
	}
	if manifest.SDK.Packages["laya"] != manifest.SDK.Version {
		return errors.New("SDK package laya version does not match pinned SDK version")
	}
	return nil
}

func validPreparedDevice(value string) bool {
	if value == "cpu" || value == "mps" {
		return true
	}
	if value == "cuda" {
		return false
	}
	if !strings.HasPrefix(value, "cuda:") {
		return false
	}
	index := strings.TrimPrefix(value, "cuda:")
	if index == "" || strings.TrimLeft(index, "0123456789") != "" {
		return false
	}
	return index == "0" || index[0] != '0'
}

func validatePreparedModelFile(file PreparedModelFile) error {
	clean := path.Clean(file.Path)
	if file.Path == "" || clean != file.Path || clean == "." || strings.HasPrefix(clean, "../") || strings.Contains(clean, `\`) || path.IsAbs(clean) {
		return errors.New("path must be a clean relative slash path")
	}
	if !preparedSHA256Pattern.MatchString(file.SHA256) {
		return errors.New("sha256 is invalid")
	}
	return nil
}

func hashPreparedModelTree(root string) ([]PreparedModelFile, string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, "", err
	}
	if !info.IsDir() {
		return nil, "", errors.New("model_directory is not a directory")
	}
	files := make([]PreparedModelFile, 0, 32)
	err = filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filePath == root {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			targetInfo, err := os.Stat(filePath)
			if err != nil {
				return err
			}
			if !targetInfo.Mode().IsRegular() {
				return fmt.Errorf("model symlink %q does not target a regular file", filePath)
			}
		} else if !info.Mode().IsRegular() {
			return fmt.Errorf("model path %q is not a regular file", filePath)
		}
		relative, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		hash, err := hashFile(filePath)
		if err != nil {
			return err
		}
		files = append(files, PreparedModelFile{Path: filepath.ToSlash(relative), SHA256: hash})
		if len(files) > MaxPreparedModelFiles {
			return fmt.Errorf("model contains more than %d files", MaxPreparedModelFiles)
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	canonical, err := json.Marshal(files)
	if err != nil {
		return nil, "", err
	}
	return files, sha256Hex(canonical), nil
}

func equalPreparedFiles(left, right []PreparedModelFile) bool {
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

func preparedChildPath(root, relative string) (string, error) {
	clean := filepath.Clean(relative)
	if relative == "" || filepath.IsAbs(relative) || filepath.VolumeName(relative) != "" || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path must stay inside the prepared artifact directory")
	}
	joined := filepath.Join(root, clean)
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", err
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path resolves outside the prepared artifact directory")
	}
	return resolved, nil
}

func readPreparedFile(name string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file exceeds %d bytes", maxBytes)
	}
	return data, nil
}

func decodePreparedJSON[T any](data []byte) (T, error) {
	var result T
	if err := rejectPreparedDuplicateJSON(data); err != nil {
		return result, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return result, errors.New("multiple JSON values are not allowed")
		}
		return result, err
	}
	return result, nil
}

func rejectPreparedDuplicateJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := consumePreparedJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON data")
		}
		return err
	}
	return nil
}

func consumePreparedJSONValue(decoder *json.Decoder) error {
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
			if err := consumePreparedJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := consumePreparedJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return errors.New("unexpected JSON delimiter")
	}
}

func hashFile(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validPreparedVersion(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "/\\ ") {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'z' || character == '.' || character == '+' || character == '-') {
			return false
		}
	}
	return value[0] >= '0' && value[0] <= '9'
}

func normalizePackageName(value string) string {
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
