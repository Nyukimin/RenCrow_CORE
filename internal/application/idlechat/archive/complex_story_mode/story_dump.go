//go:build ignore

package idlechat

import (
	"fmt"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
)

// storyBeatSpec represents a single beat in the beat plan with its label and directional content.
type storyBeatSpec struct {
	label   string
	content string
}

// storyBeatSpecs returns the 4 ordered beat specs from a StoryBeatPlan.
func storyBeatSpecs(beatPlan StoryBeatPlan) []storyBeatSpec {
	return []storyBeatSpec{
		{label: "導入", content: beatPlan.Opening},
		{label: "逸脱", content: beatPlan.Deviation},
		{label: "反転", content: beatPlan.Reversal},
		{label: "着地", content: beatPlan.Landing},
	}
}

// storyReferenceText returns the juvenile text if available, otherwise the full text.
// The juvenile text is shorter and simpler, reducing the risk of the model paraphrasing
// verbose adult prose and producing stilted output.
func storyReferenceText(source StorySource) string {
	if strings.TrimSpace(source.JuvenileText) != "" {
		return source.JuvenileText
	}
	return source.Text
}

// buildBeatMessages builds the exact LLM messages for a single beat in Step 7 (draft generation).
// i is the beat index (0-based).
// prevBeatContent は前ビートのプラン記述（「何が起きたか」要約）、
// prevBeatLastSentence は前ビートの生成テキスト末尾1文（書き出し起点）。
// forbiddenBeats はこの場面より後のビートのラベル（先走り防止用）。
func buildBeatMessages(source StorySource, plan StoryRewritePlan, adaptation StoryAdaptationPlan, i int, label, content, prevBeatContent, prevBeatLastSentence string, forbiddenBeats []string, openingSeed string) []llm.Message {
	axis := storyTransformationAxis(source, plan.RewriteStyle)

	// 前の場面の情報: 出来事（何が起きたか）のみ渡す。
	// 末尾1文は LLM がそのままコピーして beat 境界の重複を引き起こすため渡さない。
	// prevBeatLastSentence はコード側の重複チェック（copies prev last sentence）にのみ使う。
	prevNote := ""
	if prevBeatContent != "" {
		prevNote = fmt.Sprintf("前の場面（完結済み・この出来事は書かない）:\n出来事: %s（完結）\n\n", prevBeatContent)
	}

	// 後続ビートの出来事（この場面では扱わない — content 記述で具体的に禁止）
	forbiddenNote := ""
	if len(forbiddenBeats) > 0 {
		lines := make([]string, 0, len(forbiddenBeats)+1)
		lines = append(lines, "後の場面に残す（この場面では書かない）:")
		for _, fb := range forbiddenBeats {
			lines = append(lines, "- "+fb)
		}
		forbiddenNote = strings.Join(lines, "\n")
	}

	// beat 3（着地）のみ EndingFlavor の具体的な実現方法を追加する
	endingNote := ""
	if i == 3 {
		endingNote = fmt.Sprintf("- %s", endingFlavorHint(plan.EndingFlavor))
	}

	return []llm.Message{
		{Role: "system", Content: "あなたは朗読短編作家です。指定された場面だけを、聞き取りやすい日本語の短い段落で書いてください。"},
		{Role: "user", Content: fmt.Sprintf(`元作品: %s
改題: %s
改変の核（この一点を物語に反映させる）: %s
文体・トーン: %s
舞台: %s
視点: %s
必須モチーフ: %s
認識手がかり: %s
%s今回書く場面: %s（前の場面とは異なる出来事）
この場面の役割: %s

要件:
- この場面だけを2〜4文で書く
- 認識手がかりのどれかを必ず出来事として登場させる
- 会社名、開発計画、ランキング制度、観光客、SNS、スマホ、モデル、広告の話にしない
- 比喩は多用しない。「まるで〜のように」は1段落に1回以内
- 新しい固有名詞を増やさない
- 人物の行動か対話で場面を進める
- 教訓のまとめ、抽象的な総括、象徴の説明を書かない
- 前の場面の文・出来事・表現を繰り返さない（前の場面は完結している）
%s
%s
- %s
- 出力は本文だけ`, source.Title, plan.StoryTitle, axis, plan.Tone, plan.Setting, plan.Viewpoint, strings.Join(plan.CoreMotifs, " / "), strings.Join(adaptation.RecognitionCues, " / "), prevNote, label, content, forbiddenNote, endingNote, beatInstruction(i, openingSeed))},
	}
}

// ─── Exported Inspection API ───────────────────────────────────────────────

// StoryCorpus returns all loaded story sources.
func StoryCorpus() []StorySource {
	return append([]StorySource(nil), storyCorpus...)
}

// AllStoryStyles returns the 5 rewrite style keys.
func AllStoryStyles() []string {
	return append([]string(nil), storyRewriteStyles...)
}

// AllStoryGenres returns the 4 genre labels.
func AllStoryGenres() []string {
	return append([]string(nil), storyGenres...)
}

// BuildStoryPrepForSource builds a StoryPrep for a given source and style.
// Genre and setting are chosen randomly (same as production), so results may vary per call.
func BuildStoryPrepForSource(source StorySource, style string) StoryPrep {
	analysis := analyzeStorySource(source)
	plan := buildStoryRewritePlan(source, analysis, style)
	beatPlan := groundedStoryBeatPlan(source, analysis, plan)
	adaptation := buildStoryAdaptationPlan(analysis.Skeleton, plan, beatPlan)
	return StoryPrep{
		Source:     source,
		Analysis:   analysis,
		Plan:       plan,
		BeatPlan:   beatPlan,
		Adaptation: adaptation,
	}
}

// DumpStoryPrep prints a human-readable structured view of a StoryPrep.
func DumpStoryPrep(prep StoryPrep) {
	src := prep.Source
	analysis := prep.Analysis
	plan := prep.Plan
	beatPlan := prep.BeatPlan
	adaptation := prep.Adaptation

	fmt.Printf("=== StoryPrep: %s [%s] ===\n\n", src.Title, plan.RewriteStyle)

	fmt.Println("── SOURCE ──")
	fmt.Printf("  ID:    %s\n", src.ID)
	fmt.Printf("  Title: %s\n", src.Title)
	fmt.Printf("  Label: %s / %s\n", src.SourceLabel, src.Kind)
	runes := []rune(src.Text)
	if len(runes) > 200 {
		fmt.Printf("  Text (%d chars, first 200):\n    %s…\n", len(runes), string(runes[:200]))
	} else {
		fmt.Printf("  Text (%d chars):\n    %s\n", len(runes), src.Text)
	}
	fmt.Println()

	fmt.Println("── ANALYSIS ──")
	fmt.Printf("  CoreMotifs:          %s\n", joinOrEmpty(analysis.CoreMotifs))
	fmt.Printf("  TabooOrRule:         %s\n", emptyOr(analysis.TabooOrRule))
	fmt.Printf("  RewardAndPunish:     %s\n", emptyOr(analysis.RewardAndPunish))
	fmt.Printf("  EmotionalAftertaste: %s\n", emptyOr(analysis.EmotionalAftertaste))
	fmt.Printf("  RecognitionCues:     %s\n", joinOrEmpty(analysis.Skeleton.RecognitionCues))
	beatLabels := storyBeatLabels(analysis.Skeleton.RequiredBeats)
	if len(beatLabels) > 0 {
		fmt.Printf("  RequiredBeats:       %s\n", strings.Join(beatLabels, " -> "))
	} else {
		fmt.Printf("  RequiredBeats:       (none)\n")
	}
	fmt.Println()

	fmt.Println("── PLAN ──")
	fmt.Printf("  Style:        %s\n", plan.RewriteStyle)
	fmt.Printf("  Twist:        %s\n", storyTransformationAxis(src, plan.RewriteStyle))
	fmt.Printf("  StoryTitle:   %s\n", plan.StoryTitle)
	fmt.Printf("  Hook:         %s\n", plan.Hook)
	fmt.Printf("  Premise:      %s\n", plan.Premise)
	fmt.Printf("  Setting:      %s\n", plan.Setting)
	fmt.Printf("  Viewpoint:    %s\n", plan.Viewpoint)
	fmt.Printf("  Tone:         %s\n", plan.Tone)
	fmt.Printf("  EndingShape:  %s\n", plan.EndingShape)
	fmt.Printf("  EndingFlavor: %s\n", plan.EndingFlavor)
	fmt.Printf("  CoreMotifs:   %s\n", joinOrEmpty(plan.CoreMotifs))
	fmt.Printf("  MotifMap:     %s\n", joinOrEmpty(plan.MotifMap))
	fmt.Println()

	openingSeed := storyOpeningSeed(src, plan)
	fmt.Println("── BEAT PLAN ──")
	fmt.Printf("  OpeningSeed: %s\n", openingSeed)
	fmt.Printf("  Opening:     %s\n", beatPlan.Opening)
	fmt.Printf("  Deviation:   %s\n", beatPlan.Deviation)
	fmt.Printf("  Reversal:    %s\n", beatPlan.Reversal)
	fmt.Printf("  Landing:     %s\n", beatPlan.Landing)
	fmt.Println()

	fmt.Println("── ADAPTATION ──")
	fmt.Printf("  SkeletonID:      %s\n", adaptation.SkeletonID)
	fmt.Printf("  RewriteStyle:    %s\n", adaptation.RewriteStyle)
	fmt.Printf("  RecognitionCues: %s\n", joinOrEmpty(adaptation.RecognitionCues))
	fmt.Printf("  EndingFlavor:    %s\n", adaptation.EndingFlavor)
	for i, m := range adaptation.BeatMappings {
		fmt.Printf("  BeatMapping[%d]:  %s\n", i, m)
	}
	for i, m := range adaptation.MotifMappings {
		fmt.Printf("  MotifMapping[%d]: %s\n", i, m)
	}
	fmt.Println()
}

// BeatPromptView holds the inspection view of a single beat's LLM messages.
type BeatPromptView struct {
	BeatIndex   int
	BeatLabel   string
	BeatContent string
	Messages    []llm.Message
}

// RevisionPromptView holds the inspection view of the revision (Step 8) LLM messages.
type RevisionPromptView struct {
	DraftText string
	Messages  []llm.Message
}

// BuildBeatPromptViews builds the exact LLM messages that would be sent for each beat in Step 7.
// Context is shown as empty (beat 0 has no prior paragraph; beats 1-3 would have LLM output as context).
func BuildBeatPromptViews(prep StoryPrep) []BeatPromptView {
	openingSeed := storyOpeningSeed(prep.Source, prep.Plan)
	specs := storyBeatSpecs(prep.BeatPlan)
	views := make([]BeatPromptView, 0, len(specs))
	for i, spec := range specs {
		// prompt inspection なので前ビートの実行テキストはなく、プラン記述だけ渡す
		prevBeatContent := ""
		if i > 0 {
			prevBeatContent = specs[i-1].content
		}
		forbiddenBeats := make([]string, 0, len(specs)-i-1)
		for _, fs := range specs[i+1:] {
			forbiddenBeats = append(forbiddenBeats, fs.content)
		}
		messages := buildBeatMessages(prep.Source, prep.Plan, prep.Adaptation, i, spec.label, spec.content, prevBeatContent, "", forbiddenBeats, openingSeed)
		views = append(views, BeatPromptView{
			BeatIndex:   i,
			BeatLabel:   spec.label,
			BeatContent: spec.content,
			Messages:    messages,
		})
	}
	return views
}

// BuildRevisionPromptView builds the exact LLM messages that would be sent in Step 8.
// draftText may be a placeholder string for pure prompt inspection.
func BuildRevisionPromptView(prep StoryPrep, draftText string) RevisionPromptView {
	messages := buildRevisionMessages(prep.Source, prep.Analysis, prep.Plan, prep.Adaptation, prep.BeatPlan, draftText)
	return RevisionPromptView{
		DraftText: draftText,
		Messages:  messages,
	}
}

// DumpBeatPromptViews prints a human-readable view of beat prompts.
func DumpBeatPromptViews(views []BeatPromptView) {
	for _, v := range views {
		fmt.Printf("╔═══ Beat %d: %s ═══╗\n", v.BeatIndex, v.BeatLabel)
		fmt.Printf("  Direction: %s\n\n", v.BeatContent)
		for j, msg := range v.Messages {
			fmt.Printf("  ── [msg %d] role=%s ──\n", j, msg.Role)
			fmt.Println(dumpIndent(msg.Content))
			fmt.Println()
		}
		fmt.Println("╚══════════════════════╝")
		fmt.Println()
	}
}

// DumpRevisionPromptView prints a human-readable view of the revision prompt.
func DumpRevisionPromptView(v RevisionPromptView) {
	fmt.Println("╔═══ Revision Prompt (Step 8) ═══╗")
	if v.DraftText != "" {
		fmt.Printf("  Draft (%d chars):\n", len([]rune(v.DraftText)))
		fmt.Println(dumpIndent(v.DraftText))
		fmt.Println()
	}
	for j, msg := range v.Messages {
		fmt.Printf("  ── [msg %d] role=%s ──\n", j, msg.Role)
		fmt.Println(dumpIndent(msg.Content))
		fmt.Println()
	}
	fmt.Println("╚════════════════════════════════╝")
}

// PlanProblem represents a detected issue in a StoryPrep that may cause bad LLM output.
type PlanProblem struct {
	Field   string
	Value   string
	Pattern string
	Advice  string
}

// ScanStoryPlanProblems detects known anti-patterns in a StoryPrep.
// These are patterns that, based on observed outputs, reliably cause bad LLM generation.
func ScanStoryPlanProblems(prep StoryPrep) []PlanProblem {
	var problems []PlanProblem

	checkField := func(field, value, pattern, advice string) {
		if strings.Contains(value, pattern) {
			problems = append(problems, PlanProblem{
				Field: field, Value: value, Pattern: pattern, Advice: advice,
			})
		}
	}

	// Code-level fallback axes: exact match against what storyTransformationAxis returns when spec.twists is missing.
	// These indicate the JSON spec.twists entry needs to be filled in.
	codeAxisFallbacks := map[string]string{
		fmt.Sprintf("『%s』を、主役のすぐ近くにいた人物の立場からの見え方の差", prep.Source.Title):     "view_shift の code-level fallback → JSON の spec.twists.view_shift を設定する。",
		fmt.Sprintf("『%s』の報いや救いが、別の価値観から見たときに逆転する構造", prep.Source.Title):    "value_shift の code-level fallback → JSON の spec.twists.value_shift を設定する。",
		fmt.Sprintf("『%s』の因果や報いが逆だったら何が残るか", prep.Source.Title):                     "inversion の code-level fallback → JSON の spec.twists.inversion を設定する。",
		fmt.Sprintf("『%s』の力や出来事の及ぶ範囲が変わったとき、何が見えるか", prep.Source.Title):     "scale_shift の code-level fallback → JSON の spec.twists.scale_shift を設定する。",
		fmt.Sprintf("『%s』の役割と従属の非対称が生む構造", prep.Source.Title):                         "role_shift の code-level fallback → JSON の spec.twists.role_shift を設定する。",
	}
	if advice, isCodeFallback := codeAxisFallbacks[prep.Plan.Hook]; isCodeFallback {
		problems = append(problems, PlanProblem{
			Field:   "Plan.Hook",
			Value:   prep.Plan.Hook,
			Pattern: "code-level fallback",
			Advice:  advice,
		})
	}

	// Hypothetical framing in Hook/Premise: "だったら" pattern causes LLM to echo "もし〜だったら" as opener.
	// Only flag if NOT already caught as code fallback (to avoid duplicate entries).
	if _, isCodeFallback := codeAxisFallbacks[prep.Plan.Hook]; !isCodeFallback {
		checkField("Plan.Hook", prep.Plan.Hook, "だったら",
			"Plan.Hook に「だったら」→ LLM が「もし〜だったら」で書き始める直接原因。twists の文言を変える。")
	}
	checkField("Plan.Premise", prep.Plan.Premise, "だったら",
		"Plan.Premise に「だったら」→ 同様に仮定法起点の出力を誘発。")

	// Meta-language in BeatPlan that leaks verbatim into LLM context
	checkField("BeatPlan.Deviation", prep.BeatPlan.Deviation, "というひねりが見える",
		"BeatPlan.Deviation に「というひねりが見える」→ 設計説明文が LLM に渡る。groundedStoryBeatPlan の Deviation テンプレートを見直す。")
	checkField("BeatPlan.Opening", prep.BeatPlan.Opening, "最初の違和感として立ち上がる",
		"BeatPlan.Opening に fallback meta-text → fallbackStoryBeatPlan が使われた可能性。groundedStoryBeatPlan を確認。")
	checkField("BeatPlan.Landing", prep.BeatPlan.Landing, "最後に残るのは",
		"BeatPlan.Landing に「最後に残るのは〜だ」meta-text → 「最後に残るのは〜だ」という文が LLM 出力にそのまま現れる。")

	// Empty required fields that reduce story quality
	if len(prep.Analysis.Skeleton.RecognitionCues) == 0 {
		problems = append(problems, PlanProblem{
			Field: "Analysis.Skeleton.RecognitionCues", Value: "(empty)", Pattern: "empty",
			Advice: "RecognitionCues がない → JSON の spec.skeleton.recognition_cues に 2 件以上追加する。",
		})
	}
	if strings.TrimSpace(prep.Analysis.TabooOrRule) == "" {
		problems = append(problems, PlanProblem{
			Field: "Analysis.TabooOrRule", Value: "(empty)", Pattern: "empty",
			Advice: "TabooOrRule がない → JSON の spec.skeleton.taboo_or_rule を設定する。",
		})
	}
	if len(prep.Plan.CoreMotifs) == 0 {
		problems = append(problems, PlanProblem{
			Field: "Plan.CoreMotifs", Value: "(empty)", Pattern: "empty",
			Advice: "CoreMotifs がない → JSON の spec.skeleton.canonical_motifs を設定する。",
		})
	}

	// Generic opening seed fallback (source not registered in storyOpeningSeed switch)
	openingSeed := storyOpeningSeed(prep.Source, prep.Plan)
	if strings.Contains(openingSeed, "これから起こる出来事の気配を見つけた") {
		problems = append(problems, PlanProblem{
			Field: "OpeningSeed", Value: openingSeed, Pattern: "generic fallback",
			Advice: fmt.Sprintf("OpeningSeed が汎用フォールバック → storyOpeningSeed に case %q を追加して固有の書き出し文を設定する。", prep.Source.ID),
		})
	}

	return problems
}

// ─── Internal helpers ──────────────────────────────────────────────────────

func joinOrEmpty(ss []string) string {
	if len(ss) == 0 {
		return "(empty)"
	}
	return strings.Join(ss, " / ")
}

func emptyOr(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(empty)"
	}
	return s
}

// StoryGenerateResult は preview-run モードで返す生成結果。
type StoryGenerateResult struct {
	DraftText    string
	StoryText    string
	RevisionNote string
	Err          error
}

// RawBeat は1ビートのラベルと生文章を保持する。
type RawBeat struct {
	Label string // 導入 / 逸脱 / 反転 / 着地
	Text  string // LLM が生成した生テキスト（バリデーションなし）
	Err   error  // 生成失敗時のエラー
}

// GenerateRawBeats は Step 6.5 用。バリデーションなしで 4 ビートの生文章を生成して返す。
// preview-run モード専用。本番パイプラインでは使用しない。
func (o *IdleChatOrchestrator) GenerateRawBeats(prep StoryPrep) []RawBeat {
	openingSeed := storyOpeningSeed(prep.Source, prep.Plan)
	specs := storyBeatSpecs(prep.BeatPlan)
	beats := make([]RawBeat, len(specs))
	generatedTexts := make([]string, 0, len(specs))
	for i, spec := range specs {
		var prevBeatContent, prevBeatLastSentence string
		if len(generatedTexts) > 0 {
			prevBeatContent = specs[i-1].content
			prevBeatLastSentence = lastSentenceOf(generatedTexts[len(generatedTexts)-1])
		}
		forbiddenBeats := make([]string, 0, len(specs)-i-1)
		for _, fs := range specs[i+1:] {
			forbiddenBeats = append(forbiddenBeats, fs.label)
		}
		messages := buildBeatMessages(prep.Source, prep.Plan, prep.Adaptation, i, spec.label, spec.content, prevBeatContent, prevBeatLastSentence, forbiddenBeats, openingSeed)
		resp, err := o.providerForSpeaker("shiro").Generate(o.ctx, llm.GenerateRequest{
			Messages:    messages,
			MaxTokens:   300,
			Temperature: 0.3,
		})
		if err != nil {
			beats[i] = RawBeat{Label: spec.label, Err: err}
			generatedTexts = append(generatedTexts, "")
			continue
		}
		text := normalizeStoryNarrative(resp.Content)
		beats[i] = RawBeat{Label: spec.label, Text: text}
		generatedTexts = append(generatedTexts, text)
	}
	return beats
}

// GenerateFromPrep は準備済みの StoryPrep を受け取り Step 7+8 を実行して結果を返す。
// テストツールの preview-run モード専用。
func (o *IdleChatOrchestrator) GenerateFromPrep(prep StoryPrep) StoryGenerateResult {
	draftText, _, err := o.GenerateDraftFromPrep(prep)
	if err != nil {
		return StoryGenerateResult{Err: err}
	}
	return o.GenerateRevisionFromPrep(prep, draftText)
}

// GenerateDraftFromPrep は Step 7（第1稿生成）のみを実行して draft テキストとリトライログを返す。
// preview-run の Step 7.5 ゲート用。
func (o *IdleChatOrchestrator) GenerateDraftFromPrep(prep StoryPrep) (string, []string, error) {
	return o.retryStoryDraft(prep.Source, prep.Analysis, prep.Plan, prep.Adaptation, prep.BeatPlan)
}

// GenerateRevisionFromPrep は Step 8（改稿）のみを実行して結果を返す。
// preview-run の Step 7.5 ゲート用。
func (o *IdleChatOrchestrator) GenerateRevisionFromPrep(prep StoryPrep, draftText string) StoryGenerateResult {
	storyText, revisionNote, err := o.retryStoryRevision(prep.Source, prep.Analysis, prep.Plan, prep.Adaptation, prep.BeatPlan, draftText)
	if err != nil {
		return StoryGenerateResult{DraftText: draftText, Err: err}
	}
	return StoryGenerateResult{
		DraftText:    draftText,
		StoryText:    storyText,
		RevisionNote: revisionNote,
	}
}

func dumpIndent(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = "    " + line
	}
	return strings.Join(lines, "\n")
}
