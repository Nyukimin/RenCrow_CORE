package llm

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

// Failure knowledge: docs/調査/20261015_095140_ShiroChatのPrefix_Cache不成立とStrata_prefill.md, items 13 and 14.
//
// The Gateway can keep the leading system message identical across turns
// (system_message_mode single_leading_stable) only when CORE says what each
// message is (rencrow.prompt_context_blocks, built from Message.Type). A system
// message without a Type makes the Gateway fall back to single_leading_instruction
// (instruction_without_block), which folds the per-turn time Variable into the
// leading system message: the leading hash then changes on every request and the
// engine's prefix cache is lost. The CoderLoop lost about 3.5x of its speed this
// way, and every call that used an untyped system message did the same.
//
// This test fails when non-test code builds a llm.Message or llm.ChatMessage
// with Role "system" and no Type. State what the message is: Character for the
// persona or the loop's operating prompt, Stable for constant instructions,
// Recall for recalled memory, Variable for per-turn state. A constant
// instruction must not be typed Variable (it would leave the leading system
// message), and per-turn content must not be typed Stable.
//
// It is a syntactic guard: it sees composite literals with a string-literal Role
// (including the elided element of a []llm.Message literal), not roles computed
// at runtime. GenerateRequest.SystemPrompt is typed Stable by the rencrowllm
// provider, so the field is not scanned here.
//
// Entry keys are "repo-relative path:line" of the literal. A stale entry fails
// the test.
var untypedSystemMessageAllowlist = map[string]string{}

func TestNonTestCodeTypesEverySystemMessage(t *testing.T) {
	root := repoRootForArchitectureTest(t)
	var violations []string
	seen := map[string]bool{}
	fset := token.NewFileSet()
	for _, dir := range []string{"cmd", "internal", "modules"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if name := entry.Name(); name == "node_modules" || name == "testdata" || name == "Tmp" || name == "tmp" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			for _, line := range untypedSystemLiteralLines(fset, file) {
				key := rel + ":" + strconv.Itoa(line)
				if _, allowed := untypedSystemMessageAllowlist[key]; allowed {
					seen[key] = true
					continue
				}
				violations = append(violations, key)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Errorf("system messages without a Type (the Gateway cannot keep the leading system message stable):\n  %s", strings.Join(violations, "\n  "))
	}
	for key := range untypedSystemMessageAllowlist {
		if !seen[key] {
			t.Errorf("stale allowlist entry %q: the literal no longer exists or is typed", key)
		}
	}
}

func repoRootForArchitectureTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

// untypedSystemLiteralLines returns the lines of Message / ChatMessage literals
// of the file that have Role "system" and no Type.
func untypedSystemLiteralLines(fset *token.FileSet, file *ast.File) []int {
	qualifiers := map[string]bool{}
	inLLMPackage := file.Name.Name == "llm"
	for _, spec := range file.Imports {
		path, _ := strconv.Unquote(spec.Path.Value)
		if !strings.HasSuffix(path, "/internal/domain/llm") {
			continue
		}
		name := "llm"
		if spec.Name != nil {
			name = spec.Name.Name
		}
		qualifiers[name] = true
	}
	isMessageType := func(expr ast.Expr) bool {
		switch typed := expr.(type) {
		case *ast.Ident:
			return inLLMPackage && (typed.Name == "Message" || typed.Name == "ChatMessage")
		case *ast.SelectorExpr:
			qualifier, ok := typed.X.(*ast.Ident)
			return ok && qualifiers[qualifier.Name] && (typed.Sel.Name == "Message" || typed.Sel.Name == "ChatMessage")
		}
		return false
	}
	var lines []int
	check := func(literal *ast.CompositeLit) {
		role, typed := "", false
		for _, element := range literal.Elts {
			pair, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := pair.Key.(*ast.Ident)
			if !ok {
				continue
			}
			switch key.Name {
			case "Role":
				if value, ok := pair.Value.(*ast.BasicLit); ok && value.Kind == token.STRING {
					role, _ = strconv.Unquote(value.Value)
				}
			case "Type":
				typed = true
			}
		}
		if role == "system" && !typed {
			lines = append(lines, fset.Position(literal.Pos()).Line)
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok || literal.Type == nil {
			return true
		}
		if isMessageType(literal.Type) {
			check(literal)
			return true
		}
		if array, ok := literal.Type.(*ast.ArrayType); ok && isMessageType(array.Elt) {
			for _, element := range literal.Elts {
				if inner, ok := element.(*ast.CompositeLit); ok && inner.Type == nil {
					check(inner)
				}
			}
		}
		return true
	})
	return lines
}
