package taskmanager

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Failure knowledge: docs/調査/*TaskStore_ID索引形式*仕様案.md, INV-8 and F-4.
//
// The Task store keeps the whole history. A caller that lists every Task or
// every Run to join them by ID decodes (or clones) the entire history on each
// call: about 7.2万 Tasks and 5.4万 Runs on the production store, and
// the startup recoveries that did this were the first thing to stall as the
// history grew. Listing without a bound is therefore a decision, not a default.
//
// This test fails when non-test code lists Tasks or Runs without a bound
// outside the places below. A call is bounded when its filter literal names a
// non-zero Limit or (for Runs) a TaskID, which is a lookup by ID. Anything else
// - an empty filter, a filter with only a status, a filter passed in as a
// variable - needs an entry that says why it is acceptable. An interface that
// exposes a list method needs an entry too, so a new consumer of the list API is
// a visible decision.
//
// It is a syntactic guard (it cannot see through helpers that hide the call),
// so the allowlist is part of the contract; a stale entry fails the test.
// Store implementations (persistence/task, persistence/storagehost) forward
// their callers' filters and the domain package declares the store contract, so
// they are not scanned; their callers are.
//
// Entry keys:
//
//	call:      "repo-relative path:function:Method{FilterKeys}" for a filter
//	           literal, or "...:Method(arg)" for a filter that is not a literal.
//	interface: "repo-relative path:interface:Name:Method".
var unboundedTaskListAllowlist = map[string]string{
	// Startup recovery of orphan Runs reads only the Runs that are still
	// running: bounded by the active set, not by the history (F-1).
	"cmd/rencrow/runtime_run_recovery.go:recoverOrphanTaskRunsAfterRestart:ListRuns{Status}": "running Runs only; the active set is small",

	// Startup recovery of Actions reads the running Runs to find the Actions it
	// acts on (the Actions of a Run that is still running), by RunID, instead of
	// resolving the Run of every Action in the history. Bounded by the active set.
	"cmd/rencrow/runtime_action_recovery.go:recoverActionRunsAfterRestart:ListRuns{Status}": "running Runs only; the active set is small",

	// Manager.List / ListRuns forward the caller's filter to the store. The
	// callers are checked at their own call sites and interfaces.
	"internal/application/taskmanager/manager.go:List:ListTasks(arg)":    "forwards the caller's filter",
	"internal/application/taskmanager/manager.go:ListRuns:ListRuns(arg)": "forwards the caller's filter",

	// The one-pass reads for a store without an ID index (see WithIDIndex).
	// They replace the callers' own full lists with a single pass behind a bounded
	// API; delete them when the ID index is the default.
	"internal/application/taskmanager/read_task_runs.go:readTaskRunsInOnePass:ListTasks{}": "one pass for a store without an ID index",
	"internal/application/taskmanager/read_task_runs.go:readTaskRunsInOnePass:ListRuns{}":  "one pass for a store without an ID index",
	"internal/application/taskmanager/read_task_runs.go:RunResolver:ListRuns{}":            "one pass for a store without an ID index",

	// The one-shot reaper of Run-less queued Tasks (reap_queued.go). A reference
	// to a Task can come from any Task of the history and there is no reverse
	// lookup, so it reads the latest version of every Task once per dry-run and
	// once per batch transaction. It runs by hand on a stopped Core, never at
	// startup or per request.
	"internal/application/taskmanager/reap_queued.go:scanTasks:ListTasks{}": "one-shot reaper: reference scan over the history",

	// Test support that asserts the whole (tiny) store.
	"internal/application/taskmanager/taskmanagertest/saturated.go:AssertNothingPersisted:ListRuns{}": "test support over a one-Task store",

	// Interfaces that expose the list API. Each consumer's calls are governed
	// above; the entry records that the consumer exists.
	"cmd/rencrow/cli_tasks.go:interface:taskCommandStore:List":                                           "operator command; the CLI passes a Limit",
	"internal/adapter/viewer/task_handler.go:interface:TaskStore:ListTasks":                              "Viewer list; passes a Limit",
	"internal/adapter/viewer/task_handler.go:interface:TaskStore:ListRuns":                               "Viewer detail; lists by TaskID",
	"internal/application/idlechat/generation_run_lifecycle.go:interface:generationRunOwner:ListRuns":    "lists by TaskID",
	"internal/application/orchestrator/task_lifecycle.go:interface:TaskLifecycleManager:ListRuns":        "lists by TaskID",
	"internal/application/superagent/run_recovery.go:interface:InterruptedRunRecoveryTaskOwner:ListRuns": "re-reads one Task by TaskID after blocking it",
}

// Directories that implement or declare the store itself.
var unboundedTaskListSkippedDirs = []string{
	"internal/infrastructure/persistence/task/",
	"internal/infrastructure/persistence/storagehost/",
	"internal/domain/task/",
}

func TestUnboundedTaskListsAreLimitedToAllowlistedPlaces(t *testing.T) {
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
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		for _, skipped := range unboundedTaskListSkippedDirs {
			if strings.HasPrefix(relative, skipped) {
				return nil
			}
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		alias, ok := taskDomainImportAlias(parsed)
		if !ok {
			return nil
		}
		record := func(key string) {
			observed[key] = struct{}{}
			if _, allowed := unboundedTaskListAllowlist[key]; !allowed {
				violations = append(violations, key)
			}
		}

		for _, declaration := range parsed.Decls {
			switch typed := declaration.(type) {
			case *ast.FuncDecl:
				if typed.Body == nil {
					continue
				}
				for _, key := range unboundedListCalls(typed.Body, alias) {
					record(relative + ":" + typed.Name.Name + ":" + key)
				}
			case *ast.GenDecl:
				for _, spec := range typed.Specs {
					typeSpec, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					iface, ok := typeSpec.Type.(*ast.InterfaceType)
					if !ok {
						continue
					}
					for _, method := range exposedTaskListMethods(iface, alias) {
						record(relative + ":interface:" + typeSpec.Name.Name + ":" + method)
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
		t.Fatalf("these places list Tasks or Runs without a bound. Read by ID (Get, GetRun, ReadTaskRuns, RunResolver), "+
			"narrow the filter (a Limit, a TaskID, running Runs only), or add an allowlist entry with the reason:\n  %s",
			strings.Join(violations, "\n  "))
	}
	var stale []string
	for key := range unboundedTaskListAllowlist {
		if _, ok := observed[key]; !ok {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf("allowlist entries no longer match any unbounded list; remove them:\n  %s", strings.Join(stale, "\n  "))
	}
}

// taskDomainImportAlias returns the local name under which the file imports the
// Task domain package.
func taskDomainImportAlias(file *ast.File) (string, bool) {
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || !strings.HasSuffix(path, taskDomainImportSuffix) {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name, true
		}
		return "task", true
	}
	return "", false
}

// unboundedListCalls returns "Method{Keys}" / "Method(arg)" for every
// ListTasks, ListRuns, or List(ctx, <alias>.Filter{...}) call in body whose
// filter is not provably bounded.
func unboundedListCalls(body *ast.BlockStmt, alias string) []string {
	var found []string
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		method := selector.Sel.Name
		switch method {
		case "ListTasks", "ListRuns":
		case "List":
			// List is a common name. Only a Task Filter literal identifies the Task list.
			literal, isLiteral := call.Args[1].(*ast.CompositeLit)
			if !isLiteral || !isDomainType(literal.Type, alias, "Filter") {
				return true
			}
		default:
			return true
		}
		literal, isLiteral := call.Args[1].(*ast.CompositeLit)
		if !isLiteral {
			found = append(found, method+"(arg)")
			return true
		}
		keys := literalKeys(literal)
		if filterBounded(method, literal, keys) {
			return true
		}
		sorted := append([]string(nil), keys...)
		sort.Strings(sorted)
		found = append(found, method+"{"+strings.Join(sorted, ",")+"}")
		return true
	})
	return found
}

func literalKeys(literal *ast.CompositeLit) []string {
	var keys []string
	for _, element := range literal.Elts {
		if pair, ok := element.(*ast.KeyValueExpr); ok {
			if name, ok := pair.Key.(*ast.Ident); ok {
				keys = append(keys, name.Name)
			}
		}
	}
	return keys
}

// filterBounded reports whether a filter literal limits the result: a Limit
// other than the literal 0, or (for Runs) a TaskID.
func filterBounded(method string, literal *ast.CompositeLit, keys []string) bool {
	for _, element := range literal.Elts {
		pair, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		name, ok := pair.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch name.Name {
		case "Limit":
			if value, ok := pair.Value.(*ast.BasicLit); ok && value.Value == "0" {
				continue
			}
			return true
		case "TaskID":
			if method == "ListRuns" {
				return true
			}
		}
	}
	return false
}

func isDomainType(expr ast.Expr, alias, name string) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && pkg.Name == alias
}

// exposedTaskListMethods returns the list methods the interface declares:
// ListTasks, ListRuns, and List with a Task Filter parameter.
func exposedTaskListMethods(iface *ast.InterfaceType, alias string) []string {
	var methods []string
	for _, method := range iface.Methods.List {
		funcType, ok := method.Type.(*ast.FuncType)
		if !ok || len(method.Names) != 1 {
			continue
		}
		name := method.Names[0].Name
		switch name {
		case "ListTasks", "ListRuns":
			methods = append(methods, name)
		case "List":
			for _, field := range funcType.Params.List {
				if isDomainType(field.Type, alias, "Filter") {
					methods = append(methods, name)
					break
				}
			}
		}
	}
	return methods
}

// The detector itself: which filters count as bounded.
func TestUnboundedListDetectorClassifiesFilters(t *testing.T) {
	const prelude = "package p\nimport domaintask \"github.com/Nyukimin/RenCrow_CORE/internal/domain/task\"\nvar _ = domaintask.Filter{}\n"
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"empty run filter", `x.ListRuns(ctx, domaintask.RunFilter{})`, []string{"ListRuns{}"}},
		{"status-only run filter", `x.ListRuns(ctx, domaintask.RunFilter{Status: s})`, []string{"ListRuns{Status}"}},
		{"explicit zero limit", `x.ListTasks(ctx, domaintask.Filter{Limit: 0})`, []string{"ListTasks{Limit}"}},
		{"variable filter", `x.ListRuns(ctx, filter)`, []string{"ListRuns(arg)"}},
		{"empty task list through List", `x.List(ctx, domaintask.Filter{})`, []string{"List{}"}},
		{"limit is bounded", `x.ListTasks(ctx, domaintask.Filter{Limit: 200})`, nil},
		{"limit by expression is bounded", `x.ListTasks(ctx, domaintask.Filter{Status: s, Limit: limit})`, nil},
		{"run list by TaskID is bounded", `x.ListRuns(ctx, domaintask.RunFilter{TaskID: id, Status: s})`, nil},
		{"TaskID does not bound a Task list", `x.ListTasks(ctx, domaintask.Filter{TaskID: id})`, []string{"ListTasks{TaskID}"}},
		{"List of something else is ignored", `x.List(ctx, other.Query{})`, nil},
		{"List with a variable is not identifiable", `x.List(ctx, filter)`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := prelude + "func f() {\n" + tc.body + "\n}\n"
			parsed, err := parser.ParseFile(token.NewFileSet(), "p.go", source, 0)
			if err != nil {
				t.Fatal(err)
			}
			alias, ok := taskDomainImportAlias(parsed)
			if !ok {
				t.Fatal("task domain import not recognized")
			}
			var got []string
			for _, declaration := range parsed.Decls {
				if function, ok := declaration.(*ast.FuncDecl); ok {
					got = append(got, unboundedListCalls(function.Body, alias)...)
				}
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("flagged %v, want %v", got, tc.want)
			}
		})
	}
}
