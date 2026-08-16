package chatgptimport

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
)

const (
	uploadStagingDirName = ".chatgpt-import-staging"
	uploadManifestName   = "manifest.json"
	uploadArtifactName   = "artifact.tar"
	maxUploadStageIDLen  = 128
)

// These names are fixed by the ChatGPT upload storage contract. Callers must
// not choose another directory or file name.
const (
	UploadStagingDirectoryName = uploadStagingDirName
	UploadManifestFileName     = uploadManifestName
	UploadArtifactFileName     = uploadArtifactName
)

var (
	// ErrUploadStageInvalid identifies a malformed configured root or stage ID.
	ErrUploadStageInvalid = errors.New("ChatGPT upload stage input is invalid")
	// ErrUploadStageUnsafe identifies a filesystem entry that is not part of the
	// private, fixed staging layout.
	ErrUploadStageUnsafe = errors.New("ChatGPT upload stage is unsafe")
	// ErrUploadStageRootMissing identifies a configured root that does not yet
	// exist. Reconciliation treats this as an empty, not-yet-created root.
	ErrUploadStageRootMissing = errors.New("ChatGPT upload stage root is missing")
)

// UploadStage owns one private ChatGPT upload copy below the configured raw
// source root. It never owns the caller's source files or any path outside the
// fixed staging namespace.
type UploadStage struct {
	mu        sync.Mutex
	root      string
	namespace string
	stageDir  string
	stageID   string
	closed    bool
}

// CreateUploadStage creates a new stage directory without replacing an
// existing stage. A missing root and reserved namespace are created with
// private permissions; existing unsafe components are rejected.
func CreateUploadStage(rawSourceRoot, stageID string) (*UploadStage, error) {
	if err := validateUploadStageID(stageID); err != nil {
		return nil, err
	}
	root, err := resolveUploadStageRoot(rawSourceRoot, true)
	if err != nil {
		return nil, err
	}
	namespace, err := resolveUploadStagingNamespace(root, true)
	if err != nil {
		return nil, err
	}
	stageDir := filepath.Join(namespace, stageID)
	if err := ensureUploadStageChild(root, namespace, stageDir); err != nil {
		return nil, err
	}
	if err := os.Mkdir(stageDir, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create stage directory: %v", ErrUploadStageUnsafe, err)
	}
	if err := verifyPrivateDirectory(stageDir, "stage directory"); err != nil {
		_ = os.Remove(stageDir)
		return nil, err
	}
	return &UploadStage{root: root, namespace: namespace, stageDir: stageDir, stageID: stageID}, nil
}

// NewUploadStage is an explicit constructor alias for CreateUploadStage.
func NewUploadStage(rawSourceRoot, stageID string) (*UploadStage, error) {
	return CreateUploadStage(rawSourceRoot, stageID)
}

// StageID returns the pathless stage identity.
func (s *UploadStage) StageID() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stageID
}

// Paths returns the private stage directory and its two fixed file paths.
// These paths are intended for the CORE import service; they are not receipts
// or caller-controlled storage paths.
func (s *UploadStage) Paths() (stageRoot, manifestPath, artifactPath string, err error) {
	if s == nil {
		return "", "", "", errors.New("upload stage is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validateUsableLocked(); err != nil {
		return "", "", "", err
	}
	return s.stageDir, filepath.Join(s.stageDir, uploadManifestName), filepath.Join(s.stageDir, uploadArtifactName), nil
}

// StageRoot returns the validated private directory used as the verifier's
// scratch root for this upload.
func (s *UploadStage) StageRoot() (string, error) {
	stageRoot, _, _, err := s.Paths()
	return stageRoot, err
}

// ManifestPath returns the validated fixed manifest path for this upload.
func (s *UploadStage) ManifestPath() (string, error) {
	_, manifestPath, _, err := s.Paths()
	return manifestPath, err
}

// ArtifactPath returns the validated fixed artifact path for this upload.
func (s *UploadStage) ArtifactPath() (string, error) {
	_, _, artifactPath, err := s.Paths()
	return artifactPath, err
}

// importPaths is the package-internal form used by the ChatGPT import
// service. It keeps the service on the validated stage path rather than
// reconstructing paths from request input.
func (s *UploadStage) importPaths() (stageRoot, manifestPath, artifactPath string, err error) {
	return s.Paths()
}

// CreateManifest opens manifest.json for exclusive creation.
func (s *UploadStage) CreateManifest() (*os.File, error) {
	return s.createFile(uploadManifestName)
}

// CreateArtifact opens artifact.tar for exclusive creation.
func (s *UploadStage) CreateArtifact() (*os.File, error) {
	return s.createFile(uploadArtifactName)
}

// OpenManifest opens an already-created manifest.json after validating its
// private regular-file identity.
func (s *UploadStage) OpenManifest() (*os.File, error) {
	return s.openFile(uploadManifestName)
}

// OpenArtifact opens an already-created artifact.tar after validating its
// private regular-file identity.
func (s *UploadStage) OpenArtifact() (*os.File, error) {
	return s.openFile(uploadArtifactName)
}

func (s *UploadStage) createFile(name string) (*os.File, error) {
	if !isUploadStageFile(name) {
		return nil, fmt.Errorf("%w: unknown stage file", ErrUploadStageInvalid)
	}
	if s == nil {
		return nil, errors.New("upload stage is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validateUsableLocked(); err != nil {
		return nil, err
	}
	if err := validateUploadStageEntries(s.stageDir); err != nil {
		return nil, err
	}
	path := filepath.Join(s.stageDir, name)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("%w: create %s: %v", ErrUploadStageUnsafe, name, err)
	}
	keep := false
	defer func() {
		if keep {
			return
		}
		_ = file.Close()
		_ = os.Remove(path)
	}()
	if err := file.Chmod(0o600); err != nil {
		return nil, fmt.Errorf("%w: secure %s: %v", ErrUploadStageUnsafe, name, err)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: inspect %s: %v", ErrUploadStageUnsafe, name, err)
	}
	if err := verifyPrivateRegularFile(path, info, name); err != nil {
		return nil, err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, pathInfo) {
		return nil, fmt.Errorf("%w: %s identity changed while opening", ErrUploadStageUnsafe, name)
	}
	keep = true
	return file, nil
}

func (s *UploadStage) openFile(name string) (*os.File, error) {
	if !isUploadStageFile(name) {
		return nil, fmt.Errorf("%w: unknown stage file", ErrUploadStageInvalid)
	}
	if s == nil {
		return nil, errors.New("upload stage is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.validateUsableLocked(); err != nil {
		return nil, err
	}
	if err := validateUploadStageEntries(s.stageDir); err != nil {
		return nil, err
	}
	path := filepath.Join(s.stageDir, name)
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if err := verifyPrivateRegularFile(path, before, name); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	after, statErr := file.Stat()
	if statErr != nil || !os.SameFile(before, after) {
		_ = file.Close()
		if statErr != nil {
			return nil, fmt.Errorf("%w: inspect %s after opening: %v", ErrUploadStageUnsafe, name, statErr)
		}
		return nil, fmt.Errorf("%w: %s identity changed while opening", ErrUploadStageUnsafe, name)
	}
	return file, nil
}

// Close removes only this stage's validated manifest.json, artifact.tar, and
// empty stage directory. It never recursively removes unknown entries.
func (s *UploadStage) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if err := removeUploadStage(s.root, s.namespace, s.stageDir, s.stageID); err != nil {
		return err
	}
	s.closed = true
	return nil
}

// ReconcileUploadStages validates the complete reserved namespace before
// removing any stale stage. It returns the number of removed stage
// directories, not the number of files. A missing root or namespace is an
// empty no-op and is never created by reconciliation.
func ReconcileUploadStages(rawSourceRoot string) (int, error) {
	root, err := resolveUploadStageRoot(rawSourceRoot, false)
	if errors.Is(err, ErrUploadStageRootMissing) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	namespace, err := resolveUploadStagingNamespace(root, false)
	if errors.Is(err, ErrUploadStageRootMissing) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(namespace)
	if err != nil {
		return 0, fmt.Errorf("%w: inspect staging namespace: %v", ErrUploadStageUnsafe, err)
	}
	stages := make([]string, 0, len(entries))
	for _, entry := range entries {
		stageID := entry.Name()
		if err := validateUploadStageID(stageID); err != nil {
			return 0, fmt.Errorf("%w: unknown staging entry %q", ErrUploadStageUnsafe, stageID)
		}
		stageDir := filepath.Join(namespace, stageID)
		if err := ensureUploadStageChild(root, namespace, stageDir); err != nil {
			return 0, err
		}
		if err := validateUploadStageDirectory(stageDir, stageID); err != nil {
			return 0, err
		}
		stages = append(stages, stageDir)
	}

	removed := 0
	for _, stageDir := range stages {
		currentNamespace, err := resolveUploadStagingNamespace(root, false)
		if err != nil {
			return removed, err
		}
		if currentNamespace != namespace {
			return removed, fmt.Errorf("%w: staging namespace changed during reconcile", ErrUploadStageUnsafe)
		}
		if err := removeValidatedUploadStageFiles(stageDir, filepath.Base(stageDir)); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func (s *UploadStage) validateUsableLocked() error {
	if s.closed {
		return errors.New("upload stage is closed")
	}
	if err := validateUploadStageID(s.stageID); err != nil {
		return err
	}
	namespace, err := resolveUploadStagingNamespace(s.root, false)
	if err != nil {
		return err
	}
	if namespace != s.namespace {
		return fmt.Errorf("%w: staging namespace changed", ErrUploadStageUnsafe)
	}
	if err := ensureUploadStageChild(s.root, s.namespace, s.stageDir); err != nil {
		return err
	}
	if err := verifyPrivateDirectory(s.stageDir, "stage directory"); err != nil {
		return err
	}
	return validateUploadStageEntries(s.stageDir)
}

func resolveUploadStageRoot(rawRoot string, create bool) (string, error) {
	root, err := canonicalUploadStageRoot(rawRoot)
	if err != nil {
		return "", err
	}
	if err := inspectUploadStageAncestors(filepath.Dir(root)); err != nil {
		return "", err
	}
	info, statErr := os.Lstat(root)
	if errors.Is(statErr, os.ErrNotExist) {
		if !create {
			return "", ErrUploadStageRootMissing
		}
		if mkdirErr := os.Mkdir(root, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
			return "", fmt.Errorf("%w: create configured raw source root: %v", ErrUploadStageUnsafe, mkdirErr)
		}
		info, statErr = os.Lstat(root)
	}
	if statErr != nil {
		return "", fmt.Errorf("%w: inspect configured raw source root: %v", ErrUploadStageUnsafe, statErr)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%w: configured raw source root contains an unsafe final component", ErrUploadStageUnsafe)
	}
	if err := verifyPrivateDirectory(root, "configured raw source root"); err != nil {
		return "", err
	}
	return root, nil
}

func inspectUploadStageAncestors(path string) error {
	volume := filepath.VolumeName(path)
	separator := string(filepath.Separator)
	remainder := strings.TrimPrefix(path, volume)
	remainder = strings.TrimPrefix(remainder, separator)
	current := volume + separator
	if volume == "" {
		current = separator
	}
	if remainder == "" {
		return nil
	}
	for _, part := range strings.Split(remainder, separator) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return ErrUploadStageRootMissing
		}
		if err != nil {
			return fmt.Errorf("%w: inspect configured raw source ancestor: %v", ErrUploadStageUnsafe, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: configured raw source root contains an unsafe ancestor", ErrUploadStageUnsafe)
		}
	}
	return nil
}

func canonicalUploadStageRoot(rawRoot string) (string, error) {
	if strings.TrimSpace(rawRoot) == "" || strings.TrimSpace(rawRoot) != rawRoot || !filepath.IsAbs(rawRoot) {
		return "", fmt.Errorf("%w: configured raw source root must be an absolute path", ErrUploadStageInvalid)
	}
	root := filepath.Clean(rawRoot)
	volume := filepath.VolumeName(root)
	if root == string(filepath.Separator) || (volume != "" && root == volume+string(filepath.Separator)) || (volume != "" && root == volume) {
		return "", fmt.Errorf("%w: configured raw source root must not be a volume root", ErrUploadStageInvalid)
	}
	return root, nil
}

func resolveUploadStagingNamespace(root string, create bool) (string, error) {
	namespace := filepath.Join(root, uploadStagingDirName)
	if err := ensureUploadStageChild(root, root, namespace); err != nil {
		return "", err
	}
	info, err := os.Lstat(namespace)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return "", ErrUploadStageRootMissing
		}
		if err := os.Mkdir(namespace, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("%w: create upload staging namespace: %v", ErrUploadStageUnsafe, err)
		}
		info, err = os.Lstat(namespace)
	}
	if err != nil {
		return "", fmt.Errorf("%w: inspect upload staging namespace: %v", ErrUploadStageUnsafe, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%w: upload staging namespace is not a directory", ErrUploadStageUnsafe)
	}
	if err := verifyPrivateDirectory(namespace, "upload staging namespace"); err != nil {
		return "", err
	}
	return namespace, nil
}

func ensureUploadStageChild(root, parent, target string) error {
	if err := ensureUploadStagePathWithin(root, target); err != nil {
		return err
	}
	if parent != "" {
		if err := ensureUploadStagePathWithin(root, parent); err != nil {
			return err
		}
	}
	rel, err := filepath.Rel(parent, target)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == "." {
		return fmt.Errorf("%w: staging path escapes its parent", ErrUploadStageUnsafe)
	}
	return nil
}

func ensureUploadStagePathWithin(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: staging path escapes configured root", ErrUploadStageUnsafe)
	}
	return nil
}

func validateUploadStageDirectory(stageDir, stageID string) error {
	if err := validateUploadStageID(stageID); err != nil {
		return err
	}
	if err := verifyPrivateDirectory(stageDir, "stage directory"); err != nil {
		return err
	}
	return validateUploadStageEntries(stageDir)
}

func validateUploadStageEntries(stageDir string) error {
	entries, err := os.ReadDir(stageDir)
	if err != nil {
		return fmt.Errorf("%w: inspect stage entries: %v", ErrUploadStageUnsafe, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !isUploadStageFile(name) {
			return fmt.Errorf("%w: unknown or nested stage entry %q", ErrUploadStageUnsafe, name)
		}
		path := filepath.Join(stageDir, name)
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("%w: inspect stage file %q: %v", ErrUploadStageUnsafe, name, err)
		}
		if err := verifyPrivateRegularFile(path, info, name); err != nil {
			return err
		}
	}
	return nil
}

func removeUploadStage(root, namespace, stageDir, stageID string) error {
	if _, err := os.Lstat(stageDir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("%w: inspect stage before close: %v", ErrUploadStageUnsafe, err)
	}
	if _, err := resolveUploadStageRoot(root, false); err != nil {
		return err
	}
	if _, err := resolveUploadStagingNamespace(root, false); err != nil {
		return err
	}
	if err := ensureUploadStageChild(root, namespace, stageDir); err != nil {
		return err
	}
	if err := validateUploadStageDirectory(stageDir, stageID); err != nil {
		return err
	}
	return removeValidatedUploadStageFiles(stageDir, stageID)
}

func removeValidatedUploadStageFiles(stageDir, stageID string) error {
	if err := validateUploadStageDirectory(stageDir, stageID); err != nil {
		return err
	}
	for _, name := range []string{uploadManifestName, uploadArtifactName} {
		path := filepath.Join(stageDir, name)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: remove stage file %q: %v", ErrUploadStageUnsafe, name, err)
		}
	}
	if err := os.Remove(stageDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: remove stage directory: %v", ErrUploadStageUnsafe, err)
	}
	return nil
}

func validateUploadStageID(stageID string) error {
	if stageID == "" || len(stageID) > maxUploadStageIDLen || stageID == "." || stageID == ".." {
		return fmt.Errorf("%w: stage ID is invalid", ErrUploadStageInvalid)
	}
	for _, char := range stageID {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') {
			continue
		}
		if char != '-' || char > 127 {
			return fmt.Errorf("%w: stage ID must be pathless ASCII alphanumeric or hyphen", ErrUploadStageInvalid)
		}
	}
	return nil
}

func isUploadStageFile(name string) bool {
	return name == uploadManifestName || name == uploadArtifactName
}

func verifyPrivateDirectory(path, description string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: inspect %s: %v", ErrUploadStageUnsafe, description, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrUploadStageUnsafe, description)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		return fmt.Errorf("%w: %s must be mode 0700", ErrUploadStageUnsafe, description)
	}
	return nil
}

func verifyPrivateRegularFile(path string, info os.FileInfo, description string) error {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || uploadStageLinkCount(info) > 1 {
		return fmt.Errorf("%w: %s is not a private regular file", ErrUploadStageUnsafe, description)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return fmt.Errorf("%w: %s must be mode 0600", ErrUploadStageUnsafe, description)
	}
	return nil
}

func uploadStageLinkCount(info os.FileInfo) uint64 {
	if info == nil || info.Sys() == nil {
		return 0
	}
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Ptr {
		if value.IsNil() {
			return 0
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return 0
	}
	field := value.FieldByName("Nlink")
	if !field.IsValid() {
		return 0
	}
	switch field.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return field.Uint()
	default:
		return 0
	}
}
