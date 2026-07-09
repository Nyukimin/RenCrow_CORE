# Viewer 仕様

## 1. 目的

Viewer は、RenCrow の実行状態、会話、TTS再生、IdleChat、ジョブ進捗、実行証跡をブラウザで確認・操作するための統合UIである。

Viewer は単なるログ表示ではなく、以下を同時に扱う。

- Chat / Worker / Coder / IdleChat のイベント表示
- TTS音声再生と発話表示
- Live配信用表示
- ジョブ進捗と実行証跡
- IdleChat 操作
- STT入力補助
- Viewerからのメッセージ送信

対象実装:

```text
internal/adapter/viewer/viewer.html
internal/adapter/viewer/handler.go
internal/adapter/viewer/hub.go
internal/adapter/viewer/evidence_handler.go
internal/adapter/viewer/audio_router_sse.go
cmd/picoclaw/main.go
```

## 2. 基本URL

通常Viewer:

```text
http://<RenCrowホスト>:18790/viewer
```

Live Mode:

```text
http://<RenCrowホスト>:18790/viewer?mode=live
```

Live Mode は配信用の表示モードであり、入力欄や一部操作UIを隠し、表示密度と視認性を優先する。

Live2D Mode:

```text
http://<RenCrowホスト>:18790/viewer?mode=live2d
```

Live2D Mode はキャラクター全画面表示モードであり、Viewer UI を隠して疑似Live2Dステージのみを表示する（詳細は「10.1 Live2D Mode」）。

## 3. 全体構成

```text
Orchestrator / IdleChat / TTS / STT
  -> EventHub.OnEvent()
  -> /viewer/events (SSE)
  -> viewer.html
  -> Timeline / Live Mode / TTS再生 / 各パネル
```

主要コンポーネント:

- `viewer.html`
  - 単一ページUI
- `EventHub`
  - SSE用イベント蓄積・配信
- `/viewer/events`
  - Viewer本体向けSSE
- `/audio-router/events`
  - audio-router向け `tts.audio_chunk` 抽出SSE
- `/viewer/send`
  - Viewer入力欄からChatへメッセージ送信

## 4. タブ構成

Viewer は以下のタブを持つ。

| タブ | 役割 |
|---|---|
| `Ops` | 運用者向けの現在状態要約 |
| `Overview` | Agent状態の俯瞰 |
| `Progress` | Agent / Job の進捗 |
| `Timeline` | 会話・イベントの時系列表示 |
| `System` | routing / entry / TTS / STT 等の技術イベント確認 |
| `IdleChat` | IdleChat 状態・履歴・操作 |
| `Sessions` | session 単位の集約 |
| `Jobs` | job と execution evidence の参照 |

## 5. EventHub / SSE

Viewer は `/viewer/events` を EventSource で購読する。

SSEイベントは `orchestrator.OrchestratorEvent` をJSON化したものである。

基本方針:

- 初回接続時に直近履歴を送る
- 以後はリアルタイムにイベントを追記する
- `Last-Event-ID` により再接続時の既読分をスキップする
- EventHub は短期ライブ観測用であり、完全な監査ログ保存は目的としない

現行の目安:

```text
履歴件数: 200
subscriber channel容量: 64
```

遅いクライアントにはイベントがdropされうる。

## 6. Timeline 表示

Timeline は会話系・進行系イベントを時系列で表示する。

代表イベント:

- `message.received`
- `routing.decision`
- `agent.note`
- `agent.thinking`
- `agent.response`
- `idlechat.message`
- `idlechat.summary`
- `tts.audio_chunk`

Timeline は履歴・観測用途の表示であり、TTSの現在発話表示とは役割を分ける。

TTS対象テキストの全文をTimelineに残す場合でも、音声再生に同期する発話表示は `tts.audio_chunk` 単位で行う。

## 7. TTS表示・再生契約

Viewer は `tts.audio_chunk` を受け取り、音声再生キューへ積む。

`tts.audio_chunk` は、文字列chunkと音声chunkを結びつける同期単位である。

必須payload:

```json
{
  "session_id": "tts-session",
  "utterance_id": "tts-session:0000",
  "chunk_index": 0,
  "character_id": "mio",
  "text": "今日はいい天気ですね。",
  "audio_path": "viewer-tts-abc.wav",
  "audio_url": "",
  "track": "default"
}
```

Viewer の再生順序:

```text
tts.audio_chunk受信
  -> 再生キューへ追加
  -> (session_id, track, chunk_index) を優先して昇順再生
  -> 音声再生開始時に同payloadの text を現在発話として表示
  -> 音声終了時に次chunkへ進む
```

## 8. Chunk単位表示

Viewer は、TTS対象の長文を現在発話として一括表示してはならない。

発話表示は、音声再生中のchunk単位で行う。

禁止:

- TTS対象の長文全文を音声再生前に現在発話として一括表示する
- chunk 0 の音声再生中に chunk 1 以降の文字列を現在発話として表示する
- 音声chunkと異なる `text` を現在発話として表示する

必須:

- 音声再生開始時に、その音声chunkと同じ `tts.audio_chunk` payload の `text` を表示する
- 次の音声chunkへ進む時、表示文字列も次chunkの `text` へ切り替える
- 音声再生が停止・失敗した場合、現在発話表示も停止状態へ戻す、または次chunkへ進む

補足:

- `utterance_id` は追跡・デバッグ用の推奨IDである。
- 同期保証は、同じ `tts.audio_chunk` payload 内の `text` と `audio_path` / `audio_url` を同時に扱うことで成立する。

## 9. Now Playing

通常Viewerでは、`Now Playing` などの現在発話表示を使って、再生中chunkの `text` を表示してよい。

`Now Playing` はTimelineとは別の表示であり、音声再生と同期する一時表示である。

通常Viewer:

- 再生開始時に `character_id + text` を表示する
- 再生終了時にクリアする、または次chunkへ切り替える

Live Mode:

- 配信用レイアウトを優先し、`Now Playing` は非表示にしてよい
- ただし、発話表示を行う場合は必ずchunk単位にする
- 長文全文を中央Chatに一括表示し、音声だけ後追いする表示は禁止する

## 10. Live Mode

Live Mode は配信用のViewer表示である。

URL:

```text
/viewer?mode=live
```

Live Mode の方針:

- 入力欄、操作ボタン、filters、toastなどを隠す
- Topicバーを表示する
- 中央Chat表示とMio/Shiroアイコンが重ならないようにする
- `Now Playing` は表示しない
- TTS再生順序とchunk同期は通常Viewerと同じにする

アイコン配置:

- Mio は左
- Shiro は右
- Topicバーに重ねない
- 中央Chatに重ねない
- 安全な余白が確保できない画面幅では、重ねるより非表示を優先する

## 10.1 Live2D Mode

Live2D Mode はキャラクターの疑似Live2Dステージを全画面表示する。

URL:

```text
/viewer?mode=live2d
/viewer?mode=live2d&character=marin
/viewer?mode=live2d&expression=smile_niconico
/viewer?mode=live2d&ui=0
```

クエリパラメータ:

| パラメータ | 既定値 | 用途 |
|---|---|---|
| `character` | `marin` | 表示キャラクター（`assets/live2d/<character>/` を参照） |
| `expression` | `normal_genki` | 初期表情 |
| `ui` | 表示 | `0` で操作パネル（表情・自動モーション切替）を隠す |

実装:

- `viewer.js` の `initLive2DMode()` が body に `live2d-mode` クラスを付与し、`#live2dStage` の iframe に `/viewer/assets/live2d/<character>/index.html` をロードする
- キャラクター一式は `internal/adapter/viewer/assets/live2d/<character>/` に配置する（`index.html` + `model.json` + `images/`）

Marin 疑似リグの方針:

- `.moc3` 未収録のため、Cubism ランタイムではなく「ベース + パーツ重ね合わせ」で表示する
- ベース画像（`base.png`）は目・口を肌埋め、アホ毛を背景埋めした静止画で、**背景を含むベースは一切動かさない**
- 動くのはパーツのみ: 目（開き/閉じ/ニコニコ）、口（開き/閉じ、口パク）、アホ毛（振り子）、サイド髪・リボン（clip-path 切り出しの微回転）
- まばたき・口パク・髪揺れは自動モーションとして動作し、パネルで個別にON/OFFできる
- 表情8種は `model.json` の `rig_state`（目・口パーツの組合せ）にマッピングされる
- パーツ画像は `tools/marin_parts_gen/gen_marin_patches.py` で `fullbody.png` から生成する（生成物の直接編集禁止）
- `.moc3` 完成後は `model.json` の `model3` フィールドで Cubism ランタイム読み込みへ差し替える

## 10.2 Games Observer

Viewer は RenCrow_GAMES の observer UI を PicoClaw の same-origin route から提供する。

REST API:

| エンドポイント | メソッド | 用途 |
|---|---|---|
| `/viewer/games/observer` | `GET` | RenCrow_GAMES observer UI |
| `/viewer/games/observer-api/games/status` | `GET` | local observer status proxy |
| `/viewer/games/observer-api/games/sessions` | `GET` | local observer session list proxy |
| `/viewer/games/observer-api/games/sessions/{session_id}` | `GET` | local observer session summary proxy |
| `/viewer/games/observer-api/games/sessions/{session_id}/frames` | `GET` | local observer frames proxy |
| `/viewer/games/observer-api/games/sessions/{session_id}/replay` | `GET` | local observer replay proxy |
| `/viewer/games/observer-api/games/sessions/{session_id}/retry` | `POST` | SurvivalGarden retry session action proxy |
| `/viewer/games/observer-api/games/sessions/{session_id}/start_over` | `POST` | SurvivalGarden start-over session action proxy |

Rules:

- `/viewer/games/observer-api/...` は PicoClaw の title logic ではなく、`127.0.0.1:18791` の RenCrow_GAMES local observer server への same-origin proxy として扱う。
- proxy は observer contract endpoint の HTTP method を保存して upstream へ転送する。session list / frames は `GET`、`retry` / `start_over` は `POST` である。
- `retry` / `start_over` の受け入れ確認では、`18791` 直の成功だけで完了扱いにしない。必ず Viewer origin の `18790` 経由でも `POST /viewer/games/observer-api/games/sessions/{session_id}/retry` が `200 OK` になることを確認する。
- GAMES 側の game-local candidate learning は RenCrow の confirmed memory ではない。PicoClaw proxy は memory promotion や title world state reconstruction を行わない。

## 11. IdleChat 連携

Viewer は IdleChat の状態表示と手動操作を提供する。

REST API:

| エンドポイント | メソッド | 用途 |
|---|---|---|
| `/viewer/idlechat/start` | `POST` | 通常IdleChat開始 |
| `/viewer/idlechat/forecast` | `POST` | 未来展望開始 |
| `/viewer/idlechat/story` | `POST` | Story開始 |
| `/viewer/idlechat/story-simple` | `POST` | Simple Story開始 |
| `/viewer/idlechat/stop` | `POST` | 停止 |
| `/viewer/idlechat/status` | `GET` | 状態取得 |
| `/viewer/idlechat/logs` | `GET` | 履歴取得 |

IdleChatイベント:

- `idlechat.message`
- `idlechat.viewer`
- `idlechat.tts`
- `idlechat.summary`

`idlechat.viewer` はViewer表示用、`idlechat.tts` はTTS用として扱う。  
TTS読み上げの現在発話表示は、最終的に `tts.audio_chunk` のchunk単位表示へ揃える。

## 12. STT UI

Viewer はSTT補助UIを持つ。
通常の音声入力は `voice_chat` surface として扱い、STT final は Viewer のテキスト入力と同じ Chat surface へ送る。
つまり、入口は音声でも target agent / provider alias の規則は `viewer_chat` と同じである。

主な機能:

- 音声入力ボタン
- STT接続状態表示
- STTログ送信
- 入力WAV保存
- STT自動テスト起動

通常会話の経路:

```text
Viewer mic
  -> /stt
  -> STT final
  -> /viewer/send
  -> target agent conversation
```

Voice Direct / input_audio 経路:

```text
Viewer mic
  -> /voice-chat
  -> ProcessVoiceDirect
  -> Chat SSE event
```

`/stt-ws` と `/voice-chat-ws` は互換 alias であり、Viewer の正本経路は `/stt` と `/voice-chat` である。

### 12.1 Ops テスト録音（ゴールデンサンプル作成）

入口: **Ops → Runtime / LLM / Audio → STT テスト録音**

| 操作 | 動作 |
|------|------|
| 録音開始 | TTS / IdleChat 中断、Chat 🎤 無効、マイク連続キャプチャ |
| 録音停止・保存 | raw 保存 → 両端トリム → trim 保存 → HTTP STT autotest |

読み上げ文:

| 版 | ファイル | 想定時間 |
|----|---------|---------|
| ゴールデン 25 s | `tmp/viewer_test_recording_script_golden_25s.md` | 約 25 s |
| 長尺 35 s | `tmp/viewer_test_recording_script.md` | 約 35 s |

固定データセット・E2E コマンド: **`docs/STT_TTS/STT_ゴールデンテストデータセット仕様.md`**

関連API:

| エンドポイント | メソッド | 用途 |
|---|---|---|
| `/stt` | `WS` | RenCrow STT bridge |
| `/viewer/stt/log` | `POST` | クライアント側STTログ保存 |
| `/viewer/stt/wav/raw` | `POST` | トリム前 WAV 保存 |
| `/viewer/stt/wav` | `POST` | トリム後 WAV 保存 |
| `/viewer/stt/autotest` | `POST` | STT自動テスト |

## 13. Viewer送信

Viewer は `/viewer/send` でユーザーメッセージを送信できる。

Request:

```json
{
  "message": "こんにちは"
}
```

仕様:

- `POST` のみ
- `message` 必須
- HTTP応答は即時 `{"ok": true}`
- 実処理結果はSSEイベントとしてViewerへ返る
- Rolesタブで選択中の送信先がある場合、Viewerは送信前に明示ルーティングコマンドを付与する
- `mio` / Chat は本文をそのまま送信する
- `shiro` / Worker は `/ops` を付与する
- `ao` / Coder1 は `/code1` を付与する
- `aka` / `kin` / `gin` はそれぞれ `/code2` / `/code3` / `/code4` を付与する
- ユーザー入力がすでに `/ops` / `/wild` / `/code*` で始まる場合は、入力された明示指定を優先する

### 13.1 ターミナル会話CLI

`picoclaw chat` は、起動中の RenCrow server に対してターミナルから会話するための薄いCLIである。

内部契約:

- 送信は `/viewer/send` を使う
- 応答表示は `/viewer/events` のSSEを使う
- CLI専用の新しい会話APIは追加しない
- デフォルト接続先は `http://127.0.0.1:18790`
- 外部端末から使う場合は `--url` または `RENCROW_CHAT_URL` で RenCrow server の到達可能URLを指定する

使い方:

```bash
picoclaw chat
picoclaw chat --message "こんにちは"
picoclaw chat --url http://127.0.0.1:18790 --message "/ops status"
RENCROW_CHAT_URL=https://<ubuntu-tailnet-host> picoclaw chat
```

外部アクセス:

- LAN内端末では `http://<RenCrowホスト>:18790` を指定できる
- 宅外・別ネットワークでは Tailscale HTTPS / Serve のURLを指定する
- public internet へ `18790` を直接公開しない

## 14. Evidence / Jobs

Viewer は execution evidence と job 状態を参照する。

代表API:

| エンドポイント | メソッド | 用途 |
|---|---|---|
| `/viewer/evidence/recent` | `GET` | 直近evidence一覧 |
| `/viewer/evidence/detail` | `GET` | job単位evidence詳細 |
| `/viewer/evidence/summary` | `GET` | evidence集計 |
| `/viewer/jobs` | `GET` | job一覧 |
| `/viewer/job/detail` | `GET` | job詳細 |

完了判定、`mio_reported`、live job と evidence の優先順位は `docs/01_正本仕様/実装仕様.md` の「20. Viewer / Evidence / Job 実装仕様」を正本とする。

## 15. Memory昇格操作

Viewer Memoryタブは、L1 memoryを `candidate` / `confirmed` に更新し、`user:` / `char:` / `kb:` namespaceへ明示昇格できる。

昇格操作:

- UIは `target_kind` と `target_id` を送る
- APIは `target_kind` + `target_id` から `user:<id>` / `char:<id>` / `kb:<id>` を組み立てる
- 組み立てたnamespaceは `ValidateL1Namespace` を通過した場合のみpromoterへ渡す
- 互換用に `target_namespace` を直接渡す形式も受け付けるが、同じvalidatorを通す

staging validator連携:

- `memory_candidate` はvalidation通過後に自動昇格できる
- 自動昇格は `L1StagingValidationPolicy.AutoPromoteMemoryCandidate` が有効な場合のみ行う
- targetは `meta.target_namespace` を優先し、無い場合はstaging item自身の `user:` / `char:` / `kb:` namespaceを使う
- `conv:` namespaceは自動昇格targetにしない

staging archive:

- L1 staging itemはDuckDB `l1_staging_item_archive` へ保存できる
- `ExportL1ArchivesParquet()` は `l1_staging_item.parquet` を出力する
- raw_text / summary_draft / validation_status / keywords / metaをParquetへ保持する

Source Registry CLI:

- `picoclaw source-registry save --source-id <id> --url <url> --kind <kind> --license-note <text>` で登録できる
- `--trust-score`, `--interval-sec`, `--namespace`, `--disabled`, `--json` を指定できる
- `picoclaw source-registry list --json` で登録済みsourceを確認できる
- `picoclaw source-registry disable <source_id>` で登録済みsourceを無効化できる
- `picoclaw source-registry sweep --limit <n> --min-trust <score> --json` でdue sourceのfetch / staging / validate / News昇格を手動実行できる
- CLIは `conversation.l1_sqlite_path` のL1 SQLite storeを使う

Recall trace:

- 応答単位の `recall.trace` はL0 short_contextも保存する
- L1 Search Cache、L2 thread summary、L3 long_fact / knowledge と同じtrace tableで確認できる

## 16. 参照

- `docs/03_設計文書/ログViewer仕様.md`
- `docs/03_設計文書/実装仕様_ログViewer_v1.md`
- `docs/01_正本仕様/10_ログ.md`
- `docs/07_IdleChat仕様/IdleChat仕様.md`
- `docs/STT_TTS/RenCrow_TTS_仕様.md`
- `docs/STT_TTS/AUDIO_Client仕様/TTS/実装仕様.md`
