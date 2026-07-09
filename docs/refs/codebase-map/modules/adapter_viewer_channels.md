---
generated_at: "2026-07-01T13:19:25+09:00"
run_id: run_20260701_131925
phase: 2
step: "10"
profile: picoclaw_multiLLM
artifact: module
module_group_id: adapter_viewer_channels
---

## 概要

`adapter_viewer_channels`はHTTP/Viewer/外部channelからapplication層へ入る境界である。特に`internal/adapter/viewer`は`/viewer/*`の表示・操作・SSE・monitor stateを持つ巨大なadapter面で、`modules/*`はSTT/TTS/Chat/Worker/WebGatherなどの再利用bridgeを提供する。

## 関連ドキュメント

- [../アーキテクチャ総合.md](../アーキテクチャ総合.md)
- [../結合ポイントマップ.md](../結合ポイントマップ.md)
- [../ユースケース逆引き.md](../ユースケース逆引き.md)

## モジュール名: Adapter / Viewer / Channel層

### 役割と責務（Why）

- HTTP request/response、SSE、static asset、channel payloadをapplication/domainの入力へ変換する。
- Viewerは運用監視、会話、memory、evidence、job、repair、sandbox、revenue、persona、browsertraceなど多くのsurfaceを持つ。
- `modules/*`は本体runtimeと周辺moduleのHTTP/contract境界を薄く保つためのbridgeである。

### ナビゲーション

| ファイル/ディレクトリ | 役割 | 読むべき場面 |
|---|---|---|
| `internal/adapter/viewer/handler.go` | Viewer HTML entry | `/viewer`初期表示を見る時 |
| `internal/adapter/viewer/handler_send.go` | Viewerから会話送信 | chat inputがorchestratorへ渡る経路を見る時 |
| `internal/adapter/viewer/hub.go`, `handler_sse.go` | EventHub/SSE | Viewer live updateやmultitab挙動を見る時 |
| `internal/adapter/viewer/monitor*.go` | agent/job/log/evidence summary state | Ops/System/Jobs系タブを見る時 |
| `internal/adapter/viewer/memory*_handler.go` | memory layers/user/recall pack | memory Viewer面を見る時 |
| `internal/adapter/viewer/stt*_handler.go` | STT capture/admin/autotest | voice input調査を見る時 |
| `internal/adapter/channels/*`, `line`, `entry`, `chrome` | 外部channel/entry adapter | LINE/Slack/Chrome bridge入口を見る時 |
| `modules/stt`, `modules/tts`, `modules/voicechat` | 音声系module contract | STT/TTS/voicechatの境界を見る時 |

### モジュール間の関係

- **依存元**: `cmd/picoclaw/routes.go` -> `internal/adapter/viewer`。route登録のほぼ全てはcmd側にある。
- **依存先**: Viewer handler -> application service/store interface。HTTP入力をdomain/applicationの型へ変換する。
- **依存先**: Viewer EventHub -> orchestrator events。`EventHub.OnEvent`がlive updateの中心。
- **依存先**: `modules/stt` -> STT runtime / Viewer input observer。Viewer音声入力と会話経路をつなぐ。
- **依存元**: Browser/Viewer JS -> `/viewer/*` API。UI変更時はGo handlerだけでなくassets JS/CSSも影響する。

### 大関数の構造マップ（50行超の関数のみ）

| 関数名 | 行数 | 構造 | 行範囲の目安 |
|---|---:|---|---|
| `HandleSendWithAttachments()` | 80行級 | POST検証 -> attachment保存 -> message handler呼び出し -> JSON応答 | `internal/adapter/viewer/handler_send.go:140`以降 |
| `MonitorStore.reduceJobs()` | 130行級 | orchestrator event type別にjob snapshotを更新 | `internal/adapter/viewer/monitor_reducers.go:108`以降 |
| `HandleMemoryRecallPack()` | 50行超 | hot/cold/user memoryを集約しViewer応答へ整形 | `internal/adapter/viewer/memory_recall_pack_handler.go:14`以降 |
| `NormalizeTranscriptText()` | 50行超 | STT transcriptの空白・重複・記号などを正規化 | `modules/stt/session_rules.go:178`以降 |

### 落とし穴・注意点

- Viewer endpointは多くが`cmd/picoclaw/routes.go`で登録されるため、handler名検索だけではURLが分からない。
- `MonitorStore`はlive event reducerであり、永続storeそのものではない。表示stateとaudit/evidenceの真実を混同しない。
- STT/TTSはViewer、modules、infrastructure、外部RenCrow_STT/TTS repoの境界にまたがる。どのrepoが責務を持つか先に決める。
- Viewer assetはembedded/static contract testを持つものがある。UI/API名変更ではJS testも確認対象にする。

### 設計意図

- Viewerは「運用surface」と「会話surface」を同居させるが、HTTP handlerはstore/service interfaceを受ける形にしてapplication/infrastructureの詳細を隠す。
- `modules/*`はRenCrow分割moduleとの接続点であり、本体に密結合な互換層として残る。

### 初期化

- **module_init() 登録**: なし。route registrationで接続。
- **優先度**: `cmd/picoclaw`のDependencies構築後、route登録時に各handlerへstore/serviceを渡す。
- **注意点**: SSE/EventHubはruntime process内stateを持つため、再起動やmultitabでhistory/client countの見え方が変わる。

