package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
)

// The actions commands on a store with segments (layout v2): verify must read
// every segment, and `layout show` and `layout concat` tell and undo what the
// rotation did.

// moveActionStoreToSegments opens the store at root with rotation on, which moves
// a layout v1 store to layout v2.
func moveActionStoreToSegments(t *testing.T, root string) {
	t.Helper()
	if _, err := actionpersistence.NewJSONLStoreWithOptions(root, actionpersistence.OpenOptions{Index: true, Rotation: true}); err != nil {
		t.Fatalf("move the store to segments: %v", err)
	}
}

// makeActionStoreRemovable makes the read-only files of a frozen store removable
// again when the test ends.
func makeActionStoreRemovable(t *testing.T, root string) {
	t.Helper()
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err == nil && !entry.IsDir() {
				_ = os.Chmod(path, 0o644)
			}
			return nil
		})
	})
}

func TestActionsLayoutShowSaysWhatTheStoreIsMadeOf(t *testing.T) {
	root := seedActionStoreForCLI(t, nil, 4)
	makeActionStoreRemovable(t, root)
	var before actionpersistence.LayoutReport
	code, out, errOut := runActions(t, []string{"layout", "show"}, root, "")
	if code != 0 {
		t.Fatalf("layout show exit %d, stderr %q", code, errOut)
	}
	if err := json.Unmarshal([]byte(out), &before); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if before.Layout != "v1" || len(before.Segments) != 1 || before.Segments[0].Name != "legacy" || before.Segments[0].Bytes == 0 {
		t.Fatalf("layout of a plain store = %+v", before)
	}

	moveActionStoreToSegments(t, root)
	var after actionpersistence.LayoutReport
	code, out, errOut = runActions(t, []string{"layout", "show"}, root, "")
	if code != 0 {
		t.Fatalf("layout show exit %d, stderr %q", code, errOut)
	}
	if err := json.Unmarshal([]byte(out), &after); err != nil {
		t.Fatal(err)
	}
	if after.Layout != "v2" || len(after.Segments) != 2 || !after.Segment0MatchesLayout || after.Segments[0].Bytes != before.Segments[0].Bytes {
		t.Fatalf("layout of a store on segments = %+v (before: %+v)", after, before)
	}
	if strings.Contains(out, "act_") || strings.Contains(out, "task_") {
		t.Fatalf("the report carries an identifier:\n%s", out)
	}
}

func TestActionsLayoutConcatDescribesThenReturnsTheStore(t *testing.T) {
	root := seedActionStoreForCLI(t, nil, 5)
	makeActionStoreRemovable(t, root)
	moveActionStoreToSegments(t, root)
	logsBefore := filepath.Join(root, "logs")
	if _, err := os.Stat(logsBefore); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runActions(t, []string{"layout", "concat"}, root, "")
	if code != 0 {
		t.Fatalf("concat plan exit %d, stderr %q", code, errOut)
	}
	var plan actionpersistence.ConcatReport
	if err := json.Unmarshal([]byte(out), &plan); err != nil || plan.State != "plan" {
		t.Fatalf("plan = %+v (%v)\n%s", plan, err, out)
	}
	if _, err := os.Stat(logsBefore); err != nil {
		t.Fatal("a plan changed the store")
	}

	code, out, errOut = runActions(t, []string{"layout", "concat", "--apply"}, root, "")
	if code != 0 {
		t.Fatalf("concat exit %d, stderr %q", code, errOut)
	}
	var done actionpersistence.ConcatReport
	if err := json.Unmarshal([]byte(out), &done); err != nil || done.State != "done" {
		t.Fatalf("report = %+v (%v)\n%s", done, err, out)
	}
	code, out, _ = runActions(t, []string{"layout", "show"}, root, "")
	var shown actionpersistence.LayoutReport
	if code != 0 || json.Unmarshal([]byte(out), &shown) != nil || shown.Layout != "v1" {
		t.Fatalf("after the concat the layout is %q (exit %d)", shown.Layout, code)
	}
	if code, _, errOut := runActions(t, []string{"verify"}, root, ""); code != 0 {
		t.Fatalf("verify after the concat exit %d, stderr %q", code, errOut)
	}
}

func TestActionsLayoutRejectsWhatItDoesNotUnderstand(t *testing.T) {
	root := seedActionStoreForCLI(t, nil, 1)
	for _, args := range [][]string{
		{"layout"}, {"layout", "bogus"}, {"layout", "show", "extra"}, {"layout", "concat", "--force"},
	} {
		if code, _, errOut := runActions(t, args, root, ""); code == 0 || errOut == "" {
			t.Errorf("%v: exit %d, stderr %q; want a failure that says why", args, code, errOut)
		}
	}
}

func TestActionsVerifyReadsEverySegment(t *testing.T) {
	root := seedActionStoreForCLI(t, nil, 6)
	makeActionStoreRemovable(t, root)
	moveActionStoreToSegments(t, root)
	code, out, errOut := runActions(t, []string{"verify"}, root, "")
	if code != 0 {
		t.Fatalf("verify exit %d, stderr %q", code, errOut)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if report["layout"] != "v2" || report["actions"] != float64(6) {
		t.Fatalf("verify = %v, want layout v2 and 6 Actions", report)
	}
}
