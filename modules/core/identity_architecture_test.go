package core

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const canonicalThreadIdentityViolationLimit = 100

func TestCanonicalIDGeneratorsHaveOneSource(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current file")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	allowedFile := filepath.Join(repoRoot, "modules", "core", "identity.go")
	canonicalGenerators := map[string]struct{}{
		"NewTraceID": {}, "NewEventID": {}, "NewSessionID": {}, "NewThreadID": {},
		"NewTurnID": {}, "NewMessageID": {}, "NewUtteranceID": {}, "NewWorkstreamID": {},
		"NewGoalID": {}, "NewTaskID": {}, "NewRunID": {}, "NewActionID": {},
		"NewAttemptID": {}, "NewRequestID": {}, "NewResponseID": {}, "NewArtifactID": {},
		"NewEvidenceID": {}, "NewMemoryID": {}, "NewRelationID": {}, "NewScheduleID": {},
		"NewQueueItemID": {}, "NewCheckpointID": {}, "NewReceiptID": {},
	}
	legacyGeneratorSites := map[string]struct{}{
		// Frozen legacy sites are removed by later canonical replacement steps.
		// This allowlist prevents Step 01 from increasing their scope.
		"internal/adapter/viewer/browser_trace_api_handler.go:HandleBrowserTraceAPIFetcherProposal":            {},
		"internal/adapter/viewer/complexity_hotspot_handler.go:HandleComplexityHotspotConcreteDiffWithSandbox": {},
		"internal/adapter/viewer/complexity_hotspot_handler.go:HandleComplexityHotspotProposalWithSandbox":     {},
		"internal/adapter/viewer/complexity_hotspot_handler.go:HandleComplexityHotspotScan":                    {},
		"internal/adapter/viewer/complexity_hotspot_handler.go:buildComplexityCoderDiffFailureArtifact":        {},
		"internal/adapter/viewer/complexity_hotspot_handler.go:buildHighRiskComplexityReviewArtifact":          {},
		"internal/adapter/viewer/complexity_hotspot_handler.go:saveComplexityConcreteDiffReview":               {},
		"internal/adapter/viewer/persona_observation_handler.go:HandlePersonaObservationAggregate":             {},
		"internal/adapter/viewer/sandbox_handler.go:HandleSandboxPromotionApplyWithVerifierAndApplier":         {},
		"internal/adapter/viewer/sandbox_handler.go:HandleSandboxPromotionRequest":                             {},
		"internal/adapter/viewer/sandbox_handler.go:HandleSandboxPromotionRollback":                            {},
		"internal/adapter/viewer/skill_governance_handler.go:HandleSkillGovernanceBootstrap":                   {},
		"internal/adapter/viewer/skill_governance_handler.go:HandleSkillGovernanceContributionGate":            {},
		"internal/application/browsertrace/artifacts.go:BuildAPIArtifactsWithValidations":                      {},
		"internal/application/backlog/service.go:Adopt":                                                        {},
		"internal/application/heartbeat/service.go:RunBacklogIntake":                                           {},
		"internal/application/idlechat/orchestrator.go:applyPersonaCanonicalResponse":                          {},
		"internal/application/idlechat/orchestrator.go:recordPersonaTimelineEvent":                             {},
		"internal/application/orchestrator/message_orchestrator_persona.go:applyPersonaCanonicalResponse":      {},
		"internal/application/orchestrator/message_orchestrator_persona.go:recordPersonaRuntimeObservation":    {},
		"internal/application/orchestrator/superagent_runtime.go:leadAgentRunID":                               {},
		"internal/application/skillgovernance/bootstrap_service.go:Record":                                     {},
		"internal/application/skillgovernance/coder_evidence_service.go:saveCoderTranscriptEntries":            {},
		"internal/infrastructure/stt/provider.go:NextEventID":                                                  {},
		"internal/infrastructure/tools/harness_runner.go:record":                                               {},
		"internal/infrastructure/tools/runner.go:recordToolMediation":                                          {},
	}
	observedLegacyGeneratorSites := make(map[string]struct{}, len(legacyGeneratorSites))
	var violations []string
	err := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == ".git" || name == "vendor" || name == "node_modules" || name == "Tmp" {
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
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if _, canonical := canonicalGenerators[function.Name.Name]; canonical && function.Recv == nil && filepath.Clean(path) != allowedFile {
				violations = append(violations, strings.TrimPrefix(path, repoRoot+string(filepath.Separator))+":"+function.Name.Name)
			}
			if filepath.Clean(path) != allowedFile && function.Body != nil {
				relative := strings.TrimPrefix(path, repoRoot+string(filepath.Separator))
				site := relative + ":" + function.Name.Name
				ast.Inspect(function.Body, func(node ast.Node) bool {
					if literal, generated := canonicalGeneratorLiteral(node); generated {
						if _, allowed := legacyGeneratorSites[site]; allowed {
							observedLegacyGeneratorSites[site] = struct{}{}
						} else {
							violations = append(violations, site+":"+literal)
						}
					}
					return true
				})
			}
		}
		if filepath.Clean(path) != allowedFile {
			ast.Inspect(parsed, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "NewV7" {
					return true
				}
				if packageName, ok := selector.X.(*ast.Ident); ok && packageName.Name == "uuid" {
					violations = append(violations, strings.TrimPrefix(path, repoRoot+string(filepath.Separator))+":uuid.NewV7")
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan Go source: %v", err)
	}
	for site := range legacyGeneratorSites {
		if _, observed := observedLegacyGeneratorSites[site]; !observed {
			violations = append(violations, "stale legacy generator allowlist:"+site)
		}
	}
	sort.Strings(violations)
	if len(violations) != 0 {
		t.Fatalf("canonical ID generators must exist only in modules/core/identity.go: %v", violations)
	}
}

func TestCanonicalConversationTurnArchitecture(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current file")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	forbidden := map[string]struct{}{
		"ConversationTurnMessageIDs": {},
		"RecallTraceID":              {},
		"OwnerRecallTraceID":         {},
		"EndTurn":                    {},
		"EndTurnAs":                  {},
	}
	var violations []string
	err := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor", "node_modules", "Tmp":
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
		relative := strings.TrimPrefix(path, repoRoot+string(filepath.Separator))
		ast.Inspect(parsed, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.FuncDecl:
				if _, banned := forbidden[value.Name.Name]; banned {
					violations = append(violations, relative+":"+value.Name.Name)
				}
			case *ast.CallExpr:
				if selector, ok := value.Fun.(*ast.SelectorExpr); ok {
					if _, banned := forbidden[selector.Sel.Name]; banned {
						violations = append(violations, relative+":"+selector.Sel.Name)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan conversation identity architecture: %v", err)
	}
	if len(violations) != 0 {
		sort.Strings(violations)
		t.Fatalf("legacy conversation identity routes remain: %v", violations)
	}
}

func canonicalGeneratorLiteral(node ast.Node) (string, bool) {
	var literal *ast.BasicLit
	switch expression := node.(type) {
	case *ast.CallExpr:
		selector, ok := expression.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Sprintf" || len(expression.Args) == 0 {
			return "", false
		}
		packageName, packageOK := selector.X.(*ast.Ident)
		if !packageOK || packageName.Name != "fmt" {
			return "", false
		}
		literal, _ = expression.Args[0].(*ast.BasicLit)
	case *ast.BinaryExpr:
		if expression.Op != token.ADD {
			return "", false
		}
		literal, _ = expression.X.(*ast.BasicLit)
	default:
		return "", false
	}
	if literal == nil || literal.Kind != token.STRING {
		return "", false
	}
	raw, err := strconv.Unquote(literal.Value)
	if err != nil {
		return "", false
	}
	for _, prefix := range []string{
		"trc_", "evt_", "ses_", "thr_", "turn_", "msg_", "utt_", "ws_", "gol_", "tsk_", "run_", "act_",
		"att_", "req_", "rsp_", "art_", "evd_", "mem_", "rel_", "sch_", "qit_", "ckp_", "rcp_",
	} {
		if strings.HasPrefix(raw, prefix) {
			return raw, true
		}
	}
	return "", false
}

func TestCanonicalSessionIngressHasNoLegacyIDBuilder(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current file")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	var violations []string
	err := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor", "node_modules", "Tmp":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, legacy := range []string{"BuildSessionID", "func NewSession(", "func ReconstructSession("} {
			if strings.Contains(string(content), legacy) {
				violations = append(violations, strings.TrimPrefix(path, repoRoot+string(filepath.Separator))+":"+legacy)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan Go source: %v", err)
	}
	if len(violations) != 0 {
		sort.Strings(violations)
		t.Fatalf("legacy Session identity construction remains in runtime source: %v", violations)
	}
}

func TestCanonicalSessionMigrationCodeIsRemovedAfterCutover(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current file")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	for _, relative := range []string{
		filepath.Join("cmd", "rencrow-session-migrate"),
		filepath.Join("internal", "infrastructure", "persistence", "sessionmigration"),
	} {
		if _, err := os.Stat(filepath.Join(repoRoot, relative)); err == nil || !os.IsNotExist(err) {
			t.Fatalf("Step 04 migration source remains after production cutover: %s", relative)
		}
	}
}

func TestCanonicalTurnMessageMigrationSourceIsRemovedAfterCutover(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current file")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	for _, relative := range []string{
		filepath.Join("cmd", "rencrow-turn-message-migrate"),
		filepath.Join("internal", "infrastructure", "persistence", "turnmigration"),
	} {
		if _, err := os.Stat(filepath.Join(repoRoot, relative)); err == nil || !os.IsNotExist(err) {
			t.Fatalf("Step 06 migration source remains after production cutover: %s", relative)
		}
	}
}

func TestCanonicalEventRuntimeHasNoLegacyOwnerEventContract(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current file")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	legacyTokens := []string{"WorkflowEvent", "TraceEvent", "ParentEventID", "parent_event_id", "workflow_event", "trace_event", "viewer_log", "orchestrator_event_log"}
	var violations []string
	err := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == ".git" || name == "vendor" || name == "node_modules" || name == "Tmp" {
				return filepath.SkipDir
			}
			cleanPath := filepath.Clean(path)
			if cleanPath == filepath.Join(repoRoot, "internal", "application", "identitymigration") ||
				cleanPath == filepath.Join(repoRoot, "internal", "infrastructure", "persistence", "eventmigration") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative := strings.TrimPrefix(path, repoRoot+string(filepath.Separator))
		for _, token := range legacyTokens {
			if relative == "internal/adapter/config/config.go" && token == "viewer_log" {
				continue
			}
			if strings.Contains(string(content), token) {
				violations = append(violations, relative+":"+token)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan Go source: %v", err)
	}
	for _, retired := range []string{
		"internal/adapter/viewer/event_log_store.go",
		"internal/adapter/viewer/event_log_gc.go",
		"cmd/rencrow/runtime_data_write_adapters_e.go",
	} {
		if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(retired))); !os.IsNotExist(err) {
			violations = append(violations, "retired path remains:"+retired)
		}
	}
	sort.Strings(violations)
	if len(violations) != 0 {
		t.Fatalf("legacy Event contract remains in runtime source: %v", violations)
	}
}

func TestCurrentDocumentationDoesNotReuseJobIDAsTraceID(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current file")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	documents := []string{
		"docs/02_機能仕様.md",
		"docs/04_アーキテクチャ概要.md",
		"docs/06_Public_API仕様.md",
		"docs/10_ログ仕様.md",
	}
	legacyClaims := []string{
		"rootの`trace_id`には`job_id`と同じ",
		"root `trace_id`は受付時の`job_id`と同じ",
		"root `trace_id`は`job_id`と同じ",
		`"trace_id":"job-..."`,
	}
	for _, document := range documents {
		content, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(document)))
		if err != nil {
			t.Fatalf("read %s: %v", document, err)
		}
		for _, claim := range legacyClaims {
			if strings.Contains(string(content), claim) {
				t.Errorf("%s retains legacy JobID/TraceID equality claim %q", document, claim)
			}
		}
	}
}

func TestCanonicalThreadIdentityHasNoIntegerLegacy(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current file")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	var violations []string
	err := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if canonicalThreadIdentitySkipDir(repoRoot, path) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, path, source, parser.ParseComments)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if canonicalThreadIdentitySkipFile(relative) {
			return nil
		}
		violations = append(violations, canonicalThreadIdentifierViolations(relative, fileSet, parsed)...)
		violations = append(violations, canonicalThreadNumericFormatViolations(relative, fileSet, parsed)...)
		violations = append(violations, canonicalThreadJSONNumericViolations(relative, fileSet, parsed)...)
		violations = append(violations, canonicalThreadSQLColumnViolations(relative, fileSet, parsed)...)
		violations = append(violations, canonicalDiscussionIDViolations(relative, source)...)
		return nil
	})
	if err != nil {
		t.Fatalf("scan production Go source: %v", err)
	}
	sort.Strings(violations)
	if len(violations) == 0 {
		return
	}
	shown := violations
	if len(shown) > canonicalThreadIdentityViolationLimit {
		shown = shown[:canonicalThreadIdentityViolationLimit]
	}
	t.Fatalf("canonical ThreadID must be UUID-backed and DiscussionID-free: violations=%d showing=%d\n%s", len(violations), len(shown), strings.Join(shown, "\n"))
}

func TestCanonicalThreadMigrationSourceIsRemovedAfterCutover(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current file")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	for _, relative := range []string{
		filepath.Join("cmd", "rencrow-thread-migrate"),
		filepath.Join("internal", "infrastructure", "persistence", "threadmigration"),
	} {
		if _, err := os.Stat(filepath.Join(repoRoot, relative)); err == nil || !os.IsNotExist(err) {
			t.Fatalf("Step 05 migration source remains after production cutover: %s", relative)
		}
	}
}

func TestCanonicalThreadIdentityAssetsDoNotPublishDiscussionID(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current file")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	roots := []string{
		filepath.Join(repoRoot, "internal", "features", "backlog", "backfill"),
		filepath.Join(repoRoot, "internal", "features", "backlog", "testdata"),
	}
	var violations []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".json", ".jsonl", ".md":
			default:
				return nil
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, legacy := range []string{"DiscussionID", "discussion_id", "discussionId"} {
				if strings.Contains(string(contents), legacy) {
					relative, relErr := filepath.Rel(repoRoot, path)
					if relErr != nil {
						return relErr
					}
					violations = append(violations, filepath.ToSlash(relative)+":"+legacy)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan identity-bearing product assets: %v", err)
		}
	}
	sort.Strings(violations)
	if len(violations) != 0 {
		t.Fatalf("retired DiscussionID remains in identity-bearing product assets: %v", violations)
	}
}

func TestCanonicalThreadPositiveTestFixturesHaveNoIntegerLegacy(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current file")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	allowedRejectionTests := map[string]struct{}{
		"internal/infrastructure/persistence/conversation/l1sqlite/l1_sqlite_conversation_turn_test.go:TestNewL1SQLiteStoreRejectsLegacyThreadSchema":        {},
		"internal/infrastructure/persistence/conversation/archivesqlite/archive_sqlite_schema_test.go:TestArchiveSQLiteStoreRejectsLegacyThreadSchemaAtOpen": {},
	}
	var violations []string
	err := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if canonicalThreadIdentitySkipDir(repoRoot, path) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") || filepath.Clean(path) == filepath.Clean(currentFile) {
			return nil
		}
		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if _, allowed := allowedRejectionTests[relative+":"+function.Name.Name]; allowed {
				continue
			}
			fragment := &ast.File{Name: parsed.Name, Decls: []ast.Decl{function}}
			violations = append(violations, canonicalThreadIdentifierViolations(relative, fileSet, fragment)...)
			violations = append(violations, canonicalThreadNumericFormatViolations(relative, fileSet, fragment)...)
			violations = append(violations, canonicalThreadJSONNumericViolations(relative, fileSet, fragment)...)
			violations = append(violations, canonicalThreadSQLColumnViolations(relative, fileSet, fragment)...)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan Go test fixtures: %v", err)
	}
	sort.Strings(violations)
	if len(violations) != 0 {
		t.Fatalf("positive test fixtures retain integer ThreadID assumptions: %v", violations)
	}
}

func canonicalThreadIdentitySkipDir(repoRoot, path string) bool {
	base := filepath.Base(path)
	switch base {
	case ".git", "vendor", "node_modules", "Tmp":
		return true
	}
	return false
}

func canonicalThreadIdentitySkipFile(relative string) bool {
	// The immutable external-memory source retains its own string thread_id
	// contract. It is not a conversation runtime/store identity surface.
	return relative == "internal/domain/memory/common_raw.go"
}

func canonicalThreadIdentifierViolations(relative string, fileSet *token.FileSet, parsed *ast.File) []string {
	if parsed == nil {
		return nil
	}
	numericAliases := canonicalThreadNumericAliases(parsed)
	violations := make([]string, 0)
	ast.Inspect(parsed, func(node ast.Node) bool {
		switch declaration := node.(type) {
		case *ast.Field:
			if typeName, ok := canonicalThreadNumericType(declaration.Type, numericAliases, map[string]bool{}); ok {
				for _, name := range declaration.Names {
					if canonicalThreadIdentityName(name.Name) {
						violations = append(violations, canonicalThreadViolation(fileSet, name.Pos(), relative, "identifier-type", name.Name+"="+typeName))
					}
				}
			}
		case *ast.ValueSpec:
			if typeName, ok := canonicalThreadNumericType(declaration.Type, numericAliases, map[string]bool{}); ok {
				for _, name := range declaration.Names {
					if canonicalThreadIdentityName(name.Name) {
						violations = append(violations, canonicalThreadViolation(fileSet, name.Pos(), relative, "identifier-type", name.Name+"="+typeName))
					}
				}
				break
			}
			for index, name := range declaration.Names {
				if !canonicalThreadIdentityName(name.Name) || index >= len(declaration.Values) {
					continue
				}
				if typeName, ok := canonicalThreadNumericExpression(declaration.Values[index]); ok {
					violations = append(violations, canonicalThreadViolation(fileSet, name.Pos(), relative, "identifier-type", name.Name+"="+typeName))
				}
			}
		}
		return true
	})
	return violations
}

func canonicalThreadNumericAliases(parsed *ast.File) map[string]string {
	aliases := make(map[string]string)
	for _, declaration := range parsed.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.TYPE {
			continue
		}
		for _, specification := range general.Specs {
			typeSpec, ok := specification.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if typeName, ok := canonicalThreadNumericType(typeSpec.Type, aliases, map[string]bool{}); ok {
				aliases[typeSpec.Name.Name] = typeName
			}
		}
	}
	return aliases
}

func canonicalThreadNumericType(expression ast.Expr, aliases map[string]string, seen map[string]bool) (string, bool) {
	switch expression := expression.(type) {
	case *ast.Ident:
		if canonicalThreadIntegerTypeName(expression.Name) {
			return expression.Name, true
		}
		if typeName, ok := aliases[expression.Name]; ok && !seen[expression.Name] {
			seen[expression.Name] = true
			return typeName, true
		}
	case *ast.ParenExpr:
		return canonicalThreadNumericType(expression.X, aliases, seen)
	case *ast.StarExpr:
		if typeName, ok := canonicalThreadNumericType(expression.X, aliases, seen); ok {
			return "*" + typeName, true
		}
	case *ast.ArrayType:
		if typeName, ok := canonicalThreadNumericType(expression.Elt, aliases, seen); ok {
			return "[]" + typeName, true
		}
	case *ast.MapType:
		if typeName, ok := canonicalThreadNumericType(expression.Value, aliases, seen); ok {
			return "map[...]" + typeName, true
		}
	case *ast.ChanType:
		if typeName, ok := canonicalThreadNumericType(expression.Value, aliases, seen); ok {
			return "chan " + typeName, true
		}
	}
	return "", false
}

func canonicalThreadNumericExpression(expression ast.Expr) (string, bool) {
	switch expression := expression.(type) {
	case *ast.BasicLit:
		if expression.Kind == token.INT {
			return "integer-literal", true
		}
	case *ast.CallExpr:
		if identifier, ok := expression.Fun.(*ast.Ident); ok && canonicalThreadIntegerTypeName(identifier.Name) {
			return identifier.Name, true
		}
	case *ast.ParenExpr:
		return canonicalThreadNumericExpression(expression.X)
	}
	return "", false
}

func canonicalThreadNumericFormatViolations(relative string, fileSet *token.FileSet, parsed *ast.File) []string {
	var violations []string
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (selector.Sel.Name != "Printf" && selector.Sel.Name != "Sprintf" && selector.Sel.Name != "Errorf" && selector.Sel.Name != "Fprintf" && selector.Sel.Name != "Appendf") {
			return true
		}
		formatIndex := -1
		var format string
		for index, argument := range call.Args {
			literal, ok := argument.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				return true
			}
			formatIndex = index
			format = value
			break
		}
		if formatIndex < 0 {
			return true
		}
		verbs := canonicalFormatVerbs(format)
		for index, verb := range verbs {
			argumentIndex := formatIndex + 1 + index
			if argumentIndex >= len(call.Args) || !canonicalNumericFormatVerb(verb) {
				continue
			}
			if name, ok := canonicalThreadIdentityExpression(call.Args[argumentIndex]); ok {
				violations = append(violations, canonicalThreadViolation(fileSet, call.Args[argumentIndex].Pos(), relative, "numeric-format", name+"=%"+string(verb)))
			}
		}
		return true
	})
	return violations
}

func canonicalThreadJSONNumericViolations(relative string, fileSet *token.FileSet, parsed *ast.File) []string {
	var violations []string
	ast.Inspect(parsed, func(node ast.Node) bool {
		entry, ok := node.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		keyLiteral, ok := entry.Key.(*ast.BasicLit)
		if !ok || keyLiteral.Kind != token.STRING {
			return true
		}
		key, err := strconv.Unquote(keyLiteral.Value)
		if err != nil {
			return true
		}
		normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
		if normalized != "thread_id" && normalized != "last_thread_id" && normalized != "closed_thread_id" {
			return true
		}
		if numericType, numeric := canonicalThreadNumericExpression(entry.Value); numeric {
			violations = append(violations, canonicalThreadViolation(fileSet, entry.Value.Pos(), relative, "numeric-json-value", key+"="+numericType))
		}
		return true
	})
	return violations
}

func canonicalFormatVerbs(format string) []rune {
	verbs := make([]rune, 0)
	for index := 0; index < len(format); index++ {
		if format[index] != '%' {
			continue
		}
		index++
		if index < len(format) && format[index] == '%' {
			continue
		}
		for index < len(format) {
			value := rune(format[index])
			if (value >= 'A' && value <= 'Z') || (value >= 'a' && value <= 'z') {
				verbs = append(verbs, value)
				break
			}
			index++
		}
	}
	return verbs
}

func canonicalNumericFormatVerb(verb rune) bool {
	return strings.ContainsRune("bcdoxXOU", verb)
}

func canonicalThreadIdentityExpression(expression ast.Expr) (string, bool) {
	switch expression := expression.(type) {
	case *ast.Ident:
		if canonicalThreadIdentityName(expression.Name) {
			return expression.Name, true
		}
	case *ast.SelectorExpr:
		if canonicalThreadIdentityName(expression.Sel.Name) {
			return expression.Sel.Name, true
		}
		if expression.Sel.Name == "ID" && canonicalThreadSelectorOwner(expression.X) {
			return canonicalSelectorPath(expression), true
		}
	case *ast.ParenExpr:
		return canonicalThreadIdentityExpression(expression.X)
	}
	return "", false
}

func canonicalThreadSelectorOwner(expression ast.Expr) bool {
	for expression != nil {
		switch current := expression.(type) {
		case *ast.Ident:
			return strings.Contains(strings.ToLower(current.Name), "thread")
		case *ast.SelectorExpr:
			if strings.Contains(strings.ToLower(current.Sel.Name), "thread") {
				return true
			}
			expression = current.X
		default:
			return false
		}
	}
	return false
}

func canonicalSelectorPath(expression ast.Expr) string {
	segments := make([]string, 0, 3)
	for expression != nil {
		switch current := expression.(type) {
		case *ast.Ident:
			segments = append(segments, current.Name)
			expression = nil
		case *ast.SelectorExpr:
			segments = append(segments, current.Sel.Name)
			expression = current.X
		default:
			expression = nil
		}
	}
	for left, right := 0, len(segments)-1; left < right; left, right = left+1, right-1 {
		segments[left], segments[right] = segments[right], segments[left]
	}
	return strings.Join(segments, ".")
}

func canonicalThreadIntegerTypeName(name string) bool {
	switch name {
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr":
		return true
	default:
		return false
	}
}

func canonicalThreadIdentityName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, "threadid") || strings.HasSuffix(lower, "threadids")
}

func TestCanonicalThreadIdentifierScannerDetectsNumericContainers(t *testing.T) {
	source := `package scannerfixture

type legacyThreadNumber int64

type fixture struct {
	sessionThreadIDs map[string]int64
	archivedThreadIDs []legacyThreadNumber
	externalThreadID uint32
	canonicalThreadIDs []string
}
`
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "fixture.go", source, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	violations := canonicalThreadIdentifierViolations("fixture.go", fileSet, parsed)
	if len(violations) != 3 {
		t.Fatalf("numeric container violations = %v, want 3", violations)
	}
	joined := strings.Join(violations, "\n")
	for _, want := range []string{"sessionThreadIDs=map[...]int64", "archivedThreadIDs=[]int64", "externalThreadID=uint32"} {
		if !strings.Contains(joined, want) {
			t.Errorf("violations = %v, missing %q", violations, want)
		}
	}
}

func TestCanonicalThreadIdentifierScannerDetectsNumericFormatting(t *testing.T) {
	source := `package scannerfixture

import "log"

type threadFixture struct { ID string }
type statusFixture struct { ActiveThread threadFixture }

func write(thread threadFixture, status statusFixture, threadID string, seq int64) {
	log.Printf("thread=%d seq=%d", thread.ID, seq)
	log.Printf("thread=%d", status.ActiveThread.ID)
	log.Printf("thread=%d", threadID)
	log.Printf("thread=%s seq=%d", thread.ID, seq)
	fmt.Fprintf(nil, "thread=%d", threadID)
	fmt.Appendf(nil, "thread=%d", status.ActiveThread.ID)
}
`
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "fixture.go", source, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	violations := canonicalThreadNumericFormatViolations("fixture.go", fileSet, parsed)
	if len(violations) != 5 {
		t.Fatalf("numeric format violations = %v, want 5", violations)
	}
}

func TestCanonicalThreadIdentifierScannerDetectsNumericJSONMapValues(t *testing.T) {
	source := `package scannerfixture

func payload() map[string]any {
	return map[string]any{"thread_id": 42, "last_thread_id": int64(7), "thread_seq": 1, "thread_id_text": "42"}
}
`
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "fixture.go", source, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	violations := canonicalThreadJSONNumericViolations("fixture.go", fileSet, parsed)
	if len(violations) != 2 {
		t.Fatalf("numeric JSON violations = %v, want 2", violations)
	}
}

func canonicalThreadSQLColumnViolations(relative string, fileSet *token.FileSet, parsed *ast.File) []string {
	violations := make([]string, 0)
	ast.Inspect(parsed, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		raw, err := strconv.Unquote(literal.Value)
		if err != nil {
			return true
		}
		baseLine := fileSet.PositionFor(literal.Pos(), false).Line
		for offset, line := range strings.Split(raw, "\n") {
			column, sqlType, ok := canonicalThreadSQLIntegerDeclaration(line)
			if ok {
				violations = append(violations, fmt.Sprintf("%s:%d:sql-column-type:%s=%s", relative, baseLine+offset, column, sqlType))
			}
		}
		return true
	})
	return violations
}

func canonicalThreadSQLIntegerDeclaration(line string) (string, string, bool) {
	replaced := strings.NewReplacer("(", " ", ")", " ", ",", " ", "`", " ", "\"", " ", "'", " ").Replace(strings.ToLower(line))
	tokens := strings.Fields(replaced)
	for index := 0; index+1 < len(tokens); index++ {
		column := tokens[index]
		if column != "thread_id" && column != "closed_thread_id" && column != "last_thread_id" {
			continue
		}
		sqlType := tokens[index+1]
		if canonicalThreadSQLIntegerType(sqlType) {
			return column, sqlType, true
		}
	}
	return "", "", false
}

func canonicalThreadSQLIntegerType(sqlType string) bool {
	switch sqlType {
	case "integer", "bigint":
		return true
	default:
		return false
	}
}

func canonicalDiscussionIDViolations(relative string, source []byte) []string {
	violations := make([]string, 0)
	for lineNumber, line := range strings.Split(string(source), "\n") {
		for _, token := range []string{"DiscussionID", "discussion_id"} {
			if canonicalSourceContainsToken(line, token) {
				violations = append(violations, fmt.Sprintf("%s:%d:legacy-discussion-id:%s", relative, lineNumber+1, token))
			}
		}
	}
	return violations
}

func canonicalSourceContainsToken(line, token string) bool {
	for start := 0; ; {
		index := strings.Index(line[start:], token)
		if index < 0 {
			return false
		}
		index += start
		beforeOK := index == 0 || !canonicalSourceIdentifierRune(line[index-1])
		after := index + len(token)
		afterOK := after == len(line) || !canonicalSourceIdentifierRune(line[after])
		if beforeOK && afterOK {
			return true
		}
		start = after
		if start >= len(line) {
			return false
		}
	}
}

func canonicalSourceIdentifierRune(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || (value >= '0' && value <= '9') || value == '_'
}

func canonicalThreadViolation(fileSet *token.FileSet, position token.Pos, relative, kind, detail string) string {
	return fmt.Sprintf("%s:%d:%s:%s", relative, fileSet.PositionFor(position, false).Line, kind, detail)
}

const (
	canonicalTaskImportPath         = "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	canonicalConversationImportPath = "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

func TestCanonicalTaskValueObjectArchitecture(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	var violations []string

	legacyTaskFile := filepath.Join(repoRoot, "internal", "domain", "task", "task.go")
	if _, err := os.Stat(legacyTaskFile); err == nil {
		violations = append(violations, "internal/domain/task/task.go:retired user-input Task source remains")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat retired task source: %v", err)
	}

	jobIDFile := filepath.Join(repoRoot, "internal", "domain", "task", "jobid.go")
	if _, err := os.Stat(jobIDFile); os.IsNotExist(err) {
		violations = append(violations, "internal/domain/task/jobid.go:JobID value object is missing")
	} else if err != nil {
		t.Fatalf("stat JobID source: %v", err)
	}

	err := canonicalWalkProductionGoFiles(repoRoot, func(relative string, fileSet *token.FileSet, parsed *ast.File) error {
		aliases, dotImport := canonicalImportAliases(parsed, canonicalTaskImportPath)
		if dotImport {
			violations = append(violations, relative+":dot import of canonical task package is forbidden")
		}
		if canonicalArchitectureInPackageDir(relative, "internal/domain/task") {
			for _, declaration := range parsed.Decls {
				switch declaration := declaration.(type) {
				case *ast.GenDecl:
					if declaration.Tok != token.TYPE {
						continue
					}
					for _, specification := range declaration.Specs {
						typeSpec, ok := specification.(*ast.TypeSpec)
						if ok && typeSpec.Name.Name == "Task" {
							violations = append(violations, canonicalArchitectureViolation(fileSet, typeSpec.Name.Pos(), relative, "retired type declaration Task"))
						}
					}
				case *ast.FuncDecl:
					if declaration.Name.Name == "NewTask" {
						violations = append(violations, canonicalArchitectureViolation(fileSet, declaration.Name.Pos(), relative, "retired function NewTask"))
					}
				}
			}
		}
		if len(aliases) == 0 {
			return nil
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok || (selector.Sel.Name != "Task" && selector.Sel.Name != "NewTask") {
				return true
			}
			packageName, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			if _, exactImport := aliases[packageName.Name]; exactImport {
				violations = append(violations, canonicalArchitectureViolation(fileSet, selector.Pos(), relative, packageName.Name+"."+selector.Sel.Name))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan production Go source for retired task selectors: %v", err)
	}

	canonicalArchitectureFail(t, "retired user-input Task must not return and JobID must remain", violations)
}

func TestCanonicalTurnInputArchitecture(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	relative := filepath.ToSlash(filepath.Join("internal", "domain", "conversation", "turn_input.go"))
	path := filepath.Join(repoRoot, filepath.FromSlash(relative))
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", relative, err)
	}

	var violations []string
	turnInput, found := canonicalArchitectureNamedStruct(parsed, "TurnInput")
	if !found {
		violations = append(violations, relative+":missing TurnInput struct")
	} else {
		for _, field := range turnInput.Fields.List {
			for _, name := range field.Names {
				normalized := canonicalArchitectureFieldName(name.Name)
				if normalized == "jobid" || normalized == "chatid" {
					violations = append(violations, canonicalArchitectureViolation(fileSet, name.Pos(), relative, "retired field "+name.Name))
				}
			}
		}
	}

	methodErr := canonicalWalkProductionGoFiles(repoRoot, func(methodRelative string, methodFileSet *token.FileSet, methodParsed *ast.File) error {
		if !canonicalArchitectureInPackageDir(methodRelative, "internal/domain/conversation") {
			return nil
		}
		_, dotImport := canonicalImportAliases(methodParsed, canonicalConversationImportPath)
		if dotImport {
			violations = append(violations, methodRelative+":dot import of canonical conversation package is forbidden")
		}
		for _, declaration := range methodParsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv == nil || !canonicalArchitectureReceiverType(function.Recv, "TurnInput") {
				continue
			}
			switch function.Name.Name {
			case "JobID", "ChatID", "WithConversationIdentity":
				violations = append(violations, canonicalArchitectureViolation(methodFileSet, function.Name.Pos(), methodRelative, "retired method "+function.Name.Name))
			}
		}
		return nil
	})
	if methodErr != nil {
		t.Fatalf("scan conversation production source for retired TurnInput methods: %v", methodErr)
	}

	canonicalArchitectureFail(t, "TurnInput canonical identities must be immutable and free of retired scalars", violations)
}

func TestCanonicalTurnInputTaskFieldArchitecture(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	var violations []string
	err := canonicalWalkProductionGoFiles(repoRoot, func(relative string, fileSet *token.FileSet, parsed *ast.File) error {
		aliases, dotImport := canonicalImportAliases(parsed, canonicalConversationImportPath)
		if dotImport {
			violations = append(violations, relative+":dot import of canonical conversation package is forbidden")
		}
		if len(aliases) == 0 {
			return nil
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			structType, ok := node.(*ast.StructType)
			if !ok {
				return true
			}
			for _, field := range structType.Fields.List {
				if !canonicalArchitectureConversationType(field.Type, aliases, "TurnInput", true) {
					continue
				}
				for _, name := range field.Names {
					if strings.EqualFold(name.Name, "task") {
						violations = append(violations, canonicalArchitectureViolation(fileSet, name.Pos(), relative, "Task field stores conversation.TurnInput"))
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan production Go source for TurnInput Task fields: %v", err)
	}

	canonicalArchitectureFail(t, "conversation.TurnInput must not be stored under a Task field", violations)
}

func TestCanonicalSessionInputArchitecture(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	relative := filepath.ToSlash(filepath.Join("internal", "domain", "session", "session.go"))
	path := filepath.Join(repoRoot, filepath.FromSlash(relative))
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", relative, err)
	}

	var violations []string
	session, found := canonicalArchitectureNamedStruct(parsed, "Session")
	if !found {
		violations = append(violations, relative+":missing Session struct")
		canonicalArchitectureFail(t, "Session input shape must retain canonical ChannelAddress", violations)
		return
	}
	conversationAliases, dotImport := canonicalImportAliases(parsed, canonicalConversationImportPath)
	if dotImport {
		violations = append(violations, relative+":dot import of canonical conversation package is forbidden")
	}
	foundHistory := false
	foundChannelAddress := false
	for _, field := range session.Fields.List {
		for _, name := range field.Names {
			normalized := canonicalArchitectureFieldName(name.Name)
			switch normalized {
			case "channel", "chatid":
				violations = append(violations, canonicalArchitectureViolation(fileSet, name.Pos(), relative, "legacy Session field "+name.Name))
			}
			switch name.Name {
			case "history":
				foundHistory = true
				if !canonicalArchitectureSliceOf(field.Type, conversationAliases, "TurnInput") {
					violations = append(violations, canonicalArchitectureViolation(fileSet, name.Pos(), relative, "history must be []conversation.TurnInput"))
				}
			case "channelAddress":
				foundChannelAddress = true
				if !canonicalArchitectureConversationType(field.Type, conversationAliases, "ChannelAddress", false) {
					violations = append(violations, canonicalArchitectureViolation(fileSet, name.Pos(), relative, "channelAddress must be conversation.ChannelAddress"))
				}
			}
		}
	}
	if !foundHistory {
		violations = append(violations, relative+":missing history []conversation.TurnInput")
	}
	if !foundChannelAddress {
		violations = append(violations, relative+":missing channelAddress conversation.ChannelAddress")
	}

	canonicalArchitectureFail(t, "Session must retain canonical TurnInput history and ChannelAddress", violations)
}

func canonicalArchitectureRepoRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(1)
	if !ok {
		t.Fatal("resolve architecture test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
}

func canonicalWalkProductionGoFiles(repoRoot string, visit func(relative string, fileSet *token.FileSet, parsed *ast.File) error) error {
	return filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if canonicalThreadIdentitySkipDir(repoRoot, path) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fileSet := token.NewFileSet()
		parsed, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		return visit(filepath.ToSlash(relative), fileSet, parsed)
	})
}

func canonicalImportAliases(parsed *ast.File, importPath string) (map[string]struct{}, bool) {
	aliases := make(map[string]struct{})
	defaultAlias := pathpkg.Base(importPath)
	dotImport := false
	for _, declaration := range parsed.Imports {
		path, err := strconv.Unquote(declaration.Path.Value)
		if err != nil || path != importPath {
			continue
		}
		alias := defaultAlias
		if declaration.Name != nil {
			alias = declaration.Name.Name
		}
		if alias == "." {
			dotImport = true
			continue
		}
		if alias == "_" {
			continue
		}
		aliases[alias] = struct{}{}
	}
	return aliases, dotImport
}

func canonicalArchitectureInPackageDir(relative, directory string) bool {
	return pathpkg.Dir(relative) == directory
}

func canonicalArchitectureNamedStruct(parsed *ast.File, name string) (*ast.StructType, bool) {
	for _, declaration := range parsed.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.TYPE {
			continue
		}
		for _, specification := range general.Specs {
			typeSpec, ok := specification.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != name {
				continue
			}
			structType, ok := typeSpec.Type.(*ast.StructType)
			return structType, ok
		}
	}
	return nil, false
}

func canonicalArchitectureFieldName(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "_", ""))
}

func canonicalArchitectureReceiverType(receiver *ast.FieldList, name string) bool {
	for _, field := range receiver.List {
		typeExpr := field.Type
		if parenthesized, ok := typeExpr.(*ast.ParenExpr); ok {
			typeExpr = parenthesized.X
		}
		if pointer, ok := typeExpr.(*ast.StarExpr); ok {
			typeExpr = pointer.X
		}
		identifier, ok := typeExpr.(*ast.Ident)
		if ok && identifier.Name == name {
			return true
		}
	}
	return false
}

func canonicalArchitectureConversationType(expression ast.Expr, aliases map[string]struct{}, name string, pointerAllowed bool) bool {
	if parenthesized, ok := expression.(*ast.ParenExpr); ok {
		return canonicalArchitectureConversationType(parenthesized.X, aliases, name, pointerAllowed)
	}
	if pointer, ok := expression.(*ast.StarExpr); ok {
		return pointerAllowed && canonicalArchitectureConversationType(pointer.X, aliases, name, false)
	}
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != name {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	_, exactImport := aliases[packageName.Name]
	return exactImport
}

func canonicalArchitectureSliceOf(expression ast.Expr, aliases map[string]struct{}, name string) bool {
	array, ok := expression.(*ast.ArrayType)
	return ok && array.Len == nil && canonicalArchitectureConversationType(array.Elt, aliases, name, false)
}

func canonicalArchitectureViolation(fileSet *token.FileSet, position token.Pos, relative, detail string) string {
	return fmt.Sprintf("%s:%d:%s", relative, fileSet.PositionFor(position, false).Line, detail)
}

func canonicalArchitectureFail(t *testing.T, message string, violations []string) {
	if len(violations) == 0 {
		return
	}
	sort.Strings(violations)
	shown := violations
	if len(shown) > canonicalThreadIdentityViolationLimit {
		shown = shown[:canonicalThreadIdentityViolationLimit]
	}
	t.Fatalf("%s: violations=%d showing=%d\n%s", message, len(violations), len(shown), strings.Join(shown, "\n"))
}
