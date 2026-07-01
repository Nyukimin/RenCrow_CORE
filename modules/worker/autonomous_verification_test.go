package worker

import "testing"

func TestLooksLikeNonExecutable(t *testing.T) {
	if !LooksLikeNonExecutable("# 設計文書\n\nこれはシステムの設計についての説明です。") {
		t.Fatal("design document only should be non-executable")
	}
	for _, response := range []string{
		"```go\nfunc foo() {}\n```",
		"patch: internal/foo.go\n--- a/foo.go",
		"#!/bin/bash\necho hello",
		"$ go build ./...",
	} {
		if LooksLikeNonExecutable(response) {
			t.Fatalf("response should be executable: %q", response)
		}
	}
}

func TestIsTTSCapability(t *testing.T) {
	if !IsTTSCapability(AutonomousContract{Acceptance: []string{"実再生成功"}}) {
		t.Fatal("real playback acceptance should be TTS capability")
	}
	if !IsTTSCapability(AutonomousContract{Acceptance: []string{"音声ファイル生成が完了した"}}) {
		t.Fatal("audio file generation acceptance should be TTS capability")
	}
	if IsTTSCapability(AutonomousContract{Acceptance: []string{"ビルドが通る"}}) {
		t.Fatal("non-TTS acceptance should not be TTS capability")
	}
}

func TestVerifyAutonomousAttempt(t *testing.T) {
	tests := []struct {
		name     string
		route    string
		contract AutonomousContract
		last     AutonomousAttemptResult
		ok       bool
		kind     string
	}{
		{
			name:  "empty response",
			route: "OPS",
			last:  AutonomousAttemptResult{Response: "   "},
			ok:    false,
			kind:  "verification_failed",
		},
		{
			name:  "code non executable",
			route: "CODE",
			last:  AutonomousAttemptResult{Response: "# 設計文書\n\n変更点を検討中です。"},
			ok:    false,
			kind:  "non_executable_output",
		},
		{
			name:  "code executable",
			route: "CODE",
			last:  AutonomousAttemptResult{Response: "```go\nfunc foo() string { return \"bar\" }\n```"},
			ok:    true,
		},
		{
			name:  "code failure keyword",
			route: "CODE",
			last:  AutonomousAttemptResult{Response: "```go\n// code\n```\n\nエラー: パッケージが見つかりません"},
			ok:    false,
			kind:  "verification_failed",
		},
		{
			name:     "tts playback success",
			route:    "OPS",
			contract: AutonomousContract{Acceptance: []string{"実再生成功"}},
			last:     AutonomousAttemptResult{Response: "音声ファイルを生成しました", TTSAudioFile: "/tmp/audio.wav"},
			ok:       true,
		},
		{
			name:     "tts playback failed",
			route:    "OPS",
			contract: AutonomousContract{Acceptance: []string{"実再生成功"}},
			last:     AutonomousAttemptResult{Response: "音声ファイルを生成しました", TTSAudioFile: "/tmp/audio.wav", PlaybackCode: 1},
			ok:       false,
			kind:     "playback_failed",
		},
		{
			name:     "tts no audio",
			route:    "OPS",
			contract: AutonomousContract{Acceptance: []string{"実再生成功"}},
			last:     AutonomousAttemptResult{Response: "処理しました", PlaybackCode: 1},
			ok:       false,
			kind:     "tts_no_audio",
		},
		{
			name:  "ops normal",
			route: "OPS",
			last:  AutonomousAttemptResult{Response: "コマンドの実行が完了しました。出力: done"},
			ok:    true,
		},
		{
			name:  "ops failure",
			route: "OPS",
			last:  AutonomousAttemptResult{Response: "エラー: コマンドが見つかりません"},
			ok:    false,
			kind:  "verification_failed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, kind, _ := VerifyAutonomousAttempt(tt.route, tt.contract, tt.last)
			if ok != tt.ok || kind != tt.kind {
				t.Fatalf("got ok=%t kind=%q, want ok=%t kind=%q", ok, kind, tt.ok, tt.kind)
			}
		})
	}
}

func TestResponseLooksLikeFailureAllowsZeroFailures(t *testing.T) {
	if ResponseLooksLikeFailure("失敗: 0 件") {
		t.Fatal("zero failures should not be classified as failure")
	}
	if !ResponseLooksLikeFailure("Error: command failed") {
		t.Fatal("error keyword should be classified as failure")
	}
}

func TestShortFailureReason(t *testing.T) {
	long := "1234567890"
	for len(long) <= 170 {
		long += "1234567890"
	}
	got := ShortFailureReason(long)
	if len(got) != 160 || got[157:] != "..." {
		t.Fatalf("short reason len=%d suffix=%q", len(got), got[157:])
	}
}
