package core

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
		"internal/domain/conversation/conversation_turn.go:ConversationTurnMessageIDs":                         {},
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
