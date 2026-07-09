package orchestrator

import (
	"testing"

	autonomousapp "github.com/Nyukimin/RenCrow_CORE/internal/application/autonomous"
	domaincontract "github.com/Nyukimin/RenCrow_CORE/internal/domain/contract"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

// --- looksLikeNonExecutable ---

func TestLooksLikeNonExecutable_DesignDocOnly(t *testing.T) {
	// 設計文のみ → 非実行出力と判定
	response := "# 設計文書\n\nこれはシステムの設計についての説明です。\n\n## 概要\n\nアーキテクチャの概要を示します。"
	if !looksLikeNonExecutable(response) {
		t.Error("設計文のみのレスポンスは非実行出力と判定されるべき")
	}
}

func TestLooksLikeNonExecutable_HasCodeBlock(t *testing.T) {
	// コードブロックあり → 実行可能
	response := "修正内容:\n\n```go\nfunc foo() {}\n```"
	if looksLikeNonExecutable(response) {
		t.Error("コードブロックを含むレスポンスは実行可能と判定されるべき")
	}
}

func TestLooksLikeNonExecutable_HasPatchMarker(t *testing.T) {
	// patch: マーカーあり → 実行可能
	response := "patch: internal/foo.go\n--- a/foo.go\n+++ b/foo.go"
	if looksLikeNonExecutable(response) {
		t.Error("patch: マーカーを含むレスポンスは実行可能と判定されるべき")
	}
}

func TestLooksLikeNonExecutable_HasShebang(t *testing.T) {
	// シェバンあり → 実行可能
	response := "#!/bin/bash\necho hello"
	if looksLikeNonExecutable(response) {
		t.Error("シェバンを含むレスポンスは実行可能と判定されるべき")
	}
}

func TestLooksLikeNonExecutable_HasShellCommand(t *testing.T) {
	// シェルコマンドあり → 実行可能
	response := "実行してください:\n$ go build ./...\n$ ./bin/server"
	if looksLikeNonExecutable(response) {
		t.Error("シェルコマンドを含むレスポンスは実行可能と判定されるべき")
	}
}

// --- isTTSCapability ---

func TestIsTTSCapability_WithRealPlayback(t *testing.T) {
	// 実再生を含む Acceptance → TTS 判定
	c := domaincontract.Contract{
		Goal:       "音声ファイルを再生する",
		Acceptance: []string{"実再生成功", "音声ファイルが生成された"},
	}
	if !isTTSCapability(c) {
		t.Error("実再生 を含む Acceptance は TTS CapabilityPack と判定されるべき")
	}
}

func TestIsTTSCapability_WithAudioFileGeneration(t *testing.T) {
	c := domaincontract.Contract{
		Goal:       "TTSを実行",
		Acceptance: []string{"音声ファイル生成が完了した"},
	}
	if !isTTSCapability(c) {
		t.Error("音声ファイル生成 を含む Acceptance は TTS CapabilityPack と判定されるべき")
	}
}

func TestIsTTSCapability_WithoutTTS(t *testing.T) {
	// TTS 関連キーワードなし → false
	c := domaincontract.Contract{
		Goal:       "コードを修正する",
		Acceptance: []string{"ビルドが通る", "テストが通る"},
	}
	if isTTSCapability(c) {
		t.Error("TTS 関連キーワードがない Acceptance は false を返すべき")
	}
}

// --- verifyByContract ---

func TestVerifyByContract_EmptyResponse(t *testing.T) {
	// 全ルート共通: 空レスポンス拒否
	c := domaincontract.Contract{Goal: "コードを修正"}
	last := autonomousapp.AttemptResult{Response: "   "}
	ok, kind, _ := verifyByContract(routing.RouteOPS, c, last)
	if ok {
		t.Error("空レスポンスは拒否されるべき")
	}
	if kind != "verification_failed" {
		t.Errorf("failureKind = %q, want verification_failed", kind)
	}
}

func TestVerifyByContract_CodeRoute_NonExecutable(t *testing.T) {
	// Coder が設計文のみ返した場合 → non_executable_output で reject
	c := domaincontract.Contract{Goal: "コードを修正"}
	last := autonomousapp.AttemptResult{
		Response: "# 設計文書\n\nシステムの設計についての説明です。\n\n変更点を検討中です。",
	}
	ok, kind, _ := verifyByContract(routing.RouteCODE, c, last)
	if ok {
		t.Error("設計文のみのレスポンスは reject されるべき")
	}
	if kind != "non_executable_output" {
		t.Errorf("failureKind = %q, want non_executable_output", kind)
	}
}

func TestVerifyByContract_CodeRoute_Executable(t *testing.T) {
	// コードブロックを含む場合 → pass
	c := domaincontract.Contract{Goal: "コードを修正"}
	last := autonomousapp.AttemptResult{
		Response: "修正内容:\n\n```go\nfunc foo() string { return \"bar\" }\n```",
	}
	ok, kind, _ := verifyByContract(routing.RouteCODE, c, last)
	if !ok {
		t.Errorf("コードブロックを含むレスポンスは通過すべき (kind=%q)", kind)
	}
}

func TestVerifyByContract_CodeRoute_FailureKeyword(t *testing.T) {
	// "エラー" / "失敗" を含む場合 → verification_failed
	c := domaincontract.Contract{Goal: "コードを修正"}
	last := autonomousapp.AttemptResult{
		Response: "```go\n// コード\n```\n\nエラー: パッケージが見つかりません",
	}
	ok, kind, _ := verifyByContract(routing.RouteCODE, c, last)
	if ok {
		t.Error("失敗キーワードを含むレスポンスは reject されるべき")
	}
	if kind != "verification_failed" {
		t.Errorf("failureKind = %q, want verification_failed", kind)
	}
}

func TestVerifyByContract_TTS_PlaybackSuccess(t *testing.T) {
	// PlaybackCode=0, TTSAudioFile あり → pass
	c := domaincontract.Contract{
		Goal:       "音声を再生",
		Acceptance: []string{"実再生成功"},
	}
	last := autonomousapp.AttemptResult{
		Response:     "音声ファイルを生成しました",
		TTSAudioFile: "/tmp/audio.wav",
		PlaybackCode: 0,
	}
	ok, kind, _ := verifyByContract(routing.RouteOPS, c, last)
	if !ok {
		t.Errorf("PlaybackCode=0 かつ TTSAudioFile あり → 通過すべき (kind=%q)", kind)
	}
}

func TestVerifyByContract_TTS_PlaybackFailed(t *testing.T) {
	// PlaybackCode=1 → playback_failed
	c := domaincontract.Contract{
		Goal:       "音声を再生",
		Acceptance: []string{"実再生成功"},
	}
	last := autonomousapp.AttemptResult{
		Response:     "音声ファイルを生成しました",
		TTSAudioFile: "/tmp/audio.wav",
		PlaybackCode: 1,
	}
	ok, kind, _ := verifyByContract(routing.RouteOPS, c, last)
	if ok {
		t.Error("PlaybackCode!=0 は reject されるべき")
	}
	if kind != "playback_failed" {
		t.Errorf("failureKind = %q, want playback_failed", kind)
	}
}

func TestVerifyByContract_TTS_NoAudio(t *testing.T) {
	// TTSAudioFile="" かつ PlaybackCode!=0 → tts_no_audio
	c := domaincontract.Contract{
		Goal:       "音声を再生",
		Acceptance: []string{"実再生成功"},
	}
	last := autonomousapp.AttemptResult{
		Response:     "処理しました",
		TTSAudioFile: "",
		PlaybackCode: 1,
	}
	ok, kind, _ := verifyByContract(routing.RouteOPS, c, last)
	if ok {
		t.Error("TTSAudioFile が空の場合は reject されるべき")
	}
	if kind != "tts_no_audio" {
		t.Errorf("failureKind = %q, want tts_no_audio", kind)
	}
}

func TestVerifyByContract_TTS_Fallback(t *testing.T) {
	// TTSAudioFile="" かつ PlaybackCode=0 (フィールド未設定) → フォールバック (string check)
	c := domaincontract.Contract{
		Goal:       "音声を再生",
		Acceptance: []string{"実再生成功"},
	}
	last := autonomousapp.AttemptResult{
		Response:     "音声処理を完了しました",
		TTSAudioFile: "",
		PlaybackCode: 0,
	}
	ok, _, _ := verifyByContract(routing.RouteOPS, c, last)
	if !ok {
		t.Error("フィールド未設定のフォールバック: 正常レスポンスは通過すべき")
	}
}

func TestVerifyByContract_OPS_Normal(t *testing.T) {
	// OPS ルート、正常レスポンス → pass
	c := domaincontract.Contract{Goal: "コマンドを実行"}
	last := autonomousapp.AttemptResult{
		Response: "コマンドの実行が完了しました。出力: done",
	}
	ok, kind, _ := verifyByContract(routing.RouteOPS, c, last)
	if !ok {
		t.Errorf("正常レスポンスは通過すべき (kind=%q)", kind)
	}
}

func TestVerifyByContract_OPS_Failure(t *testing.T) {
	// OPS ルート、"エラー" を含む → verification_failed
	c := domaincontract.Contract{Goal: "コマンドを実行"}
	last := autonomousapp.AttemptResult{
		Response: "エラー: コマンドが見つかりません",
	}
	ok, kind, _ := verifyByContract(routing.RouteOPS, c, last)
	if ok {
		t.Error("失敗キーワードを含む OPS レスポンスは reject されるべき")
	}
	if kind != "verification_failed" {
		t.Errorf("failureKind = %q, want verification_failed", kind)
	}
}
