# RenCrow Memory Lifecycle / Recall Context 正本実装仕様

- Status: 正本実装仕様 v1.0
- Date: 2026-06-19
- Scope: RenCrow / Mio 通常会話、RecallPack、UserMemory、Knowledge / Source Registry、Persona / Observation、OperationMemory、Runtime Logs
- Primary Agent: Mio
- Related Agents: Shiro, Aka, Ao, Gin, Kin, Kuro, Midori
- Canonical Scope: Memory lifecycle、Recall Context、prompt 注入方針、promotion / decay / forgetting、recall trace の実装判断
- Related Storage Spec: `docs/10_新仕様/09_Memory_SourceRegistry仕様.md`

## 0. 目的

本仕様は、RenCrow における「記憶の保存」「記憶の昇格」「記憶の忘却」「Mio への prompt 注入」「想起 trace」を統一的に扱うための仕様である。

現状の RenCrow / Mio には、ConversationEngine、RecallPack、UserMemory、KBManager、PersonaEditor、L1 SQLite、DuckDB、Qdrant、Source Registry、Persona Observation、OperationMemory、Runtime Logs など、短期〜長期に相当する部品が存在する。ただし、それらはまだ完全な memory lifecycle として一貫運用されているわけではない。

この仕様では、保存媒体そのものではなく、記憶の状態遷移と prompt 注入可否を中心に定義する。つまり、「どこに保存されているか」よりも、「Mio が読んでよいか」「どの根拠で正式記憶になったか」「いつ弱めるか」「どの turn で何を読んだか」を重視する。

## 0.1 基本原則

1. Mio は DB 全体を直接読まない。毎ターン、RecallPack、UserMemory prompt、補助 context によって選別されたものだけを読む。
2. 保存されたものと、Mio が使ってよいものを分離する。
3. candidate / staging / raw observation は正式記憶ではない。
4. UserMemory、Conversation Memory、Character / Persona、Knowledge、Operation、Runtime Logs を混ぜない。
5. Source Registry / staging / validation を通っていない外部情報を、正式 Knowledge として扱わない。
6. Runtime / Status Logs は、原則として記憶ではなく観測ログであり、bounded retention / GC の対象にする。
7. 各 turn で「どの記憶を、なぜ、どの section に注入したか」を trace する。
8. 多人数化に備えるが、本仕様の初期適用対象は Mio の通常会話 input context とする。

---

# 1. 現行Mio入力コンテキスト

## 1.1 現行通常会話 input context の構築順

Mio の通常会話では、概念上、次の順で input context を構築する。

| 順序 | Context | 由来 | 注入種別 | 備考 |
|---:|---|---|---|---|
| 1 | System Prompt | `cfg.Prompts.MioPersona` / persona file | system | Mio の基本人格・振る舞い |
| 2 | RecallPack | `conversationEngine.BeginTurn(chatID, userMessage)` | system / assistant / memory-like prompt messages | `FilterForRole("chat")` 後に `ToPromptMessages()` へ変換 |
| 3 | UserMemory Prompt | `m.userMemoryPrompt(ctx)` | system | confirmed / pinned、active、normal sensitivity のみ |
| 4 | Persona Edit Intent | `editPersona()` | control flow | 成功時は通常生成に進まず編集結果を返す |
| 5 | Web Search Context | `needsWebSearch(userMessage)` | system | 現行は明示検索時のみ。将来は freshness-sensitive policy へ拡張 |
| 6 | 発言帰属ガード | RecallPack ShortContext | user / guard | 他者発言を Mio 自身の新規アイデアとして扱わないための補助 |
| 7 | Glossary Recent Context | `recentContext(ctx, 6)` | system | 最近の語彙・話題を軽い補足として注入 |
| 8 | Current User Message | user input + attachment metadata | user | LLM に渡る最後の user message |

## 1.2 RecallPack

RecallPack は、ConversationEngine が現在の発話に必要な会話文脈、短期文脈、summary、KB / search cache などから候補を選別したものである。

必須要件:

- `BeginTurn(ctx, chatID, userMessage)` によって turn 開始時に生成する。
- chat role では `FilterForRole("chat")` を必ず通す。
- prompt へ入れる前に、candidate の重複、古さ、機密性、役割適合性を評価する。
- RecallPack 生成時点で recall trace を作成する。
- RecallPack は「DB 全体の投げ込み」ではなく「選別済み context」でなければならない。

## 1.3 UserMemory Prompt

UserMemory Prompt は、Mio が会話時に使ってよいユーザー記憶のみを注入する。現行仕様では次の条件を満たすものだけを注入対象にする。

| 条件 | 必須値 |
|---|---|
| status | `confirmed` または `pinned` |
| active | `true` |
| sensitivity | `normal` |
| scope | `all_personas` または `mio` |
| validity | superseded / deleted ではない |

注入禁止:

- `observed`
- `candidate`
- `sensitive`
- `inactive`
- `superseded`
- evidence が弱い推測記憶
- 本人の明示確認が必要な個人情報

## 1.4 Persona Edit Intent

Mio が persona 編集意図を検出した場合、通常応答ではなく PersonaEditor の処理を優先する。編集成功時は、LLM 生成に進まず編集結果を返す。

Persona は会話の事実記憶ではない。`workspace/persona/mio.md` 等の persona file は、Mio の人格・口調・振る舞いの正本であり、UserMemory や Conversation Memory と混ぜて扱わない。

## 1.5 Web Search Context

現行の Web Search Context は、明示的な検索意図がある場合のみ注入する。

現行条件例:

- 「検索して」
- 「調べて」
- 「Webで確認して」

現行では、単に「最新」「今日」という語があるだけでは検索しない設計である。ただし、RenCrow の将来設計では、次の順序へ拡張する。

```text
Local Knowledge / News DB
  → Search Cache
  → Source Registry official source
  → External Search only if freshness risk remains
```

外部検索の結果は、直接 Knowledge として扱わない。Search Cache / Staging / Validator / Source Registry を通し、採用可能なものだけが Knowledge へ昇格する。

## 1.6 発言帰属ガード

RecallPack の ShortContext には Mio 自身の過去発言と、ユーザー・他 Agent・外部 source の発言が混在する可能性がある。そのため、Mio が他者発言を自分の新規アイデアとして扱わないように guard context を追加する。

要件:

- speaker metadata を保持する。
- source が `mio` でない発言は、引用・参照・要約として扱う。
- 他 Agent の提案を採用する場合は、採用判断を明示する。

## 1.7 生成後フロー

Mio の生成後処理は、概念上、次の順で行う。

```text
LLM response
  → attribution violation check
  → retry / rewrite if needed
  → conversationEngine.EndTurn(userMessage, response)
  → user memory candidate capture
  → trace finalize
```

生成後に保存されるものも、即座に正式記憶になるわけではない。UserMemory であれば observed / candidate、Knowledge であれば staging / validated、Persona であれば persona observation / meta profile update として扱う。

---

# 2. 記憶の分類: User / Conversation / Character / Knowledge / Operation / Runtime

## 2.1 分類一覧

| 分類 | 主対象 | 正本 / 保存先 | Prompt 注入 | 状態管理 |
|---|---|---|---|---|
| UserMemory | ユーザーの好み、制約、関係、継続指示、プロジェクト | L1 SQLite / DuckDB meta / vector index | confirmed / pinned のみ | observed → candidate → confirmed / pinned |
| Conversation Memory | 現在会話、thread、daily digest、monthly highlight | LangGraph state / L1 SQLite / DuckDB | RecallPack 経由 | turn / thread / digest / archive |
| Character / Persona | Mio 等の人格、口調、役割、観測候補 | Markdown persona file / Persona Observation store | persona 正本は常時、observation は直接注入しない | observation → review → persona edit |
| Knowledge | 外部世界の事実、作品DB、ニュース、技術情報 | Source Registry / DuckDB / VectorDB / Domain DB | 必要時 RecallPack 経由 | staging → validated → promoted |
| OperationMemory | 運用手順、障害対応、再発防止、Worker / Coder 向け知識 | Markdown / ops memory store | 必要時、運用 route で参照 | active / archived |
| Runtime / Status Logs | health、heartbeat、context usage、repair logs、session state | JSONL ring / bounded store | 原則注入しない | retention / compact / GC |

## 2.2 UserMemory

UserMemory はユーザー固有の記憶であり、Knowledge とは分離する。

例:

| 内容 | 分類 |
|---|---|
| 「ユーザーは結論から話してほしい」 | UserMemory / constraint |
| 「ユーザーは映画 A が好き」 | UserMemory / preference |
| 「映画 A の監督は X」 | Knowledge / domain fact |
| 「ユーザーは RenCrow を開発している」 | UserMemory / project |

### UserMemory type

| type | 説明 | 注入方針 |
|---|---|---|
| `profile` | 明示されたプロフィール | 原則 confirmed 後のみ |
| `preference` | 好み、文体、形式 | confirmed / pinned を注入 |
| `constraint` | 継続指示、禁止事項、出力制約 | pinned 候補。強く注入 |
| `project` | 継続中のプロジェクト情報 | 関連 turn のみ注入 |
| `relationship` | キャラとの距離感、呼称、関係性 | 慎重に扱う |
| `episode` | 会話や判断の出来事 | 要約化して L2 / L3 へ |
| `skill` | ユーザーの技能や関心領域 | 推論補助に利用 |
| `sensitive` | 健康、家庭、個人事情など | 原則注入しない。明示確認が必要 |

### UserMemory record schema

```json
{
  "memory_id": "usrmem_001",
  "user_id": "ren",
  "type": "preference",
  "statement": "ユーザーは短く、読みやすく、論理の通った文章を好む",
  "status": "confirmed",
  "confidence": 0.92,
  "sensitivity": "normal",
  "scope": "all_personas",
  "evidence_event_ids": ["evt_001", "evt_118"],
  "created_at": "2026-06-19T00:00:00+09:00",
  "updated_at": "2026-06-19T00:00:00+09:00",
  "last_used_at": null,
  "last_confirmed_at": "2026-06-19T00:00:00+09:00",
  "ttl_policy": "normal",
  "superseded_by": null,
  "embedding_id": "vec_001"
}
```

## 2.3 Conversation Memory

Conversation Memory は会話の流れを扱う。Mio の会話時には、ConversationEngine がこれを選別し、RecallPack として prompt に渡す。

対象:

- 現在 turn
- 直近発話
- thread state
- thread summary
- daily digest
- monthly highlight
- interruption state
- unresolved issue
- previous task state

Conversation Memory は全文の永続保持を前提にしない。古い raw conversation は要約化・圧縮・archive 化し、prompt へは要約または relevant snippet のみを入れる。

## 2.4 Character / Persona Memory

Character / Persona は 2 種類に分ける。

| 種類 | 説明 | 扱い |
|---|---|---|
| Persona 正本 | `workspace/persona/mio.md` 等。人格・口調・役割 | prompt の土台として常時寄与 |
| Persona Observation | observation_log、meta_profile_update、discomfort / trigger 等 | 直接注入しない。review 後に persona edit へ |

重要:

- Persona は「記憶」ではなく「人格設定の正本」として扱う。
- observation_log は候補であり、正本ではない。
- persona_interface_session などの状態確認ログは runtime log 寄りであり、bounded GC 対象とする。

## 2.5 Knowledge

Knowledge は外部世界の事実や domain DB である。UserMemory と混ぜない。

対象:

- 映画、小説、ドラマ、漫画、音楽、演劇、ボドゲ等の作品情報
- 技術仕様、リリース情報、公式ドキュメント
- ニュース記事とその metadata
- Source Registry に登録された source
- validated / promoted 済み知識

Knowledge lifecycle:

```text
raw fetch
  → staging
  → normalized
  → validated
  → promoted
  → indexed
  → recalled
```

Knowledge は Source Registry / staging / validator を通ってから正式DBへ入れる。検索結果や RSS 取得結果を、即座に Mio の正式記憶として扱ってはならない。

## 2.6 OperationMemory

OperationMemory は RenCrow の運用記憶であり、Mio の個人的な会話記憶ではない。

対象:

- 障害対応手順
- 再起動手順
- 再発防止メモ
- Worker / Coder が参照すべき運用知識
- 日次運用メモ
- 修復方針

保存は Markdown など人間が読める file store を基本とする。Mio への常時注入はしない。運用 route、Shiro / Worker、Kuro / Heavy が必要に応じて参照する。

## 2.7 Runtime / Status Logs

Runtime / Status Logs は記憶ではない。

対象:

- status polling
- context usage
- repair / run / check logs
- heartbeat
- CPU / health snapshots
- interface session
- viewer temporary state
- watchdog / tool mediation logs

扱い:

- JSONL ring / bounded store に保存する。
- retention / compact / GC の対象にする。
- prompt へ原則注入しない。
- 長期記憶に昇格しない。
- 障害調査で有益な知見が得られた場合のみ、OperationMemory の候補として要約を作成する。

## 2.8 現行キャラクター体系との整合

RenCrow の現行仕様では、キャラクターは単なる口調設定ではなく、実行責務と安全境界を持つ Agent として扱う。

| 境界 / 枠 | Agent | 主責務 |
|---|---|---|
| Chat | Mio | ユーザー対話、ルーティング判断、結果返却、進捗報告、記憶統合 |
| Worker | Shiro | 実行、ツール呼び出し、patch / command 適用、ログ記録 |
| Coder | AO / Aka / Kin / Gin | plan / patch / proposal / risk / cost hint 生成 |
| Heavy | Kuro | 深い分析、根本原因調査、リスク確認 |
| Wild | Midori | 創作、画像プロンプト、雰囲気抽出、横方向探索 |

旧キャラ体系である Lumina / Claris / Nox は現行仕様の対象外とする。本仕様では Mio を primary Chat Agent とし、将来の多人数協議では role-specific recall pack を各 Agent に配布する。

---

# 3. L0〜L4 Lifecycle

## 3.1 レイヤー定義

本仕様では、保存媒体ではなく lifecycle 上の位置により L0〜L4 を定義する。

| Layer | 名称 | 主内容 | 代表保存先 | Prompt 注入 | Retention |
|---|---|---|---|---|---|
| L0 | Turn / Prompt Context | 現在 user message、attachment、system prompt、直近数発話 | prompt memory / LangGraph state | 常時 | turn 単位 |
| L1 | Thread / Hot Memory | 直近会話、thread state、session state、short summary、search cache | LangGraph checkpoint / L1 SQLite / optional Redis | RecallPack 経由 | 数時間〜数日 |
| L2 | Daily / Candidate Memory | daily digest、thread summary、candidate UserMemory、Source Registry staging、Knowledge review 待ち | L1 SQLite / DuckDB | 条件付き | 数日〜1か月 |
| L3 | Validated / Long-term Active Memory | confirmed UserMemory、validated Knowledge、promoted source、Persona 反映候補 | DuckDB / VectorDB / L1 SQLite meta | 条件付き / relevant only | 長期 |
| L4 | Canonical / Pinned / Archive | pinned UserMemory、Persona 正本、OperationMemory、archive、canonical Knowledge | Markdown / DuckDB / VectorDB / Parquet | pinned / persona は強く寄与。他は条件付き | 無期限または明示削除まで |

## 3.1.1 実装命名・物理ストアとの対応

L番号の正本定義は上記の lifecycle layer である。物理ストア名やコード上の `L1SQLiteStore` などの命名は、実装経緯に由来する互換名であり、L番号の正本定義ではない。

| Lifecycle layer | 主な lifecycle 上の位置 | 代表保存先 / 物理ストア | 主なコード上の命名・入口 |
|---|---|---|---|
| L0 | turn 中の prompt context / current input | prompt memory / active thread / optional Redis | `RecallPack.RollingSummary`, `ShortContext`, `RealConversationManager.GetActiveThread` |
| L1 | thread / hot memory / search cache | L1 SQLite / optional Redis | `L1SQLiteStore`, `MemoryLayerL1`, `L1SearchCacheEntry`, `RecentBySession` |
| L2 | daily digest / thread summary / candidate / staging | L1 SQLite / DuckDB | `L1DailyDigest`, `L1StagingItem`, `DuckDBStore.SaveThreadSummary`, `ThreadSummary` |
| L3 | confirmed / validated / promoted active memory | L1 SQLite meta / DuckDB archive / VectorDB | `MemoryStateConfirmed`, `L1KnowledgeItem`, `VectorDBStore`, `RecallPack.LongFacts` |
| L4 | pinned / canonical / archive | Markdown / Parquet / DuckDB / VectorDB | `MemoryStatePinned`, `WikiSnippet`, `ExportL1ArchivesParquet`, OperationMemory 系 |

互換上の注意:

- `L1SQLiteStore` は名称上 L1 だが、実際には L1 hot memory だけでなく L2 candidate / staging、L3 confirmed metadata、L4 pinned metadata の保存入口も含む。
- DuckDB は旧文書で L2 と呼ばれることがあるが、本仕様では L2 / L3 / L4 の保存先になり得る。
- Qdrant / VectorDB は旧文書で L3 と呼ばれることがあるが、本仕様では validated active memory や canonical knowledge の検索 index であり、保存媒体単独で L番号を決めない。
- Redis は旧文書で L0 と呼ばれることがあるが、本仕様では optional な hot backend であり、L0 は永続記憶ではない turn / prompt context を指す。

## 3.2 State transition

```text
observed
  → candidate
  → reviewed
  → confirmed / validated
  → promoted
  → active
  → pinned
  → decayed / superseded / archived / deleted
```

UserMemory と Knowledge では名称が少し異なるが、考え方は同じである。

| 汎用状態 | UserMemory | Knowledge | Persona | Runtime |
|---|---|---|---|---|
| observed | observed | raw fetch | observation_log | event log |
| candidate | candidate | staging | meta_profile_update candidate | N/A |
| reviewed | reviewed | normalized / checked | review | incident analysis |
| confirmed / validated | confirmed | validated | accepted edit | N/A |
| promoted | pinned / active | promoted | persona file update | OperationMemory summary |
| decayed | low score | stale | outdated | compacted |
| deleted | forgotten | removed | reverted | GC |

## 3.3 L0: Turn / Prompt Context

L0 は現在 turn の作業領域であり、永続記憶ではない。

内容:

- current user message
- attachment metadata
- system prompt
- current route
- current tool result
- current guard message
- short rolling context

要件:

- L0 raw は長期保存しない。
- EndTurn 後、必要な要約だけ L1 / L2 候補へ送る。
- L0 に含まれる attachment は、必要に応じて source metadata を作成する。

## 3.4 L1: Thread / Hot Memory

L1 は、現在または直近の会話を自然に続けるための hot memory である。

内容:

- thread state
- rolling summary
- recent turn snippets
- interruption state
- unresolved issues
- L1 search / fetch cache
- user memory candidate の一時保存

Promotion to L2:

- thread 終了
- turn 数上限到達
- idle timeout
- topic change
- user request: 「この話を覚えて」「要約して」
- high importance score

Decay:

- 一定時間未参照
- daily digest 生成済み
- raw log が要約へ統合済み

## 3.5 L2: Daily / Candidate Memory

L2 は、当日〜今月の中期記憶と、正式化前の候補を扱う。

内容:

- daily digest
- thread summary
- candidate UserMemory
- Source Registry staging
- Knowledge review 待ち
- Persona Observation review 待ち
- monthly highlight seed

Promotion to L3:

- user confirmation
- repeated evidence
- validator pass
- source trust pass
- domain review pass
- score threshold pass

Decay / compression:

- daily → monthly へ要約統合
- duplicate merge
- low score candidate の削除
- stale source の archive

## 3.6 L3: Validated / Long-term Active Memory

L3 は長期利用される active memory である。

内容:

- confirmed UserMemory
- validated / promoted Knowledge
- active project memory
- relevant long-term episode
- Persona 反映候補
- canonical memory item

Prompt 注入:

- 常時ではない。
- query / intent / domain / persona role / recency / sensitivity によって RecallPack が選ぶ。
- UserMemory の constraint / preference は強めに扱う。

Decay:

- last_used_at が古い
- evidence が弱い
- user correction がある
- superseded_by がある
- source freshness が切れている

## 3.7 L4: Canonical / Pinned / Archive

L4 は、消えにくい正本・ピン留め・長期 archive である。

内容:

- pinned UserMemory
- Persona 正本 Markdown
- OperationMemory
- archive / Parquet
- canonical Knowledge
- manually confirmed project facts

要件:

- pinned は decay 対象外。ただし user 明示削除には従う。
- Persona 正本は PersonaEditor 経由で更新する。
- Archive は prompt へ直接投入せず、検索・要約・再検証を通す。
- OperationMemory は Chat ではなく Worker / Heavy / Ops route で優先参照する。

---

# 4. Prompt Injection Policy

## 4.1 注入ポリシーの分類

| 分類 | 対象 | 注入可否 |
|---|---|---|
| Always | system prompt、Mio persona 正本、current user message、必要最小限の L0 | 注入する |
| Default | RecallPack、confirmed / pinned UserMemory、active normal constraints | 通常注入 |
| Conditional | Knowledge、News、Search Cache、OperationMemory、L2 / L3 episodes | intent / role / freshness / domain に応じて注入 |
| Never Directly | candidate memory、sensitive memory、raw Source Registry record、runtime logs、unvalidated external data | 直接注入しない |

## 4.2 Injection decision

各候補は、次の評価を通して prompt へ入れるかを決める。

```text
candidate
  → scope check
  → sensitivity check
  → lifecycle status check
  → role filter
  → relevance score
  → recency / freshness score
  → token budget check
  → dedupe
  → injection
```

## 4.3 Role-specific RecallPack

将来の多人数化では、全 Agent に同じ RecallPack を渡さない。

| Agent | 主に見る context |
|---|---|
| Mio | L0 / L1 / UserMemory / selected L2 / routing context |
| Shiro | OperationMemory / task state / command logs summary / safety constraints |
| Aka | architecture spec / design memory / risk / dependency context |
| Ao | code context / existing implementation / tests / style rules |
| Gin | risk context / edge cases / security / high-complexity notes |
| Kin | alternatives / review context / finishing constraints |
| Kuro | incident / root cause / assumption review / safety gate |
| Midori | creative brief / mood / visual / lateral exploration context |

## 4.4 Token budget

RecallPack は、モデル文脈長に対する比率で制御する。

| Model context | RecallPack target |
|---:|---:|
| 8k | 600〜900 tokens |
| 16k | 1000〜1500 tokens |
| 32k | 1500〜2500 tokens |
| 64k+ | 原則 3000 tokens 以下 |

優先順位:

1. current user message
2. system / persona
3. safety / attribution guard
4. confirmed / pinned UserMemory constraints
5. L0 / L1 short context
6. high score relevant L2 / L3
7. Knowledge / News / Source snippets
8. optional glossary / examples

## 4.5 Prompt section names

Prompt へ注入する場合、section name を固定する。

```text
[System Persona]
[Current Turn]
[RecallPack: Conversation]
[RecallPack: UserMemory]
[RecallPack: Knowledge]
[RecallPack: News]
[RecallPack: Operation]
[Guard: Attribution]
[Glossary Recent Context]
[User Message]
```

Recall Trace では、この section name を `prompt_section` として保存する。

## 4.6 Sensitive memory

Sensitive memory は原則として prompt に注入しない。

注入できる例外:

- user が現在 turn で明示的にその話題を出した
- safety / legal / medical 等の高リスク文脈で、本人が継続利用を明示している
- explicit consent がある
- scope が `mio_only` などに限定されている

それでも、trace には sensitive raw content を残さず、hash / memory_id / redacted summary を残す。

---

# 5. Promotion / Decay / Forgetting

## 5.1 Promotion policy

### UserMemory promotion

UserMemory は、会話から推測しただけでは confirmed にしない。

Promotion 条件:

| 条件 | 昇格先 |
|---|---|
| user が「覚えて」と明示 | candidate → confirmed |
| user が継続指示を出した | candidate → confirmed / pinned candidate |
| 複数 event で繰り返し観測 | observed → candidate → review |
| user が確認した | candidate → confirmed |
| 重要な出力制約 | confirmed → pinned candidate |
| sensitive である | manual review / explicit consent 必須 |

### Knowledge promotion

Knowledge は Source Registry と validator を通す。

Promotion 条件:

- source_id が登録済み
- source trust score が閾値以上
- content_hash 重複が処理済み
- published_at / fetched_at が妥当
- raw_text と summary_draft が分離されている
- validator pass
- domain schema pass

### Persona promotion

Persona Observation は、直接 persona 正本へ入れない。

Promotion 条件:

- observation_log または meta_profile_update がある
- 重複観測または user 明示指示がある
- persona edit intent と一致する
- review pass
- PersonaEditor 経由で Markdown 正本を更新する

## 5.2 Scoring

Memory score は、次の概念要素で計算する。

```text
score =
  0.30 * relevance
+ 0.20 * confidence
+ 0.15 * recency
+ 0.15 * frequency
+ 0.10 * user_explicitness
+ 0.05 * source_trust
+ 0.05 * role_affinity
- risk_penalty
- staleness_penalty
```

実装では、type ごとに重みを変更してよい。

| type | 重視するもの |
|---|---|
| constraint | user_explicitness / confidence |
| preference | frequency / confirmation |
| project | recency / relevance |
| episode | relevance / recency |
| Knowledge | source_trust / freshness / domain match |
| Persona | repeated observation / review |

## 5.3 Decay

Decay は削除ではなく、想起されにくくする処理である。

Decay 対象:

- L1 raw thread snippets
- old candidate memory
- stale daily digest
- low score Knowledge
- unreferenced episode
- outdated Source Registry entry

Decay しないもの:

- pinned UserMemory
- Persona 正本
- explicit user constraint
- active project core facts
- OperationMemory の現行手順

## 5.4 Forgetting

ユーザーの明示的な「忘れて」「これは違う」「消して」「今後使わないで」は、通常の decay より優先する。

処理:

```text
forget request
  → target memory search
  → confirmation if ambiguous
  → status = deleted / inactive / superseded
  → remove from vector index or mark tombstone
  → add forget_trace
  → exclude from prompt injection
```

曖昧でない場合は、確認質問を挟まずに削除または inactive 化してよい。

## 5.5 Supersession

古い記憶と新しい記憶が衝突する場合、旧記憶を物理削除せず `superseded_by` で連結する。

例:

```json
{
  "memory_id": "usrmem_old",
  "statement": "ユーザーは短い箇条書きを好む",
  "status": "superseded",
  "superseded_by": "usrmem_new"
}
```

Prompt には最新 active memory のみを注入する。

## 5.6 Scheduled jobs

| Job | 頻度 | 内容 |
|---|---|---|
| L1 compact | daily | raw turn を digest 化し、不要 raw を削除 |
| Candidate review | daily / weekly | UserMemory candidate を review queue へ |
| Source validation | daily | staging source を validator へ |
| Monthly summary | weekly / monthly | daily digest を monthly highlight へ統合 |
| Decay update | weekly | score / last_used_at / staleness を更新 |
| Vector cleanup | monthly | deleted / superseded memory を index から除外 |
| Runtime log GC | hourly / daily | ring buffer compact / retention |

---

# 6. Recall Trace

## 6.1 目的

Recall Trace は、Mio がなぜその返答をしたかを追跡するための監査ログである。

記録すること:

- どの turn で
- どの query から
- どの layer を検索し
- どの memory / source を候補にし
- なぜ採用または棄却し
- prompt のどの section に
- 何 token 入れたか

## 6.2 Trace schema

### `recall_trace`

```sql
CREATE TABLE recall_trace (
  trace_id TEXT PRIMARY KEY,
  turn_id TEXT NOT NULL,
  chat_id TEXT NOT NULL,
  persona TEXT NOT NULL,
  route TEXT,
  user_message_hash TEXT,
  query_text_redacted TEXT,
  created_at TIMESTAMP NOT NULL,
  model_id TEXT,
  prompt_version TEXT,
  recall_policy_version TEXT,
  total_candidates INTEGER,
  injected_count INTEGER,
  total_injected_tokens INTEGER,
  status TEXT
);
```

### `recall_trace_item`

```sql
CREATE TABLE recall_trace_item (
  item_id TEXT PRIMARY KEY,
  trace_id TEXT NOT NULL,
  layer TEXT NOT NULL,
  memory_id TEXT,
  source_id TEXT,
  source_url TEXT,
  source_type TEXT,
  status TEXT NOT NULL,
  score DOUBLE,
  relevance DOUBLE,
  recency DOUBLE,
  confidence DOUBLE,
  source_trust DOUBLE,
  reason TEXT,
  injected BOOLEAN,
  prompt_section TEXT,
  token_count INTEGER,
  sensitivity TEXT,
  is_raw_or_summary TEXT,
  retrieved_at TIMESTAMP,
  published_at TIMESTAMP,
  event_id TEXT,
  FOREIGN KEY(trace_id) REFERENCES recall_trace(trace_id)
);
```

### `prompt_injection_event`

```sql
CREATE TABLE prompt_injection_event (
  injection_id TEXT PRIMARY KEY,
  trace_id TEXT NOT NULL,
  prompt_section TEXT NOT NULL,
  order_index INTEGER NOT NULL,
  item_ids TEXT,
  token_count INTEGER,
  redaction_level TEXT,
  created_at TIMESTAMP NOT NULL
);
```

## 6.3 Candidate status

| status | 意味 |
|---|---|
| `retrieved` | 検索で取得された |
| `filtered_scope` | scope 不一致で除外 |
| `filtered_sensitivity` | sensitivity により除外 |
| `filtered_status` | lifecycle status により除外 |
| `deduped` | 重複として除外 |
| `budget_dropped` | token budget により除外 |
| `injected` | prompt に注入された |
| `guard_only` | guard 判定にのみ利用 |

## 6.4 Redaction

Trace には、原文をそのまま保存しすぎない。

| sensitivity | trace content |
|---|---|
| normal | short summary 可 |
| private | redacted summary + memory_id |
| sensitive | memory_id + hash のみ |
| secret | trace item は存在のみ。内容なし |

## 6.5 Trace UI

Memory Inspector / Debug UI では、次を表示できるようにする。

- この返答で使った記憶
- 使われなかった候補
- 除外理由
- layer 別件数
- prompt section 別 token 数
- stale / superseded 警告
- sensitive redaction 表示

## 6.6 Acceptance criteria

Recall Trace は、次の問いに答えられなければならない。

1. Mio はこの turn でどの UserMemory を使ったか。
2. candidate memory が prompt に入っていないことを確認できるか。
3. Knowledge と UserMemory が混ざっていないことを確認できるか。
4. Web search result が正式 Knowledge ではなく staging / source として扱われたか。
5. どの記憶が古く、どれが pinned か分かるか。
6. Runtime logs が prompt に混入していないことを確認できるか。

---

# 7. 現行実装との差分

## 7.1 現行でできていること

| 項目 | 現行状態 |
|---|---|
| Mio の通常会話 context 構築 | system prompt → RecallPack → UserMemory prompt → 補助 context → user message の流れがある |
| RecallPack | ConversationEngine が BeginTurn で生成し、role filter 後に prompt へ入れる |
| UserMemory status | observed / candidate / confirmed / pinned がある |
| UserMemory 注入制御 | confirmed / pinned、active、normal sensitivity のみ注入する方針がある |
| Candidate 分離 | candidate は保存されても prompt に入れない方針がある |
| Persona 正本 | Markdown file base で管理される |
| KB / Source Registry | staging / validation / promote の考え方がある |
| Persona Observation | observation と interface session を分ける方針がある |
| Runtime log GC | persona_interface_session / ai_context_usage など bounded compact の考え方がある |
| Agent 境界 | Mio / Shiro / Coder / Heavy / Wild の責務境界がある |

## 7.2 現行で不足していること

| 不足 | 問題 |
|---|---|
| 統一 memory lifecycle | store はあるが、L0〜L4 の昇格・減衰・削除が一貫していない |
| Promotion pipeline | short / daily / candidate から long-term へ進む条件が明文化不足 |
| Decay / forgetting | 忘れる、弱める、統合する、要約する定期処理が弱い |
| Memory scoring | 重要度、頻度、最近性、信頼度、source trust の統合 score が必要 |
| Recall Trace | どの層から何を読んだかを追跡する trace が不足 |
| Prompt Injection Policy | candidate / sensitive / runtime log の注入禁止を機械的に保証する必要がある |
| Freshness-sensitive recall | 明示検索だけでなく、local-first の freshness check が必要 |
| Role-specific RecallPack | 多人数化に向け、Agent 別に recall pack を分ける必要がある |

## 7.3 仕様として追加するもの

1. L0〜L4 lifecycle table
2. memory status state machine
3. Prompt Injection Policy
4. UserMemory / Knowledge / Persona / Runtime の境界
5. Promotion / Decay / Forgetting rules
6. Recall Trace schema
7. Runtime log non-memory policy
8. Role-specific RecallPack の拡張点

## 7.4 実装フェーズ

### Phase 1: Trace first

最初に Recall Trace を実装する。

理由: Trace がない状態で lifecycle を調整しても、Mio が何を読んだか検証できないため。

実装:

- `recall_trace`
- `recall_trace_item`
- prompt section token count
- candidate exclusion reason

### Phase 2: Injection guard

Prompt Injection Policy を機械的に適用する。

実装:

- status filter
- sensitivity filter
- role filter
- token budget
- no candidate injection test
- no runtime logs injection test

### Phase 3: Lifecycle jobs

L1 / L2 / L3 / L4 の scheduled job を実装する。

実装:

- daily compact
- candidate review queue
- monthly summary
- decay update
- superseded handling

### Phase 4: UserMemory commands

ユーザー操作を実装する。

対象:

- 覚えて
- 忘れて
- ピン留め
- これは違う
- 要約して保存
- この記憶を見せて

### Phase 5: Knowledge / Source Registry hardening

外部情報を正式 Knowledge へ昇格する pipeline を強化する。

実装:

- staging schema
- validator
- source trust
- raw / summary separation
- citation ledger

### Phase 6: Role-specific recall

多人数化に向け、Agent 別の recall pack を実装する。

初期対象:

- Mio: Chat recall
- Shiro: Ops / execution recall
- AO / Aka / Kin / Gin: Code / design recall
- Kuro: Heavy review recall
- Midori: Creative recall

## 7.5 非目標

この仕様では、次を対象外とする。

- 全人格が常時会話参加する runtime の詳細設計
- UI 表示の詳細
- VectorDB 製品選定の最終決定
- 外部検索 API ベンダー選定

---

# 8. 実装責務と配置

## 8.1 実装単位

Memory lifecycle / Recall Context は、次の実装単位に分ける。Mio の `Chat()` 内に判定を散らさず、Domain / Application / Infrastructure の境界に置く。

| 実装単位 | 主責務 | 既存 / 追加配置 |
|---|---|---|
| RecallPack domain | prompt に入る構造体、role filter、token budget、trace item 生成 | `internal/domain/conversation/recall_pack.go`, `internal/domain/conversation/recall_trace.go` |
| Prompt injection policy | status / sensitivity / scope / role / budget による注入可否判定 | `internal/domain/conversation` に policy 型を追加 |
| UserMemory domain | user memory の state / type / promotion 制約 | `internal/domain/memory/user_memory.go` |
| UserMemory store | UserMemory CRUD、forget、supersede、list filtering | `internal/infrastructure/persistence/conversation/l1_sqlite_user_memory.go` |
| Recall trace store | turn ごとの recall_trace / item / injection event 永続化 | `internal/infrastructure/persistence/conversation/l1_sqlite_recall_trace.go` を追加 |
| Conversation engine | BeginTurn / EndTurn、RecallPack 構築、trace 開始 / finalize | `internal/infrastructure/persistence/conversation/engine_impl.go` |
| Mio agent | context 構築順、UserMemory prompt、候補 capture、guard 適用 | `internal/domain/agent/mio.go`, `chat_commands.go`, `user_memory_candidate.go` |
| Source Registry | external raw / staging / validation / promotion | `internal/infrastructure/persistence/conversation/l1_sqlite_source_registry.go`, `l1_sqlite_staging*.go` |
| OperationMemory | 運用知見の file store。Chat へ常時注入しない | `internal/infrastructure/persistence/memory/file_store.go` |
| Runtime log GC | prompt 非対象ログの bounded retention | `internal/infrastructure/persistence/*/jsonl_store.go`, `jsonlutil` |

## 8.2 実装境界

Mio の通常会話で prompt を組み立てる実装境界は次とする。

```text
MioAgent.Chat
  → handleChatCommand
  → conversationEngine.BeginTurn
  → RecallPack.FilterForRole("chat")
  → RecallPack.ApplyRecallBudget
  → RecallPack.ToPromptMessages
  → userMemoryPrompt
  → optional web/search context
  → attribution guard
  → glossary recent context
  → current user message
  → LLM generate
  → attribution violation check
  → conversationEngine.EndTurn
  → captureUserMemoryCandidate
  → recall trace finalize
```

この順序を変更する場合は、次の条件をすべて満たす。

- current user message は最後の user message として保持する。
- candidate / sensitive / runtime log を直接 prompt に入れない。
- prompt に入れた memory / source は recall trace item に残す。
- prompt section name は本仕様の固定名に正規化する。
- BeginTurn が失敗しても通常会話は graceful degradation する。ただし trace には failure status を残す。

## 8.3 Domain 型

### Recall item

RecallPack へ入る候補は、prompt text だけでなく、最低限次の metadata を保持する。

```go
type RecallCandidate struct {
    Layer         string
    Kind          string
    MemoryID      string
    SourceID      string
    SourceType    string
    Summary       string
    Score         float64
    Relevance     float64
    Recency       float64
    Confidence    float64
    SourceTrust   float64
    State         string
    Sensitivity   string
    Scope         string
    Roles         []string
    PublishedAt   time.Time
    RetrievedAt   time.Time
}
```

既存の `RecallTraceItem` は軽量 trace 型として残してよいが、prompt 注入判定に使う候補型は、`state` / `sensitivity` / `scope` / `roles` / `score` を持つ構造へ拡張する。

### Injection decision

Prompt Injection Policy は、各候補に対して次の結果を返す。

```go
type InjectionDecision struct {
    Status        string // injected / filtered_scope / filtered_sensitivity / filtered_status / deduped / budget_dropped / guard_only
    PromptSection string
    Reason        string
    Score         float64
    TokenCount    int
}
```

`Status` は 6.3 の candidate status と同じ値を使う。別名を増やす場合は trace schema と UI 表示を同時に更新する。

## 8.4 Prompt section 正規化

`RecallPack.ToPromptMessages()` は最終的に本仕様の section name へ正規化する。

既存の日本語見出しは UI / 互換表示として残してよいが、trace 上の `prompt_section` は必ず次へ正規化する。

| 既存または概念上の内容 | trace prompt_section |
|---|---|
| system persona / persona file | `[System Persona]` |
| rolling summary / current turn metadata | `[Current Turn]` |
| short context / thread summary | `[RecallPack: Conversation]` |
| confirmed / pinned UserMemory | `[RecallPack: UserMemory]` |
| KB snippets / validated knowledge | `[RecallPack: Knowledge]` |
| search cache / news digest | `[RecallPack: News]` |
| OperationMemory | `[RecallPack: Operation]` |
| attribution guard | `[Guard: Attribution]` |
| glossary recent context | `[Glossary Recent Context]` |
| final user input | `[User Message]` |

---

# 9. 永続化実装

## 9.1 L1 SQLite schema

Recall Trace は L1 SQLite に保存する。初期実装では DuckDB / VectorDB へ直書きしない。長期分析が必要になった段階で DuckDB export の対象にする。

追加ファイル:

- `internal/infrastructure/persistence/conversation/l1_sqlite_recall_trace.go`
- `internal/infrastructure/persistence/conversation/l1_sqlite_recall_trace_test.go`

既存の `l1_sqlite_schema.go` に 6.2 の `recall_trace`、`recall_trace_item`、`prompt_injection_event` を追加する。

実装要件:

- `trace_id` は turn 単位で一意にする。
- `turn_id` が未整備の場合は、初期実装では `session_id + timestamp + short hash` で代替してよい。
- `user_message_hash` は raw text ではなく hash を保存する。
- `query_text_redacted` は private / sensitive を redaction した短縮文字列にする。
- `item_ids` は初期実装では JSON 配列文字列でよい。後で junction table に移行できるようにする。
- SQLite schema は既存 DB を壊さない `CREATE TABLE IF NOT EXISTS` / additive migration とする。

## 9.2 Store interface

Conversation engine から trace store を直接具象型に依存させない。

```go
type RecallTraceStore interface {
    StartRecallTrace(ctx context.Context, trace RecallTraceRecord) error
    AddRecallTraceItems(ctx context.Context, traceID string, items []RecallTraceItemRecord) error
    AddPromptInjectionEvents(ctx context.Context, traceID string, events []PromptInjectionEventRecord) error
    FinishRecallTrace(ctx context.Context, traceID string, status string, injectedCount int, totalTokens int) error
}
```

`L1SQLiteStore` はこの interface を実装する。テストでは stub store を使い、BeginTurn / ToPromptMessages の失敗が会話全体を落とさないことを確認する。

## 9.3 UserMemory store

UserMemory は既存の `CreateUserMemory` / `ListUserMemories` / `UpdateUserMemoryState` / `ForgetUserMemory` / `SupersedeUserMemory` を正本 API とする。

追加要件:

- `confirmed` / `pinned` への昇格は `CanPromoteUserMemory` を必ず通す。
- `ListUserMemories` は prompt 用と管理 UI 用を分ける。prompt 用は `active=true`、`state in confirmed,pinned`、`sensitivity=normal`、`scope in all_personas,mio` を満たすものだけ返す helper を追加する。
- `ForgetUserMemory` は物理削除ではなく `active=false` または tombstone を基本にする。
- `SupersedeUserMemory` は旧 memory を prompt 対象から外し、trace では `filtered_status` として残せるようにする。

## 9.4 Runtime logs

Runtime / Status Logs は L0〜L4 memory store に入れない。

実装要件:

- JSONL operational logs は append bounded / compact 対象にする。
- prompt injection policy は `source_type=runtime_log` を `Never Directly` として拒否する。
- 障害調査で得られた知見だけを、人間または Worker の明示操作で OperationMemory summary にする。

---

# 10. 実装フェーズ詳細

## 10.1 Phase 1: Trace first

目的: Mio が何を読んだかを先に可視化する。

実装順:

1. `RecallTraceStore` interface を追加する。
2. L1 SQLite に trace schema を追加する。
3. `RecallPack.ToTraceItems()` の出力を `recall_trace_item` に保存する。
4. prompt section ごとの token count を `prompt_injection_event` に保存する。
5. `/viewer/memory` または既存 debug endpoint から最新 trace を取得できる read API を追加する。

完了条件:

- Mio の 1 turn 後、trace が 1 件保存される。
- RecallPack item と prompt section の対応が確認できる。
- BeginTurn 失敗時も `status=failed` の trace が残る。

## 10.2 Phase 2: Injection guard

目的: candidate / sensitive / runtime log の prompt 混入を機械的に防ぐ。

実装順:

1. `InjectionPolicy` を domain に追加する。
2. UserMemory prompt 生成を policy 経由にする。
3. RecallPack の role filter / token budget / dedupe 結果を trace に残す。
4. `Never Directly` の source type を拒否するテストを追加する。

完了条件:

- candidate UserMemory は保存されても prompt に入らない。
- sensitive UserMemory は user が明示的に当該 turn で扱う場合以外 prompt に入らない。
- runtime logs は trace 上 `filtered_status` または `filtered_sensitivity` として除外される。

## 10.3 Phase 3: Lifecycle jobs

目的: L1 / L2 / L3 / L4 の昇格・要約・減衰・GC を定期処理にする。

実装順:

1. daily compact job を追加する。
2. candidate review queue を追加する。
3. decay score update を追加する。
4. superseded / deleted memory の vector cleanup を追加する。
5. runtime log GC を startup と scheduled job の両方で実行する。
6. daily digest を monthly highlight へ統合する。
7. thread summary を monthly highlight seed として queue する。

完了条件:

- raw turn は要約化後に bounded retention へ移る。
- stale candidate は review queue または archive へ移る。
- pinned memory は decay されない。
- superseded memory は prompt に入らない。
- inactive / superseded memory は vector cleanup executor に渡され、完了状態が `vector_cleanup_status=done` として追跡できる。
- daily digest から作られた monthly highlight は idempotent に保存され、thread summary は統合候補として重複 queue されない。
- runtime / status / audit JSONL は active log から外す前に gzip JSONL archive へ可逆圧縮し、decode / timestamp 不正行は quarantine archive へ退避する。

### Runtime Log 可逆圧縮

Runtime / Status Logs は L0〜L4 memory store に入れないが、監査上あとで復元できる形を優先する。

方針:

- EventLog GC は retention 期限切れの valid JSONL を `archive/*.expired.*.jsonl.gz` へ退避してから active log を compact する。
- EventLog GC は runtime 起動時に一度実行し、その後 interval ごとに定期実行する。
- decode error / timestamp error の行は破棄せず、`archive/*.quarantine.*.jsonl.gz` へ退避する。
- `jsonlutil.CompactLatestRecords` は bounded JSONL から落とす古い行を `archive/*.compacted.*.jsonl.gz` へ退避する。
- active log は Viewer / runtime が高速に読む対象、gzip archive は監査・復元対象とする。

### 加速テスト

長期運用を待たずに lifecycle を検証するため、runtime の memory lifecycle job は opt-in の加速時計を持つ。

| 環境変数 | 意味 |
|---|---|
| `RENCROW_MEMORY_LIFECYCLE_ACCEL_MONTH_SEC=3600` | 実時間 1時間を lifecycle 上の 30日として扱う |
| `RENCROW_MEMORY_LIFECYCLE_ACCEL_MONTH_SEC=60` | 実時間 1分を lifecycle 上の 30日として扱う |
| `RENCROW_MEMORY_LIFECYCLE_ACCEL_MONTH_SEC=1` | 実時間 1秒を lifecycle 上の 30日として扱う |
| `RENCROW_MEMORY_LIFECYCLE_ACCEL_MONTH_MS=100` | 実時間 100ms を lifecycle 上の 30日として扱う。検証用の最短 smoke test |
| `RENCROW_MEMORY_LIFECYCLE_INTERVAL_SEC=5` | lifecycle job の tick 間隔を明示する |
| `RENCROW_MEMORY_LIFECYCLE_INTERVAL_MS=100` | lifecycle job の tick 間隔を ms 単位で明示する |

加速時計は `RENCROW_MEMORY_LIFECYCLE_ACCEL_MONTH_SEC` または `RENCROW_MEMORY_LIFECYCLE_ACCEL_MONTH_MS` が設定された場合だけ有効にする。通常運用では設定しない。ms 指定は検証用であり、live DB では原則使わない。短周期 tick の下限は 100ms とする。

検証例:

```bash
RENCROW_MEMORY_LIFECYCLE_ACCEL_MONTH_SEC=3600 \
RENCROW_MEMORY_LIFECYCLE_INTERVAL_SEC=30 \
systemctl --user restart rencrow.service
```

検証用 DB を作って最短 smoke test を行う例:

```bash
RENCROW_MEMORY_LIFECYCLE_ACCEL_VERIFY_DB=tmp/memory_lifecycle_accel/l1_accel_test.db \
RENCROW_MEMORY_LIFECYCLE_ACCEL_VERIFY_MONTHS=12 \
go test -v ./internal/infrastructure/persistence/conversation -run TestL1SQLiteStore_AcceleratedVerificationDB -count=1
```

より短い smoke test では `RENCROW_MEMORY_LIFECYCLE_ACCEL_MONTH_MS=100` と `RENCROW_MEMORY_LIFECYCLE_INTERVAL_MS=100` を使える。ただし live DB に対して使う場合は、実際に decay / monthly highlight / vector cleanup が進むため、検証用 DB かバックアップ済み DB で行う。

### 品質評価

lifecycle がエラーなく走ることと、記憶品質が適切であることは別に評価する。

品質評価では、gold fixture を次の分類で持つ。

| 分類 | 期待 |
|---|---|
| `must_keep` | 継続指示、pinned project、置換後の現行記憶が 12か月後も prompt injectable である |
| `must_compact_or_forget` | 一時的な raw conversation や単発障害調査メモが raw のまま残らない、または decayed になる |
| `must_not_inject` | sensitive candidate、forgotten、superseded、decayed memory が prompt injectable set に入らない |
| `must_cleanup_vector` | forgotten / superseded memory の vector cleanup が `done` のまま維持され、後続 lifecycle で `queued` に戻らない |

常時テスト:

```bash
go test -v ./internal/infrastructure/persistence/conversation -run TestL1SQLiteStore_MemoryRetentionQualityEvalOneYear -count=1
```

## 10.4 Phase 4: UserMemory commands

目的: ユーザーが記憶を制御できるようにする。

既存 command:

- 覚えて
- 忘れて
- 優先して / ピン留め相当

追加 command:

- 「この記憶を見せて」
- 「これは違う」
- 「この話を要約して保存」
- 「今後使わないで」
- 「記憶を置き換えて: old => new」
- 「old を new に置き換えて」

完了条件:

- command 実行結果が UserMemory state に反映される。
- forget / supersede が次 turn の prompt へ反映される。
- 操作結果が trace / event log で追える。
- 曖昧一致が複数ある場合は即変更せず、候補 id を提示して明示指定を求める。
- supersede は新 memory candidate を作成し、旧 memory を `superseded_by` で prompt 対象外にする。

## 10.5 Phase 5: Knowledge / Source Registry hardening

目的: 外部情報を正式 Knowledge へ昇格する pipeline を prompt recall と接続する。

実装順:

1. Source Registry staging / validation / promotion の状態を維持する。
2. Recall Context では freshness-sensitive 発話を検出する。
3. 外部検索前に L1 SearchCache と promoted L1 Knowledge FTS を local-first で参照する。
4. L1 Knowledge hit は `[L1KB]`、VectorDB hit は `[VectorKB]` として section trace できる形で RecallPack に入れる。

完了条件:

- Source Registry staging は直接 prompt に入らない。
- promoted L1 Knowledge / SearchCache のみが freshness-sensitive recall に使われる。
- 複合 query は full text miss 時も有効語 fallback で local Knowledge を検索できる。

## 10.6 Phase 6: Role-specific recall

目的: Mio / Shiro / AO / Aka / Kin / Gin / Kuro / Midori ごとに RecallPack の利用範囲を分ける。

初期 policy:

| Role | Policy |
|---|---|
| Mio / Chat | conversation memory + explicit local-first freshness KB/SearchCache |
| Shiro / Worker | conversation memory + Knowledge + SearchCache |
| AO / Aka / Kin / Gin / Coder | conversation memory + Knowledge + SearchCache |
| Kuro / Heavy | conversation memory + Knowledge |
| Midori / Creative | conversation memory + Knowledge |

完了条件:

- role alias は `FilterForRole` で正規化される。
- role 不一致や policy 外の item は rejected trace item として残る。
- Mio は汎用 KB/SearchCache を常時読むのではなく、`[L1KB]` / `[VectorKB]` または `Roles: chat` の明示 local-first item だけを読む。

---

# 11. テスト仕様

## 11.1 Unit tests

必須テスト:

| 対象 | テスト |
|---|---|
| `internal/domain/conversation` | role filter が rejected trace item を保持する |
| `internal/domain/conversation` | token budget 超過 item が `budget_dropped` になる |
| `internal/domain/conversation` | prompt section が固定名に正規化される |
| `internal/domain/memory` | confirmed / pinned promotion に evidence が必須 |
| `internal/domain/memory` | sensitive memory は auto promote できない |
| `internal/domain/agent` | UserMemory prompt は confirmed / pinned / active / normal のみ注入 |
| `internal/domain/agent` | candidate capture は prompt 注入と分離される |
| `internal/domain/agent` | UserMemory 曖昧一致は候補提示で止まり、即変更しない |
| `internal/domain/agent` | supersede command は新 candidate 作成と旧 memory の supersede を行う |
| `internal/infrastructure/persistence/conversation` | recall trace schema が作成され CRUD できる |
| `internal/infrastructure/persistence/conversation` | forget / supersede 後の memory は prompt helper から除外される |
| `internal/infrastructure/persistence/conversation` | lifecycle job は monthly highlight / thread summary seed / decay policy / vector cleanup executor を扱う |

## 11.2 Integration tests

必須テスト:

1. `BeginTurn` → RecallPack → prompt conversion → trace save の一連の流れ。
2. Knowledge / SearchCache / UserMemory が別 section に入ること。
3. Source Registry staging は直接 Knowledge として prompt に入らないこと。
4. Runtime log を候補に混ぜても prompt に入らないこと。
5. `ForgetUserMemory` 後、次 turn で該当 memory が prompt に入らないこと。
6. `SupersedeUserMemory` 後、旧 memory は除外され新 memory だけが使われること。

## 11.3 Verification commands

実装変更時は最低限次を実行する。

```bash
go test ./internal/domain/conversation ./internal/domain/memory ./internal/domain/agent ./internal/infrastructure/persistence/conversation ./internal/adapter/viewer
```

Viewer に trace / memory inspector を追加した場合は、該当 JS test も実行する。

```bash
node internal/adapter/viewer/viewer_memory_panel.test.mjs
```

広範囲の store / runtime を触った場合は最終確認として次を実行する。

```bash
go test ./...
```

---

# 12. 実装受け入れ条件

この仕様を満たしたと判断するには、次をすべて満たす。

1. Mio の各 turn について、Recall Trace から「使った記憶」「使わなかった候補」「除外理由」「prompt section」「token count」を確認できる。
2. candidate / staging / raw observation / runtime log は、直接 prompt に注入されない。
3. UserMemory、Conversation Memory、Knowledge、Persona、OperationMemory、Runtime Logs の保存先と prompt 注入経路が分離されている。
4. `confirmed` / `pinned` UserMemory だけが通常会話の UserMemory prompt に入る。
5. Source Registry 由来の外部情報は staging / validation / promotion を通るまで正式 Knowledge として扱われない。
6. Forget / supersede / inactive の結果が次 turn の prompt に反映される。
7. L1 / L2 / L3 / L4 の promotion / decay / GC job が存在し、少なくとも unit test で動作が確認できる。
8. Runtime / Status Logs は bounded retention / GC 対象であり、Mio の通常会話 prompt に混入しない。
9. Viewer または debug API で、最新 turn の trace を人間が確認できる。
10. 実装後の `go test ./...` が通る。
- LoRA / fine-tuning
- 旧キャラ体系 Lumina / Claris / Nox への対応

## 7.6 完了条件

この仕様の初期実装が完了したと判断する条件は次の通り。

1. Mio の各 turn で Recall Trace が保存される。
2. confirmed / pinned UserMemory だけが UserMemory prompt に入る。
3. candidate / sensitive / inactive / runtime logs が prompt に直接入らない。
4. Knowledge は Source Registry / staging / validation / promote を通ってから RecallPack 候補になる。
5. L0 / L1 / L2 / L3 / L4 それぞれに retention、promotion、decay、prompt 注入可否が設定されている。
6. 「忘れて」「これは違う」で対象 memory が inactive / deleted / superseded になり、以後注入されない。
7. Memory Inspector で、Mio がどの記憶を使ったか確認できる。

---

# Appendix A. 最小設定例

```yaml
memory_lifecycle:
  l0:
    retention: "turn"
    inject: "always"
    raw_persist: false
  l1:
    retention: "1-3 days"
    store: "sqlite"
    inject: "recallpack"
    promote_to: "l2"
  l2:
    retention: "1 month"
    store: "duckdb"
    inject: "conditional"
    promote_to: "l3"
  l3:
    retention: "long_term"
    store: "duckdb+vector"
    inject: "conditional"
    decay: true
  l4:
    retention: "until_explicit_delete"
    store: "markdown/duckdb/parquet"
    inject: "pinned_or_conditional"
    decay: false

prompt_injection:
  allow:
    - system_persona
    - current_user_message
    - recallpack_filtered
    - usermemory_confirmed
    - usermemory_pinned
  deny:
    - usermemory_candidate
    - sensitive_without_consent
    - runtime_logs
    - unvalidated_external_source
    - raw_staging

recall_trace:
  enabled: true
  redact_sensitive: true
  record_exclusion_reason: true
  record_prompt_section: true
```

# Appendix B. 最小テスト項目

| Test | 期待結果 |
|---|---|
| candidate UserMemory を作成して通常会話 | prompt に入らない |
| confirmed UserMemory を作成して通常会話 | UserMemory prompt に入る |
| sensitive UserMemory を作成して通常会話 | 明示同意なしでは入らない |
| runtime log が大量にある状態で通常会話 | prompt に入らない |
| source staging に未検証記事がある | Knowledge として注入されない |
| Web 検索結果を取得 | Search Cache / staging へ入り、正式 Knowledge にはならない |
| 「忘れて」と指示 | 対象 memory が inactive / deleted になり、以後注入されない |
| RecallPack 生成 | trace item が layer / score / reason 付きで保存される |
