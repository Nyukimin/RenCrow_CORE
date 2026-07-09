# 実装仕様: ログViewer v1.0

**作成日**: 2026-03-19
**ステータス**: 実装済み機能の整理 + 目標仕様差分管理
**ベース**: 現行 `feature/rencrow` 実装
**依存**: `docs/ログViewer仕様.md`, `docs/現状実装仕様_20260319.md`

---

## 1. 概要

### 1.1 目的

RenCrow のログViewerは、実行中のイベント、ジョブ進捗、execution evidence、IdleChat 状態を単一画面で確認・操作するための運用 Viewer である。

本仕様は以下を同時に扱う。

- 現在コード上で実装済みの Viewer 構成
- 現行実装の責務と制約
- 今後の整理対象となる目標仕様との差分

### 1.2 位置づけ

`ログViewer仕様.md` は運用・UI 寄りの仕様であり、本書はその実装仕様版とする。

- `ログViewer仕様.md`
  利用者視点の構成と API 整理
- `実装仕様_ログViewer_v1.md`
  実装責務、データフロー、制約、改善余地の整理

### 1.3 設計原則

- **単一ページ運用**: ブラウザ上の単一 HTML で完結する
- **短期ライブ監視**: EventHub はリアルタイム観測を担う
- **長期証跡分離**: 長期的な execution report は evidence API で扱う
- **非同期 UI**: 入力送信は即応答し、結果は SSE で反映する
- **運用コンソール優先**: ログ閲覧だけでなく、IdleChat 制御と証跡参照も含める

---

## 2. アーキテクチャ

### 2.1 コンポーネント構成

```text
cmd/picoclaw/main.go
  ├── /viewer                      -> viewer.HandlePage
  ├── /viewer/logo.png             -> viewer.HandleLogo
  ├── /viewer/events               -> EventHub.HandleSSE
  ├── /viewer/send                 -> viewer.HandleSend
  ├── /viewer/evidence/*           -> evidence handlers
  ├── /viewer/glossary/recent      -> glossary handler
  ├── /viewer/idlechat/*           -> idlechat handlers
  └── /audio-router/events         -> viewer.HandleAudioRouterSSE

internal/adapter/viewer/
  ├── viewer.html
  ├── handler.go
  ├── hub.go
  ├── evidence_handler.go
  ├── audio_router_sse.go
  └── glossary_handler.go
```

### 2.2 依存関係

| コンポーネント | 依存先 | 用途 |
|---|---|---|
| `viewer.html` | `/viewer/events` | ライブイベント購読 |
| `viewer.html` | `/viewer/send` | ユーザー入力送信 |
| `viewer.html` | `/viewer/evidence/*` | execution evidence 表示 |
| `viewer.html` | `/viewer/idlechat/*` | IdleChat 状態・制御 |
| `HandleSSE` | `EventHub` | SSE 配信 |
| `HandleAudioRouterSSE` | `EventHub` | TTS chunk の抽出配信 |
| `EventHub` | `orchestrator.OrchestratorEvent` | イベント保持と配信 |

### 2.3 データフロー

```text
Orchestrator / IdleChat / TTS
  -> EventHub.OnEvent(ev)
  -> history append + seq 付与
  -> SSE broadcast

viewer.html
  -> EventSource('/viewer/events')
  -> state.logs / state.jobs / state.sessions / state.agents 更新
  -> 各タブへ再描画
```

---

## 3. EventHub / SSE 実装

### 3.1 EventHub の責務

`EventHub` は in-memory の短期イベント集約器であり、以下を担う。

- event ごとの `Seq` 採番
- 履歴リング保持
- SSE クライアントへの broadcast

### 3.2 現行実装

実装値は以下で固定されている。

- 履歴件数: `200`
- subscriber channel 容量: `64`
- slow client のイベントは drop

このため、Viewer は監査ログの完全保存には使わず、直近のライブ監視用途に限定する。

### 3.3 `/viewer/events`

`HandleSSE` の仕様:

- `Content-Type: text/event-stream`
- 先に履歴を送る
- 新規イベントを継続配信する
- `Last-Event-ID` を解釈し、既読分をスキップする

payload は `orchestrator.OrchestratorEvent` をそのまま JSON 化したもの。

### 3.4 `/audio-router/events`

Audio Router 向け SSE は `EventHub` のサブセット配信である。

通過条件:

- `ev.Type == "tts.audio_chunk"`
- `ev.Content` が JSON として解釈可能
- `character_id` を含む
- `audio_url` または `audio_path` を含む

この endpoint は Viewer 本体ではなく、外部音声ルータ向けの中継導線として機能する。

### 3.5 目標仕様との差分

現行実装では、EventHub は単一責務を保っている一方で、以下は未分離である。

- event 種別ごとの保持方針
- クライアント別の購読フィルタ
- 長期ログと短期ライブログの transport 分離

目標仕様では、EventHub は「ライブ観測専用」に固定し、長期保持は evidence / persistence 系に完全分離する。

---

## 4. Viewer UI 実装

### 4.1 タブ構成

現行 `viewer.html` は以下の 8 タブを持つ。

- `Ops`
- `Overview`
- `Progress`
- `Timeline`
- `System`
- `IdleChat`
- `Sessions`
- `Jobs`

### 4.2 タブ責務

| タブ | 責務 |
|---|---|
| `Ops` | 運用者向け要約表示 |
| `Overview` | エージェント状態の俯瞰 |
| `Progress` | 進行中ジョブの段階表示 |
| `Timeline` | 会話系イベントの時系列追跡 |
| `System` | ルーティング・entry・TTS 系の技術イベント確認 |
| `IdleChat` | IdleChat 状態と履歴、および制御 |
| `Sessions` | session 単位の集約表示 |
| `Jobs` | job と execution evidence の参照 |

### 4.3 クライアント状態

フロント側は少なくとも以下の state を保持する。

- `logs`
- `sessions`
- `jobs`
- `evidence`
- `evidenceSummary`
- `agents`
- `idleChat`
- `progressOpenJobs`

### 4.4 表示上限

現行の固定値:

- `MAX_LOGS = 500`
- `MAX_TIMELINE_NODES = 400`
- `MAX_SEEN_EVENTS = 4000`
- `OFFLINE_MS = 120000`

### 4.5 目標仕様との差分

現行 UI は 1 ファイルに状態・描画・制御が集約されている。

目標仕様では以下へ分離可能とする。

- Timeline Viewer
- Evidence Viewer
- IdleChat Console

ただし v1 では単一 HTML を維持する。

---

## 5. Viewer 送信実装

### 5.1 `/viewer/send`

`HandleSend` は Viewer からのメッセージ入力を受ける。

入力条件:

- `POST` のみ
- body 上限 `4096` bytes
- `{"message":"..."}` 形式
- 空メッセージは拒否

### 5.2 実行方式

- HTTP は即時 `{"ok":true}` を返す
- 実処理は goroutine でバックグラウンド実行
- 実際の結果表示は SSE イベントに依存する

### 5.3 制約

現行実装では request/response 型の同期チャットではなく、イベント駆動型の UI である。
したがって `/viewer/send` は応答本文で会話結果を返さない。

### 5.4 目標仕様との差分

将来的には以下の分離余地がある。

- command 系送信
- user message 系送信
- system prompt / debug 操作用送信

現行は単一 endpoint に集約されている。

---

## 6. Evidence API 実装

### 6.1 責務

evidence API は `ExecutionReport` の参照口であり、Viewer の `Jobs` タブで使う。

### 6.2 `/viewer/evidence/recent`

仕様:

- `GET` のみ
- `limit` 省略時 `20`
- `limit` 最大 `100`
- レスポンスは `{ "items": [...] }`

### 6.3 `/viewer/evidence/detail`

仕様:

- `GET` のみ
- `job_id` 必須
- 見つからない場合 `404`
- レスポンスは `{ "item": {...} }`

### 6.4 `/viewer/evidence/summary`

仕様:

- `GET` のみ
- レスポンスは `{ "summary": {...} }`

summary の主対象:

- `status`
- `error_kind`

### 6.5 現行 UI での利用

`Jobs` タブでは以下を提供する。

- live jobs 一覧
- selected live job detail
- evidence 一覧
- selected evidence detail
- `Prev` / `Next`
- `Copy JSON`
- `Copy Summary`
- `finished` 順の sort

### 6.6 目標仕様との差分

現行では evidence 表示ロジックが `viewer.html` 側へ密に埋め込まれている。
目標仕様では `ExecutionReport` のセクション表示を責務分割し、`steps`, `verification`, `repair` などの表示規約をサーバ側または共有 formatter に寄せる余地がある。

### 6.7 live jobs と evidence の責務分離

現行の `Jobs` タブは、見た目上は 1 つのタブだが、実体としては 2 系統のデータを扱う。

- `live jobs`
  - source: `MonitorStore`
  - endpoint: `/viewer/jobs`, `/viewer/job/detail`
  - event から導出した job の現在状態
- `execution evidence`
  - source: `workspace/execution_report.jsonl`
  - endpoint: `/viewer/evidence/recent`, `/viewer/evidence/detail`, `/viewer/evidence/summary`
  - 完了結果の長期証跡

この 2 つは同一ではない。`live jobs` は進行中でも見えるが、`evidence` は保存後にのみ見える。

完了判定、`mio_reported`、live job と evidence の優先順位は `docs/01_正本仕様/実装仕様.md` の「20. Viewer / Evidence / Job 実装仕様」を正本とする。

### 6.8 live jobs の内容

`/viewer/jobs` は `MonitorStore.Jobs()` を通じて、event から導出した `JobSnapshot` を返す。

少なくとも以下を含む。

- `job_id`
- `route`
- `phase`
- `owner`
- `status`
- `session_id`
- `channel`
- `chat_id`
- `started_at`
- `updated_at`
- `summary`
- `failure_kind`
- `failure_reason`
- `final_user_report`
- `mio_reported`
- `events`

`JobSnapshot` は event reducer の結果であり、workflow engine の正本ではない。現在値の観測用スナップショットとして扱う。

### 6.9 execution evidence の内容

`ExecutionReport` は job 完了時の証跡であり、`workspace/execution_report.jsonl` に保存される。

少なくとも以下を含む。

- `job_id`
- `goal`
- `status`
- `error_kind`
- `acceptance`
- `verification`
- `steps`
- `repair_count`
- `error`
- `created_at`
- `finished_at`

`evidence` は live job の派生表示ではなく、保存済み実行結果の記録である。

### 6.10 保存タイミング

現行では distributed orchestrator 側でも `ExecutionReport` 保存が入っている。

- `CHAT` 成功時
- `OPS` 成功時
- `CODE` 成功時
- 上記の失敗時

そのため、現在の `execution_report.jsonl` は旧来の `/new tts` 系専用ではなく、distributed `ProcessMessage` の結果も含む。

### 6.11 不一致が起こるケース

運用上、以下の状態は正常に起こりうる。

- `live jobs` にはあるが `evidence` にはまだない
  - job が進行中
  - まだ保存前
- `evidence` はあるが live job と event 数が一致しない
  - live 側は event からの導出表示であり、保存済み証跡そのものではない

以前は distributed orchestrator 側に evidence 保存がなく、`Jobs` タブの evidence 一覧が `2026-03-11` のまま止まって見える状態があった。現行ではこの点は解消済みである。

### 6.12 `/viewer/job/detail`

`/viewer/job/detail?job_id=...` は live job の詳細を返す。

現行では以下を含む。

- `item`
  - `JobSnapshot`
- `evidence`
  - 対応する `ExecutionReport` があれば添付

実装上は archived events も参照し、必要に応じて `job.Events` を補完する。

### 6.13 運用上の見方

`Jobs` タブを使う際の切り分けは以下。

- 進行中か、どこで止まっているかを見たい
  - `live jobs`
- 完了結果や証跡を見たい
  - `execution evidence`
- `job は見えるのに evidence がない`
  - まず進行中かどうかを確認する
  - 次に `System` または persisted logs を見る
  - 最後に evidence 保存失敗を疑う
- `evidence.status=passed` だが `job detail` が `phase=reporting` / `status=running` / `mio_reported=false`
  - 正本仕様の「20. Viewer / Evidence / Job 実装仕様」に従い、実行自体は成功完了と見る

---

## 7. IdleChat 連携実装

### 7.1 役割

Viewer は IdleChat の観測だけでなく、制御 UI も提供する。

### 7.2 利用 endpoint

- `/viewer/idlechat/status`
- `/viewer/idlechat/logs`
- `/viewer/idlechat/start`
- `/viewer/idlechat/forecast`
- `/viewer/idlechat/story`
- `/viewer/idlechat/stop`

### 7.3 UI で扱うモード

- `manual`
- `forecast`
- `story`

表示状態:

- `IdleChat: off/on/talking`
- `Forecast: ready/talking`
- `Story: ready/talking`

### 7.4 履歴表示

IdleChat パネルは以下を表示する。

- manual on/off
- chat active on/off
- current topic
- history row
- transcript 展開
- transcript copy

### 7.5 目標仕様との差分

現行は IdleChat UI が入力バー直下に統合されている。
目標仕様では、Viewer の運用 UI と IdleChat の番組制御 UI を分離できるようにする。

---

## 8. Audio Router SSE 実装

### 8.1 役割

Audio Router SSE は、TTS サーバから出た音声チャンク通知を外部音声ルータへ流すための補助導線である。

### 8.2 特徴

- Viewer EventHub を再利用する
- `tts.audio_chunk` のみを抽出する
- character ごとのルーティング材料を保つ

### 8.3 Viewer 本体との関係

この endpoint は Viewer ページ自体の画面描画には必須ではない。
役割としては Viewer アダプタ層に属するが、利用者はブラウザではなく外部ルータである。

### 8.4 目標仕様との差分

将来的には `audio_router` 専用の adapter へ分離し、Viewer 名前空間から切り離す余地がある。
v1 では運用導線を優先して同居させる。

---

## 9. 現行実装と目標仕様の差分整理

| 項目 | 現行実装 | 目標仕様 |
|---|---|---|
| UI 構成 | 単一 HTML に集約 | 論理分割可能な構造へ整理 |
| EventHub | ライブ観測専用だが汎用配信 | 用途別購読や責務分離を明文化 |
| Evidence 表示 | フロントに表示ロジック集中 | formatter / section 規約を整理 |
| IdleChat 制御 | Viewer に統合 | 運用 Viewer と番組制御 UI を分離可能に |
| Audio Router SSE | Viewer adapter に同居 | 専用 adapter へ分離可能に |

---

## 10. テスト観点

### 10.1 サーバ側

- `/viewer/events` が履歴 + 新着配信を行う
- `Last-Event-ID` が効く
- `/viewer/send` が不正 JSON と空 message を拒否する
- `/viewer/evidence/recent` の `limit` が検証される
- `/viewer/evidence/detail` の `job_id` 必須と `404` が機能する
- `/audio-router/events` が不正 chunk を落とす

### 10.2 フロント側

- Timeline フィルタが期待どおりに効く
- System タブで system event を分離できる
- evidence detail の選択切替が崩れない
- IdleChat の mode 切替と状態更新が一致する

### 10.3 受け入れ条件

- 実装者が Viewer の導線を本書だけで追える
- 運用者が EventHub と Evidence の役割差を誤解しない
- 追加実装時に「どこへ置くべき機能か」を判断できる
