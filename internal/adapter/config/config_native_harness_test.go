package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/nativeharnessclient"
)

const (
	testIssuer   = "core:test-issuer"
	testKeyID    = "test-key-1"
	testAudience = "core:local"
)

type nativeHarnessPaths struct {
	workspace     string
	keyFile       string
	harnessConfig string
}

// harnessConfigJSON is a Harness config whose caller section satisfies the 05
// premises. Fields CORE does not read are present to prove they are ignored.
func harnessConfigJSON(principal, origin, issuer, keyID, audience string) string {
	return fmt.Sprintf(`{
  "config_version": "rencrow-harness-config/v1",
  "caller": {
    "principal": %q,
    "default_origin": %q,
    "readable_session_owners": ["core:local"],
    "controllable_session_owners": ["core:local"],
    "relay_issuers": [
      {"issuer": %q, "key_id": %q, "audience": %q, "key_file": "/not/read/by/core"}
    ]
  },
  "limits": {"max_model_steps": 10}
}`, principal, origin, issuer, keyID, audience)
}

func randomKeyHex(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("read random key: %v", err)
	}
	return hex.EncodeToString(raw)
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// validNativeHarnessConfig builds a Config whose native_harness section is
// valid: the workspace, the key file and the Harness config live in separate
// temporary directories.
func validNativeHarnessConfig(t *testing.T) (*Config, nativeHarnessPaths) {
	t.Helper()
	root := t.TempDir()
	paths := nativeHarnessPaths{
		workspace:     filepath.Join(root, "workspace"),
		keyFile:       filepath.Join(root, "private", "origin.key"),
		harnessConfig: filepath.Join(root, "private", "harness.json"),
	}
	if err := os.MkdirAll(paths.workspace, 0o700); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	writeTestFile(t, paths.keyFile, randomKeyHex(t)+"\n")
	writeTestFile(t, paths.harnessConfig, harnessConfigJSON("core:local", "automation", testIssuer, testKeyID, testAudience))
	cfg := &Config{
		WorkspaceDir: paths.workspace,
		NativeHarness: NativeHarnessConfig{
			HarnessConfig: paths.harnessConfig,
			Issuer: NativeHarnessIssuerConfig{
				Issuer:     testIssuer,
				KeyID:      testKeyID,
				Audience:   testAudience,
				KeyFile:    paths.keyFile,
				TTLSeconds: 300,
			},
		},
	}
	return cfg, paths
}

func TestNativeHarnessAbsentSectionChangesNothing(t *testing.T) {
	// Arrange
	cfg := &Config{}

	// Act
	err := cfg.validateNativeHarnessConfig()

	// Assert
	if err != nil {
		t.Fatalf("an absent native_harness section must not be validated: %v", err)
	}
	if cfg.NativeHarness.configured() {
		t.Fatalf("zero section reported as configured")
	}
}

func TestNativeHarnessValidConfigIsAccepted(t *testing.T) {
	cfg, _ := validNativeHarnessConfig(t)
	for _, ttl := range []int{1, 60, 300} {
		cfg.NativeHarness.Issuer.TTLSeconds = ttl
		if err := cfg.validateNativeHarnessConfig(); err != nil {
			t.Fatalf("ttl_seconds %d rejected: %v", ttl, err)
		}
	}
}

func TestNativeHarnessKeyFileInDirectoryWithSharedPrefixIsOutsideWorkspace(t *testing.T) {
	// Arrange: ".../workspace-keys" shares a name prefix with ".../workspace".
	cfg, paths := validNativeHarnessConfig(t)
	sibling := paths.workspace + "-keys"
	keyFile := filepath.Join(sibling, "origin.key")
	writeTestFile(t, keyFile, randomKeyHex(t))
	cfg.NativeHarness.Issuer.KeyFile = keyFile

	// Act
	err := cfg.validateNativeHarnessConfig()

	// Assert
	if err != nil {
		t.Fatalf("a sibling directory with a shared prefix is outside the workspace: %v", err)
	}
}

func TestNativeHarnessRejectsInvalidSettings(t *testing.T) {
	cases := map[string]struct {
		mutate func(*NativeHarnessConfig)
		want   string
	}{
		"harness_config empty":    {func(c *NativeHarnessConfig) { c.HarnessConfig = "" }, "native_harness.harness_config"},
		"harness_config blank":    {func(c *NativeHarnessConfig) { c.HarnessConfig = "   " }, "native_harness.harness_config"},
		"harness_config relative": {func(c *NativeHarnessConfig) { c.HarnessConfig = "harness.json" }, "native_harness.harness_config must be an absolute path"},
		"issuer empty":            {func(c *NativeHarnessConfig) { c.Issuer.Issuer = "" }, "native_harness.issuer"},
		"issuer control char":     {func(c *NativeHarnessConfig) { c.Issuer.Issuer = "core:a\nb" }, "native_harness.issuer"},
		"key_id empty":            {func(c *NativeHarnessConfig) { c.Issuer.KeyID = "" }, "native_harness.issuer"},
		"audience empty":          {func(c *NativeHarnessConfig) { c.Issuer.Audience = "" }, "native_harness.issuer"},
		"audience not principal":  {func(c *NativeHarnessConfig) { c.Issuer.Audience = "Core:Local" }, "native_harness.issuer"},
		"ttl zero":                {func(c *NativeHarnessConfig) { c.Issuer.TTLSeconds = 0 }, "native_harness.issuer"},
		"ttl negative":            {func(c *NativeHarnessConfig) { c.Issuer.TTLSeconds = -5 }, "native_harness.issuer"},
		"ttl over 300":            {func(c *NativeHarnessConfig) { c.Issuer.TTLSeconds = 301 }, "native_harness.issuer"},
		"key_file empty":          {func(c *NativeHarnessConfig) { c.Issuer.KeyFile = "" }, "native_harness.issuer.key_file"},
		"key_file relative":       {func(c *NativeHarnessConfig) { c.Issuer.KeyFile = "origin.key" }, "native_harness.issuer.key_file must be an absolute path"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, _ := validNativeHarnessConfig(t)
			tc.mutate(&cfg.NativeHarness)

			err := cfg.validateNativeHarnessConfig()

			if err == nil {
				t.Fatalf("invalid section accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestNativeHarnessRejectsWorkspaceThatCannotBeChecked(t *testing.T) {
	cfg, _ := validNativeHarnessConfig(t)
	cfg.WorkspaceDir = ""

	err := cfg.validateNativeHarnessConfig()

	if err == nil || !strings.Contains(err.Error(), "workspace_dir") {
		t.Fatalf("error = %v, want it to require workspace_dir", err)
	}
}

func TestNativeHarnessRejectsKeyFileProblems(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		cfg, paths := validNativeHarnessConfig(t)
		cfg.NativeHarness.Issuer.KeyFile = filepath.Join(filepath.Dir(paths.keyFile), "missing.key")
		if err := cfg.validateNativeHarnessConfig(); err == nil || !strings.Contains(err.Error(), "native_harness.issuer.key_file") {
			t.Fatalf("error = %v, want a key_file error", err)
		}
	})
	t.Run("directory", func(t *testing.T) {
		cfg, paths := validNativeHarnessConfig(t)
		cfg.NativeHarness.Issuer.KeyFile = filepath.Dir(paths.keyFile)
		if err := cfg.validateNativeHarnessConfig(); err == nil {
			t.Fatalf("a directory was accepted as the key file")
		}
	})
	formats := map[string]string{
		"uppercase hex": strings.ToUpper(randomKeyHex(t)),
		"CRLF":          randomKeyHex(t) + "\r\n",
		"short":         randomKeyHex(t)[:62],
		"two lines":     randomKeyHex(t) + "\n" + randomKeyHex(t) + "\n",
		"base64":        "q83vEjRWeJCrze8SNFZ4kKvN7xI0VniQq83vEjRWeJA=",
		"empty":         "",
	}
	for name, content := range formats {
		t.Run("format "+name, func(t *testing.T) {
			cfg, paths := validNativeHarnessConfig(t)
			writeTestFile(t, paths.keyFile, content)
			err := cfg.validateNativeHarnessConfig()
			if !errors.Is(err, nativeharnessclient.ErrKeyFormat) {
				t.Fatalf("error = %v, want ErrKeyFormat", err)
			}
		})
	}
	t.Run("published fixture key", func(t *testing.T) {
		cfg, paths := validNativeHarnessConfig(t)
		fixture, err := os.ReadFile(filepath.Join("..", "nativeharnessclient", "testdata", "synthetic_origin_key.hex"))
		if err != nil {
			t.Fatalf("read the fixture key file: %v", err)
		}
		writeTestFile(t, paths.keyFile, string(fixture))
		err = cfg.validateNativeHarnessConfig()
		if !errors.Is(err, nativeharnessclient.ErrKeyFixture) {
			t.Fatalf("error = %v, want ErrKeyFixture", err)
		}
	})
	t.Run("error never carries the key", func(t *testing.T) {
		cfg, paths := validNativeHarnessConfig(t)
		secret := randomKeyHex(t)
		writeTestFile(t, paths.keyFile, strings.ToUpper(secret))
		err := cfg.validateNativeHarnessConfig()
		if err == nil {
			t.Fatalf("uppercase key accepted")
		}
		if strings.Contains(strings.ToLower(err.Error()), secret) {
			t.Fatalf("error leaks key material: %q", err)
		}
	})
}

func TestNativeHarnessRejectsFilesInsideTheWorkspace(t *testing.T) {
	t.Run("key file inside", func(t *testing.T) {
		cfg, paths := validNativeHarnessConfig(t)
		inside := filepath.Join(paths.workspace, "secrets", "origin.key")
		writeTestFile(t, inside, randomKeyHex(t))
		cfg.NativeHarness.Issuer.KeyFile = inside
		err := cfg.validateNativeHarnessConfig()
		if err == nil || !strings.Contains(err.Error(), "native_harness.issuer.key_file") || !strings.Contains(err.Error(), "workspace") {
			t.Fatalf("error = %v, want a key_file workspace error", err)
		}
	})
	t.Run("harness config inside", func(t *testing.T) {
		cfg, paths := validNativeHarnessConfig(t)
		inside := filepath.Join(paths.workspace, "harness.json")
		writeTestFile(t, inside, harnessConfigJSON("core:local", "automation", testIssuer, testKeyID, testAudience))
		cfg.NativeHarness.HarnessConfig = inside
		err := cfg.validateNativeHarnessConfig()
		if err == nil || !strings.Contains(err.Error(), "native_harness.harness_config") || !strings.Contains(err.Error(), "workspace") {
			t.Fatalf("error = %v, want a harness_config workspace error", err)
		}
	})
	t.Run("key file reached through a symbolic link", func(t *testing.T) {
		cfg, paths := validNativeHarnessConfig(t)
		real := filepath.Join(paths.workspace, "secrets", "origin.key")
		writeTestFile(t, real, randomKeyHex(t))
		link := filepath.Join(filepath.Dir(paths.keyFile), "linked")
		if err := os.Symlink(filepath.Dir(real), link); err != nil {
			t.Skipf("symbolic links are not available: %v", err)
		}
		cfg.NativeHarness.Issuer.KeyFile = filepath.Join(link, "origin.key")
		if err := cfg.validateNativeHarnessConfig(); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Fatalf("error = %v, want a workspace error for a key reached through a link", err)
		}
	})
	t.Run("key file that is itself a symbolic link into the workspace", func(t *testing.T) {
		cfg, paths := validNativeHarnessConfig(t)
		real := filepath.Join(paths.workspace, "origin.key")
		writeTestFile(t, real, randomKeyHex(t))
		if err := os.Remove(paths.keyFile); err != nil {
			t.Fatalf("remove key file: %v", err)
		}
		if err := os.Symlink(real, paths.keyFile); err != nil {
			t.Skipf("symbolic links are not available: %v", err)
		}
		if err := cfg.validateNativeHarnessConfig(); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Fatalf("error = %v, want a workspace error for a key file linked into the workspace", err)
		}
	})
	t.Run("workspace given as a symbolic link", func(t *testing.T) {
		cfg, paths := validNativeHarnessConfig(t)
		inside := filepath.Join(paths.workspace, "origin.key")
		writeTestFile(t, inside, randomKeyHex(t))
		link := filepath.Join(filepath.Dir(paths.keyFile), "workspace-link")
		if err := os.Symlink(paths.workspace, link); err != nil {
			t.Skipf("symbolic links are not available: %v", err)
		}
		cfg.WorkspaceDir = link
		cfg.NativeHarness.Issuer.KeyFile = inside
		if err := cfg.validateNativeHarnessConfig(); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Fatalf("error = %v, want a workspace error when the workspace is a link", err)
		}
	})
	t.Run("same directory spelled with another case", func(t *testing.T) {
		cfg, paths := validNativeHarnessConfig(t)
		inside := filepath.Join(paths.workspace, "origin.key")
		writeTestFile(t, inside, randomKeyHex(t))
		swapped := filepath.Join(filepath.Dir(paths.workspace), strings.ToUpper(filepath.Base(paths.workspace)))
		if _, err := os.Stat(swapped); err != nil {
			t.Skip("the file system is case-sensitive")
		}
		cfg.WorkspaceDir = swapped
		cfg.NativeHarness.Issuer.KeyFile = inside
		if err := cfg.validateNativeHarnessConfig(); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Fatalf("error = %v, want a workspace error for the same directory in another case", err)
		}
	})
	t.Run("relative workspace_dir is resolved before comparing", func(t *testing.T) {
		cfg, paths := validNativeHarnessConfig(t)
		inside := filepath.Join(paths.workspace, "origin.key")
		writeTestFile(t, inside, randomKeyHex(t))
		relative, err := filepath.Rel(mustGetwd(t), paths.workspace)
		if err != nil {
			t.Skipf("no relative path from the working directory: %v", err)
		}
		cfg.WorkspaceDir = relative
		cfg.NativeHarness.Issuer.KeyFile = inside
		if err := cfg.validateNativeHarnessConfig(); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Fatalf("error = %v, want a workspace error for a relative workspace_dir", err)
		}
	})
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return dir
}

func TestNativeHarnessRejectsHarnessConfigThatDoesNotMatchTheIssuer(t *testing.T) {
	cases := map[string]string{
		"not json":             "caller: core:local",
		"no caller section":    `{"config_version": "rencrow-harness-config/v1"}`,
		"principal is a user":  harnessConfigJSON("user:ren", "automation", testIssuer, testKeyID, testAudience),
		"default origin human": harnessConfigJSON("core:local", "human", testIssuer, testKeyID, testAudience),
		"issuer not allowed":   harnessConfigJSON("core:local", "automation", "core:someone-else", testKeyID, testAudience),
		"key id not allowed":   harnessConfigJSON("core:local", "automation", testIssuer, "test-key-2", testAudience),
		"audience not allowed": harnessConfigJSON("core:local", "automation", testIssuer, testKeyID, "core:remote"),
		"empty allowlist":      `{"caller": {"principal": "core:local", "default_origin": "automation", "relay_issuers": []}}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, paths := validNativeHarnessConfig(t)
			writeTestFile(t, paths.harnessConfig, content)

			err := cfg.validateNativeHarnessConfig()

			if err == nil || !strings.Contains(err.Error(), "native_harness.harness_config") {
				t.Fatalf("error = %v, want a harness_config error", err)
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		cfg, paths := validNativeHarnessConfig(t)
		cfg.NativeHarness.HarnessConfig = filepath.Join(filepath.Dir(paths.harnessConfig), "missing.json")
		if err := cfg.validateNativeHarnessConfig(); err == nil || !strings.Contains(err.Error(), "native_harness.harness_config") {
			t.Fatalf("error = %v, want a harness_config error", err)
		}
	})
	t.Run("directory", func(t *testing.T) {
		cfg, paths := validNativeHarnessConfig(t)
		cfg.NativeHarness.HarnessConfig = filepath.Dir(paths.harnessConfig)
		if err := cfg.validateNativeHarnessConfig(); err == nil {
			t.Fatalf("a directory was accepted as the Harness config")
		}
	})
	t.Run("oversized file is refused without reading it all", func(t *testing.T) {
		cfg, paths := validNativeHarnessConfig(t)
		writeTestFile(t, paths.harnessConfig, `{"caller": "`+strings.Repeat("x", maxNativeHarnessConfigBytes)+`"}`)
		if err := cfg.validateNativeHarnessConfig(); err == nil || !strings.Contains(err.Error(), "native_harness.harness_config") {
			t.Fatalf("error = %v, want a harness_config size error", err)
		}
	})
}

func nativeHarnessYAML(t *testing.T, paths nativeHarnessPaths, ttl int) string {
	t.Helper()
	return fmt.Sprintf(`workspace_dir: %q
native_harness:
  harness_config: %q
  issuer:
    issuer: %q
    key_id: %q
    audience: %q
    key_file: %q
    ttl_seconds: %d
`, paths.workspace, paths.harnessConfig, testIssuer, testKeyID, testAudience, paths.keyFile, ttl)
}

func TestLoadConfigReadsNativeHarnessSection(t *testing.T) {
	_, paths := validNativeHarnessConfig(t)

	cfg, err := LoadConfig(writeStorageHostConfig(t, nativeHarnessYAML(t, paths, 120)))

	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	section := cfg.NativeHarness
	if section.HarnessConfig != paths.harnessConfig || section.Issuer.Issuer != testIssuer || section.Issuer.KeyID != testKeyID ||
		section.Issuer.Audience != testAudience || section.Issuer.KeyFile != paths.keyFile || section.Issuer.TTLSeconds != 120 {
		t.Fatalf("native_harness section was not read as written: %+v", section)
	}
}

func TestLoadConfigRefusesToStartOnInvalidNativeHarnessSection(t *testing.T) {
	t.Run("ttl over the limit", func(t *testing.T) {
		_, paths := validNativeHarnessConfig(t)
		_, err := LoadConfig(writeStorageHostConfig(t, nativeHarnessYAML(t, paths, 301)))
		if err == nil || !strings.Contains(err.Error(), "native_harness.issuer") {
			t.Fatalf("error = %v, want startup refusal naming native_harness.issuer", err)
		}
	})
	t.Run("section with only an issuer is not silently ignored", func(t *testing.T) {
		body := "native_harness:\n  issuer:\n    issuer: " + testIssuer + "\n"
		_, err := LoadConfig(writeStorageHostConfig(t, body))
		if err == nil || !strings.Contains(err.Error(), "native_harness") {
			t.Fatalf("error = %v, want startup refusal for a partial section", err)
		}
	})
	t.Run("key file inside the workspace", func(t *testing.T) {
		_, paths := validNativeHarnessConfig(t)
		inside := filepath.Join(paths.workspace, "origin.key")
		writeTestFile(t, inside, randomKeyHex(t))
		paths.keyFile = inside
		_, err := LoadConfig(writeStorageHostConfig(t, nativeHarnessYAML(t, paths, 300)))
		if err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Fatalf("error = %v, want startup refusal for a key inside the workspace", err)
		}
	})
}

func TestLoadConfigWithoutNativeHarnessSectionStillLoads(t *testing.T) {
	cfg, err := LoadConfig(writeStorageHostConfig(t, ""))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.NativeHarness.configured() {
		t.Fatalf("native_harness must stay absent by default: %+v", cfg.NativeHarness)
	}
}

func TestNativeHarnessIssuerSettingsCarryTheConfiguredValues(t *testing.T) {
	cfg, _ := validNativeHarnessConfig(t)

	settings := cfg.NativeHarness.IssuerSettings()

	want := nativeharnessclient.IssuerSettings{Issuer: testIssuer, KeyID: testKeyID, Audience: testAudience, TTLSeconds: 300}
	if settings != want {
		t.Fatalf("IssuerSettings() = %+v, want %+v", settings, want)
	}
}
