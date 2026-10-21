package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"testing"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func captureSpeechPlanLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	logger := log.Default()
	previousOutput := logger.Writer()
	previousFlags := logger.Flags()
	previousPrefix := logger.Prefix()
	output := &bytes.Buffer{}
	logger.SetOutput(output)
	logger.SetFlags(0)
	logger.SetPrefix("")
	t.Cleanup(func() {
		logger.SetOutput(previousOutput)
		logger.SetFlags(previousFlags)
		logger.SetPrefix(previousPrefix)
	})
	return output
}

func TestMessageRouteEmitsOneBoundedSpeechPlanObservation(t *testing.T) {
	logOutput := captureSpeechPlanLogs(t)
	const privateText = "private dialogue must never enter logs"
	generationMS := int64(28)
	validationMS := int64(3)
	result := speech.Result{
		Valid:            true,
		DisplayText:      privateText,
		LLMGenerationMS:  &generationMS,
		PlanValidationMS: &validationMS,
		Plan: speech.SpeechPlan{
			SchemaVersion: speech.SchemaVersion,
			Utterances: []speech.Utterance{{Segments: []speech.Segment{{
				Text:   privateText,
				Before: []speech.Cue{{Kind: "sigh", Strength: "light"}},
				After:  []speech.Cue{{Kind: "pause", Strength: "light"}},
			}}}},
		},
	}
	dispatcher := newMessageRouteDispatcher(
		&mockMioAgent{chatFunc: func(ctx context.Context, _ conversation.TurnInput) (string, error) {
			callback := domainagent.SpeechPlanCallbackFromContext(ctx)
			if callback == nil {
				t.Fatal("CHAT route did not install a SpeechPlan callback")
			}
			callback(result)
			return privateText, nil
		}},
		&mockShiroAgent{}, nil, func(string, string, string, string, string, string, string, string, string) {},
		newMessageTTSLifecycle(&mockTTSBridge{}, nil, nil).WithStreamHooks,
		func(context.Context, string, routing.Route, string, string, string) {},
	)
	dispatcher.SetSpeechPlanEnabled(true)
	dispatcher.SetSpeechPlanPusher(func(context.Context, string, string, string, string, speech.SpeechPlan) error { return nil })
	input := newOrchestratorTestTurnInput(t, "private input", "viewer", "viewer-user").WithSessionID("conversation-1").WithViewerRecipient("mio")
	if _, err := dispatcher.ExecuteTurnInput(context.Background(), input, routing.RouteCHAT, modulecore.NewTaskID(), modulecore.NewRunID(), "tts-1"); err != nil {
		t.Fatalf("ExecuteTurnInput() error = %v", err)
	}

	logText := strings.TrimSpace(logOutput.String())
	if logText == "" {
		t.Fatal("SpeechPlan route emitted no structured observation")
	}
	lines := strings.Split(logText, "\n")
	if len(lines) != 1 {
		t.Fatalf("SpeechPlan log lines = %d, want one structured observation: %q", len(lines), logOutput.String())
	}
	var observation map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &observation); err != nil {
		t.Fatalf("SpeechPlan observation is not structured JSON: %v; line=%q", err, lines[0])
	}
	for _, field := range []string{"turn_id", "speaker_id", "speech_plan_valid", "utterance_count", "cue_count", "llm_generation_ms", "plan_validation_ms", "fallback_reason"} {
		if _, ok := observation[field]; !ok {
			t.Errorf("SpeechPlan observation missing %q: %#v", field, observation)
		}
	}
	if observation["event"] != "speech.plan.observed" {
		t.Errorf("event = %#v, want speech.plan.observed", observation["event"])
	}
	if observation["turn_id"] != string(input.TurnID()) || observation["speaker_id"] != "mio" {
		t.Errorf("observation identity = turn=%#v speaker=%#v", observation["turn_id"], observation["speaker_id"])
	}
	if observation["speech_plan_valid"] != true || observation["utterance_count"] != float64(1) || observation["cue_count"] != float64(2) {
		t.Errorf("observation counts/status = valid:%#v utterances:%#v cues:%#v", observation["speech_plan_valid"], observation["utterance_count"], observation["cue_count"])
	}
	if observation["llm_generation_ms"] != float64(generationMS) || observation["plan_validation_ms"] != float64(validationMS) || observation["fallback_reason"] != "" {
		t.Errorf("observation measurements/fallback = generation:%#v validation:%#v fallback:%#v", observation["llm_generation_ms"], observation["plan_validation_ms"], observation["fallback_reason"])
	}
	for _, forbidden := range []string{privateText, "schema_version", "caption", "provider-secret"} {
		if strings.Contains(logOutput.String(), forbidden) {
			t.Errorf("SpeechPlan observation leaked %q", forbidden)
		}
	}
}

func TestMessageRouteSpeechPlanFailureObservationMarksValidationUnavailable(t *testing.T) {
	logOutput := captureSpeechPlanLogs(t)
	generationMS := int64(7)
	result := speech.Result{FallbackReason: speech.FallbackGenerationFailed, LLMGenerationMS: &generationMS}
	dispatcher := newMessageRouteDispatcher(
		&mockMioAgent{chatFunc: func(ctx context.Context, _ conversation.TurnInput) (string, error) {
			callback := domainagent.SpeechPlanCallbackFromContext(ctx)
			if callback == nil {
				t.Fatal("CHAT route did not install a SpeechPlan callback")
			}
			callback(result)
			return "", context.DeadlineExceeded
		}},
		&mockShiroAgent{}, nil, func(string, string, string, string, string, string, string, string, string) {},
		newMessageTTSLifecycle(&mockTTSBridge{}, nil, nil).WithStreamHooks,
		func(context.Context, string, routing.Route, string, string, string) {},
	)
	dispatcher.SetSpeechPlanEnabled(true)
	input := newOrchestratorTestTurnInput(t, "private input", "viewer", "viewer-user").WithSessionID("conversation-1").WithViewerRecipient("mio")
	if _, err := dispatcher.ExecuteTurnInput(context.Background(), input, routing.RouteCHAT, modulecore.NewTaskID(), modulecore.NewRunID(), "tts-1"); err == nil {
		t.Fatal("ExecuteTurnInput() error = nil, want provider failure")
	}

	var observation map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logOutput.Bytes()), &observation); err != nil {
		t.Fatalf("SpeechPlan failure observation is not structured JSON: %v; output=%q", err, logOutput.String())
	}
	if observation["event"] != "speech.plan.observed" || observation["speech_plan_valid"] != false || observation["fallback_reason"] != speech.FallbackGenerationFailed {
		t.Fatalf("failure observation = %#v", observation)
	}
	if observation["llm_generation_ms"] != float64(generationMS) || observation["plan_validation_ms"] != nil {
		t.Fatalf("failure measurements = generation:%#v validation:%#v", observation["llm_generation_ms"], observation["plan_validation_ms"])
	}
}

func TestMessageRouteDefaultsToLegacyChatTTS(t *testing.T) {
	const legacyResponse = "従来のCHAT応答です。"
	bridge := &mockTTSBridge{}
	var callbackWasInstalled bool
	planResult := speech.Result{
		Valid:       true,
		DisplayText: legacyResponse,
		Plan: speech.SpeechPlan{
			SchemaVersion: speech.SchemaVersion,
			Utterances:    []speech.Utterance{{Segments: []speech.Segment{{Text: legacyResponse}}}},
		},
	}
	dispatcher := newMessageRouteDispatcher(
		&mockMioAgent{chatFunc: func(ctx context.Context, _ conversation.TurnInput) (string, error) {
			callback := domainagent.SpeechPlanCallbackFromContext(ctx)
			callbackWasInstalled = callback != nil
			if callback != nil {
				callback(planResult)
				return planResult.DisplayText, nil
			}
			return legacyResponse, nil
		}},
		&mockShiroAgent{}, nil,
		func(string, string, string, string, string, string, string, string, string) {},
		newMessageTTSLifecycle(bridge, nil, nil).WithStreamHooks,
		func(context.Context, string, routing.Route, string, string, string) {},
	)
	input := newOrchestratorTestTurnInput(t, "返事して", "viewer", "viewer-user").
		WithSessionID("conversation-1").WithViewerRecipient("mio")

	got, err := dispatcher.ExecuteTurnInput(context.Background(), input, routing.RouteCHAT, modulecore.NewTaskID(), modulecore.NewRunID(), "tts-1")
	if err != nil {
		t.Fatalf("ExecuteTurnInput() error = %v", err)
	}
	if got != legacyResponse {
		t.Fatalf("response = %q, want %q", got, legacyResponse)
	}
	if callbackWasInstalled {
		t.Fatal("SpeechPlan callback was installed with the default-off activation setting")
	}
	if len(bridge.pushes) == 0 || strings.Join(bridge.pushes, "") != legacyResponse {
		t.Fatalf("legacy TTS pushes = %#v, want %q", bridge.pushes, legacyResponse)
	}
}

func TestMessageRouteHandsOffOnlyVoiceableSpeechPlan(t *testing.T) {
	validPlan := speech.Result{
		Valid:       true,
		DisplayText: "表示する本文。",
		Plan: speech.SpeechPlan{
			SchemaVersion: speech.SchemaVersion,
			Utterances: []speech.Utterance{{
				Segments: []speech.Segment{{Text: "表示する本文。"}},
				Delivery: speech.Delivery{Tone: "warm", Attitude: "gentle", Energy: "medium", Pace: "normal", Mode: "dialogue"},
			}},
		},
	}
	recoveredPlan := speech.Result{
		Valid:          true,
		DisplayText:    validPlan.DisplayText,
		FallbackReason: speech.FallbackUnknownField,
		Plan: speech.SpeechPlan{
			SchemaVersion: speech.SchemaVersion,
			Utterances: []speech.Utterance{{
				Segments:     []speech.Segment{{Text: validPlan.DisplayText}},
				Delivery:     speech.Delivery{Tone: "neutral", Attitude: "neutral", Energy: "medium", Pace: "normal", Mode: "dialogue"},
				PauseAfterMS: 0,
			}},
		},
	}
	invalidPlan := speech.Result{DisplayText: "不正な演技指定でも本文は表示する。", FallbackReason: speech.FallbackUnsupportedEmoji}

	for _, tc := range []struct {
		name               string
		plan               *speech.Result
		speechPlanEnabled  bool
		emitLegacyToken    bool
		wantLegacyTTS      bool
		wantPlanPush       bool
		rejectHandoff      bool
		wantPlanValid      bool
		wantFallbackReason string
	}{
		{name: "valid plan", plan: &validPlan, speechPlanEnabled: true, emitLegacyToken: true, wantPlanPush: true},
		{name: "recovered unknown-field plan", plan: &recoveredPlan, speechPlanEnabled: true, emitLegacyToken: true, wantPlanPush: true, wantPlanValid: true, wantFallbackReason: speech.FallbackUnknownField},
		{name: "gateway rejection does not fall back to legacy audio", plan: &validPlan, speechPlanEnabled: true, emitLegacyToken: true, wantPlanPush: true, rejectHandoff: true, wantPlanValid: true, wantFallbackReason: speechPlanHandoffFailed},
		{name: "invalid plan keeps display only", plan: &invalidPlan, speechPlanEnabled: true, emitLegacyToken: true, wantFallbackReason: speech.FallbackUnsupportedEmoji},
		{name: "disabled deterministic shortcut keeps legacy path", emitLegacyToken: true, wantLegacyTTS: true},
		{name: "enabled route without callback keeps display and fails closed", speechPlanEnabled: true, emitLegacyToken: true, wantFallbackReason: "speech_plan_callback_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logOutput := captureSpeechPlanLogs(t)
			bridge := &mockTTSBridge{}
			var events []OrchestratorEvent
			lifecycle := newMessageTTSLifecycle(bridge, nil, func(string, string, string, string, string, string, string, string, string) {})
			response := "shortcut response"
			if tc.plan != nil {
				response = tc.plan.DisplayText
			}
			dispatcher := newMessageRouteDispatcher(
				&mockMioAgent{chatFunc: func(ctx context.Context, _ conversation.TurnInput) (string, error) {
					if tc.emitLegacyToken {
						if stream := llm.StreamCallbackFromContext(ctx); stream != nil {
							stream("legacy stream token")
						}
					}
					if tc.plan != nil {
						callback := domainagent.SpeechPlanCallbackFromContext(ctx)
						if callback == nil {
							t.Fatal("CHAT route did not install typed SpeechPlan callback")
						}
						callback(*tc.plan)
					}
					return response, nil
				}},
				&mockShiroAgent{}, nil,
				func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {
					events = append(events, NewEvent(eventType, from, to, content, route, taskID, sessionID, channel, chatID))
				},
				lifecycle.WithStreamHooks,
				func(context.Context, string, routing.Route, string, string, string) {},
			)
			dispatcher.SetSpeechPlanEnabled(tc.speechPlanEnabled)
			planPushes := 0
			var wantTurnID string
			var taskID modulecore.TaskID
			dispatcher.SetSpeechPlanPusher(func(_ context.Context, gotTTSSession, gotConversationSession, gotTurnID, gotSpeaker string, gotPlan speech.SpeechPlan) error {
				planPushes++
				if gotTTSSession != "tts-1" || gotConversationSession != "conversation-1" || gotTurnID != wantTurnID || gotSpeaker != "mio" {
					t.Errorf("SpeechPlan handoff identity = tts=%q conversation=%q turn=%q speaker=%q", gotTTSSession, gotConversationSession, gotTurnID, gotSpeaker)
				}
				if err := modulecore.TurnID(gotTurnID).Validate(); err != nil {
					t.Errorf("SpeechPlan turn ID is not canonical: %v", err)
				}
				if gotTurnID == taskID.String() {
					t.Errorf("SpeechPlan turn ID incorrectly uses task ID %q", gotTurnID)
				}
				if len(gotPlan.Utterances) != 1 || gotPlan.Utterances[0].Segments[0].Text != "表示する本文。" {
					t.Errorf("SpeechPlan handoff = %#v", gotPlan)
				}
				if tc.wantFallbackReason == speech.FallbackUnknownField && gotPlan.Utterances[0].Delivery != (speech.Delivery{Tone: "neutral", Attitude: "neutral", Energy: "medium", Pace: "normal", Mode: "dialogue"}) {
					t.Errorf("recovered SpeechPlan handoff delivery = %#v, want neutral", gotPlan.Utterances[0].Delivery)
				}
				if tc.rejectHandoff {
					return errors.New("Gateway rejected SpeechPlan")
				}
				return nil
			})

			input := newOrchestratorTestTurnInput(t, "返事して", "viewer", "viewer-user").
				WithSessionID("conversation-1").WithViewerRecipient("mio")
			wantTurnID = string(input.TurnID())
			taskID = modulecore.NewTaskID()
			got, err := dispatcher.ExecuteTurnInput(context.Background(), input, routing.RouteCHAT, taskID, modulecore.NewRunID(), "tts-1")
			if err != nil {
				t.Fatalf("ExecuteTurnInput() error = %v", err)
			}
			if got != response {
				t.Fatalf("response = %q, want %q", got, response)
			}
			responseEvents := 0
			for _, event := range events {
				if event.Type != "agent.response" {
					continue
				}
				responseEvents++
				if event.Content != response {
					t.Errorf("agent.response content = %q, want projected response %q", event.Content, response)
				}
			}
			if responseEvents != 1 {
				t.Fatalf("agent.response event count = %d, want one", responseEvents)
			}
			if tc.wantPlanPush && planPushes != 1 {
				t.Fatalf("SpeechPlan pushes = %d, want 1", planPushes)
			}
			if !tc.wantPlanPush && planPushes != 0 {
				t.Fatalf("SpeechPlan pushes = %d, want 0", planPushes)
			}
			if tc.wantLegacyTTS && len(bridge.pushes) == 0 {
				t.Fatal("deterministic shortcut did not retain legacy TTS finalization")
			}
			if !tc.speechPlanEnabled && logOutput.Len() != 0 {
				t.Fatalf("non-SpeechPlan path emitted a SpeechPlan observation: %q", logOutput.String())
			}
			if tc.wantFallbackReason != "" {
				var observation map[string]any
				if err := json.Unmarshal(bytes.TrimSpace(logOutput.Bytes()), &observation); err != nil {
					t.Fatalf("SpeechPlan fallback observation is not structured JSON: %v; output=%q", err, logOutput.String())
				}
				if observation["fallback_reason"] != tc.wantFallbackReason || observation["speech_plan_valid"] != tc.wantPlanValid {
					t.Fatalf("SpeechPlan fallback observation = %#v", observation)
				}
			}
			if !tc.wantLegacyTTS && len(bridge.pushes) != 0 {
				t.Fatalf("SpeechPlan path leaked text into legacy TTS: %#v", bridge.pushes)
			}
		})
	}
}
