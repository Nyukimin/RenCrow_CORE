# STT 正本仕様

**対応仕様**: 仕様.md §12-f
**最終更新**: 2026-07-01

## 1. 目的と適用範囲

本仕様は RenCrow における STT（Speech-to-Text）連携の正本仕様を定義する。
対象は次の経路とする。

- Viewer 音声入力主経路: `Browser -> /stt -> RenCrow_STT / STT provider -> STT final -> /viewer/send`
- Voice Direct / input_audio 経路: `Browser -> /voice-chat -> RenCrow_LLM input_audio -> ProcessVoiceDirect`
- CLI 音声ファイル経路: `rencrow chat --audio -> /stt/chat-input -> /viewer/send`
- サーバ間連携: `RenCrow Core -> RenCrow_STT / STT provider`（WebSocket または HTTP）

本仕様は STT を中心に定義し、TTS / Viewer 同期は `docs/01_正本仕様/15_TTS_Viewer同期.md` を参照する。
Chat surface / target agent / provider alias の境界は `docs/01_正本仕様/19_エージェント責務と起点境界.md` を優先する。

## 2. 現状整理（2026-07時点）

- Viewer フロントには STT クライアント実装があり、同一 origin の `/stt` へ WebSocket 接続する。
- `/stt-ws` と `/ws` は互換 alias として残すが、正本上の主経路は `/stt` である。
- STT final は Chat surface の入口違いである `voice_chat` として扱う。
- STT final の通常会話 target agent は Mio など現在の target agent であり、provider alias は Chat を優先する。
- Voice Direct / `vds_sub` は LLM input_audio による音声会話経路だが、Viewer から見える会話 surface は同じ `voice_chat` である。
- CLI `--audio` は `/stt/chat-input` で文字起こし後、Viewer と同じ `/viewer/send` 契約へ送る。
- Whisper / RenCrow_STT / STT provider はサーバ側 dependency であり、ブラウザから Whisper 直接呼び出しは主経路ではない。

上記より、STT は「音声をテキスト化する adapter」であり、Chat / Worker / Coder の責務判断そのものではない。

## 3. アーキテクチャ

### 3.1 論理構成

```text
[Browser]
  - mic capture
  - send PCM16 chunks via WebSocket (/stt)
            |
            v
[RenCrow Core / STT bridge]
  - same-origin WebSocket endpoint
  - RenCrow_STT / provider への中継
            |
            v
[RenCrow_STT / STT provider]
  - VAD
  - Whisper API client
  - draft/final event emit
            |
            v
[Viewer Chat adapter]
  - STT final を通常入力へ反映
  - /viewer/send で Chat surface へ送信
            |
            v
[MessageOrchestrator]
  - target_agent=Mio など
  - route=CHAT
  - provider_alias=Chat
```

Voice Direct / input_audio 経路は次である。

```text
[Browser]
  - mic capture
  - send PCM16 chunks via WebSocket (/voice-chat)
            |
            v
[RenCrow Core / voice_chat bridge]
  - RenCrow_LLM input_audio へ送信
  - llm.final を ProcessVoiceDirect へ渡す
            |
            v
[MessageOrchestrator]
  - surface=voice_chat
  - target_agent=Mio
  - route=CHAT
  - provider_alias=Chat
```

Whisper / STT provider の内部構成例:

```text
[RenCrow_STT / STT provider]
  - VAD / chunk handling
            |
            v
[Whisper / STT engine]
  - POST /inference
  - return transcript
```

### 3.2 境界責務

- Browser
  - マイク入力取得
  - PCM/WAV 変換
  - `/stt` または `/voice-chat` への送信
  - draft/final 表示
- RenCrow Core / STT bridge
  - 同一 origin の `/stt` を提供
  - Provider / Gateway の違いを Viewer から隠蔽
  - `draft` / `final` / `error` イベント返却
- RenCrow Core / voice_chat bridge
  - `/voice-chat` を提供
  - LLM input_audio final を `voice_chat` surface の Chat event へ正規化
- RenCrow_STT / STT provider
  - STT セッション管理
  - VAD（発話区間判定）
  - Whisper 呼び出し
  - `draft`/`final`/`error` イベント返却
- Whisper
  - 音声認識エンジンとしてテキスト化を実行
- MessageOrchestrator
  - STT final / voice direct final を Chat surface の会話入力として扱う
  - target agent と provider alias を通常 Chat と同じ規則で扱う

## 4. 通信契約

### 4.1 Browser -> RenCrow Core STT bridge（WebSocket）

- 主エンドポイント: `/stt`
- 互換エンドポイント: `/stt-ws`, `/ws`
- プロトコル:
  - 初期設定メッセージ（JSON）
    - 例: `{ "type": "start", "sample_rate": 16000, "channels": 1, "format": "pcm_s16le" }`
  - 音声データ（binary）
    - PCM16 chunk（16kHz / mono 推奨）
  - 停止制御（JSON）
    - 例: `{ "type": "stop" }`

### 4.2 RenCrow Core STT bridge -> Browser（WebSocket event）

- `draft`
  - 途中認識テキスト
  - 例: `{ "type": "draft", "text": "..." }`
- `final`
  - 確定テキスト
  - 例: `{ "type": "final", "text": "..." }`
- `error`
  - 認識/接続エラー
  - 例: `{ "type": "error", "error": "..." }`

### 4.3 Browser -> RenCrow Core voice_chat bridge（WebSocket）

- 主エンドポイント: `/voice-chat`
- 互換エンドポイント: `/voice-chat-ws`
- プロトコル:
  - `session.start`
  - PCM16 binary chunk
  - `session.commit`
  - `llm.delta` / `llm.final` / `error`

`llm.final` は `ProcessVoiceDirect` で Chat SSE event に正規化される。
この event は `surface=voice_chat`、`target_agent=mio`、`provider_alias=Chat` の意味を持つ。
transport 証跡として `voice_direct` を残してよいが、surface 名としては `voice_chat` を使う。

### 4.4 RenCrow Core / RenCrow_STT -> Whisper（HTTP）

- エンドポイント: `POST /inference`
- 接続先: `WHISPER_URL` 環境変数で指定
- 既定例:
  - ローカル: `http://127.0.0.1:8080/inference`
  - リモート: `http://<whisper-host>:8080/inference`

### 4.5 CLI 音声ファイル入力

CLI からの音声ファイルは次の順で処理する。

```text
rencrow chat --audio voice.wav
  -> POST /stt/chat-input
  -> transcript text
  -> POST /viewer/send
  -> Chat surface
```

CLI は external channel の入口であるが、音声ファイルを文字起こしして通常 Chat に流す点では `voice_chat` と同じ adapter family として扱う。
`--audio-direct` は STT を skip して Chat LLM input_audio へ直接渡す別モードであり、STT final の検証には使わない。

## 5. 設定仕様

### 5.1 環境変数

- `WHISPER_URL`
  - voice-bridge が呼び出す Whisper URL
  - 起動前に設定する

### 5.2 ネットワーク要件

- Whisper サーバは `0.0.0.0:8080` 待受を許可する。
- Whisper サーバ側ファイアウォールで `TCP 8080` 受信を許可する。
- Tailscale/VPN 利用時は到達可能なホスト名またはIPを `WHISPER_URL` に設定する。

## 6. 非推奨構成

- Browser から Whisper への直接 `fetch`
  - CORS/Mixed Content 課題が増えるため非推奨。
- 公開インターネットへの 8080 直接公開
  - TLS 終端や認証がない構成は不可。

## 7. 実装方針（段階）

### Phase 1: 接続確立

- `WHISPER_URL` をリモートWhisperへ向ける。
- `/stt` 経由で認識結果（`draft`/`final`）が返ることを確認する。

### Phase 2: ドメイン統合

- `Transcriber` インターフェースへ STT 入力を統合する。
- STT 確定テキストを `voice_chat` surface の Chat 入力として接続する。
- Viewer STT final は `/viewer/send` 経由で通常の `ProcessMessage` に入る。
- Voice Direct final は `ProcessVoiceDirect` 経由で Chat SSE event に正規化される。

### Phase 3: 運用強化

- タイムアウト/再試行/サーキットブレーカの設計。
- エラーハンドリングの標準化（ユーザー通知と内部ログ分離）。

## 8. 受け入れ基準

- 機能
  - Browser からマイク入力し、`final` テキストが取得できる。
  - STT 確定テキストが `voice_chat` surface の会話入力として処理される。
  - `viewer_chat` と同じ target agent / provider alias 規則で Mio などに届く。
- 接続
  - `WHISPER_URL` を切り替えてローカル/リモート双方で動作する。
- 安全
  - ブラウザは Whisper 直アクセスしない。
  - サーバ間通信前提で CORS 依存を持たない。
- 回帰
  - TTS（SBV2）既存経路へ影響を与えない。

## 9. 未確定事項

- RenCrow_STT / STT provider の本番配置。
- Whisper へのリクエスト詳細（multipart仕様、model指定、language指定）。
- STT 用設定を `config.yaml` に正式追加するか、環境変数運用を継続するか。

## 10. voice-bridge（STT Gateway）実装詳細

### 10.1 概要

voice-bridge は STT Gateway の現行実装であり、Node.js プロセスとして動作する。

- **役割**: Browser からの WebSocket 音声ストリームを受け取り、VAD で発話区間を判定し、Whisper へ HTTP POST で転送する
- **実行場所**: Win11-HP01（`192.168.1.36`）
- **ポート**: `:8090`
- **ソース**: `server.js`（+ `stt-gateway-contract.js`、`server-https.js`）

### 10.2 物理構成

```text
Browser
  ↓ wss://fujitsu-ubunts:18790/stt
RenCrow Chat Server（Go, fujitsu-ubunts）← STT_GATEWAY_URL が設定されている場合は透過プロキシ
  ↓ ws://192.168.1.36:8090/stt
voice-bridge（Node.js, Win11-HP01 :8090）← STT Gateway 本体
  ↓ HTTP POST multipart/form-data
Whisper（whisper.cpp, Win11-HP01 :8080/inference）
```

### 10.3 接続設定

| 項目 | 値 |
|---|---|
| Chat Server 側設定 | `STT_GATEWAY_URL=ws://192.168.1.36:8090/stt` |
| voice-bridge 起動コマンド | `npm start`（voice-bridge ディレクトリ内） |
| voice-bridge WebSocket パス | `/stt`（主）、`/stt-ws`、`/ws`（互換） |
| Whisper 接続先 | `STT_PROVIDER_URL=http://192.168.1.36:8080/inference`（voice-bridge 側の環境変数） |

### 10.4 Go Chat Server のプロキシ動作

`STT_GATEWAY_URL` 環境変数の設定状態により動作が変わる：

| `STT_GATEWAY_URL` | 動作 |
|---|---|
| **設定あり** | `/stt` を voice-bridge / RenCrow_STT へ透過 WebSocket プロキシ |
| **未設定**（フォールバック） | Go が直接 Whisper を呼ぶ（VAD なし・簡易実装） |

**本番運用では `STT_GATEWAY_URL` を設定し voice-bridge 経由を使用すること。**

### 10.5 voice-bridge の主要機能

- **Silero VAD**（`@ricky0123/vad-node`）: ML モデルによる発話開始/終了の高精度判定
- **RNNoise**（`@jitsi/rnnoise-wasm`）: WASM ベースのノイズ除去
- **`session_info` 送出**: 接続時に `{"type":"session_info","session_id":"sess-..."}` を送信
- **draft / final**: VAD 発話終了時に final 確定、発話中は draft を定期送信（`STT_DRAFT_ENABLED=true` 時）
- **busy policy**: 同時推論の再入制御（`drop` / `queue_latest`）

### 10.6 voice-bridge 主要環境変数

| 環境変数 | デフォルト | 説明 |
|---|---|---|
| `STT_PROVIDER_URL` / `WHISPER_URL` | `http://127.0.0.1:8080/inference` | Whisper エンドポイント |
| `STT_PORT` / `PORT` | `8090` | 待受ポート |
| `STT_DRAFT_ENABLED` | `false` | draft イベント有効化 |
| `STT_SILENCE_END_MS` | `850` | 無音判定ウィンドウ（ms） |
| `STT_MIN_AUDIO_BYTES` | `32044` | 推論対象の最小音声サイズ |

### 10.7 依存パッケージ

voice-bridge の起動には以下が必要：

| パッケージ | 用途 |
|---|---|
| `express` | HTTP サーバー |
| `ws` | WebSocket サーバー |
| `node-fetch` (v2) | Whisper HTTP クライアント |
| `form-data` | multipart 組み立て |
| `@ricky0123/vad-node` | Silero VAD |
| `@jitsi/rnnoise-wasm` | RNNoise ノイズ除去 |

### 10.8 注意事項

- voice-bridge は Chat Server（Go）とは別プロセスで起動する
- 起動順序: `Whisper → voice-bridge → Chat Server（Go）`
- `STT_GATEWAY_URL` 未設定時は Go のフォールバック実装が動作するが、VAD なしのため認識品質が低下する
- `session_info` による `session_id` はフォールバック実装では送出されない

## 11. ゴールデンテストデータセット

Viewer 実マイク録音由来の固定 WAV + 原文ペアを E2E / 回帰 / 開発確認に使う。

| ID | 用途 | trim WAV |
|----|------|----------|
| `golden_25s_v1` | **デフォルト成功系**（約 25 s） | `tmp/stt_inputs/client_stt_input_20260609_140311.wav` |
| `long_35s_v1` | 長尺・30 s チャンク劣化再現（約 35 s） | `tmp/stt_inputs/client_stt_input_20260609_135459.wav` |

詳細（原文、STT 許容差分、probe コマンド、マニフェスト）: **`docs/STT_TTS/STT_ゴールデンテストデータセット仕様.md`**

## 12. 参照

- `docs/STT_TTS/STT_ゴールデンテストデータセット仕様.md`
- `docs/01_正本仕様/15_TTS_Viewer同期.md`
- `docs/STT_TTS/README.md`
- `docs/STT_TTS/AUDIO_Server仕様/STT/STT仕様.md`
- `docs/STT_TTS/AUDIO_Client仕様/STT/仕様.md`
- `docs/STT_TTS/STT_Remote_HTTPS仕様.md`
- `docs/STT_TTS/STT_Streaming_Client仕様.md`
- `docs/04_実装仕様_機能拡張/実装仕様_チャネル拡張_v1.md`
- `docs/02_OpenClaw移植詳細仕様/詳細実装仕様_07_アプリ・ノード統合.md`
