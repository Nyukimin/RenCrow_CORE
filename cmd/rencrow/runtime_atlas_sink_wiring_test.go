package main

// Step18 s18_atlas_event_refs (composition-order guard).
//
// An Atlas lifecycle receipt is only honest when the EventID it stores as
// TransitionEventID exists in the canonical Event log.  The canonical Event log
// reaches the Atlas service through developmentEventLogSink, so the single place
// that attaches that sink decides whether production receipts resolve or dangle.
//
// buildViewerRuntimeHandlers is called before the Atlas service is created, and
// buildDependencies reassigns deps.atlasService after it (nil on startup
// failures, then the lifecycle-migrated service).  A sink attached inside
// buildViewerRuntimeHandlers therefore never reaches the service that serves
// owner writes, and every receipt is written without its companion event.  The
// wiring must happen after the last deps.atlasService assignment.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func parseCompositionFile(t *testing.T, name string) (*token.FileSet, *ast.FuncDecl) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == compositionFuncName(name) {
			return fset, fn
		}
	}
	t.Fatalf("function %q not found in %s", compositionFuncName(name), name)
	return nil, nil
}

func compositionFuncName(file string) string {
	if file == "runtime_viewer_handlers.go" {
		return "buildViewerRuntimeHandlers"
	}
	return "buildDependencies"
}

// selectorCallPositions returns the positions of calls shaped like
// <recv>.<method>(...) inside fn.
func selectorCallPositions(fn *ast.FuncDecl, method string) []token.Pos {
	var positions []token.Pos
	ast.Inspect(fn, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != method {
			return true
		}
		positions = append(positions, call.Pos())
		return true
	})
	return positions
}

// atlasServiceAssignPositions returns the positions of `deps.atlasService = ...`
// assignments inside fn, which are the points where a previously attached sink
// stops referring to the service that owns production Atlas writes.
func atlasServiceAssignPositions(fn *ast.FuncDecl) []token.Pos {
	var positions []token.Pos
	ast.Inspect(fn, func(node ast.Node) bool {
		assign, ok := node.(*ast.AssignStmt)
		if !ok || assign.Tok != token.ASSIGN {
			return true
		}
		for _, lhs := range assign.Lhs {
			selector, ok := lhs.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "atlasService" {
				continue
			}
			positions = append(positions, assign.Pos())
		}
		return true
	})
	return positions
}

// plainCallPositions returns the positions of `name(...)` calls inside fn.
func plainCallPositions(fn *ast.FuncDecl, name string) []token.Pos {
	var positions []token.Pos
	ast.Inspect(fn, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok || ident.Name != name {
			return true
		}
		positions = append(positions, call.Pos())
		return true
	})
	return positions
}

func TestAtlasDevelopmentEventSinkIsWiredAfterAtlasServiceFinalization(t *testing.T) {
	fset, buildDependencies := parseCompositionFile(t, "runtime_dependencies.go")
	assignments := atlasServiceAssignPositions(buildDependencies)
	if len(assignments) == 0 {
		t.Fatal("no deps.atlasService assignment found in buildDependencies")
	}
	finalAtlasService := assignments[len(assignments)-1]
	wirings := selectorCallPositions(buildDependencies, "WithDevelopmentEventSink")
	if len(wirings) == 0 {
		t.Fatalf("buildDependencies never attaches the Atlas development event sink, so every receipt TransitionEventID dangles (last deps.atlasService assignment at %s)",
			fset.Position(finalAtlasService))
	}
	for _, wiring := range wirings {
		if wiring < finalAtlasService {
			t.Fatalf("Atlas development event sink wired at %s precedes the last deps.atlasService assignment at %s, so the service that owns Atlas writes has no sink",
				fset.Position(wiring), fset.Position(finalAtlasService))
		}
	}
}

func TestAtlasDevelopmentEventSinkIsNotWiredInViewerHandlerBuild(t *testing.T) {
	fset, buildViewerHandlers := parseCompositionFile(t, "runtime_viewer_handlers.go")
	if wirings := selectorCallPositions(buildViewerHandlers, "WithDevelopmentEventSink"); len(wirings) != 0 {
		t.Fatalf("buildViewerRuntimeHandlers wires the Atlas development event sink at %s, but it runs before deps.atlasService exists and the sink is dropped",
			fset.Position(wirings[0]))
	}
}

func TestAtlasDevelopmentEventSinkWiringAssumesViewerHandlersRunFirst(t *testing.T) {
	fset, buildDependencies := parseCompositionFile(t, "runtime_dependencies.go")
	viewerHandlerBuilds := plainCallPositions(buildDependencies, "buildViewerRuntimeHandlers")
	if len(viewerHandlerBuilds) == 0 {
		t.Fatal("buildDependencies does not call buildViewerRuntimeHandlers")
	}
	assignments := atlasServiceAssignPositions(buildDependencies)
	finalAtlasService := assignments[len(assignments)-1]
	if viewerHandlerBuilds[0] > finalAtlasService {
		t.Fatalf("buildViewerRuntimeHandlers now runs after the last deps.atlasService assignment at %s; re-check whether the sink wiring location in %s is still required",
			fset.Position(finalAtlasService), fset.Position(viewerHandlerBuilds[0]))
	}
}
