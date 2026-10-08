package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// These tests fix, in the structure of the code, what the profile
// shiro_native_coding_v1 promises: a selected turn is checked before every old
// route and never reaches one (docs 03, acceptance conditions 1 to 3). They
// complement the behavior tests (native_coding_test.go, the orchestrator Trace
// tests and the real-binary integration tests): a behavior test shows what a
// route did; this shows that the code cannot reach the old routes from the
// native branch at all.

func parseFile(t *testing.T, name string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return file
}

func findMethod(t *testing.T, file *ast.File, receiver, name string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != name || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		if star, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
			if id, ok := star.X.(*ast.Ident); ok && id.Name == receiver {
				return fn
			}
		}
	}
	t.Fatalf("method %s.%s not found", receiver, name)
	return nil
}

// firstPosition returns the position of the first selector or identifier named
// one of names inside the node, or token.NoPos.
func firstPosition(node ast.Node, names ...string) token.Pos {
	want := map[string]bool{}
	for _, name := range names {
		want[name] = true
	}
	first := token.NoPos
	ast.Inspect(node, func(n ast.Node) bool {
		var (
			name string
			pos  token.Pos
		)
		switch v := n.(type) {
		case *ast.SelectorExpr:
			name, pos = v.Sel.Name, v.Sel.Pos()
		case *ast.Ident:
			name, pos = v.Name, v.Pos()
		default:
			return true
		}
		if want[name] && (first == token.NoPos || pos < first) {
			first = pos
		}
		return true
	})
	return first
}

func TestShiroExecuteChecksTheBackendSelectionBeforeEveryOldRoute(t *testing.T) {
	execute := findMethod(t, parseFile(t, "shiro.go"), "ShiroAgent", "Execute")

	selection := firstPosition(execute.Body, "BackendSelection")
	native := firstPosition(execute.Body, "executeNativeCoding")
	if selection == token.NoPos || native == token.NoPos {
		t.Fatal("Execute must check the typed backend selection and branch to executeNativeCoding")
	}
	for _, oldRoute := range []string{"tryExecuteCodexWorkPath", "subagentManager", "runSubagentSafely", "llmProvider", "Generate", "assemblePromptContext"} {
		position := firstPosition(execute.Body, oldRoute)
		if position == token.NoPos {
			t.Fatalf("Execute no longer references %s: update this test to the current old routes so it is not vacuous", oldRoute)
		}
		if position < selection {
			t.Fatalf("the backend selection must be checked before %s", oldRoute)
		}
	}
	// The branch is an early return: a selected turn does not fall through.
	returns := false
	ast.Inspect(execute.Body, func(n ast.Node) bool {
		if block, ok := n.(*ast.IfStmt); ok {
			if firstPosition(block.Cond, "BackendSelection") != token.NoPos && firstPosition(block.Body, "executeNativeCoding") != token.NoPos {
				for _, stmt := range block.Body.List {
					if _, ok := stmt.(*ast.ReturnStmt); ok {
						returns = true
					}
				}
			}
		}
		return true
	})
	if !returns {
		t.Fatal("the native branch must return: a selected turn never falls through to an old route")
	}
}

func TestNativeCodingBranchCannotReachAnOldRoute(t *testing.T) {
	file := parseFile(t, "native_coding.go")
	branch := findMethod(t, file, "ShiroAgent", "executeNativeCoding")
	forbidden := []string{
		"tryExecuteCodexWorkPath", "requestCodexAdvice", "advisorService", "agentPolicy", // CodexWorkPath and the advisor
		"subagentManager", "runSubagentSafely", "RunSync", // SubagentManager (toolloop)
		"llmProvider", "Generate", "lightMemory", // plain Generate
		"toolRunner", "mcpClient", "ExecuteV2", "ExecuteTool", // direct Tool execution
	}
	for _, name := range forbidden {
		if position := firstPosition(branch, name); position != token.NoPos {
			t.Errorf("executeNativeCoding must not reference %s: it would be a way back to an old route", name)
		}
	}
	convert := findMethodOrFunc(t, file, "nativeCodingResponse")
	for _, name := range forbidden {
		if position := firstPosition(convert, name); position != token.NoPos {
			t.Errorf("nativeCodingResponse must not reference %s", name)
		}
	}
}

func findMethodOrFunc(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name {
			return fn
		}
	}
	t.Fatalf("func %s not found", name)
	return nil
}
