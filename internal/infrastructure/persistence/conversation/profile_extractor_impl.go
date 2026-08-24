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
		// The Worker target is a reasoning model whose analysis channel
		// consumes output tokens before the final JSON. 256 tokens starved
		// the final channel (EMPTY_FINAL_CONTENT) or truncated the JSON,
		// and 4096 still starved it when high-effort reasoning ran long.
		// Extraction therefore requests low reasoning effort and keeps a
		// token budget with headroom; CORE still validates the response
		// against the 64KiB exact-JSON contract after generation.
		maxTokens:   8192,
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

const profileExtractorRepairInstructionTemplate = `前回の応答はProfilePromotionの厳密な出力契約に違反しました。元の会話だけを根拠に、説明文・Markdown・コードフェンスを付けず、preferencesは文字列値だけのJSON object、factsは文字列だけのJSON arrayとし、キーをpreferencesとfactsだけにした単一JSON objectを返してください。各文字列は200文字以内の一文にしてください。preferencesとfactsを合わせて最大%d件にしてください。空の場合も{"preferences":{},"facts":[]}を返してください。`

func profileExtractorRepairInstruction(candidateLimit int) string {
	return fmt.Sprintf(profileExtractorRepairInstructionTemplate, candidateLimit)
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
		size := utf8.RuneCountInString(excerpt)
		if size > remaining {
			excerpt = headRunes(excerpt, remaining)
			size = remaining
		}
		if size <= 0 {
			break
		}
		excerpts = append(excerpts, "- "+strings.ReplaceAll(excerpt, "\n", " "))
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
			"[ProfileExtractor] evidence exceeded %d groups: thread=%d dropped_runes=%d",
			domainmemory.ProfilePromotionEvidenceGroupMax, thread.ID, droppedRunes,
		)
	}

	// 既知情報テキスト。serviceがbounded owner projectionだけを渡す。
	existingText := ""
	if len(existing.Preferences) > 0 || len(existing.Facts) > 0 {
		existingText = "既知情報:\n"
		preferenceKeys := make([]string, 0, len(existing.Preferences))
		for k := range existing.Preferences {
			preferenceKeys = append(preferenceKeys, k)
		}
		sort.Strings(preferenceKeys)
		for _, k := range preferenceKeys {
			v := existing.Preferences[k]
			existingText += fmt.Sprintf("- %s: %s\n", k, v)
		}
		for _, f := range existing.Facts {
			existingText += fmt.Sprintf("- %s\n", f)
		}
	}

	// group ごとに抽出し、結果を束ねる。1 group あたりの上限を配ることで、
	// 後半の chunk が上限に押し出されて捨てられるのを防ぐ。
	perGroupLimit := domainmemory.ProfilePromotionRawCandidateLimit / len(groups)
	if perGroupLimit < 2 {
		perGroupLimit = 2
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
	evidenceSection := "会話:\n(このバッチにユーザー自身の発言はありません)"
	if strings.TrimSpace(evidence) != "" {
		evidenceSection = "会話（ユーザー本人の発言）:\n" + evidence
	}
	materialSection := ""
	if strings.TrimSpace(materialDigest) != "" {
		materialSection = "\n\n参照資料（ユーザーが貼り付けた資料の冒頭。ユーザーの主張でも体験でもありません）:\n" + materialDigest
	}
	prompt := fmt.Sprintf(`以下の会話からユーザーに関する新しい情報を抽出してください。
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
	result, err := decodeProfileExtractionResult(resp.Content)
	if err == nil {
		return result, nil
	}
	log.Printf("[ProfileExtractor] JSON parse failed: profile_extractor_invalid")
	invalidErr := domconv.NewProfileExtractionInvalidError(err)
	if repairUsed == nil || *repairUsed {
		return nil, invalidErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	*repairUsed = true

	repairReq := req
	repairReq.Messages = append(append([]llm.Message(nil), req.Messages...), llm.Message{
		Role:    "user",
		Content: profileExtractorRepairInstruction(candidateLimit),
	})
	repairResp, repairErr := e.provider.Generate(requestCtx, repairReq)
	if repairErr != nil {
		log.Printf("[ProfileExtractor] repair LLM call failed: profile_extractor_unavailable")
		return nil, domconv.NewProfileExtractionUnavailableError(repairErr)
	}
	result, repairValidationErr := decodeProfileExtractionResult(repairResp.Content)
	if repairValidationErr != nil {
		log.Printf("[ProfileExtractor] repaired JSON remains invalid: profile_extractor_invalid")
		return nil, domconv.NewProfileExtractionInvalidError(repairValidationErr)
	}

	return result, nil
}

type profileExtractionPayload struct {
	Preferences map[string]json.RawMessage `json:"preferences"`
	Facts       []string                   `json:"facts"`
}

func decodeProfileExtractionResult(content string) (*domconv.ProfileExtractionResult, error) {
	if len(content) > domainmemory.ProfilePromotionResponseBytesMax {
		return nil, fmt.Errorf("profile extractor response exceeds %d bytes", domainmemory.ProfilePromotionResponseBytesMax)
	}
	trimmed := bytes.TrimSpace([]byte(content))
	if !utf8.Valid(trimmed) || len(trimmed) == 0 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return nil, fmt.Errorf("profile extractor response must be one JSON object")
	}
	fields, err := decodeUniqueJSONObject(trimmed)
	if err != nil {
		return nil, err
	}
	if fields == nil || len(fields) != 2 {
		return nil, fmt.Errorf("profile extractor response must contain exactly preferences and facts")
	}
	preferencesRaw, ok := fields["preferences"]
	if !ok {
		return nil, fmt.Errorf("profile extractor response is missing preferences")
	}
	factsRaw, ok := fields["facts"]
	if !ok {
		return nil, fmt.Errorf("profile extractor response is missing facts")
	}
	rawPreferences, err := decodeUniqueJSONObject(bytes.TrimSpace(preferencesRaw))
	if err != nil {
		return nil, fmt.Errorf("preferences must be a JSON object")
	}
	if bytes.Equal(bytes.TrimSpace(factsRaw), []byte("null")) {
		return nil, fmt.Errorf("facts must be a JSON array")
	}
	var facts []string
	if err := json.Unmarshal(factsRaw, &facts); err != nil || facts == nil {
		return nil, fmt.Errorf("facts must be a JSON array of strings")
	}
	if len(rawPreferences)+len(facts) > domainmemory.ProfilePromotionRawCandidateLimit {
		return nil, fmt.Errorf("profile extractor raw candidate count exceeds %d", domainmemory.ProfilePromotionRawCandidateLimit)
	}
	var payload profileExtractionPayload
	payload.Preferences = rawPreferences
	payload.Facts = facts
	result := &domconv.ProfileExtractionResult{
		NewPreferences: make(map[string]string, len(payload.Preferences)),
		NewFacts:       payload.Facts,
	}
	for key, raw := range payload.Preferences {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n\x00") || len([]rune(key)) > domainmemory.ProfilePromotionPreferenceKeyMax {
			return nil, fmt.Errorf("preference key %q is invalid", key)
		}
		var stringValue string
		if err := json.Unmarshal(raw, &stringValue); err == nil {
			if strings.TrimSpace(stringValue) == "" || strings.ContainsAny(stringValue, "\r\n\x00") || len([]rune(stringValue)) > domainmemory.ProfilePromotionPreferenceValueMax {
				return nil, fmt.Errorf("preference %q value is invalid", key)
			}
			result.NewPreferences[key] = stringValue
			continue
		}
		return nil, fmt.Errorf("preference %q must be a JSON string", key)
	}
	for i, fact := range result.NewFacts {
		if strings.TrimSpace(fact) == "" || strings.ContainsAny(fact, "\r\n\x00") || len([]rune(fact)) > domainmemory.ProfilePromotionProjectionStatementMax {
			return nil, fmt.Errorf("fact[%d] is invalid", i)
		}
	}
	return result, nil
}

func decodeUniqueJSONObject(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := first.(json.Delim)
	if !ok || delim != '{' {
		return nil, fmt.Errorf("JSON value must be an object")
	}
	values := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, fmt.Errorf("JSON object key must be a string")
		}
		if _, exists := values[key]; exists {
			return nil, fmt.Errorf("duplicate JSON object key %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		values[key] = value
	}
	last, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := last.(json.Delim); !ok || delim != '}' {
		return nil, fmt.Errorf("JSON object is not closed")
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("JSON response contains more than one value")
		}
		return nil, err
	}
	return values, nil
}
