package speech

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	SchemaVersion                   = "rencrow.speech/1.0"
	MaxUtterances                   = 6
	MaxSegmentsPerUtterance         = 3
	MaxCuesPerPosition              = 2
	MaxCueKindsPerUtterance         = 2
	MaxSegmentRunes                 = 120
	MaxDisplayRunes                 = 600
	DisplayUtteranceSeparator       = " "
	FallbackSpeechPlanLimitExceeded = "speech_plan_limit_exceeded"
	FallbackUnknownField            = "speech_plan_unknown_field"
	FallbackInvalidJSON             = "speech_plan_invalid_json"
	FallbackPlainText               = "speech_plan_not_json"
	FallbackInvalidSchema           = "speech_plan_invalid_schema"
	FallbackInvalidStructure        = "speech_plan_invalid_structure"
	FallbackInvalidSegmentText      = "speech_plan_invalid_segment_text"
	FallbackUnsupportedEmoji        = "speech_plan_unsupported_emoji"
	FallbackGenerationFailed        = "speech_plan_generation_failed"
)

type SpeechPlan struct {
	SchemaVersion string      `json:"schema_version"`
	Utterances    []Utterance `json:"utterances"`
}

type Utterance struct {
	Segments     []Segment `json:"segments"`
	Delivery     Delivery  `json:"delivery"`
	PauseAfterMS int       `json:"pause_after_ms"`
}

type Segment struct {
	Text   string `json:"text"`
	Before []Cue  `json:"before,omitempty"`
	After  []Cue  `json:"after,omitempty"`
}

type Delivery struct {
	Tone     string `json:"tone"`
	Attitude string `json:"attitude"`
	Energy   string `json:"energy"`
	Pace     string `json:"pace"`
	Mode     string `json:"mode"`
}

type Cue struct {
	Kind     string `json:"kind"`
	Strength string `json:"strength"`
}

type Result struct {
	Plan             SpeechPlan
	DisplayText      string
	Valid            bool
	FallbackReason   string
	LLMGenerationMS  *int64 `json:"llm_generation_ms"`
	PlanValidationMS *int64 `json:"plan_validation_ms"`
}

// Parse decodes a SpeechPlan, applies deterministic normalization, and keeps
// recoverable segment text available for display when speech is suppressed.
func Parse(raw string) Result {
	data := []byte(raw)
	if !json.Valid(data) {
		repaired, ok := closeMissingJSONDelimiters(data)
		if !ok || !json.Valid(repaired) {
			if !looksLikeJSONDocument(raw) {
				return Result{
					DisplayText:    stripIrodoriControlEmoji(raw),
					FallbackReason: FallbackPlainText,
				}
			}
			return Result{FallbackReason: FallbackInvalidJSON}
		}
		data = repaired
	}

	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return Result{FallbackReason: FallbackInvalidJSON}
	}
	displayText := stripIrodoriControlEmoji(displayTextFromJSON(decoded))
	hasAlias, err := hasCanonicalFieldAliasJSON(data)
	if err != nil {
		return Result{
			DisplayText:    displayText,
			FallbackReason: FallbackInvalidJSON,
		}
	}
	if hasAlias {
		return Result{
			DisplayText:    displayText,
			FallbackReason: FallbackUnknownField,
		}
	}

	var plan SpeechPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return Result{
			DisplayText:    displayText,
			FallbackReason: FallbackInvalidSchema,
		}
	}

	result := Normalize(plan)
	if result.DisplayText == "" {
		result.DisplayText = displayText
	}
	if result.Valid && hasUnknownFields(decoded) {
		recovered := Normalize(neutralSpeechPlan(result.Plan))
		if recovered.Valid {
			recovered.FallbackReason = FallbackUnknownField
		}
		return recovered
	}
	return result
}

func neutralSpeechPlan(plan SpeechPlan) SpeechPlan {
	neutral := SpeechPlan{
		SchemaVersion: plan.SchemaVersion,
		Utterances:    make([]Utterance, len(plan.Utterances)),
	}
	for i, utterance := range plan.Utterances {
		segments := make([]Segment, len(utterance.Segments))
		for j, segment := range utterance.Segments {
			segments[j] = Segment{Text: segment.Text}
		}
		neutral.Utterances[i] = Utterance{
			Segments:     segments,
			Delivery:     Delivery{Tone: "neutral", Attitude: "neutral", Energy: "medium", Pace: "normal", Mode: "dialogue"},
			PauseAfterMS: 0,
		}
	}
	return neutral
}

// Normalize returns a copy of plan with v1.0 defaults and bounded cues applied.
// Invalid structural limits preserve display text and return Valid=false.
func Normalize(plan SpeechPlan) Result {
	plan = cloneSpeechPlan(plan)
	for utteranceIndex := range plan.Utterances {
		for segmentIndex := range plan.Utterances[utteranceIndex].Segments {
			segment := &plan.Utterances[utteranceIndex].Segments[segmentIndex]
			segment.Text = stripIrodoriControlEmoji(segment.Text)
		}
	}
	result := Result{Plan: plan, DisplayText: displayTextFromPlan(plan)}
	if plan.SchemaVersion != SchemaVersion {
		result.FallbackReason = FallbackInvalidSchema
		return result
	}
	if len(plan.Utterances) == 0 {
		result.FallbackReason = FallbackInvalidStructure
		return result
	}
	limitExceeded := len(plan.Utterances) > MaxUtterances
	invalidStructure := false
	invalidSegmentText := false
	unsupportedEmoji := false

	for utteranceIndex := range plan.Utterances {
		utterance := &plan.Utterances[utteranceIndex]
		if len(utterance.Segments) == 0 {
			invalidStructure = true
		}
		if len(utterance.Segments) > MaxSegmentsPerUtterance {
			limitExceeded = true
		}

		segments := make([]Segment, 0, len(utterance.Segments))
		for _, segment := range utterance.Segments {
			if segment.Text == "" {
				invalidSegmentText = true
			}
			if containsGatewayRejectedRune(segment.Text) {
				unsupportedEmoji = true
			}
			if runeCount(segment.Text) <= MaxSegmentRunes {
				segments = append(segments, segment)
				continue
			}

			chunks, ok := splitLongTextAtPunctuation(segment.Text)
			if !ok || len(segments)+len(chunks) > MaxSegmentsPerUtterance {
				limitExceeded = true
				segments = append(segments, segment)
				continue
			}
			for chunkIndex, chunk := range chunks {
				split := Segment{Text: chunk}
				if chunkIndex == 0 {
					split.Before = cloneCues(segment.Before)
				}
				if chunkIndex == len(chunks)-1 {
					split.After = cloneCues(segment.After)
				}
				segments = append(segments, split)
			}
		}
		utterance.Segments = segments
		if len(utterance.Segments) > MaxSegmentsPerUtterance {
			limitExceeded = true
		}

		utterance.Delivery = normalizeDelivery(utterance.Delivery)
		utterance.PauseAfterMS = nearestAllowedPause(utterance.PauseAfterMS)
		normalizeUtteranceCues(utterance)
	}

	result.Plan = plan
	result.DisplayText = displayTextFromPlan(plan)
	if runeCount(result.DisplayText) > MaxDisplayRunes {
		limitExceeded = true
	}
	switch {
	case limitExceeded:
		result.FallbackReason = FallbackSpeechPlanLimitExceeded
	case invalidSegmentText:
		result.FallbackReason = FallbackInvalidSegmentText
	case invalidStructure:
		result.FallbackReason = FallbackInvalidStructure
	case unsupportedEmoji:
		result.FallbackReason = FallbackUnsupportedEmoji
	default:
		result.Valid = true
	}
	return result
}

func cloneSpeechPlan(plan SpeechPlan) SpeechPlan {
	cloned := SpeechPlan{SchemaVersion: plan.SchemaVersion, Utterances: make([]Utterance, len(plan.Utterances))}
	for i, utterance := range plan.Utterances {
		cloned.Utterances[i] = utterance
		cloned.Utterances[i].Segments = make([]Segment, len(utterance.Segments))
		for j, segment := range utterance.Segments {
			cloned.Utterances[i].Segments[j] = segment
			cloned.Utterances[i].Segments[j].Before = cloneCues(segment.Before)
			cloned.Utterances[i].Segments[j].After = cloneCues(segment.After)
		}
	}
	return cloned
}

func cloneCues(cues []Cue) []Cue {
	if cues == nil {
		return nil
	}
	return append([]Cue(nil), cues...)
}

func displayTextFromPlan(plan SpeechPlan) string {
	utterances := make([]string, len(plan.Utterances))
	for i, utterance := range plan.Utterances {
		var text strings.Builder
		for _, segment := range utterance.Segments {
			text.WriteString(segment.Text)
		}
		utterances[i] = text.String()
	}
	return strings.Join(utterances, DisplayUtteranceSeparator)
}

func displayTextFromJSON(root any) string {
	rootObject, ok := root.(map[string]any)
	if !ok {
		return ""
	}
	items, ok := rootObject["utterances"].([]any)
	if !ok {
		return ""
	}
	utterances := make([]string, len(items))
	for i, item := range items {
		utterance, ok := item.(map[string]any)
		if !ok {
			continue
		}
		segments, ok := utterance["segments"].([]any)
		if !ok {
			continue
		}
		var text strings.Builder
		for _, item := range segments {
			segment, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if value, ok := segment["text"].(string); ok {
				text.WriteString(value)
			}
		}
		utterances[i] = text.String()
	}
	return strings.Join(utterances, DisplayUtteranceSeparator)
}

// hasCanonicalFieldAliasJSON scans the token stream because decoding JSON into
// maps collapses duplicate object members before alias detection can see them.
func hasCanonicalFieldAliasJSON(data []byte) (bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	return hasCanonicalFieldAliasJSONValue(decoder)
}

func hasCanonicalFieldAliasJSONValue(decoder *json.Decoder) (bool, error) {
	token, err := decoder.Token()
	if err != nil {
		return false, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return false, nil
	}
	switch delimiter {
	case '{':
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return false, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return false, errors.New("JSON object member name is not a string")
			}
			if isCanonicalFieldAlias(key) {
				return true, nil
			}
			found, err := hasCanonicalFieldAliasJSONValue(decoder)
			if err != nil || found {
				return found, err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return false, err
		}
	case '[':
		for decoder.More() {
			found, err := hasCanonicalFieldAliasJSONValue(decoder)
			if err != nil || found {
				return found, err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return false, err
		}
	}
	return false, nil
}

func isCanonicalFieldAlias(key string) bool {
	for _, canonical := range []string{
		"schema_version", "utterances", "segments", "delivery", "pause_after_ms",
		"text", "before", "after", "tone", "attitude", "energy", "pace", "mode",
		"kind", "strength",
	} {
		if key != canonical && strings.EqualFold(key, canonical) {
			return true
		}
	}
	return false
}

func hasUnknownFields(root any) bool {
	rootObject, ok := root.(map[string]any)
	if !ok || !onlyKeys(rootObject, "schema_version", "utterances") {
		return true
	}
	items, ok := rootObject["utterances"].([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		utterance, ok := item.(map[string]any)
		if !ok || !onlyKeys(utterance, "segments", "delivery", "pause_after_ms") {
			return true
		}
		if value, exists := utterance["segments"]; exists {
			segments, ok := value.([]any)
			if !ok {
				continue
			}
			for _, item := range segments {
				segment, ok := item.(map[string]any)
				if !ok || !onlyKeys(segment, "text", "before", "after") {
					return true
				}
				for _, key := range []string{"before", "after"} {
					if value, exists := segment[key]; exists {
						cues, ok := value.([]any)
						if !ok {
							continue
						}
						for _, item := range cues {
							cue, ok := item.(map[string]any)
							if !ok || !onlyKeys(cue, "kind", "strength") {
								return true
							}
						}
					}
				}
			}
		}
		if value, exists := utterance["delivery"]; exists {
			delivery, ok := value.(map[string]any)
			if ok && !onlyKeys(delivery, "tone", "attitude", "energy", "pace", "mode") {
				return true
			}
		}
	}
	return false
}

func onlyKeys(object map[string]any, keys ...string) bool {
	allowed := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		allowed[key] = struct{}{}
	}
	for key := range object {
		if _, ok := allowed[key]; !ok {
			return false
		}
	}
	return true
}

func normalizeDelivery(delivery Delivery) Delivery {
	if !oneOf(delivery.Tone, "neutral", "calm", "warm", "amused", "cheerful", "sad", "angry", "worried", "surprised", "embarrassed", "relieved", "sleepy", "tense", "serious") {
		delivery.Tone = "neutral"
	}
	if !oneOf(delivery.Attitude, "neutral", "gentle", "teasing", "exasperated", "confident", "timid", "pleading", "questioning", "sulking") {
		delivery.Attitude = "neutral"
	}
	if !oneOf(delivery.Energy, "low", "medium", "high") {
		delivery.Energy = "medium"
	}
	if !oneOf(delivery.Pace, "slow", "normal", "fast") {
		delivery.Pace = "normal"
	}
	if !oneOf(delivery.Mode, "dialogue", "narration", "monologue", "backchannel") {
		delivery.Mode = "dialogue"
	}
	return delivery
}

func normalizeUtteranceCues(utterance *Utterance) {
	seenKinds := make(map[string]struct{}, MaxCueKindsPerUtterance)
	for i := range utterance.Segments {
		utterance.Segments[i].Before = normalizeCuePosition(utterance.Segments[i].Before, seenKinds)
		utterance.Segments[i].After = normalizeCuePosition(utterance.Segments[i].After, seenKinds)
	}
}

func normalizeCuePosition(cues []Cue, seenKinds map[string]struct{}) []Cue {
	if len(cues) == 0 {
		return nil
	}
	accepted := make([]Cue, 0, min(len(cues), MaxCuesPerPosition))
	for _, cue := range cues {
		if !oneOf(cue.Kind, "pause", "chuckle", "sigh", "breath", "gasp", "whisper", "cry", "shout", "cough", "yawn", "hum", "agreement", "surprise", "question", "relief") ||
			!oneOf(cue.Strength, "light", "normal", "strong") {
			continue
		}
		if _, ok := seenKinds[cue.Kind]; !ok && len(seenKinds) >= MaxCueKindsPerUtterance {
			continue
		}
		seenKinds[cue.Kind] = struct{}{}
		accepted = append(accepted, cue)
		if len(accepted) == MaxCuesPerPosition {
			break
		}
	}
	return accepted
}

func nearestAllowedPause(value int) int {
	allowed := [...]int{0, 150, 300, 500, 800}
	nearest := allowed[0]
	distance := absInt(value - nearest)
	for _, candidate := range allowed[1:] {
		candidateDistance := absInt(value - candidate)
		if candidateDistance < distance || (candidateDistance == distance && candidate < nearest) {
			nearest = candidate
			distance = candidateDistance
		}
	}
	return nearest
}

func splitLongTextAtPunctuation(text string) ([]string, bool) {
	runes := []rune(text)
	remaining := runes
	chunks := make([]string, 0, 2)
	for len(remaining) > MaxSegmentRunes {
		if len(chunks) == MaxSegmentsPerUtterance {
			return nil, false
		}
		cut := 0
		for i := 0; i < MaxSegmentRunes; i++ {
			if isSplitPunctuation(remaining[i]) {
				cut = i + 1
			}
		}
		if cut == 0 {
			return nil, false
		}
		chunks = append(chunks, string(remaining[:cut]))
		remaining = remaining[cut:]
	}
	if len(remaining) > 0 {
		chunks = append(chunks, string(remaining))
	}
	if len(chunks) == 0 || len(chunks) > MaxSegmentsPerUtterance {
		return nil, false
	}
	return chunks, true
}

func isSplitPunctuation(r rune) bool {
	switch r {
	case '。', '！', '？', '!', '?', '.', '；', ';', '，', ',', '、', '：', ':':
		return true
	default:
		return false
	}
}

func closeMissingJSONDelimiters(data []byte) ([]byte, bool) {
	stack := make([]byte, 0, 8)
	inString := false
	escaped := false
	for _, current := range data {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch current {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch current {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, current)
		case '}':
			if len(stack) == 0 || stack[len(stack)-1] != '{' {
				return nil, false
			}
			stack = stack[:len(stack)-1]
		case ']':
			if len(stack) == 0 || stack[len(stack)-1] != '[' {
				return nil, false
			}
			stack = stack[:len(stack)-1]
		}
	}
	if inString || len(stack) == 0 {
		return nil, false
	}
	fixed := append([]byte(nil), data...)
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			fixed = append(fixed, '}')
		} else {
			fixed = append(fixed, ']')
		}
	}
	return fixed, true
}

func looksLikeJSONDocument(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "{") {
		return true
	}
	if !strings.HasPrefix(trimmed, "[") {
		return false
	}
	items := strings.TrimSpace(trimmed[1:])
	if items == "" {
		return true
	}
	switch items[0] {
	case '{', '[', '"', ']', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', 't', 'f', 'n':
		return true
	default:
		// A bracketed label such as [mio] is plain display text, not JSON.
		return false
	}
}

var irodoriControlEmoji = [...]string{
	"😮‍💨", "⏸️", "🤭", "😮", "👂", "😭", "😱", "🤧", "🥱", "🎵", "👌", "😲", "🤔", "😌",
}

func stripIrodoriControlEmoji(text string) string {
	if text == "" {
		return text
	}
	var cleaned strings.Builder
	cleaned.Grow(len(text))
	for index := 0; index < len(text); {
		remaining := text[index:]
		matchedControl := false
		for _, control := range irodoriControlEmoji {
			if strings.HasPrefix(remaining, control) {
				index += len(control)
				matchedControl = true
				break
			}
		}
		if matchedControl {
			continue
		}
		_, size := utf8.DecodeRuneInString(remaining)
		if size == 0 {
			break
		}
		cleaned.WriteString(remaining[:size])
		index += size
	}
	return cleaned.String()
}

func containsGatewayRejectedRune(text string) bool {
	for _, character := range text {
		if isGatewayRejectedRune(character) {
			return true
		}
	}
	return false
}

func isGatewayRejectedRune(character rune) bool {
	return character == '\u200d' || character == '\u20e3' || character == '\ufe0f' ||
		(character >= 0x2300 && character <= 0x23ff && character != 0x2308 && character != 0x2309) ||
		(character >= 0x2600 && character <= 0x27bf) ||
		(character >= 0x1f000 && character <= 0x1faff)
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func runeCount(value string) int {
	return len([]rune(value))
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
