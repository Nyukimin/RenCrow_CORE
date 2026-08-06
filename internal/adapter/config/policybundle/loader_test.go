package policybundle

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policybundle"
)

func TestLoadWorkspaceMissingBundleIsSafeMissing(t *testing.T) {
	status := LoadWorkspace(t.TempDir())
	if status.State != domainpolicy.StateMissing || len(status.DisabledCapabilities) != 0 {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestLoadWorkspaceAcceptsValidBundle(t *testing.T) {
	workspace := t.TempDir()
	writeValidBundle(t, workspace, nil)

	status := LoadWorkspace(workspace)
	if status.State != domainpolicy.StateActive {
		t.Fatalf("state=%q error=%q", status.State, status.Error)
	}
	if status.BundleID != "rencrow-default" || status.BundleRevision != "2026-08-06.1" {
		t.Fatalf("unexpected identity: %+v", status)
	}
	if got := strings.Join(status.DisabledCapabilities, ","); got != "financial_order,git_remote_write" {
		t.Fatalf("disabled capabilities=%q", got)
	}
}

func TestLoadWorkspaceRejectsHashMismatch(t *testing.T) {
	workspace := t.TempDir()
	root := writeValidBundle(t, workspace, nil)
	path := filepath.Join(root, "global.yaml")
	if err := os.WriteFile(path, []byte("schema_version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	status := LoadWorkspace(workspace)
	if status.State != domainpolicy.StateInvalid || !strings.Contains(status.Error, "hash mismatch") {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestLoadWorkspaceRejectsUnknownField(t *testing.T) {
	workspace := t.TempDir()
	writeValidBundle(t, workspace, map[string]string{
		"global.yaml": "schema_version: 1\npolicy_id: global-defaults\ndefault_side_effect: blocked\nunknown: true\n",
	})

	status := LoadWorkspace(workspace)
	if status.State != domainpolicy.StateInvalid || !strings.Contains(status.Error, "field unknown") {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestLoadWorkspaceRejectsDefaultAllow(t *testing.T) {
	workspace := t.TempDir()
	writeValidBundle(t, workspace, map[string]string{
		"global.yaml": "schema_version: 1\npolicy_id: global-defaults\ndefault_side_effect: allowed\n",
	})

	status := LoadWorkspace(workspace)
	if status.State != domainpolicy.StateInvalid || !strings.Contains(status.Error, "default_side_effect") {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestLoadWorkspaceRejectsSymlink(t *testing.T) {
	workspace := t.TempDir()
	root := writeValidBundle(t, workspace, nil)
	target := filepath.Join(workspace, "outside.yaml")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "global.yaml")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	status := LoadWorkspace(workspace)
	if status.State != domainpolicy.StateInvalid || !strings.Contains(status.Error, "symlink") {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestLoadWorkspaceRejectsSymlinkPolicyRoot(t *testing.T) {
	workspace := t.TempDir()
	targetWorkspace := t.TempDir()
	writeValidBundle(t, targetWorkspace, nil)
	if err := os.Symlink(filepath.Join(targetWorkspace, "policies"), filepath.Join(workspace, "policies")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	status := LoadWorkspace(workspace)
	if status.State != domainpolicy.StateInvalid || !strings.Contains(status.Error, "policy root") {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func writeValidBundle(t *testing.T, workspace string, overrides map[string]string) string {
	t.Helper()
	root := filepath.Join(workspace, "policies")
	files := map[string]string{
		"global.yaml":                 "schema_version: 1\npolicy_id: global-defaults\ndefault_side_effect: blocked\n",
		"capabilities.yaml":           "schema_version: 1\npolicy_id: capability-ceiling\ncapabilities:\n  filesystem_read: true\n  financial_order: false\n  git_remote_write: false\n",
		"authorizations.yaml":         "schema_version: 1\npolicy_id: explicit-authorizations\nauthorizations: []\n",
		"data-handling.yaml":          "schema_version: 1\npolicy_id: data-handling\nrules: []\n",
		"external-actions.yaml":       "schema_version: 1\npolicy_id: external-actions\nactions:\n  financial_order: blocked\n  git_remote_write: explicit_authorization\n",
		"deployment/production.yaml":  "schema_version: 1\npolicy_id: production\nprofile: production\ndisabled_capabilities:\n  - financial_order\n  - git_remote_write\n",
		"deployment/development.yaml": "schema_version: 1\npolicy_id: development\nprofile: development\ndisabled_capabilities:\n  - financial_order\n",
	}
	for path, content := range overrides {
		files[path] = content
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	entries := make([]string, 0, len(paths))
	contentInput := strings.Builder{}
	for _, path := range paths {
		content := files[path]
		sum := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
		entries = append(entries, fmt.Sprintf("  - path: %s\n    sha256: %s\n", path, sum))
		contentInput.WriteString(path)
		contentInput.WriteByte('\n')
		contentInput.WriteString(sum)
		contentInput.WriteByte('\n')
	}
	manifest := fmt.Sprintf(
		"schema_version: 1\nbundle_id: rencrow-default\nrevision: 2026-08-06.1\ncreated_at: 2026-08-06T00:00:00Z\nminimum_core_contract: global-policy/v1\ncontent_sha256: %x\nfiles:\n%s",
		sha256.Sum256([]byte(contentInput.String())), strings.Join(entries, ""),
	)
	if err := os.MkdirAll(filepath.Join(root, "deployment"), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, content := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}
