package modules_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const moduleImportPrefix = "github.com/Nyukimin/RenCrow_CORE/modules/"

var allowedModuleImports = map[string]map[string]bool{
	"browseractor": {},
	"core":         {},
	"llm":          {"core": true},
	"tts":          {"core": true},
	"stt":          {"core": true},
	"voicechat":    {},
	"webgather":    {},
	"worker":       {"core": true, "llm": true},
	"chat":         {"core": true, "llm": true, "tts": true, "stt": true, "worker": true},
}

func TestModuleDependencyRules(t *testing.T) {
	for _, module := range currentModulePackageNames(t) {
		allowed := allowedModuleImports[module]
		dir := filepath.Join(module)
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			imports, err := parseModuleImports(path)
			if err != nil {
				return err
			}
			for _, imported := range imports {
				if imported == module {
					continue
				}
				if !allowed[imported] {
					t.Fatalf("%s imports forbidden module %q", path, imported)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
}

func TestModuleDependencyRulesCoverCurrentModuleDirectories(t *testing.T) {
	for _, module := range currentModulePackageNames(t) {
		if _, ok := allowedModuleImports[module]; !ok {
			t.Fatalf("module %s is missing from allowedModuleImports", module)
		}
	}
}

func parseModuleImports(path string) ([]string, error) {
	importPaths, err := parseImports(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, importPath := range importPaths {
		if !strings.HasPrefix(importPath, moduleImportPrefix) {
			continue
		}
		name := strings.TrimPrefix(importPath, moduleImportPrefix)
		name = strings.Split(name, "/")[0]
		out = append(out, name)
	}
	return out, nil
}

func parseImports(path string) ([]string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, spec := range file.Imports {
		importPath := strings.Trim(spec.Path.Value, `"`)
		out = append(out, importPath)
	}
	return out, nil
}

func TestModulePackagesAreDocumented(t *testing.T) {
	for _, module := range currentModulePackageNames(t) {
		if _, err := os.Stat(filepath.Join(module, "README.md")); err != nil {
			t.Fatalf("module %s README missing: %v", module, err)
		}
	}
}

func currentModulePackageNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read modules dir: %v", err)
	}
	var modules []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, ok := allowedModuleImports[entry.Name()]; !ok {
			modules = append(modules, entry.Name())
			continue
		}
		modules = append(modules, entry.Name())
	}
	return modules
}

func TestNoImplementationUnderGitWorktrees(t *testing.T) {
	gitWorktrees := filepath.Join("..", ".git", "worktrees")
	if _, err := os.Stat(gitWorktrees); err != nil {
		return
	}
	err := filepath.WalkDir(gitWorktrees, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".md") {
			t.Fatalf("source-like file must not be placed under %s: %s", gitWorktrees, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk git worktrees: %v", err)
	}
}

func TestModuleContractsDoNotImportImplementation(t *testing.T) {
	for module := range allowedModuleImports {
		dir := filepath.Join(module)
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			imports, err := parseImports(path)
			if err != nil {
				return err
			}
			for _, importPath := range imports {
				if strings.Contains(importPath, "/internal/") || strings.Contains(importPath, "/cmd/") {
					t.Fatalf("%s imports implementation package %q", path, importPath)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
}

func TestModuleBridgeDoesNotImportCompositionRoot(t *testing.T) {
	dir := filepath.Join("..", "internal", "adapter", "modulebridge")
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		imports, err := parseImports(path)
		if err != nil {
			return err
		}
		for _, importPath := range imports {
			if strings.Contains(importPath, "/cmd/rencrow") {
				t.Fatalf("%s imports composition root %q", path, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
}

func TestModuleBridgeDoesNotOwnHealthReportPolicy(t *testing.T) {
	dir := filepath.Join("..", "internal", "adapter", "modulebridge")
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(content), "core.HealthReport{") {
			t.Fatalf("%s owns module health report policy; build health reports in modules/*", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
}

func TestCompositionRuntimeUsesModuleSTTViewerInputHealthPolicy(t *testing.T) {
	path := filepath.Join("..", "cmd", "rencrow", "module_stt_viewer_input.go")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(content)
	if !strings.Contains(text, "BuildViewerInputHealthReport") {
		t.Fatalf("%s must delegate STT viewer-input health policy to modules/stt", path)
	}
	for _, forbidden := range []string{
		`Module: "stt.viewer_input"`,
		`"viewer stt input configured"`,
		`"viewer stt input has no provider or websocket"`,
		`"provider_configured"`,
		`"gateway_configured"`,
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("%s owns STT viewer-input health policy containing %q; keep it in modules/stt", path, forbidden)
		}
	}
}

func TestCompositionRuntimeUsesModuleTTSPlaybackHealthPolicy(t *testing.T) {
	path := filepath.Join("..", "cmd", "rencrow", "module_tts_playback_state.go")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(content)
	if !strings.Contains(text, "BuildPlaybackStateHealthReport") {
		t.Fatalf("%s must delegate TTS playback health policy to modules/tts", path)
	}
	for _, forbidden := range []string{
		`Module: "tts.playback"`,
		`"playback state clear"`,
		`"playback pending state active"`,
		`"pending_session_count"`,
		`"pending_response_count"`,
		`"public_route_count"`,
		"core.HealthLive",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("%s owns TTS playback health policy containing %q; keep it in modules/tts", path, forbidden)
		}
	}
}

func TestCompositionRuntimeUsesModuleWorkerUnavailableExecutorPolicy(t *testing.T) {
	path := filepath.Join("..", "cmd", "rencrow", "module_worker_diagnostics.go")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(content)
	if !strings.Contains(text, "UnavailableExecutor") {
		t.Fatalf("%s must delegate unavailable worker executor policy to modules/worker", path)
	}
	for _, forbidden := range []string{
		`modulecore.HealthReport{`,
		`moduleworker.Result{`,
		`"worker executor unavailable"`,
		"modulecore.HealthDown",
		"moduleworker.StatusFailed",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("%s owns unavailable worker executor policy containing %q; keep it in modules/worker", path, forbidden)
		}
	}
}

func TestCompositionRuntimeUsesModuleDiagnosticsUnavailableMessages(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		want      string
		forbidden string
	}{
		{
			name:      "tts diagnostics",
			path:      filepath.Join("..", "cmd", "rencrow", "module_tts_diagnostics.go"),
			want:      "moduletts.DiagnosticsProviderUnavailableMessage",
			forbidden: `"tts provider unavailable"`,
		},
		{
			name:      "stt diagnostics",
			path:      filepath.Join("..", "cmd", "rencrow", "module_stt_diagnostics.go"),
			want:      "modulestt.DiagnosticsProviderUnavailableMessage",
			forbidden: `"stt provider unavailable"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := os.ReadFile(tt.path)
			if err != nil {
				t.Fatalf("read %s: %v", tt.path, err)
			}
			text := string(content)
			if !strings.Contains(text, tt.want) {
				t.Fatalf("%s must delegate diagnostics unavailable message to module via %q", tt.path, tt.want)
			}
			if strings.Contains(text, tt.forbidden) {
				t.Fatalf("%s owns diagnostics unavailable message containing %q; keep it in modules/*", tt.path, tt.forbidden)
			}
		})
	}
}

func TestCompositionRuntimeUsesModuleChatRouteUnavailableMessage(t *testing.T) {
	path := filepath.Join("..", "cmd", "rencrow", "module_chat_route.go")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(content)
	if !strings.Contains(text, "modulechat.RouteServiceUnavailableMessage") {
		t.Fatalf("%s must delegate chat route unavailable message to modules/chat", path)
	}
	if strings.Contains(text, `"chat module service unavailable"`) {
		t.Fatalf("%s owns chat route unavailable message; keep it in modules/chat", path)
	}
}

func TestCompositionRuntimeUsesModuleStateEndpointMessages(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		want       []string
		forbidden  []string
		moduleName string
	}{
		{
			name: "tts playback state",
			path: filepath.Join("..", "cmd", "rencrow", "module_tts_playback_state.go"),
			want: []string{
				"moduletts.PlaybackStateObserverUnavailableMessage",
				"moduletts.PlaybackStateSnapshotFailedPrefix",
			},
			forbidden: []string{
				`"tts playback observer unavailable"`,
				`"tts playback snapshot failed: "`,
			},
			moduleName: "modules/tts",
		},
		{
			name: "stt viewer input",
			path: filepath.Join("..", "cmd", "rencrow", "module_stt_viewer_input.go"),
			want: []string{
				"modulestt.ViewerInputObserverUnavailableMessage",
				"modulestt.ViewerInputSnapshotFailedPrefix",
			},
			forbidden: []string{
				`"stt viewer input observer unavailable"`,
				`"stt viewer input snapshot failed: "`,
			},
			moduleName: "modules/stt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := os.ReadFile(tt.path)
			if err != nil {
				t.Fatalf("read %s: %v", tt.path, err)
			}
			text := string(content)
			for _, want := range tt.want {
				if !strings.Contains(text, want) {
					t.Fatalf("%s must delegate state endpoint message to %s via %q", tt.path, tt.moduleName, want)
				}
			}
			for _, forbidden := range tt.forbidden {
				if strings.Contains(text, forbidden) {
					t.Fatalf("%s owns state endpoint message containing %q; keep it in %s", tt.path, forbidden, tt.moduleName)
				}
			}
		})
	}
}

func TestModuleBridgeDoesNotOwnWorkerResultOrTTSEmotionPolicy(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		forbidden []string
	}{
		{
			name: "worker result construction",
			path: filepath.Join("..", "internal", "adapter", "modulebridge", "worker.go"),
			forbidden: []string{
				"moduleworker.Result{",
				"StatusDenied",
				"StatusFailed",
				"MetadataFromPatchExecution",
			},
		},
		{
			name: "tts emotion provider reason",
			path: filepath.Join("..", "internal", "adapter", "modulebridge", "tts.go"),
			forbidden: []string{
				`"voice_profile"`,
				`"prosody"`,
				`"metadata"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := os.ReadFile(tt.path)
			if err != nil {
				t.Fatalf("read %s: %v", tt.path, err)
			}
			text := string(content)
			for _, forbidden := range tt.forbidden {
				if strings.Contains(text, forbidden) {
					t.Fatalf("%s owns extracted adapter policy containing %q; keep it in modules/*", tt.path, forbidden)
				}
			}
		})
	}
}

func TestModuleBridgeUsesModuleRequestCopySemantics(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "llm generate request",
			path: filepath.Join("..", "internal", "adapter", "modulebridge", "llm.go"),
			want: "CloneGenerateRequest",
		},
		{
			name: "stt transcription request",
			path: filepath.Join("..", "internal", "adapter", "modulebridge", "stt.go"),
			want: "CloneTranscriptionRequest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := os.ReadFile(tt.path)
			if err != nil {
				t.Fatalf("read %s: %v", tt.path, err)
			}
			if !strings.Contains(string(content), tt.want) {
				t.Fatalf("%s does not use module-owned request copy semantics %q", tt.path, tt.want)
			}
		})
	}
}

func TestCompositionRuntimeUsesModuleSTTTimeoutPolicy(t *testing.T) {
	path := filepath.Join("..", "cmd", "rencrow", "stt_runtime_http.go")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(content)
	if !strings.Contains(text, "modulestt.IsTimeoutError") {
		t.Fatalf("%s must delegate STT timeout classification to modules/stt", path)
	}
	for _, forbidden := range []string{
		"errors.As",
		"net.Error",
		"client.timeout exceeded",
		"context deadline exceeded",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("%s owns STT timeout classification containing %q; keep it in modules/stt", path, forbidden)
		}
	}
}

func TestCompositionRuntimeUsesModuleSTTSessionRules(t *testing.T) {
	path := filepath.Join("..", "cmd", "rencrow", "stt_runtime_websocket.go")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(content)
	for _, want := range []string{
		"FinalTextAfterDraftTimeout",
		"FinalTextForPending",
		"FinalTextAfterSilence",
		"FinalTextOnProviderError",
		"NormalizeTranscriptText",
		"ApplyTimeoutFailure",
		"ApplyInferenceSuccess",
		"InferenceInCooldown",
		"MarkVoiceObserved",
		"MarkSpeechStarted",
		"ApplyDraftTranscript",
		"ResetDraftAfterFinal",
		"BuildFinalEvent",
		"BuildDraftEvent",
		"BuildSpeechStartEvent",
		"BuildTimeoutStatusEvent",
		"BuildSessionInfoEvent",
		"BuildReadyEvent",
		"BuildErrorEvent",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("%s must delegate STT session rule %q to modules/stt", path, want)
		}
	}
	for _, forbidden := range []string{
		"strings.TrimSpace(lastDraft)",
		"time.Since(lastDraftAt)",
		"time.Since(lastVoiceAt)",
		`lastDraft = ""`,
		"lastDraftAt = time.Time{}",
		"lastVoiceAt = time.Time{}",
		"speechStarted = false",
		"lastDraft = normalized",
		"func sttDraftState",
		"strings.TrimSpace(result.Text)",
		"timeoutStreak",
		"successStreak",
		"inferCooldownUntil",
		"lastTimeoutNotice",
		"800 * time.Millisecond",
		`"type": "final"`,
		`"type": "draft"`,
		`"type": "speech_start"`,
		`"type": "status"`,
		`"type": "error"`,
		`"stt provider timeout (retrying)"`,
		`"sample_rate": 16000`,
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("%s owns STT draft/final session rule containing %q; keep it in modules/stt", path, forbidden)
		}
	}
}

func TestSTTInfrastructureUsesModuleHTTPResultPolicy(t *testing.T) {
	path := filepath.Join("..", "internal", "infrastructure", "stt", "http.go")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(content)
	for _, want := range []string{
		"BuildChatInputEnvelope",
		"NormalizeHandlerResult",
		"StatusForHandlerError",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("%s must delegate STT HTTP result policy to modules/stt via %q", path, want)
		}
	}
	for _, forbidden := range []string{
		`"user_input"`,
		`"local_stt"`,
		`"voice"`,
		`"ja"`,
		`"音声が検出されませんでした。"`,
		"func statusForError",
		"func emptyToNil",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("%s owns STT HTTP result policy containing %q; keep it in modules/stt", path, forbidden)
		}
	}
}

func TestSTTInfrastructureUsesModuleBusyPolicy(t *testing.T) {
	path := filepath.Join("..", "internal", "infrastructure", "stt", "busy_provider.go")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(content)
	if !strings.Contains(text, "BuildBusyPolicyPlan") {
		t.Fatalf("%s must delegate STT busy policy planning to modules/stt", path)
	}
	for _, forbidden := range []string{
		`strings.ToLower`,
		`strings.TrimSpace(policy)`,
		`normalized == ""`,
		`normalized == BusyPolicyQueueLatest`,
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("%s owns STT busy policy normalization containing %q; keep it in modules/stt", path, forbidden)
		}
	}
}

func TestTTSInfrastructureUsesModuleStringSelectionHelpers(t *testing.T) {
	tests := []struct {
		path      string
		want      string
		forbidden []string
	}{
		{
			path: filepath.Join("..", "internal", "infrastructure", "tts", "sbv2_provider.go"),
			want: "moduletts.ChooseNonEmpty",
			forbidden: []string{
				"func chooseNonEmpty",
				"func equalFoldTrim",
			},
		},
		{
			path: filepath.Join("..", "internal", "infrastructure", "tts", "irodori_provider.go"),
			want: "moduletts.ChooseNonEmpty",
			forbidden: []string{
				"func chooseNonEmpty",
			},
		},
		{
			path: filepath.Join("..", "internal", "infrastructure", "tts", "rencrow_tts_bridge.go"),
			want: "moduletts.ChooseNonEmpty",
			forbidden: []string{
				"func chooseNonEmpty",
			},
		},
	}

	for _, tt := range tests {
		content, err := os.ReadFile(tt.path)
		if err != nil {
			t.Fatalf("read %s: %v", tt.path, err)
		}
		text := string(content)
		if !strings.Contains(text, tt.want) {
			t.Fatalf("%s must delegate string selection helper via %q", tt.path, tt.want)
		}
		for _, forbidden := range tt.forbidden {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s reimplements string selection helper containing %q; keep it in modules/tts", tt.path, forbidden)
			}
		}
	}
}

func TestCompositionRuntimeUsesModuleTTSProviderPlanEnumeration(t *testing.T) {
	path := filepath.Join("..", "cmd", "rencrow", "tts_runtime_factory.go")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(content)
	for _, want := range []string{
		"FirstRuntimeProviderPlan",
		"BuildRuntimeProviderPlans",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("%s must delegate TTS provider plan enumeration to modules/tts via %q", path, want)
		}
	}
	for _, forbidden := range []string{
		"func ttsProviderPriority",
		"RuntimeProviderPriority(",
		"for _, name := range",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("%s owns TTS provider priority enumeration containing %q; keep it in modules/tts", path, forbidden)
		}
	}
}

func TestCompositionRuntimeUsesModuleTTSSelectionLogPolicy(t *testing.T) {
	path := filepath.Join("..", "cmd", "rencrow", "tts_runtime_options.go")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(content)
	if !strings.Contains(text, "RuntimeProviderSelectionLogMessage") {
		t.Fatalf("%s must delegate TTS provider selection log policy to modules/tts", path)
	}
	for _, forbidden := range []string{
		`"irodori"`,
		"TTS Irodori bridge enabled",
		"switch sel.Name",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("%s owns TTS provider selection log policy containing %q; keep it in modules/tts", path, forbidden)
		}
	}
}

func TestCompositionRuntimeUsesModuleChatForecastProviderPlans(t *testing.T) {
	path := filepath.Join("..", "cmd", "rencrow", "runtime_idlechat.go")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(content)
	for _, want := range []string{
		"BuildForecastProviderPlans",
		"ForecastCoderLabelIndex",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("%s must delegate IdleChat forecast provider policy to modules/chat via %q", path, want)
		}
	}
	for _, forbidden := range []string{
		"ForecastCoderProviderAllowed",
		"external provider not explicitly enabled",
		"for _, candidate := range",
		"strings.TrimSpace(label)",
		`case "Coder1"`,
		`case "Coder2"`,
		`case "Coder3"`,
		`case "Coder4"`,
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("%s owns IdleChat forecast provider policy containing %q; keep it in modules/chat", path, forbidden)
		}
	}
}

func TestCompositionRuntimeUsesModuleLLMNumCtxPlans(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", "cmd", "rencrow", "llm_runtime_factory.go"),
		filepath.Join("..", "cmd", "rencrow", "llm_local_alias.go"),
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(content)
		if !strings.Contains(text, ".NumCtx") {
			t.Fatalf("%s must apply module-owned LLM num_ctx plan values", path)
		}
		for _, forbidden := range []string{"32768", "16384"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s owns LLM num_ctx constant %q; keep it in modules/llm", path, forbidden)
			}
		}
	}
}

func TestCompositionRuntimeUsesModuleLLMHealthCheckPolicy(t *testing.T) {
	path := filepath.Join("..", "cmd", "rencrow", "module_llm_health.go")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(content)
	for _, want := range []string{
		"ShouldUseLocalHealthChecks",
		"NormalizeRoleName",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("%s must delegate LLM health-check policy to modules/llm via %q", path, want)
		}
	}
	for _, forbidden := range []string{
		`cfg.LocalLLM.Provider != "local_openai"`,
		"strings.ToLower(strings.TrimSpace(role))",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("%s owns LLM health-check policy containing %q; keep it in modules/llm", path, forbidden)
		}
	}
}

func TestCompositionRuntimeUsesModuleWorkerExternalCoderPolicy(t *testing.T) {
	paths := []string{
		filepath.Join("..", "cmd", "rencrow", "runtime_orchestrator.go"),
		filepath.Join("..", "cmd", "rencrow", "runtime_coders.go"),
	}
	combined := ""
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		combined += string(content)
	}
	if !strings.Contains(combined, "BuildExternalCoderPolicy") {
		t.Fatalf("cmd/rencrow runtime must delegate external Coder policy to modules/worker")
	}
	for _, forbidden := range []string{
		"SetExternalCoderPolicy(map[string]bool",
		`"coder1": coderProviderIsExternal`,
		`"coder2": coderProviderIsExternal`,
		`"coder3": coderProviderIsExternal`,
		`"coder4": coderProviderIsExternal`,
	} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("cmd/rencrow owns external Coder policy containing %q; keep it in modules/worker", forbidden)
		}
	}
}

func TestCompositionRuntimeUsesModuleWorkerCoderSetupPlans(t *testing.T) {
	path := filepath.Join("..", "cmd", "rencrow", "runtime_coders.go")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(content)
	for _, want := range []string{
		"BuildCoderSetupPlans",
		"CoderSlotIndex",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("%s must delegate Coder setup planning to modules/worker via %q", path, want)
		}
	}
	for _, forbidden := range []string{
		"coderConfigs := []struct",
		"maxTurns <= 0",
		"maxTurns = 3",
		"cc.config.LightMemory.Enabled",
		`strings.ToLower(strings.TrimSpace(name))`,
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("%s owns Coder setup policy containing %q; keep it in modules/worker", path, forbidden)
		}
	}
}

func TestCompositionRuntimeUsesModuleTTSBridgeEventPayloadPolicy(t *testing.T) {
	path := filepath.Join("..", "cmd", "rencrow", "tts_client_bridge.go")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(content)
	for _, want := range []string{
		"BuildAudioChunkEventPayload",
		"BuildSessionCompletedEventPayload",
		"PlaybackEventRouteForSession",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("%s must delegate TTS bridge event payload policy to modules/tts via %q", path, want)
		}
	}
	for _, forbidden := range []string{
		`"speech_text"`,
		`"display_text"`,
		`"audio_path"`,
		`"audio_url"`,
		`"track"`,
		`"viewer-user"`,
		`channel := "viewer"`,
		`chatID := "viewer-user"`,
		`channel = "idlechat"`,
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("%s owns TTS bridge event payload/route policy containing %q; keep it in modules/tts", path, forbidden)
		}
	}
}

func TestAudioInfrastructureDoesNotImportOtherRuntimeModules(t *testing.T) {
	tests := []struct {
		name      string
		dir       string
		forbidden []string
	}{
		{
			name: "tts",
			dir:  filepath.Join("..", "internal", "infrastructure", "tts"),
			forbidden: []string{
				"/modules/chat",
				"/modules/llm",
				"/modules/stt",
				"/modules/worker",
				"/internal/infrastructure/stt",
			},
		},
		{
			name: "stt",
			dir:  filepath.Join("..", "internal", "infrastructure", "stt"),
			forbidden: []string{
				"/modules/chat",
				"/modules/llm",
				"/modules/tts",
				"/modules/worker",
				"/internal/infrastructure/tts",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := filepath.WalkDir(tt.dir, func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
					return nil
				}
				imports, err := parseImports(path)
				if err != nil {
					return err
				}
				for _, importPath := range imports {
					for _, forbidden := range tt.forbidden {
						if strings.Contains(importPath, forbidden) {
							t.Fatalf("%s imports forbidden package %q", path, importPath)
						}
					}
				}
				return nil
			})
			if err != nil {
				t.Fatalf("walk %s: %v", tt.dir, err)
			}
		})
	}
}

func TestWorkerServiceDoesNotImportAudioOrViewerImplementations(t *testing.T) {
	dir := filepath.Join("..", "internal", "application", "service")
	forbidden := []string{
		"/internal/infrastructure/tts",
		"/internal/infrastructure/stt",
		"/internal/adapter/viewer",
		"/modules/tts",
		"/modules/stt",
	}

	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		imports, err := parseImports(path)
		if err != nil {
			return err
		}
		for _, importPath := range imports {
			for _, forbiddenImport := range forbidden {
				if strings.Contains(importPath, forbiddenImport) {
					t.Fatalf("%s imports forbidden package %q", path, importPath)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
}

func TestTTSSpeechPolicyCallersUseModuleContract(t *testing.T) {
	repoRoot := ".."
	compatImport := "github.com/Nyukimin/RenCrow_CORE/internal/application/tts"

	err := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "tmp", "vendor":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.Contains(path, filepath.Join("internal", "application", "tts")) {
			return nil
		}
		imports, err := parseImports(path)
		if err != nil {
			return err
		}
		for _, importPath := range imports {
			if importPath == compatImport {
				t.Fatalf("%s imports TTS compatibility package; use modules/tts directly", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", repoRoot, err)
	}
}

func TestModuleHTTPHandlersDoNotOwnResponseContracts(t *testing.T) {
	dir := filepath.Join("..", "cmd", "rencrow")
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "module_") || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(content)
		for _, forbidden := range []string{
			"json.NewEncoder",
			`Header().Set("Content-Type"`,
		} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s owns JSON response wiring containing %q; use modules/core HTTP helpers", path, forbidden)
			}
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				typeName := typeSpec.Name.Name
				if strings.HasPrefix(typeName, "module") && (strings.HasSuffix(typeName, "Request") || strings.HasSuffix(typeName, "Response")) {
					t.Fatalf("%s owns HTTP request/response contract %s; put module request/response/report/snapshot types under modules/*", path, typeName)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
}

func TestExtractedPoliciesAreNotReimplementedInCompatibilityLayers(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		forbidden []string
	}{
		{
			name: "worker execution failure classification",
			path: filepath.Join("..", "internal", "application", "service", "worker_execution_errors.go"),
			forbidden: []string{
				"patch parse error",
				"command not found",
				"missing dependency",
				"verification failed",
				"spec missing",
			},
		},
		{
			name: "openai thinking bridge cleanup",
			path: filepath.Join("..", "internal", "infrastructure", "llm", "providers", "openai", "thinking_bridge.go"),
			forbidden: []string{
				"Final answer:",
				"parse_reasoning",
				"include_reasoning",
				"separate_reasoning",
				"no_reasoning",
				"want me to",
				"need to respond",
			},
		},
		{
			name: "irodori defaults",
			path: filepath.Join("..", "internal", "infrastructure", "tts", "irodori_defaults.go"),
			forbidden: []string{
				"Aratako/Irodori-TTS-500M-v2",
				`"mps"`,
				`"fp32"`,
				`"independent"`,
				`"male_01"`,
				`"female_01"`,
			},
		},
		{
			name: "irodori synthesis payload",
			path: filepath.Join("..", "internal", "infrastructure", "tts", "irodori_provider.go"),
			forbidden: []string{
				`"voice"`,
				`"style"`,
				`"text"`,
			},
		},
		{
			name: "irodori uploaded audio file data",
			path: filepath.Join("..", "internal", "infrastructure", "tts", "irodori_reference_audio.go"),
			forbidden: []string{
				`"path": referenceAudio`,
				`"meta": map[string]any`,
				`"gradio.FileData"`,
			},
		},
		{
			name: "sbv2 editor request payload",
			path: filepath.Join("..", "internal", "infrastructure", "tts", "sbv2_provider.go"),
			forbidden: []string{
				`"modelFile"`,
				`"moraToneList"`,
				`map[string]any{"text": text}`,
			},
		},
		{
			name: "rencrow bridge session and text rules",
			path: filepath.Join("..", "internal", "infrastructure", "tts", "rencrow_tts_bridge.go"),
			forbidden: []string{
				`"female_01"`,
				"30 * time.Second",
				"1000",
				"utf8.RuneCountInString",
				`"session_id is required"`,
				`"text exceeds max_text_length"`,
				`strings.TrimSpace(out.AudioPath) == ""`,
			},
		},
		{
			name: "chat route decision normalization",
			path: filepath.Join("..", "internal", "adapter", "modulebridge", "chat.go"),
			forbidden: []string{
				"routeDecisionReason",
				"strings.TrimSpace",
				"NormalizeRouteName",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := os.ReadFile(tt.path)
			if err != nil {
				t.Fatalf("read %s: %v", tt.path, err)
			}
			text := string(content)
			for _, forbidden := range tt.forbidden {
				if strings.Contains(text, forbidden) {
					t.Fatalf("%s reimplements extracted policy containing %q; keep this policy in modules/*", tt.path, forbidden)
				}
			}
		})
	}
}
