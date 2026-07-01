# IdleChat（§8）

**対応仕様**: 仕様.md §8
**ソース**: 元 `docs.zip` 内 `09_IdleChat仕様/IdleChat仕様.md`, `09_IdleChat仕様/未来展望セッション仕様.md`, `09_IdleChat仕様/会話ID仕様.md`, `15_TTS_Viewer同期.md`

上記の詳細資料はこの最小セットには含めない。IdleChat の該当カテゴリ、停止、ID、品質制御を実装・修正するときだけ、`99_整理/除外ファイル一覧.csv` の `restore_when` に従って一時復帰する。
**最終更新**: 2026-05-27

---

## 1. 概要

IdleChat は、ユーザーが一定時間操作しないアイドル時間に**エージェント同士（Mio/Shiro 等）が自律的に雑談する**仕組み。

### 1.1 目的

- アイドル時間を活用してエージェントの「人格」を表現する
- ユーザーに楽しめるコンテンツ（雑談・架空映画妄想・未来展望）を自動生成する
- Viewer / TTS 経由でリアルタイム表示・読み上げする

### 1.2 設計思想

- **本番タスク最優先**: ユーザーアクティビティで即中断
- **イベントドリブン TTS**: TTS 完了イベントで次アクションへ進む（推定ベースではない）
- **品質制御**: 4段階のリトライ + 5種類のループ検出で会話品質を維持
- **多様性確保**: Single / Double / External / Movie / News / Forecast / Story-Simple の 7 カテゴリでトピック枯渇を防止
- **話者ごとの LLM 分離**: `speakerLLMs` で Mio と Shiro に異なる LLM を割当可能

---

## 2. セッション形式

| 項目 | 通常モード | 未来展望モード |
|------|----------|--------------|
| トピック選択 | 通常カテゴリ（Single / Double / External / Movie / News） | 6ドメイン固定順回し |
| 情報源 | ジャンル辞書 + Wikipedia + カテゴリ付きニュースRSS（NHK / ITmedia 等） | トレンド + NHK + Google News（3段階） |
| ターン数 | 12ターン/トピック、最大50/セッション | 100ターン/ドメイン、最大600/セッション |
| 起動方法 | 自動（アイドル検知）/ 手動 | 手動のみ（「未来展望」ボタン） |
| セッション形式 | 単発トピック | 番組形式（ドメインアナウンス → お題 → 議論） |
| テーマ反復抑制 | ループ検出（5種類） | ループ検出 + 蓄積型テーマ抑制 |
| 要約 | Worker → Mio 読み上げ | Worker + 継続考察テーマ → Mio 読み上げ |

---

## 3. アーキテクチャ

### 3.1 コンポーネント構成

```
internal/application/idlechat/
├── orchestrator.go       # IdleChatOrchestrator 本体（ライフサイクル・発話生成・ループ検出・TTS連携）
├── forecast_session.go   # 未来展望セッション（ドメイン定義・トレンド収集・テーマ抑制）
├── topic_generator.go    # トピック生成戦略・外部シード取得
└── topic_store.go        # TopicStore（セッション要約の永続化）
```

### 3.2 LLM 役割分担

| 処理 | 担当 | 理由 |
|------|------|------|
| 通常トピック生成 | Mio (gemma3:4b) | 軽量・高速 |
| 未来展望キーワード抽出 | ローカル Forecast provider（local Coder / Worker） | 外部 API クレジット消費を避ける |
| 未来展望トピック生成 | ローカル Forecast provider（local Coder / Worker） | 外部 API クレジット消費を避ける |
| ディスカッション発話 | 各話者の LLM | ペルソナ維持 |
| 既出テーマ抽出 | RenCrow_LLM Worker alias (`Worker`) | 要約タスク、ローカル無料 |
| まとめ生成 | RenCrow_LLM Worker alias (`Worker`) | 要約タスク、ローカル無料 |

Forecast で外部 Coder API を使う場合は `idle_chat.forecast_external_enabled: true` の明示設定が必要。明示設定がない場合、外部 Coder provider は選択しない。生成失敗時に別の外部 provider へ自動切替しない。

通常 IdleChat では、話者ごとの LLM リクエストに `think` を常に明示する。
既定は Mio が常時 `think: false`、Shiro は IdleChat のみ `think: false`、その他の話者/モデルは `think: true` とし、`idle_chat.speaker_llm_options` で話者ごとに切り替え可能にする。
Shiro の NoThink は通常 Worker には適用せず、IdleChat の Shiro 呼び出しに限定する。IdleChat 以外の Shiro/Worker は `think: true` で運用する。

RenCrow_LLM 側の現在の正本では、IdleChat 内の短文 Shiro 発話は Worker endpoint (`:8082`) 上の `ChatWorker` alias を使う。`ChatWorker` は `nothink` / GPT-OSS `low` / `max_tokens` cap 8192 の短文用 alias である。要約、既出テーマ抽出、通常 Worker 処理は同じ endpoint 上の `Worker` alias を使い、`reasoning` / GPT-OSS `high` / `max_tokens` cap 65536 とする。`ChatWorker` と `Worker` は同一 Ollama runner を共有し、Ollama の `num_ctx` は 65536 のまま、`ChatWorker` の有効入力 budget だけ proxy 側で 16384 に丸める。

---

## 4. ブレイク体系

全モード共通。TTS イベントドリブン。

| タイミング | 待ち時間 | 起点 |
|----------|--------|------|
| 同一話者内の句間 | 200ms | TTS チャンクの `pause_after` |
| 話者交代（Mio↔Shiro） | 500ms | TTS 完了イベント後 (`speakerBreak`) |
| トピック/ドメイン交代 | 1000ms | TTS 完了イベント後 (`topicBreak`) |

---

## 5. 通常モード

### 5.1 ライフサイクル

```
Start()
  └─ goroutine: monitorLoop() — 30秒ごとに checkAndStartChat()
       ├─ chatBusy/workerBusy → スキップ
       ├─ nextTopicAt 前 → スキップ
       ├─ アイドル時間 < intervalMin（manualMode でなければ）→ スキップ
       └─ runChatSession()
```

### 5.2 お題カテゴリ

IdleChat のお題は、ユーザー観測・ログ・E2E 評価では次の 7 カテゴリで扱う。
内部実装名や生成関数が異なっても、Viewer 表示、履歴、ログ、テストではこのカテゴリ単位で追跡できること。

Story は、構築中に非現実的であることが分かったため仕様対象から削除する。
story-simple は Story の代替として実装済みの独立モードであり、Story とは別物として扱う。

| カテゴリ | 内部 Strategy | 内容 | 選択単位 |
|----------|---------------|------|----------|
| Single | `single` | 260 個のジャンルプールから 1 個を選び、そのジャンルを深掘りする | 通常 IdleChat |
| Double | `double` | 2 ジャンルの意外な掛け合わせを作り、その組み合わせを深掘りする | 通常 IdleChat |
| External | `external` | Wikipedia Random 等の外部刺激 1 件とジャンルを組み合わせる | 通常 IdleChat |
| Movie | `movie` | 「〜ってどんな映画？」形式で架空映画の内容を深掘りする | 通常 IdleChat |
| News | `news` | ニュース見出し 1 件を選び、そのニュース自体を深掘りする | 通常 IdleChat |
| Forecast | `forecast` | 6 ドメイン固定順の未来展望セッション | 未来展望モード |
| Story-Simple | `story-simple` | 昔話の骨格を使い、主人公を別の存在に置き換えた軽量リメイク短編 | story-simple モード |

通常 IdleChat の自動ローテーションおよび通常評価では、`single → double → external → movie → news → forecast → story-simple` の順で最低 1 巡できること。
`forecast` と `story-simple` はモード別カテゴリとして扱うが、自動ローテーションにも含め、手動起動または専用 E2E でも個別に検証できること。

#### お題サンプル正本

以下を、お題生成のユーザー評価用サンプル正本とする。
実装はこの文面を固定出力してはいけないが、各カテゴリの題名品質、粒度、混ぜ方、禁止すべきメタ表現の基準として扱う。

| 順序 | カテゴリ | 基準お題 |
|------|----------|----------|
| 1 | Single | 古書店の店主が見つけた、手紙に残る記憶の扱い方 |
| 2 | Double | 潮汐と郵便制度に共通する、遅れて届くものの設計 |
| 3 | External | 地下鉄博物館に残る音声案内と織物の記録性 |
| 4 | Movie | 「雨上がりの映写室」ってどんな映画？ |
| 5 | News | 新しい医療制度の検討が、現場の判断に与える影響 |
| 6 | Forecast | AI 技術が、個人の記憶整理をどう変えるか |
| 7 | Story-Simple | 桃太郎の主人公を宅配業者に置き換えたら、鬼ヶ島への配送はどう変わるか |

基準:

- Single は 1 ジャンルに人物・物・場所・場面の具体アンカーを入れる。
- Double は 2 領域の共通構造が見える題名にする。
- External は provider 名、取得経路、記事・ページ・検索結果などのメタ語を出さず、素材とジャンルを自然に接続する。
- Movie は必ず `「〜」ってどんな映画？` の形にする。
- News はニュースの論点・背景・影響を扱い、ランダムジャンルや外部素材と混ぜない。
- Forecast は将来変化の問いとして、対象領域と変化先が分かる題名にする。
- Story-Simple は元話、置換後の主人公、リメイクの方向が分かる題名にする。

#### お題読み上げ文字列の契約

IdleChat のお題読み上げでは、表示用 topic と speech topic の関係を次のように固定する。
この節の変換結果は読み上げ専用であり、Viewer の topic 表示、timeline、history、summary へ描画してはいけない。
カテゴリ分岐、`今日のお題。` の前置、Viewer 描画禁止、TTS 専用化は実装コードで決定的に実装する。これらを LLM prompt の指示で制御してはいけない。

- Single / Double / External / Movie / News / Forecast は、取得済み topic へ `今日のお題。` を前置するだけの置換処理とする。カテゴリ名、内部 strategy、seed、provider 名は読み上げ本文へ入れない。
- Single / Double / External / Movie / News / Forecast の topic 本文を LLM で再生成、要約、言い換えしてはいけない。許可するのは句点、括弧、読み仮名など、同一内容を保つ正規化だけである。
- Story-Simple は独立モードの導入発話として、元話と置換後の主人公を含む短い読み上げタイトルを生成してよい。ただし Story の多段階生成仕様、story category、`story` sessionMode へ戻してはいけない。
- 読み上げ用文字列は `今日のお題。<topic>` の 1 発話単位とし、TTS `speech_text` としてのみ扱う。Viewer の描画正本は変換前の topic / display event であり、読み上げ用文字列から描画本文を作ってはいけない。

#### News カテゴリの契約

- News は NHK RSS 等のニュースシードから 1 件を選び、そのニュースの論点、背景、影響を深掘りする。
- News seed は `title / category / source / url` を保持できる。カテゴリ例は `general / culture / business / world / sports / tech` とし、取得元追加時も News カテゴリ内の追跡メタデータとして扱う。
- News ではランダムジャンルを混ぜない。`News + ジャンル` は External でも News でもない曖昧な状態として禁止する。
- News を External へ黙ってすり替えない。ニュースシード取得失敗時は、`news_seed_unavailable` 等の診断をログに残し、カテゴリ成功として扱わない。
- News の Viewer 表示、TTS、ログには、同じ topic/category が残ること。表示だけ News、内部ログだけ External のような不一致は禁止する。

#### External カテゴリの契約

- External は外部刺激とジャンルの組み合わせを扱うカテゴリであり、純粋なニュース深掘りではない。
- 生成用 prompt では、外部刺激を `素材` として渡し、`Wikipedia`、`外部刺激`、`ランダム記事`、`偶然の記事` のような取得経路や provider 名をお題本文へ出させない。
- provider / category はログ追跡用に保持するが、Viewer に出るお題本文は「素材 + ジャンル」から作った自然な題名にする。
- ニュース見出しを深掘りする場合は News を使う。External でニュースを使う場合でも、News と混同しないよう provider / category を明示する。

#### Movie カテゴリの契約

- Movie は独立カテゴリであり、Single / Double / External の隠しフラグとして扱わない。
- Movie として生成した topic は、Viewer、履歴、ログ、E2E で `movie` として識別できること。

#### 正当性判定

このカテゴリ仕様は、次をすべて満たす場合のみ正当とする。

- 正本、参照元仕様、実装、Viewer、E2E のカテゴリ一覧が `single / double / external / movie / news / forecast / story-simple` で一致している。
- 通常 IdleChat の自動または強制評価で、`single → double → external → movie → news → forecast → story-simple` を 1 巡できる。
- 各 session の topic/category/strategy が、Viewer 表示、履歴、ログ、TTS イベントで追跡できる。
- News は news seed 1 件から生成され、display topic と内部 category が `news` のまま保持される。
- Movie は category/strategy として `movie` を持ち、`movie=true` の隠し属性だけで表現されない。
- Story は仕様対象から削除され、`story` category / sessionMode / 起動 API に戻らない。
- Story-Simple は `story-simple` として追跡され、Story のフォールバックとして扱われない。
- seed 取得失敗、生成失敗、カテゴリ未対応は明示的なエラーまたは診断として出し、別カテゴリで成功したように扱わない。
- 上記を確認するテストまたは E2E ログがない状態で、実装完了扱いにしない。

- **外部シード**: 起動時に1日1回取得（Wikipedia 10件、NHK 10件）
- **重複排除**: 直近12トピックと類似度チェック、最大3回リトライ

### 5.3 セッション実行

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

### 5.4 ループ検出（5種類）

| 種別 | 条件 |
|------|------|
| `exact_repeat` | 直近4発話内に完全一致 |
| `alternating_repeat` | A-B-A-B パターン（類似度 ≥ 0.9） |
| `template_repeat` | 話者テンプレートの繰り返し |
| `high_similarity` | 直近10発話の類似度が高い |
| `what_if_repeat` | 「もし〜だったら/なら」が半数以上 |

### 5.5 発話生成リトライ（4段階）

| 段階 | 条件 | リトライ内容 |
|------|------|------------|
| 1. 無効応答 | `invalidIdleResponse` | 「自然な会話文で言い直して」 |
| 2. スタイル問題 | `needsIdleStyleRetry` | 「別の手で自然に返して」 |
| 3. プロンプト漏出 | `hasPromptLeak` | 「指示文の断片を消して」 |
| 4. 発言帰属違反 | `violatesAttribution` | 「相手の案を受ける形に」 |

---

## 6. 要約と読み上げ

全モード共通。トピック/ドメインの議論終了後:

```
1. saveSummary() → Worker (Shiro) が要約生成
2. TopicStore に永続化（JSON Lines）
3. Timeline に idlechat.summary イベント emit
4. speakSummary() → Mio が要約を読み上げ（TTS完了待ち）
5. topicBreak (1000ms) → 次の IdleChat session で次トピックへ
```

**SessionSummary 型**:

```go
type SessionSummary struct {
    SessionID       string        // "idle-{unix}" / "forecast-{unix}"
    Title           string        // "3月15日の{topic}の話題まとめ"
    Topic           string
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

## 7. Viewer 連携

### 7.1 REST API

| エンドポイント | メソッド | 用途 |
|-------------|--------|------|
| `/viewer/idlechat/start` | POST | 通常モード手動開始 |
| `/viewer/idlechat/forecast` | POST | 未来展望モード開始 |
| `/viewer/idlechat/stop` | POST | 停止（両モード共通） |
| `/viewer/idlechat/status` | GET | 状態取得 |
| `/viewer/idlechat/logs` | GET | 履歴取得（両モード統合） |

### 7.1.1 Viewer 音声入出力の active 制御

Viewer は PC / ケータイ / 複数タブで同時に開ける。ただし、スピーカ（TTS 再生）とマイク（STT 入力）の操作対象は、それぞれ常時 1 つのブラウザだけとする。

- 各 Viewer は `viewer_client_id` を持つ。
- スピーカは後出し優先とし、最後に音声を有効化した Viewer が `active_audio_viewer_id` になる。
- マイクも後出し優先とし、最後に STT 開始操作をした Viewer が `active_input_viewer_id` になる。
- Viewer 表示は複数同時に許可するが、active ではない Viewer の TTS playback ack / STT 操作結果を会話進行の根拠にしてはいけない。
- IdleChat の TTS 完了待ちは `active_audio_viewer_id` と一致する ack のみで解除する。
- active ではない Viewer からの ack はログには残すが、IdleChat の待ち解除には使わない。
- active Viewer が後から切り替わった場合、旧 Viewer は音声再生またはマイク入力を停止し、新 active Viewer だけが操作対象になる。
- active audio viewer が未設定の場合、TTS を「聞いた」として IdleChat を進めてはいけない。
- TTS playback ack が返らないことは音声系エラーとしてログに残す。ただし IdleChat / Forecast の会話進行やお題更新を止める要因にはしない。
- TTS 完了待ちは短時間の best-effort とし、timeout 後は `tts_error=true` として記録して会話を継続する。

### 7.1.2 IdleChat 本文表示と TTS 同期

IdleChat の Viewer 本文表示は、`idlechat.message` の全文即時描画ではなく TTS chunk 同期を正とする。
`idlechat.message` は session / speaker / raw content / pending 発話の保持に使い、本文表示の進行根拠は原則として `tts.audio_chunk.display_text` とする。

この節は IdleChat の表示・TTS 同期に関する正本である。
一般 TTS / ChatAudioSync 仕様と矛盾する場合、IdleChat ではこの節を優先する。

- `idlechat.message` 受信時は、話者とセッションに紐づく pending 発話枠だけを作り、本文全文は表示しない。
- Mio / Shiro の本文は、対応する TTS chunk の `display_text` を到着順に追加して表示する。TTS provider 用の `speech_text` / `text` をそのまま本文へ出してはいけない。
- `message_id` / `turn_index` の一致は、TTS chunk がどの pending 発話を表示してよいかを決める必須条件とする。ID 一致がない TTS chunk は本文表示に使わない。
- 対応する `idlechat.message` が無い TTS chunk は、本文を描かず診断表示またはログへ倒す。
- スピーカ ON の場合、active audio Viewer で TTS 再生開始または再生確定した chunk に合わせて、対応する表示イベントの発話へ再生中 marker を付ける。
- スピーカ OFF の場合も、音声が流れる想定の時間だけ待ってから次 chunk へ進み、本文表示は同じ chunk 同期で進める。
- `tts.session_completed` は TTS 生成完了であり、ユーザーが聞いた完了や Viewer 表示完了ではない。
- IdleChat の TTS 完了待ちは、active audio Viewer が実際に観測した response の playback ack、またはスピーカ OFF の明示 display-only 完了でのみ解除する。
- TTS playback ack が返らない場合はエラーとして記録するが、会話進行の停止要因にはしない。
- TTS chunk が一定時間来ない場合は、TTS エラーとして診断を表示し、fallback 文で本文を補完しない。
- TTS provider への push が失敗した場合、失敗はログに残すが、IdleChat の進行制御では通常の TTS 完了と同じ扱いで pending を消化して会話を継続する。
- `tts.session_completed` だけを見て、観測していない response を playback 済みとして ack してはいけない。

#### IdleChat 通常会話の TTS chunk 契約

IdleChat 通常会話の TTS chunk は、必ず `idlechat.message` の `message_id` に従属する。
TTS chunk は音声・口パク・本文表示・再生中 marker・ACK を同じ発話へ対応付けるため、次の単位を壊してはいけない。

各 chunk は同一単位で以下を持つ。

- `message_id`
- `turn_index`
- `response_id`
- `utterance_id`
- `chunk_index`
- `display_text`
- `speech_text`（現行 payload の `text` は互換 alias とする）
- `audio_path` または `audio_url`

通常会話では、`display_text` と `speech_text` は同じ chunk から生成されなければならない。
原則として完全一致とし、読み替えが必要な場合も同一 chunk 内で理由を説明できる正規化だけを許可する。
`display_text` と `speech_text` を別々に chunk 分割し、同じ index で対応したものとして扱ってはいけない。
chunk 境界が一致しない場合は、TTS / 表示同期の契約違反として扱う。

topic や読み上げ用の表記正規化など、表示と発話を分ける必要がある場合でも、分割単位は単一の chunk 計画から作る。
表示本文の描画は `display_text` を使い、`speech_text` / `text` は音声 provider 用の文字列として扱う。

### 7.2 Viewer UI

- 「IdleChat開始」「IdleChat停止」ボタン — 通常モード
- 「未来展望」ボタン（青系、独立配置） — 未来展望モード
- 状態表示: `IdleChat: off` / `on` / `on (talking)`
- IdleChat パネル（タブ切替）: Mode・Current Topic・履歴テーブル
- Timeline: `idlechat.message` / `idlechat.summary` イベント（ルート色: 紫）

### 7.3 双方向制御

```
IdleChat → Viewer: 発話/要約イベント → EventHub → SSE → ブラウザ表示
Viewer → IdleChat: message.received → NotifyActivity() → 中断
```

`shouldStopIdleChatByEvent()`: `IDLECHAT` ルートや TTS イベントは無視、`message.received` のみ中断トリガー。

---

## 8. 並行安全性

| 機構 | 用途 |
|------|------|
| `sync.Mutex` (`o.mu`) | 全フィールドの排他制御 |
| `context.Context` (`o.ctx`) | goroutine キャンセル伝播 |
| `sync.WaitGroup` (`o.wg`) | Stop() での終了待機 |
| `sync.RWMutex` (`cacheMu`) | DailySeedCache のスレッドセーフアクセス |
| `sync.RWMutex` (`trendMu`) | TrendCache のスレッドセーフアクセス |

---

## 9. 定数一覧

| 定数 | 値 | 用途 |
|------|-----|------|
| `idleCheckInterval` | 30s | monitorLoop チェック間隔 |
| `maxTurnsPerTopic` | 12 | 通常モードの1トピック最大ターン数 |
| `speakerBreak` | 500ms | 話者交代ブレイク |
| `topicBreak` | 1000ms | 次 IdleChat session / ドメイン交代ブレイク |
| `defaultChunkPause` | 200ms | TTS チャンク間ブレイク |
| `forecastTurnsPerDomain` | 100 | 未来展望の1ドメイン最大ターン数 |
| `forecastCheckpointInterval` | 15 | 未来展望のテーマ抑制チェック間隔 |

---

**関連文書**:
- [仕様.md §8](仕様.md#8-idlechat) — 概要
- 元 `docs.zip` 内 `09_IdleChat仕様/IdleChat仕様.md` — 通常モードの完全仕様。通常モードを実装・修正するときだけ一時復帰する。
- 元 `docs.zip` 内 `09_IdleChat仕様/未来展望セッション仕様.md` — 未来展望モードの完全仕様。未来展望モードを実装・修正するときだけ一時復帰する。

## 15. Topic Context Memo

詳細仕様: 元 `docs.zip` 内 `09_IdleChat仕様/IdleChat_Topic_Context_Memo仕様.md`。Topic Context Memo を実装・修正するときだけ一時復帰する。

`single` / `double` / `external` / `movie` / `news` / `forecast` は、お題文字列だけでなく、関連語句・語句の意味・お題との関係を topic cache に保持してよい。
この補助情報は `ContextTerms` として `TopicGenerationResult` に紐づける。

基本方針:

- 対象は `single` / `double` / `external` / `movie` / `news` / `forecast` とする。
- 1 topic あたり関連語句は 3〜8 件を目安とする。
- `term / meaning / relevance / source` を持つ。
- topic cache 補充時に生成し、session 開始時に `sessionContext` へ内部補助として注入する。
- Viewer / TTS / topic display には `ContextTerms` を直接出さない。
- 関連語句生成に失敗しても、topic が有効ならキャッシュしてよい。
- `movie` は自前 Movie DB から関連する映画・俳優・監督等を引き、架空映画の参考メモとして扱う。詳細は元 `docs.zip` 内 `09_IdleChat仕様/IdleChat_Movie_DB_Context_Memo仕様.md` に従う。movie カテゴリの Context Memo を実装・修正するときだけ一時復帰する。
- `story-simple` は本仕様の対象外とし、必要なら別途、物語設定メモとして扱う。
- 対象カテゴリの category / strategy を維持し、別カテゴリへすり替えない。

目的は、IdleChat の会話がお題名の表面だけで終わらず、題材の用語・背景・見方を自然に扱えるようにすることである。

## 16. IdleChat Topic Cache

詳細仕様: 元 `docs.zip` 内 `09_IdleChat仕様/IdleChat_Topic_Cache仕様.md`。Topic Cache を実装・修正するときだけ一時復帰する。

IdleChat は、session 開始時のお題生成待ちを減らすため、全7カテゴリにお題キャッシュを持つ。

対象カテゴリ:

```text
single / double / external / movie / news / forecast / story-simple
```

キャッシュ目標:

- `single`: 10件
- `double`: 10件
- `external`: 10件
- `movie`: 10件
- `news`: 10件
- `forecast`: forecast domain ごとに10件
- `story-simple`: 完成本文10件

基本方針:

- 起動後にバックグラウンドで補充する。
- 1件使ったら、目標数へ戻すよう補充する。
- `chatActive / chatBusy / workerBusy` 中は補充生成を開始しない。
- LLM を使う補充は同時多発させない。
- cache が空の場合は同カテゴリの inline topic 生成へ fallback してよい。
- 別カテゴリへすり替えて成功扱いにしてはいけない。
- Viewer / TTS には cache 内部情報を直接出さない。
- `story-simple` は topic だけではなく、Worker で生成・判定・必要時修正した完成本文を cache する。
