# Viewer API・表示実装契約

- status: active
- lifecycle: canonical child
- owner: RenCrow_CORE
- parent_spec: `../02_実装仕様.md`
- source_spec: `../../refs/09_Viewer/Viewer仕様.md`、`../../refs/10_新仕様/05_Viewer仕様.md`、`../../refs/09_Viewer/Viewer添付入力仕様.md`
- last_reviewed: 2026-07-15
- scope: Viewerのroute、SSE、送信、表示projection、添付、検証境界

## 1. Viewerの責務

ViewerはCoreの操作面と観測面である。静的UIではなく、HTTP API、SSE、history、monitor projection、Memory / Source Registry、IdleChat、STT/TTS等の状態を表示・操作する。

次を同一の真実として扱わない。

- 表示本文
- SSE live event
- event log / audit log
- history
- MonitorStore projection
- audio / lipsync trigger
- runtime config / readiness
- debug情報

## 2. route所有

route登録のcomposition rootは`cmd/rencrow/routes.go`とし、領域別の`internal/features/*/registrar.go`へ委譲する。handlerだけ、JavaScript callerだけ、URLだけを単独で変更しない。

route変更時は最低限、次を同じ変更で確認する。

- HTTP method / path / request / response / status
- registrarとhandler
- Viewer JavaScript caller
- handler / route contract test
- `RenCrow_CMD` facade互換
- browser E2E

完全なroute一覧は変化が速いため本書へ複製せず、registrarとcontract testを現在値とする。

## 3. `/viewer/send`

`POST /viewer/send`はJSONまたはmultipartを受け、通常のmessage orchestrationへ渡す。

| 入力 | 契約 |
| --- | --- |
| `message` | text。attachmentがあれば空を許可 |
| `to` | Viewerで許可されたrecipientへ正規化 |
| `attachments` / `attachments[]` | application attachment storeで保存・検証 |
| model alias fields | 明示された場合だけroute prefixへ変換 |

無効method、JSON / multipart不正、空入力、不正recipient、attachment validation失敗は4xxとする。受理後の処理は非同期で、進捗と結果はevent経路へ返す。

## 4. 添付

- image、PDF、text相当、video等の対応種別はdomain attachment policyを正とする。
- filename、MIME、size、合計size、保存pathをvalidationする。
- Viewerは保存先filesystemを直接決めない。
- attachmentは通常message / routing経路へ渡し、別の非監査経路を作らない。
- attachmentだけの入力には、種別に応じた明示的なdefault messageを付ける。

## 5. SSE

`/viewer/events`は`text/event-stream`を返し、接続中のlive eventを配信する。

- historyは接続直後に配信する。
- `Last-Event-ID`がある場合、それ以前のsequenceを再送しない。
- heartbeat commentで接続を維持する。
- TTS audio chunk、session completed、IdleChat message / summary等のtransient eventはhistory replayしない。
- clientが遅い場合のdropをlogへ残し、server全体をblockしない。

## 6. MonitorStoreと永続証跡

MonitorStoreは現在状態を見やすくする表示projectionである。audit、evidence、job repository、memory store等の永続証跡を置き換えない。

表示上のsummaryと詳細証跡を分離し、初期画面へraw table、長文error、監査payloadを無制限に展開しない。

## 7. UI検証

Viewer変更はunit / handler testだけで完了にしない。

- 現在sourceからserverをbuildする。
- Playwright管理browserを使う。
- desktopとnarrow / mobileで確認する。
- 操作、network、console、最終stateを確認する。
- fixed input、toast、overlay、lipsync、live modeとの非干渉を確認する。
- 実行ごとにport、HOME、workspace、DB、log、browser contextを隔離する。

DOM存在やhealth 200だけをE2E合格にしない。

## 8. 参照の扱い

`docs/refs/09_Viewer/`と旧ログViewer仕様は背景・履歴・詳細候補であり、本書を上書きしない。視覚方針は`DESIGN.md`、テスト運用は`rules/common/rules_testing.md`、UI固有制約は`rules/rules_viewer_ui.md`を併用する。
