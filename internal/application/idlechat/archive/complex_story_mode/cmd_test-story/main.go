//go:build ignore

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/idlechat"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/session"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/ollama"
)

const defaultStoryDataDir = "./data/story"

func main() {
	args := os.Args[1:]
	mode := "run"
	if len(args) > 0 {
		mode = args[0]
		args = args[1:]
	}

	switch mode {
	case "run":
		runMode()
	case "preview-run":
		previewRunMode(args)
	case "list":
		listMode()
	case "dump-plan":
		dumpPlanMode(args)
	case "dump-all":
		dumpAllMode(args)
	case "dump-prompt":
		dumpPromptMode(args)
	case "scan":
		scanMode(args)
	case "scan-all":
		scanAllMode()
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q\n\n", mode)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  test-story                            — run a full story session (LLM required)")
	fmt.Fprintln(os.Stderr, "  test-story run                        — same as above")
	fmt.Fprintln(os.Stderr, "  test-story preview-run SOURCE [STYLE] — show Step6 plan, confirm, then generate")
	fmt.Fprintln(os.Stderr, "  test-story list                       — list all story source IDs")
	fmt.Fprintln(os.Stderr, "  test-story dump-plan SOURCE [STYLE]   — dump StoryPrep for source × style")
	fmt.Fprintln(os.Stderr, "  test-story dump-all  SOURCE           — dump StoryPrep for source × all 5 styles")
	fmt.Fprintln(os.Stderr, "  test-story dump-prompt SOURCE STYLE   — dump exact LLM prompts (beats + revision)")
	fmt.Fprintln(os.Stderr, "  test-story scan SOURCE STYLE          — scan plan for known anti-patterns")
	fmt.Fprintln(os.Stderr, "  test-story scan-all                   — scan all 27×5 combinations")
}

// mustLoadData loads story JSON data and exits on error.
func mustLoadData() {
	dir := os.Getenv("STORY_DATA_DIR")
	if dir == "" {
		dir = defaultStoryDataDir
	}
	if err := idlechat.LoadStoryData(dir); err != nil {
		log.Fatalf("load story data from %q: %v", dir, err)
	}
}

// findSource looks up a StorySource by ID or title.
func findSource(idOrTitle string) (idlechat.StorySource, bool) {
	for _, src := range idlechat.StoryCorpus() {
		if src.ID == idOrTitle || src.Title == idOrTitle {
			return src, true
		}
	}
	return idlechat.StorySource{}, false
}

// ─── preview-run ──────────────────────────────────────────────────────────

func previewRunMode(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: test-story preview-run SOURCE [STYLE]")
		os.Exit(1)
	}
	mustLoadData()

	src, ok := findSource(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "source not found: %q\n", args[0])
		os.Exit(1)
	}

	style := ""
	if len(args) >= 2 {
		style = args[1]
	}

	// orchestrator 初期化（Step 6.5 と Step 7+8 の両方で使用）
	cfg, err := config.LoadConfig("./config.yaml")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	chatProvider := ollama.NewOllamaProviderWithNumCtx(cfg.Ollama.BaseURL, cfg.Ollama.Model, 32768)
	memory := session.NewCentralMemory()
	dir := cfg.IdleChat.StoryDataDir
	if dir == "" {
		dir = defaultStoryDataDir
	}
	orch := idlechat.NewIdleChatOrchestrator(
		chatProvider, memory,
		cfg.IdleChat.Participants, cfg.IdleChat.IntervalMin,
		cfg.IdleChat.MaxTurns, cfg.IdleChat.Temperature,
		cfg.Prompts.IdleChatAgents, dir,
	)
	orch.SetSpeakerProviders(map[string]llm.LLMProvider{
		"mio":   chatProvider,
		"shiro": chatProvider,
	})

	// Step 6: 決定論的プラン生成（LLM不使用）
	prep := idlechat.BuildStoryPrepForSource(src, style)

	// Step 6.5: 情報収集フェーズ（全出力必須）
	fmt.Println(strings.Repeat("═", 70))
	fmt.Println("STEP 6.5 — 情報収集フェーズ")
	fmt.Println(strings.Repeat("═", 70))

	// [1] StoryPrep フルダンプ（ANALYSIS / PLAN / BEAT PLAN / ADAPTATION）
	fmt.Println()
	idlechat.DumpStoryPrep(prep)

	// [2] アンチパターン警告
	problems := idlechat.ScanStoryPlanProblems(prep)
	if len(problems) > 0 {
		fmt.Printf("\n⚠ %d 件の問題:\n", len(problems))
		for _, p := range problems {
			fmt.Printf("  [%s] %s\n", p.Field, p.Advice)
		}
	}

	// [3] BEAT 0〜3 の生文章（バリデーションなし）
	fmt.Println()
	fmt.Println(strings.Repeat("─", 70))
	fmt.Println("── BEAT 生の文章（バリデーションなし）")
	fmt.Println(strings.Repeat("─", 70))
	rawBeats := orch.GenerateRawBeats(prep)
	for i, b := range rawBeats {
		fmt.Printf("\n── BEAT %d（%s）\n", i, b.Label)
		if b.Err != nil {
			fmt.Printf("  ✘ 生成失敗: %v\n", b.Err)
		} else if b.Text == "" {
			fmt.Println("  （空）")
		} else {
			fmt.Println(b.Text)
		}
	}

	// 確認（ユーザーの OK が出るまで先に進まない）
	fmt.Println()
	fmt.Println(strings.Repeat("─", 70))
	fmt.Print("Step 7+8（バリデーション＋改稿）を実行しますか？ [Enter で続行 / q で中止]: ")
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	input := strings.TrimSpace(scanner.Text())
	if strings.ToLower(input) == "q" {
		fmt.Println("中止しました。")
		return
	}

	fmt.Println()
	fmt.Println(strings.Repeat("═", 70))
	fmt.Println("STEP 7 — 第1稿生成（LLM: shiro × 4ビート）")
	fmt.Println(strings.Repeat("═", 70))

	draftText, draftRetryLog, draftErr := orch.GenerateDraftFromPrep(prep)

	// Step 7.5: 情報収集フェーズ（全パラメータ＋第1稿全文）
	fmt.Println()
	fmt.Println(strings.Repeat("═", 70))
	fmt.Println("STEP 7.5 — 情報収集フェーズ")
	fmt.Println(strings.Repeat("═", 70))

	// [1] StoryPrep フルダンプ（ANALYSIS / PLAN / BEAT PLAN / ADAPTATION）
	fmt.Println()
	idlechat.DumpStoryPrep(prep)

	// [2] Step 7 リトライログ
	if len(draftRetryLog) > 0 {
		fmt.Println(strings.Repeat("─", 70))
		fmt.Println("── Step 7 リトライログ")
		fmt.Println(strings.Repeat("─", 70))
		for _, entry := range draftRetryLog {
			fmt.Println(entry)
		}
		fmt.Println()
	}

	// [3] DRAFT 全文
	fmt.Println(strings.Repeat("─", 70))
	fmt.Println("── DRAFT（Step 7 出力・全文）")
	fmt.Println(strings.Repeat("─", 70))
	if draftErr != nil {
		fmt.Printf("✘ Step 7 失敗: %v\n", draftErr)
	} else if draftText == "" {
		fmt.Println("（生成失敗）")
	} else {
		fmt.Println(draftText)
	}

	// 確認（ユーザーの OK が出るまで先に進まない）
	fmt.Println()
	fmt.Println(strings.Repeat("─", 70))
	fmt.Print("Step 8（改稿）を実行しますか？ [Enter で続行 / q で中止]: ")
	scanner2 := bufio.NewScanner(os.Stdin)
	scanner2.Scan()
	if strings.ToLower(strings.TrimSpace(scanner2.Text())) == "q" {
		fmt.Println("中止しました。")
		return
	}
	if draftErr != nil {
		fmt.Fprintln(os.Stderr, "Step 7 が失敗しているため Step 8 を実行できません。")
		return
	}

	fmt.Println()
	fmt.Println(strings.Repeat("═", 70))
	fmt.Println("STEP 8 — 改稿（LLM: shiro）")
	fmt.Println(strings.Repeat("═", 70))

	result := orch.GenerateRevisionFromPrep(prep, draftText)

	fmt.Println()
	fmt.Println(strings.Repeat("─", 70))
	fmt.Println("── STORY（Step 8 出力・全文）")
	fmt.Println(strings.Repeat("─", 70))
	if result.StoryText != "" {
		fmt.Println(result.StoryText)
	} else {
		fmt.Println("（生成失敗）")
	}

	if result.RevisionNote != "" {
		fmt.Printf("\n── 改稿メモ: %s\n", result.RevisionNote)
	}
	if result.Err != nil {
		fmt.Fprintf(os.Stderr, "\n✘ エラー: %v\n", result.Err)
	}
}

// ─── run ──────────────────────────────────────────────────────────────────

func runMode() {
	cfg, err := config.LoadConfig("./config.yaml")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	chatProvider := ollama.NewOllamaProviderWithNumCtx(cfg.Ollama.BaseURL, cfg.Ollama.Model, 32768)
	memory := session.NewCentralMemory()

	dir := cfg.IdleChat.StoryDataDir
	if dir == "" {
		dir = defaultStoryDataDir
	}

	orch := idlechat.NewIdleChatOrchestrator(
		chatProvider,
		memory,
		cfg.IdleChat.Participants,
		cfg.IdleChat.IntervalMin,
		cfg.IdleChat.MaxTurns,
		cfg.IdleChat.Temperature,
		cfg.Prompts.IdleChatAgents,
		dir,
	)
	orch.SetSpeakerProviders(map[string]llm.LLMProvider{
		"mio":   chatProvider,
		"shiro": chatProvider,
	})

	orch.RunStorySession()

	history := orch.GetHistory(1)
	if len(history) == 0 {
		fmt.Println(`{"status":"empty"}`)
		return
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(history[0]); err != nil {
		log.Fatalf("encode history: %v", err)
	}
}

// ─── list ─────────────────────────────────────────────────────────────────

func listMode() {
	mustLoadData()
	sources := idlechat.StoryCorpus()
	fmt.Printf("Loaded %d stories:\n\n", len(sources))
	for _, src := range sources {
		fmt.Printf("  %-22s %s  [%s / %s]\n", src.ID, src.Title, src.SourceLabel, src.Kind)
	}
}

// ─── dump-plan ────────────────────────────────────────────────────────────

func dumpPlanMode(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: test-story dump-plan SOURCE [STYLE]")
		os.Exit(1)
	}
	mustLoadData()

	src, ok := findSource(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "source not found: %q (use 'test-story list' to see IDs)\n", args[0])
		os.Exit(1)
	}

	styles := idlechat.AllStoryStyles()
	if len(args) >= 2 {
		styles = []string{args[1]}
	}

	for i, style := range styles {
		prep := idlechat.BuildStoryPrepForSource(src, style)
		idlechat.DumpStoryPrep(prep)
		if i < len(styles)-1 {
			fmt.Println(strings.Repeat("─", 60))
		}
	}
}

// ─── dump-all ─────────────────────────────────────────────────────────────

func dumpAllMode(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: test-story dump-all SOURCE")
		os.Exit(1)
	}
	mustLoadData()

	src, ok := findSource(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "source not found: %q\n", args[0])
		os.Exit(1)
	}

	styles := idlechat.AllStoryStyles()
	for i, style := range styles {
		prep := idlechat.BuildStoryPrepForSource(src, style)
		idlechat.DumpStoryPrep(prep)
		problems := idlechat.ScanStoryPlanProblems(prep)
		if len(problems) > 0 {
			fmt.Printf("  ⚠ %d problem(s):\n", len(problems))
			for _, p := range problems {
				fmt.Printf("    [%s] %s\n", p.Field, p.Advice)
			}
			fmt.Println()
		}
		if i < len(styles)-1 {
			fmt.Println(strings.Repeat("─", 60))
		}
	}
}

// ─── dump-prompt ──────────────────────────────────────────────────────────

func dumpPromptMode(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: test-story dump-prompt SOURCE STYLE")
		os.Exit(1)
	}
	mustLoadData()

	src, ok := findSource(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "source not found: %q\n", args[0])
		os.Exit(1)
	}

	style := args[1]
	prep := idlechat.BuildStoryPrepForSource(src, style)
	idlechat.DumpStoryPrep(prep)

	problems := idlechat.ScanStoryPlanProblems(prep)
	if len(problems) > 0 {
		fmt.Printf("⚠ %d problem(s) detected:\n", len(problems))
		for _, p := range problems {
			fmt.Printf("  [%s] %s\n", p.Field, p.Advice)
		}
		fmt.Println()
	}

	fmt.Println(strings.Repeat("═", 70))
	fmt.Println("BEAT PROMPTS  (Step 7 — sent to shiro × 4 beats)")
	fmt.Println(strings.Repeat("═", 70))
	beatViews := idlechat.BuildBeatPromptViews(prep)
	idlechat.DumpBeatPromptViews(beatViews)

	fmt.Println(strings.Repeat("═", 70))
	fmt.Println("REVISION PROMPT  (Step 8 — sent to shiro after draft)")
	fmt.Println(strings.Repeat("═", 70))
	revView := idlechat.BuildRevisionPromptView(prep, "[第1稿テキストがここに入る]")
	idlechat.DumpRevisionPromptView(revView)
}

// ─── scan ─────────────────────────────────────────────────────────────────

func scanMode(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: test-story scan SOURCE STYLE")
		os.Exit(1)
	}
	mustLoadData()

	src, ok := findSource(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "source not found: %q\n", args[0])
		os.Exit(1)
	}

	style := args[1]
	prep := idlechat.BuildStoryPrepForSource(src, style)
	problems := idlechat.ScanStoryPlanProblems(prep)

	if len(problems) == 0 {
		fmt.Printf("OK  [%s / %s] — no anti-patterns found\n", src.ID, style)
		return
	}

	fmt.Printf("PROBLEMS  [%s / %s] — %d issue(s):\n\n", src.ID, style, len(problems))
	for i, p := range problems {
		fmt.Printf("  [%d] %s\n", i+1, p.Field)
		if len(p.Value) > 80 {
			fmt.Printf("       value:   %s…\n", p.Value[:80])
		} else {
			fmt.Printf("       value:   %s\n", p.Value)
		}
		fmt.Printf("       pattern: %s\n", p.Pattern)
		fmt.Printf("       advice:  %s\n", p.Advice)
		fmt.Println()
	}
}

// ─── scan-all ─────────────────────────────────────────────────────────────

func scanAllMode() {
	mustLoadData()
	sources := idlechat.StoryCorpus()
	styles := idlechat.AllStoryStyles()
	total := 0
	clean := 0

	for _, src := range sources {
		for _, style := range styles {
			prep := idlechat.BuildStoryPrepForSource(src, style)
			problems := idlechat.ScanStoryPlanProblems(prep)
			if len(problems) == 0 {
				clean++
				continue
			}
			fmt.Printf("[%s / %s] %d problem(s):\n", src.ID, style, len(problems))
			for _, p := range problems {
				fmt.Printf("  ▸ [%s] %s\n", p.Field, p.Advice)
			}
			total += len(problems)
		}
	}

	combos := len(sources) * len(styles)
	fmt.Printf("\n── Summary ──\n")
	fmt.Printf("  Combinations: %d  (%d sources × %d styles)\n", combos, len(sources), len(styles))
	fmt.Printf("  Clean:        %d\n", clean)
	fmt.Printf("  With problems: %d\n", combos-clean)
	fmt.Printf("  Total problems: %d\n", total)
}
