package agent

import (
	"context"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
)

type speechPlanCallbackKey struct{}

// SpeechPlanCallback receives the normalized plan result produced for one CHAT
// response. It is carried on context so the application route can hand a typed
// plan to its TTS owner without exposing JSON tokens to legacy stream hooks.
type SpeechPlanCallback func(result speech.Result)

// ContextWithSpeechPlanCallback enables SpeechPlan mode for a normal CHAT
// generation and provides its typed result to the application route.
func ContextWithSpeechPlanCallback(ctx context.Context, callback SpeechPlanCallback) context.Context {
	return context.WithValue(ctx, speechPlanCallbackKey{}, callback)
}

// SpeechPlanCallbackFromContext returns the CHAT SpeechPlan receiver, if any.
func SpeechPlanCallbackFromContext(ctx context.Context) SpeechPlanCallback {
	callback, _ := ctx.Value(speechPlanCallbackKey{}).(SpeechPlanCallback)
	return callback
}

// reportDeterministicSpeechPlan projects known CHAT text into a neutral typed
// plan without asking another model to reinterpret it. It returns true when
// SpeechPlan mode is active, including when normalization suppresses synthesis.
func reportDeterministicSpeechPlan(ctx context.Context, text string) bool {
	callback := SpeechPlanCallbackFromContext(ctx)
	if callback == nil {
		return false
	}
	validationStarted := time.Now()
	result := speech.Normalize(speech.SpeechPlan{
		SchemaVersion: speech.SchemaVersion,
		Utterances: []speech.Utterance{{
			Segments: []speech.Segment{{Text: text}},
			Delivery: speech.Delivery{
				Tone: "neutral", Attitude: "neutral", Energy: "medium", Pace: "normal", Mode: "dialogue",
			},
		}},
	})
	result.PlanValidationMS = durationMilliseconds(time.Since(validationStarted))
	callback(result)
	return true
}

func speechPlanInstruction() string {
	return `通常のChat応答を、rencrow.speech/1.0 のSpeechPlanとして一度だけ生成してください。
出力は追加文・Markdown fence・説明のない単一JSON objectにしてください。次のschema以外のfieldを追加しないでください。
{"schema_version":"rencrow.speech/1.0","utterances":[{"segments":[{"text":"読み上げるセリフだけ","before":[{"kind":"pause|chuckle|sigh|breath|gasp|whisper|cry|shout|cough|yawn|hum|agreement|surprise|question|relief","strength":"light|normal|strong"}],"after":[]}],"delivery":{"tone":"neutral|calm|warm|amused|cheerful|sad|angry|worried|surprised|embarrassed|relieved|sleepy|tense|serious","attitude":"neutral|gentle|teasing|exasperated|confident|timid|pleading|questioning|sulking","energy":"low|medium|high","pace":"slow|normal|fast","mode":"dialogue|narration|monologue|backchannel"},"pause_after_ms":0}]}
utterancesは1〜6個、utteranceごとにsegmentsは1〜3個、各textは1〜120文字、本文合計は600文字以内です。
各segmentのbeforeとafterはcueをそれぞれ2個まで、utterance内のcue kindは先頭から2種類までにしてください。pause_after_msは0,150,300,500,800のいずれかです。
segments[].textには元表記の読み上げ本文だけを入れ、emoji、caption、演技指示、声質、性別、年齢、声の高さを含めないでください。tone等と局所cueは本文に合う範囲で選んでください。
voice_id、model、provider、caption、emoji、参照音声、target URL、lora_adapter、num_steps、cfg_scaleやその他Irodori固有の制御fieldを出力しないでください。`
}

func generateSpeechPlanResponse(ctx context.Context, provider llm.LLMProvider, request llm.GenerateRequest, enabled bool) (llm.GenerateResponse, time.Duration, error) {
	if !enabled {
		response, err := provider.Generate(ctx, request)
		return response, 0, err
	}

	started := time.Now()
	response, err := provider.Generate(ctx, request)
	generationDuration := time.Since(started)
	if err != nil {
		reportSpeechPlanGenerationFailure(ctx, generationDuration)
	}
	return response, generationDuration, err
}

func parseSpeechPlan(ctx context.Context, raw string, generationDuration time.Duration) (speech.Result, bool) {
	callback := SpeechPlanCallbackFromContext(ctx)
	if callback == nil {
		return speech.Result{}, false
	}
	validationStarted := time.Now()
	result := speech.Parse(raw)
	result.LLMGenerationMS = durationMilliseconds(generationDuration)
	result.PlanValidationMS = durationMilliseconds(time.Since(validationStarted))
	callback(result)
	return result, true
}

func reportSpeechPlanGenerationFailure(ctx context.Context, generationDuration time.Duration) {
	if callback := SpeechPlanCallbackFromContext(ctx); callback != nil {
		callback(speech.Result{
			FallbackReason:  speech.FallbackGenerationFailed,
			LLMGenerationMS: durationMilliseconds(generationDuration),
		})
	}
}

func durationMilliseconds(duration time.Duration) *int64 {
	milliseconds := duration.Milliseconds()
	return &milliseconds
}

func reportGenerationMetrics(ctx context.Context, response llm.GenerateResponse) {
	if callback := llm.GenerationMetricsCallbackFromContext(ctx); callback != nil && (response.TokensUsed > 0 || response.TokensPerSecond > 0) {
		callback(llm.GenerationMetrics{
			CompletionTokens: response.TokensUsed,
			TokensPerSecond:  response.TokensPerSecond,
		})
	}
}
