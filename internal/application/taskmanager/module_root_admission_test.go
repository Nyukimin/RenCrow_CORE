package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestTaskAdmissionBlocksOverlappingModuleRootsAcrossIDs(t *testing.T) {
	for _, tc := range []struct {
		name          string
		firstRoot     func(string) string
		candidateRoot func(string) string
	}{
		{"same root", func(root string) string { return root }, func(root string) string { return root }},
		{"candidate nested", func(root string) string { return root }, func(root string) string { return filepath.Join(root, "nested") }},
		{"candidate parent", func(root string) string { return filepath.Join(root, "nested") }, func(root string) string { return root }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limits := DefaultParallelLimits()
			limits.Global, limits.PerModule, limits.CodingTasks = 4, 2, 4
			manager := newCreateAndStartManager(t, limits)
			moduleTree := t.TempDir()
			firstRoot, candidateRoot := tc.firstRoot(moduleTree), tc.candidateRoot(moduleTree)
			if err := os.MkdirAll(firstRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(candidateRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			if _, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
				Title: "first module write", Route: domaintask.RouteCode, ModuleID: "module-a", ModuleRoot: firstRoot,
			}, domaintask.SharedRoleContext{}); err != nil {
				t.Fatalf("start first module write: %v", err)
			}
			_, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
				Title: "overlapping module write", Route: domaintask.RouteCode, ModuleID: "module-b", ModuleRoot: candidateRoot,
			}, domaintask.SharedRoleContext{})
			if !errors.Is(err, ErrParallelLimit) {
				t.Fatalf("overlapping root admission error = %v, want ErrParallelLimit", err)
			}
			assertStoreCounts(t, manager, 1, 1)
		})
	}
}

func TestTaskAdmissionAllowsReadOnlyTaskOnOverlappingModuleRoot(t *testing.T) {
	limits := DefaultParallelLimits()
	limits.Global, limits.CodingTasks = 4, 4
	manager := newCreateAndStartManager(t, limits)
	root := t.TempDir()
	if _, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "module write", Route: domaintask.RouteCode, ModuleID: "module-a", ModuleRoot: root,
	}, domaintask.SharedRoleContext{}); err != nil {
		t.Fatalf("start module write: %v", err)
	}
	if _, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "module read", Route: domaintask.RouteCode, ModuleID: "module-b", ModuleRoot: filepath.Join(root, "nested"), ReadOnly: true,
	}, domaintask.SharedRoleContext{}); err != nil {
		t.Fatalf("start read-only task on overlapping root: %v", err)
	}
}

func TestTaskAdmissionBlocksSameModuleWritesWithDistinctRoots(t *testing.T) {
	limits := DefaultParallelLimits()
	limits.Global, limits.PerModule, limits.CodingTasks = 4, 2, 4
	manager := newCreateAndStartManager(t, limits)
	moduleTree := t.TempDir()
	firstRoot := filepath.Join(moduleTree, "first")
	candidateRoot := filepath.Join(moduleTree, "second")
	for _, root := range []string{firstRoot, candidateRoot} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "first same-module write", Route: domaintask.RouteCode, ModuleID: "module-a", ModuleRoot: firstRoot,
	}, domaintask.SharedRoleContext{}); err != nil {
		t.Fatalf("start first same-module write: %v", err)
	}
	_, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "overlapping same-module write", Route: domaintask.RouteCode, ModuleID: "module-a", ModuleRoot: candidateRoot,
	}, domaintask.SharedRoleContext{})
	if !errors.Is(err, ErrParallelLimit) {
		t.Fatalf("same-module write admission error = %v, want ErrParallelLimit", err)
	}
	assertStoreCounts(t, manager, 1, 1)
}

func TestModuleRootsOverlapResolvesSymlinkAliasesAndMissingLeaves(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	alias := filepath.Join(base, "alias")
	if err := os.MkdirAll(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realRoot, alias); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	overlap, err := moduleRootsOverlap(filepath.Join(realRoot, "missing"), filepath.Join(alias, "missing", "child"))
	if err != nil {
		t.Fatalf("moduleRootsOverlap: %v", err)
	}
	if !overlap {
		t.Fatal("symlinked module roots with missing nested leaves were treated as disjoint")
	}
}

func TestModuleRootsOverlapUsesPathComponents(t *testing.T) {
	base := t.TempDir()
	overlap, err := moduleRootsOverlap(filepath.Join(base, "module"), filepath.Join(base, "module-copy"))
	if err != nil {
		t.Fatalf("moduleRootsOverlap: %v", err)
	}
	if overlap {
		t.Fatal("sibling path with a shared string prefix was treated as nested")
	}
}

func TestTaskAdmissionCountsFourDelegatedCodingLeavesAndRejectsFifth(t *testing.T) {
	manager := newCreateAndStartManager(t, ParallelLimits{Global: 4, PerModule: 1, CodingTasks: 4, LongResearchTasks: 1, DestructiveTasks: 1})
	root := startAdmissionTask(t, manager, domaintask.Task{Title: "coordination root", Route: domaintask.RouteGeneral})
	for index, route := range []domaintask.Route{domaintask.RouteCODE1, domaintask.RouteCODE2, domaintask.RouteCODE3, domaintask.RouteCODE4} {
		startAdmissionTask(t, manager, domaintask.Task{
			Title: fmt.Sprintf("coder child %d", index+1), Route: route,
			ParentTaskID: root.TaskID, ModuleID: fmt.Sprintf("module-%d", index+1), ModuleRoot: filepath.Join(t.TempDir(), fmt.Sprintf("module-%d", index+1)),
		})
	}
	assertRunningTaskCount(t, manager, 5)

	_, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "fifth coder child", Route: domaintask.RouteCode,
		ParentTaskID: root.TaskID, ModuleID: "module-5", ModuleRoot: filepath.Join(t.TempDir(), "module-5"),
	}, domaintask.SharedRoleContext{})
	if !errors.Is(err, ErrParallelLimit) {
		t.Fatalf("fifth coding child error = %v, want parallel limit refusal", err)
	}
	assertStoreCounts(t, manager, 5, 5)
}

func TestTaskAdmissionCodingRouteVariantsShareOneLimit(t *testing.T) {
	manager := newCreateAndStartManager(t, ParallelLimits{Global: 8, PerModule: 1, CodingTasks: 4, LongResearchTasks: 1, DestructiveTasks: 1})
	for index, route := range []domaintask.Route{domaintask.RouteCode, domaintask.RouteCODE1, domaintask.RouteCODE2, domaintask.RouteCODE3} {
		startAdmissionTask(t, manager, domaintask.Task{
			Title: fmt.Sprintf("coding route %d", index+1), Route: route,
			ModuleID: fmt.Sprintf("module-%d", index+1), ModuleRoot: filepath.Join(t.TempDir(), fmt.Sprintf("module-%d", index+1)),
		})
	}
	_, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "fifth route variant", Route: domaintask.RouteCODE4,
		ModuleID: "module-5", ModuleRoot: filepath.Join(t.TempDir(), "module-5"),
	}, domaintask.SharedRoleContext{})
	if !errors.Is(err, ErrParallelLimit) {
		t.Fatalf("CODE4 admission error = %v, want shared coding limit refusal", err)
	}
	assertRunningTaskCount(t, manager, 4)
}

func TestTaskAdmissionCountsTwoSiblingChildrenAsTwoSlots(t *testing.T) {
	manager := newCreateAndStartManager(t, ParallelLimits{Global: 2, PerModule: 1, CodingTasks: 2, LongResearchTasks: 1, DestructiveTasks: 1})
	root := startAdmissionTask(t, manager, domaintask.Task{Title: "sibling root", Route: domaintask.RouteGeneral})
	for index := 0; index < 2; index++ {
		startAdmissionTask(t, manager, domaintask.Task{
			Title: fmt.Sprintf("sibling child %d", index+1), Route: domaintask.RouteCode,
			ParentTaskID: root.TaskID, ModuleID: fmt.Sprintf("sibling-module-%d", index+1),
			ModuleRoot: filepath.Join(t.TempDir(), fmt.Sprintf("sibling-module-%d", index+1)),
		})
	}
	assertRunningTaskCount(t, manager, 3)
	_, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "third sibling child", Route: domaintask.RouteCode,
		ParentTaskID: root.TaskID, ModuleID: "sibling-module-3", ModuleRoot: filepath.Join(t.TempDir(), "sibling-module-3"),
	}, domaintask.SharedRoleContext{})
	if !errors.Is(err, ErrParallelLimit) {
		t.Fatalf("third sibling child error = %v, want global limit refusal", err)
	}
}

func TestTaskAdmissionCountsDirectAndDelegatedLeavesTogether(t *testing.T) {
	manager := newCreateAndStartManager(t, ParallelLimits{Global: 2, PerModule: 1, CodingTasks: 2, LongResearchTasks: 1, DestructiveTasks: 1})
	directRoot := startAdmissionTask(t, manager, domaintask.Task{Title: "direct root", Route: domaintask.RouteGeneral})
	delegatedRoot := startAdmissionTask(t, manager, domaintask.Task{Title: "delegated root", Route: domaintask.RouteGeneral})
	startAdmissionTask(t, manager, domaintask.Task{
		Title: "delegated leaf", Route: domaintask.RouteCode, ParentTaskID: delegatedRoot.TaskID,
		ModuleID: "delegated-module", ModuleRoot: filepath.Join(t.TempDir(), "delegated-module"),
	})
	startAdmissionTask(t, manager, domaintask.Task{
		Title: "direct child leaf", Route: domaintask.RouteCode, ParentTaskID: directRoot.TaskID,
		ModuleID: "direct-module", ModuleRoot: filepath.Join(t.TempDir(), "direct-module"),
	})
	assertRunningTaskCount(t, manager, 4)
	_, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "unparented fifth leaf", Route: domaintask.RouteGeneral,
	}, domaintask.SharedRoleContext{})
	if !errors.Is(err, ErrParallelLimit) {
		t.Fatalf("fifth mixed leaf error = %v, want global limit refusal", err)
	}
}

func TestTaskAdmissionRejectsAnotherParentsLeafOnSameOrOverlappingRoot(t *testing.T) {
	for _, tc := range []struct {
		name          string
		candidateRoot func(string) string
	}{
		{"same root", func(root string) string { return root }},
		{"overlapping root", func(root string) string { return filepath.Join(root, "nested") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := newCreateAndStartManager(t, ParallelLimits{Global: 4, PerModule: 1, CodingTasks: 4, LongResearchTasks: 1, DestructiveTasks: 1})
			sharedRoot := t.TempDir()
			otherParent := startAdmissionTask(t, manager, domaintask.Task{Title: "first parent", Route: domaintask.RouteGeneral})
			startAdmissionTask(t, manager, domaintask.Task{
				Title: "other parent's running leaf", Route: domaintask.RouteCode, ParentTaskID: otherParent.TaskID,
				ModuleID: "module-a", ModuleRoot: sharedRoot,
			})
			candidateParent := startAdmissionTask(t, manager, domaintask.Task{Title: "candidate parent", Route: domaintask.RouteGeneral})
			_, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
				Title: "overlapping child write", Route: domaintask.RouteCode, ParentTaskID: candidateParent.TaskID,
				ModuleID: "module-b", ModuleRoot: tc.candidateRoot(sharedRoot),
			}, domaintask.SharedRoleContext{})
			if !errors.Is(err, ErrParallelLimit) {
				t.Fatalf("overlapping leaf admission error = %v, want PerModule refusal", err)
			}
		})
	}
}

func TestTaskAdmissionRootChildGrandchildUsesOneLeafSlot(t *testing.T) {
	manager := newCreateAndStartManager(t, ParallelLimits{Global: 1, PerModule: 1, CodingTasks: 1, LongResearchTasks: 1, DestructiveTasks: 1})
	root := startAdmissionTask(t, manager, domaintask.Task{Title: "root", Route: domaintask.RouteGeneral})
	child := startAdmissionTask(t, manager, domaintask.Task{
		Title: "child", Route: domaintask.RouteCode, ParentTaskID: root.TaskID,
		ModuleID: "child-module", ModuleRoot: filepath.Join(t.TempDir(), "child-module"),
	})
	startAdmissionTask(t, manager, domaintask.Task{
		Title: "grandchild", Route: domaintask.RouteCODE1, ParentTaskID: child.TaskID,
		ModuleID: "grandchild-module", ModuleRoot: filepath.Join(t.TempDir(), "grandchild-module"),
	})
	assertRunningTaskCount(t, manager, 3)
}

func TestTaskAdmissionRestoresParentSlotAfterLastChildAndFreesOnParentTerminal(t *testing.T) {
	for _, childTerminal := range []string{"succeed", "cancel"} {
		for _, parentTerminal := range []string{"succeed", "cancel"} {
			t.Run(childTerminal+"_"+parentTerminal, func(t *testing.T) {
				manager := newCreateAndStartManager(t, ParallelLimits{Global: 1, PerModule: 1, CodingTasks: 1, LongResearchTasks: 1, DestructiveTasks: 1})
				root := startAdmissionTask(t, manager, domaintask.Task{Title: "terminal test root", Route: domaintask.RouteGeneral})
				child := startAdmissionTask(t, manager, domaintask.Task{Title: "terminal test child", Route: domaintask.RouteCode, ParentTaskID: root.TaskID})
				if err := endAdmissionTask(manager, child.TaskID, childTerminal); err != nil {
					t.Fatalf("end child: %v", err)
				}
				_, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{Title: "blocked by resumed parent", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
				if !errors.Is(err, ErrParallelLimit) {
					t.Fatalf("task admission while parent resumed: %v, want parallel limit refusal", err)
				}
				if err := endAdmissionTask(manager, root.TaskID, parentTerminal); err != nil {
					t.Fatalf("end parent: %v", err)
				}
				startAdmissionTask(t, manager, domaintask.Task{Title: "admitted after parent terminal", Route: domaintask.RouteGeneral})
				assertRunningTaskCount(t, manager, 1)
			})
		}
	}
}

func TestRunningTaskCoordinationParentsRejectsCyclesAndDuplicateIDs(t *testing.T) {
	firstID, secondID := modulecore.NewTaskID(), modulecore.NewTaskID()
	cycle := []domaintask.Task{
		{TaskID: firstID, ParentTaskID: secondID},
		{TaskID: secondID, ParentTaskID: firstID},
	}
	if _, err := runningTaskCoordinationParents(cycle, domaintask.Task{}); err == nil {
		t.Fatal("running parent cycle was accepted")
	}
	duplicate := []domaintask.Task{{TaskID: firstID}, {TaskID: firstID}}
	if _, err := runningTaskCoordinationParents(duplicate, domaintask.Task{}); err == nil {
		t.Fatal("duplicate running Task IDs were accepted")
	}
}

func TestConcurrentTaskStartsRespectGlobalLeafLimit(t *testing.T) {
	manager := newCreateAndStartManager(t, ParallelLimits{Global: 4, PerModule: 1, CodingTasks: 12, LongResearchTasks: 1, DestructiveTasks: 1})
	const attempts = 12
	var wait sync.WaitGroup
	var mu sync.Mutex
	accepted, refused := 0, 0
	unexpected := make([]error, 0)
	for index := 0; index < attempts; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
				Title: fmt.Sprintf("concurrent coding task %d", index), Route: domaintask.RouteCode,
				ModuleID: fmt.Sprintf("module-%d", index), ModuleRoot: filepath.Join(t.TempDir(), fmt.Sprintf("module-%d", index)),
			}, domaintask.SharedRoleContext{})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				accepted++
			} else if errors.Is(err, ErrParallelLimit) {
				refused++
			} else {
				unexpected = append(unexpected, err)
			}
		}(index)
	}
	wait.Wait()
	if len(unexpected) > 0 {
		t.Fatalf("unexpected concurrent start error: %v", unexpected[0])
	}
	if accepted != 4 || refused != attempts-4 {
		t.Fatalf("accepted=%d refused=%d, want 4 and %d", accepted, refused, attempts-4)
	}
	assertStoreCounts(t, manager, 4, 4)
}

func startAdmissionTask(t *testing.T, manager *Manager, task domaintask.Task) domaintask.Task {
	t.Helper()
	started, _, err := manager.CreateAndStartRun(context.Background(), task, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create and start Task %q: %v", task.Title, err)
	}
	return started
}

func assertRunningTaskCount(t *testing.T, manager *Manager, want int) {
	t.Helper()
	tasks, err := manager.List(context.Background(), domaintask.Filter{Status: domaintask.StatusRunning, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != want {
		t.Fatalf("running Task count = %d, want %d", len(tasks), want)
	}
}

func endAdmissionTask(manager *Manager, taskID modulecore.TaskID, terminal string) error {
	switch terminal {
	case "succeed":
		_, err := manager.Succeed(context.Background(), taskID, "test complete")
		return err
	case "cancel":
		_, err := manager.Cancel(context.Background(), taskID, "test cancellation")
		return err
	default:
		return fmt.Errorf("unsupported terminal status %q", terminal)
	}
}
