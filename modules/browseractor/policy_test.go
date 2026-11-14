package browseractor

import (
	"encoding/json"
	"testing"
)

func TestClassifyRisk(t *testing.T) {
	cases := []struct {
		name string
		req  RunRequest
		want string
	}{
		{name: "read only", req: RunRequest{Actions: []Action{{Type: "open"}, {Type: "snapshot"}}}, want: RiskReadOnly},
		{name: "draft input", req: RunRequest{Actions: []Action{{Type: "open"}, {Type: "fill", Selector: "#name", Value: "x"}}}, want: RiskDraftInput},
		{name: "navigation", req: RunRequest{Actions: []Action{{Type: "open"}, {Type: "click", Selector: "#next"}}}, want: RiskNavigation},
		{name: "submit keyword", req: RunRequest{Actions: []Action{{Type: "click", Selector: "#send"}}}, want: RiskExternalEffect},
		{name: "enter key", req: RunRequest{Actions: []Action{{Type: "press", Key: "Enter"}}}, want: RiskExternalEffect},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyRisk(tc.req)
			if got.Risk != tc.want {
				t.Fatalf("risk=%s want=%s decision=%+v", got.Risk, tc.want, got)
			}
		})
	}
}

func TestValidateRunRequest(t *testing.T) {
	req := NormalizeRunRequest(RunRequest{
		RunID:    "run_1",
		StartURL: "http://127.0.0.1:18790/viewer",
		Actions:  []Action{{Type: "open"}},
	})
	if err := ValidateRunRequest(req); err != nil {
		t.Fatalf("ValidateRunRequest returned error: %v", err)
	}
	req.Actions = []Action{{Type: "unknown"}}
	if err := ValidateRunRequest(req); err == nil {
		t.Fatal("expected unsupported action error")
	}
}

func TestMaskSecrets(t *testing.T) {
	got := MaskSecrets("Authorization: Bearer abc Cookie: session=secret password=hidden")
	if got == "Authorization: Bearer abc Cookie: session=secret password=hidden" {
		t.Fatalf("expected masked output, got %q", got)
	}
	if got == "" || got == Mask {
		t.Fatalf("unexpected masked output: %q", got)
	}
}

func TestValidatePlayMediaActionAndContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action Action
		valid  bool
	}{
		{name: "video chunk", action: Action{Type: "play_media", Value: "video", StartMS: 1200, MaxPlayMS: 12000}, valid: true},
		{name: "audio chunk", action: Action{Type: "play_media", Value: "audio", MaxPlayMS: 30000}, valid: true},
		{name: "unknown media kind", action: Action{Type: "play_media", Value: "film", MaxPlayMS: 1000}},
		{name: "unbounded chunk", action: Action{Type: "play_media", Value: "audio", MaxPlayMS: 30001}},
		{name: "nonpositive chunk", action: Action{Type: "play_media", Value: "audio"}},
		{name: "negative offset", action: Action{Type: "play_media", Value: "audio", StartMS: -1, MaxPlayMS: 1000}},
		{name: "selector forbidden", action: Action{Type: "play_media", Selector: "#player", Value: "video", MaxPlayMS: 1000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAction(tc.action)
			if tc.valid && err != nil {
				t.Fatalf("ValidateAction: %v", err)
			}
			if !tc.valid && err == nil {
				t.Fatal("ValidateAction unexpectedly accepted action")
			}
		})
	}

	encoded, err := json.Marshal(Action{Type: "play_media", Value: "video", StartMS: 10, MaxPlayMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	var actionJSON map[string]any
	if err := json.Unmarshal(encoded, &actionJSON); err != nil {
		t.Fatal(err)
	}
	if _, exists := actionJSON["selector"]; exists {
		t.Fatalf("empty selector should be omitted by CORE json tags: %s", encoded)
	}
	if actionJSON["start_ms"] != float64(10) || actionJSON["max_play_ms"] != float64(1000) {
		t.Fatalf("playback offsets are missing from action contract: %s", encoded)
	}

	var result ActionResult
	if err := json.Unmarshal([]byte(`{"type":"play_media","status":"completed","playback":{"kind":"video","elapsed_milliseconds":12000,"media_time_milliseconds":12000,"duration_milliseconds":30000,"ended":false,"source_url":"https://media.example.invalid/clip.mp4","video_width":640,"video_height":360,"presented_frame_count":12}}`), &result); err != nil {
		t.Fatalf("decode playback result: %v", err)
	}
	if result.Playback == nil || result.Playback.Kind != "video" || result.Playback.ElapsedMilliseconds != 12000 ||
		result.Playback.SourceURL != "https://media.example.invalid/clip.mp4" || result.Playback.PresentedFrameCount == nil || *result.Playback.PresentedFrameCount != 12 {
		t.Fatalf("typed playback evidence = %+v", result.Playback)
	}
}
