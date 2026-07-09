# IdleChat 仕様書

**作成日**: 2026-03-15
**最終更新**: 2026-05-27
**対象バージョン**: v4 (distributed mode)
**ステータス**: 実装完了・運用中

---

## 関連仕様

- [会話ID仕様](./会話ID仕様.md): IdleChat / Chat / Viewer / TTS / STT で使う `session_id`、`message_id`、`turn_index`、`utterance_id` 等の横断仕様

## 1. 概要

IdleChat は、ユーザーが一定時間操作しないアイドル時間に **エージェント同士（Mio / Shiro 等）が自律的に雑談する** 仕組みである。通常モード（ランダムトピック）と未来展望モード（ドメイン特化）の2つのセッション形式を持つ。

### 1.1 目的

- アイドル時間を活用してエージェントの「人格」を表現する
- ユーザーに楽しめるコンテンツ（雑談・架空映画妄想・未来展望）を自動生成する
- Viewer / TTS 経由でリアルタイム表示・読み上げする

### 1.2 セッション形式

| 項目 | 通常モード | 未来展望モード |
|---|---|---|
| トピック選択 | 通常カテゴリ（Single / Double / External / Movie / News） | 6ドメイン固定順回し |
| 情報源 | ジャンル辞書 + Wikipedia + NHK RSS | トレンド + NHK + Google News（3段階） |
| ターン数 | 12ターン/トピック、最大50/セッション | **100ターン/ドメイン、最大600/セッション** |
| 起動方法 | 自動（アイドル検知）/ 手動 | **手動のみ**（「未来展望」ボタン） |
| セッション形式 | 単発トピック | 番組形式（ドメインアナウンス → お題 → 議論） |
| テーマ反復抑制 | ループ検出（5種類） | ループ検出 + 蓄積型テーマ抑制 |
| 要約 | Worker (Shiro) → Mio 読み上げ | Worker (Shiro) + 継続考察テーマ → Mio 読み上げ |
| Strategy 表示 | `single: ...`, `double: ...`, `external: ...`, `movie: ...`, `news: ...` | `forecast/AI技術`, `forecast/経済` 等 |
| 詳細仕様 | 本ドキュメント | `docs/未来展望セッション仕様.md` |

### 1.3 設計思想

- **本番タスク最優先**: ユーザーアクティビティで即中断
- **イベントドリブン TTS**: TTS 完了イベントで次のアクションに進む（推定ベースではない）
- **品質制御**: 4段階のリトライ + 5種類のループ検出で会話品質を維持
- **多様性確保**: Single / Double / External / Movie / Forecast / Story / News の 7 カテゴリでトピック枯渇を防止
- **話者ごとの LLM 分離**: `speakerLLMs` で Mio と Shiro に異なる LLM を割当可能

---

## 2. アーキテクチャ

### 2.1 コンポーネント構成

```
internal/application/idlechat/
├── orchestrator.go       # IdleChatOrchestrator 本体（ライフサイクル・発話生成・ループ検出・TTS連携）
├── orchestrator_test.go  # テスト
├── forecast_session.go   # 未来展望セッション（ドメイン定義・トレンド収集・テーマ抑制）
├── topic_generator.go    # トピック生成戦略・外部シード取得
└── topic_store.go        # TopicStore（セッション要約の永続化）
```

### 2.2 依存関係

| 依存先 | 用途 |
|---|---|
| `domain/llm.LLMProvider` | LLM 呼び出し（トピック生成・発話生成・要約） |
| `domain/session.CentralMemory` | 会話履歴の記録・参照 |
| `domain/transport.Message` | メッセージ型（`MessageTypeIdleChat`） |
| `adapter/viewer.EventHub` | Viewer SSE へのイベントブロードキャスト |

### 2.3 LLM 役割分担

| 処理 | 担当 | 理由 |
|---|---|---|
| 通常トピック生成 | Mio (gemma3:4b) | 軽量・高速 |
| 未来展望キーワード抽出 | ローカル Forecast provider（local Coder / Worker） | 外部 API クレジット消費を避ける |
| 未来展望トピック生成 | ローカル Forecast provider（local Coder / Worker） | 外部 API クレジット消費を避ける |
| ディスカッション発話 | 各話者の LLM | ペルソナ維持 |
| 既出テーマ抽出 | Worker (Shiro/qwen3.5:9b) | 要約タスク、ローカル無料 |
| まとめ生成 | Worker (Shiro/qwen3.5:9b) | 要約タスク、ローカル無料 |

Forecast で外部 Coder API を使う場合は `idle_chat.forecast_external_enabled: true` の明示設定が必要。明示設定がない場合、外部 Coder provider は選択しない。生成失敗時に別の外部 provider へ自動切替しない。

通常 IdleChat では、話者ごとの LLM リクエストに `think` を常に明示する。
既定は Mio が常時 `think: false`、Shiro は IdleChat のみ `think: false`、その他の話者/モデルは `think: true` とし、`idle_chat.speaker_llm_options` で話者ごとに切り替え可能にする。
Shiro の NoThink は通常 Worker には適用せず、IdleChat の Shiro 呼び出しに限定する。IdleChat 以外の Shiro/Worker は `think: true` で運用する。

### 2.4 データフロー

```
IdleChatOrchestrator
  │
  │ emitTimelineEvent(TimelineEvent) → <-chan struct{} (TTS完了チャネル)
  │     ↓
  │ SetEventEmitter (main.go)
  │     ↓  TimelineEvent → OrchestratorEvent 変換 + TTS非同期発火
  │
  EventHub.OnEvent()
  │     ↓  JSON → SSE broadcast
  │
  viewer.html (Timeline + IdleChat パネル)
```

---

## 3. ブレイク体系

全モード共通。TTS イベントドリブン。

| タイミング | 待ち時間 | 起点 |
|---|---|---|
| 同一話者内の句間 | 200ms | TTS チャンクの `pause_after`（デフォルト） |
| 話者交代（Mio↔Shiro） | 500ms | TTS 完了イベント後 (`speakerBreak`) |
| トピック/ドメイン交代 | 1000ms | TTS 完了イベント後 (`topicBreak`) |

**TTS 完了待ちの仕組み**:
- `emitTimelineEvent()` が `<-chan struct{}` を返す
- TTS Server の `OnSessionCompleted` → `notifyIdleChatTTSCompleted` → チャネル close
- `waitForTTSDone(ch)` で待機。TTS 未設定なら `nil` が返り即座にスキップ

---

## 4. 通常モード

### 4.1 ライフサイクル

```
Start()
  └─ goroutine: monitorLoop() — 30秒ごとに checkAndStartChat()
       ├─ chatBusy/workerBusy → スキップ
       ├─ nextTopicAt 前 → スキップ
       ├─ アイドル時間 < intervalMin（manualMode でなければ）→ スキップ
       └─ runChatSession()
```

### 4.2 お題カテゴリ

IdleChat のお題は、ユーザー観測・ログ・E2E 評価では次の 7 カテゴリで扱う。
内部実装名や生成関数が異なっても、Viewer 表示、履歴、ログ、テストではこのカテゴリ単位で追跡できること。

| カテゴリ | Strategy | 内容 | 選択単位 |
|---|---|---|---|
| Single | `single` | 260 個のジャンルプールから 1 個を選び、そのジャンルを深掘りする | 通常 IdleChat |
| Double | `double` | 2 ジャンルの意外な掛け合わせを作り、その組み合わせを深掘りする | 通常 IdleChat |
| External | `external` | Wikipedia Random 等の外部刺激 1 件とジャンルを組み合わせる | 通常 IdleChat |
| Movie | `movie` | `「〜ってどんな映画？」` 形式で架空映画の内容を深掘りする | 通常 IdleChat |
| News | `news` | ニュース見出し 1 件を選び、そのニュース自体を深掘りする | 通常 IdleChat |
| Forecast | `forecast` | 6 ドメイン固定順の未来展望セッション | 未来展望モード |
| Story | `story` / `story-simple` | 昔話・童話を改変して朗読する物語セッション | Story モード |

通常 IdleChat の自動ローテーションおよび通常評価では、`single → double → external → movie → news` の順で最低 1 巡できること。
`forecast` と `story` はモード別カテゴリとして扱い、手動起動または専用 E2E で個別に検証できること。

#### お題サンプル正本

以下を、お題生成のユーザー評価用サンプル正本とする。
実装はこの文面を固定出力してはいけないが、各カテゴリの題名品質、粒度、混ぜ方、禁止すべきメタ表現の基準として扱う。

| 順序 | カテゴリ | 基準お題 |
|---|---|---|
| 1 | Single | 古書店の店主が見つけた、手紙に残る記憶の扱い方 |
| 2 | Double | 潮汐と郵便制度に共通する、遅れて届くものの設計 |
| 3 | External | 地下鉄博物館に残る音声案内と織物の記録性 |
| 4 | Movie | 「雨上がりの映写室」ってどんな映画？ |
| 5 | News | 新しい医療制度の検討が、現場の判断に与える影響 |
| 6 | Forecast | AI 技術が、個人の記憶整理をどう変えるか |
| 7 | Story | 桃太郎を、鬼側の記録係から語り直す物語 |

基準:

- Single は 1 ジャンルに人物・物・場所・場面の具体アンカーを入れる。
- Double は 2 領域の共通構造が見える題名にする。
- External は provider 名、取得経路、記事・ページ・検索結果などのメタ語を出さず、素材とジャンルを自然に接続する。
- Movie は必ず `「〜」ってどんな映画？` の形にする。
- News はニュースの論点・背景・影響を扱い、ランダムジャンルや外部素材と混ぜない。
- News seed は `title / category / source / url` を保持できる。カテゴリ例は `general / culture / business / world / sports / tech` とし、取得元追加時も News カテゴリ内の追跡メタデータとして扱う。
- Forecast は将来変化の問いとして、対象領域と変化先が分かる題名にする。
- Story は元話、視点変更、語り直しの軸が分かる題名にする。

- **News**: NHK RSS 等のニュースシードから 1 件を選び、そのニュースの論点、背景、影響を深掘りする。ランダムジャンルを混ぜない。
- **External**: 外部刺激とジャンルの組み合わせを扱うカテゴリであり、純粋なニュース深掘りではない。生成用 prompt では外部刺激を `素材` として渡し、`Wikipedia`、`外部刺激`、`ランダム記事`、`偶然の記事` のような取得経路や provider 名をお題本文へ出させない。
- **Movie**: 独立カテゴリであり、Single / Double / External の隠しフラグとして扱わない。
- **カテゴリすり替え禁止**: News を External へ黙ってすり替えない。ニュースシード取得失敗時は、`news_seed_unavailable` 等の診断をログに残し、カテゴリ成功として扱わない。
- **外部シード**: 起動時に1日1回取得（Wikipedia 10件、NHK 10件）
- **重複排除**: 直近12トピックと類似度チェック、最大3回リトライ

### 4.3 セッション実行

```
runChatSession():
  1. generateTopicFromChat() → トピック生成
  2. ターンループ（最大 maxTurnsPerTopic=12）
     ├─ generateResponse() → 発話生成
     ├─ ensureTrailingPeriod() → 末尾に「。」追記
     ├─ emit → waitForTTSDone → waitBreak(speakerBreak)
     ├─ ループ検出（detectLoopReason）
     └─ 中断/エラー/ループ → break
  3. saveSummary() → Worker 要約 → Mio 読み上げ → topicBreak
  4. 次の話題は同一 session_id 内で開始せず、次回の runChatSession() で新 session として開始
```

通常 IdleChat の境界は **1 session = 1 topic = 1 summary** とする。
話題を切り替える場合は、話題Aの session を完了し、summary と topicBreak を終えてから、話題Bを新しい IdleChat session として開始する。
同一 session_id 内で `topic-00` から `topic-01` へ進める実装は禁止する。

### 4.4 ループ検出（5種類）

| 種別 | 条件 |
|---|---|
| `exact_repeat` | 直近4発話内に完全一致 |
| `alternating_repeat` | A-B-A-B パターン（類似度 ≥ 0.9） |
| `template_repeat` | 話者テンプレートの繰り返し |
| `high_similarity` | 直近10発話の類似度が高い |
| `what_if_repeat` | 「もし〜だったら/なら」が半数以上 |

### 4.5 発話生成リトライ（4段階）

| 段階 | 条件 | リトライ内容 |
|---|---|---|
| 1. 無効応答 | `invalidIdleResponse` | 「自然な会話文で言い直して」 |
| 2. スタイル問題 | `needsIdleStyleRetry` | 「別の手で自然に返して」 |
| 3. プロンプト漏出 | `hasPromptLeak` | 「指示文の断片を消して」 |
| 4. 発言帰属違反 | `violatesAttribution` | 「相手の案を受ける形に」 |

---

## 5. 要約と読み上げ

全モード共通。トピック/ドメインの議論終了後:

```
1. saveSummary / saveForecastSummary → Worker (Shiro) が要約生成
2. TopicStore に永続化（JSON Lines）
3. Timeline に idlechat.summary イベント emit
4. speakSummary() → Mio が要約を読み上げ（TTS完了待ち）
5. topicBreak (1000ms) → 次の IdleChat session / ドメインへ
```

### 5.1 SessionSummary

```go
type SessionSummary struct {
    SessionID       string        // "idle-{unix}" or "forecast-{unix}"
    Title           string        // "3月15日の{topic}の話題まとめ"
    Topic           string        // トピック文字列
    Strategy        TopicStrategy // "single: ...", "forecast/AI技術" 等
    Summary         string        // Worker による要約
    StartedAt       string        // RFC3339
    EndedAt         string        // RFC3339
    Turns           int
    LoopRestarted   bool
    LoopReason      string
    TopicProvider   string        // "mio" or "forecast"
    SummaryProvider string        // "shiro" or "coder2"
    Transcript      []string      // "{speaker}: {content}"
}
```

---

## 6. Viewer 連携

### 6.1 REST API

| エンドポイント | メソッド | 用途 |
|---|---|---|
| `/viewer/idlechat/start` | POST | 通常モード手動開始 |
| `/viewer/idlechat/forecast` | POST | 未来展望モード開始 |
| `/viewer/idlechat/stop` | POST | 停止（両モード共通） |
| `/viewer/idlechat/status` | GET | 状態取得 |
| `/viewer/idlechat/logs` | GET | 履歴取得（両モード統合） |

### 6.1.1 複数 Viewer と音声入出力の単一 active 制御

Viewer は PC / ケータイ / 複数タブで同時に開ける。一方で、スピーカ（TTS 再生）とマイク（STT 入力）の操作対象は常時 1 つのブラウザだけに限定する。

- 各 Viewer は `viewer_client_id` を保持する。
- スピーカは後出し優先で、最後に音声を有効化した Viewer が `active_audio_viewer_id` になる。
- マイクも後出し優先で、最後に STT 開始操作をした Viewer が `active_input_viewer_id` になる。
- Viewer 表示・ログ閲覧は複数同時接続を許可する。
- IdleChat の TTS 完了待ちは `active_audio_viewer_id` と一致する TTS playback ack のみを採用する。
- active ではない Viewer の ack / STT 結果は、ログ上は観測できても会話進行や IdleChat 制御には反映しない。
- active が切り替わった場合、旧 Viewer は音声再生またはマイク入力を停止する。
- active audio viewer が未設定の場合、TTS は再生完了扱いにしない。
- TTS playback ack が返らないことは音声系エラーとしてログに残す。ただし IdleChat / Forecast の会話進行やお題更新を止める要因にはしない。
- TTS 完了待ちは短時間の best-effort とし、timeout 後は `tts_error=true` として記録して会話を継続する。

### 6.1.2 IdleChat 本文表示と TTS 同期

IdleChat の Viewer 本文表示は、`idlechat.message` 到着時点の全文即時描画ではなく、TTS chunk 同期を正とする。
`idlechat.message` は session / speaker / raw content / pending 発話の保持に使い、本文表示の進行根拠は原則 `tts.audio_chunk.display_text` とする。

- `idlechat.message` 受信時は pending 発話枠だけを作り、本文全文は表示しない。
- Mio / Shiro の本文は `tts.audio_chunk` 到着後、chunk 単位で表示する。
- スピーカ ON の場合、active audio Viewer で TTS 再生開始または再生確定した chunk に合わせて表示する。
- スピーカ OFF の場合、TTS chunk ごとに 500ms wait して次 chunk を表示する。
- `tts.session_completed` は TTS 生成完了であり、再生完了・表示完了・ユーザーが聞いた完了ではない。
- IdleChat の待ち解除は、active audio Viewer が観測した response の playback ack、またはスピーカ OFF の明示 fallback 完了に限定する。
- TTS playback ack が返らない場合はエラーとして記録するが、会話進行の停止要因にはしない。
- TTS provider への push が失敗した場合、失敗はログに残すが、IdleChat の進行制御では通常の TTS 完了と同じ扱いで pending を消化して会話を継続する。
- TTS chunk 未着や TTS 失敗時のみ、fallback として `idlechat.message` の全文表示を許可する。
- fallback は通常成功扱いにせず、fallback 表示としてログ・状態で区別する。
- `tts.session_completed` だけで、chunk を観測していない response を playback 済みとして ack してはいけない。

### 6.2 Viewer UI

**入力バーのコントロール:**
- 「IdleChat開始」「IdleChat停止」ボタン — 通常モード
- 「未来展望」ボタン（青系、独立配置） — 未来展望モード
- 状態表示: `IdleChat: off` / `on` / `on (talking)`

**IdleChat パネル（タブ切替）:**
- Mode カード: Manual / Chat Active バッジ
- Current Topic: 進行中トピック
- 履歴テーブル: Title, Strategy, Topic, Turns, Loop, Started, Ended, Summary
  - forecast 行は左ボーダー青 + Strategy 列青色で視覚区別

**Timeline:**
- `idlechat.message` / `idlechat.summary` イベントがリアルタイム表示
- ルート色: `IDLECHAT` = 紫

### 6.3 双方向制御

```
IdleChat → Viewer: 発話/要約イベント → EventHub → SSE → ブラウザ表示
Viewer → IdleChat: ユーザーメッセージ → message.received → NotifyActivity() → 中断
```

`shouldStopIdleChatByEvent()`: route が `IDLECHAT` や TTS イベントは無視、`message.received` のみ中断トリガー。

---

## 7. 並行安全性

| 機構 | 用途 |
|---|---|
| `sync.Mutex` (`o.mu`) | 全フィールドの排他制御 |
| `context.Context` (`o.ctx`) | goroutine キャンセル伝播 |
| `sync.WaitGroup` (`o.wg`) | Stop() での終了待機 |
| `sync.RWMutex` (`cacheMu`) | DailySeedCache のスレッドセーフアクセス |
| `sync.RWMutex` (`trendMu`) | TrendCache のスレッドセーフアクセス |

---

## 8. 定数

| 定数 | 値 | 用途 |
|---|---|---|
| `idleCheckInterval` | 30s | monitorLoop チェック間隔 |
| `maxTurnsPerTopic` | 12 | 通常モードの1トピック最大ターン数 |
| `speakerBreak` | 500ms | 話者交代ブレイク |
| `topicBreak` | 1000ms | 次 IdleChat session / ドメイン交代ブレイク |
| `defaultChunkPause` | 200ms | TTS チャンク間ブレイク（audio_sink） |
| `forecastTurnsPerDomain` | 100 | 未来展望の1ドメイン最大ターン数 |
| `forecastCheckpointInterval` | 15 | 未来展望のテーマ抑制チェック間隔 |

---

## 9. ファイル一覧

| ファイル | 責務 |
|---|---|
| `internal/application/idlechat/orchestrator.go` | IdleChatOrchestrator 本体、発話生成、ループ検出、TTS連携、sessionContext |
| `internal/application/idlechat/forecast_session.go` | 未来展望セッション、トレンド収集、テーマ抑制、ドメイン定義 |
| `internal/application/idlechat/topic_generator.go` | 通常モードのトピック戦略、外部シード、RSS パーサ |
| `internal/application/idlechat/topic_store.go` | TopicStore（JSON Lines 永続化） |
| `internal/infrastructure/tts/audio_sink.go` | TTS チャンク再生 + 句間ブレイク（`pause_after` / `defaultChunkPause`） |
| `internal/adapter/viewer/viewer.html` | Viewer フロントエンド |
| `internal/adapter/viewer/hub.go` | EventHub（SSE ブロードキャスト） |
| `cmd/rencrow/main.go` | 初期化・API 登録・イベントブリッジ・TTS 連携 |
| `cmd/rencrow/idlechat_tts.go` | IdleChat TTS 非同期発火・完了通知 |
