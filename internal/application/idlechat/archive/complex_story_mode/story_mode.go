//go:build ignore

package idlechat

import (
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	domaintransport "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/transport"
)

const (
	storyChunkMaxRunes     = 90
	storyChunkMinRunes     = 28
	storyStageMaxRetries   = 3
	storySourceMaxAttempts = 3
)

// beatFailError はビートのバリデーション失敗を表すエラー。
// 失敗理由（reason）とバリデーションを通過できなかった生成テキスト（text）を持つ。
type beatFailError struct {
	reason string
	text   string
}

func (e *beatFailError) Error() string { return e.reason }

type StorySource struct {
	ID           string
	Title        string
	SourceLabel  string
	Kind         string
	Language     string
	PublicDomain bool
	Text         string
	JuvenileText string // ジュブナイル版テキスト。空の場合は Text を使う
	OpeningSeed  string // 生成の起点となる冒頭文。空の場合はデフォルト文を使う
	Setting      string // 物語の舞台設定。空の場合はデフォルト設定を使う
}

type StoryRewritePlan struct {
	SourceTitle  string
	RewriteStyle string
	StoryTitle   string
	Premise      string
	Setting      string
	Viewpoint    string
	Tone         string
	Hook         string
	EndingShape  string
	EndingFlavor string
	CoreMotifs   []string
	MotifMap     []string
}

type StoryBeat struct {
	ID    string
	Label string
}

type StorySkeleton struct {
	ID                  string
	SourceTitle         string
	CanonicalMotifs     []string
	RequiredBeats       []StoryBeat
	RoleConstraints     []string
	TabooOrRule         string
	RewardPunishment    string
	EmotionalAftertaste string
	RecognitionCues     []string
}

type StorySourceAnalysis struct {
	CoreMotifs          []string
	RoleMap             []string
	TabooOrRule         string
	RewardAndPunish     string
	EmotionalAftertaste string
	Skeleton            StorySkeleton
}

type StoryBeatPlan struct {
	Opening   string
	Deviation string
	Reversal  string
	Landing   string
}

type StoryAdaptationPlan struct {
	SkeletonID      string
	RewriteStyle    string
	BeatMappings    []string
	MotifMappings   []string
	RoleRemap       []string
	EndingFlavor    string
	RecognitionCues []string
}

// StoryPrep はソース選択からアダプテーションプラン構築まで（Step 2〜6）をまとめた構造体。
// LLM を使わない決定論的な準備フェーズの出力を保持する。
type StoryPrep struct {
	Source     StorySource
	Analysis   StorySourceAnalysis
	Plan       StoryRewritePlan
	BeatPlan   StoryBeatPlan
	Adaptation StoryAdaptationPlan
}

var storyRewriteStyles = []string{"role_shift", "view_shift", "value_shift", "inversion", "scale_shift"}
var storyGenres = []string{"ノワール", "ホラー", "コメディ", "ノーマル"}
var storyScales = []string{"極小", "極大"}
var storyRandIntn = rand.Intn

// storySourceText は廃止済み。テキストは data/story/<id>.json から読み込む。
func storySourceText(_ string) string {
	return ""
}

func (o *IdleChatOrchestrator) RunStorySession() {
	sessionID := fmt.Sprintf("story-%d", time.Now().Unix())
	startedAt := time.Now().In(jst)

	o.mu.Lock()
	o.chatActive = true
	o.sessionMode = "story"
	o.mu.Unlock()

	style := chooseStoryRewriteStyle(o.GetHistory(12))
	type storySuccess struct {
		source       StorySource
		plan         StoryRewritePlan
		draftText    string
		revisionNote string
		storyText    string
	}
	var result storySuccess
	var ok bool
	usedSources := make(map[string]struct{}, storySourceMaxAttempts)
	for sourceAttempt := 0; sourceAttempt < storySourceMaxAttempts; sourceAttempt++ {
		prep := o.prepareStory(style, usedSources)
		usedSources[prep.Source.Title] = struct{}{}
		draftText, _, err := o.retryStoryDraft(prep.Source, prep.Analysis, prep.Plan, prep.Adaptation, prep.BeatPlan)
		if err != nil {
			log.Printf("[Story] draft failed after retries (%s): %v", prep.Source.Title, err)
			continue
		}
		if storyDraftMatchesSourceRetelling(prep.Source, draftText) {
			result = storySuccess{
				source:       prep.Source,
				plan:         prep.Plan,
				draftText:    draftText,
				revisionNote: "第1稿が元話の骨格を十分に保っていたため、そのまま採用した。",
				storyText:    draftText,
			}
			ok = true
			break
		}
		storyText, revisionNote, err := o.retryStoryRevision(prep.Source, prep.Analysis, prep.Plan, prep.Adaptation, prep.BeatPlan, draftText)
		if err != nil {
			log.Printf("[Story] revision failed after retries (%s): %v", prep.Source.Title, err)
			candidate := strings.TrimSpace(draftText)
			if candidate == "" || !storyNarrativeLooksLikeProse(candidate) || !storySatisfiesSkeleton(candidate, prep.Analysis.Skeleton, prep.Adaptation) {
				candidate = repairStoryDraft(prep.Source, prep.Analysis, prep.Plan, prep.Adaptation, prep.BeatPlan, draftText)
			}
			if strings.TrimSpace(candidate) == "" || !storyNarrativeLooksLikeProse(candidate) {
				continue
			}
			storyText = candidate
			revisionNote = "改稿が不安定だったため、第1稿を整文して採用した。"
		}
		result = storySuccess{
			source:       prep.Source,
			plan:         prep.Plan,
			draftText:    draftText,
			revisionNote: revisionNote,
			storyText:    storyText,
		}
		ok = true
		break
	}
	if !ok {
		log.Printf("[Story] story generation failed for %d sources, falling back to normal chat", storySourceMaxAttempts)
		o.mu.Lock()
		o.sessionMode = "idle"
		o.currentTopic = ""
		o.mu.Unlock()
		o.runChatSession(StrategySingleGenre)
		o.mu.Lock()
		o.chatActive = false
		o.sessionMode = ""
		o.currentTopic = ""
		o.sessionContext = ""
		o.lastActivity = time.Now()
		o.mu.Unlock()
		return
	}

	currentTopic := fmt.Sprintf("元: %s / 改題: %s / 方式: %s", result.source.Title, result.plan.StoryTitle, result.plan.RewriteStyle)
	o.mu.Lock()
	o.currentTopic = currentTopic
	o.mu.Unlock()

	transcript := make([]string, 0, 12)
	intro := fmt.Sprintf("今夜の物語です。元になったのは『%s』。%s。", result.source.Title, result.plan.Hook)
	o.emitStoryParagraph(sessionID, intro)
	transcript = append(transcript, "mio: "+intro)

	titleLine := fmt.Sprintf("改題は『%s』。", result.plan.StoryTitle)
	o.emitStoryParagraph(sessionID, titleLine)
	transcript = append(transcript, "mio: "+titleLine)

	// 本文: 文を約150文字でまとめてViewerに1バブルで送る（TTS はチャンク単位）
	for _, para := range groupStoryIntoViewerParagraphs(result.storyText, 150) {
		o.emitStoryParagraph(sessionID, para)
		transcript = append(transcript, "mio: "+para)
	}

	closing := fmt.Sprintf("元の『%s』を下敷きにした、%sの物語でした。", result.source.Title, rewriteStyleLabel(result.plan.RewriteStyle))
	o.emitStoryParagraph(sessionID, closing)
	transcript = append(transcript, "mio: "+closing)

	endedAt := time.Now().In(jst)
	o.saveStorySummary(sessionID, result.source, result.plan, result.draftText, result.revisionNote, result.storyText, transcript, startedAt, endedAt)

	o.mu.Lock()
	o.chatActive = false
	o.sessionMode = ""
	o.currentTopic = ""
	o.sessionContext = ""
	o.lastActivity = time.Now()
	o.mu.Unlock()
}

// prepareStory はソース選択からアダプテーションプラン構築まで（Step 2〜6）を実行する。
// すべて決定論的でLLMを使用しない。
func (o *IdleChatOrchestrator) prepareStory(style string, excluded map[string]struct{}) StoryPrep {
	source := o.selectStorySourceExcluding(excluded)
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

func (o *IdleChatOrchestrator) retryStoryDraft(source StorySource, analysis StorySourceAnalysis, plan StoryRewritePlan, adaptation StoryAdaptationPlan, beatPlan StoryBeatPlan) (string, []string, error) {
	var lastErr error
	var retryLog []string
	for attempt := 0; attempt < storyStageMaxRetries; attempt++ {
		draftText, err := o.generateStoryDraft(source, analysis, plan, adaptation, beatPlan)
		if err == nil {
			return draftText, retryLog, nil
		}
		lastErr = err
		msg := fmt.Sprintf("[Step 7] retry %d/%d failed (%s): %v", attempt+1, storyStageMaxRetries, source.Title, err)
		log.Print(msg)
		retryLog = append(retryLog, msg)
		var be *beatFailError
		if errors.As(err, &be) && be.text != "" {
			retryLog = append(retryLog, be.text)
		}
	}
	finalErr := fmt.Errorf("[Step 7] %s: 3回リトライ失敗 — %w", source.Title, lastErr)
	retryLog = append(retryLog, fmt.Sprintf("✘ Step 7 失敗: %v", finalErr))
	return "", retryLog, finalErr
}

func (o *IdleChatOrchestrator) retryStoryRevision(source StorySource, analysis StorySourceAnalysis, plan StoryRewritePlan, adaptation StoryAdaptationPlan, beatPlan StoryBeatPlan, draftText string) (string, string, error) {
	var lastErr error
	for attempt := 0; attempt < storyStageMaxRetries; attempt++ {
		storyText, revisionNote, err := o.reviseStoryNarrative(source, analysis, plan, adaptation, beatPlan, draftText)
		if err == nil {
			return storyText, revisionNote, nil
		}
		lastErr = err
		log.Printf("[Step 8] retry %d/%d failed (%s): %v", attempt+1, storyStageMaxRetries, source.Title, err)
	}
	return "", "", fmt.Errorf("[Step 8] %s: 3回リトライ失敗 — %w", source.Title, lastErr)
}

func (o *IdleChatOrchestrator) emitStoryChunk(sessionID, content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	msg := domaintransport.NewMessage("mio", "user", sessionID, "", content)
	msg.Type = domaintransport.MessageTypeIdleChat
	o.memory.RecordMessage(msg)
	ttsDone := o.emitTimelineEvent(TimelineEvent{
		Type:      "idlechat.message",
		From:      "mio",
		To:        "user",
		Content:   content,
		SessionID: sessionID,
	})
	o.waitForTTSDone(ttsDone)
	o.waitBreak(speakerBreak)
}

// emitStoryParagraph は段落テキストを Viewer に1件送り、チャンク単位で TTS 再生する。
// Viewer は段落全体を1バブルで表示し、TTS はチャンク（90文字）単位で読み上げる。
func (o *IdleChatOrchestrator) emitStoryParagraph(sessionID, paragraph string) {
	paragraph = strings.TrimSpace(paragraph)
	if paragraph == "" {
		return
	}
	// memory に段落単位で記録
	msg := domaintransport.NewMessage("mio", "user", sessionID, "", paragraph)
	msg.Type = domaintransport.MessageTypeIdleChat
	o.memory.RecordMessage(msg)
	// Viewer に段落全体を1件送る（TTS なし）
	o.emitTimelineEvent(TimelineEvent{
		Type:      "idlechat.viewer",
		From:      "mio",
		To:        "user",
		Content:   paragraph,
		SessionID: sessionID,
	})
	// TTS に文節単位で送る（Viewer には表示しない）
	for _, sentence := range splitStorySentences(paragraph) {
		sentence = strings.TrimSpace(sentence)
		if sentence == "" {
			continue
		}
		ttsDone := o.emitTimelineEvent(TimelineEvent{
			Type:      "idlechat.tts",
			From:      "mio",
			To:        "user",
			Content:   sentence,
			SessionID: sessionID,
		})
		o.waitForTTSDone(ttsDone)
		o.waitBreak(speakerBreak)
	}
}

func (o *IdleChatOrchestrator) saveStorySummary(sessionID string, source StorySource, plan StoryRewritePlan, draftText, revisionNote, storyText string, transcript []string, startedAt, endedAt time.Time) {
	summary := fmt.Sprintf("元作品: %s\n改変方式: %s\n改題: %s\n導入: %s\n余韻: %s\nモチーフ: %s\n改稿: %s", source.Title, rewriteStyleLabel(plan.RewriteStyle), plan.StoryTitle, plan.Premise, plan.EndingFlavor, strings.Join(plan.MotifMap, " / "), revisionNote)
	record := SessionSummary{
		SessionID:         sessionID,
		Title:             fmt.Sprintf("%d月%d日の%sの物語まとめ", endedAt.Month(), endedAt.Day(), truncate(plan.StoryTitle, 24)),
		Topic:             fmt.Sprintf("元: %s / 改題: %s / 方式: %s", source.Title, plan.StoryTitle, plan.RewriteStyle),
		Strategy:          TopicStrategy(fmt.Sprintf("story:%s", plan.RewriteStyle)),
		Summary:           summary,
		SourceTitle:       source.Title,
		RewriteStyle:      plan.RewriteStyle,
		StoryTitle:        plan.StoryTitle,
		StoryText:         storyText,
		StoryDraftText:    draftText,
		StoryRevisionNote: revisionNote,
		StoryEndingFlavor: plan.EndingFlavor,
		StartedAt:         startedAt.Format(time.RFC3339),
		EndedAt:           endedAt.Format(time.RFC3339),
		Turns:             len(transcript),
		TopicProvider:     "shiro",
		SummaryProvider:   "shiro",
		Transcript:        append([]string(nil), transcript...),
	}
	o.mu.Lock()
	o.history = append(o.history, record)
	if len(o.history) > 200 {
		o.history = o.history[len(o.history)-200:]
	}
	store := o.topicStore
	o.mu.Unlock()
	if store != nil {
		if err := store.Append(record); err != nil {
			log.Printf("[Story] topic store append failed: %v", err)
		}
	}
	o.emitTimelineEvent(TimelineEvent{
		Type:      "idlechat.summary",
		From:      "shiro",
		To:        "story_summary",
		Content:   record.Title + "\n" + summary,
		SessionID: sessionID,
	})
}

func chooseStoryRewriteStyle(history []SessionSummary) string {
	candidates := append([]string(nil), storyRewriteStyles...)
	if len(history) > 0 {
		last := strings.TrimSpace(history[0].RewriteStyle)
		if last == "" {
			if s := strings.TrimSpace(string(history[0].Strategy)); strings.HasPrefix(s, "story:") {
				last = strings.TrimPrefix(s, "story:")
			}
		}
		if last != "" {
			filtered := candidates[:0]
			for _, c := range candidates {
				if c != last {
					filtered = append(filtered, c)
				}
			}
			if len(filtered) > 0 {
				candidates = filtered
			}
		}
	}
	return candidates[storyRandIntn(len(candidates))]
}

func (o *IdleChatOrchestrator) selectStorySource() StorySource {
	return o.selectStorySourceExcluding(nil)
}

func (o *IdleChatOrchestrator) selectStorySourceExcluding(excluded map[string]struct{}) StorySource {
	if forceID := os.Getenv("STORY_SOURCE"); forceID != "" {
		for _, src := range storyCorpus {
			if src.ID == forceID {
				return src
			}
		}
	}
	history := o.GetHistory(12)
	blocked := make(map[string]struct{}, 4)
	for _, item := range history {
		if strings.TrimSpace(item.SourceTitle) == "" {
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(string(item.Strategy)), "story:") {
			continue
		}
		blocked[item.SourceTitle] = struct{}{}
		if len(blocked) >= 4 {
			break
		}
	}
	pool := make([]StorySource, 0, len(storyCorpus))
	for _, item := range storyCorpus {
		if excluded != nil {
			if _, skip := excluded[item.Title]; skip {
				continue
			}
		}
		if _, ok := blocked[item.Title]; ok {
			continue
		}
		pool = append(pool, item)
	}
	if len(pool) == 0 {
		for _, item := range storyCorpus {
			if excluded != nil {
				if _, skip := excluded[item.Title]; skip {
					continue
				}
			}
			pool = append(pool, item)
		}
	}
	if len(pool) == 0 {
		pool = append(pool, storyCorpus...)
	}
	return pool[storyRandIntn(len(pool))]
}

var storySettingsByGenre = map[string][]string{
	"ノワール": {"深夜の港の倉庫街", "錆びた橋の下の路地", "雨の降る古い商店街"},
	"ホラー":  {"霧の深い山道", "廃屋の離れ", "人気のない神社の境内"},
	"コメディ": {"にぎやかな市場の片隅", "田舎の郵便局前", "裏通りの八百屋"},
	"ノーマル": {"川沿いの小さな町", "山あいの集落", "海辺の漁村"},
}

func buildStoryRewritePlan(source StorySource, analysis StorySourceAnalysis, style string) StoryRewritePlan {
	genre := storyGenres[storyRandIntn(len(storyGenres))]
	norm := normalizeStoryRewriteStyle(style)
	axis := storyTransformationAxis(source, norm)
	var scale string
	if norm == "scale_shift" {
		scale = storyScales[storyRandIntn(len(storyScales))]
	}
	settings := storySettingsByGenre[genre]
	setting := settings[storyRandIntn(len(settings))]
	coreMotifs := append([]string(nil), analysis.Skeleton.CanonicalMotifs...)
	return StoryRewritePlan{
		SourceTitle:  source.Title,
		RewriteStyle: norm,
		StoryTitle:   planStoryTitle(source, norm, genre, scale),
		Premise:      axis + "を、" + genre + "の文脈で描く。",
		Setting:      setting,
		Viewpoint:    planStoryViewpoint(norm),
		Tone:         planStoryTone(genre),
		Hook:         axis,
		EndingShape:  planStoryEndingShape(norm),
		EndingFlavor: planStoryEndingFlavor(norm),
		CoreMotifs:   coreMotifs,
		MotifMap:     defaultStoryMotifMap(norm, coreMotifs),
	}
}

func planStoryTitle(source StorySource, style, genre, scale string) string {
	switch style {
	case "role_shift":
		return genre + "版・" + source.Title
	case "view_shift":
		return source.Title + "のそばにいた人"
	case "value_shift":
		return source.Title + "の裏返し"
	case "inversion":
		return "もし" + source.Title + "が逆だったら"
	case "scale_shift":
		if scale == "極小" {
			return "小さな" + source.Title
		}
		return "大きな" + source.Title
	default:
		return "今の" + source.Title
	}
}

func planStoryViewpoint(style string) string {
	switch style {
	case "role_shift":
		return "対立役の近接一人称"
	case "view_shift":
		return "傍観者の近接三人称"
	case "value_shift":
		return "語り手の俯瞰"
	case "inversion":
		return "因果を知る者の三人称"
	case "scale_shift":
		return "外側から見た三人称"
	default:
		return "語り手の三人称"
	}
}

func planStoryTone(genre string) string {
	switch genre {
	case "ノワール":
		return "乾いた緊張"
	case "ホラー":
		return "息をひそめた不穏"
	case "コメディ":
		return "軽快な滑稽"
	default:
		return "生活圏の手触りを残す静かな短編"
	}
}

func planStoryEndingShape(style string) string {
	switch style {
	case "role_shift":
		return "力の構造が最後に反転する"
	case "view_shift":
		return "視点の差が静かに明かされる"
	case "value_shift":
		return "価値観の転倒が露呈する"
	case "inversion":
		return "因果が逆に着地する"
	case "scale_shift":
		return "スケールが変わることで別の真実が見える"
	default:
		return "静かな余韻で終わる"
	}
}

func planStoryEndingFlavor(style string) string {
	switch style {
	case "role_shift":
		return "構造の露呈"
	case "view_shift":
		return "立場の差"
	case "value_shift":
		return "喪失"
	case "inversion":
		return "皮肉"
	case "scale_shift":
		return "眩暈"
	default:
		return "余韻"
	}
}

func storyTransformationAxis(source StorySource, style string) string {
	spec, ok := storySpecForSource(source)
	if ok {
		if axis, found := spec.Twists[normalizeStoryRewriteStyle(style)]; found && strings.TrimSpace(axis) != "" {
			return axis
		}
	}
	switch normalizeStoryRewriteStyle(style) {
	case "view_shift":
		return fmt.Sprintf("『%s』を、主役のすぐ近くにいた人物の立場からの見え方の差", source.Title)
	case "value_shift":
		return fmt.Sprintf("『%s』の報いや救いが、別の価値観から見たときに逆転する構造", source.Title)
	case "inversion":
		return fmt.Sprintf("『%s』の因果や報いが逆だったら何が残るか", source.Title)
	case "scale_shift":
		return fmt.Sprintf("『%s』の力や出来事の及ぶ範囲が変わったとき、何が見えるか", source.Title)
	default:
		return fmt.Sprintf("『%s』の役割と従属の非対称が生む構造", source.Title)
	}
}

func (o *IdleChatOrchestrator) generateStoryDraft(source StorySource, analysis StorySourceAnalysis, plan StoryRewritePlan, adaptation StoryAdaptationPlan, beatPlan StoryBeatPlan) (string, error) {
	return o.generateStoryDraftByBeats(source, analysis, plan, adaptation, beatPlan)
}

func (o *IdleChatOrchestrator) generateStoryDraftByBeats(source StorySource, analysis StorySourceAnalysis, plan StoryRewritePlan, adaptation StoryAdaptationPlan, beatPlan StoryBeatPlan) (string, error) {
	openingSeed := storyOpeningSeed(source, plan)
	specs := storyBeatSpecs(beatPlan)
	paragraphs := make([]string, 0, len(specs))
	for i, spec := range specs {
		// 前ビートの情報を構造化して渡す:
		//   prevBeatContent      = プランの出来事記述（「何が起きたか」の要約として機能）
		//   prevBeatLastSentence = 生成テキストの末尾1文（次ビートの書き出し起点）
		// これにより LLM が前ビート全文を「続きの文脈」として使うのを防ぐ。
		// storyParagraphRepeatsContext の重複チェックは引き続き全文で行う。
		var prevFullText, prevBeatContent, prevBeatLastSentence string
		if len(paragraphs) > 0 {
			prevFullText = paragraphs[len(paragraphs)-1]
			prevBeatContent = specs[i-1].content
			prevBeatLastSentence = lastSentenceOf(prevFullText)
		}
		// この場面より後のビートの出来事記述（先走り防止用）
		// ラベル（"逸脱"等）より content（"機を織るの場面。〜"）の方が LLM が避けやすい
		forbiddenBeats := make([]string, 0, len(specs)-i-1)
		for _, fs := range specs[i+1:] {
			forbiddenBeats = append(forbiddenBeats, fs.content)
		}
		messages := buildBeatMessages(source, plan, adaptation, i, spec.label, spec.content, prevBeatContent, prevBeatLastSentence, forbiddenBeats, openingSeed)
		resp, err := o.providerForSpeaker("shiro").Generate(o.ctx, llm.GenerateRequest{
			Messages:    messages,
			MaxTokens:   300,
			Temperature: 0.6,
		})
		if err != nil {
			return "", fmt.Errorf("beat draft failed: %w", err)
		}
		paragraph := normalizeStoryNarrative(resp.Content)
		if paragraph == "" || storyHasOutlineLanguage(paragraph) || storyHasOverblownSetting(paragraph) || storyHasDistractingDigression(paragraph) {
			return "", &beatFailError{reason: fmt.Sprintf("beat %d: outline/overblown", i), text: paragraph}
		}
		if storyParagraphIsVerbatimCopy(source, paragraph) {
			return "", &beatFailError{reason: fmt.Sprintf("beat %d: verbatim copy", i), text: paragraph}
		}
		if storyParagraphRepeatsContext(prevFullText, paragraph) {
			return "", &beatFailError{reason: fmt.Sprintf("beat %d: repeats context", i), text: paragraph}
		}
		// 前ビートの最後の文をそのまま1文目にコピーしていないかチェック
		if prevBeatLastSentence != "" {
			paragraphFirstSentences := splitStorySentences(paragraph)
			if len(paragraphFirstSentences) > 0 && storySignature(paragraphFirstSentences[0]) == storySignature(prevBeatLastSentence) {
				return "", &beatFailError{reason: fmt.Sprintf("beat %d: copies prev last sentence", i), text: paragraph}
			}
		}
		if strings.Count(paragraph, "まるで") >= 3 {
			return "", &beatFailError{reason: fmt.Sprintf("beat %d: simile overuse", i), text: paragraph}
		}
		beatSentences := strings.Count(paragraph, "。") + strings.Count(paragraph, "！") + strings.Count(paragraph, "？")
		if beatSentences > 8 {
			return "", &beatFailError{reason: fmt.Sprintf("beat %d: too long (%d sentences)", i, beatSentences), text: paragraph}
		}
		paragraphs = append(paragraphs, paragraph)
	}
	story := strings.TrimSpace(strings.Join(paragraphs, "\n\n"))
	if !storyNarrativeLooksLikeProse(story) {
		return "", fmt.Errorf("full draft: prose check failed")
	}
	if !storySatisfiesSkeleton(story, analysis.Skeleton, adaptation) {
		return "", fmt.Errorf("full draft: skeleton check failed")
	}
	return story, nil
}


// lastSentenceOf は text の末尾の文を返す。
// 前ビートの「どこで終わったか」だけを次ビートの書き出し起点として渡すために使う。
func lastSentenceOf(text string) string {
	sentences := splitStorySentences(strings.TrimSpace(text))
	if len(sentences) == 0 {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(sentences[len(sentences)-1])
}

func storyParagraphRepeatsContext(context, paragraph string) bool {
	context = strings.TrimSpace(context)
	paragraph = strings.TrimSpace(paragraph)
	if context == "" || paragraph == "" {
		return false
	}
	contextSentences := splitStorySentences(context)
	paragraphSentences := splitStorySentences(paragraph)
	seen := make(map[string]struct{}, len(contextSentences))
	for _, sentence := range contextSentences {
		seen[storySignature(sentence)] = struct{}{}
	}
	repeats := 0
	for _, sentence := range paragraphSentences {
		if _, ok := seen[storySignature(sentence)]; ok {
			repeats++
		}
	}
	return repeats >= 2
}

func beatInstruction(index int, openingSeed string) string {
	if index == 0 {
		return "第1文は必ずこの文をそのまま使う: " + openingSeed
	}
	return "前の場面の直後から書き始める（前の場面の最後の文をそのまま繰り返さない。情景描写だけの文から始めない。登場人物の行動か対話で始める）"
}

// endingFlavorHint は EndingFlavor のラベルを「どう終わらせれば実現するか」の具体的指示に変換する。
// beat 3（着地）のプロンプトだけに付加し、LLM が余韻を正しく体現できるようにする。
func endingFlavorHint(flavor string) string {
	switch strings.TrimSpace(flavor) {
	case "構造の露呈":
		return "この場面で終わる: 勝者が何かを手に入れた瞬間、損をしている側の視点から同じ出来事が見えるようにする"
	case "皮肉":
		return "この場面で終わる: 勝者と思っていた側が実は損をしていたと分かる出来事を最後に置く"
	case "喪失":
		return "この場面で終わる: 何かを得たが同時に何かが失われたことを、人物の行動か言葉で示す"
	case "眩暈":
		return "この場面で終わる: 出来事のスケールが急に変わり、それまで読んでいた物語が別の大きさに見え始める一文で締める"
	case "立場の差":
		return "この場面で終わる: 同じ出来事が立場によって全く別の意味に見えることが分かる場面を最後に置く"
	case "余韻":
		return "この場面で終わる: 何かが静かに残る感触で締める。説明せず、出来事や仕草で示す"
	default:
		return fmt.Sprintf("この場面で終わる: %s が感じられる出来事か仕草で締める", flavor)
	}
}

func (o *IdleChatOrchestrator) reviseStoryNarrative(source StorySource, analysis StorySourceAnalysis, plan StoryRewritePlan, adaptation StoryAdaptationPlan, beatPlan StoryBeatPlan, draftText string) (string, string, error) {
	messages := buildRevisionMessages(source, analysis, plan, adaptation, beatPlan, draftText)
	resp, err := o.providerForSpeaker("shiro").Generate(o.ctx, llm.GenerateRequest{
		Messages:    messages,
		MaxTokens:   1800,
		Temperature: 0.5,
	})
	if err != nil {
		return "", "", err
	}
	revisionNote, story := parseStoryRevision(resp.Content)
	story = normalizeStoryNarrative(story)
	if story == "" {
		log.Printf("[Story] revision rejected (%s): empty response", source.Title)
		return "", "", fmt.Errorf("invalid revised story")
	}
	if !storyNarrativeLooksSettled(story, draftText, plan, beatPlan) {
		log.Printf("[Story] revision rejected (%s): settling check failed: %s", source.Title, storyLogSnippet(story))
		return "", "", fmt.Errorf("invalid revised story")
	}
	// For revision, skip the full prose check (which rejects まるで overuse etc.) — the draft already
	// passed prose checks. Only reject revision if it contains meta-language or outline artifacts.
	if storyHasOutlineLanguage(story) || storyHasMetaLeak(story) {
		log.Printf("[Story] revision rejected (%s): meta/outline leak: %s", source.Title, storyLogSnippet(story))
		return "", "", fmt.Errorf("invalid revised story")
	}
	if !storySatisfiesSkeleton(story, analysis.Skeleton, adaptation) {
		log.Printf("[Story] revision rejected (%s): skeleton check failed: %s", source.Title, storyLogSnippet(story))
		return "", "", fmt.Errorf("invalid revised story")
	}
	if revisionNote == "" {
		revisionNote = fallbackStoryRevisionNote(plan, beatPlan)
	}
	return story, revisionNote, nil
}

func buildRevisionMessages(source StorySource, _ StorySourceAnalysis, plan StoryRewritePlan, _ StoryAdaptationPlan, _ StoryBeatPlan, draftText string) []llm.Message {
	openingSeed := storyOpeningSeed(source, plan)
	axis := storyTransformationAxis(source, plan.RewriteStyle)
	return []llm.Message{
		{Role: "system", Content: "あなたは朗読短編の編集者です。"},
		{Role: "user", Content: fmt.Sprintf(`次の第1稿を、声に出して聞ける短編として整えてください。

改変の核: %s
余韻: %s

方針:
- 第1稿の内容と流れはそのまま使う
- 文と文のつながりが切れている箇所だけ補う
- 最後に「%s」が感じられるようにする
- 第1文は一字一句変えない: %s
- 文量は第1稿と同程度にする

第1稿:
%s

出力形式:
REVISION_NOTE:
（一行で: 何を直したか）
STORY:
（本文）`, axis, plan.EndingFlavor, plan.EndingFlavor, openingSeed, draftText)},
	}
}

func fallbackStoryBeatPlan(source StorySource, analysis StorySourceAnalysis, plan StoryRewritePlan) StoryBeatPlan {
	return StoryBeatPlan{
		Opening:   fmt.Sprintf("%s。%sが最初の違和感として立ち上がる。", plan.Hook, analysis.TabooOrRule),
		Deviation: fmt.Sprintf("%s。ここで%sが意外な意味に変わる。", plan.Premise, firstStoryMotifLabel(plan.MotifMap)),
		Reversal:  fmt.Sprintf("%s。その飛躍が、%sによって因果として結び直される。", plan.EndingShape, analysis.RewardAndPunish),
		Landing:   fmt.Sprintf("最後に残るのは%sだ。", plan.EndingFlavor),
	}
}

func defaultGroundedStorySetting(source StorySource) string {
	if source.Setting != "" {
		return source.Setting
	}
	return "現代の地方都市とその周辺"
}

func groundedStoryBeatPlan(source StorySource, analysis StorySourceAnalysis, plan StoryRewritePlan) StoryBeatPlan {
	labels := storyBeatLabels(analysis.Skeleton.RequiredBeats)
	opening := firstOrFallback(labels, 0, "導入")
	deviation := firstOrFallback(labels, 1, "逸脱")
	reversal := firstOrFallback(labels, 2, "反転")
	landing := firstOrFallback(labels, 3, "着地")

	return StoryBeatPlan{
		Opening:   fmt.Sprintf("%sの場面。舞台は%s。", opening, plan.Setting),
		Deviation: fmt.Sprintf("%sの場面。%sが動く。", deviation, joinSome(plan.CoreMotifs, 1)),
		Reversal:  fmt.Sprintf("%sの場面。%sで決着へ向かう。", reversal, joinSome(plan.CoreMotifs, 2)),
		Landing:   fmt.Sprintf("%sの場面。%sで閉じる。", landing, plan.EndingFlavor),
	}
}

func (p StoryRewritePlan) RewriteStyleLabel() string {
	return rewriteStyleLabel(p.RewriteStyle)
}

func firstOrFallback(items []string, idx int, fallback string) string {
	if idx >= 0 && idx < len(items) && strings.TrimSpace(items[idx]) != "" {
		return items[idx]
	}
	return fallback
}

func joinSome(items []string, max int) string {
	if len(items) == 0 {
		return "元話の手がかり"
	}
	if len(items) > max {
		items = items[:max]
	}
	return strings.Join(items, "と")
}

func storyOpeningSeed(source StorySource, plan StoryRewritePlan) string {
	if source.OpeningSeed != "" {
		return source.OpeningSeed
	}
	return fmt.Sprintf("%sは%sで足を止め、これから起こる出来事の気配を見つけた。", source.Title, defaultGroundedStorySetting(source))
}

func fallbackStoryNarrative(source StorySource, analysis StorySourceAnalysis, plan StoryRewritePlan, beatPlan StoryBeatPlan) string {
	return deterministicStoryDraft(source, analysis, plan, buildStoryAdaptationPlan(analysis.Skeleton, plan, beatPlan), beatPlan)
}

func repairStoryDraft(source StorySource, analysis StorySourceAnalysis, plan StoryRewritePlan, adaptation StoryAdaptationPlan, beatPlan StoryBeatPlan, draftText string) string {
	repaired := stripStoryMetaLeak(draftText)
	repaired = strings.ReplaceAll(repaired, "。。", "。")
	repaired = strings.TrimSpace(repaired)
	if repaired == "" {
		if fallback := deterministicStoryDraft(source, analysis, plan, adaptation, beatPlan); fallback != "" {
			return stripStoryMetaLeak(fallback)
		}
		return stripStoryMetaLeak(safeStoryRetelling(source, plan))
	}
	if !strings.Contains(repaired, firstToken(plan.EndingFlavor)) && strings.TrimSpace(beatPlan.Landing) != "" {
		if !strings.HasSuffix(repaired, "。") {
			repaired += "。"
		}
		repaired = strings.TrimSpace(repaired + " " + beatPlan.Landing)
	}
	if !storySatisfiesSkeleton(repaired, analysis.Skeleton, adaptation) {
		if fallback := deterministicStoryDraft(source, analysis, plan, adaptation, beatPlan); fallback != "" {
			return stripStoryMetaLeak(fallback)
		}
		return stripStoryMetaLeak(safeStoryRetelling(source, plan))
	}
	return repaired
}

func storyDraftMatchesSourceRetelling(source StorySource, draftText string) bool {
	// A verbatim copy is not a "good retelling" — it's just copying.
	if storyParagraphIsVerbatimCopy(source, draftText) {
		return false
	}
	draftText = normalizeStoryNarrative(draftText)
	sourceText := normalizeStoryNarrative(source.Text)
	if draftText == "" || sourceText == "" {
		return false
	}
	sentences := splitStorySentences(sourceText)
	hits := 0
	for i := 0; i < len(sentences) && i < 3; i++ {
		if strings.Contains(draftText, sentences[i]) {
			hits++
		}
	}
	return hits >= 2
}

// storyParagraphIsVerbatimCopy returns true if paragraph contains any complete
// sentence (≥15 runes) from source.Text or source.JuvenileText verbatim.
// Checks both texts since the beat prompt uses juvenile_text when available.
func storyParagraphIsVerbatimCopy(source StorySource, paragraph string) bool {
	if paragraph == "" {
		return false
	}
	for _, ref := range []string{source.Text, source.JuvenileText} {
		refText := normalizeStoryNarrative(ref)
		if refText == "" {
			continue
		}
		for _, sentence := range splitStorySentences(refText) {
			sentence = strings.TrimSpace(sentence)
			if utf8.RuneCountInString(sentence) < 15 {
				continue
			}
			if strings.Contains(paragraph, sentence) {
				return true
			}
		}
	}
	return false
}

func deterministicStoryDraft(source StorySource, analysis StorySourceAnalysis, plan StoryRewritePlan, adaptation StoryAdaptationPlan, beatPlan StoryBeatPlan) string {
	opening := storyOpeningSeed(source, plan)
	motif0 := storyMappedMotif(plan.MotifMap, 0, firstStoryMotifLabel(plan.MotifMap))
	motif1 := storyMappedMotif(plan.MotifMap, 1, motif0)
	motif2 := storyMappedMotif(plan.MotifMap, 2, motif1)
	baseSentences := splitStorySentences(normalizeStoryNarrative(source.Text))
	if len(baseSentences) == 0 {
		baseSentences = []string{opening}
	}
	paragraphs := []string{
		opening + " " + fmt.Sprintf("その場では、%sと%sの名がひそやかに広まり始めていた。", motif0, motif1),
		storyDeterministicParagraph(baseSentences, 1),
		storyDeterministicParagraph(baseSentences, 2),
		storyDeterministicParagraph(baseSentences, 3) + " " + fmt.Sprintf("あとに残ったのは、%sに近い静けさだった。", plan.EndingFlavor),
	}
	story := normalizeStoryNarrative(strings.Join(paragraphs, "\n\n"))
	if !storyNarrativeLooksLikeProse(story) {
		return ""
	}
	if !storySatisfiesSkeleton(story, analysis.Skeleton, adaptation) {
		story = normalizeStoryNarrative(story + "\n\n" + fmt.Sprintf("%s、%s、%sは順番どおりにそこへ現れた。", motif0, motif1, motif2))
		if !storySatisfiesSkeleton(story, analysis.Skeleton, adaptation) {
			return ""
		}
	}
	return story
}

func storyDeterministicParagraph(sentences []string, idx int) string {
	if idx < len(sentences) {
		return strings.TrimSpace(sentences[idx])
	}
	return strings.TrimSpace(sentences[len(sentences)-1])
}

func safeStoryRetelling(source StorySource, plan StoryRewritePlan) string {
	opening := storyOpeningSeed(source, plan)
	body := normalizeStoryNarrative(source.Text)
	if body == "" {
		return ""
	}
	return normalizeStoryNarrative(opening + "\n\n" + body + "\n\n" + fmt.Sprintf("そのあとに残ったのは、%sに近い静けさだった。", plan.EndingFlavor))
}

func storyMappedMotif(motifMap []string, idx int, fallback string) string {
	if idx >= 0 && idx < len(motifMap) {
		if token := firstToken(motifMap[idx]); token != "" {
			return token
		}
	}
	return fallback
}

func rewriteStyleLabel(style string) string {
	switch normalizeStoryRewriteStyle(style) {
	case "role_shift":
		return "役割転換"
	case "view_shift":
		return "視点変更"
	case "value_shift":
		return "価値反転"
	case "inversion":
		return "因果反転"
	case "scale_shift":
		return "規模変換"
	default:
		return style
	}
}

func normalizeStoryRewriteStyle(style string) string {
	switch strings.TrimSpace(style) {
	case "role_shift", "what_if", "if", "役割転換", "もしも転換":
		return "role_shift"
	case "view_shift", "視点変更":
		return "view_shift"
	case "value_shift", "価値反転":
		return "value_shift"
	default:
		return strings.TrimSpace(style)
	}
}

func normalizeStoryEndingFlavor(flavor string) string {
	switch strings.TrimSpace(flavor) {
	case "報い", "救い", "喪失", "皮肉":
		return strings.TrimSpace(flavor)
	default:
		return "余韻"
	}
}

func fallbackStorySetting(style string) string {
	switch strings.TrimSpace(style) {
	case "view_shift":
		return "同じ事件を横から見ている地域コミュニティ"
	case "value_shift":
		return "善意と損得が衝突する商店街"
	default:
		return "深夜の物流と生活が交差する町"
	}
}

func fallbackStoryViewpoint(style string) string {
	switch strings.TrimSpace(style) {
	case "view_shift":
		return "元の脇役の一人称"
	case "value_shift":
		return "正しさを信じていた当事者の一人称"
	default:
		return "役目を押しつけられた当事者"
	}
}

func analyzeStorySource(source StorySource) StorySourceAnalysis {
	skeleton := storySkeleton(source)
	return StorySourceAnalysis{
		CoreMotifs:          skeleton.CanonicalMotifs,
		RoleMap:             skeleton.RoleConstraints,
		TabooOrRule:         skeleton.TabooOrRule,
		RewardAndPunish:     skeleton.RewardPunishment,
		EmotionalAftertaste: skeleton.EmotionalAftertaste,
		Skeleton:            skeleton,
	}
}

func buildStoryAdaptationPlan(skeleton StorySkeleton, plan StoryRewritePlan, beatPlan StoryBeatPlan) StoryAdaptationPlan {
	beatMappings := []string{
		fmt.Sprintf("%s=>%s", labelOrBeatID(skeleton.RequiredBeats, 0), beatPlan.Opening),
		fmt.Sprintf("%s=>%s", labelOrBeatID(skeleton.RequiredBeats, 1), beatPlan.Deviation),
		fmt.Sprintf("%s=>%s", labelOrBeatID(skeleton.RequiredBeats, 2), beatPlan.Reversal),
		fmt.Sprintf("%s=>%s", labelOrBeatID(skeleton.RequiredBeats, 3), beatPlan.Landing),
	}
	return StoryAdaptationPlan{
		SkeletonID:      skeleton.ID,
		RewriteStyle:    plan.RewriteStyle,
		BeatMappings:    beatMappings,
		MotifMappings:   append([]string(nil), plan.MotifMap...),
		RoleRemap:       append([]string(nil), skeleton.RoleConstraints...),
		EndingFlavor:    plan.EndingFlavor,
		RecognitionCues: append([]string(nil), skeleton.RecognitionCues...),
	}
}

func labelOrBeatID(beats []StoryBeat, idx int) string {
	if idx >= 0 && idx < len(beats) && strings.TrimSpace(beats[idx].Label) != "" {
		return beats[idx].Label
	}
	switch idx {
	case 0:
		return "導入"
	case 1:
		return "逸脱"
	case 2:
		return "反転"
	default:
		return "着地"
	}
}

func storyBeatLabels(beats []StoryBeat) []string {
	out := make([]string, 0, len(beats))
	for _, beat := range beats {
		if strings.TrimSpace(beat.Label) == "" {
			continue
		}
		out = append(out, beat.Label)
	}
	return out
}

func parseStoryRevision(raw string) (string, string) {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	note := ""
	story := raw
	lines := strings.Split(raw, "\n")
	if len(lines) == 0 {
		return "", strings.TrimSpace(raw)
	}
	if strings.HasPrefix(strings.TrimSpace(lines[0]), "REVISION_NOTE:") {
		note = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[0]), "REVISION_NOTE:"))
		story = strings.Join(lines[1:], "\n")
		if strings.HasPrefix(strings.TrimSpace(story), "STORY:") {
			story = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(story), "STORY:"))
		}
	}
	return note, strings.TrimSpace(story)
}

func normalizeStoryNarrative(story string) string {
	story = strings.ReplaceAll(story, "\r\n", "\n")
	story = strings.ReplaceAll(story, "\r", "\n")
	story = strings.ReplaceAll(story, "REVISION_NOTE:", "")
	story = strings.ReplaceAll(story, "STORY:", "")
	lines := strings.Split(story, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(kept) > 0 && kept[len(kept)-1] != "" {
				kept = append(kept, "")
			}
			continue
		}
		if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "*   **") || strings.HasPrefix(line, "- ") {
			break
		}
		if strings.HasPrefix(line, "わかりました。") || strings.HasPrefix(line, "以下に、") || strings.HasPrefix(line, "いかがでしょうか") {
			continue
		}
		if strings.HasPrefix(line, "（余韻）") || strings.HasPrefix(line, "(余韻)") || strings.HasPrefix(line, "余韻:") {
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		kept = append(kept, line)
	}
	out := strings.TrimSpace(strings.Join(kept, "\n"))
	out = stripStoryMetaSentences(out)
	out = dedupeStoryParagraphs(out)
	out = dedupeStorySentences(out)
	for strings.Contains(out, "\n\n\n") {
		out = strings.ReplaceAll(out, "\n\n\n", "\n\n")
	}
	return out
}

func fallbackStoryRevisionNote(plan StoryRewritePlan, beatPlan StoryBeatPlan) string {
	return fmt.Sprintf("逸脱を残しつつ、%s から %s へ因果が通るよう整えた。", truncate(beatPlan.Deviation, 18), plan.EndingFlavor)
}

func storyNarrativeLooksSettled(story, draft string, plan StoryRewritePlan, beatPlan StoryBeatPlan) bool {
	if storyHasMetaLeak(story) {
		return false
	}
	// Story must end with a proper sentence terminator, not trail off mid-sentence.
	trimmed := strings.TrimSpace(story)
	if trimmed == "" {
		return false
	}
	last := []rune(trimmed)
	lastChar := last[len(last)-1]
	if lastChar != '。' && lastChar != '！' && lastChar != '？' && lastChar != '」' && lastChar != '』' {
		return false
	}
	return true
}

func storyNarrativeLooksLikeProse(story string) bool {
	story = strings.TrimSpace(story)
	if utf8.RuneCountInString(story) < 160 {
		log.Printf("[StoryCheck] prose fail: too short (%d runes)", utf8.RuneCountInString(story))
		return false
	}

	if storyHasOutlineLanguage(story) {
		log.Printf("[StoryCheck] prose fail: outline language")
		return false
	}
	if storyHasOverblownSetting(story) {
		log.Printf("[StoryCheck] prose fail: overblown setting")
		return false
	}
	if storyHasDistractingDigression(story) {
		log.Printf("[StoryCheck] prose fail: distracting digression")
		return false
	}
	sentenceCount := strings.Count(story, "。") + strings.Count(story, "！") + strings.Count(story, "？")
	if sentenceCount < 3 {
		log.Printf("[StoryCheck] prose fail: too few sentences (%d)", sentenceCount)
		return false
	}
	if sentenceCount > 32 {
		log.Printf("[StoryCheck] prose fail: too many sentences (%d)", sentenceCount)
		return false
	}
	return true
}

func storyHasOverblownSetting(story string) bool {
	patterns := []string{
		"AI開発部", "量子コンピューター", "未来テック", "2040", "2041", "2042", "2043", "2044", "2045",
		"巨大企業", "世界最大手", "社会の神経回路", "ご招待ありがとうございます", "会員限定リゾート", "観光地",
		"大規模言語モデル", "量子", "システム部門の地下室", "世界規模", "未来都市", "近未来",
		"SNS", "いいね", "観光客", "スマホ", "会員制", "保養施設", "トークン", "権限", "高層", "地下保守",
		"不動産会社", "株式会社", "開発計画", "プロジェクト", "ランキング", "評価システム", "商業施設", "アプリ",
	}
	for _, pattern := range patterns {
		if strings.Contains(story, pattern) {
			return true
		}
	}
	if strings.Count(story, "まるで") >= 9 {
		return true
	}
	head := story
	if utf8.RuneCountInString(head) > 60 {
		head = string([]rune(head)[:60])
	}
	if strings.Contains(head, "あなたは") {
		return true
	}
	return false
}

func storyHasDistractingDigression(story string) bool {
	patterns := []string{
		"幼い頃",
		"子どもの頃",
		"思い出した",
		"思い出す",
		"記憶のよう",
		"象徴している",
		"物語の一部だった",
		"結局のところ",
		"最も恐ろしい",
		"悪だった",
		"唯一無二の宝",
	}
	for _, pattern := range patterns {
		if strings.Contains(story, pattern) {
			return true
		}
	}
	return false
}



// splitStorySentences は物語テキストを文節単位に分割する。
// 禁則処理: 。！？ の直後に続く行頭禁則文字（」』）等）は前の文節に含める。
func splitStorySentences(story string) []string {
	runes := []rune(story)
	n := len(runes)
	var sentences []string
	start := 0
	for i := 0; i < n; i++ {
		switch runes[i] {
		case '。', '！', '？', '\n':
			end := i + 1
			// 直後の行頭禁則文字を前の文節に含める（禁則処理）
			for end < n && isStoryLineHeadForbidden(runes[end]) {
				end++
			}
			part := strings.TrimSpace(string(runes[start:end]))
			if part != "" {
				sentences = append(sentences, part)
			}
			start = end
			i = end - 1
		}
	}
	if tail := strings.TrimSpace(string(runes[start:])); tail != "" {
		sentences = append(sentences, tail)
	}
	return sentences
}

// isStoryLineHeadForbidden は行頭禁則文字かどうかを返す（日本語組版基準）。
func isStoryLineHeadForbidden(r rune) bool {
	switch r {
	case '、', '。', '！', '？', '」', '』', '）', ')', '…', '‥', '・', '：', '；', 'ー', '～', '〜':
		return true
	}
	return false
}

func storyHasOutlineLanguage(story string) bool {
	patterns := []string{
		"どうひねったか",
		"よく分からないけど",
		"物語の始まりを予感",
		"最初の違和感として立ち上がる",
		"意外な意味に変わる",
		"因果として結び直される",
		"最後に残るのは",
		"という感触だった",
		"導入:",
		"逸脱:",
		"反転:",
		"着地:",
		"要件:",
		"改稿方針:",
		"REVISION_NOTE:",
		"STORY:",
	}
	for _, pattern := range patterns {
		if strings.Contains(story, pattern) {
			return true
		}
	}
	return false
}

func stripStoryMetaSentences(story string) string {
	sentences := splitStorySentences(story)
	if len(sentences) == 0 {
		return strings.TrimSpace(story)
	}
	filtered := make([]string, 0, len(sentences))
	for _, sentence := range sentences {
		if strings.Contains(sentence, "どうひねったか") ||
			strings.Contains(sentence, "よく分からないけど") ||
			strings.Contains(sentence, "物語の始まりを予感") {
			continue
		}
		filtered = append(filtered, sentence)
	}
	return strings.TrimSpace(strings.Join(filtered, "\n"))
}

func dedupeStoryParagraphs(story string) string {
	parts := strings.Split(strings.TrimSpace(story), "\n\n")
	if len(parts) == 0 {
		return strings.TrimSpace(story)
	}
	seen := make(map[string]struct{}, len(parts))
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key := storySignature(part)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		kept = append(kept, part)
	}
	return strings.TrimSpace(strings.Join(kept, "\n\n"))
}

func dedupeStorySentences(story string) string {
	sentences := splitStorySentences(story)
	if len(sentences) == 0 {
		return strings.TrimSpace(story)
	}
	seen := make(map[string]int, len(sentences))
	kept := make([]string, 0, len(sentences))
	for _, sentence := range sentences {
		sentence = strings.TrimSpace(sentence)
		if sentence == "" {
			continue
		}
		key := storySignature(sentence)
		if seen[key] >= 1 {
			continue
		}
		seen[key]++
		kept = append(kept, sentence)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func storySignature(text string) string {
	replacer := strings.NewReplacer(
		" ", "", "　", "", "\n", "", "。", "", "、", "", "！", "", "？", "",
		"「", "", "」", "", "（", "", "）", "", "(", "", ")", "", "『", "", "』", "",
	)
	return replacer.Replace(strings.TrimSpace(text))
}

func storyLogSnippet(story string) string {
	story = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(story, "\n", " "), "\r", " "))
	runes := []rune(story)
	if len(runes) > 180 {
		return string(runes[:180]) + "..."
	}
	return story
}

func storyHasMetaLeak(story string) bool {
	patterns := []string{
		"元の『",
		"元作品",
		"禁じられていたのは",
		"ここではそれが別の形",
		"読後感だった",
		"改変方式",
		"必須モチーフ",
		"報酬と罰",
	}
	for _, pattern := range patterns {
		if strings.Contains(story, pattern) {
			return true
		}
	}
	return false
}

func stripStoryMetaLeak(story string) string {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(story, "\r\n", "\n"), "\r", "\n"), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if storyHasMetaLeak(line) {
			continue
		}
		kept = append(kept, line)
	}
	clean := strings.TrimSpace(strings.Join(kept, "\n"))
	for strings.Contains(clean, "。。") {
		clean = strings.ReplaceAll(clean, "。。", "。")
	}
	return clean
}

func storySatisfiesSkeleton(story string, skeleton StorySkeleton, adaptation StoryAdaptationPlan) bool {
	if strings.TrimSpace(story) == "" {
		return false
	}
	if !storyHasRecognitionCues(story, skeleton) {
		return false
	}
	return true
}

func storyHasRecognitionCues(story string, skeleton StorySkeleton) bool {
	if len(skeleton.RecognitionCues) == 0 {
		return true
	}
	hits := 0
	for _, cue := range skeleton.RecognitionCues {
		if cue != "" && strings.Contains(story, cue) {
			hits++
		}
	}
	need := 2
	if len(skeleton.RecognitionCues) < need {
		need = len(skeleton.RecognitionCues)
	}
	if need == 0 {
		return true
	}
	return hits >= need
}


func firstStoryMotifLabel(motifMap []string) string {
	if len(motifMap) == 0 {
		return "元話の核"
	}
	return firstToken(motifMap[0])
}

func firstToken(s string) string {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "=>") {
		parts := strings.SplitN(s, "=>", 2)
		s = strings.TrimSpace(parts[1])
	}
	for _, sep := range []string{"、", " ", "の", "と"} {
		if idx := strings.Index(s, sep); idx > 0 {
			return strings.TrimSpace(s[:idx])
		}
	}
	return s
}

func defaultStoryMotifMap(style string, motifs []string) []string {
	out := make([]string, 0, len(motifs))
	for _, motif := range motifs {
		out = append(out, motif+"=>"+transformMotif(style, motif))
	}
	return out
}

func transformMotif(style, motif string) string {
	style = normalizeStoryRewriteStyle(style)
	switch style {
	case "view_shift":
		switch motif {
		case "舌を切る":
			return "声を奪われた理由"
		case "小さいつづら":
			return "控えめな贈り物"
		case "大きいつづら":
			return "欲の大きい選択肢"
		case "玉手箱":
			return "開けるなと言われた包み"
		case "時間のずれ":
			return "待っていた側の空白"
		case "亀を助ける":
			return "見捨てずに庇った相手"
		}
	case "value_shift":
		switch motif {
		case "舌を切る":
			return "善意の名で発言権を奪う処置"
		case "小さいつづら":
			return "控えめだが自由のある謝礼"
		case "大きいつづら":
			return "豪華だが断れない支援"
		case "玉手箱":
			return "開けば借りを負う封筒"
		case "時間のずれ":
			return "戻った時に生まれる社会的な空白"
		case "亀を助ける":
			return "助けた後に責任まで背負う相手"
		}
	default:
		switch motif {
		case "舌を切る":
			return "言葉を奪う処分"
		case "小さいつづら":
			return "小さな箱"
		case "大きいつづら":
			return "大きな箱"
		case "玉手箱":
			return "禁を破る箱"
		case "時間のずれ":
			return "帰還後の時間差"
		case "亀を助ける":
			return "弱った相手を助ける"
		}
	}
	return motif
}

func storySkeleton(source StorySource) StorySkeleton {
	if spec, ok := storySpecForSource(source); ok {
		return spec.Skeleton
	}
	log.Printf("[Story] skeleton not found for source %q: spec missing in JSON", source.ID)
	return StorySkeleton{ID: source.ID, SourceTitle: source.Title}
}

func splitStoryNarration(text string, maxRunes int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if maxRunes <= 0 {
		maxRunes = storyChunkMaxRunes
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	var out []string
	for _, para := range strings.Split(text, "\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		for utf8.RuneCountInString(para) > maxRunes {
			idx := bestStorySplitIndex(para, maxRunes)
			head := strings.TrimSpace(para[:idx])
			if head != "" {
				out = append(out, head)
			}
			para = strings.TrimSpace(para[idx:])
		}
		if para != "" {
			// 閉じ括弧・句点だけの断片は前のチャンクにマージする
			if len(out) > 0 && utf8.RuneCountInString(para) < storyChunkMinRunes && isStoryClosingFragment(para) {
				out[len(out)-1] += para
			} else {
				out = append(out, para)
			}
		}
	}
	return out
}

func bestStorySplitIndex(s string, maxRunes int) int {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return len(s)
	}
	limit := maxRunes
	if limit < storyChunkMinRunes {
		limit = storyChunkMinRunes
	}
	best := -1
	for i := limit - 1; i >= storyChunkMinRunes-1 && i < len(runes); i-- {
		switch runes[i] {
		case '。', '！', '？', '!', '?':
			// 直後に閉じ括弧が続く場合は一緒に含める（「〜！」→「」だけ残らないように）
			end := i + 1
			for end < len(runes) && (runes[end] == '」' || runes[end] == '』' || runes[end] == ')' || runes[end] == '）') {
				end++
			}
			return len(string(runes[:end]))
		case '、', '，', ',', '」':
			if best < 0 {
				best = len(string(runes[:i+1]))
			}
		}
	}
	if best > 0 {
		return best
	}
	return len(string(runes[:maxRunes]))
}

// groupStoryIntoViewerParagraphs は物語テキストを文に分解し、Viewer 表示用の段落にまとめる。
// targetRunes 文字程度を1段落とし、文途中で切らない。LLMの改行形式に依存しない。
func groupStoryIntoViewerParagraphs(text string, targetRunes int) []string {
	sentences := splitStorySentences(strings.TrimSpace(text))
	var out []string
	var buf strings.Builder
	for _, s := range sentences {
		sLen := utf8.RuneCountInString(s)
		bufLen := utf8.RuneCountInString(buf.String())
		if buf.Len() > 0 && bufLen+sLen > targetRunes {
			out = append(out, strings.TrimSpace(buf.String()))
			buf.Reset()
		}
		buf.WriteString(s)
	}
	if buf.Len() > 0 {
		out = append(out, strings.TrimSpace(buf.String()))
	}
	return out
}

// isStoryClosingFragment は、閉じ括弧・句点のみで構成された断片かどうかを判定する。
// 「」だけ、』だけ、。だけ など、前のチャンクへのマージ対象。
func isStoryClosingFragment(s string) bool {
	for _, r := range s {
		switch r {
		case '」', '』', '）', ')', '。', '！', '？', '!', '?':
			// ok
		default:
			return false
		}
	}
	return true
}
