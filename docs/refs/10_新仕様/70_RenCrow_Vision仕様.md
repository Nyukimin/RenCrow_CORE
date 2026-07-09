# RenCrow_Vision 仕様

## 目的

`RenCrow_Vision` は、RenCrow の画像・動画認識を担当する独立モジュールである。

RenCrow 本体は Viewer / Chat / Worker / Coder の責務分離を維持し、画像・動画の重い推論 runtime、frame sampling、VLM 呼び出し、モデル管理を直接持たない。

`RenCrow_Vision` は MacBook など Vision 用 LLM が動作するマシン上で稼働し、RenCrow 本体から画像・動画ファイルと prompt を受け取り、解析結果テキストと構造化 metadata を返す。

## 基本方針

- `RenCrow_Vision` は `RenCrow_LLM` / `RenCrow_STT` / `RenCrow_TTS` と並ぶ外部機能モジュールとする。
- 画像・動画認識の primary provider は LLM サーバ側で設定する。
- RenCrow 本体は Vision provider を設定しない。RenCrow 本体は Vision server の endpoint を持つだけである。
- 画像・動画解析の結果は、必要に応じて Chat alias、つまり Gemma4 の文脈へ渡す。
- `/analyze` や Worker へ自動的に落とすことを正常系にしない。
- fallback で解析済みに見せてはいけない。Vision server が失敗した場合は失敗として表示・記録する。

## 配置

推奨配置:

```text
MacBook
  RenCrow_LLM      : Chat / Gemma4 / Vision-capable LLM server
  RenCrow_STT      : STT server
  RenCrow_TTS      : TTS server
  RenCrow_Vision   : image / video analysis server

fujitsu-ubunts
  RenCrow 本体
  Viewer
```

通信経路:

```text
Viewer
  -> RenCrow 本体 /viewer/send
  -> attachment store
  -> RenCrow Vision Adapter
  -> RenCrow_Vision /v1/vision/analyze
  -> analysis result
  -> Chat / Gemma4 context or direct Viewer response
```

## RenCrow_Vision の責務

`RenCrow_Vision` が持つ責務:

- 画像ファイル受信
- 動画ファイル受信
- MIME / size / duration の検証
- 動画 frame sampling
- 必要に応じた音声 track metadata 抽出
- Vision-capable LLM / VLM server への問い合わせ
- 解析結果の正規化
- health / model info / diagnostics の提供
- request_id / trace_id 付きログ

`RenCrow_Vision` が持たない責務:

- RenCrow の Chat / Worker / Coder ルーティング判断
- RenCrow memory への直接 write
- Source Registry への直接 promote
- Viewer UI state 管理
- 添付ファイルの最終保存場所管理
- RenCrow 本体の session / job state 管理

## RenCrow 本体の責務

RenCrow 本体が持つ責務:

- Viewer から添付画像・動画を受け取る
- attachment store に保存する
- Vision Adapter で `RenCrow_Vision` に転送する
- Vision 結果を Chat context に接続する
- Vision server の失敗を失敗として Viewer / event log に出す
- 設定値 `vision.enabled` / `vision.base_url` / timeout を読む
- `/viewer/runtime-config` / Ops で Vision 接続状態を表示する

RenCrow 本体が持たない責務:

- frame sampling 実装
- VLM provider 実装
- Vision model のロード・切替
- 動画解析 runtime の queue / GPU 管理

## API 契約

### `GET /health`

Vision server の readiness を返す。

成功例:

```json
{
  "ok": true,
  "status": "ready",
  "service": "rencrow-vision",
  "version": "0.1.0",
  "provider": "gemma4",
  "model": "Chat",
  "ready": {
    "model_loaded": true,
    "tmp_writable": true,
    "ffmpeg_available": true
  }
}
```

ready 判定条件:

- `ok=true`
- `status=ready`
- `ready.model_loaded=true`

HTTP 200 だけで ready と判定してはいけない。

### `GET /v1/models`

Vision server が使用可能な model alias / backend model を返す。

成功例:

```json
{
  "object": "list",
  "data": [
    {
      "id": "Vision",
      "object": "model",
      "owned_by": "local",
      "backend_model": "/Users/yukimi/models/gemma-4-12B-it-4bit"
    }
  ]
}
```

### `POST /v1/vision/analyze`

画像・動画解析 request を受け取る。

content type は `multipart/form-data` を基本とする。

request fields:

| field | required | 内容 |
| --- | --- | --- |
| `file` | yes | 画像または動画ファイル |
| `prompt` | no | ユーザー指示。空なら server 側 default prompt を使う |
| `kind` | no | `image` / `video`。未指定時は MIME から推定 |
| `request_id` | no | RenCrow 側 job_id / trace_id |
| `session_id` | no | RenCrow session id |
| `language` | no | `ja` を default とする |
| `max_frames` | no | 動画 frame sampling 上限 |
| `output_format` | no | `text` / `json`。default は `json` |

response success:

```json
{
  "ok": true,
  "request_id": "20260607-vision-001",
  "provider": "gemma4",
  "model": "Chat",
  "kind": "video",
  "summary": "人物が画面中央に映っています。",
  "text": "人物が画面中央に映っています。大きな動きはありません。",
  "segments": [
    {
      "start_ms": 0,
      "end_ms": 3000,
      "text": "人物が正面を向いています。",
      "confidence": 0.0
    }
  ],
  "metadata": {
    "mime_type": "video/mp4",
    "duration_ms": 5210,
    "frames_sampled": 6,
    "width": 1280,
    "height": 720
  }
}
```

response failure:

```json
{
  "ok": false,
  "request_id": "20260607-vision-001",
  "error_code": "VISION_PROVIDER_UNAVAILABLE",
  "message": "vision provider unavailable"
}
```

RenCrow 本体は `ok=false` を Chat 成功として扱ってはいけない。

## Error code

| code | 意味 |
| --- | --- |
| `VISION_PROVIDER_UNAVAILABLE` | Vision provider / LLM server に接続できない |
| `VISION_MODEL_NOT_READY` | model が未ロード |
| `VISION_UNSUPPORTED_MEDIA` | MIME / extension 非対応 |
| `VISION_FILE_TOO_LARGE` | size 上限超過 |
| `VISION_VIDEO_TOO_LONG` | duration 上限超過 |
| `VISION_DECODE_FAILED` | ffmpeg / image decode 失敗 |
| `VISION_INFERENCE_TIMEOUT` | 推論 timeout |
| `VISION_EMPTY_RESULT` | provider が空結果を返した |
| `VISION_INTERNAL_ERROR` | その他 server error |

## 対応 media

初期対応:

| kind | MIME | extension |
| --- | --- | --- |
| image | `image/png` | `.png` |
| image | `image/jpeg` | `.jpg`, `.jpeg` |
| image | `image/webp` | `.webp` |
| video | `video/mp4` | `.mp4`, `.m4v` |
| video | `video/quicktime` | `.mov` |
| video | `video/webm` | `.webm` |

上限は RenCrow 本体と Vision server の両方で持つ。

初期値案:

| 項目 | 値 |
| --- | --- |
| max image bytes | 20 MiB |
| max video bytes | 100 MiB |
| max video duration | 60 sec |
| max frames | 8 |
| request timeout | 120 sec |

## 動画解析 policy

動画解析は全 frame を逐次 LLM に投げない。

初期 policy:

- duration を取得する
- scene / interval sampling で最大 `max_frames` 枚を抽出する
- 各 frame に timestamp を付与する
- frame 群と prompt を Vision provider に渡す
- 必要なら segments を生成する

音声 track の文字起こしは `RenCrow_Vision` の主責務ではない。動画内音声の STT が必要な場合は、将来的に `RenCrow_STT` との明示連携として追加する。

## RenCrow Adapter 仕様

### Domain / module 境界

RenCrow 本体には Vision provider interface を置く。

想定 package:

```text
modules/vision
internal/infrastructure/vision
internal/adapter/modulebridge
cmd/rencrow/vision_runtime_*.go
```

interface 案:

```go
type AnalyzeRequest struct {
    RequestID   string
    SessionID   string
    Prompt      string
    Kind        string
    Filename    string
    ContentType string
    Data        []byte
    MaxFrames   int
}

type AnalyzeResult struct {
    OK        bool
    Provider  string
    Model     string
    Kind      string
    Summary   string
    Text      string
    Segments  []Segment
    Metadata  map[string]any
    ErrorCode string
    Message   string
}

type Provider interface {
    Name() string
    Health(ctx context.Context) (HealthReport, error)
    Analyze(ctx context.Context, req AnalyzeRequest) (AnalyzeResult, error)
}
```

### Adapter 実装

`internal/infrastructure/vision` に HTTP client provider を置く。

責務:

- `base_url` 正規化
- `/health` ready contract 確認
- `/v1/models` model info 取得
- `/v1/vision/analyze` multipart request 送信
- timeout / HTTP status / JSON error を `AnalyzeResult` へ正規化

HTTP status mapping:

| status | RenCrow error |
| --- | --- |
| 400 | `VISION_UNSUPPORTED_MEDIA` or request validation error |
| 413 | `VISION_FILE_TOO_LARGE` |
| 422 | `VISION_DECODE_FAILED` |
| 424 | `VISION_MODEL_NOT_READY` |
| 504 | `VISION_INFERENCE_TIMEOUT` |
| 5xx | `VISION_PROVIDER_UNAVAILABLE` |

### Orchestrator 接続

Viewer から画像・動画添付が来た場合、RenCrow は次の順序で扱う。

1. 添付を保存する
2. 画像・動画添付を検出する
3. `vision.enabled=true` かつ Vision provider ready なら Vision Adapter に渡す
4. Vision result の `text` / `summary` を user message に追記する
5. 最終的な Chat request は Chat alias、つまり Gemma4 に渡す

Chat に渡す文脈例:

```text
ユーザー入力:
この動画に人物が映っているか確認して

Vision解析結果:
人物が画面中央に映っています。大きな動きはありません。

添付ファイル:
- kind=video filename=sample.mp4 mime=video/mp4 size=5214440
```

画像・動画添付があるだけで `/analyze` route に固定してはいけない。ユーザーが明示的に `/analyze` を指定した場合も、Vision 解析は先に行い、その結果を route payload に含める。

### Viewer 接続

Viewer は既存の添付 UI を使う。

Viewer 側に Vision server URL を直接持たせない。

Viewer 表示は次を区別する。

- attachment received
- vision analyzing
- vision result
- chat response
- vision error

Vision error を Chat response と混同してはいけない。

### Runtime config

`~/.rencrow/config.yaml` に追加する設定案:

```yaml
vision:
  enabled: true
  base_url: http://192.168.1.207:8770
  timeout_ms: 120000
  max_image_bytes: 20971520
  max_video_bytes: 104857600
  max_video_duration_ms: 60000
  max_frames: 8
```

Tailscale 経由にする場合:

```yaml
vision:
  enabled: true
  base_url: http://100.85.222.99:8770
```

ただし、MacBook 側 service が Tailscale interface から到達可能であることを確認してから使う。

### Runtime diagnostics

RenCrow 本体は以下を Ops / System に表示する。

- `vision.enabled`
- `vision.base_url`
- `vision.ok`
- `vision.status`
- `vision.provider`
- `vision.model`
- `vision.ready.model_loaded`
- latest error

`/viewer/runtime-config` または `/viewer/debug/system` に Vision status を追加する。

## Security

- API key を平文保存しない。
- Vision server は LAN / Tailnet 内限定を default とする。
- public internet へ公開しない。
- ファイル名は信用しない。
- MIME と magic bytes を server 側で検証する。
- 一時ファイルは request_id ごとに隔離し、処理後に削除する。
- path traversal を拒否する。
- provider error に local path / secret を含めない。

## Logging

RenCrow_Vision server log:

- request_id
- session_id
- kind
- mime
- size_bytes
- duration_ms
- frames_sampled
- provider
- model
- latency_ms
- status
- error_code

RenCrow 本体 event log:

- `vision.request.started`
- `vision.request.completed`
- `vision.request.failed`
- `viewer.attachment.received`
- `agent.response`

Vision success と Chat response は別 event として残す。

## Test / 検証

RenCrow_Vision 側:

- `/health` が ready contract を満たす
- `/v1/models` が model alias を返す
- 画像 sample が解析できる
- 動画 sample が解析できる
- unsupported MIME を拒否する
- size 超過を拒否する
- provider timeout を `VISION_INFERENCE_TIMEOUT` で返す
- LLM server unavailable を `VISION_PROVIDER_UNAVAILABLE` で返す

RenCrow 本体側:

- Vision disabled 時、画像・動画添付を成功に見せない
- Vision enabled / ready 時、画像・動画添付を Vision Adapter に渡す
- Vision result を Chat alias に渡す
- `/analyze` 明示時も Vision result を先に付加する
- Vision error を Chat 成功として隠さない
- `/viewer/debug/system` で Vision health が見える

live test:

```bash
curl http://192.168.1.207:8770/health
curl http://192.168.1.207:8770/v1/models
curl -F "file=@assets/rencrow_detect_person.mp4" \
  -F "prompt=人物が映っているか確認してください。回答は1文。" \
  http://192.168.1.207:8770/v1/vision/analyze
```

Viewer E2E:

1. `https://fujitsu-ubunts.tailb07d8d.ts.net/viewer` を開く
2. 動画を添付する
3. `人物が映っているか確認して` と送る
4. event log で `vision.request.completed` を確認する
5. Chat response が Vision result を踏まえていることを確認する

## 移行手順

1. MacBook に `RenCrow_Vision` repo を作成する
2. `/health` / `/v1/models` / `/v1/vision/analyze` を実装する
3. MacBook local で画像・動画解析を確認する
4. LAN `192.168.1.207:<port>` で到達確認する
5. RenCrow 本体に Vision Adapter を追加する
6. `~/.rencrow/config.yaml` に `vision` 設定を追加する
7. Viewer から画像・動画添付 E2E を確認する
8. Tailscale IP `100.85.222.99` 経由に切り替える場合は、MacBook 側 bind / firewall / Tailscale ACL を確認する

## 未決事項

- Vision server port の正式値。初期案は `8770`。
- Gemma4 LLM server が画像・動画を受ける exact API contract。
- frame sampling policy の詳細。
- 動画内音声を STT 連携するかどうか。
- Vision result を Memory / Source Registry 候補にする条件。
