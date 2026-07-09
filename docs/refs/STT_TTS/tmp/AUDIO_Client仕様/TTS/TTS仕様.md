# TTS仕様（AUDIO_Client仕様）

## 1. 目的
本仕様は、**Chat から見た TTS 接続契約・呼び出し手順**をベンダー非依存で定義する。  
特定実装（例: SBV2, Irodori）は本仕様に対する適用例として扱う。

## 2. 基本方針
- TTS は **必ず Chat サーバ経由**で利用する。
- クライアントが TTS Provider へ直接接続しない。
- Chat は Bridge API または Direct API を利用する。
- 音声は **単発だけでなく複数本（複数チャンク/複数トラック）** を前提とする。

## 3. 接続構成
```text
[Client/Browser]
  -> [Chat Server]
    -> [TTS Gateway]
      -> [TTS Provider]
```

## 4. 利用モード
- Bridgeモード（本線）
  - `GET /health/ready`
  - `POST /synthesize`
  - `WS /sessions`
- Directモード（補助）
  - `POST /synthesis`

## 5. Chat 側呼び出し手順（Bridge）
1. `GET /health/ready` で利用可否確認。  
2. `WS /sessions` を開始。  
3. `session_start -> text_delta* -> session_end` を送信。  
4. `audio_chunk_ready` を順序キューへ投入。  
5. `session_completed` で終了。

## 6. Chat 側呼び出し手順（Direct）
1. `/synthesis` へテキストと音声パラメータを送信。  
2. `audio_path` または `audio_url` を受け取り再生経路へ渡す。

## 7. 複数本音声の扱い（Client観点）
- 1レスポンス内で複数 `audio_chunk_ready` を受ける前提で処理する。
- `chunk_index` と `track` で再生順を確定する。
- 一部チャンク失敗時はセッション全体停止ではなく継続/スキップ可能にする。

## 8. エラー契約（Client観点）
- `ready` 不可時はフォールバック（テキストのみ等）へ切替。
- WS中断時はセッション破棄後、再接続またはHTTP fallbackへ切替。
- サーバ `error` はチャンク単位失敗として処理可能にする。

## 9. 運用要件
- 接続先設定は Chat 側で一元管理（`tts.http_base_url`, `tts.ws_url`, `tts.provider.base_url`）。
- timeout を用途別に設定（接続/受信/生成）。
- `audio_url` 返却対応を推奨（リモート再生安定化）。
- 接続前に `GET /health/live` と `GET /health/ready` で到達性を確認する。

## 10. 実装差分注記
- Provider固有APIのみでは Bridge契約と一致しない場合がある。
- Chat側は Adapter で差分吸収するか、Server側でBridge契約を提供する。

## 11. 実装例（現行採用）
- 現行例: SBV2
- 将来候補: Irodori 等

## 12. 検証コマンド例の参照
- 検証コマンド例は Server 側正本 `docs/STT_TTS/AUDIO_Server仕様/TTS/TTS仕様.md` の「検証コマンド例」節を利用する。

## 13. 現行実測メモ（最新版）
- Chat -> TTS Gateway（例）: `http://192.168.1.36:8765`
  - `GET /health/live` -> `{"status":"live"}`
  - `GET /health/ready` -> `{"status":"ready","voices":["female_01","mio"],"device":"cpu", ...}`
  - `POST /synthesis` -> `{"audio_path":"...","duration_ms":...,"voice_id":"female_01"}`
  - `POST /synthesize` -> `{"text":"...","audio_path":"...","audio_url":"..."}`

本節は運用実測値であり、契約本体は本書の各節（Provider非依存）を正とする。
