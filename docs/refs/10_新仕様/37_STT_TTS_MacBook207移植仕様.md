# STT / TTS MacBook 207 移植仕様

作成日: 2026-05-20
ステータス: 実装前仕様
対象: RenCrow runtime と MacBook 上 STT / TTS service の接続契約

## 1. 目的

STT / TTS を `192.168.1.207` の MacBook 側へ移植し、RenCrow 本体からは安定した API contract だけを参照する構成へ整理する。

この仕様では RenCrow Core、STT Gateway、STT Engine、TTS Gateway、TTS Engine、MacBook service 運用層を分離する。RenCrow Core は provider 実装詳細、モデル path、Apple Silicon 固有の inference 設定、voice profile の内部構造を知らない。STT / TTS 実装側も RenCrow の Viewer state、session state、Go config loader に依存しない。

## 2. 前提

- RenCrow 本体は Ubuntu 側で稼働する。
- RenCrow runtime endpoint は `http://127.0.0.1:18790`。
- STT / TTS 移植先は同一 MacBook。
  - LAN: `192.168.1.207`
  - Tailscale: `100.85.222.99`
  - hosts alias: `lan-macbook`, `macbook-pro`
- 207 側では LLM 系が稼働済み。
  - Chat: `http://192.168.1.207:8081`
  - Worker: `http://192.168.1.207:8082`
  - LLM 管理 daemon: `http://192.168.1.207:8079`
- STT / TTS の health OK は E2E 成功ではない。実 audio input / 実 audio output の確認を別 gate とする。

## 3. 全体構成

```text
Viewer / Channel input
  |
  | mic audio / uploaded audio / text response
  v
RenCrow Core on Ubuntu
  - config: ~/.rencrow/config.yaml
  - /viewer/runtime-config
  - /viewer/debug/system
  - STT request client
  - TTS request client
  |
  | HTTP / WebSocket only
  v
MacBook 207 Gateway Services
  |
  +-- STT Gateway API :8766
  |     - request validation
  |     - response normalization
  |     - error mapping
  |     - health / readiness
  |     v
  |   STT Engine
  |     - Whisper / faster-whisper / MLX Whisper adapter
  |     - decode / resample / transcribe / segment
  |
  +-- TTS Gateway API :7870
        - request validation
        - response normalization
        - audio URL or binary response
        - health / readiness
        v
      TTS Engine
        - Irodori / Style-Bert-VITS2 / other Apple Silicon adapter
        - text normalize / voice select / inference / audio file
```

## 4. レイヤー責務

### 4.1 RenCrow Core

責務:

- `~/.rencrow/config.yaml` から STT / TTS endpoint を読み込む。
- `/viewer/runtime-config` で endpoint と runtime readiness を secret なしで表示する。
- `/viewer/debug/system` で STT `/health`、TTS `/health/live`、TTS `/health/ready` を短い timeout で probe する。
- Viewer microphone、STT transcript、TTS playback、lip sync の状態を混同せず表示する。
- fallback、empty transcript、mock、skip を成功扱いしない。

禁止:

- MacBook 側の model path や device / precision を直接読む。
- STT / TTS Engine 実装に依存する分岐を RenCrow Core に追加する。
- health OK のみで STT / TTS E2E 成功と記録する。

### 4.2 API Gateway Layer

責務:

- RenCrow 互換 API を公開する。
- request validation、size limit、timeout、error mapping を実施する。
- Engine の戻り値を RenCrow 向け JSON / audio response へ正規化する。
- request_id を発行し、gateway log と engine log を関連付ける。
- `/health`、`/health/live`、`/health/ready` を提供する。

禁止:

- モデル固有の inference 処理を Gateway 内に直接実装しない。
- RenCrow の内部 session / Viewer DOM / Go struct に依存しない。
- Engine の例外や provider 独自レスポンスをそのまま外へ出さない。

### 4.3 STT Engine Layer

責務:

- audio decode、resampling、channel normalization を行う。
- model lifecycle、language detection、transcription、segments generation を扱う。
- provider adapter を差し替え可能にする。

禁止:

- HTTP request / response を直接組み立てない。
- RenCrow の endpoint 名や Viewer 表示仕様を参照しない。

### 4.4 TTS Engine Layer

責務:

- text normalization、voice selection、reference audio validation を行う。
- model inference、audio file generation、watermark / precision / device config を扱う。
- provider adapter を差し替え可能にする。

禁止:

- RenCrow config YAML を直接読む。
- Viewer 向け URL 生成を Engine 内に持たせない。
- 生成失敗を無音 audio や代替文で成功扱いしない。

### 4.5 Adapter Layer

候補 adapter:

- `WhisperSTTAdapter`
- `MLXWhisperAdapter`
- `FasterWhisperAdapter`
- `IrodoriTTSAdapter`
- `StyleBertVITS2Adapter`

Adapter は Gateway contract と Engine contract の間に置く。provider 固有の model path、device、precision、reference audio、voice preset は adapter / engine config に閉じ込める。

## 5. RenCrow 側 runtime contract

### 5.1 RenCrow config

207 移植後の RenCrow 側 config は以下を基本形とする。

```yaml
tts:
  enabled: true
  output_dir: ./workspace/tts
  audio_path_root: /home/nyukimi/RenCrow_CORE/scripts
  http_base_url: http://192.168.1.207:7870
  timeout_ms: 15000
  voice_id: mio
  provider_priority:
    - irodori
  irodori:
    enabled: true
    base_url: http://192.168.1.207:7870
    endpoint_path: /api/tts
    voice_id: mio
    voice_name: Mio
    timeout_sec: 120

stt:
  enabled: true
  provider: external_http
  language: ja
  model: small
  timeout_ms: 8000
  provider_url: http://192.168.1.207:8766/v1/audio/transcriptions
  stream_url: ws://192.168.1.207:8766/stt
```

Tailscale 経由で運用する場合は `192.168.1.207` を `100.85.222.99` または `macbook-pro` に置き換える。ただし Viewer から browser が直接接続する URL は、その browser から到達可能な host を使う。

### 5.2 `/viewer/runtime-config`

RenCrow は以下を返す。

- `stt_base_url`: `provider_url` から推定した base URL。例: `http://192.168.1.207:8766`
- `stt_stream_url`: config の `stt.stream_url`
- `tts_base_url`: `tts.http_base_url`
- `tts_health_path`: 明示された場合のみ。未指定なら debug system は `/health/live` と `/health/ready` を probe する。
- `runtime_readiness.stt_gateway_config_present`: STT HTTP or stream URL が設定済みなら true
- `runtime_readiness.tts_provider_config_present`: TTS base URL が設定済みなら true

`runtime-config` は設定の可視化であり、provider 到達や実 audio 成功の証明ではない。

### 5.3 `/viewer/debug/system`

RenCrow は短い timeout で以下を probe する。

- STT: `GET {stt_base_url}/health`
- TTS live: `GET {tts_base_url}/health/live`
- TTS ready: `GET {tts_base_url}/health/ready`

期待 JSON field:

- `audio.stt_base_url`
- `audio.tts_base_url`
- `audio.stt_ok`
- `audio.tts_live_ok`
- `audio.tts_ready_ok`
- `audio.stt_health`
- `audio.tts_live`
- `audio.tts_ready`
- `audio.last_error`

`stt_ok=false`、`tts_live_ok=false`、`tts_ready_ok=false` は blocked 証跡である。route が 200 を返しても、down state を成功扱いしない。

## 6. STT Gateway API contract

### 6.1 `GET /health`

目的: process と STT Gateway の軽量 readiness を返す。

成功例:

```json
{
  "ok": true,
  "status": "ready",
  "service": "stt-gateway",
  "version": "0.1.0",
  "engine": "mlx-whisper",
  "model": "small",
  "language": "ja",
  "ready": {
    "model_loaded": true,
    "tmp_writable": true,
    "audio_decode_available": true
  }
}
```

degraded / blocked 例:

```json
{
  "ok": false,
  "status": "blocked",
  "service": "stt-gateway",
  "error": {
    "code": "model_not_ready",
    "message": "STT model is not loaded",
    "retryable": true
  }
}
```

HTTP status:

- ready: `200`
- process alive but not ready: `503`
- invalid method: `405`

### 6.2 `POST /v1/audio/transcriptions`

目的: RenCrow から渡された audio を文字起こしする。

Request:

- content type: `multipart/form-data`
- required field: `file`
- optional fields:
  - `language`: default `ja`
  - `model`: default service config
  - `prompt`: STT bias prompt
  - `format`: `json`, default `json`
  - `response_format`: OpenAI 互換が必要な場合のみ受け付ける

制限:

- max audio size: MacBook service config で定義。初期値は `25MB`。
- timeout: RenCrow `stt.timeout_ms` と Gateway timeout の短い方を超えない。
- supported formats: `wav`, `webm`, `m4a`, `mp3`, `flac`, `ogg`。Engine が未対応なら Gateway が `unsupported_format` を返す。

成功 response:

```json
{
  "ok": true,
  "text": "こんにちは、テストです。",
  "language": "ja",
  "duration_ms": 1840,
  "confidence": 0.92,
  "segments": [
    {
      "start_ms": 0,
      "end_ms": 1840,
      "text": "こんにちは、テストです。",
      "confidence": 0.92
    }
  ],
  "request_id": "stt_20260520_000001"
}
```

partial success response:

```json
{
  "ok": true,
  "partial": true,
  "text": "こんにちは",
  "warning": {
    "code": "low_confidence",
    "message": "transcription confidence is below threshold"
  },
  "request_id": "stt_20260520_000002"
}
```

error response:

```json
{
  "ok": false,
  "error": {
    "code": "invalid_audio",
    "message": "audio file could not be decoded",
    "retryable": false
  },
  "request_id": "stt_20260520_000003"
}
```

Empty transcript:

- Engine が空文字を返した場合、Gateway は `ok=false` と `empty_transcript` を返す。
- 無音を正常終了とみなす場合も RenCrow E2E 成功にはしない。
- RenCrow 側は empty transcript を fallback 成功扱いしない。

### 6.3 STT Streaming API

Endpoint:

- `GET /stt`
- alias: `GET /v1/audio/stream`

Transport:

- WebSocket
- browser 直結時は Viewer が到達できる URL を使う。

Input event:

```json
{
  "type": "audio",
  "session_id": "viewer-session",
  "format": "pcm16",
  "sample_rate": 16000,
  "channels": 1,
  "seq": 1,
  "audio_base64": "..."
}
```

binary frame を使う場合は接続開始時に JSON config を送り、その後 binary PCM16 frame を送る。

Output events:

```json
{"type":"partial","session_id":"viewer-session","text":"こん","seq":1}
{"type":"final","session_id":"viewer-session","text":"こんにちは","segments":[],"seq":2}
{"type":"error","session_id":"viewer-session","error":{"code":"timeout","message":"provider timeout","retryable":true}}
{"type":"closed","session_id":"viewer-session","reason":"client_closed"}
```

Streaming API は HTTP transcription API と Gateway 層で分ける。同じ Engine を使ってよいが、session state、chunk buffering、partial event の責務は Gateway に置く。

## 7. TTS Gateway API contract

### 7.1 `GET /health/live`

目的: process と HTTP server が生きていることを返す。

成功例:

```json
{
  "ok": true,
  "status": "live",
  "service": "tts-gateway",
  "version": "0.1.0"
}
```

`live` は model loaded を意味しない。

### 7.2 `GET /health/ready`

目的: TTS request を受け付けられる状態を返す。

成功例:

```json
{
  "ok": true,
  "status": "ready",
  "service": "tts-gateway",
  "engine": "irodori",
  "ready": {
    "model_loaded": true,
    "voices_loaded": true,
    "default_voice_available": true,
    "reference_audio_readable": true,
    "output_dir_writable": true
  },
  "voices": [
    {"voice_id":"mio","name":"Mio"},
    {"voice_id":"shiro","name":"Shiro"}
  ]
}
```

blocked 例:

```json
{
  "ok": false,
  "status": "blocked",
  "service": "tts-gateway",
  "error": {
    "code": "voice_not_found",
    "message": "default voice mio is not available",
    "retryable": false
  }
}
```

HTTP status:

- ready: `200`
- live but not ready: `503`

### 7.3 `POST /api/tts`

目的: text から再生可能 audio を生成する。

Request:

```json
{
  "text": "こんにちは。音声生成のテストです。",
  "voice_id": "mio",
  "speech_mode": "conversational",
  "format": "wav",
  "speed": 1.0,
  "reference_audio": "",
  "reference_audio_url": "http://192.168.1.207:8090/voice_profile_tests/Female_01_Mio_test.wav"
}
```

Field:

- `text`: required。trim 後空なら `text_empty`。
- `voice_id`: optional。未指定時は service default。
- `speech_mode`: optional。`conversational`, `narration`, `neutral` など。
- `format`: optional。`wav` を必須対応、`mp3` は任意。
- `speed`: optional。範囲外は validation error。
- `reference_audio` / `reference_audio_url`: optional。adapter が必要とする場合のみ使用。

Response 方式は以下のどちらかを service config で選ぶ。RenCrow 側の最小 contract は「再生可能 audio を取得できること」。

Binary 直返し:

- HTTP `200`
- `Content-Type: audio/wav`
- `X-RenCrow-Audio-Duration-Ms`
- `X-RenCrow-Request-Id`

JSON + audio URL:

```json
{
  "ok": true,
  "audio_url": "http://192.168.1.207:7870/audio/tts_20260520_000001.wav",
  "audio_path": "/Users/yukimi/.rencrow-audio/tts/tts_20260520_000001.wav",
  "duration_ms": 2140,
  "format": "wav",
  "voice_id": "mio",
  "request_id": "tts_20260520_000001"
}
```

非同期 job 方式は初期移植では必須にしない。採用する場合は `202 Accepted` と `job_id`、`GET /api/tts/jobs/{job_id}` の status contract を別途定義する。

Error:

```json
{
  "ok": false,
  "error": {
    "code": "generation_failed",
    "message": "TTS generation failed",
    "retryable": true,
    "detail": "provider timeout"
  },
  "request_id": "tts_20260520_000002"
}
```

## 8. MacBook service config

MacBook 側は RenCrow config と別ファイルにする。例:

```yaml
server:
  bind_host: 0.0.0.0
  stt_port: 8766
  tts_port: 7870
  public_base_url: http://192.168.1.207
  allowed_origins:
    - http://127.0.0.1:18790
    - http://192.168.1.204:18790
  request_timeout_ms: 120000
  max_request_bytes: 26214400

logging:
  level: info
  path: /Users/yukimi/.rencrow-audio/logs/gateway.log
  request_id_header: X-Request-Id

stt:
  engine: mlx-whisper
  model_path: /Users/yukimi/models/whisper-small
  language: ja
  device: mps
  precision: fp16
  tmp_dir: /Users/yukimi/.rencrow-audio/tmp
  max_audio_seconds: 180
  health:
    require_model_loaded: true
    require_decode_probe: true

tts:
  engine: irodori
  checkpoint: Aratako/Irodori-TTS-500M-v2
  device: mps
  precision: fp32
  output_dir: /Users/yukimi/.rencrow-audio/tts
  audio_url_base: http://192.168.1.207:7870/audio
  default_voice_id: mio
  voices:
    mio:
      name: Mio
      reference_audio: /Users/yukimi/GenerativeAI/TTS/irodori/voices/Female_01_Mio/reference.wav
      reference_audio_url: http://192.168.1.207:8090/voice_profile_tests/Female_01_Mio_test.wav
    shiro:
      name: Shiro
      reference_audio: /Users/yukimi/GenerativeAI/TTS/irodori/voices/Male_01_Shiro/reference.wav
  health:
    require_model_loaded: true
    require_default_voice: true
    require_output_dir_writable: true
```

## 9. health / readiness 判定

### 9.1 STT

- `live`: process alive、HTTP server responding。
- `ready`: model loaded、tmp writable、audio decode available、transcription request を受け付け可能。
- `degraded`: model は loaded だが optional capability 欠落。例: confidence unavailable。
- `blocked`: model not loaded、audio decode unavailable、tmp not writable、provider unavailable。

RenCrow 表示:

- `/viewer/runtime-config`: endpoint configured。
- `/viewer/debug/system`: `/health` が 2xx なら `stt_ok=true`。
- 実 mic E2E 未実施なら Ops 表示では `blocked: real microphone STT E2E not verified` を残す。

### 9.2 TTS

- `live`: process alive、HTTP server responding。
- `ready`: model loaded、voice available、reference audio readable、output dir writable。
- `degraded`: optional voice unavailable だが default voice は使用可能。
- `blocked`: default voice missing、model not loaded、generation unavailable、output dir not writable。

RenCrow 表示:

- `/viewer/debug/system` で live と ready を分ける。
- `tts_live_ok=true` かつ `tts_ready_ok=false` は process は起動済みだが生成不可。
- browser playback / lip sync 未実施なら成功扱いしない。

## 10. error contract

Gateway error は共通 JSON に統一する。

```json
{
  "ok": false,
  "error": {
    "code": "model_not_ready",
    "message": "model is not loaded",
    "retryable": true,
    "detail": "optional diagnostic"
  },
  "request_id": "req_..."
}
```

Error code:

| code | HTTP | retryable | 用途 |
| --- | ---: | --- | --- |
| `model_not_ready` | 503 | true | model load 中、未初期化 |
| `voice_not_found` | 404 | false | 指定 voice が存在しない |
| `invalid_audio` | 400 | false | audio decode 不能 |
| `empty_transcript` | 422 | false | STT が空文字を返した |
| `text_empty` | 400 | false | TTS text が空 |
| `generation_failed` | 500 | true | TTS inference 失敗 |
| `timeout` | 504 | true | Gateway or Engine timeout |
| `provider_unavailable` | 503 | true | Engine / adapter 不達 |
| `unsupported_format` | 415 | false | 未対応 audio / output format |
| `request_too_large` | 413 | false | max request size 超過 |
| `internal_error` | 500 | true | その他未分類 |

RenCrow 側は `ok=false`、non-2xx、empty transcript、audio URL なしの TTS response を成功扱いしない。

## 11. logging

Gateway log は JSON Lines を推奨する。

必須 field:

- `timestamp`
- `request_id`
- `service`: `stt-gateway` or `tts-gateway`
- `endpoint`
- `method`
- `status_code`
- `duration_ms`
- `engine`
- `voice_id` or `language`
- `error_code`
- `audio_duration_ms`
- `input_bytes`
- `output_bytes`

禁止:

- API key、secret、full prompt、個人情報を平文で残さない。
- audio binary を log に直接埋め込まない。
- RenCrow の session state を MacBook service の主たる真実にしない。

## 12. security

- 初期運用は LAN / Tailscale 内限定とする。
- `bind_host=0.0.0.0` の場合、macOS firewall と allowed origins を設定する。
- 外部公開しない。
- API key を使う provider を adapter に追加する場合、MacBook service config へ平文保存しない。環境変数または OS keychain を使う。
- `reference_audio_url` は allowlist host のみ許可する。
- file path を request で受ける場合は service 管理 root 配下のみ許可する。

## 13. migration steps

1. MacBook 側に STT Gateway と TTS Gateway を配置する。
2. MacBook local で `GET http://127.0.0.1:8766/health` を確認する。
3. MacBook local で `GET http://127.0.0.1:7870/health/live` と `/health/ready` を確認する。
4. Ubuntu から `http://192.168.1.207:8766/health`、`http://192.168.1.207:7870/health/live`、`/health/ready` を確認する。
5. RenCrow `~/.rencrow/config.yaml` の STT / TTS endpoint を 207 に変更する。
6. RenCrow をクリーン停止する。
   - `systemctl --user stop rencrow.service`
   - `pgrep -a rencrow` が空
   - `ss -ltnp '( sport = :18790 )'` で listen なし
   - `curl http://127.0.0.1:18790/health` が connection refused
7. RenCrow を起動する。
8. `/viewer/runtime-config` で 207 endpoint を確認する。
9. `/viewer/debug/system` で STT / TTS health を確認する。
10. STT HTTP transcription の最小確認を行う。
11. TTS short text generation の最小確認を行う。
12. Viewer microphone / browser playback / lip sync E2E を別 gate として実施する。

## 14. verification checklist

### 14.1 MacBook local

- [ ] `GET http://127.0.0.1:8766/health` が 200 ready。
- [ ] `POST http://127.0.0.1:8766/v1/audio/transcriptions` が実 audio から非空 transcript を返す。
- [ ] `GET http://127.0.0.1:7870/health/live` が 200 live。
- [ ] `GET http://127.0.0.1:7870/health/ready` が 200 ready。
- [ ] `POST http://127.0.0.1:7870/api/tts` が再生可能 wav を返す。

### 14.2 Ubuntu to MacBook

- [ ] `curl http://192.168.1.207:8766/health` が成功する。
- [ ] `curl http://192.168.1.207:7870/health/live` が成功する。
- [ ] `curl http://192.168.1.207:7870/health/ready` が成功する。
- [ ] Ubuntu から STT 実 audio request が成功する。
- [ ] Ubuntu から TTS short text generation が成功する。

### 14.3 RenCrow runtime

- [ ] `/health` が LLM 依存を含めて 200 OK。
- [ ] `/viewer/runtime-config` が 207 の `stt_base_url` / `stt_stream_url` / `tts_base_url` を返す。
- [ ] `/viewer/debug/system` が `stt_ok=true`、`tts_live_ok=true`、`tts_ready_ok=true` を返す。
- [ ] `/viewer/debug/system` が失敗時に `last_error` または body を保持する。
- [ ] STT / TTS down state を success として記録しない。

### 14.4 True E2E

- [ ] 実 audio input から transcript が Viewer / RenCrow flow に入る。
- [ ] transcript が空でない。
- [ ] Chat 応答から TTS audio が生成される。
- [ ] browser playback が成功する。
- [ ] lip sync trigger が audio event と分離して記録される。
- [ ] fallback / mock / skip を成功扱いしていない。

## 15. unresolved decisions

- STT Engine は `mlx-whisper`、`faster-whisper`、既存外部 provider のどれを初期採用するか。
- TTS Engine は Irodori を初期採用するか、SBV2 を並行 adapter とするか。
- TTS response は binary 直返しと JSON + audio URL のどちらを RenCrow の標準にするか。
- Viewer browser が接続する STT stream URL は LAN と Tailscale のどちらを標準にするか。
- MacBook service を `launchd`、手動 shell、独自 daemon のどれで管理するか。
- STT streaming の input format を PCM16 に固定するか、MediaRecorder `webm/opus` を Gateway で decode するか。
- TTS reference audio URL を Gateway が fetch するか、service config の local path のみ許可するか。

## 16. 従来システム踏襲時に決まること

従来の RenCrow 音声構成を踏襲する場合、移植先が MacBook 207 へ変わるだけで、RenCrow 側の基本 contract は大きく変えない。

決定済みとして扱うもの:

- STT provider は `external_http`。
- STT HTTP API は `POST /v1/audio/transcriptions`。
- STT health は `GET /health`。
- STT port は `8766`。
- STT stream は `ws://192.168.1.207:8766/stt`。
- TTS provider priority は Irodori 優先。
- TTS port は `7870`。
- TTS API は `POST /api/tts`。
- TTS health は `GET /health/live` と `GET /health/ready`。
- TTS default voice は `mio`。
- RenCrow 側 config では `tts.http_base_url` と `tts.irodori.base_url` の両方を 207 に向ける。
- RenCrow Core は STT / TTS の内部実装を知らず、Gateway API だけを見る。
- 状態確認は `/viewer/runtime-config` と `/viewer/debug/system` を正とする。

ほぼ決定として扱えるもの:

- TTS Engine は Irodori。
- TTS response は JSON + `audio_url` を標準候補とする。binary 直返しは互換 option として残してよい。
- STT は HTTP transcription を先に通し、streaming は Viewer mic 用の後段確認にする。
- MacBook 側は Gateway service を立て、Engine を adapter としてぶら下げる。

まだ決める必要があるもの:

- STT Engine の中身を `mlx-whisper`、`faster-whisper`、既存外部 provider のどれにするか。
- MacBook 側 service 管理を `launchd`、手動 shell、独自 daemon のどれにするか。
- Viewer から直接使う stream URL を LAN `192.168.1.207` にするか、Tailscale `100.85.222.99` にするか。
- TTS audio 返却を JSON + `audio_url` のみに寄せるか、binary 直返しも正式対応に含めるか。

MacBook 側への最短実装指示は以下でよい。

```text
8766 で STT OpenAI 互換 /v1/audio/transcriptions と /health を出す。
7870 で Irodori 互換 /api/tts と /health/live /health/ready を出す。
RenCrow 側は 192.168.1.207 のそれらを見る前提。
```

## 17. 実装開始条件

この仕様書は実装前仕様である。実装開始前に以下を確定する。

1. STT Engine 初期 provider。
2. TTS Engine 初期 provider。
3. MacBook 側 service 管理方法。
4. TTS response 方式。
5. Viewer から直接使う STT stream URL。

実装時は Gateway contract のテストを先に作り、Engine adapter の実装差し替えで RenCrow Core の contract が変わらないことを確認する。
