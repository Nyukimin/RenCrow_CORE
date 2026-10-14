package action

import (
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// Failure knowledge: docs/調査/20261013_230400_ActionStore日次segment回転_FailureKnowledge.md
// (CHK-1, CHK-2).
//
// The production process opens the Action store with the snapshot and with
// rotation. A package that opens the store with the plain constructor gets the
// snapshot only in a build made with -tags actionindexdefault (snapshot_default_on.go),
// so a consumer's tests prove nothing about the production path unless they are
// also run with that tag. `go test ./...` of the Canonical Test Plan does not use
// the tag. The plan therefore has a step that does, and this test holds the step
// to the packages that import the store: a new consumer that is not in the step
// fails here, instead of running only against the path production does not use.
//
// It is a guard on the plan and the imports, not a run of the step.

const (
	actionStorePackageImport = "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	snapshotDefaultTag       = "actionindexdefault"
	snapshotDefaultStepName  = "action-store-snapshot-default"
)

type planStep struct {
	Name      string   `json:"name"`
	FilePath  string   `json:"filePath"`
	Arguments []string `json:"arguments"`
	Platform  []string `json:"platform"`
	TestFiles []string `json:"testFiles"`
	Tier      string   `json:"tier"`
	Impact    struct {
		Packages []string `json:"packages"`
	} `json:"impact"`
}

func planRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test file")
		}
		dir = parent
	}
}

// packagesImportingTheActionStore lists, repo-relative and slash-separated, the
// directories with a Go file (test files included) that imports the store.
func packagesImportingTheActionStore(t *testing.T, root string) []string {
	t.Helper()
	found := map[string]struct{}{}
	err := filepath.WalkDir(root, func(p string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			// A file that goes away while the tree is walked (a test's temporary
			// directory, a build cache) is not a Go package of the repository.
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if p != root && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			switch name {
			case "vendor", "node_modules", "tmp", "Tmp", "workspace", "docs", "logs", "data":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range parsed.Imports {
			if strings.Trim(spec.Path.Value, `"`) == actionStorePackageImport {
				rel, err := filepath.Rel(root, filepath.Dir(p))
				if err != nil {
					return err
				}
				found[filepath.ToSlash(rel)] = struct{}{}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(found))
	for dir := range found {
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}

// covers reports whether a go package pattern of the plan ("./dir" or "./dir/...")
// names the directory.
func covers(pattern, dir string) bool {
	pattern = strings.TrimPrefix(pattern, "./")
	if strings.HasSuffix(pattern, "/...") {
		base := strings.TrimSuffix(pattern, "/...")
		return dir == base || strings.HasPrefix(dir, base+"/")
	}
	return path.Clean(pattern) == dir
}

func loadSnapshotDefaultStep(t *testing.T, root string) planStep {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "scripts", "test-local.plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Steps []planStep `json:"steps"`
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	for _, step := range plan.Steps {
		if step.Name == snapshotDefaultStepName {
			return step
		}
	}
	t.Fatalf("scripts/test-local.plan.json has no step %q: the tests of the packages that use the Action store are never run with the snapshot on (-tags %s), which is how production opens it", snapshotDefaultStepName, snapshotDefaultTag)
	return planStep{}
}

func TestCanonicalTestPlanRunsTheActionStoreConsumersWithTheSnapshotOn(t *testing.T) {
	root := planRepoRoot(t)
	step := loadSnapshotDefaultStep(t, root)

	if step.FilePath != "go" || len(step.Arguments) < 4 || step.Arguments[0] != "test" {
		t.Fatalf("step %q runs %s %v, want go test ...", step.Name, step.FilePath, step.Arguments)
	}
	tagged := false
	for i, arg := range step.Arguments {
		if arg == "-tags" && i+1 < len(step.Arguments) && step.Arguments[i+1] == snapshotDefaultTag {
			tagged = true
		}
	}
	if !tagged {
		t.Fatalf("step %q does not pass -tags %s: %v", step.Name, snapshotDefaultTag, step.Arguments)
	}
	for _, platform := range step.Platform {
		if platform == "windows" {
			t.Fatalf("step %q lists windows; the go-regression step it follows does not run there either", step.Name)
		}
	}

	// The packages the step runs are the arguments after the flags.
	var patterns []string
	for i := 1; i < len(step.Arguments); i++ {
		arg := step.Arguments[i]
		switch {
		case arg == "-tags" || arg == "-timeout":
			i++
		case strings.HasPrefix(arg, "-"):
		default:
			patterns = append(patterns, arg)
		}
	}
	if len(patterns) == 0 {
		t.Fatalf("step %q names no package", step.Name)
	}

	var missing, notPlanned []string
	for _, dir := range packagesImportingTheActionStore(t, root) {
		inArguments, inImpact := false, false
		for _, pattern := range patterns {
			inArguments = inArguments || covers(pattern, dir)
		}
		for _, pattern := range step.Impact.Packages {
			inImpact = inImpact || covers(pattern, dir)
		}
		if !inArguments {
			missing = append(missing, dir)
		}
		if !inImpact {
			notPlanned = append(notPlanned, dir)
		}
	}
	if len(missing) > 0 {
		t.Errorf("packages that import the Action store are not run by step %q with -tags %s (add them to scripts/test-local.plan.json): %v", step.Name, snapshotDefaultTag, missing)
	}
	if len(notPlanned) > 0 {
		t.Errorf("packages that import the Action store are not in impact.packages of step %q, so a change to them would not select it: %v", step.Name, notPlanned)
	}
}
