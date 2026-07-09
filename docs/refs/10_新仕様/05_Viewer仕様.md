# Viewer 仕様

## 目的

Viewer は RenCrow の操作面と観測面である。

Viewer は単なる静的 UI ではない。HTTP API、SSE event、event log、history、monitor、Memory / Source Registry、IdleChat、STT/TTS の状態を投影する。

## 境界

Viewer では次を混同しない。

- 表示本文
- SSE event
- event log
- history
- audio trigger
- lipsync trigger
- runtime config 表示
- debug / monitor 情報

音声 chunk は本文表示の唯一の根拠ではない。音声 chunk は音声再生と口パク trigger のための契約である。

## 主な実装箇所

| 領域 | 主担当 |
| --- | --- |
| Viewer handler | `internal/adapter/viewer/*_handler.go` |
| Viewer send | `internal/adapter/viewer/handler_send.go` |
| static page/assets | `internal/adapter/viewer/viewer.html`, `internal/adapter/viewer/assets/` |
| SSE hub | `internal/adapter/viewer/hub.go` |
| event log | `internal/adapter/viewer/event_log_store.go`, `event_log_gc.go` |
| monitor | `internal/adapter/viewer/monitor_*.go` |
| runtime config | `internal/adapter/viewer/debug_system_handler.go`, `cmd/picoclaw/routes.go`, `cmd/picoclaw/runtime_*.go` |
| LLM Ops | `internal/adapter/viewer/llm_ops_handler.go` |
| Source Registry | `internal/adapter/viewer/source_registry_handler.go` |
| Memory API | `internal/adapter/viewer/memory_*_handler.go` |
| Roles tab | `internal/adapter/viewer/assets/js/tabs/roles.js` |
| News Pack | `internal/adapter/viewer/assets/js/tabs/news-pack.js` |
| attachment / upload | `internal/domain/attachment`, `internal/application/attachment`, `internal/adapter/viewer/handler_send.go` |

## 常用タブ / Inspector

Viewer は運用・会話・記憶の観測 UI を持つ。

- Roles tab: `mio/shiro/aka/ao/gin/kin`、Chat / Worker / Wild / Coder、model alias、route を表示・選択する。
- Memory Inspector: L1 memory、News、Daily Digest、Knowledge、Search Cache、Event Log、L0/L1/L2/L3 layer を横断表示する。
- News Pack: News、digest、出典詳細、関連 memory、Recall trace 利用履歴を表示する。
- Source Registry 操作: source の登録、JSON/YAML import/export、個別 run、fetch / validate / promote を操作する。
- Viewer upload / attachment: Viewer 入力に添付情報を付与し、routing classification と本文表示を混同しない。

## Viewer upload / attachment

Viewer 添付は `internal/domain/attachment` の contract に正規化してから orchestration へ渡す。

- image は file metadata と raw data を保持し、画像対応 provider へ `MessagePartImage` として渡せる。
- text / JSON / YAML / XML / CSV / Markdown は UTF-8 text として本文抽出し、`ExtractedText` に保存する。
- PDF は依存を増やさない軽量抽出を行い、埋め込み literal text を取れる場合だけ `ExtractedText` に保存する。
- 抽出できない PDF は upload 自体を失敗にせず、`ExtractionError` として attachment metadata に残す。
- 抽出本文に prompt injection pattern が含まれる場合は、拒否とは別の `SecurityWarnings` metadata に残す。
- 抽出本文は routing classification の補助情報であり、Viewer 表示本文や正式 memory state と混同しない。
- OCR、画像 PDF 高精度解析、外部クラウド抽出、添付本文の無審査 memory promote は現行範囲外である。

外部チャネル添付は Viewer upload と同じ `Attachment` contract へ寄せる。現行実装では LINE の `image` / `file` message、Slack `files[]`、Discord relay `attachments[]`、Telegram `document` / `photo` を channel adapter 側で download し、`internal/application/attachment.IncomingFile` として共通 pipeline へ渡す。channel ごとの署名検証と download は Adapter の責務であり、Application attachment pipeline に channel 固有 API を混ぜない。download / 保存 / MIME / size 失敗は通常 chat 成功として隠さない。

## route / API

代表的な route:

- `/viewer`
- `/viewer/assets/`
- `/viewer/runtime-config`
- `/viewer/send`
- `/viewer/status`
- `/viewer/jobs`
- `/viewer/logs`
- `/viewer/audit/summary`
- `/viewer/evidence/*`
- `/viewer/memory/*`
- `/viewer/source-registry`
- `/viewer/recall/traces`
- `/viewer/idlechat/*`
- `/viewer/tts/audio`
- `/viewer/llm-ops/*`

route 登録は `cmd/picoclaw/routes.go` が担当する。handler 本体は `internal/adapter/viewer` に置く。

## runtime config

Viewer runtime config は表示と操作のための投影である。

次を混同しない。

- repo example config
- live `~/.picoclaw/config.yaml`
- process 起動時に解決された runtime config
- Viewer に返す runtime config

live runtime を判断する場合は、repo example ではなく `/health`、`/viewer/runtime-config`、実設定、fresh log を確認する。

## IdleChat と Viewer

IdleChat event は Viewer に表示されるが、IdleChat の raw response、view data、audio trigger は別契約である。

- raw response: LLM の素の出力、診断用に保持する。
- view data: Viewer 表示用に整形された本文。
- audio trigger: TTS / lipsync 起動用。

## 検証

Viewer 変更では DOM 存在だけで完了扱いしない。

最低 1 session で確認する。

- Viewer が開く。
- 入力できる。
- `/viewer/send` が成功する。
- route と response が対応する。
- 添付 text / PDF の抽出結果または抽出エラーが attachment metadata として追える。
- prompt injection warning が本文や memory と混ざらず metadata として追える。
- SSE event が届く。
- event log / history に残る。
- error / invalid response が隠れない。
- TTS / lipsync trigger が本文表示と混ざらない。

主な確認:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./internal/adapter/viewer
GOCACHE=/tmp/picoclaw-gocache go test -count=1 -tags=e2e ./test/e2e
```

live 確認:

```bash
curl -fsS http://127.0.0.1:18790/health
curl -fsS http://127.0.0.1:18790/viewer/runtime-config
```
