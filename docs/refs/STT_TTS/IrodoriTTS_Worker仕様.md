# IrodoriTTS Worker 仕様書

> 注意: この文書は将来のRenCrow側TTS Worker/Gateway仕様です。2026-05-06時点のIrodori-TTS実装は独自REST APIではなく、Gradio HTTP APIを直接利用します。現行連携仕様は [IrodoriTTS_HTTP_API仕様.md](./IrodoriTTS_HTTP_API仕様.md) を正とします。

## 1. 目的

RenCrowでIrodoriTTSを利用し、Mio用の女声とShiro用の男声を常駐させるTTSワーカーの仕様を定義する。

既存のTTSインタフェースはStyle-Bert-VITS2（以下SBV2）相当を基本としつつ、IrodoriTTS固有の参照音声、VoiceDesign caption、voice profile、emotion、seed等を拡張フィールドとして扱う。

目的は以下。

- 既存SBV2互換のTTS呼び出しを維持する
- IrodoriTTSによるMio女声・Shiro男声を常駐させる
- 1つのIrodoriTTSモデルをプリロードし、2音声をvoice profileとして切り替える
- HTTPまたはHTTPS経由で音声生成を提供する
- 将来的なSBV2との併用・切替に備える

---

## 2. 基本方針

IrodoriTTSでは、MioとShiroを別々のモデルとしてロードしない。

1つのIrodoriTTSモデルをワーカー起動時にロードし、Mio/Shiroは以下のいずれかの方式でvoice profileとして管理する。

- 参照音声方式
- VoiceDesign caption方式
- 参照音声方式 + caption補助

初期運用では、キャラ音声の安定性を優先し、参照音声方式を基本とする。

```text
IrodoriTTS Worker
  ├─ IrodoriTTS model preload
  ├─ voice profile cache
  │   ├─ mio_neutral
  │   └─ shiro_neutral
  ├─ request queue
  └─ audio output
```

生成処理は、初期段階では必ず直列実行とする。

```yaml
queue:
  concurrency: 1
```

理由は以下。

- IrodoriTTSはSBV2よりVRAM使用量が大きい
- 2音声同時生成ではなく、1モデル上のprofile切替を前提とする
- 直列キューによりVRAM使用量と生成安定性を優先する

---

## 3. システム構成

### 3.1 全体構成

```text
RenCrow Chat
  ↓ HTTP / HTTPS
TTS Client or TTS Gateway
  ↓
IrodoriTTS Worker
  ├─ Model Loader
  ├─ Voice Profile Manager
  ├─ Request Queue
  ├─ Audio Generator
  ├─ Audio Storage
  └─ Management API
```

### 3.2 役割

| コンポーネント | 役割 |
|---|---|
| RenCrow Chat | 発話テキストとspeaker情報を生成する |
| TTS Client / Gateway | TTSエンジンへのHTTPリクエストを行う |
| IrodoriTTS Worker | IrodoriTTSモデルを常駐し音声生成を行う |
| Voice Profile Manager | Mio/Shiroの参照音声・caption・styleを管理する |
| Request Queue | 音声生成要求を順番に処理する |
| Audio Storage | 生成済みwav等を一時保存する |
| Management API | health/status/voices/preload等を提供する |

---

## 4. 音声プロファイル

### 4.1 初期音声

| speaker | 表示名 | 性別 | 用途 | 初期style |
|---|---|---|---|---|
| mio | Mio | female | 女声キャラクター | neutral |
| shiro | Shiro | male | 男声キャラクター | neutral |

### 4.2 参照音声

参照音声は各キャラ30秒前後のニュートラル音声を基本とする。

```text
voices/
  ├─ mio_neutral_30s.wav
  └─ shiro_neutral_30s.wav
```

参照音声は以下の条件を満たすこと。

- BGMなし
- ノイズが少ない
- 反響が少ない
- 一定のマイク距離
- 一定の声量
- 感情を強く入れない
- 笑い声・泣き声・極端な演技を入れない
- 普通の会話速度

### 4.3 感情別参照音声

初期段階では感情別参照音声は必須としない。

感情表現は以下で制御する。

- テキスト表現
- style指定
- Irodori拡張のemotion
- 必要に応じて絵文字・caption

将来的に必要であれば以下のように追加する。

```text
voices/
  ├─ mio_neutral_30s.wav
  ├─ mio_soft_30s.wav
  ├─ mio_bright_30s.wav
  ├─ shiro_neutral_30s.wav
  ├─ shiro_serious_30s.wav
  └─ shiro_gentle_30s.wav
```

ただし、感情別参照音声を増やすとキャラ声の同一性が揺れる可能性があるため、初期運用ではneutralのみを推奨する。

---

## 5. API仕様

### 5.1 基本方針

APIはSBV2互換を基本とする。

既存TTSクライアントが最低限以下のフィールドで呼び出せることを前提とする。

```json
{
  "text": "こんにちは。",
  "speaker": "mio",
  "style": "neutral"
}
```

IrodoriTTS固有の設定は `irodori` オブジェクトに閉じ込める。

```json
{
  "text": "こんにちは。",
  "speaker": "mio",
  "style": "neutral",
  "irodori": {
    "mode": "reference",
    "voice_profile": "mio_neutral",
    "ref_audio_profile": "mio_neutral_30s"
  }
}
```

### 5.2 エンドポイント一覧

| Method | Path | 用途 |
|---|---|---|
| GET | `/health` | 生存確認 |
| GET | `/status` | モデル・VRAM・キュー状態取得 |
| GET | `/voices` | 利用可能なspeaker/style一覧 |
| POST | `/preload` | モデルまたはvoice profileのプリロード |
| POST | `/reload_voice` | voice profileの再読み込み |
| POST | `/tts` | 同期音声生成 |
| POST | `/tts/async` | 非同期音声生成 |
| GET | `/tts/job/{job_id}` | 非同期ジョブ状態取得 |
| GET | `/audio/{audio_id}.wav` | 生成済み音声取得 |

### 5.3 POST /tts

同期音声生成を行う。

Request:

```json
{
  "text": "れんさん、順番に確認します。",
  "speaker": "mio",
  "style": "neutral",
  "language": "ja",
  "format": "wav",
  "speed": 1.0,
  "return_type": "binary",
  "irodori": {
    "mode": "reference",
    "voice_profile": "mio_neutral",
    "ref_audio_profile": "mio_neutral_30s",
    "emotion": "gentle",
    "caption": null,
    "seed": 12345,
    "temperature": 0.7
  }
}
```

Request fields:

| フィールド | 必須 | 型 | 説明 |
|---|---:|---|---|
| text | yes | string | 読み上げる本文 |
| speaker | yes | string | mio / shiro 等 |
| style | no | string | neutral / soft / serious 等 |
| language | no | string | 初期値 ja |
| format | no | string | wav / pcm 等 |
| speed | no | number | 話速 |
| return_type | no | string | binary / file / json |
| irodori | no | object | IrodoriTTS拡張設定 |

`irodori` fields:

| フィールド | 型 | 説明 |
|---|---|---|
| mode | string | reference / voice_design |
| voice_profile | string | 使用するvoice profile名 |
| ref_audio_profile | string | 使用する参照音声profile名 |
| emotion | string | gentle / neutral / serious 等 |
| caption | string | VoiceDesign caption |
| seed | integer | 生成安定化用seed |
| temperature | number | 生成温度 |
| top_p | number | サンプリング制御 |
| extra | object | 将来拡張用 |

`return_type = binary` の場合、音声バイナリを直接返す。

```http
200 OK
Content-Type: audio/wav
```

`return_type = json` または `file` の場合、メタ情報を返す。

```json
{
  "event_id": "evt_20260506_0001",
  "speaker": "mio",
  "style": "neutral",
  "engine": "irodori",
  "status": "completed",
  "audio_url": "/audio/evt_20260506_0001.wav",
  "duration_ms": 2480,
  "queue_wait_ms": 120,
  "generation_ms": 1840
}
```

### 5.4 POST /tts/async

非同期音声生成を行う。Requestは `POST /tts` と同一。

Response:

```json
{
  "job_id": "job_20260506_0001",
  "event_id": "evt_20260506_0001",
  "status": "queued",
  "queue_position": 2
}
```

### 5.5 GET /tts/job/{job_id}

非同期ジョブの状態を取得する。

```json
{
  "job_id": "job_20260506_0001",
  "event_id": "evt_20260506_0001",
  "status": "completed",
  "speaker": "mio",
  "audio_url": "/audio/evt_20260506_0001.wav",
  "queue_wait_ms": 120,
  "generation_ms": 1840,
  "error": null
}
```

`status` は以下のいずれかとする。

- queued
- running
- completed
- failed
- cancelled

### 5.6 GET /voices

利用可能な音声一覧を返す。

```json
{
  "engine": "irodori",
  "model_loaded": true,
  "voices": [
    {
      "speaker": "mio",
      "display_name": "Mio",
      "gender": "female",
      "styles": ["neutral", "soft", "bright"],
      "default_style": "neutral",
      "preloaded": true
    },
    {
      "speaker": "shiro",
      "display_name": "Shiro",
      "gender": "male",
      "styles": ["neutral", "serious", "gentle"],
      "default_style": "neutral",
      "preloaded": true
    }
  ]
}
```

### 5.7 GET /status

ワーカー状態を返す。

```json
{
  "engine": "irodori",
  "model_loaded": true,
  "device": "cuda:0",
  "vram_used_mb": 7420,
  "queue_size": 2,
  "queue_concurrency": 1,
  "current_job": "evt_20260506_0002",
  "loaded_profiles": ["mio_neutral", "shiro_neutral"],
  "uptime_sec": 3600
}
```

### 5.8 GET /health

生存確認を行う。

```json
{
  "status": "ok",
  "engine": "irodori",
  "model_loaded": true
}
```

---

## 6. 設定ファイル仕様

設定ファイルはYAMLとする。

```yaml
tts:
  engine: irodori

  transport:
    protocol: http
    host: 0.0.0.0
    port: 50021
    https:
      enabled: false
      cert_file: null
      key_file: null

  model:
    name: irodori-tts-v2
    device: cuda:0
    preload: true

  preload:
    model: true
    voice_profiles:
      - mio_neutral
      - shiro_neutral

  queue:
    enabled: true
    max_size: 32
    concurrency: 1
    timeout_sec: 120

  output:
    default_format: wav
    sample_rate: 44100
    audio_dir: data/audio
    cleanup_after_sec: 3600

voices:
  mio:
    display_name: Mio
    gender: female
    default_style: neutral
    styles:
      neutral:
        irodori:
          mode: reference
          voice_profile: mio_neutral
          ref_audio: voices/mio_neutral_30s.wav
          ref_audio_profile: mio_neutral_30s
          emotion: neutral
          speed: 1.0
          seed: 12345
      soft:
        irodori:
          mode: reference
          voice_profile: mio_neutral
          ref_audio_profile: mio_neutral_30s
          emotion: soft
          speed: 0.98
          seed: 12345

  shiro:
    display_name: Shiro
    gender: male
    default_style: neutral
    styles:
      neutral:
        irodori:
          mode: reference
          voice_profile: shiro_neutral
          ref_audio: voices/shiro_neutral_30s.wav
          ref_audio_profile: shiro_neutral_30s
          emotion: neutral
          speed: 0.96
          seed: 12345
      serious:
        irodori:
          mode: reference
          voice_profile: shiro_neutral
          ref_audio_profile: shiro_neutral_30s
          emotion: serious
          speed: 0.94
          seed: 12345
```

---

## 7. SBV2互換方針

SBV2互換として、以下の項目は可能な限り維持する。

| 項目 | 方針 |
|---|---|
| text | 維持 |
| speaker | 維持 |
| style | 維持 |
| speed | 維持 |
| format | 維持 |
| 音声バイナリ応答 | 維持 |
| HTTP API | 維持 |

Irodori固有項目は `irodori` オブジェクト内に閉じ込め、既存クライアントへの影響を最小化する。

以下のような従来形式でも動作すること。

```json
{
  "text": "こんにちは。",
  "speaker": "mio",
  "style": "neutral"
}
```

---

## 8. HTTPS方針

初期運用では、ワーカー自体はHTTPでよい。

外部PCやスマートフォンからアクセスする場合は、以下のいずれかを使用する。

- Caddy
- nginx
- Tailscale
- Cloudflare Tunnel
- リバースプロキシによるTLS終端

推奨構成は以下。

```text
Client
  ↓ HTTPS
Reverse Proxy
  ↓ HTTP
IrodoriTTS Worker
```

IrodoriTTS Worker自体にHTTPS機能を持たせることも可能だが、証明書管理や更新を考えると、前段proxyでTLS終端する構成を優先する。

---

## 9. キュー仕様

### 9.1 基本方針

初期運用では、IrodoriTTSの生成は直列処理とする。

```yaml
queue:
  concurrency: 1
```

### 9.2 キュー制御

| 項目 | 初期値 | 説明 |
|---|---:|---|
| max_size | 32 | 最大待ちリクエスト数 |
| concurrency | 1 | 同時生成数 |
| timeout_sec | 120 | 1生成あたりのタイムアウト |
| priority | optional | 将来拡張 |
| cancel | optional | 将来拡張 |

### 9.3 2音声交互生成

Mio/Shiroを交互に発話させる場合も、ワーカー側は通常のキューとして処理する。

```text
queue:
  1. speaker=mio
  2. speaker=shiro
  3. speaker=mio
  4. speaker=shiro
```

speakerごとにvoice profileを切り替える。

---

## 10. VRAM方針

初期構成では以下を前提とする。

- IrodoriTTS model: 1回ロード
- Mio/Shiro: voice profileとしてプリロード
- 生成: 直列

この場合、2音声運用でもモデルを2本ロードしないため、VRAMは2倍にはならない。

ただしIrodoriTTSはSBV2より重い可能性が高いため、運用開始時に以下を測定する。

1. モデルロード直後のVRAM
2. Mio voice profileロード後のVRAM
3. Shiro voice profileロード後のVRAM
4. 30秒程度の音声生成中ピークVRAM
5. 連続生成時のVRAMリーク有無

PowerShellでの確認例。

```powershell
nvidia-smi -l 1
```

---

## 11. ログ仕様

各リクエストにはevent_idを付与する。

### 11.1 ログ項目

```json
{
  "event_id": "evt_20260506_0001",
  "job_id": "job_20260506_0001",
  "engine": "irodori",
  "speaker": "mio",
  "style": "neutral",
  "text_length": 42,
  "status": "completed",
  "queue_wait_ms": 120,
  "generation_ms": 1840,
  "duration_ms": 2480,
  "vram_used_mb": 7420,
  "created_at": "2026-05-06T12:00:00+09:00",
  "completed_at": "2026-05-06T12:00:03+09:00",
  "error": null
}
```

### 11.2 ログ保存先

初期運用ではJSONLを推奨する。

```text
logs/
  └─ tts_irodori_YYYYMMDD.jsonl
```

将来的にはSQLiteまたはRenCrow Event Logへ統合する。

---

## 12. エラー仕様

### 12.1 エラーレスポンス

```json
{
  "event_id": "evt_20260506_0001",
  "status": "failed",
  "error": {
    "code": "VOICE_PROFILE_NOT_FOUND",
    "message": "voice profile not found: mio_soft"
  }
}
```

### 12.2 エラーコード

| code | 説明 |
|---|---|
| INVALID_REQUEST | リクエスト形式不正 |
| TEXT_EMPTY | textが空 |
| SPEAKER_NOT_FOUND | speakerが存在しない |
| STYLE_NOT_FOUND | styleが存在しない |
| VOICE_PROFILE_NOT_FOUND | voice profileが存在しない |
| MODEL_NOT_LOADED | モデル未ロード |
| QUEUE_FULL | キューが満杯 |
| GENERATION_TIMEOUT | 生成タイムアウト |
| GENERATION_FAILED | 生成失敗 |
| CUDA_OOM | GPUメモリ不足 |
| INTERNAL_ERROR | 内部エラー |

---

## 13. 実装優先順位

### Phase 1: 最小実装

- IrodoriTTSモデルの起動時ロード
- Mio/Shiro参照音声profileのプリロード
- `POST /tts`
- `GET /health`
- `GET /voices`
- キュー直列処理
- wavバイナリ応答

### Phase 2: 運用API

- `GET /status`
- `POST /tts/async`
- `GET /tts/job/{job_id}`
- audio_url返却
- JSONLログ
- VRAM測定ログ

### Phase 3: 拡張

- HTTPS前段proxy対応
- VoiceDesign caption対応
- emotion/style拡張
- reload_voice
- 複数style profile
- SBV2とのGateway統合

---

## 14. 初期実装時の推奨構成

初期は以下の構成を採用する。

```text
IrodoriTTS Worker: 1本
Model preload: yes
Voice profiles: mio_neutral, shiro_neutral
Queue concurrency: 1
Transport: HTTP
HTTPS: reverse proxyで対応
Response: audio/wav binary
Log: JSONL
```

この構成により、以下を満たす。

- SBV2互換を維持できる
- Mio/Shiroの男声・女声を常駐できる
- VRAM使用量を抑えられる
- 既存RenCrow側のTTS呼び出しを大きく変更しない
- Irodori固有機能を段階的に追加できる

---

## 15. 未決事項

| 項目 | 状態 |
|---|---|
| IrodoriTTSの実測VRAM | 要測定 |
| 参照音声30秒での声質安定度 | 要検証 |
| VoiceDesign caption併用の必要性 | Phase 3で判断 |
| SBV2 Gatewayとの統合方式 | 要設計 |
| HTTPSをTTS Worker内で持つかproxyに任せるか | proxy推奨 |
| 音声ファイル保存期間 | 初期値1時間、要調整 |
| audio format | 初期wav、必要に応じてpcm/mp3 |

---

## 16. まとめ

IrodoriTTS Workerは、SBV2互換のTTS APIを維持しつつ、Irodori固有の参照音声・VoiceDesign caption・voice profileを拡張として扱う。

初期実装では、1つのIrodoriTTSモデルを常駐させ、Mio女声・Shiro男声をvoice profileとしてプリロードする。生成はキューで直列処理し、HTTP APIで音声を返す。HTTPSは必要に応じて前段proxyで対応する。

この設計により、RenCrowは既存のTTS構造を壊さず、IrodoriTTSによる男女2音声の常駐運用を追加できる。
