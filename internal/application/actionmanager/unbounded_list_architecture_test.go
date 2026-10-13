package actionmanager

import (
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

// Failure knowledge: docs/調査/*ActionStore全件読み*設計案.md (AC-14) and
// docs/調査/*Tool終端保存の予算不足によるOPS失敗_FailureKnowledge.md.
//
// The Action store keeps the whole history (about 57,000 Actions on the
// production store). Without the snapshot every call decoded and validated all of
// it, and the startup recoveries that listed every Action to join them to Runs
// were among the callers that made a restart wait 10 seconds and a Tool call fail
// with a 500. With the snapshot a call that lists the whole history still builds
// and sorts a copy of all of it. Listing without a bound is therefore a decision,
// not a default.
//
// This test fails when non-test code lists Actions without a bound outside the
// places below. A call is bounded when its filter literal names a Run or a Task
// (a lookup by owner ID), a Limit other than the literal 0, or both a Kind and a
// Name (an exact name lookup). Anything else - an empty filter, a filter with only
// a Status, a filter passed in as a variable - needs an entry that says why it is
// acceptable. An interface that exposes ListActions needs an entry too, so a new
// consumer of the list API is a visible decision.
//
// It is a syntactic guard (it cannot see through helpers that hide the call), so
// the allowlist is part of the contract; a stale entry fails the test. The store
// implementation (persistence/action) forwards its callers' filters and the domain
// package declares the contract, so they are not scanned; their callers are.
//
// Entry keys:
//
//	call:      "repo-relative path:function:ListActions{FilterKeys}" for a filter
//	           literal, or "...:ListActions(arg)" for a filter that is not a literal.
//	interface: "repo-relative path:interface:Name:ListActions".
var unboundedActionListAllowlist = map[string]string{
	// Startup recovery reads the Actions it acts on: the open ones, which are the
	// Actions of a process that stopped, a handful at most (F-1).
	"cmd/rencrow/runtime_action_recovery.go:actionsInRecoveryScope:ListActions{Status}": "open Actions only; the set a restart has to settle",

	// The opt-in check of every Action's Run reference
	// (action_store.verify_run_references_at_start). It is the one place that
	// reads the whole history on purpose, and it is off by default.
	"cmd/rencrow/runtime_action_run_references.go:verifyActionRunReferences:ListActions{}": "opt-in whole-history reference check",

	// Manager.ListActions forwards the caller's filter to the store. The callers are
	// checked at their own call sites and interfaces.
	"internal/application/actionmanager/manager.go:ListActions:ListActions(arg)": "forwards the caller's filter",

	// Test support that asserts the whole (tiny) store of a test.
	"internal/application/taskmanager/taskmanagertest/closure.go:AssertActionClosed:ListActions{}":   "test support over a small store",
	"internal/application/taskmanager/taskmanagertest/closure.go:AssertActionLeftOpen:ListActions{}": "test support over a small store",
	"internal/application/taskmanager/taskmanagertest/closure.go:AssertNoAction:ListActions{}":       "test support over a small store",

	// Interfaces that expose the list API. Each consumer's calls are governed above;
	// the entry records that the consumer exists.
	"internal/adapter/nativeharnessclient/delegation/runtime.go:interface:ResumeActionReader:ListActions": "lists by Task Run",
}

// Directories that implement or declare the store itself.
var unboundedActionListSkippedDirs = []string{
	"internal/infrastructure/persistence/action/",
	"internal/domain/action/",
}

const actionDomainImportSuffix = "/internal/domain/action"

func TestUnboundedActionListsAreLimitedToAllowlistedPlaces(t *testing.T) {
	root := actionArchitectureRoot(t)
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
		for _, skipped := range unboundedActionListSkippedDirs {
			if strings.HasPrefix(relative, skipped) {
				return nil
			}
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		alias, ok := actionDomainImportAlias(parsed)
		if !ok {
			return nil
		}
		record := func(key string) {
			observed[key] = struct{}{}
			if _, allowed := unboundedActionListAllowlist[key]; !allowed {
				violations = append(violations, key)
			}
		}
		for _, declaration := range parsed.Decls {
			switch typed := declaration.(type) {
			case *ast.FuncDecl:
				if typed.Body == nil {
					continue
				}
				for _, key := range unboundedActionListCalls(typed.Body, alias) {
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
					if exposesActionList(iface, alias) {
						record(relative + ":interface:" + typeSpec.Name.Name + ":ListActions")
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
		t.Fatalf("these places list Actions without a bound. Read by ID (GetAction), narrow the filter "+
			"(a Run, a Task, a Limit, a Kind and a Name), or add an allowlist entry with the reason:\n  %s",
			strings.Join(violations, "\n  "))
	}
	var stale []string
	for key := range unboundedActionListAllowlist {
		if _, ok := observed[key]; !ok {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf("allowlist entries no longer match any unbounded list; remove them:\n  %s", strings.Join(stale, "\n  "))
	}
}

func actionArchitectureRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", ".."))
}

// actionDomainImportAlias returns the local name under which the file imports the
// Action domain package.
func actionDomainImportAlias(file *ast.File) (string, bool) {
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || !strings.HasSuffix(path, actionDomainImportSuffix) {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name, true
		}
		return "action", true
	}
	return "", false
}

// unboundedActionListCalls returns "ListActions{Keys}" / "ListActions(arg)" for
// every x.ListActions(ctx, filter) call in body whose filter is not provably
// bounded.
func unboundedActionListCalls(body *ast.BlockStmt, alias string) []string {
	var found []string
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "ListActions" {
			return true
		}
		literal, isLiteral := call.Args[1].(*ast.CompositeLit)
		if !isLiteral {
			found = append(found, "ListActions(arg)")
			return true
		}
		if !isActionDomainType(literal.Type, alias, "Filter") {
			// A literal of some other type is not an Action filter.
			found = append(found, "ListActions(arg)")
			return true
		}
		keys := actionLiteralKeys(literal)
		if actionFilterBounded(literal, keys) {
			return true
		}
		sorted := append([]string(nil), keys...)
		sort.Strings(sorted)
		found = append(found, "ListActions{"+strings.Join(sorted, ",")+"}")
		return true
	})
	return found
}

func actionLiteralKeys(literal *ast.CompositeLit) []string {
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

// actionFilterBounded reports whether a filter literal limits the result: a Run or
// a Task, a Limit other than the literal 0, or a Kind together with a Name.
func actionFilterBounded(literal *ast.CompositeLit, keys []string) bool {
	has := map[string]bool{}
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
		case "RunID", "TaskID":
			return true
		}
		has[name.Name] = true
	}
	return has["Kind"] && has["Name"]
}

func isActionDomainType(expr ast.Expr, alias, name string) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && pkg.Name == alias
}

// exposesActionList reports whether the interface declares a ListActions method
// that takes an Action filter.
func exposesActionList(iface *ast.InterfaceType, alias string) bool {
	for _, method := range iface.Methods.List {
		funcType, ok := method.Type.(*ast.FuncType)
		if !ok || len(method.Names) != 1 || method.Names[0].Name != "ListActions" {
			continue
		}
		for _, field := range funcType.Params.List {
			if isActionDomainType(field.Type, alias, "Filter") {
				return true
			}
		}
	}
	return false
}

// The detector itself: which filters count as bounded.
func TestUnboundedActionListDetectorClassifiesFilters(t *testing.T) {
	const prelude = "package p\nimport domainaction \"github.com/Nyukimin/RenCrow_CORE/internal/domain/action\"\nvar _ = domainaction.Filter{}\n"
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"empty filter", `x.ListActions(ctx, domainaction.Filter{})`, []string{"ListActions{}"}},
		{"status only", `x.ListActions(ctx, domainaction.Filter{Status: s})`, []string{"ListActions{Status}"}},
		{"kind only", `x.ListActions(ctx, domainaction.Filter{Kind: k})`, []string{"ListActions{Kind}"}},
		{"name only", `x.ListActions(ctx, domainaction.Filter{Name: n})`, []string{"ListActions{Name}"}},
		{"kind and status", `x.ListActions(ctx, domainaction.Filter{Kind: k, Status: s})`, []string{"ListActions{Kind,Status}"}},
		{"explicit zero limit", `x.ListActions(ctx, domainaction.Filter{Limit: 0})`, []string{"ListActions{Limit}"}},
		{"variable filter", `x.ListActions(ctx, filter)`, []string{"ListActions(arg)"}},
		{"other literal type", `x.ListActions(ctx, other.Filter{})`, []string{"ListActions(arg)"}},
		{"limit is bounded", `x.ListActions(ctx, domainaction.Filter{Limit: 200})`, nil},
		{"limit by expression is bounded", `x.ListActions(ctx, domainaction.Filter{Status: s, Limit: limit})`, nil},
		{"run is bounded", `x.ListActions(ctx, domainaction.Filter{RunID: id})`, nil},
		{"task is bounded", `x.ListActions(ctx, domainaction.Filter{TaskID: id, Status: s})`, nil},
		{"kind and name are bounded", `x.ListActions(ctx, domainaction.Filter{Kind: k, Name: n})`, nil},
		{"another method is ignored", `x.ListAttempts(ctx, domainaction.AttemptFilter{})`, nil},
		{"wrong arity is ignored", `x.ListActions(domainaction.Filter{})`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := prelude + "func f() {\n" + tc.body + "\n}\n"
			parsed, err := parser.ParseFile(token.NewFileSet(), "p.go", source, 0)
			if err != nil {
				t.Fatal(err)
			}
			alias, ok := actionDomainImportAlias(parsed)
			if !ok {
				t.Fatal("action domain import not recognized")
			}
			var got []string
			for _, declaration := range parsed.Decls {
				if function, ok := declaration.(*ast.FuncDecl); ok {
					got = append(got, unboundedActionListCalls(function.Body, alias)...)
				}
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("flagged %v, want %v", got, tc.want)
			}
		})
	}
}
