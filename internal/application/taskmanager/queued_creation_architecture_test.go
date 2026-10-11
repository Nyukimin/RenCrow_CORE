package taskmanager

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Failure knowledge: docs/調査/*Run無しqueued_Task*_FailureKnowledge.md.
//
// A Task is created to be executed. Create persists a queued Task with no Run,
// and nothing in the runtime starts such a Task later, so every caller that
// created a Task and then asked for its first Run in a separate transaction
// left a Run-less queued Task behind whenever the second step was refused
// (about 18,000 of them in production). A caller that executes immediately
// must use Manager.CreateAndStartRun, which admits both in one transaction.
//
// This test fails when non-test code calls the three-argument Task
// Create(ctx, Task, SharedRoleContext) or declares an interface that exposes
// it, outside the intake paths listed below. It is a syntactic guard (it
// cannot see through helpers that hide the call), so the allowlist is part of
// the contract: every entry names a path that deliberately persists a queued
// Task, with the reason, and a stale entry fails the test.

const taskDomainImportSuffix = "/internal/domain/task"

// queuedTaskIntakeAllowlist lists the only places that may persist a queued
// Task that has no Run yet. key = "repo-relative path:function" for a call, or
// "repo-relative path:interface:Name" for an interface that exposes Create.
var queuedTaskIntakeAllowlist = map[string]string{
	// The user's request is recorded as the root Task at intake. Routing
	// events are published and recorded between Create and Start, and those
	// need the Task to exist; a startup failure fails the created Task.
	"internal/application/orchestrator/task_lifecycle.go:createRootForAssignee":               "root Task at intake, before routing",
	"internal/application/orchestrator/task_lifecycle.go:createExecutionChildForRootAssignee": "child Task of a delegated step, started after assignment is recorded",
	"internal/application/orchestrator/task_lifecycle.go:startRepairExecution":                "repair Task is routed and assigned before its first Run",
	"internal/application/orchestrator/task_lifecycle.go:interface:TaskLifecycleManager":      "the orchestrator intake contract above",
	// `rencrow tasks create` is an explicit operator command that records a
	// queued Task; starting it is a separate explicit command.
	"cmd/rencrow/cli_tasks.go:runTasksCommand":            "operator-created queued Task",
	"cmd/rencrow/cli_tasks.go:interface:taskCommandStore": "operator command contract above",
}

func TestQueuedTaskCreationIsLimitedToAllowlistedIntake(t *testing.T) {
	root := repositoryRoot(t)
	observed := map[string]struct{}{}
	var violations []string

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor", "node_modules", "tmp", "Tmp", "workspace", "docs", "logs", "data":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		if !importsTaskDomain(parsed) {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)

		for _, declaration := range parsed.Decls {
			switch typed := declaration.(type) {
			case *ast.FuncDecl:
				if typed.Body == nil || !callsTaskCreate(typed.Body) {
					continue
				}
				key := relative + ":" + typed.Name.Name
				observed[key] = struct{}{}
				if _, ok := queuedTaskIntakeAllowlist[key]; !ok {
					violations = append(violations, key)
				}
			case *ast.GenDecl:
				for _, spec := range typed.Specs {
					typeSpec, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					iface, ok := typeSpec.Type.(*ast.InterfaceType)
					if !ok || !exposesTaskCreate(iface) {
						continue
					}
					key := relative + ":interface:" + typeSpec.Name.Name
					observed[key] = struct{}{}
					if _, ok := queuedTaskIntakeAllowlist[key]; !ok {
						violations = append(violations, key)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("these places persist a queued Task that nothing will start; use taskmanager.Manager.CreateAndStartRun when the Task is executed at once, "+
			"or add an allowlist entry with a reason if the Task must exist before it can start:\n  %s", strings.Join(violations, "\n  "))
	}
	var stale []string
	for key := range queuedTaskIntakeAllowlist {
		if _, ok := observed[key]; !ok {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf("allowlist entries no longer match any queued-Task creation; remove them:\n  %s", strings.Join(stale, "\n  "))
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", ".."))
}

func importsTaskDomain(file *ast.File) bool {
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err == nil && strings.HasSuffix(path, taskDomainImportSuffix) {
			return true
		}
	}
	return false
}

// callsTaskCreate reports whether the body calls x.Create(ctx, task, shared):
// a method named Create with exactly three arguments.
func callsTaskCreate(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) != 3 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if ok && selector.Sel.Name == "Create" {
			found = true
		}
		return !found
	})
	return found
}

// exposesTaskCreate reports whether the interface declares
// Create(context.Context, Task, SharedRoleContext) (Task, error).
func exposesTaskCreate(iface *ast.InterfaceType) bool {
	for _, method := range iface.Methods.List {
		funcType, ok := method.Type.(*ast.FuncType)
		if !ok || len(method.Names) != 1 || method.Names[0].Name != "Create" {
			continue
		}
		var params []string
		for _, field := range funcType.Params.List {
			text := typeName(field.Type)
			for i := 0; i < max(1, len(field.Names)); i++ {
				params = append(params, text)
			}
		}
		if len(params) == 3 && params[1] == "Task" && params[2] == "SharedRoleContext" {
			return true
		}
	}
	return false
}

func typeName(expr ast.Expr) string {
	switch typed := expr.(type) {
	case *ast.Ident:
		return typed.Name
	case *ast.SelectorExpr:
		return typed.Sel.Name
	case *ast.StarExpr:
		return typeName(typed.X)
	default:
		return fmt.Sprintf("%T", expr)
	}
}
