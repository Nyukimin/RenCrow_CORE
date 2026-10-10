package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/nativeharnessclient"
)

// RenCrow_Harness委譲のCORE側設定（Human relay issuer と、実行profile
// shiro_native_coding_v1 の選択）。
// 正本: docs/05_設定リファレンス.md#rencrow_harness委譲設定 /
//
//	docs/07_安全・自動実行・データ方針.md#rencrow_harness委譲の安全境界
//
// native_harness sectionが未設定なら何も検証せず、現行の挙動は変わらない。
// 1項目でも設定されたら全項目を起動時に検査し、不正なら起動を拒否する。

// maxNativeHarnessConfigBytes bounds the Harness config CORE reads for the
// issuer cross-check. A larger file is refused without being read in full.
const maxNativeHarnessConfigBytes = 1 << 20

// nativeHarnessKeyFileLabel names the key file setting in permission errors.
const nativeHarnessKeyFileLabel = "native_harness.issuer.key_file"

var errNativeHarnessKeyUnreadable = errors.New(nativeHarnessKeyFileLabel + " is unreadable")

// NativeHarnessConfig is the native_harness section.
type NativeHarnessConfig struct {
	// HarnessConfig is the absolute path of the config CORE hands to
	// rencrow-harness. CORE reads only its caller section, to check the issuer.
	HarnessConfig string `yaml:"harness_config"`
	// Issuer is the Human relay issuer (OriginProof) setting.
	Issuer NativeHarnessIssuerConfig `yaml:"issuer"`
	// Profile selects the execution profile shiro_native_coding_v1. Only
	// profile.enabled=true makes the admission step of the orchestrator select
	// new OPS turns for RenCrow_Harness; nothing else (message text, model
	// output, a client request) does.
	Profile NativeHarnessProfileConfig `yaml:"profile"`
}

// NativeHarnessProfileConfig are the logical items of the 05 delegation profile
// table that are not the Harness config path (harness_config) itself. The
// profile id (shiro_native_coding_v1), the agent (shiro) and the backend
// (native_harness) are fixed by the profile and are not settings.
type NativeHarnessProfileConfig struct {
	Enabled bool `yaml:"enabled"`
	// HarnessBinary is the absolute path of the rencrow-harness executable.
	HarnessBinary string `yaml:"harness_binary"`
	// ExpectedBuildRevision is the build_revision the Harness must report in
	// initialize; another build is not started against (fail closed).
	ExpectedBuildRevision string `yaml:"expected_build_revision"`
	// ExpectedCriteriaRevision is the immutable Harness-owner verification
	// criteria revision deployment-pinned for new native-capable Tasks.
	ExpectedCriteriaRevision string `yaml:"expected_criteria_revision"`
	// WorkspaceRef names the workspace the Harness works in. The root, the mode
	// and the policy must be ones the Harness config allows; the Harness
	// answers a request outside them with FORBIDDEN.
	WorkspaceRef NativeHarnessWorkspaceRef `yaml:"workspace_ref"`
	// BindingRef is the existing Shiro Gateway binding (an execution alias with
	// agent_id shiro and an execution_role).
	BindingRef NativeHarnessBindingRef `yaml:"binding_ref"`
	// Limits is the complete limits sent with every start. There are no defaults.
	Limits NativeHarnessLimits `yaml:"limits"`
}

// NativeHarnessWorkspaceRef is workspace_ref of the 05 table.
type NativeHarnessWorkspaceRef struct {
	Path          string `yaml:"path"`
	PolicyRef     string `yaml:"policy_ref"`
	ExecutionMode string `yaml:"execution_mode"`
}

// NativeHarnessBindingRef is binding_ref of the 05 table.
type NativeHarnessBindingRef struct {
	Kind            string `yaml:"kind"`
	Selector        string `yaml:"selector"`
	ProfileRevision string `yaml:"profile_revision"`
	ExecutionRole   string `yaml:"execution_role"`
}

// NativeHarnessLimits are the per-Run budgets of the Harness protocol.
type NativeHarnessLimits struct {
	MaxModelSteps         int64 `yaml:"max_model_steps"`
	MaxToolCallsPerStep   int64 `yaml:"max_tool_calls_per_step"`
	DeadlineSeconds       int64 `yaml:"deadline_seconds"`
	MaxCaptureBytes       int64 `yaml:"max_capture_bytes"`
	MaxGenerationAttempts int64 `yaml:"max_generation_attempts"`
}

// NativeHarnessIssuerConfig are the logical items of the 05 Human relay issuer
// table. KeyFile is the absolute path of the HMAC key file; the key itself is
// read to validate the file and is never kept in the configuration.
type NativeHarnessIssuerConfig struct {
	Issuer     string `yaml:"issuer"`
	KeyID      string `yaml:"key_id"`
	Audience   string `yaml:"audience"`
	KeyFile    string `yaml:"key_file"`
	TTLSeconds int    `yaml:"ttl_seconds"`
}

func (c NativeHarnessConfig) configured() bool {
	return strings.TrimSpace(c.HarnessConfig) != "" || c.Issuer != NativeHarnessIssuerConfig{} || c.Profile != NativeHarnessProfileConfig{}
}

// IssuerSettings returns the issuer items that the OriginProof signer needs.
func (c NativeHarnessConfig) IssuerSettings() nativeharnessclient.IssuerSettings {
	return nativeharnessclient.IssuerSettings{
		Issuer:     c.Issuer.Issuer,
		KeyID:      c.Issuer.KeyID,
		Audience:   c.Issuer.Audience,
		TTLSeconds: c.Issuer.TTLSeconds,
	}
}

// validateNativeHarnessConfig fails closed on the native_harness section: the
// issuer items, a key file that is absolute, outside the workspace, readable
// only by the operating user (POSIX mode bits or Windows DACL, as for the
// storage host token file) and in the 64 hex format (and not the published
// fixture key), and a Harness config whose caller section matches the issuer.
func (c *Config) validateNativeHarnessConfig() error {
	section := c.NativeHarness
	if !section.configured() {
		return nil
	}
	harnessConfig := strings.TrimSpace(section.HarnessConfig)
	if harnessConfig == "" {
		return errors.New("native_harness.harness_config is required when native_harness is configured")
	}
	if !configPathIsAbs(harnessConfig) {
		return errors.New("native_harness.harness_config must be an absolute path")
	}
	settings := section.IssuerSettings()
	if err := settings.Validate(); err != nil {
		return fmt.Errorf("native_harness.issuer: %w", err)
	}
	keyFile := strings.TrimSpace(section.Issuer.KeyFile)
	if keyFile == "" {
		return errors.New("native_harness.issuer.key_file is required when native_harness is configured")
	}
	if !configPathIsAbs(keyFile) {
		return errors.New("native_harness.issuer.key_file must be an absolute path")
	}
	workspace := strings.TrimSpace(c.WorkspaceDir)
	if workspace == "" {
		return errors.New("workspace_dir is required to check that the native_harness files are outside it")
	}
	for _, file := range []struct{ name, path string }{
		{"native_harness.issuer.key_file", keyFile},
		{"native_harness.harness_config", harnessConfig},
	} {
		inside, err := pathInsideWorkspace(workspace, file.path)
		if err != nil {
			return fmt.Errorf("%s: check against workspace_dir: %w", file.name, err)
		}
		if inside {
			return fmt.Errorf("%s must be outside workspace_dir", file.name)
		}
	}
	if err := checkNativeHarnessKeyFile(keyFile); err != nil {
		return err
	}
	data, err := readBoundedFile(harnessConfig, maxNativeHarnessConfigBytes)
	if err != nil {
		return fmt.Errorf("native_harness.harness_config: %w", err)
	}
	if err := nativeharnessclient.CheckHarnessCallerProfile(settings, data); err != nil {
		return fmt.Errorf("native_harness.harness_config: %w", err)
	}
	return c.validateNativeHarnessProfile(workspace)
}

// checkNativeHarnessKeyFile opens the key file through the platform-native
// confidential-file rule (POSIX mode bits, Windows DACL; the same check as the
// storage host token file) and reads the key from that same handle, so the
// file that was checked is the file that is read. The key is read only to
// validate the file and is dropped here. A file readable by another user is
// refused before its content is read.
func checkNativeHarnessKeyFile(path string) error {
	handle, err := openConfidentialFile(nativeHarnessKeyFileLabel, path, errNativeHarnessKeyUnreadable)
	if err != nil {
		return err
	}
	defer handle.Close()
	if _, err := nativeharnessclient.ReadKey(handle); err != nil {
		return fmt.Errorf("%s: %w", nativeHarnessKeyFileLabel, err)
	}
	return nil
}

// readBoundedFile reads a regular file of at most limit bytes. A larger file is
// refused after reading at most limit+1 bytes.
func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file is larger than %d bytes", limit)
	}
	return data, nil
}

// pathInsideWorkspace reports whether path is the workspace or lies below it.
// The workspace may be relative (it is resolved against the working
// directory, as the runtime does). The answer is yes if any of these agree:
// the resolved paths compare as inside, the symbolic-link-free paths compare
// as inside, or a parent directory of path is the same directory as the
// workspace on disk (which also covers case-insensitive file systems).
func pathInsideWorkspace(workspace, path string) (bool, error) {
	workspaceAbs, err := filepath.Abs(workspace)
	if err != nil {
		return false, err
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	if inside, err := pathWithin(workspaceAbs, pathAbs); err != nil || inside {
		return inside, err
	}
	workspaceReal, workspaceErr := filepath.EvalSymlinks(workspaceAbs)
	pathReal, pathErr := filepath.EvalSymlinks(pathAbs)
	if workspaceErr == nil && pathErr == nil {
		if inside, err := pathWithin(workspaceReal, pathReal); err != nil || inside {
			return inside, err
		}
	}
	workspaceInfo, err := os.Stat(workspaceAbs)
	if err != nil {
		// A workspace that does not exist yet cannot contain an existing file
		// except by the lexical match already checked.
		return false, nil
	}
	for dir := filepath.Dir(pathAbs); ; {
		if info, err := os.Stat(dir); err == nil && os.SameFile(workspaceInfo, info) {
			return true, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false, nil
		}
		dir = parent
	}
}
