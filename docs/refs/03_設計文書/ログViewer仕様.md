# ログViewer仕様

**作成日**: 2026-03-19
**対象**: RenCrow Live Viewer
**対象実装**:
- `internal/adapter/viewer/viewer.html`
- `internal/adapter/viewer/handler.go`
- `internal/adapter/viewer/hub.go`
- `internal/adapter/viewer/evidence_handler.go`
- `internal/adapter/viewer/audio_router_sse.go`
- `cmd/picoclaw/main.go`

---

## 1. 目的

ログViewerは、RenCrow の実行状態、エージェント間イベント、IdleChat、実行証跡をブラウザ上でリアルタイム観測するための統合 Viewer である。

本 Viewer は単純なログ表示ではなく、以下を一体で扱う。

- ライブイベント監視
- ジョブ進捗監視
- 実行証跡の参照
- IdleChat 制御
- セッションとジョブの一覧表示

---

## 2. 全体構成

### 2.1 構成要素

- `viewer.html`
  Viewer の単一 HTML UI
- `EventHub`
  SSE 用イベント蓄積・配信
- `HandleSSE`
  Viewer 向け SSE 配信
- `HandleSend`
  Viewer 入力欄からのメッセージ送信
- `HandleEvidenceRecent / Detail / Summary`
  execution evidence 参照 API
- `HandleAudioRouterSSE`
  audio-router 向けの TTS chunk SSE

### 2.2 イベント流れ

```text
Orchestrator / IdleChat / TTS
  -> EventHub.OnEvent()
  -> history 保存 + SSE broadcast
  -> /viewer/events
  -> viewer.html
```

### 2.3 設計方針

- Viewer は単一ページで運用する
- サーバ側 push は SSE を使う
- 初回接続時は `EventHub` の履歴を先に返す
- 以後はリアルタイムイベントを追記する
- 再接続時は `Last-Event-ID` で既読分を飛ばす

---

## 3. 提供エンドポイント

### 3.1 ページと静的資産

| パス | メソッド | 用途 |
|---|---|---|
| `/viewer` | `GET` | Viewer 画面 |
| `/viewer/logo.png` | `GET` | Viewer ロゴ |

### 3.2 ライブイベント

| パス | メソッド | 用途 |
|---|---|---|
| `/viewer/events` | `GET` | 全体イベント SSE |
| `/audio-router/events` | `GET` | audio-router 向け `tts.audio_chunk` SSE |

### 3.3 Viewer 操作

| パス | メソッド | 用途 |
|---|---|---|
| `/viewer/send` | `POST` | Viewer 入力欄からメッセージ送信 |

### 3.4 Evidence API

| パス | メソッド | 用途 |
|---|---|---|
| `/viewer/evidence/recent` | `GET` | 直近 evidence 一覧 |
| `/viewer/evidence/detail` | `GET` | `job_id` 単位の evidence 詳細 |
| `/viewer/evidence/summary` | `GET` | evidence 集計 |

### 3.5 Viewer 補助 API

| パス | メソッド | 用途 |
|---|---|---|
| `/viewer/glossary/recent` | `GET` | glossary recent 一覧 |
| `/viewer/idlechat/status` | `GET` | IdleChat 状態 |
| `/viewer/idlechat/logs` | `GET` | IdleChat 履歴 |
| `/viewer/idlechat/start` | `POST` | 通常 IdleChat 開始 |
| `/viewer/idlechat/forecast` | `POST` | 未来展望開始 |
| `/viewer/idlechat/story` | `POST` | Story mode 開始 |
| `/viewer/idlechat/stop` | `POST` | IdleChat 停止 |

---

## 4. EventHub 仕様

### 4.1 役割

`EventHub` は orchestrator event の in-memory ハブである。

- イベントへ連番 `Seq` を付与
- 直近履歴を保持
- 全接続クライアントへ broadcast

### 4.2 履歴保持

- `NewEventHub(200)` で初期化される
- 最大履歴件数は `200`
- 上限超過時は古い履歴を破棄する

### 4.3 配信特性

- クライアントごとに buffered channel を持つ
- channel 容量は `64`
- 遅いクライアントにはイベントを drop する

このため、Viewer は「完全な監査ログ保管」ではなく「直近イベントのライブ観測」を目的とする。

---

## 5. SSE 仕様

### 5.1 `/viewer/events`

返却形式は標準的な `text/event-stream`。

- 先頭で履歴を送る
- `id: <seq>` を付ける
- `data: <json>` を送る

イベント payload は `orchestrator.OrchestratorEvent` を JSON 化したもの。

### 5.2 `Last-Event-ID`

`HandleSSE` は `Last-Event-ID` を解釈し、その seq 以下の履歴イベントをスキップする。

### 5.3 `/audio-router/events`

この SSE は `/viewer/events` のサブセットであり、以下のみを通す。

- `type == "tts.audio_chunk"`
- `content` が JSON
- `character_id` が存在
- `audio_url` または `audio_path` が存在

出力イベント名は `event: tts.audio_chunk`。

---

## 6. Viewer 送信仕様

### 6.1 `/viewer/send`

入力 JSON:

```json
{
  "message": "こんにちは"
}
```

制約:

- `POST` のみ許可
- body 読み込み上限は `4096` bytes
- `message` 必須

### 6.2 実行方式

- HTTP 応答は即時 `{"ok":true}`
- 実処理は goroutine で非同期実行
- 実行結果の表示は SSE イベントで反映する

---

## 7. Evidence API 仕様

### 7.1 `/viewer/evidence/recent`

クエリ:

- `limit`
  - 省略時 `20`
  - 最大 `100`

レスポンス:

```json
{
  "items": [...]
}
```

### 7.2 `/viewer/evidence/detail`

クエリ:

- `job_id` 必須

レスポンス:

```json
{
  "item": {...}
}
```

存在しない場合は `404`。

### 7.3 `/viewer/evidence/summary`

レスポンス:

```json
{
  "summary": {
    "status": {...},
    "error_kind": {...}
  }
}
```

---

## 8. UI タブ構成

Viewer は以下のタブを持つ。

### 8.1 Overview

用途:

- エージェントの現状態一覧
- 最終イベント、route、peer、job の簡易確認

### 8.2 Progress

用途:

- Agent progress の視覚化
- Job progress の段階表示
- phase, owner, retry, failure, summary の確認

### 8.3 Timeline

用途:

- 対話系イベントの時系列表示
- ライブ会話の追跡

フィルタ:

- `type`
- `agent`
- `route`
- `job id`
- `content`

表示対象:

- `message.received`
- `agent.note`
- `agent.thinking`
- `agent.response`
- `idlechat.message`

備考:

- `idlechat.summary` は Timeline には出さない
- 自動追尾と「最新表示」ボタンを持つ

### 8.4 System

用途:

- システム系イベントの分離表示

主対象:

- `routing.decision`
- `entry.stage`
- `tts.audio_chunk`

フィルタ:

- 表示プリセット
- type
- content

### 8.5 IdleChat

用途:

- IdleChat 状態表示
- current topic 表示
- 履歴一覧
- transcript 展開
- transcript copy
- 通常 / 未来展望 / 物語モードの制御

### 8.6 Sessions

用途:

- session ごとのメッセージ数
- 最終 route
- active agents

### 8.7 Jobs

用途:

- job 一覧
- execution evidence 一覧
- evidence detail 閲覧
- evidence JSON / summary の copy

---

## 9. クライアント側状態管理

`viewer.html` は少なくとも以下の状態を持つ。

- `logs`
- `sessions`
- `jobs`
- `agents`
- `evidence`
- `evidenceSummary`
- `idleChat`
- `progressOpenJobs`

IdleChat の選択モードは `localStorage` に保存する。

- key: `idlechat.selectedMode`

---

## 10. 表示・件数制御

### 10.1 フロント側上限

- `MAX_LOGS = 500`
- `MAX_TIMELINE_NODES = 400`
- `MAX_SEEN_EVENTS = 4000`

### 10.2 オフライン判定

- `OFFLINE_MS = 120000`

一定時間イベント更新がない agent は offline 扱いにする。

---

## 11. ログViewerが扱うイベント種別

代表的なイベント種別:

- `message.received`
- `routing.decision`
- `agent.note`
- `agent.start`
- `agent.thinking`
- `agent.response`
- `entry.stage`
- `tts.audio_chunk`
- `idlechat.message`
- `idlechat.summary`

用途ごとにタブ側で再分類している。

---

## 12. 非機能仕様

### 12.1 応答性

- Viewer 送信は非同期化し、HTTP 応答をブロックしない
- SSE は継続接続で追従する

### 12.2 再接続性

- `Last-Event-ID` を使った履歴再取得に対応

### 12.3 保持期間

- EventHub の履歴は短期メモリ保持のみ
- 長期証跡は evidence API 側を参照する

---

## 13. 既知の位置づけ

ログViewerは現時点で「ログ閲覧専用画面」ではなく、運用コンソール寄りの Viewer である。

したがって本仕様は以下を含む。

- ログ表示
- 進捗表示
- 実行証跡参照
- IdleChat 操作

必要に応じて今後は以下へ分離可能である。

- Live Timeline Viewer
- Evidence Viewer
- IdleChat Console

