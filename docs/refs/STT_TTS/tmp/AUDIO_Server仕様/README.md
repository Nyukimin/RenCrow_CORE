# AUDIO_Server仕様（実装直結）

本ディレクトリは、**STT/TTS サーバが提供する API・挙動・運用要件**を定義する。  
実装者は API 契約 -> 実装仕様 -> 受け入れ基準の順で反映する。

## 1. 実装対象

### STT
- `STT/STT_Server側_現状サマリ.md`（最初に読む要約）
- `STT/STT仕様.md`
- `STT/STT_API.md`
- `STT/STT実装仕様.md`
- `STT/STT_ノイズ抑制・誤り訂正仕様.md`
- `STT/STT_既知良好WAV_分離テスト結果_2026-04-10.md`（再起動トラブル時の切り分け実績）

### TTS
- `TTS/TTS仕様.md`
- `TTS/TTS_API.md`
- `TTS/TTS実装仕様.md`

## 2. 実装手順（Server側）
1. `*_API.md` を一次契約としてエンドポイント実装
2. `*実装仕様.md` で状態遷移・エラー処理・監視項目を実装
3. Provider Adapter を実装し、固有差分を吸収
4. STT/TTS ともにベンダー非依存契約を維持
5. TTS は複数本音声（multi_chunk/multi_track）を前提に実装

## 2.1 起動運用（最新）
- Windows運用の既定起動:
  - STTのみ: `start-stt.ps1`
  - STT+TTS: `start-stt-tts.ps1`
- 上記スクリプトは起動後に self-test/health を実行し、READY確認を表示する

## 3. 実装ルール
- 契約本文は `Provider` 抽象名で記述
- Whisper/SBV2/Irodori は実装例セクションでのみ記述
- timeout・retry・error code を固定し、fail-safe を優先

## 4. API-DOD（Server側）
- `STT_API.md`: `API-DOD-STT-S-*`
- `TTS_API.md`: `API-DOD-TTS-S-*`
- 実装PRでは、該当IDのチェック結果を記録する

## 5. Server側 DoD
- Health/Bridge/Direct の契約を満たす
- 非2xx/timeout/例外で継続可能な失敗処理を実装
- ログに request/session/chunk/track 単位の追跡情報がある
- Client仕様の必須フィールドと齟齬がない

## 6. レビュー記録
- 各 `*_API.md` の API-DOD 節にある検証コマンド例で実施する
- 実行証跡（ログ/レスポンス/URL）を `docs/STT_TTS/API_DOD_CHECKLIST.md` に集約する
