package worldread

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// Failure knowledge: docs/world-r08-r10-open-decisions.md D-27 ("CORE read
// bridgeのproduction route／DTO") and docs/06_Public_API仕様.md.
//
// Failure / Problem: the R04 mock PoC serves GET /viewer/world/snapshot and
// GET /viewer/world/stream, and this package is a source-level read client. The
// rules that keep them apart - the PoC routes must not be promoted to a
// production route, and this client must stay source-level until CORE's
// production read bridge route/DTO is decided - were recorded only in prose.
//
// Cause: an invariant about an undecided boundary had no enforcement, so a later
// change could wire a reader into a route, hand the WORLD reader credential's
// result to a browser-facing DTO, and present the R04 mock shape as the adopted
// cross-module contract. That decision belongs to D-27 with the canonical
// route/port decision in D-14, private-room redaction, and Observation delivery.
//
// Lesson / Invariant: while D-27's production route/DTO stays undecided, no
// production file outside the R04 PoC owners may name a PoC world route, and no
// production file outside this client package may import this package. A browser
// never receives a WORLD reader credential, DB path, or private Memory reference,
// and the R04 mock DTO never becomes the production DTO.
//
// Enforcement: this test scans the repository's production sources. It is
// syntactic, so both allowlists are part of the contract; wiring needs an entry
// naming the decision it closed, and a stale entry fails.
//
// Tests: TestPoCWorldRoutesStayOffProductionRoutes and
// TestWorldReadClientStaysSourceLevel (current tree, empty promotion
// allowlists) and TestReadBridgeGuardDetectsWiring (the scan is not vacuous).

// pocWorldRouteMarkers name the R04 mock PoC routes that must not become
// production routes.
var pocWorldRouteMarkers = []string{
	"/viewer/world/snapshot",
	"/viewer/world/stream",
}

// worldreadImportMarker is the import path that makes a production file a
// consumer of the source-level read client.
const worldreadImportMarker = "github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"

// pocWorldRouteOwners declare the R04 PoC routes, so they are the only
// production files allowed to name them today.
var pocWorldRouteOwners = map[string]bool{
	"cmd/rencrow-world-poc-fixture/main.go": true,
	"internal/adapter/viewer/world_poc.go":  true,
}

// pocRoutePromotionAllowlist maps a repo-relative production file to the reason
// it may name a PoC world route. It is empty while D-27's production route/DTO
// stays undecided, and every entry must name the decision it closed.
var pocRoutePromotionAllowlist = map[string]string{}

// readBridgeConsumerAllowlist maps a repo-relative production file outside this
// package to the reason it may consume the read client. It is empty while
// D-27's production route/DTO stays undecided, and every entry must name the
// decision it closed.
var readBridgeConsumerAllowlist = map[string]string{}

func TestPoCWorldRoutesStayOffProductionRoutes(t *testing.T) {
	root := coreRepoRoot(t)
	violations, err := pocWorldRouteViolations(root)
	if err != nil {
		t.Fatalf("scan %s: %v", root, err)
	}
	if len(violations) > 0 {
		t.Errorf("production code promotes an R04 PoC world route; D-27's production read bridge route/DTO must be decided first:\n%s",
			strings.Join(violations, "\n"))
	}
	for path := range pocRoutePromotionAllowlist {
		t.Errorf("stale PoC route allowlist entry %q names a file that no longer names a PoC world route", path)
	}
}

func TestWorldReadClientStaysSourceLevel(t *testing.T) {
	root := coreRepoRoot(t)
	violations, err := readBridgeConsumerViolations(root)
	if err != nil {
		t.Fatalf("scan %s: %v", root, err)
	}
	if len(violations) > 0 {
		t.Errorf("production code consumes the WORLD read client; D-27's production read bridge route/DTO must be decided first:\n%s",
			strings.Join(violations, "\n"))
	}
	for path := range readBridgeConsumerAllowlist {
		t.Errorf("stale read bridge allowlist entry %q names a file that no longer imports the read client", path)
	}
}

func TestReadBridgeAllowlistsNameTheirDecision(t *testing.T) {
	for path, reason := range pocRoutePromotionAllowlist {
		requireDecisionReference(t, "PoC route", path, reason)
	}
	for path, reason := range readBridgeConsumerAllowlist {
		requireDecisionReference(t, "read bridge", path, reason)
	}
}

func TestReadBridgeGuardDetectsWiring(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, root, "go.mod", "module github.com/Nyukimin/RenCrow_CORE\n")
	writeFixtureFile(t, root, "internal/adapter/viewer/world_poc.go", "package viewer\n\nconst pocRoute = \"/viewer/world/snapshot\"\n")
	writeFixtureFile(t, root, "internal/adapter/viewer/world_poc_test.go", "package viewer\n\nconst pocRouteTest = \"/viewer/world/stream\"\n")
	writeFixtureFile(t, root, "internal/adapter/worldread/client.go", "package worldread\n")
	writeFixtureFile(t, root, "cmd/rencrow/runtime_world_read_bridge.go",
		"package main\n\nimport _ \"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread\"\n\nconst streamRoute = \"/viewer/world/stream\"\n")

	routes, err := pocWorldRouteViolations(root)
	if err != nil {
		t.Fatalf("scan fixture routes: %v", err)
	}
	if len(routes) != 1 || !strings.Contains(routes[0], "cmd/rencrow/runtime_world_read_bridge.go") || !strings.Contains(routes[0], "/viewer/world/stream") {
		t.Fatalf("route violations = %v, want only the wiring in cmd/rencrow/runtime_world_read_bridge.go", routes)
	}

	consumers, err := readBridgeConsumerViolations(root)
	if err != nil {
		t.Fatalf("scan fixture consumers: %v", err)
	}
	if len(consumers) != 1 || !strings.Contains(consumers[0], "cmd/rencrow/runtime_world_read_bridge.go") || !strings.Contains(consumers[0], worldreadImportMarker) {
		t.Fatalf("consumer violations = %v, want only the import in cmd/rencrow/runtime_world_read_bridge.go", consumers)
	}
}

func requireDecisionReference(t *testing.T, kind string, path string, reason string) {
	t.Helper()
	if !strings.Contains(reason, "D-27") || !strings.Contains(reason, "D-14") {
		t.Errorf("%s allowlist entry %q must name D-27's production route/DTO decision and D-14's canonical route decision; reason = %q", kind, path, reason)
	}
}

// pocWorldRouteViolations lists every production file under root that names an
// R04 PoC world route outside the PoC owners and outside the promotion
// allowlist. Test sources are not production routes.
func pocWorldRouteViolations(root string) ([]string, error) {
	var violations []string
	err := walkProductionGo(root, func(rel string, content string) error {
		if pocWorldRouteOwners[rel] {
			return nil
		}
		reason, allowed := pocRoutePromotionAllowlist[rel]
		_ = reason
		for _, marker := range pocWorldRouteMarkers {
			if !strings.Contains(content, marker) {
				continue
			}
			if allowed {
				return nil
			}
			violations = append(violations, fmt.Sprintf("%s names %s", rel, marker))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(violations)
	return violations, nil
}

// readBridgeConsumerViolations lists every production file under root, outside
// this client package, that imports the read client outside the allowlist.
func readBridgeConsumerViolations(root string) ([]string, error) {
	var violations []string
	err := walkProductionGo(root, func(rel string, content string) error {
		if strings.HasPrefix(rel, "internal/adapter/worldread/") {
			return nil
		}
		if !strings.Contains(content, worldreadImportMarker) {
			return nil
		}
		if _, allowed := readBridgeConsumerAllowlist[rel]; allowed {
			return nil
		}
		violations = append(violations, fmt.Sprintf("%s imports %s", rel, worldreadImportMarker))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(violations)
	return violations, nil
}

// walkProductionGo visits production Go sources (non-test files) under root,
// skipping vendored, cached, generated, and tool trees. The test runner keeps
// its build cache and receipts under a case-varying tmp tree, and that tree
// holds generated `_testmain.go` files that import the package under test, so
// those trees are skipped by name (case-insensitive) and by the leading
// underscore or dot Go itself ignores.
func walkProductionGo(root string, visit func(rel string, content string) error) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if skipNonSourceDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "_testmain.go" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 1<<20 {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return visit(filepath.ToSlash(rel), string(content))
	})
}

func writeFixtureFile(t *testing.T, root string, rel string, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func coreRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test file")
		}
		dir = parent
	}
}

// skipNonSourceDir reports a directory that never holds repository source:
// tool caches, test runner trees, vendored dependencies, and the leading
// underscore or dot trees Go excludes from package resolution.
func skipNonSourceDir(name string) bool {
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return true
	}
	switch strings.ToLower(name) {
	case "bin", "dist", "gocache", "gomodcache", "node_modules", "playwright", "test-results", "test-runtime", "tmp", "vendor", "work":
		return true
	}
	return false
}
