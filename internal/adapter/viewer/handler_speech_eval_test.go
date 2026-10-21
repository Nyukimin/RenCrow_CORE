package viewer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleSpeechEvaluationPageServesDedicatedWorkbench(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleSpeechEvaluationPage(rec, httptest.NewRequest(http.MethodGet, "/viewer/tts/evaluation", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q want=no-store", got)
	}
	body := rec.Body.String()
	for _, expected := range []string{
		"<title>SpeechPlan A/B 音声評価 | RenCrow</title>",
		"/viewer/assets/css/speech_eval.css",
		"/viewer/assets/js/speech_eval.js",
		"id=\"speechEvalPair\"",
		"id=\"speechEvalPreference\"",
		"id=\"speechEvalCueHint\"",
		"固定のSpeechPlanテスト用20行",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("speech evaluation HTML missing %q", expected)
		}
	}
}

func TestHandleSpeechEvaluationAssetsAreEmbedded(t *testing.T) {
	for _, path := range []string{
		"/viewer/assets/js/speech_eval.js",
		"/viewer/assets/css/speech_eval.css",
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			HandleAsset(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if rec.Body.Len() == 0 {
				t.Fatal("embedded asset is empty")
			}
		})
	}
}
