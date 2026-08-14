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

func TestLoadWorkspaceRejectsMissingDatabaseRecallInvariant(t *testing.T) {
	workspace := t.TempDir()
	writeValidBundle(t, workspace, map[string]string{
		"data-handling.yaml": "schema_version: 1\npolicy_id: data-handling\nrules: []\n",
	})

	status := LoadWorkspace(workspace)
	if status.State != domainpolicy.StateInvalid || !strings.Contains(status.Error, "database_recall") {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestLoadWorkspaceRejectsMissingDatabaseRecallProductionRequirements(t *testing.T) {
	tests := []struct {
		name       string
		read       string
		write      string
		production string
		expected   string
	}{
		{
			name:       "owner read route omitted",
			write:      "true",
			production: "true",
			expected:   "owner_read_route_required (read routes)",
		},
		{
			name:       "owner write route omitted",
			read:       "true",
			production: "true",
			expected:   "owner_write_route_required (write routes)",
		},
		{
			name:     "agent owned production e2e omitted",
			read:     "true",
			write:    "true",
			expected: "agent_owned_production_e2e_required (Agent-owned production E2E)",
		},
		{
			name:       "owner read route false",
			read:       "false",
			write:      "true",
			production: "true",
			expected:   "owner_read_route_required (read routes)",
		},
		{
			name:       "owner write route false",
			read:       "true",
			write:      "false",
			production: "true",
			expected:   "owner_write_route_required (write routes)",
		},
		{
			name:       "agent owned production e2e false",
			read:       "true",
			write:      "true",
			production: "false",
			expected:   "agent_owned_production_e2e_required (Agent-owned production E2E)",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			writeValidBundle(t, workspace, map[string]string{
				"data-handling.yaml": databaseRecallPolicyYAML(test.read, test.write, test.production),
			})

			status := LoadWorkspace(workspace)
			if status.State != domainpolicy.StateInvalid || !strings.Contains(status.Error, test.expected) {
				t.Fatalf("expected database recall requirement %q to be rejected: %+v", test.expected, status)
			}
		})
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
		"data-handling.yaml":          "schema_version: 1\npolicy_id: data-handling\ndatabase_recall:\n  all_databases_are_recall_sources: true\n  route_required: true\n  missing_route_is_incomplete: true\n  raw_access_forbidden: true\n  catalog_wide_scan_forbidden: true\n  owner_read_route_required: true\n  owner_write_route_required: true\n  agent_owned_production_e2e_required: true\nrules: []\n",
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

func databaseRecallPolicyYAML(ownerRead, ownerWrite, productionE2E string) string {
	content := "schema_version: 1\npolicy_id: data-handling\ndatabase_recall:\n  all_databases_are_recall_sources: true\n  route_required: true\n  missing_route_is_incomplete: true\n  raw_access_forbidden: true\n  catalog_wide_scan_forbidden: true\n"
	if ownerRead != "" {
		content += "  owner_read_route_required: " + ownerRead + "\n"
	}
	if ownerWrite != "" {
		content += "  owner_write_route_required: " + ownerWrite + "\n"
	}
	if productionE2E != "" {
		content += "  agent_owned_production_e2e_required: " + productionE2E + "\n"
	}
	return content + "rules: []\n"
}
