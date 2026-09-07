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
		"NewQueueItemID": {}, "NewCheckpointID": {}, "NewReceiptID": {}, "NewBacklogItemID": {},
	}
	legacyGeneratorSites := map[string]struct{}{
		// Frozen legacy sites are removed by later canonical replacement steps.
		// This allowlist prevents Step 01 from increasing their scope.
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
		"internal/application/backlog/service.go:Adopt":                                                        {},
		"internal/application/heartbeat/service.go:RunBacklogIntake":                                           {},
		"internal/application/idlechat/orchestrator.go:applyPersonaCanonicalResponse":                          {},
		"internal/application/idlechat/orchestrator.go:recordPersonaTimelineEvent":                             {},
		"internal/application/orchestrator/message_orchestrator_persona.go:applyPersonaCanonicalResponse":      {},
		"internal/application/orchestrator/message_orchestrator_persona.go:recordPersonaRuntimeObservation":    {},
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

func TestCanonicalTurnInputMigrationSourceIsRemovedAfterCutover(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current file")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	for _, relative := range []string{
		filepath.Join("cmd", "rencrow-turn-input-migrate"),
		filepath.Join("internal", "infrastructure", "persistence", "turninputmigration"),
	} {
		if _, err := os.Stat(filepath.Join(repoRoot, relative)); err == nil || !os.IsNotExist(err) {
			t.Fatalf("Step 07 migration source remains after production cutover: %s", relative)
		}
	}
}

func TestCanonicalTaskSubsystemHasNoLegacyJobContract(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current file")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	for _, relative := range []string{
		filepath.Join("internal", "domain", "job"),
		filepath.Join("internal", "application", "jobmanager"),
		filepath.Join("internal", "infrastructure", "persistence", "job"),
		filepath.Join("cmd", "rencrow", "cli_jobs.go"),
		filepath.Join("internal", "adapter", "viewer", "job_handler.go"),
	} {
		if _, err := os.Stat(filepath.Join(repoRoot, relative)); err == nil || !os.IsNotExist(err) {
			t.Errorf("retired Step08 Job owner remains: %s", relative)
		}
	}

	for _, relative := range []string{
		filepath.Join("internal", "domain", "task"),
		filepath.Join("internal", "application", "taskmanager"),
		filepath.Join("internal", "infrastructure", "persistence", "task"),
		filepath.Join("cmd", "rencrow", "cli_tasks.go"),
		filepath.Join("internal", "adapter", "viewer", "task_handler.go"),
	} {
		path := filepath.Join(repoRoot, relative)
		err := filepath.WalkDir(path, func(candidate string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || strings.HasSuffix(candidate, "_test.go") {
				return nil
			}
			content, err := os.ReadFile(candidate)
			if err != nil {
				return err
			}
			for _, token := range []string{"JobID", "job_id", "internal/domain/job", "jobmanager"} {
				if strings.Contains(string(content), token) {
					t.Errorf("legacy Step08 token %q remains in %s", token, strings.TrimPrefix(candidate, repoRoot+string(filepath.Separator)))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan canonical Task owner %s: %v", relative, err)
		}
	}

	registrar, err := os.ReadFile(filepath.Join(repoRoot, "internal", "features", "ops", "registrar.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, legacyRoute := range []string{"/viewer/parallel-jobs", "/viewer/parallel-job/detail", "/viewer/job-notifications"} {
		if strings.Contains(string(registrar), legacyRoute) {
			t.Errorf("legacy Task route remains: %s", legacyRoute)
		}
	}
	for _, canonicalRoute := range []string{"/viewer/tasks", "/viewer/task/detail", "/viewer/task-notifications"} {
		if !strings.Contains(string(registrar), canonicalRoute) {
			t.Errorf("canonical Task route is missing: %s", canonicalRoute)
		}
	}
}

func TestCanonicalTaskMigrationSourceIsRemovedAfterCutover(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current file")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	for _, relative := range []string{
		filepath.Join("cmd", "rencrow-task-store-migrate"),
		filepath.Join("internal", "infrastructure", "persistence", "taskmigration"),
	} {
		if _, err := os.Stat(filepath.Join(repoRoot, relative)); err == nil || !os.IsNotExist(err) {
			t.Fatalf("Step 08 migration source remains after production cutover: %s", relative)
		}
	}
	for _, relative := range []string{
		filepath.Join("cmd", "rencrow-event-task-migrate"),
		filepath.Join("internal", "infrastructure", "persistence", "eventtaskmigration"),
	} {
		if _, err := os.Stat(filepath.Join(repoRoot, relative)); err == nil || !os.IsNotExist(err) {
			t.Fatalf("Step 09 migration source remains after production cutover: %s", relative)
		}
	}
	for _, relative := range []string{
		filepath.Join("cmd", "rencrow-step10-run-migrate"),
		filepath.Join("internal", "infrastructure", "persistence", "step10runmigration"),
	} {
		if _, err := os.Stat(filepath.Join(repoRoot, relative)); err == nil || !os.IsNotExist(err) {
			t.Fatalf("Step 10 migration source remains after production cutover: %s", relative)
		}
	}
}

func TestStep10RunIdentityLegacyFieldsAreBanned(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	legacyTokens := []string{
		"ParentRunID",
		"TraceRunID",
		"GenerationID",
		"SubagentID",
		"parent_run_id",
		"trace_run_id",
		"subagent_id",
		"generation_id",
	}
	var violations []string
	checkGoSource := func(relative string, content []byte) {
		for lineNumber, line := range strings.Split(string(content), "\n") {
			for _, token := range legacyTokens {
				if canonicalSourceContainsToken(line, token) {
					violations = append(violations, fmt.Sprintf("%s:%d:legacy-run-identity:%s", relative, lineNumber+1, token))
				}
			}
		}
	}
	shouldSkipStep10Path := func(relative string) bool {
		return strings.Contains(relative, "step10runmigration") ||
			strings.Contains(relative, "rencrow-step10-run-migrate")
	}
	walkDirectory := func(relative string) {
		root := filepath.Join(repoRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(root); os.IsNotExist(err) {
			return
		}
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if entry.Name() == "step10runmigration" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if shouldSkipStep10Path(rel) {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			checkGoSource(rel, content)
			return nil
		})
		if err != nil {
			t.Fatalf("scan Step10 owner %s: %v", relative, err)
		}
	}
	for _, relative := range []string{
		"internal/domain/browsertrace",
		"internal/application/browsertrace",
		"internal/infrastructure/persistence/browsertrace",
		"internal/domain/knowledgememory",
		"internal/application/knowledgememory",
		"internal/infrastructure/persistence/knowledgememory",
		"internal/domain/aiworkflow",
		"internal/infrastructure/persistence/aiworkflow",
		"internal/application/toolloop",
		"internal/application/idlechat",
		"pkg/rencrowclient",
	} {
		walkDirectory(relative)
	}
	viewerDir := filepath.Join(repoRoot, "internal", "adapter", "viewer")
	viewerEntries, err := os.ReadDir(viewerDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read viewer adapter directory: %v", err)
	}
	for _, entry := range viewerEntries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "browser_trace") &&
			!strings.HasPrefix(name, "knowledge_memory") &&
			!strings.HasPrefix(name, "ai_workflow") {
			continue
		}
		relative := filepath.ToSlash(filepath.Join("internal", "adapter", "viewer", name))
		content, err := os.ReadFile(filepath.Join(viewerDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		checkGoSource(relative, content)
	}
	opsJSRelative := filepath.ToSlash(filepath.Join("internal", "adapter", "viewer", "assets", "js", "tabs", "ops.js"))
	opsJSPath := filepath.Join(repoRoot, filepath.FromSlash(opsJSRelative))
	if opsContent, err := os.ReadFile(opsJSPath); err != nil {
		if !os.IsNotExist(err) {
			t.Fatalf("read %s: %v", opsJSRelative, err)
		}
	} else {
		checkGoSource(opsJSRelative, opsContent)
	}
	cmdDir := filepath.Join(repoRoot, "cmd", "rencrow")
	cmdEntries, err := os.ReadDir(cmdDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read cmd/rencrow directory: %v", err)
	}
	for _, entry := range cmdEntries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "runtime_background_jobs") && !strings.HasPrefix(name, "runtime_idlechat") {
			continue
		}
		relative := filepath.ToSlash(filepath.Join("cmd", "rencrow", name))
		content, err := os.ReadFile(filepath.Join(cmdDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		checkGoSource(relative, content)
	}
	canonicalArchitectureFail(t, "Step10 owner packages must not retain legacy Run identity fields", violations)
}

func TestStep11ActionIdentityLegacyFieldsAreBanned(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	legacyTokens := []string{
		"ApplyID",
		"SubmitID",
		"apply_id",
		"submit_id",
		"nextActionID",
	}
	var violations []string
	checkContent := func(relative string, content []byte) {
		for lineNumber, line := range strings.Split(string(content), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			for _, token := range legacyTokens {
				if canonicalSourceContainsToken(line, token) {
					violations = append(violations, fmt.Sprintf("%s:%d:legacy-action-identity:%s", relative, lineNumber+1, token))
				}
			}
			if strings.Contains(line, `"act-%d-%d"`) || strings.Contains(line, `"act-%d"`) {
				violations = append(violations, fmt.Sprintf("%s:%d:legacy-action-mint", relative, lineNumber+1))
			}
		}
	}
	shouldSkip := func(relative string) bool {
		return strings.Contains(relative, "step11actionmigration") ||
			strings.Contains(relative, "rencrow-step11-action-migrate")
	}
	walkDirectory := func(relative string) {
		root := filepath.Join(repoRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(root); os.IsNotExist(err) {
			return
		}
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if entry.Name() == "step11actionmigration" {
					return filepath.SkipDir
				}
				return nil
			}
			if !(strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".js")) {
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if shouldSkip(rel) {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			checkContent(rel, content)
			return nil
		})
		if err != nil {
			t.Fatalf("scan Step11 owner %s: %v", relative, err)
		}
	}
	for _, relative := range []string{
		"internal/domain/execution",
		"internal/application/execution",
		"internal/infrastructure/persistence/execution",
		"internal/infrastructure/security",
		"internal/domain/revenue",
		"internal/domain/skillgovernance",
		"internal/infrastructure/persistence/revenue",
		"internal/infrastructure/persistence/skillgovernance",
		"internal/application/toolloop",
		"internal/application/actionmanager",
		"internal/domain/action",
		"pkg/rencrowclient",
	} {
		walkDirectory(relative)
	}
	for _, relative := range []string{
		"internal/adapter/viewer/revenue_handler.go",
		"internal/adapter/viewer/skill_governance_handler.go",
		"internal/adapter/viewer/assets/js/tabs/ops.js",
	} {
		path := filepath.Join(repoRoot, filepath.FromSlash(relative))
		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read %s: %v", relative, err)
		}
		checkContent(relative, content)
	}
	canonicalArchitectureFail(t, "Step11 owner packages must not retain ApplyID SubmitID nextActionID or legacy JSON keys", violations)
}

func TestStep11MigrationSourceIsRemovedAfterCutover(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	for _, relative := range []string{
		filepath.Join("cmd", "rencrow-step11-action-migrate"),
		filepath.Join("internal", "infrastructure", "persistence", "step11actionmigration"),
	} {
		if _, err := os.Stat(filepath.Join(repoRoot, relative)); err == nil || !os.IsNotExist(err) {
			t.Fatalf("Step 11 migration source remains after production cutover: %s", relative)
		}
	}
}

func TestStep12RequestResponseLegacyFieldsAreBanned(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	var violations []string

	shouldSkipStep12Path := func(relative string) bool {
		return strings.Contains(relative, "step12requestmigration") ||
			strings.Contains(relative, "rencrow-step12-request-migrate")
	}
	shouldSkipToolCallPath := func(relative string) bool {
		return strings.Contains(relative, "providers/rencrowllm")
	}
	shouldSkipResponseIDPath := func(relative string) bool {
		return strings.Contains(relative, "modules/core") ||
			strings.Contains(relative, "internal/infrastructure/tts")
	}

	checkContent := func(relative string, content []byte, tokens []string, skip func(string, string) bool) {
		for lineNumber, line := range strings.Split(string(content), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if skip != nil && skip(relative, line) {
				continue
			}
			for _, token := range tokens {
				if canonicalSourceContainsToken(line, token) {
					violations = append(violations, fmt.Sprintf("%s:%d:legacy-step12:%s", relative, lineNumber+1, token))
				}
			}
		}
	}
	walkDirectory := func(relative string, tokens []string, skipPath func(string) bool, skipLine func(string, string) bool) {
		root := filepath.Join(repoRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(root); os.IsNotExist(err) {
			return
		}
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if entry.Name() == "step12requestmigration" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if shouldSkipStep12Path(rel) || (skipPath != nil && skipPath(rel)) {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			checkContent(rel, content, tokens, skipLine)
			return nil
		})
		if err != nil {
			t.Fatalf("scan Step12 owner %s: %v", relative, err)
		}
	}

	toolCallTokens := []string{"ToolCallID", "tool_call_id"}
	for _, relative := range []string{
		"internal/domain/llm",
		"internal/application/toolloop",
		"internal/infrastructure/llm/middleware",
	} {
		walkDirectory(relative, toolCallTokens, shouldSkipToolCallPath, nil)
	}

	resolutionTokens := []string{"ResolutionRequestID", "resolution_request_id"}
	for _, relative := range []string{
		"internal/domain/workstream",
		"internal/domain/backlog",
		"internal/application/backlog",
	} {
		walkDirectory(relative, resolutionTokens, nil, nil)
	}

	writeReceiptTokens := []string{"RequestID"}
	for _, relative := range []string{
		"internal/domain/knowledgememory",
		"internal/application/knowledgememory",
		"internal/infrastructure/persistence/knowledgememory",
		"internal/infrastructure/persistence/toolregistry",
		"internal/domain/durablestore",
		"internal/application/durablestore",
		"internal/infrastructure/persistence/durablestore",
	} {
		walkDirectory(relative, writeReceiptTokens, nil, nil)
	}

	personaTokens := []string{"ResponseID", "response_id"}
	for _, relative := range []string{
		"internal/domain/persona",
		"internal/application/idlechat",
		"internal/infrastructure/persistence/persona",
	} {
		walkDirectory(relative, personaTokens, shouldSkipResponseIDPath, nil)
	}

	canonicalArchitectureFail(t, "Step12 owner packages must not retain legacy Request/Response identity fields", violations)
}

func TestStep12MigrationSourceIsRemovedAfterCutover(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	for _, relative := range []string{
		filepath.Join("cmd", "rencrow-step12-request-migrate"),
		filepath.Join("internal", "infrastructure", "persistence", "step12requestmigration"),
	} {
		if _, err := os.Stat(filepath.Join(repoRoot, relative)); err == nil || !os.IsNotExist(err) {
			t.Fatalf("Step 12 migration source remains after production cutover: %s", relative)
		}
	}
}

func TestStep13ArtifactIdentityLegacyFieldsAreBanned(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	legacyTokens := []string{
		"ReportID",
		"DraftID",
		"ContextPackID",
		"report_id",
		"draft_id",
		"context_pack_id",
	}
	var violations []string
	shouldSkip := func(relative string) bool {
		return strings.Contains(relative, "step13artifactmigration") ||
			strings.Contains(relative, "rencrow-step13-artifact-migrate")
	}
	checkContent := func(relative string, content []byte) {
		for lineNumber, line := range strings.Split(string(content), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			for _, token := range legacyTokens {
				if canonicalSourceContainsToken(line, token) {
					violations = append(violations, fmt.Sprintf("%s:%d:legacy-step13:%s", relative, lineNumber+1, token))
				}
			}
		}
	}
	walkDirectory := func(relative string) {
		root := filepath.Join(repoRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(root); os.IsNotExist(err) {
			return
		}
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if entry.Name() == "step13artifactmigration" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if shouldSkip(rel) {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			checkContent(rel, content)
			return nil
		})
		if err != nil {
			t.Fatalf("scan Step13 owner %s: %v", relative, err)
		}
	}
	for _, relative := range []string{
		"internal/domain/revenue",
		"internal/application/revenue",
		"internal/infrastructure/persistence/revenue",
		"internal/domain/superagent",
		"internal/application/superagent",
		"internal/infrastructure/persistence/superagent",
		"internal/domain/browsertrace",
		"internal/application/browsertrace",
		"internal/infrastructure/persistence/browsertrace",
		"pkg/rencrowclient",
	} {
		walkDirectory(relative)
	}
	for _, relative := range []string{
		"internal/adapter/viewer/revenue_handler.go",
		"internal/adapter/viewer/browser_trace_api_handler.go",
	} {
		path := filepath.Join(repoRoot, filepath.FromSlash(relative))
		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read %s: %v", relative, err)
		}
		checkContent(relative, content)
	}
	canonicalArchitectureFail(t, "Step13 owner packages must not retain legacy Artifact identity fields", violations)
}

func TestStep13MigrationSourceIsRemovedAfterCutover(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	for _, relative := range []string{
		filepath.Join("cmd", "rencrow-step13-artifact-migrate"),
		filepath.Join("internal", "infrastructure", "persistence", "step13artifactmigration"),
	} {
		if _, err := os.Stat(filepath.Join(repoRoot, relative)); err == nil || !os.IsNotExist(err) {
			t.Fatalf("Step 13 migration source remains after production cutover: %s", relative)
		}
	}
}

func TestStep14EvidenceMemoryLegacyFieldsAreBanned(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	var violations []string
	shouldSkip := func(relative string) bool {
		return strings.Contains(relative, "step14evidencememorymigration") ||
			strings.Contains(relative, "rencrow-step14-evidence-memory-migrate")
	}
	checkFileTokens := func(relative string, tokens []string) {
		path := filepath.Join(repoRoot, filepath.FromSlash(relative))
		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return
			}
			t.Fatalf("read %s: %v", relative, err)
		}
		if shouldSkip(relative) {
			return
		}
		for lineNumber, line := range strings.Split(string(content), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			for _, token := range tokens {
				if canonicalSourceContainsToken(line, token) {
					violations = append(violations, fmt.Sprintf("%s:%d:legacy-step14:%s", relative, lineNumber+1, token))
				}
			}
		}
	}
	walkDirectory := func(relative string, tokens []string) {
		root := filepath.Join(repoRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(root); os.IsNotExist(err) {
			return
		}
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if entry.Name() == "step14evidencememorymigration" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if shouldSkip(rel) {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for lineNumber, line := range strings.Split(string(content), "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "//") {
					continue
				}
				for _, token := range tokens {
					if canonicalSourceContainsToken(line, token) {
						violations = append(violations, fmt.Sprintf("%s:%d:legacy-step14:%s", rel, lineNumber+1, token))
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan Step14 owner %s: %v", relative, err)
		}
	}

	checkFileTokens("internal/domain/aiworkflow/types.go", []string{`json:"id"`, "\tID "})
	checkFileTokens("internal/infrastructure/persistence/aiworkflow/sqlite_store.go", []string{" project_memory_index ", "\tid TEXT"})

	if block := canonicalArchitectureStructBlock(repoRoot, "internal/domain/verification/types.go", "VerificationReport"); block != "" {
		if strings.Contains(block, `json:"id"`) && !strings.Contains(block, `json:"artifact_id"`) {
			violations = append(violations, "internal/domain/verification/types.go:VerificationReport retains json:\"id\" primary field")
		}
	}
	if block := canonicalArchitectureStructBlock(repoRoot, "internal/domain/aiworkflow/types.go", "ProjectMemoryIndex"); block != "" {
		if strings.Contains(block, `json:"id"`) || strings.Contains(block, "\tID ") {
			violations = append(violations, "internal/domain/aiworkflow/types.go:ProjectMemoryIndex retains legacy ID field")
		}
	}

	walkDirectory("internal/application/complexity", []string{"_ev_"})
	walkDirectory("internal/domain/complexity", []string{"_ev_"})

	profilePromotionPath := filepath.Join(repoRoot, "internal/domain/memory/profile_promotion.go")
	profilePromotionContent, err := os.ReadFile(profilePromotionPath)
	if err != nil {
		t.Fatalf("read profile promotion types: %v", err)
	}
	profilePromotionSource := string(profilePromotionContent)
	if !strings.Contains(profilePromotionSource, "type ProfilePromotionJob struct") {
		violations = append(violations, "internal/domain/memory/profile_promotion.go:missing ProfilePromotionJob struct")
	}
	if !strings.Contains(profilePromotionSource, "TaskID") || !strings.Contains(profilePromotionSource, "RunID") {
		violations = append(violations, "internal/domain/memory/profile_promotion.go:missing TaskID or RunID on ProfilePromotionJob")
	}

	canonicalArchitectureFail(t, "Step14 owner packages must not retain legacy Evidence/Memory identity fields", violations)
}

func TestStep14MigrationSourceIsRemovedAfterCutover(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	leftovers := []string{
		filepath.Join(repoRoot, "cmd", "rencrow-step14-evidence-memory-migrate"),
		filepath.Join(repoRoot, "internal", "infrastructure", "persistence", "step14evidencememorymigration"),
	}
	var found []string
	for _, path := range leftovers {
		if _, err := os.Stat(path); err == nil {
			found = append(found, path)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", path, err)
		}
	}
	if len(found) != 0 {
		t.Fatalf("Step14 migration source must be removed after cutover: %v", found)
	}
}

func TestStep15SchedulerLegacyFieldsAreBanned(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	legacyTokens := []string{
		"JobID",
		"job_id",
		"HeartbeatID",
		"heartbeat_id",
		"schedrun_",
	}
	var violations []string
	shouldSkip := func(relative string) bool {
		return strings.Contains(relative, "step15schedulermigration") ||
			strings.Contains(relative, "rencrow-step15-scheduler-migrate")
	}
	allowToken := func(relative string, lineNumber int, token string) bool {
		if relative == "internal/infrastructure/persistence/workstream/sqlite_store.go" &&
			token == "heartbeat_id" && strings.Contains(lineAt(repoRoot, relative, lineNumber), "RENAME COLUMN") {
			return true
		}
		return false
	}
	checkContent := func(relative string, content []byte) {
		if shouldSkip(relative) {
			return
		}
		if strings.Contains(relative, "complexity") || strings.Contains(relative, "moviecatalog") ||
			strings.Contains(relative, "movie_catalog") {
			return
		}
		for lineNumber, line := range strings.Split(string(content), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			for _, token := range legacyTokens {
				if token == "JobID" && (strings.Contains(relative, "complexity") || strings.Contains(relative, "moviecatalog") || strings.Contains(relative, "movie_catalog")) {
					continue
				}
				if canonicalSourceContainsToken(line, token) {
					if allowToken(relative, lineNumber+1, token) {
						continue
					}
					violations = append(violations, fmt.Sprintf("%s:%d:legacy-step15:%s", relative, lineNumber+1, token))
				}
			}
		}
	}
	walkDirectory := func(relative string) {
		root := filepath.Join(repoRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(root); os.IsNotExist(err) {
			return
		}
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if entry.Name() == "step15schedulermigration" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			checkContent(rel, mustReadFile(t, path))
			return nil
		})
		if err != nil {
			t.Fatalf("scan Step15 owner %s: %v", relative, err)
		}
	}
	for _, relative := range []string{
		"internal/domain/scheduler",
		"internal/application/scheduler",
		"internal/infrastructure/persistence/scheduler",
		"internal/domain/workstream",
		"internal/application/heartbeat",
		"internal/infrastructure/persistence/workstream",
	} {
		walkDirectory(relative)
	}
	for _, relative := range []string{
		"internal/adapter/viewer/scheduler_handler.go",
	} {
		path := filepath.Join(repoRoot, filepath.FromSlash(relative))
		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read %s: %v", relative, err)
		}
		checkContent(relative, content)
	}
	canonicalArchitectureFail(t, "Step15 owner packages must not retain legacy Scheduler/Heartbeat identity fields", violations)
}

func TestStep16QueueCheckpointReceiptLegacyFieldsAreBanned(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	superagentTokens := []string{
		"QueueID",
		"queue_id",
		"GenerationID",
		"generation_id",
	}
	receiptTokens := []string{
		"atlas-stage",
		"atlas-closure",
		"RequestID",
		"request_id",
	}
	atlasTokens := []string{
		"atlas-stage",
		"atlas-closure",
	}
	var violations []string
	shouldSkip := func(relative string) bool {
		return strings.Contains(relative, "step16queuecheckpointreceiptmigration") ||
			strings.Contains(relative, "rencrow-step16-queue-checkpoint-receipt-migrate")
	}
	allowToken := func(relative string, lineNumber int, token string) bool {
		if relative == "internal/infrastructure/persistence/superagent/sqlite_store.go" &&
			token == "queue_id" && strings.Contains(lineAt(repoRoot, relative, lineNumber), "RENAME COLUMN") {
			return true
		}
		return false
	}
	checkContent := func(relative string, content []byte, tokens []string) {
		if shouldSkip(relative) {
			return
		}
		for lineNumber, line := range strings.Split(string(content), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			for _, token := range tokens {
				if canonicalSourceContainsToken(line, token) {
					if allowToken(relative, lineNumber+1, token) {
						continue
					}
					violations = append(violations, fmt.Sprintf("%s:%d:legacy-step16:%s", relative, lineNumber+1, token))
				}
			}
		}
	}
	walkDirectory := func(relative string, tokens []string) {
		root := filepath.Join(repoRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(root); os.IsNotExist(err) {
			return
		}
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if entry.Name() == "step16queuecheckpointreceiptmigration" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if shouldSkip(rel) {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			checkContent(rel, content, tokens)
			return nil
		})
		if err != nil {
			t.Fatalf("scan Step16 owner %s: %v", relative, err)
		}
	}
	for _, relative := range []string{
		"internal/domain/superagent",
		"internal/application/superagent",
		"internal/infrastructure/persistence/superagent",
	} {
		walkDirectory(relative, superagentTokens)
	}
	for _, relative := range []string{
		"internal/domain/workstream",
		"internal/infrastructure/persistence/workstream",
	} {
		walkDirectory(relative, receiptTokens)
	}
	lifecyclePath := filepath.Join(repoRoot, "internal/application/backlog/lifecycle.go")
	if lifecycleContent, err := os.ReadFile(lifecyclePath); err != nil {
		if !os.IsNotExist(err) {
			t.Fatalf("read backlog lifecycle: %v", err)
		}
	} else {
		checkContent(filepath.ToSlash(filepath.Join("internal", "application", "backlog", "lifecycle.go")), lifecycleContent, atlasTokens)
	}
	for _, relative := range []string{
		"internal/adapter/viewer/superagent_handler.go",
		"cmd/rencrow/runtime_background_jobs.go",
		"pkg/rencrowclient/client.go",
	} {
		path := filepath.Join(repoRoot, filepath.FromSlash(relative))
		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read %s: %v", relative, err)
		}
		checkContent(relative, content, superagentTokens)
	}
	if block := canonicalArchitectureStructBlock(repoRoot, "internal/domain/workstream/types.go", "StageRunReceipt"); block != "" {
		if strings.Contains(block, "RequestID") || strings.Contains(block, `json:"request_id"`) {
			violations = append(violations, "internal/domain/workstream/types.go:StageRunReceipt retains legacy request_id field")
		}
	}
	if block := canonicalArchitectureStructBlock(repoRoot, "internal/domain/workstream/types.go", "ClosureReceipt"); block != "" {
		if strings.Contains(block, "RequestID") || strings.Contains(block, `json:"request_id"`) {
			violations = append(violations, "internal/domain/workstream/types.go:ClosureReceipt retains legacy request_id field")
		}
	}
	canonicalArchitectureFail(t, "Step16 owner packages must not retain legacy Queue/Checkpoint/Receipt identity fields", violations)
}

func TestStep16MigrationSourceIsRemovedAfterCutover(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	leftovers := []string{
		filepath.Join(repoRoot, "cmd", "rencrow-step16-queue-checkpoint-receipt-migrate"),
		filepath.Join(repoRoot, "internal", "infrastructure", "persistence", "step16queuecheckpointreceiptmigration"),
	}
	var found []string
	for _, path := range leftovers {
		if _, err := os.Stat(path); err == nil {
			found = append(found, path)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", path, err)
		}
	}
	if len(found) != 0 {
		t.Fatalf("Step16 migration source must be removed after cutover: %v", found)
	}
}

func TestStep17VoiceIdleChatLegacyFieldsAreBanned(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	legacyTokens := []string{
		"ChatID",
		"chat_id",
		"GenerationID",
		"generation_id",
	}
	allowToken := func(relative string, line string, token string) bool {
		switch token {
		case "ChatID", "chat_id":
			return false
		case "GenerationID", "generation_id":
			if strings.Contains(relative, "topic_generator") ||
				strings.Contains(relative, "TopicGeneration") ||
				strings.Contains(line, "TopicGeneration") {
				return true
			}
			if strings.Contains(line, "generation_attempts") ||
				strings.Contains(line, "client_generation") ||
				strings.Contains(line, "BuildGenerationPrompt") ||
				strings.Contains(line, "generationCheckpoints") ||
				strings.Contains(line, "GenerationCheckpoint") {
				return true
			}
			if strings.Contains(line, "activeGeneration") ||
				strings.Contains(line, "dailyEnrichmentGeneration") ||
				strings.Contains(line, "recordIdleMessageForGeneration") ||
				strings.Contains(line, "cancelIdleRunIfGeneration") ||
				strings.Contains(line, "generateResponseWithRawForGeneration") ||
				strings.Contains(line, "applyPersonaCanonicalResponseForGeneration") ||
				strings.Contains(line, "recordGenerationErrorToTimeline") ||
				strings.Contains(line, "isWordTopicGenerationError") ||
				strings.Contains(line, "isForecastTopicGenerationError") ||
				strings.Contains(line, "formatTopicGenerationContext") ||
				strings.Contains(line, "countTopicGenerationRequests") ||
				strings.Contains(line, "topicGenerationConfig") ||
				strings.Contains(line, "TopicGenerationResult") ||
				strings.Contains(line, "TopicGenerationConfig") ||
				strings.Contains(line, "TopicGenerationPrompt") ||
				strings.Contains(line, "TopicGenerationResume") ||
				strings.Contains(line, "TopicGenerationProgress") ||
				strings.Contains(line, "TopicGenerationDiagnostic") ||
				strings.Contains(line, "ErrTopicGeneration") ||
				strings.Contains(line, "watchdogDialogueGenerationStage") ||
				strings.Contains(line, "forecastDialogueGenerationWatchdogTimeout") {
				return true
			}
		}
		return false
	}
	var violations []string
	checkContent := func(relative string, content []byte) {
		for lineNumber, line := range strings.Split(string(content), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			for _, token := range legacyTokens {
				if !canonicalSourceContainsToken(line, token) {
					continue
				}
				if allowToken(relative, line, token) {
					continue
				}
				violations = append(violations, fmt.Sprintf("%s:%d:legacy-step17:%s", relative, lineNumber+1, token))
			}
		}
	}
	walkDirectory := func(relative string, extensions ...string) {
		if len(extensions) == 0 {
			extensions = []string{".go"}
		}
		root := filepath.Join(repoRoot, filepath.FromSlash(relative))
		info, err := os.Stat(root)
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Fatalf("stat Step17 owner %s: %v", relative, err)
		}
		if !info.IsDir() {
			checkContent(relative, mustReadFile(t, root))
			return
		}
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			allowed := false
			for _, ext := range extensions {
				if strings.HasSuffix(path, ext) {
					allowed = true
					break
				}
			}
			if !allowed || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			checkContent(filepath.ToSlash(rel), mustReadFile(t, path))
			return nil
		})
		if err != nil {
			t.Fatalf("scan Step17 owner %s: %v", relative, err)
		}
	}
	for _, relative := range []string{
		"internal/application/voiceinput",
		"internal/application/idlechat",
	} {
		walkDirectory(relative)
	}
	for _, relative := range []string{
		"internal/application/orchestrator/voice_direct.go",
		"modules/tts/event_payload.go",
		"internal/adapter/viewer/assets/js/tabs/idlechat.js",
		"cmd/rencrow/runtime_idlechat.go",
	} {
		path := filepath.Join(repoRoot, filepath.FromSlash(relative))
		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read %s: %v", relative, err)
		}
		checkContent(relative, content)
	}
	cmdDir := filepath.Join(repoRoot, "cmd", "rencrow")
	cmdEntries, err := os.ReadDir(cmdDir)
	if err != nil {
		t.Fatalf("read cmd/rencrow: %v", err)
	}
	for _, entry := range cmdEntries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "voice_chat_runtime_") || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		relative := filepath.ToSlash(filepath.Join("cmd", "rencrow", name))
		checkContent(relative, mustReadFile(t, filepath.Join(cmdDir, name)))
	}
	canonicalArchitectureFail(t, "Step17 owner packages must not retain legacy Voice/IdleChat identity fields", violations)
}

func TestStep18WorkstreamAtlasBacklogLegacyFieldsAreBanned(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	legacyTokens := []string{
		"ItemID",
		"item_id",
		"ResultID",
		"RecordID",
	}
	allowedTokens := map[string]struct{}{
		"BacklogItemID":   {},
		"backlog_item_id": {},
	}
	var violations []string
	shouldSkip := func(relative string) bool {
		return strings.Contains(relative, "step18backlogitemmigration") ||
			strings.Contains(relative, "rencrow-step18-backlog-item-migrate")
	}
	allowToken := func(line string, token string) bool {
		if token == "item_id" && strings.Contains(line, `json:"item_id"`) && strings.Contains(line, "LegacyItemID") {
			return true
		}
		if token == "item_id" && strings.Contains(line, "no item_id") {
			return true
		}
		return false
	}
	checkContent := func(relative string, content []byte) {
		if shouldSkip(relative) {
			return
		}
		for lineNumber, line := range strings.Split(string(content), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			for _, token := range legacyTokens {
				if !canonicalSourceContainsToken(line, token) {
					continue
				}
				if _, allowed := allowedTokens[token]; allowed {
					continue
				}
				if allowToken(line, token) {
					continue
				}
				violations = append(violations, fmt.Sprintf("%s:%d:legacy-step18:%s", relative, lineNumber+1, token))
			}
		}
	}
	walkDirectory := func(relative string) {
		root := filepath.Join(repoRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(root); os.IsNotExist(err) {
			return
		}
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if entry.Name() == "step18backlogitemmigration" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if shouldSkip(rel) {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			checkContent(rel, content)
			return nil
		})
		if err != nil {
			t.Fatalf("scan Step18 owner %s: %v", relative, err)
		}
	}
	for _, relative := range []string{
		"internal/domain/backlog",
		"internal/application/backlog",
		"internal/infrastructure/backlog",
		"internal/features/backlog",
		"internal/domain/workstream",
		"internal/infrastructure/persistence/workstream",
	} {
		walkDirectory(relative)
	}
	for _, relative := range []string{
		"internal/adapter/viewer/backlog_handler.go",
		"internal/adapter/viewer/atlas_handler.go",
	} {
		path := filepath.Join(repoRoot, filepath.FromSlash(relative))
		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read %s: %v", relative, err)
		}
		checkContent(relative, content)
	}
	canonicalArchitectureFail(t, "Step18 owner packages must not retain legacy backlog/workstream atlas ItemID fields", violations)
}

func TestStep19ViewerOTelGraphLegacyFieldsAreBanned(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	legacyTokens := []string{
		"job_id",
		"chat_id",
	}
	syntheticTraceMarkers := []string{
		`fmt.Sprintf("trace-`,
		`"trace-"`,
		`'trace-'`,
		`trace-%d`,
	}
	var violations []string
	allowLegacyToken := func(relative, line, token string) bool {
		return false
	}
	allowSyntheticTrace := func(relative, line string) bool {
		if strings.Contains(line, "browser-trace-api") {
			return true
		}
		return false
	}
	checkContent := func(relative string, content []byte) {
		for lineNumber, line := range strings.Split(string(content), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			for _, token := range legacyTokens {
				if !canonicalSourceContainsToken(line, token) {
					continue
				}
				if allowLegacyToken(relative, line, token) {
					continue
				}
				violations = append(violations, fmt.Sprintf("%s:%d:legacy-step19:%s", relative, lineNumber+1, token))
			}
			for _, marker := range syntheticTraceMarkers {
				if strings.Contains(line, marker) && !allowSyntheticTrace(relative, line) {
					violations = append(violations, fmt.Sprintf("%s:%d:synthetic-trace-step19:%s", relative, lineNumber+1, marker))
				}
			}
		}
	}
	walkGoDirectory := func(relative string) {
		root := filepath.Join(repoRoot, filepath.FromSlash(relative))
		info, err := os.Stat(root)
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Fatalf("stat Step19 owner %s: %v", relative, err)
		}
		if !info.IsDir() {
			checkContent(relative, mustReadFile(t, root))
			return
		}
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			checkContent(filepath.ToSlash(rel), mustReadFile(t, path))
			return nil
		})
		if err != nil {
			t.Fatalf("scan Step19 owner %s: %v", relative, err)
		}
	}
	walkGoDirectory("internal/application/otelexport")
	for _, relative := range []string{
		"internal/adapter/viewer/assets/js/viewer.js",
		"internal/adapter/viewer/assets/js/tabs/timeline.js",
		"internal/adapter/viewer/assets/js/tabs/ops.js",
		"internal/adapter/viewer/assets/js/tabs/idlechat.js",
	} {
		path := filepath.Join(repoRoot, filepath.FromSlash(relative))
		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read %s: %v", relative, err)
		}
		checkContent(relative, content)
	}
	canonicalArchitectureFail(t, "Step19 Viewer/OTel graph owners must not retain legacy identity fields or synthetic trace IDs", violations)
}


func TestStep20MigrationSourceIsRemovedAfterCutover(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	leftovers := []string{
		filepath.Join(repoRoot, "cmd", "rencrow-step20-backlog-item-migrate"),
		filepath.Join(repoRoot, "internal", "application", "step20backlogitemmigration"),
	}
	var found []string
	for _, path := range leftovers {
		if _, err := os.Stat(path); err == nil {
			found = append(found, path)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", path, err)
		}
	}
	if len(found) != 0 {
		t.Fatalf("Step20 migration source must be removed after cutover: %v", found)
	}
}

func TestStep20IdentityCleanupLegacyTokensAreBanned(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	finalLegacyTokens := []string{
		"JobID", "job_id",
		"DiscussionID", "discussion_id",
		"ParentEventID", "parent_event_id",
		"ParentRunID", "parent_run_id",
		"TraceRunID", "trace_run_id",
		"GenerationID", "generation_id",
		"SubagentID", "subagent_id",
		"DecisionID", "decision_id",
		"AssignmentID", "assignment_id",
		"ApplyID", "apply_id",
		"SubmitID", "submit_id",
		"ReportID", "report_id",
		"DraftID", "draft_id",
		"ContextPackID", "context_pack_id",
		"QueueID", "queue_id",
		"ChatID", "chat_id",
		"LegacyItemID", `json:"item_id"`,
	}
	step19Tokens := []string{
		"JobID", "job_id",
		"ChatID", "chat_id",
		`"trace-%d"`, `"trace-"`,
	}
	owners := []struct {
		relative string
		tokens   []string
	}{
		{"internal/application/otelexport", step19Tokens},
		{"internal/adapter/viewer/assets/js/viewer.js", step19Tokens},
		{"internal/adapter/viewer/assets/js/tabs/timeline.js", step19Tokens},
		{"internal/adapter/viewer/assets/js/tabs/ops.js", step19Tokens},
		{"internal/adapter/viewer/assets/js/tabs/idlechat.js", step19Tokens},
		{"internal/domain/backlog", finalLegacyTokens},
		{"internal/application/backlog", finalLegacyTokens},
		{"internal/infrastructure/backlog", finalLegacyTokens},
		{"internal/domain/workstream", finalLegacyTokens},
		{"modules/core/identity.go", finalLegacyTokens},
	}

	var violations []string
	checkFile := func(relative string, content []byte, tokens []string) {
		if strings.Contains(relative, "step20backlogitemmigration") ||
			strings.Contains(relative, "rencrow-step20-backlog-item-migrate") {
			return
		}
		inBlockComment := false
		for lineNumber, line := range strings.Split(string(content), "\n") {
			code := canonicalStep20CodeOnly(line, &inBlockComment)
			if strings.TrimSpace(code) == "" {
				continue
			}
			for _, legacy := range tokens {
				if !canonicalSourceContainsToken(code, legacy) {
					continue
				}
				violations = append(violations, fmt.Sprintf("%s:%d:legacy-step20:%s", relative, lineNumber+1, legacy))
			}
		}
	}

	for _, owner := range owners {
		path := filepath.Join(repoRoot, filepath.FromSlash(owner.relative))
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("stat Step20 owner %s: %v", owner.relative, err)
		}
		if !info.IsDir() {
			checkFile(owner.relative, mustReadFile(t, path), owner.tokens)
			continue
		}
		err = filepath.WalkDir(path, func(candidate string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if entry.Name() == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(candidate, "_test.go") ||
				(!strings.HasSuffix(candidate, ".go") && !strings.HasSuffix(candidate, ".js")) {
				return nil
			}
			relative, err := filepath.Rel(repoRoot, candidate)
			if err != nil {
				return err
			}
			checkFile(filepath.ToSlash(relative), mustReadFile(t, candidate), owner.tokens)
			return nil
		})
		if err != nil {
			t.Fatalf("scan Step20 owner %s: %v", owner.relative, err)
		}
	}
	canonicalArchitectureFail(t, "Step20 bounded owners must not retain fair-game legacy identity tokens", violations)
}

func canonicalStep20CodeOnly(line string, inBlockComment *bool) string {
	var code strings.Builder
	for index := 0; index < len(line); {
		if *inBlockComment {
			end := strings.Index(line[index:], "*/")
			if end < 0 {
				return code.String()
			}
			index += end + 2
			*inBlockComment = false
			continue
		}
		block := strings.Index(line[index:], "/*")
		single := strings.Index(line[index:], "//")
		if single >= 0 && (block < 0 || single < block) {
			code.WriteString(line[index : index+single])
			return code.String()
		}
		if block < 0 {
			code.WriteString(line[index:])
			return code.String()
		}
		code.WriteString(line[index : index+block])
		index += block + 2
		*inBlockComment = true
	}
	return code.String()
}

func TestStep15MigrationSourceIsRemovedAfterCutover(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	leftovers := []string{
		filepath.Join(repoRoot, "cmd", "rencrow-step15-scheduler-migrate"),
		filepath.Join(repoRoot, "internal", "infrastructure", "persistence", "step15schedulermigration"),
	}
	var found []string
	for _, path := range leftovers {
		if _, err := os.Stat(path); err == nil {
			found = append(found, path)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", path, err)
		}
	}
	if len(found) != 0 {
		t.Fatalf("Step15 migration source must be removed after cutover: %v", found)
	}
}

func lineAt(repoRoot, relative string, lineNumber int) string {
	content, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(relative)))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(content), "\n")
	if lineNumber <= 0 || lineNumber > len(lines) {
		return ""
	}
	return lines[lineNumber-1]
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return content
}

func canonicalArchitectureStructBlock(repoRoot, relative, structName string) string {
	path := filepath.Join(repoRoot, filepath.FromSlash(relative))
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(content), "\n")
	start := -1
	for i, line := range lines {
		if strings.Contains(line, "type "+structName+" struct") {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	depth := 0
	var block strings.Builder
	for i := start; i < len(lines); i++ {
		line := lines[i]
		block.WriteString(line)
		block.WriteByte('\n')
		depth += strings.Count(line, "{")
		depth -= strings.Count(line, "}")
		if i > start && depth <= 0 {
			break
		}
	}
	return block.String()
}

func TestCanonicalOrchestratorTaskScopeHasNoLegacyJobContract(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	directories := []string{
		"cmd/rencrow-agent",
		"cmd/test-worker",
		"internal/adapter/chrome",
		"internal/adapter/entry",
		"internal/adapter/modulebridge",
		"internal/application/execution",
		"internal/application/orchestrator",
		"internal/application/resilience",
		"internal/application/skillgovernance",
		"internal/application/verification",
		"internal/application/voiceinput",
		"internal/domain/execution",
		"internal/domain/skillgovernance",
		"internal/domain/transport",
		"internal/domain/verification",
		"internal/infrastructure/persistence/execution",
		"internal/infrastructure/persistence/skillgovernance",
		"internal/infrastructure/persistence/verification",
		"internal/infrastructure/transport",
		"modules/chat",
		"modules/worker",
	}
	files := []string{
		"cmd/rencrow/atlas_evidence_verifier.go",
		"cmd/rencrow/autonomous_entry.go",
		"cmd/rencrow/cli_evidence.go",
		"cmd/rencrow/resilience_commands.go",
		"cmd/rencrow/runtime_agent_ops.go",
		"cmd/rencrow/runtime_development_events.go",
		"cmd/rencrow/runtime_distributed_mode.go",
		"cmd/rencrow/runtime_event_relay.go",
		"cmd/rencrow/runtime_local_agents.go",
		"cmd/rencrow/runtime_orchestrator.go",
		"cmd/rencrow/runtime_repair.go",
		"cmd/rencrow/runtime_viewer_bridges.go",
		"cmd/rencrow/runtime_viewer_handlers.go",
		"cmd/rencrow/tts_client_bridge.go",
		"cmd/rencrow/voice_chat_runtime_bridge.go",
		"cmd/rencrow/voice_chat_runtime_input_audio.go",
		"cmd/rencrow-core-verify/actor_checks.go",
		"cmd/rencrow-core-verify/startup_evidence.go",
		"internal/adapter/viewer/canonical_event_log.go",
		"internal/adapter/viewer/evidence_handler.go",
		"internal/adapter/viewer/handler_send.go",
		"internal/adapter/viewer/handler_sse.go",
		"internal/adapter/viewer/monitor.go",
		"internal/adapter/viewer/monitor_events.go",
		"internal/adapter/viewer/monitor_handler.go",
		"internal/adapter/viewer/monitor_helpers.go",
		"internal/adapter/viewer/monitor_queries.go",
		"internal/adapter/viewer/monitor_reducers.go",
		"internal/adapter/viewer/monitor_types.go",
		"internal/adapter/viewer/repair_handler.go",
		"internal/adapter/viewer/verification_handler.go",
		"internal/application/heartbeat/service.go",
		"internal/application/service/worker_execution_dispatch.go",
		"internal/application/service/worker_execution_git.go",
		"internal/application/service/worker_execution_lifecycle.go",
		"internal/application/service/worker_execution_modes.go",
		"internal/application/service/worker_execution_service.go",
		"internal/application/service/worker_execution_summary.go",
		"internal/domain/agent/shiro.go",
		"internal/domain/llm/execution_observation.go",
		"internal/infrastructure/llm/middleware/promptreceipt.go",
		"internal/infrastructure/llm/providers/rencrowllm/provider.go",
		"internal/infrastructure/logging/session_log_writer.go",
	}
	legacyTokens := []string{
		"Job", "JobID", "job", "jobID", "job_id", "ParentJobID", "parent_job_id",
		"RepairJob", "RepairJobID", "repair_job_id",
	}
	var violations []string
	checkFile := func(path string) error {
		relative, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if strings.HasSuffix(relative, "_test.go") || !strings.HasSuffix(relative, ".go") {
			return nil
		}
		for _, token := range legacyTokens {
			if canonicalSourceContainsToken(relative, token) {
				violations = append(violations, relative+":path:"+token)
			}
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for lineIndex, line := range strings.Split(string(content), "\n") {
			for _, token := range legacyTokens {
				if canonicalSourceContainsToken(line, token) {
					violations = append(violations, fmt.Sprintf("%s:%d:%s", relative, lineIndex+1, token))
				}
			}
		}
		return nil
	}
	for _, relative := range directories {
		root := filepath.Join(repoRoot, filepath.FromSlash(relative))
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if entry.Name() == "Tmp" {
					return filepath.SkipDir
				}
				return nil
			}
			return checkFile(path)
		}); err != nil {
			t.Fatalf("scan Step09 directory %s: %v", relative, err)
		}
	}
	for _, relative := range files {
		if err := checkFile(filepath.Join(repoRoot, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("scan Step09 file %s: %v", relative, err)
		}
	}
	clientRelative := "pkg/rencrowclient/client.go"
	clientViolations, err := canonicalNamedDeclarationLegacyViolations(
		filepath.Join(repoRoot, filepath.FromSlash(clientRelative)),
		clientRelative,
		map[string]struct{}{
			"SkillGovernanceCoderTranscript": {},
			"validateSkillGovernanceStatus":  {},
		},
		legacyTokens,
	)
	if err != nil {
		t.Fatalf("scan Step09 client declarations: %v", err)
	}
	violations = append(violations, clientViolations...)

	for _, relative := range []string{
		"internal/application/orchestrator/event.go",
		"internal/adapter/viewer/canonical_event_log.go",
		"modules/core/event.go",
	} {
		content, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatalf("read Step09 Event contract %s: %v", relative, err)
		}
		for lineIndex, line := range strings.Split(string(content), "\n") {
			for _, token := range []string{"Seq", "seq"} {
				if canonicalSourceContainsToken(line, token) {
					violations = append(violations, fmt.Sprintf("%s:%d:legacy Event %s", relative, lineIndex+1, token))
				}
			}
		}
	}
	canonicalArchitectureFail(t, "Step09 Orchestrator Task scope must not retain legacy Job or Event Seq vocabulary", violations)
}

func canonicalNamedDeclarationLegacyViolations(path, relative string, names map[string]struct{}, tokens []string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, path, content, 0)
	if err != nil {
		return nil, err
	}
	var violations []string
	scan := func(name string, node ast.Node) {
		if _, selected := names[name]; !selected {
			return
		}
		start := fileSet.Position(node.Pos()).Offset
		end := fileSet.Position(node.End()).Offset
		if start < 0 || end < start || end > len(content) {
			violations = append(violations, relative+":"+name+":invalid source range")
			return
		}
		declaration := string(content[start:end])
		for _, legacy := range tokens {
			if canonicalSourceContainsToken(declaration, legacy) {
				violations = append(violations, relative+":"+name+":"+legacy)
			}
		}
		delete(names, name)
	}
	for _, declaration := range parsed.Decls {
		switch value := declaration.(type) {
		case *ast.FuncDecl:
			scan(value.Name.Name, value)
		case *ast.GenDecl:
			for _, specification := range value.Specs {
				if typeSpec, ok := specification.(*ast.TypeSpec); ok {
					scan(typeSpec.Name.Name, typeSpec)
				}
			}
		}
	}
	for name := range names {
		violations = append(violations, relative+":"+name+":missing declaration")
	}
	return violations, nil
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
	canonicalConversationImportPath = "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

func TestCanonicalTaskAggregateArchitecture(t *testing.T) {
	repoRoot := canonicalArchitectureRepoRoot(t)
	relative := filepath.ToSlash(filepath.Join("internal", "domain", "task", "task.go"))
	path := filepath.Join(repoRoot, filepath.FromSlash(relative))
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse canonical Task aggregate: %v", err)
	}
	var violations []string
	jobIDFile := filepath.Join(repoRoot, "internal", "domain", "task", "jobid.go")
	if _, err := os.Stat(jobIDFile); err == nil || !os.IsNotExist(err) {
		violations = append(violations, "internal/domain/task/jobid.go:retired JobID value object remains")
	}
	taskStruct, found := canonicalArchitectureNamedStruct(parsed, "Task")
	if !found {
		violations = append(violations, relative+":canonical durable Task struct is missing")
	} else {
		coreAliases, dotImport := canonicalImportAliases(parsed, "github.com/Nyukimin/RenCrow_CORE/modules/core")
		if dotImport {
			violations = append(violations, relative+":dot import of modules/core is forbidden")
		}
		foundTaskID := false
		for _, field := range taskStruct.Fields.List {
			for _, name := range field.Names {
				normalized := canonicalArchitectureFieldName(name.Name)
				switch normalized {
				case "jobid", "usermessage", "channel", "chatid", "attachment", "attachments":
					violations = append(violations, canonicalArchitectureViolation(fileSet, name.Pos(), relative, "retired input or Job field "+name.Name))
				}
				if name.Name == "TaskID" {
					foundTaskID = canonicalArchitectureConversationType(field.Type, coreAliases, "TaskID", false)
				}
			}
		}
		if !foundTaskID {
			violations = append(violations, relative+":TaskID must use modules/core.TaskID")
		}
	}
	canonicalArchitectureFail(t, "Task must be the durable canonical aggregate and not the retired TurnInput value", violations)
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
