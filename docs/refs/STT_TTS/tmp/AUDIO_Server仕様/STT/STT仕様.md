# STT仕様（AUDIO_Server仕様）

## 1. 目的
本仕様は、**STTサーバが提供する機能・挙動・運用要件**をベンダー非依存で定義する。  
特定実装（例: Whisper）は本仕様に対する適用例として扱う。

## 2. スコープ
- 対象: STT Gateway（WebSocket受け口）と STT Provider（推論エンジン）連携
- 非対象: 音声認識モデルの学習・再学習

## 3. 基本方針
- STT は Chat サーバ経由で利用する。
- STT サーバは `draft` と `final` を提供する。
- 障害時は fail-safe を優先し、全体停止を避ける。

## 4. 論理構成
```text
[Client/Browser]
  -> [STT Gateway]
    -> [STT Provider]
```

## 5. 提供責務
- Browserから音声を受信し、発話をテキスト化する。
- 発話中は `draft`、発話確定時は `final` を返す。
- 不正入力/外部障害に対して、継続可能な失敗処理を実施する。

## 6. 挙動要件
- 発話開始検知時: `speech_start` を返す。
- 発話中: 定期的に `draft` を返す。
- 発話終了時: `final` を返す。
- busy中は再入制御（破棄/待機のいずれか）を適用する。

## 7. 接続要件
- STT Gateway の待受は設定値で管理する（例: `STT_PORT=8090`）。
- STT Provider 接続先は設定値で管理する（例: `STT_PROVIDER_URL`）。
- 既定値はローカル接続を推奨（例: `http://127.0.0.1:8080/inference`）。
- リモート構成では FW 許可・到達性・RTT を確認する。

## 8. エラー要件
- Provider HTTP非2xx: WARNログ + 継続可能な失敗として処理。
- 制御メッセージ不正: `error` を返却。
- timeout: 該当発話のみ失敗扱いとして後続処理を継続。

## 9. 運用要件
- 起動順序: STT Provider -> STT Gateway
- ヘルス: `GET /health` を提供
- timeout/再試行回数は固定管理し、過剰再試行を避ける
- Windows運用では起動スクリプト（`start-stt.ps1` / `start-stt-tts.ps1`）で self-test を実行し、READY確認後のみ運用投入する

## 10. 実装例（現行採用）
現行では以下を採用している。
- STT Gateway: `voice-bridge`
- STT Provider: `whisper.cpp whisper-server`
- Provider API: `POST /inference` + `multipart/form-data`
- 起動補助: `start-stt.ps1`（STTのみ）, `start-stt-tts.ps1`（STT+TTS）

この実装例は置換可能であり、上記 3〜9 の契約を満たす限り他STTへ差し替えできる。
