package nativeharnessclient

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// These tests fix two boundaries of the RenCrow_Harness delegation in the
// structure of the repository (docs 04, RenCrow_Harness委譲境界):
//
//  1. The Harness module is imported by the delegation package, the test
//     deployment helper and the composition file only. The configuration, the
//     domain and the orchestrator never import it, so a change of the Harness
//     protocol types is contained, and CORE's other packages do not need the
//     Harness module to build.
//  2. The backend selection of a turn is written by one place: the admission step
//     of the orchestrator. A client request, the message text or a model output
//     cannot select the profile because nothing else can call it.

const harnessModule = "github.com/Nyukimin/RenCrow_Harness"

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve the repository root")
	}
	// .../internal/adapter/nativeharnessclient/architecture_test.go
	return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(file))))
}

// goFiles returns the repository-relative slash paths of the Go files that are
// part of CORE's own source (not the scratch, dependency or build directories).
func goFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			switch relative {
			case ".git", "Tmp", "tmp", "node_modules", "data", "workspace", "bin":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(relative, ".go") {
			files = append(files, relative)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the repository: %v", err)
	}
	return files
}

func TestOnlyTheDelegationPackageAndCompositionImportTheHarnessModule(t *testing.T) {
	root := repositoryRoot(t)
	allowed := func(relative string) bool {
		switch {
		case strings.HasPrefix(relative, "internal/adapter/nativeharnessclient/delegation/"):
			return true
		case strings.HasPrefix(relative, "internal/testsupport/harnessdeploy/"):
			return true
		case relative == "cmd/rencrow/runtime_native_harness.go", relative == "cmd/rencrow/runtime_native_harness_test.go":
			return true
		}
		return false
	}
	importers := 0
	for _, relative := range goFiles(t, root) {
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, filepath.FromSlash(relative)), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", relative, err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: import %s: %v", relative, spec.Path.Value, err)
			}
			if path != harnessModule && !strings.HasPrefix(path, harnessModule+"/") {
				continue
			}
			importers++
			if !allowed(relative) {
				t.Errorf("%s imports %s: only the delegation package, its test deployment helper and the composition file may import the Harness module", relative, path)
			}
			if strings.Contains(path, "/internal/") {
				t.Errorf("%s imports %s: CORE uses the Harness's public pkg/client and pkg/protocol only", relative, path)
			}
		}
	}
	if importers == 0 {
		t.Fatal("no importer of the Harness module was found: the walk or the allowlist is wrong")
	}
}

func TestBackendSelectionIsWrittenOnlyBySharedNativeCodingAdmission(t *testing.T) {
	root := repositoryRoot(t)
	const writer = "internal/application/orchestrator/message_orchestrator_native_coding.go"
	writers := 0
	for _, relative := range goFiles(t, root) {
		if strings.HasSuffix(relative, "_test.go") {
			continue // tests build inputs of their own
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, filepath.FromSlash(relative)), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", relative, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "WithBackendSelection" {
				return true
			}
			writers++
			if relative != writer {
				t.Errorf("%s calls WithBackendSelection: only %s may select a backend (from the shared configured admission)", relative, writer)
			}
			return true
		})
	}
	if writers != 1 {
		t.Fatalf("exactly one production call of WithBackendSelection is expected, found %d", writers)
	}
}

func TestAcceptedInputCapabilityIsAttachedOnlyByNativeOPSIngress(t *testing.T) {
	root := repositoryRoot(t)
	attachments, reads := 0, 0
	for _, relative := range goFiles(t, root) {
		if strings.HasSuffix(relative, "_test.go") {
			continue // tests attach synthetic readers directly
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, filepath.FromSlash(relative)), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", relative, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch selector.Sel.Name {
			case "WithAcceptedInputReader":
				attachments++
				if relative != "cmd/rencrow/runtime_agent_ops_native.go" {
					t.Errorf("%s attaches the owner capability: only the authenticated native OPS ingress may do so", relative)
				}
			case "AcceptedInputReaderFromContext":
				reads++
				if relative != "internal/adapter/nativeharnessclient/delegation/accepted_input.go" {
					t.Errorf("%s reads the owner capability outside the Start source builder", relative)
				}
			}
			return true
		})
	}
	if attachments != 1 || reads != 1 {
		t.Fatalf("owner capability must have one ingress attachment and one Start builder read, got attachments=%d reads=%d", attachments, reads)
	}
}
