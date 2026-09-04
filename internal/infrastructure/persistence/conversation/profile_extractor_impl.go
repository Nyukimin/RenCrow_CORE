package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

// LLMProfileExtractor は LLM を使ってユーザープロファイルを抽出する
type LLMProfileExtractor struct {
	provider    llm.LLMProvider
	minTurns    int // 最低ターン数（デフォルト: 3）
	maxTokens   int
	temperature float64
}

// NewLLMProfileExtractor は新しい LLMProfileExtractor を作成
func NewLLMProfileExtractor(provider llm.LLMProvider) *LLMProfileExtractor {
	return &LLMProfileExtractor{
		provider: provider,
		minTurns: 3,
		// Extraction uses a logical CORE completion budget. Provider/model
		// context details stay behind the RenCrow_LLM boundary.
		maxTokens:   domainmemory.ProfilePromotionMaxTokens,
		temperature: 0.1,
	}
}

func (e *LLMProfileExtractor) WithMinimumUserMessages(minimum int) *LLMProfileExtractor {
	if minimum < 1 {
		minimum = 1
	}
	e.minTurns = minimum
	return e
}

var (
	evidenceTagPattern       = regexp.MustCompile(`<[^<>]{1,400}>`)
	evidenceCSSRulePattern   = regexp.MustCompile(`\.?[A-Za-z_-][\w-]*\s*\{[^{}]{0,400}\}`)
	evidenceShellLinePattern = regexp.MustCompile(`^\s*(?:\S+@\S+:\S*\s*[$#]|[$>›]\s)`)
	evidencePathLinePattern  = regexp.MustCompile(`^\s*/(?:home|usr|opt|srv|var|etc|Users|proc|tmp)/\S*\s*$`)
	evidenceBlankRunPattern  = regexp.MustCompile(`\n{3,}`)
	evidenceSpaceRunPattern  = regexp.MustCompile(`[ \t]{2,}`)
	// Windows/Unix の絶対path。build logは1行に数百個並び、token を食うだけでなく
	// JSON escape の連鎖を誘発して応答を max_tokens まで暴走させる。
	evidenceWindowsPathPattern = regexp.MustCompile(`[A-Za-z]:[\\/][^\s"'` + "`" + `]{4,}`)
	evidenceUnixPathPattern    = regexp.MustCompile(`/[\w.@+-]+(?:/[\w.@+-]+){3,}`)
	evidenceBackslashRunPatt   = regexp.MustCompile(`\\{2,}`)
	evidenceParagraphBreak     = regexp.MustCompile(`\n\s*\n`)
	// 貼り付けられた機械出力の目印。
	evidenceMachineOutputPattern = regexp.MustCompile(`[A-Za-z]:\\|Traceback|RuntimeError|Error Report|(?m)^\S+@\S+:\S*\s*[$#]|(?m)^PS [A-Z]:`)
)

const evidencePathPlaceholder = "<path>"

const profileExtractorRepairInstructionTemplate = `検証コード=%s。%s 元の会話だけを根拠に、説明文・Markdown・コードフェンスを付けず、preferencesは文字列値だけのJSON object、factsは文字列だけのJSON arrayとし、キーをpreferencesとfactsだけにした単一JSON objectを返してください。各文字列は%d文字以内の一文、preferencesとfactsを合わせて最大%d件にしてください。空の場合も{"preferences":{},"facts":[]}を返してください。`

func profileExtractorCandidateLimit(candidateLimit int) int {
	if candidateLimit < 1 {
		return 1
	}
	if candidateLimit > domainmemory.ProfilePromotionPerGroupCandidateLimit {
		return domainmemory.ProfilePromotionPerGroupCandidateLimit
	}
	return candidateLimit
}

func profileExtractorRepairValidationCode(code profileExtractorValidationCode) profileExtractorValidationCode {
	switch code {
	case profileValidationResponseTooLarge,
		profileValidationJSONObject,
		profileValidationSchemaFields,
		profileValidationPreferencesType,
		profileValidationFactsType,
		profileValidationCandidateLimit,
		profileValidationPreferenceKey,
		profileValidationPreferenceValue,
		profileValidationPreferenceType,
		profileValidationFactValue,
		profileValidationInvalidJSON:
		return code
	default:
		return profileValidationInvalidJSON
	}
}

func profileExtractorRepairCorrection(code profileExtractorValidationCode) string {
	switch profileExtractorRepairValidationCode(code) {
	case profileValidationResponseTooLarge:
		return "応答を短くしてください。"
	case profileValidationJSONObject:
		return "応答全体をJSON objectにしてください。"
	case profileValidationSchemaFields:
		return "キーをpreferencesとfactsだけにしてください。"
	case profileValidationPreferencesType:
		return "preferencesを文字列値だけのJSON objectにしてください。"
	case profileValidationFactsType:
		return "factsを文字列だけのJSON arrayにしてください。"
	case profileValidationCandidateLimit:
		return "候補数を減らしてください。"
	case profileValidationPreferenceKey:
		return "preferencesのキーを有効な文字列にしてください。"
	case profileValidationPreferenceValue:
		return "preferencesの値を短い文字列にしてください。"
	case profileValidationPreferenceType:
		return "preferencesの値を文字列にしてください。"
	case profileValidationFactValue:
		return "factsの各値を短い文字列にしてください。"
	case profileValidationInvalidJSON:
		return "JSONの構文を正してください。"
	default:
		return "JSONの構文を正してください。"
	}
}

func profileExtractorRepairInstruction(code profileExtractorValidationCode) string {
	code = profileExtractorRepairValidationCode(code)
	return fmt.Sprintf(
		profileExtractorRepairInstructionTemplate,
		code,
		profileExtractorRepairCorrection(code),
		domainmemory.ProfilePromotionRepairStringMax,
		domainmemory.ProfilePromotionRepairCandidateLimit,
	)
}

type profileExtractorValidationCode string

const (
	profileValidationResponseTooLarge profileExtractorValidationCode = "response_too_large"
	profileValidationJSONObject       profileExtractorValidationCode = "json_object"
	profileValidationSchemaFields     profileExtractorValidationCode = "schema_fields"
	profileValidationPreferencesType  profileExtractorValidationCode = "preferences_type"
	profileValidationFactsType        profileExtractorValidationCode = "facts_type"
	profileValidationCandidateLimit   profileExtractorValidationCode = "candidate_limit"
	profileValidationPreferenceKey    profileExtractorValidationCode = "preference_key"
	profileValidationPreferenceValue  profileExtractorValidationCode = "preference_value"
	profileValidationPreferenceType   profileExtractorValidationCode = "preference_type"
	profileValidationFactValue        profileExtractorValidationCode = "fact_value"
	profileValidationInvalidJSON      profileExtractorValidationCode = "invalid_json"
)

// profileExtractorValidationError deliberately carries only a fixed category.
// It must never retain a provider response, field value, key, or array index.
type profileExtractorValidationError struct {
	code profileExtractorValidationCode
}

func (e *profileExtractorValidationError) Error() string {
	return "profile extractor invalid response"
}

func newProfileExtractorValidationError(code profileExtractorValidationCode) error {
	return &profileExtractorValidationError{code: code}
}

func profileExtractorValidationCodeOf(err error) profileExtractorValidationCode {
	var validationErr *profileExtractorValidationError
	if errors.As(err, &validationErr) && validationErr != nil {
		switch validationErr.code {
		case profileValidationResponseTooLarge,
			profileValidationJSONObject,
			profileValidationSchemaFields,
			profileValidationPreferencesType,
			profileValidationFactsType,
			profileValidationCandidateLimit,
			profileValidationPreferenceKey,
			profileValidationPreferenceValue,
			profileValidationPreferenceType,
			profileValidationFactValue,
			profileValidationInvalidJSON:
			return validationErr.code
		}
	}
	return profileValidationInvalidJSON
}

// profileExtractorValidationCategory maps decoder failures to an allowlisted
// operator category. Unknown errors fail closed to invalid_json without
// inspecting or exposing their text.
func profileExtractorValidationCategory(err error) string {
	return string(profileExtractorValidationCodeOf(err))
}

func logProfileExtractorInvalid(err error) {
	log.Printf("[ProfileExtractor] profile_extractor_invalid category=%s", profileExtractorValidationCategory(err))
}

// sanitizeEvidenceText removes the machine-generated noise that ChatGPT-imported
// turns carry: pasted markup dumps, CSS rules, shell prompt lines, and bare path
// listings. None of it states anything about the user, and it crowds out the
// prose that does. Markup is only stripped when the turn reads as a dump, so a
// turn that discusses HTML keeps its tags.
func sanitizeEvidenceText(text string) string {
	cleaned := text
	if evidenceTagDensityPercent(text) >= domainmemory.ProfilePromotionEvidenceTagDensityPercent {
		cleaned = evidenceTagPattern.ReplaceAllString(cleaned, " ")
		cleaned = evidenceCSSRulePattern.ReplaceAllString(cleaned, " ")
	}
	// path と backslash を先に潰す。どちらも本人について何も述べないうえ、
	// 抽出モデルを escape の反復ループへ引き込む。
	cleaned = evidenceWindowsPathPattern.ReplaceAllString(cleaned, evidencePathPlaceholder)
	cleaned = evidenceUnixPathPattern.ReplaceAllString(cleaned, evidencePathPlaceholder)
	cleaned = evidenceBackslashRunPatt.ReplaceAllString(cleaned, `\`)
	lines := strings.Split(cleaned, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if evidenceShellLinePattern.MatchString(line) || evidencePathLinePattern.MatchString(line) {
			continue
		}
		kept = append(kept, line)
	}
	cleaned = strings.Join(kept, "\n")
	cleaned = evidenceSpaceRunPattern.ReplaceAllString(cleaned, " ")
	cleaned = evidenceBlankRunPattern.ReplaceAllString(cleaned, "\n\n")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		// 除去し切った場合は元の先頭だけ残し、証跡を空にしない。
		return strings.TrimSpace(headRunes(text, 200))
	}
	return cleaned
}

func evidenceTagDensityPercent(text string) int {
	total := utf8.RuneCountInString(text)
	if total == 0 {
		return 0
	}
	inside := 0
	for _, match := range evidenceTagPattern.FindAllString(text, -1) {
		inside += utf8.RuneCountInString(match)
	}
	return inside * 100 / total
}

func headRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// splitEvidenceTurn separates the user's own words from material they pasted in.
// A ChatGPT turn that carries a quote is written as a short lead-in followed by
// a large block: "以下にURL入ってると思う" then a Confluence dump, "どう思う？"
// then an article, "ちょっと書いてみたよ" then a draft. The lead-in is what the
// user said; the block is what the conversation was about. Only the lead-in may
// become a fact about the user, so the two are returned apart.
func splitEvidenceTurn(message string) (lead string, material string) {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return "", ""
	}
	parts := evidenceParagraphBreak.Split(trimmed, 2)
	if len(parts) == 2 {
		head := strings.TrimSpace(parts[0])
		body := strings.TrimSpace(parts[1])
		if utf8.RuneCountInString(head) <= domainmemory.ProfilePromotionMaterialLeadMax &&
			utf8.RuneCountInString(body) >= domainmemory.ProfilePromotionMaterialBodyMin {
			return head, body
		}
	}
	// 空行のない巨大な貼り付け（build log や1行のダンプ）は全体が資料。
	if utf8.RuneCountInString(trimmed) >= domainmemory.ProfilePromotionMaterialBodyMin &&
		looksLikeMachineOutput(trimmed) {
		return "", trimmed
	}
	return trimmed, ""
}

func looksLikeMachineOutput(text string) bool {
	return evidenceMachineOutputPattern.MatchString(text) ||
		len(evidenceTagPattern.FindAllString(text, 21)) > 20
}

// buildMaterialDigest turns the pasted blocks into a short, clearly labelled
// excerpt used only to name the topic. Material is never expanded to fill the
// evidence budget, so a quoted article cannot crowd out what the user said.
func buildMaterialDigest(materials []string) string {
	if len(materials) == 0 {
		return ""
	}
	remaining := domainmemory.ProfilePromotionMaterialDigestMax
	excerpts := make([]string, 0, len(materials))
	seen := map[string]struct{}{}
	for _, material := range materials {
		cleaned := sanitizeEvidenceText(material)
		if cleaned == "" {
			continue
		}
		excerpt := headRunes(cleaned, domainmemory.ProfilePromotionMaterialExcerptMax)
		key := strings.Join(strings.Fields(excerpt), " ")
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		linePrefix := "- "
		separator := 0
		if len(excerpts) > 0 {
			separator = 1
		}
		available := remaining - utf8.RuneCountInString(linePrefix) - separator
		if available <= 0 {
			break
		}
		excerpt = headRunes(excerpt, available)
		line := linePrefix + strings.ReplaceAll(excerpt, "\n", " ")
		size := utf8.RuneCountInString(line) + separator
		if size <= separator {
			break
		}
		excerpts = append(excerpts, line)
		remaining -= size
		if remaining <= 0 {
			break
		}
	}
	if len(excerpts) == 0 {
		return ""
	}
	return strings.Join(excerpts, "\n")
}

// buildExistingProfileContext renders the already bounded owner projection in
// stable order. A line is appended only when the complete line fits, so the
// context never cuts a preference or fact in the middle.
func buildExistingProfileContext(existing domconv.UserProfile) string {
	if len(existing.Preferences) == 0 && len(existing.Facts) == 0 {
		return ""
	}
	const header = "既知情報:\n"
	contextText := header
	appendLine := func(line string) bool {
		candidate := contextText + line
		if utf8.RuneCountInString(candidate) > domainmemory.ProfilePromotionExistingContextMax {
			return false
		}
		contextText = candidate
		return true
	}
	preferenceKeys := make([]string, 0, len(existing.Preferences))
	for key := range existing.Preferences {
		preferenceKeys = append(preferenceKeys, key)
	}
	sort.Strings(preferenceKeys)
	for _, key := range preferenceKeys {
		if !appendLine(fmt.Sprintf("- %s: %s\n", key, existing.Preferences[key])) {
			return contextText
		}
	}
	for _, fact := range existing.Facts {
		if !appendLine(fmt.Sprintf("- %s\n", fact)) {
			return contextText
		}
	}
	return contextText
}

// splitEvidenceSegments cuts one turn at meaning boundaries so a chunk never
// ends mid-thought: blank-line paragraphs first, then sentence ends, and a hard
// cut only for a single sentence that is itself longer than the budget.
func splitEvidenceSegments(text string, budget int) []string {
	segments := []string{}
	for _, paragraph := range strings.Split(text, "\n\n") {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			continue
		}
		if utf8.RuneCountInString(paragraph) <= budget {
			segments = append(segments, paragraph)
			continue
		}
		for _, sentence := range splitSentences(paragraph) {
			if utf8.RuneCountInString(sentence) <= budget {
				segments = append(segments, sentence)
				continue
			}
			runes := []rune(sentence)
			for start := 0; start < len(runes); start += budget {
				end := start + budget
				if end > len(runes) {
					end = len(runes)
				}
				segments = append(segments, string(runes[start:end]))
			}
		}
	}
	return segments
}

func splitSentences(paragraph string) []string {
	sentences := []string{}
	current := strings.Builder{}
	for _, r := range paragraph {
		current.WriteRune(r)
		switch r {
		case '。', '！', '？', '\n', '!', '?':
			if strings.TrimSpace(current.String()) != "" {
				sentences = append(sentences, strings.TrimSpace(current.String()))
			}
			current.Reset()
		}
	}
	if strings.TrimSpace(current.String()) != "" {
		sentences = append(sentences, strings.TrimSpace(current.String()))
	}
	return sentences
}

// planEvidenceGroups packs the sanitized turns into extraction requests. Each
// group stays inside the block budget, segments are never split across groups,
// and the count of dropped runes is returned so the caller can report what a
// pathological batch could not cover instead of hiding it.
func planEvidenceGroups(messages []string) (groups []string, droppedRunes int) {
	budget := domainmemory.ProfilePromotionEvidenceBlockMax
	segments := []string{}
	for _, message := range messages {
		cleaned := sanitizeEvidenceText(message)
		if cleaned == "" {
			continue
		}
		segments = append(segments, splitEvidenceSegments(cleaned, budget)...)
	}
	// Canvas 編集などで同じ本文が複数回 evidence に載る。段落単位で重複を落とし、
	// 同じ文章のために group 予算を二重に使わないようにする。
	seen := map[string]struct{}{}
	deduped := make([]string, 0, len(segments))
	for _, segment := range segments {
		key := strings.Join(strings.Fields(segment), " ")
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, segment)
	}
	segments = deduped

	current := []string{}
	currentRunes := 0
	flush := func() {
		if len(current) > 0 {
			groups = append(groups, strings.Join(current, "\n"))
			current = nil
			currentRunes = 0
		}
	}
	for _, segment := range segments {
		size := utf8.RuneCountInString(segment)
		separator := 0
		if len(current) > 0 {
			separator = 1 // Join が挟む改行も budget に数える。
		}
		if currentRunes > 0 && currentRunes+separator+size > budget {
			flush()
			separator = 0
		}
		if len(groups) >= domainmemory.ProfilePromotionEvidenceGroupMax {
			droppedRunes += size
			continue
		}
		current = append(current, segment)
		currentRunes += separator + size
	}
	if len(groups) < domainmemory.ProfilePromotionEvidenceGroupMax {
		flush()
	} else {
		for _, segment := range current {
			droppedRunes += utf8.RuneCountInString(segment)
		}
	}
	return groups, droppedRunes
}

// mergeProfileExtractionResults unions the per-group results. Preferences keep
// the first value seen for a key and facts are deduplicated case-insensitively,
// so a statement repeated across chunk boundaries is promoted once.
func mergeProfileExtractionResults(results []*domconv.ProfileExtractionResult, limit int) *domconv.ProfileExtractionResult {
	merged := &domconv.ProfileExtractionResult{NewPreferences: map[string]string{}}
	seenFacts := map[string]struct{}{}
	count := 0
	for _, result := range results {
		if result == nil {
			continue
		}
		keys := make([]string, 0, len(result.NewPreferences))
		for key := range result.NewPreferences {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if count >= limit {
				return merged
			}
			if _, exists := merged.NewPreferences[key]; exists {
				continue
			}
			merged.NewPreferences[key] = result.NewPreferences[key]
			count++
		}
		for _, fact := range result.NewFacts {
			if count >= limit {
				return merged
			}
			normalized := strings.ToLower(strings.TrimSpace(fact))
			if normalized == "" {
				continue
			}
			if _, exists := seenFacts[normalized]; exists {
				continue
			}
			seenFacts[normalized] = struct{}{}
			merged.NewFacts = append(merged.NewFacts, fact)
			count++
		}
	}
	return merged
}

// Extract はスレッド内の会話からユーザープロファイルを抽出する
func (e *LLMProfileExtractor) Extract(ctx context.Context, thread *domconv.Thread, existing domconv.UserProfile) (*domconv.ProfileExtractionResult, error) {
	if thread == nil || len(thread.Turns) < e.minTurns {
		return &domconv.ProfileExtractionResult{}, nil
	}

	// ユーザーメッセージを収集する。
	var userMessages []string
	for _, turn := range thread.Turns {
		if turn.Speaker == domconv.SpeakerUser {
			userMessages = append(userMessages, turn.Msg)
		}
	}
	if len(userMessages) < e.minTurns {
		return &domconv.ProfileExtractionResult{}, nil
	}
	// 本人の発話と、貼り付けられた資料を分ける。事実になれるのは発話だけで、
	// 資料は「何について話していたか」を示すテーマとしてのみ渡す。
	leads := make([]string, 0, len(userMessages))
	materials := make([]string, 0, len(userMessages))
	for _, message := range userMessages {
		lead, material := splitEvidenceTurn(message)
		if lead != "" {
			leads = append(leads, lead)
		}
		if material != "" {
			materials = append(materials, material)
		}
	}
	materialDigest := buildMaterialDigest(materials)
	// 発話は切り落とさず、意味の切れ目で分割して複数回の抽出に回す。
	groups, droppedRunes := planEvidenceGroups(leads)
	if len(groups) == 0 {
		if materialDigest == "" {
			return &domconv.ProfileExtractionResult{}, nil
		}
		// 発話が無く資料だけのターンでも、テーマは拾う。
		groups = []string{""}
	}
	if droppedRunes > 0 {
		log.Printf(
			"[ProfileExtractor] evidence exceeded %d groups: thread=%s dropped_runes=%d",
			domainmemory.ProfilePromotionEvidenceGroupMax, thread.ID, droppedRunes,
		)
	}

	// 既知情報テキスト。serviceがbounded owner projectionだけを渡す。
	existingText := buildExistingProfileContext(existing)

	// group ごとに抽出し、結果を束ねる。1 group あたりの上限を配ることで、
	// 後半の chunk が上限に押し出されて捨てられるのを防ぐ。
	perGroupLimit := domainmemory.ProfilePromotionRawCandidateLimit / len(groups)
	if perGroupLimit < 1 {
		perGroupLimit = 1
	}
	if perGroupLimit > domainmemory.ProfilePromotionPerGroupCandidateLimit {
		perGroupLimit = domainmemory.ProfilePromotionPerGroupCandidateLimit
	}
	results := make([]*domconv.ProfileExtractionResult, 0, len(groups))
	// One repair is allowed for the whole Extract call, even when evidence was
	// split into multiple groups. This keeps the background retry budget fixed.
	repairUsed := false
	for index, group := range groups {
		result, err := e.extractGroup(ctx, thread, existingText, group, materialDigest, perGroupLimit, &repairUsed)
		if err != nil {
			return nil, fmt.Errorf("profile extractor group %d/%d failed: %w", index+1, len(groups), err)
		}
		results = append(results, result)
	}

	return mergeProfileExtractionResults(results, domainmemory.ProfilePromotionRawCandidateLimit), nil
}

func boundCompleteLines(text string, maxRunes int) string {
	if maxRunes <= 0 || text == "" {
		return ""
	}
	bounded := strings.Builder{}
	for _, line := range strings.SplitAfter(text, "\n") {
		candidate := bounded.String() + line
		if utf8.RuneCountInString(candidate) > maxRunes {
			break
		}
		bounded.WriteString(line)
	}
	return bounded.String()
}

func buildProfileExtractionPrompt(candidateLimit int, existingText, evidence, materialDigest string) string {
	candidateLimit = profileExtractorCandidateLimit(candidateLimit)
	evidence = headRunes(evidence, domainmemory.ProfilePromotionEvidenceBlockMax)
	existingText = boundCompleteLines(existingText, domainmemory.ProfilePromotionExistingContextMax)
	materialDigest = boundCompleteLines(materialDigest, domainmemory.ProfilePromotionMaterialDigestMax)
	evidenceSection := "会話:\n(このバッチにユーザー自身の発言はありません)"
	if strings.TrimSpace(evidence) != "" {
		evidenceSection = "会話（ユーザー本人の発言）:\n" + evidence
	}
	materialSection := ""
	if strings.TrimSpace(materialDigest) != "" {
		materialSection = "\n\n参照資料（ユーザーが貼り付けた資料の冒頭。ユーザーの主張でも体験でもありません）:\n" + materialDigest
	}
	return fmt.Sprintf(`以下の会話からユーザーに関する新しい情報を抽出してください。
既知情報と重複するものは除外してください。
JSON形式で出力してください。
preferences の各キーと値、facts の各要素は必ず JSON の文字列（string）で返してください。数値、オブジェクト、配列、真偽値、null は使わないでください。
preferences と facts を合わせて最大%d件までにしてください。特に重要なものだけを選んでください。
事実として書いてよいのは「ユーザー本人の発言」から読み取れることだけです。
参照資料が付いている場合、その中身をユーザーの事実・意見・経験として書かないでください。
資料からは「ユーザーが何について話していたか」というテーマを一文で捉え、
"〜について相談していた"、"〜を読んで意見を求めていた" のような形だけを書いてください。
小説・記事・ログの登場人物、価格、エラー内容などを、ユーザー自身の事実にしてはいけません。
会話は長い本文の一部分であることがあります。その場合も、その範囲から読み取れることだけを書いてください。
各文字列は200文字以内の一文とし、文字列の中に改行を入れないでください。
JSONオブジェクトの前後に説明文、マークダウン、コードフェンスを付けないでください。

%s

%s%s

出力形式:
{"preferences": {"カテゴリ": "値"}, "facts": ["事実1", "事実2"]}

新しい情報がない場合は空のJSONを返してください:
{"preferences": {}, "facts": []}`,
		candidateLimit,
		existingText,
		evidenceSection,
		materialSection,
	)
}

func (e *LLMProfileExtractor) extractGroup(
	ctx context.Context,
	thread *domconv.Thread,
	existingText string,
	evidence string,
	materialDigest string,
	candidateLimit int,
	repairUsed *bool,
) (*domconv.ProfileExtractionResult, error) {
	if e == nil || e.provider == nil {
		return nil, domconv.NewProfileExtractionUnavailableError(errors.New("profile extractor provider is not configured"))
	}
	candidateLimit = profileExtractorCandidateLimit(candidateLimit)
	prompt := buildProfileExtractionPrompt(candidateLimit, existingText, evidence, materialDigest)

	req := llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "user", Content: prompt},
		},
		MaxTokens:       e.maxTokens,
		Temperature:     e.temperature,
		ResponseFormat:  llm.ResponseFormatJSONObject,
		ReasoningEffort: llm.ReasoningEffortLow,
	}

	requestCtx := llm.WithExecutionObservation(ctx, llm.ExecutionObservation{
		SessionID: thread.SessionID, Initiator: "shiro", Caller: "memory.profile_promotion", Purpose: "extract_profile_candidates",
	})
	resp, err := e.provider.Generate(requestCtx, req)
	if err != nil {
		log.Printf("[ProfileExtractor] LLM call failed: profile_extractor_unavailable")
		return nil, domconv.NewProfileExtractionUnavailableError(err)
	}

	// JSONはresponse全体でなければならない。prefix/suffixからJSONを抜き出さない。
	result, err := decodeProfileExtractionResultWithLimit(resp.Content, candidateLimit)
	if err == nil {
		return result, nil
	}
	logProfileExtractorInvalid(err)
	validationCode := profileExtractorValidationCodeOf(err)
	invalidErr := domconv.NewProfileExtractionInvalidError(err)
	if repairUsed == nil || *repairUsed {
		return nil, invalidErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	*repairUsed = true

	repairReq := req
	repairReq.MaxTokens = domainmemory.ProfilePromotionRepairMaxTokens
	repairReq.Messages = append([]llm.Message(nil), req.Messages...)
	repairReq.Messages[0].Content = buildProfileExtractionPrompt(
		domainmemory.ProfilePromotionRepairCandidateLimit,
		existingText,
		evidence,
		materialDigest,
	)
	repairReq.Messages = append(repairReq.Messages, llm.Message{
		Role:    "user",
		Content: profileExtractorRepairInstruction(validationCode),
	})
	repairResp, repairErr := e.provider.Generate(requestCtx, repairReq)
	if repairErr != nil {
		log.Printf("[ProfileExtractor] repair LLM call failed: profile_extractor_unavailable")
		return nil, domconv.NewProfileExtractionUnavailableError(repairErr)
	}
	result, repairValidationErr := decodeProfileExtractionResultWithStringLimit(
		repairResp.Content,
		domainmemory.ProfilePromotionRepairCandidateLimit,
		domainmemory.ProfilePromotionRepairStringMax,
	)
	if repairValidationErr != nil {
		logProfileExtractorInvalid(repairValidationErr)
		return nil, domconv.NewProfileExtractionInvalidError(repairValidationErr)
	}

	return result, nil
}

type profileExtractionPayload struct {
	Preferences map[string]json.RawMessage `json:"preferences"`
	Facts       []string                   `json:"facts"`
}

func decodeProfileExtractionResult(content string) (*domconv.ProfileExtractionResult, error) {
	return decodeProfileExtractionResultWithLimit(content, domainmemory.ProfilePromotionRawCandidateLimit)
}

func decodeProfileExtractionResultWithLimit(content string, candidateLimit int) (*domconv.ProfileExtractionResult, error) {
	return decodeProfileExtractionResultWithStringLimit(content, candidateLimit, 0)
}

func decodeProfileExtractionResultWithStringLimit(content string, candidateLimit, stringLimit int) (*domconv.ProfileExtractionResult, error) {
	if candidateLimit <= 0 || candidateLimit > domainmemory.ProfilePromotionRawCandidateLimit {
		candidateLimit = domainmemory.ProfilePromotionRawCandidateLimit
	}
	preferenceKeyMax := domainmemory.ProfilePromotionPreferenceKeyMax
	preferenceValueMax := domainmemory.ProfilePromotionPreferenceValueMax
	factValueMax := domainmemory.ProfilePromotionProjectionStatementMax
	if stringLimit > 0 {
		if stringLimit < preferenceKeyMax {
			preferenceKeyMax = stringLimit
		}
		if stringLimit < preferenceValueMax {
			preferenceValueMax = stringLimit
		}
		if stringLimit < factValueMax {
			factValueMax = stringLimit
		}
	}
	if len(content) > domainmemory.ProfilePromotionResponseBytesMax {
		return nil, newProfileExtractorValidationError(profileValidationResponseTooLarge)
	}
	trimmed := bytes.TrimSpace([]byte(content))
	if !utf8.Valid(trimmed) || len(trimmed) == 0 {
		return nil, newProfileExtractorValidationError(profileValidationInvalidJSON)
	}
	if trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return nil, newProfileExtractorValidationError(profileValidationJSONObject)
	}
	fields, err := decodeUniqueJSONObject(trimmed)
	if err != nil {
		return nil, err
	}
	if fields == nil || len(fields) != 2 {
		return nil, newProfileExtractorValidationError(profileValidationSchemaFields)
	}
	preferencesRaw, ok := fields["preferences"]
	if !ok {
		return nil, newProfileExtractorValidationError(profileValidationSchemaFields)
	}
	factsRaw, ok := fields["facts"]
	if !ok {
		return nil, newProfileExtractorValidationError(profileValidationSchemaFields)
	}
	preferencesTrimmed := bytes.TrimSpace(preferencesRaw)
	if !json.Valid(preferencesTrimmed) {
		return nil, newProfileExtractorValidationError(profileValidationInvalidJSON)
	}
	if len(preferencesTrimmed) == 0 || preferencesTrimmed[0] != '{' || preferencesTrimmed[len(preferencesTrimmed)-1] != '}' {
		return nil, newProfileExtractorValidationError(profileValidationPreferencesType)
	}
	rawPreferences, err := decodeUniqueJSONObject(preferencesTrimmed)
	if err != nil {
		code := profileExtractorValidationCodeOf(err)
		if code == profileValidationJSONObject {
			code = profileValidationPreferencesType
		}
		return nil, newProfileExtractorValidationError(code)
	}
	factsTrimmed := bytes.TrimSpace(factsRaw)
	if !json.Valid(factsTrimmed) {
		return nil, newProfileExtractorValidationError(profileValidationInvalidJSON)
	}
	if len(factsTrimmed) == 0 || factsTrimmed[0] != '[' || factsTrimmed[len(factsTrimmed)-1] != ']' {
		return nil, newProfileExtractorValidationError(profileValidationFactsType)
	}
	var rawFacts []json.RawMessage
	if err := json.Unmarshal(factsTrimmed, &rawFacts); err != nil || rawFacts == nil {
		return nil, newProfileExtractorValidationError(profileValidationFactsType)
	}
	facts := make([]string, len(rawFacts))
	for index, rawFact := range rawFacts {
		if err := json.Unmarshal(rawFact, &facts[index]); err != nil {
			return nil, newProfileExtractorValidationError(profileValidationFactValue)
		}
	}
	if len(rawPreferences)+len(facts) > candidateLimit {
		return nil, newProfileExtractorValidationError(profileValidationCandidateLimit)
	}
	var payload profileExtractionPayload
	payload.Preferences = rawPreferences
	payload.Facts = facts
	result := &domconv.ProfileExtractionResult{
		NewPreferences: make(map[string]string, len(payload.Preferences)),
		NewFacts:       payload.Facts,
	}
	for key, raw := range payload.Preferences {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n\x00") || len([]rune(key)) > preferenceKeyMax {
			return nil, newProfileExtractorValidationError(profileValidationPreferenceKey)
		}
		var stringValue string
		if err := json.Unmarshal(raw, &stringValue); err == nil {
			if strings.TrimSpace(stringValue) == "" || strings.ContainsAny(stringValue, "\r\n\x00") || len([]rune(stringValue)) > preferenceValueMax {
				return nil, newProfileExtractorValidationError(profileValidationPreferenceValue)
			}
			result.NewPreferences[key] = stringValue
			continue
		}
		return nil, newProfileExtractorValidationError(profileValidationPreferenceType)
	}
	for _, fact := range result.NewFacts {
		if strings.TrimSpace(fact) == "" || strings.ContainsAny(fact, "\r\n\x00") || len([]rune(fact)) > factValueMax {
			return nil, newProfileExtractorValidationError(profileValidationFactValue)
		}
	}
	return result, nil
}

func decodeUniqueJSONObject(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil {
		return nil, newProfileExtractorValidationError(profileValidationInvalidJSON)
	}
	delim, ok := first.(json.Delim)
	if !ok || delim != '{' {
		return nil, newProfileExtractorValidationError(profileValidationJSONObject)
	}
	values := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, newProfileExtractorValidationError(profileValidationInvalidJSON)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, newProfileExtractorValidationError(profileValidationInvalidJSON)
		}
		if _, exists := values[key]; exists {
			return nil, newProfileExtractorValidationError(profileValidationInvalidJSON)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, newProfileExtractorValidationError(profileValidationInvalidJSON)
		}
		values[key] = value
	}
	last, err := decoder.Token()
	if err != nil {
		return nil, newProfileExtractorValidationError(profileValidationInvalidJSON)
	}
	if delim, ok := last.(json.Delim); !ok || delim != '}' {
		return nil, newProfileExtractorValidationError(profileValidationInvalidJSON)
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, newProfileExtractorValidationError(profileValidationInvalidJSON)
	}
	return values, nil
}
