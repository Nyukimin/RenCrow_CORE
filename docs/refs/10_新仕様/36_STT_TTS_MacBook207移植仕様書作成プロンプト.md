# STT / TTS を MacBook 207 へ移植するための仕様書作成プロンプト

RenCrow の STT / TTS を `192.168.1.207 lan-macbook`、または `100.85.222.99 macbook-pro.tailb07d8d.ts.net macbook-pro` 上へ移植するため、RenCrow 側インタフェースに合わせた仕様書を作成してください。

## 目的

現在 RenCrow 側では STT / TTS endpoint が runtime dependency として扱われています。  
これを MacBook 側サービスとして再構成し、RenCrow 本体からは安定した API 契約だけを見ればよい構造にします。

仕様書では、以下を明確に分離してください。

1. RenCrow 側 API 契約
2. STT 実装レイヤー
3. TTS 実装レイヤー
4. MacBook 上の service / process / 起動運用
5. health / readiness / debug / error contract
6. Viewer / runtime-config / debug-system から見える状態

## 前提環境

- RenCrow 本体: Ubuntu 側で稼働
- RenCrow runtime endpoint: `http://127.0.0.1:18790`
- STT / TTS 移植先:
  - LAN: `192.168.1.207`
  - Tailscale: `100.85.222.99`
  - host alias: `lan-macbook`, `macbook-pro`
- LLM 系はすでに 207 側で稼働確認済み
  - Chat: `192.168.1.207:8081`
  - Worker: `192.168.1.207:8082`
  - LLM 管理 daemon: `192.168.1.207:8079`

## 現在の RenCrow 側 config 参考

現在または過去の config では以下のような形で参照されている。

```yaml
tts:
  enabled: true
  http_base_url: http://192.168.1.13:7870
  irodori:
    enabled: true
    base_url: http://192.168.1.13:7870

stt:
  enabled: true
  provider: external_http
  provider_url: http://192.168.1.33:8766/v1/audio/transcriptions
  stream_url: wss://fujitsu-ubunts.tailb07d8d.ts.net/stt
```

これを 207 / MacBook 側に寄せる前提で、RenCrow 側が要求する最小 API 契約を定義してください。

## 仕様書に必ず含めること

### 1. 全体アーキテクチャ

以下を分けて説明してください。

- RenCrow Core
  - runtime config
  - `/viewer/runtime-config`
  - `/viewer/debug/system`
  - health check
  - Viewer mic / playback
- STT Gateway API
  - RenCrow から見える HTTP / WebSocket API
  - 音声入力を STT 実装へ渡す境界
- STT Engine
  - Whisper / faster-whisper / MLX / 外部 provider などの実装差し替え層
  - RenCrow API contract に直接依存させない
- TTS Gateway API
  - RenCrow から見える HTTP API
  - 音声生成要求を TTS 実装へ渡す境界
- TTS Engine
  - Irodori / Style-Bert-VITS2 / Apple Silicon 実装などの差し替え層
  - voice profile / reference audio / model config を保持する層
- Service 運用層
  - MacBook 上の起動方法
  - port
  - logs
  - health
  - restart policy

### 2. API 契約

RenCrow から見える API を定義してください。

#### STT HTTP API

必須 endpoint 案:

- `GET /health`
- `POST /v1/audio/transcriptions`

`POST /v1/audio/transcriptions` について以下を定義してください。

- request content type
  - `multipart/form-data`
  - audio file field name
  - optional fields: `language`, `model`, `prompt`, `format`
- response JSON
  - success
  - partial success
  - error
- timeout
- max audio size
- supported audio formats
- empty transcription の扱い
- confidence / segments を返すか
- RenCrow 側で fallback 成功扱いにしない条件

#### STT Streaming API

必要なら WebSocket を別契約として定義してください。

- `GET /stt` or `/v1/audio/stream`
- input frame format
- PCM16 / wav chunk / browser MediaRecorder format
- partial transcript event
- final transcript event
- error event
- session close event

ただし、HTTP transcription API と streaming API は同じ実装に直結させず、Gateway 層で分けること。

#### TTS HTTP API

必須 endpoint 案:

- `GET /health/live`
- `GET /health/ready`
- `POST /api/tts`

`POST /api/tts` について以下を定義してください。

- request JSON
  - `text`
  - `voice_id`
  - `speech_mode`
  - `format`
  - `speed`
  - `reference_audio` or `reference_audio_url`
- response
  - audio binary 直返しの場合
  - JSON + audio URL の場合
  - job id 非同期の場合
- RenCrow が期待する最小 contract
- voice not found
- model not loaded
- text empty
- generation timeout
- audio generation failed

### 3. レイヤー分離

以下のように責務を明確にしてください。

#### API Gateway Layer

責務:

- RenCrow 互換 API を提供する
- request validation
- response normalization
- timeout / error mapping
- health / readiness
- logs / request id
- 実装 engine の詳細を隠す

禁止:

- モデル固有の重い処理を直接書かない
- RenCrow の内部 state に依存しない
- UI / Viewer の都合を engine に漏らさない

#### STT Engine Layer

責務:

- audio decode
- resampling
- transcription
- language detection
- segment generation
- model lifecycle

禁止:

- RenCrow の endpoint 名に依存しない
- HTTP response 形式を直接組み立てない

#### TTS Engine Layer

責務:

- text normalization
- voice selection
- model inference
- audio file generation
- reference audio handling
- watermark / precision / device config

禁止:

- RenCrow config の YAML を直接読む
- Viewer 向け URL 生成を engine 内に持たせない

#### Adapter Layer

必要なら、Irodori / SBV2 / Whisper など provider ごとに adapter を定義してください。

- `IrodoriTTSAdapter`
- `WhisperSTTAdapter`
- `MLXWhisperAdapter`
- `StyleBertVITS2Adapter`

Adapter は Gateway contract と Engine contract の間に置くこと。

### 4. config 設計

MacBook 側 service config と RenCrow 側 config を分けて定義してください。

RenCrow 側 config 例:

```yaml
tts:
  enabled: true
  http_base_url: http://192.168.1.207:7870
  irodori:
    enabled: true
    base_url: http://192.168.1.207:7870

stt:
  enabled: true
  provider: external_http
  provider_url: http://192.168.1.207:8766/v1/audio/transcriptions
  stream_url: ws://192.168.1.207:8766/stt
```

MacBook 側 service config 例も作ってください。

含めること:

- bind host
- port
- model path
- device
- precision
- timeout
- max request size
- output dir
- log path
- allowed origins
- health readiness condition

### 5. health / readiness 仕様

`live` と `ready` を分けてください。

- live:
  - process が生きている
  - HTTP server が応答する
- ready:
  - model loaded
  - required voice available
  - reference audio readable
  - temp/output dir writable
  - inference dry-run または lightweight check 済み

RenCrow 側 `/viewer/debug/system` で何を OK / blocked / degraded と表示するべきかも定義してください。

### 6. error contract

RenCrow が正しく blocked と判断できるよう、error JSON を統一してください。

例:

```json
{
  "ok": false,
  "error": {
    "code": "model_not_ready",
    "message": "TTS model is not loaded",
    "retryable": true,
    "detail": "..."
  }
}
```

必要な error code を列挙してください。

- `model_not_ready`
- `voice_not_found`
- `invalid_audio`
- `empty_transcript`
- `generation_failed`
- `timeout`
- `provider_unavailable`
- `unsupported_format`
- `internal_error`

### 7. RenCrow 側での成功条件

以下を明確にしてください。

- health OK だけでは E2E 成功扱いしない
- STT は実 audio input から transcript が返ること
- TTS は短文から再生可能 audio が生成されること
- Viewer mic E2E は別検証
- fallback / empty / mock / skip は成功扱いしない

### 8. 検証手順

段階ごとに書いてください。

1. MacBook local health
2. Ubuntu から MacBook 207 への疎通
3. RenCrow config 更新
4. RenCrow clean restart
5. `/viewer/runtime-config` 確認
6. `/viewer/debug/system` 確認
7. STT HTTP transcription 最小確認
8. TTS short text generation 最小確認
9. Viewer 経由の音声確認
10. 失敗時の切り分け

RenCrow 再起動時は以下の停止ルールを守ること。

- `systemctl --user stop rencrow.service`
- 残存 `rencrow` process なし
- `:18790` listen なし
- `/health` connection refused
- その後に起動

### 9. 成果物

以下の構成で仕様書を作ってください。

- 目的
- 前提
- 全体構成図
- RenCrow 側 API contract
- STT Gateway contract
- STT Engine contract
- TTS Gateway contract
- TTS Engine contract
- config
- health / readiness
- error contract
- logging
- security
- migration steps
- verification checklist
- unresolved decisions

### 10. 注意

- 実装はまだ行わない
- まず仕様書だけ作る
- RenCrow Core と STT/TTS 実装を密結合させない
- API contract と engine implementation を混同しない
- Viewer 表示、音声再生、STT/TTS 生成を同一責務にしない
- `STT` と `TTS` の typo に注意する
